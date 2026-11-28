package rollup_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/rollup"
	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/testutil"
	"github.com/boson-chat/stack/images/telemetry/internal/vm"
)

var day0 = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func seed(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	for i, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		m := testutil.Report(id)
		if i == 1 {
			m["stack"] = map[string]any{"version": "1.47.0"}
			m["health"].(map[string]any)["updates"] = map[string]any{"attempts": 1, "failures": 1, "rollbacks": 1, "errors": []string{"checks"}}
			m["host_size"] = map[string]any{"cpu_cores": "3-4", "ram_gb": "4-8", "disk_gb": nil, "arch": "arm64"}
		}
		r, err := schema.ParseReport(testutil.JSON(t, m))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutReport(ctx, r, []string{"DE", "US"}[i], day0.Add(10*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := schema.ParseApp(testutil.JSON(t, map[string]any{"schema": 1, "device_id": "cccccccccccccccccccccccccccccccc",
		"app_version": "0.106.0", "platform": "ios", "channel": "stable", "crashes": 2, "servers": "1"}))
	st.PutApp(ctx, a, "FR", day0.Add(12*time.Hour))
	return st
}

func find(samples []vm.Sample, name string, labels map[string]string) (float64, bool) {
	for _, s := range samples {
		if s.Name != name {
			continue
		}
		ok := true
		for k, v := range labels {
			if s.Labels[k] != v {
				ok = false
			}
		}
		if ok && len(s.Labels) == len(labels) {
			return s.Value, true
		}
	}
	return 0, false
}

func TestDay(t *testing.T) {
	st := seed(t)
	r := &rollup.Rollup{Store: st, Now: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s, err := r.Day(context.Background(), "2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"boson_installs_active", map[string]string{}, 2},
		{"boson_installs_new", map[string]string{}, 2},
		{"boson_installs_version", map[string]string{"service": "stack", "version": "1.48.0"}, 1},
		{"boson_installs_version", map[string]string{"service": "stack", "version": "1.47.0"}, 1},
		{"boson_installs_version", map[string]string{"service": "auth", "version": "1.9.2-rc.1"}, 2},
		{"boson_installs_country", map[string]string{"country": "US"}, 1},
		{"boson_installs_problem", map[string]string{"problem": "update_failed"}, 1},
		{"boson_installs_feature", map[string]string{"feature": "voice"}, 2},
		{"boson_installs_setting", map[string]string{"setting": "sso", "value": "oidc"}, 2},
		{"boson_installs_setting", map[string]string{"setting": "guest_first", "value": "unknown"}, 2},
		{"boson_people_lo", map[string]string{"count": "accounts"}, 22},
		{"boson_people_hi", map[string]string{"count": "accounts"}, 100},
		{"boson_app_devices", map[string]string{"platform": "ios", "version": "0.106.0", "channel": "stable"}, 1},
		{"boson_app_crashes", map[string]string{"platform": "ios", "version": "0.106.0"}, 2},
		// One install without host_size (an older sender), one with it.
		{"boson_installs_host_size", map[string]string{"dim": "cpu_cores", "bucket": "3-4"}, 1},
		{"boson_installs_host_size", map[string]string{"dim": "cpu_cores", "bucket": "unknown"}, 1},
		{"boson_installs_host_size", map[string]string{"dim": "ram_gb", "bucket": "4-8"}, 1},
		{"boson_installs_host_size", map[string]string{"dim": "disk_gb", "bucket": "unknown"}, 2},
		{"boson_installs_host_size", map[string]string{"dim": "arch", "bucket": "arm64"}, 1},
	} {
		got, ok := find(s, c.name, c.labels)
		if !ok || got != c.want {
			t.Errorf("%s%v = %v (found %v), want %v", c.name, c.labels, got, ok, c.want)
		}
	}
	for _, x := range s {
		if !x.Time.Equal(day0) {
			t.Errorf("%s at %s, want the day's 00:00 UTC", x.Name, x.Time)
		}
		for k, v := range x.Labels {
			if strings.Contains(v, "aaaa") || strings.Contains(v, "bbbb") || k == "id" || k == "install" {
				t.Errorf("an ID in a label: %s=%s", k, v)
			}
		}
	}
}

func TestCatchupMarksOnlyWrittenDays(t *testing.T) {
	st := seed(t)
	fake := &vm.Fake{Fail: errors.New("down")}
	now := day0.Add(3 * 24 * time.Hour)
	r := &rollup.Rollup{Store: st, VM: fake, Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := r.Catchup(context.Background()); err == nil {
		t.Fatal("no error with VictoriaMetrics down")
	}
	if done, _ := st.RolledUp(context.Background(), "2026-11-20"); done {
		t.Fatal("marked done without a write")
	}
	fake.Fail = nil
	if err := r.Catchup(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := len(fake.Samples)
	if n == 0 {
		t.Fatal("nothing written")
	}
	if err := r.Catchup(context.Background()); err != nil || len(fake.Samples) != n {
		t.Errorf("a day written twice: %d -> %d", n, len(fake.Samples))
	}
	if done, _ := st.RolledUp(context.Background(), "2026-11-22"); !done {
		t.Error("empty days in between aren't marked")
	}
	if done, _ := st.RolledUp(context.Background(), "2026-11-23"); done {
		t.Error("today was rolled up before it ended")
	}
}

// Against a real VictoriaMetrics (CI starts one: TELEMETRY_TEST_VM_URL).
func TestRealVictoriaMetrics(t *testing.T) {
	url := os.Getenv("TELEMETRY_TEST_VM_URL")
	if url == "" {
		t.Skip("TELEMETRY_TEST_VM_URL not set")
	}
	st := seed(t)
	c := &vm.Client{URL: url, HTTP: http.DefaultClient}
	now := day0.Add(2 * 24 * time.Hour)
	r := &rollup.Rollup{Store: st, VM: c, Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := r.Catchup(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Make the import visible to queries.
	resp, err := http.Get(url + "/internal/force_flush")
	if err == nil {
		resp.Body.Close()
	}
	var series []vm.Series
	for i := 0; i < 20; i++ {
		series, err = c.QueryRange(context.Background(), `last_over_time(boson_installs_version{service="stack"}[1d])`, day0.Add(-24*time.Hour), day0.Add(24*time.Hour))
		if err == nil && len(series) == 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil || len(series) != 2 {
		t.Fatalf("series: %+v, %v", series, err)
	}
	for _, s := range series {
		var at float64
		for _, p := range s.Points {
			if p.T == day0.Unix() {
				at = p.V
			}
		}
		if at != 1 {
			t.Errorf("%v: %+v, want 1 at %s", s.Labels, s.Points, day0)
		}
	}
	sum, err := c.QueryRange(context.Background(), `last_over_time(boson_installs_active[1d])`, day0.Add(-24*time.Hour), day0.Add(24*time.Hour))
	if err != nil || len(sum) != 1 || len(sum[0].Points) == 0 || sum[0].Points[0].T != day0.Unix() || sum[0].Points[0].V != 2 {
		t.Errorf("active: %+v %v", sum, err)
	}
}
