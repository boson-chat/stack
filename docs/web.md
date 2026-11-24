# Web profile: Boson in the browser

The Boson web client (served at `WEB_HOST`, e.g. chat.boson.chat) runs the
whole app in a browser. A browser can't open TCP connections, so its IRC
connections go through **webircgateway**: WebSocket in, TCP/TLS out, to this
network's Ergo or to any other IRC network.

```
browser ── wss://WEB_HOST/webirc/websocket/ ──► Traefik ──► webircgateway ──┬─► ergo:6680 (WEBIRC)
                                                                          └─► irc.example.net:6697 (TLS)
```

Turn it on with the `web` profile and `WEB_HOST` (add it to `CERT_HOSTS`):

```
COMPOSE_PROFILES=…,web
WEB_HOST=chat.example.com
CERT_HOSTS=…,chat.example.com
WEBIRC_PASSWORD=…            # scripts/setup.sh makes one: openssl rand -hex 24
```

## The web client

The `web` service serves the client (`harbor.floret.dev/boson/web`, built
from boson-chat/boson `infra/web`: the same renderer as the desktop app,
the Go IRC engine compiled to WebAssembly) at `https://WEB_HOST`, with its
Content-Security-Policy and caching headers. Its endpoints are set at start
(`/boson-config.js`), so the image is the same for every stack:

| Setting | Default | |
|---|---|---|
| `WEB_IMAGE` | `harbor.floret.dev/boson/web:<pinned>` | pin a release |
| `WEB_API_URL` | `https://API_HOST` | the Boson API (`api` profile) |
| `WEB_DIRECTORY_URL` | `https://directory.boson.chat` | the directory the app browses |
| `WEB_GATEWAY_URL` | this host's `/webirc/websocket/` | another gateway |

The app's sign-in goes to `https://AUTH_HOST`. With `WEB_HOST` set, these
browser origins follow by default (each has its own override):

