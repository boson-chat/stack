package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeUsers struct {
	fp    string
	users []User
}

func (f *fakeUsers) Fingerprint(context.Context) (string, error) { return f.fp, nil }
func (f *fakeUsers) List(context.Context) ([]User, error)        { return f.users, nil }

// fakeBridge is the managed API over HTTP, in memory.
type fakeBridge struct {
	mu    sync.Mutex
	accts map[string]Account
	calls []string
}

func (f *fakeBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/managed/users/")
	f.calls = append(f.calls, r.Method+" "+name)
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/managed/users":
		var out []Account
		for _, a := range f.accts {
			out = append(out, a)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": out})
	case r.Method == "PUT":
		var a Account
		_ = json.NewDecoder(r.Body).Decode(&a)
		for n, o := range f.accts {
			if o.Subject == a.Subject && n != name {
				w.WriteHeader(409)
				return
			}
		}
		a.Username = name
		f.accts[name] = a
	case r.Method == "DELETE":
		if _, ok := f.accts[name]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(f.accts, name)
	}
}

func TestSync(t *testing.T) {
	fb := &fakeBridge{accts: map[string]Account{"stale": {Username: "stale", Subject: "usr_gone"}}}
	srv := httptest.NewServer(fb)
	defer srv.Close()
	u := &fakeUsers{fp: "1", users: []User{
		{ID: "usr_a", Username: "Alice", Email: "a@x"},
		{ID: "usr_b", Username: "bob_the_builder_9", Disabled: true},
		{ID: "usr_c", Username: "9bad"},
	}}
	s := &Syncer{Users: u, Bridge: NewBridge(srv.URL, "tok"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	if err := s.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if a := fb.accts["alice"]; a.Subject != "usr_a" || a.Nick != "Alice" || a.Ident != "alice" || a.Realname != "Alice" {
		t.Fatalf("alice: %+v", a)
	}
	if b := fb.accts["bob_the_builder_9"]; !b.Disabled || b.Ident != "bobthebuil" {
		t.Fatalf("bob: %+v", b)
	}
	if _, ok := fb.accts["stale"]; ok {
		t.Fatal("a deleted Boson account kept its bridge account")
	}
	if _, ok := fb.accts["9bad"]; ok {
		t.Fatal("an invalid name reached the bridge")
	}
	// Nothing changed: no calls.
	fb.calls = nil
	if err := s.Once(ctx); err != nil || len(fb.calls) != 0 {
		t.Fatalf("unchanged: %v %v", err, fb.calls)
	}
	// A rename: the old account goes, the new one comes with the subject.
	u.fp = "2"
	u.users[0].Username = "alicia"
	if err := s.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := fb.accts["alice"]; ok || fb.accts["alicia"].Subject != "usr_a" {
		t.Fatalf("rename: %+v", fb.accts)
	}
	// Wrong token: an error, and the next run tries again.
	s.Bridge = NewBridge(srv.URL, "nope")
	u.fp = "3"
	if err := s.Once(ctx); err == nil {
		t.Fatal("no error with a wrong token")
	}
}
