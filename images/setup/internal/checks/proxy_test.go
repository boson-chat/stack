package checks

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestSoftwareFromImage(t *testing.T) {
	for im, want := range map[string]string{"traefik:v3.4": "traefik", "docker.io/library/nginx:1.27": "nginx", "jc21/nginx-proxy-manager:2": "nginx",
		"haproxy:3.0": "haproxy", "caddy:2": "caddy", "lscr.io/linuxserver/swag": "nginx", "openresty/openresty": "nginx", "postgres:17": "", "codeberg.org/forgejo/forgejo:9": ""} {
		if got := SoftwareFromImage(im); got != want {
			t.Errorf("%s: %q", im, got)
		}
	}
}

func TestDetectProxy(t *testing.T) {
	ps := strings.Join([]string{
		"b1\tboson-traefik-1\ttraefik:v3.7.19\t0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp\tboson",
		"f1\tforgejo\tcodeberg.org/forgejo/forgejo:9\t0.0.0.0:2222->22/tcp\tforgejo",
		"t1\tforgejo-traefik\ttraefik:v3.4\t0.0.0.0:80->80/tcp, [::]:80->80/tcp, 0.0.0.0:443->443/tcp\tforgejo",
		"n1\thost-nginx\tnginx:1.27\t\t",
		"p1\tpg\tpostgres:17\t\t",
	}, "\n")
	inspected := map[string]bool{}
	docker := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "ps":
			return []byte(ps + "\n"), nil
		case "inspect":
			id := args[len(args)-1]
			inspected[id] = true
			switch id {
			case "t1":
				return []byte(`"forgejo_default" {"forgejo_default":{"Gateway":"172.20.0.1"},"proxy":{"Gateway":"172.18.0.1"},"none":{"Gateway":""}}`), nil
			case "n1":
				return []byte(`"host" {"host":{"Gateway":""}}`), nil
			}
		}
		return nil, errors.New("no")
	}
	det := DetectProxy(context.Background(), docker, "boson")
	if len(det.Containers) != 2 {
		t.Fatalf("%+v", det)
	}
	c := det.Containers[0]
	if c.Name != "forgejo-traefik" || c.Software != "traefik" || c.HostNetwork || len(c.Networks) != 2 || c.Networks[0].Name != "proxy" {
		t.Fatalf("%+v", c)
	}
	if det.SuggestIP != "172.18.0.1" || det.Software != "traefik" {
		t.Fatalf("%+v", det)
	}
	if h := det.Containers[1]; h.Name != "host-nginx" || !h.HostNetwork || len(h.Networks) != 0 {
		t.Fatalf("%+v", h)
	}
	if inspected["b1"] || inspected["f1"] || inspected["p1"] {
		t.Error("inspected a container that can't be the proxy", inspected)
	}
	// Only a host-network proxy: 127.0.0.1.
	ps = "n1\thost-nginx\tnginx:1.27\t\t"
	if det := DetectProxy(context.Background(), docker, "boson"); det.SuggestIP != "127.0.0.1" || det.Software != "nginx" {
		t.Fatalf("%+v", det)
	}
	// No docker, or it fails: nothing found, 127.0.0.1.
	for _, d := range []DockerOutput{nil, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("x") }} {
		if det := DetectProxy(context.Background(), d, "boson"); len(det.Containers) != 0 || det.SuggestIP != "127.0.0.1" {
			t.Fatalf("%+v", det)
		}
	}
	if m, n := parseInspectNetworks("garbage"); m != "" || n != nil {
		t.Error(m, n)
	}
}

func TestBehindProxy(t *testing.T) {
	specs := BehindProxy(PortsFor(true, false), "127.0.0.1", 8081, "172.18.0.1", 8444)
	var names []string
	for _, s := range specs {
		names = append(names, s.Name())
	}
	if got := strings.Join(names, " "); got != "80/tcp 127.0.0.1:8081/tcp 443/tcp 172.18.0.1:8444/tcp 6697/tcp 7700/udp" {
		t.Fatal(got)
	}
	if !specs[0].Proxy || specs[1].Proxy || !specs[1].Private() || specs[0].Private() || specs[4].Private() {
		t.Fatalf("%+v", specs)
	}
	if (PortSpec{Port: 1, Proto: "tcp", Bind: "::1"}).Name() != "[::1]:1/tcp" || (PortSpec{Bind: "0.0.0.0"}).Private() {
		t.Error("names")
	}
}

func TestPortsBehindProxy(t *testing.T) {
	// The "proxy" holds a port on every address; Boson's ports are on
	// 127.0.0.1: one free, one taken, one on an address this host lacks.
	proxy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skip(err)
	}
	defer proxy.Close()
	proxyPort := proxy.Addr().(*net.TCPAddr).Port
	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	takenPort := taken.Addr().(*net.TCPAddr).Port
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	freePort := free.Addr().(*net.TCPAddr).Port
	free.Close()
	idle, _ := net.Listen("tcp", ":0")
	idlePort := idle.Addr().(*net.TCPAddr).Port
	idle.Close()
	var probed []int
	p := &Ports{
		HostIP: func() net.IP { return net.ParseIP("127.0.0.1") },
		Nonce:  func() string { return "n" },
		RunProbe: func(_ context.Context, _, _ string, tcp, _ []int) (ProbeResult, error) {
			probed = tcp
			r := ProbeResult{TCP: map[string]bool{}}
			for _, x := range tcp {
				r.TCP[strconv.Itoa(x)] = true
			}
			return r, nil
		},
	}
	specs := []PortSpec{
		{Proto: "tcp", Port: proxyPort, Feature: "web", Proxy: true},
		{Proto: "tcp", Port: idlePort, Feature: "web", Proxy: true},
		{Proto: "tcp", Port: freePort, Feature: "web", Bind: "127.0.0.1"},
		{Proto: "tcp", Port: takenPort, Feature: "web", Bind: "127.0.0.1"},
		{Proto: "tcp", Port: freePort, Feature: "web", Bind: "192.0.2.77"},
	}
	r := p.Check(context.Background(), specs)
	want := []struct{ local, level, msg string }{
		{"in_use", "ok", "by your reverse proxy"},
		{"free", "warn", "Nothing listens"},
		{"free", "ok", "Only your reverse proxy connects here"},
		{"in_use", "error", "Pick another port"},
		{"no_address", "error", "no address 192.0.2.77"},
	}
	for i, w := range want {
		g := r.Ports[i]
		if g.Local != w.local || g.Level != w.level || !strings.Contains(g.Message, w.msg) || g.Reach != "unknown" {
			t.Errorf("%d: %+v, want %+v", i, g, w)
		}
	}
	if len(probed) != 0 {
		t.Errorf("probed private/proxy ports: %v", probed)
	}
	// A previous Boson still on 443: says so.
	p.BosonPorts = func(context.Context) map[string]bool { return map[string]bool{"tcp/" + strconv.Itoa(proxyPort): true} }
	r = p.Check(context.Background(), specs[:1])
	if r.Ports[0].Level != "warn" || !strings.Contains(r.Ports[0].Message, "earlier install") {
		t.Errorf("%+v", r.Ports[0])
	}
	p.BosonPorts = func(context.Context) map[string]bool { return map[string]bool{"tcp/" + strconv.Itoa(takenPort): true} }
	r = p.Check(context.Background(), specs[3:4])
	if r.Ports[0].Level != "ok" || !strings.Contains(r.Ports[0].Message, "this Boson install") {
		t.Errorf("%+v", r.Ports[0])
	}
}
