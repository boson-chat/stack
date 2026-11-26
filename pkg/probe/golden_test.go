package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the live fixtures")

// The fixture pairings (test/ircds/compose.yaml) on their default ports.
var fixtures = map[string]struct {
	addr, rpcURL, token string
	maxPerIP            int
}{
	"ngircd-anope": {"127.0.0.1:16697", "http://127.0.0.1:18080/jsonrpc", "fixture-rpc-token", 5},
	"unreal-anope": {"127.0.0.1:26697", "http://127.0.0.1:28080/jsonrpc", "fixture-rpc-token", 3},
	"insp-atheme":  {"127.0.0.1:36697", "http://127.0.0.1:38080/jsonrpc", "", 3},
	"ngircd":       {"127.0.0.1:46697", "", "", 5},
}

// TestGolden probes a running fixture pairing and compares the capability
// table with testdata/golden/<pairing>.json (or <pairing>.webirc.json once
// test/ircds/snippets/webirc.<ircd> has been applied). Run by CI after
// test/ircds/run.sh up + smoke:
//
//	PROBE_FIXTURE=insp-atheme [PROBE_WEBIRC_APPLIED=1] go test -run TestGolden [-update]
//
// On a mismatch in CI, the job's "probe-<pairing>" artifact has the new
// table (and the full report): copy it over testdata/golden after review.
//
// PROBE_ACCOUNT / PROBE_PASSWORD: the account smoke registered
// (default bosonprobe / probe-fixture-pass).
func TestGolden(t *testing.T) {
	name := os.Getenv("PROBE_FIXTURE")
	if name == "" {
		t.Skip("PROBE_FIXTURE not set (needs a running test/ircds pairing)")
	}
	fx, ok := fixtures[name]
	if !ok {
		t.Fatalf("unknown fixture %q", name)
	}
	acct, pass := os.Getenv("PROBE_ACCOUNT"), os.Getenv("PROBE_PASSWORD")
	if acct == "" {
		acct, pass = "bosonprobe", "probe-fixture-pass"
	}
	golden := filepath.Join("testdata", "golden", name+".json")
	if os.Getenv("PROBE_WEBIRC_APPLIED") == "1" {
		golden = filepath.Join("testdata", "golden", name+".webirc.json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	opts := Options{
		Addr: fx.addr, TLS: true, InsecureSkipVerify: true, Timeout: 10 * time.Second,
		WebIRC:        WebIRCOptions{Password: "fixture-webirc"},
		ConnLimitTest: 8,
		Transcript:    true,
	}
	if fx.rpcURL != "" {
		opts.RPC = RPCOptions{URL: fx.rpcURL, Token: fx.token, Account: acct, Password: pass}
	}
	r, err := Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := json.MarshalIndent(r, "", "  ")
	t.Logf("report:\n%s", full)
	if r.Limits.MaxPerIP != fx.maxPerIP {
		t.Errorf("max per IP: %d, want %d (%s)", r.Limits.MaxPerIP, fx.maxPerIP, r.Limits.Refusal)
	}

	got, _ := json.MarshalIndent(r.Table, "", "  ")
	got = append(got, '\n')
	// PROBE_GOLDEN_OUT: also write the table (and the full report) there,
	// for CI to upload when it differs.
	if dir := os.Getenv("PROBE_GOLDEN_OUT"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, filepath.Base(golden)), got, 0o644)
		_ = os.WriteFile(filepath.Join(dir, strings.TrimSuffix(filepath.Base(golden), ".json")+".report.json"), full, 0o644)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it); got:\n%s", err, got)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("capability table differs from %s (go test -run TestGolden -update to accept):\n--- got\n%s", golden, got)
	}
}
