package roles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUnsupported: the auth service doesn't have this endpoint (404/405, an
// auth release from before roles). Callers fall back to today's behaviour.
var ErrUnsupported = errors.New("not supported by this auth service")

// ErrNotFound: the user or role doesn't exist.
var ErrNotFound = errors.New("not found")

// APIError is a non-2xx answer from an acting endpoint, with auth's message.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("auth service: HTTP %d", e.Status)
}

// Client talks to the auth service's internal listener (X-Internal-Token).
type Client struct {
	// InternalURL is the internal listener's base, e.g. http://auth:8081.
	InternalURL string
	// PublicURL is the public listener's base, e.g. http://auth:8080.
	PublicURL string
	Token     string
	// ActingToken authenticates the acting (on behalf of an account)
	// endpoints; empty = Token.
	ActingToken string
	HTTP        *http.Client
}

// NewClient returns a client with a 5 s timeout.
func NewClient(internalURL, publicURL, token, actingToken string) *Client {
	return &Client{
		InternalURL: strings.TrimRight(internalURL, "/"),
		PublicURL:   strings.TrimRight(publicURL, "/"),
		Token:       token, ActingToken: actingToken,
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, u, token string, hdr map[string]string, body any, out any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth service unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return resp, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		var e struct{ Error, Code string }
		_ = json.Unmarshal(data, &e)
		if e.Code == "" {
			e.Code = e.Error // auth < 1.6 sends the code only in "error"
		}
		// A missing user/role is {"code":"not_found"}; anything else is a
		// route this auth release doesn't have.
		if strings.HasSuffix(e.Code, "not_found") {
			return resp, ErrNotFound
		}
		return resp, ErrUnsupported
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// auth sends {"error": code, "code": code, "message": human text};
		// older releases have no "code" and some errors no "message".
		var e struct{ Error, Code, Message string }
		_ = json.Unmarshal(data, &e)
		if e.Code == "" {
			e.Code = e.Error
		}
		if e.Message == "" {
			e.Message = e.Error
		}
		return resp, &APIError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("auth service: bad response from %s: %w", req.URL.Path, err)
		}
	}
	return resp, nil
}

// Roles fetches GET /internal/v1/roles. With etag set and nothing changed,
// it returns notModified = true and an empty list.
func (c *Client) Roles(ctx context.Context, etag string) (list List, newETag string, notModified bool, err error) {
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-None-Match"] = etag
	}
	resp, err := c.do(ctx, http.MethodGet, c.InternalURL+"/internal/v1/roles", c.Token, hdr, nil, &list)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			err = ErrUnsupported
		}
		return list, "", false, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return List{}, etag, true, nil
	}
	return list, resp.Header.Get("ETag"), false, nil
}

// List fetches the roles without caching.
func (c *Client) List(ctx context.Context) ([]Role, error) {
	list, _, _, err := c.Roles(ctx, "")
	return list.Roles, err
}

// User fetches GET /internal/v1/users/{username}/irc.
func (c *Client) User(ctx context.Context, username string) (User, error) {
	var u User
	_, err := c.do(ctx, http.MethodGet, c.InternalURL+"/internal/v1/users/"+url.PathEscape(username)+"/irc", c.Token, nil, nil, &u)
	return u, err
}

// UserGrant is one entry of GET /internal/v1/irc/grants (requested from
// auth, see docs/roles.md).
type UserGrant struct {
	Username string `json:"username"`
	UserIRC
}

// Grants fetches GET /internal/v1/irc/grants: every user with an oper class
// or a vhost. ErrUnsupported if auth doesn't have it.
func (c *Client) Grants(ctx context.Context, etag string) (users []UserGrant, newETag string, notModified bool, err error) {
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-None-Match"] = etag
	}
	var body struct {
		Users []UserGrant `json:"users"`
	}
	resp, err := c.do(ctx, http.MethodGet, c.InternalURL+"/internal/v1/irc/grants", c.Token, hdr, nil, &body)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			err = ErrUnsupported
		}
		return nil, "", false, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	return body.Users, resp.Header.Get("ETag"), false, nil
}

// RemovedAccount is a user deleted or disabled in auth.
type RemovedAccount struct {
	Username string    `json:"username"`
	Reason   string    `json:"reason"` // "deleted" or "disabled"
	At       time.Time `json:"at"`
}

