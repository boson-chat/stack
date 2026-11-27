// Package testutil builds valid schema-1 bodies and a test country database.
package testutil

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

// Report is a valid install report as a generic map (tests mutate it).
func Report(id string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "schema": 1, "install_id": "`+id+`", "reason": "daily",
  "stack": {"version": "v1.48.0"},
  "services": {"traefik": "3.7.19", "ergo": "1.48.0", "auth": "1.9.2-rc.1"},
  "profiles": ["irc", "auth", "admin", "metrics"],
  "install": {"mode": "fresh", "ircd": "ergo", "arch": "amd64"},
  "counts": {"accounts": "11-50", "active_7d": "2-10", "active_30d": "11-50", "irc_peak_24h": "2-10", "channels": null},
  "features": {"voice_minutes_24h": "51-100", "video_minutes_24h": null, "files_uploaded_24h": null, "push_sent_24h": "0"},
  "settings": {"registration": "open", "guest_access": true, "guest_first": null, "smtp": true, "sso": ["oidc"], "federation_peers": "0", "directory_listed": false},
  "health": {
    "updates": {"attempts": 0, "failures": 0, "rollbacks": 0, "errors": []},
    "services": {"auth": {"restarts": 0, "state": "running"}},
    "host": {"cpu_p95": 34.56, "cpu_max": 98.2, "mem_p95": 71, "disk_used": 62.3, "load_per_cpu": null},
    "healthy": true
  },
  "uptime_days": 41
}`), &m)
	return m
}

// App is a valid app ping.
func App(id string) map[string]any {
	return map[string]any{"schema": 1, "device_id": id, "app_version": "0.106.0", "platform": "macos",
		"channel": "stable", "crashes": 0, "servers": "2-10"}
}

// JSON marshals v.
func JSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// CountryDB writes an mmdb in DB-IP's layout (country.iso_code) mapping
// each CIDR to a code, and returns its path.
func CountryDB(t testing.TB, nets map[string]string) string {
	t.Helper()
	w, err := mmdbwriter.New(mmdbwriter.Options{DatabaseType: "DBIP-Country-Lite", RecordSize: 24, IncludeReservedNetworks: true})
	if err != nil {
		t.Fatal(err)
	}
	for cidr, code := range nets {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Insert(n, mmdbtype.Map{"country": mmdbtype.Map{"iso_code": mmdbtype.String(code)}}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "country.mmdb")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := w.WriteTo(f); err != nil {
		t.Fatal(err)
	}
	return p
}
