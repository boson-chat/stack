# The stack config file (`config/boson.yaml`)

One YAML file holds the stack's **non-secret runtime settings**: who can
register, channel rules, outgoing email (minus the password), IRC options,
voice rooms, the stack directory's identity and peers, nick claims (the
api's inbound mailbox, minus the password), and the admin panel's own
options. The admin panel edits it, you can edit it by hand, and every
service watches it and applies changes live.

`.env` keeps everything else: secrets (passwords, tokens, keys, DSNs) and
deployment values (hostnames, images, published ports, URLs between
services). The `.env` variables a key replaced are still read, as that key's
**default**.

## Where it lives

| | |
|---|---|
| On the host | `<stack dir>/config/boson.yaml` (gitignored) |
| In containers | `/config/boson.yaml`, path in `BOSON_CONFIG` |
| Reference | [`config/boson.example.yaml`](../config/boson.example.yaml): every key, commented |
| Owner / mode | uid 65532, `0644`; the directory `0755` |

`scripts/setup.sh` creates the file from the example plus your answers
(network name, voice rooms, registration). If it's missing, the
`config-init` service (busybox, runs before the services on every
`docker compose up`) copies the example; it also sets the owner and mode
each time.

Next to it, `config/branding/` holds the network's branding images (logo,
sign-in background, pane backgrounds) that `branding.images` names; auth
serves them. [docs/branding.md](branding.md) covers branding and email
customization.

Mounts: `admin` mounts `./config` read-write, every other reader (`auth`,
`ergo`, `roleserv`, `quicvoiced`, `directory`, `api`) mounts it read-only. It's the
**directory** that's mounted, not the file: the panel saves by writing a
temp file next to it and renaming it over `boson.yaml`, and a single-file
bind mount would keep showing the old file.

## Precedence

**file > env > built-in default.** A key that's absent (or commented out)
means "use the env value, else the default". Removing a key resets it; the
panel's **Reset to default** does exactly that.

Each service reads only its own section (`auth:`, `branding:`, `irc:`,
`voice:`, `directory:`, `nick_claim:`, `files:`, `admin:`, and the shared `network:`) and ignores keys it
doesn't know. `version: 1` is required at the top.

## Keys (schema v1)

"Live" = applied within seconds of the save; "restart" = the service reports
it in `restart_required` and the panel offers a restart. Permission = what an
admin needs to change it in the panel.

