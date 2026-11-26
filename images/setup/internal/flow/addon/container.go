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

// The probe runs in a long-lived helper container (ProbeHelper, idle,
// started when the wizard starts) through docker exec: starting a container
// per probe, or making the network then, changes the host's interfaces
// mid-request, and a browser on this machine drops requests in flight when
// they do (Chrome's ERR_NETWORK_CHANGED). The helper sits on the stack's
// network (project_default), or before the first install on a temporary
// network with the same subnet (ProbeNetwork); both go just before the
// install starts the stack (RemoveProbeHelper).

func ProbeNetwork(project string) string { return project + "-setup-irc-probe" }
func ProbeHelper(project string) string  { return project + "-setup-irc-probe" }

// StartProbeHelper starts the helper (and its network) unless it runs.
func StartProbeHelper(ctx context.Context, d Docker, image, project, subnet string) error {
	if out, err := d.Output(ctx, "container", "inspect", "-f", "{{.State.Running}}", ProbeHelper(project)); err == nil && strings.TrimSpace(string(out)) == "true" {
		return nil
	}
	_, _ = d.Output(ctx, "rm", "-f", ProbeHelper(project))
	network := project + "_default"
	if _, err := d.Output(ctx, "network", "inspect", network); err != nil {
		network = ProbeNetwork(project)
		if _, err := d.Output(ctx, "network", "inspect", network); err != nil {
			if _, err := d.Output(ctx, "network", "create", "--subnet", subnet, "--label", "org.boson.setup=probe", network); err != nil {
				return fmt.Errorf("creating the probe's network (%s): %w", subnet, err)
			}
		}
	}
	if _, err := d.Output(ctx, "run", "-d", "--rm", "--name", ProbeHelper(project), "--network", network, "--label", "org.boson.setup=probe",
		"--entrypoint", "/usr/local/bin/stack-setup", image, "idle"); err != nil {
		return fmt.Errorf("starting the probe container: %w", err)
	}
	return nil
}

// RemoveProbeHelper removes the helper and the temporary network (the
// stack's network takes its subnet).
func RemoveProbeHelper(ctx context.Context, d Docker, project string) {
	_, _ = d.Output(ctx, "rm", "-f", ProbeHelper(project))
	if _, err := d.Output(ctx, "network", "inspect", ProbeNetwork(project)); err == nil {
		_, _ = d.Output(ctx, "network", "rm", ProbeNetwork(project))
	}
}

// ContainerProbe runs the probe in the helper.
func ContainerProbe(d Docker, image, project, subnet string) Runner {
	return func(ctx context.Context, o probe.Options) (*probe.Report, error) {
		if err := StartProbeHelper(ctx, d, image, project, subnet); err != nil {
			return nil, err
		}
		in, _ := json.Marshal(toWire(o))
		var out bytes.Buffer
		err := d.Run(ctx, bytes.NewReader(in), func(l string) { out.WriteString(l + "\n") },
			"exec", "-i", ProbeHelper(project), "/usr/local/bin/stack-setup", "ircprobe")
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
