package install

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

// Job is one install attempt's input. AdminPassword is only in memory.
type Job struct {
	Plan          stack.Plan
	AdminPassword string
}

// StepError is a failure with advice for the person installing.
type StepError struct {
	Msg, Hint string
	// Code lets the page react ("need_admin_password").
	Code string
}

func (e *StepError) Error() string { return e.Msg }

type step struct {
	id, title string
	run       func(ctx context.Context, r *Runner, j *Job, log func(string)) error
}

// Runner runs the install steps, one attempt at a time.
type Runner struct {
	Dir      string // install dir, e.g. /opt/boson-stack
	StackSrc string // the image's copy of the stack files
	Compose  Compose
	Store    *Store
	Hub      *Hub
	// Edge: where Traefik listens for the TLS/verify checks (127.0.0.1).
	Edge string
	// TrustACME: verify certificates against the system roots (Let's
	// Encrypt production). Off for staging / a custom CA.
	Timeouts Timeouts
	// OnDone runs after a successful install.
	OnDone func(State)
	// Logf also gets every progress line (already redacted), for the
	// container's log.
	Logf func(step, line string)

	mu      sync.Mutex
	state   State
	running bool
	steps   []step
}

type Timeouts struct {
	Health, Certs, Verify time.Duration
	// FailFor: a service failing this long fails the health step early.
	FailFor time.Duration
	Poll    time.Duration
}

func DefaultTimeouts() Timeouts {
	return Timeouts{Health: 8 * time.Minute, Certs: 5 * time.Minute, Verify: 4 * time.Minute, FailFor: 60 * time.Second, Poll: 2 * time.Second}
}

// Steps: the install steps in order.
func Steps() []StepState {
	var out []StepState
	for _, s := range allSteps() {
		out = append(out, StepState{ID: s.id, Title: s.title, Status: StatusPending})
	}
	return out
}

func allSteps() []step {
	return []step{
		{"config", "Write the configuration", stepConfig},
		{"pull", "Download the images", stepPull},
		{"start", "Start the services", stepStart},
		{"health", "Wait for every service to be healthy", stepHealth},
		{"certs", "Get TLS certificates", stepCerts},
		{"admin", "Create your admin account", stepAdmin},
		{"verify", "Check it all works", stepVerify},
	}
}

// NewRunner loads the saved state (a previous attempt).
func NewRunner(r *Runner) (*Runner, error) {
	st, _, err := r.Store.Load()
	if err != nil {
		return nil, err
	}
	if st.Status == StatusDone {
		// A new wizard on an installed server (install.sh again): start
		// from the top, with what's installed (secrets, admin) kept.
		st.Status, st.Steps, st.Finished = "idle", nil, false
	}
	r.state = st
	r.steps = allSteps()
	if r.Edge == "" {
		r.Edge = "127.0.0.1"
	}
	if r.Timeouts == (Timeouts{}) {
		r.Timeouts = DefaultTimeouts()
	}
	return r, nil
}

// State returns a copy of the current state.
func (r *Runner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state
	s.Steps = append([]StepState(nil), r.state.Steps...)
	return s
}

func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// update changes the state, saves it and tells the page.
func (r *Runner) update(fn func(*State)) {
	r.mu.Lock()
	fn(&r.state)
	s := r.state
	s.Steps = append([]StepState(nil), r.state.Steps...)
	r.mu.Unlock()
	_ = r.Store.Save(s)
	r.Hub.Publish(Event{Type: "state", State: &s})
}

// SetFinished marks the wizard closed after success.
func (r *Runner) SetFinished() { r.update(func(s *State) { s.Finished = true }) }

// SaveAnswers keeps the answers (not secrets) for a restart of the wizard.
func (r *Runner) SaveAnswers(flow string, a stack.Answers) {
	r.update(func(s *State) { s.Flow = flow; s.Answers = a })
}

var ErrBusy = errors.New("an install is already running")

// Start begins an attempt in the background. Every step runs again (they
// are idempotent), so a retry after a failure picks up where it broke.
func (r *Runner) Start(j Job) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return ErrBusy
	}
	r.running = true
	r.mu.Unlock()
	r.Hub.ClearLogs()
	r.update(func(s *State) {
		s.Status = StatusRunning
		s.Answers = j.Plan.Answers
		s.Attempts++
		s.StartedAt = time.Now().UTC()
		s.CompletedAt = time.Time{}
		s.Steps = Steps()
	})
	go r.run(j)
	return nil
}

