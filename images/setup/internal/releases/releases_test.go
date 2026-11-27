package releases

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTags struct {
	mu    sync.Mutex
	tags  map[string][]string
	err   error
	calls map[string]int
}

func (f *fakeTags) Tags(_ context.Context, repo string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[repo]++
	if f.err != nil {
		return nil, f.err
	}
	return f.tags[repo], nil
}

type fakeReg struct {
	mu    sync.Mutex
	have  map[string]bool
	err   error
	calls int
}

func (f *fakeReg) TagExists(_ context.Context, image, tag string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.have[image+":"+tag], nil
}

const H = "harbor.floret.dev/boson/"

var defaults = map[string]string{
	"AUTH_IMAGE":       H + "auth:1.27.0",
	"API_IMAGE":        H + "backend:0.57.0",
	"WEB_IMAGE":        H + "web:0.89.0",
	"ADMIN_IMAGE":      H + "admin:1.29.0",
	"QUICVOICE_IMAGE":  H + "quicvoice:1.15.0",
	"QUICVIDEO_IMAGE":  H + "quicvideo:1.14.1",
	"FILES_IMAGE":      H + "files:1.0.3",
	"DIRECTORY_IMAGE":  H + "directory:1.7.0",
	"EVRD_IMAGE":       "ghcr.io/4evrd/evrd:0.60.0",
	"EVRD_LINKS_IMAGE": "ghcr.io/4evrd/evrd:0.60.0",
}

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "v1.2.3", 0}, {"0.102.0", "0.89.0", 1}, {"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1}, {"1.0.0-rc.1", "1.0.0-beta", 1}, {"1.0.0-1", "1.0.0-a", -1}, {"1.0.0-beta", "1.0.0-alpha", 1},
		{"1.0.0-rc.1.1", "1.0.0-rc.1", 1}, {"1.0.0-rc", "1.0.0-rc.1", -1}, {"2.19", "2.19.0", 0}, {"1.2.3+b", "1.2.3", 0},
	} {
		a, ok1 := ParseVersion(c.a)
		b, ok2 := ParseVersion(c.b)
		if !ok1 || !ok2 || Compare(a, b) != c.want || Compare(b, a) != -c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, Compare(a, b), c.want)
		}
	}
	for _, bad := range []string{"latest", "1", "1.2.3.4", "01.2.3", "1.2.3-", "a.b.c", "1..2"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	if v, _ := ParseVersion("v1.1.0-boson.2"); v.String() != "1.1.0-boson.2" {
		t.Error(v.String())
	}
	for ref, want := range map[string][2]string{
		H + "web:0.89.0":               {H + "web", "0.89.0"},
		"localhost:5000/x":             {"localhost:5000/x", ""},
		"ghcr.io/a/b:1.0.0@sha256:abc": {"ghcr.io/a/b", "1.0.0"},
		"busybox":                      {"busybox", ""},
	} {
		if r, tg := SplitImage(ref); r != want[0] || tg != want[1] {
			t.Errorf("SplitImage(%q) = %q %q", ref, r, tg)
		}
	}
}

