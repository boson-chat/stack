# Nick claims (`nick_claim:`, api profile)

**Off by default.** boson.chat turns it on; a self-hosted stack only needs it
if you want to offer it, and it needs a mailbox of its own.

## What it does

A boson app user who connects to **another** IRC network (Libera, OFTC, a
friend's server) can have the app register their nick with that network's
NickServ for them, without typing an email address or copying a code from
their inbox:

1. The app asks the api for a claim: `POST /me/nick-claims`
   `{"server_id": "irc.libera.chat", "account_nick": "alice"}`. The api answers
   with a one-off address, `reg+<user id>-<token>@<email_domain>`.
2. The app sends `NickServ REGISTER <password> <that address>` on the other
   network.
3. That network's NickServ mails a confirmation code to the address. Your
   domain's mail server delivers it to the **inbound mailbox**.
4. The api's worker polls the mailbox (POP3, every few seconds), matches the
   message to the claim by its address, and pulls the code out (Anope
   `CONFIRM`, Atheme/Ergo `VERIFY REGISTER`, or a generic "code: …").
5. The app polls `GET /me/nick-claims/<id>`, gets the code once, and sends
   `NickServ CONFIRM` / `VERIFY` itself.

A claim expires after 30 minutes. Each user can start
`rate_limit_per_hour` claims per hour (429 after that).

While it's off, `POST /me/nick-claims` answers
`403 {"error": "nick_claim_disabled"}` (the app hides the option), and the
mailbox is never touched.

## Mail setup

The worker needs one mailbox that receives **every** `reg+…@<email_domain>`
address. Outgoing mail (`auth.smtp`) has nothing to do with it.

1. **Pick the domain.** Use a subdomain just for this, e.g.
   `nick.example.com`: its mail stays apart from your own, and you can turn
   it off with one DNS change. `email_domain` defaults to `DOMAIN`.
2. **Point its MX at your mail provider** and add the domain there (with the
   SPF/DKIM records it asks for; only receiving matters here).
3. **Create a mailbox**, e.g. `reg@nick.example.com`, that holds nothing
   else. The worker **deletes** every message that isn't a confirmation for a
   pending claim (matched ones are kept, as a record).
4. **Get `reg+anything@` delivered to it.** Either:
   - plus-addressing (subaddressing): most providers deliver `reg+tag@` to
     `reg@` out of the box; or
   - a catch-all: deliver every address on the domain to the mailbox.
     On **PurelyMail**: *Routing* → add a rule for the domain, *any address*
     (catch-all) → `reg@nick.example.com`. On a provider with neither, use a
     domain that has nothing else on it and its catch-all.
5. **Turn on POP3** for the mailbox if the provider makes it optional, and
   note its server and port (PurelyMail: `mailserver.purelymail.com`, 995
   with TLS; 995 with TLS is the usual anywhere).

Test it before you turn the feature on: send a mail to
`reg+test@nick.example.com` and check it arrives in `reg@`.

## Turning it on

1. Put the mailbox password in `.env` (it's a secret, so it never goes in the
   config file) and recreate the api so it reads it:

   ```sh
   echo 'NICK_CLAIM_POP3_PASSWORD=…' >> .env
   docker compose up -d api
   ```

2. Add the section to `config/boson.yaml` (or fill in the admin panel's
   **Nick claims** page, which writes the same keys):

   ```yaml
   nick_claim:
     enabled: true
     email_domain: nick.example.com
     rate_limit_per_hour: 5
     inbound:
       protocol: pop3
       host: pop3.example.com
       port: 995
       tls: true
       user: reg@nick.example.com
   ```

   The api picks it up within a few seconds, no restart: it starts the
   worker (or reconnects with the new mailbox settings). `enabled: false`
   stops it again; claims already handed out simply expire.

Every key is optional except `enabled`; an absent one falls back to its
`.env` default (table below). [config.md](config.md) has the file's rules.

### Test connection (admin panel)

The **Nick claims** page has a **Test connection** button
(`settings.manage`). The panel asks the api, over its internal port
(`BOSON_API_INTERNAL_URL`, `http://api:3001`, with `API_INTERNAL_TOKEN`), to
log in to the mailbox with the settings on the page, before you save them,
and the password from `.env`. It shows "Connected, N messages waiting" or the
mail server's error (wrong password, TLS, unreachable host). It only logs in
and counts; it never reads or deletes mail. Test, then save.

## Is it working?

The api's config status adds a `nick_claim` object
(`http://api:3001/internal/config/status`, compose network only):

```json
{"ok": true, "loaded_at": "…", "file_mtime": "…", "restart_required": [],
 "nick_claim": {"enabled": true, "worker": "running", "last_poll_at": "…", "last_error": null}}
```

`worker` is `stopped` (off), `running`, or `error` (with `last_error`: the
worker keeps retrying). The admin panel shows the same on the Nick claims
page and in the per-service "applied" list after a save.

```sh
docker run --rm --network boson_default busybox:1.37.0 wget -qO- http://api:3001/internal/config/status
```

## Privacy

Tell your users what this means before you turn it on.

- **You hold the keys to those accounts.** The other network's NickServ
  knows the account by an address on your domain. Anything it sends there,
  including password-reset mail, lands in your mailbox. Whoever can read the
  mailbox (you, your mail provider, anyone with its password) can confirm or
  recover those accounts. Keep the password in `.env` only, and don't share
  the mailbox.
- **The address identifies the user to you, not to them.** It carries the
  boson user's id and a random token, not their name or real email, so the
  other network learns only that the account was made through your stack.
  Their operators can see the address (NickServ `INFO` to opers, and to
  anyone if the user doesn't hide it).
- **What's stored.** The api keeps each claim (user, network, nick, status,
  the code once captured) in its database; codes are handed to the app once.
  Confirmation mails stay in the mailbox as a record; everything else there
  is deleted on sight. Clear the mailbox if you want them gone.
- **It's per claim.** A user who registers their nick with their own
  address doesn't go through any of this.
- **Off means off.** With `enabled: false` the api refuses new claims and
  never connects to the mailbox.

## Settings

| `config/boson.yaml` | `.env` default | |
|---|---|---|
| `nick_claim.enabled` | `NICK_CLAIM_ENABLED` (`false`) | the switch |
| `nick_claim.email_domain` | `NICK_CLAIM_EMAIL_DOMAIN` (`DOMAIN`) | domain in the `reg+…@` addresses |
| `nick_claim.rate_limit_per_hour` | `NICK_CLAIM_RATE_LIMIT_PER_HOUR` (`5`) | claims per user per hour |
| `nick_claim.inbound.protocol` | – (`pop3`) | only POP3 so far |
| `nick_claim.inbound.host` / `.port` / `.tls` / `.user` | `POP3_HOST` / `POP3_PORT` (`995`) / `POP3_TLS` (`true`) / `POP3_USER` | the mailbox |
| never in the file | `NICK_CLAIM_POP3_PASSWORD` (or the older `POP3_PASSWORD`) | its password |

Before this section existed, setting `POP3_HOST` was enough to turn the
feature on. Now it's off until `nick_claim.enabled` (or
`NICK_CLAIM_ENABLED=true`) says so. `scripts/migrate-config.sh` keeps an
existing install that has `POP3_HOST` as it was: it writes
`enabled: true` and the `POP3_*` values into the file. Run it (or set
`NICK_CLAIM_ENABLED=true` in `.env`) **before** moving such an install to an
api image with the switch, or nick claims stop until you do.
