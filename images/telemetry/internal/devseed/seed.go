// Package devseed fills a store with made-up installs and devices over the
// last weeks, for the dev server, the panel e2e and its screenshots.
package devseed

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
)

func ptr[T any](v T) *T { return &v }

var countries = []string{"DE", "US", "US", "FR", "GB", "NL", "FI", "BR", "JP", "CA", "PL", "SE", "ZZ"}
var stackVersions = []string{"1.44.0", "1.45.0", "1.46.1", "1.47.0", "1.48.0"}

// Seed writes n installs and 4n devices reporting daily for up to `days`
// days before now. Deterministic for a seed.
func Seed(ctx context.Context, st *store.Store, now time.Time, n, days int, seed uint64) error {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%032x", rng.Uint64())[:16] + fmt.Sprintf("%016x", rng.Uint64())
		start := rng.IntN(days) // days ago it started
		stop := 0               // days ago it stopped
		if rng.IntN(8) == 0 {
			stop = rng.IntN(start + 1)
		}
		country := countries[rng.IntN(len(countries))]
		vi := rng.IntN(len(stackVersions))
		addon := rng.IntN(5) == 0
		accounts := []int{3, 12, 40, 80, 300, 900, 2500}[rng.IntN(7)]
		restarts := 0
		// The hardware, from its own generator so the rest of the seed stays as it was.
		hw := rand.New(rand.NewPCG(seed+uint64(i), 0x5bd1e995))
		size := hostSize(hw)
		for d := start; d >= stop; d-- {
			t := now.Add(-time.Duration(d)*24*time.Hour - time.Duration(rng.IntN(20))*time.Hour)
			if t.After(now) {
				t = now
			}
			if d < start/2 && vi < len(stackVersions)-1 && rng.IntN(6) == 0 {
				vi++
			}
			r := report(rng, id, stackVersions[vi], addon, accounts, d == start)
			if d < 6 && i%7 == 3 {
				r.Health.Updates = schema.Updates{Attempts: 1, Failures: 1, Rollbacks: 1, Errors: []string{"checks"}}
				r.Health.Healthy = false
			}
			if d < 3 && i%11 == 5 {
				restarts += 5
				r.Health.Services["ergo"] = schema.ServiceHealth{Restarts: restarts, State: "restarting"}
				r.Health.Healthy = false
			}
			if size != nil {
				r.HostSize = size
				// Smaller boxes run fuller.
				h := r.Health.Host
				h.CPUP95 = ptr(clampPct(float64(70-8*slices.Index(schema.CPUBuckets, *size.CPUCores)) + hw.NormFloat64()*8))
				h.MemP95 = ptr(clampPct(float64(85-7*slices.Index(schema.RAMBuckets, *size.RAMGB)) + hw.NormFloat64()*6))
				if size.DiskGB != nil {
					h.DiskUsed = ptr(clampPct(float64(75-8*slices.Index(schema.DiskBuckets, *size.DiskGB)) + hw.NormFloat64()*8))
				}
			}
			if i%13 == 2 && r.Health.Host != nil {
				r.Health.Host.DiskUsed = ptr(94.5)
			}
			if _, err := st.PutReport(ctx, r, country, t); err != nil {
				return err
			}
		}
	}
	plats := []string{"macos", "windows", "linux", "ios", "android", "web", "web", "android"}
	appVersions := []string{"0.104.0", "0.105.0", "0.105.2"}
	for i := 0; i < 4*n; i++ {
		id := fmt.Sprintf("%016x%016x", rng.Uint64(), rng.Uint64())
		p := plats[rng.IntN(len(plats))]
		start := rng.IntN(days)
		country := countries[rng.IntN(len(countries))]
		for d := start; d >= 0; d-- {
			if rng.IntN(4) == 0 {
				continue
			}
			v := appVersions[min(len(appVersions)-1, (start-d)/10)]
			crashes := 0
			if rng.IntN(12) == 0 {
				crashes = 1 + rng.IntN(3)
			}
			a := &schema.App{Schema: 1, DeviceID: id, AppVersion: v, Platform: p, Channel: "stable", Crashes: crashes,
				Servers: schema.Bucket(1 + rng.IntN(4))}
			t := now.Add(-time.Duration(d)*24*time.Hour - time.Duration(rng.IntN(20))*time.Hour)
			if t.After(now) {
				t = now
			}
			if _, err := st.PutApp(ctx, a, country, t); err != nil {
				return err
			}
		}
	}
	return nil
}

