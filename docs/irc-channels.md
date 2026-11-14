# Text channels: who can create them, registration, expiry, roles and locks (Ergo 2.19)

Text channels are plain IRC channels on Ergo. The stack adds one thing on top:
**who may create channels** follows the auth service's network settings.
Everything else (roles, private or moderated channels) is standard Ergo,
using the commands below. The boson client can surface these as UI.

## Who can create channels: `channel_creation`

The auth service keeps the network setting (admin panel), served at
`GET http://auth:8081/internal/v1/settings` (`X-Internal-Token:
$INTERNAL_API_TOKEN`) as `{"channel_creation": "registered" | "admins", ...}`.

| `channel_creation` | Ergo config | Effect |
|---|---|---|
| `registered` | `channels.operator-only-creation: false`, `channels.registration.operator-only: false` | Anyone can create a channel by joining it; logged-in users can register it with ChanServ (`/msg ChanServ REGISTER #chan`) and become its founder |
| `admins` | both `true` | Only IRC operators with the `chanreg` capability can create or register channels. Others get `403 #chan :Only server operators can create new channels`. Existing channels are unaffected |

How a change reaches Ergo:

1. At container start `boson-ergo render` fetches the setting (10 s timeout)
   and renders `/ircd/ircd.yaml` with it. It remembers the last value it got
   in `/ircd/boson-settings.json` (the `ergo_db` volume), so if auth is down at
   start Ergo comes up with the last known policy rather than a default.
   With nothing remembered, `IRC_CHANNEL_CREATION` (default `registered`) is
   used.
2. `boson-ergo watch-settings` (started by the entrypoint) polls every
   `SETTINGS_POLL_SECONDS` (default 30). When the rendered `ircd.yaml`
   changes, it rewrites it and sends Ergo `SIGHUP`, Ergo's REHASH, which
   applies the new policy without disconnecting anyone. Failed polls change
   nothing. Logs: `docker compose logs ergo | grep watch-settings`.

So an admin-panel change takes effect on IRC within about
`SETTINGS_POLL_SECONDS`.

## Channel registration: automatic, and expiry of unused channels

Two more network settings (auth 1.7+, admin panel → Network settings →
Channels; `GET /internal/v1/settings`) shape channel registration. They
reach Ergo the same way as `channel_creation` (re-render + rehash, within
`SETTINGS_POLL_SECONDS`) and need the patched Ergo
([patches 0002/0003](../images/ergo/patches), see below).

| Setting (auth) | Default | Ergo config | Effect |
|---|---|---|---|
| `channel_auto_register` | `true` | `channels.registration.auto-register` | A logged-in user who creates a channel by joining it becomes its founder at once, as if they had sent `/msg ChanServ REGISTER #chan`: the usual ChanServ notice and `+q`. Same limits as REGISTER: under `channel_creation: admins` only opers with `chanreg` get one, and at most 15 per account (`max-channels-per-account`; past that, ChanServ says the channel "was not registered automatically"). Guests (not logged in) never do. `SAJOIN`ed channels aren't registered |
| `channel_expiry_days` | `0` (never) | `channels.registration.expire-after: <n>d` | A registered channel with no **JOIN, PRIVMSG or NOTICE** for that many days is unregistered: it becomes an ordinary channel again (topic, modes, bans, persistent roles and ChanServ settings are dropped; anyone in it stays) and the name can be registered by someone else |

What expiry looks like on IRC:

- **Warning**: during the last 7 days the founder gets a ChanServ notice,
  once on the next check if they're online, and again at every login (works
  like a memo, nothing is stored):
  `Channel #chan has had no activity since … and will be unregistered in 6 days (…) unless someone joins it or talks in it`.
- **Expiry**: logged (`docker compose logs ergo | grep expired`), sent to
  opers with the channels snomask (`/MODE nick +s +j`), and the founder, if
  online, gets `Channel #chan was unregistered because it had no activity for 90 days`.
- **Checking**: `/msg ChanServ GET #chan EXPIRY` (founder or oper) shows
  when the channel will expire.
- **Exempting** (opers with `chanreg`, e.g. Owner/Admin roles):
  `/msg ChanServ SET #chan EXPIRY OFF` (and `ON` to undo). Founders can't
  exempt their own channels.

