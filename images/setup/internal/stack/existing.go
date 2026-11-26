package stack

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Existing is what an install directory already holds.
type Existing struct {
	// Found: there is a .env (a previous install, by this wizard or by
	// scripts/setup.sh).
	Found  bool
	Env    map[string]string
	EnvRaw string
	// Config: config/boson.yaml, nil when missing.
	Config []byte
}

func LoadExisting(dir string) (Existing, error) {
	var ex Existing
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	switch {
	case err == nil:
		ex.Found = true
		ex.EnvRaw = string(b)
		ex.Env = ParseEnv(ex.EnvRaw)
	case errors.Is(err, fs.ErrNotExist):
		ex.Env = map[string]string{}
	default:
		return ex, err
	}
	if c, err := os.ReadFile(filepath.Join(dir, "config", "boson.yaml")); err == nil {
		ex.Config = c
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ex, err
	}
	return ex, nil
}

func cfgBool(cfg []byte, def bool, path ...string) bool {
	if v, ok := ConfigValue(cfg, path...).(bool); ok {
		return v
	}
	return def
}

func cfgString(cfg []byte, def string, path ...string) string {
	switch v := ConfigValue(cfg, path...).(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	}
	return def
}

func envBool(env map[string]string, k string, def bool) bool {
	if b, err := strconv.ParseBool(env[k]); err == nil {
		return b
	}
	return def
}

// Answers reads the wizard's answers back from an existing install, so a
// re-run starts from what is there (and changes nothing unless asked).
func (ex Existing) Answers() Answers {
	a := Defaults()
	if !ex.Found {
		return a
	}
	e, c := ex.Env, ex.Config
	a.Domain = e["DOMAIN"]
	a.NetworkName = cfgString(c, e["NETWORK_NAME"], "network", "name")
	if a.NetworkName == "" {
		a.NetworkName = "Boson"
	}
	prof := map[string]bool{}
	for _, p := range strings.Split(e["COMPOSE_PROFILES"], ",") {
		prof[strings.TrimSpace(p)] = true
	}
	a.Voice = prof["voice"]
	a.Video = prof["video"]
	a.Wellknown = prof["wellknown"]
	a.OwnDirectory = prof["directory"]
	a.Files.Enabled = prof["files"]
	switch {
	case cfgString(c, "local", "files", "storage", "driver") != "s3":
		a.Files.Storage = "local"
	case prof["minio"] || cfgString(c, "", "files", "storage", "s3", "endpoint") == "http://minio:9000":
		a.Files.Storage = "minio"
	default:
		a.Files.Storage = "s3"
		a.Files.S3Endpoint = cfgString(c, "", "files", "storage", "s3", "endpoint")
		a.Files.S3Bucket = cfgString(c, e["FILES_S3_BUCKET"], "files", "storage", "s3", "bucket")
		a.Files.S3Region = cfgString(c, "auto", "files", "storage", "s3", "region")
		a.Files.S3PathStyle = cfgBool(c, true, "files", "storage", "s3", "path_style")
	}
	a.TLS.Email = e["TLS_ISSUER"]
	switch ca := e["ACME_CA_SERVER"]; ca {
	case "":
		a.TLS.CA = "production"
	case "https://acme-staging-v02.api.letsencrypt.org/directory":
		a.TLS.CA = "staging"
	default:
		a.TLS.CA = "custom"
		a.TLS.CustomURL = ca
	}
	a.Admin.Email = e["ADMIN_EMAIL"]
	a.SMTP.Host = cfgString(c, e["SMTP_HOST"], "auth", "smtp", "host")
	a.SMTP.Port, _ = strconv.Atoi(cfgString(c, e["SMTP_PORT"], "auth", "smtp", "port"))
	a.SMTP.From = cfgString(c, e["SMTP_FROM"], "auth", "smtp", "from")
	a.SMTP.TLS = cfgString(c, e["SMTP_TLS"], "auth", "smtp", "tls")
	a.SMTP.User = e["SMTP_USER"]
	a.Registration = cfgString(c, e["REGISTRATION"], "auth", "registration")
	a.EmailVerification = cfgBool(c, envBool(e, "REQUIRE_EMAIL_VERIFICATION", true), "auth", "require_email_verification")
	a.Guests = cfgBool(c, true, "auth", "guest_access")
	a.GuestVoice = cfgBool(c, false, "auth", "guest_voice")
	a.DirectoryListing = cfgBool(c, envBool(e, "ADMIN_DIRECTORY_REGISTRATION", true), "admin", "directory_registration")
	a.Federation = cfgString(c, e["FEDERATION_MODE"], "auth", "federation", "mode")
	if allow, ok := ConfigValue(c, "auth", "federation", "allow").([]any); ok {
		var list []string
		for _, d := range allow {
			if s, ok := d.(string); ok {
				list = append(list, s)
			}
		}
		a.FederationAllow = strings.Join(list, ",")
	} else {
		a.FederationAllow = e["FEDERATION_ALLOW"]
	}
	if e["BRIDGE_CERT_HOST"] != "" {
		// An add-on install (Boson next to an existing IRC server).
		a.Mode = ModeAddon
		if host, port, err := net.SplitHostPort(e["BRIDGE_IRC_SERVER"]); err == nil {
			a.Addon.Host = host
			a.Addon.Port, _ = strconv.Atoi(port)
		}
		a.Addon.TLS, a.Addon.TLSVerify = true, e["NICKSERV_TLS_PIN_SHA256"] == ""
		a.Addon.Accounts = AccountsBoson
		if e["ACCOUNT_BACKEND"] == "nickserv" {
			a.Addon.Accounts = AccountsNickServ
			a.Addon.Strategy = e["NICKSERV_STRATEGY"]
		}
		a.Addon.RPCURL = e["NICKSERV_RPC_URL"]
		a.Addon.ServicesServer = e["NICKSERV_SERVICES_SERVER"]
	}
	a.Normalize()
	return a
}

// InstallFiles copies the stack's own files (compose.yaml, traefik/,
// config/boson.example.yaml, …; the image's copy of this repo) into dir.
// Files the install owns (.env, config/boson.yaml, compose.override.yaml,
// your traefik/dynamic/*.yml) are not in src, so they are never touched.
func InstallFiles(src, dir string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if st.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if old, err := os.ReadFile(dst); err == nil && string(old) == string(data) {
			if ost, err := os.Stat(dst); err == nil && ost.Mode().Perm() == mode {
				return nil
			}
		}
		n++
		return WriteFileAtomic(dst, data, mode)
	})
	return n, err
}
