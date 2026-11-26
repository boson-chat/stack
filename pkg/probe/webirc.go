package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// tryWebIRC opens a second connection, sends WEBIRC before registering, and
// decides whether the server took it: a refusal is an ERROR / disconnect
// before 001 (UnrealIRCd, InspIRCd) or 464 (ngIRCd, which then registers
// the client anyway); acceptance is a registration without those, verified
// when the claimed IP shows up in our own WHOIS.
func tryWebIRC(ctx context.Context, o *Options) WebIRCResult {
	res := WebIRCResult{Tested: true}
	w := o.WebIRC
	if w.Gateway == "" {
		w.Gateway = "boson-probe"
	}
	if w.IP == "" {
		w.IP = "192.0.2.44"
	}
	c, err := dial(ctx, o)
	if err != nil {
		res.Detail = "connect: " + err.Error()
		return res
	}
	defer c.close()
	nick := truncate("bweb"+randHex(3), 10)
	c.sendRaw(fmt.Sprintf("WEBIRC %s %s %s %s", w.Password, w.Gateway, w.IP, w.IP))
	c.send("NICK", nick)
	c.send("USER", "bosonprobe", "0", "*", "Boson setup probe (WEBIRC)")
	registered, refused := false, ""
	tries := 0
	err = c.until(3*o.stepTimeout(), func(m message) bool {
		switch m.Command {
		case "464":
			refused = "464 " + m.last()
		case "421":
			if strings.EqualFold(m.param(1), "WEBIRC") {
				refused = "421 unknown command WEBIRC"
			}
		case "433":
			tries++
			if tries > 5 {
				return true
			}
			nick = fmt.Sprintf("%s%d", truncate(nick, 8), tries)
			c.send("NICK", nick)
		case "FAIL":
			if strings.EqualFold(m.param(0), "WEBIRC") {
				refused = "FAIL " + strings.Join(m.Params, " ")
			}
		case "NOTICE":
			if strings.Contains(strings.ToUpper(m.last()), "WEBIRC") && !registered {
				low := strings.ToLower(m.last())
				if strings.Contains(low, "denied") || strings.Contains(low, "invalid") || strings.Contains(low, "not") {
					refused = "NOTICE " + m.last()
				}
			}
		case "ERROR":
			if refused == "" {
				refused = "ERROR " + m.last()
			}
			return true
		case "001":
			registered = true
			return true
		}
		return false
	})
	if !registered {
		switch {
		case refused != "":
			res.Detail = refused
		case errors.Is(err, errClosed):
			res.Detail = "the server closed the connection"
		case err != nil:
			res.Detail = "no registration: " + err.Error()
		default:
			res.Detail = "no registration"
		}
		return res
	}
	if refused != "" {
		res.Detail = refused
		c.quit()
		return res
	}
	// Registered without a refusal: look for the claimed IP in our WHOIS
	// (311 host, 338 actually-using, 378 connecting-from).
	c.send("WHOIS", nick)
	_ = c.until(o.stepTimeout(), lineFn(func(m message) bool {
		switch m.Command {
		case "311", "338", "378":
			for _, p := range m.Params[1:] {
				if strings.Contains(p, w.IP) {
					res.Verified = true
				}
			}
		}
		return false
	}).withPing(c, "webirc"))
	c.quit()
	res.Accepted = true
	if res.Verified {
		res.Detail = "accepted; WHOIS shows the claimed IP " + w.IP
	} else {
		res.Detail = "registered with no refusal; WHOIS doesn't show the claimed IP " + w.IP
	}
	return res
}
