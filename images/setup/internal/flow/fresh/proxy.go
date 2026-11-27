package fresh

// Behind an existing reverse proxy (docs/reverse-proxy.md): the Firewall
// and ports step's extra API. What holds 80/443 (a container we can see),
// where Boson's Traefik should listen, and the snippets for the proxy.

import (
	"context"
	"net/http"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/checks"
	"github.com/boson-chat/stack/images/setup/internal/flow"
	"github.com/boson-chat/stack/images/setup/internal/stack"
)

// stackGateway: where Docker hands Boson's Traefik connections from this
// server (the trusted address for a proxy here).
func (f *Flow) stackGateway() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return stack.StackGateway(f.existing.Env["STACK_SUBNET"])
}

// proxyDefaults fills what the proxy answers leave empty.
func (f *Flow) proxyDefaults(a *stack.Answers) {
	if a.Proxy.Enabled && a.Proxy.TrustedIPs == "" {
		a.Proxy.TrustedIPs = f.stackGateway()
	}
}

// portSpecs: the ports to check for these answers.
func portSpecs(a stack.Answers) []checks.PortSpec {
	specs := checks.PortsFor(a.Voice, a.Video)
	if !a.Proxy.Enabled {
		return specs
	}
	hp, err1 := stack.ParsePublish(a.Proxy.HTTPAddr)
	sp, err2 := stack.ParsePublish(a.Proxy.HTTPSAddr)
	if err1 != nil || err2 != nil {
		return specs
	}
	return checks.BehindProxy(specs, hp.IP, hp.Port, sp.IP, sp.Port)
}

type proxyDetectResp struct {
	checks.ProxyDetection
	// Suggested answers: listen on SuggestIP:8081/8444, trust the stack
	// network's gateway.
	Suggest stack.ProxyAnswers `json:"suggest"`
	Gateway string             `json:"gateway"`
}

func (f *Flow) proxyDetect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	det := checks.DetectProxy(ctx, f.d.Docker, "boson")
	gw := f.stackGateway()
	sug := stack.ProxyAnswers{Enabled: true, Software: det.Software, TrustedIPs: gw,
		HTTPAddr: det.SuggestIP + ":8081", HTTPSAddr: det.SuggestIP + ":8444"}
	if sug.Software == "" {
		sug.Software = stack.ProxyTraefik
	}
	flow.JSON(w, http.StatusOK, proxyDetectResp{ProxyDetection: det, Suggest: sug, Gateway: gw})
}

func (f *Flow) proxySnippets(w http.ResponseWriter, _ *http.Request) {
	a := f.currentAnswers()
	if !a.Proxy.Enabled {
		flow.JSON(w, http.StatusOK, map[string]any{"enabled": false, "snippets": []stack.Snippet{}})
		return
	}
	if !stack.ValidDomain(a.Domain) {
		flow.Error(w, http.StatusUnprocessableEntity, "no_domain", "Enter your domain first.")
		return
	}
	flow.JSON(w, http.StatusOK, map[string]any{
		"enabled":  true,
		"software": a.Proxy.Software,
		"https":    a.HTTPSTarget(),
		"http":     a.HTTPTarget(),
		"hosts":    a.NeededHosts(),
		"snippets": stack.ProxySnippets(a),
	})
}