// RemovedAccounts fetches one page of GET /internal/v1/irc/accounts/removed
// (auth >= 1.7): users deleted or disabled after the cursor since ("" = from
// the beginning), and the cursor to continue from. ErrUnsupported if auth
// doesn't have it.
func (c *Client) RemovedAccounts(ctx context.Context, since string, limit int) (accounts []RemovedAccount, next string, err error) {
	var body struct {
		Accounts []RemovedAccount `json:"accounts"`
		Next     string           `json:"next"`
	}
	u := fmt.Sprintf("%s/internal/v1/irc/accounts/removed?limit=%d", c.InternalURL, limit)
	if since != "" {
		u += "&since=" + url.QueryEscape(since)
	}
	if _, err := c.do(ctx, http.MethodGet, u, c.Token, nil, nil, &body); err != nil {
		if errors.Is(err, ErrNotFound) {
			err = ErrUnsupported
		}
		return nil, "", err
	}
	return body.Accounts, body.Next, nil
}

// Member is one holder of a role.
type Member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Disabled bool   `json:"disabled"`
}

// Members fetches every page of GET /internal/v1/roles/{id}/members
// (requested from auth: the admin API's members list on the internal
// listener). At most max members.
func (c *Client) Members(ctx context.Context, roleID string, max int) ([]Member, error) {
	var out []Member
	cursor := ""
	for {
		var body struct {
			Users      []Member `json:"users"`
			NextCursor string   `json:"next_cursor"`
		}
		u := c.InternalURL + "/internal/v1/roles/" + url.PathEscape(roleID) + "/members?limit=200"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		if _, err := c.do(ctx, http.MethodGet, u, c.Token, nil, nil, &body); err != nil {
			return out, err
		}
		out = append(out, body.Users...)
		if body.NextCursor == "" || len(out) >= max {
			return out, nil
		}
		cursor = body.NextCursor
	}
}

// Acting endpoints (requested from auth, docs/roles.md "Acting API"): the
// admin API under /internal/v1/acting, same bodies and errors, with the
// acting account named in X-Acting-Account and authenticated by the
// dedicated acting token. auth applies that account's permissions and the
// role hierarchy and audits the change as theirs.

func (c *Client) acting(ctx context.Context, method, path, account string, body, out any) error {
	tok := c.ActingToken
	if tok == "" {
		tok = c.Token
	}
	_, err := c.do(ctx, method, c.InternalURL+"/internal/v1/acting"+path, tok,
		map[string]string{"X-Acting-Account": account, "X-Acting-Via": "roleserv"}, body, out)
	return err
}

// Actor is GET /internal/v1/acting/me: who auth thinks is acting.
type Actor struct {
	Username    string   `json:"username"`
	Permissions []string `json:"permissions"`
}

// Me probes the acting API (ErrUnsupported if auth doesn't have it).
func (c *Client) Me(ctx context.Context, account string) (Actor, error) {
	var a Actor
	err := c.acting(ctx, http.MethodGet, "/me", account, nil, &a)
	return a, err
}

type roleEnvelope struct {
	Role Role `json:"role"`
}

// CreateRole: POST /internal/v1/acting/roles {name}.
func (c *Client) CreateRole(ctx context.Context, account, name string) (Role, error) {
	var r roleEnvelope
	err := c.acting(ctx, http.MethodPost, "/roles", account, map[string]string{"name": name}, &r)
	return r.Role, err
}

// DeleteRole: DELETE /internal/v1/acting/roles/{id}.
func (c *Client) DeleteRole(ctx context.Context, account, roleID string) error {
	return c.acting(ctx, http.MethodDelete, "/roles/"+url.PathEscape(roleID), account, nil, nil)
}

// UpdateRole: PATCH /internal/v1/acting/roles/{id} with the admin API's
// fields (nil values clear color / irc.whois_line / irc.vhost).
func (c *Client) UpdateRole(ctx context.Context, account, roleID string, patch map[string]any) (Role, error) {
	var r roleEnvelope
	err := c.acting(ctx, http.MethodPatch, "/roles/"+url.PathEscape(roleID), account, patch, &r)
	return r.Role, err
}

