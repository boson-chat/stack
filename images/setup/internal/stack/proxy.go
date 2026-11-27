package stack

// Behind an existing reverse proxy (docs/reverse-proxy.md): another program
// on this server (a Traefik, nginx, HAProxy or Caddy that serves other
// sites) already holds 80/443. Boson's Traefik then publishes on private
// addresses instead (TRAEFIK_HTTP_PUBLISH / TRAEFIK_HTTPS_PUBLISH), the
// outer proxy passes TLS for Boson's names through untouched with a PROXY
// protocol v2 header (so Traefik, and everything behind it, sees each
// visitor's own address) and forwards port 80 for them (Let's Encrypt's
// HTTP-01 challenges). The wizard never edits the outer proxy: it shows
// the snippets (ProxySnippets) for its admin to paste.

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Reverse proxies the wizard has snippets for.
const (
	ProxyTraefik = "traefik"
	ProxyNginx   = "nginx"
	ProxyCaddy   = "caddy"
	ProxyHAProxy = "haproxy"
	ProxyOther   = "other"
)

// Defaults for Boson's Traefik behind a proxy: loopback, so only programs
// on this server reach it.
const (
	DefaultProxyHTTP  = "127.0.0.1:8081"
	DefaultProxyHTTPS = "127.0.0.1:8444"
	// DefaultStackSubnet: compose.yaml's STACK_SUBNET default.
	DefaultStackSubnet = "10.203.53.0/24"
)

// The .env keys (compose.yaml traefik).
const (
	EnvHTTPPublish  = "TRAEFIK_HTTP_PUBLISH"
	EnvHTTPSPublish = "TRAEFIK_HTTPS_PUBLISH"
	EnvProxyTrusted = "TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS"
)

// ProxyAnswers: Boson behind a reverse proxy that already runs here.
type ProxyAnswers struct {
	Enabled bool `json:"enabled"`
	// traefik | nginx | caddy | haproxy | other: which snippets to show.
	Software string `json:"software"`
	// Where Boson's Traefik publishes :80 and :443, [ip:]port.
	HTTPAddr  string `json:"http_addr"`
	HTTPSAddr string `json:"https_addr"`
	// TrustedIPs: whose PROXY header (and X-Forwarded-For on :80) Boson's
	// Traefik believes, comma-separated IPs or CIDRs. Docker hands it every
	// connection to a published port from the stack network's gateway.
	TrustedIPs string `json:"trusted_ips"`
}

func (p *ProxyAnswers) normalize() {
	p.Software = strings.ToLower(strings.TrimSpace(p.Software))
	if p.Software == "" {
		p.Software = ProxyTraefik
	}
	p.HTTPAddr = strings.TrimSpace(p.HTTPAddr)
	p.HTTPSAddr = strings.TrimSpace(p.HTTPSAddr)
	if p.HTTPAddr == "" {
		p.HTTPAddr = DefaultProxyHTTP
	}
	if p.HTTPSAddr == "" {
		p.HTTPSAddr = DefaultProxyHTTPS
	}
	p.TrustedIPs = strings.Join(splitList(p.TrustedIPs), ",")
}

func splitList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == ';'
	}) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// TrustedList: TrustedIPs as a list.
func (p ProxyAnswers) TrustedList() []string { return splitList(p.TrustedIPs) }

// Publish is a parsed [ip:]port (compose's short port syntax, host side).
type Publish struct {
	// IP: empty = every address.
	IP   string
	Port int
}

// ParsePublish reads "8444", "127.0.0.1:8444" or "[::1]:8444".
func ParsePublish(s string) (Publish, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Publish{}, fmt.Errorf("empty address")
	}
	host, port := "", s
	if strings.Contains(s, ":") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return Publish{}, fmt.Errorf("%q isn't an address and port, like 127.0.0.1:8444", s)
		}
		host, port = h, p
		if _, err := netip.ParseAddr(host); err != nil {
			return Publish{}, fmt.Errorf("%q isn't an IP address", host)
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Publish{}, fmt.Errorf("%q isn't a port (1-65535)", port)
	}
	if host == "0.0.0.0" || host == "::" {
		host = ""
	}
	return Publish{IP: host, Port: n}, nil
}

// Public: published on every address (the internet reaches it too, unless
// a firewall stops it).
func (p Publish) Public() bool { return p.IP == "" }

// Target: where a proxy on this server connects to reach it.
func (p Publish) Target() string {
	ip := p.IP
	if ip == "" {
		ip = "127.0.0.1"
	}
	return net.JoinHostPort(ip, strconv.Itoa(p.Port))
}

