package rolechannels

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

type fakeAuth struct {
	mu       sync.Mutex
	feed     roles.ChannelAccessFeed
	err      error
	autojoin map[string]roles.Autojoin
	recorded map[string][]string
	calls    int
}

func (a *fakeAuth) ChannelAccess(_ context.Context, etag string) (roles.ChannelAccessFeed, string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return roles.ChannelAccessFeed{}, "", false, a.err
	}
	tag := `"` + a.feed.Version + `"`
	if etag == tag {
		return roles.ChannelAccessFeed{}, etag, true, nil
	}
	return a.feed, tag, false, nil
}

func (a *fakeAuth) Autojoin(_ context.Context, account string) (roles.Autojoin, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	aj, ok := a.autojoin[strings.ToLower(account)]
	if !ok {
		return aj, roles.ErrNotFound
	}
	aj.Enabled, aj.Rejoin = a.feed.Autojoin, a.feed.Rejoin
	for _, ch := range aj.Channels {
		if !slices.Contains(a.recorded[strings.ToLower(account)], ch) {
			aj.Pending = append(aj.Pending, ch)
		}
	}
	aj.Network = a.feed.Network.AutojoinChannels()
	for _, ch := range aj.Network {
		if !slices.Contains(a.recorded[strings.ToLower(account)], ch) {
			aj.NetworkPending = append(aj.NetworkPending, ch)
		}
	}
	return aj, nil
}

func (a *fakeAuth) RecordAutojoin(ctx context.Context, account string, channels []string) (roles.Autojoin, error) {
	a.mu.Lock()
	if a.recorded == nil {
		a.recorded = map[string][]string{}
	}
	a.recorded[strings.ToLower(account)] = append(a.recorded[strings.ToLower(account)], channels...)
	a.mu.Unlock()
	return a.Autojoin(ctx, account)
}

type fakeErgo struct {
	mu       sync.Mutex
	exists   map[string]bool
	modes    map[string]string // channel → "+i", "+R", ""
	invex    map[string][]string
	online   map[string]bool
	joined   map[string][]string // account → channels
	failNext bool
	pushes   int
	founders map[string]string // registered channel → founder
	topics   map[string]string
	ensures  int
	dropped  []string
	guests   map[string]bool     // connected nicks without an account
	accounts map[string]string   // connected nick → account
	nickJoin map[string][]string // nick → channels (SajoinNick)
}

func (e *fakeErgo) EnsureChannel(_ context.Context, req ergoapi.ChannelEnsure) (ergoapi.ChannelEnsureResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensures++
	if e.failNext {
		e.failNext = false
		return ergoapi.ChannelEnsureResult{}, errors.New("boom")
	}
	ch := strings.ToLower(req.Channel)
	var res ergoapi.ChannelEnsureResult
	if !e.exists[ch] {
		e.exists[ch], res.Created = true, true
	}
	if e.founders[ch] == "" {
		e.founders[ch], res.Registered = strings.ToLower(req.Founder), true
	}
	res.Founder = e.founders[ch]
	if req.SetTopic && e.topics[ch] != req.Topic {
		e.topics[ch], res.TopicChanged = req.Topic, true
	}
	return res, nil
}

func (e *fakeErgo) DropChannel(_ context.Context, channel, founder string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.founders[channel] != strings.ToLower(founder) {
		return false, nil
	}
	delete(e.founders, channel)
	e.dropped = append(e.dropped, channel)
	return true, nil
}

func (e *fakeErgo) AccountOf(_ context.Context, nick string) (string, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.accounts[nick]; ok {
		return a, true, nil
	}
	return "", e.guests[nick], nil
}

func (e *fakeErgo) SajoinNick(_ context.Context, nick string, channels []string) (ergoapi.SajoinResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.guests[nick] && e.accounts[nick] == "" {
		return ergoapi.SajoinResult{Joined: []string{}}, nil
	}
	for _, ch := range channels {
		if !e.exists[ch] {
			panic("SAJOIN to a channel that wasn't ensured: " + ch)
		}
	}
	e.nickJoin[nick] = append(e.nickJoin[nick], channels...)
	return ergoapi.SajoinResult{Clients: 1, Joined: channels}, nil
}

func newErgo() *fakeErgo {
	return &fakeErgo{exists: map[string]bool{}, modes: map[string]string{}, invex: map[string][]string{}, online: map[string]bool{}, joined: map[string][]string{},
		founders: map[string]string{}, topics: map[string]string{}, guests: map[string]bool{}, accounts: map[string]string{}, nickJoin: map[string][]string{}}
}

