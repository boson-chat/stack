package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseMessage(t *testing.T) {
	m := parseMessage(`@time=2026-01-01T00:00:00Z;+draft/reply=abc\sdef :nick!u@h PRIVMSG #chan :hello there`)
	if m.Command != "PRIVMSG" || m.Source != "nick!u@h" || m.nick() != "nick" || len(m.Params) != 2 || m.last() != "hello there" {
		t.Fatalf("%+v", m)
	}
	if m.Tags["+draft/reply"] != "abc def" || m.Tags["time"] == "" {
		t.Fatalf("tags %v", m.Tags)
	}
	if m := parseMessage("PING :x"); m.Command != "PING" || m.last() != "x" {
		t.Fatalf("%+v", m)
	}
}

func TestISupportAndCaps(t *testing.T) {
	is := map[string]string{}
	parseISupport([]string{"NETWORK=Test\\x20Net", "CHANTYPES=#", "SAFELIST", "-SAFELIST"}, is)
	if is["NETWORK"] != "Test Net" || is["CHANTYPES"] != "#" {
		t.Fatalf("%v", is)
	}
	if _, ok := is["SAFELIST"]; ok {
		t.Fatal("-SAFELIST not removed")
	}
	caps := map[string]string{}
	parseCapList("sasl=PLAIN,EXTERNAL message-tags draft/multiline=max-bytes=4096", caps)
	if caps["sasl"] != "PLAIN,EXTERNAL" || caps["draft/multiline"] != "max-bytes=4096" {
		t.Fatalf("%v", caps)
	}
	if _, ok := caps["message-tags"]; !ok {
		t.Fatal("message-tags missing")
	}
}

func TestIdentify(t *testing.T) {
	for raw, want := range map[string][2]string{
		"UnrealIRCd-6.2.7.1.":  {"unrealircd", "6.2.7.1"},
		"InspIRCd-4":           {"inspircd", "4"},
		"ngircd-28":            {"ngircd", "28"},
		"ergo-v2.19.1":         {"ergo", "2.19.1"},
		"solanum-1.0-dev":      {"solanum", "1.0-dev"},
		"something-1.0":        {"unknown", "something-1.0"},
		"ircd-ratbox-3.0.10":   {"ratbox", "3.0.10"},
		"charybdis-4.1.2":      {"charybdis", "4.1.2"},
		"hybrid-8.2.43+plugin": {"hybrid", "8.2.43+plugin"},
	} {
		sw, v := identifySoftware(raw)
		if sw != want[0] || v != want[1] {
			t.Errorf("%q: got %s %s, want %v", raw, sw, v, want)
		}
	}
	for raw, want := range map[string][2]string{
		"Anope-2.1.28 services.ngircd.test :ngircd - (enc_sha2) -- Built: ...": {"anope", "2.1.28"},
		"atheme 7.3.0-rc2. services.insp.test :bf55352 [enc:pbkdf2v2]":         {"atheme", "7.3.0-rc2"},
		"Atheme IRC Services (atheme-7.2.12)":                                  {"atheme", "7.2.12"},
		"some services":                                                        {"unknown", ""},
	} {
		p, v := identifyServices(raw)
		if p != want[0] || v != want[1] {
			t.Errorf("%q: got %s %s, want %v", raw, p, v, want)
		}
	}
}

func statuses(t Table) map[string]Status {
	out := map[string]Status{}
	for _, c := range t.Features {
		out[c.Feature] = c.Status
	}
	return out
}

