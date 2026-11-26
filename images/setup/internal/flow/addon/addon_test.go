package addon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boson-chat/stack/images/setup/internal/dockercli"
	"github.com/boson-chat/stack/images/setup/internal/flow/fresh"
	"github.com/boson-chat/stack/images/setup/internal/install"
	"github.com/boson-chat/stack/images/setup/internal/stack"
	"github.com/boson-chat/stack/pkg/probe"
)

// fakeServer answers like ngIRCd with Anope: WEBIRC and the limit come
// right once "applied".
type fakeServer struct {
	applied bool
	calls   []probe.Options
}

func (f *fakeServer) run(_ context.Context, o probe.Options) (*probe.Report, error) {
	f.calls = append(f.calls, o)
	r := &probe.Report{
		Target:   probe.Target{Addr: o.Addr, TLS: o.TLS, CertSHA256: strings.Repeat("ab", 32), SPKISHA256: strings.Repeat("cd", 32)},
		Server:   probe.ServerInfo{Name: "irc.ngircd.test", Software: "ngircd", Network: "NgircdNet", ClientHost: "172.18.0.1"},
		Caps:     map[string]string{},
		ISupport: map[string]string{},
		Services: probe.ServicesInfo{NickServ: true, Package: "anope", Server: "services.ngircd.test"},
		WebIRC:   probe.WebIRCResult{Tested: true, Accepted: f.applied, Detail: "464 Invalid password"},
		Limits:   probe.LimitsInfo{Tested: o.ConnLimitTest, MaxPerIP: 5, Refusal: "ERROR :too many"},
	}
	if f.applied {
		r.Limits.MaxPerIP, r.Limits.Refusal = 0, ""
	}
	if o.RPC.URL != "" {
		ok := o.RPC.Token == "good"
		r.Services.RPC = &probe.RPCResult{URL: o.RPC.URL, Protocol: "jsonrpc", Flavor: "anope", Reachable: true, Authorized: ok}
		if !ok {
			r.Services.RPC.Error = "access denied"
		}
	}
	r.Table = probe.Evaluate(r)
	return r, nil
}

func newFlow(t *testing.T) (*Flow, *fakeServer, http.Handler) {
	dir := t.TempDir()
	r, err := install.NewRunner(&install.Runner{Dir: dir, StackSrc: t.TempDir(), Compose: &install.FakeCompose{}, Store: install.NewStore(dir), Hub: install.NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(fresh.Deps{InstallDir: dir, Runner: r, Redactor: &dockercli.Redactor{}})
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeServer{}
	f.Probe = fs.run
	mux := http.NewServeMux()
	f.Register(mux, "/api/flows/addon/")
	return f, fs, mux
}

func call(t *testing.T, h http.Handler, method, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return out
}

func TestProbeFlow(t *testing.T) {
	f, fs, h := newFlow(t)
	// A wrong RPC token: a clear error.
	out := call(t, h, "POST", "/api/flows/addon/probe", `{"host":"IRC.example.org","port":6697,"tls":true,"rpc_url":"http://10.0.0.5:8080/jsonrpc","rpc_token":"bad"}`)
	res := out["result"].(map[string]any)
	if !strings.Contains(res["rpc_error"].(string), "refused the token") {
		t.Fatalf("rpc error: %v", res["rpc_error"])
	}
	o := fs.calls[0]
	if o.Addr != "irc.example.org:6697" || !o.CheckCert || o.ConnLimitTest != ConnLimitTest || o.WebIRC.Password != f.BridgeWebIRCPassword() || len(o.WebIRC.Password) != 48 {
		t.Fatalf("options %+v", o)
	}
	// The right one: NickServ via RPC is what the decision order says.
	out = call(t, h, "POST", "/api/flows/addon/probe", `{"host":"irc.example.org","port":6697,"tls":true,"rpc_url":"http://10.0.0.5:8080/jsonrpc","rpc_token":"good"}`)
	res = out["result"].(map[string]any)
	rec := res["recommended"].(map[string]any)
	if rec["strategy"] != "anope_rpc" && stack.AuthNickServ || res["all_ok"] != false {
		t.Fatalf("recommended %v all_ok %v", rec, res["all_ok"])
	}
	a := f.Answers()
	if a.Addon.Software != "ngircd" || a.Addon.SourceIP != "172.18.0.1" || a.NetworkName != "NgircdNet" || a.Addon.TLSVerify || a.Addon.CertSHA256 == "" {
		t.Fatalf("answers %+v", a.Addon)
	}
	// Snippets: the password and the address in them.
	sn := call(t, h, "GET", "/api/flows/addon/snippets", "")
	b, _ := json.Marshal(sn)
	if !strings.Contains(string(b), f.BridgeWebIRCPassword()) || !strings.Contains(string(b), "172.18.0.1") || strings.Contains(string(b), "services-rpc") {
		t.Fatalf("snippets %s", b)
	}
	// Re-check: red until applied, then green; the source address stays.
	f.SaveAddon(func(x *stack.Answers) { x.Addon.SourceIP = "198.51.100.7" })
	out = call(t, h, "POST", "/api/flows/addon/probe", `{"recheck":true}`)
	if out["result"].(map[string]any)["all_ok"] != false {
		t.Fatal("green before the snippets")
	}
	fs.applied = true
	out = call(t, h, "POST", "/api/flows/addon/probe", `{"recheck":true}`)
	if out["result"].(map[string]any)["all_ok"] != true {
		t.Fatalf("still red: %v", out["result"].(map[string]any)["checks"])
	}
	if f.Answers().Addon.SourceIP != "198.51.100.7" {
		t.Fatal("re-check replaced the source address")
	}
	if last := fs.calls[len(fs.calls)-1]; last.RPC.Token != "good" {
		t.Fatal("re-check lost the RPC token")
	}
}
