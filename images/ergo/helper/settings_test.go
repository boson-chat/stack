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
	extra  string // more JSON fields, e.g. `,"channel_expiry_days":90`
}

func (f *fakeSettings) setExtra(extra string) {
	f.mu.Lock()
	f.extra = extra
	f.mu.Unlock()
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
	_, _ = w.Write([]byte(`{"registration":"open","require_email_verification":true,"channel_creation":"` + f.value + `","voice_channel_creation":"founders"` + f.extra + `}`))
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
	if _, err := render(get, "", dir, netSettings{ChannelCreation: policyRegistered}, nil); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, dir)
	if !strings.Contains(conf, "operator-only-creation: false") || !strings.Contains(conf, "        operator-only: false") ||
		strings.Contains(conf, "\napi:") || !strings.Contains(conf, "require-sasl:\n        enabled: false") {
		t.Fatalf("registered conf:\n%s", conf)
	}
	changed, err := render(get, "", dir, netSettings{ChannelCreation: policyRegistered}, nil)
	if err != nil || changed {
		t.Fatalf("re-render same policy: changed=%v err=%v", changed, err)
	}
	tok := strings.Repeat("a", 64)
	get = testEnv(map[string]string{"ERGO_API_TOKEN": tok, "IRC_REQUIRE_SASL": "true"})
	changed, err = render(get, "", dir, netSettings{ChannelCreation: policyAdmins}, nil)
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
	if _, err := render(testEnv(map[string]string{"ERGO_API_TOKEN": "short"}), "", dir, netSettings{ChannelCreation: policyAdmins}, nil); err == nil {
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

	if p := resolveSettings(ctx, get, dir, newSettingsClient(get), logf).ChannelCreation; p != policyAdmins {
		t.Fatalf("from auth: %q", p)
	}
	// Auth down: the remembered value wins over the env fallback.
	fs.set("", http.StatusBadGateway)
	if p := resolveSettings(ctx, get, dir, newSettingsClient(get), logf).ChannelCreation; p != policyAdmins {
		t.Fatalf("cached: %q", p)
	}
	// Nothing remembered: IRC_CHANNEL_CREATION, default registered.
	if p := resolveSettings(ctx, get, t.TempDir(), newSettingsClient(get), logf).ChannelCreation; p != policyRegistered {
		t.Fatalf("default: %q", p)
	}
	get2 := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL, "IRC_CHANNEL_CREATION": "admins"})
	if p := resolveSettings(ctx, get2, t.TempDir(), newSettingsClient(get2), logf).ChannelCreation; p != policyAdmins {
		t.Fatalf("env fallback: %q", p)
	}
	// Bad value from auth: treated as an error (cached value stays).
	fs.set("everyone", 0)
	if p := resolveSettings(ctx, get, dir, newSettingsClient(get), logf).ChannelCreation; p != policyAdmins {
		t.Fatalf("bad value: %q", p)
	}
}

func TestWatchSettingsRehashesOnChange(t *testing.T) {
	fs := &fakeSettings{value: "registered"}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	get := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL, "IRC_ROLES": "false"})
	if _, err := render(get, "", dir, netSettings{ChannelCreation: policyRegistered}, nil); err != nil {
		t.Fatal(err)
	}
	var rehashes atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w := newWatcher(get, "", dir, 0, func() error { rehashes.Add(1); return nil }, t.Logf)
		w.run(ctx, 10*time.Millisecond)
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

