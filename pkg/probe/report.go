package probe

// SchemaVersion is the version of the Report and Table JSON. It changes
// only when a field changes meaning or goes away; new fields don't bump it.
const SchemaVersion = 1

// Report is everything the probe learned about one IRC server.
type Report struct {
	Schema int    `json:"schema"`
	Target Target `json:"target"`

	Server   ServerInfo        `json:"server"`
	ISupport map[string]string `json:"isupport"`
	// Caps is the CAP LS 302 list: capability name to value ("" when the
	// server gave no value). Empty when the server has no CAP command.
	Caps map[string]string `json:"caps"`
	// CapSupported is false when the server answered CAP with 421 (or not at all).
	CapSupported   bool     `json:"cap_supported"`
	SASLMechanisms []string `json:"sasl_mechanisms"`

	Services ServicesInfo `json:"services"`
	WebIRC   WebIRCResult `json:"webirc"`
	Limits   LimitsInfo   `json:"limits"`

	// Table is the per-feature verdict (see Evaluate).
	Table Table `json:"table"`

	// Warnings are steps that failed without failing the probe (a VERSION
	// that timed out, an RPC endpoint that didn't answer).
	Warnings []string `json:"warnings,omitempty"`
	// Transcript is the main connection's lines (">> " sent, "<< " received;
	// passwords redacted). Only kept when Options.Transcript is set.
	Transcript []string `json:"transcript,omitempty"`
}

// Target is what was probed.
type Target struct {
	Addr string `json:"addr"`
	TLS  bool   `json:"tls"`
}

// ServerInfo identifies the IRC server software.
type ServerInfo struct {
	// Name is the server's name (001's source / 004).
	Name string `json:"name"`
	// Software is a normalised family name: "unrealircd", "inspircd",
	// "ngircd", "ergo", "solanum", "charybdis", "hybrid", "ratbox",
	// "ircu", "nefarious", "bahamut", or "unknown".
	Software string `json:"software"`
	// Version is the version without the software name ("6.2.7.1").
	Version string `json:"version"`
	// RawVersion is the version string as the server sent it (004 or 351).
	RawVersion string `json:"raw_version"`
	// Network is ISUPPORT NETWORK.
	Network string `json:"network"`
}

// ServicesInfo describes NickServ and the services RPC check.
type ServicesInfo struct {
	NickServ bool `json:"nickserv"`
	// Package is "anope", "atheme", "ergo" (built-in NickServ) or
	// "unknown"; "" when there is no NickServ.
	Package string `json:"package"`
	Version string `json:"version,omitempty"`
	// Server is the server NickServ is on (WHOIS 312).
	Server     string `json:"server,omitempty"`
	RawVersion string `json:"raw_version,omitempty"`
	// RPC is nil when no RPC URL was given.
	RPC *RPCResult `json:"rpc,omitempty"`
}

// RPCResult is the outcome of checking the services RPC endpoint.
type RPCResult struct {
	URL string `json:"url"`
	// Protocol is "jsonrpc" or "xmlrpc".
	Protocol string `json:"protocol"`
	// Flavor is "anope" or "atheme": whose methods answered.
	Flavor string `json:"flavor,omitempty"`
	// Reachable: the endpoint answered with a well-formed RPC reply.
	Reachable bool `json:"reachable"`
	// Authorized: the bearer token (Anope) was accepted. Atheme has no
	// token, so it's true whenever Reachable is.
	Authorized bool `json:"authorized"`
	// CredentialsChecked: an account and password were given and tried.
	CredentialsChecked bool `json:"credentials_checked"`
	// CredentialsOK: the services accepted that account and password.
	CredentialsOK bool   `json:"credentials_ok"`
	Error         string `json:"error,omitempty"`
}

// WebIRCResult is the outcome of the WEBIRC attempt.
type WebIRCResult struct {
	Tested   bool `json:"tested"`
	Accepted bool `json:"accepted"`
	// Verified: the spoofed IP showed up in our own WHOIS (not every server
	// shows it, so Accepted can be true without it).
	Verified bool   `json:"verified"`
	Detail   string `json:"detail,omitempty"`
}

// LimitsInfo is what the probe could tell about connection limits.
type LimitsInfo struct {
	// MaxPerIP is how many simultaneous connections from the probe's
	// address the server took before refusing one; 0 when not measured or
	// when no refusal happened within Options.ConnLimitTest connections.
	MaxPerIP int `json:"max_per_ip"`
	// Tested is how many extra connections were opened (Options.ConnLimitTest).
	Tested int `json:"tested"`
	// Refusal is the server's message for the refused connection.
	Refusal string `json:"refusal,omitempty"`
	// ISupport holds the limit tokens the server advertises (CHANLIMIT,
	// MAXTARGETS, TARGMAX, NICKLEN, ...).
	ISupport map[string]string `json:"isupport,omitempty"`
	// Hint explains why limits matter for Boson and how to lift them on
	// this software.
	Hint string `json:"hint"`
}

// Status is one feature's verdict.
type Status string

const (
	// Native: the IRC server (or its services) provides it.
	Native Status = "native"
	// ViaBridge: evrd in front of the server can provide it.
	ViaBridge Status = "via-bridge"
	// Unavailable: neither; Fix says what would change that.
	Unavailable Status = "unavailable"
)

// Feature names, in table order.
const (
	FeatureMessageTags      = "message-tags"
	FeatureEchoMessage      = "echo-message"
	FeatureServerTime       = "server-time"
	FeatureChatHistory      = "chathistory"
	FeatureReadMarkers      = "read-markers"
	FeatureReplies          = "replies"
	FeatureReactions        = "reactions"
	FeatureMessageRedaction = "message-redaction"
	FeatureSASL             = "sasl"
	FeatureServicesRPC      = "services-rpc"
	FeatureWebIRC           = "webirc"
	FeatureMultiline        = "multiline"
)

// Features lists every feature in table order.
var Features = []string{
	FeatureMessageTags, FeatureEchoMessage, FeatureServerTime, FeatureChatHistory,
	FeatureReadMarkers, FeatureReplies, FeatureReactions, FeatureMessageRedaction, FeatureSASL,
	FeatureServicesRPC, FeatureWebIRC, FeatureMultiline,
}

// Table is the capability table: one row per Boson feature.
type Table struct {
	Schema   int    `json:"schema"`
	Software string `json:"software"`
	Services string `json:"services"`
	// NeedsBridge is true when any row is via-bridge: Boson should put evrd
	// in front of this server.
	NeedsBridge bool         `json:"needs_bridge"`
	Features    []Capability `json:"features"`
}

// Capability is one row of the table.
type Capability struct {
	Feature string `json:"feature"`
	// Title is a short human name ("Replies and reactions").
	Title  string `json:"title"`
	Status Status `json:"status"`
	// Explanation says why, in a sentence or two, for the admin.
	Explanation string `json:"explanation"`
	// Fix says what the admin can do to get it natively (or at all); empty
	// when native.
	Fix string `json:"fix,omitempty"`
}

// Get returns the row for feature, or false.
func (t Table) Get(feature string) (Capability, bool) {
	for _, c := range t.Features {
		if c.Feature == feature {
			return c, true
		}
	}
	return Capability{}, false
}
