package stack

// The "Add Boson to my existing IRC server" answers (Answers.Mode "addon"):
// their IRC server, how Boson accounts relate to its NickServ accounts, and
// what the bridge (evrd in managed mode, docs/bridge.md) needs to know.

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/boson-chat/stack/images/setup/internal/secret"
)

const (
	ModeFresh = ""
	ModeAddon = "addon"
)

// Account modes (AddonAnswers.Accounts).
const (
	// AccountsBoson: Boson has its own accounts (auth's local backend);
	// the bridge connects each one to the IRC server under its username.
	AccountsBoson = "boson"
	// AccountsNickServ: people sign in to Boson with their NickServ
	// account (auth ACCOUNT_BACKEND=nickserv, docs/nickserv.md in auth).
	AccountsNickServ = "nickserv"
)

// NickServ strategies, in auth's decision order (NICKSERV_STRATEGY).
var nickservStrategies = []string{"anope_rpc", "atheme_jsonrpc", "atheme_xmlrpc", "sasl", "identify"}

// AddonAnswers describe the IRC server Boson is added to.
type AddonAnswers struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls"`
	// TLSVerify: the server's certificate chain is checked. Off: the
	// certificate seen by the probe is pinned instead (self-signed).
	TLSVerify bool `json:"tls_verify"`
	// CertSHA256 / SPKISHA256: the pinned certificate (the bridge) and its
	// public key (auth's NICKSERV_TLS_PIN_SHA256), from the probe.
	CertSHA256 string `json:"cert_sha256"`
	SPKISHA256 string `json:"spki_sha256"`

	// Software: unrealircd | inspircd | ngircd | other (from the probe;
	// picks the config snippets).
	Software string `json:"software"`
	// ServerName: the server's own name (001), what Boson users see the
	// bridge's lines come from.
	ServerName string `json:"server_name"`
	// SourceIP: the address the IRC server sees this machine connect
	// from (the WEBIRC block and the connection-limit exemption).
	SourceIP string `json:"source_ip"`

	// Services: anope | atheme | none | unknown; ServicesServer: its
	// server name (IDENTIFY checks NickServ is there).
	Services       string `json:"services"`
	ServicesServer string `json:"services_server"`
	// NoSASL: the probe found no SASL PLAIN on the server, so the bridge
	// identifies NickServ accounts with IDENTIFY once connected
	// (BRIDGE_NICKSERV_METHOD=nickserv, auth docs/bridge.md).
	NoSASL bool `json:"no_sasl"`
	// RPCURL: the services' JSON-RPC / XML-RPC endpoint (optional); its
	// token is a secret (Secrets.RPCToken).
	RPCURL string `json:"rpc_url"`

	Accounts string `json:"accounts"`
	// Strategy: NICKSERV_STRATEGY with Accounts nickserv.
	Strategy string `json:"strategy"`

	// AllowReact: UnrealIRCd only: load the module that lets +draft/react
	// through (Boson's reactions).
	AllowReact bool `json:"allow_react"`
}

func (a *AddonAnswers) normalize() {
	a.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a.Host)), ".")
	a.SourceIP = strings.TrimSpace(a.SourceIP)
	a.RPCURL = strings.TrimSpace(a.RPCURL)
	a.ServerName = strings.TrimSpace(a.ServerName)
	a.ServicesServer = strings.TrimSpace(a.ServicesServer)
	if a.Port == 0 {
		a.Port = 6697
	}
	switch a.Software {
	case "unrealircd", "inspircd", "ngircd":
	default:
		a.Software = "other"
	}
	if a.Accounts == "" {
		a.Accounts = AccountsBoson
	}
	if a.Accounts != AccountsNickServ {
		a.Strategy = ""
	}
	if a.Software != "unrealircd" {
		a.AllowReact = false
	}
	a.CertSHA256 = strings.ToLower(a.CertSHA256)
	a.SPKISHA256 = strings.ToLower(a.SPKISHA256)
}

