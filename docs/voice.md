# Voice channels, the room list and client discovery

## Voice channels

quicvoiced keeps a **room registry**: the **static** channels in `.env`

```sh
# id:Display Name, comma-separated, in display order
VOICE_ROOMS=general:General,gaming:Gaming,music:Music
# true (default): only registry rooms can be joined (ROOM_NOT_FOUND otherwise).
# false: any other name creates an ad-hoc room (listed while it's in use).
#VOICE_ROOMS_STRICT=true
```

plus **dynamic** channels people create through the API, Discord-style,
optionally under an IRC channel (`parent: "#boson"`), and lockable to IRC
channel roles. Static ids are lowercase `[a-z0-9-]{1,32}` (a bare `id` uses
the id as its name); quicvoiced refuses to start on a bad list. Changing
`VOICE_ROOMS` takes `docker compose up -d` (quicvoiced restarts; everyone in
voice reconnects). Static channels can't be changed through the API.

The registry is `/data/rooms.json` in the `voice_data` volume
(`VOICE_ROOMS_DB`); the one-shot `voice-data-init` service makes it writable
for quicvoiced. Back it up with `ergo_db`.

A voice token's `rooms` claim (`POST /v1/voice/token` with `{"rooms": [...]}`)
still limits which channels a user may join and see.

Dynamic channels need quicvoice **1.5.0** or later (`QUICVOICE_IMAGE`).

### Who can create, rename and delete

**Create**: the auth service's `voice_channel_creation` setting (admin
panel), which quicvoiced reads from `http://auth:8081/internal/v1/settings`
(`INTERNAL_API_TOKEN`) and caches for 30 s; `VOICE_CHANNEL_CREATION`
(default `admins`) until auth answers.

| Setting | Auth admins | Everyone else |
|---|---|---|
| `admins` | anywhere | nowhere |
| `founders` | anywhere | only under an IRC channel they founded (`/msg ChanServ REGISTER #chan`) |
| `registered` | anywhere | anywhere |

**Rename / reorder / re-lock / delete**: the channel's creator, the founder
of its parent IRC channel, or an auth admin. Deleting kicks everyone in it
(`Error{KICKED}`). Limits: `VOICE_ROOMS_MAX_PER_USER` (10) per non-admin,
`VOICE_ROOMS_MAX` (500) in total.

"Auth admin" = voice token `role` `moderator`/`admin`, which the auth service
gives its admins. They pass every lock.

### Roles and locks

A dynamic channel with a parent can require an IRC role to join
(`min_role`) and, separately, to talk (`speak_role`; below it you're a
listener), and optionally that the user is in the IRC channel right now
(`require_channel_membership`). Roles, lowest first:
`member` < `voice` (+v) < `halfop` (%) < `op` (@) < `admin` (&) <
`founder` (~ / ChanServ founder) < `oper` (auth admin). Channels without a
parent can only be locked to `member` (any registered user) or `oper`.

Roles come from Ergo: the registered founder (works while they're offline)
and the user's current prefix mode in the channel (ChanServ AMODEs are
applied when they join it). To give someone a role that voice sees, use
ChanServ in the IRC channel: `/msg ChanServ AMODE #chan +o bob`. See
[irc-channels.md](irc-channels.md) for the commands.

### How quicvoiced asks Ergo

Ergo's HTTP API is on with `ERGO_API_TOKEN` set (`scripts/setup.sh` makes
one): it listens on `:8089` inside the compose network, is not published on
the host and not routed by Traefik. quicvoiced (`ERGO_API_URL=http://ergo:8089`)
calls

- `POST /v1/ns/info {"accountName"}` → channels the account is the
  registered founder of, and
- `POST /v1/whois {"nickname"}` → the user's live channels and prefix modes
  (nick = account name on this stack),

with the voice token's `name` (= auth username = Ergo account), cached 15 s
per user. If Ergo can't be reached, role-locked channels refuse joins and
founder-only creation returns `503`; unlocked channels keep working.
Without `ERGO_API_TOKEN` (or without the irc profile) there are no IRC roles:
only auth admins pass role locks and the `founders` policy.

