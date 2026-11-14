# Network roles on IRC

The auth service's network roles (Owner, Admin, Moderator, Member and any
custom ones, edited in the admin panel; auth 1.5+, contract in
[boson-chat/auth docs/roles.md](https://github.com/boson-chat/auth/blob/main/docs/roles.md))
are **IRC-native**: they show up in any IRC client, mIRC included, with
nothing boson-specific on the client side.

| A role's setting (admin panel) | On IRC |
|---|---|
| any `irc.*` permission, or "IRC operator" on | members are **IRC operators** as soon as they log in (no `/OPER`): `381 You are now an IRC operator`, user mode `+o` |
| `irc.kill`, `irc.ban`, `irc.samode`, … | exactly those Ergo oper powers (`/KILL`, `UBAN`/`KLINE`, `/SAMODE`, …) |
| WHOIS line | `313` line in everyone's `/WHOIS` (oper roles) |
| vhost template (`staff/{username}`) | the member's **hostname** (`alice!~u@staff/alice`), for every role, oper or not |
| colour, hoist, mentionable | not shown on IRC (no standard for it); `/msg RoleServ INFO` lists them |
| `voice.*` | voice channels (quicvoiced), see [voice.md](voice.md) |

`/msg RoleServ HELP` lists and explains roles from IRC.

## What it looks like in mIRC

Alice holds Admin (WHOIS line "is a Network Administrator", vhost
`staff/{username}`); Dave holds DJ (no oper powers, vhost `dj/{username}`):

```
* Connecting to irc.boson.chat (6697)            (SASL: alice / password)
-irc.boson.chat- *** You are now logged in as alice
...
* You are now an IRC operator
* alice sets mode: +o

/whois alice
alice is ~u@staff/alice * alice
alice on @#boson
alice is a Network Administrator
alice is logged in as alice
alice using irc.boson.chat Boson
alice End of /WHOIS list.

/whois dave
dave is ~u@dj/dave * dave
dave is logged in as dave
```

The vhost replaces the cloak (`abcd1234.boson`) in the hostmask everywhere:
joins, `/WHO`, bans (`/mode #chan +b *!*@staff/*` matches the whole staff).
The member of several roles gets the oper class and WHOIS line of their
**most senior** role that is an IRC operator role, and the vhost of their
most senior role that has one (auth decides both).

Operators see their powers with `/HELPOP`, use them as usual (`/KILL`,
`/SAMODE #chan +o bob`, `/SAJOIN`, `/msg HostServ SET`, `UBAN ADD`, …) and
can drop them for the session with `/MODE <nick> -o`. Logging in again
brings them back.

The emergency login `/OPER $OPER_NAME <password>` (`.env`, class
`boson.server-admin`, every capability) still works, for when auth is down.

## RoleServ

A services bot in the stack (`roleserv` service). Every IRC client can talk
to it; it identifies you by the **account you're logged into** (the
`account` tag Ergo puts on your message), never by nick, and does nothing
for users who aren't logged in.

| Command | Who | |
|---|---|---|
| `HELP [command]` | everyone | the commands you can use |
| `LIST` | everyone | all roles, most senior first, with colour, position, oper / vhost / hoisted |
| `INFO <role>` | everyone | id, built-in, colour, hoist, mentionable, oper, WHOIS line, vhost template, IRC powers, permissions |
| `WHO <role>` | everyone | the role's members (first 200) |
| `ROLES <nick\|account>` | everyone | someone's roles (a connected nick is resolved to its account) |
| `CREATE <name>` | `roles.manage` | new role (position 1, no permissions) |
| `DELETE <role>` | `roles.manage` | delete a custom role; members lose it at once |
| `SET <role> COLOR <#rrggbb\|none>` | `roles.manage` | |
| `SET <role> WHOIS <line\|none>` | `roles.manage` | WHOIS line (`none`: the default, "is a <name>") |
| `SET <role> VHOST <template\|none>` | `roles.manage` | e.g. `staff/{username}`, `{username}.mods.boson.chat` |
| `SET <role> POSITION <n>` | `roles.manage` | higher = more senior |
| `SET <role> HOIST <on\|off>` | `roles.manage` | |
| `SET <role> OPER <on\|off>` | `roles.manage` | make it an IRC operator role (on is implied by any `irc.*` permission) |
| `PERM <role> +perm -perm …` | `roles.manage` | e.g. `PERM Helpers +irc.samode +irc.sajoin -voice.create` |
| `ASSIGN <account> <role>` | `roles.manage` | |
| `REMOVE <account> <role>` | `roles.manage` | |