Details: activity is tracked in memory and stored with the channel at most
once an hour (so a restart loses at most an hour of it); the check runs every
hour. Channels registered before the patched Ergo (or with Ergo older than
the patch) start their clock at the first check, so turning expiry on never
drops a channel sooner than `channel_expiry_days` after that. Always-on
clients being rejoined at a restart don't count as activity.

Fallbacks: with an auth older than 1.7 (no such fields) the helper uses
`IRC_CHANNEL_AUTO_REGISTER` (default `true`) and `IRC_CHANNEL_EXPIRY_DAYS`
(default `0`) from the ergo container's environment; the last values auth
sent are remembered in `/ircd/boson-settings.json` like `channel_creation`.
`IRC_CHANNEL_EXPIRE_AFTER` (an Ergo duration: `90d`, `12h`, `30s`)
overrides `channel_expiry_days` altogether; it's meant for testing (the
roles test uses `20s`). None of the three are in `compose.yaml`; add them
under `ergo: environment:` in a compose override if you need them.

## Deleted and disabled users

The helper (`boson-ergo watch-settings`, every `SETTINGS_POLL_SECONDS`,
needs `ERGO_API_TOKEN`) reads auth's feed of removed users
(`GET /internal/v1/irc/accounts/removed?since=<cursor>`, auth 1.7+; cursor
kept in `/ircd/boson-removed.json`, so each removal is handled once, also
after downtime):

| In auth | On IRC |
|---|---|
| user **deleted** | the Ergo account is **erased** (patched Ergo `POST /v1/ns/unregister` with `erase`, i.e. `NS ERASE`): their connections are closed, **every channel they founded is unregistered** (Ergo's own unregister logic), their always-on client, read markers, account settings and vhost go, and the name is free for a new account |
| user **disabled** | nothing: the account and its channels are kept, so re-enabling them restores everything. They can't log in (auth refuses), and their unused channels still expire with `channel_expiry_days` |

Why erase rather than only unregister the channels: the person is gone,
and Ergo's account holds their data (always-on client, settings, read
markers) and reserves the name. A plain `NS UNREGISTER` would keep the name
reserved forever, so a new auth user who later takes the username couldn't
log in to IRC. Auth only reports a deletion while nobody has that username
again, so a re-created user's account is never touched. Disabling is
reversible, so it destroys nothing.

With an older auth (no feed) deleted users keep their Ergo accounts and
channels; their channels then only go away by expiry (or an oper:
`/msg NickServ ERASE <account>`).

## The Ergo patches

[images/ergo/patches](../images/ergo/patches), applied in order on Ergo
v2.19.1 (see [ergo-account-opers.md](ergo-account-opers.md) for 0001):

- **0002 `channels: auto-register new channels, expire inactive registrations`**:
  the two `channels.registration` options above, `LastActivity` in the
  stored channel registration, `CS SET/GET <chan> EXPIRY`, a periodic check
  and the login-time warning (`irc/channelexpiry.go` plus small hooks in
  `channel.go` (JOIN/PRIVMSG/NOTICE), `channelmanager.go` (JOIN of a new
  channel), `chanserv.go` (REGISTER refactored so auto-register shares it),
  `server.go`, `handlers.go`, `config.go`; documented in `default.yaml`,
  `traditional.yaml` and the manual; unit tests in
  `irc/channelexpiry_test.go`). Ergo 2.19 had nothing to reuse: no channel
  expiry, no last-activity time (history timestamps live only in the
  optional history database), and `CS PURGE` bans a channel rather than
  freeing it.
- **0003 `api: add /v1/ns/unregister`**: `NS UNREGISTER`/`NS ERASE` over the
  HTTP API, for deleted users.

Both are written against upstream and kept as commits on the
`channel-lifecycle` branch of a local Ergo checkout (based on v2.19.1);
0002/0003 here are the same commits on top of 0001.

### "Logged-in users only" and guests

Ergo 2.19 has no switch for "only logged-in users may create channels":
under `registered`, a guest (not logged in) can still create an unregistered
channel by joining it. Only ChanServ registration (ownership) needs an
account. To make the network logged-in-only, set `IRC_REQUIRE_SASL=true`
(`accounts.require-sasl.enabled`): every client must authenticate with SASL
or `PASS user:password`.

### Network roles and IRC operators

