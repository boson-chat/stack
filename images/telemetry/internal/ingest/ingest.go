// Package ingest is the public side of telemetryd (TELEMETRY_HOST):
// POST /v1/report, POST /v1/app and GET /healthz. Nothing else answers.
// The request's IP is used for the per-IP limit (as a keyed hash, in
// memory) and the country lookup, then dropped.
package ingest

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
)

// Limits.
const (
	PerIPPerHour = 60
	PerIPBurst   = 20
	PerIDPerDay  = 48
)

// Geo is the country lookup.
type Geo interface {
	Country(netip.Addr) string
}

// Handler serves the ingest endpoints.
type Handler struct {
	Store   *store.Store
	Geo     Geo
	Proxies []netip.Prefix // whose X-Forwarded-For is believed
	Now     func() time.Time
	Log     *slog.Logger

	once  sync.Once
	key   []byte
	mu    sync.Mutex
	ips   map[string]*bucket
	ids   map[string]*window
	sweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

type window struct {
	n     int
	start time.Time
}

func (h *Handler) init() {
	h.once.Do(func() {
		h.key = make([]byte, 32)
		_, _ = rand.Read(h.key)
		h.ips = map[string]*bucket{}
		h.ids = map[string]*window{}
	})
}

// ServeHTTP routes.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.init()
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.Store.Ping(ctx); err != nil {
			http.Error(w, "db", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	case "/v1/report":
		h.accept(w, r, schema.MaxReportBytes, false)
	case "/v1/app":
		// The web app posts from any origin: simple CORS, no credentials.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "POST")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.accept(w, r, schema.MaxAppBytes, true)
	default:
		http.NotFound(w, r)
	}
}

func reply(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, code int, kind string, extra ...string) {
	b := map[string]any{"ok": false, "error": kind}
	if len(extra) == 2 {
		b[extra[0]] = extra[1]
	}
	reply(w, code, b)
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request, max int64, app bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(w, http.StatusMethodNotAllowed, "method")
		return
	}
	ip := ClientIP(r, h.Proxies)
	now := h.Now()
	if !h.allowIP(ip, now) {
		fail(w, http.StatusTooManyRequests, "rate_limited")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "content_type")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			fail(w, http.StatusRequestEntityTooLarge, "too_large")
			return
		}
		fail(w, http.StatusBadRequest, "invalid", "field", "$")
		return
	}
	var id string
	var put func(country string) (store.Outcome, error)
	if app {
		a, err := schema.ParseApp(body)
		if err != nil {
			invalid(w, err)
			return
		}
		id = a.DeviceID
		put = func(c string) (store.Outcome, error) { return h.Store.PutApp(r.Context(), a, c, now) }
	} else {
		rep, err := schema.ParseReport(body)
		if err != nil {
			invalid(w, err)
			return
		}
		id = rep.InstallID
		put = func(c string) (store.Outcome, error) { return h.Store.PutReport(r.Context(), rep, c, now) }
	}
	// Past PerIDPerDay accepted reports an ID is dropped silently.
	if !h.allowID(id, now) {
		reply(w, http.StatusAccepted, map[string]any{"ok": true})
		return
	}
	country := h.Geo.Country(ip)
	ip = netip.Addr{} // dropped: from here on only the country exists
	_ = ip
	if _, err := put(country); err != nil {
		h.Log.Error("store failed", "err", err)
		fail(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	reply(w, http.StatusAccepted, map[string]any{"ok": true})
}

func invalid(w http.ResponseWriter, err error) {
	field := "$"
	var se *schema.Error
	if errors.As(err, &se) {
		field = se.Field
	}
	if len(field) > 80 {
		field = field[:80]
	}
	fail(w, http.StatusBadRequest, "invalid", "field", field)
}

func (h *Handler) hashIP(ip netip.Addr) string {
	m := hmac.New(sha256.New, h.key)
	m.Write(ip.AsSlice())
	return string(m.Sum(nil))
}

// allowIP is a token bucket per IP: PerIPBurst at once, PerIPPerHour an hour.
func (h *Handler) allowIP(ip netip.Addr, now time.Time) bool {
	k := h.hashIP(ip)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gc(now)
	b, ok := h.ips[k]
	if !ok {
		b = &bucket{tokens: PerIPBurst, at: now}
		h.ips[k] = b
	}
	b.tokens = min(PerIPBurst, b.tokens+now.Sub(b.at).Hours()*PerIPPerHour)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (h *Handler) allowID(id string, now time.Time) bool {
	k := store.HashID(id)
	h.mu.Lock()
	defer h.mu.Unlock()
	win, ok := h.ids[k]
	if !ok || now.Sub(win.start) >= 24*time.Hour {
		win = &window{start: now}
		h.ids[k] = win
	}
	win.n++
	return win.n <= PerIDPerDay
}

// gc drops IP buckets idle for an hour (full again anyway) and ID windows
// past their day, at most once a minute.
func (h *Handler) gc(now time.Time) {
	if now.Sub(h.sweep) < time.Minute {
		return
	}
	h.sweep = now
	for k, b := range h.ips {
		if now.Sub(b.at) >= time.Hour {
			delete(h.ips, k)
		}
	}
	for k, w := range h.ids {
		if now.Sub(w.start) >= 24*time.Hour {
			delete(h.ids, k)
		}
	}
}

// ClientIP is the request's address, or, from a trusted proxy, the last
// X-Forwarded-For hop that isn't one.
func ClientIP(r *http.Request, proxies []netip.Prefix) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := ap.Addr().Unmap()
	if !trusted(ip, proxies) {
		return ip
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return ip
		}
		a = a.Unmap()
		if !trusted(a, proxies) {
			return a
		}
		ip = a
	}
	return ip
}

func trusted(ip netip.Addr, proxies []netip.Prefix) bool {
	for _, p := range proxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseProxies parses a comma-separated list of IPs/CIDRs.
func ParseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, err
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
