package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed ircd.yaml.tmpl
var defaultTemplate string

//go:embed ergo.motd.tmpl
var motdTemplate string

type renderData struct {
	NetworkName       string
	IRCHost           string
	AuthHost          string
	AuthVerifyURL     string
	OperName          string
	OperPasswordHash  string
	TLSCert, TLSKey   string
	Plaintext         bool
	WebSocket         bool
	WebSocketOrigins  []string
	StackSubnet       string
	CloakNetname      string
	OAuthBearer       bool
	PersistentHistory bool
	HistoryExpire     string
	LogLevel          string
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

// q quotes a value as a YAML double-quoted scalar (JSON strings are valid YAML).
func q(v any) string {
	b, _ := json.Marshal(fmt.Sprint(v))
	return string(b)
}

func render(get func(string) string, tmplPath, dir string) error {
	d, err := buildData(get)
	if err != nil {
		return err
	}
	src := defaultTemplate
	if tmplPath != "" {
		b, err := os.ReadFile(tmplPath)
		if err != nil {
			return err
		}
		src = string(b)
	}
	if err := writeTemplate(filepath.Join(dir, "ircd.yaml"), src, d, 0o600); err != nil {
		return err
	}
	motd := motdTemplate
	if p := get("IRC_MOTD_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		motd = string(b)
	}
	return writeTemplate(filepath.Join(dir, "ergo.motd"), motd, d, 0o644)
}

func writeTemplate(path, src string, d renderData, mode os.FileMode) error {
	t, err := template.New(filepath.Base(path)).Funcs(template.FuncMap{"q": q}).Option("missingkey=error").Parse(src)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := t.Execute(f, d); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
