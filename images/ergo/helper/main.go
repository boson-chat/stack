// boson-ergo is the glue between Ergo and the boson stack:
//
//	boson-ergo render          write /ircd/ircd.yaml and /ircd/ergo.motd from the environment
//	                           and the auth service's channel_creation setting
//	boson-ergo watch-settings  poll channel_creation; on a change re-render and SIGHUP Ergo
//	boson-ergo auth-script     Ergo accounts.auth-script: checks SASL credentials against the auth service
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
		fmt.Fprintln(os.Stderr, "usage: boson-ergo render | watch-settings | auth-script")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "render":
		dir := envOr("ERGO_DIR", "/ircd")
		logf := log.New(os.Stderr, "boson-ergo render: ", 0).Printf
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		policy := resolvePolicy(ctx, os.Getenv, dir, newSettingsClient(os.Getenv), logf)
		cancel()
		if _, err := render(os.Getenv, envOr("ERGO_CONFIG_TEMPLATE", ""), dir, policy); err != nil {
			fmt.Fprintln(os.Stderr, "boson-ergo render:", err)
			os.Exit(1)
		}
		logf("channel_creation=%s", policy)
	case "watch-settings":
		interval := 30 * time.Second
		if n, err := strconv.Atoi(os.Getenv("SETTINGS_POLL_SECONDS")); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
		}
		pid := 1
		if n, err := strconv.Atoi(os.Getenv("ERGO_PID")); err == nil && n > 0 {
			pid = n
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		watchSettings(ctx, os.Getenv, envOr("ERGO_CONFIG_TEMPLATE", ""), envOr("ERGO_DIR", "/ircd"), interval,
			newSettingsClient(os.Getenv), sighup(pid), log.New(os.Stderr, "boson-ergo watch-settings: ", 0).Printf)
	case "auth-script":
		// Ergo reads exactly one JSON line from stdout; never exit non-zero
		// without printing one.
		os.Exit(authScriptMain(os.Stdin, os.Stdout, newAuthClient(os.Getenv)))
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		os.Exit(2)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
