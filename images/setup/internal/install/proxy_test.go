package install

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
	"github.com/boson-chat/stack/images/setup/internal/tlscert"
)

// tlsServer answers TLS on 127.0.0.1 with a certificate for names.
func tlsServer(t *testing.T, names []string) string {
	t.Helper()
	cert, _, err := tlscert.New(nil, names)
	if err != nil {
		t.Fatal(err)
	}
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	return l.Addr().String()
}

// forward relays TCP from a new 127.0.0.1 port to to (a passthrough proxy).
func forward(t *testing.T, to string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.Dial("tcp", to)
				if err != nil {
					return
				}
				defer u.Close()
				go func() {
					buf := make([]byte, 32<<10)
					for {
						n, err := c.Read(buf)
						if n > 0 {
							u.Write(buf[:n])
						}
						if err != nil {
							return
						}
					}
				}()
				buf := make([]byte, 32<<10)
				for {
					n, err := u.Read(buf)
					if n > 0 {
						c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().String()
}

func proxyPlan(t *testing.T, https string) stack.Plan {
	a := stack.Defaults()
	a.Domain = "boson.test"
	a.Admin = stack.AdminAnswers{Username: "alice", Email: "alice@boson.test"}
	a.TLS.Email = "alice@boson.test"
	a.Proxy = stack.ProxyAnswers{Enabled: true, Software: "nginx", HTTPAddr: "127.0.0.1:8081", HTTPSAddr: https, TrustedIPs: "10.203.53.1"}
	p, err := stack.BuildPlan(a, stack.Secrets{}, nil, stack.NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProxyCertsAndPath(t *testing.T) {
	var names []string
	for _, h := range proxyPlan(t, "127.0.0.1:1").Hosts {
		names = append(names, h.Name)
	}
	boson := tlsServer(t, names)
	p := proxyPlan(t, boson)
	r := &Runner{Edge: "127.0.0.1", Timeouts: Timeouts{Certs: time.Second, Verify: time.Second}}
	if got := r.traefikAddr(p); got != boson {
		t.Fatal(got)
	}
	if got := r.traefikAddr(plan(t, nil)); got != "127.0.0.1:443" {
		t.Fatal(got)
	}
	var lines []string
	log := func(s string) { lines = append(lines, s) }
	ctx := context.Background()
	// Certificates: read from Boson's Traefik directly, not through 443.
	if err := stepCerts(ctx, r, &Job{Plan: p}, log); err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(names) {
		t.Fatal(lines)
	}

	// The proxy passes TLS through: ok.
	r.edgeHTTPS = forward(t, boson)
	lines = nil
	if err := r.checkProxyPath(ctx, p, time.Now().Add(time.Second), log); err != nil || !strings.Contains(lines[0], "passes auth.boson.test through") {
		t.Fatal(err, lines)
	}
	// The proxy terminates TLS itself (its own certificate): a clear error.
	r.edgeHTTPS = tlsServer(t, []string{"auth.boson.test"})
	err := r.checkProxyPath(ctx, p, time.Now().Add(100*time.Millisecond), log)
	var se *StepError
	if !errors.As(err, &se) || !strings.Contains(se.Msg, "another certificate") || !strings.Contains(se.Hint, "Firewall and ports step") || !strings.Contains(se.Hint, boson) {
		t.Fatal(err)
	}
	// Nothing on 443.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	r.edgeHTTPS = l.Addr().String()
	l.Close()
	if err := r.checkProxyPath(ctx, p, time.Now(), log); err == nil || !strings.Contains(err.Error(), "nothing passes TLS through") {
		t.Fatal(err)
	}
	// Boson's Traefik itself down.
	p2 := proxyPlan(t, r.edgeHTTPS)
	if err := r.checkProxyPath(ctx, p2, time.Now(), log); err == nil || !strings.Contains(err.Error(), "Boson's Traefik on") {
		t.Fatal(err)
	}
	// No certificate in time: the hint is about the proxy's port 80.
	err = stepCerts(ctx, &Runner{Edge: "127.0.0.1", Compose: &FakeCompose{}, Timeouts: Timeouts{Certs: 10 * time.Millisecond}}, &Job{Plan: p2}, log)
	if !errors.As(err, &se) || !strings.Contains(se.Hint, "through your reverse proxy on port 80") || !strings.Contains(se.Hint, "127.0.0.1:8081") {
		t.Fatal(err)
	}
}
