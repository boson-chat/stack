// Package schema is docs/telemetry.md's schema 1: the install report and the
// app ping, decoded strictly (unknown fields are an error) and validated
// against the enums, buckets and patterns there.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Size caps for the request bodies.
const (
	MaxReportBytes = 16 << 10
	MaxAppBytes    = 2 << 10
)

// Buckets, in order.
var Buckets = []string{"0", "1", "2-10", "11-50", "51-100", "101-500", "501-1k", "1k-5k", "5k+"}

// BucketRange is the lower and upper bound a bucket stands for (5k+ is
// 5001-5001: totals over it are "at least").
func BucketRange(b string) (lo, hi int) {
	switch b {
	case "0":
		return 0, 0
	case "1":
		return 1, 1
	case "2-10":
		return 2, 10
	case "11-50":
		return 11, 50
	case "51-100":
		return 51, 100
	case "101-500":
		return 101, 500
	case "501-1k":
		return 501, 1000
	case "1k-5k":
		return 1001, 5000
	case "5k+":
		return 5001, 5001
	}
	return 0, 0
}

// Bucket puts n in its bucket.
func Bucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 10:
		return "2-10"
	case n <= 50:
		return "11-50"
	case n <= 100:
		return "51-100"
	case n <= 500:
		return "101-500"
	case n <= 1000:
		return "501-1k"
	case n <= 5000:
		return "1k-5k"
	}
	return "5k+"
}

// Enums.
var (
	Services = []string{"traefik", "certsync", "ergo", "roleserv", "evrd", "evrd-links", "postgres",
		"auth", "quicvoiced", "quicvideod", "victoriametrics", "node-exporter", "admin",
		"docker-proxy", "docker-proxy-updates", "api", "directory", "files", "pushrelay",
		"minio", "webroot", "wellknown", "web", "webircgateway", "telemetry",
		"victoriametrics-telemetry"}
	Profiles = []string{"irc", "voice", "video", "auth", "api", "directory", "files", "minio",
		"metrics", "admin", "updates", "webroot", "wellknown", "web", "bridge", "pushrelay", "telemetry"}
	Reasons       = []string{"daily", "setup", "update", "rollback"}
	Modes         = []string{"fresh", "addon", "unknown"}
	IRCds         = []string{"ergo", "unrealircd", "inspircd", "ngircd", "solanum", "charybdis", "hybrid", "other", "unknown"}
	Arches        = []string{"amd64", "arm64", "other"}
	Registrations = []string{"open", "invite", "closed"}
	SSOKinds      = []string{"oidc", "saml", "oauth2", "other"}
	UpdateErrors  = []string{"preflight", "backup", "pull", "migrate", "recreate", "health", "checks",
		"self_update", "rollback", "timeout", "other"}
	ServiceStates = []string{"running", "restarting", "unhealthy", "exited", "created"}
	Platforms     = []string{"macos", "windows", "linux", "ios", "android", "web"}
	// host_size buckets, in order. A bucket includes its upper edge.
	CPUBuckets  = []string{"1", "2", "3-4", "5-8", "9-16", "17-32", "33+"}
	RAMBuckets  = []string{"<1", "1-2", "2-4", "4-8", "8-16", "16-32", "32-64", "64+"}
	DiskBuckets = []string{"<25", "25-50", "50-100", "100-250", "250-500", "500-1000", "1000+"}
	Channels    = []string{"stable", "beta", "dev"}
)