func TestEvaluateBareServer(t *testing.T) {
	r := &Report{Server: ServerInfo{Software: "ngircd"}, Caps: map[string]string{}, ISupport: map[string]string{}}
	tab := Evaluate(r)
	if len(tab.Features) != len(Features) {
		t.Fatalf("%d rows", len(tab.Features))
	}
	for i, f := range Features {
		if tab.Features[i].Feature != f {
			t.Fatalf("row %d is %s, want %s", i, tab.Features[i].Feature, f)
		}
	}
	s := statuses(tab)
	want := map[string]Status{
		FeatureMessageTags: ViaBridge, FeatureEchoMessage: ViaBridge, FeatureServerTime: ViaBridge,
		FeatureChatHistory: ViaBridge, FeatureReadMarkers: ViaBridge, FeatureReplies: Unavailable, FeatureReactions: Unavailable,
		FeatureMessageRedaction: Unavailable, FeatureSASL: Unavailable, FeatureServicesRPC: Unavailable,
		FeatureWebIRC: Unavailable, FeatureMultiline: Unavailable,
	}
	for k, v := range want {
		if s[k] != v {
			t.Errorf("%s: %s, want %s", k, s[k], v)
		}
	}
	if !tab.NeedsBridge || tab.Services != "none" {
		t.Errorf("needs_bridge %v services %q", tab.NeedsBridge, tab.Services)
	}
	for _, c := range tab.Features {
		if c.Status != Native && c.Fix == "" {
			t.Errorf("%s: no fix", c.Feature)
		}
		if c.Explanation == "" || c.Title == "" {
			t.Errorf("%s: no explanation/title", c.Feature)
		}
	}
	// NickServ but no SASL: the bridge identifies.
	r.Services = ServicesInfo{NickServ: true, Package: "anope"}
	if s := statuses(Evaluate(r)); s[FeatureSASL] != ViaBridge {
		t.Errorf("sasl with NickServ: %s", s[FeatureSASL])
	}
}

func TestEvaluateModernServer(t *testing.T) {
	r := &Report{
		Server:   ServerInfo{Software: "ergo"},
		Caps:     map[string]string{"message-tags": "", "echo-message": "", "server-time": "", "draft/chathistory": "", "draft/read-marker": "", "draft/message-redaction": "", "sasl": "PLAIN,EXTERNAL", "draft/multiline": "max-bytes=4096"},
		ISupport: map[string]string{},
		Services: ServicesInfo{NickServ: true, Package: "ergo", RPC: &RPCResult{Reachable: true, Authorized: true, CredentialsChecked: true, CredentialsOK: true}},
		WebIRC:   WebIRCResult{Tested: true, Accepted: true, Verified: true},
	}
	tab := Evaluate(r)
	for _, c := range tab.Features {
		if c.Status != Native {
			t.Errorf("%s: %s (%s)", c.Feature, c.Status, c.Explanation)
		}
	}
	if tab.NeedsBridge {
		t.Error("needs_bridge on an all-native server")
	}
	r.ISupport["CLIENTTAGDENY"] = "*,-draft/typing"
	if s := statuses(Evaluate(r)); s[FeatureReplies] != Unavailable || s[FeatureReactions] != Unavailable {
		t.Errorf("CLIENTTAGDENY=*: %v", s)
	}
	r.ISupport["CLIENTTAGDENY"] = "*,-draft/reply,-draft/react"
	if s := statuses(Evaluate(r)); s[FeatureReplies] != Native || s[FeatureReactions] != Native {
		t.Errorf("CLIENTTAGDENY allows reply/react: %v", s)
	}
	// UnrealIRCd 6's default: replies relayed, reactions dropped.
	r.ISupport["CLIENTTAGDENY"] = "*,-draft/typing,-typing,-draft/channel-context,-draft/reply,-reply"
	if s := statuses(Evaluate(r)); s[FeatureReplies] != Native || s[FeatureReactions] != Unavailable {
		t.Errorf("UnrealIRCd default CLIENTTAGDENY: %v", s)
	}
}

func TestDeniedClientTags(t *testing.T) {
	if d := deniedClientTags(map[string]string{"CLIENTTAGDENY": "draft/react"}, "+draft/reply", "+draft/react"); len(d) != 1 || d[0] != "+draft/react" {
		t.Fatal(d)
	}
	if d := deniedClientTags(map[string]string{}, "+draft/react"); d != nil {
		t.Fatal(d)
	}
}

// fakeIRCd is a scripted server: it answers registration, VERSION, WHOIS,
// PING, WEBIRC and CTCP VERSION the way the real ones do.
type fakeIRCd struct {
	ln       net.Listener
	caps     string // "" = no CAP (421)
	version  string
	isupport string
	nickserv string // services server name; "" = no NickServ
	svcVer   string // 351 from the services server
	webircPW string
	maxConns int
	conns    chan struct{}
}

