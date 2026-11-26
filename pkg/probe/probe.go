// Package probe connects to an IRC server as a normal client and reports
// what it supports (software, ISUPPORT, CAP LS 302, SASL, services, services
// RPC, WEBIRC, connection limits), then turns that into a capability table:
// for every Boson feature, whether the server provides it natively, whether
// evrd in front of it can (via-bridge), or neither, with an explanation and
// the fix.
//
// It has no dependencies outside the standard library so the setup wizard
// (and anything else) can import it.
package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Options say what to probe and how.
type Options struct {
	// Addr is host:port of the IRC server.
	Addr string
	// TLS connects with TLS. ServerName overrides the SNI / verification
	// name (default: Addr's host). InsecureSkipVerify accepts any
	// certificate (self-signed test servers).
	TLS                bool
	ServerName         string
	InsecureSkipVerify bool
	// CheckCert: connect whatever the certificate, then check it against
	// the system roots and report the outcome (Target.CertTrusted) instead
	// of failing. One connection either way: a failed handshake would leave
	// an unregistered connection on servers that count them per address.
	CheckCert bool

	// Nick is the nick to use (default "bprobe" + 6 random hex digits; a
	// numeric suffix is tried if it's taken).
	Nick string

	// Timeout bounds each step (registration, a reply); default 10s.
	// The whole probe is bounded by ctx.
	Timeout time.Duration

	// Dialer replaces net.Dialer (tests, proxies).
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

	// RPC, when URL is set, checks the services RPC endpoint.
	RPC RPCOptions

	// WebIRC, when Password is set, tries a WEBIRC on a second connection.
	WebIRC WebIRCOptions

	// ConnLimitTest, when > 0, opens up to that many simultaneous extra
	// connections to find the per-IP limit (Report.Limits). Off by default:
	// it briefly uses up the probe address's connection slots.
	ConnLimitTest int

	// Transcript keeps the main connection's lines in the report.
	Transcript bool
}

// RPCOptions describe the services RPC endpoint to check.
type RPCOptions struct {
	// URL is the endpoint: Anope 2.1's jsonrpc (http://host:port/jsonrpc)
	// or xmlrpc, Atheme's /jsonrpc or /xmlrpc. A path ending in "/xmlrpc"
	// means XML-RPC, anything else JSON-RPC.
	URL string
	// Token is Anope's bearer token as written in its jsonrpc/xmlrpc
	// token { token = "..." } block; it's sent base64-encoded, which is
	// what Anope 2.1 expects.
	Token string
	// Account and Password, when set, are checked with
	// anope.checkCredentials or atheme.login.
	Account  string
	Password string
	// HTTPClient replaces http.DefaultClient's settings (timeout: Options.Timeout).
	HTTPClient *http.Client
}

// WebIRCOptions describe the WEBIRC attempt.
type WebIRCOptions struct {
	Password string
	// Gateway is the gateway name sent (default "boson-probe").
	Gateway string
	// IP is the client IP to claim (default 192.0.2.44, TEST-NET-1); it's
	// also sent as the hostname.
	IP string
}

func (o *Options) stepTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 10 * time.Second
}

// Run probes the server. It returns an error only when the IRC server
// can't be reached or won't register a client; everything else ends up in
// the report (Warnings, the RPC and WEBIRC results).
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Addr == "" {
		return nil, errors.New("probe: no address")
	}
	if _, _, err := net.SplitHostPort(opts.Addr); err != nil {
		return nil, fmt.Errorf("probe: address %q: %w", opts.Addr, err)
	}
	if opts.Nick == "" {
		opts.Nick = "bprobe" + randHex(3)
	}
	r := &Report{
		Schema:   SchemaVersion,
		Target:   Target{Addr: opts.Addr, TLS: opts.TLS},
		ISupport: map[string]string{},
		Caps:     map[string]string{},
	}

	c, err := dial(ctx, &opts)
	if err != nil {
		return nil, fmt.Errorf("probe: connect to %s: %w", opts.Addr, err)
	}
	defer c.close()
	r.Target.CertSHA256, r.Target.SPKISHA256 = c.certSHA256, c.spkiSHA256
	r.Target.CertTrusted, r.Target.CertError = c.certTrusted, c.certError
	if err := register(c, &opts, r); err != nil {
		if opts.Transcript {
			r.Transcript = c.transcriptLines()
		}
		return r, fmt.Errorf("probe: register on %s: %w", opts.Addr, err)
	}
	queryVersion(c, &opts, r)
	queryOwnAddress(c, &opts, r)
	queryServices(c, &opts, r)
	c.quit()
	if opts.Transcript {
		r.Transcript = c.transcriptLines()
	}

	if opts.RPC.URL != "" {
		r.Services.RPC = checkRPC(ctx, &opts, r.Services.Package)
	}
	if opts.WebIRC.Password != "" {
		r.WebIRC = tryWebIRC(ctx, &opts)
	} else {
		r.WebIRC.Detail = "not tested: no WEBIRC password given"
	}
	r.Limits = limits(ctx, &opts, r)
	r.Table = Evaluate(r)
	return r, nil
}

