package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/boson-chat/stack/images/ergo/helper/internal/ergoapi"
	"github.com/boson-chat/stack/images/ergo/helper/internal/rolechannels"
	"github.com/boson-chat/stack/images/ergo/helper/internal/roleserv"
)

// runRoleServ runs RoleServ (its own compose service, see compose.yaml):
// a client of Ergo's services listener (ROLESERV_IRC_ADDR) that logs in as
// a local Ergo account (password reset through the API at every connect),
// and talks to auth's internal API on behalf of the users who message it.
// It also enforces role channels (internal/rolechannels): private channels
// and auto-join.
func runRoleServ(ctx context.Context, get func(string) string, logf func(string, ...any)) error {
	tok := get("ERGO_API_TOKEN")
	if tok == "" || get("INTERNAL_API_TOKEN") == "" {
		// Not an error worth a restart loop: idle until stopped.
		logf("ERGO_API_TOKEN and INTERNAL_API_TOKEN are needed (scripts/setup.sh makes them); RoleServ is off")
		<-ctx.Done()
		return nil
	}
	base, err := baseCapabilities(get)
	if err != nil {
		return err
	}
	ergo := ergoapi.New(strEnv(get, "ERGO_API_URL", "http://ergo:8089"), tok)
	svc := &roleserv.Service{
		Auth:        newRolesClient(get),
		Ergo:        ergo,
		BaseCaps:    base,
		RetryDelays: []time.Duration{secondsEnv("ROLES_POLL_SECONDS", 10) + 5*time.Second, 45 * time.Second},
		Logf:        logf,
	}
	nick := strEnv(get, "ROLESERV_NICK", "RoleServ")
	channels := &rolechannels.Syncer{
		Auth: newRolesClient(get), Ergo: ergo, SetBy: nick,
		StateFile: filepath.Join(strEnv(get, "ROLESERV_STATE_DIR", "/roleserv"), "role-channels.json"),
		Poll:      secondsEnv("ROLES_POLL_SECONDS", 10),
		Resync:    secondsEnv("ROLE_CHANNELS_RESYNC_SECONDS", 300),
		Logf:      logf,
		// a guest is joined to the network channels a moment after it
		// connects (its registration burst comes first)
		GuestDelay: time.Second,
	}
	go channels.Run(ctx)
	bot := &roleserv.Bot{
		Addr:      strEnv(get, "ROLESERV_IRC_ADDR", "ergo:6680"),
		Nick:      nick,
		Class:     serviceClass,
		Session:   ergo,
		Service:   svc,
		Secret:    randomSecret,
		Logf:      logf,
		OnLogin:   channels.Login,
		OnConnect: channels.Connect,
	}
	bot.Run(ctx)
	return nil
}
