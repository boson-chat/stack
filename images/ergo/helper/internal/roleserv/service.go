// Package roleserv is RoleServ, the IRC face of the boson network roles:
// any IRC client (mIRC included) can list and inspect roles, and users with
// the roles.manage permission can change them, by /msg RoleServ.
//
// Every change is made by the auth service *as the caller's account*
// (acting endpoints, see docs/roles.md), so auth applies the caller's own
// permissions and role hierarchy and audits it as theirs. Callers are
// identified by the account Ergo attaches to their message (IRCv3
// account-tag), never by nick.
package roleserv

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// Auth is what RoleServ needs from the auth service (*roles.Client).
// List and User exist in auth 1.5; Members and the acting calls (Me and the
// writes) are requested additions: until auth has them, WHO and the write
// commands say so and HELP doesn't offer them.
type Auth interface {
	List(ctx context.Context) ([]roles.Role, error)
	User(ctx context.Context, username string) (roles.User, error)
	Members(ctx context.Context, roleID string, max int) ([]roles.Member, error)
	Me(ctx context.Context, account string) (roles.Actor, error)
	CreateRole(ctx context.Context, account, name string) (roles.Role, error)
	DeleteRole(ctx context.Context, account, roleID string) error
	UpdateRole(ctx context.Context, account, roleID string, patch map[string]any) (roles.Role, error)
	SetUserRoles(ctx context.Context, account, userID string, roleIDs []string) error
}

// Ergo is what RoleServ needs from Ergo's API (*ergoapi.Client).
type Ergo interface {
	ApplyGrants(ctx context.Context, account string, g roles.Grant) (int, error)
	AccountOf(ctx context.Context, nick string) (account string, online bool, err error)
}

// Service answers RoleServ commands.
type Service struct {
	Auth Auth
	Ergo Ergo
	// BaseCaps: IRC_ROLE_BASE_CAPABILITIES, for resolving oper classes the
	// same way boson-ergo render does.
	BaseCaps []string
	// RetryDelays: when a push names an oper class Ergo doesn't have yet
	// (the role was just made an IRC operator role; watch-settings renders
	// it within ROLES_POLL_SECONDS), retry after these delays.
	RetryDelays []time.Duration
	Logf        func(string, ...any)
	// Rate limit per account: Burst commands, then one per Every.
	Burst int
	Every time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
	wg      sync.WaitGroup
	// noActingUntil: auth answered "no acting API"; don't ask again until then.
	noActingUntil time.Time
}

var errNoActing = errors.New("acting API unsupported")

// actor asks auth who caller is for the acting API (their permissions).
// errNoActing when auth has no acting API (remembered for 5 minutes).
func (s *Service) actor(ctx context.Context, caller string) (roles.Actor, error) {
	s.mu.Lock()
	off := time.Now().Before(s.noActingUntil)
	s.mu.Unlock()
	if off {
		return roles.Actor{}, errNoActing
	}
	a, err := s.Auth.Me(ctx, caller)
	if errors.Is(err, roles.ErrUnsupported) {
		s.mu.Lock()
		s.noActingUntil = time.Now().Add(5 * time.Minute)
		s.mu.Unlock()
		return a, errNoActing
	}
	return a, err
}

const noWrites = "Changing roles from IRC isn't available on this network yet (its auth service has no acting API); use the admin panel."

type bucket struct {
	tokens float64
	last   time.Time
}

// ManagePermission lets a user change roles (subject to auth's hierarchy).
const ManagePermission = "roles.manage"