func (e *fakeErgo) SetChannelAccess(_ context.Context, req ergoapi.ChannelAccess) (ergoapi.ChannelAccessResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pushes++
	if e.failNext {
		e.failNext = false
		return ergoapi.ChannelAccessResult{}, errors.New("boom")
	}
	if !e.exists[req.Channel] {
		return ergoapi.ChannelAccessResult{}, nil
	}
	m := e.modes[req.Channel]
	set := func(on *bool, flag string) {
		if on == nil {
			return
		}
		m = strings.ReplaceAll(m, flag, "")
		if *on {
			m += flag
		}
	}
	set(req.InviteOnly, "i")
	set(req.RegisteredOnly, "R")
	e.modes[req.Channel] = m
	var added, removed []string
	for _, a := range req.Accounts {
		if !slices.Contains(e.invex[req.Channel], a) {
			added = append(added, a)
		}
	}
	for _, a := range e.invex[req.Channel] {
		if !slices.Contains(req.Accounts, a) {
			removed = append(removed, a)
		}
	}
	e.invex[req.Channel] = slices.Clone(req.Accounts)
	return ergoapi.ChannelAccessResult{Exists: true, Registered: true, Added: added, Removed: removed}, nil
}

func (e *fakeErgo) Sajoin(_ context.Context, account string, channels []string) (ergoapi.SajoinResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.online[account] {
		return ergoapi.SajoinResult{Joined: []string{}}, nil
	}
	e.joined[account] = append(e.joined[account], channels...)
	return ergoapi.SajoinResult{Clients: 1, Joined: channels}, nil
}

func setup(t *testing.T) (*Syncer, *fakeAuth, *fakeErgo) {
	auth := &fakeAuth{autojoin: map[string]roles.Autojoin{}}
	auth.feed = roles.ChannelAccessFeed{Version: "1", Rejoin: "login"}
	ergo := newErgo()
	s := &Syncer{Auth: auth, Ergo: ergo, SetBy: "RoleServ", StateFile: filepath.Join(t.TempDir(), "state", "rc.json"), Logf: t.Logf}
	return s, auth, ergo
}

func TestPrivateChannels(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	ergo.exists["#staff"], ergo.exists["#all"] = true, true
	auth.feed = roles.ChannelAccessFeed{Version: "2", Rejoin: "login", Channels: []roles.ChannelAccess{
		{Channel: "#staff", Private: true, Accounts: []string{"alice", "bob"}},
		{Channel: "#all", Private: true, Everyone: true, Accounts: []string{}},
		{Channel: "#lobby", Accounts: []string{}},
		{Channel: "#later", Private: true, Accounts: []string{"alice"}},
	}}
	if !s.Tick(ctx, false) {
		t.Fatal("tick failed")
	}
	if ergo.modes["#staff"] != "i" || fmt.Sprint(ergo.invex["#staff"]) != "[alice bob]" || ergo.modes["#all"] != "R" {
		t.Fatalf("modes %v invex %v", ergo.modes, ergo.invex)
	}
	if _, ok := ergo.modes["#lobby"]; ok {
		t.Fatal("public channel touched")
	}
	if fmt.Sprint(s.Managed()) != "map[#all:registered #staff:invite]" {
		t.Fatalf("managed %v", s.Managed())
	}
	// Unchanged: one ETag poll, no pushes.
	n := ergo.pushes
	s.Tick(ctx, false)
	if ergo.pushes != n {
		t.Fatal("pushed without a change")
	}
	// A full resync pushes again, and picks up #later once it exists.
	ergo.exists["#later"] = true
	s.Tick(ctx, true)
	if ergo.modes["#later"] != "i" {
		t.Fatalf("#later not locked: %v", ergo.modes)
	}

	// Revoke bob; #all is now role-limited; #later public; state survives a restart.
	auth.feed = roles.ChannelAccessFeed{Version: "3", Rejoin: "login", Channels: []roles.ChannelAccess{
		{Channel: "#staff", Private: true, Accounts: []string{"alice"}},
		{Channel: "#all", Private: true, Accounts: []string{"carol"}},
	}}
	s2 := &Syncer{Auth: auth, Ergo: ergo, SetBy: "RoleServ", StateFile: s.StateFile, Logf: t.Logf}
	s2.Tick(ctx, false)
	if fmt.Sprint(ergo.invex["#staff"]) != "[alice]" || ergo.modes["#all"] != "i" || ergo.modes["#later"] != "" {
		t.Fatalf("after revoke: modes %v invex %v", ergo.modes, ergo.invex)
	}
	if fmt.Sprint(s2.Managed()) != "map[#all:invite #staff:invite]" {
		t.Fatalf("managed %v", s2.Managed())
	}

	// A failed push is retried on the next poll (no ETag kept).
	auth.feed.Version = "4"
	auth.feed.Channels[0].Accounts = []string{"alice", "dave"}
	ergo.failNext = true
	s2.Tick(ctx, false)
	s2.Tick(ctx, false)
	if fmt.Sprint(ergo.invex["#staff"]) != "[alice dave]" || fmt.Sprint(ergo.invex["#all"]) != "[carol]" {
		t.Fatalf("failed push not retried: %v", ergo.invex)
	}

	// Everything public: unlocked and forgotten.
	auth.feed = roles.ChannelAccessFeed{Version: "5", Rejoin: "login"}
	s2.Tick(ctx, false)
	if ergo.modes["#staff"] != "" || len(ergo.invex["#staff"]) != 0 || len(s2.Managed()) != 0 {
		t.Fatalf("not unlocked: %v %v %v", ergo.modes, ergo.invex, s2.Managed())
	}
}