Role names with spaces can be quoted (`INFO "Chat Helpers"`); `SET` and
`PERM` also find the role name before the keyword (`SET Chat Helpers WHOIS
is here to help`). Commands are rate limited per account (8, then one every
2 s).

Changes are made **by the auth service, as your account**: auth checks your
`roles.manage` permission and the role hierarchy (only roles below your
highest role, only permissions you hold yourself, only people below you;
Owners bypass it) exactly as in the admin panel, and the audit log records
you as the actor. RoleServ never decides who may do what. Its replies are
auth's answer ("Permission denied: you can only manage roles (and people)
below your highest role.").

**Today (auth 1.5.0)** RoleServ is read-only except for `WHO`: auth has no
internal members list and no acting API yet, so `WHO` says so (and gives the
member count) and the write commands answer "Changing roles from IRC isn't
available on this network yet … use the admin panel", and `HELP` doesn't
list them. They switch on by themselves once auth has the endpoints below.

RoleServ runs as its own container from the ergo image (`boson-ergo
roleserv`): it connects to Ergo's services listener (`ergo:6680`,
plaintext, compose network only, never published) and logs in with SASL as
`RoleServ`, a local Ergo account (not an auth account) whose password it
resets through Ergo's API at every connect. The auth-script refuses to log
anyone into `RoleServ` through auth, and the nick is reserved by the account.
It grants itself the `boson.service` oper class (no fakelag, WHOIS "is a
network service") and sets `+B` (bot).

## How changes reach IRC

| Change | Connected users | Latency |
|---|---|---|
| role edited: WHOIS line, irc.* permissions, IRC operator on/off, role deleted | class re-rendered + Ergo rehash: members' powers and WHOIS line change, members de-opered if the role stops being an oper role | ≤ `ROLES_POLL_SECONDS` (10 s) |
| someone gains or loses a role, vhost template edited, position change | **through RoleServ**: pushed at once (`POST /v1/account_grants`) | immediate |
| same, **in the admin panel** | with auth's grants feed (below): pushed | ≤ `ROLES_POLL_SECONDS` |
| same, admin panel, **auth 1.5.0** (no grants feed yet) | next login | next login |
| account disabled | next login refused (SASL PLAIN by auth, OAUTHBEARER by the auth-script); connected sessions stay until auth's feed or a reconnect | next login |
| account deleted (auth 1.7+) | Ergo account erased: sessions closed, founded channels unregistered ([irc-channels.md](irc-channels.md#deleted-and-disabled-users)) | ≤ `SETTINGS_POLL_SECONDS` |

At every login (SASL PLAIN, `PASS`, NickServ IDENTIFY: auth's
verify-password; SASL OAUTHBEARER: `GET /internal/v1/users/{username}/irc`)
the user gets their current oper class, WHOIS line and vhost, so whatever
the path, a reconnect is always up to date.

## How it works

```
auth ──/internal/v1/roles (ETag)──▶ boson-ergo watch-settings ──ircd.yaml oper-classes + /v1/rehash──▶ Ergo
     ──/internal/v1/irc/grants────▶          (ergo container)  ──/v1/account_grants───────────────▶
     ◀─verify-password / users/{u}/irc── boson-ergo auth-script ──operClass, whoisLine, vhost──────▶ (at login)
     ◀─/internal/v1/acting/* (as the caller)── RoleServ ──/v1/account_grants──────────────────────▶ (live)
```

1. **Oper classes.** `boson-ergo render` (start) and `watch-settings`
   (every `ROLES_POLL_SECONDS`, `If-None-Match`, so a poll is a `304`)
   render one `oper-classes` entry per role with `irc.oper`: name = auth's
   `oper_class` (the role slug, `[a-z0-9_-]`), title = role name,
   `whoisline` = the role's effective WHOIS line, capabilities = auth's
   `capabilities` plus `IRC_ROLE_BASE_CAPABILITIES` (default `nofakelag`:
   staff can act and paste without being throttled; `none` to drop it). The
   stack's own classes (`boson.server-admin`, `boson.service`) contain a
   `.`, which a slug can't, so names never collide; anything that isn't a
   safe class name is skipped. When `ircd.yaml` changes it rehashes Ergo
   (`POST /v1/rehash`, synchronous; SIGHUP without `ERGO_API_TOKEN`). The
   last list is kept in `/ircd/boson-roles.json` (`ergo_db` volume), so a
   restart while auth is down keeps the classes.
2. **At login** the auth-script returns, with the account, the user's
   `operClass` (auth's `oper_class` resolved against the rendered classes;
   unknown → none), `whoisLine` (only when it differs from the class's own
   line, so the class's line, which follows edits, is used otherwise) and
   `vhost` (`""` when the user has none, so a lost role's vhost goes away;
   invalid ones are dropped). The patched Ergo opers the client up into that
   class when registration completes and stores the vhost as the account's
   vhost. Disabled accounts are refused (OAUTHBEARER) or never verified
   (PLAIN). With an auth that has no roles (1.4), the auth-script returns no
   grant fields at all and nothing changes.
3. **Live pushes.** `watch-settings` polls auth's grants feed (if auth has
   it) and pushes each changed account to Ergo's `POST /v1/account_grants`
   (`/ircd/boson-grants.json` remembers what was pushed, so removals are
   pushed too, also across restarts). RoleServ pushes the accounts it
   changed right away, and retries in the background if it named a class
   Ergo hasn't loaded yet (a role that just became an oper role).
4. **Patched Ergo** ([images/ergo/patches](../images/ergo/patches),
   [ergo-account-opers.md](ergo-account-opers.md)): auth-script grants,
   the `/v1/account_grants` API endpoint, and account-bound opers following
   their class on rehash. Opers who used `/OPER` are never touched.

Settings (`.env`, all optional):

| Variable | Default | |
|---|---|---|
| `IRC_ROLES` | `true` | `false`: no role classes, no grants (today's behaviour) |
| `ROLES_POLL_SECONDS` | `10` | roles / grants poll interval |
| `IRC_ROLE_BASE_CAPABILITIES` | `nofakelag` | added to every role class; comma-separated Ergo capabilities, or `none` |
| `ERGO_API_TOKEN` | (setup.sh) | needed for live pushes and RoleServ |
| `ROLESERV_AUTH_TOKEN` | (setup.sh) | RoleServ's token for auth's acting API; auth gets it as `INTERNAL_ACTING_TOKEN` |

## Requested from auth

Three additions to auth's internal listener (`:8081`), none of which exist in
auth 1.5.0. The stack already uses them when they answer, and falls back as
described above while they 404. [test/roles/fakeauth.py](../test/roles/fakeauth.py)
implements all three (and auth 1.5's endpoints) as an executable reference.

### 1. Acting API (RoleServ writes)

The **admin API, mirrored** under `/internal/v1/acting`, with the acting
user named instead of authenticated by an access token:

- Headers: `X-Internal-Token: <INTERNAL_ACTING_TOKEN>` (a separate secret,
  only RoleServ has it; the general `INTERNAL_API_TOKEN` must **not** be
  accepted here, otherwise every internal client could act as anyone),
  `X-Acting-Account: <username>` (the Ergo account = auth username, RFC 1459
  folding), `X-Acting-Via: roleserv` (for the audit log). If
  `INTERNAL_ACTING_TOKEN` is unset the whole prefix is `404`.
- The acting user is loaded like a session user: unknown or disabled →
  `403 forbidden`. Permissions are read from the database, exactly like the
  admin API; the hierarchy rules, error codes (`403 forbidden |
  role_hierarchy | permission_not_held`, `404 not_found`, `409
  role_name_taken | builtin_role | last_owner`, `400 invalid_request`) and
  audit events are the admin API's, with the acting user as actor and
  `meta.via: "roleserv"`.
- Endpoints (bodies and responses as in the admin API):

| Method & path | Body → response |
|---|---|
| `GET /internal/v1/acting/me` | → `{username, permissions: [id, …]}` (effective) |
| `POST /internal/v1/acting/roles` | `{name, …}` → `201 {role}` |
| `PATCH /internal/v1/acting/roles/{id}` | subset of `{color, position, permissions, irc: {whois_line, vhost, oper}, hoist, mentionable}` → `{role}`; `null` clears |
| `DELETE /internal/v1/acting/roles/{id}` | → `204` |
| `PUT /internal/v1/acting/users/{user_id}/roles` | `{role_ids}` (replaces) → `{roles}` |

RoleServ uses `GET /internal/v1/users/{username}/irc` for the user id and
current roles, and computes `PERM` / `ASSIGN` / `REMOVE` as a full list.
(Two people changing the same role's permissions or the same user's roles at
the same second can overwrite each other; if that matters, add
`If-Match: <role.updated_at>` support and RoleServ will send it.)

### 2. Internal members list (RoleServ `WHO`, pushes after role edits)

`GET /internal/v1/roles/{id}/members?cursor=&limit=` (`X-Internal-Token`):
the admin API's members list (`{users: [{id, username, disabled, …}],
next_cursor}`, `limit` 1–200) on the internal listener.

### 3. Grants feed (live admin-panel changes)

`GET /internal/v1/irc/grants` (`X-Internal-Token`):

```json
{"version": "…", "users": [
  {"username": "alice", "oper_class": "admin", "whois_line": "is a Network Administrator", "vhost": "staff/alice"},
  {"username": "dave", "oper_class": null, "whois_line": null, "vhost": "dj/dave"}]}
```

Every **enabled** account whose `irc` object (as in verify-password) has a
non-null `oper_class` or `vhost`, sorted by username, with `ETag:
"<version>"` and `If-None-Match` → `304`. The version must change whenever
any of these objects changes: a role edit, an assignment, a user disabled or
enabled or renamed. (A Member vhost template puts every account in the
list; fine for a few thousand.) An account that drops out of the list is
de-opered and loses its vhost.

## Limitations

- Ergo 2.19 has no per-account switch for creating channels, so
  `channel.create` isn't enforced on IRC: under `channel_creation:
  registered` anyone can create a channel by joining it (turn the
  `guest_access` network setting off to keep guests out, see
  [irc-channels.md](irc-channels.md#guest-access-guest_access)); under `admins`, only opers with
  `irc.chanreg` can (Owner and Admin by default).
- Role colours, hoisting and mentions have no IRC representation (a client
  can read them from `GET https://AUTH_HOST/v1/roles` and
  `/v1/users/roles`).
- Only oper roles get a WHOIS line: Ergo shows `313` for operators only.
  A non-oper role's line (DJ "is a DJ") shows in `RoleServ INFO`, not in
  `/WHOIS`; its vhost does show.
- Vhosts from roles replace HostServ vhosts: `HS SET` by an oper is
  overwritten at the user's next login (or push). A user's `HS OFF` is kept.
- With auth 1.5.0, assignments made in the admin panel reach connected users
  at their next login (no grants feed yet), and RoleServ is read-only (no
  acting API yet).
- Always-on clients restored after an Ergo restart are not opered until a
  session logs in again (Ergo doesn't persist `+o`).
- One rehash per role-definition change: Ergo reloads its whole config, which
  is cheap and doesn't drop anyone.
- `/MODE nick -o` drops the powers until the next login; there's no
  "re-oper" command (reconnect, or `/msg NickServ IDENTIFY` on a new
  connection).

## Testing

```sh
test/roles/run.sh fake                      # patched Ergo + fake auth (full contract) + RoleServ
AUTH_SRC=../auth test/roles/run.sh real     # patched Ergo + a real auth checkout (>= 1.5)
```

[test/roles/smoke-roles.py](../test/roles/smoke-roles.py) connects like a
plain IRC client: SASL PLAIN (and OAUTHBEARER against real auth), checks
381/`+o` at login, WHOIS `313` and the vhost in `311` as another user, a
non-oper role's vhost without `313`, RoleServ's answers (and the hierarchy
on writes, with the fake), a WHOIS-line edit and an oper role losing its
`irc.*` permissions reaching connected users, membership changes (live with
the grants feed, at reconnect without), disabled accounts refused, and that
`RoleServ` can't be logged into through auth. It also covers the channel
lifecycle ([irc-channels.md](irc-channels.md)): auto-registration on JOIN,
the 15-per-account limit, guests' channels staying unregistered, expiry with
a 20 s `IRC_CHANNEL_EXPIRE_AFTER` (warning, expiry, a busy channel kept, `CS
SET EXPIRY` refused for non-opers), a deleted user's account and channels
removed and a disabled user's channels kept (real auth: `AUTH_FULL=1`, auth
1.7+; deletion via SQL, auth has no delete API). CI runs the fake variant.
