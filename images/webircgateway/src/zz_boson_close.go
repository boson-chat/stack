package webircgateway

// Boson: leave the network as soon as the client's connection ends. A
// closed or reloaded tab must not leave its IRC connection (and nick) behind
// on the network: on a reload the new connection would get nick_.
//
// Upstream webircgateway only sent its QUIT when the client was already
// shutting down (never, on a client close), and kept forwarding lines queued
// behind the throttle until they drained (seconds for a busy client) before
// closing the network connection. Here the transport reports the close at
// once (bosonClientGone), and the client's worker, which owns the network
// connection, sends one QUIT and closes it (bosonQuitUpstream):
//
//   - the client's own QUIT, if it sent one that is still queued, so its
//     quit message is kept; else send_quit_on_client_close (the entrypoint
//     sets "Web client closed");
//   - lines still queued behind the throttle are dropped (the client is gone,
//     nothing will read the replies).

import (
	"strings"
	"sync"
)

type bosonClose struct {
	gone     chan struct{}
	goneOnce *sync.Once
	mu       *sync.Mutex
	quit     string // the client's last QUIT line, if any
}

func newBosonClose() bosonClose {
	return bosonClose{gone: make(chan struct{}), goneOnce: &sync.Once{}, mu: &sync.Mutex{}}
}

// bosonClientGone: the client's connection has ended (any close: a close
// frame, a dropped socket, a read error). Safe to call more than once.
func bosonClientGone(c *Client) {
	c.boson.goneOnce.Do(func() { close(c.boson.gone) })
}

// bosonNoteLine records a QUIT from the client as it arrives (before the
// throttle), so it isn't lost if the client closes right after sending it.
func bosonNoteLine(c *Client, line string) {
	cmd := line
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		cmd = cmd[:i]
	}
	if strings.EqualFold(cmd, "QUIT") {
		c.boson.mu.Lock()
		c.boson.quit = line
		c.boson.mu.Unlock()
	}
}

// bosonQuitLine is the QUIT to send for a client that has gone: its own, or
// the configured one ("" for none).
func bosonQuitLine(c *Client) string {
	c.boson.mu.Lock()
	quit := c.boson.quit
	c.boson.mu.Unlock()
	if quit != "" {
		if strings.IndexByte(quit, ' ') < 0 {
			quit += " :" // "QUIT" alone: processLineToUpstream only notices "QUIT "
		}
		return quit
	}
	if msg := c.Gateway.Config.SendQuitOnClientClose; msg != "" {
		return "QUIT :" + msg
	}
	return ""
}

// bosonQuitUpstream runs on the client's worker goroutine (the one that
// writes to the network): QUIT unless one was already sent, shut down, close
// the network connection, and let the throttle's queue drain into nothing.
func (c *Client) bosonQuitUpstream() {
	if up := c.upstream; up != nil && !c.SeenQuit {
		if line := bosonQuitLine(c); line != "" {
			c.processLineToUpstream(line)
		}
	}
	c.StartShutdown("client_closed")
	if up := c.upstream; up != nil {
		up.Close()
	}
	// The throttle's goroutine still holds queued lines; nothing reads them
	// now, so drain them or it blocks forever on a full queue.
	go func() {
		for range c.ThrottledRecv.Output {
		}
	}()
}
