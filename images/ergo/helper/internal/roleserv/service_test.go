package roleserv

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

func s(v string) *string { return &v }

// fakeAuth implements Auth with a tiny role model and the hierarchy rule
// auth enforces: you need roles.manage, and can only touch roles strictly
// below your highest role. acting=false is auth 1.5 (no acting API, no
// internal members list).
type fakeAuth struct {
	mu      sync.Mutex
	acting  bool
	roles   []roles.Role
	members map[string][]string // role id -> usernames
	calls   []string
}

func newFakeAuth() *fakeAuth {
	owner, member := "owner", "member"
	return &fakeAuth{
		acting: true,
		roles: []roles.Role{
			{ID: "r1", Name: "Owner", Slug: "owner", Position: 1000, Builtin: &owner, Permissions: []string{"roles.manage", "irc.kill"},
				IRC: roles.RoleIRC{Oper: true, OperClass: s("owner"), Capabilities: []string{"kill"}, WhoisLine: "is the owner", VHost: "owner/{username}"}},
			{ID: "r2", Name: "Moderator", Slug: "moderator", Position: 500, Permissions: []string{"roles.manage", "irc.kill", "irc.ban"},
				IRC: roles.RoleIRC{Oper: true, OperClass: s("moderator"), Capabilities: []string{"ban", "kill"}, WhoisLine: "is a Moderator"}, MemberCount: 1},
			{ID: "r3", Name: "Senior DJ", Slug: "senior-dj", Position: 10, Permissions: []string{"voice.create"}, IRC: roles.RoleIRC{VHost: "dj/{username}", WhoisLine: "is a Senior DJ"}, MemberCount: 2},
			{ID: "r4", Name: "Member", Slug: "member", Position: 0, Builtin: &member, Permissions: []string{"channel.create"}},
		},
		members: map[string][]string{"r1": {"olivia"}, "r2": {"mike"}, "r3": {"dana", "erin"}},
	}
}

func (f *fakeAuth) find(id string) *roles.Role {
	for i := range f.roles {
		if f.roles[i].ID == id {
			return &f.roles[i]
		}
	}
	return nil
}

func (f *fakeAuth) rolesOf(user string) []*roles.Role {
	var out []*roles.Role
	for id, ms := range f.members {
		for _, m := range ms {
			if strings.EqualFold(m, user) {
				out = append(out, f.find(id))
			}
		}
	}
	return out
}

func (f *fakeAuth) perms(user string) []string {
	var out []string
	for _, r := range f.rolesOf(user) {
		out = append(out, r.Permissions...)
	}
	return out
}

func (f *fakeAuth) authorize(actor string, target *roles.Role) error {
	top, manage := -1, false
	for _, r := range f.rolesOf(actor) {
		top = max(top, r.Position)
		manage = manage || r.HasPermission("roles.manage")
	}
	if !manage {
		return &roles.APIError{Status: 403, Code: "forbidden", Message: "missing permission roles.manage"}
	}
	if target != nil && target.Position >= top {
		return &roles.APIError{Status: 403, Code: "role_hierarchy", Message: "role hierarchy"}
	}
	return nil
}

func (f *fakeAuth) List(context.Context) ([]roles.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]roles.Role(nil), f.roles...), nil
}

func (f *fakeAuth) Members(_ context.Context, id string, _ int) ([]roles.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.acting {
		return nil, roles.ErrUnsupported
	}
	var out []roles.Member
	for _, m := range f.members[id] {
		out = append(out, roles.Member{Username: m})
	}
	return out, nil
}

func (f *fakeAuth) User(_ context.Context, u string) (roles.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u == "ghost" {
		return roles.User{}, roles.ErrNotFound
	}
	out := roles.User{UserID: "usr_" + strings.ToLower(u), Username: strings.ToLower(u), Permissions: f.perms(u), Disabled: u == "zed"}
	var best, vh *roles.Role
	for _, r := range f.rolesOf(u) {
		out.Roles = append(out.Roles, r.Name)
		if r.IRC.Oper && (best == nil || r.Position > best.Position) {
			best = r
		}
		if r.IRC.VHost != "" && (vh == nil || r.Position > vh.Position) {
			vh = r
		}
	}
	if best != nil {
		out.IRC.OperClass, out.IRC.WhoisLine = s(best.Slug), best.IRC.WhoisLine
	}
	if vh != nil {
		out.IRC.VHost = s(strings.ReplaceAll(vh.IRC.VHost, "{username}", strings.ToLower(u)))
	}
	return out, nil
}

