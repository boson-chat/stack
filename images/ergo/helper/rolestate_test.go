package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

const testRoles = `{"version":"3","roles":[
 {"id":"rol_2","name":"Admin","slug":"admin","position":900,"builtin":"admin","permissions":["roles.manage","irc.kill","irc.ban","irc.samode","irc.chanreg"],"irc":{"whois_line":"is a Network Admin","vhost":"admin/{username}","oper":true,"oper_class":"admin","capabilities":["kill","ban","samode","chanreg"]}},
 {"id":"rol_3","name":"Moderator","slug":"moderator","position":500,"builtin":"moderator","permissions":["irc.kill"],"irc":{"whois_line":"is a Moderator","vhost":null,"oper":true,"oper_class":"moderator","capabilities":["kill"]}},
 {"id":"rol_dj","name":"DJ","slug":"dj","position":10,"permissions":["voice.create"],"irc":{"whois_line":"is a DJ","vhost":"dj/{username}","oper":false,"oper_class":null,"capabilities":[]}},
 {"id":"rol_4","name":"Member","slug":"member","position":0,"builtin":"member","permissions":["channel.create"],"irc":{"whois_line":"is a Member","vhost":null,"oper":false,"oper_class":null,"capabilities":[]}}]}`

// fakeBoson is the auth service (roles, verify-password, users/{u}/irc,
// irc/grants) and Ergo's API (account_grants, rehash) in one server.
type fakeBoson struct {
	mu        sync.Mutex
	roles     string
	rolesETag string
	grants    string // body of /internal/v1/irc/grants; "" = 404
	pushed    map[string]roles.Grant
	rehashes  int
	noRoles   bool
}

func (f *fakeBoson) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/internal/v1/settings":
		_, _ = w.Write([]byte(`{"channel_creation":"registered"}`))
	case "/internal/v1/roles":
		if f.noRoles {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("If-None-Match") == f.rolesETag {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", f.rolesETag)
		_, _ = w.Write([]byte(f.roles))
	case "/internal/v1/irc/grants":
		if f.grants == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(f.grants))
	case "/internal/v1/verify-password":
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req["username"] + ":" + req["password"] {
		case "alice:pw":
			_, _ = w.Write([]byte(`{"ok":true,"username":"alice","irc":{"oper_class":"admin","whois_line":"is a Network Admin","vhost":"admin/alice"},"roles":["Admin"],"permissions":[]}`))
		case "dave:pw":
			_, _ = w.Write([]byte(`{"ok":true,"username":"dave","irc":{"oper_class":null,"whois_line":null,"vhost":"dj/dave"},"roles":["DJ"],"permissions":[]}`))
		case "old:pw": // auth without roles
			_, _ = w.Write([]byte(`{"ok":true,"username":"old"}`))
		case "roleserv:pw":
			_, _ = w.Write([]byte(`{"ok":true,"username":"roleserv"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":false}`))
		}
	case "/internal/v1/users/alice/irc":
		_, _ = w.Write([]byte(`{"user_id":"usr_1","username":"alice","disabled":false,"irc":{"oper_class":"admin","whois_line":"is a Network Admin","vhost":"admin/alice"},"roles":["Admin"],"permissions":["irc.kill"]}`))
	case "/internal/v1/users/carl/irc":
		_, _ = w.Write([]byte(`{"user_id":"usr_2","username":"carl","disabled":true,"irc":{"oper_class":null,"whois_line":null,"vhost":"users/carl"},"roles":[],"permissions":[]}`))
	case "/internal/v1/users/gone/irc":
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not found","code":"not_found"}`))
	case "/v1/account_grants":
		var req struct {
			AccountName string `json:"accountName"`
			roles.Grant
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if f.pushed == nil {
			f.pushed = map[string]roles.Grant{}
		}
		f.pushed[req.AccountName] = req.Grant
		_, _ = w.Write([]byte(`{"success":true,"clients":1}`))
	case "/v1/rehash":
		f.rehashes++
		_, _ = w.Write([]byte(`{"success":true}`))
	case "/v1/ns/saregister":
		_, _ = w.Write([]byte(`{"success":false,"errorCode":"ACCOUNT_EXISTS"}`))
	default:
		http.NotFound(w, r)
	}
}