func TestRenderChannelLifecycle(t *testing.T) {
	yes, no, ninety, zero := true, false, 90, 0
	cases := []struct {
		name     string
		env      map[string]string
		st       netSettings
		want     []string
		rejected bool
	}{
		{"auth < 1.7: env defaults", nil, netSettings{ChannelCreation: policyRegistered},
			[]string{"auto-register: true\n", "expire-after: 0\n"}, false},
		{"auth < 1.7: env values", map[string]string{"IRC_CHANNEL_AUTO_REGISTER": "false", "IRC_CHANNEL_EXPIRY_DAYS": "30"},
			netSettings{ChannelCreation: policyRegistered}, []string{"auto-register: false\n", "expire-after: 30d\n"}, false},
		{"auth values win over the env fallback", map[string]string{"IRC_CHANNEL_AUTO_REGISTER": "true", "IRC_CHANNEL_EXPIRY_DAYS": "30"},
			netSettings{ChannelCreation: policyAdmins, AutoRegister: &no, ExpiryDays: &ninety},
			[]string{"auto-register: false\n", "expire-after: 90d\n", "operator-only: true"}, false},
		{"auth: expiry off", nil, netSettings{ChannelCreation: policyRegistered, AutoRegister: &yes, ExpiryDays: &zero},
			[]string{"auto-register: true\n", "expire-after: 0\n"}, false},
		{"IRC_CHANNEL_EXPIRE_AFTER overrides auth", map[string]string{"IRC_CHANNEL_EXPIRE_AFTER": "20s"},
			netSettings{ChannelCreation: policyRegistered, ExpiryDays: &ninety}, []string{"expire-after: 20s\n"}, false},
		{"bad IRC_CHANNEL_EXPIRE_AFTER", map[string]string{"IRC_CHANNEL_EXPIRE_AFTER": "soon"}, netSettings{ChannelCreation: policyRegistered}, nil, true},
		{"bad IRC_CHANNEL_EXPIRY_DAYS", map[string]string{"IRC_CHANNEL_EXPIRY_DAYS": "-1"}, netSettings{ChannelCreation: policyRegistered}, nil, true},
	}
	for _, c := range cases {
		dir := t.TempDir()
		_, err := render(testEnv(c.env), "", dir, c.st, nil)
		if c.rejected {
			if err == nil {
				t.Errorf("%s: accepted", c.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		conf := readConf(t, dir)
		for _, w := range c.want {
			if !strings.Contains(conf, "        "+w) {
				t.Errorf("%s: ircd.yaml lacks %q", c.name, w)
			}
		}
	}
}

func TestParseSettings(t *testing.T) {
	st, err := parseSettings([]byte(`{"channel_creation":"admins","channel_auto_register":false,"channel_expiry_days":365}`))
	if err != nil || st.ChannelCreation != policyAdmins || st.AutoRegister == nil || *st.AutoRegister || st.ExpiryDays == nil || *st.ExpiryDays != 365 {
		t.Fatalf("%+v %v", st, err)
	}
	// auth < 1.7 (and caches written by older helpers): no lifecycle fields
	st, err = parseSettings([]byte(`{"channel_creation":"registered"}`))
	if err != nil || st.AutoRegister != nil || st.ExpiryDays != nil {
		t.Fatalf("%+v %v", st, err)
	}
	for _, bad := range []string{`{"channel_creation":"registered","channel_expiry_days":3651}`, `{"channel_creation":"registered","channel_expiry_days":-1}`,
		`{"channel_auto_register":true}`, `{"channel_creation":"registered","channel_auto_register":"yes"}`} {
		if _, err := parseSettings([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestResolveSettingsLifecycleCached(t *testing.T) {
	fs := &fakeSettings{value: "registered", extra: `,"channel_auto_register":false,"channel_expiry_days":90`}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	get := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL})
	logf := func(string, ...any) {}
	st := resolveSettings(context.Background(), get, dir, newSettingsClient(get), logf)
	if st.AutoRegister == nil || *st.AutoRegister || st.ExpiryDays == nil || *st.ExpiryDays != 90 {
		t.Fatalf("from auth: %s", st)
	}
	fs.set("", http.StatusBadGateway)
	cached := resolveSettings(context.Background(), get, dir, newSettingsClient(get), logf)
	if !cached.equal(st) {
		t.Fatalf("cached %s, want %s", cached, st)
	}
}

func TestWatchSettingsRehashesOnExpiryChange(t *testing.T) {
	fs := &fakeSettings{value: "registered", extra: `,"channel_auto_register":true,"channel_expiry_days":0`}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	dir := t.TempDir()
	get := testEnv(map[string]string{"AUTH_SETTINGS_URL": srv.URL, "IRC_ROLES": "false"})
	w := newWatcher(get, "", dir, 0, func() error { return nil }, t.Logf)
	var rehashes int
	w.rehash = func() error { rehashes++; return nil }
	w.tick(context.Background())
	first := rehashes
	w.tick(context.Background())
	if rehashes != first {
		t.Fatal("rehash without a change")
	}
	fs.setExtra(`,"channel_auto_register":true,"channel_expiry_days":180`)
	w.tick(context.Background())
	if rehashes != first+1 || !strings.Contains(readConf(t, dir), "expire-after: 180d") {
		t.Fatalf("rehashes %d, conf:\n%s", rehashes, readConf(t, dir))
	}
}
