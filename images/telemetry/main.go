// telemetryd: Boson's anonymous usage and health stats receiver and the
// owner's panel (docs/telemetry.md). Runs only on boson.chat, in the
// stack's telemetry profile.
//
//	telemetryd [serve]        the ingest listener (:8080) and the panel (:8081)
//	telemetryd hash-password  read a password (stdin or a prompt), print its bcrypt hash
//	telemetryd healthcheck    exit 0 when the ingest listener answers /healthz
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/boson-chat/stack/images/telemetry/internal/geoip"
	"github.com/boson-chat/stack/images/telemetry/internal/ingest"
	"github.com/boson-chat/stack/images/telemetry/internal/panel"
	"github.com/boson-chat/stack/images/telemetry/internal/rollup"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/vm"
	"github.com/boson-chat/stack/images/telemetry/web"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			fmt.Fprintln(os.Stderr, "telemetryd:", err)
			os.Exit(1)
		}
	case "hash-password":
		if err := hashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, "telemetryd:", err)
			os.Exit(1)
		}
	case "healthcheck":
		c := http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://127.0.0.1" + env("INGEST_ADDR", ":8080") + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: telemetryd [serve|hash-password|healthcheck]")
		os.Exit(2)
	}
}

func hashPassword() error {
	var pw []byte
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		a, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "Again: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if string(a) != string(b) {
			return errors.New("the passwords don't match")
		}
		pw = a
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no password on stdin")
		}
		pw = []byte(strings.TrimRight(line, "\r\n"))
	}
	if len(pw) < 12 {
		return errors.New("use at least 12 characters")
	}
	h, err := bcrypt.GenerateFromPassword(pw, 12)
	if err != nil {
		return err
	}
	fmt.Println(string(h))
	return nil
}

func serve() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level(env("LOG_LEVEL", "info"))}))
	dataDir := env("TELEMETRY_DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "telemetry.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	proxies, err := ingest.ParseProxies(env("TRUSTED_PROXIES", ""))
	if err != nil {
		return fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	user := env("STATS_USERNAME", "")
	hash := env("STATS_PASSWORD_HASH", "")
	if user == "" || hash == "" {
		log.Warn("STATS_USERNAME / STATS_PASSWORD_HASH not set: nobody can sign in to the panel")
	} else if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return errors.New("STATS_PASSWORD_HASH is not a bcrypt hash (make one with: telemetryd hash-password)")
	}

	geoPath := env("TELEMETRY_GEOIP_FILE", filepath.Join(dataDir, "country.mmdb"))
	geo, err := geoip.Open(geoPath)
	if err != nil {
		log.Warn("geoip: can't open the database; downloading a new one", "err", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	httpc := &http.Client{Timeout: 30 * time.Second}
	if u := env("TELEMETRY_GEOIP_URL", geoip.DefaultURL); u != "off" {
		go (&geoip.Updater{DB: geo, Path: geoPath, URL: u, MaxAge: 35 * 24 * time.Hour, Client: httpc, Now: time.Now, Log: log}).Run(ctx)
	}

	vmc := &vm.Client{URL: strings.TrimRight(env("TELEMETRY_VM_URL", "http://victoriametrics-telemetry:8428"), "/"), HTTP: httpc}
	go (&rollup.Rollup{Store: st, VM: vmc, Now: time.Now, Log: log}).Run(ctx)

	static, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return err
	}
	in := &ingest.Handler{Store: st, Geo: geo, Proxies: proxies, Now: time.Now, Log: log}
	pn := &panel.Panel{Store: st, VM: vmc, Username: user, PasswordHash: []byte(hash), Proxies: proxies,
		Static: static, Now: time.Now, Log: log}

	servers := []*http.Server{
		{Addr: env("INGEST_ADDR", ":8080"), Handler: in, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10},
		{Addr: env("PANEL_ADDR", ":8081"), Handler: pn.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second},
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		log.Info("listening", "addr", s.Addr)
		go func() { errc <- s.ListenAndServe() }()
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return nil
}

func level(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
