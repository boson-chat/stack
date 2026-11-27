package checks

import (
	"context"
	"encoding/json"
	"net"
	"regexp"
	"sort"
	"strings"
)

// ProxyContainer is a container (not Boson's) that publishes 80 or 443:
// most likely the reverse proxy Boson goes behind.
type ProxyContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Software: traefik | nginx | caddy | haproxy | "" (unknown), from the image.
	Software string `json:"software"`
	// HostNetwork: it uses the host's network (reaches 127.0.0.1).
	HostNetwork bool `json:"host_network"`
	// Networks it's on, with each one's gateway (this server's address
	// on that network, where Boson can publish for it).
	Networks []ProxyNetwork `json:"networks"`
}

type ProxyNetwork struct {
	Name    string `json:"name"`
	Gateway string `json:"gateway"`
}

// ProxyDetection: what holds 80/443, and where Boson's Traefik should listen.
type ProxyDetection struct {
	Containers []ProxyContainer `json:"containers"`
	// Suggested listen IP for Boson's Traefik: a bridge gateway when the
	// proxy is a container on a bridge network, else 127.0.0.1.
	SuggestIP string `json:"suggest_ip"`
	// Software guessed from the container ("" = unknown).
	Software string `json:"software"`
}

// DockerOutput runs a docker command (dockercli.CLI.Output).
type DockerOutput func(ctx context.Context, args ...string) ([]byte, error)

var hostPortRe = regexp.MustCompile(`:(80|443)->\d+/tcp`)

// SoftwareFromImage guesses the proxy from its image name.
func SoftwareFromImage(image string) string {
	im := strings.ToLower(image)
	if i := strings.LastIndex(im, "/"); i >= 0 {
		im = im[i+1:]
	}
	for _, s := range []struct{ match, sw string }{
		{"traefik", "traefik"}, {"haproxy", "haproxy"}, {"caddy", "caddy"},
		{"nginx", "nginx"}, {"openresty", "nginx"}, {"swag", "nginx"},
	} {
		if strings.Contains(im, s.match) {
			return s.sw
		}
	}
	return ""
}

// DetectProxy looks for containers outside the boson project that publish
// 80 or 443. Errors (no docker) give an empty detection.
func DetectProxy(ctx context.Context, docker DockerOutput, project string) ProxyDetection {
	det := ProxyDetection{SuggestIP: "127.0.0.1", Containers: []ProxyContainer{}}
	if docker == nil {
		return det
	}
	out, err := docker(ctx, "ps", "--format", `{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Ports}}\t{{.Label "com.docker.compose.project"}}`)
	if err != nil {
		return det
	}
	for _, line := range strings.Split(strings.Trim(string(out), "\r\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || (len(f) >= 5 && f[4] == project) {
			continue
		}
		published := hostPortRe.MatchString(f[3])
		sw := SoftwareFromImage(f[2])
		if !published && (f[3] != "" || sw == "") {
			// Neither publishes 80/443 nor could be a proxy on the host's
			// network (which publishes nothing).
			continue
		}
		c := ProxyContainer{Name: f[1], Image: f[2], Software: sw, Networks: []ProxyNetwork{}}
		if info, err := docker(ctx, "inspect", "-f", `{{json .HostConfig.NetworkMode}} {{json .NetworkSettings.Networks}}`, f[0]); err == nil {
			mode, nets := parseInspectNetworks(string(info))
			c.HostNetwork = mode == "host"
			if !c.HostNetwork {
				c.Networks = nets
			}
		}
		if !published && !c.HostNetwork {
			continue
		}
		det.Containers = append(det.Containers, c)
	}
	if len(det.Containers) > 0 {
		c := det.Containers[0]
		det.Software = c.Software
		if !c.HostNetwork {
			for _, n := range c.Networks {
				if n.Gateway != "" {
					det.SuggestIP = n.Gateway
					break
				}
			}
		}
	}
	return det
}

func parseInspectNetworks(s string) (string, []ProxyNetwork) {
	s = strings.TrimSpace(s)
	i := strings.Index(s, " ")
	if i < 0 {
		return "", nil
	}
	var mode string
	_ = json.Unmarshal([]byte(s[:i]), &mode)
	var nets map[string]struct {
		Gateway string `json:"Gateway"`
	}
	_ = json.Unmarshal([]byte(s[i+1:]), &nets)
	out := []ProxyNetwork{}
	for name, n := range nets {
		if ip := net.ParseIP(n.Gateway); ip != nil {
			out = append(out, ProxyNetwork{Name: name, Gateway: n.Gateway})
		}
	}
	// A network named for the proxy first ("proxy", "traefik", "web"),
	// then by name, so the suggestion is stable.
	rank := func(n string) int {
		n = strings.ToLower(n)
		for _, w := range []string{"proxy", "traefik", "web", "edge", "ingress"} {
			if strings.Contains(n, w) {
				return 0
			}
		}
		return 1
	}
	sort.Slice(out, func(a, b int) bool {
		if ra, rb := rank(out[a].Name), rank(out[b].Name); ra != rb {
			return ra < rb
		}
		return out[a].Name < out[b].Name
	})
	return mode, out
}
