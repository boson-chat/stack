// Package-level note: this file is named zz_ so its init runs after
// hooks.go's (inits run per file in name order), which sets up the hook
// registry it registers into.

package webircgateway

// Boson's gateway policy (stack images/webircgateway): what keeps a
// WebSocket→TCP gateway from being an open proxy. Read from the environment
// once at start:
//
//	WEBIRC_EXTRA_PORTS      ports allowed besides the IRC defaults ("6680,8000-8001")
//	WEBIRC_ALIASES          internal targets: "irc.boson.chat:6697=ergo:6680,…"
//	WEBIRC_ALLOW_PRIVATE    true: any destination may be internal (dev/e2e only)
//	WEBIRC_UPSTREAM_ALLOW   hostname globs; when set, only these ("*.libera.chat,irc.oftc.net")
//	WEBIRC_UPSTREAM_DENY    hostname globs never allowed
//	WEBIRC_PASSWORDS        WEBIRC passwords by destination host (after aliases): "ergo=secret,irc.example.net=pw"
//	WEBIRC_MAX_PER_IP       concurrent connections per client IP (default 5, 0 = no cap)
//	WEBIRC_RATE_PER_MIN     new connections per client IP per minute (default 10, 0 = no cap)
//	WEBIRC_THROTTLE_BURST   lines a registered client may send at once before the
//	                        throttle (lines/s, config) applies (default 20)
//
// Refusals reach the client as one IRC line before the socket closes:
//
//	ERROR :Gateway refused (<code>): <reason>
//
// codes: host, port, address, denied, limit, rate, connect, tls.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gobwas/glob"
	"golang.org/x/time/rate"
)

// The ports IRC networks listen on.
var bosonDefaultPorts = []string{"6660-6669", "6697", "7000", "7070", "9999"}

// bosonMaxFrame caps one WebSocket message (an IRC line with tags is at
// most 8191 + 512 bytes; a client may batch a few).
const bosonMaxFrame = 64 << 10

type bosonAlias struct {
	host string
	port int
}

type bosonIPState struct {
	conns   int
	limiter *rate.Limiter
	seen    time.Time
}

type bosonPolicy struct {
	ports        map[int]bool
	aliases      map[string]bosonAlias // "host:port", lowercase
	allowPrivate bool
	allow, deny  []glob.Glob
	passwords    map[string]string
	maxPerIP     int
	ratePerMin   int
	burst        int

	mu       sync.Mutex
	perIP    map[string]*bosonIPState
	admitted map[uint64]string // client id → ip
	now      func() time.Time
}

var bosonPol *bosonPolicy

func init() {
	p, err := loadBosonPolicy(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "webircgateway: boson policy:", err)
		os.Exit(2)
	}
	bosonPol = p
	HookRegister("irc.connection.pre", func(h *HookIrcConnectionPre) { bosonPol.connectionPre(h) })
	HookRegister("client.state", func(h *HookClientState) {
		if !h.Connected {
			bosonPol.release(h.Client.Id)
		}
	})
}

func parsePorts(specs []string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		lo, hi := s, s
		if i := strings.IndexByte(s, '-'); i > 0 {
			lo, hi = s[:i], s[i+1:]
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b || b-a > 1000 {
			return nil, fmt.Errorf("bad port or range %q", s)
		}
		for p := a; p <= b; p++ {
			out[p] = true
		}
	}
	return out, nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func hostPort(s string) (string, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, fmt.Errorf("bad port in %q", s)
	}
	return normHost(h), n, nil
}

func normHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]")), ".")
}

func intEnv(get func(string) string, k string, def int) (int, error) {
	v := strings.TrimSpace(get(k))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a number ≥ 0, got %q", k, v)
	}
	return n, nil
}

