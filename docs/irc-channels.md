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

**Who counts as an admin:** an account whose network roles include the
`irc.chanreg` permission. Any `irc.*` permission makes a role an IRC operator
role, so its members are opered at login with that capability (docs/roles.md).
The built-in **Owner** and **Admin** roles have it; Moderator and Member
don't. In the admin panel the setting reads "Who can create channels: Anyone
signed in / Only admins". The boson client reads `channel_creation` from
auth's `GET /v1/config` and hides "create channel" for people who can't.

With `admins`, people can still join every channel that exists: the
**network channels** below (which always exist), role channels, and channels
an admin created.

## Network channels (fixed channels and auto-join for new users)

`irc.channels` in `config/boson.yaml` (admin panel: Settings → Network
channels) lists channels that belong to the network:

```yaml
irc:
  channels:
    - { name: "#lobby", topic: "Welcome!", autojoin: true }
    - { name: "#help" }
    - { name: "#opers", autojoin: true, roles: [Admin, Owner] }   # only these roles
  channel_founder: thelion   # default: the earliest Owner, else RoleServ
  autojoin_guests: true      # default true
```

| | |
|---|---|
| **Always exist** | RoleServ creates each one (with no members) and registers it to `channel_founder` through the patched Ergo API (`POST /v1/channel_ensure`, patch 0005), before anyone is joined to it. Nobody can create it first and become its founder, with any `channel_creation`. A channel already registered to someone else is left with them (logged). Network channels never expire (`EXPIRY OFF`). |
| **Topic** | Set when RoleServ first sees a non-empty topic, and again whenever it changes in the file. A `/topic` by the channel's ops stays until the file's topic changes. |
| **Auto-join: accounts** | On an account's **first login** (Ergo's ACCOUNT snomask), RoleServ SAJOINs it to the `autojoin: true` channels and records that in auth (`irc_autojoined`), so a `/part` sticks. A channel added to the list later is joined once at each account's next login (and right away for accounts RoleServ saw log in). Existing accounts count as new for a channel they've never been auto-joined to. Independent of `roles.channels.autojoin`. |
| **Auto-join: guests** | With `autojoin_guests`, a client that connects without an account (a Boson guest, or a plain IRC client without SASL: HexChat, irssi) is SAJOINed by nick (`POST /v1/sajoin {"nick"}`) about a second after it connects (Ergo's CONNECT snomask, `+s c`). Once per nick per 24 h (kept in RoleServ's memory), so a reconnecting guest that left isn't pulled back. A client that logs in later is handled as an account (joining again is a no-op). |
| **Who can join** (`roles`, auth 1.21+) | Absent or `[]`: everyone, guests included. Otherwise only accounts holding one of the listed roles (admin panel names, matched case-insensitively to a role's name or slug; `Member` = any signed-in account). Guests never get into a restricted channel (there's no "guests" role). The founder of the network channels and IRC operators with the `sajoin` capability (Owner and Admin by default) always get in, by plain `/join`. A name no role has lets nobody in. See below. |
| **Removing one** | A channel RoleServ registered is unregistered when it's removed from the list (`POST /v1/channel_drop`); nobody is kicked, and it goes away when it empties. A channel that was registered to someone else before is just no longer managed. |

### Channels restricted to roles

