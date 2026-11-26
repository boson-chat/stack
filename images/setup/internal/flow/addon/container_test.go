package addon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/boson-chat/stack/pkg/probe"
)

type fakeDocker struct {
	haveStack bool
	calls     []string
	stdin     string
}

func (f *fakeDocker) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "network" && args[1] == "inspect" && (!f.haveStack || args[2] != "boson_default") {
		return nil, errors.New("no such network")
	}
	return nil, nil
}

func (f *fakeDocker) Run(_ context.Context, stdin io.Reader, onLine func(string), args ...string) error {
	f.calls = append(f.calls, strings.Join(args, " "))
	b, _ := io.ReadAll(stdin)
	f.stdin = string(b)
	var out bytes.Buffer
	_ = RunWireFake(&out)
	onLine("Unable to find image locally")
	onLine(strings.TrimSpace(out.String()))
	return nil
}

func RunWireFake(w io.Writer) error {
	return json.NewEncoder(w).Encode(WireResult{Report: &probe.Report{Server: probe.ServerInfo{ClientHost: "172.18.0.1"}}})
}

func TestContainerProbe(t *testing.T) {
	d := &fakeDocker{}
	run := ContainerProbe(d, "img", "boson", "10.203.53.0/24")
	r, err := run(context.Background(), probe.Options{Addr: "irc.example.org:6697", TLS: true, WebIRC: probe.WebIRCOptions{Password: "pw"}, ConnLimitTest: 8})
	if err != nil || r.Server.ClientHost != "172.18.0.1" {
		t.Fatalf("%v %+v", err, r)
	}
	all := strings.Join(d.calls, "\n")
	for _, want := range []string{"network create --subnet 10.203.53.0/24 --label org.boson.setup=probe boson-setup-irc-probe",
		"run --rm -i --network boson-setup-irc-probe", "network rm boson-setup-irc-probe"} {
		if !strings.Contains(all, want) {
			t.Fatalf("no %q in\n%s", want, all)
		}
	}
	if strings.Contains(all, "pw") || !strings.Contains(d.stdin, `"webirc_password":"pw"`) {
		t.Fatal("the password goes on stdin only")
	}
	// After the install: the stack's own network.
	d = &fakeDocker{haveStack: true}
	if _, err := ContainerProbe(d, "img", "boson", "10.203.53.0/24")(context.Background(), probe.Options{Addr: "x:1"}); err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(d.calls, "\n"); !strings.Contains(all, "--network boson_default") || strings.Contains(all, "network create") {
		t.Fatal(all)
	}
}
