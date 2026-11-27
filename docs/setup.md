# Setup wizard (`install.sh` + `stack-setup`)

The quickest way onto a fresh Linux server:

```sh
curl -fsSL https://get.boson.chat | sudo sh
```

```
Boson setup is running.

  Open https://203.0.113.5:8443 and enter code 7KQ4-MXR2
```

`install.sh` checks the OS (Linux, amd64/arm64; Ubuntu, Debian, Fedora and
RHEL-likes are tested, others ask first), installs Docker with Docker's
official convenience script if it's missing (it asks; `--yes` doesn't), and
starts the wizard: the `boson-setup` container from
`harbor.floret.dev/boson/stack-setup`. Options: `--yes`, `--dir`
(default `/opt/boson-stack`), `--image`, `--port` (default 8443),
`--no-pull`, `--firewall` (only the firewall step below, then exit).

With ufw active (and the metrics profile on, as in every install the wizard
makes), it also allows the stack network to reach node-exporter: one rule,
`ufw allow from STACK_SUBNET to NODE_EXPORTER_ADDR port 9100 proto tcp comment
'boson: metrics -> node-exporter'` (from `.env`; default 10.203.53.0/24 and
10.203.53.1), added once, and one line saying what it did. Without it ufw's
default-deny INPUT drops the scrapes and the admin panel's Service health
shows job `node` down. The setup wizard and the admin panel's updates run in
containers and can't change the host firewall, so an install that already
runs gets it with `curl -fsSL https://get.boson.chat | sudo sh -s -- --firewall`.
`test/install/ufw.sh` tests it against a fake `ufw`.

## The wizard

A web page on `https://<server>:8443` with a self-signed certificate
(`install.sh` prints its SHA-256 fingerprint). Steps:

