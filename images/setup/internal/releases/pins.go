// Package releases picks the image versions an install runs: the newest
// release of each Boson component whose image is on the registry, decided
// the way the admin panel's Updates page decides (boson-chat/admin
// internal/services/updates: GitHub Releases, semver tags, the registry has
// the tag), so a fresh install starts with nothing to update.
package releases

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Component is one *_IMAGE variable of the stack's .env.
type Component struct {
	EnvVar string
	Name   string
	// Repo: the GitHub repository whose releases are its versions.
	Repo string
	// Profile: the compose profile its services run in.
	Profile string
	// NoBump: compose.yaml's default is never moved (cmd/bumpimages). The
	// evrd links holder: a new default would recreate it on every install
	// that doesn't pin it, dropping every Boson user off IRC.
	NoBump bool
}

// Components: every image the Updates page sets (ModeImage parts of its
// catalog), in the order the review lists them. Ergo, RoleServ and the web
// IRC gateway are built in this repository and pinned in compose.yaml: they
// come with the stack.
var Components = []Component{
	{EnvVar: "AUTH_IMAGE", Name: "Auth", Repo: "boson-chat/auth", Profile: "auth"},
	{EnvVar: "API_IMAGE", Name: "API", Repo: "boson-chat/backend", Profile: "api"},
	{EnvVar: "WEB_IMAGE", Name: "Web client", Repo: "boson-chat/boson", Profile: "web"},
	{EnvVar: "ADMIN_IMAGE", Name: "Admin panel", Repo: "boson-chat/admin", Profile: "admin"},
	{EnvVar: "QUICVOICE_IMAGE", Name: "Voice", Repo: "boson-chat/quicvoice", Profile: "voice"},
	{EnvVar: "QUICVIDEO_IMAGE", Name: "Video", Repo: "boson-chat/quicvideo", Profile: "video"},
	{EnvVar: "FILES_IMAGE", Name: "Files", Repo: "boson-chat/files", Profile: "files"},
	{EnvVar: "DIRECTORY_IMAGE", Name: "Directory", Repo: "boson-chat/directory", Profile: "directory"},
	{EnvVar: "EVRD_IMAGE", Name: "Bridge (evrd)", Repo: "4evrd/evrd", Profile: "bridge"},
	{EnvVar: "EVRD_LINKS_IMAGE", Name: "Bridge connections (evrd-links)", Repo: "4evrd/evrd", Profile: "bridge", NoBump: true},
}

var composeDefaultRe = regexp.MustCompile(`\$\{([A-Z0-9_]+_IMAGE):-([^}\s]+)\}`)

// ComposeDefaults reads the pinned defaults (${X_IMAGE:-ref}) from
// compose.yaml's text.
func ComposeDefaults(compose string) map[string]string {
	out := map[string]string{}
	for _, m := range composeDefaultRe.FindAllStringSubmatch(compose, -1) {
		if _, ok := out[m[1]]; !ok {
			out[m[1]] = m[2]
		}
	}
	return out
}

// TagSource lists a repository's release tags (GitHub).
type TagSource interface {
	Tags(ctx context.Context, repo string) ([]string, error)
}

// TagChecker tells whether the registry has an image tag.
type TagChecker interface {
	TagExists(ctx context.Context, image, tag string) (bool, error)
}

// Resolver finds newest releases, asking GitHub at most once per
// repository per wizard run (a failed answer is asked again after
// RetryAfter) and the registry once per tag.
type Resolver struct {
	Tags     TagSource
	Registry TagChecker
	// RetryAfter: how long a failed lookup is remembered (default 1 minute).
	RetryAfter time.Duration
	// MaxCandidates: how many releases newer than the default to try on the
	// registry, newest first (default 5, as the Updates page).
	MaxCandidates int

	mu   sync.Mutex
	tags map[string]*tagsEntry
	reg  map[string]*regEntry
}

type tagsEntry struct {
	ready chan struct{}
	tags  []string
	err   error
	at    time.Time
}

type regEntry struct {
	ready chan struct{}
	ok    bool
	err   error
	at    time.Time
}

// NewResolver returns a Resolver for GitHub and the public registries.
func NewResolver(githubToken string) *Resolver {
	return &Resolver{Tags: NewGitHub(githubToken, 10*time.Second), Registry: NewRegistry(10 * time.Second)}
}

func (r *Resolver) retryAfter() time.Duration {
	if r.RetryAfter > 0 {
		return r.RetryAfter
	}
	return time.Minute
}

