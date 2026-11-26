// Package addon is the "Add Boson to my existing IRC server" flow: their
// IRC server and services, the probe's capability table (what Boson users
// get natively, through the bridge, or not at all), the bridge explained,
// the account mode, then the fresh flow's domain, TLS, admin, features and
// ports steps, the config snippets for their server (with a Re-check that
// probes again until the needed rows are green), review, install, done.
//
// The pages are web/flows/addon.js; it reuses fresh.js's steps. The API is
// fresh's (same handlers, this flow's ID and stack.ModeAddon answers) plus
// probe and snippets. The probe is github.com/boson-chat/stack/pkg/probe.
package addon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/flow/fresh"
	"github.com/boson-chat/stack/images/setup/internal/stack"
	"github.com/boson-chat/stack/pkg/probe"
)

const ID = "addon"

// ConnLimitTest: connections the probe opens to check the bridge's
// address isn't limited (more than any usual per-address limit).
const ConnLimitTest = 8

// Runner runs the probe (tests replace it).
type Runner func(ctx context.Context, o probe.Options) (*probe.Report, error)

type Flow struct {
	*fresh.Flow
	Probe  Runner
	HostIP func() net.IP

	mu   sync.Mutex
	last *Result
	// The test account typed on the server step, for Re-check (memory only).
	testAccount, testPassword string
}

func New(d fresh.Deps) (*Flow, error) {
	f, err := fresh.NewFlow(d, fresh.Config{ID: ID, Mode: stack.ModeAddon, Info: flow.Info{
		ID:        ID,
		Title:     "Add Boson to my existing IRC server",
		Summary:   "Keep your IRC server (UnrealIRCd, InspIRCd, ngIRCd). Boson adds accounts, voice, video, uploads, the web client and the admin panel next to it, and a bridge that gives Boson apps what your server lacks.",
		Available: true,
	}})
	if err != nil {
		return nil, err
	}
	return &Flow{Flow: f, Probe: probe.Run, HostIP: checks.PrimaryIP}, nil
}

func (f *Flow) Register(mux *http.ServeMux, p string) {
	f.Flow.Register(mux, p)
	mux.HandleFunc("POST "+p+"probe", f.probe)
	mux.HandleFunc("GET "+p+"probe", f.lastProbe)
	mux.HandleFunc("GET "+p+"snippets", f.snippets)
}

// Check is one thing their server must do for the bridge.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Fix: what to do (usually: the snippet with this ID).
	Fix string `json:"fix,omitempty"`
}

// Result is what the probe step shows.
type Result struct {
	At     time.Time     `json:"at"`
	Report *probe.Report `json:"report"`
	Table  probe.Table   `json:"table"`
	// Checks are the needed items (Re-check turns them green).
	Checks []Check `json:"checks"`
	AllOK  bool    `json:"all_ok"`
	// Recommended account mode (auth's decision order) and why.
	Recommended struct {
		Accounts string `json:"accounts"`
		Strategy string `json:"strategy"`
		Why      string `json:"why"`
	} `json:"recommended"`
	NickServSupported bool `json:"nickserv_supported"`
	// RPCError: the services RPC didn't take the URL or token (clear text).
	RPCError string `json:"rpc_error,omitempty"`
	// TLSUnverified: the certificate isn't trusted (self-signed): pinned.
	TLSUnverified bool `json:"tls_unverified"`
}

type probeReq struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls"`
	// RPCURL / RPCToken: the services RPC (token empty: keep the one typed
	// before).
	RPCURL   string `json:"rpc_url"`
	RPCToken string `json:"rpc_token"`
	// Account / Password: an existing NickServ account to try over RPC
	// (never stored).
	Account  string `json:"account"`
	Password string `json:"password"`
	// Recheck: after the snippets; keeps what was detected (source IP).
	Recheck bool `json:"recheck"`
}

