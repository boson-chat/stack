package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

//go:embed ircd.yaml.tmpl
var defaultTemplate string

//go:embed ergo.motd.tmpl
var motdTemplate string

type renderData struct {
	NetworkName      string
	IRCHost          string
	AuthHost         string
	AuthVerifyURL    string
	OperName         string
	OperPasswordHash string
	TLSCert, TLSKey  string
	Plaintext        bool
	// Plaintext listener port (IRC_PLAINTEXT_LISTEN_PORT / irc.plaintext_port).
	PlaintextPort     int
	WebSocket         bool
	WebSocketOrigins  []string
	StackSubnet       string
	CloakNetname      string
	OAuthBearer       bool
	PersistentHistory bool
	HistoryExpire     string
	LogLevel          string
	// Text channel creation (channel_creation from the auth service).
	ChannelCreation      channelPolicy
	OperatorOnlyCreation bool
	// channels.registration.auto-register / expire-after (patched Ergo).
	ChannelAutoRegister bool
	ChannelExpireAfter  string
	// channels.creator-grace (patched Ergo): how long an emptied unregistered
	// channel's creator keeps operator status (IRC_CHANNEL_CREATOR_GRACE /
	// irc.channel_creator_grace, default 10m; 0 = off).
	ChannelCreatorGrace string
	// accounts.multiclient.always-on / auto-away (IRC_ALWAYS_ON /
	// irc.always_on): disabled | opt-in | opt-out | mandatory. Default
	// opt-out: signed-in users stay connected (in their channels, history
	// kept, marked away) with no device open, unless they turn it off.
	AlwaysOn string
	// Ergo HTTP API (internal network only), for quicvoiced's founder/role
	// lookups. Empty token = API off.
	APIToken    string
	APIListener string
	// accounts.require-sasl: every client must log in (guest_access false
	// in auth, else IRC_REQUIRE_SASL), except from RequireSASLExempt.
	RequireSASL       bool
	RequireSASLExempt []string
	// IPLimitsExempt: nets exempt from the per-IP connection limits and
	// throttle (IRC_IP_LIMITS_EXEMPT; tests open many connections from one IP).
	IPLimitsExempt []string
	// Network roles with irc.oper, as oper classes (docs/roles.md).
	RoleClasses []roles.OperClass
	// Oper class RoleServ grants itself (nofakelag, via account_grants).
	ServiceClass string
	// Plaintext listener for in-stack services (RoleServ); "" = none.
	ServicesListener string
	// WEBIRC from the web gateway (webircgateway, the web profile): a bcrypt
	// hash of WEBIRC_PASSWORD and the addresses it may come from
	// (WEBIRC_HOSTS, default STACK_SUBNET). Empty hash = no WEBIRC.
	WebIRCPasswordHash string
	WebIRCHosts        []string
}

var bcryptRe = regexp.MustCompile(`^\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}$`)

func boolEnv(get func(string) string, k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(get(k))) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func strEnv(get func(string) string, k, def string) string {
	if v := strings.TrimSpace(get(k)); v != "" {
		return v
	}
	return def
}

func buildData(get func(string) string) (renderData, error) {
	d := renderData{
		NetworkName:       strEnv(get, "NETWORK_NAME", "Boson"),
		IRCHost:           strEnv(get, "IRC_HOST", ""),
		AuthHost:          strEnv(get, "AUTH_HOST", ""),
		AuthVerifyURL:     strEnv(get, "AUTH_VERIFY_URL", defaultVerifyURL),
		OperName:          strEnv(get, "OPER_NAME", "admin"),
		OperPasswordHash:  strEnv(get, "OPER_PASSWORD_HASH", ""),
		TLSCert:           strEnv(get, "TLS_CERT", "/certs/fullchain.pem"),
		TLSKey:            strEnv(get, "TLS_KEY", "/certs/privkey.pem"),
		Plaintext:         boolEnv(get, "IRC_PLAINTEXT", false),
		WebSocket:         boolEnv(get, "IRC_WEBSOCKET", true),
		StackSubnet:       strEnv(get, "STACK_SUBNET", "10.203.53.0/24"),
		OAuthBearer:       boolEnv(get, "IRC_OAUTHBEARER", true),
		PersistentHistory: boolEnv(get, "IRC_PERSISTENT_HISTORY", true),
		HistoryExpire:     strEnv(get, "IRC_HISTORY_EXPIRE", "1w"),
		LogLevel:          strEnv(get, "IRC_LOG_LEVEL", "info"),
		APIToken:          strEnv(get, "ERGO_API_TOKEN", ""),
		APIListener:       strEnv(get, "ERGO_API_LISTENER", ":8089"),
		RequireSASL:       boolEnv(get, "IRC_REQUIRE_SASL", false),
		RequireSASLExempt: []string{"localhost"},
		ServiceClass:      serviceClass,
		ServicesListener:  strEnv(get, "IRC_SERVICES_LISTENER", ":6680"),
	}
	d.PlaintextPort = 6667
	if v := strEnv(get, "IRC_PLAINTEXT_LISTEN_PORT", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return d, fmt.Errorf("IRC_PLAINTEXT_LISTEN_PORT must be a port number, got %q", v)
		}
		d.PlaintextPort = n
	}
	if d.Plaintext && (d.PlaintextPort == 6697 || d.PlaintextPort == 8097 || strings.HasSuffix(d.ServicesListener, ":"+strconv.Itoa(d.PlaintextPort))) {
		return d, fmt.Errorf("plaintext port %d is already used by another listener", d.PlaintextPort)
	}
	if strings.EqualFold(d.ServicesListener, "off") {
		d.ServicesListener = ""
	}
	if d.APIToken != "" && (len(d.APIToken) < 32 || strings.ContainsAny(d.APIToken, " \t\r\n")) {
		return d, fmt.Errorf("ERGO_API_TOKEN must be at least 32 printable characters without spaces (openssl rand -hex 32)")
	}
	if d.IRCHost == "" {
		return d, fmt.Errorf("IRC_HOST is not set (see .env.example)")
	}
	if !bcryptRe.MatchString(d.OperPasswordHash) {
		return d, fmt.Errorf("OPER_PASSWORD_HASH must be a bcrypt hash from `ergo genpasswd` (scripts/setup.sh makes one)")
	}
	if d.AuthHost == "" {
		d.AuthHost = "auth." + strEnv(get, "DOMAIN", d.IRCHost)
	}
	// Cloak suffix: the network name, lowercased, DNS-safe.
	d.CloakNetname = strings.Trim(regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(d.NetworkName), "-"), "-")
	if d.CloakNetname == "" {
		d.CloakNetname = "irc"
	}
	if v := get("IRC_REQUIRE_SASL_EXEMPT"); v != "" {
		d.RequireSASLExempt = nil
		for _, n := range strings.Split(v, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if !validNet(n) {
				return d, fmt.Errorf("IRC_REQUIRE_SASL_EXEMPT: %q is not an IP, CIDR or localhost", n)
			}
			d.RequireSASLExempt = append(d.RequireSASLExempt, n)
		}
	}
	for _, n := range strings.Split(get("IRC_IP_LIMITS_EXEMPT"), ",") {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if !validNet(n) {
			return d, fmt.Errorf("IRC_IP_LIMITS_EXEMPT: %q is not an IP, CIDR or localhost", n)
		}
		d.IPLimitsExempt = append(d.IPLimitsExempt, n)
	}
	d.WebIRCHosts = []string{d.StackSubnet}
	if v := get("WEBIRC_HOSTS"); v != "" {
		d.WebIRCHosts = nil
		for _, n := range strings.Split(v, ",") {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			if !validNet(n) {
				return d, fmt.Errorf("WEBIRC_HOSTS: %q is not an IP, CIDR or localhost", n)
			}
			d.WebIRCHosts = append(d.WebIRCHosts, n)
		}
	}
	if v := get("IRC_WEBSOCKET_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				d.WebSocketOrigins = append(d.WebSocketOrigins, o)
			}
		}
	} else if dom := strEnv(get, "DOMAIN", ""); dom != "" {
		d.WebSocketOrigins = []string{"https://" + dom, "https://*." + dom}
	}
	return d, nil
}

