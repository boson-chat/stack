// Package geoip looks up an IP's country in an offline mmdb (DB-IP Lite
// country, CC BY 4.0: "IP Geolocation by DB-IP", https://db-ip.com) and
// keeps that file fresh: downloaded on first start, refreshed monthly.
package geoip

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// Unknown is the country when there's no database or no match.
const Unknown = "ZZ"

// DefaultURL is DB-IP's monthly Lite country download; %s is YYYY-MM.
const DefaultURL = "https://download.db-ip.com/free/dbip-country-lite-%s.mmdb.gz"

var isoRe = regexp.MustCompile(`^[A-Z]{2}$`)

// DB is a hot-swappable country database.
type DB struct {
	mu     sync.RWMutex
	reader *maxminddb.Reader
}

type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

// Open loads path; a missing file gives an empty DB (everything Unknown).
func Open(path string) (*DB, error) {
	d := &DB{}
	if _, err := os.Stat(path); err != nil {
		return d, nil
	}
	return d, d.Load(path)
}

// Load swaps in the database at path.
func (d *DB) Load(path string) error {
	r, err := maxminddb.Open(path)
	if err != nil {
		return err
	}
	d.mu.Lock()
	old := d.reader
	d.reader = r
	d.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

// Loaded reports whether a database is loaded.
func (d *DB) Loaded() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.reader != nil
}

// Country is ip's ISO 3166-1 alpha-2 code, or Unknown.
func (d *DB) Country(ip netip.Addr) string {
	if !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return Unknown
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.reader == nil {
		return Unknown
	}
	var rec record
	if err := d.reader.Lookup(net.IP(ip.Unmap().AsSlice()), &rec); err != nil {
		return Unknown
	}
	c := strings.ToUpper(rec.Country.ISOCode)
	if !isoRe.MatchString(c) {
		return Unknown
	}
	return c
}

// Updater keeps the file at Path fresh from URL (a template with %s for
// YYYY-MM; the previous month is tried when this month's isn't out yet).
type Updater struct {
	DB     *DB
	Path   string
	URL    string
	MaxAge time.Duration
	Client *http.Client
	Now    func() time.Time
	Log    *slog.Logger
}

// Run checks once now and then daily until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	for {
		if err := u.Check(ctx); err != nil {
			u.Log.Warn("geoip: update failed; countries are ZZ until it works", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// Check downloads the database if it's missing or older than MaxAge.
func (u *Updater) Check(ctx context.Context) error {
	now := u.Now()
	if st, err := os.Stat(u.Path); err == nil && now.Sub(st.ModTime()) < u.MaxAge {
		if !u.DB.Loaded() {
			return u.DB.Load(u.Path)
		}
		return nil
	}
	var last error
	for _, m := range []time.Time{now, now.AddDate(0, -1, 0)} {
		url := u.URL
		if strings.Contains(url, "%s") {
			url = fmt.Sprintf(url, m.UTC().Format("2006-01"))
		}
		if last = u.fetch(ctx, url); last == nil {
			u.Log.Info("geoip: database updated", "month", m.UTC().Format("2006-01"))
			return nil
		}
	}
	// An old file is better than none.
	if _, err := os.Stat(u.Path); err == nil && !u.DB.Loaded() {
		_ = u.DB.Load(u.Path)
	}
	return last
}

func (u *Updater) fetch(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "boson-telemetryd")
	resp, err := u.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	var body io.Reader = resp.Body
	if strings.HasSuffix(url, ".gz") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer gz.Close()
		body = gz
	}
	tmp, err := os.CreateTemp(filepath.Dir(u.Path), ".geoip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(body, 256<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Only a file that opens replaces the old one.
	r, err := maxminddb.Open(tmp.Name())
	if err != nil {
		return fmt.Errorf("not a country database: %w", err)
	}
	r.Close()
	if err := os.Rename(tmp.Name(), u.Path); err != nil {
		return err
	}
	return u.DB.Load(u.Path)
}
