package install

// Behind a reverse proxy (docs/reverse-proxy.md): the public checks go
// through it (Edge:443 is the proxy now), Boson's own Traefik is checked on
// its published address.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

const snippetsAt = "the snippets on the Firewall and ports step (docs/reverse-proxy.md)"

func proxyCertHint(a stack.Answers) string {
	return fmt.Sprintf("Let's Encrypt reaches Boson through your reverse proxy on port 80: it must forward http:// for each of these names to %s (%s). Check that, DNS, and that 80/tcp is open, then retry.",
		a.HTTPTarget(), snippetsAt)
}

// checkProxyPath: the reverse proxy on 443 passes Boson's names through to
// Boson's Traefik (the same certificate on both). Retries until deadline:
// a proxy that was just reconfigured may take a moment.
func (r *Runner) checkProxyPath(ctx context.Context, p stack.Plan, deadline time.Time, log func(string)) error {
	a := p.Answers
	host := a.HostNames()[stack.RoleAuth]
	public := r.publicHTTPS()
	for {
		err := r.compareProxyCert(ctx, public, r.traefikAddr(p), host)
		if err == nil {
			log(fmt.Sprintf("your reverse proxy on %s passes %s through to Boson (%s)", public, host, a.HTTPSTarget()))
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return &StepError{Msg: fmt.Sprintf("https://%s through your reverse proxy (%s): %s", host, public, err),
				Hint: fmt.Sprintf("Boson answers on %s, but your reverse proxy doesn't pass its names through to it yet. Add %s to your proxy, reload it, then retry.", a.HTTPSTarget(), snippetsAt)}
		}
		time.Sleep(2 * time.Second)
	}
}

// publicHTTPS: where visitors' HTTPS arrives on this server (the proxy).
func (r *Runner) publicHTTPS() string {
	if r.edgeHTTPS != "" {
		return r.edgeHTTPS
	}
	return net.JoinHostPort(r.Edge, "443")
}

func (r *Runner) compareProxyCert(ctx context.Context, public, direct, host string) error {
	want, err := r.edgeCert(ctx, direct, host)
	if err != nil {
		return fmt.Errorf("Boson's Traefik on %s: %w", direct, err)
	}
	got, err := r.edgeCert(ctx, public, host)
	if err != nil {
		return fmt.Errorf("nothing passes TLS through on %s: %w", public, err)
	}
	if !bytes.Equal(got.Raw, want.Raw) {
		return fmt.Errorf("it answers with another certificate (%s, from %s): it terminates TLS for this name itself, or sends it elsewhere, instead of passing it through", got.Subject.CommonName, issuerName(got))
	}
	return nil
}
