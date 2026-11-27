package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boson-chat/stack/images/setup/internal/releases"
)

type tags map[string][]string

func (t tags) Tags(_ context.Context, repo string) ([]string, error) { return t[repo], nil }

type reg map[string]bool

func (r reg) TagExists(_ context.Context, image, tag string) (bool, error) {
	return r[image+":"+tag], nil
}

const H = "harbor.floret.dev/boson/"

func TestBump(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	write := func(p, s string) { os.WriteFile(filepath.Join(root, p), []byte(s), 0o644) }
	write("compose.yaml", "  web:\n    image: ${WEB_IMAGE:-"+H+"web:0.89.0}\n  auth:\n    image: ${AUTH_IMAGE:-"+H+"auth:1.27.0}\n"+
		"  evrd-links:\n    image: ${EVRD_LINKS_IMAGE:-ghcr.io/4evrd/evrd:0.60.0}\n  evrd:\n    image: ${EVRD_IMAGE:-ghcr.io/4evrd/evrd:0.60.0}\n")
	write(".env.example", "#WEB_IMAGE="+H+"web:0.70.0\n#EVRD_IMAGE=ghcr.io/4evrd/evrd:0.60.0\n#EVRD_LINKS_IMAGE=ghcr.io/4evrd/evrd:0.60.0\n")
	write("docs/web.md", "default `"+H+"web:0.89.0`; not "+H+"web:0.89.01\n")
	r := &releases.Resolver{
		Tags:     tags{"boson-chat/boson": {"v0.102.0", "v1.0.0"}, "boson-chat/auth": {"v1.27.0"}, "4evrd/evrd": {"v0.61.0"}},
		Registry: reg{H + "web:0.102.0": true, "ghcr.io/4evrd/evrd:0.61.0": true},
	}
	var out bytes.Buffer
	if err := bump(context.Background(), root, r, &out); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(root, p)); return string(b) }
	c := read("compose.yaml")
	for _, want := range []string{"${WEB_IMAGE:-" + H + "web:0.102.0}", "${AUTH_IMAGE:-" + H + "auth:1.27.0}",
		"${EVRD_IMAGE:-ghcr.io/4evrd/evrd:0.61.0}", "${EVRD_LINKS_IMAGE:-ghcr.io/4evrd/evrd:0.60.0}"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose.yaml lacks %s:\n%s", want, c)
		}
	}
	if e := read(".env.example"); e != "#WEB_IMAGE="+H+"web:0.102.0\n#EVRD_IMAGE=ghcr.io/4evrd/evrd:0.61.0\n#EVRD_LINKS_IMAGE=ghcr.io/4evrd/evrd:0.60.0\n" {
		t.Errorf(".env.example:\n%s", e)
	}
	if d := read("docs/web.md"); d != "default `"+H+"web:0.102.0`; not "+H+"web:0.89.01\n" {
		t.Errorf("docs: %s", d)
	}
	if s := out.String(); !strings.Contains(s, "| Web client (`WEB_IMAGE`) | 0.89.0 | **0.102.0** |") || strings.Contains(s, "Auth") {
		t.Errorf("summary:\n%s", s)
	}

	// Current: nothing printed, nothing written.
	out.Reset()
	before := read("compose.yaml")
	if err := bump(context.Background(), root, r, &out); err != nil || out.Len() != 0 || read("compose.yaml") != before {
		t.Fatalf("second run: %v %q", err, out.String())
	}
	if err := bump(context.Background(), t.TempDir(), r, &out); err == nil {
		t.Fatal("no compose.yaml")
	}
}
