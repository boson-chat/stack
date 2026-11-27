package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/geoip"
	"github.com/boson-chat/stack/images/telemetry/internal/ingest"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/testutil"
)

const id = "0123456789abcdef0123456789abcdef"

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T) (*ingest.Handler, *store.Store, *clock) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	geo, err := geoip.Open(testutil.CountryDB(t, map[string]string{"81.2.69.0/24": "GB", "2001:db8::/32": "DE"}))
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}
	proxies, _ := ingest.ParseProxies("10.203.53.0/24")
	h := &ingest.Handler{Store: st, Geo: geo, Proxies: proxies, Now: c.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return h, st, c
}

func post(h http.Handler, path string, body []byte, remote, xff string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestReportCountedOncePer20h(t *testing.T) {
	h, st, c := setup(t)
	ctx := context.Background()
	body := testutil.JSON(t, testutil.Report(id))
	if w := post(h, "/v1/report", body, "10.203.53.2:5000", "81.2.69.160"); w.Code != 202 {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	// An extra one 2 hours later, after a failed update: accepted, the day row updated, not counted.
	c.t = c.t.Add(2 * time.Hour)
	m := testutil.Report(id)
	m["reason"] = "update"
	m["health"].(map[string]any)["updates"] = map[string]any{"attempts": 1, "failures": 1, "rollbacks": 0, "errors": []string{"pull"}}
	if w := post(h, "/v1/report", testutil.JSON(t, m), "10.203.53.2:5000", "81.2.69.160"); w.Code != 202 {
		t.Fatalf("extra: %d", w.Code)
	}
	rows, _ := st.ReportsSince(ctx, "2026-01-01")
	if len(rows) != 1 || rows[0].Report.Health.Updates.Failures != 1 || rows[0].Country != "GB" {
		t.Fatalf("rows after the extra: %+v", rows)
	}
	// 20 h after the first: counted again, a new day row.
	c.t = c.t.Add(18 * time.Hour)
	post(h, "/v1/report", body, "10.203.53.2:5000", "81.2.69.160")
	rows, _ = st.ReportsSince(ctx, "2026-01-01")
	if len(rows) != 2 {
		t.Fatalf("want 2 day rows, got %d", len(rows))
	}
	ins, _ := st.InstallsSeenSince(ctx, time.Time{})
	if len(ins) != 1 || ins[0].ID != store.HashID(id) || ins[0].Stack != "1.48.0" {
		t.Fatalf("install: %+v", ins)
	}
}

func TestNoIPStored(t *testing.T) {
	h, st, _ := setup(t)
	post(h, "/v1/report", testutil.JSON(t, testutil.Report(id)), "81.2.69.160:443", "")
	post(h, "/v1/app", testutil.JSON(t, testutil.App(id)), "[2001:db8::1]:443", "")
	ins, _ := st.InstallsSeenSince(context.Background(), time.Time{})
	devs, _ := st.DevicesSeenSince(context.Background(), time.Time{})
	if ins[0].Country != "GB" || devs[0].Country != "DE" {
		t.Errorf("countries: %s %s", ins[0].Country, devs[0].Country)
	}
	b, _ := json.Marshal([]any{ins, devs})
	for _, s := range []string{"81.2.69", "2001:db8", id} {
		if strings.Contains(string(b), s) {
			t.Errorf("stored %q", s)
		}
	}
}

func TestValidationAndLimits(t *testing.T) {
	h, _, _ := setup(t)
	bad := testutil.Report(id)
	bad["hostname"] = "chat.example.com"
	w := post(h, "/v1/report", testutil.JSON(t, bad), "81.2.69.1:1", "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"field":"hostname"`) {
		t.Errorf("unknown field: %d %s", w.Code, w.Body)
	}
	big := bytes.Repeat([]byte(" "), 17<<10)
	if w := post(h, "/v1/report", big, "81.2.69.2:1", ""); w.Code != 413 {
		t.Errorf("too large: %d", w.Code)
	}
	if w := post(h, "/v1/app", append(testutil.JSON(t, testutil.App(id)), bytes.Repeat([]byte(" "), 2<<10)...), "81.2.69.2:1", ""); w.Code != 413 {
		t.Errorf("app too large: %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/report", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "text/plain")
	r.RemoteAddr = "81.2.69.3:1"
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != 415 {
		t.Errorf("content type: %d", rw.Code)
	}
	for _, p := range []string{"/", "/api/overview", "/v1/other", "/metrics"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code != 404 {
			t.Errorf("GET %s: %d", p, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/report", nil))
	if rr.Code != 405 {
		t.Errorf("GET /v1/report: %d", rr.Code)
	}
}

func TestPerIPRateLimit(t *testing.T) {
	h, _, c := setup(t)
	body := testutil.JSON(t, testutil.App(id))
	ok := 0
	for i := 0; i < 30; i++ {
		if post(h, "/v1/app", body, "10.203.53.2:1", "81.2.69.9").Code == 202 {
			ok++
		}
	}
	if ok != ingest.PerIPBurst {
		t.Errorf("burst: %d accepted, want %d", ok, ingest.PerIPBurst)
	}
	// Another address isn't affected; a spoofed X-Forwarded-For from outside the proxy range is ignored.
	if post(h, "/v1/app", body, "10.203.53.2:1", "81.2.69.10").Code != 202 {
		t.Error("another IP limited")
	}
	if post(h, "/v1/app", body, "81.2.69.9:1", "81.2.69.77").Code != 429 {
		t.Error("X-Forwarded-For from an untrusted peer was believed")
	}
	c.t = c.t.Add(time.Minute)
	if post(h, "/v1/app", body, "10.203.53.2:1", "81.2.69.9").Code != 202 {
		t.Error("no refill after a minute (60/h)")
	}
}

func TestPerIDDailyCap(t *testing.T) {
	h, _, c := setup(t)
	body := testutil.JSON(t, testutil.Report(id))
	for i := 0; i < ingest.PerIDPerDay+5; i++ {
		c.t = c.t.Add(time.Second)
		ip := netip.AddrFrom4([4]byte{81, 2, 69, byte(i)})
		if w := post(h, "/v1/report", body, ip.String()+":1", ""); w.Code != 202 {
			t.Fatalf("report %d: %d", i, w.Code)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	h, _, _ := setup(t)
	r := httptest.NewRequest(http.MethodOptions, "/v1/app", nil)
	r.Header.Set("Origin", "https://chat.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("preflight: %d %v", w.Code, w.Header())
	}
}

func TestClientIP(t *testing.T) {
	p, _ := ingest.ParseProxies("10.0.0.0/8, 192.0.2.1")
	for _, c := range []struct{ remote, xff, want string }{
		{"203.0.113.5:1", "1.1.1.1", "203.0.113.5"},
		{"10.0.0.2:1", "1.1.1.1", "1.1.1.1"},
		{"10.0.0.2:1", "9.9.9.9, 1.1.1.1, 192.0.2.1", "1.1.1.1"},
		{"10.0.0.2:1", "", "10.0.0.2"},
		{"10.0.0.2:1", "garbage", "10.0.0.2"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ingest.ClientIP(r, p).String(); got != c.want {
			t.Errorf("%s via %q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
