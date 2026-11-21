// Package ergoapi is a small client for Ergo's HTTP API (api: in ircd.yaml),
// including the patched POST /v1/account_grants (images/ergo/patches).
package ergoapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/roles"
)

// Error is an unsuccessful API answer ({"success": false, "errorCode": …}).
type Error struct {
	Code, Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("ergo api: %s: %s", e.Code, e.Message)
	}
	return "ergo api: " + e.Code
}

// IsCode reports whether err is an *Error with that errorCode.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

type Client struct {
	URL, Token string
	HTTP       *http.Client
}

func New(url, token string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) call(ctx context.Context, path string, body, out any) error {
	if c.Token == "" {
		return errors.New("ERGO_API_TOKEN is not set")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("ergo api unreachable: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ergo api %s: HTTP %d: %s", path, resp.StatusCode, bytes.TrimSpace(data))
	}
	var gen struct {
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(data, &gen); err != nil {
		return fmt.Errorf("ergo api %s: %w", path, err)
	}
	if !gen.Success {
		code := gen.ErrorCode
		if code == "" {
			code = "UNKNOWN_ERROR"
		}
		return &Error{Code: code, Message: gen.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ApplyGrants calls POST /v1/account_grants. It returns how many connected
// clients were synced.
func (c *Client) ApplyGrants(ctx context.Context, account string, g roles.Grant) (int, error) {
	body := struct {
		AccountName string `json:"accountName"`
		roles.Grant
	}{account, g}
	var out struct {
		Clients int `json:"clients"`
	}
	err := c.call(ctx, "/v1/account_grants", body, &out)
	return out.Clients, err
}

// Rehash calls POST /v1/rehash (synchronous, unlike SIGHUP).
func (c *Client) Rehash(ctx context.Context) error {
	return c.call(ctx, "/v1/rehash", struct{}{}, nil)
}

// Register calls POST /v1/ns/saregister (ACCOUNT_EXISTS if it exists).
func (c *Client) Register(ctx context.Context, account, passphrase string) error {
	return c.call(ctx, "/v1/ns/saregister", map[string]string{"accountName": account, "passphrase": passphrase}, nil)
}

// EnsureAccount registers a local Ergo account with this passphrase, or
// resets the passphrase of an existing one.
func (c *Client) EnsureAccount(ctx context.Context, account, passphrase string) error {
	err := c.call(ctx, "/v1/ns/saregister", map[string]string{"accountName": account, "passphrase": passphrase}, nil)
	if IsCode(err, "ACCOUNT_EXISTS") {
		err = c.call(ctx, "/v1/ns/passwd", map[string]string{"accountName": account, "passphrase": passphrase}, nil)
	}
	return err
}

// AccountOf returns the account a connected nick is logged into ("" if
// none), via POST /v1/whois. online is false if nobody has that nick.
func (c *Client) AccountOf(ctx context.Context, nick string) (account string, online bool, err error) {
	var out struct {
		Present bool   `json:"present"`
		Account string `json:"account"`
	}
	if err = c.call(ctx, "/v1/whois", map[string]string{"nickname": nick}, &out); err != nil {
		return "", false, err
	}
	return out.Account, out.Present, nil
}

// EraseAccount calls POST /v1/ns/unregister with erase (patched Ergo): the
// account is deleted (its name can be registered again), its clients are
// disconnected and the channels it founded are unregistered.
// ACCOUNT_DOES_NOT_EXIST if there is no such account.
func (c *Client) EraseAccount(ctx context.Context, account string) error {
	return c.call(ctx, "/v1/ns/unregister", map[string]any{"accountName": account, "erase": true}, nil)
}

// ChannelAccess is the request of POST /v1/channel_access (patched Ergo,
// patches/0004): set or clear +i / +R (nil leaves a mode alone) and make
// Accounts the a:<account> invite exceptions SetBy manages.
type ChannelAccess struct {
	Channel        string   `json:"channel"`
	InviteOnly     *bool    `json:"inviteOnly,omitempty"`
	RegisteredOnly *bool    `json:"registeredOnly,omitempty"`
	Accounts       []string `json:"accounts"`
	SetBy          string   `json:"setBy"`
}

// ChannelAccessResult is its answer. Exists is false (and nothing changed)
// when the channel doesn't exist.
type ChannelAccessResult struct {
	Exists         bool     `json:"exists"`
	Registered     bool     `json:"registered"`
	Added          []string `json:"added"`
	Removed        []string `json:"removed"`
	InviteOnly     bool     `json:"inviteOnly"`
	RegisteredOnly bool     `json:"registeredOnly"`
}

// SetChannelAccess calls POST /v1/channel_access. It never creates a channel.
func (c *Client) SetChannelAccess(ctx context.Context, req ChannelAccess) (ChannelAccessResult, error) {
	if req.Accounts == nil {
		req.Accounts = []string{}
	}
	var out ChannelAccessResult
	err := c.call(ctx, "/v1/channel_access", req, &out)
	return out, err
}

// SajoinResult is the answer of POST /v1/sajoin.
type SajoinResult struct {
	// Clients is how many connected clients the account has (0: offline).
	Clients int               `json:"clients"`
	Joined  []string          `json:"joined"`
	Failed  map[string]string `json:"failed"`
}

// Sajoin calls POST /v1/sajoin (patched Ergo): joins every connected client
// of account to channels, like SAJOIN.
func (c *Client) Sajoin(ctx context.Context, account string, channels []string) (SajoinResult, error) {
	var out SajoinResult
	err := c.call(ctx, "/v1/sajoin", map[string]any{"accountName": account, "channels": channels}, &out)
	return out, err
}

// SajoinNick calls POST /v1/sajoin with a nick (patch 0005): joins that one
// client (logged in or not) to channels. Clients is 0 if nobody has the nick.
func (c *Client) SajoinNick(ctx context.Context, nick string, channels []string) (SajoinResult, error) {
	var out SajoinResult
	err := c.call(ctx, "/v1/sajoin", map[string]any{"nick": nick, "channels": channels}, &out)
	return out, err
}

// ChannelEnsure is the request of POST /v1/channel_ensure (patch 0005).
type ChannelEnsure struct {
	Channel    string `json:"channel"`
	Founder    string `json:"founder"`
	Topic      string `json:"topic,omitempty"`
	SetTopic   bool   `json:"setTopic,omitempty"`
	TopicSetBy string `json:"topicSetBy,omitempty"`
	NoExpire   bool   `json:"noExpire,omitempty"`
}

// ChannelEnsureResult is its answer.
type ChannelEnsureResult struct {
	Created      bool   `json:"created"`
	Registered   bool   `json:"registered"`
	Founder      string `json:"founder"`
	TopicChanged bool   `json:"topicChanged"`
}

// EnsureChannel calls POST /v1/channel_ensure: the channel exists (created
// with no members if needed) and is registered (to Founder if it wasn't).
func (c *Client) EnsureChannel(ctx context.Context, req ChannelEnsure) (ChannelEnsureResult, error) {
	var out ChannelEnsureResult
	err := c.call(ctx, "/v1/channel_ensure", req, &out)
	return out, err
}

// DropChannel calls POST /v1/channel_drop: unregisters channel if founder
// founds it. dropped is false when it doesn't (or doesn't exist).
func (c *Client) DropChannel(ctx context.Context, channel, founder string) (dropped bool, err error) {
	var out struct {
		Dropped bool `json:"dropped"`
	}
	err = c.call(ctx, "/v1/channel_drop", map[string]string{"channel": channel, "founder": founder}, &out)
	return out.Dropped, err
}