// register does CAP LS 302 + NICK/USER and reads up to the end of the MOTD.
func register(c *conn, o *Options, r *Report) error {
	nick := o.Nick
	c.send("CAP", "LS", "302")
	c.send("NICK", nick)
	c.send("USER", "bosonprobe", "0", "*", "Boson setup probe")
	capDone := false
	registered := false
	tries := 0
	t := o.stepTimeout()
	err := c.until(3*t, func(m message) bool {
		switch m.Command {
		case "CAP":
			switch strings.ToUpper(m.param(1)) {
			case "LS":
				r.CapSupported = true
				// "CAP * LS * :caps" continues, "CAP * LS :caps" is the last.
				if m.param(2) == "*" && len(m.Params) >= 4 {
					parseCapList(m.last(), r.Caps)
				} else {
					parseCapList(m.last(), r.Caps)
					if !capDone {
						capDone = true
						c.send("CAP", "END")
					}
				}
			}
		case "421":
			if strings.EqualFold(m.param(1), "CAP") {
				capDone = true
			}
		case "433", "432":
			if registered {
				return false
			}
			tries++
			if tries > 5 {
				return false
			}
			nick = fmt.Sprintf("%s%d", truncate(o.Nick, 7), tries)
			c.send("NICK", nick)
		case "001":
			registered = true
			r.Server.Name = m.Source
			r.Server.ClientHost = welcomeHost(m.last())
			c.nick = m.param(0)
			if !capDone {
				// The server ignored CAP (no reply at all before 001).
				capDone = true
			}
		case "004":
			r.Server.Name = firstNonEmpty(m.param(1), r.Server.Name)
			r.Server.RawVersion = m.param(2)
		case "005":
			if len(m.Params) > 2 {
				parseISupport(m.Params[1:len(m.Params)-1], r.ISupport)
			}
		case "376", "422":
			return registered
		case "ERROR":
			return true
		}
		return false
	})
	if v, ok := r.Caps["sasl"]; ok {
		if v != "" {
			r.SASLMechanisms = strings.Split(v, ",")
		}
	}
	r.Server.Network = r.ISupport["NETWORK"]
	if !registered {
		if err == nil {
			err = errors.New("the server closed the connection during registration")
		}
		return err
	}
	if err != nil {
		// Registered but no end of MOTD: keep what we have.
		r.Warnings = append(r.Warnings, "no end of MOTD after registration: "+err.Error())
	}
	r.Server.Software, r.Server.Version = identifySoftware(r.Server.RawVersion)
	return nil
}

// queryVersion asks VERSION for the 351 reply (a fuller version than 004
// on some servers) and the 005 lines servers send after it.
func queryVersion(c *conn, o *Options, r *Report) {
	c.send("VERSION")
	err := c.until(o.stepTimeout(), lineFn(func(m message) bool {
		switch m.Command {
		case "351":
			if raw := m.param(1); raw != "" {
				if sw, ver := identifySoftware(raw); sw != "unknown" || r.Server.Software == "unknown" {
					r.Server.RawVersion, r.Server.Software, r.Server.Version = raw, sw, ver
				}
			}
			// Servers send 005 after 351; wait a moment for them.
			return false
		case "005":
			if len(m.Params) > 2 {
				parseISupport(m.Params[1:len(m.Params)-1], r.ISupport)
			}
			return false
		}
		return false
	}).withPing(c, "version"))
	if err != nil {
		r.Warnings = append(r.Warnings, "VERSION: "+err.Error())
	}
	r.Server.Network = r.ISupport["NETWORK"]
}

type lineFn func(message) bool

// withPing makes a step end at the PONG for a PING sent after the command:
// every server answers commands in order, so that PONG means "done".
func (fn lineFn) withPing(c *conn, token string) lineFn {
	tok := "bprobe-" + token
	c.send("PING", tok)
	return func(m message) bool {
		if m.Command == "PONG" && m.last() == tok {
			return true
		}
		return fn(m)
	}
}

