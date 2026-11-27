package stack

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/boson-chat/stack/images/setup/internal/secret"
	"golang.org/x/crypto/bcrypt"
)

// Generator makes each generated secret once per wizard run, so the review
// and the install see the same values. Values from an existing .env always
// win over it.
type Generator struct {
	mu   sync.Mutex
	vals map[string]string
}

func NewGenerator() *Generator { return &Generator{vals: map[string]string{}} }

// Get is the run's value for key, made with mk the first time.
func (g *Generator) Get(key string, mk func() string) string { return g.get(key, mk) }

func (g *Generator) get(key string, mk func() string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if v, ok := g.vals[key]; ok {
		return v
	}
	v := mk()
	g.vals[key] = v
	return v
}

// generated secrets, as scripts/setup.sh makes them.
var secretGen = []struct {
	key string
	mk  func() string
}{
	{"POSTGRES_PASSWORD", func() string { return secret.Alnum(40) }},
	{"AUTH_KEY_ENCRYPTION_KEY", func() string { return secret.Base64(32) }},
	{"INTERNAL_API_TOKEN", func() string { return secret.Hex(32) }},
	{"ERGO_API_TOKEN", func() string { return secret.Hex(32) }},
	{"ROLESERV_AUTH_TOKEN", func() string { return secret.Hex(32) }},
	{"ADMIN_SESSION_KEY", func() string { return secret.Base64(32) }},
	{"API_DB_PASSWORD", func() string { return secret.Alnum(40) }},
	{"API_INTERNAL_TOKEN", func() string { return secret.Hex(32) }},
	{"DIRECTORY_DB_PASSWORD", func() string { return secret.Alnum(40) }},
	{"DIRECTORY_INTERNAL_TOKEN", func() string { return secret.Hex(32) }},
	{"FILES_DB_PASSWORD", func() string { return secret.Alnum(40) }},
	{"FILES_INTERNAL_TOKEN", func() string { return secret.Hex(32) }},
	{"FILES_GUEST_SECRET", func() string { return secret.Hex(32) }},
	{"MINIO_ROOT_USER", func() string { return "bosonroot" + secret.Alnum(6) }},
	{"MINIO_ROOT_PASSWORD", func() string { return secret.Alnum(40) }},
	{"VOICE_JWT_SECRET", func() string { return secret.Alnum(64) }},
	{"WEBIRC_PASSWORD", func() string { return secret.Hex(24) }},
	{"VIDEO_CONTROL_TOKEN", func() string { return secret.Hex(32) }},
}

// SecretKeys: every .env key whose value is a secret (masked, never logged).
var SecretKeys = func() map[string]bool {
	m := map[string]bool{"OPER_PASSWORD_HASH": true, "SMTP_PASSWORD": true, "SMTP_USER": true,
		"FILES_S3_ACCESS_KEY": true, "FILES_S3_SECRET_KEY": true, "NICK_CLAIM_POP3_PASSWORD": true,
		"WEBIRC_PASSWORDS": true, "NICKSERV_RPC_TOKEN": true}
	for _, b := range bridgeSecrets {
		m[b.key] = true
	}
	for _, s := range secretGen {
		m[s.key] = true
	}
	return m
}()

// UpdatesProxyURL: ADMIN_UPDATES_DOCKER_PROXY_URL with the updates profile
// (compose.yaml's docker-proxy-updates).
const UpdatesProxyURL = "http://docker-proxy-updates:2375"

// ConfigKey is one config/boson.yaml key the wizard sets.
type ConfigKey struct {
	Path  []string `json:"path"`
	Value any      `json:"value"`
	// IfAbsent: only on a new file or when the key isn't there (voice rooms
	// the admin may have edited since).
	IfAbsent bool `json:"-"`
	// IfPresent: only change a key that's there (clearing SMTP).
	IfPresent bool `json:"-"`
}

