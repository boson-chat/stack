package fresh

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/install"
	"github.com/boson-chat/stack/images/setup/internal/stack"
)

func newTestFlow(t *testing.T, env string, docker checks.DockerOutput) (*Flow, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := install.NewRunner(&install.Runner{Dir: dir, Compose: &install.FakeCompose{}, Store: install.NewStore(dir), Hub: install.NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(Deps{InstallDir: dir, Runner: r, Docker: docker,
		Ports: &checks.Ports{HostIP: func() net.IP { return nil }, Nonce: func() string { return "n" }}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.Register(mux, "/api/flows/fresh/")
	return f, mux
}

func call(t *testing.T, mux *http.ServeMux, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestProxyFlow(t *testing.T) {
	docker := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("t1\tedge\ttraefik:v3\t0.0.0.0:443->443/tcp\tforgejo\n"), nil
		}
		return []byte(`"bridge" {"proxy":{"Gateway":"172.18.0.1"}}`), nil
	}
	f, mux := newTestFlow(t, "DOMAIN=boson.test\nSTACK_SUBNET=10.9.0.0/24\nCOMPOSE_PROFILES=irc,auth\n", docker)

	code, det := call(t, mux, "GET", "/api/flows/fresh/proxy/detect", nil)
	sug, _ := det["suggest"].(map[string]any)
	if code != 200 || det["gateway"] != "10.9.0.1" || det["suggest_ip"] != "172.18.0.1" || sug["https_addr"] != "172.18.0.1:8444" || sug["trusted_ips"] != "10.9.0.1" || sug["software"] != "traefik" {
		t.Fatalf("%d %v", code, det)
	}

	// Off: no snippets.
	if _, r := call(t, mux, "GET", "/api/flows/fresh/proxy/snippets", nil); r["enabled"] != false {
		t.Fatal(r)
	}

	a := f.Answers()
	a.Proxy = stack.ProxyAnswers{Enabled: true, Software: "haproxy", HTTPAddr: "80"}
	if code, r := call(t, mux, "PUT", "/api/flows/fresh/answers", map[string]any{"answers": a, "section": "proxy"}); code != 422 || !strings.Contains(toJSON(r), "proxy.http_addr") {
		t.Fatal(code, r)
	}
	a.Proxy.HTTPAddr = "172.18.0.1:8081"
	a.Proxy.HTTPSAddr = "172.18.0.1:8444"
	if code, r := call(t, mux, "PUT", "/api/flows/fresh/answers", map[string]any{"answers": a, "section": "proxy"}); code != 200 {
		t.Fatal(code, r)
	}
	// The trusted list defaults to the stack network's gateway.
	if got := f.Answers().Proxy.TrustedIPs; got != "10.9.0.1" {
		t.Fatal(got)
	}
	code, sn := call(t, mux, "GET", "/api/flows/fresh/proxy/snippets", nil)
	if code != 200 || sn["software"] != "haproxy" || sn["https"] != "172.18.0.1:8444" || !strings.Contains(toJSON(sn), "send-proxy-v2") {
		t.Fatal(code, sn)
	}
	// The ports checked: Boson's two private ports, 80/443 informational.
	specs := portSpecs(f.Answers())
	var names []string
	for _, s := range specs {
		names = append(names, s.Name())
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "172.18.0.1:8081/tcp") || !strings.Contains(got, "172.18.0.1:8444/tcp") {
		t.Fatal(got)
	}
	bad := f.Answers()
	bad.Proxy.HTTPAddr = "nope"
	if len(portSpecs(bad)) != len(checks.PortsFor(bad.Voice, bad.Video)) {
		t.Error("unparsable address: plain ports")
	}
	off := f.Answers()
	off.Proxy.Enabled = false
	if len(portSpecs(off)) != len(checks.PortsFor(off.Voice, off.Video)) {
		t.Error("off: plain ports")
	}

	// No domain: snippets can't be made.
	f.mu.Lock()
	f.answers.Domain = ""
	f.mu.Unlock()
	if code, _ := call(t, mux, "GET", "/api/flows/fresh/proxy/snippets", nil); code != 422 {
		t.Fatal(code)
	}
}

func TestProxyRerunKeepsSoftware(t *testing.T) {
	// A re-run reads the proxy back from .env, and which one it is from
	// the wizard's state.
	env := "DOMAIN=boson.test\nCOMPOSE_PROFILES=irc,auth\nTRAEFIK_HTTP_PUBLISH=127.0.0.1:8081\nTRAEFIK_HTTPS_PUBLISH=127.0.0.1:8444\nTRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS=10.203.53.1\n"
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600)
	st := install.NewStore(dir)
	a := stack.Defaults()
	a.Domain = "boson.test"
	a.Proxy.Software = "nginx"
	r, _ := install.NewRunner(&install.Runner{Dir: dir, Compose: &install.FakeCompose{}, Store: st, Hub: install.NewHub()})
	r.SaveAnswers(ID, a)
	f, err := New(Deps{InstallDir: dir, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	p := f.Answers().Proxy
	if !p.Enabled || p.Software != "nginx" || p.HTTPSAddr != "127.0.0.1:8444" || p.TrustedIPs != "10.203.53.1" {
		t.Fatalf("%+v", p)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
