# Deploying boson.chat (production host)

Host: `5.78.80.243` (Hetzner, Ubuntu), `ssh boson` (root). Hostnames:
`irc.boson.chat`, `voice.boson.chat`, and new: `auth.boson.chat`.

**Nothing in this document has been run yet.** It is the plan for two
separate maintenance windows:

1. [Upgrade the stack to Traefik and add auth + metrics](#phase-1-traefik-auth-metrics-legacy-irc-stays),
   keeping the legacy IRC server as it is.
2. Later: [move irc.boson.chat from UnrealIRCd + Anope to Ergo](#phase-2-irc-on-ergo).

## Current state (before phase 1)

- This repo's stack (Caddy version) runs with `COMPOSE_PROFILES=voice`:
  `caddy` (80/443, cert for `voice.boson.chat`), `certsync`, `quicvoiced`
  (7700/udp, HS256 tokens from `VOICE_JWT_SECRET`).
- A `caddy/sites/*.caddy` snippet serves `http://irc.boson.chat/.well-known/acme-challenge/*`
  from the host directory `/var/www/certbot`, where **host certbot (webroot
  mode)** renews the certificate for the legacy IRC server.
- **Legacy IRC** runs outside the stack: containers `unrealircd` and
  `anope`, host networking, ports 6667/6697. certbot's deploy hook copies
  the renewed cert into UnrealIRCd's config and runs `unrealircdctl reloadtls`.

Find the stack checkout first (the commands below run there):

```sh
STACK=$(docker inspect boson-caddy-1 --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}')
cd "$STACK" && git status && grep -v -E 'PASSWORD|SECRET|KEY|TOKEN|HASH' .env
ls caddy/sites/ compose.override.yaml 2>/dev/null
```

## Phase 1: Traefik, auth, metrics (legacy IRC stays)

Result: Traefik on 80/443 with certs for `voice.boson.chat` and
`auth.boson.chat`; `auth` + `postgres`; `victoriametrics`; quicvoiced
accepting auth-service tokens (and the old HS256 ones); certbot for
`irc.boson.chat` keeps working through the `webroot` profile. The legacy
IRC containers are not touched.

Voice is down for about a minute (between `down` and `up`). Auth is new.

### Before the window

1. **DNS:** `auth.boson.chat` A record → `5.78.80.243` (check:
   `dig +short auth.boson.chat`). Traefik needs it to get the certificate.
2. **SMTP** credentials for verification / password-reset mail
   (`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM`,
   `SMTP_TLS`). Without them, set `REQUIRE_EMAIL_VERIFICATION=false` for
   now, since nobody could verify.
3. The stack subnet must be free on the host:
   `ip route | grep 10.203.53. ; docker network ls`. (Otherwise set
   `STACK_SUBNET` to another free /24.)

### Steps

```sh
cd "$STACK"

# 0. Backups (Caddy's certs and the current config; nothing is deleted below)
cp .env ".env.caddy-$(date +%F)"
docker run --rm -v boson_caddy_data:/d:ro -v "$PWD":/b alpine tar czf "/b/caddy_data-$(date +%F).tgz" -C /d .
mkdir -p ../stack-caddy-leftovers && cp -a caddy/sites compose.override.yaml ../stack-caddy-leftovers/ 2>/dev/null || true

# 1. Code
git fetch origin && git checkout main && git pull --ff-only

# 2. Remove the Caddy-only leftovers (untracked files survive the checkout):
#    the caddy/sites snippet, and compose.override.yaml if it only existed to
#    mount /var/www/certbot into caddy (it would now reference a missing service).
rm -rf caddy
[ -f compose.override.yaml ] && grep -q caddy compose.override.yaml && mv compose.override.yaml ../stack-caddy-leftovers/compose.override.yaml.caddy
```

3. **Edit `.env`.** Remove `SERVICES_HOST`, `SERVICES_LINK_PASSWORD`,
   `CLOAK_KEY1..3` (unused now) and any `QUICVOICE_IMAGE=ghcr.io/...:1.1.0`
   line (1.1.0 can't verify auth tokens; the compose default is
   `harbor.floret.dev/boson/quicvoice:1.7.0`). Keep `VOICE_JWT_SECRET`,
   `TLS_ISSUER` (the Let's Encrypt email) and the existing values. Set:

   ```sh
   COMPOSE_PROFILES=voice,auth,metrics,webroot
   DOMAIN=boson.chat
   IRC_HOST=irc.boson.chat
   VOICE_HOST=voice.boson.chat
   AUTH_HOST=auth.boson.chat
   ADMIN_HOST=admin.boson.chat
   # irc.boson.chat's cert stays with host certbot until phase 2:
   CERT_HOSTS=voice.boson.chat,auth.boson.chat
   # certbot webroot for the legacy IRC cert, served by the stack:
   WEBROOT_HOST=irc.boson.chat
   WEBROOT_DIR=/var/www/certbot
   REGISTRATION=open          # or invite, see "Before phase 2" below
   REQUIRE_EMAIL_VERIFICATION=true
   SMTP_HOST=...              # see "Before the window"
   SMTP_PORT=587
   SMTP_USER=...
   SMTP_PASSWORD=...
   SMTP_FROM=boson <no-reply@boson.chat>
   SMTP_TLS=starttls
   ```

   and append the new secrets (generated on the host, never printed):

   ```sh
   umask 077
   {
     echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)"
     echo "AUTH_KEY_ENCRYPTION_KEY=$(openssl rand -base64 32)"
     echo "INTERNAL_API_TOKEN=$(openssl rand -hex 32)"
   } >> .env
   ```

   Keep a copy of `AUTH_KEY_ENCRYPTION_KEY` somewhere safe (password
   manager): without it the auth database's signing keys are unusable.

4. **Switch over** (voice blips here; legacy IRC is a separate set of containers and keeps running):

   ```sh
   docker compose pull
   docker compose build
   docker compose down              # stops caddy, certsync, quicvoiced (no -v: volumes stay)
   docker compose up -d
   docker compose ps
   ```

5. **Check:**

   ```sh
   docker compose logs traefik | grep -iE 'certificate|error'      # "Server responded with a certificate" x2
   docker compose logs certsync                                     # "updated voice.boson.chat -> /certs/voice"
   curl -fsS https://auth.boson.chat/readyz && curl -fsS https://auth.boson.chat/v1/config
   curl -sI http://voice.boson.chat/ | head -1                      # 301 to https
   # certbot's webroot through Traefik:
   echo ok > /var/www/certbot/.well-known/acme-challenge/stack-test
   curl -fsS http://irc.boson.chat/.well-known/acme-challenge/stack-test && rm /var/www/certbot/.well-known/acme-challenge/stack-test
   certbot renew --dry-run --cert-name irc.boson.chat
   # metrics
   docker compose exec victoriametrics wget -qO- 'http://127.0.0.1:8428/api/v1/query?query=up'
   ```

   The webroot container runs as `nobody`, so challenge files must be world
   readable (certbot's default 0644 files / 0755 dirs are fine).

6. **Admin account:**
   `docker compose exec auth ./auth user create --username <you> --email <you>@boson.chat --role admin`

7. **Voice clients:** tokens now come from `POST https://auth.boson.chat/v1/voice/token`
   (with a bearer access token from `/v1/login`). Old HS256 tokens keep
   working as long as `VOICE_JWT_SECRET` is set.

### Rollback (phase 1)

```sh
docker compose down
git checkout 97235f3                         # last Caddy commit
cp .env.caddy-YYYY-MM-DD .env
cp -a ../stack-caddy-leftovers/sites caddy/ && cp ../stack-caddy-leftovers/compose.override.yaml* . 2>/dev/null
docker compose up -d --build
```

Caddy's volumes were never removed, so its certificates are still there.
Once phase 1 has run for a while: `docker volume rm boson_caddy_data boson_caddy_config`.

## Phase 2: IRC on Ergo

Result: `irc.boson.chat` served by the stack's Ergo (6697 TLS, optional
6667, `wss://irc.boson.chat/webirc`), certificate from Traefik instead of
host certbot, logins against the auth service, Anope's accounts and channels
imported. UnrealIRCd, Anope and the IRC certbot cert are retired.

**Downtime:** IRC is down from stopping UnrealIRCd until Ergo listens:
about 5 minutes (the import takes seconds for a small Anope DB, Ergo starts
in a few seconds); plan a 30-minute window for checks and rollback. Every
client reconnects; voice and auth stay up.

### How Anope accounts carry over

Ergo checks a login with the auth service first; if the auth service says
no (or doesn't know the user), Ergo falls back to its own database, which
is where imported Anope accounts and their password hashes live (Ergo
understands Anope's `enc_bcrypt`, `enc_sha256`, `md5`, `sha1`, plain). So
after the import, old NickServ passwords keep working on IRC.

**The catch:** if someone registers an imported name in the auth service
(say `alice`, which belonged to a different Anope user), the auth service
accepts their password and Ergo logs them into the imported `alice`
account, with its channel founder rights. So every imported name **must
exist in the auth service before it is importable by anyone else**:
reserve them as password-less accounts with the Anope email; the real owner
claims theirs with "forgot password" (mail goes to the Anope address and
also marks it verified). Until then their old password works via the
fallback. (Verified locally: an imported account plus a later auth
registration of the same name is a takeover; a reserved name gets
`409 username_taken` on registration and SASL falls back to the Anope hash.)

### Before phase 2

1. **Close the window for name squatting** between phase 1 and phase 2:
   either run phase 1 with `REGISTRATION=invite`, or reserve the Anope
   names right after phase 1 (steps 2-4 below on a fresh export) and again
   just before phase 2 for names registered in between.
2. **Export Anope's DB** (flush it first with `/msg OperServ UPDATE` as a
   services oper, or stop and start anope):

   ```sh
   docker inspect anope --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{"\n"}}{{end}}'
   docker cp anope:<data dir>/anope.db /root/anope-$(date +%F).db       # data dir from the mounts above
   ```

3. **Convert** with Ergo's tool from the same release as the image:

   ```sh
   curl -fsSLo /root/anope2json.py https://raw.githubusercontent.com/ergochat/ergo/v2.19.1/distrib/anope/anope2json.py
   python3 /root/anope2json.py /root/anope-$(date +%F).db /root/anope.json
   ```

4. **Find collisions and reserve names** in the auth service:

   ```sh
   cd "$STACK"
   # Names that already exist in auth (registered since phase 1): resolve by hand
   # (same person? keep; different person? rename one side before importing).
   docker compose exec -T auth ./auth user list </dev/null > /root/auth-users.txt
   # Reserve every Anope account (no password; email marked verified):
   python3 -c 'import json,sys
   for u in json.load(open(sys.argv[1]))["users"].values(): print(u["name"], u.get("email") or "")' /root/anope.json |
   while read -r name email; do
     docker compose exec -T auth ./auth user create --username "$name" ${email:+--email "$email"} --no-password </dev/null \
       || echo "$name $email" >> /root/reserve-failed.txt
   done
   ```

   Check `/root/reserve-failed.txt`: names that already exist (fine if it's
   the same person), names the auth service rejects (IRC nick rules; rename
   in `anope.json` or drop them), duplicate emails (create without
   `--email`; that user can't use "forgot password" and keeps the Anope
   password via the fallback until an admin sorts it out).

5. **Tell users:** sign in with the same nick and password as before; to
   set a new password, or to use voice and the web, use "forgot password"
   at auth.boson.chat with your NickServ email. New users sign up at
   auth.boson.chat; `/msg NickServ REGISTER` no longer exists.

### Cutover

```sh
cd "$STACK"
cp .env ".env.phase1-$(date +%F)"
```

`.env` changes:

```sh
COMPOSE_PROFILES=irc,voice,auth,metrics        # webroot dropped
CERT_HOSTS=irc.boson.chat,voice.boson.chat,auth.boson.chat
OPER_NAME=<oper name>
OPER_PASSWORD_HASH='<from: printf "%s\n" "<password>" | docker run --rm -i --entrypoint /ircd-bin/ergo ghcr.io/ergochat/ergo:v2.19.1 genpasswd>'
# The legacy server offered plaintext on 6667; keep it public only if needed:
IRC_PLAINTEXT=true
IRC_PLAINTEXT_PORT=6667
```

```sh
# 1. Traefik takes over irc.boson.chat's certificate (HTTP-01 now answered by Traefik)
docker compose up -d traefik certsync
docker compose logs -f traefik certsync      # wait for "updated irc.boson.chat -> /certs/irc"

# 2. Final Anope export (steps 2-4 of "Before phase 2" again if anything changed), then stop legacy IRC
docker stop unrealircd anope                  # IRC downtime starts

# 3. Import into a new Ergo datastore, then start Ergo
docker compose build ergo                     # or: docker compose pull ergo
docker compose run --rm --no-deps -v /root/anope.json:/import/anope.json:ro \
  -e ERGO_IMPORT_JSON=/import/anope.json ergo true
docker compose up -d                          # IRC back (6697, 6667)
```

Check:

```sh
docker compose logs ergo | tail -20
echo | openssl s_client -connect irc.boson.chat:6697 -servername irc.boson.chat 2>/dev/null | openssl x509 -noout -issuer -enddate
# then with a client: SASL as an imported user (old password) and as a new auth user;
# /msg ChanServ INFO #<channel> for an imported channel
```

Clean up afterwards: disable host certbot for the IRC name
(`certbot delete --cert-name irc.boson.chat`, remove the deploy hook
`/etc/letsencrypt/renewal-hooks/deploy/irc-boson-chat.sh` and
`check-irc-cert.timer`), remove `WEBROOT_*` from `.env`, and after a week
`docker rm unrealircd anope`.

### Differences users and opers will notice

- **Cloaks change.** UnrealIRCd's `Boson-XXXX.ip`-style cloaks become
  Ergo's `<hash>.boson`; the secret is generated in Ergo's datastore. Bans
  on old cloaks don't match anymore.
- **Bans aren't imported:** UnrealIRCd TKLs (G/K/Z-lines) and Anope AKILLs
  need re-adding with Ergo's `UBAN ADD` / `KLINE` / `DLINE`.
- **Opers:** staff are IRC operators through their network roles (auth
  1.5+, see [roles.md](roles.md)): opered up at login, no `/OPER`. One
  emergency `/OPER` login stays, from `OPER_NAME`/`OPER_PASSWORD_HASH`
  (bcrypt via `ergo genpasswd`, class `boson.server-admin`: kill, ban,
  sajoin, samode, vhosts, rehash, accreg, chanreg, ...). Unreal's operclasses, snomask
  names and `/OPER` mask restrictions don't carry over; there is no
  OperServ (Ergo's equivalents are server commands: `UBAN`, `DEFCON`,
  `SAMODE`, `NS SAREGISTER`, `CS ...`). Opers also need an auth account (or
  their imported one) to identify.
- **Services:** NickServ and ChanServ are built in (`/msg NickServ HELP`).
  Anope's MemoServ, BotServ, OperServ and HostServ data aren't imported;
  vhosts are re-added with `/msg HostServ SET`. Channel founders, access
  levels (as channel modes), topics and modes are imported.
- **Nick grouping:** Anope's grouped nicks become Ergo "additional nicks"
  on the account. With `force-nick-equals-account`, a logged-in user's
  nick is their account name.
- **New:** always-on (`/msg NickServ SET always-on true`), history playback
  (CHATHISTORY), multiclient, `wss://irc.boson.chat/webirc` for web clients.

### Rollback (phase 2)

```sh
docker compose stop ergo
cp .env.phase1-YYYY-MM-DD .env
docker compose up -d                  # webroot profile back, irc host out of CERT_HOSTS
docker start unrealircd anope
```

certbot's last certificate for irc.boson.chat is still in
`/etc/letsencrypt` (until you delete it), so UnrealIRCd starts with it.
Ergo's datastore stays in the `boson_ergo_db` volume for the next attempt
(remove the volume to re-import).

## Voice channels and the room list API

Needs the quicvoice release with `/v1/rooms` (1.4.0, the compose default
from this change on) and phase 1 (Traefik). Voice blips for a
few seconds while quicvoiced restarts.

`.env` (keep `CERT_HOSTS` as it is: `voice.boson.chat` already has its
certificate, which Traefik now also uses for `https://voice.boson.chat`):

```sh
VOICE_ROOMS=general:General
#VOICE_ROOMS_STRICT=true     # default; the old ad-hoc room names (e.g. "lobby") stop working
```

Remove any `QUICVOICE_IMAGE=` override older than 1.4.0. Don't add the
`wellknown` profile: `boson.chat` is the website on Cloudflare, which serves
`/.well-known/boson.json` itself (boson-chat/website `public/`).

```sh
cd "$STACK" && git pull --ff-only
docker compose pull quicvoiced && docker compose up -d
docker compose logs quicvoiced | grep -E 'voice rooms configured|public HTTP listening'
curl -s -o /dev/null -w '%{http_code}\n' https://voice.boson.chat/v1/rooms          # 401
curl -s -o /dev/null -w '%{http_code}\n' https://voice.boson.chat/metrics           # 404
curl -fsS https://boson.chat/.well-known/boson.json                                  # from the website
```

and with a voice token (see [voice.md](voice.md#room-list-api)):
`curl -fsS https://voice.boson.chat/v1/rooms -H "Authorization: Bearer $VOICE"`.

## Host metrics (node-exporter)

The `metrics` profile includes `node-exporter` (VPS health in the admin
panel: job `node`, `instance="host"`). It listens only on the stack
network's gateway, and ufw (22, 80, 443, 6667, 6697, 7700/udp) drops traffic
from the stack bridge to the host too, so 9100 has to be open to the stack
subnet only (nothing on the public interface changes).

This is automatic now: with ufw active, the installer (`install.sh`,
get.boson.chat) adds that rule, from `STACK_SUBNET` to `NODE_EXPORTER_ADDR`
(`.env`; default 10.203.53.0/24 and 10.203.53.1), once, with the comment
`boson: metrics -> node-exporter`, and prints one line saying so. On an
install that already runs, only that step:

```sh
curl -fsSL https://get.boson.chat | sudo sh -s -- --firewall
```

The setup wizard's Firewall step also tests the path from a container on the
stack network and shows the exact command when it's dropped. By hand (the
fallback, or a firewall other than ufw):

```sh
cd "$STACK"
grep -E '^(STACK_SUBNET|NODE_EXPORTER_ADDR)=' .env   # none: 10.203.53.0/24 and 10.203.53.1
docker network inspect boson_default -f '{{range .IPAM.Config}}{{.Subnet}} gw {{.Gateway}}{{end}}'
sudo ufw allow from 10.203.53.0/24 to 10.203.53.1 port 9100 proto tcp comment 'boson: metrics -> node-exporter'
git pull && docker compose up -d     # starts node-exporter, recreates victoriametrics (extra_hosts)
curl -s -m 3 http://<public IP>:9100/ ; echo "exit $?"   # from outside: must fail
docker compose exec victoriametrics wget -qO- 'http://127.0.0.1:8428/api/v1/query?query=up{job="node"}'   # value "1" within ~30 s
```

## Open questions

- **auth rate limits for SASL:** auth 1.0.0 ignores `X-Forwarded-For` on its
  internal listener, so every IRC login shares Ergo's IP for the per-IP limit
  (`RATE_LIMIT_IP_BURST=20`, `RATE_LIMIT_IP_PER_MINUTE=10` for
  `verify-password`). A reconnect storm after an Ergo restart could get
  logins throttled (Ergo logs `auth service rate limit hit`). Fix in auth:
  honour `X-Forwarded-For` from `TRUSTED_PROXIES` on the internal listener
  (boson-ergo already sends it), or exempt internal callers from the IP
  bucket and keep the per-account one.
- **SSO-only users and IRC:** password-less (SSO) accounts can only use
  OAUTHBEARER with a short-lived access token today; most IRC clients
  can't refresh it. App passwords in auth would cover them.