func (f *fakeIRCd) start(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	if f.maxConns == 0 {
		f.maxConns = 100
	}
	f.conns = make(chan struct{}, f.maxConns)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return ln.Addr().String()
}

func (f *fakeIRCd) serve(c net.Conn) {
	defer c.Close()
	select {
	case f.conns <- struct{}{}:
		defer func() { <-f.conns }()
	default:
		io.WriteString(c, "ERROR :Closing Link: Too many connections from your IP\r\n")
		return
	}
	w := func(s string) { io.WriteString(c, s+"\r\n") }
	r := bufio.NewScanner(c)
	nick, host := "*", "127.0.0.1"
	registered := false
	webircBad := false
	for r.Scan() {
		m := parseMessage(r.Text())
		switch m.Command {
		case "WEBIRC":
			if m.param(0) != f.webircPW || f.webircPW == "" {
				webircBad = true
				w(":irc.fake 464 * :Password incorrect")
				if f.webircPW == "" {
					// ngircd-style: keep going
				}
			} else {
				host = m.param(3)
			}
		case "CAP":
			if f.caps == "" {
				w(":irc.fake 421 * CAP :Unknown command")
				continue
			}
			if m.param(0) == "LS" {
				w(":irc.fake CAP * LS * :multi-prefix")
				w(":irc.fake CAP * LS :" + f.caps)
			}
		case "NICK":
			nick = m.param(0)
		case "USER":
			registered = true
			_ = webircBad
			w(":irc.fake 001 " + nick + " :Welcome " + nick + "!u@" + host)
			w(":irc.fake 004 " + nick + " irc.fake " + f.version + " iow biklmnopstv")
			w(":irc.fake 005 " + nick + " " + f.isupport + " :are supported by this server")
			w(":irc.fake 422 " + nick + " :MOTD File is missing")
		case "PING":
			w(":irc.fake PONG irc.fake :" + m.last())
		case "VERSION":
			if m.param(0) != "" {
				if m.param(0) == f.nickserv && f.svcVer != "" {
					w(":" + f.nickserv + " 351 " + nick + " " + f.svcVer)
				} else {
					w(":irc.fake 402 " + nick + " " + m.param(0) + " :No such server")
				}
				continue
			}
			w(":irc.fake 351 " + nick + " " + f.version + ". irc.fake :fake")
			w(":irc.fake 005 " + nick + " " + f.isupport + " :are supported by this server")
		case "WHOIS":
			if strings.EqualFold(m.param(0), "NickServ") && f.nickserv != "" {
				w(":irc.fake 311 " + nick + " NickServ services " + f.nickserv + " * :Nickname Services")
				w(":irc.fake 312 " + nick + " NickServ " + f.nickserv + " :Services")
			} else if m.param(0) == nick {
				w(":irc.fake 311 " + nick + " " + nick + " u " + host + " * :real")
			} else {
				w(":irc.fake 401 " + nick + " " + m.param(0) + " :No such nick")
			}
			w(":irc.fake 318 " + nick + " " + m.param(0) + " :End of WHOIS")
		case "PRIVMSG":
			if strings.EqualFold(m.param(0), "NickServ") && m.last() == "\x01VERSION\x01" && f.nickserv != "" {
				w(":NickServ!NickServ@" + f.nickserv + " NOTICE " + nick + " :\x01VERSION atheme-7.3.0-rc2. " + f.nickserv + "\x01")
			}
		case "QUIT":
			w("ERROR :Closing Link")
			return
		}
		_ = registered
	}
}

