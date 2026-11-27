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
		"WELLKNOWN_SECTIONS": "irc,auth,voice,files",
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
	if len(pw) != 48 || p.EnvValue("WEBIRC_PASSWORDS") != "irc.example.org="+pw {
		t.Fatalf("webirc %q / %q", pw, p.EnvValue("WEBIRC_PASSWORDS"))
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
		"links_socket /run/evrd-links/links.sock", "tls_cert /certs/fullchain.pem"} {
		if !strings.Contains(conf, want+"\n") {
			t.Errorf("evrd.conf lacks %q:\n%s", want, conf)
		}
	}
	for _, s := range p.SecretValues() {
		if strings.Contains(conf, s) {
			t.Fatal("a secret in evrd.conf")
		}
	}
	// Same answers again with that .env: the secrets stay.
	text, _ := MergeEnv("", p.Env, "# t")
	p2, _ := BuildPlan(addonAnswers(), Secrets{}, ParseEnv(text), NewGenerator(), "999")
	if p2.EnvValue("BRIDGE_TOKEN") != p.EnvValue("BRIDGE_TOKEN") || p2.EnvValue("WEBIRC_PASSWORDS") != p.EnvValue("WEBIRC_PASSWORDS") {
		t.Fatal("bridge secrets not kept")
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