func (r *Resolver) repoTags(ctx context.Context, repo string) ([]string, error) {
	r.mu.Lock()
	if r.tags == nil {
		r.tags = map[string]*tagsEntry{}
	}
	e := r.tags[repo]
	if e != nil {
		select {
		case <-e.ready:
			if e.err != nil && time.Since(e.at) >= r.retryAfter() {
				e = nil
			}
		default: // being fetched: wait for it
		}
	}
	if e == nil {
		e = &tagsEntry{ready: make(chan struct{})}
		r.tags[repo] = e
		r.mu.Unlock()
		e.tags, e.err = r.Tags.Tags(ctx, repo)
		e.at = time.Now()
		close(e.ready)
		return e.tags, e.err
	}
	r.mu.Unlock()
	select {
	case <-e.ready:
		return e.tags, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Resolver) tagExists(ctx context.Context, image, tag string) (bool, error) {
	key := image + ":" + tag
	r.mu.Lock()
	if r.reg == nil {
		r.reg = map[string]*regEntry{}
	}
	e := r.reg[key]
	if e != nil {
		select {
		case <-e.ready:
			// A pushed tag stays pushed; a missing one may appear.
			if !e.ok && time.Since(e.at) >= r.retryAfter() {
				e = nil
			}
		default:
		}
	}
	if e == nil {
		e = &regEntry{ready: make(chan struct{})}
		r.reg[key] = e
		r.mu.Unlock()
		e.ok, e.err = r.Registry.TagExists(ctx, image, tag)
		e.at = time.Now()
		close(e.ready)
		return e.ok, e.err
	}
	r.mu.Unlock()
	select {
	case <-e.ready:
		return e.ok, e.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Result is the image Newest picked.
type Result struct {
	// Image: the full reference to run.
	Image string
	// Newest: Image is the newest release (the default may be it). False:
	// Image is the default because the newest couldn't be found or pulled;
	// Note says why.
	Newest bool
	Note   string
}

// Newest is the newest release of c's repository that is newer than def
// (compose.yaml's pinned default), in def's major version, and whose image
// the registry has; else def. Pre-releases only when def is one. A new
// major version can need a change to compose.yaml, so it comes with a
// stack release (cmd/bumpimages opens no PR for it either; the Updates
// page still lists it).
func (r *Resolver) Newest(ctx context.Context, c Component, def string) Result {
	repo, tag := SplitImage(def)
	defV, ok := ParseVersion(tag)
	if !ok {
		return Result{Image: def, Note: fmt.Sprintf("the default tag %q isn't a release version", tag)}
	}
	tags, err := r.repoTags(ctx, c.Repo)
	if err != nil {
		return Result{Image: def, Note: "couldn't check GitHub: " + err.Error()}
	}
	var cands []Version
	seen := map[string]bool{}
	for _, t := range tags {
		v, ok := ParseVersion(t)
		if !ok || v.Major != defV.Major || v.Pre != "" && defV.Pre == "" || Compare(v, defV) <= 0 || seen[v.String()] {
			continue
		}
		seen[v.String()] = true
		cands = append(cands, v)
	}
	if len(cands) == 0 {
		return Result{Image: def, Newest: true}
	}
	sort.Slice(cands, func(i, j int) bool { return Compare(cands[i], cands[j]) > 0 })
	max := r.MaxCandidates
	if max <= 0 {
		max = 5
	}
	for i, v := range cands {
		if i >= max {
			break
		}
		ok, err := r.tagExists(ctx, repo, v.String())
		if err != nil {
			return Result{Image: def, Note: "couldn't check the registry: " + err.Error()}
		}
		if ok {
			return Result{Image: repo + ":" + v.String(), Newest: i == 0,
				Note: map[bool]string{true: "", false: cands[0].String() + " is released but its image isn't on the registry yet"}[i == 0]}
		}
	}
	return Result{Image: def, Note: cands[0].String() + " is released but its image isn't on the registry yet"}
}

// Pin sources.
const (
	// SourceNewest: resolved now, written to .env.
	SourceNewest = "newest"
	// SourceDefault: compose.yaml's pinned default, written to .env: the
	// newest couldn't be checked or pulled (Note says why).
	SourceDefault = "default"
	// SourceKept: the existing .env pins it; left as it is (updates are
	// the admin panel's job).
	SourceKept = "kept"
	// SourceCompose: an existing install runs it without a pin in .env
	// (compose.yaml's default); left as it is.
	SourceCompose = "compose"
)

// Pin is one component's image for an install.
type Pin struct {
	EnvVar  string `json:"env"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Version string `json:"version"`
	Default string `json:"default"`
	Source  string `json:"source"`
	Note    string `json:"note,omitempty"`
}

// Write: the install writes this pin's EnvVar to .env.
func (p Pin) Write() bool { return p.Source == SourceNewest || p.Source == SourceDefault }

// Choose picks the image of every component the install runs (profiles:
// its COMPOSE_PROFILES), given the existing .env (nil or empty for a new
// install) and compose.yaml's defaults. It never moves what an existing
// install runs: a pin in .env is kept, and a component that already runs
// on compose's default stays on it. Only components new to this server get
// the newest release. r nil: no lookups (the defaults are written).
func Choose(ctx context.Context, r *Resolver, defaults map[string]string, profiles []string, existing map[string]string) []Pin {
	want := map[string]bool{}
	for _, p := range profiles {
		want[p] = true
	}
	before := map[string]bool{}
	for _, p := range strings.Split(existing["COMPOSE_PROFILES"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			before[p] = true
		}
	}
	var pins []Pin
	var wg sync.WaitGroup
	for _, c := range Components {
		def := defaults[c.EnvVar]
		if !want[c.Profile] || def == "" {
			continue
		}
		p := Pin{EnvVar: c.EnvVar, Name: c.Name, Default: def}
		switch {
		case existing[c.EnvVar] != "":
			p.Image, p.Source = existing[c.EnvVar], SourceKept
		case before[c.Profile]:
			p.Image, p.Source = def, SourceCompose
		case r == nil:
			p.Image, p.Source, p.Note = def, SourceDefault, "release check off"
		default:
			p.Source = "?"
		}
		pins = append(pins, p)
	}
	for i := range pins {
		if pins[i].Source != "?" {
			continue
		}
		wg.Add(1)
		go func(p *Pin) {
			defer wg.Done()
			c, _ := ComponentByEnv(p.EnvVar)
			res := r.Newest(ctx, c, p.Default)
			p.Image, p.Note = res.Image, res.Note
			p.Source = SourceDefault
			if res.Newest || res.Image != p.Default {
				p.Source = SourceNewest
			}
		}(&pins[i])
	}
	wg.Wait()
	for i := range pins {
		_, pins[i].Version = SplitImage(pins[i].Image)
	}
	return pins
}

// ComponentByEnv finds a component by its .env variable.
func ComponentByEnv(env string) (Component, bool) {
	for _, c := range Components {
		if c.EnvVar == env {
			return c, true
		}
	}
	return Component{}, false
}
