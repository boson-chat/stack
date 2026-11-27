package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/vm"
)

// Querier reads the daily points back.
type Querier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time) ([]vm.Series, error)
}

// Panel serves the panel.
type Panel struct {
	Store        *store.Store
	VM           Querier
	Username     string
	PasswordHash []byte
	Proxies      []netip.Prefix
	Static       fs.FS // the built Svelte app (index.html + assets)
	Now          func() time.Time
	Log          *slog.Logger

	fails failures
}

// Handler is the panel's mux.
func (p *Panel) Handler() http.Handler {
	p.fails.m = map[netip.Addr][]time.Time{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Stats") != "1" {
			jsonErr(w, http.StatusForbidden, "missing X-Stats header")
			return
		}
		p.login(w, r)
	})
	mux.HandleFunc("POST /api/logout", p.guard(p.logout))
	mux.HandleFunc("GET /api/me", p.guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"username": p.Username})
	}))
	mux.HandleFunc("GET /api/overview", p.guard(p.overview))
	mux.HandleFunc("GET /api/versions", p.guard(p.versions))
	mux.HandleFunc("GET /api/features", p.guard(p.features))
	mux.HandleFunc("GET /api/problems", p.guard(p.problems))
	mux.HandleFunc("GET /api/installs/{id}", p.guard(p.install))
	mux.HandleFunc("GET /api/apps", p.guard(p.apps))
	mux.HandleFunc("GET /api/countries", p.guard(p.countries))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { jsonErr(w, http.StatusNotFound, "not found") })
	mux.HandleFunc("/", p.static)
	return secure(mux)
}

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("Strict-Transport-Security", "max-age=31536000")
		h.ServeHTTP(w, r)
	})
}

// static serves the app; unknown paths get index.html (client routing).
func (p *Panel) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name != "" {
		if st, err := fs.Stat(p.Static, name); err == nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, p.Static, name)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFileFS(w, r, p.Static, "index.html")
}

// ---- helpers -----------------------------------------------------------------

