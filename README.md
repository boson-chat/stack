# boson stack

A self-hostable chat server in one `docker compose up`: accounts, IRC,
voice, and automatic TLS, wired together for your own domain.

| Service | Profile | What it does | Ports |
|---|---|---|---|
| `traefik` | always | Reverse proxy and TLS: gets and renews Let's Encrypt certs for every hostname (or serves a local CA's cert for dev), routes `AUTH_HOST` and the IRC WebSocket | 80, 443 |
| `certsync` | always | Exports Traefik's certs (`acme.json`, via traefik-certs-dumper) as files for IRC and voice, owned by each service's uid; they hot-reload | – |
| `ergo` | `irc` | IRC server ([Ergo](https://ergo.chat) 2.19) with built-in NickServ/ChanServ, history, always-on clients. Logins are checked against the auth service | 6697 (TLS), 6667 (optional), `wss://IRC_HOST/webirc` |
| `auth` | `auth` | Accounts ([boson-chat/auth](https://github.com/boson-chat/auth)): sign-up, login, sessions, password reset, signed tokens for voice and IRC | via Traefik at `https://AUTH_HOST` |
| `postgres` | `auth` | Database for `auth` (Postgres 18) | internal |
| `quicvoiced` | `voice` | Voice rooms over QUIC ([boson-chat/quicvoice](https://github.com/boson-chat/quicvoice)); accepts tokens from `auth` | 7700/udp |
| `victoriametrics` | `metrics` | Scrapes `/metrics` of traefik, auth, quicvoiced (and admin), 90-day retention. Internal only, at `http://victoriametrics:8428` | internal |
| `admin` | `admin` | **Slot for the upcoming admin panel** (not released yet; off by default) | via Traefik at `https://ADMIN_HOST` |
| `webroot` | `webroot` | Optional: serves a host directory at `http://WEBROOT_HOST/.well-known/acme-challenge/` for a certbot outside the stack | via Traefik |

Planned: SSO (OIDC/SAML) in the auth service, video, the admin panel, and the
desktop client configured from your domain.

## Quick start

```sh
scripts/setup.sh            # asks for your domain etc., generates every secret into .env
docker compose up -d --build
```

Before starting on a real server:

1. DNS A records for your IRC, voice and auth hostnames → this server.
2. Open 80/tcp and 443/tcp (certificates, auth, WebSocket), 6697/tcp (IRC), 7700/udp (voice).
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
   still work while it is set (`docker compose exec quicvoiced ./quicvoiced token --user alice --room lobby`).

Ergo keeps channel registrations, always-on state, history and its own
per-account settings; passwords live only in the auth service. Ergo's IRC
operator account is separate (`OPER_NAME` / `OPER_PASSWORD_HASH`, `/OPER`).

The auth internal listener (`:8081`: `/internal/v1/*`, `/metrics`) is only
reachable inside the compose network; Traefik routes only the public
listener (`:8080`).

## Profiles

`COMPOSE_PROFILES` in `.env` picks what runs: `irc`, `voice`, `auth`,
`metrics`, `admin`, `webroot` (traefik and certsync always run).
`scripts/setup.sh` defaults to `irc,voice,auth`. Examples:

- `voice,auth,metrics` runs accounts, voice and metrics next to an IRC
  server you already have (leave `IRC_HOST` out of `CERT_HOSTS` if that
  server manages its own certificate).
- `irc` without `auth`: Ergo starts, but nobody can log in (there is no
  local registration); run `auth` with it.

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

Everything lives in `.env` (see `.env.example`). Config files are generated
inside the containers at every start, so changing `.env` and running
`docker compose up -d` applies it:

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
(exported copies), `ergo_db` (Ergo accounts, channels, history),
`pg_data` (auth database), `vm_data` (metrics). Back up `ergo_db`,
`pg_data` and your `.env` (`AUTH_KEY_ENCRYPTION_KEY` encrypts the signing
keys in the database).

## Notes

- **UDP buffers:** quicvoiced logs a warning unless the host allows larger UDP
  buffers: `sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000`.
- **Network:** the stack network uses `STACK_SUBNET` (default
  `10.203.53.0/24`) so `auth` (`TRUSTED_PROXIES`) and Ergo
  (`proxy-allowed-from`) trust X-Forwarded-For from Traefik. Pick another
  range if it collides with something on the host.
- **Images:** `certsync` and `ergo` are built from `images/` and published to
  `harbor.floret.dev/boson/stack-*` by CI on every push to `main`
  (`stack-certsync:latest`, `stack-ergo:2.19.1`, plus `:sha-<commit>`).
- **Compose project name** is `boson`; Traefik only looks at containers of
  that project.
- Deploying the production host (boson.chat): [docs/deploy-boson-chat.md](docs/deploy-boson-chat.md).
