// Package gate guards the wizard: the one-time setup code printed by
// install.sh is redeemed once for a browser session, and every API call
// needs that session. Wrong codes are rate limited per IP and overall.
package gate

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/secret"
)

var (
	ErrBadCode     = errors.New("that code is not right")
	ErrUsed        = errors.New("this code was already used in another browser; run install.sh again for a new one")
	ErrClosed      = errors.New("setup is finished and switched off; run install.sh again for a new code")
	ErrRateLimited = errors.New("too many wrong codes; wait a few minutes and try again")
)

// Limits for wrong codes. A code has ~39 bits; at these rates guessing it
// takes longer than the universe has.
const (
	PerIPFailures  = 5
	PerIPWindow    = 5 * time.Minute
	GlobalFailures = 30
	GlobalWindow   = 10 * time.Minute
	SessionTTL     = 24 * time.Hour
)

type Gate struct {
	mu       sync.Mutex
	code     string // normalized
	redeemed bool
	closed   bool
	sessions map[[32]byte]time.Time
	ipFails  map[string][]time.Time
	allFails []time.Time
	now      func() time.Time
}

// New makes a gate with a fresh random code.
func New() *Gate { return NewWithCode(secret.Code()) }

// NewWithCode is for tests.
func NewWithCode(code string) *Gate {
	return &Gate{
		code:     secret.NormalizeCode(code),
		sessions: map[[32]byte]time.Time{},
		ipFails:  map[string][]time.Time{},
		now:      time.Now,
	}
}

// Code is the code to show (XXXX-XXXX).
func (g *Gate) Code() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.code[:4] + "-" + g.code[4:]
}

func prune(ts []time.Time, since time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(since) {
			out = append(out, t)
		}
	}
	return out
}

// RetryAfter: how long ip has to wait before another attempt (0 = may try).
func (g *Gate) RetryAfter(ip string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.retryAfterLocked(ip)
}

func (g *Gate) retryAfterLocked(ip string) time.Duration {
	now := g.now()
	g.ipFails[ip] = prune(g.ipFails[ip], now.Add(-PerIPWindow))
	g.allFails = prune(g.allFails, now.Add(-GlobalWindow))
	var wait time.Duration
	if f := g.ipFails[ip]; len(f) >= PerIPFailures {
		wait = f[len(f)-PerIPFailures].Add(PerIPWindow).Sub(now)
	}
	if f := g.allFails; len(f) >= GlobalFailures {
		if w := f[len(f)-GlobalFailures].Add(GlobalWindow).Sub(now); w > wait {
			wait = w
		}
	}
	if len(g.ipFails[ip]) == 0 {
		delete(g.ipFails, ip)
	}
	return wait
}

// Redeem trades the code for a session token, once.
func (g *Gate) Redeem(ip, code string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return "", ErrClosed
	}
	if g.retryAfterLocked(ip) > 0 {
		return "", ErrRateLimited
	}
	c := secret.NormalizeCode(code)
	if subtle.ConstantTimeCompare([]byte(c), []byte(g.code)) != 1 {
		now := g.now()
		g.ipFails[ip] = append(g.ipFails[ip], now)
		g.allFails = append(g.allFails, now)
		return "", ErrBadCode
	}
	if g.redeemed {
		return "", ErrUsed
	}
	g.redeemed = true
	tok := secret.Hex(32)
	g.sessions[sha256.Sum256([]byte(tok))] = g.now().Add(SessionTTL)
	return tok, nil
}

// Valid reports whether tok is a live session.
func (g *Gate) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	h := sha256.Sum256([]byte(tok))
	exp, ok := g.sessions[h]
	if !ok {
		return false
	}
	if g.now().After(exp) {
		delete(g.sessions, h)
		return false
	}
	return true
}

// Close ends every session and refuses the code from now on (setup is done).
func (g *Gate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.sessions = map[[32]byte]time.Time{}
}

// Closed reports whether Close was called.
func (g *Gate) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}