// queryServices finds NickServ (WHOIS) and which package it is (VERSION to
// its server; CTCP VERSION as a fallback).
func queryServices(c *conn, o *Options, r *Report) {
	c.send("WHOIS", "NickServ")
	found := false
	err := c.until(o.stepTimeout(), lineFn(func(m message) bool {
		switch m.Command {
		case "311":
			found = true
		case "312":
			r.Services.Server = m.param(2)
		case "401", "402":
			return false
		}
		return false
	}).withPing(c, "whois"))
	if err != nil {
		r.Warnings = append(r.Warnings, "WHOIS NickServ: "+err.Error())
	}
	r.Services.NickServ = found
	if !found {
		r.Services.Server = ""
		return
	}
	r.Services.Package = "unknown"
	if r.Server.Software == "ergo" && (r.Services.Server == "" || r.Services.Server == r.Server.Name) {
		r.Services.Package, r.Services.Version = "ergo", r.Server.Version
		return
	}

	if r.Services.Server != "" && r.Services.Server != r.Server.Name {
		c.send("VERSION", r.Services.Server)
		_ = c.until(o.stepTimeout(), lineFn(func(m message) bool {
			if m.Command == "351" {
				raw := strings.TrimSpace(m.param(1) + " " + m.last())
				if pkg, ver := identifyServices(raw); pkg != "unknown" {
					r.Services.Package, r.Services.Version, r.Services.RawVersion = pkg, ver, raw
				} else if r.Services.RawVersion == "" {
					r.Services.RawVersion = raw
				}
			}
			return false
		}).withPing(c, "svcversion"))
	}
	if r.Services.Package != "unknown" {
		return
	}
	// CTCP VERSION: both Anope and Atheme answer it. The reply comes after
	// the PONG (services are a hop away), so wait for the NOTICE itself.
	c.send("PRIVMSG", "NickServ", "\x01VERSION\x01")
	_ = c.until(o.stepTimeout(), func(m message) bool {
		if m.Command == "NOTICE" && strings.EqualFold(m.nick(), "NickServ") && strings.HasPrefix(m.last(), "\x01VERSION") {
			raw := strings.Trim(strings.TrimPrefix(m.last(), "\x01VERSION"), "\x01 ")
			r.Services.RawVersion = raw
			r.Services.Package, r.Services.Version = identifyServices(raw)
			return true
		}
		return false
	})
}

// limits measures (optionally) and explains connection limits.
func limits(ctx context.Context, o *Options, r *Report) LimitsInfo {
	l := LimitsInfo{ISupport: map[string]string{}}
	for _, k := range []string{"CHANLIMIT", "MAXCHANNELS", "MAXTARGETS", "TARGMAX", "NICKLEN", "MAXNICKLEN", "MONITOR", "WATCH", "SILENCE"} {
		if v, ok := r.ISupport[k]; ok {
			l.ISupport[k] = v
		}
	}
	if len(l.ISupport) == 0 {
		l.ISupport = nil
	}
	l.Hint = limitHint(r.Server.Software)
	if o.ConnLimitTest > 0 {
		l.Tested = o.ConnLimitTest
		l.MaxPerIP, l.Refusal = measurePerIP(ctx, o)
	}
	return l
}

// measurePerIP opens connections one after another, keeping each open,
// until one is refused or n are open. It returns how many registered
// before the refusal (0 if none was refused) and the refusal text.
func measurePerIP(ctx context.Context, o *Options) (int, string) {
	var open []*conn
	defer func() {
		for _, c := range open {
			c.quit()
		}
	}()
	for i := 0; i < o.ConnLimitTest; i++ {
		c, err := dial(ctx, o)
		if err != nil {
			var pr *plaintextRefusal
			if errors.As(err, &pr) {
				return len(open), pr.line
			}
			if errors.Is(err, io.EOF) {
				return len(open), "the server closed the connection during the TLS handshake"
			}
			return len(open), "connect: " + err.Error()
		}
		nick := fmt.Sprintf("%s%02d", truncate("blim"+randHex(2), 8), i)
		c.send("NICK", nick)
		c.send("USER", "bosonprobe", "0", "*", "Boson setup probe (limit test)")
		ok, why := false, ""
		err = c.until(o.stepTimeout(), func(m message) bool {
			switch m.Command {
			case "001":
				ok = true
				return true
			case "ERROR":
				why = m.last()
				return true
			case "465", "463":
				why = m.last()
				return true
			}
			return false
		})
		if !ok {
			c.close()
			if why == "" && err != nil {
				why = err.Error()
			}
			// The first refusal counts all open ones (including our main
			// connection, which is closed by now).
			return len(open), why
		}
		open = append(open, c)
	}
	return 0, ""
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// welcomeHost is the host in 001's "... nick!user@host" ("" when there's none).
func welcomeHost(text string) string {
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	last := f[len(f)-1]
	at := strings.LastIndexByte(last, '@')
	if at < 0 || !strings.Contains(last[:at], "!") {
		return ""
	}
	return strings.TrimSpace(last[at+1:])
}

// queryOwnAddress asks WHOIS about ourselves for the address the server
// sees (378 "is connecting from *@host IP", 338): 001 often has a host name
// there, which can't be resolved back reliably (a name the server's
// resolver made up for a private address, say).
func queryOwnAddress(c *conn, o *Options, r *Report) {
	if ip := net.ParseIP(r.Server.ClientHost); ip != nil {
		r.Server.ClientIP = ip.String()
	}
	if c.nick == "" {
		return
	}
	c.send("WHOIS", c.nick)
	_ = c.until(o.stepTimeout(), lineFn(func(m message) bool {
		switch m.Command {
		case "378", "338":
			for _, f := range append(strings.Fields(m.last()), m.Params...) {
				if ip := net.ParseIP(strings.TrimPrefix(f, "*@")); ip != nil && r.Server.ClientIP == "" {
					r.Server.ClientIP = ip.String()
				}
			}
		case "311":
			if ip := net.ParseIP(m.param(3)); ip != nil && r.Server.ClientIP == "" {
				r.Server.ClientIP = ip.String()
			}
		}
		return false
	}).withPing(c, "whoisself"))
}
