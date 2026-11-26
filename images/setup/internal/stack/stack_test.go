package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func example(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../config/boson.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func answers() Answers {
	a := Defaults()
	a.Domain = "boson.test"
	a.NetworkName = "TestNet"
	a.Admin = AdminAnswers{Username: "alice", Email: "alice@boson.test"}
	a.TLS.Email = "alice@boson.test"
	return a
}

func TestPlanNewInstall(t *testing.T) {
	gen := NewGenerator()
	p, err := BuildPlan(answers(), Secrets{}, nil, gen, "999")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.EnvValue("COMPOSE_PROFILES"); got != "irc,auth,api,web,admin,metrics,voice,files" {
		t.Fatalf("profiles %q", got)
	}
	if got := p.EnvValue("CERT_HOSTS"); got != "irc.boson.test,auth.boson.test,chat.boson.test,admin.boson.test,api.boson.test,voice.boson.test,files.boson.test" {
		t.Fatalf("cert hosts %q", got)
	}
	if p.OperPassword == "" || bcrypt.CompareHashAndPassword([]byte(p.EnvValue("OPER_PASSWORD_HASH")), []byte(p.OperPassword)) != nil {
		t.Fatal("oper hash doesn't match the password")
	}
	for _, v := range p.Env {
		if SecretKeys[v.Key] != v.Secret {
			t.Errorf("%s: secret=%v", v.Key, v.Secret)
		}
		if v.Secret && v.Origin == "generated" && len(v.Value) < 15 {
			t.Errorf("%s: short secret", v.Key)
		}
	}
	// Same generator: same secrets (the review and the install agree).
	p2, _ := BuildPlan(answers(), Secrets{}, nil, gen, "999")
	if p2.EnvValue("POSTGRES_PASSWORD") != p.EnvValue("POSTGRES_PASSWORD") || p2.OperPassword != p.OperPassword {
		t.Fatal("generator not stable")
	}
	// Masked review: no secret value in it.
	m := MaskedEnv(p)
	for _, s := range p.SecretValues() {
		if strings.Contains(m, s) {
			t.Fatalf("masked env leaks %q", s)
		}
	}
	if !strings.Contains(m, "POSTGRES_PASSWORD=••••••••  # new, random") {
		t.Fatal(m)
	}
}

func TestPlanKeepsSecrets(t *testing.T) {
	p, _ := BuildPlan(answers(), Secrets{}, nil, NewGenerator(), "999")
	text, _ := MergeEnv("", p.Env, "# test")
	existing := ParseEnv(text)
	p2, err := BuildPlan(answers(), Secrets{}, existing, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.Env {
		if v.Secret && p2.EnvValue(v.Key) != v.Value {
			t.Errorf("%s not kept", v.Key)
		}
	}
	if p2.OperPassword != "" {
		t.Fatal("oper password regenerated")
	}
	text2, changed := MergeEnv(text, p2.Env, "# test")
	if changed || text2 != text {
		t.Fatalf("re-run changed .env:\n%s", text2)
	}
}

func TestMergeEnvKeepsOtherLines(t *testing.T) {
	old := "# my comment\nCOMPOSE_FILE=compose.yaml:compose.auth-src.yaml\nDOMAIN=old.test\nOPER_PASSWORD_HASH='$2a$04$abc'\nDOMAIN=dup.test\n"
	out, changed := MergeEnv(old, []EnvVar{{Key: "DOMAIN", Value: "new.test"}, {Key: "NETWORK_NAME", Value: "My Net"}}, "#")
	if !changed {
		t.Fatal("not changed")
	}
	for _, want := range []string{"# my comment\n", "COMPOSE_FILE=compose.yaml:compose.auth-src.yaml\n", "DOMAIN=new.test\n", "OPER_PASSWORD_HASH='$2a$04$abc'\n", "NETWORK_NAME='My Net'\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "dup.test") {
		t.Error("duplicate kept")
	}
	if e := ParseEnv(out); e["OPER_PASSWORD_HASH"] != "$2a$04$abc" || e["NETWORK_NAME"] != "My Net" {
		t.Errorf("parse: %v", e)
	}
}

func TestConfigNewAndRerun(t *testing.T) {
	ex := example(t)
	p, _ := BuildPlan(answers(), Secrets{}, nil, NewGenerator(), "999")
	out, changed, err := ApplyConfig(nil, ex, p.Config)
	if err != nil || !changed {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), string(ex[:200])) {
		t.Fatal("example header lost")
	}
	if ConfigValue(out, "network", "name") != "TestNet" || ConfigValue(out, "auth", "registration") != "open" ||
		ConfigValue(out, "files", "storage", "driver") != "local" || ConfigValue(out, "version") != 1 {
		t.Fatalf("values:\n%s", out[:2000])
	}
	rooms, _ := ConfigValue(out, "voice", "rooms").([]any)
	if len(rooms) != 1 {
		t.Fatalf("rooms %v", rooms)
	}
	// Re-run with the same answers: untouched, byte for byte.
	out2, changed, err := ApplyConfig(out, ex, p.Config)
	if err != nil || changed || string(out2) != string(out) {
		t.Fatalf("re-run changed the file: %v", err)
	}
	// A changed answer edits the file in place, comments kept.
	a := answers()
	a.Registration = "closed"
	a.Files.Storage = "minio"
	p3, _ := BuildPlan(a, Secrets{}, nil, NewGenerator(), "999")
	out3, changed, err := ApplyConfig(out, ex, p3.Config)
	if err != nil || !changed {
		t.Fatal(err)
	}
	if ConfigValue(out3, "auth", "registration") != "closed" || ConfigValue(out3, "files", "storage", "s3", "endpoint") != "http://minio:9000" {
		t.Fatalf("edit:\n%s", out3)
	}
	if !strings.Contains(string(out3), "# boson stack config: config/boson.yaml") || !strings.Contains(string(out3), "# ---- network: the whole stack") {
		t.Fatal("comments lost")
	}
}

func TestConfigKeepsAdminEdits(t *testing.T) {
	ex := example(t)
	p, _ := BuildPlan(answers(), Secrets{}, nil, NewGenerator(), "999")
	out, _, _ := ApplyConfig(nil, ex, p.Config)
	edited := strings.Replace(string(out), "id: general, name: General", "id: lobby, name: Lobby", 1)
	edited = strings.Replace(edited, "version: 1", "version: 1\nbranding:\n  theme: { name: x }", 1)
	out2, _, err := ApplyConfig([]byte(edited), ex, p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out2), "lobby") || ConfigValue(out2, "branding", "theme", "name") != "x" {
		t.Fatalf("admin edits lost:\n%s", out2)
	}
}

func TestExistingAnswersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := answers()
	a.Video = true
	a.Files.Storage = "minio"
	a.Registration = "invite"
	a.Guests = false
	a.SMTP = SMTPAnswers{Host: "smtp.boson.test", Port: 465, From: "Boson <no@boson.test>", TLS: "tls", User: "u"}
	a.EmailVerification = true
	a.TLS.CA = "staging"
	a.Federation = FederationAllowlist
	a.FederationAllow = "boson.chat, *.example.org"
	p, err := BuildPlan(a, Secrets{SMTPPassword: "pw-secret"}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	env, _ := MergeEnv("", p.Env, "#")
	cfg, _, _ := ApplyConfig(nil, example(t), p.Config)
	os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600)
	os.WriteFile(filepath.Join(dir, "config", "boson.yaml"), cfg, 0o644)
	ex, err := LoadExisting(dir)
	if err != nil || !ex.Found {
		t.Fatal(err)
	}
	got := ex.Answers()
	a.Normalize()
	got.Admin.Username = a.Admin.Username
	if got != a {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, a)
	}
	if ex.Env["SMTP_PASSWORD"] != "pw-secret" {
		t.Fatal("smtp password")
	}
}

func TestValidate(t *testing.T) {
	a := answers()
	if e := a.Validate("", Secrets{AdminPassword: "long enough pw"}, true); len(e) != 0 {
		t.Fatal(e)
	}
	a.Domain = "https://x.com/"
	a.Admin.Username = "1bad"
	a.NetworkName = "My Net"
	e := a.Validate("", Secrets{AdminPassword: "short"}, true)
	fields := map[string]bool{}
	for _, f := range e {
		fields[f.Field] = true
	}
	for _, f := range []string{"domain", "admin.username", "admin.password", "network_name"} {
		if !fields[f] {
			t.Errorf("no error for %s: %v", f, e)
		}
	}
	if ValidDomain("localhost") || !ValidDomain("a.b.example.com") {
		t.Fatal("ValidDomain")
	}
}

func TestInstallFiles(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "traefik", "dynamic"), 0o755)
	os.WriteFile(filepath.Join(src, "compose.yaml"), []byte("name: boson\n"), 0o644)
	os.WriteFile(filepath.Join(src, "traefik", "dynamic", "stack.yml"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dst, ".env"), []byte("KEEP=1\n"), 0o600)
	n, err := InstallFiles(src, dst)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, _ := InstallFiles(src, dst); n != 0 {
		t.Fatalf("second copy wrote %d", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, ".env")); string(b) != "KEEP=1\n" {
		t.Fatal(".env touched")
	}
}

func TestWriteFileAtomicMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", ".env")
	if err := WriteFileAtomic(p, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestFederationAnswers(t *testing.T) {
	a := answers()
	p, err := BuildPlan(a, Secrets{}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := ApplyConfig(nil, example(t), p.Config)
	if ConfigValue(out, "auth", "federation", "mode") != "directory" || ConfigValue(out, "auth", "federation", "allow") != nil {
		t.Fatalf("default: verified servers, no allow list:\n%s", out[:1500])
	}

	a.Federation = FederationAllowlist
	a.FederationAllow = "https://Boson.chat/, alice@friends.example.org\n*.example.net boson.chat"
	p, err = BuildPlan(a, Secrets{}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Answers.FederationAllow; got != "boson.chat,friends.example.org,*.example.net" {
		t.Fatalf("allow %q", got)
	}
	out, _, _ = ApplyConfig(out, example(t), p.Config)
	allow, _ := ConfigValue(out, "auth", "federation", "allow").([]any)
	if ConfigValue(out, "auth", "federation", "mode") != "allowlist" || len(allow) != 3 || allow[2] != "*.example.net" {
		t.Fatalf("allowlist:\n%s", out)
	}

	for _, bad := range []Answers{
		func() Answers { b := answers(); b.Federation = "everyone"; return b }(),
		func() Answers { b := answers(); b.Federation = FederationAllowlist; return b }(),
		func() Answers {
			b := answers()
			b.Federation = FederationAllowlist
			b.FederationAllow = "not a domain!"
			return b
		}(),
	} {
		bad.Normalize()
		if e := bad.Validate("features", Secrets{}, false); len(e) == 0 || !strings.HasPrefix(e[0].Field, "federation") {
			t.Errorf("%q %q: %v", bad.Federation, bad.FederationAllow, e)
		}
	}

	// The add-on path: off (federation v1 needs Ergo).
	b := answers()
	b.Mode = ModeAddon
	b.Federation = FederationOpen
	b.Normalize()
	if b.Federation != FederationOff {
		t.Fatalf("add-on: %q", b.Federation)
	}
}
