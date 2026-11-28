// Package rollup turns each finished UTC day's stored reports and pings
// into daily points in VictoriaMetrics (docs/telemetry.md "Daily
// aggregates"). Labels are enums, versions and country codes only.
package rollup

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/vm"
)

// Writer is where the points go.
type Writer interface {
	Import(ctx context.Context, samples []vm.Sample) error
}

// Rollup writes finished days.
type Rollup struct {
	Store *store.Store
	VM    Writer
	Now   func() time.Time
	Log   *slog.Logger
}

type counter map[string]float64

func key(labels ...string) string { return strings.Join(labels, "\x00") }

// Day computes the samples of one day (YYYY-MM-DD).
func (r *Rollup) Day(ctx context.Context, day string) ([]vm.Sample, error) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil, err
	}
	reports, err := r.Store.ReportsOn(ctx, day)
	if err != nil {
		return nil, err
	}
	apps, err := r.Store.AppsOn(ctx, day)
	if err != nil {
		return nil, err
	}
	newN, err := r.Store.NewOn(ctx, day)
	if err != nil {
		return nil, err
	}
	// The previous report of each install, for restart growth.
	prevDay := t.AddDate(0, 0, -1).Format("2006-01-02")
	prevRows, err := r.Store.ReportsOn(ctx, prevDay)
	if err != nil {
		return nil, err
	}
	prev := map[string]*schema.Report{}
	for _, p := range prevRows {
		prev[p.ID] = p.Report
	}

	var out []vm.Sample
	add := func(name string, c counter, labelNames ...string) {
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			labels := map[string]string{}
			if len(labelNames) > 0 {
				for i, v := range strings.Split(k, "\x00") {
					labels[labelNames[i]] = v
				}
			}
			out = append(out, vm.Sample{Name: name, Labels: labels, Value: c[k], Time: t})
		}
	}

	versions, countries, profiles, modes, settings, features, problems := counter{}, counter{}, counter{}, counter{}, counter{}, counter{}, counter{}
	lo, hi, sizes := counter{}, counter{}, counter{}
	for _, row := range reports {
		rep := row.Report
		versions[key("stack", rep.Stack.Version)]++
		for svc, v := range rep.Services {
			versions[key(svc, v)]++
		}
		countries[key(row.Country)]++
		for _, p := range rep.Profiles {
			profiles[key(p)]++
		}
		modes[key(rep.Install.Mode, rep.Install.IRCd)]++
		for _, d := range schema.HostSizeOf(rep) {
			sizes[key(d.Dim, d.Bucket)]++
		}
		s := rep.Settings
		settings[key("registration", s.Registration)]++
		settings[key("guest_access", fmt.Sprint(s.GuestAccess))]++
		gf := "unknown"
		if s.GuestFirst != nil {
			gf = fmt.Sprint(*s.GuestFirst)
		}
		settings[key("guest_first", gf)]++
		settings[key("smtp", fmt.Sprint(s.SMTP))]++
		settings[key("directory_listed", fmt.Sprint(s.DirectoryListed))]++
		settings[key("federation", fmt.Sprint(s.FederationPeers != "0"))]++
		for _, k := range s.SSO {
			settings[key("sso", k)]++
		}
		for name, b := range map[string]*string{"voice": rep.Features.Voice, "video": rep.Features.Video,
			"files": rep.Features.Files, "push": rep.Features.Push} {
			if b != nil && *b != "0" {
				features[key(name)]++
			}
		}
		for _, k := range schema.Of(rep, prev[row.ID]).Kinds {
			problems[key(k)]++
		}
		for name, b := range map[string]*string{"accounts": rep.Counts.Accounts, "active_7d": rep.Counts.Active7d,
			"active_30d": rep.Counts.Active30d, "irc_peak_24h": rep.Counts.IRCPeak24h} {
			if b != nil {
				l, h := schema.BucketRange(*b)
				lo[key(name)] += float64(l)
				hi[key(name)] += float64(h)
			}
		}
	}
	if len(reports) > 0 || newN > 0 {
		out = append(out,
			vm.Sample{Name: "boson_installs_active", Labels: map[string]string{}, Value: float64(len(reports)), Time: t},
			vm.Sample{Name: "boson_installs_new", Labels: map[string]string{}, Value: float64(newN), Time: t})
	}
	add("boson_installs_version", versions, "service", "version")
	add("boson_installs_country", countries, "country")
	add("boson_installs_profile", profiles, "profile")
	add("boson_installs_mode", modes, "mode", "ircd")
	add("boson_installs_host_size", sizes, "dim", "bucket")
	add("boson_installs_setting", settings, "setting", "value")
	add("boson_installs_feature", features, "feature")
	add("boson_installs_problem", problems, "problem")
	add("boson_people_lo", lo, "count")
	add("boson_people_hi", hi, "count")

	devices, crashes, servers := counter{}, counter{}, counter{}
	for _, a := range apps {
		devices[key(a.Platform, a.Version, a.Channel)]++
		if a.Crashes > 0 {
			crashes[key(a.Platform, a.Version)] += float64(a.Crashes)
		}
		servers[key(a.Servers)]++
	}
	add("boson_app_devices", devices, "platform", "version", "channel")
	add("boson_app_crashes", crashes, "platform", "version")
	add("boson_app_servers", servers, "servers")
	return out, nil
}

// Catchup writes every finished day since the oldest stored row that isn't
// written yet. A day is marked done only once VictoriaMetrics took it.
func (r *Rollup) Catchup(ctx context.Context) error {
	first, err := r.Store.FirstDay(ctx)
	if err != nil || first == "" {
		return err
	}
	today := store.Day(r.Now())
	t, err := time.Parse("2006-01-02", first)
	if err != nil {
		return err
	}
	for ; t.Format("2006-01-02") < today; t = t.AddDate(0, 0, 1) {
		day := t.Format("2006-01-02")
		done, err := r.Store.RolledUp(ctx, day)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		samples, err := r.Day(ctx, day)
		if err != nil {
			return err
		}
		if err := r.VM.Import(ctx, samples); err != nil {
			return fmt.Errorf("%s: %w", day, err)
		}
		if err := r.Store.MarkRolledUp(ctx, day, r.Now()); err != nil {
			return err
		}
		r.Log.Info("rollup: day written", "day", day, "points", len(samples))
	}
	return nil
}

// Run catches up now and then every 30 minutes, pruning old rows too.
func (r *Rollup) Run(ctx context.Context) {
	for {
		if err := r.Catchup(ctx); err != nil {
			r.Log.Warn("rollup failed; retrying later", "err", err)
		}
		if err := r.Store.Prune(ctx, r.Now()); err != nil {
			r.Log.Warn("prune failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Minute):
		}
	}
}
