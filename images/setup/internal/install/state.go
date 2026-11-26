// Package install runs the install: write the config, pull, start, wait
// for health and certificates, create the admin, verify. Each step is
// idempotent; progress is saved in <install dir>/.setup/state.json so a
// failed or interrupted install can be retried from the wizard.
package install

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

type StepState struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Hint: what to do about the error.
	Hint     string    `json:"hint,omitempty"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
}

// State is saved after every change. It holds no secrets.
type State struct {
	Version int    `json:"version"`
	Flow    string `json:"flow"`
	// idle | running | failed | done
	Status       string        `json:"status"`
	Answers      stack.Answers `json:"answers"`
	AdminCreated bool          `json:"admin_created"`
	Steps        []StepState   `json:"steps"`
	Attempts     int           `json:"attempts"`
	StartedAt    time.Time     `json:"started_at,omitzero"`
	CompletedAt  time.Time     `json:"completed_at,omitzero"`
	// Finished: the wizard was closed after a successful install.
	Finished bool `json:"finished"`
}

func (s *State) Step(id string) *StepState {
	for i := range s.Steps {
		if s.Steps[i].ID == id {
			return &s.Steps[i]
		}
	}
	return nil
}

// FailedStep: the step that failed last, if any.
func (s *State) FailedStep() *StepState {
	for i := range s.Steps {
		if s.Steps[i].Status == StatusFailed {
			return &s.Steps[i]
		}
	}
	return nil
}

// Store keeps the state file (0600, in a 0700 directory).
type Store struct{ Path string }

func NewStore(installDir string) *Store {
	return &Store{Path: filepath.Join(installDir, ".setup", "state.json")}
}

func (st *Store) Load() (State, bool, error) {
	var s State
	b, err := os.ReadFile(st.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Version: 1, Status: "idle"}, false, nil
	}
	if err != nil {
		return s, false, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return State{Version: 1, Status: "idle"}, false, nil
	}
	if s.Status == StatusRunning {
		// The wizard stopped mid-install (container restarted).
		s.Status = StatusFailed
		for i := range s.Steps {
			if s.Steps[i].Status == StatusRunning {
				s.Steps[i].Status = StatusFailed
				s.Steps[i].Error = "Setup stopped while this step was running."
				s.Steps[i].Hint = "Retry: every step is safe to run again."
			}
		}
	}
	return s, true, nil
}

func (st *Store) Save(s State) error {
	if err := os.MkdirAll(filepath.Dir(st.Path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(st.Path), 0o700)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return stack.WriteFileAtomic(st.Path, append(b, '\n'), 0o600)
}
