// devserver: telemetryd's panel and ingest on made-up data, for UI work,
// the Playwright e2e and screenshots. Sign in as owner / correct-horse-battery.
//
//	go run ./cmd/devserver [-panel :8081] [-ingest :8080] [-vm http://127.0.0.1:8428] [-installs 60]
package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/boson-chat/stack/images/telemetry/internal/devseed"
	"github.com/boson-chat/stack/images/telemetry/internal/geoip"
	"github.com/boson-chat/stack/images/telemetry/internal/ingest"
	"github.com/boson-chat/stack/images/telemetry/internal/panel"
	"github.com/boson-chat/stack/images/telemetry/internal/rollup"
	"github.com/boson-chat/stack/images/telemetry/internal/store"
	"github.com/boson-chat/stack/images/telemetry/internal/vm"
	"github.com/boson-chat/stack/images/telemetry/web"
)

type both interface {
	rollup.Writer
	panel.Querier
}

func main() {
	panelAddr := flag.String("panel", "127.0.0.1:8081", "panel listen address")
	ingestAddr := flag.String("ingest", "127.0.0.1:8080", "ingest listen address")
	vmURL := flag.String("vm", "", "a real VictoriaMetrics (empty: in-memory fake)")
	installs := flag.Int("installs", 60, "made-up installs")
	empty := flag.Bool("empty", false, "no made-up data")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.Open(":memory:")
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	if !*empty {
		if err := devseed.Seed(ctx, st, now, *installs, 60, 42); err != nil {
			log.Fatal(err)
		}
	}
	var v both = &vm.Fake{}
	if *vmURL != "" {
		v = &vm.Client{URL: *vmURL, HTTP: http.DefaultClient}
	}
	if err := (&rollup.Rollup{Store: st, VM: v, Now: time.Now, Log: logger}).Catchup(ctx); err != nil {
		log.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-horse-battery"), bcrypt.MinCost)
	static, _ := fs.Sub(web.Dist, "dist")
	p := &panel.Panel{Store: st, VM: v, Username: "owner", PasswordHash: hash, Static: static, Now: time.Now, Log: logger}
	geo, _ := geoip.Open("")
	in := &ingest.Handler{Store: st, Geo: geo, Now: time.Now, Log: logger}
	go func() { log.Fatal(http.ListenAndServe(*ingestAddr, in)) }()
	logger.Info("dev panel", "url", "http://"+*panelAddr, "user", "owner", "password", "correct-horse-battery")
	log.Fatal(http.ListenAndServe(*panelAddr, p.Handler()))
}
