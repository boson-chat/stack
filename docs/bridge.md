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
   in with your NickServ password). Guests aren't available on this path.
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
| `evrd-links` | evrd's connection holder (`evrd-links`): holds every Boson user's connection to your server and the bridge's listeners, publishes 6697. Pinned to `EVRD_LINKS_IMAGE` (default `ghcr.io/4evrd/evrd:0.59.0`) and **never restarted by updates**: restarting it drops every Boson user off IRC. The admin panel can't restart it (`DOCKER_PROTECTED_SERVICES`). |
| `evrd` | evrd (`EVRD_IMAGE`, same image) in `evrd-links`' network namespace, config `bridge/evrd.conf` (written by setup), data in `bridge_data`, certificate from certsync (`BRIDGE_CERT_HOST`). Health: `GET /api/managed/health` with `BRIDGE_TOKEN`. Update it on its own (`up -d --no-deps evrd`): connections stay up. |
| `bridge-sync` | `images/bridgesync`: gives every Boson account (auth's `users`, read-only) its bridge account through the managed API (`PUT/DELETE /api/managed/users/{username}`, subject = the token's `sub`), within `BRIDGE_SYNC_INTERVAL` (2s). Disabled accounts are switched off, deleted ones removed. |

`.env`: `BRIDGE_TOKEN`, `BRIDGE_WEBIRC_PASSWORD`, `BRIDGE_SECRET_KEY`
(generated), `BRIDGE_IRC_SERVER`, `BRIDGE_CERT_HOST`; auth's
`ACCOUNT_BACKEND` and `NICKSERV_*` (auth `docs/nickserv.md`, auth ≥ 1.23.0);
`WEBIRC_ALIASES` (the web client's IRC goes to the bridge),
`WEBIRC_PASSWORDS` (your server, for direct connections through the web
gateway), `WELLKNOWN_SECTIONS` (boson.json names `irc.DOMAIN` as the IRC
server), `ADMIN_IRC_CHECK`, `ADMIN_CONFIG_STATUS_TARGETS` (no Ergo),
`ERGO_API_TOKEN` empty (no IRC roles).

## Not yet

- **NickServ identification on the bridge.** With NickServ accounts, a
  Boson user signs in with their NickServ password, but the bridge's
  connection isn't identified to NickServ yet: auth must hand the bridge the
  services login. auth's `nsaccount.Provisioner` hook (called after each
  NickServ sign-in, with the password in hand) should `PUT
  /api/managed/users/{username}` on the bridge with `{"subject", "email",
  "nick", "auth": {"method": "sasl" or "nickserv", "account", "password"}}`
  (`BRIDGE_API_URL=http://evrd-links:8098`, `BRIDGE_TOKEN`; evrd keeps the
  password encrypted with its secret key). Then `bridge-sync` only creates
  accounts and never touches `auth`.
- **Guests** (evrd's managed mode has none yet).
- **Web client users' addresses**: the web gateway connects to the bridge
  from the compose network and evrd takes no WEBIRC from it, so a web user
  shows on your server with the gateway's address.
