package install

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// What the Install page shows when a service is still waiting: its recent
// log lines, its last healthcheck results, a hint, and warnings about
// Traefik not getting certificates.

// HealthCheck is one healthcheck run (docker inspect .State.Health.Log).
type HealthCheck struct {
	Start    time.Time `json:"start"`
	ExitCode int       `json:"exit_code"`
	Output   string    `json:"output"`
}

// Health is a container's healthcheck state (docker inspect .State.Health).
type Health struct {
	Status        string        `json:"status"`
	FailingStreak int           `json:"failing_streak"`
	Log           []HealthCheck `json:"log"`
}

// ServiceLogs is one service's recent output, redacted and capped.
type ServiceLogs struct {
	Service string   `json:"service"`
	State   string   `json:"state"`
	Health  string   `json:"health"`
	Tail    int      `json:"tail"`
	Lines   []string `json:"lines"`
	// Truncated: lines were dropped or cut to stay under the byte cap.
	Truncated bool `json:"truncated"`
	// Checks: the last healthcheck runs, while the service isn't healthy.
	Checks []HealthCheck `json:"checks,omitempty"`
	Hint   string        `json:"hint,omitempty"`
}

// Warning is something wrong that the person can fix, shown on a step.
type Warning struct {
	ID     string   `json:"id"`
	Step   string   `json:"step"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Lines  []string `json:"lines,omitempty"`
	// Fix: the wizard step to change ("tls").
	Fix string `json:"fix,omitempty"`
}

const (
	DefaultLogTail = 100
	MaxLogTail     = 200
	maxLogBytes    = 64 << 10
	maxLineBytes   = 2 << 10
	maxChecks      = 3
	maxCheckBytes  = 1 << 10
)

var (
	ErrUnknownService = errors.New("no such service in this install")
	serviceNameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
)

// CertWaiters need their TLS certificate (Traefik gets it, certsync copies
// it into their volume) before they are ready: evrd's entrypoint waits for
// it, the others won't start their TLS listener without it.
var CertWaiters = map[string]bool{"ergo": true, "evrd": true, "quicvoiced": true, "quicvideod": true}

// waitingFor: what a service that isn't ready is waiting on, when that is
// known without asking it ("" otherwise).
func waitingFor(s ServiceStatus) string {
	switch {
	case s.Ready:
		return ""
	case s.State == "restarting":
		return "it keeps restarting: its log says why"
	case CertWaiters[s.Service] && !s.Failed:
		return "its TLS certificate (Traefik gets it, certsync copies it in)"
	}
	return ""
}

// ServiceLogs returns the last tail lines of a service's log, and its last
// healthcheck runs while it isn't healthy. Only services with a container
// in this install's compose project are answered. redact runs on every
// line (the same secret redaction as the compose output on the page).
func (r *Runner) ServiceLogs(ctx context.Context, service string, tail int, redact func(string) string) (ServiceLogs, error) {
	if !serviceNameRe.MatchString(service) {
		return ServiceLogs{}, ErrUnknownService
	}
	if tail <= 0 {
		tail = DefaultLogTail
	}
	if tail > MaxLogTail {
		tail = MaxLogTail
	}
	have, err := r.Compose.PS(ctx)
	if err != nil {
		return ServiceLogs{}, err
	}
	var row *ServiceStatus
	for i := range have {
		if have[i].Service == service {
			row = &have[i]
			break
		}
	}
	if row == nil {
		return ServiceLogs{}, ErrUnknownService
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	out := ServiceLogs{Service: service, State: row.State, Health: row.Health, Tail: tail, Lines: []string{}}
	text := r.Compose.Logs(ctx, service, tail)
	out.Lines, out.Truncated = capLines(text, tail, redact)
	if row.Health != "" && row.Health != "healthy" {
		if h, err := r.Compose.Health(ctx, service); err == nil && h != nil {
			checks := h.Log
			if len(checks) > maxChecks {
				checks = checks[len(checks)-maxChecks:]
			}
			for _, c := range checks {
				o := redact(strings.TrimSpace(c.Output))
				if len(o) > maxCheckBytes {
					o = o[:maxCheckBytes] + " …"
				}
				out.Checks = append(out.Checks, HealthCheck{Start: c.Start, ExitCode: c.ExitCode, Output: o})
			}
		}
	}
	ev := Evaluate([]string{service}, []ServiceStatus{*row})[0]
	out.Hint = waitingFor(ev)
	if out.Hint != "" {
		out.Hint = "Waiting for " + out.Hint + "."
	}
	if !ev.Ready && len(out.Lines) == 0 && len(out.Checks) > 0 {
		out.Hint = strings.TrimSpace(out.Hint + " It hasn't logged anything yet: its entrypoint may still be waiting before the program starts.")
	}
	return out, nil
}

// capLines splits text into at most n redacted lines, each at most
// maxLineBytes, maxLogBytes in all (the newest lines are kept).
func capLines(text string, n int, redact func(string) string) ([]string, bool) {
	lines := []string{}
	truncated := false
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r ")
		if l == "" {
			continue
		}
		l = redact(l)
		if len(l) > maxLineBytes {
			l = l[:maxLineBytes] + " …"
			truncated = true
		}
		lines = append(lines, l)
	}
	if len(lines) > n {
		lines, truncated = lines[len(lines)-n:], true
	}
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		size += len(lines[i]) + 1
		if size > maxLogBytes {
			return lines[i+1:], true
		}
	}
	return lines, truncated
}

// ---- Traefik's ACME errors -------------------------------------------------

var acmeErrRe = regexp.MustCompile(`error="((?:[^"\\]|\\.)*)"`)

