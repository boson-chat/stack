# Push notifications (v1)

> **Status: in progress.** This is the design being implemented across the stack, a new push relay, the Boson app and evrd; sections land as their PRs merge.
> Stack side done: Ergo `webpush:` (§1), the `boson.json` `push` block and the `pushrelay`
> compose profile ([Running the relay](#running-the-relay)). The relay itself, the apps and
> evrd are still to come.

Goal: a person signed in to a Boson install gets a notification on their phone, desktop or
browser when they are mentioned or sent a DM while no Boson device is connected, from any
Boson install they are signed in to. Self-hosted installs need no account with anyone and no
push credentials of their own. Nobody but the person's own install can make their device show
a notification, and nobody in between (relay, Apple, Google, browser vendors) can read it.

v1 scope: networks running the Boson stack with Ergo, plus federated accounts on them. evrd
(add-on installs like ircforever, and later the home bouncer) is phase 2, using the same
protocol.

## Decisions (user-approved)
- **Only people with a Boson account** get push: an account on boson.chat or on a self-hosted
  Boson install, federated accounts included. Not guests, not plain IRC servers added
  directly, not people using their own ZNC/evrd.
- **Browser push is sent directly** by each install. No relay involved.
- **Phone push goes through one relay** run at boson.chat. It holds the APNs and FCM keys of
  the official Boson app, and every install uses it. Its URL is configurable, so a
  third-party build of the app runs its own relay.
- **No accounts for installs on the relay.** Installs don't sign up or get API keys.
- Push is a later feature. Always-on (stack #56, `IRC_ALWAYS_ON` default `opt-out`) ships
  first and is required: Ergo only pushes for always-on accounts.

## 0. Building blocks that already exist
- **Ergo 2.19.1 (the stack's Ergo) implements IRCv3 `draft/webpush`.** A logged-in client sends
  `WEBPUSH REGISTER <endpoint> <keys>`, where `keys` holds the client's P-256 public key and
  auth secret. Ergo then encrypts every DM and highlight to that key (RFC 8291), signs the
  request with its own VAPID key (RFC 8292, advertised in ISUPPORT `VAPID=`), and POSTs it to
  the endpoint (RFC 8030). It requires an account and always-on, refuses non-HTTPS and
  internal-IP endpoints, caps subscriptions per account (`max-subscriptions`, default 4),
  expires them (`expiration`, 14d, renewed on reconnect), and can delay a push and drop it if
  the message was read elsewhere (`delay` + MARKREAD).
- **Browsers** give a page a standard Web Push endpoint plus keys, which is exactly what
  `WEBPUSH REGISTER` takes.
- So **the relay is itself a Web Push endpoint.** For phones, the app registers the relay's
  URL with Ergo instead of a browser's. Ergo needs no Boson-specific push code; the relay
  receives ordinary encrypted, VAPID-signed Web Push and forwards it to APNs or FCM.

## 1. Stack (Ergo)
> **Status: done** (this repo). Settings below; `scripts/smoke-irc.py` checks that Ergo
> offers `draft/webpush` and `VAPID=`.

- Render `webpush:` in `images/ergo/helper/ircd.yaml.tmpl`:
  - `enabled` (`IRC_PUSH` / `irc.push`, default true),
  - `delay` (default `20s`, so a message read on another device within 20 s is not pushed),
  - `subscriber: https://<DOMAIN>/`,
  - `max-subscriptions: 8` (phone, tablet, desktop, a few browsers),
  - `expiration: 30d`.
- Ergo generates and stores its VAPID key itself: in its datastore, `/ircd/ircd.db`
  (created by `ergo initdb`, or added by the schema upgrade on an existing database), which is
  the `ergo_db` volume. So the key and the subscriptions survive restarts, image updates and
  recreating the container; only removing the volume makes a new key (devices then register
  again on their next connect).
- Add `push` to the Ergo `fakelag` command budgets only if needed (WEBPUSH is already 1).
- Guests can't register (Ergo refuses unless logged in), so the "accounts only" rule holds on
  Ergo networks without extra code.
- `boson.json` gains `"push": {"version": 1, "relay": "https://push.boson.chat"}` (the relay
  this install recommends; default boson.chat's, override `PUSH_RELAY_URL`, empty = phone
  push off for this install; browser push still works).

### Settings
| Where | Default | |
|---|---|---|
| `IRC_PUSH` / `irc.push` | `true` | `webpush.enabled`. Forced off when `IRC_ALWAYS_ON` / `irc.always_on` is `disabled`, since Ergo won't start with push on and always-on off. Applies with a rehash (clients get `CAP NEW`/`DEL`) |
| `IRC_PUSH_DELAY` / `irc.push_delay` | `20s` | `webpush.delay`. A Go duration (`s`/`m`/`h`, not `d`), 0 to 1h; 0 = push at once |
| `PUSH_RELAY_URL` (`wellknown`) | `https://push.boson.chat` | `boson.json` `push.relay`; an `https://` URL, or empty for no relay |
| fixed | | `subscriber: https://DOMAIN/` (IRC_HOST without `DOMAIN`), `max-subscriptions: 8`, `expiration: 30d`, `timeout: 10s` |

The `push` block is published with the `irc` section (it describes this network's IRC
server). Clients still check that the server offers `draft/webpush` and `VAPID=`, so an
add-on install's block (evrd has no push before phase 2) is harmless.

### Egress
Ergo sends pushes itself, from the `ergo` container, over HTTPS (443) to wherever each
device registered: browser push services (`fcm.googleapis.com`,
`updates.push.services.mozilla.com`, `web.push.apple.com`, ...) and the relay
(`push.boson.chat`). Nothing in `compose.yaml` restricts that: `ergo` is on the stack's
default bridge network, which has a route out (only `docker-proxy` is `internal`). Things that
do break it:
- **A host firewall or egress filter** that blocks outbound 443 from Docker networks.
- **An HTTP(S) proxy as the only way out.** Ergo's push client has its own transport and
  ignores `HTTPS_PROXY`; such installs can't push.
- **Split-horizon DNS** that answers a private address for the relay or a push service:
  Ergo refuses to dial loopback, private (RFC 1918, ULA), link-local and CGNAT addresses,
  whatever the name. That includes boson.chat itself reaching `push.boson.chat`: it must
  resolve to the public address (hairpinning through Docker's published 443 works).
- Non-HTTPS endpoints are refused at `WEBPUSH REGISTER`.

## 2. The relay (`boson-chat/pushrelay`, new Go service, runs only at boson.chat)
A small Web Push service that delivers to APNs and FCM. Stack profile `pushrelay`, off by
default; boson.chat turns it on. Storage: Postgres (the stack's), one table of mailboxes.
> **Status:** the stack profile is done ([Running the relay](#running-the-relay)); the
> service itself is being built in `boson-chat/pushrelay`.

### 2.1 Creating a mailbox (phone → relay)
`POST /v1/mailboxes`, body:
```json
{
  "platform": "apns" | "apns-sandbox" | "fcm",
  "token": "<APNs device token or FCM registration token>",
  "app": "chat.boson.app",
  "attestation": "<App Attest assertion (iOS) or Play Integrity token (Android)>",
  "vapid": "<the install's VAPID public key, from Ergo ISUPPORT VAPID=>",
  "install": "boson.ircforever.org"
}
```
The relay:
1. Verifies the attestation: the request comes from the genuine official app on a real device
   (App Attest key registered once per install of the app; Play Integrity verdict
   `MEETS_DEVICE_INTEGRITY` and our package name/signing cert). Development builds use
   `apns-sandbox` and a dev allowlist instead.
2. Creates a mailbox: random 128-bit id, bound to (`platform`, `token`, `vapid`, `install`).
   One mailbox per (device, install).
3. Returns `{"endpoint": "https://push.boson.chat/v1/push/<id>", "secret": "<mailbox
   management secret>"}`. The secret lets the app later refresh the device token or delete
   the mailbox; it never leaves the device.

Mailbox endpoints are the app's to hand to its install; the relay never tells anyone else.

### 2.2 Accepting a push (install → relay)
`POST /v1/push/<id>`, a standard RFC 8030 Web Push request (RFC 8291 `aes128gcm` body,
RFC 8292 VAPID `Authorization: vapid t=<JWT>, k=<key>`). The relay accepts it only if:
- the mailbox exists and isn't paused;
- `k` equals the mailbox's bound VAPID key and the JWT verifies with it, with `aud` = the
  relay origin and `exp` ≤ 24 h ahead;
- the body is at most 4 KB (APNs' limit, minus overhead);
- rate limits allow it (§2.4).
Otherwise `401/404/413/429` per RFC 8030. A valid push returns `201` and is forwarded:
- **APNs:** `apns-push-type: alert`, `mutable-content: 1`, a generic alert ("New message")
  as fallback, and the encrypted body base64 in a custom key. The app's Notification Service
  Extension decrypts it and rewrites the notification ("thelion in #team: …"). Collapse id =
  mailbox id + channel hash so floods collapse. Token-based auth with our `.p8` key.
- **FCM:** a data-only, high-priority message with the encrypted body; the app decrypts and
  posts the notification itself.
- If APNs/FCM says the token is dead (`410 Unregistered`, `UNREGISTERED`), delete the mailbox
  and answer the install `410 Gone`, so Ergo drops the subscription.

The relay never holds a decryption key and logs only mailbox id, install, size, outcome.

### 2.3 Managing mailboxes (phone → relay, with the mailbox secret)
- `PATCH /v1/mailboxes/<id>`: new device token (tokens rotate).
- `DELETE /v1/mailboxes/<id>`: on sign-out, on removing the server, on turning push off.
- Mailboxes unused for 60 days are deleted.

### 2.4 Abuse limits
- Per mailbox: burst 10, then 1 per 6 s; at most 300 per day.
- Per install (by bound `install`): configurable ceiling, default 10 000 per hour; above it,
  `429` and an admin alert.
- Per IP creating mailboxes: 20 per hour; attestation is the main gate.
- Operator controls (relay admin API/CLI, boson.chat admin panel later): block an install
  domain (its pushes get `403`, its mailboxes paused), delete a mailbox, see per-install
  counts.
- Metrics: pushes accepted/rejected by reason, APNs/FCM errors, mailboxes by platform.

### 2.5 Why this stops strangers
- Nobody can create a mailbox without the official app on a real device (attestation).
- Nobody can guess a mailbox id (128 random bits), and knowing one isn't enough: pushes must
  be signed with the VAPID key of the install the phone registered with.
- Even a push that got through can't show anything: the app only displays what it can
  decrypt with its own key, which only the user's install has the public half of.
- The worst case is an install misusing pushes to its own signed-in users, who chose it and
  can sign out (which deletes the mailbox).

### Running the relay
Only boson.chat runs it (or whoever ships their own build of the app). Compose service
`pushrelay`, profile `pushrelay`, needs `auth` (the stack's Postgres).

| Service | Listens | Image |
|---|---|---|
| `pushrelay` | `:8080` public API, routed by Traefik at `https://PUSH_HOST`; `:3001` operator API (`X-Internal-Token`) and `/metrics`, compose network only | `harbor.floret.dev/boson/pushrelay` |

Before turning it on:
1. **DNS:** an A/AAAA record for `PUSH_HOST` (default `push.$DOMAIN`, so
   `push.boson.chat`) at the server. It must be the public address (see [Egress](#egress));
   behind Cloudflare, use a DNS-only record so Traefik sees client IPs and gets the cert.
2. `.env`: `pushrelay` in `COMPOSE_PROFILES`, `PUSH_HOST` added to `CERT_HOSTS`,
   `PUSHRELAY_DB_PASSWORD` and `PUSHRELAY_INTERNAL_TOKEN` (`scripts/setup.sh` writes both;
   `openssl rand -hex 32`), and the settings below. If `PUSH_HOST` isn't `push.boson.chat`,
   set `PUSH_RELAY_URL=https://PUSH_HOST` so this install's `boson.json` names it.
3. **Secrets as files**, never in `.env` or `compose.yaml`: put them in
   `PUSHRELAY_SECRETS_DIR` (default `./secrets/pushrelay`, git-ignored), mounted read-only
   at `/run/secrets/pushrelay`. The relay runs as uid 65532:
   ```sh
   install -d -m 750 -o root -g 65532 secrets/pushrelay
   install -m 640 -o root -g 65532 AuthKey_XXXXXXXXXX.p8 secrets/pushrelay/apns.p8
   install -m 640 -o root -g 65532 firebase-service-account.json secrets/pushrelay/fcm.json
   ```
4. **Existing install** (Postgres volume already there; `postgres/initdb/40-pushrelay.sh`
   only runs on a new one): create the database once:
   ```sh
   P=$(grep ^PUSHRELAY_DB_PASSWORD= .env | cut -d= -f2-)
   docker compose exec -T postgres psql -U auth -d postgres \
     -v pw="$P" -v usr=pushrelay -v db=pushrelay <<'SQL'
   CREATE ROLE :"usr" LOGIN PASSWORD :'pw';
   CREATE DATABASE :"db" OWNER :"usr";
   SQL
   ```
5. `docker compose up -d` (recreates `postgres` for the new env, `traefik` for
   `CERT_HOSTS`, and starts `pushrelay`).

Settings (`.env`; compose passes the same names to the relay unless noted):

| Variable | Default | |
|---|---|---|
| `PUSHRELAY_IMAGE` | `harbor.floret.dev/boson/pushrelay:0.1.0` | placeholder until the first release |
| `PUSH_HOST` | `push.$DOMAIN` | public hostname; the relay gets `PUSHRELAY_PUBLIC_URL=https://PUSH_HOST` (the VAPID `aud` it checks, the base of mailbox endpoints) |
| `PUSHRELAY_DB_PASSWORD`, `_USER`, `_NAME` | –, `pushrelay`, `pushrelay` | the relay gets `DATABASE_URL` |
| `PUSHRELAY_INTERNAL_TOKEN` | – | operator API on `:3001`: block an install domain, delete a mailbox, per-install counts |
| `PUSHRELAY_SECRETS_DIR` | `./secrets/pushrelay` | host directory, mounted read-only at `/run/secrets/pushrelay` (not passed to the relay) |
| `PUSHRELAY_APNS_KEY_FILE` | `/run/secrets/pushrelay/apns.p8` | APNs token-auth key (`.p8`), path in the container |
| `PUSHRELAY_APNS_KEY_ID` | – | that key's id (10 characters) |
| `PUSHRELAY_APNS_TEAM_ID` | – | Apple Developer team id |
| `PUSHRELAY_APNS_TOPIC` | `chat.boson.app` | the app's bundle id (`apns-topic`) |
| `PUSHRELAY_FCM_CREDENTIALS_FILE` | `/run/secrets/pushrelay/fcm.json` | Firebase project service account (FCM HTTP v1) |
| `PUSHRELAY_APP_ATTEST_APP_ID` | – (`TEAM_ID.TOPIC`) | App Attest app id |
| `PUSHRELAY_APP_ATTEST_ENV` | `production` | `production`, or `development` to accept dev-signed builds' attestations |
| `PUSHRELAY_PLAY_INTEGRITY_PACKAGE` | `chat.boson.app` | Android package name |
| `PUSHRELAY_PLAY_INTEGRITY_CERT_SHA256` | – | release signing certificate SHA-256 digest(s), comma-separated |
| `PUSHRELAY_PLAY_INTEGRITY_CREDENTIALS_FILE` | – (the FCM one) | service account allowed to decode integrity tokens |
| `PUSHRELAY_DEV_ALLOWLIST_FILE` | – | development builds (`apns-sandbox`) allowed in; empty = refused |
| `PUSHRELAY_MAILBOX_BURST`, `_INTERVAL`, `_PER_DAY` | `10`, `6s`, `300` | per mailbox (§2.4) |
| `PUSHRELAY_INSTALL_PER_HOUR` | `10000` | per install domain; `429` above it |
| `PUSHRELAY_CREATE_PER_IP_PER_HOUR` | `20` | new mailboxes per client IP (from Traefik's `X-Forwarded-For`, `TRUSTED_PROXIES`) |
| `PUSHRELAY_MAILBOX_IDLE_DAYS` | `60` | unused mailboxes are deleted after this |
| `PUSHRELAY_LOG_LEVEL` | `info` | passed as `LOG_LEVEL` |

The relay also gets `HTTP_ADDR=:8080`, `INTERNAL_ADDR=:3001` and `TRUSTED_PROXIES` (the stack
subnet). These names are the stack's side of the contract with `boson-chat/pushrelay`; if
the relay settles on different ones, change them here.

Check: `curl -si https://PUSH_HOST/v1/push/0` answers `404` (no such mailbox).

## 3. Client (Boson app)

### 3.1 Which servers offer push
A server offers push when all hold:
- the person is **signed in** there with a Boson account (local or federated), not a guest;
- the IRC server advertises `draft/webpush` and `VAPID=`;
- for phones, its `boson.json` has `push.relay` (else: browser/desktop only).
Elsewhere, Settings → Notifications shows "Sign in to get notifications" (Boson servers) or
nothing (plain IRC).

### 3.2 Turning it on (per server, on by default after sign-in)
1. Ask the OS for notification permission once (explain first: "Get notified about mentions
   and DMs when Boson is closed").
2. Generate a P-256 key pair and 16-byte auth secret **per (device, server)**. Store the
   private key in the keychain (iOS) / Keystore (Android) / app storage (desktop) / IndexedDB
   (web, via the browser's own subscription).
3. Get an endpoint:
   - **Web client:** `pushManager.subscribe({applicationServerKey: <VAPID>})`; the browser
     returns endpoint and keys.
   - **Desktop app (Wails):** use the platform's notification service where available; until
     then, desktop relies on being connected (always-on catches it up). Phase 2: desktop via
     relay + APNs (macOS) / FCM or UnifiedPush (Linux/Windows).
   - **iOS / Android:** register with APNs/FCM, then create a relay mailbox (§2.1).
4. Send `WEBPUSH REGISTER <endpoint> p256dh=<pub>;auth=<secret>` on that server's IRC
   connection. Re-send on every connect (renews Ergo's expiration).
5. Settings show each server's state: On / Off / Unavailable (with why).

### 3.3 Receiving
- iOS: a Notification Service Extension (new target in the iOS build) decrypts with the
  keychain key (shared via an App Group), parses the IRC line, and sets title/body:
  "<nick> in #channel" / "<nick>" for DMs, plus the server name. Tap opens that buffer.
  Payloads it can't decrypt are shown as the generic "New message" fallback only if the
  mailbox is known, otherwise suppressed.
- Android: the FCM service decrypts and posts a notification per conversation (grouped by
  server), same text.
- Web: the service worker decrypts (the browser does it), shows the notification, focuses
  the tab or opens the client on click.
- Respect per-channel mute and the app's notification level (all / mentions / none),
  applied on the device after decryption.

### 3.4 Turning it off
`WEBPUSH UNREGISTER <endpoint>` on the server, then `DELETE` the relay mailbox. Do both on
sign-out, server removal, and when the person turns push off. If the server is unreachable,
delete the mailbox anyway: Ergo then gets `404/410` and drops the subscription itself.

## 4. evrd (phase 2: add-on installs and the home bouncer)
evrd implements the same server side as Ergo so add-on installs (ircforever) and, later,
home-bouncer networks push too:
- advertise `draft/webpush` and `VAPID=` to its clients; per-install VAPID key in its data
  dir;
- `WEBPUSH REGISTER/UNREGISTER` only for clients logged in with a Boson account (managed
  mode: an auth token, not guests);
- push on DMs and highlights received while the account has no attached client, with the
  same delay + read-marker suppression, RFC 8291/8292 encryption and signing, and `410`
  handling;
- a subscription cap per account and expiration renewed on reconnect.

## 5. Privacy
- Content is end-to-end encrypted from the install to the device. Relay, Apple, Google and
  browser vendors see only: a push happened, its size, the mailbox, and (relay only) the
  install domain.
- The relay stores device token, VAPID key, install domain and timestamps per mailbox; no
  account names, nicks or message data.
- The app's privacy text says which relay is used and that notification content is
  encrypted.

## 6. Rollout
1. Always-on default (stack #56, **done**).
2. Stack: Ergo `webpush:` config + `boson.json` `push` block (**done**); web client browser
   push. Browser push works end to end with no relay.
3. pushrelay service + stack profile (**stack profile done**); deploy at push.boson.chat (needs: APNs `.p8` key, Apple
   Team ID and bundle id; Firebase project service account; Play Integrity and App Attest
   setup).
4. iOS and Android app support (key storage, mailbox registration, Notification Service
   Extension / FCM service).
5. evrd `draft/webpush` (phase 2), then desktop via relay.

## 7. Open questions
- Notification text when the app can't decrypt (iOS fallback): generic "New message" vs
  suppress entirely.
- Whether to show who/where on the lock screen by default or only "New message" (privacy
  setting).
- Per-install relay rate ceiling for large installs; whether directory-verified installs get
  a higher default.
- UnifiedPush support for de-Googled Android (ntfy as the endpoint instead of the relay).
