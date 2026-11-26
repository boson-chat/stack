package probe

import (
	"regexp"
	"strings"
)

var softwarePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"unrealircd", regexp.MustCompile(`(?i)^unreal(?:ircd)?[-_ ]?v?([0-9][^ ]*)`)},
	{"inspircd", regexp.MustCompile(`(?i)^inspircd[-_ ]?v?([0-9][^ ]*)`)},
	{"ngircd", regexp.MustCompile(`(?i)^ngircd[-_ ]?v?([0-9][^ ]*)`)},
	{"ergo", regexp.MustCompile(`(?i)^(?:ergo|oragono)[-_ ]?v?([0-9][^ ]*)`)},
	{"solanum", regexp.MustCompile(`(?i)^solanum[-_ ]?v?([0-9][^ ]*)?`)},
	{"charybdis", regexp.MustCompile(`(?i)^charybdis[-_ ]?v?([0-9][^ ]*)`)},
	{"hybrid", regexp.MustCompile(`(?i)^(?:ircd-)?hybrid[-_ ]?v?([0-9][^ ]*)`)},
	{"ratbox", regexp.MustCompile(`(?i)^ircd-ratbox[-_ ]?v?([0-9][^ ]*)`)},
	{"nefarious", regexp.MustCompile(`(?i)^nefarious[-_ ]?v?([0-9][^ ]*)?`)},
	{"ircu", regexp.MustCompile(`(?i)^u([0-9]+\.[0-9][^ ]*)`)},
	{"bahamut", regexp.MustCompile(`(?i)^bahamut[-_ ]?v?([0-9][^ ]*)`)},
}

// identifySoftware maps a 004/351 version string to a software family and
// version ("UnrealIRCd-6.2.7.1." -> "unrealircd", "6.2.7.1").
func identifySoftware(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	for _, p := range softwarePatterns {
		if m := p.re.FindStringSubmatch(raw); m != nil {
			v := ""
			if len(m) > 1 {
				v = strings.TrimRight(m[1], ".")
			}
			return p.name, v
		}
	}
	return "unknown", strings.TrimRight(raw, ".")
}

var (
	anopeRe  = regexp.MustCompile(`(?i)anope[-_ ]?v?([0-9][0-9a-z.\-]*)`)
	athemeRe = regexp.MustCompile(`(?i)atheme(?:[-_ ]services)?[-_ ]?v?([0-9][0-9a-z.\-]*)`)
)

// identifyServices maps a services VERSION / CTCP VERSION reply to a
// package and version.
func identifyServices(raw string) (string, string) {
	if m := anopeRe.FindStringSubmatch(raw); m != nil {
		return "anope", strings.TrimRight(m[1], ".-")
	}
	if m := athemeRe.FindStringSubmatch(raw); m != nil {
		return "atheme", strings.TrimRight(m[1], ".-")
	}
	switch {
	case strings.Contains(strings.ToLower(raw), "anope"):
		return "anope", ""
	case strings.Contains(strings.ToLower(raw), "atheme"):
		return "atheme", ""
	}
	return "unknown", ""
}

// limitHint explains per-IP limits for the software.
func limitHint(sw string) string {
	const why = "Boson's web gateway and evrd connect every Boson user from one address, so the server's per-IP connection limit caps how many can be online. "
	switch sw {
	case "unrealircd":
		return why + "UnrealIRCd: allow::maxperip (default 3) and set::anti-flood connect-flood; give the gateway's address its own allow block with a higher maxperip, or an except ban { type { maxperip; connect-flood; } }. A webirc block also makes UnrealIRCd count users by their real IP."
	case "inspircd":
		return why + "InspIRCd: <connect localmax/globalmax> (default 3); add a <connect allow=\"GATEWAY-IP\" localmax=\"1000\" globalmax=\"1000\"> before the default block. With the gateway module, WEBIRC users are counted by their real IP."
	case "ngircd":
		return why + "ngIRCd: [Limits] MaxConnectionsIP (default 5; 0 = no limit). WEBIRC users are still counted against the gateway's address, so raise it."
	case "ergo":
		return why + "Ergo: server.ip-limits (max-concurrent-connections, exempted); exempt the gateway's address."
	}
	return why + "Raise or exempt the per-IP limit for the gateway's address."
}
