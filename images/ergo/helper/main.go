// boson-ergo is the glue between Ergo and the boson stack:
//
//	boson-ergo render          write /ircd/ircd.yaml and /ircd/ergo.motd from the environment
//	                           and the auth service's network settings (channel creation, auto-register, expiry)
//	boson-ergo watch-settings  poll the settings; on a change re-render and rehash Ergo
//	boson-ergo watch-settings  also: erase the Ergo accounts of users deleted in auth
//	boson-ergo watch-settings  also: re-render the network roles' oper classes, push role changes to Ergo
//	boson-ergo auth-script     Ergo accounts.auth-script: checks SASL credentials against the auth service
//	boson-ergo roleserv        RoleServ, the /msg RoleServ service for network roles (docs/roles.md)
//
// It is a static binary with no dependencies beyond the Go standard library.
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		st := resolveSettings(ctx, os.Getenv, dir, newSettingsClient(os.Getenv), logf)
		rc := resolveRoles(ctx, os.Getenv, dir, newRolesClient(os.Getenv), logf)
		cancel()
		classes, err := classesFor(os.Getenv, rc)
		if err == nil {
			_, err = render(os.Getenv, envOr("ERGO_CONFIG_TEMPLATE", ""), dir, st, classes)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "boson-ergo render:", err)
			os.Exit(1)
		}
		logf("%s, %d role oper class(es)", st, len(classes))
	case "watch-settings":
		settingsEvery := secondsEnv("SETTINGS_POLL_SECONDS", 30)
		interval := secondsEnv("ROLES_POLL_SECONDS", 10)
		if !rolesEnabled(os.Getenv) || settingsEvery < interval {
			interval = settingsEvery
		}
		pid := 1
		if n, err := strconv.Atoi(os.Getenv("ERGO_PID")); err == nil && n > 0 {
			pid = n
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		logf := log.New(os.Stderr, "boson-ergo watch-settings: ", 0).Printf
		w := newWatcher(os.Getenv, envOr("ERGO_CONFIG_TEMPLATE", ""), envOr("ERGO_DIR", "/ircd"), settingsEvery, nil, logf)
		w.rehash = rehasher(w, sighup(pid))
		w.run(ctx, interval)
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

func secondsEnv(k string, def int) time.Duration {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