var (
	hexSHA256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	serverNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,62}$`)
)

func validHost(s string) bool {
	return net.ParseIP(s) != nil || ValidDomain(s) || s == "localhost"
}

func (a AddonAnswers) validate(add func(f, m string), s Secrets, nickservOK bool) {
	if !validHost(a.Host) || !safeText(a.Host) {
		add("addon.host", "Enter your IRC server's hostname or address, like irc.example.org.")
	}
	if a.Port < 1 || a.Port > 65535 {
		add("addon.port", "Enter a port between 1 and 65535.")
	}
	if a.TLS && !a.TLSVerify && !hexSHA256Re.MatchString(a.CertSHA256) {
		add("addon.tls_verify", "Check the server first: its certificate is pinned from what it presents.")
	}
	if ip := net.ParseIP(a.SourceIP); ip == nil {
		add("addon.source_ip", "Enter the address your IRC server sees this server connect from (an IP address).")
	}
	if a.ServerName != "" && !serverNameRe.MatchString(a.ServerName) {
		add("addon.server_name", "One word: the server's name, like irc.example.org.")
	}
	if a.RPCURL != "" {
		u, err := url.Parse(a.RPCURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !safeText(a.RPCURL) || strings.ContainsAny(a.RPCURL, " ,") {
			add("addon.rpc_url", "The services RPC address is a URL, like http://services.example.org:8080/jsonrpc.")
		}
	}
	if !safeText(s.RPCToken) || strings.ContainsAny(s.RPCToken, " ,") {
		add("addon.rpc_token", "The RPC token can't contain spaces, commas, quotes, $, backslashes or line breaks.")
	}
	switch a.Accounts {
	case AccountsBoson:
	case AccountsNickServ:
		if !nickservOK {
			add("addon.accounts", "This version of Boson's account service can't use NickServ accounts yet: pick Boson accounts.")
		}
		known := false
		for _, st := range nickservStrategies {
			known = known || st == a.Strategy
		}
		switch {
		case !known:
			add("addon.strategy", "Pick how Boson checks NickServ passwords.")
		case strings.HasSuffix(a.Strategy, "rpc") && a.RPCURL == "":
			add("addon.rpc_url", "Checking passwords through the services RPC needs its address.")
		case a.Strategy == "anope_rpc" && s.RPCToken == "":
			add("addon.rpc_token", "Anope's RPC needs the token from its jsonrpc block.")
		}
	default:
		add("addon.accounts", "Pick how accounts work.")
	}
}

// bridgeEnvKeys: every .env key only the add-on path writes. The fresh
// path clears the ones an earlier add-on install left.
var bridgeEnvKeys = []string{"WELLKNOWN_SECTIONS", "ADMIN_IRC_CHECK", "ADMIN_CONFIG_STATUS_TARGETS",
	"WEBIRC_ALIASES", "WEBIRC_PASSWORDS", "WEBIRC_EXTRA_PORTS", "BRIDGE_CERT_HOST", "BRIDGE_IRC_SERVER",
	"ACCOUNT_BACKEND", "NICKSERV_STRATEGY", "NICKSERV_SERVICES", "NICKSERV_RPC_URL", "NICKSERV_RPC_TOKEN",
	"NICKSERV_IRC_ADDR", "NICKSERV_IRC_TLS", "NICKSERV_TLS_PIN_SHA256", "NICKSERV_ALLOW_PLAINTEXT",
	"NICKSERV_SERVICES_SERVER", "NICKSERV_REGISTRATION", "BRIDGE_NICKSERV_METHOD"}

// bridgeSecrets are generated for the add-on path only.
var bridgeSecrets = []struct {
	key string
	n   int
}{
	{"BRIDGE_TOKEN", 32},
	{"BRIDGE_WEBIRC_PASSWORD", 24},
	// evrd's secret_key: 32 bytes, hex.
	{"BRIDGE_SECRET_KEY", 32},
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// statusTargetsNoErgo: the admin panel's config status targets without
// Ergo (compose.yaml's default minus ergo=).
const statusTargetsNoErgo = "auth=http://auth:8081/internal/config/status,quicvoiced=http://quicvoiced:8080/internal/config/status,directory=http://directory:8081/internal/config/status,api=http://api:3001/internal/config/status,files=http://files:3001/internal/config/status"

// addonEnv adds the add-on path's .env lines.
func addonEnv(p *Plan, a Answers, s Secrets, existing map[string]string, gen *Generator,
	set func(k, v string), setSecret func(k, v, origin string), keepOr func(k string, mk func() string)) {
	ad := a.Addon
	h := a.HostNames()
	sections := []string{"irc", "auth"}
	if a.Voice {
		sections = append(sections, "voice")
	}
	if a.Files.Enabled {
		sections = append(sections, "files")
	}
	set("WELLKNOWN_SECTIONS", strings.Join(sections, ","))
	set("ADMIN_IRC_CHECK", "ircs://evrd-links:6697")
	set("ADMIN_CONFIG_STATUS_TARGETS", statusTargetsNoErgo)
	// The web client's IRC goes to the bridge (its plaintext listener on
	// the compose network); a connection to their server itself goes
	// straight there, with WEBIRC.
	set("WEBIRC_ALIASES", h[RoleIRC]+":6697=evrd-links:6667,"+h[RoleIRC]+":6667=evrd-links:6667")
	webircPW, origin := existing["BRIDGE_WEBIRC_PASSWORD"], "kept"
	if webircPW == "" {
		webircPW, origin = gen.get("BRIDGE_WEBIRC_PASSWORD", func() string { return hexSecret(24) }), "generated"
	}
	setSecret("WEBIRC_PASSWORDS", ad.Host+"="+webircPW, origin)
	if ad.Port != 6697 && ad.Port != 6667 {
		set("WEBIRC_EXTRA_PORTS", strconv.Itoa(ad.Port))
	} else {
		set("WEBIRC_EXTRA_PORTS", "")
	}
	set("BRIDGE_CERT_HOST", h[RoleIRC])
	set("BRIDGE_IRC_SERVER", net.JoinHostPort(ad.Host, strconv.Itoa(ad.Port)))
	for _, b := range bridgeSecrets {
		n := b.n
		keepOr(b.key, func() string { return hexSecret(n) })
	}

	// auth: the account backend (auth's docs/nickserv.md).
	if ad.Accounts != AccountsNickServ {
		set("ACCOUNT_BACKEND", "local")
		set("BRIDGE_NICKSERV_METHOD", "")
		for _, k := range []string{"NICKSERV_STRATEGY", "NICKSERV_SERVICES", "NICKSERV_RPC_URL", "NICKSERV_IRC_ADDR",
			"NICKSERV_IRC_TLS", "NICKSERV_TLS_PIN_SHA256", "NICKSERV_ALLOW_PLAINTEXT", "NICKSERV_SERVICES_SERVER", "NICKSERV_REGISTRATION"} {
			set(k, "")
		}
		setSecret("NICKSERV_RPC_TOKEN", "", "")
		return
	}
	set("ACCOUNT_BACKEND", "nickserv")
	set("NICKSERV_STRATEGY", ad.Strategy)
	// How the bridge logs NickServ accounts in (auth hands it the login at
	// each sign-in): SASL where the server has it (auth's default), else
	// NickServ IDENTIFY once connected.
	if ad.NoSASL {
		set("BRIDGE_NICKSERV_METHOD", "nickserv")
	} else {
		set("BRIDGE_NICKSERV_METHOD", "")
	}
	svc := ad.Services
	if strings.HasPrefix(ad.Strategy, "atheme") {
		svc = "atheme"
	}
	if svc != "atheme" {
		svc = "anope"
	}
	set("NICKSERV_SERVICES", svc)
	plaintext := false
	if strings.HasSuffix(ad.Strategy, "rpc") {
		set("NICKSERV_RPC_URL", ad.RPCURL)
		if u, err := url.Parse(ad.RPCURL); err == nil && u.Scheme == "http" && !isLoopback(u.Hostname()) {
			plaintext = true
		}
		set("NICKSERV_IRC_ADDR", "")
	} else {
		set("NICKSERV_RPC_URL", "")
		set("NICKSERV_IRC_ADDR", net.JoinHostPort(ad.Host, strconv.Itoa(ad.Port)))
		if !ad.TLS && !isLoopback(ad.Host) {
			plaintext = true
		}
	}
	set("NICKSERV_IRC_TLS", strconv.FormatBool(ad.TLS))
	if ad.TLS && !ad.TLSVerify {
		set("NICKSERV_TLS_PIN_SHA256", ad.SPKISHA256)
	} else {
		set("NICKSERV_TLS_PIN_SHA256", "")
	}
	set("NICKSERV_ALLOW_PLAINTEXT", strconv.FormatBool(plaintext))
	if ad.Strategy == "identify" {
		set("NICKSERV_SERVICES_SERVER", ad.ServicesServer)
	} else {
		set("NICKSERV_SERVICES_SERVER", "")
	}
	// Sign-up registers NickServ accounts where the services allow it
	// (Atheme); auth turns it off by itself elsewhere.
	set("NICKSERV_REGISTRATION", strconv.FormatBool(a.Registration == "open"))
	switch {
	case s.RPCToken != "":
		setSecret("NICKSERV_RPC_TOKEN", s.RPCToken, "entered")
	case existing["NICKSERV_RPC_TOKEN"] != "":
		setSecret("NICKSERV_RPC_TOKEN", existing["NICKSERV_RPC_TOKEN"], "kept")
	default:
		setSecret("NICKSERV_RPC_TOKEN", "", "")
	}
}

// BridgeConfPath is the bridge's config file in the install dir.
const BridgeConfPath = "bridge/evrd.conf"

// BridgeConf is the bridge's config (evrd's managed mode, docs/managed.md
// in 4evrd/evrd). Secrets (the managed token, the WEBIRC password, the
// secret key) come from .env through compose, never this file.
func BridgeConf(a Answers) string {
	ad := a.Addon
	h := a.HostNames()
	name := ad.ServerName
	if name == "" {
		name = h[RoleIRC]
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("# Boson's bridge in front of your IRC server: written by Boson setup.")
	w("# Boson apps connect here (irc.%s:6697) and get IRCv3 from the bridge;", a.Domain)
	w("# it connects each signed-in user to %s with WEBIRC.", net.JoinHostPort(ad.Host, strconv.Itoa(ad.Port)))
	w("# Running setup again rewrites it. Secrets are in .env (BRIDGE_*).")
	w("")
	w("listen ircs://:6697")
	w("listen irc+insecure://:6667   # the web client's gateway, compose network only")
	w("api :8098                     # the managed API, compose network only")
	w("tls files")
	w("tls_cert /certs/fullchain.pem")
	w("tls_key /certs/privkey.pem")
	w("db sqlite:/var/lib/evrd/evrd.db")
	w("history pebble:/var/lib/evrd/history")
	w("links_socket /run/evrd-links/links.sock")
	w("irc_host %s", h[RoleIRC])
	w("irc_sts off")
	w("network_directory off")
	w("update_check off")
	w("self_update no")
	w("max_clients 0")
	w("")
	w("managed yes")
	w("managed_network %s", a.NetworkName)
	plus := ""
	if ad.TLS {
		plus = "+"
	}
	w("managed_server %s %s%d", ad.Host, plus, ad.Port)
	if ad.TLS && !ad.TLSVerify {
		w("managed_tls_verify no")
		w("managed_fingerprint %s", ad.CertSHA256)
	}
	w("managed_server_name %s", name)
	w("managed_token_issuer https://%s", h[RoleAuth])
	w("managed_token_jwks http://auth:8080/.well-known/jwks.json")
	w("managed_token_audience boson")
	if ad.Software == "ngircd" {
		// ngIRCd reads WEBIRC's second parameter as the user name.
		w("managed_webirc_gateway {ident}")
	} else {
		w("managed_webirc_gateway boson")
	}
	w("managed_version %s", a.NetworkName)
	return b.String()
}

func hexSecret(n int) string { return secret.Hex(n) }

// RecommendAccounts is auth's strategy decision order (docs/nickserv.md
// "Choosing a strategy"): the services' RPC → NickServ accounts checked
// through it; SASL PLAIN linked to services → NickServ via SASL; nothing
// → Boson accounts. IDENTIFY is never recommended (it reads reply texts),
// only offered.
func RecommendAccounts(rpcFlavor, rpcProtocol string, rpcOK bool, saslPlain, nickserv bool) (accounts, strategy, why string) {
	switch {
	case rpcOK && rpcFlavor == "anope":
		return AccountsNickServ, "anope_rpc", "Your services answer on their RPC (Anope): Boson can check NickServ passwords there."
	case rpcOK && rpcFlavor == "atheme" && rpcProtocol == "xmlrpc":
		return AccountsNickServ, "atheme_xmlrpc", "Your services answer on their RPC (Atheme, XML-RPC): Boson can check NickServ passwords there."
	case rpcOK && rpcFlavor == "atheme":
		return AccountsNickServ, "atheme_jsonrpc", "Your services answer on their RPC (Atheme): Boson can check NickServ passwords there."
	case saslPlain && nickserv:
		return AccountsNickServ, "sasl", "Your IRC server offers SASL linked to NickServ: Boson can check NickServ passwords over IRC."
	case nickserv:
		return AccountsBoson, "", "Your server has NickServ but no services RPC and no SASL. Boson accounts are simpler; NickServ via IDENTIFY works too, as a last resort."
	}
	return AccountsBoson, "", "Your server has no NickServ: Boson keeps its own accounts."
}

// NickServAuthVersion is the first auth release with the NickServ account
// backend (boson-chat/auth#27); "" while none is released.
const NickServAuthVersion = "1.23.0"

// AuthNickServ: the auth image this stack installs can use NickServ
// accounts. Off, the wizard offers Boson accounts only.
var AuthNickServ = NickServAuthVersion != ""
