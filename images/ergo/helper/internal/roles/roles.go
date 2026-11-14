// Package roles is the auth service's network-roles contract as boson-ergo
// sees it: the role objects, how they become Ergo oper classes, and the
// per-user IRC grants (oper class, WHOIS line, vhost).
package roles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Role is one entry of GET /internal/v1/roles.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Color       Color    `json:"color"`
	Position    int      `json:"position"`
	Builtin     *string  `json:"builtin"` // owner | admin | moderator | member | null
	Permissions []string `json:"permissions"`
	IRC         RoleIRC  `json:"irc"`
	Hoist       bool     `json:"hoist"`
	Mentionable bool     `json:"mentionable"`
	MemberCount int      `json:"member_count"`
}

// RoleIRC is how a role shows on IRC.
type RoleIRC struct {
	// WhoisLine is the effective line (auth applies the "is a <name>"
	// default); null = "".
	WhoisLine string `json:"whois_line"`
	// VHost is a template; auth computes the per-user value.
	VHost string `json:"vhost"`
	Oper  bool   `json:"oper"`
	// OperClass is the Ergo oper class name (the role's slug), null when
	// the role isn't an oper.
	OperClass *string `json:"oper_class,omitempty"`
	// Capabilities are the effective Ergo capability names. nil (an older
	// auth) = derived from the irc.* permissions.
	Capabilities []string `json:"capabilities,omitempty"`
}

// List is the body of GET /internal/v1/roles.
type List struct {
	Roles   []Role  `json:"roles"`
	Version Version `json:"version"`
}

// UserIRC is a user's IRC grant: the `irc` object of a verify-password
// success and of GET /internal/v1/users/{username}/irc.
type UserIRC struct {
	OperClass *string `json:"oper_class"` // a role slug, or null
	WhoisLine string  `json:"whois_line"`
	VHost     *string `json:"vhost"`
}

