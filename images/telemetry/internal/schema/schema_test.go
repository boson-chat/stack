package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/testutil"
)

const id = "3f2a9c0e5b7d41e8a6c2f90b1d4e7a53"

func TestBuckets(t *testing.T) {
	for n, want := range map[int]string{-1: "0", 0: "0", 1: "1", 2: "2-10", 10: "2-10", 11: "11-50", 50: "11-50", 51: "51-100",
		100: "51-100", 101: "101-500", 500: "101-500", 501: "501-1k", 1000: "501-1k", 1001: "1k-5k", 5000: "1k-5k", 5001: "5k+", 1e6: "5k+"} {
		if got := schema.Bucket(n); got != want {
			t.Errorf("Bucket(%d) = %s, want %s", n, got, want)
		}
		lo, hi := schema.BucketRange(schema.Bucket(n))
		if n >= 0 && n <= 5000 && (n < lo || n > hi) {
			t.Errorf("%d not in its bucket's range %d-%d", n, lo, hi)
		}
	}
}

func TestValidReport(t *testing.T) {
	r, err := schema.ParseReport(testutil.JSON(t, testutil.Report(id)))
	if err != nil {
		t.Fatal(err)
	}
	if r.Stack.Version != "1.48.0" {
		t.Errorf("leading v kept: %s", r.Stack.Version)
	}
	if r.Services["auth"] != "1.9.2-rc.1" || *r.Health.Host.CPUP95 != 34.6 || r.Health.Host.LoadPerCPU != nil {
		t.Errorf("normalised wrong: %+v %+v", r.Services, r.Health.Host)
	}
	if strings.Join(r.Profiles, ",") != "admin,auth,irc,metrics" {
		t.Errorf("profiles not sorted: %v", r.Profiles)
	}
	// host may be null as a whole
	m := testutil.Report(id)
	m["health"].(map[string]any)["host"] = nil
	m["uptime_days"] = nil
	if _, err := schema.ParseReport(testutil.JSON(t, m)); err != nil {
		t.Fatal(err)
	}
}

func set(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		m = m[p].(map[string]any)
	}
	if v == "DELETE" {
		delete(m, parts[len(parts)-1])
		return
	}
	m[parts[len(parts)-1]] = v
}

func TestInvalidReports(t *testing.T) {
	cases := map[string]any{
		"schema":                     2,
		"install_id":                 "3F2A9C0E5B7D41E8A6C2F90B1D4E7A53",
		"reason":                     "hourly",
		"stack.version":              "latest",
		"services.redis":             "7.0.0",
		"services.auth":              "1.9.2+build",
		"profiles":                   []string{"irc", "irc"},
		"install.mode":               "bridge",
		"install.ircd":               "irc.example.com",
		"counts.accounts":            "12",
		"counts.channels":            "DELETE",
		"features.push_sent_24h":     "6k",
		"settings.registration":      "public",
		"settings.sso":               []string{"google"},
		"settings.federation_peers":  nil,
		"settings.guest_access":      "yes",
		"settings.smtp":              nil,
		"health.updates.attempts":    101,
		"health.updates.errors":      []string{"connection refused to db.example.com"},
		"health.services.auth":       map[string]any{"restarts": 0},
		"health.services.auth.state": "dead",
		"health.host.cpu_p95":        100.5,
		"health.host.mem_p95":        "DELETE",
		"uptime_days":                9999,
		"hostname":                   "chat.example.com",
		"settings.network_name":      "My Network",
		"health.healthy":             "DELETE",
		"health.updates.failures":    1.5,
	}
	for path, v := range cases {
		m := testutil.Report(id)
		set(m, path, v)
		_, err := schema.ParseReport(testutil.JSON(t, m))
		if err == nil {
			t.Errorf("%s = %v: accepted", path, v)
			continue
		}
		var se *schema.Error
		if !asErr(err, &se) {
			t.Errorf("%s: not a schema.Error: %v", path, err)
		}
	}
	for _, body := range []string{``, `[]`, `{"schema":1}`, `{} {}`, `null`} {
		if _, err := schema.ParseReport([]byte(body)); err == nil {
			t.Errorf("%q accepted", body)
		}
	}
}

func asErr(err error, se **schema.Error) bool {
	e, ok := err.(*schema.Error)
	*se = e
	return ok
}

func TestAppPing(t *testing.T) {
	if _, err := schema.ParseApp(testutil.JSON(t, testutil.App(id))); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{"platform": "freebsd", "channel": "nightly", "crashes": 1001, "servers": "3",
		"app_version": "unknown", "device_id": "x", "server_names": []string{"irc.example.com"}, "schema": "DELETE"} {
		m := testutil.App(id)
		set(m, k, v)
		if _, err := schema.ParseApp(testutil.JSON(t, m)); err == nil {
			t.Errorf("%s = %v accepted", k, v)
		}
	}
}

