package fresh

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/dockercli"
	"github.com/boson-chat/stack/images/setup/internal/install"
)

// brokenPS: docker isn't answering.
type brokenPS struct{ *install.FakeCompose }

func (brokenPS) PS(context.Context) ([]install.ServiceStatus, error) {
	return nil, errors.New("Cannot connect to the Docker daemon\nmore")
}

func logsFlow(t *testing.T, c install.Compose) (*Flow, *http.ServeMux, *dockercli.Redactor) {
	t.Helper()
	dir, src := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "config"), 0o755)
	r, err := install.NewRunner(&install.Runner{Dir: dir, StackSrc: src, Compose: c, Store: install.NewStore(dir), Hub: install.NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	red := &dockercli.Redactor{}
	f, err := New(Deps{InstallDir: dir, Runner: r, Redactor: red})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.Register(mux, "/api/flows/fresh/")
	return f, mux, red
}

func get(t *testing.T, mux *http.ServeMux, path string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func TestServiceLogsEndpoint(t *testing.T) {
	var lines []string
	for range 300 {
		lines = append(lines, "POSTGRES_PASSWORD=pg-secret-value ok")
	}
	fc := &install.FakeCompose{LogLines: map[string][]string{"postgres": lines}}
	f, mux, red := logsFlow(t, fc)
	red.Set([]string{"pg-secret-value"})
	const p = "/api/flows/fresh/install/services/"

	code, m := get(t, mux, p+"postgres/logs")
	if code != 200 || m["tail"] != float64(100) || len(m["lines"].([]any)) != 100 {
		t.Fatalf("%d %v", code, m["tail"])
	}
	if l := m["lines"].([]any)[0].(string); l != "POSTGRES_PASSWORD=[redacted] ok" {
		t.Fatal(l)
	}
	code, m = get(t, mux, p+"postgres/logs?tail=5000")
	if code != 200 || m["tail"] != float64(install.MaxLogTail) || len(m["lines"].([]any)) != install.MaxLogTail {
		t.Fatalf("%d %v", code, m["tail"])
	}
	for _, q := range []string{"abc", "0", "-3"} {
		if code, m := get(t, mux, p+"postgres/logs?tail="+q); code != 400 || m["code"] != "bad_request" {
			t.Errorf("tail=%s: %d", q, code)
		}
	}
	for _, s := range []string{"nope", "..%2F..%2Fetc", "Postgres", "boson-postgres-1", "%2e%2e"} {
		if code, m := get(t, mux, p+s+"/logs"); code != 404 || m["code"] != "unknown_service" {
			t.Errorf("%s: %d %v", s, code, m)
		}
	}

	// At most four at once (each runs docker).
	for range cap(f.logSlots) {
		f.logSlots <- struct{}{}
	}
	if code, _ := get(t, mux, p+"postgres/logs"); code != 429 {
		t.Fatal(code)
	}
	for range cap(f.logSlots) {
		<-f.logSlots
	}
}

func TestServiceLogsEndpointHealthchecks(t *testing.T) {
	fc := &install.FakeCompose{Slow: map[string]time.Duration{"ergo": time.Hour}, HealthOutput: map[string]string{"ergo": "refused"}}
	_, mux, _ := logsFlow(t, fc)
	code, m := get(t, mux, "/api/flows/fresh/install/services/ergo/logs?tail=10")
	if code != 200 || m["health"] != "starting" || len(m["checks"].([]any)) != 3 || !strings.Contains(m["hint"].(string), "TLS certificate") {
		t.Fatalf("%d %v", code, m)
	}
}

func TestServiceLogsEndpointDockerDown(t *testing.T) {
	_, mux, _ := logsFlow(t, brokenPS{&install.FakeCompose{}})
	code, m := get(t, mux, "/api/flows/fresh/install/services/web/logs")
	if code != 502 || m["code"] != "docker" || strings.Contains(m["error"].(string), "more") {
		t.Fatalf("%d %v", code, m)
	}
}