type count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func counts(m map[string]int) []count {
	out := make([]count, 0, len(m))
	for k, v := range m {
		out = append(out, count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Short is the panel's install/device handle: the hash's first 12 chars.
func Short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (p *Panel) series(ctx context.Context, query string, days int) ([]vm.Series, string) {
	if p.VM == nil {
		return nil, "VictoriaMetrics not configured"
	}
	end := p.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -days)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s, err := p.VM.QueryRange(ctx, query, start, end)
	if err != nil {
		p.Log.Warn("panel: query failed", "query", query, "err", err)
		return nil, "VictoriaMetrics unavailable"
	}
	return s, ""
}

func days(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || n < 7 || n > 730 {
		return def
	}
	return n
}

func weekStart(t time.Time) time.Time {
	t = t.UTC().Truncate(24 * time.Hour)
	wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
	return t.AddDate(0, 0, -wd)
}

// ---- Overview ----------------------------------------------------------------

type rangeSum struct {
	Lo       int  `json:"lo"`
	Hi       int  `json:"hi"`
	Reported int  `json:"reported"` // installs that sent this count
	AtLeast  bool `json:"at_least"` // a 5k+ bucket was in it
}

func (p *Panel) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := p.Now()
	all, err := p.Store.AllInstalls(ctx)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	active := map[string]int{"d1": 0, "d7": 0, "d30": 0}
	people := map[string]*rangeSum{"accounts": {}, "active_7d": {}, "active_30d": {}, "irc_peak_24h": {}}
	type week struct {
		Week    string `json:"week"`
		New     int    `json:"new"`
		Stopped int    `json:"stopped"`
	}
	const nWeeks = 12
	first := weekStart(now).AddDate(0, 0, -7*(nWeeks-1))
	weeks := make([]week, nWeeks)
	for i := range weeks {
		weeks[i].Week = first.AddDate(0, 0, 7*i).Format("2006-01-02")
	}
	widx := func(t time.Time) int {
		d := int(weekStart(t).Sub(first).Hours() / (24 * 7))
		if d < 0 || d >= nWeeks {
			return -1
		}
		return d
	}
	for _, in := range all {
		age := now.Sub(in.LastSeen)
		if age <= 24*time.Hour {
			active["d1"]++
		}
		if age <= 7*24*time.Hour {
			active["d7"]++
			if in.Report != nil {
				c := in.Report.Counts
				for name, b := range map[string]*string{"accounts": c.Accounts, "active_7d": c.Active7d, "active_30d": c.Active30d, "irc_peak_24h": c.IRCPeak24h} {
					if b == nil {
						continue
					}
					lo, hi := schema.BucketRange(*b)
					s := people[name]
					s.Lo += lo
					s.Hi += hi
					s.Reported++
					s.AtLeast = s.AtLeast || *b == "5k+"
				}
			}
		}
		if age <= 30*24*time.Hour {
			active["d30"]++
		}
		if i := widx(in.FirstSeen); i >= 0 {
			weeks[i].New++
		}
		// Stopped reporting: silent for over 7 days, counted in the week of its last report.
		if age > 7*24*time.Hour {
			if i := widx(in.LastSeen); i >= 0 {
				weeks[i].Stopped++
			}
		}
	}
	devs, _ := p.Store.DevicesSeenSince(ctx, now.Add(-30*24*time.Hour))
	dev := map[string]int{"d1": 0, "d7": 0, "d30": 0}
	for _, d := range devs {
		age := now.Sub(d.LastSeen)
		if age <= 24*time.Hour {
			dev["d1"]++
		}
		if age <= 7*24*time.Hour {
			dev["d7"]++
		}
		dev["d30"]++
	}
	n := days(r, 90)
	act, e1 := p.series(ctx, `last_over_time(boson_installs_active[1d])`, n)
	dau, e2 := p.series(ctx, `sum(last_over_time(boson_app_devices[1d]))`, n)
	writeJSON(w, map[string]any{
		"active": active, "devices": dev, "total": len(all), "weeks": weeks, "people": people,
		"series": map[string]any{"installs": act, "devices": dau}, "series_error": firstNonEmpty(e1, e2),
	})
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---- Versions ----------------------------------------------------------------

// semverLess orders versions; "unknown" sorts first.
func semverLess(a, b string) bool {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			return pa.n[i] < pb.n[i]
		}
	}
	// A pre-release sorts before its release.
	if (pa.pre == "") != (pb.pre == "") {
		return pa.pre != ""
	}
	return pa.pre < pb.pre
}

type semver struct {
	n   [3]int
	pre string
}

func parseSemver(v string) semver {
	var s semver
	if v == "unknown" {
		return semver{n: [3]int{-1, -1, -1}}
	}
	core, pre, _ := strings.Cut(v, "-")
	s.pre = pre
	for i, f := range strings.SplitN(core, ".", 3) {
		s.n[i], _ = strconv.Atoi(f)
	}
	return s
}

type versionCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