func TestRunFakeModern(t *testing.T) {
	f := &fakeIRCd{
		caps:     "sasl=PLAIN message-tags echo-message server-time draft/chathistory draft/multiline=max-bytes=4096",
		version:  "UnrealIRCd-6.2.7.1",
		isupport: "NETWORK=FakeNet CHANLIMIT=#:10 NICKLEN=30",
		nickserv: "services.fake", svcVer: "Anope-2.1.28 services.fake :fake",
		webircPW: "secret",
	}
	addr := f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := Run(ctx, Options{Addr: addr, Timeout: 2 * time.Second, WebIRC: WebIRCOptions{Password: "secret"}, Transcript: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Server.Software != "unrealircd" || r.Server.Version != "6.2.7.1" || r.Server.Network != "FakeNet" || r.Server.Name != "irc.fake" {
		t.Errorf("server %+v", r.Server)
	}
	if !r.CapSupported || len(r.SASLMechanisms) != 1 || r.SASLMechanisms[0] != "PLAIN" {
		t.Errorf("caps %v sasl %v", r.Caps, r.SASLMechanisms)
	}
	if _, ok := r.Caps["multi-prefix"]; !ok {
		t.Errorf("multi-line CAP LS lost the first line: %v", r.Caps)
	}
	if !r.Services.NickServ || r.Services.Package != "anope" || r.Services.Version != "2.1.28" || r.Services.Server != "services.fake" {
		t.Errorf("services %+v", r.Services)
	}
	if !r.WebIRC.Accepted || !r.WebIRC.Verified {
		t.Errorf("webirc %+v", r.WebIRC)
	}
	if r.Limits.ISupport["CHANLIMIT"] != "#:10" || !strings.Contains(r.Limits.Hint, "maxperip") {
		t.Errorf("limits %+v", r.Limits)
	}
	for _, l := range r.Transcript {
		if strings.Contains(l, "secret") {
			t.Errorf("password in transcript: %s", l)
		}
	}
	s := statuses(r.Table)
	if s[FeatureSASL] != Native || s[FeatureChatHistory] != Native || s[FeatureWebIRC] != Native || s[FeatureReadMarkers] != ViaBridge || s[FeatureMultiline] != Native {
		t.Errorf("table %v", s)
	}
}

func TestRunFakeBare(t *testing.T) {
	f := &fakeIRCd{version: "ngircd-28", isupport: "NETWORK=Bare", nickserv: "services.fake", maxConns: 3}
	addr := f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := Run(ctx, Options{Addr: addr, Timeout: 2 * time.Second, WebIRC: WebIRCOptions{Password: "wrong"}, ConnLimitTest: 6})
	if err != nil {
		t.Fatal(err)
	}
	if r.CapSupported || len(r.Caps) != 0 {
		t.Errorf("caps on a server without CAP: %v", r.Caps)
	}
	// No 351 from the services server: CTCP VERSION fallback.
	if r.Services.Package != "atheme" || r.Services.Version != "7.3.0-rc2" {
		t.Errorf("services %+v", r.Services)
	}
	if r.WebIRC.Accepted || !strings.HasPrefix(r.WebIRC.Detail, "464") {
		t.Errorf("webirc %+v", r.WebIRC)
	}
	if r.Limits.MaxPerIP != 3 || !strings.Contains(r.Limits.Refusal, "Too many") {
		t.Errorf("limits %+v", r.Limits)
	}
	if s := statuses(r.Table); s[FeatureSASL] != ViaBridge || s[FeatureReplies] != Unavailable {
		t.Errorf("table %v", s)
	}
}

func TestRunUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if _, err := Run(context.Background(), Options{Addr: addr, Timeout: time.Second}); err == nil {
		t.Fatal("no error for a closed port")
	}
	if _, err := Run(context.Background(), Options{Addr: "nope"}); err == nil {
		t.Fatal("no error for a bad address")
	}
}

func TestRPCAnope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("Authorization") != "Bearer dG9r" { // base64("tok")
			reply(map[string]any{"jsonrpc": "2.0", "id": "x", "error": map[string]any{"code": -32601, "message": "No authorization for method: " + req.Method}})
			return
		}
		switch {
		case req.Method != "anope.checkCredentials":
			reply(map[string]any{"jsonrpc": "2.0", "id": "x", "error": map[string]any{"code": -32601, "message": "Method not found: " + req.Method}})
		case req.Params[0] != "alice":
			reply(map[string]any{"jsonrpc": "2.0", "id": "x", "error": map[string]any{"code": -32099, "message": "No such account"}})
		case req.Params[1] != "pw":
			reply(map[string]any{"jsonrpc": "2.0", "id": "x", "error": map[string]any{"code": -32098, "message": "Invalid password"}})
		default:
			reply(map[string]any{"jsonrpc": "2.0", "id": "x", "result": map[string]any{"account": "alice", "confirmed": true}})
		}
	}))
	defer srv.Close()
	o := &Options{RPC: RPCOptions{URL: srv.URL + "/jsonrpc", Token: "tok", Account: "alice", Password: "pw"}}
	if r := checkRPC(context.Background(), o, "anope"); !r.CredentialsOK || r.Flavor != "anope" || r.Protocol != "jsonrpc" {
		t.Errorf("good creds: %+v", r)
	}
	o.RPC.Password = "bad"
	if r := checkRPC(context.Background(), o, "anope"); r.CredentialsOK || !r.CredentialsChecked || !r.Authorized || r.Error == "" {
		t.Errorf("bad creds: %+v", r)
	}
	o.RPC.Token = "nope"
	if r := checkRPC(context.Background(), o, "anope"); r.Authorized || !r.Reachable || !strings.Contains(r.Error, "token") {
		t.Errorf("bad token: %+v", r)
	}
	o.RPC = RPCOptions{URL: srv.URL + "/jsonrpc", Token: "tok"}
	if r := checkRPC(context.Background(), o, "anope"); !r.Authorized || r.CredentialsChecked {
		t.Errorf("no account: %+v", r)
	}
	tab := Evaluate(&Report{Caps: map[string]string{}, ISupport: map[string]string{}, Services: ServicesInfo{NickServ: true, Package: "anope", RPC: checkRPC(context.Background(), o, "anope")}})
	if c, _ := tab.Get(FeatureServicesRPC); c.Status != Unavailable || !strings.Contains(c.Explanation, "no test account") {
		t.Errorf("row %+v", c)
	}
}

