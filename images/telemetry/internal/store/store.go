// Package store keeps telemetryd's per-install and per-device state in
// SQLite: first/last seen, the latest report, one validated report per
// install per UTC day (90 days), the app pings per device per day, the
// panel's sessions and which days are rolled up into VictoriaMetrics.
// IDs are stored only as SHA-256 hashes (hex).
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/boson-chat/stack/images/telemetry/internal/schema"
	_ "modernc.org/sqlite"
)

// Windows.
const (
	CountWindow   = 20 * time.Hour
	DayRetention  = 90 * 24 * time.Hour
	SeenRetention = 2 * 365 * 24 * time.Hour
)

// Store is the database.
type Store struct {
	db *sql.DB
}

// HashID is how an install or device ID is stored.
func HashID(id string) string {
	s := sha256.Sum256([]byte(id))
	return hex.EncodeToString(s[:])
}

// Day is t's UTC day, YYYY-MM-DD.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

const ddl = `
CREATE TABLE IF NOT EXISTS installs (
  id TEXT PRIMARY KEY,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  last_counted INTEGER NOT NULL,
  country TEXT NOT NULL,
  stack_version TEXT NOT NULL,
  report TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS installs_last_seen ON installs(last_seen);
CREATE TABLE IF NOT EXISTS reports (
  id TEXT NOT NULL,
  day TEXT NOT NULL,
  received INTEGER NOT NULL,
  country TEXT NOT NULL,
  report TEXT NOT NULL,
  PRIMARY KEY (id, day)
);
CREATE INDEX IF NOT EXISTS reports_day ON reports(day);
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  last_counted INTEGER NOT NULL,
  country TEXT NOT NULL,
  platform TEXT NOT NULL,
  version TEXT NOT NULL,
  channel TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS devices_last_seen ON devices(last_seen);
CREATE TABLE IF NOT EXISTS app_days (
  id TEXT NOT NULL,
  day TEXT NOT NULL,
  received INTEGER NOT NULL,
  country TEXT NOT NULL,
  platform TEXT NOT NULL,
  version TEXT NOT NULL,
  channel TEXT NOT NULL,
  crashes INTEGER NOT NULL,
  servers TEXT NOT NULL,
  PRIMARY KEY (id, day)
);
CREATE INDEX IF NOT EXISTS app_days_day ON app_days(day);
CREATE TABLE IF NOT EXISTS rollups (
  day TEXT PRIMARY KEY,
  done_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  expires INTEGER NOT NULL
);
`

// Open opens (and creates) the database at path; ":memory:" for tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Outcome of an accepted report or ping.
type Outcome struct {
	Counted bool
}

