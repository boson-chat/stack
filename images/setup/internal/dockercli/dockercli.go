// Package dockercli runs the docker CLI (and its compose plugin) that the
// setup image ships, against the host's Docker socket. Output lines are
// redacted before anyone sees them.
package dockercli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Redactor replaces known secret values in text.
type Redactor struct {
	mu   sync.RWMutex
	vals []string
}

func (r *Redactor) Set(vals []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vals = nil
	for _, v := range vals {
		if len(v) >= 6 {
			r.vals = append(r.vals, v)
		}
	}
}

func (r *Redactor) Add(v string) {
	if len(v) < 6 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vals = append(r.vals, v)
}

func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vals {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}

// CLI runs docker commands.
type CLI struct {
	Bin string // "docker"
	// Project dir and name for compose commands.
	Dir     string
	Project string
	Redact  *Redactor
}

func (c *CLI) env() []string {
	// A clean environment: nothing of the wizard's own leaks into compose
	// (which would otherwise read it as variable overrides).
	keep := []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY"}
	var env []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// Compose prefixes args with `compose` and the project.
func (c *CLI) Compose(args ...string) []string {
	return append([]string{"compose", "--project-directory", c.Dir, "-p", c.Project, "--ansi", "never"}, args...)
}

// Error is a failed command, with the last lines of its output.
type Error struct {
	Cmd  string
	Err  error
	Tail []string
}

func (e *Error) Error() string {
	t := strings.Join(e.Tail, "\n")
	if t == "" {
		return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
	}
	return fmt.Sprintf("%s: %v\n%s", e.Cmd, e.Err, t)
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs docker args, sending every (redacted) output line to onLine.
func (c *CLI) Run(ctx context.Context, stdin io.Reader, onLine func(string), args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = c.env()
	cmd.Dir = c.Dir
	cmd.Stdin = stdin
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return &Error{Cmd: c.describe(args), Err: err}
	}
	var tail []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			// compose progress redraws with \r; keep the last state.
			line := sc.Text()
			if i := strings.LastIndex(line, "\r"); i >= 0 {
				line = line[i+1:]
			}
			line = c.Redact.Redact(strings.TrimRight(line, " "))
			if line == "" {
				continue
			}
			tail = append(tail, line)
			if len(tail) > 30 {
				tail = tail[1:]
			}
			if onLine != nil {
				onLine(line)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return &Error{Cmd: c.describe(args), Err: err, Tail: tail}
	}
	return nil
}

// Output runs docker args and returns stdout (stderr goes into the error).
func (c *CLI) Output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = c.env()
	cmd.Dir = c.Dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(c.Redact.Redact(errb.String()))
		var tail []string
		if msg != "" {
			tail = strings.Split(msg, "\n")
		}
		return out.Bytes(), &Error{Cmd: c.describe(args), Err: err, Tail: tail}
	}
	return out.Bytes(), nil
}

func (c *CLI) bin() string {
	if c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

func (c *CLI) describe(args []string) string {
	// Never echo a command line in full: it could carry a value.
	for i, a := range args {
		if a == "compose" && i+7 < len(args) {
			return "docker compose " + args[i+7]
		}
	}
	if len(args) > 0 {
		return "docker " + args[0]
	}
	return "docker"
}

// IsNotFound: the docker binary is missing.
func IsNotFound(err error) bool { return errors.Is(err, exec.ErrNotFound) }