RoleServ keeps a restricted channel the way it keeps a private role channel
([roles.md](roles.md#role-channels-private-channels-and-auto-join)): `+i`
with an account invite exception `+I a:<account>` per member of the roles
(or `+R` when `Member` is listed), plus a *guard* on the channel (patched
Ergo, `POST /v1/channel_access` `joinError` / `operJoin` / `kick`, patch
0006):

- **Refused joins say why**: `473 #opers :You need the Admin or Owner role
  to join #opers` (Boson shows "Cannot join #opers: You need …"); with
  `Member`, a guest gets `477 … :Sign in with an account to join #x`. A list
  where no name matches a role: `#x is closed: only network operators can
  join it`.
- **Losing access removes you**: when a role is taken away, a role is
  removed from `roles`, or a channel becomes restricted, RoleServ's next
  push (within `ROLES_POLL_SECONDS`, 10 s) kicks every member who couldn't
  join now, as ChanServ, with `You need the Admin or Owner role to be in
  #opers`, and they can't rejoin. The founder, accounts with a ChanServ
  `AMODE` entry (they always get in, as on any `+i` channel), anyone
  matching an `+I` entry and opers with `sajoin` stay. Someone an oper
  `SAJOIN`s without the role is removed at the next push (every
  `ROLE_CHANNELS_RESYNC_SECONDS` at the latest).
- **Opers with `sajoin`** may `/join` directly (they could always `SAJOIN`).
- **Auto-join** skips restricted channels the account may not join (auth
  filters `network_pending`), and guests only get the open ones. Gaining a
  role joins the account to its restricted auto-join channels once (at login,
  or right away if RoleServ saw it log in); losing it forgets that, so
  getting it back joins it once more.
- **Role changes apply live**: memberships, renames and the file all change
  auth's feed version, which RoleServ polls. Renaming a role means updating
  the `roles` lists that name it (the admin panel shows a name that matches
  no role).
- **Opening it again** (`roles` emptied or removed): RoleServ clears `+i`/
  `+R`, its `+I` entries and the guard. Nobody is pulled back in.
- The directory listing (admin) and `GET /v1/config` don't advertise which
  roles a channel admits; the directory leaves restricted channels out.

The guard (message, oper bypass) lives in Ergo's memory: RoleServ pushes it
again on start and at every resync, so after an Ergo restart refused joins
show Ergo's own `Cannot join channel (+i)` for at most a few seconds.

Why not Ergo's own `channels.auto-join`: it joins every client at every
connect (so a `/part` never sticks, and accounts get it at every login), as
a normal JOIN (a missing channel would be created with the joiner as op).

Status: `docker compose logs roleserv | grep -E "network channels|auto-join"`.
RoleServ logs one summary line when it starts and whenever the list changes
(`network channels: #lobby (auto-join), #help, #opers (auto-join,
Admin/Owner only) exist, founder thelion; guest auto-join on`), a line
whenever it removes people from a restricted channel,, plus a line per channel it registers, drops or sets the
topic of. Channels already registered to the founder log nothing else.

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

## Guest access: `guest_access`

The auth setting `guest_access` (auth 1.8+, admin panel → Network settings
→ "Guest access", default `true`) decides whether people can use IRC
without an account. It reaches Ergo like the other settings (re-render +
rehash, within `SETTINGS_POLL_SECONDS`):

| `guest_access` | Ergo config | On IRC |
|---|---|---|
| `true` | `accounts.require-sasl.enabled: false` | Anyone can connect. Guests can chat and create (unregistered) channels, but hold no roles: no ChanServ registration, no voice, no role modes, vhosts or oper |
| `false` | `accounts.require-sasl.enabled: true` | Every connection must log in with SASL (PLAIN or OAUTHBEARER) or `PASS account:password` before registering. A guest gets `FAIL * ACCOUNT_REQUIRED :You must log in with SASL to join this server` and is disconnected |

Details:

- **Already connected guests** stay until they disconnect; the switch only
  applies to new connections.
- **Older auth** (no `guest_access` in `/internal/v1/settings`):
  `IRC_REQUIRE_SASL` decides (`false`, guests allowed, unless set). With
  auth unreachable, the last value auth sent (kept in
  `/ircd/boson-settings.json`) wins over `IRC_REQUIRE_SASL`, so a restart
  during an auth outage never reopens a closed network.
- **Web clients** (`wss://IRC_HOST/webirc`, through Traefik) and the boson
  app log in with SASL like any other client, so they're unaffected for
  users with an account. Ergo checks the client's real address (Traefik's
  `X-Forwarded-For`), not Traefik's.
- **In-stack services**: RoleServ logs in with SASL to its local Ergo
  account, and quicvoiced only uses Ergo's HTTP API, so neither needs an
  exemption. The admin panel's IRC health check (`ADMIN_IRC_CHECK`) only
  needs the server to answer `CAP LS` (Ergo does that before the login
  check); with guests off it shows "up" without the server version, since it
  has no account to finish registering with.
- **Exemptions**: `localhost` (inside the Ergo container) by default.
  `irc.require_sasl_exempt` in the stack config file (or
  `IRC_REQUIRE_SASL_EXEMPT`, comma-separated IPs/CIDRs; either replaces the
  default) lets e.g. a bouncer or gateway that can't do SASL connect without
  logging in. Don't exempt `STACK_SUBNET`: published ports can reach Ergo
  from the Docker gateway address (e.g. IPv6 clients through docker-proxy),
  which would let guests in.

## Stack config file: `network.name` and `irc:`

With `BOSON_CONFIG` set (the stack sets `/config/boson.yaml`), boson-ergo
also reads the shared stack config file that the admin panel writes. It
reads `network.name` and the `irc:` section. Each key stands for an
environment variable. A key in the file beats the env var, and the env var
beats the default. An absent key, or one with no value, uses the env var,
so removing a key resets it. Unknown keys are ignored. Without the file
(unset, or missing on disk) nothing changes.

```yaml
version: 1
network:
  name: Boson                      # NETWORK_NAME (also the cloak suffix)
irc:
  plaintext: false                 # IRC_PLAINTEXT
  plaintext_port: 6667             # IRC_PLAINTEXT_LISTEN_PORT: Ergo's port in the container
  require_sasl_exempt: [127.0.0.1/32]  # IRC_REQUIRE_SASL_EXEMPT; [] = default (localhost)
  roles_poll_seconds: 10           # ROLES_POLL_SECONDS (watch-settings tick)
  settings_poll_seconds: 30        # SETTINGS_POLL_SECONDS
  websocket: true                  # IRC_WEBSOCKET (the :8097 listener)
  websocket_origins: [https://example.com, "https://*.example.com"]  # IRC_WEBSOCKET_ORIGINS
  oauthbearer: true                # IRC_OAUTHBEARER (SASL OAUTHBEARER)
  history_expire: 1w               # IRC_HISTORY_EXPIRE (Ergo duration: 12h, 30d, 1w)
  log_level: info                  # IRC_LOG_LEVEL: debug | info | warn | error
  persistent_history: true         # IRC_PERSISTENT_HISTORY  (restart required)
```

Channel and guest policy (`channel_creation`, `guest_access`,
`channel_auto_register`, `channel_expiry_days`) are not read here. They are
in the file's `auth:` section, which the auth service reads, and they reach
Ergo through auth's `/internal/v1/settings` as described above, so the file
has only one reader for them.

Not in the file: deployment values and secrets. These are `IRC_HOST`,
`DOMAIN`, `AUTH_*`, `OPER_NAME` / `OPER_PASSWORD_HASH`, `ERGO_API_*`,
`STACK_SUBNET`, `IRC_SERVICES_LISTENER`, `IRC_MOTD_FILE`,
`ERGO_CONFIG_TEMPLATE`, `IRC_ROLES` and `IRC_ROLE_BASE_CAPABILITIES` (the
last two are also read by the separate roleserv container). The auth
fallbacks `IRC_CHANNEL_*` and `IRC_REQUIRE_SASL` also stay in the env.

**Live reload.** `boson-ergo watch-settings` watches the file. It uses
fsnotify on the directory, because the admin panel writes the file by
renaming a temp file over it, plus an mtime poll every 10 s. It debounces
changes, then validates the two sections.

- **Valid:** it re-renders `ircd.yaml` and rehashes Ergo, over the API or
  with SIGHUP. Ergo 2.19's rehash applies listener changes too: it opens
  new listeners and closes removed ones (`setupListeners`). So
  `plaintext`, `plaintext_port` and `websocket` apply without a restart,
  and connected clients stay. The log line is `config reloaded`.
- **Invalid:** it keeps the last good config and logs the error with line
  and column.
- **Restart required:** `irc.persistent_history`. Ergo opens the history
  database only at startup. Until a restart, `ircd.yaml` keeps the value Ergo
  started with, saved by `boson-ergo render` in
  `/ircd/boson-config-running.json`. The key is reported in
  `restart_required`. Network name changes are rehashed (`NETWORK=`, and the
  cloak suffix for new connections).

The compose file publishes the container's port 6667 (`IRC_PLAINTEXT_PORT`
is the host side of that mapping). A `plaintext_port` other than 6667 makes
Ergo listen there, but that port is published only if the compose port
mapping points at it.

