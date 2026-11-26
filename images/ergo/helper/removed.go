package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// Users removed in auth (GET /internal/v1/irc/accounts/removed, auth 1.7+):
//
//	deleted:  the Ergo account is erased (POST /v1/ns/unregister with erase,
//	          patched Ergo): its sessions are disconnected, the channels it
//	          founded are unregistered (Ergo's own NS UNREGISTER logic), its
//	          always-on client, read markers, settings and vhost go, and the
//	          name is free for a new account (auth only reports a deletion if
//	          nobody has the username again)
//	disabled: its clients are disconnected (POST /v1/account_disconnect,
//	          patched Ergo; also a banned federated user, docs/federation.md)
//	          and new logins are refused (the auth-script asks auth); the
//	          account and its channels are kept so that re-enabling
//	          restores everything. Idle channels still expire
//	          (channel_expiry_days)
//
// The feed's cursor is kept in /ircd/boson-removed.json, so every removal is
// handled once, also across restarts and while Ergo or auth were down.
const (
	removedStateFile = "boson-removed.json"
	removedPageSize  = 500
	removedMaxPages  = 50 // per poll; the rest comes next poll
)

type removedState struct {
	Cursor string `json:"cursor"`
}

func loadRemovedState(dir string) (st removedState) {
	b, err := os.ReadFile(filepath.Join(dir, removedStateFile))
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveRemovedState(dir string, st removedState) error {
	b, _ := json.Marshal(st)
	return writeFileAtomic(filepath.Join(dir, removedStateFile), append(b, '\n'), 0o600)
}

// eraser is the part of the Ergo API syncRemoved needs.
type eraser interface {
	EraseAccount(ctx context.Context, account string) error
	DisconnectAccount(ctx context.Context, account, reason string) (int, error)
}

// disabledReason is the QUIT message of a disabled account's clients.
const disabledReason = "Your account was disabled on this server"

type removedFeed interface {
	RemovedAccounts(ctx context.Context, since string, limit int) ([]roles.RemovedAccount, string, error)
}

// syncRemovedAccounts applies the feed from st.Cursor on. It returns the new
// state; on an error it stops at the start of the failed page (handling a
// page twice is harmless: erasing an erased account is a no-op).
func syncRemovedAccounts(ctx context.Context, feed removedFeed, ergo eraser, st removedState, protected []string,
	logf func(string, ...any)) (removedState, error) {
	for range removedMaxPages {
		accounts, next, err := feed.RemovedAccounts(ctx, st.Cursor, removedPageSize)
		if err != nil {
			return st, err
		}
		for _, a := range accounts {
			switch {
			case a.Username == "":
			case a.Reason == "disabled" && isProtectedAccount(a.Username, protected):
			case a.Reason == "disabled":
				n, err := ergo.DisconnectAccount(ctx, a.Username, disabledReason)
				switch {
				case err == nil:
					logf("removed accounts: %s was disabled in auth: disconnected %d client(s); keeping their Ergo account and channels", a.Username, n)
				case ergoapi.IsCode(err, "ACCOUNT_DOES_NOT_EXIST"):
				default:
					return st, err
				}
			case a.Reason != "deleted":
				logf("removed accounts: %s: unknown reason %q, ignored", a.Username, a.Reason)
			case isProtectedAccount(a.Username, protected):
				logf("removed accounts: not erasing service account %s", a.Username)
			default:
				err := ergo.EraseAccount(ctx, a.Username)
				switch {
				case err == nil:
					logf("removed accounts: %s was deleted in auth: erased their Ergo account (their channels are unregistered)", a.Username)
				case ergoapi.IsCode(err, "ACCOUNT_DOES_NOT_EXIST"):
					// never used IRC, or already erased
				default:
					return st, err
				}
			}
		}
		// auth filters rows after paging, so a short page doesn't mean the
		// end: keep going until the cursor stops moving
		if next == "" || next == st.Cursor {
			return st, nil
		}
		st.Cursor = next
	}
	return st, nil
}

func isProtectedAccount(name string, protected []string) bool {
	for _, p := range protected {
		if strings.EqualFold(name, p) {
			return true
		}
	}
	return false
}

func (w *watcher) syncRemoved(ctx context.Context) {
	next, err := syncRemovedAccounts(ctx, w.removed, w.ergo, w.removedState, []string{strEnv(w.get, "ROLESERV_NICK", "RoleServ")}, w.logf)
	switch {
	case errors.Is(err, roles.ErrUnsupported):
		if !w.removedLogged {
			w.logf("removed accounts: auth has no /internal/v1/irc/accounts/removed (< 1.7); deleted users keep their Ergo accounts and channels")
			w.removedLogged = true
		}
		return
	case err != nil:
		w.logf("removed accounts: %v (will retry)", err)
	}
	w.removedLogged = false
	if next != w.removedState {
		w.removedState = next
		if err := saveRemovedState(w.dir, next); err != nil {
			w.logf("removed accounts: %v", err)
		}
	}
}
