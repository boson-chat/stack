package panel_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/boson-chat/stack/images/telemetry/internal/devseed"
	"github.com/boson-chat/stack/images/telemetry/internal/panel"
	"github.com/boson-chat/stack/images/telemetry/internal/rollup"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/vm"
)

type env struct {
	h   http.Handler
	now time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	ctx := context.Background()
	if err := devseed.Seed(ctx, st, e.now, 20, 30, 1); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := &vm.Fake{}
	if err := (&rollup.Rollup{Store: st, VM: fake, Now: func() time.Time { return e.now }, Log: log}).Catchup(ctx); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("right password"), bcrypt.MinCost)
	p := &panel.Panel{Store: st, VM: fake, Username: "owner", PasswordHash: hash, Now: func() time.Time { return e.now }, Log: log,
		Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>app")}, "assets/a.js": {Data: []byte("js")}}}
	e.h = p.Handler()
	return e
}

func (e *env) do(method, path, body, cookie string, xstats bool, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if xstats {
		r.Header.Set("X-Stats", "1")
	}
	if cookie != "" {
		r.Header.Set("Cookie", panel.CookieName+"="+cookie)
	}
	if remote != "" {
		r.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) login(t *testing.T) string {
	t.Helper()
	w := e.do("POST", "/api/login", `{"username":"owner","password":"right password"}`, "", true, "")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == panel.CookieName {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
				t.Errorf("cookie flags: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestAuth(t *testing.T) {
	e := setup(t)
	if w := e.do("GET", "/api/overview", "", "", false, ""); w.Code != 401 {
		t.Errorf("no session: %d", w.Code)
	}
	if w := e.do("GET", "/api/overview", "", "forged", false, ""); w.Code != 401 {
		t.Errorf("forged session: %d", w.Code)
	}
	if w := e.do("POST", "/api/login", `{"username":"owner","password":"right password"}`, "", false, ""); w.Code != 403 {
		t.Errorf("login without X-Stats: %d", w.Code)
	}
	for _, bad := range []string{`{"username":"owner","password":"wrong"}`, `{"username":"root","password":"right password"}`} {
		if w := e.do("POST", "/api/login", bad, "", true, ""); w.Code != 401 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	tok := e.login(t)
	if w := e.do("GET", "/api/overview", "", tok, false, ""); w.Code != 200 {
		t.Errorf("with session: %d", w.Code)
	}
	if w := e.do("POST", "/api/logout", "", tok, false, ""); w.Code != 403 {
		t.Errorf("logout without X-Stats: %d", w.Code)
	}
	if w := e.do("POST", "/api/logout", "", tok, true, ""); w.Code != 200 {
		t.Errorf("logout: %d", w.Code)
	}
	if w := e.do("GET", "/api/overview", "", tok, false, ""); w.Code != 401 {
		t.Errorf("after logout: %d", w.Code)
	}
	// Sessions expire.
	tok = e.login(t)
	e.now = e.now.Add(panel.SessionTTL + time.Minute)
	if w := e.do("GET", "/api/me", "", tok, false, ""); w.Code != 401 {
		t.Errorf("expired session: %d", w.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t)
	for i := 0; i < panel.LoginFailures; i++ {
		e.do("POST", "/api/login", `{"username":"owner","password":"nope"}`, "", true, "198.51.100.7:1")
	}
	if w := e.do("POST", "/api/login", `{"username":"owner","password":"right password"}`, "", true, "198.51.100.7:1"); w.Code != 429 {
		t.Errorf("after %d failures: %d", panel.LoginFailures, w.Code)
	}
	if w := e.do("POST", "/api/login", `{"username":"owner","password":"right password"}`, "", true, "198.51.100.8:1"); w.Code != 200 {
		t.Errorf("another IP: %d", w.Code)
	}
	e.now = e.now.Add(panel.LoginFailWindow)
	if w := e.do("POST", "/api/login", `{"username":"owner","password":"right password"}`, "", true, "198.51.100.7:1"); w.Code != 200 {
		t.Errorf("after the window: %d", w.Code)
	}
}

func TestPages(t *testing.T) {
	e := setup(t)
	tok := e.login(t)
	get := func(path string) map[string]any {
		w := e.do("GET", path, "", tok, false, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	ov := get("/api/overview")
	if ov["active"].(map[string]any)["d30"].(float64) == 0 || len(ov["weeks"].([]any)) != 12 {
		t.Errorf("overview: %v", ov)
	}
	if s := ov["series"].(map[string]any)["installs"].([]any); len(s) != 1 {
		t.Errorf("installs series: %v", s)
	}
	v := get("/api/versions?behind=2")
	if v["latest"] != "1.48.0" || len(v["services"].([]any)) == 0 {
		t.Errorf("versions: latest %v", v["latest"])
	}
	for _, o := range v["old"].([]any) {
		if o.(map[string]any)["behind"].(float64) < 2 {
			t.Errorf("old install not behind: %v", o)
		}
	}
	get("/api/features")
	pr := get("/api/problems")
	list := pr["installs"].([]any)
	if len(list) == 0 {
		t.Fatal("no problems in the seed")
	}
	id := list[0].(map[string]any)["id"].(string)
	if len(id) != 12 {
		t.Errorf("short id %q", id)
	}
	in := get("/api/installs/" + id)
	h := in["history"].([]any)
	if len(h) == 0 {
		t.Fatal("no history")
	}
	if _, ok := h[0].(map[string]any)["report"].(map[string]any)["install_id"]; ok {
		t.Error("install_id in the history")
	}
	get("/api/apps")
	get("/api/countries")
	for _, bad := range []string{"/api/installs/zzzzzzzzzzzz", "/api/installs/abc", "/api/installs/000000000000"} {
		if w := e.do("GET", bad, "", tok, false, ""); w.Code != 404 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}

func TestStaticAndHeaders(t *testing.T) {
	e := setup(t)
	w := e.do("GET", "/problems", "", "", false, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "app") {
		t.Errorf("SPA fallback: %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers: %v", w.Header())
	}
	if w := e.do("GET", "/assets/a.js", "", "", false, ""); !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset caching: %v", w.Header())
	}
	if w := e.do("POST", "/v1/report", "{}", "", false, ""); w.Code != 404 {
		t.Errorf("ingest on the panel: %d", w.Code)
	}
}