func (f *Flow) probe(w http.ResponseWriter, r *http.Request) {
	var req probeReq
	if err := flow.Decode(r, &req); err != nil {
		flow.Error(w, http.StatusBadRequest, "bad_request", "Invalid request.")
		return
	}
	a := f.Answers()
	if req.Host == "" {
		req.Host, req.Port, req.TLS, req.RPCURL = a.Addon.Host, a.Addon.Port, a.Addon.TLS, a.Addon.RPCURL
	}
	req.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.Host)), ".")
	req.RPCURL = strings.TrimSpace(req.RPCURL)
	if req.Port == 0 {
		req.Port = 6697
	}
	check := stack.Answers{Mode: stack.ModeAddon, Addon: stack.AddonAnswers{Host: req.Host, Port: req.Port, RPCURL: req.RPCURL, SourceIP: "192.0.2.1", Accounts: stack.AccountsBoson}}
	var errs []stack.FieldError
	for _, e := range check.Validate("irc", stack.Secrets{RPCToken: req.RPCToken}, false) {
		if e.Field == "addon.host" || e.Field == "addon.port" || e.Field == "addon.rpc_url" || e.Field == "addon.rpc_token" {
			errs = append(errs, e)
		}
	}
	if len(errs) > 0 {
		flow.JSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": errs})
		return
	}
	f.mu.Lock()
	if req.Account != "" {
		f.testAccount, f.testPassword = req.Account, req.Password
	} else if req.Recheck {
		req.Account, req.Password = f.testAccount, f.testPassword
	}
	f.mu.Unlock()
	token := req.RPCToken
	if token == "" && req.RPCURL != "" {
		token = f.RPCToken()
	}
	if req.RPCToken != "" {
		f.SetRPCToken(req.RPCToken)
	}
	webircPW := f.BridgeWebIRCPassword()
	addr := net.JoinHostPort(req.Host, strconv.Itoa(req.Port))
	opts := probe.Options{
		Addr: addr, TLS: req.TLS, Timeout: 8 * time.Second,
		WebIRC:        probe.WebIRCOptions{Password: webircPW, Gateway: "boson"},
		ConnLimitTest: ConnLimitTest,
	}
	if req.RPCURL != "" {
		opts.RPC = probe.RPCOptions{URL: req.RPCURL, Token: token, Account: req.Account, Password: req.Password}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res := &Result{At: time.Now().UTC(), NickServSupported: stack.AuthNickServ}
	// One connection checks the certificate too (see probe.Options.CheckCert).
	opts.CheckCert = req.TLS
	rep, err := f.runProbe(ctx, opts)
	if err == nil && req.TLS && !rep.Target.CertTrusted {
		// Self-signed (or another name): pin what it presents instead.
		res.TLSUnverified = true
	}
	if err != nil {
		flow.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": probeError(err, addr, req.TLS)})
		return
	}
	res.Report, res.Table = rep, rep.Table
	if a.Addon.Host == req.Host && a.Addon.Port == req.Port && a.Addon.TLS && !a.Addon.TLSVerify && a.Addon.CertSHA256 != "" &&
		rep.Target.CertSHA256 != a.Addon.CertSHA256 && req.Recheck {
		flow.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": "The server's certificate changed since the first check (SHA-256 " + rep.Target.CertSHA256 + "). If that's expected, go back to the first step and check the server again to pin the new one."})
		return
	}
	if rpc := rep.Services.RPC; rpc != nil && (!rpc.Reachable || !rpc.Authorized) {
		res.RPCError = rpcError(rpc)
	}
	acc, strat, why := recommend(rep)
	res.Recommended.Accounts, res.Recommended.Strategy, res.Recommended.Why = acc, strat, why
	if !stack.AuthNickServ && acc == stack.AccountsNickServ {
		res.Recommended.Accounts, res.Recommended.Strategy = stack.AccountsBoson, ""
		res.Recommended.Why = why + " This version of Boson's account service can't use NickServ accounts yet, so Boson accounts it is for now."
	}

	a = f.SaveAddon(func(x *stack.Answers) {
		ad := &x.Addon
		changedServer := ad.Host != req.Host || ad.Port != req.Port || ad.TLS != req.TLS
		ad.Host, ad.Port, ad.TLS, ad.RPCURL = req.Host, req.Port, req.TLS, req.RPCURL
		ad.TLSVerify = !res.TLSUnverified
		ad.CertSHA256, ad.SPKISHA256 = rep.Target.CertSHA256, rep.Target.SPKISHA256
		ad.Software = rep.Server.Software
		ad.ServerName = rep.Server.Name
		ad.Services = rep.Services.Package
		if ad.Services == "" {
			ad.Services = "none"
		}
		ad.ServicesServer = rep.Services.Server
		if !req.Recheck || ad.SourceIP == "" || changedServer {
			ad.SourceIP = f.sourceIP(rep)
		}
		if !req.Recheck || changedServer {
			ad.Accounts, ad.Strategy = res.Recommended.Accounts, res.Recommended.Strategy
			if n := rep.Server.Network; n != "" && validNetworkName(n) {
				x.NetworkName = n
			}
		}
	})
	res.Checks = checksFor(a, rep)
	res.AllOK = true
	for _, c := range res.Checks {
		res.AllOK = res.AllOK && c.OK
	}
	f.mu.Lock()
	f.last = res
	f.mu.Unlock()
	flow.JSON(w, http.StatusOK, map[string]any{"ok": true, "result": res, "answers": a})
}