func bosonEnv(srv *httptest.Server, extra map[string]string) func(string) string {
	m := map[string]string{
		"AUTH_SETTINGS_URL": srv.URL + "/internal/v1/settings",
		"AUTH_VERIFY_URL":   srv.URL + "/internal/v1/verify-password",
		"AUTH_INTERNAL_URL": srv.URL, "AUTH_PUBLIC_URL": srv.URL,
		"ERGO_API_URL": srv.URL, "ERGO_API_TOKEN": strings.Repeat("e", 32),
	}
	for k, v := range extra {
		m[k] = v
	}
	return testEnv(m)
}

func TestRenderRoleClasses(t *testing.T) {
	f := &fakeBoson{roles: testRoles, rolesETag: `"3"`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := t.TempDir()
	get := bosonEnv(srv, nil)
	rc := resolveRoles(context.Background(), get, dir, newRolesClient(get), t.Logf)
	classes, err := classesFor(get, rc)
	if err != nil || len(classes) != 2 {
		t.Fatalf("%v %+v", err, classes)
	}
	if _, err := render(get, "", dir, netSettings{ChannelCreation: policyRegistered}, classes); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, dir)
	for _, want := range []string{
		`"admin":`, `title: "Admin"`, `whoisline: "is a Network Admin"`, `- "chanreg"`,
		`"moderator":`, `title: "Moderator"`, `"boson.service":`, `"boson.server-admin":`, `":6680": {}`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("ircd.yaml lacks %s", want)
		}
	}
	if strings.Contains(conf, `"dj":`) || strings.Contains(conf, `"member":`) {
		t.Error("non-oper roles must not become oper classes")
	}
	// the base capability is added to every role class
	mod := conf[strings.Index(conf, `"moderator":`):]
	if !strings.Contains(mod[:200], `- "nofakelag"`) || !strings.Contains(mod[:200], `- "kill"`) {
		t.Errorf("moderator class: %s", mod[:200])
	}

	// auth down at the next start: the cached roles are used
	f.mu.Lock()
	f.roles, f.rolesETag = "", "x"
	f.mu.Unlock()
	srv.Close()
	rc = resolveRoles(context.Background(), get, dir, newRolesClient(get), t.Logf)
	if len(rc.Roles) != 4 {
		t.Errorf("cache fallback: %d roles", len(rc.Roles))
	}
	if _, err := baseCapabilities(testEnv(map[string]string{"IRC_ROLE_BASE_CAPABILITIES": "kill,fly"})); err == nil {
		t.Error("unknown base capability accepted")
	}
}

func TestAuthScriptGrants(t *testing.T) {
	f := &fakeBoson{roles: testRoles, rolesETag: `"3"`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := t.TempDir()
	get := bosonEnv(srv, map[string]string{"ERGO_DIR": dir, "INTERNAL_API_TOKEN": "tok"})
	resolveRoles(context.Background(), get, dir, newRolesClient(get), t.Logf) // the cache render leaves behind
	c := newAuthClient(get)

	o := run(t, c, `{"accountName":"alice","passphrase":"pw"}`)
	if !o.Success || o.OperClass == nil || *o.OperClass != "admin" || o.WhoisLine != "" || *o.VHost != "admin/alice" {
		t.Fatalf("alice: %+v", o)
	}
	o = run(t, c, `{"accountName":"dave","passphrase":"pw"}`)
	if !o.Success || *o.OperClass != "" || *o.VHost != "dj/dave" {
		t.Fatalf("dave: %+v", o)
	}
	// old auth (no irc object): no grant fields at all, Ergo changes nothing
	var raw strings.Builder
	authScriptMain(strings.NewReader(`{"accountName":"old","passphrase":"pw"}`+"\n"), &raw, c)
	if strings.Contains(raw.String(), "operClass") || strings.Contains(raw.String(), "vhost") {
		t.Fatalf("old auth: %s", raw.String())
	}
	// RoleServ's account can't be logged into through auth, whatever auth says
	for _, in := range []string{`{"accountName":"RoleServ","passphrase":"pw"}`, `{"accountName":"roleserv","passphrase":"pw"}`} {
		if o := run(t, c, in); o.Success || o.Error != "" {
			t.Fatalf("reserved: %+v", o)
		}
	}
}

func TestBearerGrantAndReserved(t *testing.T) {
	f := &fakeBoson{roles: testRoles, rolesETag: `"3"`}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	mux := http.NewServeMux()
	mux.Handle("/", f)
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "kid": "k", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	get := bosonEnv(srv, map[string]string{"ERGO_DIR": dir, "AUTH_JWKS_URL": srv.URL + "/jwks", "AUTH_ISSUER": "https://auth.example.com", "INTERNAL_API_TOKEN": "tok"})
	resolveRoles(context.Background(), get, dir, newRolesClient(get), t.Logf)
	c := newAuthClient(get)
	tok := func(name string) string {
		h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "k", "typ": "at+jwt"})
		p, _ := json.Marshal(map[string]any{"iss": "https://auth.example.com", "sub": "u", "aud": "boson", "name": name, "exp": time.Now().Unix() + 60})
		s := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
		b, _ := json.Marshal(map[string]any{"oauth2": map[string]string{"token": s + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(s)))}})
		return string(b)
	}
	if o := run(t, c, tok("alice")); !o.Success || o.OperClass == nil || *o.OperClass != "admin" || *o.VHost != "admin/alice" {
		t.Fatalf("alice bearer: %+v", o)
	}
	if o := run(t, c, tok("RoleServ")); o.Success {
		t.Fatalf("reserved bearer: %+v", o)
	}
	// users/{u}/irc missing (old auth): login works, no grant
	if o := run(t, c, tok("zed")); !o.Success || o.OperClass != nil {
		t.Fatalf("zed: %+v", o)
	}
	// disabled or deleted accounts don't get in with a leftover token
	for _, name := range []string{"carl", "gone"} {
		if o := run(t, c, tok(name)); o.Success {
			t.Fatalf("%s: %+v", name, o)
		}
	}
}