// User is GET /internal/v1/users/{username}/irc: the account, whether it's
// disabled, its IRC grant, role names and effective permissions.
type User struct {
	UserID      string   `json:"user_id"`
	Username    string   `json:"username"`
	Disabled    bool     `json:"disabled"`
	IRC         UserIRC  `json:"irc"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// Has reports whether the user has perm.
func (u User) Has(perm string) bool {
	for _, p := range u.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Color accepts "#rrggbb", a bare hex string, a number (0xrrggbb) or null.
type Color string

func (c *Color) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*c = ""
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		s = strings.TrimSpace(s)
		if s != "" && !strings.HasPrefix(s, "#") {
			s = "#" + s
		}
		*c = Color(strings.ToLower(s))
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("color: %w", err)
	}
	*c = Color(fmt.Sprintf("#%06x", n&0xffffff))
	return nil
}

// Version is the roles list version: auth may send a number or a string.
type Version string

func (v *Version) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*v = Version(s)
		return nil
	}
	*v = Version(strings.TrimSpace(string(b)))
	return nil
}

// ErgoCapabilities are Ergo 2.19's oper capabilities. A role permission
// irc.<capability> maps 1:1 onto these; anything else is ignored.
var ErgoCapabilities = map[string]bool{
	"accreg": true, "ban": true, "chanreg": true, "defcon": true, "history": true,
	"kill": true, "massmessage": true, "metadata": true, "nofakelag": true,
	"rehash": true, "relaymsg": true, "roleplay": true, "sajoin": true,
	"samode": true, "snomasks": true, "vhosts": true,
}

// Capabilities returns the Ergo capabilities a role's irc.* permissions
// grant, sorted.
func (r Role) Capabilities() []string {
	set := map[string]bool{}
	if r.IRC.Capabilities != nil {
		for _, c := range r.IRC.Capabilities {
			if c = strings.ToLower(strings.TrimSpace(c)); ErgoCapabilities[c] {
				set[c] = true
			}
		}
		return sortedKeys(set)
	}
	for _, p := range r.Permissions {
		if c, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(p)), "irc."); ok && ErgoCapabilities[c] {
			set[c] = true
		}
	}
	return sortedKeys(set)
}

// HasPermission reports whether the role grants perm.
func (r Role) HasPermission(perm string) bool {
	for _, p := range r.Permissions {
		if strings.EqualFold(strings.TrimSpace(p), perm) {
			return true
		}
	}
	return false
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases s and turns every run of other characters into "-".
func Slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// OperClass is a rendered Ergo oper class for a role with irc.oper.
type OperClass struct {
	Name         string // auth's oper_class (the role slug): [a-z0-9_-]
	Title        string
	WhoisLine    string
	Capabilities []string
	Role         Role
}

// Class names auth's slugs are made of. The stack's own classes (the
// emergency OPER_NAME oper, RoleServ) contain a "." so they never collide.
var classNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// Max lengths of what goes into ircd.yaml (Ergo has no limits of its own;
// these keep WHOIS lines within one IRC line).
const (
	maxTitle     = 64
	maxWhoisLine = 200
)

// className is auth's oper_class, else (older auth) the role's slug, else
// one made from the name; "" if none is usable.
func className(r Role) string {
	for _, c := range []string{deref(r.IRC.OperClass), r.Slug, Slug(r.Name), Slug(r.ID)} {
		if c = strings.ToLower(strings.TrimSpace(c)); classNameRe.MatchString(c) {
			return c
		}
	}
	return ""
}

// Classes returns one oper class per role with irc.oper, highest position
// first. base are extra capabilities every role class gets. Names are
// auth's slugs, which are unique; a duplicate (shouldn't happen) keeps the
// more senior role.
func Classes(roles []Role, base []string) []OperClass {
	sorted := append([]Role(nil), roles...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Position > sorted[j].Position
	})
	used := map[string]bool{}
	var out []OperClass
	for _, r := range sorted {
		if !r.IRC.Oper {
			continue
		}
		name := className(r)
		if name == "" || used[name] {
			continue
		}
		used[name] = true
		caps := map[string]bool{}
		for _, c := range base {
			if ErgoCapabilities[c] {
				caps[c] = true
			}
		}
		for _, c := range r.Capabilities() {
			caps[c] = true
		}
		title := clean(r.Name, maxTitle)
		if title == "" {
			title = "IRC operator"
		}
		out = append(out, OperClass{
			Name:         name,
			Title:        title,
			WhoisLine:    clean(r.IRC.WhoisLine, maxWhoisLine),
			Capabilities: sortedKeys(caps),
			Role:         r,
		})
	}
	return out
}

// ResolveClass maps the oper_class auth returned for a user (a role slug;
// also accepted: the role's id or name) to the rendered class name. ok is
// false when no rendered class matches.
func ResolveClass(classes []OperClass, operClass string) (name string, ok bool) {
	want := strings.TrimSpace(operClass)
	if want == "" {
		return "", false
	}
	for _, c := range classes {
		if c.Name == want || c.Role.ID == want || (c.Role.Slug != "" && c.Role.Slug == want) {
			return c.Name, true
		}
	}
	for _, c := range classes {
		if strings.EqualFold(c.Role.Name, want) || Slug(c.Role.Name) == Slug(want) {
			return c.Name, true
		}
	}
	return "", false
}

// Ergo's accounts.vhosts limits, as rendered in ircd.yaml.
var vhostRe = regexp.MustCompile(`^[0-9A-Za-z.\-_/]+$`)

const MaxVHost = 64

// ValidVHost reports whether Ergo will accept vhost.
func ValidVHost(vhost string) bool {
	return len(vhost) <= MaxVHost && vhostRe.MatchString(vhost)
}

// Grant is what Ergo gets for one account (auth-script output fields and
// POST /v1/account_grants): nil = no opinion, "" = none.
type Grant struct {
	OperClass *string `json:"operClass,omitempty"`
	WhoisLine string  `json:"whoisLine,omitempty"`
	VHost     *string `json:"vhost,omitempty"`
}

// GrantFor turns auth's view of a user into Ergo's: the oper class resolved
// against the rendered classes ("" when it is null or unknown), the WHOIS
// line cleaned up, and the vhost ("" when null or invalid). It always has an
// opinion on both, so a user who lost a role loses its oper status and vhost.
func GrantFor(classes []OperClass, u UserIRC) Grant {
	class := ""
	if u.OperClass != nil {
		class, _ = ResolveClass(classes, *u.OperClass)
	}
	vhost := ""
	if u.VHost != nil && ValidVHost(strings.TrimSpace(*u.VHost)) {
		vhost = strings.TrimSpace(*u.VHost)
	}
	g := Grant{OperClass: &class, VHost: &vhost}
	if class != "" {
		// Only a line that differs from the class's own: otherwise Ergo
		// uses the class's whois-line, which follows edits of the role on
		// the next rehash instead of being frozen at login.
		if line := clean(u.WhoisLine, maxWhoisLine); line != classWhois(classes, class) {
			g.WhoisLine = line
		}
	}
	return g
}

func classWhois(classes []OperClass, name string) string {
	for _, c := range classes {
		if c.Name == name {
			return c.WhoisLine
		}
	}
	return ""
}

// None is the grant for a user with no roles on IRC.
func None() Grant {
	empty := ""
	empty2 := ""
	return Grant{OperClass: &empty, VHost: &empty2}
}

// Equal compares two grants by value.
func (g Grant) Equal(o Grant) bool {
	return deref(g.OperClass) == deref(o.OperClass) && (g.OperClass == nil) == (o.OperClass == nil) &&
		deref(g.VHost) == deref(o.VHost) && (g.VHost == nil) == (o.VHost == nil) && g.WhoisLine == o.WhoisLine
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// clean drops control characters, trims, and cuts s to max bytes (on a
// rune boundary).
func clean(s string, max int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8RuneStart(s[max]) {
		max--
	}
	return strings.TrimSpace(s[:max])
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Find looks a role up by id, name (case-insensitive) or slug.
func Find(list []Role, ref string) (Role, bool) {
	ref = strings.TrimSpace(ref)
	for _, r := range list {
		if r.ID == ref {
			return r, true
		}
	}
	for _, r := range list {
		if strings.EqualFold(r.Name, ref) {
			return r, true
		}
	}
	if s := Slug(ref); s != "" {
		for _, r := range list {
			if Slug(r.Name) == s {
				return r, true
			}
		}
	}
	return Role{}, false
}
