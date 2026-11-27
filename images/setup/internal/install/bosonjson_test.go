package install

import (
	"strings"
	"testing"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

func TestCheckBosonJSON(t *testing.T) {
	a := stack.Defaults()
	a.Domain = "boson.test"
	irc := `"irc":{"host":"irc.boson.test","port":6697,"tls":true}`
	auth := `"auth":{"url":"https://auth.boson.test"}`
	fed := `"federation":{"issuer":"https://auth.boson.test"}`
	for body, want := range map[string]string{
		`{"version":1,` + irc + `,` + auth + `,` + fed + `}`:         "",
		`{"version":1,` + irc + `,` + auth + `}`:                     "no federation section",
		`{"version":1,` + irc + `,` + auth + `,"federation":null}`:   "no federation section",
		`{"version":1,` + auth + `}`:                                 "no irc section",
		`{"version":1,"irc":{"host":"irc.other.test"},` + auth + `}`: `irc.host is "irc.other.test"`,
		`{"version":1,` + irc + `}`:                                  "no auth section",
		`{"version":1,` + irc + `,"auth":{"url":"https://x"}}`:       `auth.url is "https://x"`,
		`<html>`: "not JSON",
	} {
		err := CheckBosonJSON([]byte(body), a)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	// Federation off: no section needed.
	a.Federation = stack.FederationOff
	if err := CheckBosonJSON([]byte(`{"version":1,`+irc+`,`+auth+`}`), a); err != nil {
		t.Error(err)
	}
}
