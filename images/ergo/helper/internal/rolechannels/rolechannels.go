// Package rolechannels is RoleServ's enforcement of role channels (auth
// GET /internal/v1/irc/channel-access, see docs/roles.md):
//
//   - private channels are kept +i with an account invite exception
//     (+I a:<account>, patched Ergo) for every member of a role that lists
//     them, through Ergo's API (POST /v1/channel_access). Exceptions are
//     removed when access is revoked; nobody is kicked. A private channel
//     listed on everyone is +R instead (any logged-in account).
//   - with roles.channels.autojoin on, members are joined (POST /v1/sajoin)
//     to their roles' auto-join channels when they log in (RoleServ sees
//     logins through Ergo's ACCOUNT snomask) and when they gain a role.
//
// The channels RoleServ made private are remembered in a state file, so a
// channel made public again gets its +i (and RoleServ's exceptions) removed
// even across restarts.
package rolechannels

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// Auth is what the syncer needs from auth (*roles.Client).
type Auth interface {
	ChannelAccess(ctx context.Context, etag string) (roles.ChannelAccessFeed, string, bool, error)
	Autojoin(ctx context.Context, account string) (roles.Autojoin, error)
	RecordAutojoin(ctx context.Context, account string, channels []string) (roles.Autojoin, error)
}

// Ergo is what it needs from Ergo's API (*ergoapi.Client).
type Ergo interface {
	SetChannelAccess(ctx context.Context, req ergoapi.ChannelAccess) (ergoapi.ChannelAccessResult, error)
	Sajoin(ctx context.Context, account string, channels []string) (ergoapi.SajoinResult, error)
}

// How RoleServ locks a private channel.
const (
	modeInvite     = "invite"     // +i, +I a:<account> per member
	modeRegistered = "registered" // +R (listed on everyone)

	rejoinOnce = "once"
)

// Syncer keeps Ergo in line with the feed.
type Syncer struct {
	Auth Auth
	Ergo Ergo
	// SetBy names RoleServ as the creator of the exceptions it adds (only
	// those are ever removed).
	SetBy string
	// StateFile remembers the channels RoleServ locked ("" = memory only).
	StateFile string
	// Poll is how often the feed is polled (ETag); Resync how often every
	// private channel is pushed again anyway (an unregistered channel that
	// emptied and was created again lost its modes).
	Poll, Resync time.Duration
	Logf         func(string, ...any)

	mu        sync.Mutex
	etag      string
	feed      roles.ChannelAccessFeed
	have      bool
	managed   map[string]string   // channel → mode
	missing   map[string]bool     // private channels that don't exist (logged once)
	online    map[string][]string // folded account → auto-join channels when last checked
	unsupport bool
	loaded    bool
	sem       chan struct{}
}

// maxConcurrentLogins bounds the logins handled at once (a restart
// reconnects everyone).
const maxConcurrentLogins = 8

func (s *Syncer) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

type stateFile struct {
	Managed map[string]string `json:"managed"`
}

func (s *Syncer) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.managed, s.missing, s.online = map[string]string{}, map[string]bool{}, map[string][]string{}
	if s.StateFile == "" {
		return
	}
	b, err := os.ReadFile(s.StateFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("role channels: reading %s: %v", s.StateFile, err)
		}
		return
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		s.logf("role channels: %s: %v", s.StateFile, err)
		return
	}
	for ch, m := range st.Managed {
		s.managed[ch] = m
	}
}

func (s *Syncer) save() {
	if s.StateFile == "" {
		return
	}
	b, _ := json.Marshal(stateFile{Managed: s.managed})
	tmp := s.StateFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.StateFile), 0o700); err == nil {
		if err = os.WriteFile(tmp, append(b, '\n'), 0o600); err == nil {
			err = os.Rename(tmp, s.StateFile)
		}
		if err == nil {
			return
		}
		s.logf("role channels: saving %s: %v", s.StateFile, err)
	}
}

