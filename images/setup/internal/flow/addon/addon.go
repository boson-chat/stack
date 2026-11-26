// Package addon is the "Add Boson to my existing IRC server" flow: accounts,
// voice, the web client and the admin panel next to an IRC server you
// already run (README: e.g. COMPOSE_PROFILES=voice,auth,metrics). It's a
// stub: the next step wires in the IRC server probe (images/setup/probe,
// branch feat/setup-probe) that reads the server's software, TLS, SASL and
// services, and builds the plan from what it finds.
//
// Where it plugs in: Info().Available turns the first-screen card on;
// Register adds /api/flows/addon/* (probe, plan, install). The install can
// reuse install.Runner with its own steps and stack.Plan (without the irc
// profile), the same way flow/fresh does.
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
