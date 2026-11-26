// Package server is the wizard's HTTPS server: static pages, the code →
// session exchange, and the flows' APIs behind the session.
package server

import (
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/gate"
)

const cookieName = "boson_setup"

type Server struct {
	Gate     *gate.Gate
	Flows    *flow.Registry
	Static   fs.FS
	Instance string // random id, answered at /.well-known/boson-setup
	Log      *slog.Logger
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/flows", func(w http.ResponseWriter, _ *http.Request) {
		flow.JSON(w, http.StatusOK, map[string]any{"flows": s.Flows.Infos()})
	})
	for _, f := range s.Flows.All() {
		f.Register(api, "/api/flows/"+f.Info().ID+"/")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/boson-setup", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(s.Instance))
	})
	mux.HandleFunc("POST /api/session", s.session)
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		flow.JSON(w, http.StatusOK, map[string]any{"ok": s.authed(r), "closed": s.Gate.Closed()})
	})
	mux.Handle("/api/", s.requireSession(api))
	mux.Handle("/", http.FileServerFS(s.Static))
	return s.secure(mux)
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && s.Gate.Valid(c.Value)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := flow.Decode(r, &req); err != nil {
		flow.Error(w, http.StatusBadRequest, "bad_request", "Invalid request.")
		return
	}
	ip := clientIP(r)
	tok, err := s.Gate.Redeem(ip, req.Code)
	switch err {
	case nil:
	case gate.ErrRateLimited:
		wait := s.Gate.RetryAfter(ip)
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.Log.Warn("setup code: rate limited", "ip", ip)
		flow.Error(w, http.StatusTooManyRequests, "rate_limited", err.Error())
		return
	case gate.ErrBadCode:
		s.Log.Warn("setup code: wrong code", "ip", ip)
		flow.Error(w, http.StatusUnauthorized, "bad_code", "That code is not right. Check the code install.sh printed.")
		return
	case gate.ErrUsed:
		flow.Error(w, http.StatusForbidden, "used", err.Error())
		return
	default:
		flow.Error(w, http.StatusGone, "closed", err.Error())
		return
	}
	s.Log.Info("setup code redeemed", "ip", ip)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(gate.SessionTTL.Seconds()),
	})
	flow.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			if s.Gate.Closed() {
				flow.Error(w, http.StatusGone, "closed", gate.ErrClosed.Error())
				return
			}
			flow.Error(w, http.StatusUnauthorized, "no_session", "Enter the setup code first.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// secure: headers, a body cap, same-origin writes, an access log without
// bodies or query strings.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					flow.Error(w, http.StatusForbidden, "origin", "Cross-origin request refused.")
					return
				}
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				flow.Error(w, http.StatusUnsupportedMediaType, "content_type", "JSON only.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		sw := &statusWriter{ResponseWriter: w, code: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}
