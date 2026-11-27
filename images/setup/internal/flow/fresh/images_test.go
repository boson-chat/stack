package fresh

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/boson-chat/stack/images/setup/internal/install"
	"github.com/boson-chat/stack/images/setup/internal/releases"
	"github.com/boson-chat/stack/images/setup/internal/stack"
)

type tagsFake struct {
	mu   sync.Mutex
	tags map[string][]string
}

func (f *tagsFake) Tags(_ context.Context, repo string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tags[repo], nil
}

type regAll struct{}

func (regAll) TagExists(context.Context, string, string) (bool, error) { return true, nil }

const composeFixture = `services:
  auth:
    image: ${AUTH_IMAGE:-harbor.floret.dev/boson/auth:1.27.0}
  api:
    image: ${API_IMAGE:-harbor.floret.dev/boson/backend:0.57.0}
  web:
    image: ${WEB_IMAGE:-harbor.floret.dev/boson/web:0.89.0}
  admin:
    image: ${ADMIN_IMAGE:-harbor.floret.dev/boson/admin:1.29.0}
  quicvoiced:
    image: ${QUICVOICE_IMAGE:-harbor.floret.dev/boson/quicvoice:1.15.0}
  files:
    image: ${FILES_IMAGE:-harbor.floret.dev/boson/files:1.0.3}
`

func imagesFlow(t *testing.T, dir string, tags *tagsFake) *Flow {
	t.Helper()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "compose.yaml"), []byte(composeFixture), 0o644)
	r, err := install.NewRunner(&install.Runner{Dir: dir, StackSrc: src, Compose: &install.FakeCompose{}, Store: install.NewStore(dir), Hub: install.NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(Deps{InstallDir: dir, Runner: r, Releases: &releases.Resolver{Tags: tags, Registry: regAll{}}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.answers.Domain = "boson.test"
	f.answers.Admin = stack.AdminAnswers{Username: "alice", Email: "alice@boson.test"}
	f.answers.TLS.Email = "alice@boson.test"
	f.mu.Unlock()
	return f
}

func TestImagesReviewInstallRerun(t *testing.T) {
	dir := t.TempDir()
	tags := &tagsFake{tags: map[string][]string{"boson-chat/boson": {"v0.102.0"}, "boson-chat/admin": {"v1.29.1"}}}
	f := imagesFlow(t, dir, tags)
	mux := http.NewServeMux()
	f.Register(mux, "/api/flows/fresh/")

	code, body := get(t, mux, "/api/flows/fresh/review")
	if code != 200 {
		t.Fatal(code, body)
	}
	env, _ := body["env"].(string)
	for _, want := range []string{"WEB_IMAGE=harbor.floret.dev/boson/web:0.102.0", "ADMIN_IMAGE=harbor.floret.dev/boson/admin:1.29.1",
		"AUTH_IMAGE=harbor.floret.dev/boson/auth:1.27.0", "ADMIN_UPDATES_DOCKER_PROXY_URL=http://docker-proxy-updates:2375"} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("review .env lacks %s", want)
		}
	}
	imgs, _ := body["images"].([]any)
	if len(imgs) != 6 {
		t.Fatalf("images: %v", imgs)
	}

	// A release lands between the review and Install: the install writes
	// what the review showed.
	tags.mu.Lock()
	tags.tags["boson-chat/boson"] = []string{"v0.103.0"}
	tags.mu.Unlock()
	p, _, _, err := f.planImages(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.EnvValue("WEB_IMAGE"); got != "harbor.floret.dev/boson/web:0.102.0" {
		t.Fatalf("install writes %q", got)
	}
	// Video turned on after the review: looked up at install.
	f.mu.Lock()
	f.answers.Video = true
	f.mu.Unlock()
	os.WriteFile(filepath.Join(f.d.Runner.StackSrc, "compose.yaml"), []byte(composeFixture+
		"  quicvideod:\n    image: ${QUICVIDEO_IMAGE:-harbor.floret.dev/boson/quicvideo:1.14.1}\n"), 0o644)
	tags.mu.Lock()
	tags.tags["boson-chat/quicvideo"] = []string{"v1.14.3"}
	tags.mu.Unlock()
	p, _, _, _ = f.planImages(context.Background(), true)
	if p.EnvValue("QUICVIDEO_IMAGE") != "harbor.floret.dev/boson/quicvideo:1.14.3" || p.EnvValue("WEB_IMAGE") != "harbor.floret.dev/boson/web:0.102.0" {
		t.Fatalf("video %q web %q", p.EnvValue("QUICVIDEO_IMAGE"), p.EnvValue("WEB_IMAGE"))
	}

	// Installed: .env as the install writes it. A re-run keeps every
	// version, even with newer releases out.
	text, _ := stack.MergeEnv("", p.Env, "#")
	os.WriteFile(filepath.Join(dir, ".env"), []byte(text), 0o600)
	f2 := imagesFlow(t, dir, tags)
	p2, _, pins, err := f2.planImages(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range pins {
		if x.Source != releases.SourceKept || x.Write() {
			t.Errorf("re-run: %+v", x)
		}
	}
	if !f2.currentAnswers().ApplyUpdates {
		t.Error("re-run reads applying updates back as on")
	}
	merged, _ := stack.MergeEnv(text, p2.Env, "#")
	if e := stack.ParseEnv(merged); e["WEB_IMAGE"] != "harbor.floret.dev/boson/web:0.102.0" {
		t.Errorf("re-run changed WEB_IMAGE to %q", e["WEB_IMAGE"])
	}
}

func TestImagesNoComposeFile(t *testing.T) {
	dir := t.TempDir()
	f := imagesFlow(t, dir, &tagsFake{})
	os.Remove(filepath.Join(f.d.Runner.StackSrc, "compose.yaml"))
	_, _, pins, err := f.planImages(context.Background(), false)
	if err != nil || len(pins) != 0 {
		t.Fatal(pins, err)
	}
}
