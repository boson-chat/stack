package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
)

// Install is one install's state.
type Install struct {
	ID         string // the hash
	FirstSeen  time.Time
	LastSeen   time.Time
	Country    string
	Stack      string
	Report     *schema.Report
	reportJSON string
}

// DayReport is one install's report of one day.
type DayReport struct {
	ID       string
	Day      string
	Received time.Time
	Country  string
	Report   *schema.Report
}

// Device is one app install's state.
type Device struct {
	ID        string
	FirstSeen time.Time
	LastSeen  time.Time
	Country   string
	Platform  string
	Version   string
	Channel   string
}

// AppDay is one device's ping of one day.
type AppDay struct {
	ID       string
	Day      string
	Country  string
	Platform string
	Version  string
	Channel  string
	Crashes  int
	Servers  string
}

func parse(s string) *schema.Report {
	var r schema.Report
	if json.Unmarshal([]byte(s), &r) != nil {
		return nil
	}
	return &r
}

func (s *Store) installs(ctx context.Context, where string, args ...any) ([]Install, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, first_seen, last_seen, country, stack_version, report FROM installs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Install
	for rows.Next() {
		var i Install
		var f, l int64
		if err := rows.Scan(&i.ID, &f, &l, &i.Country, &i.Stack, &i.reportJSON); err != nil {
			return nil, err
		}
		i.FirstSeen, i.LastSeen = time.Unix(f, 0).UTC(), time.Unix(l, 0).UTC()
		i.Report = parse(i.reportJSON)
		out = append(out, i)
	}
	return out, rows.Err()
}

// InstallsSeenSince are the installs that reported at or after t.
func (s *Store) InstallsSeenSince(ctx context.Context, t time.Time) ([]Install, error) {
	return s.installs(ctx, `WHERE last_seen >= ? ORDER BY last_seen DESC`, t.Unix())
}

// AllInstalls is every install (first/last seen for the weekly counts).
func (s *Store) AllInstalls(ctx context.Context) ([]Install, error) {
	return s.installs(ctx, `ORDER BY first_seen`)
}

// InstallByPrefix finds an install by the start of its hash (≥ 8 chars).
func (s *Store) InstallByPrefix(ctx context.Context, prefix string) (*Install, error) {
	if len(prefix) < 8 {
		return nil, sql.ErrNoRows
	}
	list, err := s.installs(ctx, `WHERE id >= ? AND id < ? ORDER BY id LIMIT 2`, prefix, prefix+"g")
	if err != nil {
		return nil, err
	}
	if len(list) != 1 {
		return nil, sql.ErrNoRows
	}
	return &list[0], nil
}

func (s *Store) dayReports(ctx context.Context, where string, args ...any) ([]DayReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, day, received, country, report FROM reports `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayReport
	for rows.Next() {
		var d DayReport
		var rec int64
		var js string
		if err := rows.Scan(&d.ID, &d.Day, &rec, &d.Country, &js); err != nil {
			return nil, err
		}
		d.Received = time.Unix(rec, 0).UTC()
		if d.Report = parse(js); d.Report != nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// ReportsOn are the day rows of one UTC day.
func (s *Store) ReportsOn(ctx context.Context, day string) ([]DayReport, error) {
	return s.dayReports(ctx, `WHERE day = ?`, day)
}

// ReportsSince are the day rows from day on, oldest first per install.
func (s *Store) ReportsSince(ctx context.Context, day string) ([]DayReport, error) {
	return s.dayReports(ctx, `WHERE day >= ? ORDER BY id, day`, day)
}

// History is one install's day rows, newest first.
func (s *Store) History(ctx context.Context, id string) ([]DayReport, error) {
	return s.dayReports(ctx, `WHERE id = ? ORDER BY day DESC`, id)
}

// NewOn counts installs first seen on day.
func (s *Store) NewOn(ctx context.Context, day string) (int, error) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM installs WHERE first_seen >= ? AND first_seen < ?`,
		t.Unix(), t.Add(24*time.Hour).Unix()).Scan(&n)
	return n, err
}

// DevicesSeenSince are the devices that pinged at or after t.
func (s *Store) DevicesSeenSince(ctx context.Context, t time.Time) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, first_seen, last_seen, country, platform, version, channel FROM devices WHERE last_seen >= ?`, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var f, l int64
		if err := rows.Scan(&d.ID, &f, &l, &d.Country, &d.Platform, &d.Version, &d.Channel); err != nil {
			return nil, err
		}
		d.FirstSeen, d.LastSeen = time.Unix(f, 0).UTC(), time.Unix(l, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) appDays(ctx context.Context, where string, args ...any) ([]AppDay, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, day, country, platform, version, channel, crashes, servers FROM app_days `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppDay
	for rows.Next() {
		var d AppDay
		if err := rows.Scan(&d.ID, &d.Day, &d.Country, &d.Platform, &d.Version, &d.Channel, &d.Crashes, &d.Servers); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AppsOn are the ping rows of one UTC day.
func (s *Store) AppsOn(ctx context.Context, day string) ([]AppDay, error) {
	return s.appDays(ctx, `WHERE day = ?`, day)
}

// AppsSince are the ping rows from day on.
func (s *Store) AppsSince(ctx context.Context, day string) ([]AppDay, error) {
	return s.appDays(ctx, `WHERE day >= ?`, day)
}
