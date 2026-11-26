// Package fresh is the "New Boson server" flow: domain, TLS, admin,
// features, ports, review, install, done. The pages are web/flows/fresh.js.
package fresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/dockercli"
	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/install"
	"github.com/boson-chat/stack/images/setup/internal/stack"
)

const ID = "fresh"

type Deps struct {
	InstallDir string
	Runner     *install.Runner
	DNS        *checks.DNS
	Ports      *checks.Ports
	DockerGID  func() string
	SendMail   func(ctx context.Context, s stack.SMTPAnswers, password, to, network string) error
	Redactor   *dockercli.Redactor
	// Finish closes the wizard for good (after the Done page).
	Finish func()
}

type Flow struct {
	d Deps

	mu       sync.Mutex
	answers  stack.Answers
	secrets  stack.Secrets
	gen      *stack.Generator
	existing stack.Existing
	// operPassword: generated for this install, shown once on Done.
	operPassword string
}

func New(d Deps) (*Flow, error) {
	if d.SendMail == nil {
		d.SendMail = checks.SendTestMail
	}
	f := &Flow{d: d, gen: stack.NewGenerator()}
	ex, err := stack.LoadExisting(d.InstallDir)
	if err != nil {
		return nil, err
	}
	f.existing = ex
	f.answers = ex.Answers()
	if st := d.Runner.State(); st.Flow == ID && st.Answers.Domain != "" {
		// A previous run of the wizard: its answers (with the admin's name).
		f.answers = st.Answers
		if ex.Found {
			// The files win for what they hold (the admin panel may have
			// changed things since).
			fromFiles := ex.Answers()
			fromFiles.Admin.Username = st.Answers.Admin.Username
			if fromFiles.Admin.Email == "" {
				fromFiles.Admin.Email = st.Answers.Admin.Email
			}
			f.answers = fromFiles
		}
	}
	return f, nil
}

func (f *Flow) Info() flow.Info {
	return flow.Info{
		ID:        ID,
		Title:     "New Boson server",
		Summary:   "Install the whole Boson stack on this machine: IRC, accounts, voice, video, uploads, the web client and the admin panel.",
		Available: true,
	}
}

func (f *Flow) Register(mux *http.ServeMux, p string) {
	mux.HandleFunc("GET "+p+"state", f.getState)
	mux.HandleFunc("PUT "+p+"answers", f.putAnswers)
	mux.HandleFunc("POST "+p+"check/dns", f.checkDNS)
	mux.HandleFunc("POST "+p+"check/ports", f.checkPorts)
	mux.HandleFunc("POST "+p+"check/smtp", f.checkSMTP)
	mux.HandleFunc("GET "+p+"review", f.review)
	mux.HandleFunc("POST "+p+"install", f.startInstall)
	mux.HandleFunc("GET "+p+"install", f.installState)
	mux.HandleFunc("GET "+p+"install/events", f.events)
	mux.HandleFunc("GET "+p+"done", f.done)
	mux.HandleFunc("POST "+p+"finish", f.finish)
}

func (f *Flow) needAdminPassword() bool {
	return !f.d.Runner.State().AdminCreated
}

type stateResp struct {
	Answers    stack.Answers  `json:"answers"`
	SecretsSet map[string]any `json:"secrets_set"`
	Existing   map[string]any `json:"existing"`
	Hosts      []stack.Host   `json:"hosts"`
	Install    install.State  `json:"install"`
	Running    bool           `json:"running"`
}

func (f *Flow) snapshot() stateResp {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.d.Runner.State()
	return stateResp{
		Answers: f.answers,
		SecretsSet: map[string]any{
			"smtp_password":  f.secrets.SMTPPassword != "" || f.existing.Env["SMTP_PASSWORD"] != "",
			"s3_keys":        f.secrets.S3AccessKey != "" || f.answers.Files.Storage == "s3" && f.existing.Env["FILES_S3_ACCESS_KEY"] != "",
			"admin_password": f.secrets.AdminPassword != "",
		},
		Existing: map[string]any{
			"found":         f.existing.Found,
			"dir":           f.d.InstallDir,
			"admin_created": st.AdminCreated,
			"admin_needed":  !st.AdminCreated,
		},
		Hosts:   f.answers.NeededHosts(),
		Install: st,
		Running: f.d.Runner.Running(),
	}
}

