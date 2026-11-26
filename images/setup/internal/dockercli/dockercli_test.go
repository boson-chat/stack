package dockercli

import "testing"

func TestRedact(t *testing.T) {
	r := &Redactor{}
	r.Set([]string{"supersecretvalue", "abc"})
	r.Add("anothersecret")
	got := r.Redact("pw=supersecretvalue x=abc y=anothersecret")
	if got != "pw=[redacted] x=abc y=[redacted]" {
		t.Fatal(got)
	}
	var nilR *Redactor
	if nilR.Redact("x") != "x" {
		t.Fatal("nil")
	}
}

func TestDescribeHidesArgs(t *testing.T) {
	c := &CLI{Dir: "/d", Project: "boson"}
	if d := c.describe(c.Compose("exec", "-T", "auth", "./auth", "secret")); d != "docker compose exec" {
		t.Fatal(d)
	}
}
