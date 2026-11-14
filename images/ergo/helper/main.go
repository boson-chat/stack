// boson-ergo is the glue between Ergo and the boson stack:
//
//	boson-ergo render          write /ircd/ircd.yaml and /ircd/ergo.motd from the environment,
//	                           the stack config file (BOSON_CONFIG: network.name, irc:) and
//	                           the auth service's network settings (channel creation, auto-register, expiry)
//	boson-ergo watch-settings  poll the settings; on a change re-render and rehash Ergo
//	boson-ergo watch-settings  also: watch BOSON_CONFIG, re-render + rehash on a change, and
//	                           serve GET /internal/config/status on CONFIG_STATUS_LISTENER (:8098)
//	boson-ergo watch-settings  also: erase the Ergo accounts of users deleted in auth
//	boson-ergo watch-settings  also: re-render the network roles' oper classes, push role changes to Ergo
//	boson-ergo auth-script     Ergo accounts.auth-script: checks SASL credentials against the auth service
//	boson-ergo roleserv        RoleServ, the /msg RoleServ service for network roles (docs/roles.md)
//
// It is a static binary; its only dependencies are gopkg.in/yaml.v3 and
// fsnotify (for the config file).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: boson-ergo render | watch-settings | auth-script | roleserv")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "render":
		dir := envOr("ERGO_DIR", "/ircd")
		logf := log.New(os.Stderr, "boson-ergo render: ", 0).Printf
		get := loadOverlay(logf).get
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		st := resolveSettings(ctx, get, dir, newSettingsClient(get), logf)
		rc := resolveRoles(ctx, get, dir, newRolesClient(get), logf)
		cancel()
		classes, err := classesFor(get, rc)
		if err == nil {
			_, err = render(get, envOr("ERGO_CONFIG_TEMPLATE", ""), dir, st, classes)
		}
		if err == nil {
			// What Ergo starts with: watch-settings keeps the restart-only keys there.
			err = saveRunningConfig(dir, get)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "boson-ergo render:", err)
			os.Exit(1)
		}
		logf("%s, %d role oper class(es)", st, len(classes))
	case "watch-settings":
		pid := 1
		if n, err := strconv.Atoi(os.Getenv("ERGO_PID")); err == nil && n > 0 {
			pid = n
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		logf := log.New(os.Stderr, "boson-ergo watch-settings: ", 0).Printf
		dir := envOr("ERGO_DIR", "/ircd")
		cfg := loadOverlay(logf)
		cfg.pin(loadRunningConfig(dir, cfg.unpinned))
		w := newWatcher(cfg.get, envOr("ERGO_CONFIG_TEMPLATE", ""), dir, secondsFrom(cfg.get, "SETTINGS_POLL_SECONDS", 30), nil, logf)
		w.cfg = cfg
		w.rehash = rehasher(w, sighup(pid))
		cw := newConfigWatcher(os.Getenv("BOSON_CONFIG"), w.loadConfigFile, logf)
		if addr := envOr("CONFIG_STATUS_LISTENER", defaultStatusListener); addr != "off" {
			go serveStatus(ctx, addr, cw, logf)
		}
		go func() {
			_ = cw.reload(ctx) // the file as rendered at start (re-renders only if it changed since)
			cw.run(ctx)
		}()
		w.run(ctx, pollInterval(cfg.get))
	case "auth-script":
		// Ergo reads exactly one JSON line from stdout; never exit non-zero
		// without printing one.
		os.Exit(authScriptMain(os.Stdin, os.Stdout, newAuthClient(os.Getenv)))
	case "roleserv":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runRoleServ(ctx, os.Getenv, log.New(os.Stderr, "roleserv: ", 0).Printf); err != nil {
			fmt.Fprintln(os.Stderr, "roleserv:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		os.Exit(2)
	}
}

func secondsEnv(k string, def int) time.Duration { return secondsFrom(os.Getenv, k, def) }

func secondsFrom(get func(string) string, k string, def int) time.Duration {
	if n, err := strconv.Atoi(get(k)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return time.Duration(def) * time.Second
}

// rehasher prefers Ergo's API (synchronous: grants pushed right after it can
// use the new classes) and falls back to SIGHUP.
func rehasher(w *watcher, sig func() error) func() error {
	return func() error {
		if w.ergo != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := w.ergo.Rehash(ctx)
			if err == nil {
				return nil
			}
			w.logf("rehash via API: %v; sending SIGHUP", err)
		}
		return sig()
	}
}

// loadOverlay: the environment with BOSON_CONFIG's values on top. An
// invalid file is logged and ignored (env only); watch-settings reports it
// on /internal/config/status.
func loadOverlay(logf func(string, ...any)) *overlay {
	vals, _, err := readStackConfig(os.Getenv("BOSON_CONFIG"))
	if err != nil {
		logf("config file %s: %v; using the environment only", os.Getenv("BOSON_CONFIG"), err)
		vals = map[string]string{}
	}
	return newOverlay(os.Getenv, vals)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