| Key | Type | Default from | Applies | Read by | Permission |
|---|---|---|---|---|---|
| `network.name` | string | `NETWORK_NAME` | live | auth, ergo, directory | `settings.manage` |
| `auth.registration` | `open` \| `invite` \| `closed` | `REGISTRATION` | live | auth | `settings.manage` |
| `auth.require_email_verification` | bool | `REQUIRE_EMAIL_VERIFICATION` | live | auth | `settings.manage` |
| `auth.guest_access` | bool | `true` (`IRC_REQUIRE_SASL`) | live | auth → ergo | `settings.manage` |
| `auth.channel_creation` | `registered` \| `admins` | `IRC_CHANNEL_CREATION` | live | auth → ergo | `settings.manage` |
| `auth.voice_channel_creation` | `admins` \| `founders` \| `registered` | `VOICE_CHANNEL_CREATION` | live | auth → quicvoiced | `settings.manage` |
| `auth.channel_auto_register` | bool | `true` | live | auth → ergo | `settings.manage` |
| `auth.channel_expiry_days` | int, 0–3650 (0 = never) | `0` | live | auth → ergo | `settings.manage` |
| `auth.cors_allowed_origins` | list of origins | `AUTH_CORS_ALLOWED_ORIGINS` | live | auth | `settings.manage` |
| `auth.smtp.host` / `.port` / `.from` / `.tls` | string / int / string / `starttls` \| `tls` \| `none` | `SMTP_HOST` / `SMTP_PORT` / `SMTP_FROM` / `SMTP_TLS` | live | auth | `settings.manage` |
| `auth.email.sender_name` / `.accent` / `.footer` / `.logo` | string / `#rrggbb` / string / bool | the `auth.smtp.from` name / `#3b5bdb` / none / `true` | live | auth | `settings.manage` (Emails page) |
| `auth.email.templates.<verification\|password_reset\|account_setup\|invite>.subject` / `.body` | string with `{{variables}}` | Boson's wording | live | auth | `settings.manage` (Emails page) |
| `branding.theme` | boson-theme/1 without image URLs | Boson's built-in theme | live | auth | `settings.manage` (Branding page) |
| `branding.images.<slot>` | file name in `config/branding/` | none | live | auth | `settings.manage` (Branding page) |
| `irc.plaintext` | bool | `IRC_PLAINTEXT` | **restart** (listener) | ergo | `services.manage` |
| `irc.plaintext_port` | int | `6667` | **restart** (listener) | ergo | hand edit only (compose publishes the container's 6667) |
| `irc.require_sasl_exempt` | list of IPs/CIDRs | `IRC_REQUIRE_SASL_EXEMPT` | live (rehash) | ergo | `services.manage` |
| `irc.roles_poll_seconds` | int | `ROLES_POLL_SECONDS` | live | ergo, roleserv | `services.manage` |
| `irc.settings_poll_seconds` | int | `SETTINGS_POLL_SECONDS` | live | ergo | `services.manage` |
| `irc.websocket` | bool | `IRC_WEBSOCKET` | live (rehash) | ergo | `services.manage` |
| `irc.websocket_origins` | list of origins | `IRC_WEBSOCKET_ORIGINS` | live (rehash) | ergo | `services.manage` |
| `irc.oauthbearer` | bool | `IRC_OAUTHBEARER` | live (rehash) | ergo | `services.manage` |
| `irc.persistent_history` | bool | `IRC_PERSISTENT_HISTORY` | **restart** | ergo | `services.manage` |
| `irc.history_expire` | Ergo duration (`7d`) | built-in | live (rehash) | ergo | `services.manage` |
| `irc.log_level` | `debug`/`info`/`warn`/`error` | built-in | live (rehash) | ergo | `services.manage` |
| `voice.rooms` | list of `{id, name}` | `VOICE_ROOMS` | live | quicvoiced | `settings.manage` |
| `voice.rooms_strict` | bool | `VOICE_ROOMS_STRICT` | live | quicvoiced | `settings.manage` |
| `voice.max_rooms_per_user` | int | `VOICE_ROOMS_MAX_PER_USER` | live | quicvoiced | `settings.manage` |
| `voice.max_rooms`, `voice.max_participants` | int (0 = no limit) | built-in | live | quicvoiced | `settings.manage` |
| `voice.settings_ttl`, `voice.ergo_cache_ttl` | duration | built-in | live | quicvoiced | hand edit |
| `voice.quic_addr`, `voice.http_addr`, `voice.public_http_addr`, `voice.max_datagrams_per_sec`, `voice.max_datagram_payload` | listeners / limits | built-in | **restart** | quicvoiced | hand edit |
| `directory.name` / `.operator` / `.contact` | string | `DIRECTORY_NAME` / `DIRECTORY_OPERATOR` / `DIRECTORY_CONTACT` | live | directory | `directory.manage` |
| `directory.include_main` | bool | `DIRECTORY_INCLUDE_MAIN` | live | directory | `directory.manage` |
| `directory.main_peer_url` | https URL | `DIRECTORY_MAIN_PEER` | live | directory | `directory.manage` |
| `directory.sync_every` | duration, 5m–24h | `10m` | live | directory | `directory.manage` |
| `directory.peers` | list of https URLs | `DIRECTORY_PEERS` | live | directory | `directory.manage` |
| `nick_claim.enabled` | bool | `NICK_CLAIM_ENABLED` (`false`) | live (starts/stops the mailbox worker) | api | `settings.manage` (Nick registration page) |
| `nick_claim.email_domain` | domain | `NICK_CLAIM_EMAIL_DOMAIN` (`DOMAIN`) | live (new claims) | api | `settings.manage` |
| `nick_claim.rate_limit_per_hour` | int | `NICK_CLAIM_RATE_LIMIT_PER_HOUR` (`5`) | live | api | `settings.manage` |
| `nick_claim.inbound.protocol` | `pop3` | `pop3` | live | api | `settings.manage` |
| `nick_claim.inbound.host` / `.port` / `.tls` / `.user` | string / int / bool / string | `POP3_HOST` / `POP3_PORT` (`995`) / `POP3_TLS` (`true`) / `POP3_USER` | live (the worker reconnects) | api | `settings.manage` |
| `files.enabled` | bool | `FILES_ENABLED` (`false`) | live | files, auth (`files_url`) | `settings.manage` (Uploads page) |
| `files.retention`, `files.window` | duration | `72h` | live | files | `settings.manage` |
| `files.account.quota` / `.max_file` | size (`5GB`, `100MB`) | `5GB` / `100MB` | live | files | `settings.manage` |
| `files.guest.enabled` / `.quota` / `.max_file` | bool / size / size | `true` / `500MB` / `25MB` | live | files | `settings.manage` |
| `files.storage.driver` | `local` \| `s3` | `local` | live | files | `settings.manage` |
| `files.storage.s3.endpoint` / `.bucket` / `.region` / `.path_style` / `.public_base_url` | string / string / string / bool / URL | – / – / `auto` / `true` / – | live | files | `settings.manage` |
| `admin.directory_registration` | bool | `ADMIN_DIRECTORY_REGISTRATION` | live | admin | `directory.manage` |
| `admin.directory_hostname` | hostname | `IRC_HOST` (admin's `IRC_CHECK_SERVER_NAME`) | live | admin | `directory.manage` |
| `admin.tls_check_hosts` | list of `host:port[@dial-host:port]`, ≤ 20 | `ADMIN_TLS_CHECK_HOSTS` | live | admin | `services.manage` |

`auth → ergo` means auth owns the value and ergo/quicvoiced get it from
auth's internal settings API as before. Secrets that used to sit next to
these (`SMTP_USER`, `SMTP_PASSWORD`, `NICK_CLAIM_POP3_PASSWORD`, `FILES_S3_ACCESS_KEY` / `FILES_S3_SECRET_KEY`, tokens, keys) stay in `.env` and are
never written to this file. Peers added in the panel's Directory page are
still kept in the directory's peer table (`directory.peers` only seeds it).

## Editing it

**In the admin panel:** Network settings, Branding, Emails, Directory,
Voice, IRC and the admin options write the file; the **Config file** page shows it read-only
with its path. The panel keeps your comments, key order and any keys it
doesn't know, and notes the last save on the `version:` line
(`# last saved in the admin panel by alice at …`). Two admins saving at
once: last write wins (each save re-reads the file and changes only its
keys); the panel's API also takes `If-Match` with the file's ETag.

**By hand:** the file belongs to uid 65532, so

```sh
sudo $EDITOR config/boson.yaml
```

Services notice within a few seconds (they watch the directory, and poll
every 10 s as a fallback). To set a key, add it under its section near the
top (where the panel puts its sections), or uncomment it in the reference
block below; keep the two-space indent.

**A broken file is never used.** If the YAML doesn't parse or a value is
invalid, each service keeps its last good config and logs the error with
its line; the admin panel shows a banner with the message and line, and
won't save over the file until it's fixed.

## Did it apply? `/internal/config/status`

Every service answers on its internal listener (never routed by Traefik):

```json
{"loaded_at": "…", "file_mtime": "…", "ok": true, "error": null, "restart_required": []}
```

`restart_required` lists keys that changed but only take effect after a
restart (e.g. `irc.plaintext`). Ergo has no HTTP server of its own; the
boson-ergo helper reports for it. The api answers on its internal port
(`http://api:3001/internal/config/status`) and adds a `nick_claim` object:
whether it's enabled and the mailbox worker's state
([nick-claim.md](nick-claim.md#is-it-working)). The admin panel polls these after a save
(`CONFIG_STATUS_TARGETS` in compose, overridable with
`ADMIN_CONFIG_STATUS_TARGETS`) and shows per service: applied, needs a
restart (with a **Restart** button through the Docker socket proxy,
`services.manage`, typed confirmation for `admin` and `traefik`), or the
error.

## Existing installs: moving settings into the file

Settings that used to live in auth's database (Network settings) and the
directory's database become read-only once `BOSON_CONFIG` is set: the file
wins, and the panel writes to it. To carry today's values over once:

```sh
git pull                      # config/boson.example.yaml, compose changes
scripts/migrate-config.sh     # creates config/boson.yaml and fills it
docker compose up -d
```

`migrate-config.sh` asks each enabled service for its current settings
(`docker compose run --rm --no-deps -T <service> export-config`, a YAML
fragment of its section built from its database and env; the api prints
`nick_claim:`) and merges them
into the file with the admin image (`admin config merge /config/boson.yaml`,
which keeps comments). Keys already in the file are kept; `--overwrite`
lets the services' values win. Images too old to have `export-config` are
skipped with a warning (their env values stay the defaults). The api is
the exception: an api without `export-config` but with `POP3_HOST` in `.env`
gets `nick_claim.enabled: true` plus its `POP3_*` / `NICK_CLAIM_*` values
written for it, because nick claims are now off unless turned on (the same
happens when `export-config` exists: it runs with `NICK_CLAIM_ENABLED=true`
unless `.env` sets that variable). Without the
step nothing breaks: an absent key falls back to env, but values you only
changed in the panel's database before would revert to their env defaults.

## Without the file

If `BOSON_CONFIG` is unset or the file doesn't exist, every service works as
before: env values, plus auth's and the directory's database settings, and
the admin panel saves Network/Directory settings through their APIs.