var (
	idRe      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	versionRe = regexp.MustCompile(`^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)
)

// Report is the install report.
type Report struct {
	Schema     int               `json:"schema"`
	InstallID  string            `json:"install_id,omitempty"`
	Reason     string            `json:"reason"`
	Stack      Stack             `json:"stack"`
	Services   map[string]string `json:"services"`
	Profiles   []string          `json:"profiles"`
	Install    Install           `json:"install"`
	Counts     Counts            `json:"counts"`
	Features   Features          `json:"features"`
	Settings   Settings          `json:"settings"`
	Health     Health            `json:"health"`
	UptimeDays *int              `json:"uptime_days"`
	// HostSize is optional (an addition to schema 1): nil when absent or null.
	HostSize *HostSize `json:"host_size,omitempty"`
}

// HostSize is the host's hardware, in buckets. Each field is nil when unknown.
type HostSize struct {
	CPUCores *string `json:"cpu_cores"`
	RAMGB    *string `json:"ram_gb"`
	DiskGB   *string `json:"disk_gb"`
	Arch     *string `json:"arch"`
}

type Stack struct {
	Version string `json:"version"`
}

type Install struct {
	Mode string `json:"mode"`
	IRCd string `json:"ircd"`
	Arch string `json:"arch"`
}

type Counts struct {
	Accounts   *string `json:"accounts"`
	Active7d   *string `json:"active_7d"`
	Active30d  *string `json:"active_30d"`
	IRCPeak24h *string `json:"irc_peak_24h"`
	Channels   *string `json:"channels"`
}

type Features struct {
	Voice *string `json:"voice_minutes_24h"`
	Video *string `json:"video_minutes_24h"`
	Files *string `json:"files_uploaded_24h"`
	Push  *string `json:"push_sent_24h"`
}

type Settings struct {
	Registration    string   `json:"registration"`
	GuestAccess     bool     `json:"guest_access"`
	GuestFirst      *bool    `json:"guest_first"`
	SMTP            bool     `json:"smtp"`
	SSO             []string `json:"sso"`
	FederationPeers string   `json:"federation_peers"`
	DirectoryListed bool     `json:"directory_listed"`
}

type Health struct {
	Updates  Updates                  `json:"updates"`
	Services map[string]ServiceHealth `json:"services"`
	Host     *Host                    `json:"host"`
	Healthy  bool                     `json:"healthy"`
}

type Updates struct {
	Attempts  int      `json:"attempts"`
	Failures  int      `json:"failures"`
	Rollbacks int      `json:"rollbacks"`
	Errors    []string `json:"errors"`
}

type ServiceHealth struct {
	Restarts int    `json:"restarts"`
	State    string `json:"state"`
}

type Host struct {
	CPUP95     *float64 `json:"cpu_p95"`
	CPUMax     *float64 `json:"cpu_max"`
	MemP95     *float64 `json:"mem_p95"`
	DiskUsed   *float64 `json:"disk_used"`
	LoadPerCPU *float64 `json:"load_per_cpu"`
}

// App is the app ping.
type App struct {
	Schema     int    `json:"schema"`
	DeviceID   string `json:"device_id"`
	AppVersion string `json:"app_version"`
	Platform   string `json:"platform"`
	Channel    string `json:"channel"`
	Crashes    int    `json:"crashes"`
	Servers    string `json:"servers"`
}

// HostDims are host_size's fields, in the panel's order.
var HostDims = []string{"cpu_cores", "ram_gb", "disk_gb", "arch"}

// HostDimBuckets is each host_size field's buckets, in order.
var HostDimBuckets = map[string][]string{"cpu_cores": CPUBuckets, "ram_gb": RAMBuckets, "disk_gb": DiskBuckets, "arch": Arches}

// SizeOf is one host_size field's bucket ("unknown" when null or absent).
type SizeOf struct{ Dim, Bucket string }

// HostSizeOf is the report's host_size, one entry per field in HostDims
// order: a report without host_size (an older sender) is unknown on each.
func HostSizeOf(r *Report) []SizeOf {
	var hs HostSize
	if r != nil && r.HostSize != nil {
		hs = *r.HostSize
	}
	out := make([]SizeOf, 0, len(HostDims))
	for i, v := range []*string{hs.CPUCores, hs.RAMGB, hs.DiskGB, hs.Arch} {
		b := "unknown"
		if v != nil {
			b = *v
		}
		out = append(out, SizeOf{HostDims[i], b})
	}
	return out
}

// Error is a validation failure at a JSON path.
type Error struct{ Field string }

func (e *Error) Error() string { return "invalid: " + e.Field }

func bad(f string, a ...any) error { return &Error{Field: fmt.Sprintf(f, a...)} }

// decode decodes exactly one JSON object, refusing unknown fields, and
// checks that every required key is present (encoding/json can't tell an
// absent key from a zero value).
func decode(body []byte, v any, required map[string][]string) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var ue *json.UnmarshalTypeError
		if errors.As(err, &ue) {
			return bad("%s", ue.Field)
		}
		if f, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return bad("%s", strings.Trim(f, `"`))
		}
		return bad("$")
	}
	if _, err := d.Token(); err != io.EOF {
		return bad("$")
	}
	// Required keys (and explicit nulls only where allowed): walk a generic copy.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return bad("$")
	}
	for path, keys := range required {
		obj := raw
		if path != "" {
			for _, p := range strings.Split(path, ".") {
				m, ok := obj[p].(map[string]any)
				if !ok {
					return bad("%s", path)
				}
				obj = m
			}
		}
		for _, k := range keys {
			if _, ok := obj[k]; !ok {
				return bad("%s", strings.TrimPrefix(path+"."+k, "."))
			}
		}
	}
	return nil
}

