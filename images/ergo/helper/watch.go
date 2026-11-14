package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// watcher is `boson-ergo watch-settings`: it keeps Ergo in line with the
// auth service at runtime.
//
//   - channel_creation (every settingsEvery): re-render + rehash on change
//   - roles (every tick, ETag): re-render the role oper classes + rehash
//   - IRC grants (every tick, ETag, needs ERGO_API_TOKEN and auth's
//     /internal/v1/irc/grants): push each changed user's oper class and
//     vhost to Ergo (POST /v1/account_grants), so role changes apply to
//     connected users without a reconnect
//
// Failed polls change nothing; auth may be restarting.
type watcher struct {
	get           func(string) string
	tmplPath, dir string
	settings      *settingsClient
	roles         *roles.Client // nil = roles off
	ergo          *ergoapi.Client
	rehash        func() error
	logf          func(string, ...any)
	settingsEvery time.Duration

	policy           channelPolicy
	lastSettings     time.Time
	rolesCache       rolesCache
	grants           grantsSnapshot
	grantsSupported  bool
	grantsLogged     bool
	serviceAccountOK bool
}

func newWatcher(get func(string) string, tmplPath, dir string, settingsEvery time.Duration, rehash func() error,
	logf func(string, ...any)) *watcher {
	w := &watcher{
		get: get, tmplPath: tmplPath, dir: dir,
		settings: newSettingsClient(get), rehash: rehash, logf: logf, settingsEvery: settingsEvery,
		grantsSupported: true,
	}
	if rolesEnabled(get) {
		w.roles = newRolesClient(get)
		w.rolesCache, _ = loadRolesCache(dir)
		w.grants = loadGrants(dir)
	}
	if tok := get("ERGO_API_TOKEN"); tok != "" {
		w.ergo = ergoapi.New(strEnv(get, "ERGO_API_URL", defaultErgoAPIURL), tok)
	}
	if p, ok := loadCachedPolicy(dir); ok {
		w.policy = p
	}
	return w
}

func (w *watcher) run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w.tick(ctx)
	}
}

func (w *watcher) tick(ctx context.Context) {
	if w.lastSettings.IsZero() || time.Since(w.lastSettings) >= w.settingsEvery {
		w.lastSettings = time.Now()
		if p, err := w.settings.fetch(ctx); err != nil {
			if w.policy != "" {
				w.logf("channel_creation: %v (keeping %q)", err, w.policy)
			}
		} else {
			if p != w.policy && w.policy != "" {
				w.logf("channel_creation is now %q", p)
			}
			w.policy = p
			saveCachedPolicy(w.dir, p)
		}
	}
	if w.policy == "" {
		return // nothing known yet; render already used the env fallback
	}

	classesChanged := false
	if w.roles != nil {
		next, changed, err := fetchRoles(ctx, w.roles, w.rolesCache)
		if err != nil {
			w.logf("roles: %v (keeping %d roles)", err, len(w.rolesCache.Roles))
		} else {
			if changed {
				w.logf("roles changed (%d roles, version %q)", len(next.Roles), next.Version)
				classesChanged = true
			}
			w.rolesCache = next
			if changed {
				if err := saveRolesCache(w.dir, next); err != nil {
					w.logf("roles: %v", err)
				}
			}
		}
	}

	classes, err := classesFor(w.get, w.rolesCache)
	if err != nil {
		w.logf("roles: %v", err)
		return
	}
	changed, err := render(w.get, w.tmplPath, w.dir, w.policy, classes)
	if err != nil {
		w.logf("re-render: %v", err)
		return
	}
	if changed {
		w.logf("ircd.yaml updated, rehashing ergo")
		if err := w.rehash(); err != nil {
			w.logf("rehash: %v", err)
			return
		}
	}

	if w.ergo != nil {
		w.ensureServiceAccount(ctx)
	}
	if w.roles != nil && w.ergo != nil && !w.rolesCache.Unsupported {
		w.syncGrants(ctx, classes, classesChanged || changed)
	}
}

func (w *watcher) syncGrants(ctx context.Context, classes []roles.OperClass, force bool) {
	etag := w.grants.ETag
	if force || !w.grantsSupported {
		etag = ""
	}
	users, newETag, notModified, err := w.roles.Grants(ctx, etag)
	switch {
	case errors.Is(err, roles.ErrUnsupported):
		if !w.grantsLogged {
			w.logf("grants: auth has no /internal/v1/irc/grants; role changes reach connected users at their next login (or right away through RoleServ)")
			w.grantsLogged = true
		}
		w.grantsSupported = false
		return
	case err != nil:
		w.logf("grants: %v", err)
		return
	case notModified:
		return
	}
	w.grantsSupported, w.grantsLogged = true, false
	next, pushed, failed := syncGrants(ctx, users, classes, w.grants.Grants, w.ergo, w.logf)
	if pushed > 0 {
		w.logf("grants: pushed %d account(s) to ergo", pushed)
	}
	w.grants.Grants = next
	w.grants.ETag = newETag
	if failed > 0 {
		w.grants.ETag = "" // refetch next time to retry
	}
	if err := saveGrants(w.dir, w.grants); err != nil {
		w.logf("grants: %v", err)
	}
}

// ensureServiceAccount creates RoleServ's local Ergo account (with a
// throwaway password; RoleServ resets it when it starts), so its nick is
// reserved even while RoleServ isn't running.
func (w *watcher) ensureServiceAccount(ctx context.Context) {
	if w.serviceAccountOK {
		return
	}
	name := strEnv(w.get, "ROLESERV_NICK", "RoleServ")
	err := w.ergo.Register(ctx, name, randomSecret())
	if err == nil || ergoapi.IsCode(err, "ACCOUNT_EXISTS") {
		w.serviceAccountOK = true
		return
	}
	w.logf("reserving %s: %v", name, err)
}

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
