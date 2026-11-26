// Package flow is the wizard's top level: each way of setting Boson up is
// a Flow with its own API under /api/flows/<id>/ and its own pages in
// web/flows/<id>.js. The first screen lists the flows.
//
// Two exist: "fresh" (a new Boson server on this machine) and "addon"
// (Boson next to an IRC server you already run; a stub until the IRC
// server probe lands). Adding a flow = a package that implements Flow, a
// line in main.go's registry, and web/flows/<id>.js.
package flow

import (
	"encoding/json"
	"net/http"
)

// Info is what the first screen shows for a flow.
type Info struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Available: false shows the flow as "coming soon".
	Available bool `json:"available"`
}

type Flow interface {
	Info() Info
	// Register adds the flow's handlers under prefix (/api/flows/<id>/).
	// The server has already checked the session for every request.
	Register(mux *http.ServeMux, prefix string)
}

// Registry keeps flows in display order.
type Registry struct{ flows []Flow }

func (r *Registry) Add(f Flow) { r.flows = append(r.flows, f) }

func (r *Registry) All() []Flow { return r.flows }

func (r *Registry) Infos() []Info {
	var out []Info
	for _, f := range r.flows {
		out = append(out, f.Info())
	}
	return out
}

// JSON writes v with status code.
func JSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": msg, "code": code}.
func Error(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, map[string]string{"error": msg, "code": code})
}

// Decode reads a JSON body (the server caps its size).
func Decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