func TestComposeDefaults(t *testing.T) {
	b, err := os.ReadFile("../../../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d := ComposeDefaults(string(b))
	for _, c := range Components {
		repo, tag := SplitImage(d[c.EnvVar])
		if _, ok := ParseVersion(tag); !ok || repo == "" {
			t.Errorf("%s: compose.yaml default %q isn't image:version", c.EnvVar, d[c.EnvVar])
		}
	}
}

func TestNewest(t *testing.T) {
	tags := &fakeTags{tags: map[string][]string{
		// Not sorted, with junk, a pre-release, a new major and an old one.
		"boson-chat/boson":   {"v0.101.0", "nightly", "v0.102.0", "v0.103.0-rc.1", "v1.0.0", "v0.88.0", "0.102.0"},
		"boson-chat/backend": {"v0.57.3", "v0.57.2"},
		"boson-chat/auth":    {"v1.27.0"},
		"boson-chat/admin":   {"v1.29.3", "v1.29.2", "v1.29.1"},
	}}
	reg := &fakeReg{have: map[string]bool{
		H + "web:0.102.0": true, H + "web:0.101.0": true, H + "backend:0.57.3": true, H + "admin:1.29.1": true,
		H + "web:1.0.0": true,
	}}
	r := &Resolver{Tags: tags, Registry: reg}
	ctx := context.Background()
	c := func(env string) Component { x, _ := ComponentByEnv(env); return x }

	if got := r.Newest(ctx, c("WEB_IMAGE"), defaults["WEB_IMAGE"]); got != (Result{Image: H + "web:0.102.0", Newest: true}) {
		t.Errorf("web: %+v", got)
	}
	if got := r.Newest(ctx, c("API_IMAGE"), defaults["API_IMAGE"]); got.Image != H+"backend:0.57.3" || !got.Newest {
		t.Errorf("api: %+v", got)
	}
	// Already the newest: the default, as the newest.
	if got := r.Newest(ctx, c("AUTH_IMAGE"), defaults["AUTH_IMAGE"]); got != (Result{Image: defaults["AUTH_IMAGE"], Newest: true}) {
		t.Errorf("auth: %+v", got)
	}
	// The two newest aren't pushed yet: the newest one that is, with a note.
	got := r.Newest(ctx, c("ADMIN_IMAGE"), defaults["ADMIN_IMAGE"])
	if got.Image != H+"admin:1.29.1" || got.Newest || !strings.Contains(got.Note, "1.29.3 is released but its image isn't on the registry yet") {
		t.Errorf("admin: %+v", got)
	}
	// A pre-release default may move to a newer pre-release.
	if got := r.Newest(ctx, c("WEB_IMAGE"), H+"web:0.103.0-rc.0"); got.Image != H+"web:0.103.0-rc.0" || !strings.Contains(got.Note, "0.103.0-rc.1") {
		t.Errorf("web rc: %+v", got)
	}
	// Not a version: kept, with a note, no lookups.
	if got := r.Newest(ctx, c("WEB_IMAGE"), H+"web:latest"); got.Image != H+"web:latest" || got.Note == "" {
		t.Errorf("latest: %+v", got)
	}
	// One GitHub request per repository per run.
	r.Newest(ctx, c("WEB_IMAGE"), defaults["WEB_IMAGE"])
	if tags.calls["boson-chat/boson"] != 1 {
		t.Errorf("GitHub asked %d times", tags.calls["boson-chat/boson"])
	}

	// Nothing on the registry at all.
	r2 := &Resolver{Tags: tags, Registry: &fakeReg{}, MaxCandidates: 1}
	if got := r2.Newest(ctx, c("ADMIN_IMAGE"), defaults["ADMIN_IMAGE"]); got.Image != defaults["ADMIN_IMAGE"] || got.Newest || !strings.Contains(got.Note, "1.29.3") {
		t.Errorf("none pushed: %+v", got)
	}
	// Registry unreachable: the default.
	r3 := &Resolver{Tags: tags, Registry: &fakeReg{err: errors.New("dial tcp: timeout")}}
	if got := r3.Newest(ctx, c("WEB_IMAGE"), defaults["WEB_IMAGE"]); got.Image != defaults["WEB_IMAGE"] || got.Newest || !strings.Contains(got.Note, "couldn't check the registry") {
		t.Errorf("registry down: %+v", got)
	}
}

func TestNewestGitHubDownRetries(t *testing.T) {
	tags := &fakeTags{err: errors.New("GitHub unreachable")}
	r := &Resolver{Tags: tags, Registry: &fakeReg{}, RetryAfter: 20 * time.Millisecond}
	c, _ := ComponentByEnv("WEB_IMAGE")
	got := r.Newest(context.Background(), c, defaults["WEB_IMAGE"])
	if got.Image != defaults["WEB_IMAGE"] || got.Newest || !strings.Contains(got.Note, "couldn't check GitHub") {
		t.Fatalf("%+v", got)
	}
	r.Newest(context.Background(), c, defaults["WEB_IMAGE"])
	if tags.calls["boson-chat/boson"] != 1 {
		t.Fatal("a failure is remembered for a while")
	}
	time.Sleep(30 * time.Millisecond)
	tags.mu.Lock()
	tags.err, tags.tags = nil, map[string][]string{"boson-chat/boson": {"v0.90.0"}}
	tags.mu.Unlock()
	r.Registry = &fakeReg{have: map[string]bool{H + "web:0.90.0": true}}
	if got := r.Newest(context.Background(), c, defaults["WEB_IMAGE"]); got.Image != H+"web:0.90.0" {
		t.Fatalf("after retry: %+v", got)
	}
}

func TestChoose(t *testing.T) {
	tags := &fakeTags{tags: map[string][]string{
		"boson-chat/boson": {"v0.102.0"}, "boson-chat/backend": {"v0.57.3"}, "4evrd/evrd": {"v0.61.0"},
		"boson-chat/quicvoice": {"v1.15.1"}, "boson-chat/quicvideo": {"v1.14.3"},
	}}
	reg := &fakeReg{have: map[string]bool{H + "web:0.102.0": true, H + "backend:0.57.3": true, "ghcr.io/4evrd/evrd:0.61.0": true,
		H + "quicvoice:1.15.1": true, H + "quicvideo:1.14.3": true}}
	r := &Resolver{Tags: tags, Registry: reg}
	ctx := context.Background()
	byEnv := func(pins []Pin) map[string]Pin {
		m := map[string]Pin{}
		for _, p := range pins {
			m[p.EnvVar] = p
		}
		return m
	}

	// A new add-on install: everything it runs, newest; nothing it doesn't.
	pins := byEnv(Choose(ctx, r, defaults, []string{"bridge", "auth", "api", "web", "admin", "metrics", "voice", "updates"}, nil))
	if len(pins) != 7 {
		t.Fatalf("%d pins: %+v", len(pins), pins)
	}
	for env, want := range map[string]string{
		"WEB_IMAGE": H + "web:0.102.0", "API_IMAGE": H + "backend:0.57.3", "AUTH_IMAGE": defaults["AUTH_IMAGE"],
		"EVRD_IMAGE": "ghcr.io/4evrd/evrd:0.61.0", "EVRD_LINKS_IMAGE": "ghcr.io/4evrd/evrd:0.61.0", "QUICVOICE_IMAGE": H + "quicvoice:1.15.1",
	} {
		p := pins[env]
		if p.Image != want || p.Source != SourceNewest || !p.Write() {
			t.Errorf("%s: %+v", env, p)
		}
	}
	if pins["WEB_IMAGE"].Version != "0.102.0" || pins["WEB_IMAGE"].Default != defaults["WEB_IMAGE"] || pins["WEB_IMAGE"].Name != "Web client" {
		t.Errorf("web pin: %+v", pins["WEB_IMAGE"])
	}
	if _, ok := pins["QUICVIDEO_IMAGE"]; ok {
		t.Error("video isn't on")
	}

	// A re-run: pins in .env are kept, components already running on
	// compose's default stay on it, and only a newly enabled one (video)
	// gets the newest. No upgrade of what runs.
	existing := map[string]string{
		"COMPOSE_PROFILES": "irc,auth,api,web,admin,voice",
		"WEB_IMAGE":        H + "web:0.80.0",
	}
	calls := reg.calls
	pins = byEnv(Choose(ctx, r, defaults, []string{"irc", "auth", "api", "web", "admin", "voice", "video"}, existing))
	if p := pins["WEB_IMAGE"]; p.Source != SourceKept || p.Image != H+"web:0.80.0" || p.Version != "0.80.0" || p.Write() {
		t.Errorf("kept: %+v", p)
	}
	for _, env := range []string{"API_IMAGE", "AUTH_IMAGE", "QUICVOICE_IMAGE", "ADMIN_IMAGE"} {
		if p := pins[env]; p.Source != SourceCompose || p.Image != defaults[env] || p.Write() {
			t.Errorf("%s: %+v", env, p)
		}
	}
	if p := pins["QUICVIDEO_IMAGE"]; p.Source != SourceNewest || p.Image != H+"quicvideo:1.14.3" || !p.Write() {
		t.Errorf("new on re-run: %+v", p)
	}
	if reg.calls != calls+1 {
		t.Errorf("registry asked %d times for one new component", reg.calls-calls)
	}

	// GitHub down: the defaults, written, with the reason.
	down := &Resolver{Tags: &fakeTags{err: errors.New("GitHub unreachable: no route")}, Registry: reg}
	for _, p := range Choose(ctx, down, defaults, []string{"auth", "web"}, nil) {
		if p.Source != SourceDefault || p.Image != p.Default || !p.Write() || !strings.Contains(p.Note, "no route") {
			t.Errorf("fallback: %+v", p)
		}
	}
	// No resolver: the defaults.
	for _, p := range Choose(ctx, nil, defaults, []string{"auth"}, nil) {
		if p.Source != SourceDefault || p.Image != defaults["AUTH_IMAGE"] {
			t.Errorf("nil resolver: %+v", p)
		}
	}
	// No default known for a component: skipped.
	if got := Choose(ctx, nil, map[string]string{}, []string{"auth"}, nil); len(got) != 0 {
		t.Errorf("%+v", got)
	}
}

func TestGitHub(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/repos/boson-chat/boson/releases":
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("per_page")
			}
			fmt.Fprint(w, `[{"tag_name":"v0.102.0"},{"tag_name":"v0.103.0","draft":true},{"tag_name":"v0.101.0","prerelease":true}]`)
		case "/repos/x/limited/releases":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1700000000")
			w.WriteHeader(403)
		case "/repos/x/forbidden/releases":
			w.WriteHeader(403)
		case "/repos/x/broken/releases":
			fmt.Fprint(w, `{`)
		case "/repos/x/500/releases":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	g := NewGitHub("tok", time.Second)
	g.BaseURL = srv.URL
	ctx := context.Background()
	tags, err := g.Tags(ctx, "boson-chat/boson")
	if err != nil || strings.Join(tags, ",") != "v0.102.0,v0.101.0" || auth != "Bearer tok" {
		t.Fatalf("%v %v %q", tags, err, auth)
	}
	for repo, want := range map[string]string{
		"x/limited": "rate limit", "x/forbidden": "refused", "x/broken": "reading", "x/500": "500", "x/missing": "no releases",
	} {
		if _, err := g.Tags(ctx, repo); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", repo, err)
		}
	}
	g.BaseURL = "http://127.0.0.1:1"
	if _, err := g.Tags(ctx, "a/b"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Error(err)
	}
}

