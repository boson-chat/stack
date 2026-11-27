package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bundle builds this repository's release bundle (scripts/release/bundle.sh).
func bundle(t *testing.T, version string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bundle")
	cmd := exec.Command("sh", "../../../../scripts/release/bundle.sh", "--out", out, "--version", version, "--commit", "test")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundle.sh: %v\n%s", err, b)
	}
	return out
}

func TestInstallFromBundle(t *testing.T) {
	src := bundle(t, "1.4.0")
	dir := t.TempDir()
	r := &Runner{Dir: dir, StackSrc: src}
	var lines []string
	log := func(l string) { lines = append(lines, l) }
	if err := installStackFiles(context.Background(), r, log); err != nil {
		t.Fatal(err, lines)
	}
	if v, _ := os.ReadFile(filepath.Join(dir, "VERSION")); strings.TrimSpace(string(v)) != "1.4.0" {
		t.Fatalf("VERSION %q", v)
	}
	if _, err := os.Stat(filepath.Join(dir, ".boson", "stack-files.sha256")); err != nil {
		t.Fatal("no record of the installed files")
	}
	if _, err := os.Stat(filepath.Join(dir, ".bundle")); err == nil {
		t.Fatal(".bundle copied into the install")
	}
	// A bridge install from an older wizard: the migration adds what's missing.
	os.MkdirAll(filepath.Join(dir, "bridge"), 0o755)
	os.WriteFile(filepath.Join(dir, "bridge", "evrd.conf"), []byte("managed_version Net\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("COMPOSE_PROFILES=auth,bridge,web\nWEBIRC_PASSWORDS=irc.example.net=abc\n"), 0o600)
	if err := runMigrations(context.Background(), r, log); err != nil {
		t.Fatal(err, lines)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(env), ",evrd-links=") || !strings.Contains(string(env), "BRIDGE_CLIENT_WEBIRC_PASSWORD=") {
		t.Fatalf(".env after the migration:\n%s", env)
	}
	if st, _ := os.Stat(filepath.Join(dir, ".env")); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode %v", st.Mode())
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "bridge", "evrd.conf"))
	if !strings.Contains(string(conf), "managed_nick_guard_grace 60s") {
		t.Fatalf("evrd.conf: %s", conf)
	}
	applied, _ := os.ReadFile(filepath.Join(dir, ".boson", "migrations.applied"))
	if !strings.HasPrefix(string(applied), "0001-bridge-client-webirc.sh ") {
		t.Fatalf("migrations.applied: %q", applied)
	}
	// A re-run changes nothing.
	before := string(env)
	if err := installStackFiles(context.Background(), r, log); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(context.Background(), r, log); err != nil {
		t.Fatal(err)
	}
	if env, _ := os.ReadFile(filepath.Join(dir, ".env")); string(env) != before {
		t.Fatalf(".env changed on a re-run:\n%s", env)
	}
	// A file changed on this server: replaced, kept as .local.
	os.WriteFile(filepath.Join(dir, "monitoring", "scrape.yml"), []byte("# mine\n"), 0o644)
	lines = nil
	if err := installStackFiles(context.Background(), r, log); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "monitoring", "scrape.yml.local")); string(b) != "# mine\n" {
		t.Fatalf("no .local copy: %q", b)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "monitoring/scrape.yml was changed on this server") {
		t.Fatal(lines)
	}
}

func TestInstallNotABundle(t *testing.T) {
	src, dir := stackSrc(t), t.TempDir()
	r := &Runner{Dir: dir, StackSrc: src}
	if err := installStackFiles(context.Background(), r, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(context.Background(), r, func(string) {}); err != nil {
		t.Fatal(err)
	}
}