// ACMEErrors picks Traefik's certificate errors out of its log.
func ACMEErrors(logs string) []string {
	var out []string
	for _, l := range strings.Split(logs, "\n") {
		low := strings.ToLower(l)
		if strings.Contains(l, "Unable to obtain ACME certificate") ||
			strings.Contains(low, "acme") && (strings.Contains(l, " ERR ") || strings.Contains(low, "level=error")) {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// acmeReason: the error="…" of a Traefik log line, else the line.
func acmeReason(line string) string {
	why := line
	if m := acmeErrRe.FindStringSubmatch(line); m != nil {
		why = m[1]
		if u, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
			why = u
		}
	}
	why = strings.Join(strings.Fields(why), " ")
	if len(why) > 400 {
		why = why[:400] + " …"
	}
	return why
}

// acmeWarning turns Traefik's log into a warning for step (nil: none).
func acmeWarning(step, logs string) *Warning {
	errs := ACMEErrors(logs)
	if len(errs) == 0 {
		return nil
	}
	last := errs
	if len(last) > 3 {
		last = last[len(last)-3:]
	}
	for i, l := range last {
		if len(l) > maxLineBytes {
			last[i] = l[:maxLineBytes] + " …"
		}
	}
	return &Warning{
		ID:     "acme",
		Step:   step,
		Title:  "Traefik can't get certificates",
		Detail: acmeReason(errs[len(errs)-1]),
		Lines:  append([]string(nil), last...),
		Fix:    "tls",
	}
}

// acmeWatch reads Traefik's log every few polls of a waiting step and
// publishes (or clears) the warning.
type acmeWatch struct {
	r     *Runner
	step  string
	log   func(string)
	every time.Duration
	last  time.Time
	shown string
}

func (r *Runner) newACMEWatch(step string, log func(string)) *acmeWatch {
	every := min(max(r.Timeouts.Poll*5, time.Second), 10*time.Second)
	return &acmeWatch{r: r, step: step, log: log, every: every}
}

func (a *acmeWatch) publish(w *Warning) {
	var ws []Warning
	if w != nil {
		ws = []Warning{*w}
	}
	a.r.Hub.Publish(Event{Type: "warnings", Step: a.step, Warnings: ws})
}

// Check runs on every poll; it reads the log at most every a.every.
func (a *acmeWatch) Check(ctx context.Context) {
	if time.Since(a.last) < a.every {
		return
	}
	a.last = time.Now()
	w := acmeWarning(a.step, a.r.Compose.Logs(ctx, "traefik", 200))
	switch {
	case w == nil && a.shown != "":
		a.Clear()
	case w != nil && w.Detail != a.shown:
		a.shown = w.Detail
		a.log("traefik can't get certificates: " + w.Detail)
		a.publish(w)
	}
}

// Clear removes the warning (the step got past it).
func (a *acmeWatch) Clear() {
	if a.shown != "" {
		a.shown = ""
		a.publish(nil)
	}
}

// Current: the error shown now ("" for none).
func (a *acmeWatch) Current() string { return a.shown }
