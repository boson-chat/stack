package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSettings struct {
	mu     sync.Mutex
	value  string
	status int
}

func (f *fakeSettings) set(v string, status int) {
	f.mu.Lock()
	f.value, f.status = v, status
	f.mu.Unlock()
}

func (f *fakeSettings) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Internal-Token") != "itok" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		http.Error(w, "down", f.status)
		return
	}
	_, _ = w.Write([]byte(`{"registration":"open","require_email_verification":true,"channel_creation":"` + f.value + `","voice_channel_creation":"founders"}`))
}

func testEnv(extra map[string]string) func(string) string {
	env := map[string]string{
		"IRC_HOST": "irc.example.com", "DOMAIN": "example.com",
		"OPER_PASSWORD_HASH": "$2a$04$0123456789abcdef0123456789abcdef0123456789abcdef01234",
		"INTERNAL_API_TOKEN": "itok",
	}
	for k, v := range extra {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

func readConf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "ircd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRenderPolicyAndAPI(t *testing.T) {
	dir := t.TempDir()
	get := testEnv(nil)
	if _, err := render(get, "", dir, policyRegistered); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, dir)
	if !strings.Contains(conf, "operator-only-creation: false") || !strings.Contains(conf, "        operator-only: false") ||
		strings.Contains(conf, "\napi:") || !strings.Contains(conf, "require-sasl:\n        enabled: false") {
		t.Fatalf("registered conf:\n%s", conf)
	}
	changed, err := render(get, "", dir, policyRegistered)
	if err != nil || changed {
		t.Fatalf("re-render same policy: changed=%v err=%v", changed, err)
	}
	tok := strings.Repeat("a", 64)
	get = testEnv(map[string]string{"ERGO_API_TOKEN": tok, "IRC_REQUIRE_SASL": "true"})
	changed, err = render(get, "", dir, policyAdmins)
	if err != nil || !changed {
		t.Fatalf("admins: changed=%v err=%v", changed, err)
	}
	conf = readConf(t, dir)
	for _, want := range []string{
		"operator-only-creation: true", "        operator-only: true",
		"api:\n    enabled: true\n    listener: \":8089\"\n    bearer-tokens:\n        - \"" + tok + "\"",
		"require-sasl:\n        enabled: true",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("admins conf lacks %q:\n%s", want, conf)
		}
	}
	if _, err := render(testEnv(map[string]string{"ERGO_API_TOKEN": "short"}), "", dir, policyAdmins); err == nil {
		t.Fatal("short API token accepted")
	}
}

func TestResolvePolicy(t *testing.T) {
	fs := &fakeSettings{value: "admins"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	get := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL})
	logf := func(string, ...any) {}
	ctx := context.Background()

	if p := resolvePolicy(ctx, get, dir, newSettingsClient(get), logf); p != policyAdmins {
		t.Fatalf("from auth: %q", p)
	}
	// Auth down: the remembered value wins over the env fallback.
	fs.set("", http.StatusBadGateway)
	if p := resolvePolicy(ctx, get, dir, newSettingsClient(get), logf); p != policyAdmins {
		t.Fatalf("cached: %q", p)
	}
	// Nothing remembered: IRC_CHANNEL_CREATION, default registered.
	if p := resolvePolicy(ctx, get, t.TempDir(), newSettingsClient(get), logf); p != policyRegistered {
		t.Fatalf("default: %q", p)
	}
	get2 := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL, "IRC_CHANNEL_CREATION": "admins"})
	if p := resolvePolicy(ctx, get2, t.TempDir(), newSettingsClient(get2), logf); p != policyAdmins {
		t.Fatalf("env fallback: %q", p)
	}
	// Bad value from auth: treated as an error (cached value stays).
	fs.set("everyone", 0)
	if p := resolvePolicy(ctx, get, dir, newSettingsClient(get), logf); p != policyAdmins {
		t.Fatalf("bad value: %q", p)
	}
}

func TestWatchSettingsRehashesOnChange(t *testing.T) {
	fs := &fakeSettings{value: "registered"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	get := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL})
	if _, err := render(get, "", dir, policyRegistered); err != nil {
		t.Fatal(err)
	}
	var rehashes atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchSettings(ctx, get, "", dir, 10*time.Millisecond, newSettingsClient(get),
			func() error { rehashes.Add(1); return nil }, t.Logf)
		close(done)
	}()
	waitFor := func(cond func() bool, what string) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); !cond(); {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for " + what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if rehashes.Load() != 0 {
		t.Fatal("rehash without a change")
	}
	fs.set("admins", 0)
	waitFor(func() bool { return rehashes.Load() == 1 }, "rehash after admins")
	if !strings.Contains(readConf(t, dir), "operator-only-creation: true") {
		t.Fatal("not re-rendered")
	}
	fs.set("", http.StatusInternalServerError) // auth down: nothing changes
	time.Sleep(50 * time.Millisecond)
	fs.set("registered", 0)
	waitFor(func() bool { return rehashes.Load() == 2 }, "rehash back to registered")
	if !strings.Contains(readConf(t, dir), "operator-only-creation: false") {
		t.Fatal("not re-rendered back")
	}
	cancel()
	<-done
}