// StackGateway: the stack network's gateway, the first address of subnet
// (Docker's default). Every connection to a port Boson publishes reaches
// Traefik from there when it comes from this server: Docker's proxy or NAT
// rewrites its source (checked on Docker 29, with and without the
// userland proxy).
func StackGateway(subnet string) string {
	if subnet == "" {
		subnet = DefaultStackSubnet
	}
	pfx, err := netip.ParsePrefix(subnet)
	if err != nil {
		pfx = netip.MustParsePrefix(DefaultStackSubnet)
	}
	return pfx.Masked().Addr().Next().String()
}

func validTrusted(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	_, err := netip.ParsePrefix(s)
	return err == nil
}

func (p ProxyAnswers) validate(add func(f, m string)) {
	if !p.Enabled {
		return
	}
	switch p.Software {
	case ProxyTraefik, ProxyNginx, ProxyCaddy, ProxyHAProxy, ProxyOther:
	default:
		add("proxy.software", "Pick your reverse proxy (or Other).")
	}
	httpP, err := ParsePublish(p.HTTPAddr)
	if err != nil {
		add("proxy.http_addr", err.Error()+".")
	} else if httpP.Public() && (httpP.Port == 80 || httpP.Port == 443) {
		add("proxy.http_addr", "Your reverse proxy has this port. Pick another one, like 127.0.0.1:8081.")
	}
	httpsP, err2 := ParsePublish(p.HTTPSAddr)
	if err2 != nil {
		add("proxy.https_addr", err2.Error()+".")
	} else if httpsP.Public() && (httpsP.Port == 80 || httpsP.Port == 443) {
		add("proxy.https_addr", "Your reverse proxy has this port. Pick another one, like 127.0.0.1:8444.")
	}
	if err == nil && err2 == nil && httpP.Port == httpsP.Port && (httpP.IP == httpsP.IP || httpP.Public() || httpsP.Public()) {
		add("proxy.https_addr", "HTTP and HTTPS need different ports.")
	}
	list := p.TrustedList()
	if len(list) == 0 {
		add("proxy.trusted_ips", "Boson needs to know who passes it visitors' addresses: enter at least one address.")
	}
	for _, t := range list {
		if !validTrusted(t) {
			add("proxy.trusted_ips", fmt.Sprintf("%q isn't an IP address or a range like 172.18.0.0/16.", t))
			break
		}
		if a, err := netip.ParseAddr(t); err == nil && a.IsUnspecified() || strings.HasSuffix(t, "/0") {
			add("proxy.trusted_ips", "Not everyone: anyone could then claim any address. List your proxy’s addresses only.")
			break
		}
	}
}

// proxyEnv: the .env lines for Boson's Traefik. Off: compose's defaults
// (written only over values a previous run left).
func proxyEnv(a Answers, existing map[string]string, set func(k, v string)) {
	if a.Proxy.Enabled {
		set(EnvHTTPPublish, a.Proxy.HTTPAddr)
		set(EnvHTTPSPublish, a.Proxy.HTTPSAddr)
		set(EnvProxyTrusted, a.Proxy.TrustedIPs)
		return
	}
	for k, def := range map[string]string{EnvHTTPPublish: "80", EnvHTTPSPublish: "443", EnvProxyTrusted: ""} {
		if v, ok := existing[k]; ok && v != def {
			set(k, def)
		}
	}
}

// proxyFromEnv reads the proxy answers back from .env.
func proxyFromEnv(e map[string]string) ProxyAnswers {
	p := ProxyAnswers{HTTPAddr: e[EnvHTTPPublish], HTTPSAddr: e[EnvHTTPSPublish], TrustedIPs: e[EnvProxyTrusted]}
	def := func(v, port string) bool { return v == "" || v == port || v == "0.0.0.0:"+port }
	p.Enabled = !def(p.HTTPAddr, "80") || !def(p.HTTPSAddr, "443") || p.TrustedIPs != ""
	if !p.Enabled {
		p.HTTPAddr, p.HTTPSAddr = "", ""
	}
	return p
}

// HTTPSTarget / HTTPTarget: where a proxy on this server reaches Boson's
// Traefik (the published addresses; every address = 127.0.0.1).
func (a Answers) HTTPSTarget() string { return publishTarget(a.Proxy.HTTPSAddr, "443") }
func (a Answers) HTTPTarget() string  { return publishTarget(a.Proxy.HTTPAddr, "80") }

func publishTarget(addr, def string) string {
	p, err := ParsePublish(addr)
	if err != nil {
		p, _ = ParsePublish(def)
	}
	return p.Target()
}

// ProxySnippets: what to add to the reverse proxy for these answers (its
// software's config), with the install's own names and addresses.
func ProxySnippets(a Answers) []Snippet {
	var names []string
	for _, h := range a.NeededHosts() {
		names = append(names, h.Name)
	}
	return proxySnippets(a.Proxy.Software, a.Domain, names, a.HTTPTarget(), a.HTTPSTarget())
}