func TestFeedErrors(t *testing.T) {
	s, auth, _ := setup(t)
	auth.err = roles.ErrUnsupported
	if s.Tick(context.Background(), false) {
		t.Fatal("unsupported feed reported as read")
	}
	auth.err = errors.New("down")
	if s.Tick(context.Background(), false) {
		t.Fatal("failed feed reported as read")
	}
	// No feed yet: logins do nothing.
	s.Login(context.Background(), "alice")
}

func TestAutojoinLoginAndRoleGain(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	ergo.online["alice"], ergo.online["bob"] = true, true
	auth.autojoin["alice"] = roles.Autojoin{Account: "alice", Channels: []string{"#lobby", "#staff"}}
	auth.autojoin["bob"] = roles.Autojoin{Account: "bob", Channels: []string{"#lobby"}}
	auth.feed = roles.ChannelAccessFeed{Version: "1", Rejoin: "login"}
	s.Tick(ctx, false)

	// Off: nothing, but carol (logged in while off) is remembered.
	ergo.online["carol"] = true
	auth.autojoin["carol"] = roles.Autojoin{Account: "carol", Channels: []string{"#lobby"}}
	s.Login(ctx, "alice")
	s.Login(ctx, "carol")
	if len(ergo.joined["alice"]) != 0 {
		t.Fatal("auto-join while off")
	}
	auth.feed.Version, auth.feed.Autojoin = "2", true
	s.Tick(ctx, false)
	s.Login(ctx, "alice")
	s.Login(ctx, "bob")
	s.Login(ctx, "ghost") // unknown in auth
	if fmt.Sprint(ergo.joined) != "map[alice:[#lobby #staff] bob:[#lobby]]" {
		t.Fatalf("login joins %v", ergo.joined)
	}
	// carol, connected since before auto-join was on, gains #staff.
	auth.autojoin["carol"] = roles.Autojoin{Account: "carol", Channels: []string{"#lobby", "#staff"}}
	auth.feed.Version = "2b"
	s.Tick(ctx, false)
	if fmt.Sprint(ergo.joined["carol"]) != "[#staff]" {
		t.Fatalf("carol role gain %v", ergo.joined["carol"])
	}
	// rejoin: login joins again at every login.
	s.Login(ctx, "alice")
	if len(ergo.joined["alice"]) != 4 {
		t.Fatalf("login rejoin %v", ergo.joined)
	}

	// bob gains #staff and #music: joined to just those.
	auth.autojoin["bob"] = roles.Autojoin{Account: "bob", Channels: []string{"#lobby", "#music", "#staff"}}
	auth.feed.Version = "3"
	s.Tick(ctx, false)
	if fmt.Sprint(ergo.joined["bob"]) != "[#lobby #music #staff]" {
		t.Fatalf("role gain %v", ergo.joined["bob"])
	}
	// alice went offline: she's forgotten when she next gains something.
	ergo.online["alice"] = false
	auth.autojoin["alice"] = roles.Autojoin{Account: "alice", Channels: []string{"#lobby", "#new", "#staff"}}
	auth.feed.Version = "4"
	s.Tick(ctx, false)
	s.mu.Lock()
	_, still := s.online["alice"]
	s.mu.Unlock()
	if still {
		t.Fatal("offline account kept")
	}
}

func TestAutojoinOnce(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	ergo.online["alice"] = true
	auth.autojoin["alice"] = roles.Autojoin{Account: "alice", Channels: []string{"#lobby", "#staff"}}
	auth.feed = roles.ChannelAccessFeed{Version: "1", Autojoin: true, Rejoin: "once"}
	s.Tick(ctx, false)
	s.Login(ctx, "alice")
	// she /parts; next login doesn't rejoin
	s.Login(ctx, "alice")
	if fmt.Sprint(ergo.joined["alice"]) != "[#lobby #staff]" || fmt.Sprint(auth.recorded["alice"]) != "[#lobby #staff]" {
		t.Fatalf("once: joined %v recorded %v", ergo.joined, auth.recorded)
	}
	// a new role channel is still joined once
	auth.autojoin["alice"] = roles.Autojoin{Account: "alice", Channels: []string{"#lobby", "#mods", "#staff"}}
	auth.feed.Version = "2"
	s.Tick(ctx, false)
	s.Login(ctx, "alice")
	if fmt.Sprint(ergo.joined["alice"]) != "[#lobby #staff #mods]" {
		t.Fatalf("once role gain: %v", ergo.joined["alice"])
	}
}