func clampPct(v float64) float64 { return math.Round(min(max(v, 1), 99)*10) / 10 }

// hostSize is a made-up host: most are small VPSes, 1 in 6 is an older
// admin without host_size (nil), and without metrics the disk is unknown.
func hostSize(rng *rand.Rand) *schema.HostSize {
	if rng.IntN(6) == 0 {
		return nil
	}
	pick := func(b []string, weights ...int) *string {
		total := 0
		for _, w := range weights {
			total += w
		}
		n := rng.IntN(total)
		for i, w := range weights {
			if n < w {
				return ptr(b[i])
			}
			n -= w
		}
		return ptr(b[len(b)-1])
	}
	hs := &schema.HostSize{
		CPUCores: pick(schema.CPUBuckets, 14, 30, 28, 16, 8, 3, 1),
		RAMGB:    pick(schema.RAMBuckets, 3, 12, 30, 28, 16, 7, 3, 1),
		DiskGB:   pick(schema.DiskBuckets, 10, 30, 28, 18, 8, 4, 2),
		Arch:     pick(schema.Arches, 70, 28, 2),
	}
	if rng.IntN(8) == 0 {
		hs.DiskGB = nil
	}
	return hs
}

func report(rng *rand.Rand, id, stack string, addon bool, accounts int, first bool) *schema.Report {
	reason := "daily"
	if first {
		reason = "setup"
	}
	mode, ircd := "fresh", "ergo"
	profiles := []string{"admin", "auth", "irc", "metrics", "updates", "voice"}
	if addon {
		mode, ircd = "addon", []string{"unrealircd", "inspircd", "ngircd", "solanum"}[rng.IntN(4)]
		profiles = []string{"admin", "auth", "bridge", "metrics", "voice"}
	}
	if rng.IntN(3) == 0 {
		profiles = append(profiles, "files", "web")
	}
	feature := func(p float64) *string {
		if rng.Float64() < p {
			return ptr(schema.Bucket(rng.IntN(300)))
		}
		return nil
	}
	sso := []string{}
	if rng.IntN(4) == 0 {
		sso = []string{"oidc"}
	}
	r := &schema.Report{
		Schema: 1, InstallID: id, Reason: reason, Stack: schema.Stack{Version: stack},
		Services: map[string]string{"traefik": "3.7.19", "auth": "1.9.2", "quicvoiced": "1.8.0", "admin": "1.32.0", "postgres": "17.6.0"},
		Profiles: profiles,
		Install:  schema.Install{Mode: mode, IRCd: ircd, Arch: []string{"amd64", "amd64", "arm64"}[rng.IntN(3)]},
		Counts: schema.Counts{Accounts: ptr(schema.Bucket(accounts)), Active7d: ptr(schema.Bucket(accounts / 4)),
			Active30d: ptr(schema.Bucket(accounts / 2)), IRCPeak24h: ptr(schema.Bucket(accounts / 6)), Channels: ptr(schema.Bucket(accounts / 10))},
		Features: schema.Features{Voice: feature(0.7), Video: feature(0.2), Files: feature(0.3), Push: feature(0.5)},
		Settings: schema.Settings{Registration: []string{"open", "open", "invite", "closed"}[rng.IntN(4)], GuestAccess: rng.IntN(2) == 0,
			SMTP: rng.IntN(3) > 0, SSO: sso, FederationPeers: schema.Bucket(rng.IntN(3)), DirectoryListed: rng.IntN(2) == 0},
		Health: schema.Health{
			Updates:  schema.Updates{Errors: []string{}},
			Services: map[string]schema.ServiceHealth{"auth": {State: "running"}, "traefik": {State: "running"}},
			Host:     &schema.Host{CPUP95: ptr(float64(5 + rng.IntN(60))), CPUMax: ptr(float64(40 + rng.IntN(60))), MemP95: ptr(float64(30 + rng.IntN(50))), DiskUsed: ptr(float64(20 + rng.IntN(50))), LoadPerCPU: ptr(float64(rng.IntN(60)))},
			Healthy:  true,
		},
		UptimeDays: ptr(rng.IntN(200)),
	}
	if !addon {
		r.Services["ergo"] = stack
	} else {
		r.Services["evrd"] = "0.40.1"
	}
	return r
}
