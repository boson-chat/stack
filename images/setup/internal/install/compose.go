package install

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/boson-chat/stack/images/setup/internal/dockercli"
)

// ServiceStatus is one compose service's container state.
type ServiceStatus struct {
	Service  string `json:"service"`
	State    string `json:"state"`  // running | exited | restarting | created | missing
	Health   string `json:"health"` // healthy | starting | unhealthy | ""
	ExitCode int    `json:"exit_code"`
	// Ready: running (and healthy when it has a healthcheck), or a one-shot
	// that exited 0.
	Ready bool `json:"ready"`
	// Failed: exited non-zero or unhealthy.
	Failed bool `json:"failed"`
}

// Compose is what the install needs from docker compose.
type Compose interface {
	Pull(ctx context.Context, onLine func(string)) error
	Up(ctx context.Context, onLine func(string)) error
	// Services: the services the current .env's profiles enable.
	Services(ctx context.Context) ([]string, error)
	PS(ctx context.Context) ([]ServiceStatus, error)
	Logs(ctx context.Context, service string, tail int) string
	Exec(ctx context.Context, service string, stdin io.Reader, args ...string) (string, error)
	// Remove deletes the services' containers (compose rm -fs).
	Remove(ctx context.Context, services []string) error
}

// OneShot services exit 0 when done (restart: "no" in compose.yaml).
var OneShot = map[string]bool{"config-init": true, "voice-data-init": true, "api-migrate": true,
	"directory-media-init": true, "files-data-init": true, "minio-init": true}

// DockerCompose runs compose through the docker CLI.
type DockerCompose struct{ CLI *dockercli.CLI }

func (d *DockerCompose) Pull(ctx context.Context, onLine func(string)) error {
	return d.CLI.Run(ctx, nil, onLine, d.CLI.Compose("--progress", "plain", "pull", "--policy", "missing")...)
}

func (d *DockerCompose) Up(ctx context.Context, onLine func(string)) error {
	return d.CLI.Run(ctx, nil, onLine, d.CLI.Compose("--progress", "plain", "up", "-d", "--no-build")...)
}

func (d *DockerCompose) Services(ctx context.Context) ([]string, error) {
	out, err := d.CLI.Output(ctx, d.CLI.Compose("config", "--services")...)
	if err != nil {
		return nil, err
	}
	var s []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			s = append(s, l)
		}
	}
	sort.Strings(s)
	return s, nil
}

type psRow struct {
	Service  string
	State    string
	Health   string
	ExitCode int
}

// parsePS reads `compose ps --format json`: a JSON array (older compose)
// or one object per line.
func parsePS(out []byte) ([]psRow, error) {
	out = bytes.TrimSpace(out)
	var rows []psRow
	if len(out) == 0 {
		return nil, nil
	}
	if out[0] == '[' {
		err := json.Unmarshal(out, &rows)
		return rows, err
	}
	for _, l := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var r psRow
		if err := json.Unmarshal(l, &r); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func (d *DockerCompose) PS(ctx context.Context) ([]ServiceStatus, error) {
	out, err := d.CLI.Output(ctx, d.CLI.Compose("ps", "-a", "--format", "json")...)
	if err != nil {
		return nil, err
	}
	rows, err := parsePS(out)
	if err != nil {
		return nil, err
	}
	var st []ServiceStatus
	for _, r := range rows {
		st = append(st, ServiceStatus{Service: r.Service, State: r.State, Health: r.Health, ExitCode: r.ExitCode})
	}
	return st, nil
}

func (d *DockerCompose) Remove(ctx context.Context, services []string) error {
	if len(services) == 0 {
		return nil
	}
	return d.CLI.Run(ctx, nil, nil, d.CLI.Compose(append([]string{"rm", "-f", "-s"}, services...)...)...)
}

func (d *DockerCompose) Logs(ctx context.Context, service string, tail int) string {
	var lines []string
	_ = d.CLI.Run(ctx, nil, func(l string) { lines = append(lines, l) },
		d.CLI.Compose("logs", "--no-color", "--no-log-prefix", "--tail", strconv.Itoa(tail), service)...)
	return strings.Join(lines, "\n")
}

func (d *DockerCompose) Exec(ctx context.Context, service string, stdin io.Reader, args ...string) (string, error) {
	var lines []string
	err := d.CLI.Run(ctx, stdin, func(l string) { lines = append(lines, l) },
		d.CLI.Compose(append([]string{"exec", "-T", service}, args...)...)...)
	return strings.Join(lines, "\n"), err
}

// Evaluate fills Ready/Failed and adds "missing" rows for services
// without a container.
func Evaluate(want []string, have []ServiceStatus) []ServiceStatus {
	by := map[string]ServiceStatus{}
	for _, s := range have {
		by[s.Service] = s
	}
	var out []ServiceStatus
	for _, name := range want {
		s, ok := by[name]
		if !ok {
			s = ServiceStatus{Service: name, State: "missing"}
		}
		switch {
		case s.State == "exited" && s.ExitCode == 0 && OneShot[name]:
			s.Ready = true
		case s.State == "exited" || s.State == "dead":
			s.Failed = true
		case s.State == "running" && (s.Health == "" || s.Health == "healthy"):
			s.Ready = true
		case s.Health == "unhealthy":
			s.Failed = true
		}
		out = append(out, s)
	}
	return out
}
