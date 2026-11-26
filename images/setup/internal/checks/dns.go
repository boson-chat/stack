package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

// Resolver is the part of net.Resolver the DNS check uses.
type Resolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// NewResolver: the system resolver, or a fixed DNS server (host:port).
func NewResolver(server string) Resolver {
	if server == "" {
		return &net.Resolver{PreferGo: true}
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, server)
	}}
}

// HostResult is one hostname's DNS state.
type HostResult struct {
	stack.Host
	A    []string `json:"a"`
	AAAA []string `json:"aaaa"`
	// ok | missing | elsewhere | error
	Status  string `json:"status"`
	Message string `json:"message"`
}

// DNS checks that hostnames point at this server: an address counts as
// this server when it's one of the host's own, or when this wizard answers
// on it (NAT'd clouds: the public IP isn't on any interface).
type DNS struct {
	Resolver Resolver
	LocalIPs func() []net.IP
	// Probe reports whether ip reaches this wizard; nil = no probe.
	Probe func(ctx context.Context, ip net.IP) bool
}

// Summary is the whole check.
type Summary struct {
	OK       bool         `json:"ok"`
	Hosts    []HostResult `json:"hosts"`
	ServerIP []string     `json:"server_ips"`
}

func (d *DNS) Check(ctx context.Context, hosts []stack.Host) Summary {
	local := map[string]bool{}
	var serverIPs []string
	for _, ip := range d.LocalIPs() {
		local[ip.String()] = true
		serverIPs = append(serverIPs, ip.String())
	}
	var mu sync.Mutex
	probed := map[string]bool{}
	ours := func(ip net.IP) bool {
		s := ip.String()
		if local[s] {
			return true
		}
		mu.Lock()
		v, ok := probed[s]
		mu.Unlock()
		if ok {
			return v
		}
		v = d.Probe != nil && d.Probe(ctx, ip)
		mu.Lock()
		probed[s] = v
		mu.Unlock()
		return v
	}
	out := make([]HostResult, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(i int, h stack.Host) {
			defer wg.Done()
			out[i] = d.one(ctx, h, ours)
		}(i, h)
	}
	wg.Wait()
	s := Summary{OK: true, Hosts: out, ServerIP: serverIPs}
	for _, h := range out {
		if h.Status != "ok" {
			s.OK = false
		}
	}
	return s
}

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && (de.IsNotFound || strings.Contains(de.Err, "no such host"))
}

func (d *DNS) one(ctx context.Context, h stack.Host, ours func(net.IP) bool) (r HostResult) {
	r = HostResult{Host: h, A: []string{}, AAAA: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v4, err4 := d.Resolver.LookupIP(ctx, "ip4", h.Name)
	v6, err6 := d.Resolver.LookupIP(ctx, "ip6", h.Name)
	if err4 != nil && !notFound(err4) {
		r.Status = "error"
		r.Message = "Couldn't look this name up right now (" + shortErr(err4) + "). Checking again shortly."
		return r
	}
	aaaaNote := ""
	if err6 != nil && !notFound(err6) {
		// Some DNS servers answer AAAA badly when there is none; the A
		// record decides, with a note.
		v6 = nil
		aaaaNote = " (The AAAA lookup failed: " + shortErr(err6) + ".)"
	}
	defer func() { r.Message += aaaaNote }()
	var wrong []string
	for _, ip := range v4 {
		r.A = append(r.A, ip.String())
		if !ours(ip) {
			wrong = append(wrong, ip.String())
		}
	}
	for _, ip := range v6 {
		r.AAAA = append(r.AAAA, ip.String())
		if !ours(ip) {
			wrong = append(wrong, ip.String())
		}
	}
	switch {
	case len(r.A)+len(r.AAAA) == 0:
		r.Status = "missing"
		r.Message = "No A or AAAA record yet. Add one pointing at this server."
	case len(wrong) > 0:
		r.Status = "elsewhere"
		kind := "points to"
		if len(wrong) < len(r.A)+len(r.AAAA) {
			kind = "also points to"
		}
		r.Message = fmt.Sprintf("%s %s %s, which is not this server.", h.Name, kind, strings.Join(wrong, ", "))
		if len(r.AAAA) > 0 && containsAny(wrong, r.AAAA) {
			r.Message += " Let's Encrypt uses the AAAA (IPv6) record when there is one, so fix or remove it too."
		}
	default:
		r.Status = "ok"
		r.Message = "Points at this server."
	}
	return r
}

func containsAny(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func shortErr(err error) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		return de.Err
	}
	return err.Error()
}

// InstanceProbe asks https://ip:port/.well-known/boson-setup for this
// wizard's random instance id.
func InstanceProbe(port int, instance string) func(ctx context.Context, ip net.IP) bool {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // our own self-signed cert; the id is what's compared
			Proxy:           nil,
		},
	}
	return func(ctx context.Context, ip net.IP) bool {
		url := fmt.Sprintf("https://%s/.well-known/boson-setup", net.JoinHostPort(ip.String(), fmt.Sprint(port)))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return resp.StatusCode == 200 && strings.TrimSpace(string(b)) == instance
	}
}