func (f *Flow) getState(w http.ResponseWriter, _ *http.Request) {
	flow.JSON(w, http.StatusOK, f.snapshot())
}

type answersReq struct {
	Answers stack.Answers `json:"answers"`
	Secrets stack.Secrets `json:"secrets"`
	// Section to validate: domain | tls | admin | features | "" (none).
	Section string `json:"section"`
}

func (f *Flow) putAnswers(w http.ResponseWriter, r *http.Request) {
	if f.d.Runner.Running() {
		flow.Error(w, http.StatusConflict, "busy", "The install is running; wait for it to finish or fail.")
		return
	}
	var req answersReq
	if err := flow.Decode(r, &req); err != nil {
		flow.Error(w, http.StatusBadRequest, "bad_request", "Invalid request.")
		return
	}
	a := req.Answers
	a.Normalize()
	f.mu.Lock()
	sec := f.secrets
	// Empty = keep what was entered before.
	if req.Secrets.SMTPPassword != "" {
		sec.SMTPPassword = req.Secrets.SMTPPassword
	}
	if req.Secrets.S3AccessKey != "" {
		sec.S3AccessKey = req.Secrets.S3AccessKey
	}
	if req.Secrets.S3SecretKey != "" {
		sec.S3SecretKey = req.Secrets.S3SecretKey
	}
	if req.Secrets.AdminPassword != "" {
		sec.AdminPassword = req.Secrets.AdminPassword
	}
	existing := f.existing
	f.mu.Unlock()
	var errs []stack.FieldError
	if req.Section != "" {
		errs = a.Validate(req.Section, sec, f.needAdminPassword())
		if req.Section == "features" && a.Files.Enabled && a.Files.Storage == "s3" {
			prevS3 := existing.Found && existing.Answers().Files.Storage == "s3"
			if (sec.S3AccessKey == "" || sec.S3SecretKey == "") && !prevS3 {
				errs = append(errs, stack.FieldError{Field: "files.s3_keys", Message: "Enter the bucket's access key and secret key."})
			}
		}
	}
	if len(errs) > 0 {
		flow.JSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": errs})
		return
	}
	f.mu.Lock()
	f.answers = a
	f.secrets = sec
	f.mu.Unlock()
	f.d.Runner.SaveAnswers(ID, a)
	flow.JSON(w, http.StatusOK, f.snapshot())
}

func (f *Flow) currentAnswers() stack.Answers {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answers
}

func (f *Flow) checkDNS(w http.ResponseWriter, r *http.Request) {
	a := f.currentAnswers()
	if !stack.ValidDomain(a.Domain) {
		flow.Error(w, http.StatusUnprocessableEntity, "no_domain", "Enter your domain first.")
		return
	}
	flow.JSON(w, http.StatusOK, f.d.DNS.Check(r.Context(), a.NeededHosts()))
}

func (f *Flow) checkPorts(w http.ResponseWriter, r *http.Request) {
	a := f.currentAnswers()
	flow.JSON(w, http.StatusOK, f.d.Ports.Check(r.Context(), checks.PortsFor(a.Voice, a.Video)))
}

