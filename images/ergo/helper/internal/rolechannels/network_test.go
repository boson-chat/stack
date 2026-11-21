package rolechannels

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

func networkFeed(version string, channels ...roles.NetworkChannel) roles.ChannelAccessFeed {
	return roles.ChannelAccessFeed{Version: version, Rejoin: "login", Network: roles.NetworkChannels{
		Founder: "thelion", AutojoinGuests: true, Channels: channels,
	}}
}

func TestNetworkChannelsEnsured(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	ergo.exists["#taken"], ergo.founders["#taken"] = true, "someone"
	auth.feed = networkFeed("1",
		roles.NetworkChannel{Channel: "#lobby", Name: "#Lobby", Topic: "Welcome", Autojoin: true},
		roles.NetworkChannel{Channel: "#help", Name: "#help"},
		roles.NetworkChannel{Channel: "#taken", Name: "#taken"},
	)
	s.Tick(ctx, false)
	if ergo.founders["#lobby"] != "thelion" || ergo.founders["#help"] != "thelion" || ergo.topics["#lobby"] != "Welcome" {
		t.Fatalf("founders %v topics %v", ergo.founders, ergo.topics)
	}
	if ergo.founders["#taken"] != "someone" {
		t.Fatalf("took over #taken: %v", ergo.founders)
	}
	if _, ok := ergo.topics["#help"]; ok {
		t.Fatal("empty topic was set on #help")
	}

	// A channel op changes the topic: a resync doesn't put it back.
	ergo.topics["#lobby"] = "changed on IRC"
	s.Tick(ctx, true)
	if ergo.topics["#lobby"] != "changed on IRC" {
		t.Fatalf("resync clobbered the topic: %q", ergo.topics["#lobby"])
	}
	// The admin changes it in the config: it's set.
	auth.feed = networkFeed("2",
		roles.NetworkChannel{Channel: "#lobby", Name: "#Lobby", Topic: "New topic", Autojoin: true},
		roles.NetworkChannel{Channel: "#taken", Name: "#taken"},
	)
	s.Tick(ctx, false)
	if ergo.topics["#lobby"] != "New topic" {
		t.Fatalf("topic: %q", ergo.topics["#lobby"])
	}
	// #help was removed: RoleServ registered it, so it's dropped; #taken
	// (someone else's) never is.
	if !slices.Equal(ergo.dropped, []string{"#help"}) {
		t.Fatalf("dropped %v", ergo.dropped)
	}
	auth.feed = networkFeed("3")
	s.Tick(ctx, false)
	if slices.Contains(ergo.dropped, "#taken") || ergo.founders["#taken"] != "someone" {
		t.Fatalf("dropped someone else's channel: %v", ergo.dropped)
	}

	// The state survives a restart: a restarted RoleServ still knows it
	// registered #lobby.
	auth.feed = networkFeed("4", roles.NetworkChannel{Channel: "#lobby", Name: "#Lobby", Topic: "New topic", Autojoin: true})
	s.Tick(ctx, false)
	s2 := &Syncer{Auth: auth, Ergo: ergo, SetBy: "RoleServ", StateFile: s.StateFile, Logf: t.Logf}
	if st := s2.Network()["#lobby"]; st.Topic != "New topic" {
		t.Fatalf("state after restart: %+v", s2.Network())
	}
}

func TestNetworkChannelsFounderFallback(t *testing.T) {
	s, auth, ergo := setup(t)
	auth.feed = networkFeed("1", roles.NetworkChannel{Channel: "#lobby", Autojoin: true})
	auth.feed.Network.Founder = ""
	s.Tick(context.Background(), false)
	if ergo.founders["#lobby"] != "roleserv" {
		t.Fatalf("founder: %v", ergo.founders)
	}
}

func TestNetworkAutojoinFirstLogin(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	// Role auto-join is off: network channels are joined anyway, once.
	auth.feed = networkFeed("1",
		roles.NetworkChannel{Channel: "#lobby", Name: "#lobby", Autojoin: true},
		roles.NetworkChannel{Channel: "#quiet", Name: "#quiet"},
	)
	auth.autojoin["alice"] = roles.Autojoin{Account: "alice", Channels: []string{"#staff"}}
	ergo.online["alice"] = true
	s.Tick(ctx, false)

	s.Login(ctx, "alice")
	if got := ergo.joined["alice"]; !slices.Equal(got, []string{"#lobby"}) {
		t.Fatalf("first login joined %v", got)
	}
	if got := auth.recorded["alice"]; !slices.Equal(got, []string{"#lobby"}) {
		t.Fatalf("recorded %v", got)
	}
	// alice parts #lobby and logs in again: not joined again.
	s.Login(ctx, "alice")
	if got := ergo.joined["alice"]; len(got) != 1 {
		t.Fatalf("second login joined again: %v", got)
	}

	// A new network auto-join channel reaches connected accounts.
	auth.feed = networkFeed("2",
		roles.NetworkChannel{Channel: "#lobby", Name: "#lobby", Autojoin: true},
		roles.NetworkChannel{Channel: "#news", Name: "#news", Autojoin: true},
	)
	s.Tick(ctx, false)
	if got := ergo.joined["alice"]; !slices.Equal(got, []string{"#lobby", "#news"}) {
		t.Fatalf("after adding #news: %v", got)
	}
}