func proxySnippets(software, domain string, names []string, httpT, httpsT string) []Snippet {
	id := snippetName(domain)
	switch software {
	case ProxyTraefik:
		return []Snippet{traefikSnippet(id, domain, names, httpT, httpsT)}
	case ProxyNginx:
		return nginxSnippets(id, domain, names, httpT, httpsT)
	case ProxyHAProxy:
		return []Snippet{haproxySnippet(id, domain, names, httpT, httpsT)}
	case ProxyCaddy:
		return caddySnippets(id, domain, names, httpT, httpsT)
	}
	return []Snippet{otherSnippet(domain, names, httpT, httpsT)}
}

// snippetName: a config identifier for the install ("boson-chat-example-com").
func snippetName(domain string) string {
	return "boson-" + strings.ReplaceAll(domain, ".", "-")
}

func traefikSnippet(id, domain string, names []string, httpT, httpsT string) Snippet {
	var sni, host []string
	for _, n := range names {
		sni = append(sni, "HostSNI(`"+n+"`)")
		host = append(host, "Host(`"+n+"`)")
	}
	text := fmt.Sprintf(`# Boson (%[1]s): TLS for its names passes through untouched, with a PROXY
# protocol v2 header so Boson sees each visitor's own address; port 80 for
# them goes to Boson too (Let's Encrypt's checks). Use your entrypoint names.
tcp:
  routers:
    %[2]s:
      entryPoints: [websecure]
      rule: %[3]s
      tls:
        passthrough: true
      service: %[2]s
  services:
    %[2]s:
      loadBalancer:
        proxyProtocol:
          version: 2
        servers:
          - address: "%[4]s"
http:
  routers:
    %[2]s-http:
      entryPoints: [web]
      rule: %[5]s
      # Above your own routers for these names.
      priority: 1000
      service: %[2]s-http
  services:
    %[2]s-http:
      loadBalancer:
        servers:
          - url: "http://%[6]s"
`, domain, id, yamlQuote(strings.Join(sni, " || ")), httpsT, yamlQuote(strings.Join(host, " || ")), httpT)
	return Snippet{ID: "proxy-traefik", Title: "Traefik: route Boson's names to Boson", For: "proxy",
		Where: "A file in your Traefik's file provider directory (it reloads on its own when it watches the directory), e.g. boson.yml.",
		Text:  text,
		Warning: "An http→https redirect on your web entrypoint (entryPoints.web.http.redirections) wins over every router unless you set its priority: 1. " +
			"If your Traefik gets its own certificates with the HTTP challenge, also set allowACMEBypass: true on that entrypoint, or it answers Boson's Let's Encrypt checks itself (with a 404).",
	}
}

func yamlQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func nginxSnippets(id, domain string, names []string, httpT, httpsT string) []Snippet {
	up := strings.ReplaceAll(id, "-", "_")
	var m strings.Builder
	for _, n := range names {
		fmt.Fprintf(&m, "        %-28s %s;\n", n, up)
	}
	stream := fmt.Sprintf(`# Boson (%[1]s): nginx reads the name a visitor asks for (SNI) and passes
# Boson's TLS through untouched, with a PROXY protocol header so Boson sees
# the visitor's own address. Everything else goes to your own HTTPS sites,
# which now listen on 127.0.0.1:4443 instead of 443 (see below).
stream {
    map $ssl_preread_server_name $boson_upstream {
%[2]s        default                      own_https;
    }
    upstream %[3]s { server %[4]s; }
    upstream own_https { server 127.0.0.1:4443; }
    server {
        listen 443;
        listen [::]:443;
        ssl_preread on;
        proxy_pass $boson_upstream;
        proxy_protocol on;
    }
}
`, domain, m.String(), up, httpsT)
	httpText := fmt.Sprintf(`# Boson (%[1]s): port 80 for Boson's names (Let's Encrypt's checks and
# the redirect to https).
server {
    listen 80;
    listen [::]:80;
    server_name %[2]s;
    location / {
        proxy_pass http://%[3]s;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

# Your own HTTPS sites: the stream block above has 443 now, and sends them
# here with the same header. In each of your server blocks, instead of
# "listen 443 ssl;":
#     listen 127.0.0.1:4443 ssl proxy_protocol;
#     set_real_ip_from 127.0.0.1;
#     real_ip_header proxy_protocol;
`, domain, strings.Join(names, "\n                "), httpT)
	return []Snippet{
		{ID: "proxy-nginx-stream", Title: "nginx: pass Boson's TLS through (stream)", For: "proxy",
			Where: "nginx.conf at the top level, next to http { } (not inside it). Needs nginx's stream module: built in on most packages, or load_module modules/ngx_stream_module.so; (Debian/Ubuntu: libnginx-mod-stream). Then nginx -t && nginx -s reload.",
			Text:  stream},
		{ID: "proxy-nginx-http", Title: "nginx: port 80 for Boson's names (http)", For: "proxy",
			Where: "Inside http { }, e.g. a file in /etc/nginx/conf.d/ or sites-enabled/. Move your own sites off 443 as the comment says, then nginx -t && nginx -s reload.",
			Text:  httpText},
	}
}

