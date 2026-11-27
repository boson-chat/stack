package geoip_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/geoip"
	"github.com/boson-chat/stack/images/telemetry/internal/testutil"
)

func TestLookup(t *testing.T) {
	db, err := geoip.Open(testutil.CountryDB(t, map[string]string{"81.2.69.0/24": "GB", "2001:db8::/32": "de"}))
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]string{"81.2.69.160": "GB", "2001:db8::1": "DE", "8.8.8.8": "ZZ", "10.0.0.1": "ZZ", "::ffff:81.2.69.1": "GB"} {
		if got := db.Country(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s: %s, want %s", ip, got, want)
		}
	}
	empty, _ := geoip.Open(filepath.Join(t.TempDir(), "missing.mmdb"))
	if empty.Country(netip.MustParseAddr("81.2.69.160")) != "ZZ" {
		t.Error("no database should give ZZ")
	}
}

func TestUpdater(t *testing.T) {
	src, _ := os.ReadFile(testutil.CountryDB(t, map[string]string{"81.2.69.0/24": "GB"}))
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(src)
	zw.Close()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if strings.Contains(r.URL.Path, "2026-11") { // this month's isn't out yet
			http.NotFound(w, r)
			return
		}
		w.Write(gz.Bytes())
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "country.mmdb")
	db, _ := geoip.Open(path)
	u := &geoip.Updater{DB: db, Path: path, URL: srv.URL + "/dbip-country-lite-%s.mmdb.gz", MaxAge: 35 * 24 * time.Hour,
		Client: srv.Client(), Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.Country(netip.MustParseAddr("81.2.69.1")) != "GB" {
		t.Error("not loaded after download")
	}
	if len(asked) != 2 || !strings.Contains(asked[1], "2026-10") {
		t.Errorf("asked %v", asked)
	}
	// Fresh: no download.
	asked = nil
	u.Now = time.Now
	if err := u.Check(context.Background()); err != nil || len(asked) != 0 {
		t.Errorf("fresh file re-downloaded: %v %v", asked, err)
	}
	// A broken download never replaces a good file.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not gzip")) })
	u.Now = func() time.Time { return time.Now().Add(60 * 24 * time.Hour) }
	if err := u.Check(context.Background()); err == nil {
		t.Error("garbage accepted")
	}
	if db.Country(netip.MustParseAddr("81.2.69.1")) != "GB" {
		t.Error("good database lost")
	}
}