var reportRequired = map[string][]string{
	"":               {"schema", "install_id", "reason", "stack", "services", "profiles", "install", "counts", "features", "settings", "health"},
	"stack":          {"version"},
	"install":        {"mode", "ircd", "arch"},
	"counts":         {"accounts", "active_7d", "active_30d", "irc_peak_24h", "channels"},
	"features":       {"voice_minutes_24h", "video_minutes_24h", "files_uploaded_24h", "push_sent_24h"},
	"settings":       {"registration", "guest_access", "guest_first", "smtp", "sso", "federation_peers", "directory_listed"},
	"health":         {"updates", "services", "host", "healthy"},
	"health.updates": {"attempts", "failures", "rollbacks", "errors"},
}

var appRequired = map[string][]string{
	"": {"schema", "device_id", "app_version", "platform", "channel", "crashes", "servers"},
}

// Nulls that aren't allowed: objects/strings/bools/arrays decode null as the
// zero value, so check them on the generic copy.
func noNulls(body []byte, paths ...string) error {
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	for _, p := range paths {
		parts := strings.Split(p, ".")
		var cur any = raw
		for _, x := range parts {
			m, ok := cur.(map[string]any)
			if !ok {
				return bad("%s", p)
			}
			cur = m[x]
		}
		if cur == nil {
			return bad("%s", p)
		}
	}
	return nil
}

// ParseReport decodes and validates an install report, and normalises it
// (versions without a leading v, sorted lists).
func ParseReport(body []byte) (*Report, error) {
	var r Report
	if err := decode(body, &r, reportRequired); err != nil {
		return nil, err
	}
	if err := noNulls(body, "schema", "install_id", "reason", "stack", "stack.version", "services", "profiles",
		"install", "install.mode", "install.ircd", "install.arch", "counts", "features", "settings",
		"settings.registration", "settings.guest_access", "settings.smtp", "settings.sso",
		"settings.federation_peers", "settings.directory_listed", "health", "health.updates",
		"health.updates.attempts", "health.updates.failures", "health.updates.rollbacks",
		"health.updates.errors", "health.services", "health.healthy"); err != nil {
		return nil, err
	}
	if r.Schema != 1 {
		return nil, bad("schema")
	}
	if !idRe.MatchString(r.InstallID) {
		return nil, bad("install_id")
	}
	if !slices.Contains(Reasons, r.Reason) {
		return nil, bad("reason")
	}
	var ok bool
	if r.Stack.Version, ok = Version(r.Stack.Version, true); !ok {
		return nil, bad("stack.version")
	}
	if len(r.Services) > len(Services) {
		return nil, bad("services")
	}
	for k, v := range r.Services {
		if !slices.Contains(Services, k) {
			return nil, bad("services.%s", k)
		}
		if r.Services[k], ok = Version(v, true); !ok {
			return nil, bad("services.%s", k)
		}
	}
	if err := enumSet(r.Profiles, Profiles, "profiles"); err != nil {
		return nil, err
	}
	if !slices.Contains(Modes, r.Install.Mode) {
		return nil, bad("install.mode")
	}
	if !slices.Contains(IRCds, r.Install.IRCd) {
		return nil, bad("install.ircd")
	}
	if !slices.Contains(Arches, r.Install.Arch) {
		return nil, bad("install.arch")
	}
	for name, b := range map[string]*string{"counts.accounts": r.Counts.Accounts, "counts.active_7d": r.Counts.Active7d,
		"counts.active_30d": r.Counts.Active30d, "counts.irc_peak_24h": r.Counts.IRCPeak24h, "counts.channels": r.Counts.Channels,
		"features.voice_minutes_24h": r.Features.Voice, "features.video_minutes_24h": r.Features.Video,
		"features.files_uploaded_24h": r.Features.Files, "features.push_sent_24h": r.Features.Push} {
		if b != nil && !slices.Contains(Buckets, *b) {
			return nil, bad("%s", name)
		}
	}
	if !slices.Contains(Registrations, r.Settings.Registration) {
		return nil, bad("settings.registration")
	}
	if len(r.Settings.SSO) > len(SSOKinds) {
		return nil, bad("settings.sso")
	}
	if err := enumSet(r.Settings.SSO, SSOKinds, "settings.sso"); err != nil {
		return nil, err
	}
	if !slices.Contains(Buckets, r.Settings.FederationPeers) {
		return nil, bad("settings.federation_peers")
	}
	u := r.Health.Updates
	for name, n := range map[string]int{"attempts": u.Attempts, "failures": u.Failures, "rollbacks": u.Rollbacks} {
		if n < 0 || n > 100 {
			return nil, bad("health.updates.%s", name)
		}
	}
	if len(u.Errors) > 10 {
		return nil, bad("health.updates.errors")
	}
	for _, e := range u.Errors {
		if !slices.Contains(UpdateErrors, e) {
			return nil, bad("health.updates.errors")
		}
	}
	if len(r.Health.Services) > len(Services) {
		return nil, bad("health.services")
	}
	for k, s := range r.Health.Services {
		if !slices.Contains(Services, k) {
			return nil, bad("health.services.%s", k)
		}
		if s.Restarts < 0 || s.Restarts > 1000 || !slices.Contains(ServiceStates, s.State) {
			return nil, bad("health.services.%s", k)
		}
	}
	if err := checkHostShape(body); err != nil {
		return nil, err
	}
	if h := r.Health.Host; h != nil {
		for name, p := range map[string]*float64{"cpu_p95": h.CPUP95, "cpu_max": h.CPUMax, "mem_p95": h.MemP95,
			"disk_used": h.DiskUsed, "load_per_cpu": h.LoadPerCPU} {
			if p != nil {
				if math.IsNaN(*p) || *p < 0 || *p > 100 {
					return nil, bad("health.host.%s", name)
				}
				v := math.Round(*p*10) / 10
				*p = v
			}
		}
	}
	if d := r.UptimeDays; d != nil && (*d < 0 || *d > 3650) {
		return nil, bad("uptime_days")
	}
	if hs := r.HostSize; hs != nil {
		for _, f := range []struct {
			name string
			v    *string
			enum []string
		}{{"cpu_cores", hs.CPUCores, CPUBuckets}, {"ram_gb", hs.RAMGB, RAMBuckets},
			{"disk_gb", hs.DiskGB, DiskBuckets}, {"arch", hs.Arch, Arches}} {
			if f.v != nil && !slices.Contains(f.enum, *f.v) {
				return nil, bad("host_size.%s", f.name)
			}
		}
	}
	slices.Sort(r.Profiles)
	slices.Sort(r.Settings.SSO)
	slices.Sort(r.Health.Updates.Errors)
	return &r, nil
}