func (s *Service) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Service) allow(account string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.buckets == nil {
		s.buckets = map[string]*bucket{}
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	burst, every := s.Burst, s.Every
	if burst <= 0 {
		burst = 8
	}
	if every <= 0 {
		every = 2 * time.Second
	}
	b := s.buckets[account]
	if b == nil {
		b = &bucket{tokens: float64(burst), last: now}
		s.buckets[account] = b
	}
	b.tokens = min(float64(burst), b.tokens+now.Sub(b.last).Seconds()/every.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Wait waits for background re-pushes (tests).
func (s *Service) Wait() { s.wg.Wait() }

const (
	bold      = "\x02"
	maxRoles  = 50  // lines in LIST
	maxShown  = 200 // names in WHO
	lineBytes = 380 // text per NOTICE (Ergo's line limit is 512 incl. prefix)
)

type command struct {
	name, args, help string
	manage           bool
	run              func(s *Service, ctx context.Context, caller string, args []string, raw string) []string
}

var commands []command

func init() {
	commands = []command{
		{"LIST", "", "list the network's roles", false, (*Service).cmdList},
		{"INFO", "<role>", "show a role: color, position, IRC settings, permissions", false, (*Service).cmdInfo},
		{"WHO", "<role>", "list who has a role", false, (*Service).cmdWho},
		{"ROLES", "<nick|account>", "list someone's roles", false, (*Service).cmdRoles},
		{"CREATE", "<name>", "create a role", true, (*Service).cmdCreate},
		{"DELETE", "<role>", "delete a role", true, (*Service).cmdDelete},
		{"SET", "<role> COLOR|WHOIS|VHOST|POSITION|HOIST|OPER <value>", "change a role (WHOIS/VHOST/COLOR: none clears)", true, (*Service).cmdSet},
		{"PERM", "<role> +perm -perm ...", "add/remove permissions (irc.kill, voice.create, ...)", true, (*Service).cmdPerm},
		{"ASSIGN", "<account> <role>", "give an account a role", true, (*Service).cmdAssign},
		{"REMOVE", "<account> <role>", "take a role away from an account", true, (*Service).cmdRemove},
	}
}

// Handle runs one command line from caller (their logged-in account; ""
// when they aren't logged in) and returns the reply lines.
func (s *Service) Handle(ctx context.Context, caller, line string) []string {
	line = strings.TrimSpace(line)
	if caller == "" {
		return []string{"You need to be logged in to your account to use RoleServ (SASL, or /msg NickServ IDENTIFY)."}
	}
	if !s.allow(strings.ToLower(caller)) {
		return []string{"Slow down: too many commands. Try again in a few seconds."}
	}
	verb, raw, _ := strings.Cut(line, " ")
	raw = strings.TrimSpace(raw)
	verb = strings.ToUpper(verb)
	if verb == "" || verb == "HELP" {
		return s.cmdHelp(ctx, caller, raw)
	}
	for _, c := range commands {
		if c.name == verb {
			args, err := tokenize(raw)
			if err != nil {
				return []string{err.Error()}
			}
			if c.manage {
				if _, err := s.actor(ctx, caller); errors.Is(err, errNoActing) {
					return []string{noWrites}
				} else if err != nil {
					return authError(err)
				}
			}
			return c.run(s, ctx, caller, args, raw)
		}
	}
	return []string{fmt.Sprintf("Unknown command %s. /msg RoleServ HELP", verb)}
}

func (s *Service) cmdHelp(ctx context.Context, caller, topic string) []string {
	canManage := s.callerCanManage(ctx, caller)
	if topic != "" {
		for _, c := range commands {
			if strings.EqualFold(c.name, topic) {
				out := []string{bold + c.name + " " + c.args + bold + ": " + c.help}
				if c.manage {
					out = append(out, "Needs the roles.manage permission; you can only manage roles below your highest role.")
				}
				if c.name == "PERM" {
					out = append(out, "IRC operator powers are irc.<capability>: irc.kill irc.ban irc.samode irc.sajoin irc.vhosts irc.nofakelag irc.history irc.chanreg irc.accreg irc.snomasks irc.relaymsg irc.massmessage irc.metadata irc.rehash irc.defcon; they apply to roles with OPER on.")
				}
				return out
			}
		}
		return []string{"No help for " + topic + "."}
	}
	out := []string{bold + "RoleServ" + bold + " shows the network's roles. Role names with spaces can be quoted: INFO \"Senior Mod\"."}
	for _, c := range commands {
		if c.manage && !canManage {
			continue
		}
		out = append(out, fmt.Sprintf("  %s%s%s %s - %s", bold, c.name, bold, c.args, c.help))
	}
	if !canManage {
		out = append(out, "Users with the roles.manage permission can also create, change and assign roles here (when the network supports it) or in the admin panel.")
	}
	return out
}

// callerCanManage: the acting API exists and auth says caller has
// roles.manage (the hierarchy is checked by auth on each change).
func (s *Service) callerCanManage(ctx context.Context, caller string) bool {
	a, err := s.actor(ctx, caller)
	if err != nil {
		return false
	}
	for _, p := range a.Permissions {
		if p == ManagePermission {
			return true
		}
	}
	return false
}

func sortRoles(list []roles.Role) []roles.Role {
	out := append([]roles.Role(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position > out[j].Position
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func (s *Service) cmdList(ctx context.Context, _ string, _ []string, _ string) []string {
	list, err := s.Auth.List(ctx)
	if err != nil {
		return authError(err)
	}
	if len(list) == 0 {
		return []string{"This network has no roles."}
	}
	out := []string{fmt.Sprintf("%d role(s), highest first:", len(list))}
	for i, r := range sortRoles(list) {
		if i == maxRoles {
			out = append(out, fmt.Sprintf("... and %d more", len(list)-maxRoles))
			break
		}
		out = append(out, "  "+summary(r))
	}
	return out
}

func summary(r roles.Role) string {
	var tags []string
	if r.Builtin != nil && *r.Builtin != "" {
		tags = append(tags, "built-in")
	}
	if r.IRC.Oper {
		tags = append(tags, "IRC operator")
	}
	if r.IRC.VHost != "" {
		tags = append(tags, "vhost "+r.IRC.VHost)
	}
	if r.Hoist {
		tags = append(tags, "hoisted")
	}
	color := ""
	if r.Color != "" {
		color = " " + string(r.Color)
	}
	s := fmt.Sprintf("%s%s%s%s (position %d)", bold, r.Name, bold, color, r.Position)
	if len(tags) > 0 {
		s += ": " + strings.Join(tags, ", ")
	}
	return s
}

// role resolves a role reference (name, slug or id) against auth's list.
func (s *Service) role(ctx context.Context, ref string) (roles.Role, []string) {
	if strings.TrimSpace(ref) == "" {
		return roles.Role{}, []string{"Which role? /msg RoleServ LIST"}
	}
	list, err := s.Auth.List(ctx)
	if err != nil {
		return roles.Role{}, authError(err)
	}
	r, ok := roles.Find(list, ref)
	if !ok {
		return roles.Role{}, []string{fmt.Sprintf("No role %q. /msg RoleServ LIST", ref)}
	}
	return r, nil
}

func (s *Service) cmdInfo(ctx context.Context, _ string, args []string, _ string) []string {
	r, errs := s.role(ctx, strings.Join(args, " "))
	if errs != nil {
		return errs
	}
	out := []string{summary(r)}
	builtin := "no"
	if r.Builtin != nil && *r.Builtin != "" {
		builtin = *r.Builtin
	}
	out = append(out, fmt.Sprintf("  id %s, built-in: %s, color: %s, hoisted: %s, mentionable: %s",
		r.ID, builtin, orNone(string(r.Color)), yesNo(r.Hoist), yesNo(r.Mentionable)))
	out = append(out, fmt.Sprintf("  IRC operator: %s, WHOIS line: %s, vhost: %s",
		yesNo(r.IRC.Oper), orNone(r.IRC.WhoisLine), orNone(r.IRC.VHost)))
	if caps := r.Capabilities(); r.IRC.Oper && len(caps) > 0 {
		out = append(out, "  IRC powers: "+strings.Join(caps, " "))
	}
	perms := append([]string(nil), r.Permissions...)
	sort.Strings(perms)
	if len(perms) == 0 {
		out = append(out, "  permissions: none")
	} else {
		out = append(out, wrap("  permissions: ", perms)...)
	}
	return out
}

func (s *Service) cmdWho(ctx context.Context, _ string, args []string, _ string) []string {
	r, errs := s.role(ctx, strings.Join(args, " "))
	if errs != nil {
		return errs
	}
	members, err := s.Auth.Members(ctx, r.ID, maxShown+1)
	if errors.Is(err, roles.ErrUnsupported) {
		return []string{fmt.Sprintf("Listing members isn't available on this network yet. %s%s%s has %d member(s).", bold, r.Name, bold, r.MemberCount)}
	} else if err != nil {
		return authError(err)
	}
	if len(members) == 0 {
		return []string{fmt.Sprintf("Nobody has %s%s%s.", bold, r.Name, bold)}
	}
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.Username)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	more := ""
	total := max(r.MemberCount, len(members))
	if len(names) > maxShown {
		more = fmt.Sprintf(" (first %d shown)", maxShown)
		names = names[:maxShown]
	}
	out := []string{fmt.Sprintf("%s%s%s: %d member(s)%s", bold, r.Name, bold, total, more)}
	return append(out, wrap("  ", names)...)
}

func (s *Service) cmdRoles(ctx context.Context, _ string, args []string, _ string) []string {
	if len(args) != 1 {
		return []string{"Syntax: ROLES <nick|account>"}
	}
	account, label := s.accountFor(ctx, args[0])
	if account == "" {
		return []string{label + " is not logged in to an account."}
	}
	u, err := s.Auth.User(ctx, account)
	if errors.Is(err, roles.ErrNotFound) {
		return []string{fmt.Sprintf("No account %q.", account)}
	} else if err != nil {
		return authError(err)
	}
	note := ""
	if u.Disabled {
		note = " (account disabled)"
	}
	if len(u.Roles) == 0 {
		return []string{fmt.Sprintf("%s has no roles besides Member%s.", label, note)}
	}
	return []string{fmt.Sprintf("%s: %s%s", label, strings.Join(u.Roles, ", "), note)}
}

// accountFor resolves a nick (if someone is connected with it) to their
// account, else takes the argument as an account name.
func (s *Service) accountFor(ctx context.Context, arg string) (account, label string) {
	if acct, online, err := s.Ergo.AccountOf(ctx, arg); err == nil && online {
		if acct == "" {
			return "", arg
		}
		if strings.EqualFold(acct, arg) {
			return acct, acct
		}
		return acct, fmt.Sprintf("%s (account %s)", arg, acct)
	}
	return arg, arg
}

var roleNameBad = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// auth's rule for role names.
var roleNameRe = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,32}$`)

func (s *Service) cmdCreate(ctx context.Context, caller string, args []string, _ string) []string {
	name := strings.Join(args, " ")
	if !roleNameRe.MatchString(name) {
		return []string{"Syntax: CREATE <name> (1-32 characters: letters, digits, space, - and _)"}
	}
	r, err := s.Auth.CreateRole(ctx, caller, name)
	if err != nil {
		return authError(err)
	}
	return []string{fmt.Sprintf("Created %s%s%s (position %d). Set it up with SET %s ... and PERM %s +perm.",
		bold, r.Name, bold, r.Position, quoteRef(r.Name), quoteRef(r.Name))}
}

func (s *Service) cmdDelete(ctx context.Context, caller string, args []string, _ string) []string {
	r, errs := s.role(ctx, strings.Join(args, " "))
	if errs != nil {
		return errs
	}
	members, _ := s.Auth.Members(ctx, r.ID, 10000)
	if err := s.Auth.DeleteRole(ctx, caller, r.ID); err != nil {
		return authError(err)
	}
	s.push(ctx, usernames(members)...)
	return []string{fmt.Sprintf("Deleted %s%s%s.", bold, r.Name, bold)}
}

var setFields = map[string]bool{"COLOR": true, "COLOUR": true, "WHOIS": true, "VHOST": true, "POSITION": true, "HOIST": true, "OPER": true}

var colorRe = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

func (s *Service) cmdSet(ctx context.Context, caller string, args []string, _ string) []string {
	// SET <role words...> FIELD <value...>: the role is everything before
	// the first field keyword (or one quoted argument).
	idx := -1
	for i, a := range args {
		if i > 0 && setFields[strings.ToUpper(a)] {
			idx = i
			break
		}
	}
	if idx < 0 || idx == len(args)-1 {
		return []string{"Syntax: SET <role> COLOR|WHOIS|VHOST|POSITION|HOIST|OPER <value> (none clears COLOR, WHOIS, VHOST)"}
	}
	r, errs := s.role(ctx, strings.Join(args[:idx], " "))
	if errs != nil {
		return errs
	}
	field := strings.ToUpper(args[idx])
	value := strings.Join(args[idx+1:], " ")
	clear := strings.EqualFold(value, "none") || value == "-"
	patch := map[string]any{}
	switch field {
	case "COLOR", "COLOUR":
		switch {
		case clear:
			patch["color"] = nil
		case colorRe.MatchString(value):
			patch["color"] = "#" + strings.ToLower(strings.TrimPrefix(value, "#"))
		default:
			return []string{"COLOR takes #rrggbb (e.g. #e91e63) or none."}
		}
	case "WHOIS":
		if clear {
			patch["irc"] = map[string]any{"whois_line": nil}
		} else if len(value) > 200 || roleNameBad.MatchString(value) {
			return []string{"WHOIS takes a line of up to 200 characters (e.g. is a Moderator on Boson), or none."}
		} else {
			patch["irc"] = map[string]any{"whois_line": value}
		}
	case "VHOST":
		// A template auth renders per user ({username}); auth checks that
		// every rendering is a valid vhost.
		if clear {
			patch["irc"] = map[string]any{"vhost": nil}
		} else if len(value) > 64 || !vhostTemplateRe.MatchString(value) {
			return []string{"VHOST takes a vhost template (letters, digits, . - _ / and {username}, e.g. staff/{username}), or none."}
		} else {
			patch["irc"] = map[string]any{"vhost": value}
		}
	case "POSITION":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return []string{"POSITION takes a number (higher = more senior)."}
		}
		patch["position"] = n
	case "HOIST", "OPER":
		b, ok := parseBool(value)
		if !ok {
			return []string{field + " takes on or off."}
		}
		if field == "HOIST" {
			patch["hoist"] = b
		} else {
			patch["irc"] = map[string]any{"oper": b}
		}
	}
	updated, err := s.Auth.UpdateRole(ctx, caller, r.ID, patch)
	if err != nil {
		return authError(err)
	}
	out := []string{"Updated " + summary(updated)}
	if _, ok := patch["irc"]; ok || field == "POSITION" {
		// WHOIS lines, vhosts and oper status are per-member grants (the
		// most senior role wins, so POSITION can change them too).
		members, _ := s.Auth.Members(ctx, r.ID, 10000)
		s.push(ctx, usernames(members)...)
		if len(members) > 0 {
			out = append(out, fmt.Sprintf("Applying to %d member(s) on IRC.", len(members)))
		}
	}
	return out
}

var vhostTemplateRe = regexp.MustCompile(`^[0-9A-Za-z.\-_/{}]+$`)

var permRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)

func (s *Service) cmdPerm(ctx context.Context, caller string, args []string, _ string) []string {
	idx := -1
	for i, a := range args {
		if strings.HasPrefix(a, "+") || strings.HasPrefix(a, "-") {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return []string{"Syntax: PERM <role> +perm -perm ... (e.g. PERM Moderator +irc.kill +irc.ban -voice.create)"}
	}
	add, remove := map[string]bool{}, map[string]bool{}
	for _, a := range args[idx:] {
		p := strings.ToLower(a[1:])
		if (a[0] != '+' && a[0] != '-') || !permRe.MatchString(p) {
			return []string{fmt.Sprintf("%q is not +permission or -permission.", a)}
		}
		if a[0] == '+' {
			add[p], remove[p] = true, false
		} else {
			remove[p], add[p] = true, false
		}
	}
	r, errs := s.role(ctx, strings.Join(args[:idx], " "))
	if errs != nil {
		return errs
	}
	// The admin API replaces the whole list; auth rejects permissions the
	// caller doesn't hold (permission_not_held) and unknown ids.
	set := map[string]bool{}
	for _, p := range r.Permissions {
		set[p] = true
	}
	for p, on := range add {
		if on {
			set[p] = true
		}
	}
	for p, on := range remove {
		if on {
			delete(set, p)
		}
	}
	perms := make([]string, 0, len(set))
	for p := range set {
		perms = append(perms, p)
	}
	sort.Strings(perms)
	updated, err := s.Auth.UpdateRole(ctx, caller, r.ID, map[string]any{"permissions": perms})
	if err != nil {
		return authError(err)
	}
	got := append([]string(nil), updated.Permissions...)
	sort.Strings(got)
	out := wrap(fmt.Sprintf("%s%s%s now has: ", bold, updated.Name, bold), got)
	if len(got) == 0 {
		out = []string{fmt.Sprintf("%s%s%s now has no permissions.", bold, updated.Name, bold)}
	}
	if touchesIRC(perms) || touchesIRC(r.Permissions) {
		out = append(out, "IRC operator powers change for connected members within a few seconds (when Ergo reloads).")
		if r.IRC.Oper != updated.IRC.Oper {
			members, _ := s.Auth.Members(ctx, r.ID, 10000)
			s.push(ctx, usernames(members)...)
		}
	}
	return out
}

func touchesIRC(perms []string) bool {
	for _, p := range perms {
		if strings.HasPrefix(p, "irc.") {
			return true
		}
	}
	return false
}

func (s *Service) cmdAssign(ctx context.Context, caller string, args []string, _ string) []string {
	return s.membership(ctx, caller, args, true)
}

func (s *Service) cmdRemove(ctx context.Context, caller string, args []string, _ string) []string {
	return s.membership(ctx, caller, args, false)
}

func (s *Service) membership(ctx context.Context, caller string, args []string, assign bool) []string {
	verb := map[bool]string{true: "ASSIGN", false: "REMOVE"}[assign]
	if len(args) < 2 {
		return []string{"Syntax: " + verb + " <account> <role>"}
	}
	account := args[0]
	list, err := s.Auth.List(ctx)
	if err != nil {
		return authError(err)
	}
	r, ok := roles.Find(list, strings.Join(args[1:], " "))
	if !ok {
		return []string{fmt.Sprintf("No role %q. /msg RoleServ LIST", strings.Join(args[1:], " "))}
	}
	if r.Builtin != nil && *r.Builtin == "member" {
		return []string{"Everyone has Member; it can't be assigned or removed."}
	}
	u, err := s.Auth.User(ctx, account)
	if errors.Is(err, roles.ErrNotFound) {
		return []string{fmt.Sprintf("No account %q (accounts, not nicks: see /msg RoleServ ROLES <nick>).", account)}
	} else if err != nil {
		return authError(err)
	}
	// PUT replaces the user's roles: their current ones (by name) +/- this.
	var ids []string
	has := false
	for _, name := range u.Roles {
		cur, ok := roles.Find(list, name)
		if !ok {
			continue
		}
		if cur.ID == r.ID {
			has = true
			continue
		}
		ids = append(ids, cur.ID)
	}
	switch {
	case assign && has:
		return []string{fmt.Sprintf("%s already has %s%s%s.", u.Username, bold, r.Name, bold)}
	case !assign && !has:
		return []string{fmt.Sprintf("%s doesn't have %s%s%s.", u.Username, bold, r.Name, bold)}
	case assign:
		ids = append(ids, r.ID)
	}
	if err := s.Auth.SetUserRoles(ctx, caller, u.UserID, ids); err != nil {
		return authError(err)
	}
	s.push(ctx, u.Username)
	if assign {
		return []string{fmt.Sprintf("%s now has %s%s%s.", u.Username, bold, r.Name, bold)}
	}
	return []string{fmt.Sprintf("%s no longer has %s%s%s.", u.Username, bold, r.Name, bold)}
}

// push brings each account's oper class, WHOIS line and vhost on Ergo in
// line with auth right away (connected clients included). Accounts whose
// class Ergo doesn't know yet are retried in the background.
func (s *Service) push(ctx context.Context, accounts ...string) {
	if len(accounts) == 0 {
		return
	}
	pending := s.pushOnce(ctx, accounts)
	if len(pending) == 0 || len(s.RetryDelays) == 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for _, d := range s.RetryDelays {
			time.Sleep(d)
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			pending = s.pushOnce(rctx, pending)
			cancel()
			if len(pending) == 0 {
				return
			}
		}
		s.logf("push: gave up on %v (they get it at their next login)", pending)
	}()
}

func (s *Service) pushOnce(ctx context.Context, accounts []string) (retry []string) {
	list, err := s.Auth.List(ctx)
	if err != nil {
		s.logf("push: roles: %v", err)
		return accounts
	}
	classes := roles.Classes(list, s.BaseCaps)
	for _, a := range accounts {
		u, err := s.Auth.User(ctx, a)
		var g roles.Grant
		switch {
		case err == nil && u.Disabled:
			g = roles.None()
		case err == nil:
			g = roles.GrantFor(classes, u.IRC)
			if u.IRC.OperClass != nil && *u.IRC.OperClass != "" && *g.OperClass == "" {
				retry = append(retry, a) // class not rendered yet
			}
		case errors.Is(err, roles.ErrNotFound):
			g = roles.None()
		default:
			s.logf("push: %s: %v", a, err)
			retry = append(retry, a)
			continue
		}
		if _, err := s.Ergo.ApplyGrants(ctx, a, g); err != nil {
			if ergoapi.IsCode(err, "UNKNOWN_OPER_CLASS") {
				retry = append(retry, a)
			} else if !ergoapi.IsCode(err, "ACCOUNT_DOES_NOT_EXIST") {
				s.logf("push: %s: %v", a, err)
			}
		}
	}
	return dedupe(retry)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func usernames(members []roles.Member) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.Username)
	}
	return out
}

func authError(err error) []string {
	var api *roles.APIError
	switch {
	case errors.As(err, &api) && (api.Status == 401 || api.Status == 403):
		switch api.Code {
		case "role_hierarchy":
			return []string{"Permission denied: you can only manage roles (and people) below your highest role."}
		case "permission_not_held":
			return []string{"Permission denied: you can only grant permissions you have yourself."}
		}
		msg := api.Message
		if msg == "" {
			msg = "you can't do that"
		}
		return []string{"Permission denied: " + msg + ". (Managing roles needs roles.manage, and you can only manage roles below your highest role.)"}
	case errors.As(err, &api) && api.Status == 409:
		return []string{"Conflict: " + orDefault(api.Message, "that already exists") + "."}
	case errors.As(err, &api) && api.Status == 400:
		return []string{"Invalid: " + orDefault(api.Message, "the auth service rejected that") + "."}
	case errors.Is(err, roles.ErrUnsupported):
		return []string{"This network's auth service doesn't support that yet."}
	case errors.Is(err, roles.ErrNotFound):
		return []string{"Not found."}
	default:
		return []string{"The auth service isn't answering right now; try again later."}
	}
}

// tokenize splits on spaces, honouring "double quotes".
func tokenize(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote, have = !inQuote, true
		case r == ' ' && !inQuote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if inQuote {
		return nil, errors.New("Unbalanced quote.")
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}

func wrap(prefix string, items []string) []string {
	var out []string
	line := prefix
	for _, it := range items {
		sep := ""
		if line != prefix {
			sep = " "
		}
		if len(line)+len(sep)+len(it) > lineBytes && line != prefix {
			out = append(out, line)
			line = "  " + it
			continue
		}
		line += sep + it
	}
	return append(out, line)
}

func quoteRef(name string) string {
	if strings.ContainsRune(name, ' ') {
		return `"` + name + `"`
	}
	return name
}

func parseBool(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "on", "yes", "true", "1":
		return true, true
	case "off", "no", "false", "0":
		return false, true
	}
	return false, false
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orNone(s string) string { return orDefault(s, "none") }

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ptr[T any](v T) *T { return &v }
