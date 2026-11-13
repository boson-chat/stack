// boson-ergo is the glue between Ergo and the boson stack:
//
//	boson-ergo render        write /ircd/ircd.yaml and /ircd/ergo.motd from the environment
//	boson-ergo auth-script   Ergo accounts.auth-script: checks SASL credentials against the auth service
//
// It is a static binary with no dependencies beyond the Go standard library.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: boson-ergo render | auth-script")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "render":
		if err := render(os.Getenv, envOr("ERGO_CONFIG_TEMPLATE", ""), envOr("ERGO_DIR", "/ircd")); err != nil {
			fmt.Fprintln(os.Stderr, "boson-ergo render:", err)
			os.Exit(1)
		}
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