// Run polls the feed until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	poll, resync := s.Poll, s.Resync
	if poll <= 0 {
		poll = 10 * time.Second
	}
	if resync <= 0 {
		resync = 5 * time.Minute
	}
	lastFull := time.Time{}
	for {
		full := time.Since(lastFull) >= resync
		if s.Tick(ctx, full) && full {
			lastFull = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// Tick polls the feed once and applies a change (or, with full, pushes
// every channel again). It reports whether the feed could be read.
func (s *Syncer) Tick(ctx context.Context, full bool) bool {
	s.mu.Lock()
	s.load()
	etag := s.etag
	if full || !s.have {
		etag = ""
	}
	s.mu.Unlock()

	fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	feed, newETag, notModified, err := s.Auth.ChannelAccess(fctx, etag)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case errors.Is(err, roles.ErrUnsupported):
		if !s.unsupport {
			s.logf("role channels: auth has no /internal/v1/irc/channel-access (auth < 1.16); private channels and auto-join are off")
			s.unsupport = true
		}
		return false
	case err != nil:
		s.logf("role channels: %v", err)
		return false
	case notModified:
		return true
	}
	s.unsupport = false
	changed := !s.have || feed.Version != s.feed.Version
	s.feed, s.have = feed, true
	if s.syncChannels(ctx) {
		s.etag = newETag
	} else {
		s.etag = "" // retry the failed pushes next poll
	}
	if changed {
		s.recheckOnline(ctx)
	}
	return true
}

func ptr(b bool) *bool { return &b }

// syncChannels pushes every private channel and unlocks the ones that are
// no longer private. It reports whether every push succeeded.
func (s *Syncer) syncChannels(ctx context.Context) bool {
	ok := true
	want := map[string]roles.ChannelAccess{}
	for _, c := range s.feed.Channels {
		if c.Private {
			want[strings.ToLower(c.Channel)] = c
		}
	}
	dirty := false
	for ch, c := range want {
		mode := modeInvite
		if c.Everyone {
			mode = modeRegistered
		}
		req := ergoapi.ChannelAccess{Channel: ch, SetBy: s.SetBy}
		switch mode {
		case modeInvite:
			req.InviteOnly, req.Accounts = ptr(true), c.Accounts
			if s.managed[ch] == modeRegistered {
				req.RegisteredOnly = ptr(false)
			}
		case modeRegistered:
			req.RegisteredOnly = ptr(true)
			if s.managed[ch] == modeInvite {
				req.InviteOnly = ptr(false)
			}
		}
		res, err := s.push(ctx, req)
		if err != nil {
			s.logf("role channels: %s: %v", ch, err)
			ok = false
			continue
		}
		if !res.Exists {
			if !s.missing[ch] {
				s.logf("role channels: %s is private but doesn't exist on IRC yet; create (and register) it, RoleServ locks it within a few minutes", ch)
				s.missing[ch] = true
			}
			continue
		}
		delete(s.missing, ch)
		if !res.Registered {
			s.logf("role channels: %s is not registered: its +i and invite exceptions are lost when it empties (/msg ChanServ REGISTER %s)", ch, ch)
		}
		if len(res.Added)+len(res.Removed) > 0 {
			s.logf("role channels: %s: +I %v -I %v", ch, res.Added, res.Removed)
		}
		if s.managed[ch] != mode {
			s.managed[ch], dirty = mode, true
		}
	}
	for ch, mode := range s.managed {
		if _, ok := want[ch]; ok {
			continue
		}
		req := ergoapi.ChannelAccess{Channel: ch, SetBy: s.SetBy, Accounts: []string{}}
		if mode == modeRegistered {
			req.RegisteredOnly = ptr(false)
		} else {
			req.InviteOnly = ptr(false)
		}
		if _, err := s.push(ctx, req); err != nil {
			s.logf("role channels: making %s public: %v", ch, err)
			ok = false
			continue
		}
		s.logf("role channels: %s is public again", ch)
		delete(s.managed, ch)
		dirty = true
	}
	for ch := range s.missing {
		if _, ok := want[ch]; !ok {
			delete(s.missing, ch)
		}
	}
	if dirty {
		s.save()
	}
	return ok
}

func (s *Syncer) push(ctx context.Context, req ergoapi.ChannelAccess) (ergoapi.ChannelAccessResult, error) {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.Ergo.SetChannelAccess(pctx, req)
}

// Login handles a login: with auto-join on, account is joined to its
// roles' channels. Either way its current channels are remembered, so a
// role it gains while connected is noticed (recheckOnline).
func (s *Syncer) Login(ctx context.Context, account string) {
	s.mu.Lock()
	s.load()
	have := s.have
	if s.sem == nil {
		s.sem = make(chan struct{}, maxConcurrentLogins)
	}
	sem := s.sem
	s.mu.Unlock()
	if !have || account == "" {
		return
	}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return
	}
	actx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	aj, err := s.Auth.Autojoin(actx, account)
	if err != nil {
		if !errors.Is(err, roles.ErrNotFound) {
			s.logf("auto-join %s: %v", account, err)
		}
		return
	}
	clients := 1
	if aj.Enabled {
		targets := aj.Channels
		if aj.Rejoin == rejoinOnce {
			targets = aj.Pending
		}
		clients = s.join(actx, account, aj, targets)
	}
	key := strings.ToLower(account)
	s.mu.Lock()
	defer s.mu.Unlock()
	if clients == 0 {
		delete(s.online, key)
		return
	}
	if _, ok := s.online[key]; !ok && len(s.online) >= maxTracked {
		s.logf("role channels: tracking %d connected accounts; forgetting them (they get role channels at their next login)", len(s.online))
		s.online = map[string][]string{}
	}
	s.online[key] = aj.Channels
}

