package roleserv

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// Session is what the bot needs to log in and stay opered: Ergo's API
// (*ergoapi.Client).
type Session interface {
	EnsureAccount(ctx context.Context, account, passphrase string) error
	ApplyGrants(ctx context.Context, account string, g roles.Grant) (int, error)
}

// Bot connects RoleServ to Ergo.
type Bot struct {
	Addr    string // e.g. ergo:6680 (the services listener, plaintext)
	Nick    string // RoleServ
	Class   string // oper class it grants itself (nofakelag)
	Session Session
	Service *Service
	Secret  func() string
	Logf    func(string, ...any)
	// MaxBackoff between reconnects.
	MaxBackoff time.Duration
	// OnLogin, if set, is called (in its own goroutine) with the account
	// of every client that logs in: RoleServ subscribes to Ergo's ACCOUNT
	// snomask (+s u, needs the snomasks capability in its oper class).
	OnLogin func(ctx context.Context, account string)
}

var (
	ircFormatting = regexp.MustCompile("\x03(\\d{1,2}(,\\d{1,2})?)?|[\x02\x0f\x11\x16\x1d\x1e\x1f]")
	// Ergo's login snomask: "-ACCOUNT- Client [nick!user@host] logged into account [name]".
	loginNotice = regexp.MustCompile(`^-ACCOUNT- Client \[[^\]]*\] logged into account \[([^\]\s]+)\]$`)
)

