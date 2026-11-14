package roles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func str(s string) *string { return &s }

// The shape of auth 1.5's GET /internal/v1/roles (plus one role from an
// older auth without slug/oper_class/capabilities, and one without a usable
// name).
const listJSON = `{"version": "5f0c", "roles": [
 {"id":"rol_1","name":"Owner","slug":"owner","color":"#F1C40F","position":1000,"builtin":"owner","permissions":["roles.manage","irc.kill","irc.rehash"],"irc":{"whois_line":"is the network owner","vhost":"owner/{username}","oper":true,"oper_class":"owner","capabilities":["kill","rehash","bogus"]},"hoist":true},
 {"id":"rol_3","name":"Moderator","slug":"moderator","color":3447003,"position":500,"builtin":"moderator","permissions":["irc.kill","irc.ban"],"irc":{"whois_line":"is a Moderator","vhost":null,"oper":true,"oper_class":"moderator","capabilities":["kill","ban","samode"]}},
 {"id":"rol_dj","name":"DJ","slug":"dj","color":null,"position":10,"builtin":null,"permissions":["voice.create"],"irc":{"whois_line":"is a DJ","vhost":"dj/{username}","oper":false,"oper_class":null,"capabilities":[]}},
 {"id":"rol_old","name":"Chat Helper","position":5,"permissions":["irc.samode","IRC.KILL"],"irc":{"oper":true}},
 {"id":"rol_4","name":"Member","slug":"member","position":0,"builtin":"member","permissions":["channel.create"],"irc":{"whois_line":"is a Member","oper":false,"oper_class":null}}
]}`