func TestRegistry(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:boson/web:pull" || r.URL.Query().Get("service") != "harbor-registry" {
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"token":"T"}`)
		case r.Header.Get("Authorization") != "Bearer T":
			w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="harbor-registry"`)
			w.WriteHeader(401)
		case r.Method != http.MethodHead || !strings.Contains(r.Header.Get("Accept"), "oci.image.index"):
			w.WriteHeader(400)
		case r.URL.Path == "/v2/boson/web/manifests/0.102.0":
			w.WriteHeader(200)
		case r.URL.Path == "/v2/boson/web/manifests/0.500.0":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	reg := NewRegistry(time.Second)
	reg.Override = srv.URL
	ctx := context.Background()
	if ok, err := reg.TagExists(ctx, H+"web", "0.102.0"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := reg.TagExists(ctx, H+"web", "0.103.0"); ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := reg.TagExists(ctx, H+"web", "0.500.0"); err == nil {
		t.Fatal("500")
	}
	// A token endpoint that refuses.
	if _, err := reg.TagExists(ctx, "harbor.floret.dev/other/x", "1.0.0"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatal(err)
	}
	reg.Override = "http://127.0.0.1:1"
	if _, err := reg.TagExists(ctx, "busybox", "1.0.0"); err == nil {
		t.Fatal("unreachable")
	}
}

func TestRegistryChallenges(t *testing.T) {
	reg := NewRegistry(time.Second)
	ctx := context.Background()
	if _, err := reg.token(ctx, `Basic realm="x"`, "r"); err == nil {
		t.Error("basic")
	}
	if _, err := reg.token(ctx, `Bearer service="x"`, "r"); err == nil {
		t.Error("no realm")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"access_token":"A"}`) }))
	defer srv.Close()
	if tok, err := reg.token(ctx, `Bearer realm="`+srv.URL+`"`, "r"); tok != "A" || err != nil {
		t.Error(tok, err)
	}
}
