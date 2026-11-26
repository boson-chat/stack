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
}

// PortsFor: the ports for the chosen features.
func PortsFor(voice, video bool) []PortSpec {
	p := []PortSpec{
		{"tcp", 80, "web", "HTTP: Let's Encrypt checks and redirects to HTTPS"},
		{"tcp", 443, "web", "HTTPS: the web client, sign-in, admin panel, API"},
		{"tcp", 6697, "irc", "IRC over TLS"},
	}
	if voice {
		p = append(p, PortSpec{"udp", 7700, "voice", "voice (QUIC)"})
	}
	if video {
		p = append(p, PortSpec{"udp", 7710, "video", "video and screen share (QUIC)"})
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
		addr := ":" + strconv.Itoa(s.Port)
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
		case errors.Is(err, syscall.EADDRINUSE):
			r.Local = "in_use"
		default:
			r.Local = "in_use"
		}
		if r.Local != "in_use" {
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
		if rep.Probe == "ran" {
			switch {
			case r.Proto == "tcp" && r.Local != "in_use":
				r.Reach = map[bool]string{true: "ok", false: "blocked"}[probe.TCP[strconv.Itoa(r.Port)]]
			case r.Proto == "udp" && r.Local == "free":
				r.Reach = map[bool]string{true: "ok", false: "blocked"}[gotUDP[r.Port]]
			}
		}
		describe(r)
	}
	rep.Ports = res
	return rep
}

func describe(r *PortResult) {
	name := fmt.Sprintf("%d/%s", r.Port, r.Proto)
	switch {
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
