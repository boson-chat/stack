package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/secret"
	"github.com/boson-chat/stack/images/setup/internal/stack"
)

// SendTestMail sends one test message with the SMTP settings, the way
// auth will. The password is only used for AUTH, never echoed.
func SendTestMail(ctx context.Context, s stack.SMTPAnswers, password, to, network string) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("the sender address doesn't parse: %v", err)
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("the recipient address doesn't parse: %v", err)
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	d := net.Dialer{}
	var conn net.Conn
	tcfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if s.TLS == "tls" {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tcfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("couldn't connect to %s: %v", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("%s doesn't talk SMTP: %v", addr, err)
	}
	defer c.Close()
	if err := c.Hello("boson-setup.invalid"); err != nil {
		return fmt.Errorf("EHLO: %v", err)
	}
	if s.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server doesn't offer STARTTLS; pick TLS or None")
		}
		if err := c.StartTLS(tcfg); err != nil {
			return fmt.Errorf("STARTTLS: %v", err)
		}
	}
	if s.User != "" {
		if s.TLS == "none" {
			return errors.New("won't send the SMTP password without TLS; pick STARTTLS or TLS")
		}
		if err := c.Auth(smtp.PlainAuth("", s.User, password, s.Host)); err != nil {
			return fmt.Errorf("login refused: %v", scrub(err.Error(), password))
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("MAIL FROM refused: %v", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("recipient refused: %v", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %v", err)
	}
	host := from.Address[strings.LastIndex(from.Address, "@")+1:]
	msg := strings.Join([]string{
		"From: " + from.String(),
		"To: " + to,
		"Subject: " + network + ": test email from Boson setup",
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: <" + secret.Hex(12) + "@" + host + ">",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"This is a test from the Boson setup wizard. Your server can send email:",
		"sign-up verification and password resets will work.",
		"",
	}, "\r\n")
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the server didn't accept the message: %v", err)
	}
	return c.Quit()
}

func scrub(s, pw string) string {
	if pw != "" {
		s = strings.ReplaceAll(s, pw, "[redacted]")
	}
	return s
}
