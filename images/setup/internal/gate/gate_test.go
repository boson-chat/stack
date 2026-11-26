package gate

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newGate() (*Gate, *clock) {
	g := NewWithCode("ABCD-EFGH")
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	g.now = c.now
	return g, c
}

func TestSingleUse(t *testing.T) {
	g, _ := newGate()
	tok, err := g.Redeem("1.1.1.1", "abcd efgh")
	if err != nil || !g.Valid(tok) {
		t.Fatalf("redeem: %v", err)
	}
	if _, err := g.Redeem("2.2.2.2", "ABCD-EFGH"); !errors.Is(err, ErrUsed) {
		t.Fatalf("second redeem: %v", err)
	}
	if g.Valid("nope") || g.Valid("") {
		t.Fatal("random token valid")
	}
	g.Close()
	if g.Valid(tok) {
		t.Fatal("session survives Close")
	}
	if _, err := g.Redeem("1.1.1.1", "ABCD-EFGH"); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestPerIPRateLimit(t *testing.T) {
	g, c := newGate()
	for i := 0; i < PerIPFailures; i++ {
		if _, err := g.Redeem("1.1.1.1", "WRONG-CODE"); !errors.Is(err, ErrBadCode) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Even the right code is refused while limited.
	if _, err := g.Redeem("1.1.1.1", "ABCD-EFGH"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("limited: %v", err)
	}
	if g.RetryAfter("1.1.1.1") <= 0 {
		t.Fatal("no retry-after")
	}
	// Another IP is not limited.
	if _, err := g.Redeem("2.2.2.2", "WRONG-CODE"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("other ip: %v", err)
	}
	c.t = c.t.Add(PerIPWindow + time.Second)
	if _, err := g.Redeem("1.1.1.1", "ABCD-EFGH"); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestSessionExpires(t *testing.T) {
	g, c := newGate()
	tok, _ := g.Redeem("1.1.1.1", "ABCD-EFGH")
	c.t = c.t.Add(SessionTTL + time.Minute)
	if g.Valid(tok) {
		t.Fatal("expired session valid")
	}
}

func TestGlobalRateLimit(t *testing.T) {
	g, _ := newGate()
	for i := 0; i < GlobalFailures; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/250, i%250)
		if _, err := g.Redeem(ip, "WRONG-CODE"); !errors.Is(err, ErrBadCode) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := g.Redeem("9.9.9.9", "ABCD-EFGH"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("global limit: %v", err)
	}
}