// runProbe runs the probe, once more after a pause when the server hung up
// during registration (a server still counting the last check's
// connections against this address, like UnrealIRCd's unknown-connection
// limit).
func (f *Flow) runProbe(ctx context.Context, o probe.Options) (*probe.Report, error) {
	rep, err := f.Probe(ctx, o)
	if err != nil && strings.Contains(err.Error(), "during registration") {
		select {
		case <-ctx.Done():
			return rep, err
		case <-time.After(5 * time.Second):
		}
		rep, err = f.Probe(ctx, o)
	}
	return rep, err
}

func validNetworkName(n string) bool {
	t := stack.Defaults()
	t.NetworkName = n
	t.Domain = "x.example"
	for _, e := range t.Validate("domain", stack.Secrets{}, false) {
		if e.Field == "network_name" {
			return false
		}
	}
	return true
}

func (f *Flow) lastProbe(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	res := f.last
	f.mu.Unlock()
	if res == nil {
		flow.JSON(w, http.StatusOK, map[string]any{"ok": false})
		return
	}
	a := f.Answers()
	res.Checks = checksFor(a, res.Report)
	res.AllOK = true
	for _, c := range res.Checks {
		res.AllOK = res.AllOK && c.OK
	}
	flow.JSON(w, http.StatusOK, map[string]any{"ok": true, "result": res, "answers": a})
}

func (f *Flow) snippets(w http.ResponseWriter, _ *http.Request) {
	a := f.Answers()
	if a.Addon.Host == "" {
		flow.Error(w, http.StatusConflict, "no_server", "Check your IRC server first.")
		return
	}
	pw := f.BridgeWebIRCPassword()
	f.mu.Lock()
	last := f.last
	f.mu.Unlock()
	var out []stack.Snippet
	for _, s := range stack.Snippets(a, pw) {
		// The services' RPC hint only while it isn't working.
		if s.ID == "services-rpc" && last != nil && last.Report != nil {
			if rpc := last.Report.Services.RPC; rpc != nil && rpc.Reachable && rpc.Authorized {
				continue
			}
		}
		out = append(out, s)
	}
	flow.JSON(w, http.StatusOK, map[string]any{
		"software":  a.Addon.Software,
		"source_ip": a.Addon.SourceIP,
		"snippets":  out,
	})
}

// sourceIP: the address the server says we come from (001), else this
// machine's primary address.
func (f *Flow) sourceIP(rep *probe.Report) string {
	if h := rep.Server.ClientHost; h != "" {
		if ip := net.ParseIP(h); ip != nil {
			return ip.String()
		}
		if ips, err := net.LookupIP(h); err == nil && len(ips) > 0 {
			return ips[0].String()
		}
	}
	if f.HostIP != nil {
		if ip := f.HostIP(); ip != nil {
			return ip.String()
		}
	}
	return ""
}

func probeError(err error, addr string, tls bool) string {
	s := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "i/o timeout"):
		return "No answer from " + addr + " in time. Check the address and port, and that this server may connect to it (firewall)."
	case strings.Contains(s, "connection refused"):
		return addr + " refused the connection: nothing listens there, or a firewall rejects this server."
	case strings.Contains(s, "no such host"):
		return "The name in " + addr + " doesn't resolve."
	case tls && strings.Contains(s, "TLS handshake"):
		return "TLS failed on " + addr + " (is it a plaintext port? untick TLS for that): " + s
	}
	return s
}