func (p *Panel) versions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := p.Now()
	act, err := p.Store.InstallsSeenSince(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	behind, err := strconv.Atoi(r.URL.Query().Get("behind"))
	if err != nil || behind < 1 || behind > 50 {
		behind = 3
	}
	per := map[string]map[string]int{}
	for _, in := range act {
		if in.Report == nil {
			continue
		}
		bump := func(svc, v string) {
			if per[svc] == nil {
				per[svc] = map[string]int{}
			}
			per[svc][v]++
		}
		bump("stack", in.Report.Stack.Version)
		for s, v := range in.Report.Services {
			bump(s, v)
		}
	}
	type svc struct {
		Service  string         `json:"service"`
		Installs int            `json:"installs"`
		Versions []versionCount `json:"versions"`
	}
	var services []svc
	for name, m := range per {
		s := svc{Service: name}
		for v, n := range m {
			s.Versions = append(s.Versions, versionCount{v, n})
			s.Installs += n
		}
		sort.Slice(s.Versions, func(i, j int) bool { return semverLess(s.Versions[j].Version, s.Versions[i].Version) })
		services = append(services, s)
	}
	sort.Slice(services, func(i, j int) bool {
		if (services[i].Service == "stack") != (services[j].Service == "stack") {
			return services[i].Service == "stack"
		}
		return services[i].Service < services[j].Service
	})
	// Installs whose stack is N or more releases behind the newest seen.
	var releases []string
	for v := range per["stack"] {
		if v != "unknown" && !strings.Contains(v, "-") {
			releases = append(releases, v)
		}
	}
	sort.Slice(releases, func(i, j int) bool { return semverLess(releases[j], releases[i]) })
	type old struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		Behind  int    `json:"behind"`
		Country string `json:"country"`
	}
	var olds []old
	if len(releases) > behind {
		cut := releases[behind] // this one and older are >= behind releases old
		for _, in := range act {
			v := in.Stack
			if v == "unknown" || semverLess(cut, v) {
				continue
			}
			b := len(releases)
			for i, rel := range releases {
				if !semverLess(v, rel) {
					b = i
					break
				}
			}
			olds = append(olds, old{Short(in.ID), v, b, in.Country})
		}
	}
	sort.Slice(olds, func(i, j int) bool { return olds[i].Behind > olds[j].Behind })
	latest := ""
	if len(releases) > 0 {
		latest = releases[0]
	}
	hist, e := p.series(ctx, `topk_max(8, last_over_time(boson_installs_version{service="stack"}[1d]))`, days(r, 90))
	writeJSON(w, map[string]any{"installs": len(act), "services": services, "latest": latest, "releases": releases,
		"behind": behind, "old": olds, "series": hist, "series_error": e})
}

// ---- Features ----------------------------------------------------------------

type tf struct {
	Name    string `json:"name"`
	True    int    `json:"true"`
	False   int    `json:"false"`
	Unknown int    `json:"unknown"`
}

func (p *Panel) features(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	act, err := p.Store.InstallsSeenSince(ctx, p.Now().Add(-7*24*time.Hour))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	profiles, feats, sso, reg, modes, fed := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	flags := map[string]*tf{}
	flag := func(name string, v *bool) {
		f := flags[name]
		if f == nil {
			f = &tf{Name: name}
			flags[name] = f
		}
		switch {
		case v == nil:
			f.Unknown++
		case *v:
			f.True++
		default:
			f.False++
		}
	}
	n := 0
	for _, in := range act {
		rep := in.Report
		if rep == nil {
			continue
		}
		n++
		for _, pr := range rep.Profiles {
			profiles[pr]++
		}
		for name, b := range map[string]*string{"voice": rep.Features.Voice, "video": rep.Features.Video, "files": rep.Features.Files, "push": rep.Features.Push} {
			if b != nil && *b != "0" {
				feats[name]++
			}
		}
		for _, k := range rep.Settings.SSO {
			sso[k]++
		}
		if len(rep.Settings.SSO) == 0 {
			sso["none"]++
		}
		reg[rep.Settings.Registration]++
		modes[rep.Install.Mode+" · "+rep.Install.IRCd]++
		fed[rep.Settings.FederationPeers]++
		s := rep.Settings
		flag("guest_access", &s.GuestAccess)
		flag("guest_first", s.GuestFirst)
		flag("smtp", &s.SMTP)
		flag("directory_listed", &s.DirectoryListed)
	}
	var fl []tf
	for _, k := range []string{"guest_access", "guest_first", "smtp", "directory_listed"} {
		if f := flags[k]; f != nil {
			fl = append(fl, *f)
		}
	}
	var fedOut []count
	for _, b := range schema.Buckets {
		if fed[b] > 0 {
			fedOut = append(fedOut, count{b, fed[b]})
		}
	}
	hist, e := p.series(ctx, `last_over_time(boson_installs_feature[1d])`, days(r, 90))
	writeJSON(w, map[string]any{"installs": n, "profiles": counts(profiles), "features": counts(feats), "sso": counts(sso),
		"registration": counts(reg), "modes": counts(modes), "federation": fedOut, "flags": fl,
		"series": hist, "series_error": e})
}

// ---- Problems ----------------------------------------------------------------