// SetUserRoles: PUT /internal/v1/acting/users/{user_id}/roles {role_ids}
// (replaces the user's roles).
func (c *Client) SetUserRoles(ctx context.Context, account, userID string, roleIDs []string) error {
	return c.acting(ctx, http.MethodPut, "/users/"+url.PathEscape(userID)+"/roles", account,
		map[string][]string{"role_ids": nonNil(roleIDs)}, nil)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ChannelAccessFeed is GET /internal/v1/irc/channel-access (auth >= 1.16).
type ChannelAccessFeed struct {
	Version  string          `json:"version"`
	Autojoin bool            `json:"autojoin"`
	Rejoin   string          `json:"rejoin"` // "login" or "once"
	Channels []ChannelAccess `json:"channels"`
	// Network: the network channels (irc.channels; auth >= the network
	// channels release). Absent from older auth: no network channels.
	Network NetworkChannels `json:"network"`
}

// NetworkChannels are the feed's network channels: they always exist,
// registered to Founder, and the Autojoin ones are joined by new accounts
// (first login) and, with AutojoinGuests, by guests when they connect.
type NetworkChannels struct {
	// Founder is the account network channels are registered to ("" =
	// RoleServ's own account).
	Founder        string           `json:"founder"`
	AutojoinGuests bool             `json:"autojoin_guests"`
	Channels       []NetworkChannel `json:"channels"`
}

// NetworkChannel is one network channel.
type NetworkChannel struct {
	Channel  string `json:"channel"` // casefolded
	Name     string `json:"name"`    // as written
	Topic    string `json:"topic"`
	Autojoin bool   `json:"autojoin"`
}

// AutojoinChannels are the network channels guests and new accounts join.
func (n NetworkChannels) AutojoinChannels() []string {
	var out []string
	for _, c := range n.Channels {
		if c.Autojoin {
			out = append(out, c.Channel)
		}
	}
	return out
}

// ChannelAccess is one channel of the feed.
type ChannelAccess struct {
	Channel string `json:"channel"`
	Private bool   `json:"private"`
	// Everyone: listed on the Member role (any logged-in account may join).
	Everyone bool `json:"everyone"`
	// Accounts may join a private channel (lower-cased).
	Accounts []string `json:"accounts"`
}

// ChannelAccess fetches the role channel feed. With etag set and nothing
// changed, notModified is true. ErrUnsupported if auth doesn't have it.
func (c *Client) ChannelAccess(ctx context.Context, etag string) (feed ChannelAccessFeed, newETag string, notModified bool, err error) {
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-None-Match"] = etag
	}
	resp, err := c.do(ctx, http.MethodGet, c.InternalURL+"/internal/v1/irc/channel-access", c.Token, hdr, nil, &feed)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			err = ErrUnsupported
		}
		return feed, "", false, err
	}
	if resp.StatusCode == http.StatusNotModified {
		return ChannelAccessFeed{}, etag, true, nil
	}
	return feed, resp.Header.Get("ETag"), false, nil
}

// Autojoin is one account's auto-join channels (GET/POST
// /internal/v1/irc/autojoin).
type Autojoin struct {
	Account  string   `json:"account"`
	Enabled  bool     `json:"enabled"`
	Rejoin   string   `json:"rejoin"`
	Channels []string `json:"channels"`
	// Pending: Channels RoleServ hasn't auto-joined the account to yet.
	Pending []string `json:"pending"`
	// Network: the auto-join network channels (joined once per account,
	// whatever Enabled and Rejoin say); NetworkPending: the ones RoleServ
	// hasn't joined the account to yet.
	Network        []string `json:"network"`
	NetworkPending []string `json:"network_pending"`
}

// Autojoin fetches an account's auto-join channels (ErrNotFound for an
// account auth doesn't have).
func (c *Client) Autojoin(ctx context.Context, account string) (Autojoin, error) {
	var a Autojoin
	_, err := c.do(ctx, http.MethodGet, c.InternalURL+"/internal/v1/irc/autojoin?account="+url.QueryEscape(account), c.Token, nil, nil, &a)
	return a, err
}

// RecordAutojoin tells auth RoleServ joined account to channels (rejoin:
// once) and returns the account's auto-join channels after that.
func (c *Client) RecordAutojoin(ctx context.Context, account string, channels []string) (Autojoin, error) {
	var a Autojoin
	_, err := c.do(ctx, http.MethodPost, c.InternalURL+"/internal/v1/irc/autojoin", c.Token, nil,
		map[string]any{"account": account, "channels": nonNil(channels)}, &a)
	return a, err
}