Mount the config **directory** read-only (e.g. `./config:/config:ro`), not
the single file. A single-file bind mount keeps the old inode after an
atomic replace and never sees the change.

**Status.** `GET http://ergo:8098/internal/config/status` is served on the
compose network only. Don't publish it. `CONFIG_STATUS_LISTENER` sets the
address, and `off` disables it. Ergo has no HTTP API of its own for this,
so boson-ergo reports for it. Example response:

```json
{"path":"/config/boson.yaml","loaded_at":"2026-11-14T10:00:00Z","file_mtime":"2026-11-14T09:59:59Z",
 "ok":false,"error":"line 4, column 25: irc.require_sasl_exempt: \"nope\" is not an IP, CIDR or localhost",
 "error_line":4,"error_column":25,"restart_required":["irc.persistent_history"]}
```

- `loaded_at`: when the file in use was rendered and rehashed.
- `ok: false`: the file is invalid, or the rehash failed. The error is
  given, and the last good config stays.
- `file_mtime`: `null` when the file is missing.
- `restart_required`: keys to apply with `docker compose restart ergo`.

The endpoint answers even without `BOSON_CONFIG`, with `path: ""` and
`ok: true`. It exists only while `watch-settings` runs, so not with
`IRC_WATCH_SETTINGS=false`.

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
- **0004** and **0005**: account extbans, `channel_access`/`sajoin` and the
  network channel API ([roles.md](roles.md), above).
- **0006 `api: channel guard`**: `POST /v1/channel_access` takes
  `joinError` (the `473`/`477` text for clients `+i`/`+R` keeps out),
  `operJoin` (opers with `sajoin` may `JOIN`) and `kick`/`kickReason`
  (remove, as ChanServ, members who couldn't join now), for
  [channels restricted to roles](#channels-restricted-to-roles)
  (`irc/apichannelguard.go`, hooks in `channel.go` `Join` and
  `handlers.go` `sendJoinError`; tests in `irc/apichannelguard_test.go`).

Both are written against upstream and kept as commits on the
`channel-lifecycle` branch of a local Ergo checkout (based on v2.19.1);
0002/0003 here are the same commits on top of 0001.

### "Logged-in users only" and guests

Ergo 2.19 has no switch for "only logged-in users may create channels":
under `registered`, a guest (not logged in) can still create an unregistered
channel by joining it. Only ChanServ registration (ownership) needs an
account. To make the network logged-in-only, turn
[guest access](#guest-access-guest_access) off: every client must
authenticate with SASL or `PASS user:password`.

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