func (r *Runner) run(j Job) {
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()
	ctx := context.Background()
	for _, s := range r.steps {
		id := s.id
		r.update(func(st *State) {
			p := st.Step(id)
			p.Status, p.Started = StatusRunning, time.Now().UTC()
		})
		log := func(line string) {
			r.Hub.Publish(Event{Type: "log", Step: id, Line: line})
			if r.Logf != nil {
				r.Logf(id, line)
			}
		}
		log("== " + s.title)
		err := s.run(ctx, r, &j, log)
		if err != nil {
			msg, hint, code := err.Error(), "", ""
			var se *StepError
			if errors.As(err, &se) {
				msg, hint, code = se.Msg, se.Hint, se.Code
			}
			if code != "" {
				hint = code + ": " + hint
			}
			log("FAILED: " + firstLine(msg))
			r.update(func(st *State) {
				p := st.Step(id)
				p.Status, p.Error, p.Hint, p.Finished = StatusFailed, msg, hint, time.Now().UTC()
				st.Status = StatusFailed
			})
			return
		}
		r.update(func(st *State) {
			p := st.Step(id)
			p.Status, p.Finished = StatusDone, time.Now().UTC()
		})
	}
	r.update(func(st *State) {
		st.Status = StatusDone
		st.CompletedAt = time.Now().UTC()
	})
	if r.OnDone != nil {
		r.OnDone(r.State())
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- steps ----------------------------------------------------------------

func stepConfig(_ context.Context, r *Runner, j *Job, log func(string)) error {
	n, err := stack.InstallFiles(r.StackSrc, r.Dir)
	if err != nil {
		return fmt.Errorf("copying the stack files to %s: %w", r.Dir, err)
	}
	log(fmt.Sprintf("stack files in %s (%d updated)", r.Dir, n))
	ex, err := stack.LoadExisting(r.Dir)
	if err != nil {
		return err
	}
	envText, changed := stack.MergeEnv(ex.EnvRaw, j.Plan.Env,
		"# Written by Boson setup on "+time.Now().UTC().Format("2006-01-02")+". See .env.example for every option.")
	if changed {
		if err := stack.WriteFileAtomic(filepath.Join(r.Dir, ".env"), []byte(envText), 0o600); err != nil {
			return err
		}
		log(".env written (mode 0600)")
	} else {
		log(".env unchanged")
	}
	// Even unchanged, make sure it's private.
	if err := os.Chmod(filepath.Join(r.Dir, ".env"), 0o600); err != nil {
		return err
	}
	example, err := os.ReadFile(filepath.Join(r.Dir, "config", "boson.example.yaml"))
	if err != nil {
		return err
	}
	cfg, changed, err := stack.ApplyConfig(ex.Config, example, j.Plan.Config)
	if err != nil {
		return &StepError{Msg: err.Error(), Hint: "Fix config/boson.yaml by hand (or move it away), then retry."}
	}
	if changed {
		if err := stack.WriteFileAtomic(filepath.Join(r.Dir, "config", "boson.yaml"), cfg, 0o644); err != nil {
			return err
		}
		log("config/boson.yaml written")
	} else {
		log("config/boson.yaml unchanged")
	}
	return os.MkdirAll(filepath.Join(r.Dir, "config", "branding"), 0o755)
}

func stepPull(ctx context.Context, r *Runner, _ *Job, log func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := r.Compose.Pull(ctx, log); err != nil {
		return &StepError{Msg: err.Error(), Hint: "Check this server can reach the image registry (harbor.floret.dev, Docker Hub, ghcr.io), then retry."}
	}
	return nil
}

func stepStart(ctx context.Context, r *Runner, _ *Job, log func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// A container left "created" by a failed attempt (say its port was
	// taken) keeps a broken network setup: starting it again gives a
	// container with no published ports. Remove those so up recreates them.
	if have, err := r.Compose.PS(ctx); err == nil {
		var stale []string
		for _, s := range have {
			if s.State == "created" {
				stale = append(stale, s.Service)
			}
		}
		if len(stale) > 0 {
			log("removing containers a failed attempt left behind: " + strings.Join(stale, ", "))
			if err := r.Compose.Remove(ctx, stale); err != nil {
				return err
			}
		}
	}
	if err := r.Compose.Up(ctx, log); err != nil {
		hint := "Look at the lines above, fix what they point at, then retry."
		if s := err.Error(); strings.Contains(s, "address already in use") || strings.Contains(s, "port is already allocated") {
			hint = "Another program uses one of the stack's ports (see the error). Stop it, then retry."
		}
		return &StepError{Msg: err.Error(), Hint: hint}
	}
	return nil
}

func stepHealth(ctx context.Context, r *Runner, _ *Job, log func(string)) error {
	want, err := r.Compose.Services(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(r.Timeouts.Health)
	failingSince := map[string]time.Time{}
	lastLine := ""
	for {
		have, err := r.Compose.PS(ctx)
		if err != nil {
			return err
		}
		st := Evaluate(want, have)
		r.Hub.Publish(Event{Type: "services", Services: st})
		var waiting []string
		var failed []string
		for _, s := range st {
			if s.Ready {
				delete(failingSince, s.Service)
				continue
			}
			waiting = append(waiting, s.Service)
			if s.Failed {
				if _, ok := failingSince[s.Service]; !ok {
					failingSince[s.Service] = time.Now()
				}
				if OneShot[s.Service] || time.Since(failingSince[s.Service]) >= r.Timeouts.FailFor {
					failed = append(failed, s.Service)
				}
			}
		}
		if len(waiting) == 0 {
			log(fmt.Sprintf("all %d services are up", len(st)))
			return nil
		}
		if line := "waiting for " + strings.Join(waiting, ", "); line != lastLine {
			log(line)
			lastLine = line
		}
		if len(failed) > 0 || time.Now().After(deadline) {
			who := failed
			if len(who) == 0 {
				who = waiting
			}
			var b strings.Builder
			for _, s := range who {
				fmt.Fprintf(&b, "\n--- %s, last log lines:\n%s", s, r.Compose.Logs(ctx, s, 15))
			}
			msg := "not healthy: " + strings.Join(who, ", ")
			if len(failed) == 0 {
				msg = "still not ready after " + r.Timeouts.Health.String() + ": " + strings.Join(who, ", ")
			}
			return &StepError{Msg: msg + b.String(), Hint: "The log lines in the error usually say why. Fix it, then retry."}
		}
		time.Sleep(r.Timeouts.Poll)
	}
}

// edgeCert dials Traefik with SNI host and returns the certificate it serves.
func (r *Runner) edgeCert(ctx context.Context, host string) (*x509.Certificate, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config: &tls.Config{ServerName: host, InsecureSkipVerify: true}} // inspected below
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(r.Edge, "443"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("no certificate")
	}
	return certs[0], nil
}

func stepCerts(ctx context.Context, r *Runner, j *Job, log func(string)) error {
	deadline := time.Now().Add(r.Timeouts.Certs)
	pending := map[string]string{}
	for _, h := range j.Plan.Hosts {
		pending[h.Name] = "not checked yet"
	}
	for len(pending) > 0 {
		for host := range pending {
			cert, err := r.edgeCert(ctx, host)
			switch {
			case err != nil:
				pending[host] = err.Error()
			case strings.Contains(cert.Issuer.CommonName, "TRAEFIK DEFAULT CERT") || cert.VerifyHostname(host) != nil:
				pending[host] = "Traefik is still serving its placeholder certificate"
			default:
				log(fmt.Sprintf("%s: certificate from %s, valid until %s", host, issuerName(cert), cert.NotAfter.Format("2006-01-02")))
				delete(pending, host)
			}
		}
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			var names []string
			for h, why := range pending {
				names = append(names, h+" ("+why+")")
			}
			logs := r.Compose.Logs(ctx, "traefik", 200)
			var acme []string
			for _, l := range strings.Split(logs, "\n") {
				if strings.Contains(strings.ToLower(l), "acme") || strings.Contains(l, "Unable to obtain") {
					acme = append(acme, l)
				}
			}
			if len(acme) > 8 {
				acme = acme[len(acme)-8:]
			}
			return &StepError{
				Msg:  "no certificate yet for " + strings.Join(names, ", ") + "\n" + strings.Join(acme, "\n"),
				Hint: "Let's Encrypt must reach this server on port 80 at each of these names. Check DNS and that 80/tcp is open (also in your provider's firewall), then retry.",
			}
		}
		time.Sleep(3 * time.Second)
	}
	return nil
}

func issuerName(c *x509.Certificate) string {
	if len(c.Issuer.Organization) > 0 {
		return c.Issuer.Organization[0] + " " + c.Issuer.CommonName
	}
	return c.Issuer.CommonName
}

// Users are created with auth's CLI in the auth container (README:
// `auth user create --role owner`): Owner is the network role that may do
// everything, including managing other admins (docs/roles.md).
func stepAdmin(ctx context.Context, r *Runner, j *Job, log func(string)) error {
	a := j.Plan.Answers.Admin
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exists, role, email, err := findUser(ctx, r.Compose, a.Username)
	if err != nil {
		return &StepError{Msg: "couldn't ask auth for its users: " + err.Error(), Hint: "auth may still be starting: retry in a moment."}
	}
	if exists && !r.State().AdminCreated && !strings.EqualFold(email, a.Email) {
		// Someone else has this name (it was free to sign up while the
		// stack was starting): never hand them the Owner role.
		return &StepError{Msg: "an account named " + a.Username + " already exists with another email address",
			Hint: "Pick another admin username on the Admin step, then retry."}
	}
	if exists {
		if role == "owner" {
			log(a.Username + " exists and is the Owner already: nothing to do")
		} else {
			if _, err := r.Compose.Exec(ctx, "auth", nil, "./auth", "user", "set-role", a.Username, "owner"); err != nil {
				return fmt.Errorf("making %s the Owner: %w", a.Username, err)
			}
			log(a.Username + " exists: given the Owner role")
		}
		r.update(func(s *State) { s.AdminCreated = true })
		return nil
	}
	if j.AdminPassword == "" {
		return &StepError{Code: "need_admin_password", Msg: "the admin account doesn't exist yet and setup no longer has its password (setup was restarted)",
			Hint: "Enter the admin password again on the Admin step, then retry."}
	}
	out, err := r.Compose.Exec(ctx, "auth", strings.NewReader(j.AdminPassword+"\n"),
		"./auth", "user", "create", "--username", a.Username, "--email", a.Email, "--role", "owner", "--password-stdin")
	if err != nil {
		return fmt.Errorf("creating %s: %w", a.Username, err)
	}
	line := "created " + a.Username
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "created ") {
			line = l
		}
	}
	log(line)
	r.update(func(s *State) { s.AdminCreated = true })
	return nil
}