func (f *fakeAuth) Me(_ context.Context, a string) (roles.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.acting {
		return roles.Actor{}, roles.ErrUnsupported
	}
	return roles.Actor{Username: a, Permissions: f.perms(a)}, nil
}

func (f *fakeAuth) CreateRole(_ context.Context, actor, name string) (roles.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create "+actor+" "+name)
	if err := f.authorize(actor, nil); err != nil {
		return roles.Role{}, err
	}
	r := roles.Role{ID: "r" + name, Name: name, Slug: roles.Slug(name), Position: 1}
	f.roles = append(f.roles, r)
	return r, nil
}

func (f *fakeAuth) DeleteRole(_ context.Context, actor, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete "+actor+" "+id)
	if err := f.authorize(actor, f.find(id)); err != nil {
		return err
	}
	for i := range f.roles {
		if f.roles[i].ID == id {
			f.roles = append(f.roles[:i], f.roles[i+1:]...)
			break
		}
	}
	delete(f.members, id)
	return nil
}

func (f *fakeAuth) UpdateRole(_ context.Context, actor, id string, p map[string]any) (roles.Role, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "update "+actor+" "+id)
	r := f.find(id)
	if err := f.authorize(actor, r); err != nil {
		return roles.Role{}, err
	}
	if v, ok := p["color"]; ok {
		c, _ := v.(string)
		r.Color = roles.Color(c)
	}
	if v, ok := p["position"].(int); ok {
		r.Position = v
	}
	if v, ok := p["permissions"].([]string); ok {
		r.Permissions = v
		r.IRC.Capabilities = nil
		for _, perm := range v {
			if c, ok := strings.CutPrefix(perm, "irc."); ok {
				r.IRC.Oper = true
				r.IRC.Capabilities = append(r.IRC.Capabilities, c)
			}
		}
	}
	if irc, ok := p["irc"].(map[string]any); ok {
		if v, ok := irc["whois_line"]; ok {
			w, _ := v.(string)
			r.IRC.WhoisLine = w
		}
		if v, ok := irc["vhost"]; ok {
			w, _ := v.(string)
			r.IRC.VHost = w
		}
		if v, ok := irc["oper"].(bool); ok {
			r.IRC.Oper = v
			if v {
				r.IRC.OperClass = s(r.Slug)
			} else {
				r.IRC.OperClass = nil
			}
		}
	}
	return *r, nil
}

func (f *fakeAuth) SetUserRoles(_ context.Context, actor, userID string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	user := strings.TrimPrefix(userID, "usr_")
	f.calls = append(f.calls, "setroles "+actor+" "+user+" "+strings.Join(ids, ","))
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for id := range f.members {
		had := false
		var keep []string
		for _, m := range f.members[id] {
			if m == user {
				had = true
				continue
			}
			keep = append(keep, m)
		}
		if had != want[id] {
			if err := f.authorize(actor, f.find(id)); err != nil {
				return err
			}
		}
		f.members[id] = keep
	}
	for id := range want {
		if _, ok := f.members[id]; !ok {
			if err := f.authorize(actor, f.find(id)); err != nil {
				return err
			}
		}
		f.members[id] = append(f.members[id], user)
	}
	return nil
}

type fakeErgo struct {
	mu      sync.Mutex
	grants  map[string]roles.Grant
	nicks   map[string]string // nick -> account ("" = not logged in)
	unknown map[string]bool   // oper classes Ergo doesn't have yet
}

func (e *fakeErgo) ApplyGrants(_ context.Context, a string, g roles.Grant) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g.OperClass != nil && e.unknown[*g.OperClass] {
		return 0, &ergoapi.Error{Code: "UNKNOWN_OPER_CLASS"}
	}
	if e.grants == nil {
		e.grants = map[string]roles.Grant{}
	}
	e.grants[a] = g
	return 1, nil
}

func (e *fakeErgo) AccountOf(_ context.Context, nick string) (string, bool, error) {
	a, ok := e.nicks[strings.ToLower(nick)]
	return a, ok, nil
}

func (e *fakeErgo) grant(a string) roles.Grant {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.grants[a]
}