// Plan is everything the install writes.
type Plan struct {
	Answers  Answers     `json:"answers"`
	Profiles []string    `json:"profiles"`
	Hosts    []Host      `json:"hosts"`
	Env      []EnvVar    `json:"env"`
	Config   []ConfigKey `json:"config"`
	// OperPassword: the emergency /OPER password when it was generated now
	// (shown once on the last page); empty when the existing hash is kept.
	OperPassword string `json:"-"`
	// Files: other files the install writes (the bridge's config), relative
	// to the install dir. Never secrets.
	Files []PlanFile `json:"files,omitempty"`
}

// PlanFile is a generated file the install writes.
type PlanFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// EnvValue looks a key up in the plan.
func (p Plan) EnvValue(key string) string {
	for _, v := range p.Env {
		if v.Key == key {
			return v.Value
		}
	}
	return ""
}

// SecretValues: every secret value of the plan (for redacting output).
func (p Plan) SecretValues() []string {
	var out []string
	for _, v := range p.Env {
		if v.Secret && len(v.Value) >= 4 {
			out = append(out, v.Value)
		}
	}
	if p.OperPassword != "" {
		out = append(out, p.OperPassword)
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// BuildPlan works out .env and config/boson.yaml for the answers. existing
// is the current .env (nil for a new install): its secrets are kept.
func BuildPlan(a Answers, s Secrets, existing map[string]string, gen *Generator, dockerGID string) (Plan, error) {
	a.Normalize()
	if errs := a.Validate("", s, false); len(errs) > 0 {
		return Plan{}, fmt.Errorf("%s", errs[0].Error())
	}
	h := a.HostNames()
	p := Plan{Answers: a, Profiles: a.Profiles(), Hosts: a.NeededHosts()}
	var certHosts []string
	for _, x := range p.Hosts {
		certHosts = append(certHosts, x.Name)
	}
	set := func(k, v string) { p.Env = append(p.Env, EnvVar{Key: k, Value: v}) }
	setSecret := func(k, v, origin string) {
		p.Env = append(p.Env, EnvVar{Key: k, Value: v, Secret: true, Origin: origin})
	}
	keepOr := func(k string, mk func() string) {
		if v := existing[k]; v != "" {
			setSecret(k, v, "kept")
			return
		}
		setSecret(k, gen.get(k, mk), "generated")
	}

	set("COMPOSE_PROFILES", strings.Join(p.Profiles, ","))
	set("NETWORK_NAME", a.NetworkName)
	set("DOMAIN", a.Domain)
	set("IRC_HOST", h[RoleIRC])
	set("VOICE_HOST", h[RoleVoice])
	set("AUTH_HOST", h[RoleAuth])
	set("ADMIN_HOST", h[RoleAdmin])
	set("API_HOST", h[RoleAPI])
	set("WEB_HOST", h[RoleWeb])
	set("DIRECTORY_HOST", h[RoleDirectory])
	set("FILES_HOST", h[RoleFiles])
	// VIDEO_HOST set = video in voice channels is on (docs/video.md).
	if a.Video {
		set("VIDEO_HOST", h[RoleVideo])
	} else {
		set("VIDEO_HOST", "")
	}
	set("CERT_HOSTS", strings.Join(certHosts, ","))
	set("ADMIN_EMAIL", a.Admin.Email)
	set("TLS_ISSUER", a.TLS.Email)
	set("ACME_CA_SERVER", a.ACMEServer())
	proxyEnv(a, existing, set)
	set("OPER_NAME", "admin")
	if a.IsAddon() {
		// No Ergo, so no emergency oper: keep a hash that's there.
		if v := existing["OPER_PASSWORD_HASH"]; v != "" {
			setSecret("OPER_PASSWORD_HASH", v, "kept")
		}
	} else if v := existing["OPER_PASSWORD_HASH"]; v != "" {
		setSecret("OPER_PASSWORD_HASH", v, "kept")
	} else {
		pw := gen.get("_OPER_PASSWORD", func() string { return secret.Alnum(20) })
		hash := gen.get("OPER_PASSWORD_HASH", func() string {
			b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
			if err != nil {
				panic(err)
			}
			return string(b)
		})
		setSecret("OPER_PASSWORD_HASH", hash, "generated")
		p.OperPassword = pw
	}
	set("IRC_PLAINTEXT", "false")
	set("REGISTRATION", a.Registration)
	set("REQUIRE_EMAIL_VERIFICATION", strconv.FormatBool(a.EmailVerification))
	for _, sg := range secretGen {
		if a.IsAddon() && sg.key == "ERGO_API_TOKEN" {
			// No Ergo: auth and quicvoiced ask nobody for IRC roles.
			setSecret(sg.key, "", "")
			continue
		}
		keepOr(sg.key, sg.mk)
	}
	set("DOCKER_GID", dockerGID)
	if v := existing["VOICE_ROOMS"]; v != "" {
		set("VOICE_ROOMS", v)
	} else {
		set("VOICE_ROOMS", "general:General")
	}

	// Outgoing mail: host/port/from/tls also go to config/boson.yaml (it
	// wins); the user and password are secrets and stay here.
	set("SMTP_HOST", a.SMTP.Host)
	set("SMTP_PORT", strconv.Itoa(a.SMTP.Port))
	set("SMTP_FROM", a.SMTP.From)
	set("SMTP_TLS", a.SMTP.TLS)
	enteredOrKept := func(k, entered string) {
		switch {
		case entered != "":
			setSecret(k, entered, "entered")
		case existing[k] != "":
			setSecret(k, existing[k], "kept")
		default:
			setSecret(k, "", "")
		}
	}
	if a.SMTP.Host == "" {
		setSecret("SMTP_USER", "", "")
		setSecret("SMTP_PASSWORD", "", "")
	} else {
		if a.SMTP.User != "" {
			setSecret("SMTP_USER", a.SMTP.User, "entered")
		} else {
			setSecret("SMTP_USER", "", "")
		}
		enteredOrKept("SMTP_PASSWORD", s.SMTPPassword)
	}

	// Directory: listing in directory.boson.chat (admin panel) and/or a
	// directory of your own (directory profile).
	set("ADMIN_DIRECTORY_REGISTRATION", strconv.FormatBool(a.DirectoryListing))
	origins := []string{"https://" + h[RoleWeb]}
	if a.OwnDirectory {
		set("DIRECTORY_INCLUDE_MAIN", "true")
		set("ADMIN_DIRECTORY_LOCAL_URL", "http://directory:8080")
		origins = append(origins, "https://"+h[RoleDirectory])
	} else {
		set("ADMIN_DIRECTORY_LOCAL_URL", "")
	}
	set("AUTH_CORS_ALLOWED_ORIGINS", strings.Join(origins, ","))

	// Applying updates from the admin panel (docs/updates.md): the updates
	// profile (in Profiles) and the panel's socket proxy for it.
	if a.ApplyUpdates {
		set("ADMIN_UPDATES_DOCKER_PROXY_URL", UpdatesProxyURL)
	} else {
		set("ADMIN_UPDATES_DOCKER_PROXY_URL", "")
	}

	// Files.
	set("FILES_ENABLED", strconv.FormatBool(a.Files.Enabled))
	if a.Files.Enabled {
		set("ADMIN_FILES_INTERNAL_URL", "http://files:3001")
	} else {
		// No files profile: the admin panel's Uploads page says so.
		set("ADMIN_FILES_INTERNAL_URL", "")
	}
	switch {
	case a.Files.Enabled && a.Files.Storage == "s3":
		enteredOrKept("FILES_S3_ACCESS_KEY", s.S3AccessKey)
		enteredOrKept("FILES_S3_SECRET_KEY", s.S3SecretKey)
		set("FILES_S3_BUCKET", a.Files.S3Bucket)
	default:
		// minio-init creates this user for the files service.
		keepOr("FILES_S3_ACCESS_KEY", func() string { return "bosonfiles" + secret.Alnum(6) })
		keepOr("FILES_S3_SECRET_KEY", func() string { return secret.Alnum(40) })
		if a.Files.Storage == "minio" {
			set("FILES_S3_BUCKET", "boson-files")
		}
	}

	if a.IsAddon() {
		addonEnv(&p, a, s, existing, gen, set, setSecret, keepOr)
		p.Files = append(p.Files, PlanFile{Path: BridgeConfPath, Content: BridgeConf(a, existing["EVRD_IMAGE"])})
	} else {
		// Back from an add-on install: its keys go (compose's defaults are
		// the fresh path's).
		for _, k := range bridgeEnvKeys {
			if _, ok := existing[k]; ok && existing["BRIDGE_CERT_HOST"] != "" {
				if SecretKeys[k] {
					setSecret(k, "", "")
				} else {
					set(k, "")
				}
			}
		}
	}

	// config/boson.yaml
	c := func(v any, path ...string) { p.Config = append(p.Config, ConfigKey{Path: path, Value: v}) }
	c(a.NetworkName, "network", "name")
	c(a.Registration, "auth", "registration")
	c(a.EmailVerification, "auth", "require_email_verification")
	if a.NickServAccounts() {
		// Sign-up with NickServ accounts (auth docs/nickserv.md).
		c(a.RegistersNickServ(), "auth", "nickserv", "registration")
		if a.NickServSignupMessage != "" {
			c(a.NickServSignupMessage, "auth", "nickserv", "signup_message")
		} else {
			p.Config = append(p.Config, ConfigKey{Path: []string{"auth", "nickserv", "signup_message"}, Value: "", IfPresent: true})
		}
	}
	c(a.Guests, "auth", "guest_access")
	c(a.GuestVoice, "auth", "guest_voice")
	c(origins, "auth", "cors_allowed_origins")
	if a.SMTP.Host != "" {
		c(a.SMTP.Host, "auth", "smtp", "host")
		c(a.SMTP.Port, "auth", "smtp", "port")
		c(a.SMTP.From, "auth", "smtp", "from")
		c(a.SMTP.TLS, "auth", "smtp", "tls")
	} else {
		p.Config = append(p.Config, ConfigKey{Path: []string{"auth", "smtp", "host"}, Value: "", IfPresent: true})
	}
	p.Config = append(p.Config, ConfigKey{Path: []string{"voice", "rooms"}, IfAbsent: true,
		Value: []map[string]any{{"id": "general", "name": "General"}}})
	c(a.Files.Enabled, "files", "enabled")
	if a.Files.Enabled {
		c(a.Guests, "files", "guest", "enabled")
		switch a.Files.Storage {
		case "minio":
			c("s3", "files", "storage", "driver")
			c("http://minio:9000", "files", "storage", "s3", "endpoint")
			c("boson-files", "files", "storage", "s3", "bucket")
			c("auto", "files", "storage", "s3", "region")
			c(true, "files", "storage", "s3", "path_style")
		case "s3":
			c("s3", "files", "storage", "driver")
			c(a.Files.S3Endpoint, "files", "storage", "s3", "endpoint")
			c(a.Files.S3Bucket, "files", "storage", "s3", "bucket")
			c(a.Files.S3Region, "files", "storage", "s3", "region")
			c(a.Files.S3PathStyle, "files", "storage", "s3", "path_style")
		default:
			c("local", "files", "storage", "driver")
		}
	}
	c(a.DirectoryListing, "admin", "directory_registration")
	// Federated sign-in (docs/federation.md); the admin panel's Federation
	// page edits these later.
	c(a.Federation, "auth", "federation", "mode")
	if a.Federation == FederationAllowlist {
		c(a.FederationAllowList(), "auth", "federation", "allow")
	}
	return p, nil
}

// MaskedEnv renders the plan's .env lines for the review page: secrets as
// dots with where they came from, never their value.
func MaskedEnv(p Plan) string {
	var b strings.Builder
	for _, v := range p.Env {
		val := quoteEnv(v.Value)
		if v.Secret && v.Value != "" {
			val = "••••••••  # " + map[string]string{"kept": "kept from your .env", "generated": "new, random",
				"entered": "as you entered it"}[v.Origin]
		}
		fmt.Fprintf(&b, "%s=%s\n", v.Key, val)
	}
	return b.String()
}
