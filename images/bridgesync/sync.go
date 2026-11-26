package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// User is one Boson account.
type User struct {
	ID          string
	Username    string
	Email       string
	DisplayName string
	Disabled    bool
}

// Users lists Boson accounts.
type Users interface {
	// Fingerprint changes whenever an account is added, changed or removed.
	Fingerprint(ctx context.Context) (string, error)
	List(ctx context.Context) ([]User, error)
}

// Account is a bridge account (evrd's managed API).
type Account struct {
	Username string `json:"username"`
	Subject  string `json:"subject"`
	Email    string `json:"email"`
	Nick     string `json:"nick"`
	Ident    string `json:"ident"`
	Realname string `json:"realname"`
	Disabled bool   `json:"disabled"`
}

// Bridge is the managed API.
type Bridge interface {
	List(ctx context.Context) ([]Account, error)
	Put(ctx context.Context, a Account) error
	Delete(ctx context.Context, username string) error
}

// Syncer makes the bridge's accounts match the Boson accounts.
type Syncer struct {
	Users  Users
	Bridge Bridge
	Log    *slog.Logger
	// Full: a full sync at least this often even when nothing changed
	// (default 1m), so changes made on the bridge by hand are put back.
	Full time.Duration

	last     string
	lastFull time.Time
}

// validName is evrd's account name rule (2–32 characters, a letter first).
var validName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,31}$`)

var identBad = regexp.MustCompile(`[^a-z0-9]`)

// want is the bridge account for a Boson user.
func want(u User) Account {
	ident := identBad.ReplaceAllString(strings.ToLower(u.Username), "")
	if len(ident) > 10 {
		ident = ident[:10]
	}
	if ident == "" {
		ident = "boson"
	}
	real := u.DisplayName
	if real == "" {
		real = u.Username
	}
	return Account{Username: strings.ToLower(u.Username), Subject: u.ID, Email: u.Email, Nick: u.Username,
		Ident: ident, Realname: real, Disabled: u.Disabled}
}

func same(a, b Account) bool {
	return a.Subject == b.Subject && a.Email == b.Email && a.Nick == b.Nick && a.Ident == b.Ident &&
		a.Realname == b.Realname && a.Disabled == b.Disabled
}

// Once syncs when something changed (or a full sync is due).
func (s *Syncer) Once(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	fp, err := s.Users.Fingerprint(ctx)
	if err != nil {
		return fmt.Errorf("auth's users: %w", err)
	}
	full := s.Full
	if full == 0 {
		full = time.Minute
	}
	if fp == s.last && time.Since(s.lastFull) < full {
		return nil
	}
	users, err := s.Users.List(ctx)
	if err != nil {
		return fmt.Errorf("auth's users: %w", err)
	}
	have, err := s.Bridge.List(ctx)
	if err != nil {
		return fmt.Errorf("the bridge's accounts: %w", err)
	}
	byName := map[string]Account{}
	bySubject := map[string]Account{}
	for _, a := range have {
		byName[strings.ToLower(a.Username)] = a
		if a.Subject != "" {
			bySubject[a.Subject] = a
		}
	}
	wanted := map[string]bool{}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, u := range users {
		if !validName.MatchString(u.Username) {
			s.Log.Warn("skipping an account the bridge can't name", "username", u.Username)
			continue
		}
		w := want(u)
		wanted[w.Username] = true
		// Renamed in Boson: the old bridge account goes first (a subject
		// belongs to one account).
		if old, ok := bySubject[w.Subject]; ok && !strings.EqualFold(old.Username, w.Username) {
			s.Log.Info("renamed: removing the old bridge account", "old", old.Username, "new", w.Username)
			note(s.Bridge.Delete(ctx, old.Username))
			delete(byName, strings.ToLower(old.Username))
		}
		if cur, ok := byName[w.Username]; ok && same(cur, w) {
			continue
		}
		if err := s.Bridge.Put(ctx, w); err != nil {
			note(fmt.Errorf("%s: %w", w.Username, err))
			continue
		}
		s.Log.Info("bridge account up to date", "username", w.Username, "disabled", w.Disabled)
	}
	for name, a := range byName {
		if wanted[name] {
			continue
		}
		s.Log.Info("Boson account gone: removing its bridge account", "username", a.Username)
		note(s.Bridge.Delete(ctx, a.Username))
	}
	if firstErr != nil {
		return firstErr
	}
	s.last, s.lastFull = fp, time.Now()
	return nil
}

// httpBridge talks to the managed API.
type httpBridge struct {
	base, token string
	c           *http.Client
}

func NewBridge(base, token string) Bridge {
	return &httpBridge{base: strings.TrimRight(base, "/"), token: token, c: &http.Client{Timeout: 15 * time.Second}}
}

func (b *httpBridge) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		j, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound && method == http.MethodDelete {
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (b *httpBridge) List(ctx context.Context) ([]Account, error) {
	var r struct {
		Users []Account `json:"users"`
	}
	err := b.do(ctx, http.MethodGet, "/api/managed/users", nil, &r)
	return r.Users, err
}

func (b *httpBridge) Put(ctx context.Context, a Account) error {
	body := map[string]any{"subject": a.Subject, "email": a.Email, "nick": a.Nick, "altNick": a.Nick + "_",
		"ident": a.Ident, "realname": a.Realname, "disabled": a.Disabled}
	return b.do(ctx, http.MethodPut, "/api/managed/users/"+url.PathEscape(a.Username), body, nil)
}

func (b *httpBridge) Delete(ctx context.Context, username string) error {
	return b.do(ctx, http.MethodDelete, "/api/managed/users/"+url.PathEscape(username), nil, nil)
}
