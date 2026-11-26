// Package addon is the "Add Boson to my existing IRC server" flow: accounts,
// voice, the web client and the admin panel next to an IRC server you
// already run (README: e.g. COMPOSE_PROFILES=voice,auth,metrics). It's a
// stub: Info().Available is false, so the first screen shows it as
// "Coming soon", and its API only answers GET /api/flows/addon/status.
//
// Wiring it (a later step) uses the IRC server probe, the module
// github.com/boson-chat/stack/pkg/probe (stdlib only; in images/setup/go.mod:
// require it and `replace github.com/boson-chat/stack/pkg/probe => ../../pkg/probe`,
// and COPY pkg/probe into the Dockerfile's build stage, whose context is the
// repo root already). probe.Run(ctx, probe.Options{...}) returns a
// *probe.Report whose capability table (native / via-bridge / unavailable)
// decides the plan. The fixtures to test against are test/ircds
// (run.sh up|env|apply|reset|smoke|down PAIRING).
//
// Where it plugs in: Info().Available turns the card on; Register adds the
// flow's API under /api/flows/addon/ (probe, plan, install); the pages are
// web/flows/addon.js (mount(ctx)). The install can reuse install.Runner and
// stack.Plan (a profile set without irc), as flow/fresh does.
package addon

import (
	"net/http"

	"github.com/boson-chat/stack/images/setup/internal/flow"
)

type Flow struct{}

func (Flow) Info() flow.Info {
	return flow.Info{
		ID:        "addon",
		Title:     "Add Boson to my existing IRC server",
		Summary:   "Keep your IRC server and add Boson's accounts, voice, web client and admin panel next to it.",
		Available: false,
	}
}

func (Flow) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"status", func(w http.ResponseWriter, _ *http.Request) {
		flow.JSON(w, http.StatusOK, map[string]any{
			"available": false,
			"message":   "This path isn't ready yet. It will look at your IRC server first and set Boson up around it.",
		})
	})
}
