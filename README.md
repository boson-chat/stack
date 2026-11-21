# boson stack

A self-hostable chat server in one `docker compose up`: accounts, IRC,
voice, and automatic TLS, wired together for your own domain.

| Service | Profile | What it does | Ports |
|---|---|---|---|
| `traefik` | always | Reverse proxy and TLS: gets and renews Let's Encrypt certs for every hostname (or serves a local CA's cert for dev), routes `AUTH_HOST` and the IRC WebSocket | 80, 443 |
| `certsync` | always | Exports Traefik's certs (`acme.json`, via traefik-certs-dumper) as files for IRC and voice, owned by each service's uid; they hot-reload | – |
| `ergo` | `irc` | IRC server ([Ergo](https://ergo.chat) 2.19, built with the [account-opers patch](docs/ergo-account-opers.md)) with built-in NickServ/ChanServ, history, always-on clients. Logins are checked against the auth service; network roles become IRC operator classes and vhosts | 6697 (TLS), 6667 (optional), `wss://IRC_HOST/webirc` |
| `roleserv` | `irc` | `/msg RoleServ`: network roles from any IRC client ([docs/roles.md](docs/roles.md)) | – |
| `auth` | `auth` | Accounts ([boson-chat/auth](https://github.com/boson-chat/auth)): sign-up, login, sessions, password reset, signed tokens for voice and IRC | via Traefik at `https://AUTH_HOST` |
| `postgres` | `auth` | Database for `auth` (Postgres 18) | internal |
| `quicvoiced` | `voice` | Voice rooms over QUIC ([boson-chat/quicvoice](https://github.com/boson-chat/quicvoice)); accepts tokens from `auth`. Static channels from `VOICE_ROOMS` plus channels created through the rooms API at `https://VOICE_HOST/v1/rooms` (optionally under an IRC channel, lockable to IRC roles) | 7700/udp, via Traefik at `https://VOICE_HOST` |
| `quicvideod` | `video` | Camera + screen share in voice channels ([boson-chat/quicvideo](https://github.com/boson-chat/quicvideo)): each voice channel's video room. Only quicvoiced's video tickets get in, and quicvoiced keeps its rooms in step with voice. Also runs on its own box ([docs/video.md](docs/video.md)) | 7710/udp (QUIC + WebTransport), via Traefik at `https://VIDEO_HOST` |
| `victoriametrics` | `metrics` | Scrapes `/metrics` of traefik, auth, quicvoiced, admin and the host (`node-exporter`), 90-day retention. Internal only, at `http://victoriametrics:8428` | internal |
| `node-exporter` | `metrics` | Host metrics of the machine the stack runs on (CPU, memory, load, PSI, disks, network, uptime), scraped as job `node`, `instance="host"`; the admin panel's host health. Host network, listening only on the stack network's gateway (see **Host metrics** under [Notes](#notes)) | internal |
| `admin` | `admin` | The admin panel ([boson-chat/admin](https://github.com/boson-chat/admin)): users, invites, sign-in providers, network settings, audit log, stack health, and services control (restart/stop/start, logs); edits [`config/boson.yaml`](docs/config.md) | via Traefik at `https://ADMIN_HOST` |
| `docker-proxy` | `admin` | [Socket proxy](https://github.com/wollomatic/socket-proxy) that is the admin panel's only access to Docker: list/inspect containers, logs, stats, start/stop/restart; nothing else, only from `admin`, on an internal network | internal |
| `directory` | `directory` | Optional: a boson network directory ([boson-chat/directory](https://github.com/boson-chat/directory)): website and paginated `/v1` API where networks list themselves (DNS-verified), federated with other directories; includes directory.boson.chat's listings by default ([docs/directory.md](docs/directory.md)) | via Traefik at `https://DIRECTORY_HOST` |
| `files` | `files` | Optional: uploads ([boson-chat/files](https://github.com/boson-chat/files)) the boson app links in chat, with a rolling per-person quota, guest uploads and expiry; local volume or S3 storage ([docs/files.md](docs/files.md)) | via Traefik at `https://FILES_HOST` |
| `minio`, `minio-init` | `minio` | Optional: an internal S3 bucket for `files` (MinIO), with a bucket-only user for it | internal |
| `webroot` | `webroot` | Optional: serves a host directory at `http://WEBROOT_HOST/.well-known/acme-challenge/` for a certbot outside the stack | via Traefik |
| `webircgateway` | `web` | The browser client's IRC connections ([webircgateway](https://github.com/kiwiirc/webircgateway) with Boson's guards): WebSocket in, TCP/TLS out to any IRC network on IRC ports, never to internal addresses, per-IP caps, WEBIRC to this network's Ergo ([docs/web.md](docs/web.md)) | via Traefik at `wss://WEB_HOST/webirc/websocket/` |
| `wellknown` | `wellknown` | Optional: serves `https://DOMAIN/.well-known/boson.json` (client discovery: IRC, auth and voice endpoints, and the network's branding), generated from `.env`. Only when `DOMAIN` points at this server | via Traefik |

Planned: SSO (OIDC/SAML) in the auth service, video, the admin panel, and the
desktop client configured from your domain.

## Quick start

```sh
scripts/setup.sh            # asks for your domain etc., generates every secret into .env
docker compose up -d --build
```

Before starting on a real server:

1. DNS A records for your IRC, voice and auth hostnames → this server.
2. Open 80/tcp and 443/tcp (certificates, auth, WebSocket), 6697/tcp (IRC), 7700/udp (voice), and 7710/udp with the video profile.
3. SMTP settings in `.env` so the auth service can send verification and
   password-reset email (needed with `REQUIRE_EMAIL_VERIFICATION=true`).
4. Create yourself an admin account:
   `docker compose exec auth ./auth user create --username you --email you@example.com --role admin`

Then sign up at `https://auth.<your domain>` (API: `POST /v1/register`) and
connect any IRC client to `irc.<your domain>:6697` with TLS and SASL.

### Trying it locally

```sh
scripts/setup.sh --local    # *.localhost hostnames, a local CA in ./local-certs
docker compose up -d --build
scripts/smoke-irc.py        # signs up via auth, IRC SASL over TLS, wrong password, OAUTHBEARER
```

`--local` creates a CA and one certificate for `localhost`, `*.localhost`
and the stack's hostnames in `./local-certs/` (gitignored). Traefik serves
it as its default certificate and certsync hands it to Ergo and quicvoiced,
so everything uses the same cert. Trust `local-certs/ca.pem` in your OS or
browser to get rid of warnings. Local mode also turns off email
verification, so sign-ups can log in right away.

## How accounts work

There is one account per person, held by the auth service:

1. **Sign up** at `https://AUTH_HOST` (`POST /v1/register`; with
   `REQUIRE_EMAIL_VERIFICATION=true`, follow the emailed link first).
   `REGISTRATION=invite` or `closed` restricts this.
2. **IRC:** log in with **SASL PLAIN** (username + password), or
   `/msg NickServ IDENTIFY <user> <password>`. Ergo runs `boson-ergo
   auth-script` for every login, which asks the auth service's internal API
   (`POST http://auth:8081/internal/v1/verify-password`, with
   `INTERNAL_API_TOKEN`); on success Ergo creates its side of the account
   on first login. Your nick is your username (reserved once you have logged
   in). NickServ `REGISTER` is disabled; the MOTD points people to the auth
   host. Clients can also use **SASL OAUTHBEARER** with an auth access token
   (`POST /v1/login`): `boson-ergo` verifies it against the auth JWKS
   (EdDSA, `aud: boson`, `typ: at+jwt`, issuer `https://AUTH_HOST`).
3. **Voice:** get a short-lived token with `POST /v1/voice/token` (bearer
   access token); quicvoiced verifies it against
   `http://auth:8080/.well-known/jwks.json` (issuer `https://AUTH_HOST`,
   audience `quicvoice`). Legacy HS256 tokens signed with `VOICE_JWT_SECRET`
   still work while it is set (`docker compose exec quicvoiced ./quicvoiced token --user alice --room general`).
   The same token lists the voice channels and who is in them:
   `GET https://VOICE_HOST/v1/rooms` (see [docs/voice.md](docs/voice.md)).

Ergo keeps channel registrations, always-on state, history and its own
per-account settings; passwords live only in the auth service.

**Network roles are IRC-native** (auth 1.5+, [docs/roles.md](docs/roles.md)):
roles with IRC operator powers (Owner, Admin, Moderator, custom ones with
`irc.*` permissions) make their members IRC operators at login, no `/OPER`;
WHOIS shows the role's line and the hostmask the role's vhost
(`alice!~u@staff/alice`), in any client, mIRC included. `/msg RoleServ HELP`
lists roles and members from IRC (and will manage them, once auth has its
acting API). Ergo is built with a small patch for this
([docs/ergo-account-opers.md](docs/ergo-account-opers.md)). The emergency
`/OPER $OPER_NAME` login (`OPER_PASSWORD_HASH`) stays.

**Network settings from the admin panel** (`config/boson.yaml` `auth:`,
[docs/config.md](docs/config.md); served to the others by auth's
`/internal/v1/settings`) apply at runtime: `channel_creation`
(`registered | admins`), the channel lifecycle settings and `guest_access`
(whether people can use IRC without an account) are polled by `boson-ergo watch-settings` every
`SETTINGS_POLL_SECONDS` (30), which re-renders `ircd.yaml` and rehashes Ergo
on a change; `voice_channel_creation` (`admins | founders | registered`) is
read by quicvoiced (cached 30 s). `guest_voice` (default `false`; auth 1.18+)
lets guests join voice rooms, and so video, under their unverified IRC nick
with a guest badge, while `guest_access` is on: auth mints them a guest voice
token (`POST /v1/voice/guest-token`) and refuses nicks that belong to an
account. See
[docs/irc-channels.md](docs/irc-channels.md) and [docs/voice.md](docs/voice.md).

The auth internal listener (`:8081`: `/internal/v1/*`, `/metrics`) is only
reachable inside the compose network; Traefik routes only the public
listener (`:8080`). Likewise quicvoiced: Traefik routes `https://VOICE_HOST`
to its public API (`:8090`), never to `:8080` (`/metrics`).

## Voice channels and discovery

- `VOICE_ROOMS` (default `general:General`) lists the static voice channels
  as `id:Display Name`, comma-separated, in display order.
  `VOICE_ROOMS_STRICT=true` (default) refuses any room not in the registry.
- Dynamic voice channels: `POST/PATCH/DELETE https://VOICE_HOST/v1/rooms`,
  optionally under an IRC channel and locked to IRC roles (founder, op,
  voice, …) that quicvoiced looks up through Ergo's internal API
  (`ERGO_API_TOKEN`). Stored in the `voice_data` volume. Who may create them
  is the auth setting `voice_channel_creation`. Needs quicvoice >= 1.5.0.
- `https://VOICE_HOST/v1/rooms` returns all of them with their live
  participants and what the caller may do (voice token required).
- The boson client finds a server from its domain via
  `https://DOMAIN/.well-known/boson.json`. If `DOMAIN` points at this
  server, the `wellknown` profile serves it (add `DOMAIN` to `CERT_HOSTS`);
  if your website lives elsewhere, publish the same file there.

Details, the JSON formats and examples: [docs/voice.md](docs/voice.md).

Friends and private 1:1 calls (ringing only while the app is open) run on
`auth` and `quicvoiced` with nothing extra to deploy:
[docs/friends-calls.md](docs/friends-calls.md).
Text channel creation policy, IRC roles and channel locks (exact Ergo
commands): [docs/irc-channels.md](docs/irc-channels.md). Network roles on
IRC, RoleServ, and what the auth service provides for them:
[docs/roles.md](docs/roles.md).

## Profiles

`COMPOSE_PROFILES` in `.env` picks what runs: `irc`, `voice`, `auth`,
`api`, `directory`, `files`, `minio`, `metrics`, `admin`, `webroot`, `wellknown` (traefik and
certsync always run).
`scripts/setup.sh` defaults to `irc,voice,auth`. Examples:

- `voice,auth,metrics` runs accounts, voice and metrics next to an IRC
  server you already have (leave `IRC_HOST` out of `CERT_HOSTS` if that
  server manages its own certificate).
- `irc` without `auth`: Ergo starts, but nobody can log in (there is no
  local registration); run `auth` with it.
- `directory` needs `auth` (its database is in the stack Postgres, and
  owners sign in with the stack's accounts). See
  [docs/directory.md](docs/directory.md).
- `files` needs `auth` (database, sign-in); add `minio` for S3 storage on
  the same host. See [docs/files.md](docs/files.md).

## TLS and certificates

```
Let's Encrypt ──HTTP-01 on :80──▶ traefik ──▶ acme.json (traefik_acme volume)
                                                 │
                    certsync (traefik-certs-dumper, every 30 s)
                                                 │
            certs_irc/fullchain.pem,privkey.pem (uid 10000) ──▶ ergo (SIGHUP rehash)
            certs_voice/tls.crt,tls.key       (uid 65532) ──▶ quicvoiced (mtime reload)
```

- Traefik obtains a certificate for every name in `CERT_HOSTS` (default
  `IRC_HOST,VOICE_HOST,AUTH_HOST`): `traefik/dynamic/stack.yml` declares a
  low-priority router per host on Traefik's no-op service, which is enough
  for the ACME resolver to obtain and renew it, even for IRC and voice,
  which aren't HTTP. HTTP routers (auth, webirc, your own) set `tls: {}`
  and get these certificates by SNI.
- Port 80 redirects to https, except `/.well-known/acme-challenge/`
  (`allowACMEByPass`), so a more specific router (the `webroot` profile) can
  answer challenges for a host Traefik doesn't manage.
- Ergo's entrypoint checks its cert every 30 s and sends itself `SIGHUP`
  when certsync replaces it (Ergo reloads TLS on rehash without dropping
  anyone). quicvoiced re-reads its files when they change.
- `ACME_CA_SERVER` switches to Let's Encrypt staging (or any ACME CA).
  `test/acme/run.sh` runs the whole path against Pebble, Let's Encrypt's
  test CA (CI does too).

## Configuration

Two places:

- **`config/boson.yaml`**: runtime settings (registration, channel rules,
  email, IRC options, voice rooms, the directory's identity, nick claims
  (off by default, [docs/nick-claim.md](docs/nick-claim.md)), the admin
  panel's options). The admin panel edits it, you can too (`sudo`, it
  belongs to uid 65532), and every service reloads it live. A key there wins
  over the matching `.env` variable, which is only its default. See
  [docs/config.md](docs/config.md).
- **`config/branding/`**: the network's branding images (logo, sign-in and
  pane backgrounds) that `config/boson.yaml` `branding:` names. With the
  theme and `auth.email` they brand the client and the account emails:
  [docs/branding.md](docs/branding.md).
- **`.env`** (see `.env.example`): secrets and deployment values (hosts,
  images, ports, tokens). Changing it takes `docker compose up -d`.

**Upgrading an existing install:** run `scripts/migrate-config.sh` once
after pulling, so settings you changed in the admin panel before (stored in
auth's and the directory's databases) move into the file; otherwise they
fall back to their `.env` defaults. Details in [docs/config.md](docs/config.md).

Config files are generated inside the containers at every start, so changing
`.env` and running `docker compose up -d` applies it:

- **Ergo:** `images/ergo/helper/ircd.yaml.tmpl`, rendered by
  `boson-ergo render` (Go template). Server name = `IRC_HOST`, cloaks
  (`<hash>.<network name>`, secret generated in the datastore), history
  (in memory + SQLite, 1 week), always-on and multiclient opt-in, WebSocket
  on `:8097` behind Traefik at `wss://IRC_HOST/webirc` (same host as IRC so
  web clients reuse its certificate and origin rules). Mount your own
  template and set `ERGO_CONFIG_TEMPLATE` to replace it, or `IRC_MOTD_FILE`
  for the MOTD.
- **Traefik:** static config is the `command:` in `compose.yaml`; dynamic
  config comes from Docker labels (only containers with
  `traefik.enable=true` in this project) and `traefik/dynamic/`.

### Install-specific additions

- `traefik/dynamic/*.yml`: your own routers/services/middlewares (Traefik
  file provider, reloaded on change; gitignored except `stack.yml`).
- `compose.override.yaml`: extra services or changes (gitignored; loaded
  automatically unless `COMPOSE_FILE` is set, then list it there).
- `webroot` profile: serves `WEBROOT_DIR` for a host certbot in webroot
  mode (`WEBROOT_HOST`); see `docs/deploy-boson-chat.md`.
- `wellknown` profile: serves `https://DOMAIN/.well-known/boson.json`; see
  `docs/voice.md`.

### Running an unreleased auth service

Releases are pulled from `harbor.floret.dev/boson/auth:<version>`
(`AUTH_IMAGE`). To build from a checkout instead:

```sh
# .env
COMPOSE_FILE=compose.yaml:compose.auth-src.yaml
AUTH_SRC=../auth
```

`scripts/setup.sh` does this by itself when Harbor has no auth release yet
and `../auth` exists.

### Data

Named volumes: `traefik_acme` (certificates), `certs_irc`/`certs_voice`
(exported copies), `ergo_db` (Ergo accounts, channels, history, last
known network settings), `voice_data` (voice channel registry), `pg_data`
(auth, api, directory and files databases), `directory_media` (directory icons and
banners), `files_data` / `minio_data` (uploads), `vm_data` (metrics). Back up `ergo_db`, `voice_data`, `pg_data`,
`directory_media` and your `.env` (`AUTH_KEY_ENCRYPTION_KEY` encrypts the signing
keys in the database).

## Notes

- **UDP buffers:** quicvoiced logs a warning unless the host allows larger UDP
  buffers: `sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000`.
- **Network:** the stack network uses `STACK_SUBNET` (default
  `10.203.53.0/24`) so `auth` (`TRUSTED_PROXIES`) and Ergo
  (`proxy-allowed-from`) trust X-Forwarded-For from Traefik. Pick another
  range if it collides with something on the host.
- **Host metrics (`node-exporter`, `metrics` profile):**
  [node-exporter](https://github.com/prometheus/node_exporter) reports the
  host, not a container: host PID namespace, the host's `/` read-only at
  `/host`, and host networking so the network collectors see the real
  interfaces. Collectors: cpu, meminfo, loadavg, pressure (PSI), filesystem
  (no overlay/tmpfs/Docker mounts), diskstats, netdev (no `lo`, `veth*`,
  `br-*`, `docker*`), netstat, stat (boot time), time, uname, os; everything
  else is off. VictoriaMetrics scrapes it as job `node` with
  `instance="host"`. It is never bound to `0.0.0.0`: it listens on
  `NODE_EXPORTER_ADDR:9100` (default `10.203.53.1`, the gateway of the
  default `STACK_SUBNET`; change both together), an address only containers
  on the stack network route to. Host firewalls that drop incoming traffic
  by default (ufw) also drop it from the stack bridge, so allow it once:
  `sudo ufw allow from 10.203.53.0/24 to 10.203.53.1 port 9100 proto tcp`.
  Until then the `node` target shows as down; nothing else is affected.
  Per-container CPU/memory (cAdvisor) is not included: too heavy for a small
  VPS; the admin panel's services page already shows `docker stats`.
- **Images:** `certsync`, `ergo` and `webircgateway` are built from
  `images/` and published to `harbor.floret.dev/boson/stack-*` by CI on
  every push to `main` (`stack-certsync:latest`, `stack-ergo:2.19.1`,
  `stack-webircgateway:1.1.0-boson.1`, plus `:sha-<commit>`).
- **Compose project name** is `boson`; Traefik only looks at containers of
  that project.
- Deploying the production host (boson.chat): [docs/deploy-boson-chat.md](docs/deploy-boson-chat.md).

## License

Copyright (C) 2026 bludot / the boson authors

Licensed under the [GNU Affero General Public License v3.0](LICENSE) only (`AGPL-3.0-only`). Third-party code keeps its own license; see [NOTICE](NOTICE).

In plain English: if you run a modified version of this software as a network service, you must offer its source code to the people who use that service.
