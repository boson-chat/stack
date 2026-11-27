// bumpimages moves compose.yaml's pinned *_IMAGE defaults to the newest
// releases, with the wizard's own rules (internal/releases: GitHub
// Releases, same major version, the image is on the registry), so a
// stack checkout and a setup image built from it start current. The
// "bump images" workflow (.github/workflows/bump-images.yml) runs it and
// opens a pull request; the full CI (e2e included) then tests the new
// versions before anyone merges.
//
//	go run ./cmd/bumpimages -root ../..   (GITHUB_TOKEN raises the API limit)
//
// It rewrites, in the repository at -root: the defaults in compose.yaml;
// every other mention of the old default reference (docs/*.md, README.md,
// .env.example); and .env.example's commented "#X_IMAGE=…" examples. It
// prints a Markdown summary (the pull request's body) and nothing when
// everything is current.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/releases"
)

func main() {
	root := flag.String("root", ".", "the stack repository")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := releases.NewResolver(os.Getenv("GITHUB_TOKEN"))
	if err := bump(ctx, *root, r, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "bumpimages:", err)
		os.Exit(1)
	}
}

// mentionsNoBump: a line about a variable whose default never moves (it
// may name the same image reference as one that does).
func mentionsNoBump(line string) bool {
	for _, c := range releases.Components {
		if c.NoBump && strings.Contains(line, c.EnvVar) {
			return true
		}
	}
	return false
}

type change struct {
	c        releases.Component
	from, to string
	note     string
}

func bump(ctx context.Context, root string, r *releases.Resolver, out io.Writer) error {
	composePath := filepath.Join(root, "compose.yaml")
	b, err := os.ReadFile(composePath)
	if err != nil {
		return err
	}
	defaults := releases.ComposeDefaults(string(b))
	var changes []change
	var notes []string
	for _, c := range releases.Components {
		def := defaults[c.EnvVar]
		if c.NoBump || def == "" {
			continue
		}
		res := r.Newest(ctx, c, def)
		if res.Note != "" {
			notes = append(notes, fmt.Sprintf("%s: %s", c.Name, res.Note))
		}
		if res.Image != def {
			changes = append(changes, change{c: c, from: def, to: res.Image})
		}
	}
	if len(changes) == 0 {
		if len(notes) > 0 {
			fmt.Fprintln(os.Stderr, "nothing to bump; notes:\n  "+strings.Join(notes, "\n  "))
		}
		return nil
	}

	// compose.yaml: only the ${X_IMAGE:-…} defaults.
	compose := string(b)
	for _, ch := range changes {
		compose = strings.ReplaceAll(compose, "${"+ch.c.EnvVar+":-"+ch.from+"}", "${"+ch.c.EnvVar+":-"+ch.to+"}")
	}
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		return err
	}
	// Mentions of the old default elsewhere, and .env.example's examples.
	files, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	files = append(files, filepath.Join(root, "README.md"), filepath.Join(root, ".env.example"))
	for _, path := range files {
		old, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lines := strings.Split(string(old), "\n")
		for i, line := range lines {
			if mentionsNoBump(line) {
				continue
			}
			for _, ch := range changes {
				// Not inside a longer tag (…:0.89.0 vs …:0.89.01).
				line = regexp.MustCompile(regexp.QuoteMeta(ch.from)+`\b`).ReplaceAllString(line, ch.to)
			}
			lines[i] = line
		}
		s := strings.Join(lines, "\n")
		for _, ch := range changes {
			if filepath.Base(path) == ".env.example" {
				repo, _ := releases.SplitImage(ch.to)
				re := regexp.MustCompile(`(?m)^#` + ch.c.EnvVar + `=` + regexp.QuoteMeta(repo) + `:[^\s]+$`)
				s = re.ReplaceAllString(s, "#"+ch.c.EnvVar+"="+ch.to)
			}
		}
		if s != string(old) {
			if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
				return err
			}
		}
	}

	fmt.Fprintln(out, "Newer releases of the stack's images (compose.yaml defaults; what a fresh install gets when the setup wizard can't ask GitHub, and what `docker compose` runs without a pin in `.env`):")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "| Component | From | To | Release |")
	fmt.Fprintln(out, "|---|---|---|---|")
	for _, ch := range changes {
		_, ft := releases.SplitImage(ch.from)
		_, tt := releases.SplitImage(ch.to)
		fmt.Fprintf(out, "| %s (`%s`) | %s | **%s** | https://github.com/%s/releases/tag/v%s |\n", ch.c.Name, ch.c.EnvVar, ft, tt, ch.c.Repo, tt)
	}
	if len(notes) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Notes:")
		for _, n := range notes {
			fmt.Fprintln(out, "- "+n)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "`EVRD_LINKS_IMAGE` is never bumped (a new default would recreate evrd-links and drop every Boson user off IRC). New major versions aren't bumped automatically.")
	return nil
}
