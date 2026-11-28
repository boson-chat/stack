# Boson next to an existing IRC server (the bridge)

The setup wizard's second path, **Add Boson to my existing IRC server**,
installs Boson around an IRC server you already run (UnrealIRCd 6,
InspIRCd 4, ngIRCd, or another): accounts, voice, video, uploads, the web
client, the directory and the admin panel, without Ergo (no `irc` profile).
Your server keeps running as it is; classic IRC clients keep using it.

Boson apps don't connect to your server directly. They connect to a
**bridge** on this machine, `irc.DOMAIN:6697`: [evrd](https://github.com/4evrd/evrd)
in managed mode (its `docs/managed.md`). For each signed-in Boson user it
keeps a connection to your server, made with `WEBIRC` so your server sees
the user's own address, and gives Boson apps what your server lacks
(message tags, message IDs, server time, echo, chat history, read markers),
as the wizard's capability table shows. Nothing Boson users see names the
bridge: its lines come from your server's name, under your network's name.

## What the wizard does

1. **Your IRC server**: address, port, TLS. A certificate that isn't from a
   trusted CA is pinned (the bridge accepts exactly that one; auth gets its
   key's SHA-256). Optionally the services RPC (Anope 2.1 `jsonrpc`, Atheme
   `transport/jsonrpc` or `xmlrpc`) and a test account.
2. **What Boson gets**: `pkg/probe`'s table: native, via the bridge, or not
   available, with how to get each natively.
3. **The bridge**, in plain words, and the address your server sees this
   machine connect from (from your server's welcome; editable for NAT).
4. **Accounts**, in auth's decision order (auth `docs/nickserv.md`):
   services RPC → people sign in with their **NickServ accounts**, checked
   through the RPC; SASL linked to services → NickServ through SASL;
   otherwise **Boson accounts**. You can override it (IDENTIFY is offered as
   a last resort, never recommended).
5. Domain, TLS, admin, features, ports, as on a new server. With NickServ
   accounts the admin is your NickServ account (no Boson password: you sign
   in with your NickServ password). **Guests** (on by default, as on a new
   server) come in through the bridge: see [Guests](#guests), and
   [Guests in voice](#guests-in-voice). Accounts from other Boson servers (federated login) are: they join your
   server through the bridge under a nick that carries their home server,
   without a services account there ([federation.md](federation.md#add-on-installs)).
6. **Your server's config**: what to add to your server, per software, with
   Copy buttons. The wizard never edits your server. **Re-check** probes
   again until WEBIRC works with the bridge's password and this machine may
   open many connections; with NickServ accounts over RPC, until the RPC
   answers; with the UnrealIRCd reactions option, until `+draft/react`
   passes.
7. Review, install, done.

### The snippets

| | WEBIRC | Many connections from the bridge |
|---|---|---|
| UnrealIRCd 6 | `webirc { mask IP; password …; }` | `allow { mask IP; class clients; maxperip 10000; global-maxperip 10000; }` (after your own allow blocks: the last match wins), `except throttle`, `except ban { type { connect-flood; handshake-data-flood; } }`, `set::connthrottle::except` |
| InspIRCd 4 | `<gateway type="webirc" mask password>` (module `gateway`) | `<connect name="boson-bridge" allow="IP" localmax globalmax limit …>` **before** your other `<connect>` blocks (the first match wins) |
| ngIRCd | `[Options] WebIRCPassword` | `[Limits] MaxConnectionsIP = 0` |

**ngIRCd can't exempt one address**: `MaxConnectionsIP = 0` lifts the
per-address limit for every client on your server. The wizard says so next
to the snippet. If you keep your limit, only that many Boson users can be
online at once.

**UnrealIRCd drops `+draft/react`** (only tags a module registers pass, and
none ships for reactions). The wizard can add a small module,
`images/setup/internal/stack/snippets/boson-react.c` (modelled on the
official `reply-tag`): build it in your UnrealIRCd source tree
(`src/modules/third/`, `make && make install`), then `loadmodule
"third/boson-react";`.

Services RPC hints (Anope `httpd` + `jsonrpc` + `rpc_user`/`rpc_data`,
Atheme `misc/httpd` + `transport/jsonrpc`) are shown while the RPC doesn't
answer.

## In the stack (profile `bridge`)

| Service | |
|---|---|
| `evrd-links` | evrd's connection holder (`evrd-links`): holds every Boson user's connection to your server and the bridge's listeners, publishes 6697. Pinned to `EVRD_LINKS_IMAGE` (default `ghcr.io/4evrd/evrd:0.60.0`) and **never restarted by updates**: restarting it drops every Boson user off IRC. The admin panel can't restart it (`DOCKER_PROTECTED_SERVICES`). |
| `evrd` | evrd (`EVRD_IMAGE`, same image) in `evrd-links`' network namespace, config `bridge/evrd.conf` (written by setup), data in `bridge_data`, certificate from certsync (`BRIDGE_CERT_HOST`). Health: `GET /api/managed/health` with `BRIDGE_TOKEN`. Update it on its own (`up -d --no-deps evrd`): connections stay up. |
| `auth` | Also provisions the bridge (auth ≥ 1.25.0, auth `docs/bridge.md`): with `BRIDGE_API_URL` (compose sets `http://evrd-links:8098` while `BRIDGE_CERT_HOST` is set) and `BRIDGE_TOKEN`, it gives every Boson account its bridge account through evrd's managed API (`PUT/DELETE /api/managed/users/{username}`, subject = the token's `sub`). Database triggers queue every change (sign-up, rename, disable or federation ban, delete) and a full sync runs at start and every 10 minutes, so a bridge outage loses nothing. After each NickServ sign-in it also hands the bridge that NickServ login, encrypted there with evrd's secret key, so the user's connection to your server is identified to NickServ (`BRIDGE_NICKSERV_METHOD`: `sasl` by default; setup writes `nickserv`, IDENTIFY once connected, for a server without SASL such as ngIRCd). Status: `GET http://auth:8081/internal/v1/bridge/status` (`X-Internal-Token`). This replaces the stack's earlier `bridge-sync` service, whose accounts auth takes over on its first full sync; on an existing install `docker compose up -d --remove-orphans` removes its old container. |

`.env`: `BRIDGE_TOKEN`, `BRIDGE_WEBIRC_PASSWORD`, `BRIDGE_CLIENT_WEBIRC_PASSWORD`,
`BRIDGE_SECRET_KEY` (generated), `BRIDGE_IRC_SERVER`, `BRIDGE_CERT_HOST`, `BRIDGE_NICKSERV_METHOD`; auth's
`ACCOUNT_BACKEND` and `NICKSERV_*` (auth `docs/nickserv.md`, auth ≥ 1.23.0);
`WEBIRC_ALIASES` (the web client's IRC goes to the bridge),
`WEBIRC_PASSWORDS` (`<your server>=<BRIDGE_WEBIRC_PASSWORD>` for direct
connections through the web gateway, and `evrd-links=<BRIDGE_CLIENT_WEBIRC_PASSWORD>`
for the bridge: [web users' addresses](#web-users-addresses)), `WELLKNOWN_SECTIONS` (boson.json names `irc.DOMAIN` as the IRC
server), `ADMIN_IRC_CHECK`, `ADMIN_CONFIG_STATUS_TARGETS` (no Ergo),
`ERGO_API_TOKEN` empty (no IRC roles).

### bridge/evrd.conf

Setup writes it (running setup again rewrites it); secrets stay in `.env`.
Besides the listeners, storage and `managed_server`/`managed_token_*`
lines it sets:

- `managed_nick_guard_grace 60s` (evrd ≥ 0.61): a Boson user without a
  services login who lands on a nick registered with NickServ has 60
  seconds to `IDENTIFY` before the bridge moves them to their alternative
  nick. One who identifies keeps the nick, and the bridge tries it first
  next time (`identifiedNick` in the managed API). `0` moves them at once.
- No `managed_guests`: auth sets the bridge's guests (below).

While `.env` pins an evrd older than 0.61 (`EVRD_IMAGE`), setup leaves the
grace out (evrd stops on a directive it doesn't know) and doesn't add the
gateway's `evrd-links=` WEBIRC password; run it again after updating evrd.

### Guests

With guests allowed (`auth.guest_access` in `config/boson.yaml`, the
wizard's Features step or the admin panel), people without an account
chat in Boson under a nick, through the bridge (evrd ≥ 0.61, evrd
`docs/managed.md` "Guests"): each gets a temporary connection to your
server, with WEBIRC and their own address, kept off nicks registered with
NickServ, and gone 30 seconds after they leave (`managed_guest_linger`).
Limits: 100 at once (`managed_guests_max`), 3 from one address
(`managed_guests_per_ip`); add those lines to `bridge/evrd.conf` to change
them.

auth drives the switch (auth `docs/bridge.md` "Guests"): at startup and on
every change of `guest_access` it sends `PUT /api/managed/settings
{"guests": …}`, so the bridge lets guests in exactly when Boson does. That
needs an auth with that change; an older auth leaves the bridge's
setting alone (evrd.conf's, off), and guests are refused (`464`).

### Web users' addresses

The web client reaches the bridge through the web gateway
(`WEBIRC_ALIASES`: `irc.DOMAIN:6697` → `evrd-links:6667`). The gateway
sends WEBIRC with each web user's address to the bridge too:
`WEBIRC_PASSWORDS` has `evrd-links=<BRIDGE_CLIENT_WEBIRC_PASSWORD>`, and
compose hands evrd the same secret as `EVRD_MANAGED_CLIENT_WEBIRC_PASSWORD`
(`managed_client_webirc_password`, evrd ≥ 0.61). So a web user, guest or
account, shows on your server with their own address rather than the
gateway's, and guests are limited per address.

### Guests in voice

With `auth.guest_voice` on as well, guests join voice under their IRC nick
as on any install. A voice channel under a text channel with **Only people
in #channel** needs the guest's nick to be in that channel: quicvoiced asks
the bridge (`BRIDGE_API_URL`, compose sets `http://evrd-links:8098` while
`BRIDGE_CERT_HOST` is set, and `BRIDGE_TOKEN`), whose connections already
know who's in each channel. Needs quicvoice 1.17 or later (boson-chat/quicvoice#22) and an evrd (4evrd/evrd#185) with
`GET /api/managed/channels/{channel}/members/{nick}`; with older ones those
channels stay locked for guests ("IRC role check unavailable"). Open voice
channels (everyone can join) never need it. Guests are plain members
everywhere: a channel that needs voice or op stays locked for them, even
if their nick has it (the nick isn't verified), and the app says so.

### Upgrading an older add-on install

An install made by an earlier wizard has evrd 0.60 or older, a
`bridge/evrd.conf` without the grace, no `BRIDGE_CLIENT_WEBIRC_PASSWORD`
and guests forced off. First update evrd to 0.61 or later (the admin
panel's Updates page, or `EVRD_IMAGE=ghcr.io/4evrd/evrd:0.64.0` in `.env`
and `docker compose up -d --no-deps evrd`; a re-run of setup keeps the
pinned version). Leave `EVRD_LINKS_IMAGE` alone: changing it drops every
Boson user. Then either run setup again (`install.sh`; it writes the rest
and keeps your secrets), or by hand:

1. In `.env`: `BRIDGE_CLIENT_WEBIRC_PASSWORD=<openssl rand -hex 24>`, and
   append `,evrd-links=<that password>` to `WEBIRC_PASSWORDS`.
2. In `bridge/evrd.conf`: add `managed_nick_guard_grace 60s`.
3. `docker compose up -d --no-deps evrd webircgateway` (connections stay
   up: evrd-links isn't touched).
4. Guests: update auth to a release that drives the bridge's guests, then
   set `auth.guest_access: true` in `config/boson.yaml` (or in the admin
   panel) if you want them.

## Not yet

- **Channel roles in voice** for signed-in users on this path: voice
  channels that need +v/+o (or people in the channel) only let in network
  admins; quicvoiced has no Ergo to ask.
- **A web user's new address**: the bridge keeps the address a user's
  connection was made with until it reconnects.
