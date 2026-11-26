// bridge-sync gives every Boson account its account on the bridge (evrd in
// managed mode, docs/bridge.md): it reads auth's users (read-only, from
// auth's database) and creates, updates, switches off and removes the
// bridge's accounts through its managed API, so a Boson user who signs up
// can connect to the bridge a moment later.
//
//	bridge-sync          run (DATABASE_URL, BRIDGE_API_URL, BRIDGE_TOKEN,
//	                     BRIDGE_SYNC_INTERVAL, default 2s)
//	bridge-sync health   exit 0 if the last sync succeeded recently
//
// It's a stopgap until auth provisions bridge accounts itself (its
// nsaccount.Provisioner hook): auth then also hands the bridge the NickServ
// password so the bridge can identify each user to NickServ.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const healthFile = "/tmp/bridge-sync.ok"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		os.Exit(health())
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("bridge-sync stopped", "err", err)
		os.Exit(1)
	}
}

func health() int {
	b, err := os.ReadFile(healthFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no successful sync yet")
		return 1
	}
	ts, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > 2*time.Minute {
		fmt.Fprintln(os.Stderr, "the last successful sync is too old")
		return 1
	}
	return 0
}

func run(log *slog.Logger) error {
	dsn, api, token := os.Getenv("DATABASE_URL"), os.Getenv("BRIDGE_API_URL"), os.Getenv("BRIDGE_TOKEN")
	if dsn == "" || api == "" || token == "" {
		return fmt.Errorf("set DATABASE_URL, BRIDGE_API_URL and BRIDGE_TOKEN")
	}
	every := 2 * time.Second
	if v := os.Getenv("BRIDGE_SYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 200*time.Millisecond {
			return fmt.Errorf("BRIDGE_SYNC_INTERVAL %q: a duration of at least 200ms", v)
		}
		every = d
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	s := &Syncer{Users: &pgUsers{pool}, Bridge: NewBridge(api, token), Log: log}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if err := s.Once(ctx); err != nil {
			log.Warn("sync failed", "err", err)
		} else {
			_ = os.WriteFile(healthFile, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// pgUsers reads auth's users table (db/migrations/000001_initial in
// boson-chat/auth): id is the access token's sub.
type pgUsers struct{ pool *pgxpool.Pool }

func (p *pgUsers) Fingerprint(ctx context.Context) (string, error) {
	var n int64
	var last time.Time
	err := p.pool.QueryRow(ctx, `SELECT count(*), coalesce(max(updated_at), 'epoch'::timestamptz) FROM users`).Scan(&n, &last)
	return fmt.Sprintf("%d/%d", n, last.UnixMicro()), err
}

func (p *pgUsers) List(ctx context.Context) ([]User, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, username::text, coalesce(email::text, ''), display_name, disabled_at IS NOT NULL FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Disabled); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
