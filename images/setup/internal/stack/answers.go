// Package stack turns the wizard's answers into the stack's own config: the
// same .env and config/boson.yaml that scripts/setup.sh writes (README,
// docs/config.md), and reads them back from an existing install.
package stack

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

// Answers are the wizard's non-secret choices. They are kept in the setup
// state file between restarts; secrets never are (see Secrets).
type Answers struct {
	NetworkName string `json:"network_name"`
	Domain      string `json:"domain"`
	// Serve https://DOMAIN/.well-known/boson.json from this server (only
	// when the bare domain points here).
	Wellknown bool `json:"wellknown"`

	TLS   TLSAnswers   `json:"tls"`
	Admin AdminAnswers `json:"admin"`

	Voice            bool         `json:"voice"`
	Video            bool         `json:"video"`
	Files            FilesAnswers `json:"files"`
	DirectoryListing bool         `json:"directory_listing"`
	OwnDirectory     bool         `json:"own_directory"`
	SMTP             SMTPAnswers  `json:"smtp"`
	// open | invite | closed (auth.registration)
	Registration      string `json:"registration"`
	EmailVerification bool   `json:"email_verification"`
	Guests            bool   `json:"guests"`
	GuestVoice        bool   `json:"guest_voice"`
}

type TLSAnswers struct {
	// Let's Encrypt account email (TLS_ISSUER).
	Email string `json:"email"`
	// production | staging | custom
	CA        string `json:"ca"`
	CustomURL string `json:"custom_url"`
}

type AdminAnswers struct {
	Username string `json:"username"`
	Email    string `json:"email"`
}

type FilesAnswers struct {
	Enabled bool `json:"enabled"`
	// local | minio | s3
	Storage     string `json:"storage"`
	S3Endpoint  string `json:"s3_endpoint"`
	S3Bucket    string `json:"s3_bucket"`
	S3Region    string `json:"s3_region"`
	S3PathStyle bool   `json:"s3_path_style"`
}

type SMTPAnswers struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	From string `json:"from"`
	// starttls | tls | none
	TLS  string `json:"tls"`
	User string `json:"user"`
}

// Secrets typed into the wizard. Held in memory only; SMTP and S3 ones are
// written to .env (0600) at install, the admin password only goes to auth.
type Secrets struct {
	SMTPPassword  string `json:"smtp_password,omitempty"`
	S3AccessKey   string `json:"s3_access_key,omitempty"`
	S3SecretKey   string `json:"s3_secret_key,omitempty"`
	AdminPassword string `json:"admin_password,omitempty"`
}

// Defaults for a new install.
func Defaults() Answers {
	return Answers{
		NetworkName:       "Boson",
		TLS:               TLSAnswers{CA: "production"},
		Voice:             true,
		Video:             false,
		Files:             FilesAnswers{Enabled: true, Storage: "local", S3Region: "auto", S3PathStyle: true},
		DirectoryListing:  true,
		SMTP:              SMTPAnswers{Port: 587, TLS: "starttls"},
		Registration:      "open",
		EmailVerification: false,
		Guests:            true,
	}
}

// Normalize trims input and fixes combinations that can't work (video
// without voice, guest voice without guests).
func (a *Answers) Normalize() {
	a.NetworkName = strings.TrimSpace(a.NetworkName)
	a.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a.Domain)), ".")
	a.TLS.Email = strings.TrimSpace(a.TLS.Email)
	a.TLS.CustomURL = strings.TrimSpace(a.TLS.CustomURL)
	a.Admin.Username = strings.TrimSpace(a.Admin.Username)
	a.Admin.Email = strings.TrimSpace(a.Admin.Email)
	a.SMTP.Host = strings.TrimSpace(a.SMTP.Host)
	a.SMTP.From = strings.TrimSpace(a.SMTP.From)
	a.SMTP.User = strings.TrimSpace(a.SMTP.User)
	if a.TLS.CA == "" {
		a.TLS.CA = "production"
	}
	if a.SMTP.TLS == "" {
		a.SMTP.TLS = "starttls"
	}
	if a.SMTP.Port == 0 {
		a.SMTP.Port = 587
	}
	if a.Registration == "" {
		a.Registration = "open"
	}
	if a.Files.Storage == "" {
		a.Files.Storage = "local"
	}
	if a.Files.S3Region == "" {
		a.Files.S3Region = "auto"
	}
	if !a.Voice {
		a.Video = false
	}
	if !a.Guests || !a.Voice {
		a.GuestVoice = false
	}
	if a.TLS.Email == "" {
		a.TLS.Email = a.Admin.Email
	}
}

