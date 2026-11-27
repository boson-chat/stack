package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/dockercli"
)

func logsRunner(t *testing.T, fc *FakeCompose) *Runner {
	t.Helper()
	return newRunner(t, t.TempDir(), stackSrc(t), fc)
}

func TestServiceLogsOnlyThisProjectsServices(t *testing.T) {
	r := logsRunner(t, &FakeCompose{})
	ctx := context.Background()
	for _, name := range []string{"", "../traefik", "traefik/../../etc", "Traefik", "-traefik", "web;id", "nope", "boson-traefik-1",
		strings.Repeat("a", 64)} {
		if _, err := r.ServiceLogs(ctx, name, 10, nil); !errors.Is(err, ErrUnknownService) {
			t.Errorf("%q: %v", name, err)
		}
	}
	out, err := r.ServiceLogs(ctx, "traefik", 10, nil)
	if err != nil || out.Service != "traefik" || len(out.Lines) != 1 || out.Lines[0] != "traefik: (fake log)" || out.Checks != nil {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestServiceLogsTailAndByteCaps(t *testing.T) {
	var many []string
	for i := range 500 {
		many = append(many, fmt.Sprintf("line %d", i))
	}
	big := strings.Repeat("x", 1000)
	var bigs []string
	for range 150 {
		bigs = append(bigs, big)
	}
	fc := &FakeCompose{LogLines: map[string][]string{"web": many, "auth": bigs, "ergo": {strings.Repeat("y", 5000), "", "  ", "ok\r"}}}
	r := logsRunner(t, fc)
	ctx := context.Background()

	out, _ := r.ServiceLogs(ctx, "web", 0, nil)
	if out.Tail != DefaultLogTail || len(out.Lines) != DefaultLogTail || out.Lines[99] != "line 499" {
		t.Fatalf("default tail: %d %d", out.Tail, len(out.Lines))
	}
	out, _ = r.ServiceLogs(ctx, "web", 100000, nil)
	if out.Tail != MaxLogTail || len(out.Lines) != MaxLogTail || out.Lines[0] != "line 300" {
		t.Fatalf("max tail: %d %d %q", out.Tail, len(out.Lines), out.Lines[0])
	}
	out, _ = r.ServiceLogs(ctx, "auth", 200, nil)
	size := 0
	for _, l := range out.Lines {
		size += len(l) + 1
	}
	if !out.Truncated || size > maxLogBytes || len(out.Lines) < 60 {
		t.Fatalf("byte cap: %d lines, %d bytes, truncated %v", len(out.Lines), size, out.Truncated)
	}
	out, _ = r.ServiceLogs(ctx, "ergo", 10, nil)
	if len(out.Lines) != 2 || len(out.Lines[0]) > maxLineBytes+4 || out.Lines[1] != "ok" || !out.Truncated {
		t.Fatalf("line cap: %+v", out.Lines)
	}
}

func TestServiceLogsRedactsAndShowsHealthchecks(t *testing.T) {
	red := &dockercli.Redactor{}
	red.Set([]string{"hunter2-password", "tok-abcdef"})
	fc := &FakeCompose{
		Svc:          []string{"evrd", "traefik"},
		Slow:         map[string]time.Duration{"evrd": time.Hour},
		LogLines:     map[string][]string{"evrd": {}, "traefik": {"password=hunter2-password", "fine"}},
		HealthOutput: map[string]string{"evrd": "wget: can't connect to remote host (127.0.0.1): Connection refused tok-abcdef"},
	}
	fc.upAt = time.Now()
	r := logsRunner(t, fc)
	ctx := context.Background()
	out, err := r.ServiceLogs(ctx, "traefik", 10, red.Redact)
	if err != nil || out.Lines[0] != "password=[redacted]" {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = r.ServiceLogs(ctx, "evrd", 10, red.Redact)
	if err != nil {
		t.Fatal(err)
	}
	if out.Health != "starting" || len(out.Checks) != 3 || len(out.Lines) != 0 {
		t.Fatalf("%+v", out)
	}
	if c := out.Checks[2]; c.ExitCode != 1 || c.Output != "wget: can't connect to remote host (127.0.0.1): Connection refused [redacted]" {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(out.Hint, "TLS certificate") || !strings.Contains(out.Hint, "hasn't logged anything") {
		t.Fatal(out.Hint)
	}
}

func TestWaitingFor(t *testing.T) {
	ev := Evaluate([]string{"ergo", "web", "auth", "evrd"}, []ServiceStatus{
		{Service: "ergo", State: "running", Health: "starting"},
		{Service: "web", State: "restarting"},
		{Service: "auth", State: "running", Health: "healthy"},
		{Service: "evrd", State: "running", Health: "unhealthy"},
	})
	want := []string{"its TLS certificate (Traefik gets it, certsync copies it in)", "it keeps restarting: its log says why", "", ""}
	for i, s := range ev {
		if s.Waiting != want[i] {
			t.Errorf("%s: %q", s.Service, s.Waiting)
		}
	}
}

const traefikACME = `2026-11-20T10:00:00Z INF Starting provider *acme.Provider
2026-11-20T10:00:01Z ERR Unable to obtain ACME certificate for domains error="unable to generate a certificate for the domains [irc.boson.test]: acme: error: 0 :: POST :: https://acme.example/dir :: \"tls: failed to verify certificate: x509: certificate signed by unknown authority\"" ACME CA=https://acme.example/dir domains=["irc.boson.test"] providerName=le.acme
2026-11-20T10:00:02Z INF something else`

func TestACMEWarning(t *testing.T) {
	if got := ACMEErrors(traefikACME); len(got) != 1 {
		t.Fatal(got)
	}
	if acmeWarning("health", "all fine\nlevel=info msg=acme ok") != nil {
		t.Fatal("warning without errors")
	}
	w := acmeWarning("health", traefikACME+"\n2026-11-20T10:00:03Z ERR acme: some other error")
	if w == nil || w.Step != "health" || w.Fix != "tls" || len(w.Lines) != 2 || !strings.HasSuffix(w.Detail, "ERR acme: some other error") {
		t.Fatalf("%+v", w)
	}
	w = acmeWarning("health", traefikACME)
	if !strings.HasPrefix(w.Detail, "unable to generate a certificate") || !strings.Contains(w.Detail, `"tls: failed to verify certificate`) {
		t.Fatal(w.Detail)
	}
	long := `x ERR Unable to obtain ACME certificate error="` + strings.Repeat("e", 3000) + `"`
	if w := acmeWarning("certs", long); len(w.Detail) > 410 || len(w.Lines[0]) > maxLineBytes+4 {
		t.Fatal(len(w.Detail), len(w.Lines[0]))
	}
}

func TestHealthStepWarnsAboutACME(t *testing.T) {
	fc := &FakeCompose{
		Slow:      map[string]time.Duration{"ergo": 300 * time.Millisecond},
		ACMEError: "acme: error: tls: failed to verify certificate",
	}
	r := logsRunner(t, fc)
	ch, cancel := r.Hub.Subscribe()
	defer cancel()
	if err := r.Start(Job{Plan: plan(t, nil), AdminPassword: "long enough password"}); err != nil {
		t.Fatal(err)
	}
	st := wait(t, r)
	if st.Status != StatusDone {
		t.Fatalf("%+v", st)
	}
	var shown, cleared, logged bool
	for len(ch) > 0 {
		e := <-ch
		switch {
		case e.Type == "warnings" && len(e.Warnings) == 1 && e.Step == "health":
			shown = shown || strings.Contains(e.Warnings[0].Detail, "failed to verify certificate")
		case e.Type == "warnings" && shown && len(e.Warnings) == 0:
			cleared = true
		case e.Type == "log" && strings.Contains(e.Line, "traefik can't get certificates"):
			logged = true
		case e.Type == "services":
			for _, s := range e.Services {
				if s.Service == "ergo" && !s.Ready && s.Waiting == "" {
					t.Error("ergo waiting without a reason")
				}
			}
		}
	}
	if !shown || !cleared || !logged {
		t.Fatalf("shown %v cleared %v logged %v", shown, cleared, logged)
	}
}

func TestHealthStepFailsWithACMEHint(t *testing.T) {
	fc := &FakeCompose{
		Slow:      map[string]time.Duration{"ergo": time.Hour},
		ACMEError: "acme: wrong CA",
	}
	r := logsRunner(t, fc)
	r.Timeouts.Health = 1200 * time.Millisecond
	if err := r.Start(Job{Plan: plan(t, nil), AdminPassword: "long enough password"}); err != nil {
		t.Fatal(err)
	}
	st := wait(t, r)
	h := st.Step("health")
	if h.Status != StatusFailed || !strings.Contains(h.Error, "acme: wrong CA") || !strings.Contains(h.Hint, "TLS step") {
		t.Fatalf("%+v", h)
	}
	// The warning stays up after the failure; a new attempt clears it.
	ch, cancel := r.Hub.Subscribe()
	var warned bool
	for len(ch) > 0 {
		if e := <-ch; e.Type == "warnings" && len(e.Warnings) == 1 {
			warned = true
		}
	}
	cancel()
	if !warned {
		t.Fatal("warning gone after the failure")
	}
}

func TestParseHealth(t *testing.T) {
	if h, err := parseHealth([]byte("null\n")); h != nil || err != nil {
		t.Fatal(h, err)
	}
	h, err := parseHealth([]byte(`{"Status":"starting","FailingStreak":4,"Log":[{"Start":"2026-11-20T10:00:00.5Z","End":"2026-11-20T10:00:01Z","ExitCode":1,"Output":"refused\n"}]}`))
	if err != nil || h.Status != "starting" || h.FailingStreak != 4 || len(h.Log) != 1 || h.Log[0].Output != "refused\n" || h.Log[0].Start.IsZero() {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := parseHealth([]byte("{")); err == nil {
		t.Fatal("bad json")
	}
}

// A docker CLI stand-in: compose ps -q prints an ID, inspect the health.
func fakeDocker(t *testing.T, id string) *dockercli.CLI {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := `#!/bin/sh
echo "$@" >> "` + argsFile + `"
for a in "$@"; do
  case "$a" in
    inspect) echo '{"Status":"unhealthy","FailingStreak":9,"Log":[{"ExitCode":1,"Output":"secret-value-123 refused"}]}'; exit 0;;
    ps) echo "` + id + `"; exit 0;;
  esac
done
exit 1
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	red := &dockercli.Redactor{}
	red.Add("secret-value-123")
	return &dockercli.CLI{Bin: bin, Dir: dir, Project: "boson", Redact: red}
}

func TestDockerComposeHealth(t *testing.T) {
	ctx := context.Background()
	d := &DockerCompose{CLI: fakeDocker(t, "0123456789abcdef0123")}
	h, err := d.Health(ctx, "evrd")
	if err != nil || h.Status != "unhealthy" || len(h.Log) != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	args, _ := os.ReadFile(filepath.Join(d.CLI.Dir, "args"))
	if !strings.Contains(string(args), "ps -a -q evrd") || !strings.Contains(string(args), "inspect --format {{json .State.Health}} 0123456789abcdef0123") {
		t.Fatal(string(args))
	}
	// Anything but a container ID from compose ps is never inspected.
	for _, id := range []string{"", "--help", "../x", "boson-evrd-1"} {
		d := &DockerCompose{CLI: fakeDocker(t, id)}
		if _, err := d.Health(ctx, "evrd"); !errors.Is(err, ErrUnknownService) {
			t.Errorf("%q: %v", id, err)
		}
	}
}
