package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, c *authClient, in string) scriptOutput {
	t.Helper()
	var out strings.Builder
	if code := authScriptMain(strings.NewReader(in+"\n"), &out, c); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasSuffix(out.String(), "\n") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("want exactly one line, got %q", out.String())
	}
	var o scriptOutput
	if err := json.Unmarshal([]byte(out.String()), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "tok" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("X-Forwarded-For") != "203.0.113.9" {
			t.Errorf("xff = %q", r.Header.Get("X-Forwarded-For"))
		}
		var req map[string]string
		json.NewDecoder(r.Body).Decode(&req)
		if req["username"] == "alice" && req["password"] == "right" {
			w.Write([]byte(`{"ok":true,"user_id":"usr_1","username":"Alice"}`))
			return
		}
		w.Write([]byte(`{"ok":false,"reason":"invalid_credentials"}`))
	}))
	defer srv.Close()
	get := func(k string) string {
		return map[string]string{"AUTH_VERIFY_URL": srv.URL, "INTERNAL_API_TOKEN": "tok"}[k]
	}
	c := newAuthClient(get)

	if o := run(t, c, `{"accountName":"alice","passphrase":"right","ip":"203.0.113.9"}`); !o.Success || o.AccountName != "Alice" || o.Error != "" {
		t.Fatalf("good password: %+v", o)
	}
	if o := run(t, c, `{"accountName":"alice","passphrase":"wrong","ip":"203.0.113.9"}`); o.Success || o.Error != "" {
		t.Fatalf("bad password: %+v", o)
	}
	if o := run(t, c, `{"certfp":"abc","ip":"203.0.113.9"}`); o.Success || o.Error != "" {
		t.Fatalf("certfp: %+v", o)
	}
	c.token = "nope"
	if o := run(t, c, `{"accountName":"alice","passphrase":"right","ip":"203.0.113.9"}`); o.Success || o.Error == "" {
		t.Fatalf("bad internal token should be an error: %+v", o)
	}
}

func TestPasswordTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()
	c := newAuthClient(func(k string) string {
		return map[string]string{"AUTH_VERIFY_URL": srv.URL, "INTERNAL_API_TOKEN": "tok", "AUTH_SCRIPT_TIMEOUT": "100ms"}[k]
	})
	start := time.Now()
	o := run(t, c, `{"accountName":"a","passphrase":"b"}`)
	if o.Success || o.Error == "" || time.Since(start) > 400*time.Millisecond {
		t.Fatalf("timeout: %+v after %v", o, time.Since(start))
	}
}

func TestBearer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "crv": "Ed25519", "kid": "key_1", "alg": "EdDSA",
			"x": base64.RawURLEncoding.EncodeToString(pub),
		}}})
	}))
	defer srv.Close()
	c := newAuthClient(func(k string) string {
		return map[string]string{"AUTH_JWKS_URL": srv.URL, "AUTH_ISSUER": "https://auth.example.com"}[k]
	})
	now := time.Now().Unix()
	mk := func(hdr, claims map[string]any, key ed25519.PrivateKey) string {
		h, _ := json.Marshal(hdr)
		p, _ := json.Marshal(claims)
		s := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
		return s + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(s)))
	}
	hdr := map[string]any{"alg": "EdDSA", "kid": "key_1", "typ": "at+jwt"}
	good := map[string]any{"iss": "https://auth.example.com", "sub": "usr_1", "aud": "boson", "name": "alice", "exp": now + 60, "nbf": now}
	in := func(tok string) string {
		b, _ := json.Marshal(map[string]any{"oauth2": map[string]string{"token": tok}})
		return string(b)
	}
	if o := run(t, c, in(mk(hdr, good, priv))); !o.Success || o.AccountName != "alice" {
		t.Fatalf("good token: %+v", o)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if o := run(t, c, in(mk(hdr, good, other))); o.Success {
		t.Fatal("wrong key accepted")
	}
	bad := map[string]map[string]any{}
	for k, v := range map[string]any{"aud": "quicvoice", "iss": "https://evil", "exp": now - 120} {
		m := map[string]any{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		bad[k] = m
	}
	for name, cl := range bad {
		if o := run(t, c, in(mk(hdr, cl, priv))); o.Success {
			t.Fatalf("bad %s accepted", name)
		}
	}
	if o := run(t, c, in(mk(map[string]any{"alg": "EdDSA", "kid": "key_1", "typ": "JWT"}, good, priv))); o.Success {
		t.Fatal("voice-style typ accepted")
	}
}

func TestRender(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"IRC_HOST": "irc.example.com", "DOMAIN": "example.com", "NETWORK_NAME": "Boson Net",
		"OPER_PASSWORD_HASH": "$2a$04$0123456789abcdef0123456789abcdef0123456789abcdef01234",
		"IRC_PLAINTEXT":      "true",
	}
	if _, err := render(func(k string) string { return env[k] }, "", dir, netSettings{ChannelCreation: policyRegistered}, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := buildData(func(k string) string { return env[k] })
	if d.CloakNetname != "boson-net" || d.AuthHost != "auth.example.com" || len(d.WebSocketOrigins) != 2 {
		t.Fatalf("%+v", d)
	}
	env["OPER_PASSWORD_HASH"] = "plain"
	if _, err := render(func(k string) string { return env[k] }, "", dir, netSettings{ChannelCreation: policyRegistered}, nil); err == nil {
		t.Fatal("non-bcrypt oper hash accepted")
	}
}

func TestRenderIPLimitsExempt(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"IRC_HOST": "irc.example.com", "DOMAIN": "example.com",
		"OPER_PASSWORD_HASH":   "$2a$04$0123456789abcdef0123456789abcdef0123456789abcdef01234",
		"IRC_IP_LIMITS_EXEMPT": "10.0.0.0/8, 192.0.2.7",
	}
	get := func(k string) string { return env[k] }
	if _, err := render(get, "", dir, netSettings{ChannelCreation: policyRegistered}, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ircd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "- \"10.0.0.0/8\"") || !strings.Contains(string(b), "- \"192.0.2.7\"") {
		t.Fatalf("exempt nets not rendered:\n%s", b)
	}
	env["IRC_IP_LIMITS_EXEMPT"] = "nope"
	if _, err := render(get, "", dir, netSettings{ChannelCreation: policyRegistered}, nil); err == nil {
		t.Fatal("bad net accepted")
	}
}
