package stack

import (
	"os"
	"strings"
	"testing"
)

func addonAnswers() Answers {
	a := answers()
	a.Mode = ModeAddon
	a.NetworkName = "NgircdNet"
	a.Addon = AddonAnswers{Host: "irc.example.org", Port: 6697, TLS: true, TLSVerify: false,
		CertSHA256: strings.Repeat("ab", 32), SPKISHA256: strings.Repeat("cd", 32),
		Software: "ngircd", ServerName: "irc.example.org", SourceIP: "198.51.100.7", Services: "anope",
		RPCURL: "http://10.0.0.5:8080/jsonrpc", Accounts: AccountsBoson}
	return a
}

func TestAddonPlan(t *testing.T) {
	p, err := BuildPlan(addonAnswers(), Secrets{}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.EnvValue("COMPOSE_PROFILES"); got != "bridge,auth,api,web,admin,metrics,voice,files,wellknown,updates" {
		t.Fatalf("profiles %q", got)
	}
	for k, want := range map[string]string{
		"ERGO_API_TOKEN":     "",
		"ACCOUNT_BACKEND":    "local",
		"WELLKNOWN_SECTIONS": "irc,auth,voice,files,web",
		"ADMIN_IRC_CHECK":    "ircs://evrd-links:6697",
		"WEBIRC_ALIASES":     "irc.boson.test:6697=evrd-links:6667,irc.boson.test:6667=evrd-links:6667",
		"BRIDGE_CERT_HOST":   "irc.boson.test",
		"BRIDGE_IRC_SERVER":  "irc.example.org:6697",
	} {
		if got := p.EnvValue(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	if strings.Contains(p.EnvValue("ADMIN_CONFIG_STATUS_TARGETS"), "ergo") {
		t.Error("status targets name ergo")
	}
	if p.OperPassword != "" || p.EnvValue("OPER_PASSWORD_HASH") != "" {
		t.Error("an oper password without Ergo")
	}
	pw := p.EnvValue("BRIDGE_WEBIRC_PASSWORD")
	clientPW := p.EnvValue("BRIDGE_CLIENT_WEBIRC_PASSWORD")
	if len(pw) != 48 || len(clientPW) != 48 || pw == clientPW ||
		p.EnvValue("WEBIRC_PASSWORDS") != "irc.example.org="+pw+",evrd-links="+clientPW {
		t.Fatalf("webirc %q %q / %q", pw, clientPW, p.EnvValue("WEBIRC_PASSWORDS"))
	}
	if len(p.EnvValue("BRIDGE_SECRET_KEY")) != 64 || len(p.EnvValue("BRIDGE_TOKEN")) != 64 {
		t.Error("bridge secrets")
	}
	for _, v := range p.Env {
		if SecretKeys[v.Key] != v.Secret {
			t.Errorf("%s: secret=%v", v.Key, v.Secret)
		}
	}
	if !strings.Contains(MaskedEnv(p), "BRIDGE_TOKEN=••••••••") || strings.Contains(MaskedEnv(p), pw) {
		t.Fatal("bridge secrets not masked")
	}
	if len(p.Files) != 1 || p.Files[0].Path != BridgeConfPath {
		t.Fatalf("files %+v", p.Files)
	}
	conf := p.Files[0].Content
	for _, want := range []string{"managed yes", "managed_network NgircdNet", "managed_server irc.example.org +6697",
		"managed_tls_verify no", "managed_fingerprint " + strings.Repeat("ab", 32), "managed_server_name irc.example.org",
		"managed_token_issuer https://auth.boson.test", "managed_token_audience boson", "managed_webirc_gateway {ident}",
		"links_socket /run/evrd-links/links.sock", "tls_cert /certs/fullchain.pem", "managed_nick_guard_grace 60s"} {
		if !strings.Contains(conf, want+"\n") {
			t.Errorf("evrd.conf lacks %q:\n%s", want, conf)
		}
	}
	for _, s := range p.SecretValues() {
		if strings.Contains(conf, s) {
			t.Fatal("a secret in evrd.conf")
		}
	}
	// auth drives the bridge's guests (PUT /api/managed/settings).
	if strings.Contains(conf, "\nmanaged_guests ") {
		t.Errorf("evrd.conf sets managed_guests:\n%s", conf)
	}
	// Same answers again with that .env: the secrets stay.
	text, _ := MergeEnv("", p.Env, "# t")
	p2, _ := BuildPlan(addonAnswers(), Secrets{}, ParseEnv(text), NewGenerator(), "999")
	if p2.EnvValue("BRIDGE_TOKEN") != p.EnvValue("BRIDGE_TOKEN") || p2.EnvValue("WEBIRC_PASSWORDS") != p.EnvValue("WEBIRC_PASSWORDS") ||
		p2.EnvValue("BRIDGE_CLIENT_WEBIRC_PASSWORD") != clientPW {
		t.Fatal("bridge secrets not kept")
	}
	// An install from an older wizard (no client WEBIRC password yet):
	// its server password stays, the client one is added.
	old := ParseEnv(text)
	delete(old, "BRIDGE_CLIENT_WEBIRC_PASSWORD")
	old["WEBIRC_PASSWORDS"] = "irc.example.org=" + pw
	p4, _ := BuildPlan(addonAnswers(), Secrets{}, old, NewGenerator(), "999")
	if c := p4.EnvValue("BRIDGE_CLIENT_WEBIRC_PASSWORD"); len(c) != 48 || p4.EnvValue("WEBIRC_PASSWORDS") != "irc.example.org="+pw+",evrd-links="+c {
		t.Fatalf("upgrade: %q / %q", c, p4.EnvValue("WEBIRC_PASSWORDS"))
	}
	// Still pinned to an evrd before 0.61: nothing it would refuse (an
	// unknown directive stops evrd) or not understand (WEBIRC from the
	// gateway); the secret is made anyway, for after the update.
	old["EVRD_IMAGE"] = "ghcr.io/4evrd/evrd:0.60.0"
	p5, _ := BuildPlan(addonAnswers(), Secrets{}, old, NewGenerator(), "999")
	if p5.EnvValue("WEBIRC_PASSWORDS") != "irc.example.org="+pw || len(p5.EnvValue("BRIDGE_CLIENT_WEBIRC_PASSWORD")) != 48 {
		t.Fatalf("evrd 0.60: %q", p5.EnvValue("WEBIRC_PASSWORDS"))
	}
	if c := p5.Files[0].Content; strings.Contains(c, "\nmanaged_nick_guard_grace") || !strings.Contains(c, "evrd 0.61.0 or later") {
		t.Fatalf("evrd 0.60 conf:\n%s", c)
	}
	for img, want := range map[string]bool{"": true, "ghcr.io/4evrd/evrd:0.61.0": true, "ghcr.io/4evrd/evrd:1.0.0": true,
		"evrd:local": true, "ghcr.io/4evrd/evrd:0.59.2": false, "ghcr.io/4evrd/evrd:0.61.0-rc.1": false} {
		if evrdAtLeast(img, evrdGuests) != want {
			t.Errorf("evrdAtLeast(%q) != %v", img, want)
		}
	}
	// Read back: an add-on install.
	if ex := (Existing{Found: true, Env: ParseEnv(text)}).Answers(); !ex.IsAddon() || ex.Addon.Host != "irc.example.org" || ex.Addon.Port != 6697 {
		t.Fatalf("existing: %+v", ex.Addon)
	}
	// Fresh again on that .env: the add-on keys are cleared.
	p3, _ := BuildPlan(answers(), Secrets{}, ParseEnv(text), NewGenerator(), "999")
	if p3.EnvValue("WEBIRC_ALIASES") != "" || p3.EnvValue("ACCOUNT_BACKEND") != "" {
		t.Fatal("fresh kept the add-on keys")
	}
	if strings.HasPrefix(p3.EnvValue("COMPOSE_PROFILES"), "bridge") {
		t.Fatal("fresh profiles")
	}
}

func TestAddonNickServ(t *testing.T) {
	old := AuthNickServ
	defer func() { AuthNickServ = old }()
	a := addonAnswers()
	a.Addon.Accounts, a.Addon.Strategy = AccountsNickServ, "anope_rpc"
	AuthNickServ = false
	if errs := a.Validate("irc", Secrets{RPCToken: "t"}, false); len(errs) == 0 || errs[0].Field != "addon.accounts" {
		t.Fatalf("nickserv without auth support: %v", errs)
	}
	AuthNickServ = true
	if errs := a.Validate("irc", Secrets{}, false); len(errs) == 0 || errs[0].Field != "addon.rpc_token" {
		t.Fatalf("anope_rpc without a token: %v", errs)
	}
	p, err := BuildPlan(a, Secrets{RPCToken: "fixture-rpc-token"}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"ACCOUNT_BACKEND": "nickserv", "NICKSERV_STRATEGY": "anope_rpc", "NICKSERV_SERVICES": "anope",
		"NICKSERV_RPC_URL": "http://10.0.0.5:8080/jsonrpc", "NICKSERV_RPC_TOKEN": "fixture-rpc-token", "NICKSERV_ALLOW_PLAINTEXT": "true",
		"NICKSERV_IRC_ADDR": "", "NICKSERV_TLS_PIN_SHA256": strings.Repeat("cd", 32)} {
		if got := p.EnvValue(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	// The admin needs no Boson password.
	if errs := a.Validate("admin", Secrets{}, true); len(errs) != 0 {
		t.Fatalf("admin: %v", errs)
	}
	a.Addon.Strategy = "sasl"
	p, _ = BuildPlan(a, Secrets{}, nil, NewGenerator(), "999")
	if p.EnvValue("NICKSERV_IRC_ADDR") != "irc.example.org:6697" || p.EnvValue("NICKSERV_RPC_URL") != "" || p.EnvValue("NICKSERV_ALLOW_PLAINTEXT") != "false" {
		t.Fatal("sasl env")
	}
	if p.EnvValue("BRIDGE_NICKSERV_METHOD") != "" {
		t.Fatalf("a server with SASL: auth's default (sasl), got %q", p.EnvValue("BRIDGE_NICKSERV_METHOD"))
	}
	// No SASL on the server (ngIRCd): the bridge identifies with NickServ.
	a.Addon.Strategy, a.Addon.NoSASL = "anope_rpc", true
	p, _ = BuildPlan(a, Secrets{RPCToken: "t"}, nil, NewGenerator(), "999")
	if got := p.EnvValue("BRIDGE_NICKSERV_METHOD"); got != "nickserv" {
		t.Fatalf("BRIDGE_NICKSERV_METHOD=%q, want nickserv", got)
	}
	// Boson accounts: nothing to identify.
	a.Addon.Accounts = AccountsBoson
	p, _ = BuildPlan(a, Secrets{}, nil, NewGenerator(), "999")
	if got := p.EnvValue("BRIDGE_NICKSERV_METHOD"); got != "" {
		t.Fatalf("boson accounts: %q", got)
	}
}

func TestRecommendAccounts(t *testing.T) {
	for _, c := range []struct {
		flavor, proto       string
		rpc, sasl, nickserv bool
		acc, strat          string
	}{
		{"anope", "jsonrpc", true, true, true, AccountsNickServ, "anope_rpc"},
		{"atheme", "jsonrpc", true, true, true, AccountsNickServ, "atheme_jsonrpc"},
		{"atheme", "xmlrpc", true, false, true, AccountsNickServ, "atheme_xmlrpc"},
		{"anope", "jsonrpc", false, true, true, AccountsNickServ, "sasl"},
		{"", "", false, false, true, AccountsBoson, ""},
		{"", "", false, false, false, AccountsBoson, ""},
	} {
		acc, strat, why := RecommendAccounts(c.flavor, c.proto, c.rpc, c.sasl, c.nickserv)
		if acc != c.acc || strat != c.strat || why == "" {
			t.Errorf("%+v: %s %s", c, acc, strat)
		}
	}
}

func TestSnippets(t *testing.T) {
	a := addonAnswers()
	ids := func(s []Snippet) string {
		var out []string
		for _, x := range s {
			out = append(out, x.ID)
		}
		return strings.Join(out, ",")
	}
	s := Snippets(a, "PW")
	if ids(s) != "webirc,limits,services-rpc" || !strings.Contains(s[0].Text, "WebIRCPassword = PW") || s[1].Warning == "" {
		t.Fatalf("ngircd: %s %+v", ids(s), s)
	}
	a.Addon.Software, a.Addon.AllowReact = "unrealircd", true
	s = Snippets(a, "PW")
	if ids(s) != "webirc,limits,react-module,react,services-rpc" || !strings.Contains(s[0].Text, "mask 198.51.100.7;") {
		t.Fatalf("unreal: %s", ids(s))
	}
	a.Addon.Software = "inspircd"
	s = Snippets(a, "PW")
	if ids(s) != "limits,webirc,services-rpc" || !s[0].First || !strings.Contains(s[0].Text, `allow="198.51.100.7"`) {
		t.Fatalf("insp: %s", ids(s))
	}
}

// The UnrealIRCd fixture builds the same module the wizard hands out.
func TestReactModuleMatchesFixture(t *testing.T) {
	b, err := os.ReadFile("../../../../test/ircds/unrealircd/boson-react.c")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != BosonReactModule() {
		t.Fatal("test/ircds/unrealircd/boson-react.c differs from snippets/boson-react.c")
	}
}

func TestAddonNickServSignup(t *testing.T) {
	old := AuthNickServ
	defer func() { AuthNickServ = old }()
	AuthNickServ = true
	a := addonAnswers()
	a.Addon.Accounts, a.Addon.Strategy = AccountsNickServ, "atheme_jsonrpc"
	a.Registration = "invite"
	if !a.RegistersNickServ() || !a.NickServCanRegister() {
		t.Fatal("default: register NickServ accounts (atheme)")
	}
	p, err := BuildPlan(a, Secrets{RPCToken: "t"}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	// On by default, whatever the registration mode (it was registration ==
	// open before): auth's registration setting still gates sign-up.
	if got := p.EnvValue("NICKSERV_REGISTRATION"); got != "true" {
		t.Fatalf("NICKSERV_REGISTRATION=%q", got)
	}
	cfg, _, err := ApplyConfig(nil, example(t), p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if ConfigValue(cfg, "auth", "nickserv", "registration") != true || ConfigValue(cfg, "auth", "nickserv", "signup_message") != nil {
		t.Fatalf("config:\n%s", cfg)
	}

	// Off, with a message: both in the config file, the switch in .env too.
	off := false
	a.NickServRegistration = &off
	a.NickServSignupMessage = "  Register at https://example.org/register, then sign in here.  "
	a.Normalize()
	if errs := a.Validate("features", Secrets{}, false); len(errs) != 0 {
		t.Fatal(errs)
	}
	p, _ = BuildPlan(a, Secrets{RPCToken: "t"}, nil, NewGenerator(), "999")
	if got := p.EnvValue("NICKSERV_REGISTRATION"); got != "false" {
		t.Fatalf("NICKSERV_REGISTRATION=%q", got)
	}
	cfg2, _, _ := ApplyConfig(cfg, example(t), p.Config)
	if ConfigValue(cfg2, "auth", "nickserv", "registration") != false ||
		ConfigValue(cfg2, "auth", "nickserv", "signup_message") != "Register at https://example.org/register, then sign in here." {
		t.Fatalf("config:\n%s", cfg2)
	}
	// Read back on a re-run.
	env, _ := MergeEnv("", p.Env, "#")
	back := Existing{Found: true, Env: ParseEnv(env), Config: cfg2}.Answers()
	if back.RegistersNickServ() || back.NickServSignupMessage != a.NickServSignupMessage {
		t.Fatalf("read back: %v %q", back.RegistersNickServ(), back.NickServSignupMessage)
	}
	// The message cleared: the key in the file is emptied (auth's default).
	a.NickServSignupMessage = ""
	p, _ = BuildPlan(a, Secrets{RPCToken: "t"}, nil, NewGenerator(), "999")
	cfg3, _, _ := ApplyConfig(cfg2, example(t), p.Config)
	if ConfigValue(cfg3, "auth", "nickserv", "signup_message") != "" {
		t.Fatalf("cleared message:\n%s", cfg3)
	}

	// Messages that can't go in: too long, more than one line.
	a.NickServSignupMessage = strings.Repeat("x", MaxSignupMessageLen+1)
	if errs := a.Validate("features", Secrets{}, false); len(errs) != 1 || errs[0].Field != "nickserv_signup_message" {
		t.Fatalf("long: %v", errs)
	}
	a.NickServSignupMessage = "two\nlines"
	if errs := a.Validate("features", Secrets{}, false); len(errs) != 1 || errs[0].Field != "nickserv_signup_message" {
		t.Fatalf("multi-line: %v", errs)
	}

	// Only Atheme's RPC registers accounts.
	a.Addon.Strategy = "anope_rpc"
	if a.NickServCanRegister() {
		t.Fatal("anope can't register")
	}
	// Boson accounts: no nickserv keys at all.
	b := addonAnswers()
	p, _ = BuildPlan(b, Secrets{}, nil, NewGenerator(), "999")
	for _, k := range p.Config {
		if len(k.Path) > 1 && k.Path[1] == "nickserv" {
			t.Fatalf("boson accounts wrote %v", k.Path)
		}
	}
	if got := p.EnvValue("NICKSERV_REGISTRATION"); got != "" {
		t.Fatalf("boson accounts: NICKSERV_REGISTRATION=%q", got)
	}
}

func TestSMTPNeed(t *testing.T) {
	a := answers()
	for reg, want := range map[string]string{"open": SMTPRecommended, "invite": SMTPRecommended, "closed": SMTPNotNeeded} {
		a.Registration = reg
		if got := a.SMTPNeed(); got != want {
			t.Errorf("%s: %s, want %s", reg, got, want)
		}
	}
	// NickServ accounts: the services send their own mail.
	n := addonAnswers()
	n.Registration = "open"
	n.Addon.Accounts, n.Addon.Strategy = AccountsNickServ, "atheme_jsonrpc"
	if n.SMTPNeed() != SMTPNotNeeded {
		t.Fatal("nickserv accounts need no SMTP")
	}
	// Skipping SMTP is never an error; only verification without it is.
	a.Registration, a.SMTP.Host, a.EmailVerification = "open", "", false
	if errs := a.Validate("features", Secrets{}, false); len(errs) != 0 {
		t.Fatal(errs)
	}
}
