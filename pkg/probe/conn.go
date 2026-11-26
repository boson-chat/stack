package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// errClosed is returned by next once the server closed the connection.
var errClosed = errors.New("connection closed by the server")

// conn is a minimal IRC client connection: a reader goroutine feeds parsed
// lines into a channel, PINGs are answered, and every line is kept for the
// transcript.
type conn struct {
	nc    net.Conn
	lines chan message
	errc  chan error
	done  chan struct{}

	mu         sync.Mutex
	transcript []string
	maxLines   int
	closeOnce  sync.Once
}

func dial(ctx context.Context, o *Options) (*conn, error) {
	d := o.Dialer
	if d == nil {
		nd := &net.Dialer{Timeout: o.stepTimeout()}
		d = nd.DialContext
	}
	nc, err := d(ctx, "tcp", o.Addr)
	if err != nil {
		return nil, err
	}
	if o.TLS {
		host := o.ServerName
		if host == "" {
			host, _, _ = net.SplitHostPort(o.Addr)
		}
		tc := tls.Client(nc, &tls.Config{ServerName: host, InsecureSkipVerify: o.InsecureSkipVerify, MinVersion: tls.VersionTLS12}) //nolint:gosec // opt-in for self-signed test servers
		hctx, cancel := context.WithTimeout(ctx, o.stepTimeout())
		defer cancel()
		if err := tc.HandshakeContext(hctx); err != nil {
			defer nc.Close()
			// A server that refuses at accept (ngIRCd's per-IP limit) writes
			// a plaintext ERROR instead of a TLS record.
			var rh tls.RecordHeaderError
			if errors.As(err, &rh) && rh.Conn != nil {
				_ = rh.Conn.SetReadDeadline(time.Now().Add(time.Second))
				buf := make([]byte, 512)
				n, _ := io.ReadFull(rh.Conn, buf)
				text := strings.TrimSpace(string(rh.RecordHeader[:]) + string(buf[:n]))
				// Only the record header's 5 bytes survive crypto/tls's
				// buffering when the rest arrived in the same read.
				if line, _, _ := strings.Cut(text, "\n"); strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, ":") {
					line = strings.TrimSpace(line)
					if line == "ERROR" {
						line = "ERROR (plaintext, before the TLS handshake)"
					}
					return nil, &plaintextRefusal{line: line}
				}
			}
			return nil, fmt.Errorf("TLS handshake: %w", err)
		}
		nc = tc
	}
	c := &conn{nc: nc, lines: make(chan message, 256), errc: make(chan error, 1), done: make(chan struct{}), maxLines: 2000}
	go c.read()
	go func() {
		select {
		case <-ctx.Done():
			c.close()
		case <-c.done:
		}
	}()
	return c, nil
}

func (c *conn) read() {
	r := bufio.NewReaderSize(c.nc, 16384)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			c.record("<< " + line)
			m := parseMessage(line)
			if m.Command == "PING" {
				c.send("PONG", m.Params...)
				continue
			}
			select {
			case c.lines <- m:
			case <-c.done:
				c.errc <- errClosed
				close(c.lines)
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errClosed
			}
			c.errc <- err
			close(c.lines)
			return
		}
	}
}

func (c *conn) record(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.transcript) < c.maxLines {
		c.transcript = append(c.transcript, s)
	}
}

// send writes one command; the last parameter gets a ':' when it needs one.
func (c *conn) send(cmd string, params ...string) {
	var b strings.Builder
	b.WriteString(cmd)
	for i, p := range params {
		b.WriteByte(' ')
		if i == len(params)-1 && (p == "" || strings.ContainsAny(p, " :") || strings.HasPrefix(p, ":")) {
			b.WriteByte(':')
		}
		b.WriteString(p)
	}
	c.sendRaw(b.String())
}

func (c *conn) sendRaw(line string) {
	rec := line
	if strings.HasPrefix(strings.ToUpper(line), "WEBIRC ") || strings.HasPrefix(strings.ToUpper(line), "AUTHENTICATE ") || strings.HasPrefix(strings.ToUpper(line), "PASS ") {
		rec = strings.SplitN(line, " ", 2)[0] + " <redacted>"
	}
	c.record(">> " + rec)
	_ = c.nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(c.nc, line+"\r\n")
}

// next returns the next line, or an error on timeout or disconnect.
func (c *conn) next(timeout time.Duration) (message, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case m, ok := <-c.lines:
		if !ok {
			err := <-c.errc
			c.errc <- err
			return message{}, err
		}
		return m, nil
	case <-t.C:
		return message{}, errTimeout
	}
}

var errTimeout = errors.New("timed out waiting for the server")

// plaintextRefusal is a server's plaintext ERROR on a TLS port.
type plaintextRefusal struct{ line string }

func (p *plaintextRefusal) Error() string {
	return "the server refused the connection before TLS: " + p.line
}

// until reads lines until fn returns true (stop) or the timeout expires.
// Every line goes to fn, including the one that stops it.
func (c *conn) until(timeout time.Duration, fn func(message) bool) error {
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return errTimeout
		}
		m, err := c.next(left)
		if err != nil {
			return err
		}
		if fn(m) {
			return nil
		}
	}
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.nc.Close()
	})
}

func (c *conn) quit() {
	c.send("QUIT", "boson-probe done")
	_ = c.until(2*time.Second, func(m message) bool { return m.Command == "ERROR" })
	c.close()
}

func (c *conn) transcriptLines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.transcript...)
}