// findUser looks a username up in `auth user list <name>` (a search, so
// the exact name is matched here).
func findUser(ctx context.Context, c Compose, name string) (exists bool, role, email string, err error) {
	out, err := c.Exec(ctx, "auth", nil, "./auth", "user", "list", name, "--limit", "200")
	if err != nil {
		return false, "", "", err
	}
	// ID USERNAME EMAIL VERIFIED ROLE DISABLED (EMAIL may be empty)
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 && strings.EqualFold(f[1], name) {
			if len(f) >= 6 {
				email = f[2]
			}
			return true, f[len(f)-2], email, nil
		}
	}
	return false, "", "", nil
}

func stepVerify(ctx context.Context, r *Runner, j *Job, log func(string)) error {
	p := j.Plan
	h := p.Answers.HostNames()
	type probe struct{ host, path string }
	probes := []probe{{h[stack.RoleAuth], "/v1/config"}, {h[stack.RoleWeb], "/"}, {h[stack.RoleAdmin], "/"}}
	if p.Answers.Voice {
		probes = append(probes, probe{h[stack.RoleVoice], "/healthz"})
	}
	if p.Answers.Files.Enabled {
		probes = append(probes, probe{h[stack.RoleFiles], "/v1/config"})
	}
	deadline := time.Now().Add(r.Timeouts.Verify)
	for _, pr := range probes {
		for {
			code, err := r.get(ctx, pr.host, pr.path)
			if err == nil && code < 500 && code != 404 {
				log(fmt.Sprintf("https://%s%s answers (%d)", pr.host, pr.path, code))
				break
			}
			if time.Now().After(deadline) {
				why := fmt.Sprint(err)
				if err == nil {
					why = fmt.Sprintf("HTTP %d", code)
				}
				return &StepError{Msg: fmt.Sprintf("https://%s%s: %s", pr.host, pr.path, why), Hint: "The service is up but Traefik can't reach it yet. Retry in a moment."}
			}
			time.Sleep(2 * time.Second)
		}
	}
	// IRC over TLS on 6697, with its certificate.
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{ServerName: h[stack.RoleIRC], InsecureSkipVerify: true}}
	for {
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(r.Edge, "6697"))
		if err == nil {
			cert := c.(*tls.Conn).ConnectionState().PeerCertificates[0]
			c.Close()
			if cert.VerifyHostname(h[stack.RoleIRC]) == nil {
				log("IRC answers on 6697 with " + h[stack.RoleIRC] + "'s certificate")
				return nil
			}
			err = errors.New("Ergo doesn't have its certificate yet")
		}
		if time.Now().After(deadline) {
			return &StepError{Msg: "IRC on port 6697: " + err.Error(), Hint: "certsync hands Ergo its certificate within a minute of Traefik getting it. Retry in a moment."}
		}
		time.Sleep(2 * time.Second)
	}
}

func (r *Runner) get(ctx context.Context, host, path string) (int, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: host, InsecureSkipVerify: true}, // the certs step checked them
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(r.Edge, "443"))
		},
		Proxy: nil,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// DevSkipNetwork replaces the certificate and verify steps (which talk to
// Traefik on :443) with no-ops: for tests and the dev server only.
func DevSkipNetwork(r *Runner) {
	for i := range r.steps {
		switch r.steps[i].id {
		case "certs", "verify":
			id := r.steps[i].id
			r.steps[i].run = func(_ context.Context, _ *Runner, _ *Job, log func(string)) error {
				log(id + ": skipped (no network in this mode)")
				return nil
			}
		}
	}
}
