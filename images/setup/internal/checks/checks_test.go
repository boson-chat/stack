package checks

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

type mapDNS map[string][]string

func (m mapDNS) LookupIP(_ context.Context, network, host string) ([]net.IP, error) {
	var out []net.IP
	for _, s := range m[host] {
		ip := net.ParseIP(s)
		if (ip.To4() != nil) == (network == "ip4") {
			out = append(out, ip)
		}
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}

func TestDNS(t *testing.T) {
	d := &DNS{
		Resolver: mapDNS{
			"irc.x.test":   {"192.0.2.1"},
			"auth.x.test":  {"203.0.113.9"},
			"chat.x.test":  {"198.51.100.7"}, // NAT: not on an interface, but reaches the wizard
			"admin.x.test": {"192.0.2.1", "2001:db8::99"},
		},
		LocalIPs: func() []net.IP { return []net.IP{net.ParseIP("192.0.2.1")} },
		Probe:    func(_ context.Context, ip net.IP) bool { return ip.String() == "198.51.100.7" },
	}
	hosts := []stack.Host{{Name: "irc.x.test"}, {Name: "auth.x.test"}, {Name: "chat.x.test"}, {Name: "admin.x.test"}, {Name: "api.x.test"}}
	s := d.Check(context.Background(), hosts)
	want := []string{"ok", "elsewhere", "ok", "elsewhere", "missing"}
	for i, h := range s.Hosts {
		if h.Status != want[i] {
			t.Errorf("%s: %s (%s), want %s", h.Name, h.Status, h.Message, want[i])
		}
	}
	if s.OK {
		t.Fatal("summary ok")
	}
	if !strings.Contains(s.Hosts[1].Message, "203.0.113.9") || !strings.Contains(s.Hosts[3].Message, "AAAA") {
		t.Fatal(s.Hosts[1].Message, s.Hosts[3].Message)
	}
}

func TestPorts(t *testing.T) {
	// A port that's taken by someone else.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	// Two free ones: one TCP reached, one UDP whose datagram is "dropped".
	free := func(network string) int {
		if network == "udp" {
			c, _ := net.ListenPacket("udp", ":0")
			p := c.LocalAddr().(*net.UDPAddr).Port
			c.Close()
			return p
		}
		l, _ := net.Listen("tcp", ":0")
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	tcpFree, udpDropped, udpOK := free("tcp"), free("udp"), free("udp")
	p := &Ports{
		HostIP: func() net.IP { return net.ParseIP("127.0.0.1") },
		Nonce:  func() string { return "nonce123" },
		RunProbe: func(ctx context.Context, host, nonce string, tcp, udp []int) (ProbeResult, error) {
			var send []int
			for _, u := range udp {
				if u == udpOK {
					send = append(send, u)
				}
			}
			return RunProbeClient(host, nonce, tcp, send), nil
		},
	}
	specs := []PortSpec{{Proto: "tcp", Port: busyPort, Feature: "irc"}, {Proto: "tcp", Port: tcpFree, Feature: "web"}, {Proto: "udp", Port: udpDropped, Feature: "voice"}, {Proto: "udp", Port: udpOK, Feature: "video"}}
	// busy listens on 127.0.0.1 only; make ":port" collide on every OS by
	// listening on all addresses instead.
	busy.Close()
	busy2, err := net.Listen("tcp", ":"+fmtInt(busyPort))
	if err != nil {
		t.Skip("can't hold a port:", err)
	}
	defer busy2.Close()
	r := p.Check(context.Background(), specs)
	got := map[int]PortResult{}
	for _, x := range r.Ports {
		got[x.Port] = x
	}
	if got[busyPort].Local != "in_use" || got[busyPort].Level != "error" {
		t.Errorf("busy: %+v", got[busyPort])
	}
	if got[tcpFree].Reach != "ok" || got[tcpFree].Level != "ok" {
		t.Errorf("tcp: %+v", got[tcpFree])
	}
	if got[udpDropped].Reach != "blocked" || got[udpDropped].Level != "warn" || !strings.Contains(got[udpDropped].Message, "turn voice off") {
		t.Errorf("udp dropped: %+v", got[udpDropped])
	}
	if got[udpOK].Reach != "ok" {
		t.Errorf("udp ok: %+v", got[udpOK])
	}
	if !strings.Contains(r.Outside, "security groups") {
		t.Error(r.Outside)
	}
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestParseProbeOutput(t *testing.T) {
	r, err := ParseProbeOutput([]byte("noise\n{\"tcp\":{\"443\":true}}\n"))
	if err != nil || !r.TCP["443"] {
		t.Fatal(r, err)
	}
	if _, err := ParseProbeOutput([]byte("nothing")); err == nil {
		t.Fatal("no error")
	}
}

// A minimal SMTP server: records the message.
func fakeSMTP(t *testing.T) (string, int, chan string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		defer l.Close()
		c.SetDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(c)
		w := func(s string) { c.Write([]byte(s + "\r\n")) }
		w("220 fake")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					got <- data.String()
					w("250 ok")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.Fields(line)[0]); cmd {
			case "EHLO":
				w("250-fake")
				w("250 8BITMIME")
			case "DATA":
				inData = true
				w("354 go")
			case "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return "127.0.0.1", l.Addr().(*net.TCPAddr).Port, got
}

func TestSendTestMail(t *testing.T) {
	host, port, got := fakeSMTP(t)
	err := SendTestMail(context.Background(), stack.SMTPAnswers{Host: host, Port: port, From: "Net <no@x.test>", TLS: "none"}, "", "a@x.test", "Net")
	if err != nil {
		t.Fatal(err)
	}
	if m := <-got; !strings.Contains(m, "Subject: Net: test email from Boson setup") {
		t.Fatal(m)
	}
	// No password over plaintext.
	host, port, _ = fakeSMTP(t)
	err = SendTestMail(context.Background(), stack.SMTPAnswers{Host: host, Port: port, From: "no@x.test", TLS: "none", User: "u"}, "pw", "a@x.test", "Net")
	if err == nil || !strings.Contains(err.Error(), "without TLS") {
		t.Fatal("sent a password without TLS:", err)
	}
	// STARTTLS required but not offered.
	host, port, _ = fakeSMTP(t)
	err = SendTestMail(context.Background(), stack.SMTPAnswers{Host: host, Port: port, From: "no@x.test", TLS: "starttls"}, "", "a@x.test", "Net")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatal(err)
	}
}

func TestPortsNodeExporter(t *testing.T) {
	check := func(addr string, reached bool, perr error) (PortResult, int) {
		calls := 0
		p := &Ports{
			HostIP: func() net.IP { return nil },
			Nonce:  func() string { return "n" },
			Node:   func() (string, string) { return addr, "10.203.53.0/24" },
			RunNodeProbe: func(ctx context.Context, a string, port int) (bool, error) {
				calls++
				if a != addr || port != 9100 {
					t.Errorf("probe %s:%d", a, port)
				}
				return reached, perr
			},
		}
		r := p.Check(context.Background(), nil)
		for _, x := range r.Ports {
			if x.Internal {
				return x, calls
			}
		}
		t.Fatalf("no node-exporter row: %+v", r.Ports)
		return PortResult{}, calls
	}
	rule := "sudo ufw allow from 10.203.53.0/24 to 127.0.0.1 port 9100 proto tcp comment 'boson: metrics -> node-exporter'"

	// 127.0.0.1:9100 stands in for the stack bridge's gateway (free, or
	// node-exporter's already: probed either way).
	r, calls := check("127.0.0.1", false, nil)
	if calls != 1 || r.Reach != "blocked" || r.Level != "warn" || r.Command != rule || r.Feature != "metrics" ||
		!strings.Contains(r.Message, "job node") || r.Name() != "127.0.0.1:9100/tcp" {
		t.Errorf("dropped: %d %+v", calls, r)
	}
	r, _ = check("127.0.0.1", true, nil)
	if r.Reach != "ok" || r.Level != "ok" || r.Command != "" {
		t.Errorf("reached: %+v", r)
	}
	r, _ = check("127.0.0.1", false, errors.New("no docker"))
	if r.Reach != "unknown" || r.Level != "ok" || !strings.Contains(r.Message, rule) {
		t.Errorf("probe failed: %+v", r)
	}
	// An address this host doesn't have (the stack network isn't up): not probed.
	r, calls = check("192.0.2.77", false, nil)
	if calls != 0 || r.Local != "no_address" || r.Reach != "unknown" {
		t.Errorf("no address: %d %+v", calls, r)
	}
	// Not configured: no row.
	if rep := (&Ports{HostIP: func() net.IP { return nil }, Nonce: func() string { return "n" }}).Check(context.Background(), nil); len(rep.Ports) != 0 {
		t.Errorf("unconfigured: %+v", rep.Ports)
	}
}
