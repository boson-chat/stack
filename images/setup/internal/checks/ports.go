package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PortSpec is a port the stack publishes.
type PortSpec struct {
	Proto   string `json:"proto"` // tcp | udp
	Port    int    `json:"port"`
	Feature string `json:"feature"` // web | irc | voice | video
	What    string `json:"what"`
	// Bind: the one address the stack publishes this port on (empty =
	// every address). A private one (127.0.0.1, a Docker bridge's gateway)
	// is only for a reverse proxy on this server: not probed from outside.
	Bind string `json:"bind,omitempty"`
	// Proxy: a port the reverse proxy that runs here already holds (Boson
	// behind it). In use is what's expected; informational.
	Proxy bool `json:"proxy,omitempty"`
	// Internal: a port on this server only Boson's own containers connect
	// to (node-exporter on the stack bridge's gateway), tested from a
	// container on the stack network.
	Internal bool `json:"internal,omitempty"`
}

// BehindProxy turns the ports for a server whose 80/443 belong to a reverse
// proxy already there: those become informational, and Boson's Traefik
// publishes on httpAddr / httpsAddr ([ip:]port, parsed by the caller)
// instead.
func BehindProxy(specs []PortSpec, httpIP string, httpPort int, httpsIP string, httpsPort int) []PortSpec {
	var out []PortSpec
	for _, s := range specs {
		if s.Proto == "tcp" && (s.Port == 80 || s.Port == 443) {
			s.Proxy = true
			s.What = map[int]string{80: "HTTP: your reverse proxy (it forwards Boson's names)", 443: "HTTPS: your reverse proxy (it passes Boson's names through)"}[s.Port]
			out = append(out, s)
			if s.Port == 80 {
				out = append(out, PortSpec{Proto: "tcp", Port: httpPort, Feature: "web", What: "Boson's HTTP, for your reverse proxy (Let's Encrypt's checks)", Bind: httpIP})
			} else {
				out = append(out, PortSpec{Proto: "tcp", Port: httpsPort, Feature: "web", What: "Boson's HTTPS, for your reverse proxy", Bind: httpsIP})
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// Private: published on one address that isn't the internet's way in.
func (s PortSpec) Private() bool {
	ip := net.ParseIP(s.Bind)
	return ip != nil && !ip.IsUnspecified()
}

// Name: "8444/tcp", or "127.0.0.1:8444/tcp" for a port on one address.
func (s PortSpec) Name() string {
	if s.Bind != "" {
		return fmt.Sprintf("%s/%s", net.JoinHostPort(s.Bind, strconv.Itoa(s.Port)), s.Proto)
	}
	return fmt.Sprintf("%d/%s", s.Port, s.Proto)
}

// PortsFor: the ports for the chosen features.
func PortsFor(voice, video bool) []PortSpec {
	p := []PortSpec{
		{Proto: "tcp", Port: 80, Feature: "web", What: "HTTP: Let's Encrypt checks and redirects to HTTPS"},
		{Proto: "tcp", Port: 443, Feature: "web", What: "HTTPS: the web client, sign-in, admin panel, API"},
		{Proto: "tcp", Port: 6697, Feature: "irc", What: "IRC over TLS"},
	}
	if voice {
		p = append(p, PortSpec{Proto: "udp", Port: 7700, Feature: "voice", What: "voice (QUIC)"})
	}
	if video {
		p = append(p, PortSpec{Proto: "udp", Port: 7710, Feature: "video", What: "video and screen share (QUIC)"})
	}
	return p
}

// PortResult is one port's state.
type PortResult struct {
	PortSpec
	// free | boson | in_use
	Local string `json:"local"`
	// ok | blocked | unknown
	Reach string `json:"reach"`
	// ok | warn | error
	Level   string `json:"level"`
	Message string `json:"message"`
	// Command: what to run on this server to fix it (a firewall rule).
	Command string `json:"command,omitempty"`
}

// ProbeResult is what the probe container reports (stdout, one JSON line).
type ProbeResult struct {
	TCP map[string]bool `json:"tcp"`
}

// Ports checks ports from the inside: whether another program already
// uses each one, and whether a connection from a container on Docker's
// network reaches it (that path crosses this server's own firewall:
// iptables, nftables, ufw). Firewalls in front of the server (a cloud
// provider's security groups) can't be seen from here.
type Ports struct {
	HostIP func() net.IP
	// BosonPorts: ports this stack's containers publish already ("tcp/443").
	BosonPorts func(ctx context.Context) map[string]bool
	// RunProbe starts the probe container (nil: reachability unknown).
	RunProbe func(ctx context.Context, host, nonce string, tcp, udp []int) (ProbeResult, error)
	Nonce    func() string
	// Node: node-exporter's address (NODE_EXPORTER_ADDR, the stack bridge's
	// gateway) and the stack subnet that scrapes it (nil: not checked).
	Node func() (addr, subnet string)
	// RunNodeProbe: whether a container on the stack network connects to
	// addr:port (this server's firewall decides).
	RunNodeProbe func(ctx context.Context, addr string, port int) (bool, error)

	// One check at a time: two would fight over the ports they listen on.
	mu sync.Mutex
}

type Report struct {
	HostIP  string       `json:"host_ip"`
	Ports   []PortResult `json:"ports"`
	Probe   string       `json:"probe"` // ran | failed: ...
	Outside string       `json:"outside"`
}

const outsideNote = "Not checked from here: firewalls in front of this server, such as your hosting provider's " +
	"security groups or cloud firewall. Open these ports there too."

func (p *Ports) Check(ctx context.Context, specs []PortSpec) Report {
	p.mu.Lock()
	defer p.mu.Unlock()
	host := p.HostIP()
	rep := Report{Outside: outsideNote}
	if host != nil {
		rep.HostIP = host.String()
	}
	boson := map[string]bool{}
	if p.BosonPorts != nil {
		boson = p.BosonPorts(ctx)
	}
	nonce := p.Nonce()
	var (
		mu      sync.Mutex
		gotUDP  = map[int]bool{}
		closers []func()
		tcp     []int
		udp     []int
	)
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	res := make([]PortResult, len(specs))
	for i, s := range specs {
		r := PortResult{PortSpec: s, Reach: "unknown"}
		addr := net.JoinHostPort(s.Bind, strconv.Itoa(s.Port))
		var err error
		if s.Proto == "tcp" {
			var l net.Listener
			if l, err = net.Listen("tcp", addr); err == nil {
				closers = append(closers, func() { l.Close() })
				go func() {
					for {
						c, err := l.Accept()
						if err != nil {
							return
						}
						c.Close()
					}
				}()
			}
		} else {
			var pc net.PacketConn
			if pc, err = net.ListenPacket("udp", addr); err == nil {
				closers = append(closers, func() { pc.Close() })
				port := s.Port
				go func() {
					buf := make([]byte, 512)
					for {
						n, _, err := pc.ReadFrom(buf)
						if err != nil {
							return
						}
						if strings.TrimSpace(string(buf[:n])) == nonce {
							mu.Lock()
							gotUDP[port] = true
							mu.Unlock()
						}
					}
				}()
			}
		}
		key := s.Proto + "/" + strconv.Itoa(s.Port)
		switch {
		case err == nil:
			r.Local = "free"
		case boson[key]:
			r.Local = "boson"
		case errors.Is(err, syscall.EADDRNOTAVAIL):
			r.Local = "no_address"
		case errors.Is(err, syscall.EADDRINUSE):
			r.Local = "in_use"
		default:
			r.Local = "in_use"
		}
		if r.Local != "in_use" && r.Local != "no_address" && !s.Private() && !s.Proxy {
			if s.Proto == "tcp" {
				tcp = append(tcp, s.Port)
			} else if r.Local == "free" {
				udp = append(udp, s.Port)
			}
		}
		res[i] = r
	}
	var probe ProbeResult
	switch {
	case p.RunProbe == nil || host == nil:
		rep.Probe = "skipped"
	default:
		pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		pr, err := p.RunProbe(pctx, host.String(), nonce, tcp, udp)
		cancel()
		if err != nil {
			rep.Probe = "failed: " + err.Error()
		} else {
			rep.Probe = "ran"
			probe = pr
			// Late datagrams.
			time.Sleep(500 * time.Millisecond)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range res {
		r := &res[i]
		if rep.Probe == "ran" && !r.Private() && !r.Proxy {
			switch {
			case r.Proto == "tcp" && r.Local != "in_use":
				r.Reach = map[bool]string{true: "ok", false: "blocked"}[probe.TCP[strconv.Itoa(r.Port)]]
			case r.Proto == "udp" && r.Local == "free":
				r.Reach = map[bool]string{true: "ok", false: "blocked"}[gotUDP[r.Port]]
			}
		}
		describe(r)
	}
	if n, ok := p.checkNode(ctx); ok {
		res = append(res, n)
	}
	rep.Ports = res
	return rep
}

// NodeExporterPort: node-exporter's port (compose.yaml).
const NodeExporterPort = 9100

// NodeRule: the ufw rule that lets the stack subnet reach node-exporter
// (install.sh adds it when ufw is active).
func NodeRule(addr, subnet string) string {
	return fmt.Sprintf("sudo ufw allow from %s to %s port %d proto tcp comment 'boson: metrics -> node-exporter'", subnet, addr, NodeExporterPort)
}

// checkNode: can the metrics container (on the stack network) reach
// node-exporter on the stack bridge's gateway? A firewall that drops
// incoming traffic by default (ufw) drops that too, and the admin panel's
// host metrics (job "node") stay down. Until the stack runs, a listener
// here stands in for node-exporter.
func (p *Ports) checkNode(ctx context.Context) (PortResult, bool) {
	if p.Node == nil || p.RunNodeProbe == nil {
		return PortResult{}, false
	}
	addr, subnet := p.Node()
	if net.ParseIP(addr) == nil {
		return PortResult{}, false
	}
	r := PortResult{
		PortSpec: PortSpec{Proto: "tcp", Port: NodeExporterPort, Feature: "metrics", Bind: addr, Internal: true,
			What: "host metrics: node-exporter, for Boson’s containers (" + subnet + ") only"},
		Reach: "unknown",
	}
	l, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(NodeExporterPort)))
	switch {
	case err == nil:
		r.Local = "free"
		defer l.Close()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		// The stack network isn't up: nothing to connect to.
		r.Local = "no_address"
	default:
		// node-exporter (or the listener of an earlier check) has it.
		r.Local = "boson"
	}
	if r.Local != "no_address" {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		reached, err := p.RunNodeProbe(pctx, addr, NodeExporterPort)
		cancel()
		if err == nil {
			r.Reach = map[bool]string{true: "ok", false: "blocked"}[reached]
		}
	}
	switch r.Reach {
	case "blocked":
		r.Level = "warn"
		r.Message = fmt.Sprintf("This server’s firewall drops connections from Boson’s network (%s) to %s, so the admin panel’s host metrics (CPU, memory, disks; Service health’s job node) stay down. Nothing else is affected. Allow just that path:", subnet, r.Name())
		r.Command = NodeRule(addr, subnet)
	case "ok":
		r.Level = "ok"
		r.Message = "Boson’s containers reach it: the admin panel gets this server’s host metrics."
	default:
		r.Level = "ok"
		r.Message = "Couldn’t test it from here. If the admin panel’s Service health shows job node down, allow it in the firewall: " + NodeRule(addr, subnet)
	}
	return r, true
}

func describe(r *PortResult) {
	name := r.Name()
	switch {
	case r.Proxy && r.Local == "in_use":
		r.Level = "ok"
		r.Message = "In use, by your reverse proxy: as expected. It forwards Boson’s names to the port below, so open it to the internet as before."
	case r.Proxy && r.Local == "boson":
		r.Level = "warn"
		r.Message = fmt.Sprintf("Boson itself still has %s (an earlier install). It moves to the address below when you install, and your reverse proxy can have it then.", name)
	case r.Proxy:
		r.Level = "warn"
		r.Message = fmt.Sprintf("Nothing listens on %s. Your reverse proxy should: is it running? Visitors and Let’s Encrypt reach Boson through it.", name)
	case r.Local == "no_address":
		r.Level = "error"
		r.Message = fmt.Sprintf("This server has no address %s, so Boson can’t listen there. Use 127.0.0.1, or the gateway of a Docker network your proxy is on.", r.Bind)
	case r.Local == "in_use" && r.Private():
		r.Level = "error"
		r.Message = fmt.Sprintf("Another program on this server already uses %s. Pick another port for Boson above.", name)
	case r.Private() && r.Local == "boson":
		r.Level = "ok"
		r.Message = "Used by this Boson install already. Only your reverse proxy connects here."
	case r.Private():
		r.Level = "ok"
		r.Message = "Free. Only your reverse proxy connects here, so it needs no firewall rule (and shouldn’t be open to the internet)."
	case r.Local == "in_use":
		r.Level = "error"
		r.Message = fmt.Sprintf("Another program on this server already uses %s. Stop it (or move it to another port) before installing, or the %s part can't start.", name, r.Feature)
	case r.Reach == "blocked" && r.Proto == "udp":
		r.Level = "warn"
		r.Message = fmt.Sprintf("This server's firewall drops %s: %s won't work for anyone. Allow it (ufw: sudo ufw allow %d/udp), or turn %s off.", name, r.Feature, r.Port, r.Feature)
	case r.Reach == "blocked":
		r.Level = "warn"
		r.Message = fmt.Sprintf("A test connection to %s from Docker's network was refused or dropped by this server's firewall. Ports Docker publishes usually get through anyway, but check your firewall allows %s.", name, name)
	case r.Local == "boson":
		r.Level = "ok"
		r.Message = "Used by this Boson install already."
	case r.Reach == "ok":
		r.Level = "ok"
		r.Message = "Free, and this server's firewall lets it through."
	default:
		r.Level = "ok"
		r.Message = "Free. Couldn't test the firewall from here."
	}
}

// RunProbeClient is the probe container's side: connect to each TCP port
// and send the nonce to each UDP port, then print a ProbeResult.
func RunProbeClient(host, nonce string, tcp, udp []int) ProbeResult {
	res := ProbeResult{TCP: map[string]bool{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, p := range tcp {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(p)), 4*time.Second)
			if err == nil {
				c.Close()
			}
			mu.Lock()
			res.TCP[strconv.Itoa(p)] = err == nil
			mu.Unlock()
		}(p)
	}
	for _, p := range udp {
		if c, err := net.Dial("udp", net.JoinHostPort(host, strconv.Itoa(p))); err == nil {
			for i := 0; i < 3; i++ {
				_, _ = c.Write([]byte(nonce))
				time.Sleep(150 * time.Millisecond)
			}
			c.Close()
		}
	}
	wg.Wait()
	return res
}

// ParseProbeOutput finds the result line in the probe container's output.
func ParseProbeOutput(out []byte) (ProbeResult, error) {
	for _, line := range strings.Split(string(out), "\n") {
		var r ProbeResult
		if strings.HasPrefix(strings.TrimSpace(line), "{") && json.Unmarshal([]byte(line), &r) == nil && r.TCP != nil {
			return r, nil
		}
	}
	return ProbeResult{}, errors.New("no result from the probe container")
}

// ParseInts parses "80,443".
func ParseInts(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}