// validNet reports whether s is something Ergo's require-sasl.exempted
// accepts: localhost, an IP or a CIDR.
func validNet(s string) bool {
	if s == "localhost" {
		return true
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// q quotes a value as a YAML double-quoted scalar (JSON strings are valid YAML).
func q(v any) string {
	b, _ := json.Marshal(fmt.Sprint(v))
	return string(b)
}

// render writes ircd.yaml and ergo.motd for the network settings and the role oper
// classes. changed reports whether ircd.yaml's content changed (only then
// does Ergo need a rehash).
func render(get func(string) string, tmplPath, dir string, st netSettings, classes []roles.OperClass) (changed bool, err error) {
	d, err := buildData(get)
	if err != nil {
		return false, err
	}
	if d.WebIRCPasswordHash, err = webircHash(get("WEBIRC_PASSWORD"), dir); err != nil {
		return false, err
	}
	d.RoleClasses = classes
	d.ChannelCreation = st.ChannelCreation
	d.OperatorOnlyCreation = st.ChannelCreation == policyAdmins
	d.RequireSASL = requireSASL(get, st)
	if d.ChannelAutoRegister, d.ChannelExpireAfter, err = channelLifecycle(get, st); err != nil {
		return false, err
	}
	d.ChannelCreatorGrace = strEnv(get, "IRC_CHANNEL_CREATOR_GRACE", "10m")
	if !ergoDurRe.MatchString(d.ChannelCreatorGrace) {
		return false, fmt.Errorf("IRC_CHANNEL_CREATOR_GRACE must be a duration like 10m, 1h or 0, got %q", d.ChannelCreatorGrace)
	}
	d.AlwaysOn = strings.ToLower(strEnv(get, "IRC_ALWAYS_ON", "opt-out"))
	switch d.AlwaysOn {
	case "disabled", "opt-in", "opt-out", "mandatory":
	default:
		return false, fmt.Errorf("IRC_ALWAYS_ON must be disabled, opt-in, opt-out or mandatory, got %q", d.AlwaysOn)
	}
	src := defaultTemplate
	if tmplPath != "" {
		b, err := os.ReadFile(tmplPath)
		if err != nil {
			return false, err
		}
		src = string(b)
	}
	changed, err = writeTemplate(filepath.Join(dir, "ircd.yaml"), src, d, 0o600)
	if err != nil {
		return false, err
	}
	motd := motdTemplate
	if p := get("IRC_MOTD_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return false, err
		}
		motd = string(b)
	}
	_, err = writeTemplate(filepath.Join(dir, "ergo.motd"), motd, d, 0o644)
	return changed, err
}

// writeTemplate renders src to path atomically, skipping the write when the
// content is unchanged.
func writeTemplate(path, src string, d renderData, mode os.FileMode) (changed bool, err error) {
	t, err := template.New(filepath.Base(path)).Funcs(template.FuncMap{"q": q}).Option("missingkey=error").Parse(src)
	if err != nil {
		return false, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return false, err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, buf.Bytes()) {
		return false, nil
	}
	return true, writeFileAtomic(path, buf.Bytes(), mode)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
