package panel_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/testutil"
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
	hw := get("/api/hardware")
	if hw["reported"].(float64) == 0 || len(hw["sizes"].(map[string]any)["ram_gb"].([]any)) == 0 {
		t.Errorf("hardware: %v", hw)
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

// The Hardware page: installs per bucket in bucket order (unknown last,
// empty buckets left out), and the median usage next to each size.
func TestHardware(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	put := func(i int, hs any, cpu, mem, disk any, age time.Duration) {
		m := testutil.Report(fmt.Sprintf("%032x", i+1))
		if hs != "absent" {
			m["host_size"] = hs
		}
		h := m["health"].(map[string]any)["host"].(map[string]any)
		h["cpu_p95"], h["mem_p95"], h["disk_used"] = cpu, mem, disk
		r, err := schema.ParseReport(testutil.JSON(t, m))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutReport(ctx, r, "DE", now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	size := func(cpu, ram, disk, arch any) map[string]any {
		return map[string]any{"cpu_cores": cpu, "ram_gb": ram, "disk_gb": disk, "arch": arch}
	}
	put(0, size("2", "2-4", "25-50", "amd64"), 60.0, 80.0, 40.0, time.Hour)
	put(1, size("2", "2-4", "25-50", "amd64"), 70.0, 90.0, nil, time.Hour)
	put(2, size("2", "4-8", "50-100", "arm64"), 50.0, 50.0, 20.0, time.Hour)
	put(3, size("9-16", "64+", nil, "amd64"), 10.0, 20.0, 5.0, time.Hour)
	put(4, size(nil, nil, nil, nil), 99.0, 99.0, 99.0, time.Hour)
	put(5, "absent", 99.0, 99.0, 99.0, time.Hour)                           // an older admin
	put(6, size("1", "<1", "<25", "other"), 1.0, 1.0, 1.0, 10*24*time.Hour) // not active in 7 days

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	p := &panel.Panel{Store: st, Username: "owner", PasswordHash: hash, Now: func() time.Time { return now }, Log: log,
		Static: fstest.MapFS{"index.html": {Data: []byte("app")}}}
	e := &env{h: p.Handler(), now: now}
	w := e.do("POST", "/api/login", `{"username":"owner","password":"pw"}`, "", true, "")
	tok := w.Result().Cookies()[0].Value
	w = e.do("GET", "/api/hardware", "", tok, false, "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct {
		Installs int                         `json:"installs"`
		Reported int                         `json:"reported"`
		Sizes    map[string][]map[string]any `json:"sizes"`
		Usage    map[string][]struct {
			Bucket   string   `json:"bucket"`
			Installs int      `json:"installs"`
			Reported int      `json:"reported"`
			Median   *float64 `json:"median"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Installs != 6 || got.Reported != 5 {
		t.Errorf("installs %d reported %d", got.Installs, got.Reported)
	}
	str := func(l []map[string]any) string {
		var b []string
		for _, c := range l {
			b = append(b, fmt.Sprintf("%s=%v", c["name"], c["count"]))
		}
		return strings.Join(b, ",")
	}
	for dim, want := range map[string]string{
		"cpu_cores": "2=3,9-16=1,unknown=2",
		"ram_gb":    "2-4=2,4-8=1,64+=1,unknown=2",
		"disk_gb":   "25-50=2,50-100=1,unknown=3",
		"arch":      "amd64=3,arm64=1,unknown=2",
	} {
		if s := str(got.Sizes[dim]); s != want {
			t.Errorf("%s: %s, want %s", dim, s, want)
		}
	}
	med := func(dim, bucket string) (string, int) {
		for _, u := range got.Usage[dim] {
			if u.Bucket == bucket {
				if u.Median == nil {
					return "null", u.Reported
				}
				return fmt.Sprint(*u.Median), u.Reported
			}
		}
		return "missing", 0
	}
	for _, c := range []struct {
		dim, bucket, want string
		reported          int
	}{
		{"cpu_cores", "2", "60", 3}, // 50, 60, 70
		{"cpu_cores", "9-16", "10", 1},
		{"ram_gb", "2-4", "85", 2},    // 80, 90
		{"disk_gb", "25-50", "40", 1}, // one didn't report disk_used
		{"disk_gb", "unknown", "missing", 0},
	} {
		if m, n := med(c.dim, c.bucket); m != c.want || n != c.reported {
			t.Errorf("%s %s: median %s from %d, want %s from %d", c.dim, c.bucket, m, n, c.want, c.reported)
		}
	}
	if _, ok := got.Usage["arch"]; ok {
		t.Error("usage for arch")
	}
}
