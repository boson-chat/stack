package probe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// checkRPC checks the services RPC endpoint: whether it answers, whether
// the token is accepted (Anope), and whether the account and password are.
// pkg is the services package seen on IRC ("anope", "atheme", or other);
// it picks which methods to try first.
func checkRPC(ctx context.Context, o *Options, pkg string) *RPCResult {
	ro := o.RPC
	res := &RPCResult{URL: ro.URL, Protocol: "jsonrpc"}
	if strings.HasSuffix(strings.TrimRight(ro.URL, "/"), "/xmlrpc") {
		res.Protocol = "xmlrpc"
	}
	hc := ro.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: o.stepTimeout()}
	}
	cl := rpcClient{hc: hc, url: ro.URL, token: ro.Token, xml: res.Protocol == "xmlrpc"}

	order := []string{"anope", "atheme"}
	if pkg == "atheme" || (pkg != "anope" && ro.Token == "") {
		order = []string{"atheme", "anope"}
	}
	var lastErr error
	for _, flavor := range order {
		var err error
		switch flavor {
		case "anope":
			err = checkAnope(ctx, cl, ro, res)
		case "atheme":
			err = checkAtheme(ctx, cl, ro, res)
		}
		if res.Flavor != "" {
			if err != nil {
				res.Error = err.Error()
			}
			return res
		}
		lastErr = err
	}
	if lastErr != nil {
		res.Error = lastErr.Error()
	}
	return res
}

// rpcError is a JSON-RPC error object or an XML-RPC fault.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type rpcClient struct {
	hc    *http.Client
	url   string
	token string
	xml   bool
}

// errNotRPC means the endpoint answered with something that isn't an RPC
// reply (wrong URL, an HTML page, a proxy error).
var errNotRPC = errors.New("not an RPC reply")

// call runs method with string params. It returns the result as raw JSON
// (JSON-RPC) or the first <value>'s text (XML-RPC), or an *rpcError.
func (c rpcClient) call(ctx context.Context, method string, params ...string) (string, error) {
	var body []byte
	ctype := "application/json"
	if c.xml {
		ctype = "text/xml"
		var b bytes.Buffer
		b.WriteString(`<?xml version="1.0"?><methodCall><methodName>`)
		_ = xml.EscapeText(&b, []byte(method))
		b.WriteString(`</methodName><params>`)
		for _, p := range params {
			b.WriteString(`<param><value><string>`)
			_ = xml.EscapeText(&b, []byte(p))
			b.WriteString(`</string></value></param>`)
		}
		b.WriteString(`</params></methodCall>`)
		body = b.Bytes()
	} else {
		if params == nil {
			params = []string{}
		}
		body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "bprobe", "method": method, "params": params})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", ctype)
	if c.token != "" {
		// Anope 2.1 base64-decodes the bearer token before comparing it
		// with the one in its config (include/modules/rpc.h CanExecute).
		req.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString([]byte(c.token)))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if c.xml {
		return parseXMLRPC(data, resp.StatusCode)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil || (out.Result == nil && out.Error == nil) {
		return "", fmt.Errorf("%w: HTTP %d, %q", errNotRPC, resp.StatusCode, snippet(data))
	}
	if out.Error != nil {
		return "", out.Error
	}
	return string(out.Result), nil
}

func parseXMLRPC(data []byte, status int) (string, error) {
	var out struct {
		XMLName xml.Name
		Params  []struct {
			Value xmlValue `xml:"value"`
		} `xml:"params>param"`
		Fault *struct {
			Members []struct {
				Name  string   `xml:"name"`
				Value xmlValue `xml:"value"`
			} `xml:"value>struct>member"`
		} `xml:"fault"`
	}
	if err := xml.Unmarshal(data, &out); err != nil || out.XMLName.Local != "methodResponse" {
		return "", fmt.Errorf("%w: HTTP %d, %q", errNotRPC, status, snippet(data))
	}
	if out.Fault != nil {
		e := &rpcError{}
		for _, m := range out.Fault.Members {
			switch m.Name {
			case "faultCode":
				_, _ = fmt.Sscan(m.Value.text(), &e.Code)
			case "faultString":
				e.Message = m.Value.text()
			}
		}
		return "", e
	}
	if len(out.Params) == 0 {
		return "", nil
	}
	return out.Params[0].Value.text(), nil
}

