package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Registry tells whether an image tag has been pushed. A GitHub release can
// exist minutes before its image does (or its image build can fail), and
// an install must be able to pull what it pins.
type Registry struct {
	HTTP *http.Client
	// Override sends every request to this base URL instead of the image's
	// registry host (tests): "http://127.0.0.1:5000".
	Override string
}

func NewRegistry(timeout time.Duration) *Registry {
	return &Registry{HTTP: &http.Client{Timeout: timeout}}
}

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json"

// TagExists asks HEAD /v2/<repo>/manifests/<tag>, with the anonymous
// bearer-token flow Harbor and ghcr.io use for public repositories.
func (r *Registry) TagExists(ctx context.Context, image, tag string) (bool, error) {
	host, repo, ok := strings.Cut(image, "/")
	if !ok || !strings.ContainsAny(host, ".:") {
		host, repo = "registry-1.docker.io", image
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	base := "https://" + host
	if r.Override != "" {
		base = strings.TrimRight(r.Override, "/")
	}
	u := base + "/v2/" + repo + "/manifests/" + url.PathEscape(tag)
	status, challenge, err := r.head(ctx, u, "")
	if err == nil && status == http.StatusUnauthorized && challenge != "" {
		var tok string
		if tok, err = r.token(ctx, challenge, repo); err == nil {
			status, _, err = r.head(ctx, u, tok)
		}
	}
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("registry %s answered %d for %s:%s", host, status, image, tag)
}

func (r *Registry) head(ctx context.Context, u, token string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("registry unreachable: %w", err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Www-Authenticate"), nil
}

// token follows a `Bearer realm="…",service="…"` challenge anonymously.
func (r *Registry) token(ctx context.Context, challenge, repo string) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("registry wants %s auth", scheme)
	}
	p := map[string]string{}
	for _, kv := range strings.Split(params, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			p[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	if p["realm"] == "" {
		return "", fmt.Errorf("registry challenge has no realm")
	}
	q := url.Values{"scope": {"repository:" + repo + ":pull"}}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token: %d", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	return t.Token, nil
}