type problemRow struct {
	ID       string          `json:"id"`
	Country  string          `json:"country"`
	Stack    string          `json:"stack"`
	LastSeen time.Time       `json:"last_seen"`
	Problems schema.Problems `json:"problems"`
	Host     *schema.Host    `json:"host"`
	Healthy  bool            `json:"healthy"`
}

func (p *Panel) problems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := p.Now()
	since := store.Day(now.AddDate(0, 0, -7))
	// One day earlier too, for the first day's restart growth.
	rows, err := p.Store.ReportsSince(ctx, store.Day(now.AddDate(0, 0, -8)))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	act, _ := p.Store.InstallsSeenSince(ctx, now.AddDate(0, 0, -8))
	latest := map[string]store.Install{}
	for _, in := range act {
		latest[in.ID] = in
	}
	byID := map[string]*problemRow{}
	var prev *schema.Report
	prevID := ""
	for _, row := range rows {
		if row.ID != prevID {
			prev, prevID = nil, row.ID
		}
		if row.Day >= since {
			pr := schema.Of(row.Report, prev)
			if len(pr.Kinds) > 0 {
				pe := byID[row.ID]
				if pe == nil {
					pe = &problemRow{ID: Short(row.ID)}
					byID[row.ID] = pe
				}
				pe.Problems.Merge(pr)
			}
		}
		prev = row.Report
	}
	kinds := map[string]int{}
	var out []problemRow
	for id, pe := range byID {
		if in, ok := latest[id]; ok {
			pe.Country, pe.Stack, pe.LastSeen = in.Country, in.Stack, in.LastSeen
			if in.Report != nil {
				pe.Host, pe.Healthy = in.Report.Health.Host, in.Report.Health.Healthy
			}
		}
		for _, k := range pe.Problems.Kinds {
			kinds[k]++
		}
		out = append(out, *pe)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Problems.Kinds) != len(out[j].Problems.Kinds) {
			return len(out[i].Problems.Kinds) > len(out[j].Problems.Kinds)
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	var kc []count
	for _, k := range schema.ProblemKinds {
		kc = append(kc, count{k, kinds[k]})
	}
	hist, e := p.series(ctx, `last_over_time(boson_installs_problem[1d])`, days(r, 90))
	writeJSON(w, map[string]any{"installs": out, "kinds": kc,
		"thresholds": map[string]any{"cpu_p95": schema.CPUThreshold, "mem_p95": schema.MemoryThreshold, "disk_used": schema.DiskThreshold, "restart_growth": schema.RestartGrowthLoop},
		"series":     hist, "series_error": e})
}

func (p *Panel) install(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := strings.ToLower(r.PathValue("id"))
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			jsonErr(w, http.StatusNotFound, "no such install")
			return
		}
	}
	in, err := p.Store.InstallByPrefix(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "no such install")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	hist, err := p.Store.History(ctx, in.ID)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	type day struct {
		Day      string          `json:"day"`
		Received time.Time       `json:"received"`
		Country  string          `json:"country"`
		Problems schema.Problems `json:"problems"`
		Report   map[string]any  `json:"report"`
	}
	out := make([]day, len(hist))
	for i, h := range hist {
		var prev *schema.Report
		if i+1 < len(hist) {
			prev = hist[i+1].Report
		}
		// The raw install ID never leaves the server.
		var rep map[string]any
		b, _ := json.Marshal(h.Report)
		_ = json.Unmarshal(b, &rep)
		delete(rep, "install_id")
		out[i] = day{h.Day, h.Received, h.Country, schema.Of(h.Report, prev), rep}
	}
	writeJSON(w, map[string]any{"id": Short(in.ID), "first_seen": in.FirstSeen, "last_seen": in.LastSeen,
		"country": in.Country, "stack": in.Stack, "history": out})
}

// ---- Apps --------------------------------------------------------------------

