# Push notifications (v1)

> **Status: planned.** This is the design being implemented across the stack, a new push relay, the Boson app and evrd; sections land as their PRs merge.

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
- Render `webpush:` in `images/ergo/helper/ircd.yaml.tmpl`:
  - `enabled` (`IRC_PUSH` / `irc.push`, default true),
  - `delay` (default `20s`, so a message read on another device within 20 s is not pushed),
  - `subscriber: https://<DOMAIN>/`,
  - `max-subscriptions: 8` (phone, tablet, desktop, a few browsers),
  - `expiration: 30d`.
- Ergo generates and stores its VAPID key itself; keep it in the persistent data volume so
  subscriptions survive restarts and image updates.
- Add `push` to the Ergo `fakelag` command budgets only if needed (WEBPUSH is already 1).
- Guests can't register (Ergo refuses unless logged in), so the "accounts only" rule holds on
  Ergo networks without extra code.
- `boson.json` gains `"push": {"version": 1, "relay": "https://push.boson.chat"}` (the relay
  this install recommends; default boson.chat's, override `PUSH_RELAY_URL`, empty = phone
  push off for this install; browser push still works).

## 2. The relay (`boson-chat/pushrelay`, new Go service, runs only at boson.chat)
A small Web Push service that delivers to APNs and FCM. Stack profile `pushrelay`, off by
default; boson.chat turns it on. Storage: Postgres (the stack's), one table of mailboxes.

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
1. Always-on default (stack #56, done/merging).
2. Stack: Ergo `webpush:` config + `boson.json` `push` block; web client browser push.
   Browser push works end to end with no relay.
3. pushrelay service + stack profile; deploy at push.boson.chat (needs: APNs `.p8` key, Apple
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