func TestRPCAthemeJSONAndXML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/xmlrpc") {
			if strings.Contains(string(body), "<string>bob</string><") && strings.Contains(string(body), "<string>pw</string>") {
				io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><string>cookie123</string></value></param></params></methodResponse>`)
				return
			}
			io.WriteString(w, `<?xml version="1.0"?><methodResponse><fault><value><struct><member><name>faultCode</name><value><int>5</int></value></member><member><name>faultString</name><value><string>The password is incorrect.</string></value></member></struct></value></fault></methodResponse>`)
			return
		}
		var req struct {
			Method string   `json:"method"`
			Params []string `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		switch {
		case req.Method == "atheme.logout":
			io.WriteString(w, `{"result":"ok","id":"x"}`)
		case req.Method != "atheme.login":
			io.WriteString(w, `{"error":{"code":-32601,"message":"Method not found"},"id":"x"}`)
		case req.Params[0] != "bob":
			io.WriteString(w, `{"error":{"code":3,"message":"The account is not registered."},"id":"x"}`)
		case req.Params[1] != "pw":
			io.WriteString(w, `{"error":{"code":5,"message":"The password is incorrect."},"id":"x"}`)
		default:
			io.WriteString(w, `{"result":"cookie123","id":"x"}`)
		}
	}))
	defer srv.Close()
	for _, path := range []string{"/jsonrpc", "/xmlrpc"} {
		o := &Options{RPC: RPCOptions{URL: srv.URL + path, Account: "bob", Password: "pw"}}
		r := checkRPC(context.Background(), o, "atheme")
		if !r.CredentialsOK || r.Flavor != "atheme" {
			t.Errorf("%s good: %+v", path, r)
		}
		o.RPC.Password = "bad"
		if r := checkRPC(context.Background(), o, "atheme"); r.CredentialsOK || !r.CredentialsChecked || r.Flavor != "atheme" {
			t.Errorf("%s bad: %+v", path, r)
		}
	}
	// Unknown package: tries both.
	o := &Options{RPC: RPCOptions{URL: srv.URL + "/jsonrpc", Account: "bob", Password: "pw"}}
	if r := checkRPC(context.Background(), o, "unknown"); !r.CredentialsOK {
		t.Errorf("unknown pkg: %+v", r)
	}
	// Not an RPC endpoint at all.
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>hi</html>") }))
	defer html.Close()
	o = &Options{RPC: RPCOptions{URL: html.URL + "/jsonrpc", Account: "bob", Password: "pw"}}
	if r := checkRPC(context.Background(), o, "atheme"); r.Reachable || !strings.Contains(r.Error, "not an RPC reply") {
		t.Errorf("html: %+v", r)
	}
}
