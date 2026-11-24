package main

import (
	"context"
	"encoding/json"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseStackConfig(t *testing.T) {
	got, err := parseStackConfig([]byte(`version: 1
network:
  name: Test Net
auth: {guest_access: false, channel_creation: admins}   # auth's, not read here
irc:
  plaintext: true
  plaintext_port: 6668
  require_sasl_exempt: [127.0.0.1/32, "::1", localhost]
  roles_poll_seconds: 5
  settings_poll_seconds: 20
  websocket: false
  websocket_origins: ["https://example.com", "https://*.example.com"]
  oauthbearer: false
  history_expire: 30d
  channel_creator_grace: 15m
  log_level: DEBUG
  persistent_history: false
  something_new: 1
  # auth's (served to RoleServ on its feed), not read here
  channels:
    - {name: "#lobby", topic: "Welcome", autojoin: true}
  channel_founder: thelion
  autojoin_guests: true
voice: {rooms: []}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"NETWORK_NAME": "Test Net", "IRC_PLAINTEXT": "true", "IRC_PLAINTEXT_LISTEN_PORT": "6668",
		"IRC_REQUIRE_SASL_EXEMPT": "127.0.0.1/32,::1,localhost", "ROLES_POLL_SECONDS": "5", "SETTINGS_POLL_SECONDS": "20",
		"IRC_WEBSOCKET": "false", "IRC_WEBSOCKET_ORIGINS": "https://example.com,https://*.example.com",
		"IRC_OAUTHBEARER": "false", "IRC_HISTORY_EXPIRE": "30d", "IRC_CHANNEL_CREATOR_GRACE": "15m", "IRC_LOG_LEVEL": "debug", "IRC_PERSISTENT_HISTORY": "false",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, in := range []string{"", "version: 1\n", "irc:\n", "irc: ~\nnetwork:\n", "irc:\n  plaintext:\n"} {
		if got, err := parseStackConfig([]byte(in)); err != nil || len(got) != 0 {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
}

func TestParseStackConfigErrors(t *testing.T) {
	for _, c := range []struct {
		in        string
		line, col int
		msg       string
	}{
		{"irc:\n  plaintext: maybe\n", 2, 14, "irc.plaintext: want true or false"},
		{"irc:\n  plaintext_port: 70000\n", 2, 19, "must be 1..65535"},
		{"irc:\n  require_sasl_exempt: [10.0.0.0/8, nope]\n", 2, 37, `"nope" is not an IP, CIDR or localhost`},
		{"irc:\n  require_sasl_exempt: 10.0.0.0/8\n", 2, 24, "want a list"},
		{"irc:\n  roles_poll_seconds: 0\n", 2, 23, "must be 1..86400"},
		{"irc:\n  history_expire: forever\n", 2, 19, "not a duration"},
		{"irc:\n  channel_creator_grace: 10 minutes\n", 2, 26, "not a duration"},
		{"irc:\n  log_level: loud\n", 2, 14, "want one of"},
		{"network:\n  name: \"\"\n", 2, 9, "network.name: must be 1-64"},
		{"irc: [plaintext]\n", 1, 6, "irc: must be a mapping"},
		{"version: 2\n", 1, 10, "unsupported config file version"},
		{"irc:\n  plaintext: [\n", 0, 0, "yaml"},
	} {
		_, err := parseStackConfig([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Fatalf("%q: err %v, want %q", c.in, err, c.msg)
		}
		if c.line == 0 {
			continue
		}
		if l, col := errPosition(err); l != c.line || col != c.col {
			t.Fatalf("%q: at %d:%d, want %d:%d (%v)", c.in, l, col, c.line, c.col, err)
		}
	}
}

func TestOverlayPinsRestartKeys(t *testing.T) {
	env := testEnv(map[string]string{"IRC_PLAINTEXT": "false", "NETWORK_NAME": "EnvNet"})
	o := newOverlay(env, map[string]string{"IRC_PLAINTEXT": "true"})
	if o.get("IRC_PLAINTEXT") != "true" || o.get("NETWORK_NAME") != "EnvNet" {
		t.Fatal("file must beat env, env must fill in")
	}
	o.pin(map[string]string{"IRC_PERSISTENT_HISTORY": ""}) // started with the default (true)
	if got := o.restartRequired(); len(got) != 0 {
		t.Fatalf("restart_required %v", got)
	}
	o.set(map[string]string{"IRC_PERSISTENT_HISTORY": "true"}) // explicit default: no change
	if got := o.restartRequired(); len(got) != 0 {
		t.Fatalf("restart_required %v", got)
	}
	o.set(map[string]string{"IRC_PERSISTENT_HISTORY": "false"})
	if got := o.restartRequired(); !slices.Equal(got, []string{"irc.persistent_history"}) {
		t.Fatalf("restart_required %v", got)
	}
	if o.get("IRC_PERSISTENT_HISTORY") != "" {
		t.Fatal("pinned key must keep the running value")
	}
}

func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(path), ".boson.yaml.tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func eventuallyT(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestConfigFileReRendersAndRehashes: watch-settings with BOSON_CONFIG. An
// atomic rewrite of the file re-renders ircd.yaml (plaintext listener on a
// new port, exempt list, network name) and rehashes Ergo; an invalid file
// keeps the last good config with the error's position on the status
// endpoint; persistent_history is restart_required and stays as rendered.
func TestConfigFileReRendersAndRehashes(t *testing.T) {
	dir := t.TempDir()
	cfgDir := t.TempDir()
	path := filepath.Join(cfgDir, "boson.yaml")
	replaceFile(t, path, "version: 1\nirc:\n  plaintext: false\n")

	get := testEnv(map[string]string{"IRC_ROLES": "false", "AUTH_SETTINGS_URL": "http://127.0.0.1:1/unreachable"})
	vals, _, err := readStackConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := newOverlay(get, vals)
	// What `boson-ergo render` does at container start.
	st := netSettings{ChannelCreation: policyRegistered}
	if _, err := render(cfg.get, "", dir, st, nil); err != nil {
		t.Fatal(err)
	}
	if err := saveRunningConfig(dir, cfg.get); err != nil {
		t.Fatal(err)
	}
	cfg.pin(loadRunningConfig(dir, cfg.unpinned))

	var rehashes atomic.Int32
	w := newWatcher(cfg.get, "", dir, time.Hour, func() error { rehashes.Add(1); return nil }, t.Logf)
	w.cfg = cfg
	cw := newConfigWatcher(path, w.loadConfigFile, t.Logf)
	cw.debounce, cw.pollEvery = 10*time.Millisecond, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.run(ctx, time.Hour)
	if err := cw.reload(ctx); err != nil {
		t.Fatal(err)
	}
	go cw.run(ctx)
	srv := httptest.NewServer(statusHandler(cw))
	defer srv.Close()
	status := func() configStatus {
		resp, err := srv.Client().Get(srv.URL + configStatusPath)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s configStatus
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); !s.OK || s.LoadedAt == nil || s.Path != path || len(s.RestartRequired) != 0 || rehashes.Load() != 0 {
		t.Fatalf("start: %+v, %d rehashes", s, rehashes.Load())
	}
	if strings.Contains(readConf(t, dir), `":6667"`) {
		t.Fatal("plaintext listener while off")
	}
	time.Sleep(30 * time.Millisecond)

	replaceFile(t, path, `version: 1
network: {name: Renamed}
irc:
  plaintext: true
  plaintext_port: 6668
  require_sasl_exempt: [10.1.0.0/16]
`)
	eventuallyT(t, "rehash", func() bool { return rehashes.Load() == 1 })
	conf := readConf(t, dir)
	for _, want := range []string{`":6668": {}`, `- "10.1.0.0/16"`, `name: "Renamed"`, `netname: "renamed"`} {
		if !strings.Contains(conf, want) {
			t.Fatalf("ircd.yaml lacks %q", want)
		}
	}
	if s := status(); !s.OK || len(s.RestartRequired) != 0 {
		t.Fatalf("after reload: %+v", s)
	}

	// Invalid: nothing re-rendered, the status says where.
	replaceFile(t, path, "version: 1\nirc:\n  plaintext: false\n  require_sasl_exempt: [nope]\n")
	eventuallyT(t, "status not ok", func() bool { return !status().OK })
	if s := status(); s.ErrorLine != 4 || !strings.Contains(s.Error, "nope") || s.LoadedAt == nil {
		t.Fatalf("invalid: %+v", s)
	}
	if !strings.Contains(readConf(t, dir), `":6668": {}`) || rehashes.Load() != 1 {
		t.Fatal("invalid file applied")
	}

	// persistent_history can't be rehashed: reported, not rendered.
	replaceFile(t, path, "version: 1\nirc:\n  plaintext: false\n  persistent_history: false\n")
	eventuallyT(t, "restart_required", func() bool {
		s := status()
		return s.OK && slices.Equal(s.RestartRequired, []string{"irc.persistent_history"})
	})
	conf = readConf(t, dir)
	if strings.Contains(conf, `":6668"`) || !strings.Contains(conf, "database-path") || rehashes.Load() != 2 {
		t.Fatalf("plaintext off must apply, persistent history must stay (rehashes %d)", rehashes.Load())
	}

	// Removing the file: back to env (persistent history default true = running).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	eventuallyT(t, "missing file", func() bool { s := status(); return s.OK && s.FileMtime == nil })
	if s := status(); len(s.RestartRequired) != 0 {
		t.Fatalf("no file: %+v", s)
	}
}

func TestConfigWatcherPollFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boson.yaml")
	replaceFile(t, path, "a: 1\n")
	var loads atomic.Int32
	cw := newConfigWatcher(path, func(context.Context, []byte) ([]string, error) { loads.Add(1); return nil, nil }, t.Logf)
	cw.debounce, cw.pollEvery = time.Hour, 20*time.Millisecond // only the poller can see it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = cw.reload(ctx)
	go cw.run(ctx)
	time.Sleep(30 * time.Millisecond)
	replaceFile(t, path, "a: 22\n")
	eventuallyT(t, "poll reload", func() bool { return loads.Load() == 2 })
}

func TestStatusWithoutConfigFile(t *testing.T) {
	cw := newConfigWatcher("", func(context.Context, []byte) ([]string, error) { return nil, nil }, t.Logf)
	if err := cw.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := cw.Status(); !s.OK || s.Path != "" || s.LoadedAt == nil || s.RestartRequired == nil {
		t.Fatalf("%+v", s)
	}
}
