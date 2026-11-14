package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// Network roles (auth service GET /internal/v1/roles) become Ergo oper
// classes: one "role-<slug>" class per role with irc.oper, whose
// capabilities are the role's irc.* permissions plus
// IRC_ROLE_BASE_CAPABILITIES. The last list fetched is kept in the datastore
// volume, both so a restart while auth is down keeps the classes, and for
// the auth-script, which maps a login's oper_class onto a class name with it.
const (
	rolesCacheFile  = "boson-roles.json"
	grantsCacheFile = "boson-grants.json"

	defaultAuthInternalURL = "http://auth:8081"
	defaultAuthPublicURL   = "http://auth:8080"
	defaultErgoAPIURL      = "http://127.0.0.1:8089"
	defaultBaseCaps        = "nofakelag"

	// RoleServ's account (a local Ergo account, never an auth one).
	serviceClass = "boson.service"
)

type rolesCache struct {
	ETag    string       `json:"etag"`
	Version string       `json:"version"`
	Roles   []roles.Role `json:"roles"`
	// Unsupported: auth answered 404 (no roles API), so nothing is rendered.
	Unsupported bool `json:"unsupported,omitempty"`
}

func newRolesClient(get func(string) string) *roles.Client {
	return roles.NewClient(strEnv(get, "AUTH_INTERNAL_URL", defaultAuthInternalURL),
		strEnv(get, "AUTH_PUBLIC_URL", defaultAuthPublicURL), get("INTERNAL_API_TOKEN"), get("AUTH_ACTING_TOKEN"))
}

// rolesEnabled: IRC_ROLES=false turns the whole roles integration off.
func rolesEnabled(get func(string) string) bool {
	return boolEnv(get, "IRC_ROLES", true)
}

// baseCapabilities parses IRC_ROLE_BASE_CAPABILITIES (comma-separated Ergo
// capabilities every role class gets; "none" = none).
func baseCapabilities(get func(string) string) ([]string, error) {
	v := strings.TrimSpace(strEnv(get, "IRC_ROLE_BASE_CAPABILITIES", defaultBaseCaps))
	if strings.EqualFold(v, "none") {
		return nil, nil
	}
	var out []string
	for _, c := range strings.Split(v, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if !roles.ErgoCapabilities[c] {
			return nil, fmt.Errorf("IRC_ROLE_BASE_CAPABILITIES: unknown Ergo capability %q", c)
		}
		out = append(out, c)
	}
	return out, nil
}

func loadRolesCache(dir string) (rolesCache, bool) {
	b, err := os.ReadFile(filepath.Join(dir, rolesCacheFile))
	if err != nil {
		return rolesCache{}, false
	}
	var c rolesCache
	if json.Unmarshal(b, &c) != nil {
		return rolesCache{}, false
	}
	return c, true
}

func saveRolesCache(dir string, c rolesCache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, rolesCacheFile), append(b, '\n'), 0o600)
}

// fetchRoles asks auth for the roles, using (and updating) cache. changed
// reports whether the role list differs from the cached one.
func fetchRoles(ctx context.Context, c *roles.Client, cache rolesCache) (next rolesCache, changed bool, err error) {
	etag := cache.ETag
	if cache.Unsupported {
		etag = ""
	}
	list, newETag, notModified, err := c.Roles(ctx, etag)
	switch {
	case errors.Is(err, roles.ErrUnsupported):
		next = rolesCache{Unsupported: true}
		return next, !cache.Unsupported || len(cache.Roles) > 0, nil
	case err != nil:
		return cache, false, err
	case notModified:
		return cache, false, nil
	}
	next = rolesCache{ETag: newETag, Version: string(list.Version), Roles: list.Roles}
	oldJSON, _ := json.Marshal(cache.Roles)
	newJSON, _ := json.Marshal(next.Roles)
	return next, cache.Unsupported || string(oldJSON) != string(newJSON), nil
}

// resolveRoles for `render`: auth's list (and remember it), else the cached
// one, else none.
func resolveRoles(ctx context.Context, get func(string) string, dir string, c *roles.Client, logf func(string, ...any)) rolesCache {
	cache, _ := loadRolesCache(dir)
	if !rolesEnabled(get) {
		return rolesCache{Unsupported: true}
	}
	next, _, err := fetchRoles(ctx, c, rolesCache{Roles: cache.Roles})
	if err != nil {
		logf("roles: %v; using the %d last known roles", err, len(cache.Roles))
		return cache
	}
	if err := saveRolesCache(dir, next); err != nil {
		logf("roles: saving %s: %v", rolesCacheFile, err)
	}
	return next
}

// classesFor renders the oper classes for the cached roles.
func classesFor(get func(string) string, cache rolesCache) ([]roles.OperClass, error) {
	base, err := baseCapabilities(get)
	if err != nil {
		return nil, err
	}
	return roles.Classes(cache.Roles, base), nil
}

// grantsSnapshot is the last set of grants pushed to Ergo (by account), so
// the watcher can revoke what disappeared even across restarts.
type grantsSnapshot struct {
	ETag   string                 `json:"etag"`
	Grants map[string]roles.Grant `json:"grants"`
}

func loadGrants(dir string) grantsSnapshot {
	var s grantsSnapshot
	if b, err := os.ReadFile(filepath.Join(dir, grantsCacheFile)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Grants == nil {
		s.Grants = map[string]roles.Grant{}
	}
	return s
}

func saveGrants(dir string, s grantsSnapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, grantsCacheFile), append(b, '\n'), 0o600)
}

// grantPusher is what syncGrants needs from Ergo's API.
type grantPusher interface {
	ApplyGrants(ctx context.Context, account string, g roles.Grant) (int, error)
}

// syncGrants pushes the difference between prev and the users auth lists
// now: changed and new grants as they are, vanished ones as "none". It
// returns the new snapshot (accounts whose push failed keep their old
// value), how many accounts it pushed and how many pushes failed (the
// caller then refetches instead of trusting its ETag, to retry them).
func syncGrants(ctx context.Context, users []roles.UserGrant, classes []roles.OperClass, prev map[string]roles.Grant,
	ergo grantPusher, logf func(string, ...any)) (next map[string]roles.Grant, pushed, failed int) {
	want := map[string]roles.Grant{}
	for _, u := range users {
		if name := strings.ToLower(strings.TrimSpace(u.Username)); name != "" {
			want[name] = roles.GrantFor(classes, u.UserIRC)
		}
	}
	for name := range prev {
		if _, ok := want[name]; !ok {
			want[name] = roles.None()
		}
	}
	next = map[string]roles.Grant{}
	for name, g := range want {
		old, had := prev[name]
		if had && old.Equal(g) {
			next[name] = g
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := ergo.ApplyGrants(pctx, name, g)
		cancel()
		switch {
		case err == nil, isAccountMissing(err):
			// an account that never logged in gets its grant at first login
			pushed++
			if !g.Equal(roles.None()) {
				next[name] = g
			}
		default:
			logf("grants: %s: %v", name, err)
			failed++
			if had {
				next[name] = old
			}
		}
	}
	return next, pushed, failed
}

func isAccountMissing(err error) bool {
	return ergoapi.IsCode(err, "ACCOUNT_DOES_NOT_EXIST")
}