type xmlValue struct {
	Inner string `xml:",innerxml"`
	Str   string `xml:"string"`
	Int   string `xml:"int"`
	I4    string `xml:"i4"`
	Bool  string `xml:"boolean"`
}

func (v xmlValue) text() string {
	for _, s := range []string{v.Str, v.Int, v.I4, v.Bool} {
		if s != "" {
			return s
		}
	}
	return strings.TrimSpace(v.Inner)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return s
}

// Anope 2.1 (modules/rpc): without a matching token every method is
// "No authorization for method" (-32601, like an unknown method).
func checkAnope(ctx context.Context, cl rpcClient, ro RPCOptions, res *RPCResult) error {
	if ro.Account == "" {
		// Any rpc_user/rpc_data method proves the token; checkCredentials
		// with a nick that can't exist answers -32099 (no such account).
		_, err := cl.call(ctx, "anope.checkCredentials", "bprobe-none-"+randHex(4), "x")
		var re *rpcError
		switch {
		case err == nil:
			res.Reachable, res.Authorized, res.Flavor = true, true, "anope"
			return nil
		case errors.As(err, &re):
			res.Reachable = true
			if re.Code <= -32097 && re.Code >= -32099 {
				res.Authorized, res.Flavor = true, "anope"
				return nil
			}
			if strings.Contains(re.Message, "No authorization") {
				res.Flavor = "anope"
				return fmt.Errorf("token refused: %s", re.Message)
			}
			return err
		}
		return err
	}
	_, err := cl.call(ctx, "anope.checkCredentials", ro.Account, ro.Password)
	var re *rpcError
	switch {
	case err == nil:
		res.Reachable, res.Authorized, res.Flavor = true, true, "anope"
		res.CredentialsChecked, res.CredentialsOK = true, true
		return nil
	case errors.As(err, &re):
		res.Reachable = true
		switch {
		case re.Code <= -32097 && re.Code >= -32099:
			res.Authorized, res.Flavor, res.CredentialsChecked = true, "anope", true
			return fmt.Errorf("account %q: %s", ro.Account, re.Message)
		case strings.Contains(re.Message, "No authorization"):
			res.Flavor = "anope"
			return fmt.Errorf("token refused: %s", re.Message)
		}
		return err
	}
	return err
}

// Atheme (transport/jsonrpc, transport/xmlrpc): no token; atheme.login
// returns an authcookie, which we log out again.
func checkAtheme(ctx context.Context, cl rpcClient, ro RPCOptions, res *RPCResult) error {
	acct, pass := ro.Account, ro.Password
	if acct == "" {
		acct, pass = "bprobe-none-"+randHex(4), "x"
	}
	out, err := cl.call(ctx, "atheme.login", acct, pass, "boson-probe")
	var re *rpcError
	switch {
	case err == nil:
		res.Reachable, res.Authorized, res.Flavor = true, true, "atheme"
		res.CredentialsChecked, res.CredentialsOK = ro.Account != "", ro.Account != ""
		cookie := strings.Trim(out, `"`)
		if cookie != "" {
			_, _ = cl.call(ctx, "atheme.logout", cookie, acct)
		}
		return nil
	case errors.As(err, &re):
		res.Reachable = true
		// Atheme's faults: 1 needmoreparams, 3 nosuch_source, 5 authfail, 6 noprivs (frozen).
		switch re.Code {
		case 1, 3, 5, 6:
			if strings.Contains(strings.ToLower(re.Message), "method") {
				return err
			}
			res.Authorized, res.Flavor = true, "atheme"
			if ro.Account == "" {
				return nil
			}
			res.CredentialsChecked = true
			return fmt.Errorf("account %q: %s", ro.Account, re.Message)
		}
		return err
	}
	return err
}