Known gap: an AMODE only counts while the user is in the IRC channel (Ergo's
API can't list AMODEs). Founders always count.

## Rooms API

Traefik routes `https://VOICE_HOST` to quicvoiced's public listener
(`:8090`). Its internal listener (`:8080`, with `/metrics`) is never routed.
Every call: `Authorization: Bearer <voice token from POST https://AUTH_HOST/v1/voice/token>`.
The full reference (every field and error) is
[quicvoice docs/ROOMS.md](https://github.com/boson-chat/quicvoice/blob/main/docs/ROOMS.md).

| Call | Body | Success |
|---|---|---|
| `GET /v1/rooms` | | `200` `{"permissions": {...}, "rooms": [...]}` |
| `POST /v1/rooms` | `{"name", "parent"?, "min_role"?, "speak_role"?, "require_channel_membership"?}` | `201` room, `Location: /v1/rooms/<id>` |
| `PATCH /v1/rooms/{id}` | any of `{"name", "position", "min_role", "speak_role", "require_channel_membership"}` | `200` room |
| `DELETE /v1/rooms/{id}` | | `204` (everyone in it is kicked) |

`GET /v1/rooms` (`Cache-Control: no-store`):

```json
{"permissions":{"policy":"founders","can_create":true,"create_scope":"founded_channels","founded_channels":["#boson"]},
 "rooms":[
  {"id":"general","name":"General","kind":"static","parent":null,"owner":null,"position":0,
   "created_at":"2026-11-13T20:00:00.000Z","min_role":"member","speak_role":"member",
   "require_channel_membership":false,"can_manage":false,"can_join":true,"can_speak":true,
   "participants":[
    {"session_id":3,"user_id":"usr_123","name":"alice","muted":false,"deafened":false,"speaking":true}]},
  {"id":"boson-game-night","name":"Game Night","kind":"dynamic","parent":"#boson",
   "owner":{"user_id":"usr_456","name":"fred"},"position":1,"created_at":"2026-11-13T21:04:05.123Z",
   "min_role":"voice","speak_role":"op","require_channel_membership":false,
   "can_manage":true,"can_join":true,"can_speak":true,"participants":[]}]}
```

- `permissions.create_scope`: `any`, `founded_channels` (only under
  `founded_channels`), or `none`: whether and where to show "+ voice channel".
- `can_manage` / `can_join` / `can_speak` are for the caller.
- Rooms in display order (`position`, then creation). `participants` is
  always an array, sorted by `session_id`; `muted` = self- or server-muted;
  `speaking` = sent voice (VAD bit) in the last second.
- Names: 1–32 characters, unique per parent (case-insensitive). The id is a
  slug made at creation (`boson-game-night`) and never changes, so renames are
  safe.
- Errors: `{"error":"...","code":"..."}`, with `400 invalid`, `401
  unauthorized`, `403 forbidden | static_room | limit`, `404 not_found`,
  `409 conflict`, `503 unavailable` (Ergo unreachable for a founder/role check).

`GET https://VOICE_HOST/healthz` → `ok` (no token).

Try it:

```sh
ACCESS=$(curl -fsS https://AUTH_HOST/v1/login -H 'Content-Type: application/json' \
  -d '{"login":"you","password":"..."}' | jq -r .access_token)
VOICE=$(curl -fsS -X POST https://AUTH_HOST/v1/voice/token -H "Authorization: Bearer $ACCESS" \
  -H 'Content-Type: application/json' -d '{}' | jq -r .token)
curl -fsS https://VOICE_HOST/v1/rooms -H "Authorization: Bearer $VOICE" | jq
curl -fsS -X POST https://VOICE_HOST/v1/rooms -H "Authorization: Bearer $VOICE" \
  -H 'Content-Type: application/json' -d '{"name":"Game Night","parent":"#boson"}'
```

## Discovery: `/.well-known/boson.json`

The boson client asks for a domain (e.g. `boson.chat`) and reads
`https://<domain>/.well-known/boson.json`:

```json
{"version":1,
 "irc":{"host":"irc.boson.chat","port":6697,"tls":true},
 "auth":{"url":"https://auth.boson.chat"},
 "voice":{"addr":"voice.boson.chat:7700","url":"https://voice.boson.chat"}}
```

Any section may be missing (no `voice` key = no voice on this network).
Served as `application/json` with `Access-Control-Allow-Origin: *`. When the
file isn't there, the client falls back to `irc.`/`auth.`/`voice.<domain>`
(QUIC to `voice.<domain>:7700` with ALPN `quicvoice/1`), so the standard
hostnames work without it.

### Served by the stack (`wellknown` profile)

For installs where `DOMAIN` itself points at this server:

```sh
COMPOSE_PROFILES=irc,voice,auth,wellknown
CERT_HOSTS=irc.example.com,voice.example.com,auth.example.com,example.com
# Optional. Default: the irc/auth/voice profiles that are on.
#WELLKNOWN_SECTIONS=irc,auth,voice
```

The `wellknown` service (busybox httpd, read-only, as `nobody`) writes the
file from `IRC_HOST`, `AUTH_HOST`, `VOICE_HOST` and `WELLKNOWN_SECTIONS` at
every start (`images/wellknown/serve.sh`), and Traefik routes only
`https://DOMAIN/.well-known/boson.json` to it, adding
`Access-Control-Allow-Origin: *` and `Cache-Control: public, max-age=300`.
Set `WELLKNOWN_SECTIONS` when a part runs outside the stack, e.g. a legacy
IRC server on `IRC_HOST` (`irc,auth,voice`). The irc section always says
port 6697 with TLS.

`scripts/setup.sh` asks whether to turn it on (and then adds `DOMAIN` to
`CERT_HOSTS`); `--local` turns it on for `https://localhost`.

### Served by your website

If `DOMAIN` is a website hosted elsewhere (boson.chat is on Cloudflare),
publish the same JSON there at `/.well-known/boson.json` with
`Content-Type: application/json` and `Access-Control-Allow-Origin: *`, and
don't run the `wellknown` profile.