// The spec's example report must validate.
func TestSpecExample(t *testing.T) {
	ex := `{"schema":1,"install_id":"3f2a9c0e5b7d41e8a6c2f90b1d4e7a53","reason":"daily","stack":{"version":"1.48.0"},
"services":{"traefik":"3.7.19","ergo":"1.48.0","auth":"1.9.2","quicvoiced":"1.8.0","admin":"1.32.0","postgres":"17.6.0","victoriametrics":"1.131.0"},
"profiles":["irc","voice","auth","metrics","admin","updates"],"install":{"mode":"fresh","ircd":"ergo","arch":"amd64"},
"counts":{"accounts":"11-50","active_7d":"2-10","active_30d":"11-50","irc_peak_24h":"2-10","channels":"2-10"},
"features":{"voice_minutes_24h":"51-100","video_minutes_24h":null,"files_uploaded_24h":null,"push_sent_24h":"11-50"},
"settings":{"registration":"open","guest_access":true,"guest_first":null,"smtp":true,"sso":["oidc"],"federation_peers":"0","directory_listed":true},
"health":{"updates":{"attempts":1,"failures":1,"rollbacks":1,"errors":["checks"]},
"services":{"auth":{"restarts":0,"state":"running"},"ergo":{"restarts":7,"state":"restarting"}},
"host":{"cpu_p95":34.5,"cpu_max":98.2,"mem_p95":71.0,"disk_used":62.3,"load_per_cpu":40.1},"healthy":false},"uptime_days":41,
"host_size":{"cpu_cores":"3-4","ram_gb":"4-8","disk_gb":"50-100","arch":"amd64"}}`
	r, err := schema.ParseReport([]byte(ex))
	if err != nil {
		t.Fatal(err)
	}
	p := schema.Of(r, nil)
	if strings.Join(p.Kinds, ",") != "update_failed,rollback,crash_loop,unhealthy" {
		t.Errorf("problems: %v", p.Kinds)
	}
}

func TestProblems(t *testing.T) {
	prev, _ := schema.ParseReport(testutil.JSON(t, testutil.Report(id)))
	m := testutil.Report(id)
	set(m, "health.services.auth", map[string]any{"restarts": 3, "state": "running"})
	set(m, "health.host.disk_used", 90.1)
	set(m, "health.host.cpu_p95", 85.0) // not over
	set(m, "health.host.mem_p95", 95)
	r, err := schema.ParseReport(testutil.JSON(t, m))
	if err != nil {
		t.Fatal(err)
	}
	p := schema.Of(r, prev)
	if strings.Join(p.Kinds, ",") != "crash_loop,memory,disk" || strings.Join(p.Looping, ",") != "auth" {
		b, _ := json.Marshal(p)
		t.Errorf("problems: %s", b)
	}
	if q := schema.Of(r, nil); strings.Contains(strings.Join(q.Kinds, ","), "crash_loop") {
		t.Errorf("restart growth without a previous report: %v", q.Kinds)
	}
}

// host_size is optional in schema 1: a report without it (an older sender)
// is valid, and so is null, an object of nulls or one of buckets.
func TestHostSize(t *testing.T) {
	r, err := schema.ParseReport(testutil.JSON(t, testutil.Report(id)))
	if err != nil || r.HostSize != nil {
		t.Fatalf("without host_size: %v %+v", err, r)
	}
	full := map[string]any{"cpu_cores": "3-4", "ram_gb": "4-8", "disk_gb": "50-100", "arch": "arm64"}
	nulls := map[string]any{"cpu_cores": nil, "ram_gb": nil, "disk_gb": nil, "arch": nil}
	for name, v := range map[string]any{"null": nil, "nulls": nulls, "full": full} {
		m := testutil.Report(id)
		m["host_size"] = v
		r, err := schema.ParseReport(testutil.JSON(t, m))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name == "full" && (r.HostSize == nil || *r.HostSize.CPUCores != "3-4" || *r.HostSize.RAMGB != "4-8" ||
			*r.HostSize.DiskGB != "50-100" || *r.HostSize.Arch != "arm64") {
			t.Errorf("full: %+v", r.HostSize)
		}
		if name == "nulls" && (r.HostSize == nil || r.HostSize.CPUCores != nil || r.HostSize.Arch != nil) {
			t.Errorf("nulls: %+v", r.HostSize)
		}
	}
	// Every bucket of each enum is accepted.
	for field, enum := range map[string][]string{"cpu_cores": schema.CPUBuckets, "ram_gb": schema.RAMBuckets,
		"disk_gb": schema.DiskBuckets, "arch": schema.Arches} {
		for _, b := range enum {
			m := testutil.Report(id)
			hs := map[string]any{"cpu_cores": nil, "ram_gb": nil, "disk_gb": nil, "arch": nil}
			hs[field] = b
			m["host_size"] = hs
			if _, err := schema.ParseReport(testutil.JSON(t, m)); err != nil {
				t.Errorf("%s=%s: %v", field, b, err)
			}
		}
	}
	bad := map[string]any{
		"host_size.cpu_cores": "4",      // an exact count
		"host_size.ram_gb":    "7.7",    // an exact size
		"host_size.disk_gb":   "1000",   // not a bucket
		"host_size.arch":      "x86_64", // Docker's name, not the enum
	}
	for path, v := range bad {
		m := testutil.Report(id)
		m["host_size"] = map[string]any{"cpu_cores": "2", "ram_gb": "2-4", "disk_gb": "25-50", "arch": "amd64"}
		set(m, path, v)
		_, err := schema.ParseReport(testutil.JSON(t, m))
		var se *schema.Error
		if err == nil || !asErr(err, &se) || se.Field != path {
			t.Errorf("%s = %v: %v", path, v, err)
		}
	}
	for name, v := range map[string]any{
		"missing key": map[string]any{"cpu_cores": "2", "ram_gb": "2-4", "arch": "amd64"},
		"extra key":   map[string]any{"cpu_cores": "2", "ram_gb": "2-4", "disk_gb": nil, "arch": "amd64", "hostname": "chat.example.com"},
		"not object":  "4 cores",
		"number":      4,
	} {
		m := testutil.Report(id)
		m["host_size"] = v
		if _, err := schema.ParseReport(testutil.JSON(t, m)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
