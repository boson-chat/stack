package install

import "sync"

// Event goes to the wizard's progress stream.
type Event struct {
	// state | log | services | warnings
	Type     string          `json:"type"`
	Step     string          `json:"step,omitempty"`
	Line     string          `json:"line,omitempty"`
	State    *State          `json:"state,omitempty"`
	Services []ServiceStatus `json:"services,omitempty"`
	// Warnings: the current ones (none = cleared).
	Warnings []Warning `json:"warnings,omitempty"`
}

// Hub fans events out to subscribers and keeps recent log lines, so a page
// that (re)connects sees what happened.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
	logs []Event
	last map[string]Event
}

func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}, last: map[string]Event{}} }

func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e.Type == "log" {
		h.logs = append(h.logs, e)
		if len(h.logs) > 400 {
			h.logs = h.logs[len(h.logs)-400:]
		}
	} else {
		h.last[e.Type] = e
	}
	for c := range h.subs {
		select {
		case c <- e:
		default: // a slow page misses lines, not the install
		}
	}
}

// Subscribe returns a channel with the backlog first.
func (h *Hub) Subscribe() (chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan Event, 1024)
	for _, e := range h.logs {
		c <- e
	}
	for _, t := range []string{"services", "warnings", "state"} {
		if e, ok := h.last[t]; ok {
			c <- e
		}
	}
	h.subs[c] = struct{}{}
	return c, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[c]; ok {
			delete(h.subs, c)
			close(c)
		}
	}
}

// ClearLogs starts a fresh log for a new attempt.
func (h *Hub) ClearLogs() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logs = nil
	delete(h.last, "warnings")
}
