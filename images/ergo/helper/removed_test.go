package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// fakeFeed serves pages keyed by cursor.
type fakeFeed struct {
	pages map[string]struct {
		accounts []roles.RemovedAccount
		next     string
	}
	err   error
	calls []string
}

func (f *fakeFeed) RemovedAccounts(_ context.Context, since string, limit int) ([]roles.RemovedAccount, string, error) {
	f.calls = append(f.calls, since)
	if f.err != nil {
		return nil, "", f.err
	}
	p, ok := f.pages[since]
	if !ok {
		return []roles.RemovedAccount{}, since, nil
	}
	return p.accounts, p.next, nil
}

type fakeEraser struct {
	erased       []string
	disconnected []string
	fail         map[string]error
}

func (e *fakeEraser) DisconnectAccount(_ context.Context, account, reason string) (int, error) {
	if err := e.fail[account]; err != nil {
		return 0, err
	}
	if reason == "" {
		return 0, errors.New("no reason")
	}
	e.disconnected = append(e.disconnected, account)
	return 1, nil
}

func (e *fakeEraser) EraseAccount(_ context.Context, account string) error {
	if err := e.fail[account]; err != nil {
		return err
	}
	e.erased = append(e.erased, account)
	return nil
}

func TestSyncRemovedAccounts(t *testing.T) {
	feed := &fakeFeed{pages: map[string]struct {
		accounts []roles.RemovedAccount
		next     string
	}{
		"": {[]roles.RemovedAccount{
			{Username: "alice", Reason: "deleted"},
			{Username: "bob", Reason: "disabled"},
			{Username: "ghost", Reason: "deleted"}, // never used IRC
			{Username: "roleserv", Reason: "deleted"},
		}, "c1"},
	}}
	ergo := &fakeEraser{fail: map[string]error{"ghost": &ergoapi.Error{Code: "ACCOUNT_DOES_NOT_EXIST"}}}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }

	st, err := syncRemovedAccounts(context.Background(), feed, ergo, removedState{}, []string{"RoleServ"}, logf)
	if err != nil || st.Cursor != "c1" {
		t.Fatalf("state %+v, err %v", st, err)
	}
	if strings.Join(ergo.erased, ",") != "alice" {
		t.Fatalf("erased %v; disabled users and service accounts must be kept", ergo.erased)
	}
	if strings.Join(ergo.disconnected, ",") != "bob" {
		t.Fatalf("disconnected %v; want the disabled user only", ergo.disconnected)
	}

	// nothing new: the cursor stays
	st, err = syncRemovedAccounts(context.Background(), feed, ergo, st, nil, logf)
	if err != nil || st.Cursor != "c1" || len(ergo.erased) != 1 {
		t.Fatalf("second sync: %+v %v %v", st, err, ergo.erased)
	}

	// Ergo down: stop before the failed page, retry it next time
	feed.pages["c1"] = struct {
		accounts []roles.RemovedAccount
		next     string
	}{[]roles.RemovedAccount{{Username: "carol", Reason: "deleted"}}, "c2"}
	ergo.fail["carol"] = errors.New("ergo api unreachable")
	st, err = syncRemovedAccounts(context.Background(), feed, ergo, st, nil, logf)
	if err == nil || st.Cursor != "c1" {
		t.Fatalf("failed erase: %+v %v", st, err)
	}
	delete(ergo.fail, "carol")
	st, err = syncRemovedAccounts(context.Background(), feed, ergo, st, nil, logf)
	if err != nil || st.Cursor != "c2" || ergo.erased[len(ergo.erased)-1] != "carol" {
		t.Fatalf("retry: %+v %v %v", st, err, ergo.erased)
	}

	// state survives restarts
	dir := t.TempDir()
	if err := saveRemovedState(dir, st); err != nil {
		t.Fatal(err)
	}
	if got := loadRemovedState(dir); got != st {
		t.Fatalf("loaded %+v", got)
	}
}

func TestRemovedAccountsClient(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/irc/accounts/removed" || r.Header.Get("X-Internal-Token") != "itok" {
			http.NotFound(w, r)
			return
		}
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accounts":[{"username":"alice","reason":"deleted","at":"2026-11-14T12:00:00Z"}],"next":"42.7"}`))
	}))
	defer srv.Close()
	c := roles.NewClient(srv.URL, srv.URL, "itok", "")
	accts, next, err := c.RemovedAccounts(context.Background(), "41.3", 500)
	if err != nil || next != "42.7" || len(accts) != 1 || accts[0].Username != "alice" || accts[0].Reason != "deleted" {
		t.Fatalf("%v %q %v", accts, next, err)
	}
	if query != "limit=500&since=41.3" {
		t.Fatalf("query %q", query)
	}
	// auth < 1.7
	c = roles.NewClient(srv.URL+"/old", srv.URL, "itok", "")
	if _, _, err := c.RemovedAccounts(context.Background(), "", 500); !errors.Is(err, roles.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}