func loadBosonPolicy(get func(string) string) (*bosonPolicy, error) {
	p := &bosonPolicy{
		aliases:   map[string]bosonAlias{},
		passwords: map[string]string{},
		perIP:     map[string]*bosonIPState{},
		admitted:  map[uint64]string{},
		now:       time.Now,
	}
	var err error
	if p.ports, err = parsePorts(append(append([]string{}, bosonDefaultPorts...), splitList(get("WEBIRC_EXTRA_PORTS"))...)); err != nil {
		return nil, fmt.Errorf("WEBIRC_EXTRA_PORTS: %w", err)
	}
	for _, a := range splitList(get("WEBIRC_ALIASES")) {
		from, to, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("WEBIRC_ALIASES: %q must be host:port=target:port", a)
		}
		fh, fp, err := hostPort(from)
		if err != nil {
			return nil, fmt.Errorf("WEBIRC_ALIASES: %w", err)
		}
		th, tp, err := hostPort(to)
		if err != nil {
			return nil, fmt.Errorf("WEBIRC_ALIASES: %w", err)
		}
		p.aliases[net.JoinHostPort(fh, strconv.Itoa(fp))] = bosonAlias{host: th, port: tp}
	}
	switch strings.ToLower(strings.TrimSpace(get("WEBIRC_ALLOW_PRIVATE"))) {
	case "", "false", "0", "no":
	case "true", "1", "yes":
		p.allowPrivate = true
	default:
		return nil, fmt.Errorf("WEBIRC_ALLOW_PRIVATE must be true or false")
	}
	for _, k := range []string{"WEBIRC_UPSTREAM_ALLOW", "WEBIRC_UPSTREAM_DENY"} {
		for _, g := range splitList(get(k)) {
			m, err := glob.Compile(strings.ToLower(g))
			if err != nil {
				return nil, fmt.Errorf("%s: %q: %w", k, g, err)
			}
			if k == "WEBIRC_UPSTREAM_ALLOW" {
				p.allow = append(p.allow, m)
			} else {
				p.deny = append(p.deny, m)
			}
		}
	}
	for _, kv := range splitList(get("WEBIRC_PASSWORDS")) {
		h, pw, ok := strings.Cut(kv, "=")
		if !ok || normHost(h) == "" {
			return nil, fmt.Errorf("WEBIRC_PASSWORDS: entries are host=password")
		}
		if pw = strings.TrimSpace(pw); pw != "" { // "ergo=" (no password set): no WEBIRC
			p.passwords[normHost(h)] = pw
		}
	}
	if p.maxPerIP, err = intEnv(get, "WEBIRC_MAX_PER_IP", 5); err != nil {
		return nil, err
	}
	if p.ratePerMin, err = intEnv(get, "WEBIRC_RATE_PER_MIN", 10); err != nil {
		return nil, err
	}
	if p.burst, err = intEnv(get, "WEBIRC_THROTTLE_BURST", 20); err != nil {
		return nil, err
	}
	if p.burst < 1 {
		p.burst = 1
	}
	return p, nil
}

// bosonDecision is where a HOST request may go, or why not.
type bosonDecision struct {
	host         string
	port         int
	tls          bool
	allowPrivate bool
	password     string
	refused      string // the full ERROR text when refused
}

func refused(code, reason string) bosonDecision {
	return bosonDecision{refused: fmt.Sprintf("Gateway refused (%s): %s", code, reason)}
}

func (p *bosonPolicy) matches(globs []glob.Glob, host string) bool {
	for _, g := range globs {
		if g.Match(host) {
			return true
		}
	}
	return false
}

// decide applies ports, aliases and the allow/deny lists to a HOST request.
func (p *bosonPolicy) decide(host string, port int, tls bool) bosonDecision {
	host = normHost(host)
	if host == "" {
		return refused("host", "send HOST <server>:[+]<port> first")
	}
	if !p.ports[port] {
		return refused("port", fmt.Sprintf("port %d is not an IRC port", port))
	}
	if a, ok := p.aliases[net.JoinHostPort(host, strconv.Itoa(port))]; ok {
		return bosonDecision{host: a.host, port: a.port, tls: false, allowPrivate: true, password: p.passwords[a.host]}
	}
	if p.matches(p.deny, host) {
		return refused("denied", host+" is not allowed through this gateway")
	}
	if len(p.allow) > 0 && !p.matches(p.allow, host) {
		return refused("denied", host+" is not allowed through this gateway")
	}
	return bosonDecision{host: host, port: port, tls: tls, allowPrivate: p.allowPrivate, password: p.passwords[host]}
}