| Step | What happens |
|---|---|
| Domain | Network name and domain. Every hostname the chosen features need (`irc.`, `auth.`, `chat.`, `admin.`, `api.`, plus `voice.`, `video.`, `files.`, `directory.`) is looked up live (A and AAAA, every 5 s). An address counts as this server when it's one of the host's own, or when the wizard answers on it (`/.well-known/boson-setup`, for NAT'd clouds). A name pointing elsewhere blocks the step, and the install. **App discovery** (on by default): the `wellknown` profile serves `https://DOMAIN/.well-known/boson.json`, so `DOMAIN` itself goes in the DNS check and `CERT_HOSTS`. The Boson app finds a server's accounts, voice, uploads and federation through that file (on the domain of `irc.DOMAIN` without its first label); without it the app sees plain IRC. Turn it off only when `DOMAIN` is your website on another server: the step then shows the JSON to publish there yourself ([federation.md](federation.md), "Which domain", for the `federation` section). |
| TLS | Traefik's ACME resolver, as with `scripts/setup.sh`: Let's Encrypt, its staging CA, or any ACME directory (`ACME_CA_SERVER`). A switch for **"This server already runs a reverse proxy on ports 80 and 443"**: Boson goes behind it ([reverse-proxy.md](reverse-proxy.md)), set up on the ports step. |
| Admin | Username, email, password. Created after the stack is up with `auth user create --role owner` (the Owner network role, [roles.md](roles.md)). |
| Features | Voice, video, uploads (this disk, MinIO, or an S3 bucket), the Boson directory listing and/or a directory of your own, sign-up (open / invite / closed), guests (and guests in voice), email verification, accounts from other Boson servers ([federated login](federation.md): verified servers by default, or only servers you list, any server, or off; `auth.federation.mode`/`.allow`; on the add-on path too, [federation.md](federation.md#add-on-installs)), SMTP with a test-send button, and **Let the admin panel apply updates** (on by default; the `updates` profile and `ADMIN_UPDATES_DOCKER_PROXY_URL`, [updates.md](updates.md#applying-updates)): it gives the panel root-equivalent access to the host for the people with `services.manage` + `settings.manage`, which the step says. Off, the Updates page lists updates and you apply them on the server. Then **Usage & health stats**: why we collect them, and the switch (on by default; off writes `TELEMETRY=off`, [telemetry.md](telemetry.md#how-to-opt-out)). |
| Firewall and ports | 80, 443, 6697/tcp, 7700/udp (voice), 7710/udp (video). From inside: is another program on the port; does a connection from a container on Docker's network reach it (that path crosses the host's own firewall: iptables, nftables, ufw). A dropped UDP port is a warning with a "turn voice off" button. Host metrics: from the probe container on the stack network (`STACK_SUBNET`), does a connection reach node-exporter's address (`NODE_EXPORTER_ADDR:9100`, the stack bridge's gateway; the wizard listens there until the stack runs)? A firewall that drops it (ufw's default-deny INPUT) is a warning with the exact command, `sudo ufw allow from 10.203.53.0/24 to 10.203.53.1 port 9100 proto tcp comment 'boson: metrics -> node-exporter'` (install.sh already adds it when ufw is active). What can't be checked from inside, a provider's firewall, is said so. When 80 or 443 is taken it offers **"I already run a reverse proxy on this server"** (also a link under the results, and the TLS step's switch): which proxy (Traefik, nginx, Caddy, HAProxy, other), where Boson's Traefik listens (`TRAEFIK_HTTP_PUBLISH` / `TRAEFIK_HTTPS_PUBLISH`, default `127.0.0.1:8081` / `127.0.0.1:8444`; for a proxy container on 80/443 it finds the gateway of its Docker network and offers that), and whose PROXY protocol header to believe (`TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS`, default the stack network's gateway, `10.203.53.1`). 80/443 then only need to be in use (by the proxy), Boson's two ports are checked instead (free, an address this server has; not probed from outside), and the step shows the snippets for the proxy with the install's names. See [reverse-proxy.md](reverse-proxy.md). |
| Review | The `.env` lines and `config/boson.yaml` keys it will write, secrets masked (each says whether it is new, kept from your `.env`, or as you entered it), whether the panel applies updates, and the **versions** it installs (see [Versions](#versions)). |
| Install | Writes the stack's files, `.env` (0600) and `config/boson.yaml` into the install dir; `docker compose pull`, `up -d`; waits for every service to be healthy (one-shot services to exit 0), for every certificate, creates the admin, checks each public URL, that `https://DOMAIN/.well-known/boson.json` names this server's IRC and auth (and has the `federation` section unless federation is off) when app discovery is on, and IRC on 6697. Progress streams live (Server-Sent Events). A failed step shows its error and a **Retry**. While services start, each row of the services list has a **Logs** panel (see [Why is a service still starting?](#why-is-a-service-still-starting)). |
| Done | Links to the web client and the admin panel; the emergency `/OPER` password, once. **Close setup** switches the wizard off (also after 30 minutes on its own): the container exits. |

### Security

- The code (`XXXX-XXXX`, from `crypto/rand`) is redeemed once for an
  HttpOnly, Secure, SameSite=Strict session cookie; every API call needs
  that session, a second browser can't use the code again. Wrong codes:
  5 per IP per 5 minutes, 30 in all per 10 minutes. The code isn't logged:
  `install.sh` reads it with `docker exec boson-setup stack-setup code`.
- Writes must be JSON from the same origin (CSP, no framing).
- Secrets are generated with `crypto/rand`, kept only in `.env` (0600);
  the wizard's state file (`<dir>/.setup/state.json`, 0600) has none.
  Output from compose shown on the page has every secret value redacted,
  and so do the services' logs and healthcheck output (Logs panels).

### Why is a service still starting?

On the Install page every service in the list has a **Logs** button. It
opens the container's last 100 log lines (refreshed every 3 s, kept scrolled
to the end unless you scroll up, with a **Copy** button) and, while its
Docker healthcheck isn't passing, the last healthcheck results
(`docker inspect` `.State.Health.Log`). That matters for a container that
logs nothing while it waits: evrd's entrypoint waits for its certificate
before it starts, so its log is empty while its healthcheck says
`wget: can't connect to remote host (127.0.0.1): Connection refused`.

A row also says what the service waits for when that's known: ergo, evrd,
quicvoiced and quicvideod need their TLS certificate (Traefik gets it from
the ACME CA, certsync copies it in). While services wait, and while the
certificates step runs, the wizard reads Traefik's log every few seconds;
ACME errors (`Unable to obtain ACME certificate …`, e.g. a CA it can't
verify, or a challenge that can't reach port 80) show as a warning on the
step, *Traefik can't get certificates: …*, pointing at the TLS step. A
health step that times out with that warning says so in its error.

The API is `GET /api/flows/<flow>/install/services/<service>/logs?tail=N`
(the session cookie, like every call). Only a service with a container in
this install's compose project is answered (the name is checked against
`docker compose ps`; the healthcheck is read from the container ID compose
gives), every line has the install's secrets redacted like the compose
output, and it is capped: 200 lines, 2 KiB per line, 64 KiB in all, the
last 3 healthcheck results, 4 requests at a time.

### Versions

A new install gets the **newest release** of every component it runs
(`AUTH_IMAGE`, `API_IMAGE`, `WEB_IMAGE`, `ADMIN_IMAGE`, `QUICVOICE_IMAGE`,
`QUICVIDEO_IMAGE`, `FILES_IMAGE`, `DIRECTORY_IMAGE`, and on the add-on path
`EVRD_IMAGE` and `EVRD_LINKS_IMAGE`), written to `.env`, so the admin
panel's Updates page has nothing to offer right after the install. The rules
are the Updates page's (boson-chat/admin `internal/services/updates`):
GitHub Releases of the component's repository, semver tags, drafts left
out, and only a version whose image the registry (harbor.floret.dev, or
ghcr.io for evrd) has. On top of that the wizard stays in the major version
of `compose.yaml`'s pinned default (a new major version can need a stack
change) and never goes below the default. It asks GitHub once per repository
per run (about ten requests; unauthenticated GitHub allows 60 an hour per
address; `BOSON_SETUP_GITHUB_TOKEN`, a token with no scopes, passed through
`install.sh`, raises that) and the registry once per tag. When GitHub or the
registry can't be asked, the Review step says so and the install uses
`compose.yaml`'s pinned defaults; the Updates page offers anything newer
afterwards.

**A re-run never upgrades**: an image already in `.env` is kept, and a
component that runs without a pin in `.env` (an install from before this)
stays on `compose.yaml`'s default. Only a component the re-run turns on gets
the newest release. Updates are the admin panel's job
([updates.md](updates.md)).

The pinned defaults in `compose.yaml` stay current too: the **Bump images**
workflow (`.github/workflows/bump-images.yml`, daily and on demand) runs
`go run ./cmd/bumpimages` (the same rules) and opens a pull request moving
them, and their mentions in `.env.example` and `docs/`, to the newest
releases; CI tests the new versions on that PR. `EVRD_LINKS_IMAGE`'s default
never moves (a new default would recreate evrd-links and drop every Boson
user off IRC). Ergo, RoleServ, the web IRC gateway, certsync and the setup
image are built from this repository and published by its CI, so they are
always the stack's own.

The stack files themselves come from the newest **stack release** (the
setup image carries its bundle; `install.sh --edge` runs `main`'s): the
wizard writes them with the bundle's `scripts/stack-apply.sh` (a file you
changed since the last install is replaced and kept as `<file>.local`),
records the release (`VERSION`, `.boson/`) and runs its migrations
([updates.md](updates.md#stack-releases)).

### Re-running

Run `install.sh` again any time: it replaces the wizard container, with a
new code. On an existing install the wizard reads `.env` and
`config/boson.yaml` back (the admin's name from its state file), keeps every
secret (a database password changed after the first start would lock the
services out), never creates the admin twice (it only makes sure they are
the Owner, and refuses to promote an account with another email), and edits
`.env` and `config/boson.yaml` in place: comments, your own lines and keys,
and admin-panel edits stay. With the same answers nothing changes: image
versions included (see [Versions](#versions)), and applying updates and app
discovery are read back as they are (an install from before those choices
reads them as off; turn them on here).

An install that fails or is interrupted (the wizard restarted) comes back on
the Install page with the failed step. Every step is idempotent, so Retry
runs them all again. If the wizard restarted before the admin was created it
asks for the password again.

### How it runs

`install.sh` starts the container with the host network (it checks the
host's addresses and ports), the Docker socket, and the install dir mounted
at the same path (compose resolves the stack's bind mounts on the host).
The image carries this repo's stack files and the docker CLI with compose.

## Code layout (`images/setup`)

```
main.go                  serve | code | fingerprint | probe
internal/flow            Flow interface + registry: the first screen's choices
internal/flow/fresh      "New Boson server": its API (/api/flows/fresh/*)
internal/flow/addon      "Add Boson to my existing IRC server": probe, snippets, Re-check (docs/bridge.md)
internal/stack           answers → .env + config/boson.yaml; reads an install back;
                         IRC server and reverse proxy snippets (snippets.go, proxy.go)
internal/checks          DNS, ports (and the probe container), SMTP test, the proxy on 80/443
internal/releases        the newest release of each image (GitHub + registry), pins for .env
cmd/bumpimages           moves compose.yaml's pinned image defaults (the Bump images workflow)
internal/install         the install steps, state file, progress hub, compose
internal/gate            the one-time code, sessions, rate limits
internal/server          HTTPS, security headers, session check
web/                     the pages: app.js (code, chooser), flows/<id>.js
cmd/devserver            -tags dev: the pages against fake docker/DNS (ergo stays
                         "starting" for -slow, 20s, with an ACME error in Traefik's log;
                         -releases live|fail|off for the Review step's versions)
```

**Adding a flow**: a package implementing `flow.Flow` (`Info()` for its
card, `Register()` for its API under `/api/flows/<id>/`), one `reg.Add(...)`
in `main.go`, and `web/flows/<id>.js` exporting `mount(ctx)`.

**The add-on path** (`internal/flow/addon`, `web/flows/addon.js`,
[bridge.md](bridge.md)) reuses the fresh flow: `fresh.NewFlow` with its own
ID and `stack.ModeAddon` answers serves the same API, and `addon.js` reuses
`fresh.js`'s domain, TLS, admin, features, ports, review, install and done
pages (`useFlow`). Its own steps: the IRC server (address, TLS, services
RPC), what Boson gets (the probe's capability table, `pkg/probe`, its own
module: `replace … => ../../pkg/probe` in `go.mod`), the bridge, accounts
(auth's decision order: services RPC → NickServ accounts, SASL → NickServ via
SASL, else Boson accounts; the admin can override), and the config for their
server: per-software snippets (`internal/stack/snippets.go`) with Copy
buttons and Re-check, which probes again (WEBIRC with the bridge's password,
8 connections at once, the RPC, `+draft/react`) until those are green.

## Developing

```sh
cd images/setup
go test ./...
go run -tags dev ./cmd/devserver -stack <a copy of the stack files>   # https://127.0.0.1:8443 (-addr), code TEST-CODE
cd ../../test/setup && DEV=1 npx playwright test wizard.spec.ts       # against the dev server
```

`test/setup/run.sh` is the CI end-to-end test: `install.sh --yes` with the
image built from the checkout, the whole wizard in Playwright (wrong code,
DNS pointing elsewhere, a UDP port the firewall drops, a port taken so a
service can't start, then Retry), certificates from Pebble, the installed
stack checked (`smoke-result.py`: admin and a new user over IRC, a channel,
history after reconnecting, a voice token, an upload; the web client), and
`install.sh` again (same secrets, same config, one admin). Screenshots of
every step at desktop and phone width are the job's artifact.

`test/setup/run-addon.sh PAIRING` is the add-on path's: a `test/ircds`
pairing (published on the runner's address so the stack reaches it), the
wizard in Playwright (`addon.spec.ts`: a wrong RPC token, the capability
table equal to `pkg/probe/testdata/golden/<pairing>.json`, Re-check red, the
snippets applied with `test/ircds/run.sh apply`, Re-check green and the
`.webirc.json` table, install), then `addon-result.py`: Boson users over the
bridge, replies/reactions/history, a classic client on the IRC server seeing
them with their own address, NickServ sign-in in NickServ mode, and nothing
a Boson user receives saying "evrd". Locally against the dev server:
`test/ircds/run.sh up ngircd-anope`, then `DEV=1 SETUP_PAIRING=ngircd-anope
SETUP_RPC_URL=http://127.0.0.1:18080/jsonrpc SETUP_RPC_TOKEN=fixture-rpc-token
npx playwright test addon.spec.ts` (`SETUP_IRC_PORT` for the others).
