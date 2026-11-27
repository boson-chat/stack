// proxysnippets writes the reverse-proxy snippets the wizard shows (Firewall
// and ports step, behind an existing proxy) to files, so test/proxy runs a
// real Traefik, nginx or HAProxy with exactly that config.
//
//	go run ./cmd/proxysnippets -software nginx -domain boson.test \
//	    -http 10.213.0.1:8081 -https 10.213.0.1:8444 -out DIR
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

func main() {
	sw := flag.String("software", "traefik", "traefik | nginx | haproxy | caddy | other")
	domain := flag.String("domain", "boson.test", "the install's domain")
	httpA := flag.String("http", stack.DefaultProxyHTTP, "TRAEFIK_HTTP_PUBLISH")
	httpsA := flag.String("https", stack.DefaultProxyHTTPS, "TRAEFIK_HTTPS_PUBLISH")
	wellknown := flag.Bool("wellknown", false, "the bare domain too")
	out := flag.String("out", ".", "directory: one file per snippet, named by its id")
	flag.Parse()
	a := stack.Defaults()
	a.Domain = *domain
	a.Wellknown = *wellknown
	a.Proxy = stack.ProxyAnswers{Enabled: true, Software: *sw, HTTPAddr: *httpA, HTTPSAddr: *httpsA, TrustedIPs: "127.0.0.1"}
	a.Normalize()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, s := range stack.ProxySnippets(a) {
		p := filepath.Join(*out, s.ID)
		if err := os.WriteFile(p, []byte(s.Text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(p)
	}
}