func (f *Flow) checkSMTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := flow.Decode(r, &req); err != nil || req.To == "" {
		flow.Error(w, http.StatusBadRequest, "bad_request", "Say where to send the test email.")
		return
	}
	f.mu.Lock()
	a, pw := f.answers, f.secrets.SMTPPassword
	if pw == "" {
		pw = f.existing.Env["SMTP_PASSWORD"]
	}
	f.mu.Unlock()
	if a.SMTP.Host == "" {
		flow.Error(w, http.StatusUnprocessableEntity, "no_smtp", "Fill in the mail server first.")
		return
	}
	if err := f.d.SendMail(r.Context(), a.SMTP, pw, req.To, a.NetworkName); err != nil {
		flow.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	flow.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (f *Flow) plan() (stack.Plan, stack.Secrets, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Re-read: the config step of a failed attempt may have written .env.
	if ex, err := stack.LoadExisting(f.d.InstallDir); err == nil {
		f.existing = ex
	}
	gid := "999"
	if f.d.DockerGID != nil {
		gid = f.d.DockerGID()
	}
	p, err := stack.BuildPlan(f.answers, f.secrets, f.existing.Env, f.gen, gid)
	if err == nil && p.OperPassword != "" {
		f.operPassword = p.OperPassword
	}
	return p, f.secrets, err
}

func (f *Flow) review(w http.ResponseWriter, _ *http.Request) {
	p, _, err := f.plan()
	if err != nil {
		flow.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	f.mu.Lock()
	cfgOld := f.existing.Config
	f.mu.Unlock()
	example := []byte("version: 1\n")
	cfg, changed, err := stack.ApplyConfig(cfgOld, example, p.Config)
	if err != nil {
		flow.Error(w, http.StatusUnprocessableEntity, "config", err.Error())
		return
	}
	kept := 0
	for _, v := range p.Env {
		if v.Secret && v.Origin == "kept" {
			kept++
		}
	}
	flow.JSON(w, http.StatusOK, map[string]any{
		"dir":            f.d.InstallDir,
		"env":            stack.MaskedEnv(p),
		"config":         string(cfg),
		"config_changed": changed,
		"config_new":     cfgOld == nil,
		"profiles":       p.Profiles,
		"hosts":          p.Hosts,
		"kept_secrets":   kept,
		"existing":       f.existing.Found,
	})
}

func (f *Flow) startInstall(w http.ResponseWriter, r *http.Request) {
	p, sec, err := f.plan()
	if err != nil {
		flow.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	if errs := p.Answers.Validate("admin", sec, f.needAdminPassword()); len(errs) > 0 {
		flow.JSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "admin", "error": errs[0].Message, "errors": errs})
		return
	}
	// DNS is the one hard stop: without it no certificate can be issued.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	sum := f.d.DNS.Check(ctx, p.Hosts)
	cancel()
	if !sum.OK {
		flow.JSON(w, http.StatusConflict, map[string]any{"code": "dns", "error": "Some names don't point at this server yet.", "dns": sum})
		return
	}
	f.d.Redactor.Set(append(p.SecretValues(), sec.AdminPassword, sec.SMTPPassword, sec.S3SecretKey, sec.S3AccessKey))
	if err := f.d.Runner.Start(install.Job{Plan: p, AdminPassword: sec.AdminPassword}); err != nil {
		if errors.Is(err, install.ErrBusy) {
			flow.Error(w, http.StatusConflict, "busy", err.Error())
			return
		}
		flow.Error(w, http.StatusInternalServerError, "start", err.Error())
		return
	}
	flow.JSON(w, http.StatusAccepted, f.snapshot())
}

func (f *Flow) installState(w http.ResponseWriter, _ *http.Request) {
	flow.JSON(w, http.StatusOK, map[string]any{"install": f.d.Runner.State(), "running": f.d.Runner.Running()})
}

func (f *Flow) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		flow.Error(w, http.StatusInternalServerError, "stream", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := f.d.Runner.Hub.Subscribe()
	defer cancel()
	st := f.d.Runner.State()
	write := func(e install.Event) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	write(install.Event{Type: "state", State: &st})
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			write(e)
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			fl.Flush()
		}
	}
}

func (f *Flow) done(w http.ResponseWriter, _ *http.Request) {
	st := f.d.Runner.State()
	if st.Status != install.StatusDone {
		flow.Error(w, http.StatusConflict, "not_done", "The install hasn't finished.")
		return
	}
	a := st.Answers
	h := a.HostNames()
	f.mu.Lock()
	oper := f.operPassword
	f.mu.Unlock()
	links := map[string]string{
		"web":   "https://" + h[stack.RoleWeb] + "/",
		"admin": "https://" + h[stack.RoleAdmin] + "/",
		"auth":  "https://" + h[stack.RoleAuth] + "/",
		"irc":   h[stack.RoleIRC] + ":6697",
	}
	flow.JSON(w, http.StatusOK, map[string]any{
		"links":         links,
		"admin":         a.Admin.Username,
		"oper_password": oper,
		"dir":           f.d.InstallDir,
		"voice":         a.Voice,
		"video":         a.Video,
	})
}

func (f *Flow) finish(w http.ResponseWriter, _ *http.Request) {
	if st := f.d.Runner.State(); st.Status != install.StatusDone {
		flow.Error(w, http.StatusConflict, "not_done", "The install hasn't finished.")
		return
	}
	f.mu.Lock()
	f.operPassword = ""
	f.secrets = stack.Secrets{}
	f.mu.Unlock()
	f.d.Runner.SetFinished()
	flow.JSON(w, http.StatusOK, map[string]any{"ok": true})
	if f.d.Finish != nil {
		go f.d.Finish()
	}
}
