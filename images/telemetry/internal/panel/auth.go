// Package panel is the owner's private web panel (STATS_HOST): one user
// from .env (STATS_USERNAME, bcrypt STATS_PASSWORD_HASH), a server-side
// session behind an HttpOnly, Secure, SameSite=Strict cookie, a login rate
// limit, and JSON endpoints the embedded Svelte app reads.
package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/boson-chat/stack/images/telemetry/internal/ingest"
)

// Login limits.
const (
	CookieName      = "stats_session"
	SessionTTL      = 7 * 24 * time.Hour
	LoginFailures   = 5
	LoginFailWindow = 15 * time.Minute
)

// a dummy hash so an unknown username costs the same as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy password"), bcrypt.DefaultCost)

type failures struct {
	mu sync.Mutex
	m  map[netip.Addr][]time.Time
}

func (f *failures) blocked(ip netip.Addr, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	keep := f.m[ip][:0]
	for _, t := range f.m[ip] {
		if now.Sub(t) < LoginFailWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(f.m, ip)
		return false
	}
	f.m[ip] = keep
	return len(keep) >= LoginFailures
}

func (f *failures) add(ip netip.Addr, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[ip] = append(f.m[ip], now)
}

func (p *Panel) clientIP(r *http.Request) netip.Addr { return ingest.ClientIP(r, p.Proxies) }

func (p *Panel) login(w http.ResponseWriter, r *http.Request) {
	ip := p.clientIP(r)
	now := p.Now()
	if p.fails.blocked(ip, now) {
		jsonErr(w, http.StatusTooManyRequests, "Too many failed logins. Try again in 15 minutes.")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad request")
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(in.Username), []byte(p.Username)) == 1
	hash := p.PasswordHash
	if !userOK || len(hash) == 0 {
		hash = dummyHash
	}
	passOK := bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) == nil
	if !userOK || !passOK || len(p.PasswordHash) == 0 {
		p.fails.add(ip, now)
		jsonErr(w, http.StatusUnauthorized, "Wrong username or password.")
		return
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := p.Store.NewSession(r.Context(), token, now.Add(SessionTTL)); err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: now.Add(SessionTTL), MaxAge: int(SessionTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, map[string]any{"ok": true, "username": p.Username})
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		_ = p.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, map[string]any{"ok": true})
}

func (p *Panel) authed(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && c.Value != "" && p.Store.ValidSession(r.Context(), c.Value, p.Now())
}

// guard: every /api/ call but login needs a session; state-changing calls
// also need the X-Stats header (a page on another site can't set it
// without a CORS preflight, which this never answers).
func (p *Panel) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Stats") != "1" {
			jsonErr(w, http.StatusForbidden, "missing X-Stats header")
			return
		}
		if !p.authed(r) {
			jsonErr(w, http.StatusUnauthorized, "sign in")
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
