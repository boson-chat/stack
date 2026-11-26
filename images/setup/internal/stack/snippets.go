package stack

// The config snippets the add-on path gives an IRC server's admin to paste
// (the wizard never edits their server). test/setup applies each one to the
// matching fixture in test/ircds, so they're checked against the real
// UnrealIRCd 6, InspIRCd 4 and ngIRCd.

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed snippets/boson-react.c
var bosonReactModule string

// BosonReactModule is the UnrealIRCd module that lets +draft/react through.
func BosonReactModule() string { return bosonReactModule }

// Snippet is one block for the admin to add.
type Snippet struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// For: "ircd" (the IRC server's config), "services" (Anope/Atheme), or
	// "module" (a source file to build).
	For string `json:"for"`
	// Where it goes, for the admin.
	Where string `json:"where"`
	// First: it must come before the server's own blocks of that kind
	// (InspIRCd's <connect>: the first that matches wins).
	First bool   `json:"first,omitempty"`
	Text  string `json:"text"`
	// Warning the admin must read (ngIRCd's per-address limit).
	Warning  string `json:"warning,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	// Needs: the probe rows this snippet turns green (webirc, limits,
	// services-rpc, reactions).
	Needs string `json:"needs,omitempty"`
}

// Snippets for the answers. webircPW is the bridge's WEBIRC password.
func Snippets(a Answers, webircPW string) []Snippet {
	ad := a.Addon
	ip := ad.SourceIP
	if ip == "" {
		ip = "THIS-SERVER-IP"
	}
	var out []Snippet
	switch ad.Software {
	case "ngircd":
		out = append(out,
			Snippet{ID: "webirc", Title: "WEBIRC for the bridge", For: "ircd", Needs: "webirc",
				Where: "ngircd.conf (or a file in its IncludeDir), then reload ngIRCd (kill -HUP).",
				Text: fmt.Sprintf("# Boson: the bridge passes each Boson user's own address (WEBIRC).\n"+
					"# ngIRCd takes WEBIRC from any host that knows this password: keep it secret.\n"+
					"[Options]\n\tWebIRCPassword = %s\n", webircPW)},
			Snippet{ID: "limits", Title: "No per-address connection limit", For: "ircd", Needs: "limits",
				Where: "ngircd.conf, then reload ngIRCd.",
				Text: fmt.Sprintf("# Boson: every Boson user connects from this server (%s).\n"+
					"# ngIRCd can't exempt one address: this lifts the limit for everyone.\n"+
					"[Limits]\n\tMaxConnectionsIP = 0\n", ip),
				Warning: "ngIRCd can’t exempt a single address from MaxConnectionsIP. Setting it to 0 removes the per-address connection limit for every client on your server, not just Boson’s. Anyone can then open as many connections from one address as your server’s overall limits allow. If that’s not acceptable, keep your limit: Boson works until that many Boson users are online at once, and the rest are refused."})
	case "unrealircd":
		out = append(out,
			Snippet{ID: "webirc", Title: "WEBIRC for the bridge", For: "ircd", Needs: "webirc",
				Where: "unrealircd.conf (or an included file), then /REHASH.",
				Text: fmt.Sprintf("/* Boson: the bridge passes each Boson user's own address (WEBIRC). */\n"+
					"webirc {\n\tmask %s;\n\tpassword \"%s\";\n};\n", ip, webircPW)},
			Snippet{ID: "limits", Title: "Many connections from the bridge", For: "ircd", Needs: "limits",
				Where: "unrealircd.conf, after your own allow blocks (UnrealIRCd uses the last allow block that matches), then /REHASH.",
				Text: fmt.Sprintf("/* Boson: every Boson user connects from this server. */\n"+
					"allow {\n\tmask %[1]s;\n\tclass clients;\n\tmaxperip 10000;\n\tglobal-maxperip 10000;\n};\n"+
					"except throttle {\n\tmask %[1]s;\n};\n"+
					"except ban {\n\tmask %[1]s;\n\ttype { connect-flood; handshake-data-flood; };\n};\n"+
					"set {\n\tconnthrottle { except { mask %[1]s; } }\n};\n", ip)})
		if ad.AllowReact {
			out = append(out,
				Snippet{ID: "react-module", Title: "The +draft/react module (source)", For: "module", Optional: true,
					Where: "Save as src/modules/third/boson-react.c in your UnrealIRCd source directory, then run make && make install there.",
					Text:  bosonReactModule},
				Snippet{ID: "react", Title: "Load the +draft/react module", For: "ircd", Optional: true, Needs: "reactions",
					Where: "unrealircd.conf, after building the module above, then /REHASH.",
					Text:  "/* Boson: reactions (+draft/react) reach everyone with an IRCv3 client. */\nloadmodule \"third/boson-react\";\n"})
		}
	case "inspircd":
		out = append(out,
			Snippet{ID: "limits", Title: "A connect class for the bridge", For: "ircd", First: true, Needs: "limits",
				Where: "inspircd.conf, BEFORE your other <connect> blocks (InspIRCd uses the first one that matches), then /REHASH.",
				Text: fmt.Sprintf("# Boson: every Boson user connects from this server.\n"+
					"<connect name=\"boson-bridge\" allow=\"%s\" localmax=\"10000\" globalmax=\"10000\" limit=\"10000\"\n"+
					"         threshold=\"1000\" commandrate=\"100000\" useident=\"no\" usednsbl=\"no\" resolvehostnames=\"no\">\n", ip)},
			Snippet{ID: "webirc", Title: "WEBIRC for the bridge", For: "ircd", Needs: "webirc",
				Where: "inspircd.conf (anywhere), then /REHASH. Leave out the module line if gateway is loaded already.",
				Text: fmt.Sprintf("# Boson: the bridge passes each Boson user's own address (WEBIRC).\n"+
					"<module name=\"gateway\">\n<gateway type=\"webirc\" mask=\"%s\" password=\"%s\">\n", ip, webircPW)})
	default:
		out = append(out, Snippet{ID: "webirc", Title: "WEBIRC and connection limits", For: "ircd", Needs: "webirc",
			Where: "Your IRC server's config (its documentation has the syntax).",
			Text: fmt.Sprintf("Add a WEBIRC (CGI:IRC) gateway block for %s with the password\n  %s\n"+
				"and exempt %s from per-address connection limits, throttling and connect-flood protection.\n", ip, webircPW, ip)})
	}

	// Services RPC: for NickServ accounts checked over RPC, or to offer it.
	if ad.Services == "anope" || ad.Services == "atheme" {
		out = append(out, servicesSnippet(ad.Services))
	}
	return out
}

func servicesSnippet(pkg string) Snippet {
	if pkg == "atheme" {
		return Snippet{ID: "services-rpc", Title: "Atheme's RPC (optional)", For: "services", Optional: true, Needs: "services-rpc",
			Where: "atheme.conf, then restart Atheme. Atheme's RPC has no token: keep it on a private network that only this server reaches.",
			Text: "loadmodule \"modules/misc/httpd\";\nloadmodule \"modules/transport/jsonrpc\";\n" +
				"httpd {\n\thost = \"PRIVATE-ADDRESS\";\n\tport = 8080;\n};\n"}
	}
	return Snippet{ID: "services-rpc", Title: "Anope's RPC (optional)", For: "services", Optional: true, Needs: "services-rpc",
		Where: "Anope's modules.conf, then restart Anope. Use the same token in Boson's services step. Keep httpd on a private address (or ssl = yes).",
		Text: strings.Join([]string{
			`module { name = "httpd"; httpd { name = "httpd/main"; ip = "PRIVATE-ADDRESS"; port = 8080; timeout = 30; } }`,
			`module {`,
			`	name = "jsonrpc"`,
			`	server = "httpd/main"`,
			`	token { token = "CHOOSE-A-TOKEN"; methods = "anope.checkCredentials anope.account"; }`,
			`}`,
			`module { name = "rpc_user" }`,
			`module { name = "rpc_data" }`,
			``}, "\n")}
}
