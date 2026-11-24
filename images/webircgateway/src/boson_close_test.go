package webircgateway

import (
	"bufio"
	"fmt"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// closeTestEnded hears of each client's end, after the policy's own
// client.state hook (registered first) has read bosonPol.
var closeTestEnded = make(chan struct{}, 16)

func init() {
	HookRegister("client.state", func(h *HookClientState) {
		if !h.Connected {
			select {
			case closeTestEnded <- struct{}{}:
			default:
			}
		}
	})
}

// fakeNetwork is an IRC server that welcomes one client and records the
// lines it gets until the connection closes.
type fakeNetwork struct {
	ln     net.Listener
	lines  chan string
	closed chan time.Time
}

func newFakeNetwork(t *testing.T) *fakeNetwork {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeNetwork{ln: ln, lines: make(chan string, 1000), closed: make(chan time.Time, 1)}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				n.closed <- time.Now()
				return
			}
			line = strings.TrimRight(line, "\r\n")
			n.lines <- line
			if strings.HasPrefix(line, "USER ") {
				fmt.Fprintf(conn, ":irc.test 001 ghost :Welcome\r\n")
			}
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return n
}

func (n *fakeNetwork) port() int { return n.ln.Addr().(*net.TCPAddr).Port }

// closeGateway is a gateway (WebSocket transport) with a slow throttle: one
// line a second after registration, so lines a client sends pile up.
func closeGateway(t *testing.T, port int) *httptest.Server {
	t.Helper()
	old := bosonPol
	bosonPol = mustPolicy(t, map[string]string{
		"WEBIRC_ALLOW_PRIVATE":  "true",
		"WEBIRC_EXTRA_PORTS":    strconv.Itoa(port),
		"WEBIRC_MAX_PER_IP":     "0",
		"WEBIRC_RATE_PER_MIN":   "0",
		"WEBIRC_THROTTLE_BURST": "1",
	})
	g := NewGateway("gateway")
	g.Config.Gateway = true
	g.Config.GatewayTimeout = 5
	g.Config.GatewayThrottle = 1
	g.Config.GatewayProtocol = "tcp"
	g.Config.LogLevel = 3
	g.Config.SendQuitOnClientClose = "Web client closed"
	go func() {
		for range g.LogOutput {
		}
	}()
	// Put the policy back once this gateway's clients have all ended (their
	// hooks read it on the way out).
	t.Cleanup(func() {
		select {
		case <-closeTestEnded:
		case <-time.After(2 * time.Second):
			t.Error("the gateway's client never ended")
		}
		bosonPol = old
	})
	(&TransportWebsocket{}).Init(g)
	srv := httptest.NewServer(g.HttpRouter)
	t.Cleanup(srv.Close)
	return srv
}

// dialRegistered opens a WebSocket to the gateway, registers on the fake
// network and returns once the client has seen 001.
func dialRegistered(t *testing.T, srv *httptest.Server, port int) (*websocket.Conn, net.Conn) {
	t.Helper()
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(srv.URL, "http")+"/webirc/websocket/", "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ws, err := websocket.NewClient(cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{fmt.Sprintf("HOST 127.0.0.1:%d", port), "NICK ghost", "USER u 0 * :u"} {
		if err := websocket.Message.Send(ws, l); err != nil {
			t.Fatal(err)
		}
	}
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var m string
		if err := websocket.Message.Receive(ws, &m); err != nil {
			t.Fatalf("waiting for 001: %v", err)
		}
		if strings.Contains(m, " 001 ") {
			return ws, raw
		}
	}
}

// waitClosed collects the network's lines until it sees the connection
// close, failing if that takes longer than within.
func (n *fakeNetwork) waitClosed(t *testing.T, since time.Time, within time.Duration) []string {
	t.Helper()
	var got []string
	timeout := time.After(within)
	for {
		select {
		case l := <-n.lines:
			got = append(got, l)
		case at := <-n.closed:
			for {
				select {
				case l := <-n.lines:
					got = append(got, l)
				default:
					t.Logf("network connection closed %v after the client's", at.Sub(since).Round(time.Millisecond))
					return got
				}
			}
		case <-timeout:
			t.Fatalf("network connection still open %v after the client closed; lines %q", within, got)
		}
	}
}

func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

func TestQuitOnClientClose(t *testing.T) {
	cases := []struct {
		name string
		// what the client sends after registering, before it goes
		send []string
		// true: drop the TCP connection without a close frame
		abrupt   bool
		wantQuit string
	}{
		{name: "idle client, close frame", wantQuit: "QUIT :Web client closed"},
		{name: "idle client, dropped socket", abrupt: true, wantQuit: "QUIT :Web client closed"},
		{name: "busy client: lines queued behind the throttle", send: repeat("PRIVMSG #c :hi", 30), wantQuit: "QUIT :Web client closed"},
		{name: "client's own QUIT behind the queue", send: append(repeat("PRIVMSG #c :hi", 30), "QUIT :reloading"), wantQuit: "QUIT :reloading"},
		// Sent at once or on close ("QUIT :"), but only once.
		{name: "bare QUIT", send: []string{"QUIT"}, abrupt: true, wantQuit: "QUIT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := newFakeNetwork(t)
			srv := closeGateway(t, n.port())
			ws, raw := dialRegistered(t, srv, n.port())
			for _, l := range c.send {
				if err := websocket.Message.Send(ws, l); err != nil {
					t.Fatal(err)
				}
			}
			closedAt := time.Now()
			if c.abrupt {
				raw.Close()
			} else {
				ws.Close()
			}
			got := n.waitClosed(t, closedAt, time.Second)
			if last := strings.TrimSuffix(lastLine(got), " :"); last != c.wantQuit {
				t.Fatalf("last line to the network %q, want %q (all: %q)", last, c.wantQuit, got)
			}
			quits := 0
			for _, l := range got {
				if strings.HasPrefix(l, "QUIT") {
					quits++
				}
			}
			if quits != 1 {
				t.Fatalf("%d QUITs sent, want 1: %q", quits, got)
			}
		})
	}
}

func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestQuitLine(t *testing.T) {
	g := NewGateway("gateway")
	for _, c := range []struct{ noted, conf, want string }{
		{"", "Web client closed", "QUIT :Web client closed"},
		{"", "", ""},
		{"QUIT :bye now", "Web client closed", "QUIT :bye now"},
		{"quit", "x", "quit :"},
		{"QUITTER :no", "x", "QUIT :x"},
		{"PRIVMSG #c :QUIT", "x", "QUIT :x"},
	} {
		g.Config.SendQuitOnClientClose = c.conf
		cl := &Client{Gateway: g, boson: newBosonClose()}
		if c.noted != "" {
			bosonNoteLine(cl, c.noted)
		}
		if got := bosonQuitLine(cl); got != c.want {
			t.Errorf("noted %q conf %q: got %q, want %q", c.noted, c.conf, got, c.want)
		}
	}
	cl := &Client{Gateway: g, boson: newBosonClose()}
	bosonClientGone(cl)
	bosonClientGone(cl) // twice: no panic
	select {
	case <-cl.boson.gone:
	default:
		t.Fatal("gone not closed")
	}
}
