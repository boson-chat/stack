package install

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// FakeCompose stands in for docker compose in tests and the dev server.
type FakeCompose struct {
	mu sync.Mutex
	// FailUp: Up fails this many times.
	FailUp int
	// Users in auth: username → "role email".
	Users   map[string]string
	Calls   []string
	Started bool
	Svc     []string
	Delay   time.Duration
}

func (f *FakeCompose) call(s string) {
	f.mu.Lock()
	f.Calls = append(f.Calls, s)
	f.mu.Unlock()
}

func (f *FakeCompose) Pull(ctx context.Context, onLine func(string)) error {
	f.call("pull")
	for _, s := range f.svc() {
		onLine(" " + s + " Pulled")
		time.Sleep(f.Delay / 4)
	}
	return nil
}

func (f *FakeCompose) Up(ctx context.Context, onLine func(string)) error {
	f.call("up")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailUp > 0 {
		f.FailUp--
		onLine(" Container boson-ergo-1  Starting")
		return fmt.Errorf("docker compose up: exit status 1\nError response from daemon: failed to set up container networking: driver failed programming external connectivity on endpoint boson-ergo-1: Bind for 0.0.0.0:6697 failed: port is already allocated")
	}
	f.Started = true
	onLine(" Container boson-traefik-1  Started")
	return nil
}

func (f *FakeCompose) svc() []string {
	if f.Svc != nil {
		return f.Svc
	}
	return []string{"auth", "certsync", "config-init", "ergo", "postgres", "traefik", "web"}
}

func (f *FakeCompose) Services(ctx context.Context) ([]string, error) { return f.svc(), nil }

func (f *FakeCompose) PS(ctx context.Context) ([]ServiceStatus, error) {
	var out []ServiceStatus
	for _, s := range f.svc() {
		if OneShot[s] {
			out = append(out, ServiceStatus{Service: s, State: "exited"})
		} else {
			out = append(out, ServiceStatus{Service: s, State: "running", Health: "healthy"})
		}
	}
	return out, nil
}

func (f *FakeCompose) Logs(ctx context.Context, service string, tail int) string {
	return service + ": (fake log)"
}

func (f *FakeCompose) Exec(ctx context.Context, service string, stdin io.Reader, args ...string) (string, error) {
	f.call("exec " + strings.Join(args, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Users == nil {
		f.Users = map[string]string{}
	}
	if len(args) >= 4 && args[1] == "user" && args[2] == "list" {
		out := "ID  USERNAME  EMAIL  VERIFIED  ROLE  DISABLED\n"
		if v, ok := f.Users[args[3]]; ok {
			p := strings.Fields(v)
			out += fmt.Sprintf("id1  %s  %s  true  %s  false\n", args[3], p[1], p[0])
		}
		return out, nil
	}
	if len(args) >= 3 && args[1] == "user" && args[2] == "create" {
		var name, email string
		for i := 0; i+1 < len(args); i++ {
			switch args[i] {
			case "--username":
				name = args[i+1]
			case "--email":
				email = args[i+1]
			}
		}
		pw, _ := io.ReadAll(stdin)
		if len(strings.TrimSpace(string(pw))) == 0 {
			return "", fmt.Errorf("no password")
		}
		f.Users[name] = "owner " + email
		return "created " + name + " (id1) role=owner", nil
	}
	if len(args) >= 5 && args[2] == "set-role" {
		p := strings.Fields(f.Users[args[3]])
		f.Users[args[3]] = args[4] + " " + p[1]
		return args[3] + " role=" + args[4], nil
	}
	return "", nil
}