// health.host is null or an object with all five keys, each service
// entry has both of its keys, and host_size (optional) is absent, null or an
// object with all four keys.
func checkHostShape(body []byte) error {
	var raw struct {
		Health struct {
			Host     map[string]any            `json:"host"`
			Services map[string]map[string]any `json:"services"`
		} `json:"health"`
		HostSize map[string]any `json:"host_size"`
	}
	_ = json.Unmarshal(body, &raw)
	if raw.HostSize != nil {
		for _, k := range []string{"cpu_cores", "ram_gb", "disk_gb", "arch"} {
			if _, ok := raw.HostSize[k]; !ok {
				return bad("host_size.%s", k)
			}
		}
	}
	if raw.Health.Host != nil {
		for _, k := range []string{"cpu_p95", "cpu_max", "mem_p95", "disk_used", "load_per_cpu"} {
			if _, ok := raw.Health.Host[k]; !ok {
				return bad("health.host.%s", k)
			}
		}
	}
	for name, s := range raw.Health.Services {
		for _, k := range []string{"restarts", "state"} {
			if v, ok := s[k]; !ok || v == nil {
				return bad("health.services.%s.%s", name, k)
			}
		}
	}
	return nil
}

func enumSet(vals, enum []string, field string) error {
	seen := map[string]bool{}
	for _, v := range vals {
		if !slices.Contains(enum, v) || seen[v] {
			return bad("%s", field)
		}
		seen[v] = true
	}
	return nil
}

// Version checks a version against the pattern and drops a leading v.
// "unknown" passes only when allowed.
func Version(v string, allowUnknown bool) (string, bool) {
	if v == "unknown" {
		return v, allowUnknown
	}
	if len(v) > 64 || !versionRe.MatchString(v) {
		return "", false
	}
	return strings.TrimPrefix(v, "v"), true
}

// ParseApp decodes and validates an app ping.
func ParseApp(body []byte) (*App, error) {
	var a App
	if err := decode(body, &a, appRequired); err != nil {
		return nil, err
	}
	if err := noNulls(body, "schema", "device_id", "app_version", "platform", "channel", "crashes", "servers"); err != nil {
		return nil, err
	}
	if a.Schema != 1 {
		return nil, bad("schema")
	}
	if !idRe.MatchString(a.DeviceID) {
		return nil, bad("device_id")
	}
	var ok bool
	if a.AppVersion, ok = Version(a.AppVersion, false); !ok {
		return nil, bad("app_version")
	}
	if !slices.Contains(Platforms, a.Platform) {
		return nil, bad("platform")
	}
	if !slices.Contains(Channels, a.Channel) {
		return nil, bad("channel")
	}
	if a.Crashes < 0 || a.Crashes > 1000 {
		return nil, bad("crashes")
	}
	if !slices.Contains(Buckets, a.Servers) {
		return nil, bad("servers")
	}
	return &a, nil
}
