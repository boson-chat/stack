package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	defaultVerifyURL = "http://auth:8081/internal/v1/verify-password"
	defaultJWKSURL   = "http://auth:8080/.well-known/jwks.json"
	accessAudience   = "boson"
	clockSkew        = 30 * time.Second
)

// Ergo's auth-script protocol: one JSON line in, one JSON line out.
// https://github.com/ergochat/ergo/blob/stable/docs/MANUAL.md#external-authentication-systems
type scriptInput struct {
	AccountName string `json:"accountName,omitempty"`
	Passphrase  string `json:"passphrase,omitempty"`
	Certfp      string `json:"certfp,omitempty"`
	IP          string `json:"ip,omitempty"`
	OAuth2      *struct {
		Username string `json:"username,omitempty"`
		Token    string `json:"token,omitempty"`
	} `json:"oauth2,omitempty"`
}

type scriptOutput struct {
	Success     bool   `json:"success"`
	AccountName string `json:"accountName,omitempty"`
	// Error is for infrastructure failures only: Ergo logs it as an error.
	// A wrong password is success=false with no error.
	Error string `json:"error,omitempty"`
}

type authClient struct {
	verifyURL, jwksURL, token, issuer string
	timeout                           time.Duration
	http                              *http.Client
	now                               func() time.Time
}

func newAuthClient(get func(string) string) *authClient {
	timeout := 5 * time.Second
	if d, err := time.ParseDuration(get("AUTH_SCRIPT_TIMEOUT")); err == nil && d > 0 {
		timeout = d
	}
	return &authClient{
		verifyURL: strEnv(get, "AUTH_VERIFY_URL", defaultVerifyURL),
		jwksURL:   strEnv(get, "AUTH_JWKS_URL", defaultJWKSURL),
		token:     get("INTERNAL_API_TOKEN"),
		issuer:    strEnv(get, "AUTH_ISSUER", ""),
		timeout:   timeout,
		http:      &http.Client{Timeout: timeout},
		now:       time.Now,
	}
}

func authScriptMain(in io.Reader, out io.Writer, c *authClient) int {
	res := c.handle(in)
	b, _ := json.Marshal(res)
	b = append(b, '\n')
	if _, err := out.Write(b); err != nil {
		return 1
	}
	return 0
}

func (c *authClient) handle(in io.Reader) scriptOutput {
	line, err := bufio.NewReaderSize(in, 64<<10).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return scriptOutput{Error: "reading input: " + err.Error()}
	}
	var req scriptInput
	if err := json.Unmarshal(line, &req); err != nil {
		return scriptOutput{Error: "bad input: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	switch {
	case req.OAuth2 != nil && req.OAuth2.Token != "":
		return c.bearer(ctx, req.OAuth2.Username, req.OAuth2.Token)
	case req.AccountName != "" && req.Passphrase != "":
		return c.password(ctx, req.AccountName, req.Passphrase, req.IP)
	default:
		// certfp / cookies: not handled here; Ergo falls back to its own DB.
		return scriptOutput{}
	}
}

func (c *authClient) password(ctx context.Context, user, pass, ip string) scriptOutput {
	if c.token == "" {
		return scriptOutput{Error: "INTERNAL_API_TOKEN is not set"}
	}
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.verifyURL, bytes.NewReader(body))
	if err != nil {
		return scriptOutput{Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.token)
	// Pass the IRC client's IP along. (auth 1.0.0 ignores X-Forwarded-For on
	// its internal listener, so today its per-IP limit counts all of Ergo's
	// logins together; see docs/deploy-boson-chat.md "Open questions".)
	if net.ParseIP(ip) != nil {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return scriptOutput{Error: "auth service unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusTooManyRequests {
		// Logged by Ergo, so operators can see logins being throttled.
		return scriptOutput{Error: "auth service rate limit hit (HTTP 429)"}
	}
	if resp.StatusCode != http.StatusOK {
		return scriptOutput{Error: fmt.Sprintf("auth service: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(data))}
	}
	var r struct {
		OK       bool   `json:"ok"`
		Username string `json:"username"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return scriptOutput{Error: "auth service: bad response: " + err.Error()}
	}
	if !r.OK || r.Username == "" {
		return scriptOutput{}
	}
	return scriptOutput{Success: true, AccountName: r.Username}
}

// bearer verifies a boson access token (EdDSA JWT, typ at+jwt, aud "boson")
// against the auth service's JWKS and returns its `name` claim as the account.
func (c *authClient) bearer(ctx context.Context, authzid, token string) scriptOutput {
	if c.issuer == "" {
		return scriptOutput{Error: "AUTH_ISSUER is not set"}
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return scriptOutput{}
	}
	var hdr struct {
		Alg, Kid, Typ string
	}
	if !decodeSegment(parts[0], &hdr) || hdr.Alg != "EdDSA" || hdr.Typ != "at+jwt" || hdr.Kid == "" {
		return scriptOutput{}
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return scriptOutput{}
	}
	key, err := c.fetchKey(ctx, hdr.Kid)
	if err != nil {
		return scriptOutput{Error: err.Error()}
	}
	if key == nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return scriptOutput{}
	}
	var cl struct {
		Iss  string          `json:"iss"`
		Sub  string          `json:"sub"`
		Aud  json.RawMessage `json:"aud"`
		Name string          `json:"name"`
		Exp  *float64        `json:"exp"`
		Nbf  *float64        `json:"nbf"`
	}
	if !decodeSegment(parts[1], &cl) {
		return scriptOutput{}
	}
	now := c.now()
	switch {
	case cl.Iss != c.issuer, cl.Sub == "", cl.Name == "", !hasAudience(cl.Aud, accessAudience),
		cl.Exp == nil || now.After(time.Unix(int64(*cl.Exp), 0).Add(clockSkew)),
		cl.Nbf != nil && now.Add(clockSkew).Before(time.Unix(int64(*cl.Nbf), 0)),
		authzid != "" && !strings.EqualFold(authzid, cl.Name):
		return scriptOutput{}
	}
	return scriptOutput{Success: true, AccountName: cl.Name}
}

func (c *authClient) fetchKey(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JWKS unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS: HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct{ Kty, Crv, X, Kid string } `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("JWKS: %w", err)
	}
	for _, k := range set.Keys {
		if k.Kid != kid || k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, nil
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, nil
}

func decodeSegment(s string, v any) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && json.Unmarshal(b, v) == nil
}

func hasAudience(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}
