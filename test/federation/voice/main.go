// Command fedvoice is the federation e2e's voice check (test/federation/run.sh):
// two quicvoice SDK clients join one room on the remote's quicvoiced over
// QUIC, each sends synthetic 20 ms audio datagrams, and each must receive
// the other's. One holds alice's voice token (a federated user of the
// remote), the other bob's (a local user). Verified TLS: the server name is
// voice.<remote> (run.sh points it at quicvoiced with --add-host), trusting
// Pebble's root through SSL_CERT_FILE.
//
//	fedvoice -addr voice.remote.test:7700 -room general   # tokens in VOICE_A, VOICE_B
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/boson-chat/quicvoice/client"
	"github.com/boson-chat/quicvoice/wire"
)

type peer struct {
	name  string
	c     *client.Client
	sid   uint16
	mu    sync.Mutex
	froms map[uint16]int // downlink frames per sender session
}

func (p *peer) from(sid uint16) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.froms[sid]
}

var failed bool

func check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("PASS %s%s\n", name, sep(detail))
	} else {
		failed = true
		fmt.Printf("FAIL %s%s\n", name, sep(detail))
	}
}

func sep(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
}

func join(ctx context.Context, addr, room, name, token string) (*peer, error) {
	p := &peer{name: name, froms: map[uint16]int{}}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := client.Dial(dctx, client.Config{
		Addr:  addr,
		Token: token,
		OnAudio: func(d wire.Downlink) {
			p.mu.Lock()
			p.froms[d.SessionID]++
			p.mu.Unlock()
		},
	})
	if err != nil {
		return nil, err
	}
	p.c = c
	st, err := c.Join(dctx, room)
	if err != nil {
		c.Close()
		return nil, err
	}
	p.sid = uint16(st.GetYourSessionId())
	return p, nil
}

// talk sends n frames of synthetic audio, 20 ms apart.
func talk(p *peer, n int) {
	payload := make([]byte, 120)
	var seq uint16
	var ts uint32
	for i := range n {
		_, _ = rand.Read(payload)
		u := &wire.Uplink{Audio: wire.Audio{Voice: true, Level: 30, Seq: seq, Timestamp: ts, Payload: payload}}
		if i == 0 {
			u.Flags = wire.FlagStart
		}
		_ = p.c.SendAudio(u)
		seq++
		ts += 960
		time.Sleep(20 * time.Millisecond)
	}
}

func main() {
	addr := flag.String("addr", "", "voice server host:port")
	room := flag.String("room", "general", "room")
	want := flag.Int("want", 20, "frames each side must get from the other")
	flag.Parse()
	ta, tb := os.Getenv("VOICE_A"), os.Getenv("VOICE_B")
	if *addr == "" || ta == "" || tb == "" {
		fmt.Fprintln(os.Stderr, "usage: VOICE_A=<token> VOICE_B=<token> fedvoice -addr host:port")
		os.Exit(2)
	}
	ctx := context.Background()

	a, err := join(ctx, *addr, *room, "alice", ta)
	check("alice (federated) joins "+*room+" over QUIC with her remote voice token", err == nil, errText(err))
	b, err2 := join(ctx, *addr, *room, "bob", tb)
	check("bob (local) joins "+*room+" over QUIC", err2 == nil, errText(err2))
	if err != nil || err2 != nil {
		os.Exit(1)
	}
	defer a.c.Close()
	defer b.c.Close()

	// Both talk at once for 3 s (150 frames each; the server allows 60/s).
	var wg sync.WaitGroup
	for _, p := range []*peer{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); talk(p, 150) }()
	}
	wg.Wait()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) && (a.from(b.sid) < *want || b.from(a.sid) < *want) {
		time.Sleep(100 * time.Millisecond)
	}
	check("bob receives alice's audio datagrams", b.from(a.sid) >= *want,
		fmt.Sprintf("%d frames from session %d", b.from(a.sid), a.sid))
	check("alice receives bob's audio datagrams", a.from(b.sid) >= *want,
		fmt.Sprintf("%d frames from session %d", a.from(b.sid), b.sid))
	sa, sb := a.c.Stats(), b.c.Stats()
	fmt.Printf("stats: alice sent=%d recv=%d, bob sent=%d recv=%d\n", sa.Sent, sa.Received, sb.Sent, sb.Received)
	if failed {
		os.Exit(1)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
