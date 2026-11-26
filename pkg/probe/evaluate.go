package probe

import (
	"strings"
)

// Evaluate turns a report into the capability table. It only reads the
// report, so callers can re-evaluate a saved report.
func Evaluate(r *Report) Table {
	t := Table{Schema: SchemaVersion, Software: r.Server.Software, Services: r.Services.Package}
	if t.Services == "" {
		t.Services = "none"
	}
	e := evaluator{r: r, sw: r.Server.Software}
	t.Features = []Capability{
		e.messageTags(),
		e.echoMessage(),
		e.serverTime(),
		e.chathistory(),
		e.readMarkers(),
		e.clientTags(),
		e.redaction(),
		e.sasl(),
		e.servicesRPC(),
		e.webirc(),
		e.multiline(),
	}
	for _, c := range t.Features {
		if c.Status == ViaBridge {
			t.NeedsBridge = true
		}
	}
	return t
}

type evaluator struct {
	r  *Report
	sw string
}

func (e evaluator) has(caps ...string) bool {
	for _, c := range caps {
		if _, ok := e.r.Caps[c]; ok {
			return true
		}
	}
	return false
}

// nativeOn lists, per feature, the servers that provide it natively
// (checked against UnrealIRCd 6.2.7.1, InspIRCd 4.12.1, ngIRCd 28, Ergo 2.19).
var nativeOn = map[string]string{
	FeatureMessageTags:      "Ergo, UnrealIRCd 6 and InspIRCd 4",
	FeatureEchoMessage:      "Ergo, UnrealIRCd 6 and InspIRCd 4",
	FeatureServerTime:       "Ergo, UnrealIRCd 6 and InspIRCd 4",
	FeatureClientTags:       "Ergo, UnrealIRCd 6 and InspIRCd 4",
	FeatureChatHistory:      "Ergo and UnrealIRCd 6",
	FeatureMultiline:        "Ergo and UnrealIRCd 6",
	FeatureReadMarkers:      "Ergo",
	FeatureMessageRedaction: "Ergo",
	FeatureSASL:             "Ergo, and UnrealIRCd 6 or InspIRCd 4 with Anope or Atheme",
	FeatureWebIRC:           "Ergo, UnrealIRCd 6, InspIRCd 4 and ngIRCd",
}

func provides(feature string) string {
	who := nativeOn[feature]
	if strings.Contains(who, " and ") || strings.Contains(who, " or ") {
		return who + " provide it natively."
	}
	return who + " provides it natively."
}

// upgrade returns software-specific advice for getting a capability natively.
func (e evaluator) upgrade(feature string) string {
	switch e.sw {
	case "inspircd":
		if m, ok := inspModules[feature]; ok {
			return m
		}
		return "InspIRCd 4 has no module for this; " + provides(feature)
	case "unrealircd":
		if m, ok := unrealModules[feature]; ok {
			return m
		}
		return "UnrealIRCd 6 has no module for this; " + provides(feature)
	case "ngircd":
		return "ngIRCd has no support for this; " + provides(feature)
	case "ergo":
		return "Upgrade Ergo and check that it isn't disabled in ircd.yaml."
	}
	return "Switch to a server that provides it natively: " + nativeOn[feature] + "."
}

var inspModules = map[string]string{
	FeatureMessageTags: `Load InspIRCd's ircv3_ctctags and ircv3_msgid modules (<module name="ircv3_ctctags">, <module name="ircv3_msgid">).`,
	FeatureEchoMessage: `Load InspIRCd's ircv3_echomessage module (<module name="ircv3_echomessage">).`,
	FeatureServerTime:  `Load InspIRCd's ircv3_servertime module (<module name="ircv3_servertime">).`,
	FeatureClientTags:  `Load InspIRCd's ircv3_ctctags module and leave <ctctags clientonlytags="all"> (the default).`,
	FeatureSASL:        `Load InspIRCd's sasl module with <sasl target="your.services.server"> (and services that do SASL).`,
	FeatureWebIRC:      `Load InspIRCd's gateway module and add <gateway type="webirc" mask="GATEWAY-IP" password="..."> (the wizard generates it).`,
}

