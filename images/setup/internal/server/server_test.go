package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/dockercli"
	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/flow/addon"
	"github.com/boson-chat/stack/images/setup/internal/flow/fresh"
	"github.com/boson-chat/stack/images/setup/internal/gate"
	"github.com/boson-chat/stack/images/setup/internal/install"
)

type dnsTo string

func (d dnsTo) LookupIP(_ context.Context, network, host string) ([]net.IP, error) {
	if network == "ip6" {
		return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	if strings.HasSuffix(host, "elsewhere.test") {
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	return []net.IP{net.ParseIP(string(d))}, nil
}

func setup(t *testing.T) (*httptest.Server, *gate.Gate, *install.FakeCompose, string) {
	dir, src := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "config"), 0o755)
	ex, _ := os.ReadFile("../../../../config/boson.example.yaml")
	os.WriteFile(filepath.Join(src, "config", "boson.example.yaml"), ex, 0o644)
	fc := &install.FakeCompose{}
	r, err := install.NewRunner(&install.Runner{Dir: dir, StackSrc: src, Compose: fc, Store: install.NewStore(dir), Hub: install.NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	r.Timeouts = install.Timeouts{Health: time.Second, Certs: time.Second, Verify: time.Second, FailFor: time.Second, Poll: 10 * time.Millisecond}
	install.DevSkipNetwork(r)
	g := gate.NewWithCode("ABCD-EFGH")
	deps := fresh.Deps{InstallDir: dir, Runner: r, Redactor: &dockercli.Redactor{},
		DNS:   &checks.DNS{Resolver: dnsTo("192.0.2.1"), LocalIPs: func() []net.IP { return []net.IP{net.ParseIP("192.0.2.1")} }},
		Ports: &checks.Ports{HostIP: func() net.IP { return nil }, Nonce: func() string { return "n" }},
	}
	fr, err := fresh.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ad, err := addon.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	reg := &flow.Registry{}
	reg.Add(fr)
	reg.Add(ad)
	s := &Server{Gate: g, Flows: reg, Static: fstest.MapFS{"index.html": {Data: []byte("hi")}}, Instance: "inst", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, g, fc, dir
}

func client(ts *httptest.Server) *http.Client {
	c := ts.Client()
	c.Jar, _ = cookiejar.New(nil)
	return c
}

func do(t *testing.T, c *http.Client, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestSessionGate(t *testing.T) {
	ts, _, _, _ := setup(t)
	c := client(ts)
	if code, _ := do(t, c, "GET", ts.URL+"/api/flows", ""); code != 401 {
		t.Fatalf("no session: %d", code)
	}
	if code, _ := do(t, c, "GET", ts.URL+"/api/flows/fresh/state", ""); code != 401 {
		t.Fatalf("flow without session: %d", code)
	}
	if code, m := do(t, c, "POST", ts.URL+"/api/session", `{"code":"XXXX-XXXX"}`); code != 401 || m["code"] != "bad_code" {
		t.Fatalf("bad code: %d %v", code, m)
	}
	// Not JSON: refused before anything else.
	req, _ := http.NewRequest("POST", ts.URL+"/api/session", strings.NewReader("code=ABCD-EFGH"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, _ := c.Do(req); resp.StatusCode != 415 {
		t.Fatalf("form post: %d", resp.StatusCode)
	}
	// Cross-origin: refused.
	req, _ = http.NewRequest("POST", ts.URL+"/api/session", strings.NewReader(`{"code":"ABCD-EFGH"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ := c.Do(req); resp.StatusCode != 403 {
		t.Fatalf("cross origin: %d", resp.StatusCode)
	}
	code, _ := do(t, c, "POST", ts.URL+"/api/session", `{"code":"abcd-efgh"}`)
	if code != 200 {
		t.Fatalf("redeem: %d", code)
	}
	u, _ := http.NewRequest("GET", ts.URL, nil)
	ck := c.Jar.Cookies(u.URL)
	if len(ck) != 1 {
		t.Fatal("no cookie")
	}
	if code, m := do(t, c, "GET", ts.URL+"/api/flows", ""); code != 200 || len(m["flows"].([]any)) != 2 {
		t.Fatalf("flows: %d %v", code, m)
	}
	// Single use: another browser can't.
	if code, m := do(t, client(ts), "POST", ts.URL+"/api/session", `{"code":"ABCD-EFGH"}`); code != 403 || m["code"] != "used" {
		t.Fatalf("second redeem: %d %v", code, m)
	}
	// Rate limit.
	c2 := client(ts)
	for i := 0; i < gate.PerIPFailures; i++ {
		do(t, c2, "POST", ts.URL+"/api/session", `{"code":"ZZZZ-ZZZZ"}`)
	}
	if code, _ := do(t, c2, "POST", ts.URL+"/api/session", `{"code":"ZZZZ-ZZZZ"}`); code != 429 {
		t.Fatalf("rate limit: %d", code)
	}
	// The instance probe is public.
	resp, _ := c2.Get(ts.URL + "/.well-known/boson-setup")
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "inst" {
		t.Fatal(string(b))
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("no CSP")
	}
}

func TestFreshFlowHTTP(t *testing.T) {
	ts, g, fc, dir := setup(t)
	c := client(ts)
	do(t, c, "POST", ts.URL+"/api/session", `{"code":"ABCD-EFGH"}`)
	base := ts.URL + "/api/flows/fresh/"

	ans := `{"network_name":"Net","domain":"x.elsewhere.test","wellknown":false,"tls":{"email":"a@x.test","ca":"production","custom_url":""},
	"admin":{"username":"alice","email":"a@x.test"},"voice":true,"video":false,"files":{"enabled":true,"storage":"local","s3_endpoint":"","s3_bucket":"","s3_region":"auto","s3_path_style":true},
	"directory_listing":true,"own_directory":false,"smtp":{"host":"","port":587,"from":"","tls":"starttls","user":""},
	"registration":"open","email_verification":false,"guests":true,"guest_voice":false}`
	if code, m := do(t, c, "PUT", base+"answers", `{"answers":`+ans+`,"secrets":{"admin_password":"short"},"section":"admin"}`); code != 422 {
		t.Fatalf("short password: %d %v", code, m)
	}
	if code, m := do(t, c, "PUT", base+"answers", `{"answers":`+ans+`,"secrets":{"admin_password":"long enough password"},"section":"admin"}`); code != 200 {
		t.Fatalf("answers: %d %v", code, m)
	}
	// DNS elsewhere: the check says so, and install refuses.
	code, m := do(t, c, "POST", base+"check/dns", "{}")
	if code != 200 || m["ok"] != false {
		t.Fatalf("dns: %v", m)
	}
	if code, m := do(t, c, "POST", base+"install", "{}"); code != 409 || m["code"] != "dns" {
		t.Fatalf("install with bad dns: %d %v", code, m)
	}
	ok := strings.Replace(ans, "x.elsewhere.test", "x.test", 1)
	do(t, c, "PUT", base+"answers", `{"answers":`+ok+`,"secrets":{},"section":"domain"}`)
	code, m = do(t, c, "GET", base+"review", "")
	if code != 200 {
		t.Fatalf("review: %d %v", code, m)
	}
	env := m["env"].(string)
	if !strings.Contains(env, "POSTGRES_PASSWORD=••••••••") || !strings.Contains(env, "AUTH_HOST=auth.x.test") {
		t.Fatal(env)
	}
	if code, m := do(t, c, "POST", base+"install", "{}"); code != 202 {
		t.Fatalf("install: %d %v", code, m)
	}
	for i := 0; i < 300; i++ {
		_, m = do(t, c, "GET", base+"install", "")
		if m["running"] == false {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := m["install"].(map[string]any)["status"]; st != "done" {
		t.Fatalf("install: %v", m)
	}
	// The review's masked .env never had a real value in it.
	real, _ := os.ReadFile(filepath.Join(dir, ".env"))
	for _, line := range strings.Split(string(real), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.Contains(k, "PASSWORD") && len(v) > 8 && strings.Contains(env, v) {
			t.Fatalf("review leaked %s", k)
		}
	}
	if fc.Users["alice"] == "" {
		t.Fatal("no admin")
	}
	code, m = do(t, c, "GET", base+"done", "")
	if code != 200 || len(m["oper_password"].(string)) != 20 {
		t.Fatalf("done: %d %v", code, m)
	}
	// finish closes the gate (Finish is nil in this setup; close by hand).
	if code, _ := do(t, c, "POST", base+"finish", "{}"); code != 200 {
		t.Fatal("finish")
	}
	g.Close()
	if code, m := do(t, c, "GET", base+"state", ""); code != 410 || m["code"] != "closed" {
		t.Fatalf("after close: %d %v", code, m)
	}
}