// tx runs f in a transaction.
func (s *Store) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// PutReport stores an accepted report: counted (a new day row) when it's
// the first in CountWindow, else it replaces the install's latest day row.
func (s *Store) PutReport(ctx context.Context, r *schema.Report, country string, now time.Time) (Outcome, error) {
	id := HashID(r.InstallID)
	// Only the hash is kept: the stored report has no install_id.
	cp := *r
	cp.InstallID = ""
	body, err := json.Marshal(&cp)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	err = s.tx(ctx, func(tx *sql.Tx) error {
		var lastCounted int64
		err := tx.QueryRowContext(ctx, `SELECT last_counted FROM installs WHERE id = ?`, id).Scan(&lastCounted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out.Counted = true
			_, err = tx.ExecContext(ctx, `INSERT INTO installs (id, first_seen, last_seen, last_counted, country, stack_version, report)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, id, now.Unix(), now.Unix(), now.Unix(), country, r.Stack.Version, string(body))
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			out.Counted = now.Sub(time.Unix(lastCounted, 0)) >= CountWindow
			lc := lastCounted
			if out.Counted {
				lc = now.Unix()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE installs SET last_seen = ?, last_counted = ?, country = ?, stack_version = ?, report = ? WHERE id = ?`,
				now.Unix(), lc, country, r.Stack.Version, string(body), id); err != nil {
				return err
			}
		}
		if out.Counted {
			_, err = tx.ExecContext(ctx, `INSERT INTO reports (id, day, received, country, report) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (id, day) DO UPDATE SET received = excluded.received, country = excluded.country, report = excluded.report`,
				id, Day(now), now.Unix(), country, string(body))
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE reports SET received = ?, country = ?, report = ?
			WHERE id = ? AND day = (SELECT max(day) FROM reports WHERE id = ?)`, now.Unix(), country, string(body), id, id)
		return err
	})
	return out, err
}

// PutApp stores an accepted app ping, counted the same way.
func (s *Store) PutApp(ctx context.Context, a *schema.App, country string, now time.Time) (Outcome, error) {
	id := HashID(a.DeviceID)
	var out Outcome
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var lastCounted int64
		err := tx.QueryRowContext(ctx, `SELECT last_counted FROM devices WHERE id = ?`, id).Scan(&lastCounted)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out.Counted = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, first_seen, last_seen, last_counted, country, platform, version, channel)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, now.Unix(), now.Unix(), now.Unix(), country, a.Platform, a.AppVersion, a.Channel); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			out.Counted = now.Sub(time.Unix(lastCounted, 0)) >= CountWindow
			lc := lastCounted
			if out.Counted {
				lc = now.Unix()
			}
			if _, err := tx.ExecContext(ctx, `UPDATE devices SET last_seen = ?, last_counted = ?, country = ?, platform = ?, version = ?, channel = ? WHERE id = ?`,
				now.Unix(), lc, country, a.Platform, a.AppVersion, a.Channel, id); err != nil {
				return err
			}
		}
		if out.Counted {
			_, err = tx.ExecContext(ctx, `INSERT INTO app_days (id, day, received, country, platform, version, channel, crashes, servers)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (id, day) DO UPDATE SET received = excluded.received, country = excluded.country, platform = excluded.platform,
				version = excluded.version, channel = excluded.channel, crashes = app_days.crashes + excluded.crashes, servers = excluded.servers`,
				id, Day(now), now.Unix(), country, a.Platform, a.AppVersion, a.Channel, a.Crashes, a.Servers)
			return err
		}
		// Not counted: the crashes still add up on the latest day row.
		_, err = tx.ExecContext(ctx, `UPDATE app_days SET received = ?, version = ?, crashes = min(crashes + ?, 100000), servers = ?
			WHERE id = ? AND day = (SELECT max(day) FROM app_days WHERE id = ?)`, now.Unix(), a.AppVersion, a.Crashes, a.Servers, id, id)
		return err
	})
	return out, err
}

// Prune deletes day rows past DayRetention, installs and devices unseen for
// SeenRetention, and expired sessions.
func (s *Store) Prune(ctx context.Context, now time.Time) error {
	cut := Day(now.Add(-DayRetention))
	seen := now.Add(-SeenRetention).Unix()
	for _, q := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM reports WHERE day < ?`, cut},
		{`DELETE FROM app_days WHERE day < ?`, cut},
		{`DELETE FROM installs WHERE last_seen < ?`, seen},
		{`DELETE FROM devices WHERE last_seen < ?`, seen},
		{`DELETE FROM sessions WHERE expires < ?`, now.Unix()},
		{`DELETE FROM rollups WHERE day < ?`, cut},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.arg); err != nil {
			return err
		}
	}
	return nil
}

// ---- sessions ----

func tokenHash(t string) string { return HashID(t) }

// NewSession stores a session token until expires.
func (s *Store) NewSession(ctx context.Context, token string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token, expires) VALUES (?, ?)`, tokenHash(token), expires.Unix())
	return err
}

// ValidSession reports whether token is a live session.
func (s *Store) ValidSession(ctx context.Context, token string, now time.Time) bool {
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT expires FROM sessions WHERE token = ?`, tokenHash(token)).Scan(&exp)
	return err == nil && exp > now.Unix()
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, tokenHash(token))
	return err
}

// ---- rollups ----

// RolledUp reports whether day is already written to VictoriaMetrics.
func (s *Store) RolledUp(ctx context.Context, day string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM rollups WHERE day = ?`, day).Scan(&n)
	return n > 0, err
}

// MarkRolledUp records that day is written.
func (s *Store) MarkRolledUp(ctx context.Context, day string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO rollups (day, done_at) VALUES (?, ?)`, day, now.Unix())
	return err
}

// FirstDay is the oldest day with a report or ping ("" if none).
func (s *Store) FirstDay(ctx context.Context) (string, error) {
	var a, b sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT min(day) FROM reports`).Scan(&a); err != nil {
		return "", err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT min(day) FROM app_days`).Scan(&b); err != nil {
		return "", err
	}
	switch {
	case a.Valid && b.Valid:
		return min(a.String, b.String), nil
	case a.Valid:
		return a.String, nil
	case b.Valid:
		return b.String, nil
	}
	return "", nil
}
