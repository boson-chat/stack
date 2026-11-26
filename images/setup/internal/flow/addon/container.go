package addon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/boson-chat/stack/pkg/probe"
)

// The probe runs from where the bridge will connect from: a container on
// the stack's network (boson_default, or a temporary network with the same
// subnet before the first install). Docker NATs both the same way, so the
// address the IRC server sees in the probe is the bridge's, also when the
// IRC server runs on this machine (natively or in Docker), where this
// host's own address would be wrong.

// Wire is the probe's options as the container takes them (stdin, JSON).
type Wire struct {
	Addr          string        `json:"addr"`
	TLS           bool          `json:"tls"`
	CheckCert     bool          `json:"check_cert"`
	Insecure      bool          `json:"insecure"`
	Timeout       time.Duration `json:"timeout"`
	WebIRCPass    string        `json:"webirc_password"`
	WebIRCGateway string        `json:"webirc_gateway"`
	ConnLimitTest int           `json:"conn_limit_test"`
	RPCURL        string        `json:"rpc_url"`
	RPCToken      string        `json:"rpc_token"`
	RPCAccount    string        `json:"rpc_account"`
	RPCPassword   string        `json:"rpc_password"`
}

func toWire(o probe.Options) Wire {
	return Wire{Addr: o.Addr, TLS: o.TLS, CheckCert: o.CheckCert, Insecure: o.InsecureSkipVerify, Timeout: o.Timeout,
		WebIRCPass: o.WebIRC.Password, WebIRCGateway: o.WebIRC.Gateway, ConnLimitTest: o.ConnLimitTest,
		RPCURL: o.RPC.URL, RPCToken: o.RPC.Token, RPCAccount: o.RPC.Account, RPCPassword: o.RPC.Password}
}

func (w Wire) Options() probe.Options {
	return probe.Options{Addr: w.Addr, TLS: w.TLS, CheckCert: w.CheckCert, InsecureSkipVerify: w.Insecure, Timeout: w.Timeout,
		WebIRC: probe.WebIRCOptions{Password: w.WebIRCPass, Gateway: w.WebIRCGateway}, ConnLimitTest: w.ConnLimitTest,
		RPC: probe.RPCOptions{URL: w.RPCURL, Token: w.RPCToken, Account: w.RPCAccount, Password: w.RPCPassword}}
}

// WireResult is what the container prints.
type WireResult struct {
	Report *probe.Report `json:"report,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// RunWire is `stack-setup ircprobe` inside the container.
func RunWire(ctx context.Context, in io.Reader, out io.Writer) error {
	var w Wire
	if err := json.NewDecoder(in).Decode(&w); err != nil {
		return err
	}
	r, err := probe.Run(ctx, w.Options())
	res := WireResult{Report: r}
	if err != nil {
		res.Error = err.Error()
	}
	return json.NewEncoder(out).Encode(res)
}

// Docker is what the container runner needs from the docker CLI.
type Docker interface {
	Output(ctx context.Context, args ...string) ([]byte, error)
	Run(ctx context.Context, stdin io.Reader, onLine func(string), args ...string) error
}

// ProbeNetwork is the temporary network the probe uses before the stack's
// own exists: made once when the wizard starts and removed just before the
// install starts the stack (RemoveProbeNetwork), so the host's interfaces
// don't change while someone is using the wizard (a browser on this machine
// drops requests in flight when they do).
func ProbeNetwork(project string) string { return project + "-setup-irc-probe" }

// PrepareProbeNetwork makes the probe network unless the stack's exists.
func PrepareProbeNetwork(ctx context.Context, d Docker, project, subnet string) error {
	if _, err := d.Output(ctx, "network", "inspect", project+"_default"); err == nil {
		return nil
	}
	if _, err := d.Output(ctx, "network", "inspect", ProbeNetwork(project)); err == nil {
		return nil
	}
	if _, err := d.Output(ctx, "network", "create", "--subnet", subnet, "--label", "org.boson.setup=probe", ProbeNetwork(project)); err != nil {
		return fmt.Errorf("creating the probe's network (%s): %w", subnet, err)
	}
	return nil
}

// RemoveProbeNetwork removes it (the stack's network takes its subnet).
func RemoveProbeNetwork(ctx context.Context, d Docker, project string) {
	if _, err := d.Output(ctx, "network", "inspect", ProbeNetwork(project)); err == nil {
		_, _ = d.Output(ctx, "network", "rm", ProbeNetwork(project))
	}
}

// ContainerProbe runs the probe in image on the stack's network (or the
// probe network with its subnet).
func ContainerProbe(d Docker, image, project, subnet string) Runner {
	return func(ctx context.Context, o probe.Options) (*probe.Report, error) {
		network := project + "_default"
		if _, err := d.Output(ctx, "network", "inspect", network); err != nil {
			if err := PrepareProbeNetwork(ctx, d, project, subnet); err != nil {
				return nil, err
			}
			network = ProbeNetwork(project)
		}
		in, _ := json.Marshal(toWire(o))
		var out bytes.Buffer
		err := d.Run(ctx, bytes.NewReader(in), func(l string) { out.WriteString(l + "\n") },
			"run", "--rm", "-i", "--network", network, "--label", "org.boson.setup=probe",
			"--entrypoint", "/usr/local/bin/stack-setup", image, "ircprobe")
		var res WireResult
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(l, "{") && json.Unmarshal([]byte(l), &res) == nil {
				break
			}
		}
		if res.Report == nil && res.Error == "" {
			if err == nil {
				err = errors.New("no answer")
			}
			return nil, fmt.Errorf("the probe container: %w: %s", err, strings.TrimSpace(out.String()))
		}
		if res.Error != "" {
			return res.Report, errors.New(res.Error)
		}
		return res.Report, nil
	}
}