func TestWatcherPushesGrants(t *testing.T) {
	f := &fakeBoson{roles: testRoles, rolesETag: `"3"`,
		grants: `{"users":[{"username":"alice","oper_class":"admin","whois_line":"is a Network Admin","vhost":"admin/alice"},{"username":"Dave","oper_class":null,"vhost":"dj/dave"}]}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := t.TempDir()
	get := bosonEnv(srv, nil)
	rehashed := 0
	w := newWatcher(get, "", dir, time.Hour, func() error { rehashed++; return nil }, t.Logf)
	ctx := context.Background()

	w.tick(ctx)
	f.mu.Lock()
	if g := f.pushed["alice"]; g.OperClass == nil || *g.OperClass != "admin" {
		t.Errorf("alice: %+v", g)
	}
	if g := f.pushed["dave"]; g.OperClass == nil || *g.OperClass != "" || *g.VHost != "dj/dave" {
		t.Errorf("dave: %+v", g)
	}
	f.pushed = nil
	// Dave loses DJ; Moderator becomes a non-oper role (a re-render)
	f.grants = `{"users":[{"username":"alice","oper_class":"admin","whois_line":"is a Network Admin","vhost":"admin/alice"}]}`
	f.roles = strings.Replace(testRoles, `"oper":true,"oper_class":"moderator"`, `"oper":false,"oper_class":null`, 1)
	f.rolesETag = `"4"`
	f.mu.Unlock()
	if rehashed != 1 { // the first tick rendered for the first time
		t.Errorf("rehashes after first tick: %d", rehashed)
	}

	w.tick(ctx)
	f.mu.Lock()
	if g, ok := f.pushed["dave"]; !ok || !g.Equal(roles.None()) {
		t.Errorf("dave should be revoked: %+v", g)
	}
	if _, ok := f.pushed["alice"]; ok {
		t.Errorf("alice unchanged, shouldn't be pushed again")
	}
	f.mu.Unlock()
	if rehashed != 2 || strings.Contains(readConf(t, dir), `"moderator":`) {
		t.Errorf("moderator class should be gone after a rehash (%d)", rehashed)
	}

	// the snapshot survives a restart: nothing is pushed again
	f.mu.Lock()
	f.pushed = nil
	f.mu.Unlock()
	w2 := newWatcher(get, "", dir, time.Hour, func() error { return nil }, t.Logf)
	w2.tick(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushed) != 0 {
		t.Errorf("pushed after restart: %v", f.pushed)
	}
}

func TestWatcherWithoutRolesAPI(t *testing.T) {
	f := &fakeBoson{noRoles: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	dir := t.TempDir()
	w := newWatcher(bosonEnv(srv, nil), "", dir, time.Hour, func() error { return nil }, t.Logf)
	w.tick(context.Background())
	conf := readConf(t, dir)
	if strings.Contains(conf, `title: "Admin"`) || !strings.Contains(conf, `"boson.server-admin":`) {
		t.Error("an auth without roles must render today's config")
	}
	if len(f.pushed) != 0 {
		t.Error("nothing to push without roles")
	}
}