// LoginAccount returns the account of an Ergo login snomask notice.
func LoginAccount(text string) (string, bool) {
	m := loginNotice.FindStringSubmatch(strings.TrimSpace(ircFormatting.ReplaceAllString(text, "")))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Run connects and serves until ctx ends, reconnecting with backoff.
func (b *Bot) Run(ctx context.Context) {
	backoff := time.Second
	maxBackoff := b.MaxBackoff
	if maxBackoff == 0 {
		maxBackoff = time.Minute
	}
	for ctx.Err() == nil {
		start := time.Now()
		err := b.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 2*time.Minute {
			backoff = time.Second
		}
		b.Logf("disconnected: %v; reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, maxBackoff)
	}
}

type conn struct {
	c  net.Conn
	mu sync.Mutex
}

func (c *conn) send(line string) error {
	line = strings.NewReplacer("\r", "", "\n", " ").Replace(line)
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := c.c.Write([]byte(line + "\r\n"))
	return err
}

func (b *Bot) runOnce(ctx context.Context) error {
	// A fresh password every connect: the account is local to Ergo and only
	// this process ever needs to know it.
	pass := b.Secret()
	actx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err := b.Session.EnsureAccount(actx, b.Nick, pass)
	cancel()
	if err != nil {
		return fmt.Errorf("setting up the %s account through the Ergo API: %w", b.Nick, err)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", b.Addr)
	if err != nil {
		return err
	}
	defer nc.Close()
	c := &conn{c: nc}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()

	r := bufio.NewReaderSize(nc, 16<<10)
	read := func() (Message, error) {
		_ = nc.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := r.ReadString('\n')
		if err != nil {
			return Message{}, err
		}
		return Parse(strings.TrimRight(line, "\r\n")), nil
	}

	for _, l := range []string{"CAP LS 302", "NICK " + b.Nick, "USER roleserv 0 * :Boson network roles"} {
		if err := c.send(l); err != nil {
			return err
		}
	}
	registered := false
	for {
		m, err := read()
		if err != nil {
			return err
		}
		switch m.Command {
		case "PING":
			_ = c.send("PONG :" + m.Param(0))
		case "CAP":
			switch strings.ToUpper(m.Param(1)) {
			case "LS":
				if m.Param(2) == "*" { // multi-line LS
					continue
				}
				_ = c.send("CAP REQ :sasl account-tag message-tags")
			case "ACK":
				_ = c.send("AUTHENTICATE PLAIN")
			case "NAK":
				return errors.New("server refused sasl/account-tag/message-tags")
			}
		case "AUTHENTICATE":
			if m.Param(0) == "+" {
				blob := base64.StdEncoding.EncodeToString([]byte(b.Nick + "\x00" + b.Nick + "\x00" + pass))
				_ = c.send("AUTHENTICATE " + blob)
			}
		case "903":
			_ = c.send("CAP END")
		case "902", "904", "905", "906", "908":
			return fmt.Errorf("SASL login as %s failed: %s", b.Nick, m.Raw)
		case "433", "432":
			return fmt.Errorf("nick %s unavailable: %s", b.Nick, m.Raw)
		case "001":
			registered = true
			_ = c.send("MODE " + m.Param(0) + " +B")
			b.Logf("connected to %s as %s", b.Addr, m.Param(0))
			if b.Class != "" {
				gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				class := b.Class
				if _, err := b.Session.ApplyGrants(gctx, b.Nick, roles.Grant{OperClass: &class, WhoisLine: "is a network service"}); err != nil {
					b.Logf("granting %s the %s oper class (no fakelag): %v", b.Nick, b.Class, err)
				} else if b.OnLogin != nil {
					_ = c.send("MODE " + m.Param(0) + " +s u")
				}
				cancel()
			}
		case "ERROR":
			return fmt.Errorf("server: %s", m.Param(0))
		case "NOTICE":
			// Server notices have a server name (no "!") as their source.
			if b.OnLogin == nil || !registered || strings.Contains(m.Source, "!") || !strings.EqualFold(m.Param(0), b.Nick) {
				continue
			}
			if account, ok := LoginAccount(m.Param(1)); ok && !strings.EqualFold(account, b.Nick) {
				go b.OnLogin(ctx, account)
			}
		case "PRIVMSG":
			if !registered || !strings.EqualFold(m.Param(0), b.Nick) {
				continue
			}
			text := m.Param(1)
			if strings.HasPrefix(text, "\x01") { // CTCP
				continue
			}
			nick := m.Nick()
			if nick == "" || strings.EqualFold(nick, b.Nick) {
				continue
			}
			// The account Ergo attached (account-tag). Clients can't set
			// it: only "+"-prefixed tags are client tags.
			account := m.Tags["account"]
			if account == "*" {
				account = ""
			}
			go b.answer(ctx, c, nick, account, text)
		}
	}
}

func (b *Bot) answer(ctx context.Context, c *conn, nick, account, text string) {
	hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, line := range b.Service.Handle(hctx, account, text) {
		if err := c.send("NOTICE " + nick + " :" + line); err != nil {
			return
		}
	}
}

// Message is a parsed IRC line.
type Message struct {
	Raw     string
	Tags    map[string]string
	Source  string
	Command string
	Params  []string
}

func (m Message) Param(i int) string {
	if i < len(m.Params) {
		return m.Params[i]
	}
	return ""
}

// Nick is the nick part of the source.
func (m Message) Nick() string {
	n, _, _ := strings.Cut(m.Source, "!")
	return n
}

var tagUnescape = strings.NewReplacer(`\:`, ";", `\s`, " ", `\\`, `\`, `\r`, "\r", `\n`, "\n")

// Parse parses one IRC line (IRCv3 tags included).
func Parse(line string) Message {
	m := Message{Raw: line, Tags: map[string]string{}}
	if strings.HasPrefix(line, "@") {
		var tags string
		tags, line, _ = strings.Cut(line[1:], " ")
		for _, t := range strings.Split(tags, ";") {
			k, v, _ := strings.Cut(t, "=")
			if k != "" {
				m.Tags[k] = tagUnescape.Replace(v)
			}
		}
		line = strings.TrimLeft(line, " ")
	}
	if strings.HasPrefix(line, ":") {
		m.Source, line, _ = strings.Cut(line[1:], " ")
		line = strings.TrimLeft(line, " ")
	}
	m.Command, line, _ = strings.Cut(line, " ")
	m.Command = strings.ToUpper(m.Command)
	for line != "" {
		line = strings.TrimLeft(line, " ")
		if strings.HasPrefix(line, ":") {
			m.Params = append(m.Params, line[1:])
			break
		}
		var p string
		p, line, _ = strings.Cut(line, " ")
		if p != "" {
			m.Params = append(m.Params, p)
		}
	}
	return m
}
