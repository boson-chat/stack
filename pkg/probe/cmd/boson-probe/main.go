// Command boson-probe runs the setup probe against an IRC server and prints
// the report (or just the capability table) as JSON.
//
//	boson-probe -addr irc.example.net:6697 -tls [-insecure] \
//	  [-rpc-url http://services:8080/jsonrpc -rpc-token T -rpc-account A -rpc-password P] \
//	  [-webirc-password W] [-conn-limit 8] [-table] [-expect webirc=native ...]
//
// Secrets can come from the environment instead of flags (PROBE_RPC_TOKEN,
// PROBE_RPC_PASSWORD, PROBE_WEBIRC_PASSWORD). -expect feature=status fails
// (exit 1) when the table disagrees; -expect max-per-ip=N checks the measured
// per-IP limit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/boson-chat/stack/pkg/probe"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var o probe.Options
	var expects multi
	table := flag.Bool("table", false, "print only the capability table")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall timeout")
	flag.StringVar(&o.Addr, "addr", "", "IRC server host:port")
	flag.BoolVar(&o.TLS, "tls", false, "connect with TLS")
	flag.BoolVar(&o.InsecureSkipVerify, "insecure", false, "accept any TLS certificate")
	flag.StringVar(&o.ServerName, "servername", "", "TLS server name (default: the host)")
	flag.StringVar(&o.Nick, "nick", "", "nick to use (default random)")
	flag.DurationVar(&o.Timeout, "step-timeout", 10*time.Second, "timeout per step")
	flag.StringVar(&o.RPC.URL, "rpc-url", "", "services RPC URL (Anope/Atheme /jsonrpc or /xmlrpc)")
	flag.StringVar(&o.RPC.Token, "rpc-token", os.Getenv("PROBE_RPC_TOKEN"), "Anope RPC bearer token")
	flag.StringVar(&o.RPC.Account, "rpc-account", "", "existing account to check over RPC")
	flag.StringVar(&o.RPC.Password, "rpc-password", os.Getenv("PROBE_RPC_PASSWORD"), "that account's password")
	flag.StringVar(&o.WebIRC.Password, "webirc-password", os.Getenv("PROBE_WEBIRC_PASSWORD"), "WEBIRC password to try")
	flag.StringVar(&o.WebIRC.Gateway, "webirc-gateway", "", "WEBIRC gateway name (default boson-probe)")
	flag.IntVar(&o.ConnLimitTest, "conn-limit", 0, "open up to N connections to measure the per-IP limit (0 = off)")
	flag.BoolVar(&o.Transcript, "transcript", false, "include the IRC transcript in the report")
	flag.Var(&expects, "expect", "feature=status or max-per-ip=N (repeatable)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r, err := probe.Run(ctx, o)
	if r != nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if *table {
			_ = enc.Encode(r.Table)
		} else {
			_ = enc.Encode(r)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	failed := false
	for _, e := range expects {
		k, want, ok := strings.Cut(e, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "-expect %q: want feature=status\n", e)
			os.Exit(2)
		}
		got := ""
		if k == "max-per-ip" {
			got = strconv.Itoa(r.Limits.MaxPerIP)
		} else if c, ok := r.Table.Get(k); ok {
			got = string(c.Status)
		} else {
			got = "(no such feature)"
		}
		if got != want {
			fmt.Fprintf(os.Stderr, "expect %s=%s, got %s\n", k, want, got)
			failed = true
		} else {
			fmt.Fprintf(os.Stderr, "ok %s=%s\n", k, got)
		}
	}
	if failed {
		os.Exit(1)
	}
}
