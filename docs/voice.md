# Voice channels, the room list and client discovery

## Voice channels

quicvoiced has a fixed list of channels, set in `.env`:

```sh
# id:Display Name, comma-separated, in display order
VOICE_ROOMS=general:General,gaming:Gaming,music:Music
# true (default): joining any other room fails with ROOM_NOT_FOUND.
# false: any other name creates an ad-hoc room (listed while it's in use).
#VOICE_ROOMS_STRICT=true
```

Ids are lowercase `[a-z0-9-]{1,32}` (a bare `id` uses the id as its name);
quicvoiced refuses to start on a bad list. Changing it takes
`docker compose up -d` (quicvoiced restarts; everyone in voice reconnects).

A voice token's `rooms` claim (`POST /v1/voice/token` with `{"rooms": [...]}`)
still limits which of these a user may join and see.

## Room list API

Traefik routes `https://VOICE_HOST` to quicvoiced's public listener
(`:8090`). Its internal listener (`:8080`, with `/metrics`) is never routed.

```
GET https://VOICE_HOST/v1/rooms
Authorization: Bearer <voice token from POST https://AUTH_HOST/v1/voice/token>
```

`200`, `Content-Type: application/json`, `Cache-Control: no-store`:

```json
{"rooms":[
  {"id":"general","name":"General","participants":[
    {"session_id":3,"user_id":"usr_123","name":"alice","muted":false,"deafened":false,"speaking":true}]},
  {"id":"gaming","name":"Gaming","participants":[]}]}
```

- Rooms in `VOICE_ROOMS` order; rooms the token's `rooms` claim doesn't
  allow are left out. `participants` is always an array, sorted by
  `session_id` (the id in that room's audio datagrams).
- `muted`: self- or server-muted. `speaking`: sent voice (VAD bit) in the
  last second.
- No, invalid or expired token: `401` with `{"error":"..."}`.

`GET https://VOICE_HOST/healthz` → `ok` (no token).

Try it:

```sh
ACCESS=$(curl -fsS https://AUTH_HOST/v1/login -H 'Content-Type: application/json' \
  -d '{"login":"you","password":"..."}' | jq -r .access_token)
VOICE=$(curl -fsS -X POST https://AUTH_HOST/v1/voice/token -H "Authorization: Bearer $ACCESS" \
  -H 'Content-Type: application/json' -d '{}' | jq -r .token)
curl -fsS https://VOICE_HOST/v1/rooms -H "Authorization: Bearer $VOICE" | jq
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
