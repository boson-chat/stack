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
`--no-pull`.

## The wizard

A web page on `https://<server>:8443` with a self-signed certificate
(`install.sh` prints its SHA-256 fingerprint). Steps:

| Step | What happens |
|---|---|
| Domain | Network name and domain. Every hostname the chosen features need (`irc.`, `auth.`, `chat.`, `admin.`, `api.`, plus `voice.`, `video.`, `files.`, `directory.`) is looked up live (A and AAAA, every 5 s). An address counts as this server when it's one of the host's own, or when the wizard answers on it (`/.well-known/boson-setup`, for NAT'd clouds). A name pointing elsewhere blocks the step, and the install. |
| TLS | Traefik's ACME resolver, as with `scripts/setup.sh`: Let's Encrypt, its staging CA, or any ACME directory (`ACME_CA_SERVER`). |
| Admin | Username, email, password. Created after the stack is up with `auth user create --role owner` (the Owner network role, [roles.md](roles.md)). |
| Features | Voice, video, uploads (this disk, MinIO, or an S3 bucket), the Boson directory listing and/or a directory of your own, sign-up (open / invite / closed), guests (and guests in voice), email verification, accounts from other Boson servers ([federated login](federation.md): verified servers by default, or only servers you list, any server, or off; `auth.federation.mode`/`.allow`; on the add-on path too, [federation.md](federation.md#add-on-installs)), SMTP with a test-send button. |
| Firewall and ports | 80, 443, 6697/tcp, 7700/udp (voice), 7710/udp (video). From inside: is another program on the port; does a connection from a container on Docker's network reach it (that path crosses the host's own firewall: iptables, nftables, ufw). A dropped UDP port is a warning with a "turn voice off" button. What can't be checked from inside, a provider's firewall, is said so. |
| Review | The `.env` lines and `config/boson.yaml` keys it will write, secrets masked (each says whether it is new, kept from your `.env`, or as you entered it). |
| Install | Writes the stack's files, `.env` (0600) and `config/boson.yaml` into the install dir; `docker compose pull`, `up -d`; waits for every service to be healthy (one-shot services to exit 0), for every certificate, creates the admin, checks each public URL and IRC on 6697. Progress streams live (Server-Sent Events). A failed step shows its error and a **Retry**. |
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
  Output from compose shown on the page has every secret value redacted.

### Re-running

Run `install.sh` again any time: it replaces the wizard container, with a
new code. On an existing install the wizard reads `.env` and
`config/boson.yaml` back (the admin's name from its state file), keeps every
secret (a database password changed after the first start would lock the
services out), never creates the admin twice (it only makes sure they are
the Owner, and refuses to promote an account with another email), and edits
`.env` and `config/boson.yaml` in place: comments, your own lines and keys,
and admin-panel edits stay. With the same answers nothing changes.

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
internal/stack           answers → .env + config/boson.yaml; reads an install back
internal/checks          DNS, ports (and the probe container), SMTP test
internal/install         the install steps, state file, progress hub, compose
internal/gate            the one-time code, sessions, rate limits
internal/server          HTTPS, security headers, session check
web/                     the pages: app.js (code, chooser), flows/<id>.js
cmd/devserver            -tags dev: the pages against fake docker/DNS
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
go run -tags dev ./cmd/devserver -stack <a copy of the stack files>   # https://127.0.0.1:8443, code TEST-CODE
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