Network roles (auth 1.5+) make staff IRC operators **through their
account**: anyone holding a role with irc.* permissions (Owner, Admin,
Moderator, or a custom one) is opered up when they log in with SASL or
NickServ IDENTIFY, no `/OPER` needed (patched Ergo, see
[roles.md](roles.md)). Under `channel_creation: admins`, the roles with
`irc.chanreg` (Owner and Admin by default) can create and register channels,
and hand them over:

```
/JOIN #newchannel
/msg ChanServ REGISTER #newchannel
/msg ChanServ TRANSFER #newchannel alice           # prints a confirmation code
/msg ChanServ TRANSFER #newchannel alice <code>    # does it
```

(For an oper with `chanreg` the transfer happens at once; for anyone else
the recipient must accept with `/msg ChanServ TRANSFER accept #newchannel`.)
The emergency `/OPER $OPER_NAME <password>` login (class
`boson.server-admin`, which has `chanreg`) still works.

Ergo 2.19 has no per-account "may create channels" switch, so the
`channel.create` permission is not enforced on IRC: under `registered`
anyone can create a channel by joining it (see above), under `admins` only
opers with `chanreg` can.

Voice reads the roles from the voice token (`perms`: `voice.create`,
`voice.manage_any`, `voice.bypass_locks`, see [voice.md](voice.md)).

## Roles in a channel

Ergo's channel privilege levels, highest first. These are also the roles
voice channels can be locked to (`min_role` / `speak_role`).

| Role | Prefix | Mode | What it can do |
|---|---|---|---|
| founder | `~` | `+q` | Registered owner (`ChanServ REGISTER`); everything, incl. ChanServ settings and any `AMODE` |
| admin | `&` | `+a` | Op powers, can't be kicked/deopped by ops (registered channels) |
| op | `@` | `+o` | Modes, kick, ban, invite, topic, grant op and below |
| halfop | `%` | `+h` | Kick (no ban), topic, grant voice |
| voice | `+` | `+v` | Talk when the channel is `+m` |
| member | | | Join and talk (unless the modes below say otherwise) |

Live modes (`/MODE #chan +o bob`) last until the user leaves. **Persistent
roles** are ChanServ AMODEs, applied every time the account joins (needs a
registered channel; you can grant levels below your own persistent level,
the founder any):

```
/msg ChanServ AMODE #chan +o bob      # make bob a permanent op
/msg ChanServ AMODE #chan +a carol    # channel admin
/msg ChanServ AMODE #chan +h dave     # halfop
/msg ChanServ AMODE #chan +v erin     # permanent voice
/msg ChanServ AMODE #chan -o bob      # remove
/msg ChanServ AMODE #chan             # list (persistent halfop and up, founder, chanreg opers)
/msg ChanServ TRANSFER #chan newowner # change founder (code + accept, see above)
/msg ChanServ INFO #chan              # founder, registration date
```

Account names are used (the stack forces nick = account name for logged-in
users).

## Locks for text channels

No custom enforcement is needed: Ergo's channel modes give the same effects
as voice-channel locks. Set them with `/MODE` (op or higher):

| Want | Commands |
|---|---|
| Only logged-in users can join | `/MODE #chan +R` |
| Only logged-in users can talk (guests can read) | `/MODE #chan +M` (voiced guests excepted) |
| Only voice and above can talk ("speak_role: voice") | `/MODE #chan +m`, then `AMODE`/`MODE +v` the speakers |
| Only specific accounts can join ("min_role" / private) | `/MODE #chan +i`, then `/MODE #chan +I alice` per account, or give them a persistent role (`/msg ChanServ AMODE #chan +v alice`) |
| Let ops see and approve messages from unprivileged users | `/MODE #chan +U` |
| Announcement channel (quiet joins/parts for members) | `/MODE #chan +mu` |
| Hide from `/LIST` and `/WHOIS` | `/MODE #chan +s` |
| Join with a password | `/MODE #chan +k <key>` |
| Mute one user without kicking | `/MODE #chan +b m:bob!*@*` |
| Cap the member count | `/MODE #chan +l 50` |

Remove any of them with `-` instead of `+`. Modes on a registered channel
are kept across restarts.

## Names

Channel names are IRC names: start with `#`, at most 64 bytes
(`limits.channellen`), no spaces, commas or BEL, case-insensitive (ASCII
casemapping, so `#Boson` = `#boson`). Ergo also rejects names that are
confusable with an existing channel (e.g. Unicode look-alikes). A channel's
name can't be changed; the topic (`/TOPIC #chan :text`) is its description.
