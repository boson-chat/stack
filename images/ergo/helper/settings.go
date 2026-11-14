package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Text channel creation policy: the auth service's `channel_creation`
// network setting (admin panel), mapped onto Ergo's config:
//
//	registered: channels.operator-only-creation false, registration.operator-only false
//	admins:     both true: only opers with the chanreg capability (the
//	            server-admin class) can create or ChanServ-register channels
//
// boson-ergo render fetches it once at start; boson-ergo watch-settings
// polls it, re-renders ircd.yaml when the result changes and sends Ergo
// SIGHUP (REHASH), which applies it without dropping anyone.
type channelPolicy string

const (
	policyRegistered channelPolicy = "registered"
	policyAdmins     channelPolicy = "admins"

	defaultSettingsURL = "http://auth:8081/internal/v1/settings"
	// Last policy fetched from auth, kept in the datastore volume so a
	// restart while auth is down doesn't fall back to a looser default.
	settingsCacheFile = "boson-settings.json"
)

func parsePolicy(s string) (channelPolicy, error) {
	switch p := channelPolicy(strings.ToLower(strings.TrimSpace(s))); p {
	case policyRegistered, policyAdmins:
		return p, nil
	}
	return "", fmt.Errorf("unknown channel_creation %q (want registered or admins)", s)
}

type settingsClient struct {
	url, token string
	http       *http.Client
}

func newSettingsClient(get func(string) string) *settingsClient {
	return &settingsClient{
		url:   strEnv(get, "AUTH_SETTINGS_URL", defaultSettingsURL),
		token: get("INTERNAL_API_TOKEN"),
		http:  &http.Client{Timeout: 5 * time.Second},
	}
}

// fetch asks the auth service for channel_creation.
func (c *settingsClient) fetch(ctx context.Context) (channelPolicy, error) {
	if c.token == "" {
		return "", errors.New("INTERNAL_API_TOKEN is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Internal-Token", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth settings unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth settings: HTTP %d", resp.StatusCode)
	}
	var body struct {
		ChannelCreation string `json:"channel_creation"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("auth settings: %w", err)
	}
	if body.ChannelCreation == "" {
		return "", errors.New("auth settings: no channel_creation field")
	}
	return parsePolicy(body.ChannelCreation)
}

// resolvePolicy: the auth service's value (and remember it), else the last
// remembered one, else IRC_CHANNEL_CREATION (default registered).
func resolvePolicy(ctx context.Context, get func(string) string, dir string, c *settingsClient, logf func(string, ...any)) channelPolicy {
	p, err := c.fetch(ctx)
	if err == nil {
		saveCachedPolicy(dir, p)
		return p
	}
	if cached, ok := loadCachedPolicy(dir); ok {
		logf("channel_creation: %v; using the last known value %q", err, cached)
		return cached
	}
	fallback, perr := parsePolicy(strEnv(get, "IRC_CHANNEL_CREATION", string(policyRegistered)))
	if perr != nil {
		logf("IRC_CHANNEL_CREATION: %v; using admins", perr)
		fallback = policyAdmins
	}
	logf("channel_creation: %v; using IRC_CHANNEL_CREATION=%q", err, fallback)
	return fallback
}

func loadCachedPolicy(dir string) (channelPolicy, bool) {
	b, err := os.ReadFile(filepath.Join(dir, settingsCacheFile))
	if err != nil {
		return "", false
	}
	var v struct {
		ChannelCreation string `json:"channel_creation"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", false
	}
	p, err := parsePolicy(v.ChannelCreation)
	return p, err == nil
}

func saveCachedPolicy(dir string, p channelPolicy) {
	b, _ := json.Marshal(map[string]string{"channel_creation": string(p)})
	_ = writeFileAtomic(filepath.Join(dir, settingsCacheFile), append(b, '\n'), 0o600)
}

// watchSettings polls the policy every interval and, when the rendered
// config changes, rewrites it and calls rehash. It returns when ctx ends.
func watchSettings(ctx context.Context, get func(string) string, tmplPath, dir string, interval time.Duration,
	c *settingsClient, rehash func() error, logf func(string, ...any)) {
	last := channelPolicy("")
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p, err := c.fetch(ctx)
		if err != nil {
			// Keep whatever is rendered; auth may be restarting.
			if last != "" {
				logf("channel_creation: %v (keeping %q)", err, last)
			}
			continue
		}
		saveCachedPolicy(dir, p)
		changed, err := render(get, tmplPath, dir, p)
		if err != nil {
			logf("re-render: %v", err)
			continue
		}
		if changed {
			logf("channel_creation is now %q: ircd.yaml updated, rehashing ergo", p)
			if err := rehash(); err != nil {
				logf("rehash: %v", err)
			}
		}
		last = p
	}
}

// sighup sends SIGHUP to pid (Ergo is PID 1 in the container).
func sighup(pid int) func() error {
	return func() error { return syscall.Kill(pid, syscall.SIGHUP) }
}