func (p *Panel) apps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := p.Now()
	devs, err := p.Store.DevicesSeenSince(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	type pv struct {
		Platform string `json:"platform"`
		Version  string `json:"version"`
		Channel  string `json:"channel"`
		D1       int    `json:"d1"`
		D7       int    `json:"d7"`
	}
	plat := map[string]int{}
	pvs := map[string]*pv{}
	for _, d := range devs {
		k := d.Platform + "\x00" + d.Version + "\x00" + d.Channel
		e := pvs[k]
		if e == nil {
			e = &pv{Platform: d.Platform, Version: d.Version, Channel: d.Channel}
			pvs[k] = e
		}
		e.D7++
		if now.Sub(d.LastSeen) <= 24*time.Hour {
			e.D1++
			plat[d.Platform]++
		}
	}
	var versions []pv
	for _, e := range pvs {
		versions = append(versions, *e)
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].D1 != versions[j].D1 {
			return versions[i].D1 > versions[j].D1
		}
		if versions[i].Platform != versions[j].Platform {
			return versions[i].Platform < versions[j].Platform
		}
		return semverLess(versions[j].Version, versions[i].Version)
	})
	// Crashes per version over 7 days.
	rows, err := p.Store.AppsSince(ctx, store.Day(now.AddDate(0, 0, -6)))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	type cr struct {
		Platform string  `json:"platform"`
		Version  string  `json:"version"`
		Crashes  int     `json:"crashes"`
		Devices  int     `json:"devices"`
		PerDev   float64 `json:"per_device"`
	}
	crs := map[string]*cr{}
	devSeen := map[string]map[string]bool{}
	servers := map[string]int{}
	latestDay := map[string]string{}
	for _, a := range rows {
		k := a.Platform + "\x00" + a.Version
		e := crs[k]
		if e == nil {
			e = &cr{Platform: a.Platform, Version: a.Version}
			crs[k] = e
			devSeen[k] = map[string]bool{}
		}
		e.Crashes += a.Crashes
		devSeen[k][a.ID] = true
		if a.Day >= latestDay[a.ID] {
			latestDay[a.ID] = a.Day
		}
	}
	for _, a := range rows {
		if latestDay[a.ID] == a.Day {
			servers[a.Servers]++
		}
	}
	var crashes []cr
	for k, e := range crs {
		e.Devices = len(devSeen[k])
		if e.Devices > 0 {
			e.PerDev = float64(e.Crashes) / float64(e.Devices)
		}
		crashes = append(crashes, *e)
	}
	sort.Slice(crashes, func(i, j int) bool {
		if crashes[i].Crashes != crashes[j].Crashes {
			return crashes[i].Crashes > crashes[j].Crashes
		}
		return semverLess(crashes[j].Version, crashes[i].Version)
	})
	var srv []count
	for _, b := range schema.Buckets {
		if servers[b] > 0 {
			srv = append(srv, count{b, servers[b]})
		}
	}
	hist, e := p.series(ctx, `sum by (platform) (last_over_time(boson_app_devices[1d]))`, days(r, 90))
	writeJSON(w, map[string]any{"platforms": counts(plat), "versions": versions, "crashes": crashes, "servers": srv,
		"series": hist, "series_error": e})
}

// ---- Countries ---------------------------------------------------------------

func (p *Panel) countries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := p.Now().Add(-30 * 24 * time.Hour)
	ins, err := p.Store.InstallsSeenSince(ctx, since)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	devs, err := p.Store.DevicesSeenSince(ctx, since)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	type row struct {
		Country  string `json:"country"`
		Installs int    `json:"installs"`
		Devices  int    `json:"devices"`
	}
	m := map[string]*row{}
	get := func(c string) *row {
		if m[c] == nil {
			m[c] = &row{Country: c}
		}
		return m[c]
	}
	for _, in := range ins {
		get(in.Country).Installs++
	}
	for _, d := range devs {
		get(d.Country).Devices++
	}
	out := make([]row, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Installs != out[j].Installs {
			return out[i].Installs > out[j].Installs
		}
		if out[i].Devices != out[j].Devices {
			return out[i].Devices > out[j].Devices
		}
		return out[i].Country < out[j].Country
	})
	writeJSON(w, map[string]any{"countries": out, "installs": len(ins), "devices": len(devs)})
}