var unrealModules = map[string]string{
	FeatureMessageTags: `UnrealIRCd 6 loads message-tags by default (modules.default.conf); check it isn't blacklisted.`,
	FeatureEchoMessage: `UnrealIRCd 6 loads echo-message by default; check it isn't blacklisted.`,
	FeatureServerTime:  `UnrealIRCd 6 loads server-time by default; check it isn't blacklisted.`,
	FeatureChatHistory: `Load UnrealIRCd's chathistory and history modules (both in modules.default.conf) and set channel mode +H on channels.`,
	FeatureClientTags:  `Allow +draft/reply and +draft/react: remove them from set::clienttagdeny (they're allowed by default).`,
	FeatureMultiline:   `Load UnrealIRCd's multiline module (modules.default.conf).`,
	FeatureSASL:        `UnrealIRCd 6 does SASL through services: set set::sasl-server (or services-server) to the services' name and run services that do SASL.`,
	FeatureWebIRC:      `Add a webirc { mask GATEWAY-IP; password "..."; } block (the wizard generates it).`,
}

func (e evaluator) messageTags() Capability {
	c := Capability{Feature: FeatureMessageTags, Title: "Message tags"}
	if e.has("message-tags") {
		c.Status, c.Explanation = Native, "The server offers message-tags: Boson gets message IDs, timestamps and client tags from it."
		return c
	}
	c.Status = ViaBridge
	c.Explanation = "The server has no message-tags. evrd offers message-tags to Boson and adds its own message IDs and timestamps; tags between users (replies, reactions) still need the server."
	c.Fix = e.upgrade(FeatureMessageTags)
	return c
}

func (e evaluator) echoMessage() Capability {
	c := Capability{Feature: FeatureEchoMessage, Title: "Echo of sent messages"}
	if e.has("echo-message") {
		c.Status, c.Explanation = Native, "The server offers echo-message: Boson sees its own messages as the server delivered them."
		return c
	}
	c.Status = ViaBridge
	c.Explanation = "The server has no echo-message. evrd echoes each message back to Boson once the server has taken it."
	c.Fix = e.upgrade(FeatureEchoMessage)
	return c
}

func (e evaluator) serverTime() Capability {
	c := Capability{Feature: FeatureServerTime, Title: "Server timestamps"}
	if e.has("server-time") {
		c.Status, c.Explanation = Native, "The server offers server-time: every message carries the time the server saw it."
		return c
	}
	c.Status = ViaBridge
	c.Explanation = "The server has no server-time. evrd stamps messages with the time it received them."
	c.Fix = e.upgrade(FeatureServerTime)
	return c
}

func (e evaluator) chathistory() Capability {
	c := Capability{Feature: FeatureChatHistory, Title: "Chat history"}
	_, isup := e.r.ISupport["CHATHISTORY"]
	if e.has("draft/chathistory", "chathistory") || isup {
		c.Status = Native
		c.Explanation = "The server offers CHATHISTORY: Boson can scroll back in channels and private messages from the server's own log."
		if e.sw == "unrealircd" {
			c.Explanation += " On UnrealIRCd only channels with mode +H keep history, and private messages have none."
		}
		return c
	}
	c.Status = ViaBridge
	c.Explanation = "The server has no CHATHISTORY. evrd logs the channels and private messages of Boson users and serves history from that log, so history starts when Boson first joins a channel."
	if e.sw == "inspircd" {
		c.Explanation += " (InspIRCd's chanhistory module only replays recent lines on join; it has no CHATHISTORY command.)"
	}
	c.Fix = e.upgrade(FeatureChatHistory)
	return c
}

func (e evaluator) readMarkers() Capability {
	c := Capability{Feature: FeatureReadMarkers, Title: "Read markers"}
	if e.has("draft/read-marker") {
		c.Status, c.Explanation = Native, "The server offers draft/read-marker: what a user has read syncs between their devices."
		return c
	}
	c.Status = ViaBridge
	c.Explanation = "The server has no draft/read-marker. evrd keeps each account's read markers and syncs them between that user's Boson devices."
	c.Fix = e.upgrade(FeatureReadMarkers)
	return c
}