| Service | Setting | Why |
|---|---|---|
| auth | `AUTH_CORS_ALLOWED_ORIGINS` (or `auth.cors_allowed_origins` in config/boson.yaml, which wins: add `https://WEB_HOST` there if you set it) | calls from the page, and SSO coming back to `https://WEB_HOST/sso/callback` |
| auth | `AUTH_WEB_CLIENT_URL` (default `https://WEB_HOST/`) | where users land after verifying their email (auth with boson-chat/auth#26; older auth ignores it) |
| api | `API_ALLOWED_ORIGINS` | calls from the page |
| directory | `DIRECTORY_CORS_ALLOWED_ORIGINS` | owner edits from the page |
| quicvoiced | `VOICE_WEBTRANSPORT_ORIGINS` (or `voice.web_origins`) | voice over WebTransport at `https://VOICE_HOST:7700/wt`, and its room API |
| quicvideod | `VIDEO_WEBTRANSPORT_ORIGINS` (default any) | video over WebTransport at `https://VIDEO_HOST:7710/wt` |

Link previews and Spotify cards come from the Boson API (`GET /v1/unfurl`,
backend 0.57.0 or later, the `api` profile; see [api.md](api.md#link-previews)), so
`WEB_API_URL` must point at an API that has it. YouTube videos play through a
small wrapper page the web container serves from `WEB_HOST` itself
(`/youtube-embed.html`, framable only by the app).

Calls need a browser with WebTransport and WebCodecs (Chrome, Edge, Firefox; Safari where it has WebTransport);
elsewhere the app is chat-only and says why. The UDP ports
(7700, 7710) are the same ones the desktop app uses.

## The gateway

[kiwiirc/webircgateway](https://github.com/kiwiirc/webircgateway), built
from a pinned commit (`images/webircgateway/Dockerfile`) with our patches
(`patches/`) and policy (`src/zz_boson_policy.go`, whose unit tests run in
the image build). Without guards a WebSocket→TCP gateway is an open proxy;
these are the guards:

| Guard | What | Setting (default) |
|---|---|---|
| Ports | IRC ports only: 6660–6669, 6697, 7000, 7070, 9999. Anything else is refused before dialing. | `WEBIRC_EXTRA_PORTS` (none), e.g. `6680,8000-8001` |
| Addresses | The address actually dialed (after DNS, so a name that rebinds to an internal address is caught) must be public: loopback, private, link-local, CGNAT, multicast, documentation and reserved ranges (v4 and v6) are refused. | `WEBIRC_ALLOW_PRIVATE` (false; dev/e2e only) |
| Aliases | The only internal targets: a requested `host:port` is sent to `target:port` instead, plaintext, internal addresses allowed. This network's own Ergo by default. | `WEBIRC_ALIASES` (`IRC_HOST:6697` and `IRC_HOST:6667` → `ergo:6680`) |
| Allow / deny | Hostname globs (`*.libera.chat`); deny wins; an empty allow list means any public network. | `WEBIRC_UPSTREAM_ALLOW`, `WEBIRC_UPSTREAM_DENY` |
| Per client IP | Concurrent connections, and new connections a minute (a token bucket: that many at once, then one every 60 s / N). | `WEBIRC_MAX_PER_IP` (30), `WEBIRC_RATE_PER_MIN` (60); 0 = no cap |
| Lines | After registration a client may send `WEBIRC_THROTTLE_BURST` lines at once, then `WEBIRC_THROTTLE` a second (registration itself isn't throttled). Networks add their own flood control on top (Ergo: 5 lines, then 2 a second). | `WEBIRC_THROTTLE` (4), `WEBIRC_THROTTLE_BURST` (20) |
| Origins | Only pages from these origins may open the WebSocket (non-browser clients send none; the caps above still apply). | `WEBIRC_ORIGINS` (`https://WEB_HOST`) |
| Client IP | Read from `X-Forwarded-For` only from the stack network (Traefik), rightmost first, so a client can't claim an address. | `WEBIRC_TRUSTED_PROXIES` (`STACK_SUBNET`) |
| Upstream TLS | Certificates are verified (upstream webircgateway skipped this). A server the user marked "accept self-signed" is the only exception. | – |
| Logs | Client IP, destination, close reason; never IRC lines (level 1, debug, would log them: never in production). | `WEBIRC_LOG_LEVEL` (2) |

### WEBIRC

With WEBIRC a gateway tells the IRC server each user's real address, so bans,
connection limits and cloaks work per user instead of hitting everyone on
the gateway's IP.

- **This network's Ergo** trusts it automatically: `WEBIRC_PASSWORD` in
  `.env` goes to both. Ergo gets a `webirc` block (password hashed with
  bcrypt, from `WEBIRC_HOSTS`, default `STACK_SUBNET`) and exempts those
  addresses from its own per-IP limits (they apply to the real address
  after WEBIRC).
- **Other networks** see the server's IP unless they trust the gateway. To
  ask one: send its operators the server's IP address(es), the gateway name
  (`boson`, `WEBIRC_GATEWAY_NAME`) and a contact, and agree on a password.
  Add it as `WEBIRC_PASSWORDS=ergo=<WEBIRC_PASSWORD>,irc.example.net=<theirs>`
  (by the hostname clients connect to; keep the `ergo=` entry). Networks that
  don't trust the gateway may cap how many web users can be on at once.

### Client protocol

What the web client's engine speaks (any IRC-over-WebSocket client can):

1. Open `wss://WEB_HOST/webirc/websocket/`. One IRC line per WebSocket text
   message (several lines in one message, `\n`-separated, also work; up to
   64 KiB per message).
2. Before registering, say where: `HOST irc.example.net:+6697` (`+` = TLS
   to the network, no `+` = plaintext). `HOST irc.example.net:+6697 insecure`
   accepts a self-signed or otherwise unverifiable certificate.
3. Register as usual (`CAP`, `PASS`, `NICK`, `USER`, SASL). The gateway
   connects once it has `NICK` and `USER`.

A refusal is one line, then the socket closes:

```
ERROR :Gateway refused (<code>): <reason>
```

| code | when |
|---|---|
| `port` | not an IRC port |
| `address` | the server resolves to an internal address |
| `denied` | the deny list, or not on the allow list |
| `limit` | too many connections from this IP (sent right after the socket opens) |
| `rate` | too many new connections from this IP (same) |
| `connect` | the name doesn't resolve, the server refused, or didn't answer in time |
| `tls` | the certificate didn't verify (wrong name, untrusted, expired) |
| `host` | (internal) no destination |

Without `HOST` at all the gateway answers `ERROR :The server has not been
configured` (webircgateway's own wording). If the network closes the
connection, the socket closes without an `ERROR` from the gateway (the
network's own `ERROR` line, if any, comes through).

When the socket closes (a closed or reloaded tab, a dropped connection: close
frame or not), the gateway leaves the network at once: it sends `QUIT`, the
client's own if it sent one, else `QUIT :Web client closed`, and closes the
connection, dropping lines still queued behind the throttle. The nick is free
for the reloaded page straight away (`scripts/smoke-webirc.py` checks within
a second). A connection that goes silent without closing (a sleeping laptop)
is still only noticed by the network's ping timeout.

### Running it elsewhere (e2e, dev)

The image needs nothing but its environment. In another compose file, with
private destinations allowed (only on a machine nobody else can reach):

```yaml
webircgateway:
  image: harbor.floret.dev/boson/stack-webircgateway:1.1.0-boson.2
  ports: ["127.0.0.1:18089:8080"]
  environment:
    WEBIRC_ALLOW_PRIVATE: "true"   # any address, e.g. ergo:6667 or 127.0.0.1
    WEBIRC_EXTRA_PORTS: "16667,16501"
    WEBIRC_ORIGINS: ""             # any page
    WEBIRC_MAX_PER_IP: "0"
    WEBIRC_RATE_PER_MIN: "0"
    WEBIRC_PASSWORDS: "ergo=…"     # if that Ergo trusts it
```

Health: `GET /webirc/info` (200 with `{"name":"webircgateway",…}`). Traefik
routes `https://WEB_HOST/webirc/info` to the gateway too (nothing else of its
HTTP side but the WebSocket path).

### On the admin panel

The admin Stack page lists `web` (Boson in the browser) and `webircgateway`
(IRC gateway) once their containers exist: internal health from
`web:8080/healthz` and `webircgateway:8080/webirc/info`, web's version from
`/boson-config.js` (the gateway's from its image tag), and "from you" timings
to `https://WEB_HOST/healthz` and `https://WEB_HOST/webirc/info` (the admin
service gets `WEB_HOST`). Restart, stop and logs are on its Services page like
every other container.

The CI smoke (`scripts/smoke-webirc.py`, through Traefik) checks the alias
registration with WEBIRC, the refusals, origins and both per-IP caps.
