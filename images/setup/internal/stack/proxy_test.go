package stack

import (
	"strings"
	"testing"
)

func proxyAnswers() Answers {
	a := answers()
	a.Proxy = ProxyAnswers{Enabled: true, Software: "traefik", HTTPAddr: "172.18.0.1:8081", HTTPSAddr: "172.18.0.1:8444", TrustedIPs: "10.203.53.1"}
	return a
}

func TestParsePublish(t *testing.T) {
	for in, want := range map[string]Publish{
		"8444":           {"", 8444},
		"127.0.0.1:8444": {"127.0.0.1", 8444},
		"0.0.0.0:8081":   {"", 8081},
		"[::1]:8444":     {"::1", 8444},
		"[::]:8444":      {"", 8444},
		" 172.18.0.1:1 ": {"172.18.0.1", 1},
	} {
		got, err := ParsePublish(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "x", "host:80", "1.2.3.4:0", "1.2.3.4:70000", "1.2.3.4:", "::1:80", "8444:443"} {
		if _, err := ParsePublish(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
	if p, _ := ParsePublish("8444"); p.Target() != "127.0.0.1:8444" || !p.Public() {
		t.Error(p.Target())
	}
	if p, _ := ParsePublish("[::1]:8444"); p.Target() != "[::1]:8444" || p.Public() {
		t.Error(p.Target())
	}
}

func TestStackGateway(t *testing.T) {
	for in, want := range map[string]string{"": "10.203.53.1", "10.203.53.0/24": "10.203.53.1", "10.9.0.0/16": "10.9.0.1",
		"10.9.3.7/16": "10.9.0.1", "garbage": "10.203.53.1"} {
		if got := StackGateway(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestProxyValidate(t *testing.T) {
	ok := proxyAnswers()
	if errs := ok.Validate("proxy", Secrets{}, false); len(errs) != 0 {
		t.Fatal(errs)
	}
	off := answers()
	off.Proxy = ProxyAnswers{HTTPAddr: "nonsense"}
	if errs := off.Validate("proxy", Secrets{}, false); len(errs) != 0 {
		t.Fatal("off is never invalid", errs)
	}
	cases := map[string]func(p *ProxyAnswers){
		"proxy.software":    func(p *ProxyAnswers) { p.Software = "iis" },
		"proxy.http_addr":   func(p *ProxyAnswers) { p.HTTPAddr = "80" },
		"proxy.https_addr":  func(p *ProxyAnswers) { p.HTTPSAddr = "0.0.0.0:443" },
		"proxy.trusted_ips": func(p *ProxyAnswers) { p.TrustedIPs = "" },
	}
	cases["proxy.http_addr "] = func(p *ProxyAnswers) { p.HTTPAddr = "localhost:8081" }
	cases["proxy.https_addr "] = func(p *ProxyAnswers) { p.HTTPSAddr = "nope" }
	cases["proxy.https_addr  "] = func(p *ProxyAnswers) { p.HTTPSAddr = "8081" }
	cases["proxy.trusted_ips "] = func(p *ProxyAnswers) { p.TrustedIPs = "10.0.0.1, example.com" }
	cases["proxy.trusted_ips  "] = func(p *ProxyAnswers) { p.TrustedIPs = "0.0.0.0/0" }
	cases["proxy.trusted_ips   "] = func(p *ProxyAnswers) { p.TrustedIPs = "::" }
	for field, mod := range cases {
		a := proxyAnswers()
		mod(&a.Proxy)
		a.Normalize()
		errs := a.Validate("proxy", Secrets{}, false)
		if len(errs) == 0 || errs[0].Field != strings.TrimSpace(field) {
			t.Errorf("%s: %v", field, errs)
		}
	}
	// Same port on two addresses is fine; on one, not.
	a := proxyAnswers()
	a.Proxy.HTTPAddr, a.Proxy.HTTPSAddr = "127.0.0.1:8444", "172.18.0.1:8444"
	if errs := a.Validate("proxy", Secrets{}, false); len(errs) != 0 {
		t.Error(errs)
	}
	// Ranges and IPv6 are trusted fine.
	a = proxyAnswers()
	a.Proxy.TrustedIPs = "10.203.53.1 172.18.0.0/16;fd00::1"
	a.Normalize()
	if errs := a.Validate("", Secrets{}, false); len(errs) != 0 || a.Proxy.TrustedIPs != "10.203.53.1,172.18.0.0/16,fd00::1" {
		t.Error(errs, a.Proxy.TrustedIPs)
	}
}

func TestProxyNormalizeDefaults(t *testing.T) {
	a := answers()
	a.Proxy = ProxyAnswers{Enabled: true, TrustedIPs: " 10.203.53.1 ,10.203.53.1"}
	a.Normalize()
	p := a.Proxy
	if p.Software != "traefik" || p.HTTPAddr != DefaultProxyHTTP || p.HTTPSAddr != DefaultProxyHTTPS || p.TrustedIPs != "10.203.53.1" {
		t.Fatalf("%+v", p)
	}
	if a.HTTPSTarget() != "127.0.0.1:8444" || a.HTTPTarget() != "127.0.0.1:8081" {
		t.Error(a.HTTPSTarget(), a.HTTPTarget())
	}
	a.Proxy.HTTPSAddr = "bad"
	if a.HTTPSTarget() != "127.0.0.1:443" {
		t.Error(a.HTTPSTarget())
	}
}

func TestProxyEnvRoundTrip(t *testing.T) {
	// Off on a new install: nothing written (compose's defaults).
	p, err := BuildPlan(answers(), Secrets{}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.Env {
		if strings.HasPrefix(v.Key, "TRAEFIK_") {
			t.Errorf("off: %s written", v.Key)
		}
	}
	// On.
	p, err = BuildPlan(proxyAnswers(), Secrets{}, nil, NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	if p.EnvValue(EnvHTTPPublish) != "172.18.0.1:8081" || p.EnvValue(EnvHTTPSPublish) != "172.18.0.1:8444" || p.EnvValue(EnvProxyTrusted) != "10.203.53.1" {
		t.Fatal(MaskedEnv(p))
	}
	env := map[string]string{}
	for _, v := range p.Env {
		env[v.Key] = v.Value
	}
	text, _ := MergeEnv("", p.Env, "# test")
	back := Existing{Found: true, Env: ParseEnv(text)}.Answers()
	if !back.Proxy.Enabled || back.Proxy.HTTPAddr != "172.18.0.1:8081" || back.Proxy.HTTPSAddr != "172.18.0.1:8444" || back.Proxy.TrustedIPs != "10.203.53.1" {
		t.Fatalf("read back: %+v", back.Proxy)
	}
	// Off again over an install that had it: back to compose's defaults.
	p, _ = BuildPlan(answers(), Secrets{}, env, NewGenerator(), "999")
	if p.EnvValue(EnvHTTPPublish) != "80" || p.EnvValue(EnvHTTPSPublish) != "443" || p.EnvValue(EnvProxyTrusted) != "" {
		t.Fatal(MaskedEnv(p))
	}
	text, _ = MergeEnv(text, p.Env, "# test")
	if back := (Existing{Found: true, Env: ParseEnv(text)}).Answers(); back.Proxy.Enabled {
		t.Fatalf("still on: %+v", back.Proxy)
	}
	// Hand-written .env: only a trusted list, or only a port, is on too.
	for _, e := range []map[string]string{{EnvProxyTrusted: "10.0.0.1"}, {EnvHTTPSPublish: "127.0.0.1:8444"}} {
		if !proxyFromEnv(e).Enabled {
			t.Error(e)
		}
	}
	for _, e := range []map[string]string{{}, {EnvHTTPPublish: "80", EnvHTTPSPublish: "0.0.0.0:443"}} {
		if proxyFromEnv(e).Enabled {
			t.Error(e)
		}
	}
}

func TestProxySnippets(t *testing.T) {
	a := proxyAnswers()
	a.Wellknown = true
	a.Normalize()
	names := []string{}
	for _, h := range a.NeededHosts() {
		names = append(names, h.Name)
	}
	check := func(sw string, want ...string) []Snippet {
		t.Helper()
		a.Proxy.Software = sw
		sn := ProxySnippets(a)
		if len(sn) == 0 {
			t.Fatalf("%s: no snippets", sw)
		}
		var all strings.Builder
		for _, s := range sn {
			if s.ID == "" || s.Title == "" || s.Where == "" || s.For != "proxy" {
				t.Errorf("%s: incomplete %+v", sw, s)
			}
			all.WriteString(s.Text)
		}
		text := all.String()
		for _, w := range append(want, "172.18.0.1:8444", "172.18.0.1:8081") {
			if !strings.Contains(text, w) {
				t.Errorf("%s: no %q in\n%s", sw, w, text)
			}
		}
		for _, n := range names {
			if !strings.Contains(text, n) {
				t.Errorf("%s: name %s missing", sw, n)
			}
		}
		// Only the install's names, never a wildcard that could take the
		// proxy's other sites under the same domain.
		if strings.Contains(text, "*.boson.test") || strings.Contains(text, "HostSNIRegexp") {
			t.Errorf("%s: wildcard", sw)
		}
		return sn
	}
	tr := check("traefik", "passthrough: true", "proxyProtocol:", "version: 2", "HostSNI(`auth.boson.test`) || HostSNI(`chat.boson.test`)",
		"Host(`boson.test`)", "priority: 1000", "url: \"http://172.18.0.1:8081\"", "boson-boson-test:")
	if !strings.Contains(tr[0].Warning, "priority: 1") || !strings.Contains(tr[0].Warning, "allowACMEBypass") {
		t.Error(tr[0].Warning)
	}
	check("nginx", "ssl_preread on;", "proxy_protocol on;", "auth.boson.test", "boson_boson_test;", "upstream boson_boson_test { server 172.18.0.1:8444; }",
		"proxy_pass http://172.18.0.1:8081;", "listen 127.0.0.1:4443 ssl proxy_protocol;")
	check("haproxy", "send-proxy-v2", "mode tcp", "req_ssl_sni -i irc.boson.test auth.boson.test", "option forwardfor", "backend boson_boson_test_https")
	cad := check("caddy", "proxy_protocol v2", "upstream tcp/172.18.0.1:8444", "tls sni irc.boson.test", "http://auth.boson.test, ", "reverse_proxy 172.18.0.1:8081")
	if !strings.Contains(cad[0].Warning, "caddy-l4") {
		t.Error(cad[0].Warning)
	}
	check("other", "PROXY protocol header", "http://172.18.0.1:8081")
	check("unknown-thing", "PROXY protocol header")
}
