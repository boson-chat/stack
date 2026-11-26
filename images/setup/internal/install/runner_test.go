package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/setup/internal/stack"
)

func stackSrc(t *testing.T) string {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "config"), 0o755)
	ex, err := os.ReadFile("../../../../config/boson.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "config", "boson.example.yaml"), ex, 0o644)
	os.WriteFile(filepath.Join(src, "compose.yaml"), []byte("name: boson\n"), 0o644)
	return src
}

func newRunner(t *testing.T, dir, src string, fc *FakeCompose) *Runner {
	r, err := NewRunner(&Runner{Dir: dir, StackSrc: src, Compose: fc, Store: NewStore(dir), Hub: NewHub()})
	if err != nil {
		t.Fatal(err)
	}
	r.Timeouts = Timeouts{Health: 2 * time.Second, Certs: time.Second, Verify: time.Second, FailFor: time.Second, Poll: 10 * time.Millisecond}
	DevSkipNetwork(r)
	return r
}

func plan(t *testing.T, existing map[string]string) stack.Plan {
	a := stack.Defaults()
	a.Domain = "boson.test"
	a.Admin = stack.AdminAnswers{Username: "alice", Email: "alice@boson.test"}
	a.TLS.Email = "alice@boson.test"
	p, err := stack.BuildPlan(a, stack.Secrets{}, existing, stack.NewGenerator(), "999")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wait(t *testing.T, r *Runner) State {
	t.Helper()
	for i := 0; i < 500; i++ {
		if !r.Running() {
			return r.State()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("install didn't finish")
	return State{}
}

func TestInstallFailRetryRerun(t *testing.T) {
	dir, src := t.TempDir(), stackSrc(t)
	fc := &FakeCompose{FailUp: 1}
	r := newRunner(t, dir, src, fc)
	done := 0
	r.OnDone = func(State) { done++ }
	j := Job{Plan: plan(t, nil), AdminPassword: "long enough password"}

	// 1. ports busy: "start" fails, later steps don't run.
	if err := r.Start(j); err != nil {
		t.Fatal(err)
	}
	st := wait(t, r)
	if st.Status != StatusFailed || st.Step("start").Status != StatusFailed || st.Step("admin").Status != StatusPending {
		t.Fatalf("after failure: %+v", st)
	}
	if !strings.Contains(st.Step("start").Hint, "Another program uses") {
		t.Fatal(st.Step("start").Hint)
	}
	// Saved: a restarted wizard sees the failure.
	if st2, ok, _ := NewStore(dir).Load(); !ok || st2.Step("start").Status != StatusFailed {
		t.Fatal("state not saved")
	}
	// .env is private, config/boson.yaml exists.
	if fi, err := os.Stat(filepath.Join(dir, ".env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env: %v %v", fi, err)
	}
	envBefore, _ := os.ReadFile(filepath.Join(dir, ".env"))

	// 2. retry: everything passes, the admin is created once.
	if err := r.Start(j); err != nil {
		t.Fatal(err)
	}
	st = wait(t, r)
	if st.Status != StatusDone || !st.AdminCreated || done != 1 {
		t.Fatalf("after retry: %+v", st)
	}
	if fc.Users["alice"] != "owner alice@boson.test" {
		t.Fatal(fc.Users)
	}
	removed := false
	for _, c := range fc.Calls {
		removed = removed || c == "rm ergo"
	}
	if !removed {
		t.Fatalf("the retry didn't remove the container the failed attempt left: %v", fc.Calls)
	}
	envAfter, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(envBefore) != string(envAfter) {
		t.Fatal("retry changed .env")
	}

	// 3. a new wizard (install.sh again): no password, same answers from
	// the existing .env: nothing changes, no second admin.
	ex, _ := stack.LoadExisting(dir)
	r2 := newRunner(t, dir, src, fc)
	if r2.State().Status != "idle" || !r2.State().AdminCreated {
		t.Fatalf("re-run state: %+v", r2.State())
	}
	creates := 0
	for _, c := range fc.Calls {
		if strings.Contains(c, "user create") {
			creates++
		}
	}
	if err := r2.Start(Job{Plan: plan(t, ex.Env)}); err != nil {
		t.Fatal(err)
	}
	st = wait(t, r2)
	if st.Status != StatusDone {
		t.Fatalf("re-run: %+v", st)
	}
	n := 0
	for _, c := range fc.Calls {
		if strings.Contains(c, "user create") {
			n++
		}
	}
	if n != creates {
		t.Fatal("re-run created the admin again")
	}
	envRerun, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(envRerun) != string(envAfter) {
		t.Fatalf("re-run changed .env:\n%s", envRerun)
	}
	// Secrets never land in the state file.
	b, _ := os.ReadFile(filepath.Join(dir, ".setup", "state.json"))
	for _, v := range plan(t, ex.Env).SecretValues() {
		if strings.Contains(string(b), v) {
			t.Fatal("secret in state.json")
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".setup", "state.json")); fi.Mode().Perm() != 0o600 {
		t.Fatal("state.json mode")
	}
}

func TestAdminNameTaken(t *testing.T) {
	dir, src := t.TempDir(), stackSrc(t)
	fc := &FakeCompose{Users: map[string]string{"alice": "user mallory@evil.test"}}
	r := newRunner(t, dir, src, fc)
	r.Start(Job{Plan: plan(t, nil), AdminPassword: "long enough password"})
	st := wait(t, r)
	if st.Step("admin").Status != StatusFailed || !strings.Contains(st.Step("admin").Error, "another email") {
		t.Fatalf("%+v", st.Step("admin"))
	}
	if fc.Users["alice"] != "user mallory@evil.test" {
		t.Fatal("someone else's account got the Owner role")
	}
}

func TestAdminNeedsPassword(t *testing.T) {
	dir, src := t.TempDir(), stackSrc(t)
	r := newRunner(t, dir, src, &FakeCompose{})
	r.Start(Job{Plan: plan(t, nil)})
	st := wait(t, r)
	if s := st.Step("admin"); s.Status != StatusFailed || !strings.HasPrefix(s.Hint, "need_admin_password") {
		t.Fatalf("%+v", s)
	}
}

func TestInterruptedInstallIsFailed(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	s := State{Version: 1, Status: StatusRunning, Steps: Steps()}
	s.Steps[2].Status = StatusRunning
	st.Save(s)
	got, _, _ := st.Load()
	if got.Status != StatusFailed || got.Steps[2].Status != StatusFailed {
		t.Fatalf("%+v", got)
	}
}

func TestEvaluateAndParsePS(t *testing.T) {
	rows, err := parsePS([]byte(`{"Service":"auth","State":"running","Health":"","ExitCode":0}
{"Service":"config-init","State":"exited","ExitCode":0}
{"Service":"ergo","State":"running","Health":"starting"}
{"Service":"minio-init","State":"exited","ExitCode":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var have []ServiceStatus
	for _, r := range rows {
		have = append(have, ServiceStatus{Service: r.Service, State: r.State, Health: r.Health, ExitCode: r.ExitCode})
	}
	ev := Evaluate([]string{"auth", "config-init", "ergo", "minio-init", "web"}, have)
	want := map[string][2]bool{"auth": {true, false}, "config-init": {true, false}, "ergo": {false, false}, "minio-init": {false, true}, "web": {false, false}}
	for _, s := range ev {
		if w := want[s.Service]; s.Ready != w[0] || s.Failed != w[1] {
			t.Errorf("%s: %+v", s.Service, s)
		}
	}
	if arr, err := parsePS([]byte(`[{"Service":"a","State":"running"}]`)); err != nil || len(arr) != 1 {
		t.Fatal(arr, err)
	}
}