func (p *bosonPolicy) connectionPre(h *HookIrcConnectionPre) {
	c, u := h.Client, h.UpstreamConfig
	if c.DestHost == "" {
		// The configured-upstream mode isn't used: every client says where.
		c.SendIrcError(refused("host", "send HOST <server>:[+]<port> first").refused)
		h.Halt = true
		return
	}
	d := p.decide(c.DestHost, c.DestPort, c.DestTLS)
	if d.refused != "" {
		c.Log(2, "Refused %s:%d: %s", c.DestHost, c.DestPort, d.refused)
		c.SendIrcError(d.refused)
		h.Halt = true
		return
	}
	u.Hostname = d.host
	u.Port = d.port
	u.TLS = d.tls
	u.TLSInsecure = d.tls && c.DestTLSInsecure
	u.AllowPrivate = d.allowPrivate
	u.WebircPassword = d.password
}

// admit counts a new client against its IP's caps; "" = allowed, else the
// ERROR text.
func (p *bosonPolicy) admit(id uint64, ip string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	// Forget idle IPs now and then.
	if len(p.perIP) > 4096 {
		for k, st := range p.perIP {
			if st.conns == 0 && now.Sub(st.seen) > 2*time.Minute {
				delete(p.perIP, k)
			}
		}
	}
	st := p.perIP[ip]
	if st == nil {
		st = &bosonIPState{}
		if p.ratePerMin > 0 {
			st.limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(p.ratePerMin)), p.ratePerMin)
		}
		p.perIP[ip] = st
	}
	st.seen = now
	if p.maxPerIP > 0 && st.conns >= p.maxPerIP {
		return refused("limit", fmt.Sprintf("too many connections from your address (at most %d)", p.maxPerIP)).refused
	}
	if st.limiter != nil && !st.limiter.AllowN(now, 1) {
		return refused("rate", "too many new connections from your address; wait a minute").refused
	}
	st.conns++
	p.admitted[id] = ip
	return ""
}

func (p *bosonPolicy) release(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ip, ok := p.admitted[id]
	if !ok {
		return
	}
	delete(p.admitted, id)
	if st := p.perIP[ip]; st != nil && st.conns > 0 {
		st.conns--
		st.seen = p.now()
	}
}

func (p *bosonPolicy) isAdmitted(id uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.admitted[id]
	return ok
}

func bosonAdmit(c *Client) string {
	return bosonPol.admit(c.Id, c.RemoteAddr)
}

func bosonThrottleBurst() int {
	return bosonPol.burst
}

// bosonQueueLines hands each line of one WebSocket message to the client,
// waiting (up to 10 s) for room rather than dropping lines. false = stop
// reading (the client isn't draining, or wasn't admitted).
func bosonQueueLines(c *Client, message string) bool {
	if !bosonPol.isAdmitted(c.Id) {
		return false
	}
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		bosonNoteLine(c, line)
		select {
		case c.Recv <- line:
		case <-time.After(10 * time.Second):
			c.Log(3, "Client not draining its queue; closing")
			return false
		}
	}
	return true
}

// Addresses no gateway user may reach unless the destination is an
// operator's alias (or WEBIRC_ALLOW_PRIVATE): everything that isn't a
// public unicast address.
var bosonBlockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()

func bosonPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// v4 and v4-mapped v6 (::ffff:a.b.c.d) are checked as v4.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range bosonBlockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

var errBosonInternal = errors.New("internal address")

// bosonDialControl checks the address actually being dialed (after DNS).
func bosonDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if !bosonPublicIP(net.ParseIP(h)) {
			return errBosonInternal
		}
		return nil
	}
}

// bosonDialError is the ERROR text for a failed dial.
func bosonDialError(err error) string {
	if errors.Is(err, errBosonInternal) {
		return "Gateway refused (address): that server is on an internal address"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "Gateway refused (connect): the server's name doesn't resolve"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "Gateway refused (connect): the server didn't answer in time"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "Gateway refused (connect): the server refused the connection"
	}
	return "Gateway refused (connect): couldn't reach the server"
}

// bosonTLSError says why the upstream TLS handshake failed, briefly.
func bosonTLSError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "certificate is not valid for"), strings.Contains(s, "x509: certificate is valid for"):
		return "the server's certificate is for a different name"
	case strings.Contains(s, "unknown authority"), strings.Contains(s, "self-signed"), strings.Contains(s, "self signed"):
		return "the server's certificate isn't trusted (self-signed?)"
	case strings.Contains(s, "expired"):
		return "the server's certificate has expired"
	default:
		return "TLS handshake failed"
	}
}