var (
	labelRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	usernameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,29}$`)
	// networkNameRe: Ergo refuses a NETWORK token with spaces (and so does
	// the IRC ISUPPORT grammar).
	networkNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
)

// ValidDomain: a DNS name with at least two labels.
func ValidDomain(d string) bool {
	if len(d) > 200 {
		return false
	}
	parts := strings.Split(d, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !labelRe.MatchString(p) {
			return false
		}
	}
	return true
}

// FieldError names the field a message is about, for the UI.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// safeText: no quotes, backslashes or control characters (it goes into
// .env and YAML).
func safeText(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\'' || r == '"' || r == '\\' || r == '`' || r == '$' {
			return false
		}
	}
	return true
}

func validEmail(s string) bool {
	if s == "" || len(s) > 254 || !safeText(s) || strings.ContainsAny(s, " <>,;") {
		return false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s {
		return false
	}
	at := strings.LastIndex(s, "@")
	return at > 0 && at < len(s)-1
}

// Validate checks one section ("domain", "tls", "admin", "features") or
// all of them (""). needPassword: a new admin needs a password.
func (a Answers) Validate(section string, s Secrets, needPassword bool) []FieldError {
	var errs []FieldError
	add := func(f, m string) { errs = append(errs, FieldError{f, m}) }
	all := section == ""
	if all || section == "domain" {
		if !networkNameRe.MatchString(a.NetworkName) {
			// It's also IRC's NETWORK= (ISUPPORT): one token, no spaces.
			add("network_name", "One word, up to 32 characters: letters, digits, - _ and . (it's also the IRC network name, which can't have spaces).")
		}
		if !ValidDomain(a.Domain) {
			add("domain", "Enter a domain name like chat.example.com (no https://, no path).")
		}
	}
	if all || section == "tls" {
		if !validEmail(a.TLS.Email) {
			add("tls.email", "Let's Encrypt needs an email address for expiry notices.")
		}
		switch a.TLS.CA {
		case "production", "staging":
		case "custom":
			if !strings.HasPrefix(a.TLS.CustomURL, "https://") || !safeText(a.TLS.CustomURL) {
				add("tls.custom_url", "The ACME directory URL must start with https://.")
			}
		default:
			add("tls.ca", "Pick a certificate authority.")
		}
	}
	if all || section == "admin" {
		if !usernameRe.MatchString(a.Admin.Username) {
			add("admin.username", "2–30 characters: letters, digits, - and _, starting with a letter. It's also your IRC nick.")
		}
		if !validEmail(a.Admin.Email) {
			add("admin.email", "Enter a valid email address.")
		}
		if needPassword {
			if len(s.AdminPassword) < 10 {
				add("admin.password", "Use at least 10 characters.")
			} else if len(s.AdminPassword) > 256 || strings.ContainsAny(s.AdminPassword, "\r\n") {
				add("admin.password", "The password can't contain line breaks or be longer than 256 characters.")
			}
		}
	}
	if all || section == "features" {
		switch a.Registration {
		case "open", "invite", "closed":
		default:
			add("registration", "Pick who can sign up.")
		}
		if a.Files.Enabled {
			switch a.Files.Storage {
			case "local", "minio":
			case "s3":
				if !strings.HasPrefix(a.Files.S3Endpoint, "https://") && !strings.HasPrefix(a.Files.S3Endpoint, "http://") || !safeText(a.Files.S3Endpoint) {
					add("files.s3_endpoint", "The S3 endpoint is a URL, like https://s3.eu-central-1.amazonaws.com.")
				}
				if a.Files.S3Bucket == "" || !safeText(a.Files.S3Bucket) || strings.ContainsAny(a.Files.S3Bucket, " /") {
					add("files.s3_bucket", "Enter the bucket name.")
				}
				if !safeText(a.Files.S3Region) {
					add("files.s3_region", "Invalid region.")
				}
			default:
				add("files.storage", "Pick where uploads are stored.")
			}
		}
		if a.SMTP.Host != "" {
			if !ValidDomain(a.SMTP.Host) && !isIP(a.SMTP.Host) || !safeText(a.SMTP.Host) {
				add("smtp.host", "Enter the mail server's hostname.")
			}
			if a.SMTP.Port < 1 || a.SMTP.Port > 65535 {
				add("smtp.port", "Enter a port between 1 and 65535.")
			}
			if a.SMTP.From == "" || !safeText(a.SMTP.From) {
				add("smtp.from", "Enter the sender address, like Boson <noreply@example.com>.")
			}
			switch a.SMTP.TLS {
			case "starttls", "tls", "none":
			default:
				add("smtp.tls", "Pick how to connect.")
			}
			if !safeText(a.SMTP.User) || !safeText(s.SMTPPassword) && s.SMTPPassword != "" {
				add("smtp.user", "The user name and password can't contain quotes, $, backslashes or line breaks.")
			}
		}
		if a.EmailVerification && a.SMTP.Host == "" {
			add("email_verification", "Email verification needs an outgoing mail server (SMTP).")
		}
	}
	return errs
}

func isIP(s string) bool {
	return regexp.MustCompile(`^[0-9.]+$|^[0-9a-fA-F:]+$`).MatchString(s)
}

// Host roles, in the order the DNS step lists them.
const (
	RoleIRC       = "irc"
	RoleAuth      = "auth"
	RoleWeb       = "web"
	RoleAdmin     = "admin"
	RoleAPI       = "api"
	RoleVoice     = "voice"
	RoleVideo     = "video"
	RoleFiles     = "files"
	RoleDirectory = "directory"
	RoleDomain    = "domain"
)

// HostNames: every hostname the stack uses (also the ones a feature that's
// off would use; .env always names them, as scripts/setup.sh does).
func (a Answers) HostNames() map[string]string {
	d := a.Domain
	return map[string]string{
		RoleIRC: "irc." + d, RoleAuth: "auth." + d, RoleWeb: "chat." + d, RoleAdmin: "admin." + d,
		RoleAPI: "api." + d, RoleVoice: "voice." + d, RoleVideo: "video." + d, RoleFiles: "files." + d,
		RoleDirectory: "directory." + d, RoleDomain: d,
	}
}

// Host is one hostname this install needs in DNS (and a certificate for).
type Host struct {
	Role string `json:"role"`
	Name string `json:"name"`
	What string `json:"what"`
}

// NeededHosts: the hostnames that must point at this server for the
// chosen features, in CERT_HOSTS order.
func (a Answers) NeededHosts() []Host {
	h := a.HostNames()
	out := []Host{
		{RoleIRC, h[RoleIRC], "IRC (port 6697)"},
		{RoleAuth, h[RoleAuth], "accounts and sign-in"},
		{RoleWeb, h[RoleWeb], "the web client"},
		{RoleAdmin, h[RoleAdmin], "the admin panel"},
		{RoleAPI, h[RoleAPI], "the Boson API (profiles)"},
	}
	if a.Voice {
		out = append(out, Host{RoleVoice, h[RoleVoice], "voice"})
	}
	if a.Video {
		out = append(out, Host{RoleVideo, h[RoleVideo], "video and screen share"})
	}
	if a.Files.Enabled {
		out = append(out, Host{RoleFiles, h[RoleFiles], "file uploads"})
	}
	if a.OwnDirectory {
		out = append(out, Host{RoleDirectory, h[RoleDirectory], "your network directory"})
	}
	if a.Wellknown {
		out = append(out, Host{RoleDomain, h[RoleDomain], "app discovery (/.well-known/boson.json)"})
	}
	return out
}

// Profiles: COMPOSE_PROFILES for these answers.
func (a Answers) Profiles() []string {
	p := []string{"irc", "auth", "api", "web", "admin", "metrics"}
	if a.Voice {
		p = append(p, "voice")
	}
	if a.Video {
		p = append(p, "video")
	}
	if a.Files.Enabled {
		p = append(p, "files")
		if a.Files.Storage == "minio" {
			p = append(p, "minio")
		}
	}
	if a.OwnDirectory {
		p = append(p, "directory")
	}
	if a.Wellknown {
		p = append(p, "wellknown")
	}
	return p
}

// ACMEServer: ACME_CA_SERVER (empty = Let's Encrypt production, compose's default).
func (a Answers) ACMEServer() string {
	switch a.TLS.CA {
	case "staging":
		return "https://acme-staging-v02.api.letsencrypt.org/directory"
	case "custom":
		return a.TLS.CustomURL
	}
	return ""
}

func (a Answers) String() string { return fmt.Sprintf("%s (%s)", a.NetworkName, a.Domain) }
