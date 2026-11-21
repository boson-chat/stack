package webircgateway

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func mustPolicy(t *testing.T, m map[string]string) *bosonPolicy {
	t.Helper()
	p, err := loadBosonPolicy(envOf(m))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPorts(t *testing.T) {
	p := mustPolicy(t, map[string]string{"WEBIRC_EXTRA_PORTS": "6680, 8000-8001"})
	for _, port := range []int{6660, 6667, 6669, 6697, 7000, 7070, 9999, 6680, 8000, 8001} {
		if d := p.decide("irc.example.net", port, true); d.refused != "" {
			t.Errorf("port %d refused: %s", port, d.refused)
		}
	}
	for _, port := range []int{22, 25, 80, 443, 5432, 6670, 6698, 8002, 65535} {
		d := p.decide("irc.example.net", port, true)
		if !strings.HasPrefix(d.refused, "Gateway refused (port):") {
			t.Errorf("port %d: %q", port, d.refused)
		}
	}
	for _, bad := range []string{"0", "70000", "9-1", "x", "1-5000"} {
		if _, err := loadBosonPolicy(envOf(map[string]string{"WEBIRC_EXTRA_PORTS": bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAliases(t *testing.T) {
	p := mustPolicy(t, map[string]string{
		"WEBIRC_ALIASES":   "irc.boson.chat:6697=ergo:6680, irc.boson.chat:6667=ergo:6680",
		"WEBIRC_PASSWORDS": "ergo=s3cret,irc.example.net=pw",
	})
	d := p.decide("IRC.Boson.Chat.", 6697, true)
	if d.refused != "" || d.host != "ergo" || d.port != 6680 || d.tls || !d.allowPrivate || d.password != "s3cret" {
		t.Fatalf("alias: %+v", d)
	}
	// The alias's target port needn't be an IRC port, but the asked one must.
	if d := p.decide("irc.boson.chat", 6680, false); d.refused == "" {
		t.Fatal("non-IRC port through an alias name was allowed")
	}
	// Not an alias: public only, its own password.
	d = p.decide("irc.example.net", 6697, true)
	if d.allowPrivate || !d.tls || d.password != "pw" || d.host != "irc.example.net" {
		t.Fatalf("plain: %+v", d)
	}
	// Asking for the alias target by name gets no privilege.
	if d := p.decide("ergo", 6667, false); d.allowPrivate {
		t.Fatal("target name got allowPrivate")
	}
	if p := mustPolicy(t, map[string]string{"WEBIRC_PASSWORDS": "ergo="}); len(p.passwords) != 0 {
		t.Fatal("empty password kept")
	}
	if _, err := loadBosonPolicy(envOf(map[string]string{"WEBIRC_PASSWORDS": "nohost"})); err == nil {
		t.Fatal("bad password entry accepted")
	}
	if _, err := loadBosonPolicy(envOf(map[string]string{"WEBIRC_ALIASES": "a=b"})); err == nil {
		t.Fatal("bad alias accepted")
	}
}

func TestAllowDeny(t *testing.T) {
	p := mustPolicy(t, map[string]string{"WEBIRC_UPSTREAM_DENY": "*.evil.net, bad.example"})
	for _, h := range []string{"irc.evil.net", "a.b.evil.net", "BAD.example"} {
		if d := p.decide(h, 6697, true); !strings.HasPrefix(d.refused, "Gateway refused (denied):") {
			t.Errorf("%s: %q", h, d.refused)
		}
	}
	if d := p.decide("irc.libera.chat", 6697, true); d.refused != "" {
		t.Error(d.refused)
	}
	p = mustPolicy(t, map[string]string{"WEBIRC_UPSTREAM_ALLOW": "*.libera.chat,irc.oftc.net", "WEBIRC_UPSTREAM_DENY": "bad.libera.chat"})
	for h, ok := range map[string]bool{"irc.libera.chat": true, "irc.oftc.net": true, "irc.other.net": false, "bad.libera.chat": false} {
		if d := p.decide(h, 6697, true); (d.refused == "") != ok {
			t.Errorf("%s: %q", h, d.refused)
		}
	}
	if d := p.decide("", 6697, true); !strings.HasPrefix(d.refused, "Gateway refused (host):") {
		t.Error(d.refused)
	}
}

func TestPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "169.254.169.254",
		"0.0.0.0", "224.0.0.1", "255.255.255.255", "::1", "::", "fe80::1", "fd00::1", "ff02::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1"} {
		if bosonPublicIP(net.ParseIP(s)) {
			t.Errorf("%s counted as public", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "130.239.18.120", "2001:4860:4860::8888", "2a01:4f8::1"} {
		if !bosonPublicIP(net.ParseIP(s)) {
			t.Errorf("%s counted as internal", s)
		}
	}
	ctl := bosonDialControl(false)
	if err := ctl("tcp", "127.0.0.1:6667", nil); !errors.Is(err, errBosonInternal) {
		t.Errorf("loopback dial: %v", err)
	}
	if err := ctl("tcp", "[::1]:6667", nil); !errors.Is(err, errBosonInternal) {
		t.Errorf("v6 loopback dial: %v", err)
	}
	if err := ctl("tcp", "1.1.1.1:6697", nil); err != nil {
		t.Error(err)
	}
	if err := bosonDialControl(true)("tcp", "10.0.0.1:6667", nil); err != nil {
		t.Error(err)
	}
	if !strings.Contains(bosonDialError(&net.OpError{Err: errBosonInternal}), "(address)") {
		t.Error("internal dial error text")
	}
}

// A real dial: the guard runs after DNS, so a name for loopback is caught.
func TestDialGuardAfterDNS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	d := net.Dialer{Timeout: time.Second, Control: bosonDialControl(false)}
	if _, err := d.Dial("tcp", "localhost:"+port); !errors.Is(err, errBosonInternal) {
		t.Fatalf("localhost dial: %v", err)
	}
	d.Control = bosonDialControl(true)
	c, err := d.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestLimits(t *testing.T) {
	p := mustPolicy(t, map[string]string{"WEBIRC_MAX_PER_IP": "2", "WEBIRC_RATE_PER_MIN": "3"})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	if p.admit(1, "1.2.3.4") != "" || p.admit(2, "1.2.3.4") != "" {
		t.Fatal("first two refused")
	}
	if msg := p.admit(3, "1.2.3.4"); !strings.HasPrefix(msg, "Gateway refused (limit):") {
		t.Fatalf("third: %q", msg)
	}
	if p.admit(4, "5.6.7.8") != "" {
		t.Fatal("other IP refused")
	}
	p.release(1)
	p.release(1) // twice is harmless
	if p.admit(5, "1.2.3.4") != "" {
		t.Fatal("after release refused")
	}
	p.release(2)
	p.release(5)
	// Three new connections a minute: the fourth within the minute is refused.
	if msg := p.admit(6, "1.2.3.4"); !strings.HasPrefix(msg, "Gateway refused (rate):") {
		t.Fatalf("rate: %q", msg)
	}
	now = now.Add(time.Minute)
	if p.admit(7, "1.2.3.4") != "" {
		t.Fatal("refused after a minute")
	}
	if !p.isAdmitted(7) || p.isAdmitted(6) {
		t.Fatal("admitted set")
	}
	unl := mustPolicy(t, map[string]string{"WEBIRC_MAX_PER_IP": "0", "WEBIRC_RATE_PER_MIN": "0"})
	for i := uint64(0); i < 100; i++ {
		if unl.admit(i, "9.9.9.9") != "" {
			t.Fatal("caps off still refused")
		}
	}
	if _, err := loadBosonPolicy(envOf(map[string]string{"WEBIRC_MAX_PER_IP": "-1"})); err == nil {
		t.Fatal("negative accepted")
	}
}

func TestDefaults(t *testing.T) {
	p := mustPolicy(t, nil)
	if p.maxPerIP != 5 || p.ratePerMin != 10 || p.burst != 20 || p.allowPrivate {
		t.Fatalf("%+v", p)
	}
	if _, err := loadBosonPolicy(envOf(map[string]string{"WEBIRC_ALLOW_PRIVATE": "maybe"})); err == nil {
		t.Fatal("bad bool accepted")
	}
	if !mustPolicy(t, map[string]string{"WEBIRC_ALLOW_PRIVATE": "true"}).allowPrivate {
		t.Fatal("allow private")
	}
}

func TestQueueLines(t *testing.T) {
	old := bosonPol
	defer func() { bosonPol = old }()
	bosonPol = mustPolicy(t, nil)
	c := &Client{Id: 42, Recv: make(chan string, 10)}
	if bosonQueueLines(c, "NICK a\r\n") {
		t.Fatal("unadmitted client's lines queued")
	}
	bosonPol.admit(42, "1.2.3.4")
	if !bosonQueueLines(c, "CAP LS 302\r\nNICK a\r\n\r\nUSER a 0 * :a") {
		t.Fatal("refused")
	}
	close(c.Recv)
	var got []string
	for l := range c.Recv {
		got = append(got, l)
	}
	if strings.Join(got, "|") != "CAP LS 302|NICK a|USER a 0 * :a" {
		t.Fatalf("%q", got)
	}
}

func TestForwardedFor(t *testing.T) {
	g := NewGateway("gateway")
	_, n, _ := net.ParseCIDR("10.0.0.0/8")
	g.Config.ReverseProxies = []net.IPNet{*n}
	req := func(remote string, xff ...string) *http.Request {
		r, _ := http.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	cases := []struct {
		r    *http.Request
		want string
	}{
		// A client's own header is ignored: the proxy appended the real one.
		{req("10.0.0.2:1", "6.6.6.6, 1.2.3.4"), "1.2.3.4"},
		{req("10.0.0.2:1", "6.6.6.6", "1.2.3.4"), "1.2.3.4"},
		// Chained proxies of ours are skipped.
		{req("10.0.0.2:1", "1.2.3.4, 10.0.0.9"), "1.2.3.4"},
		// Not from a trusted proxy: the header is ignored entirely.
		{req("8.8.8.8:1", "1.2.3.4"), "8.8.8.8"},
		{req("10.0.0.2:1"), "10.0.0.2"},
	}
	for _, c := range cases {
		if got := g.GetRemoteAddressFromRequest(c.r).String(); got != c.want {
			t.Errorf("%v %v: got %s, want %s", c.r.RemoteAddr, c.r.Header, got, c.want)
		}
	}
}

func TestInsecureParam(t *testing.T) {
	old := bosonPol
	defer func() { bosonPol = old }()
	bosonPol = mustPolicy(t, map[string]string{"WEBIRC_ALIASES": "irc.boson.chat:6697=ergo:6680"})
	g := NewGateway("gateway")
	g.Config.Gateway = true
	c := &Client{Gateway: g, IrcState: nil}
	for line, want := range map[string]bool{"HOST irc.example.net:+6697 insecure": true, "HOST irc.example.net:+6697": false} {
		c.DestTLSInsecure = !want
		if _, err := c.ProcessLineFromClient(line); err != nil {
			t.Fatal(err)
		}
		if c.DestTLSInsecure != want || c.DestHost != "irc.example.net" || c.DestPort != 6697 || !c.DestTLS {
			t.Fatalf("%s: %+v", line, c)
		}
		u := &ConfigUpstream{}
		bosonPol.connectionPre(&HookIrcConnectionPre{Client: c, UpstreamConfig: u})
		if u.TLSInsecure != want || u.Hostname != "irc.example.net" || u.AllowPrivate {
			t.Fatalf("%s: upstream %+v", line, u)
		}
	}
	// Alias: plaintext internal, never "insecure" (no TLS at all).
	c.DestHost, c.DestPort, c.DestTLS, c.DestTLSInsecure = "irc.boson.chat", 6697, true, true
	u := &ConfigUpstream{}
	bosonPol.connectionPre(&HookIrcConnectionPre{Client: c, UpstreamConfig: u})
	if u.Hostname != "ergo" || u.Port != 6680 || u.TLS || u.TLSInsecure || !u.AllowPrivate {
		t.Fatalf("alias upstream %+v", u)
	}
}
