package install

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

// The setup image carries a stack release bundle (scripts/release/bundle.sh)
// at StackSrc: its files go in with the bundle's own scripts/stack-apply.sh,
// which records the installed release (VERSION, .boson/stack-files.sha256)
// so the admin panel's Updates page can move the install to newer releases,
// keeps a copy of a file changed on this server (<file>.local), and runs the
// migrations once the .env is written. A StackSrc that isn't a bundle (the
// tests, a dev tree) is copied as it is.

// isBundle: src has the bundle's manifest and its stack-apply.sh.
func isBundle(src string) bool {
	for _, p := range []string{".bundle/manifest.json", ".bundle/files.sha256", "scripts/stack-apply.sh"} {
		if _, err := os.Stat(filepath.Join(src, p)); err != nil {
			return false
		}
	}
	return true
}

// stackApply runs src's stack-apply.sh (files or migrate) for dir. Plain
// lines go to log; the @@ lines are counted into a summary.
func stackApply(ctx context.Context, src, dir, cmd string, log func(string)) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", filepath.Join(src, "scripts", "stack-apply.sh"), cmd, src)
	c.Env = append(os.Environ(), "STACK_DIR="+dir, "ENV_FILE="+filepath.Join(dir, ".env"))
	c.Dir = dir
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	counts := map[string]int{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "@@STACKFILE "):
				f := strings.Fields(l)
				if len(f) == 3 {
					counts[f[1]]++
					if f[1] == "local" {
						log(f[2] + " was changed on this server: replaced, your copy is " + f[2] + ".local")
					}
				}
			case strings.HasPrefix(l, "@@MIGRATION "):
				f := strings.Fields(l)
				if len(f) >= 3 {
					counts[f[2]]++
					if f[2] != "done" {
						log("migration " + f[1] + ": " + f[2])
					}
				}
			case strings.HasPrefix(l, "@@"):
			default:
				log(l)
			}
		}
	}()
	err := c.Run()
	pw.Close()
	<-done
	if err != nil {
		return counts, fmt.Errorf("stack-apply.sh %s: %w", cmd, err)
	}
	return counts, nil
}

// installStackFiles puts the stack files in r.Dir.
func installStackFiles(ctx context.Context, r *Runner, log func(string)) error {
	if !isBundle(r.StackSrc) {
		n, err := stack.InstallFiles(r.StackSrc, r.Dir)
		if err != nil {
			return fmt.Errorf("copying the stack files to %s: %w", r.Dir, err)
		}
		log(fmt.Sprintf("stack files in %s (%d updated)", r.Dir, n))
		return nil
	}
	counts, err := stackApply(ctx, r.StackSrc, r.Dir, "files", log)
	if err != nil {
		return &StepError{Msg: err.Error(), Hint: "Check the install directory is writable, then retry."}
	}
	log(fmt.Sprintf("stack release %s in %s (%d new, %d updated, %d unchanged)", bundleVersion(r.StackSrc), r.Dir,
		counts["add"], counts["update"]+counts["unknown"]+counts["local"], counts["same"]))
	return nil
}

// runMigrations runs the bundle's migrations the install hasn't run
// (a new install: all of them, as no-ops; they only add what's missing).
func runMigrations(ctx context.Context, r *Runner, log func(string)) error {
	if !isBundle(r.StackSrc) {
		return nil
	}
	counts, err := stackApply(ctx, r.StackSrc, r.Dir, "migrate", log)
	if err != nil {
		return &StepError{Msg: err.Error(), Hint: "A stack migration failed (above); fix what it says, then retry."}
	}
	if counts["applied"]+counts["deferred"] > 0 {
		log(fmt.Sprintf("stack migrations: %d run, %d waiting for a later update", counts["applied"], counts["deferred"]))
	}
	return nil
}

func bundleVersion(src string) string {
	b, err := os.ReadFile(filepath.Join(src, "VERSION"))
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}
