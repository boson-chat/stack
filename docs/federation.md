# Federated login

People with an account on another Boson server can use this one without
signing up here, and people from here can do the same on other servers. In
the Boson app, adding a server that accepts federated accounts offers
**Continue as alice@boson.chat** next to the usual sign-in.

No passwords or chat data cross servers. The person's **home** server signs
a short-lived **assertion** (who they are, for which server); this server,
the **remote**, checks it and signs them in to a local **federated account**.
From then on this server's own tokens work for everything (IRC, voice,
video, uploads) under this server's rules: its roles, bans, quotas and
moderation.

The auth service does the work (boson-chat/auth `docs/federation.md` has the
protocol, the API and every error code). This page covers the stack:
turning it on, the trust modes, ports and DNS, and how it relates to the
directory.

- [Turning it on](#turning-it-on) · [Trust modes](#trust-modes) · [Ports and DNS](#ports-and-dns)
- [The directory](#the-directory-verified-servers) · [Remote users](#remote-users-on-this-server) · [Settings](#settings)
- [Add-on installs](#add-on-installs) · [Testing](#testing)

## Turning it on

It needs auth 1.24.0 or newer (the stack's default image), the `wellknown`
profile, and `DOMAIN` pointing at this server:

1. `COMPOSE_PROFILES` includes `auth` and `wellknown`, and `DOMAIN` is in
   `CERT_HOSTS`. The setup wizard does both when you turn on "app discovery"
   on its Domain step.
2. `https://DOMAIN/.well-known/boson.json` has a `federation` section:

   ```sh
   curl -s https://example.org/.well-known/boson.json | python3 -m json.tool
   ```

   ```json
   "federation": {
     "version": 1,
     "issuer": "https://auth.example.org",
     "jwks_uri": "https://auth.example.org/.well-known/jwks.json",
     "assertion_endpoint": "https://auth.example.org/v1/federation/assertions",
     "login_endpoint": "https://auth.example.org/v1/federation/login",
     "accepts": "directory"
   }
   ```

3. Pick who you accept (below). New installs accept **verified servers**.

Both directions work from then on: your users can sign in elsewhere (as a
home), and other servers' users can sign in here (as a remote), unless the
mode is `off`, which turns off both.

**How the section gets there.** The `wellknown` service fetches auth's
`http://auth:8080/.well-known/boson-federation.json` inside the compose
network and publishes it verbatim as `"federation"`. It fetches it again
every `WELLKNOWN_REFRESH_SECONDS` (60), since `accepts` follows the mode an
admin sets. If auth can't be reached, the last good copy stays. Until the
first fetch works (auth still starting, or an auth older than 1.24.0), the
section is left out. `WELLKNOWN_FEDERATION_URL=` (empty) leaves it out for
good.

**Which domain.** Assertions name the home by its `boson_domain`: auth's
`FEDERATION_DOMAIN`, by default `AUTH_HOST` without its `auth.` (so
`DOMAIN` with the usual `AUTH_HOST=auth.DOMAIN`). Set `FEDERATION_DOMAIN=DOMAIN`
in `.env` when `AUTH_HOST` is something else. (A dev install on `localhost`
has no domain and issues no assertions.) Other servers fetch
`https://<boson_domain>/.well-known/boson.json` and require its
`federation.issuer` to be this auth. So `DOMAIN` must be the domain that
serves boson.json. If boson.json is served elsewhere (your website at
`DOMAIN` already has one), set `FEDERATION_DOMAIN` in `.env` to that
domain, and publish auth's section there yourself.

## Trust modes

`auth.federation.mode` in `config/boson.yaml` (the admin panel's Federation
page, or the wizard's Features step):

| mode | who can sign in here with an account from another server | |
|---|---|---|
| `directory` | servers with a **verified** listing in the directory (default `https://directory.boson.chat`) | the default: a server's owner had to prove its domain with a DNS record before its users get in |
| `allowlist` | only the home domains in `auth.federation.allow` | the tightest: you add each server you trust |
| `open` | any server whose boson.json and keys check out | anyone can set up a server, spammers too; only with active moderation |
| `off` | nobody | and your users can't sign in on other servers with their account here either |

In every mode, home domains in `auth.federation.block` and banned
identities are refused. List entries are domains (`boson.chat`) or
`*.example.org` (example.org and all its subdomains). The mode is applied
live: no restart.

Whatever the mode, a home server must itself serve a valid boson.json with
`accepts` other than `off`, and its assertion must be signed with the keys
its boson.json points at, be for this server, be at most 5 minutes old and
not have been used before.

## Ports and DNS

Nothing new is published. Everything is HTTPS on 443, through Traefik:

| who | asks | for |
|---|---|---|
| the app | `https://DOMAIN/.well-known/boson.json` | whether a server takes federated accounts |
| the app | `POST https://AUTH_HOST/v1/federation/assertions` (home) | an assertion, with the user's home token |
| the app | `POST https://AUTH_HOST/v1/federation/login` (remote) | the sign-in |
| this auth, as a remote | `https://<home domain>/.well-known/boson.json`, the home's JWKS, the directory | checking assertions |

DNS: `DOMAIN` must point at this server (an `A`/`AAAA` record, in
`CERT_HOSTS`) and so must `AUTH_HOST`, as for any install. As a remote,
auth makes outgoing HTTPS requests to other servers: allow outgoing 443 if
your firewall filters it. Those requests time out after
`FEDERATION_HTTP_TIMEOUT` (10s), follow at most 3 redirects, never go to
plain http, and refuse private, loopback and link-local addresses, so a
hostile boson.json can't point auth at your internal services.

## The directory (verified servers)

The default mode trusts the directory's DNS check. A server is verified
when its owner lists it in the directory and publishes the TXT record
`_boson.<IRC hostname>` = `boson-verify=<token>`; the directory re-checks
it daily ([directory.md](directory.md)).

- **To be accepted elsewhere** (as a home): list your network in the
  directory and verify it. The wizard's "List this network in the Boson
  directory" switch, then the admin panel's Directory page, do that.
  Listings are by IRC hostname, so a remote looks up your `boson_domain`
  and then the `irc.host` of your boson.json when that is your domain or a
  subdomain of it (`irc.example.org` for `example.org`). Another domain's
  IRC host never counts.
- **Which directory** a remote trusts is `auth.federation.directory_url`
  (`FEDERATION_DIRECTORY_URL`, default `https://directory.boson.chat`). A
  server that runs its own directory (the `directory` profile) can trust
  that one instead. It then accepts the networks verified there, its peers'
  included, since each listing is re-checked against its own DNS.
- Answers are cached 15 minutes (not listed: 2 minutes). While the
  directory is down, a server already seen as verified stays trusted.
- **Browser calls follow the same directory.** Federated users upload and
  join calls here from their home server's web client, so voice, video and
  files accept the web clients of the servers verified in that directory
  (auth's `/v1/web-origins`, from each server's boson.json `web.url`; your
  own `WEB_HOST` is published there by the `wellknown` profile). See
  [voice.md](voice.md#browsers-which-pages). The api does too
  (`API_ALLOWED_ORIGINS`, [api.md](api.md)): a federated session on another
  server's web client calls the user's home api with the home token, so
  your users signed in on another server's web client still reach their
  account's api here.

## Remote users on this server

- Each person is a local account with a **handle** (their IRC account name
  and nick): their home username if free here, else `alice-bosonchat`,
  then a number. People see them as `alice@boson.chat` in the app.
- They get the role `Remote` (`FEDERATION_REMOTE_ROLE`) plus Member, like
  everyone; give them more like any user.
- No password: IRC sign-in is with tokens only (SASL OAUTHBEARER, which the
  stack's Ergo has on: keep `IRC_OAUTHBEARER=true`).
- **Uploads** go to this server, under `auth.federation.remote_upload_quota`
  (empty = the normal account quota). auth sets it per user on the files
  service at sign-in, through its internal API (`FILES_INTERNAL_URL`,
  `FILES_INTERNAL_TOKEN`; compose wires both).
- **Voice and video**: `auth.federation.remote_voice` (default on).
- **Banning** an identity (admin panel, Federation page) ends its sessions,
  disables its account here and refuses its next sign-ins. Ergo drops it at
  once: the boson-ergo helper sees the disabled account in auth's feed of
  removed accounts (every `SETTINGS_POLL_SECONDS`) and disconnects it
  (patched Ergo `POST /v1/account_disconnect`,
  [irc-channels.md](irc-channels.md#deleted-and-disabled-users)).
- **Blocking a home domain** (`auth.federation.block`) refuses its sign-ins
  from then on and ends that domain's sessions. From auth 1.26.0
  (the stack's default; boson-chat/auth#30) it also drops its users from IRC at once, like a ban:
  auth puts each of the domain's federated accounts in the same feed as a
  disabled account, so the helper disconnects them, and refuses their
  reconnects until the domain is unblocked (nothing is deleted; unblocking
  lets them straight back in). This needs `IRC_ROLES=true` (the default):
  without it the auth-script doesn't ask auth, and an access token that
  hasn't expired (at most `ACCESS_TOKEN_TTL`) could still reconnect. With
  an older auth, IRC clients already connected stay until they reconnect.

## Settings

`config/boson.yaml` wins over `.env`, which wins over the defaults; changes
in the file apply live ([config.md](config.md)):

| config/boson.yaml | .env default | |
|---|---|---|
| `auth.federation.mode` | `FEDERATION_MODE` (`directory`) | `off` \| `allowlist` \| `directory` \| `open` |
| `auth.federation.allow` | `FEDERATION_ALLOW` | home domains for `allowlist` |
| `auth.federation.block` | `FEDERATION_BLOCK` | home domains refused in every mode |
| `auth.federation.directory_url` | `FEDERATION_DIRECTORY_URL` (`https://directory.boson.chat`) | the directory `directory` mode trusts |
| `auth.federation.remote_upload_quota` | `FEDERATION_REMOTE_UPLOAD_QUOTA` | e.g. `500MB`; empty = the account quota |
| `auth.federation.remote_voice` | `FEDERATION_REMOTE_VOICE` (`true`) | remote users may get voice and video tokens |

`.env` only: `FEDERATION_DOMAIN` (default `AUTH_HOST` minus `auth.`),
`FEDERATION_REMOTE_ROLE` (`Remote`), `FEDERATION_REMOTE_MAX_FILE`,
`WELLKNOWN_REFRESH_SECONDS` (60), `WELLKNOWN_FEDERATION_URL`.

The admin panel's Federation page (Accounts) edits the file's keys, lists
remote users, bans identities and blocks domains, and checks this server's
own boson.json as a home (it uses `BOSON_DOMAIN`, which compose sets to
`DOMAIN`).

## Add-on installs

On the add-on path (Boson next to an existing IRC server through the
bridge, [bridge.md](bridge.md)) the wizard offers the same choice as on a
new server, verified servers by default, and writes the same
`auth.federation` keys. It needs auth ≥ 1.27.0 and evrd ≥ 0.60.0 (the
stack's defaults). Installs made before stay `off` until you change the
mode.

Remote users reach your IRC server like every Boson user, through the
bridge (one connection each, with `WEBIRC`), but **without a services
account there**: auth gives their bridge account no services login
(`auth.method: none`), so the bridge never identifies them to NickServ,
let alone as someone else's account. Their nick is kept off nicks people
registered with your services:

1. **The handle carries the home domain.** Next to an existing network
   auth skips the bare home username: `alice@boson.chat` is
   `alice-bosonchat` here, never `alice`, which may be someone's nick on
   your network (and, with NickServ accounts, would keep its owner from
   signing in to Boson). auth's `FEDERATION_HANDLE_SUFFIX` (`auto`: always
   with the bridge; `always`; `clash`).
2. **The bridge's nick guard** (evrd `managed_nick_guard`, on by default)
   covers a suffixed nick someone registered all the same. When your
   services say the nick is registered (NickServ's notice), rename the
   user (`SVSNICK` to `Guest…`) or kill them over it, the bridge moves them
   to `alice-bosonchat_`, then `alice-bosonchat_2`…, remembers the nick so a
   reconnect doesn't loop, and holds their channel joins until the nick has
   settled, so a registered nick never shows in a channel. Their own app
   gets the `NICK` line like any nick change.

Banning an identity disables its account, and auth disables the bridge
account at once: the bridge drops the user's connection to your server.
Blocking a home domain refuses its sign-ins; users already connected stay
until their session ends. Uploads, voice and video work as on a new server.

## Testing

The add-on path has its own run: `test/setup/run-addon.sh ngircd-anope`
and `insp-atheme` (CI `setup-addon-e2e`) also bring up a second, home
install (`test/setup/addon-home.sh`: `home.test`, auth and wellknown only,
its own Pebble, on a shared Docker network the add-on's auth joins), let
`home.test` in on the wizard's Features step (only servers I list), and
`addon-federation.py` checks that alice@home.test signs in on the add-on
install as `alice-hometest` and gets a bridge account with no services
login. Someone else has registered `alice` and `alice-hometest` with
NickServ, with enforcement on (Anope `SET KILL 20`, Atheme `SET ENFORCE
ON`). The bridge moves her to `alice-hometest_` before she joins. A classic
client on the IRC server sees her join and talk only under that nick, not
identified to NickServ. Past the enforcement delay she hasn't been renamed
or killed (an impostor on `alice` has been). A ban on the add-on install
drops her, and her next sign-in is refused.

The two-install run below covers fresh installs.

`test/federation/run.sh` (CI job `federation-e2e`) runs two full installs
on one machine, `home.test` and `remote.test`, each its own compose project
from its own copy of the repo. Nothing is published on the host. Both
Traefiks join a shared Docker network under their hostnames, which is the
test's DNS, and certificates come from Pebble over real HTTP-01. The remote
trusts verified servers in its own directory (`directory` profile, fake DNS
verifier) and runs the admin panel (`admin` profile, without docker-proxy).
The checks are `test/federation/fed-e2e.py`, in four phases:

**untrusted** (API)

1. alice signs up on home; home's boson.json has the federation section;
2. home issues her an assertion for the remote (claims, `boson-fed+jwt`,
   EdDSA);
3. while home is only *pending* in the remote's directory, the sign-in is
   refused with `domain_not_trusted`.

**main** (API; home is verified now)

4. the sign-in works (`federated`, `alice@home.test`, handle `alice`), with
   the remote's own token; the same assertion again is `replayed`;
5. she connects to the remote's Ergo with SASL OAUTHBEARER as her handle
   and joins a channel; bob, a local user of the remote, sees her message;
6. the remote's identity lookup returns `alice@home.test` (bob: local);
7. she uploads a file, with the remote-user quota (2MB, the account quota
   being 10MB);
8. she gets a voice token that the remote's voice server's HTTPS API takes.

**voice** (QUIC, `test/federation/voice`: the quicvoice client SDK in a
container on the remote's network, verifying quicvoiced's certificate)

9. alice, with her remote voice token, and bob join the room `general` on
   the remote's quicvoiced; both send 20 ms audio datagrams for 3 seconds
   and each must receive at least 20 of the other's.

**ui** (the remote's admin panel in Chromium, Playwright for Python; alice
and bob are connected to IRC throughout)

10. the remote's owner signs in to the panel and opens Federation: "Your
    server as a home" is green and `alice@home.test` is in Remote users;
11. **Ban** on her row: Ergo drops her connection, bob sees her quit, a new
    IRC login with her token is refused and a new sign-in is
    `identity_banned`;
12. **Unban**: she signs in and connects to IRC again;
13. "Remote users can join voice" off and saved: `remote_voice: false` in
    config/boson.yaml and her next voice token is refused
    (`remote_voice_disabled`); back on, she gets one again;
14. **Block server** (home.test): Ergo drops her within seconds (bob sees
    her quit), her IRC reconnect is refused and her sign-in is
    `domain_blocked`; **Unblock**: she signs in and connects again;
15. mode **Off** saved: config/boson.yaml says `mode: off`, the remote's
    boson.json says `accepts: off` within `WELLKNOWN_REFRESH_SECONDS` (5
    in the test) and a sign-in is `federation_off`.

Screenshots of the panel steps go to `test/federation/shots/` (the CI job
uploads them as `federation-screenshots`). Both installs keep
`IRC_ROLES=true`: refusing a banned or blocked account's IRC reconnect
relies on the helper's auth-script asking auth.

Trimmed: home runs `irc,auth,voice,files,wellknown`, the remote adds
`directory` and `admin`; neither runs api, web, metrics or video, which
federation doesn't touch.