func TestNetworkAutojoinWithRoleChannels(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	auth.feed = networkFeed("1", roles.NetworkChannel{Channel: "#lobby", Name: "#lobby", Autojoin: true})
	auth.feed.Autojoin = true // rejoin: login
	auth.autojoin["bob"] = roles.Autojoin{Account: "bob", Channels: []string{"#staff"}}
	ergo.online["bob"] = true
	s.Tick(ctx, false)
	s.Login(ctx, "bob")
	s.Login(ctx, "bob")
	// role channels every login (rejoin: login), #lobby only the first time
	if got := ergo.joined["bob"]; !slices.Equal(got, []string{"#staff", "#lobby", "#staff"}) {
		t.Fatalf("joined %v", got)
	}
	if got := auth.recorded["bob"]; !slices.Equal(got, []string{"#lobby"}) {
		t.Fatalf("recorded %v (role channels aren't recorded with rejoin: login)", got)
	}
}

func TestGuestAutojoin(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	auth.feed = networkFeed("1",
		roles.NetworkChannel{Channel: "#lobby", Name: "#lobby", Autojoin: true},
		roles.NetworkChannel{Channel: "#quiet", Name: "#quiet"},
	)
	ergo.guests["Guest1"] = true
	ergo.accounts["alice"] = "alice"

	// Before the feed is loaded: nothing (and no SAJOIN to unknown channels).
	s.Connect(ctx, "Guest1")
	if len(ergo.nickJoin) != 0 {
		t.Fatalf("joined before the feed: %v", ergo.nickJoin)
	}
	s.Tick(ctx, false)
	s.Connect(ctx, "Guest1")
	s.Connect(ctx, "alice")    // logged in: Login's job
	s.Connect(ctx, "gone")     // disconnected already
	s.Connect(ctx, "RoleServ") // itself
	if !slices.Equal(ergo.nickJoin["Guest1"], []string{"#lobby"}) || len(ergo.nickJoin) != 1 {
		t.Fatalf("guest joins: %v", ergo.nickJoin)
	}
	// Reconnecting within 24 h: not joined again; after: joined again.
	s.Connect(ctx, "guest1")
	now = now.Add(23 * time.Hour)
	s.Connect(ctx, "Guest1")
	if len(ergo.nickJoin["Guest1"]) != 1 {
		t.Fatalf("rejoined within 24h: %v", ergo.nickJoin)
	}
	now = now.Add(2 * time.Hour)
	s.Connect(ctx, "Guest1")
	if len(ergo.nickJoin["Guest1"]) != 2 {
		t.Fatalf("not rejoined after 24h: %v", ergo.nickJoin)
	}

	// autojoin_guests off.
	auth.feed = networkFeed("2", roles.NetworkChannel{Channel: "#lobby", Name: "#lobby", Autojoin: true})
	auth.feed.Network.AutojoinGuests = false
	s.Tick(ctx, false)
	ergo.guests["Guest2"] = true
	s.Connect(ctx, "Guest2")
	if len(ergo.nickJoin["Guest2"]) != 0 {
		t.Fatalf("guest joined with autojoin_guests off: %v", ergo.nickJoin)
	}
}

func TestOldAuthHasNoNetworkChannels(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	auth.feed = roles.ChannelAccessFeed{Version: "1", Rejoin: "login"} // no network key
	ergo.guests["Guest1"] = true
	s.Tick(ctx, false)
	s.Connect(ctx, "Guest1")
	if ergo.ensures != 0 || len(ergo.nickJoin) != 0 {
		t.Fatalf("ensures %d joins %v", ergo.ensures, ergo.nickJoin)
	}
}

func TestNetworkEnsureFailureRetried(t *testing.T) {
	s, auth, ergo := setup(t)
	ctx := context.Background()
	auth.feed = networkFeed("1", roles.NetworkChannel{Channel: "#lobby", Name: "#lobby", Autojoin: true})
	ergo.failNext = true
	ergo.guests["Guest1"] = true
	s.Tick(ctx, false)
	// not ensured: guests aren't SAJOINed (that would create it unregistered)
	s.Connect(ctx, "Guest1")
	if len(ergo.nickJoin) != 0 {
		t.Fatalf("joined an unensured channel: %v", ergo.nickJoin)
	}
	s.Tick(ctx, false) // etag was dropped, so this retries
	if ergo.founders["#lobby"] != "thelion" {
		t.Fatalf("not retried: %v", ergo.founders)
	}
}