// deniedClientTags reports whether CLIENTTAGDENY blocks tag.
func deniedClientTags(isupport map[string]string, tags ...string) []string {
	v, ok := isupport["CLIENTTAGDENY"]
	if !ok {
		return nil
	}
	rules := strings.Split(v, ",")
	var denied []string
	for _, tag := range tags {
		name := strings.TrimPrefix(tag, "+")
		deny := false
		for _, r := range rules {
			switch {
			case r == "*":
				deny = true
			case r == "-"+name:
				deny = false
			case r == name:
				deny = true
			}
		}
		if deny {
			denied = append(denied, tag)
		}
	}
	return denied
}

func (e evaluator) clientTags() Capability {
	c := Capability{Feature: FeatureClientTags, Title: "Replies and reactions"}
	if !e.has("message-tags") {
		c.Status = Unavailable
		c.Explanation = "Replies and reactions are client tags (+draft/reply, +draft/react) that the server has to pass between users, and this server has no message-tags. evrd can't add them: other users would never receive them. Boson shows replies as plain messages and hides reactions."
		c.Fix = e.upgrade(FeatureClientTags)
		return c
	}
	if denied := deniedClientTags(e.r.ISupport, "+draft/reply", "+draft/react", "+reply", "+react"); len(denied) > 0 && containsAll(denied, "+draft/reply", "+draft/react") {
		c.Status = Unavailable
		c.Explanation = "The server has message-tags but its CLIENTTAGDENY drops " + strings.Join(denied, ", ") + ", so replies and reactions never reach other users."
		c.Fix = e.upgrade(FeatureClientTags)
		return c
	}
	c.Status = Native
	c.Explanation = "The server relays client tags (message-tags, no CLIENTTAGDENY for replies or reactions): replies and reactions reach everyone with an IRCv3 client."
	return c
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, l := range list {
			if l == w {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (e evaluator) redaction() Capability {
	c := Capability{Feature: FeatureMessageRedaction, Title: "Deleting messages"}
	if e.has("draft/message-redaction") {
		c.Status, c.Explanation = Native, "The server offers draft/message-redaction: users can delete their messages for everyone."
		return c
	}
	c.Status = Unavailable
	c.Explanation = "The server has no draft/message-redaction. Deleting has to reach every user's client, which only the server can do; Boson hides Delete."
	c.Fix = e.upgrade(FeatureMessageRedaction)
	return c
}

func (e evaluator) sasl() Capability {
	c := Capability{Feature: FeatureSASL, Title: "SASL login"}
	_, hasSASL := e.r.Caps["sasl"]
	plain := len(e.r.SASLMechanisms) == 0 || containsFold(e.r.SASLMechanisms, "PLAIN")
	if hasSASL && plain {
		c.Status = Native
		mechs := "PLAIN"
		if len(e.r.SASLMechanisms) > 0 {
			mechs = strings.Join(e.r.SASLMechanisms, ", ")
		}
		c.Explanation = "The server offers SASL (" + mechs + "): Boson logs users in before they join anything."
		return c
	}
	if e.r.Services.NickServ {
		c.Status = ViaBridge
		if hasSASL {
			c.Explanation = "The server offers SASL but not PLAIN (" + strings.Join(e.r.SASLMechanisms, ", ") + "). evrd takes Boson's SASL PLAIN login and identifies to NickServ instead."
		} else {
			c.Explanation = "The server has no SASL. evrd takes Boson's SASL login and identifies to NickServ (IDENTIFY) right after connecting, before joining channels."
		}
		c.Fix = e.upgrade(FeatureSASL)
		return c
	}
	c.Status = Unavailable
	c.Explanation = "The server has no SASL and no NickServ: there are no IRC accounts to log in to."
	c.Fix = "Run services (Anope 2.1 or Atheme) linked to the server, or let Boson run its own IRC server (Ergo) for accounts."
	return c
}

func containsFold(list []string, s string) bool {
	for _, l := range list {
		if strings.EqualFold(l, s) {
			return true
		}
	}
	return false
}

func (e evaluator) servicesRPC() Capability {
	c := Capability{Feature: FeatureServicesRPC, Title: "Existing accounts (services RPC)"}
	pkg := e.r.Services.Package
	enable := ""
	switch pkg {
	case "anope":
		enable = `Anope 2.1: load httpd, jsonrpc (with a token { token = "..." methods = "anope.checkCredentials anope.identify anope.account" } block) and rpc_user, then give Boson the URL (http://services-host:8080/jsonrpc) and the token.`
	case "atheme":
		enable = `Atheme: load misc/httpd and transport/jsonrpc (httpd { host; port; }), then give Boson the URL (http://services-host:8080/jsonrpc).`
	case "ergo":
		enable = "Ergo: Boson reads Ergo's accounts through its API; no services RPC needed."
	default:
		enable = "Run Anope 2.1 (jsonrpc + rpc_user) or Atheme (misc/httpd + transport/jsonrpc) and give Boson the RPC URL."
	}
	rpc := e.r.Services.RPC
	if rpc == nil {
		c.Status = Unavailable
		if !e.r.Services.NickServ {
			c.Explanation = "No NickServ: there are no existing IRC accounts for Boson to reuse."
		} else {
			c.Explanation = "Not checked: no services RPC URL given. Without it Boson can't check existing NickServ passwords, so users would make new Boson accounts."
		}
		c.Fix = enable
		return c
	}
	switch {
	case rpc.Reachable && rpc.Authorized && rpc.CredentialsChecked && rpc.CredentialsOK:
		c.Status = Native
		c.Explanation = "The " + flavorName(rpc.Flavor) + " RPC endpoint accepted the test account: Boson can log users in with their existing NickServ accounts."
		return c
	case rpc.Reachable && rpc.Authorized && !rpc.CredentialsChecked:
		c.Status = Unavailable
		c.Explanation = "The " + flavorName(rpc.Flavor) + " RPC endpoint answers, but no test account was given, so password checks weren't verified."
		c.Fix = "Give the probe an existing account and its password to verify."
		return c
	case rpc.Reachable && rpc.Authorized:
		c.Status = Unavailable
		c.Explanation = "The " + flavorName(rpc.Flavor) + " RPC endpoint refused the test account: " + rpc.Error
		c.Fix = "Check the test account's name and password."
		if rpc.Flavor == "anope" {
			c.Fix += " The token needs anope.checkCredentials (rpc_user loaded)."
		}
		return c
	case rpc.Reachable:
		c.Status = Unavailable
		c.Explanation = "The RPC endpoint answers but refused the token: " + rpc.Error
		c.Fix = "Check the token and that its methods include anope.checkCredentials."
		return c
	}
	c.Status = Unavailable
	c.Explanation = "The services RPC endpoint didn't work: " + rpc.Error
	c.Fix = enable
	return c
}

func flavorName(f string) string {
	switch f {
	case "anope":
		return "Anope"
	case "atheme":
		return "Atheme"
	}
	return "services"
}

func (e evaluator) webirc() Capability {
	c := Capability{Feature: FeatureWebIRC, Title: "Real user IPs (WEBIRC)"}
	w := e.r.WebIRC
	switch {
	case w.Tested && w.Accepted:
		c.Status = Native
		c.Explanation = "The server accepted WEBIRC with the given password: it sees each Boson user's real IP (bans and per-IP limits work per user)."
		if !w.Verified {
			c.Explanation += " (The server didn't show the claimed IP back, so this is from the absence of a refusal.)"
		}
		return c
	case w.Tested:
		c.Status = Unavailable
		c.Explanation = "The server refused WEBIRC with the given password (" + w.Detail + "): every Boson user would appear to come from the gateway's IP."
	default:
		c.Status = Unavailable
		c.Explanation = "Not checked: no WEBIRC password given. Without WEBIRC every Boson user appears to come from the gateway's IP."
	}
	c.Fix = e.upgrade(FeatureWebIRC)
	if e.sw == "ngircd" {
		c.Fix = "Set WebircPassword in ngIRCd's [Options] (the wizard generates it). ngIRCd has one password for all gateways."
	}
	return c
}

func (e evaluator) multiline() Capability {
	c := Capability{Feature: FeatureMultiline, Title: "Multiline messages"}
	if e.has("draft/multiline") {
		c.Status = Native
		c.Explanation = "The server offers draft/multiline"
		if v := e.r.Caps["draft/multiline"]; v != "" {
			c.Explanation += " (" + v + ")"
		}
		c.Explanation += ": a pasted block arrives as one message."
		return c
	}
	c.Status = Unavailable
	c.Explanation = "The server has no draft/multiline. A pasted block goes out as separate lines (evrd and Boson split it), and everyone sees separate lines: keeping it one message needs the server."
	c.Fix = e.upgrade(FeatureMultiline)
	return c
}