func TestDecodeAndClasses(t *testing.T) {
	var l List
	if err := json.Unmarshal([]byte(listJSON), &l); err != nil {
		t.Fatal(err)
	}
	if l.Version != "5f0c" || l.Roles[0].Color != "#f1c40f" || l.Roles[1].Color != "#3498db" || l.Roles[2].Color != "" {
		t.Fatalf("decode: %+v", l.Roles[:3])
	}
	cls := Classes(l.Roles, []string{"nofakelag", "bogus"})
	names := []string{}
	for _, c := range cls {
		names = append(names, c.Name)
	}
	// auth's oper_class names; DJ and Member aren't opers; the old-style
	// role gets a slug made from its name.
	want := []string{"owner", "moderator", "chat-helper"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("classes %v, want %v", names, want)
	}
	// auth's effective capabilities (unknown names dropped) + the base
	if got := cls[0].Capabilities; !reflect.DeepEqual(got, []string{"kill", "nofakelag", "rehash"}) {
		t.Errorf("owner caps %v", got)
	}
	if got := cls[1].Capabilities; !reflect.DeepEqual(got, []string{"ban", "kill", "nofakelag", "samode"}) {
		t.Errorf("mod caps %v (from capabilities, not permissions)", got)
	}
	// older auth: derived from irc.* permissions
	if got := cls[2].Capabilities; !reflect.DeepEqual(got, []string{"kill", "nofakelag", "samode"}) {
		t.Errorf("old-style caps %v", got)
	}
	if cls[0].WhoisLine != "is the network owner" || cls[1].WhoisLine != "is a Moderator" || cls[0].Title != "Owner" {
		t.Errorf("whois/title %+v", cls[:2])
	}
	for in, want := range map[string]string{"moderator": "moderator", "Moderator": "moderator", "rol_3": "moderator", "chat-helper": "chat-helper"} {
		if got, ok := ResolveClass(cls, in); !ok || got != want {
			t.Errorf("ResolveClass(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := ResolveClass(cls, "dj"); ok {
		t.Error("dj is not an oper role")
	}
	// a slug that isn't a safe class name is never rendered
	bad := Classes([]Role{{Name: "x", IRC: RoleIRC{Oper: true, OperClass: str("evil\n  key: 1")}}}, nil)
	if len(bad) != 1 || bad[0].Name != "x" {
		t.Errorf("unsafe oper_class: %+v", bad)
	}
}

func TestGrantFor(t *testing.T) {
	var l List
	_ = json.Unmarshal([]byte(listJSON), &l)
	cls := Classes(l.Roles, nil)
	g := GrantFor(cls, UserIRC{OperClass: str("moderator"), WhoisLine: "is a \nModerator", VHost: str("mod/alice")})
	if *g.OperClass != "moderator" || g.WhoisLine != "" || *g.VHost != "mod/alice" {
		t.Errorf("the class's own line is left to the class: %+v", g)
	}
	g = GrantFor(cls, UserIRC{OperClass: str("moderator"), WhoisLine: "is a \nspecial Moderator"})
	if g.WhoisLine != "is a special Moderator" {
		t.Errorf("a different line is granted (cleaned): %+v", g)
	}
	// non-oper role: vhost only, explicit "no oper"
	g = GrantFor(cls, UserIRC{VHost: str("dj/bob")})
	if *g.OperClass != "" || g.WhoisLine != "" || *g.VHost != "dj/bob" {
		t.Errorf("dj grant %+v", g)
	}
	// unknown class, invalid vhost: none of either
	g = GrantFor(cls, UserIRC{OperClass: str("ghost"), WhoisLine: "x", VHost: str("bad vhost")})
	if !g.Equal(None()) {
		t.Errorf("want none, got %+v", g)
	}
	if (Grant{}).Equal(None()) {
		t.Error("no opinion != none")
	}
}

func TestClientRolesETag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/internal/v1/roles":
			if r.Header.Get("If-None-Match") == `"v7"` {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", `"v7"`)
			_, _ = w.Write([]byte(listJSON))
		case "/internal/v1/users/carl/irc":
			_, _ = w.Write([]byte(`{"user_id":"usr_1","username":"carl","disabled":true,"irc":{"oper_class":null,"whois_line":null,"vhost":"users/carl"},"roles":[],"permissions":["channel.create"]}`))
		case "/internal/v1/users/ghost/irc":
			w.WriteHeader(404)
			// auth 1.5 shape: the code only in "error".
			_, _ = w.Write([]byte(`{"error":"not_found","message":"no such user"}`))
		case "/internal/v1/acting/roles":
			if r.Header.Get("X-Acting-Account") != "alice" {
				t.Errorf("acting account %q", r.Header.Get("X-Acting-Account"))
			}
			w.WriteHeader(403)
			// auth 1.6 shape: code in both "error" and "code", text in "message".
			_, _ = w.Write([]byte(`{"error":"role_hierarchy","code":"role_hierarchy","message":"you can only manage roles below your own"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.URL, "tok", "")
	ctx := context.Background()
	l, etag, nm, err := c.Roles(ctx, "")
	if err != nil || nm || etag != `"v7"` || len(l.Roles) != 5 {
		t.Fatalf("%v %v %q %d", err, nm, etag, len(l.Roles))
	}
	if _, _, nm, err = c.Roles(ctx, etag); err != nil || !nm {
		t.Fatalf("want 304: %v %v", nm, err)
	}
	if u, err := c.User(ctx, "carl"); err != nil || !u.Disabled || *u.IRC.VHost != "users/carl" || !u.Has("channel.create") {
		t.Errorf("carl: %+v %v", u, err)
	}
	if _, err := c.User(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, _, _, err := c.Grants(ctx, ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("want ErrUnsupported for a missing route, got %v", err)
	}
	_, err = c.CreateRole(ctx, "alice", "DJ")
	var api *APIError
	if !errors.As(err, &api) || api.Status != 403 || api.Code != "role_hierarchy" || api.Message != "you can only manage roles below your own" {
		t.Errorf("want APIError 403, got %v", err)
	}
	if _, err := c.Me(ctx, "alice"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no acting API: want ErrUnsupported, got %v", err)
	}
}