// maxTracked bounds the accounts remembered for role gains.
const maxTracked = 50000

// join SAJOINs account to channels and, for rejoin: once, records them. It
// returns the account's connected client count (-1 on an API error).
func (s *Syncer) join(ctx context.Context, account string, aj roles.Autojoin, channels []string) int {
	if len(channels) == 0 {
		return 1 // nothing to do; treat as online so role gains are seen
	}
	res, err := s.Ergo.Sajoin(ctx, account, channels)
	if err != nil {
		s.logf("auto-join %s: %v", account, err)
		return -1
	}
	for ch, why := range res.Failed {
		s.logf("auto-join %s to %s: %s", account, ch, why)
	}
	if res.Clients == 0 {
		return 0
	}
	if len(res.Joined) > 0 {
		s.logf("auto-joined %s to %s", account, strings.Join(res.Joined, ","))
	}
	if aj.Rejoin == rejoinOnce && len(res.Joined) > 0 {
		if _, err := s.Auth.RecordAutojoin(ctx, account, res.Joined); err != nil {
			s.logf("auto-join %s: recording: %v", account, err)
		}
	}
	return res.Clients
}

// recheckOnline runs after a feed change (role or setting changed): every
// account RoleServ auto-joined this session gets the channels it gained.
// Called with s.mu held; releases it while talking to auth and Ergo.
func (s *Syncer) recheckOnline(ctx context.Context) {
	if !s.feed.Autojoin || len(s.online) == 0 {
		return
	}
	accounts := make([]string, 0, len(s.online))
	for a := range s.online {
		accounts = append(accounts, a)
	}
	slices.Sort(accounts)
	prev := s.online
	s.mu.Unlock()
	next := map[string][]string{}
	for _, account := range accounts {
		actx, cancel := context.WithTimeout(ctx, 20*time.Second)
		aj, err := s.Auth.Autojoin(actx, account)
		if err != nil || !aj.Enabled {
			cancel()
			if err != nil && !errors.Is(err, roles.ErrNotFound) {
				next[account] = prev[account]
			}
			continue
		}
		var gained []string
		if aj.Rejoin == rejoinOnce {
			gained = aj.Pending
		} else {
			for _, ch := range aj.Channels {
				if !slices.Contains(prev[account], ch) {
					gained = append(gained, ch)
				}
			}
		}
		clients := 1
		if len(gained) > 0 {
			clients = s.join(actx, account, aj, gained)
		}
		cancel()
		switch {
		case clients > 0:
			next[account] = aj.Channels
		case clients < 0:
			next[account] = prev[account]
		}
		// 0: offline, forget it until its next login
	}
	s.mu.Lock()
	for a, chs := range next {
		s.online[a] = chs
	}
	for _, a := range accounts {
		if _, ok := next[a]; !ok {
			delete(s.online, a)
		}
	}
}

// Managed returns the channels RoleServ keeps locked (tests).
func (s *Syncer) Managed() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	out := map[string]string{}
	for k, v := range s.managed {
		out[k] = v
	}
	return out
}