func rpcError(rpc *probe.RPCResult) string {
	switch {
	case !rpc.Reachable:
		return "The services RPC at " + rpc.URL + " didn't answer like an RPC endpoint: " + rpc.Error
	case !rpc.Authorized:
		return "The services RPC at " + rpc.URL + " refused the token. Use the token from Anope's jsonrpc token { } block, exactly as written there." + suffix(rpc.Error)
	}
	return ""
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func recommend(rep *probe.Report) (string, string, string) {
	rpcOK, flavor, proto := false, "", ""
	if rpc := rep.Services.RPC; rpc != nil {
		rpcOK, flavor, proto = rpc.Reachable && rpc.Authorized, rpc.Flavor, rpc.Protocol
	}
	_, sasl := rep.Caps["sasl"]
	plain := sasl && (len(rep.SASLMechanisms) == 0 || containsFold(rep.SASLMechanisms, "PLAIN"))
	return stack.RecommendAccounts(flavor, proto, rpcOK, plain, rep.Services.NickServ)
}

func containsFold(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func row(t probe.Table, feature string) probe.Capability {
	for _, c := range t.Features {
		if c.Feature == feature {
			return c
		}
	}
	return probe.Capability{}
}

// checksFor: what their server must do before the install: WEBIRC for
// the bridge's password, no per-address limit for this server, the RPC
// when NickServ passwords are checked through it, +draft/react when asked.
func checksFor(a stack.Answers, rep *probe.Report) []Check {
	if rep == nil {
		return nil
	}
	ad := a.Addon
	var out []Check
	web := Check{ID: "webirc", Title: "WEBIRC for the bridge", OK: rep.WebIRC.Accepted}
	if web.OK {
		web.Detail = "Your server takes WEBIRC with the bridge's password: each Boson user shows with their own address."
	} else {
		web.Detail = "Your server refused WEBIRC with the bridge's password (" + rep.WebIRC.Detail + ")."
		web.Fix = "Add the “WEBIRC for the bridge” snippet below, reload your server, then Re-check."
	}
	out = append(out, web)
	lim := Check{ID: "limits", Title: "Many connections from this server", OK: rep.Limits.Tested > 0 && rep.Limits.MaxPerIP == 0}
	if lim.OK {
		lim.Detail = "Your server took " + strconv.Itoa(rep.Limits.Tested+1) + " connections at once from " + ad.SourceIP + ": every Boson user connects from there."
	} else {
		lim.Detail = "Your server stopped at " + strconv.Itoa(rep.Limits.MaxPerIP) + " connections from " + ad.SourceIP + suffix(rep.Limits.Refusal) + ". Every Boson user connects from this server, so only that many could be online."
		lim.Fix = "Add the connection-limit snippet below (for " + ad.SourceIP + "), reload your server, then Re-check."
	}
	out = append(out, lim)
	if ad.Accounts == stack.AccountsNickServ && strings.HasSuffix(ad.Strategy, "rpc") {
		c := Check{ID: "services-rpc", Title: "Services RPC"}
		if rpc := rep.Services.RPC; rpc != nil && rpc.Reachable && rpc.Authorized {
			c.OK, c.Detail = true, "Boson can check NickServ passwords at "+rpc.URL+"."
		} else {
			c.Detail = "NickServ passwords are checked through the services RPC, and it doesn't answer yet."
			if rpc != nil {
				c.Detail = rpcError(rpc)
			}
			c.Fix = "Enable the RPC (snippet below) or fix its address and token in the first step, then Re-check."
		}
		out = append(out, c)
	}
	if ad.AllowReact {
		c := Check{ID: "reactions", Title: "Reactions (+draft/react)", OK: row(rep.Table, probe.FeatureReactions).Status == probe.Native}
		if c.OK {
			c.Detail = "Your server relays +draft/react: reactions reach everyone."
		} else {
			c.Detail = "Your server still drops +draft/react."
			c.Fix = "Build and load the boson-react module (snippets below), then Re-check. Or turn reactions off: it's optional."
		}
		out = append(out, c)
	}
	return out
}
