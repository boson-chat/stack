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
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Text channel creation policy: the auth service's `channel_creation`
// network setting (admin panel), mapped onto Ergo's config:
//
//	registered: channels.operator-only-creation false, registration.operator-only false
//	admins:     both true: only opers with the chanreg capability (the
//	            emergency OPER_NAME oper, or a network role with irc.chanreg)
//	            can create or ChanServ-register channels
//
// boson-ergo render fetches it once at start; boson-ergo watch-settings
// polls it, re-renders ircd.yaml when the result changes and sends Ergo
// SIGHUP (REHASH), which applies it without dropping anyone.
type channelPolicy string

const (
	policyRegistered channelPolicy = "registered"
	policyAdmins     channelPolicy = "admins"

	defaultSettingsURL = "http://auth:8081/internal/v1/settings"
	// Last settings fetched from auth, kept in the datastore volume so a
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

// netSettings are the auth service's network settings that shape ircd.yaml.
// The channel lifecycle fields are nil when auth doesn't have them (< 1.7).
type netSettings struct {
	ChannelCreation channelPolicy `json:"channel_creation"`
	// channel_auto_register: a logged-in user's new channel is registered to them
	AutoRegister *bool `json:"channel_auto_register,omitempty"`
	// channel_expiry_days: registered channels unused this long are unregistered (0 = never)
	ExpiryDays *int `json:"channel_expiry_days,omitempty"`
}

const maxExpiryDays = 3650

func (s netSettings) String() string {
	out := "channel_creation=" + string(s.ChannelCreation)
	if s.AutoRegister != nil {
		out += fmt.Sprintf(" channel_auto_register=%t", *s.AutoRegister)
	}
	if s.ExpiryDays != nil {
		out += fmt.Sprintf(" channel_expiry_days=%d", *s.ExpiryDays)
	}
	return out
}

func (s netSettings) equal(o netSettings) bool { return s.String() == o.String() }

// parseSettings validates a settings document (auth's answer or the cache).
func parseSettings(b []byte) (netSettings, error) {
	var raw struct {
		ChannelCreation string `json:"channel_creation"`
		AutoRegister    *bool  `json:"channel_auto_register"`
		ExpiryDays      *int   `json:"channel_expiry_days"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return netSettings{}, err
	}
	if raw.ChannelCreation == "" {
		return netSettings{}, errors.New("no channel_creation field")
	}
	p, err := parsePolicy(raw.ChannelCreation)
	if err != nil {
		return netSettings{}, err
	}
	if raw.ExpiryDays != nil && (*raw.ExpiryDays < 0 || *raw.ExpiryDays > maxExpiryDays) {
		return netSettings{}, fmt.Errorf("channel_expiry_days %d out of range", *raw.ExpiryDays)
	}
	return netSettings{ChannelCreation: p, AutoRegister: raw.AutoRegister, ExpiryDays: raw.ExpiryDays}, nil
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

// fetch asks the auth service for its network settings.
func (c *settingsClient) fetch(ctx context.Context) (netSettings, error) {
	if c.token == "" {
		return netSettings{}, errors.New("INTERNAL_API_TOKEN is not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return netSettings{}, err
	}
	req.Header.Set("X-Internal-Token", c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return netSettings{}, fmt.Errorf("auth settings unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netSettings{}, fmt.Errorf("auth settings: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return netSettings{}, fmt.Errorf("auth settings: %w", err)
	}
	st, err := parseSettings(b)
	if err != nil {
		return netSettings{}, fmt.Errorf("auth settings: %w", err)
	}
	return st, nil
}

// resolveSettings: the auth service's values (and remember them), else the
// last remembered ones, else IRC_CHANNEL_CREATION (default registered).
func resolveSettings(ctx context.Context, get func(string) string, dir string, c *settingsClient, logf func(string, ...any)) netSettings {
	st, err := c.fetch(ctx)
	if err == nil {
		saveCachedSettings(dir, st)
		return st
	}
	if cached, ok := loadCachedSettings(dir); ok {
		logf("network settings: %v; using the last known values (%s)", err, cached)
		return cached
	}
	fallback, perr := parsePolicy(strEnv(get, "IRC_CHANNEL_CREATION", string(policyRegistered)))
	if perr != nil {
		logf("IRC_CHANNEL_CREATION: %v; using admins", perr)
		fallback = policyAdmins
	}
	logf("network settings: %v; using IRC_CHANNEL_CREATION=%q", err, fallback)
	return netSettings{ChannelCreation: fallback}
}

func loadCachedSettings(dir string) (netSettings, bool) {
	b, err := os.ReadFile(filepath.Join(dir, settingsCacheFile))
	if err != nil {
		return netSettings{}, false
	}
	st, err := parseSettings(b)
	return st, err == nil
}

func saveCachedSettings(dir string, st netSettings) {
	b, _ := json.Marshal(st)
	_ = writeFileAtomic(filepath.Join(dir, settingsCacheFile), append(b, '\n'), 0o600)
}

// Channel registration lifecycle in ircd.yaml (patched Ergo, docs/irc-channels.md):
// auth's value, else IRC_CHANNEL_AUTO_REGISTER / IRC_CHANNEL_EXPIRY_DAYS
// (auth < 1.7). IRC_CHANNEL_EXPIRE_AFTER (an Ergo duration such as 90d or
// 30s) overrides the expiry, mostly for testing.
var ergoDurationRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,5}(s|m|h|d))$`)

func channelLifecycle(get func(string) string, st netSettings) (autoRegister bool, expireAfter string, err error) {
	autoRegister = boolEnv(get, "IRC_CHANNEL_AUTO_REGISTER", true)
	if st.AutoRegister != nil {
		autoRegister = *st.AutoRegister
	}
	if v := strEnv(get, "IRC_CHANNEL_EXPIRE_AFTER", ""); v != "" {
		if !ergoDurationRe.MatchString(v) {
			return false, "", fmt.Errorf("IRC_CHANNEL_EXPIRE_AFTER must be 0 or a number with s, m, h or d (e.g. 90d), got %q", v)
		}
		return autoRegister, v, nil
	}
	days := 0
	if st.ExpiryDays != nil {
		days = *st.ExpiryDays
	} else if v := strEnv(get, "IRC_CHANNEL_EXPIRY_DAYS", "0"); v != "0" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxExpiryDays {
			return false, "", fmt.Errorf("IRC_CHANNEL_EXPIRY_DAYS must be 0 to %d, got %q", maxExpiryDays, v)
		}
		days = n
	}
	if days == 0 {
		return autoRegister, "0", nil
	}
	return autoRegister, fmt.Sprintf("%dd", days), nil
}

// sighup sends SIGHUP to pid (Ergo is PID 1 in the container).
func sighup(pid int) func() error {
	return func() error { return syscall.Kill(pid, syscall.SIGHUP) }
}
