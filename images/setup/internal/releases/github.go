package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GitHub lists a repository's release tags from the GitHub REST API, the
// same source the admin panel's Updates page reads. Without a token GitHub
// allows 60 requests an hour per IP address: the wizard asks once per
// repository per run (Resolver caches), about ten requests in all.
type GitHub struct {
	// BaseURL: https://api.github.com (a test server in tests).
	BaseURL string
	// Token is optional (BOSON_SETUP_GITHUB_TOKEN; CI uses one).
	Token string
	HTTP  *http.Client
}

// NewGitHub returns a client for api.github.com.
func NewGitHub(token string, timeout time.Duration) *GitHub {
	return &GitHub{BaseURL: "https://api.github.com", Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: timeout}}
}

// Tags returns the tags of the repository's published releases (drafts
// left out), as GitHub lists them (newest first, up to 100).
func (g *GitHub) Tags(ctx context.Context, repo string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.BaseURL, "/")+"/repos/"+repo+"/releases?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "boson-stack-setup")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("no releases at github.com/%s", repo)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			msg := "GitHub's API rate limit for this server's address is used up"
			if r, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				msg += " until " + time.Unix(r, 0).UTC().Format("15:04 UTC")
			}
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("GitHub refused the request (%d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub answered %d for %s", resp.StatusCode, repo)
	}
	var raw []struct {
		TagName string `json:"tag_name"`
		Draft   bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("GitHub: reading %s releases: %w", repo, err)
	}
	tags := make([]string, 0, len(raw))
	for _, r := range raw {
		if !r.Draft {
			tags = append(tags, r.TagName)
		}
	}
	return tags, nil
}