func haproxySnippet(id, domain string, names []string, httpT, httpsT string) Snippet {
	text := fmt.Sprintf(`# Boson (%[1]s): TLS for Boson's names passes through untouched with a
# PROXY protocol v2 header (Boson sees each visitor's own address); port 80
# for them goes to Boson too (Let's Encrypt's checks). If you have frontends
# on :443 and :80 already, add the use_backend lines to them instead.
frontend https_in
    bind :443
    mode tcp
    tcp-request inspect-delay 5s
    tcp-request content accept if { req_ssl_hello_type 1 }
    use_backend %[2]s_https if { req_ssl_sni -i %[3]s }
    # default_backend your_https

frontend http_in
    bind :80
    mode http
    use_backend %[2]s_http if { req.hdr(host),field(1,:) -i %[3]s }
    # default_backend your_http

backend %[2]s_https
    mode tcp
    server boson %[4]s send-proxy-v2

backend %[2]s_http
    mode http
    option forwardfor
    server boson %[5]s
`, domain, strings.ReplaceAll(id, "-", "_"), strings.Join(names, " "), httpsT, httpT)
	return Snippet{ID: "proxy-haproxy", Title: "HAProxy: route Boson's names to Boson", For: "proxy",
		Where: "haproxy.cfg, then check it (haproxy -c -f /etc/haproxy/haproxy.cfg) and reload HAProxy.",
		Text:  text}
}

func caddySnippets(id, domain string, names []string, httpT, httpsT string) []Snippet {
	var http []string
	for _, n := range names {
		http = append(http, "http://"+n)
	}
	l4 := fmt.Sprintf(`# Boson (%[1]s): needs Caddy built with the layer4 app
# (xcaddy build --with github.com/mholt/caddy-l4). On Caddy's own :443 it
# passes TLS for Boson's names through untouched, with a PROXY protocol v2
# header; everything else goes on to your sites as before.
{
	servers :443 {
		listener_wrappers {
			layer4 {
				@boson tls sni %[2]s
				route @boson {
					proxy {
						proxy_protocol v2
						upstream tcp/%[3]s
					}
				}
			}
			tls
		}
	}
}
`, domain, strings.Join(names, " "), httpsT)
	plain := fmt.Sprintf(`# Boson (%[1]s): port 80 for Boson's names (Let's Encrypt's checks and the
# redirect to https). Plain Caddy can do this part.
%[2]s {
	reverse_proxy %[3]s
}
`, domain, strings.Join(http, ", "), httpT)
	return []Snippet{
		{ID: "proxy-caddy-l4", Title: "Caddy (with caddy-l4): pass Boson's TLS through", For: "proxy",
			Where: "The global options block at the top of your Caddyfile.",
			Warning: "Plain Caddy can't do this: it can only pass TLS through untouched with the caddy-l4 plugin. Without it, Caddy would have to terminate TLS for Boson's names itself, and that doesn't work for Boson: IRC (6697) and voice use the same certificates, which Boson's Traefik gets. " +
				"Boson's CI doesn't test this snippet (it does test the Traefik, nginx and HAProxy ones): check it against caddy-l4's documentation for your version.",
			Text: l4},
		{ID: "proxy-caddy-http", Title: "Caddy: port 80 for Boson's names", For: "proxy",
			Where: "Your Caddyfile, then caddy reload.",
			Text:  plain},
	}
}

func otherSnippet(domain string, names []string, httpT, httpsT string) Snippet {
	text := fmt.Sprintf(`Boson (%[1]s) needs two things from your reverse proxy, for these names:
  %[2]s

1. Port 443: pass TLS through untouched (TCP, routed by SNI; your proxy must
   not terminate TLS for these names) to
     %[3]s
   with a PROXY protocol header (v2, or v1) in front of each connection.
2. Port 80: forward HTTP for the same names to
     http://%[4]s
   keeping the Host header (Let's Encrypt's checks). Set X-Forwarded-For.
`, domain, strings.Join(names, "\n  "), httpsT, httpT)
	return Snippet{ID: "proxy-other", Title: "What your reverse proxy needs to do", For: "proxy",
		Where: "Your reverse proxy's config (its documentation has the syntax).", Text: text}
}