func newTest() (*Service, *fakeAuth, *fakeErgo) {
	a, e := newFakeAuth(), &fakeErgo{nicks: map[string]string{"mikey": "mike", "guest": ""}}
	return &Service{Auth: a, Ergo: e, Burst: 100, Every: time.Millisecond}, a, e
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestReadCommands(t *testing.T) {
	svc, _, _ := newTest()
	ctx := context.Background()
	cases := []struct{ caller, line, want string }{
		{"", "LIST", "need to be logged in"},
		{"erin", "LIST", "Owner"},
		{"erin", "list", "Senior DJ"},
		{"erin", "INFO \"senior dj\"", "vhost: dj/{username}"},
		{"erin", "INFO Senior DJ", "voice.create"},
		{"erin", "INFO moderator", "IRC powers: ban kill"},
		{"erin", "INFO nope", `No role "nope"`},
		{"erin", "WHO Senior DJ", "dana erin"},
		{"erin", "ROLES mikey", "mikey (account mike): Moderator"},
		{"erin", "ROLES olivia", "olivia: Owner"},
		{"erin", "ROLES zed", "account disabled"},
		{"erin", "ROLES guest", "not logged in"},
		{"erin", "ROLES ghost", `No account "ghost"`},
		{"erin", "FROB", "Unknown command FROB"},
		{"erin", "HELP", "LIST"},
		{"erin", "INFO \"unbalanced", "Unbalanced quote"},
	}
	for _, c := range cases {
		if got := joined(svc.Handle(ctx, c.caller, c.line)); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q => %q, want it to contain %q", c.caller, c.line, got, c.want)
		}
	}
	// HELP hides management commands from people who can't use them
	if got := joined(svc.Handle(ctx, "erin", "HELP")); strings.Contains(got, "ASSIGN") {
		t.Errorf("erin sees ASSIGN: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "HELP")); !strings.Contains(got, "ASSIGN") {
		t.Errorf("mike doesn't see ASSIGN: %s", got)
	}
}

func TestManageActsAsCaller(t *testing.T) {
	svc, a, e := newTest()
	ctx := context.Background()

	// erin has no roles.manage: auth refuses, RoleServ says why
	if got := joined(svc.Handle(ctx, "erin", "CREATE Helper")); !strings.Contains(got, "Permission denied") {
		t.Errorf("erin CREATE: %s", got)
	}
	// mike (Moderator, 50) can't touch Owner (100)
	if got := joined(svc.Handle(ctx, "mike", "SET Owner COLOR #ff0000")); !strings.Contains(got, "below your highest role") {
		t.Errorf("mike SET Owner: %s", got)
	}
	// ... but can assign Senior DJ, and the new member's vhost is pushed at once
	if got := joined(svc.Handle(ctx, "mike", "ASSIGN frank Senior DJ")); !strings.Contains(got, "frank now has") {
		t.Errorf("assign: %s", got)
	}
	if g := e.grant("frank"); g.VHost == nil || *g.VHost != "dj/frank" || *g.OperClass != "" {
		t.Errorf("frank's grant: %+v", g)
	}
	if got := joined(svc.Handle(ctx, "mike", "ASSIGN ghost Senior DJ")); !strings.Contains(got, `No account "ghost"`) {
		t.Errorf("ghost: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "SET \"Senior DJ\" WHOIS is spinning records")); !strings.Contains(got, "Updated") {
		t.Errorf("set whois: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "SET Senior DJ VHOST bad vhost!")); !strings.Contains(got, "vhost template") {
		t.Errorf("bad vhost: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "ASSIGN frank Senior DJ")); !strings.Contains(got, "already has") {
		t.Errorf("assign twice: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "ASSIGN frank Member")); !strings.Contains(got, "Everyone has Member") {
		t.Errorf("member: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "PERM Senior DJ +irc.kill +voice.manage_any")); !strings.Contains(got, "irc.kill") || !strings.Contains(got, "Ergo reloads") {
		t.Errorf("perm: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "PERM Senior DJ kill")); !strings.Contains(got, "Syntax") {
		t.Errorf("perm syntax: %s", got)
	}
	if got := joined(svc.Handle(ctx, "mike", "REMOVE frank Senior DJ")); !strings.Contains(got, "no longer has") {
		t.Errorf("remove: %s", got)
	}
	if g := e.grant("frank"); !g.Equal(roles.None()) {
		t.Errorf("frank should have nothing left: %+v", g)
	}

	// every write named the caller's account, never someone else's
	for _, c := range a.calls {
		if f := strings.Fields(c); f[1] != "mike" && f[1] != "erin" {
			t.Errorf("acting account in %q", c)
		}
	}
}

func TestOperRoleChangeRetriesUntilClassExists(t *testing.T) {
	svc, a, e := newTest()
	svc.RetryDelays = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond}
	ctx := context.Background()
	e.unknown = map[string]bool{"senior-dj": true}

	// olivia (Owner) makes Senior DJ an IRC operator role: Ergo doesn't
	// have the class until watch-settings renders it.
	if got := joined(svc.Handle(ctx, "olivia", "SET Senior DJ OPER on")); !strings.Contains(got, "IRC operator") {
		t.Fatalf("set oper: %s", got)
	}
	if g := e.grant("dana"); g.OperClass != nil && *g.OperClass == "senior-dj" {
		t.Fatal("pushed before the class existed?")
	}
	e.mu.Lock()
	e.unknown = nil // rendered + rehashed
	e.mu.Unlock()
	svc.Wait()
	if g := e.grant("dana"); g.OperClass == nil || *g.OperClass != "senior-dj" {
		t.Errorf("dana after retry: %+v", g)
	}
	_ = a
}

func TestReadOnlyWithoutActingAPI(t *testing.T) {
	svc, a, _ := newTest()
	a.acting = false
	ctx := context.Background()
	if got := joined(svc.Handle(ctx, "olivia", "HELP")); strings.Contains(got, "ASSIGN") || !strings.Contains(got, "INFO") {
		t.Errorf("help without acting API: %s", got)
	}
	for _, line := range []string{"CREATE Helpers", "ASSIGN bob Moderator", "SET Moderator COLOR #ffffff", "PERM Moderator +irc.ban", "DELETE Moderator"} {
		if got := joined(svc.Handle(ctx, "olivia", line)); !strings.Contains(got, "isn't available on this network yet") {
			t.Errorf("%s: %s", line, got)
		}
	}
	if got := joined(svc.Handle(ctx, "olivia", "WHO Moderator")); !strings.Contains(got, "has 1 member") {
		t.Errorf("who: %s", got)
	}
	if got := joined(svc.Handle(ctx, "olivia", "INFO Moderator")); !strings.Contains(got, "IRC powers: ban kill") {
		t.Errorf("info: %s", got)
	}
	if len(a.calls) != 0 {
		t.Errorf("writes reached auth: %v", a.calls)
	}
}

func TestDeletePushesFormerMembers(t *testing.T) {
	svc, _, e := newTest()
	ctx := context.Background()
	if got := joined(svc.Handle(ctx, "olivia", "DELETE Moderator")); !strings.Contains(got, "Deleted") {
		t.Fatalf("delete: %s", got)
	}
	if g := e.grant("mike"); !g.Equal(roles.None()) {
		t.Errorf("mike after delete: %+v", g)
	}
}

func TestRateLimit(t *testing.T) {
	svc, _, _ := newTest()
	svc.Burst, svc.Every = 2, time.Hour
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if got := joined(svc.Handle(ctx, "erin", "LIST")); strings.Contains(got, "Slow down") {
			t.Fatal("limited too early")
		}
	}
	if got := joined(svc.Handle(ctx, "erin", "LIST")); !strings.Contains(got, "Slow down") {
		t.Fatal("not limited")
	}
	if got := joined(svc.Handle(ctx, "dana", "LIST")); strings.Contains(got, "Slow down") {
		t.Fatal("limit is per account")
	}
}

func TestParse(t *testing.T) {
	m := Parse(`@account=alice;msgid=abc\sdef;+typing=active :alice!~a@host PRIVMSG RoleServ :INFO "Senior DJ"`)
	if m.Tags["account"] != "alice" || m.Tags["msgid"] != "abc def" || m.Nick() != "alice" || m.Command != "PRIVMSG" ||
		m.Param(0) != "RoleServ" || m.Param(1) != `INFO "Senior DJ"` {
		t.Errorf("%+v", m)
	}
	m = Parse(":irc.test CAP * LS * :sasl account-tag")
	if m.Param(1) != "LS" || m.Param(2) != "*" || m.Param(3) != "sasl account-tag" {
		t.Errorf("%+v", m)
	}
}
