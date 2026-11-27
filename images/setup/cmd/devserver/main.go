//go:build dev

// A local dev server for the wizard's pages: the real handlers with a fake
// docker compose, fake DNS (every name → 192.0.2.10 = "this server"; names
// under *.elsewhere.test → 203.0.113.9) and a fake ports probe.
//
//	go run -tags dev ./cmd/devserver -dir /tmp/boson-dev
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/dockercli"
	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/flow/addon"
	"github.com/boson-chat/stack/images/setup/internal/flow/fresh"
	"github.com/boson-chat/stack/images/setup/internal/gate"
	"github.com/boson-chat/stack/images/setup/internal/install"
	"github.com/boson-chat/stack/images/setup/internal/server"
	"github.com/boson-chat/stack/images/setup/internal/stack"
	"github.com/boson-chat/stack/images/setup/internal/tlscert"
	"github.com/boson-chat/stack/images/setup/web"
)

type fakeDNS struct{}

func (fakeDNS) LookupIP(_ context.Context, network, host string) ([]net.IP, error) {
	if network == "ip6" {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	if strings.HasSuffix(host, "elsewhere.test") {
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}
	if strings.HasPrefix(host, "missing.") || strings.Contains(host, ".missing.") {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return []net.IP{net.ParseIP("192.0.2.10")}, nil
}

func main() {
	dir := flag.String("dir", "/tmp/boson-dev", "install dir")
	addr := flag.String("addr", "127.0.0.1:8443", "listen")
	src := flag.String("stack", "../..", "stack files")
	admin := flag.String("admin", "", "an existing auth user (re-run)")
	slow := flag.Duration("slow", 20*time.Second, "ergo stays \"starting\" this long, with Traefik logging an ACME error (0: off)")
	flag.Parse()
	os.MkdirAll(*dir, 0o755)
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	g := gate.NewWithCode("TEST-CODE")
	fake := &install.FakeCompose{FailUp: 1, Delay: 2 * time.Second}
	if *slow > 0 {
		// What a stuck install looks like on the Install page: ergo waits
		// for its certificate, its healthcheck fails, Traefik can't get one.
		fake.Slow = map[string]time.Duration{"ergo": *slow}
		fake.HealthOutput = map[string]string{"ergo": "wget: can't connect to remote host (127.0.0.1): Connection refused"}
		fake.ACMEError = `unable to generate a certificate for the domains [irc.boson.test]: acme: error: 0 :: POST :: https://acme.example/directory :: "tls: failed to verify certificate: x509: certificate signed by unknown authority"`
		fake.LogLines = map[string][]string{
			"ergo": {},
			"traefik": {
				`2026-11-26T10:00:00Z INF Traefik version 3.6.2 built on 2025-11-18T00:00:00Z version=3.6.2`,
				`2026-11-26T10:00:00Z INF Starting provider aggregator *aggregator.ProviderAggregator`,
				`2026-11-26T10:00:00Z INF Starting provider *acme.ChallengeTLSALPN`,
				`2026-11-26T10:00:00Z INF Starting provider *docker.Provider`,
				`2026-11-26T10:00:01Z INF Testing certificate renew... acmeCA=https://acme.example/directory providerName=letsencrypt.acme`,
			},
		}
	}
	if *admin != "" {
		fake.FailUp = 0
		fake.Users = map[string]string{*admin: "owner " + *admin + "@boson.test"}
	}
	runner, err := install.NewRunner(&install.Runner{Dir: *dir, StackSrc: *src, Compose: fake, Store: install.NewStore(*dir), Hub: install.NewHub(),
		Edge: "127.0.0.1"})
	if err != nil {
		panic(err)
	}
	// No Traefik here: skip the certificate and verify steps' network checks.
	runner.Timeouts = install.Timeouts{Health: time.Minute, Certs: time.Second, Verify: time.Second, FailFor: time.Second, Poll: time.Second}
	install.DevSkipNetwork(runner)
	deps := fresh.Deps{
		InstallDir: *dir, Runner: runner, Redactor: &dockercli.Redactor{},
		DNS: &checks.DNS{Resolver: fakeDNS{}, LocalIPs: func() []net.IP { return []net.IP{net.ParseIP("192.0.2.10")} }},
		Ports: &checks.Ports{HostIP: func() net.IP { return net.ParseIP("127.0.0.1") }, Nonce: func() string { return "n" },
			RunProbe: func(ctx context.Context, host, nonce string, tcp, udp []int) (checks.ProbeResult, error) {
				r := checks.ProbeResult{TCP: map[string]bool{}}
				for _, p := range tcp {
					r.TCP[fmt.Sprint(p)] = true
				}
				return r, nil
			}},
		SendMail: func(ctx context.Context, s stack.SMTPAnswers, pw, to, n string) error {
			if strings.Contains(s.Host, "bad") {
				return fmt.Errorf("couldn't connect to %s:%d: connection refused", s.Host, s.Port)
			}
			return nil
		},
		Finish: func() { g.Close() },
	}
	fr, err := fresh.New(deps)
	if err != nil {
		panic(err)
	}
	// The add-on path probes for real (e.g. a test/ircds fixture on
	// 127.0.0.1:16697).
	ad, err := addon.New(deps)
	if err != nil {
		panic(err)
	}
	reg := &flow.Registry{}
	reg.Add(fr)
	reg.Add(ad)
	cert, _, _ := tlscert.New([]net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"})
	srv := &http.Server{Addr: *addr, Handler: (&server.Server{Gate: g, Flows: reg, Static: os.DirFS("web"), Instance: "dev", Log: log}).Handler(),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	_ = web.FS
	fmt.Println("dev wizard on https://" + *addr + "  code TEST-CODE")
	panic(srv.ListenAndServeTLS("", ""))
}
