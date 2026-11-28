# Updates from the admin panel

The admin panel's **Updates** page (boson-chat/admin, `admin` profile) shows
when any part of the install has a newer release: the stack, Ergo, auth, the
api, the web client, voice, video, files, the directory, the web IRC gateway,
the panel itself and, when it runs here, evrd. For each it lists every
changelog between the installed and the newest version (GitHub Releases),
who an update affects (IRC disconnects, dropped calls, failed uploads, a
brief pause, nothing) and breaking changes that need manual steps. It checks
every 6 hours (`ADMIN_UPDATES_CHECK_INTERVAL`) and on **Check now**; with
`ADMIN_UPDATES_GITHUB_TOKEN` (a token with no scopes) GitHub allows 5,000
checks an hour instead of 60.

Versions are the `*_IMAGE` variables in `.env` (`AUTH_IMAGE`, `API_IMAGE`,
`WEB_IMAGE`, `QUICVOICE_IMAGE`, `QUICVIDEO_IMAGE`, `FILES_IMAGE`,
`DIRECTORY_IMAGE`, `ADMIN_IMAGE`, `EVRD_IMAGE`; the compose defaults
otherwise), each updated on its own. The **stack** itself (compose files,
Traefik and the other pinned images, the config layout, and Ergo, RoleServ,
the web IRC gateway and certsync, which this repository builds) is one
versioned release: [Stack releases](#stack-releases).

A new install from the setup wizard starts on the newest releases, so the
page starts empty ([setup.md](setup.md#versions)); a re-run of the wizard
never changes versions.

## Applying updates

Seeing updates needs nothing. Applying them needs the `updates` profile. The
setup wizard turns it on by default (Features → **Let the admin panel apply
updates**; re-running the wizard turns it on or off). By hand:

```sh
# .env
COMPOSE_PROFILES=...,admin,updates
ADMIN_UPDATES_DOCKER_PROXY_URL=http://docker-proxy-updates:2375
docker compose up -d docker-proxy-updates admin
```

`docker-proxy-updates` is a second socket proxy that, on top of what
`docker-proxy` allows, lets the panel pull images and create and remove
containers. The panel runs every step of an update in a short-lived
`docker:cli` helper container (label `chat.boson.updates`, removed when the
step ends) that mounts the Docker socket and this directory. **That is
root-equivalent access to the host**: only turn it on if the people with the
panel's `services.manage` + `settings.manage` permissions may have that.

An update, after the admin confirms it:

1. checks the compose project and that the backup fits on the disk;
2. **backs up** to `ADMIN_UPDATES_BACKUP_DIR/<run>` (default
   `/root/backups/boson-updates`, the newest `ADMIN_UPDATES_BACKUP_KEEP`
   kept): `.env`, `config/`, the compose files, every service's image
   digest, a `pg_dump -Fc` of every database plus globals, the data volumes
   (`ergo_db`, `roleserv_data`, `voice_data`, `directory_media`, evrd's) and
   MinIO's metadata. If the backup fails, nothing changes;
3. pulls the new images, sets the `*_IMAGE` lines in `.env`, and recreates
   only those services (`up -d --no-deps`, `api-migrate` first for the api);
4. waits for them to be healthy and checks the stack the way people use it
   (IRC over TLS, auth issuing a token, the api, voice, video, files,
   directory, the web client's page, the versions);
5. updates the panel itself last: a detached helper recreates `admin` and
   puts the previous digest back if the new one doesn't stay up. The panel's
   `admin_data` volume keeps the run so the new version finishes it.

**Rollback** (on the page after a failure, or after a success) puts back
`.env`, `config/` and the exact previous image digests, and, when the update
ran database migrations, restores those databases (and the components'
volumes) from the backup; then it runs the same checks.

## Stack releases

Every merge to `main` is a release of the stack (semantic-release,
`.releaserc.json`: `feat:` a minor version, a breaking change a major one,
anything else a patch), `vX.Y.Z` on GitHub. Before the release exists, CI
(the `publish` job, `scripts/release/publish.sh`) tags the images this
repository builds with its version (`harbor.floret.dev/boson/stack-ergo:X.Y.Z`,
`stack-certsync`, `stack-webircgateway`, `stack-setup`; the old tags
`stack-ergo:2.19.1`, `stack-certsync:latest` and
`stack-webircgateway:1.1.0-boson.2` are still pushed from `main` for
installs from before releases), so every image a release names is on
Harbor when the release appears. `stack-setup:latest`, what `install.sh`
runs, is the newest release; `install.sh --edge` runs `main`'s
(`stack-setup:edge`).

A release has three assets:

| Asset | |
|---|---|
| `boson-stack-X.Y.Z.tar.gz` | the **bundle**: everything an install needs from this repository at runtime (below) |
| `boson-stack-X.Y.Z.tar.gz.sha256` | its SHA-256, checked before anything is unpacked |
| `manifest.json` | the same as the bundle's `.bundle/manifest.json` |

`scripts/release/bundle.sh` builds it (`--out DIR [--version V] [--release]`;
the setup image's build runs it too, so the wizard installs exactly the
bundle). In it: `compose.yaml`, `compose.video-node.yaml`, `.env.example`,
`config/boson.example.yaml`, `traefik/dynamic/{stack,directory}.yml`,
`postgres/`, `monitoring/`, the build contexts compose names
(`images/{ergo,certsync,webircgateway}`) and `images/wellknown/serve.sh`,
`scripts/setup.sh`, `scripts/migrate-config.sh`, `scripts/stack-apply.sh`,
`migrations/`, `docs/`, `README.md`, `LICENSE`, `NOTICE` and `VERSION`
(the release's version). Not in it: tests, CI, `compose.auth-src.yaml`
(development), and nothing install-specific. In a release bundle the
compose files name the stack's own images by the release's version
(`stack-ergo:X.Y.Z`); on `main` they keep the old tags.

`.bundle/manifest.json`:

```json
{
  "schema": 1,
  "version": "1.4.0",
  "commit": "…",
  "release": true,
  "min_admin": "1.30.0",
  "services": {
    "ergo": {"compose": "compose.yaml", "image": "harbor.floret.dev/boson/stack-ergo:1.4.0", "built": true, "profiles": ["irc"], "mounts": ["config"]},
    "auth": {"compose": "compose.yaml", "image": "harbor.floret.dev/boson/auth:1.27.0", "env": "AUTH_IMAGE", "profiles": ["auth"], "mounts": ["config"]},
    "config-init": {"compose": "compose.yaml", "image": "busybox:1.37.0", "oneshot": true, "profiles": [], "mounts": ["config"]}
  },
  "video_node_services": {},
  "migrations": ["0001-bridge-client-webirc.sh"],
  "files": {"compose.yaml": "<sha256>"}
}
```

`image` is the reference compose uses when `.env` doesn't set `env`
(`${AUTH_IMAGE:-…}`) or the literal `image:`; `built`: this repository
builds it; `oneshot`: `restart: "no"`, it runs and exits; `mounts`: its
`./…` bind mounts. `min_admin` is the oldest admin panel that can apply it
(`scripts/release/min-admin-version`; raise it when a release needs
something new from the panel).

### What a stack update does

On the Updates page the stack row shows the installed release (`VERSION`;
"pre-release" for an install from before releases, which can still update)
and, for a newer one, before anything is applied: every file that changes
with its diff, the image changes (the stack's own images and the
components' defaults), the migrations it will run, the release notes, and
who it affects (which services restart; Ergo changing disconnects
everyone on IRC). Ergo, RoleServ, the web IRC gateway and certsync are part
of it: they have no rows of their own and move with the stack's version.

Applying it (admin ≥ `min_admin`, the `updates` profile):

1. the usual checks and **backup**, plus the whole stack directory
   (`stack-dir.tgz`, without backups and `.boson/staging`);
2. downloads the bundle to `.boson/staging/` and checks its SHA-256;
3. pulls the images of the services that change;
4. `scripts/stack-apply.sh files` from the new bundle (below), the image
   versions (`*_IMAGE`, [precedence](#stack-releases-and-image-pins)) into
   `.env`, then `scripts/stack-apply.sh migrate`;
5. recreates **only the services whose configuration changed** (compose's
   config hash before and after, plus those whose bind-mounted bundle files
   changed or that a migration asks for), with `--no-deps`; one-shot
   services run to completion first. `evrd-links` is never recreated, nor
   is the panel (it goes last, as for its own update);
6. health and the same checks as every update;
7. on any failure from step 4 on, it **rolls back by itself**: the stack
   directory, `.env` and `config/` from the backup, files the update added
   removed, the previous images, and the services recreated again.

By hand, on a server without the panel (as root, in the install
directory):

```sh
V=1.4.0   # https://github.com/boson-chat/stack/releases
mkdir -p .boson/staging && cd .boson/staging
curl -fsSLO https://github.com/boson-chat/stack/releases/download/v$V/boson-stack-$V.tar.gz
curl -fsSLO https://github.com/boson-chat/stack/releases/download/v$V/boson-stack-$V.tar.gz.sha256
sha256sum -c boson-stack-$V.tar.gz.sha256 && mkdir $V && tar xzf boson-stack-$V.tar.gz -C $V
cd ../..
STACK_DIR=$PWD sh .boson/staging/$V/scripts/stack-apply.sh plan --diff .boson/staging/$V   # what changes
STACK_DIR=$PWD sh .boson/staging/$V/scripts/stack-apply.sh files .boson/staging/$V
STACK_DIR=$PWD sh .boson/staging/$V/scripts/stack-apply.sh migrate .boson/staging/$V
docker compose up -d      # evrd-links: see docs/bridge.md before letting it be recreated
```

### What a stack update never changes

- `.env` values. Migrations may **add** keys (or fill an empty one); the
  only values an update sets are `*_IMAGE` versions, by the rule below.
- `config/boson.yaml`, `compose.override.yaml`, `secrets/`, `local-certs/`,
  `bridge/` (except through a migration), `.setup/`, backups, and every file
  that was never in a bundle (your own `traefik/dynamic/*.yml`, say).
- A bundle file changed on this server: `.boson/stack-files.sha256` records
  what each installed file was. One that differs from that record was
  edited here: the update replaces it with the release's and keeps your
  copy next to it as `<file>.local` (the Updates page says so before and
  after). Put install-specific changes in `compose.override.yaml` or
  `traefik/dynamic/` instead. A file an earlier release had and this one
  doesn't is removed, unless it was edited here. An install from before
  releases has no record: its files are replaced with the release's (the
  page shows each difference first), and the backup has the old ones.

`.boson/` is the install's stack state: `stack-files.sha256`, `manifest.json`
(the installed release's), `migrations.applied`, `staging/`.

### Stack releases and image pins

A component's `*_IMAGE` in `.env` and the release's default for it
(`manifest.json`) can disagree. A stack update:

- **raises** the pin to the release's default when it pins an older
  version of the same image (an install from the wizard pins every
  component; the release moved its default on);
- **keeps** the pin when it is newer than the release's default (a
  component updated on its own since), or a different image (your own
  build or registry);
- leaves a component without a pin on compose's default (the release's);
- never touches `EVRD_LINKS_IMAGE` (it pins it to what runs, when unset,
  so a later `docker compose up -d` can't recreate it).

The page lists each of these before you apply.

### Migrations

`migrations/NNNN-name.sh`, in the bundle: POSIX `sh` scripts for one-off
changes an existing install needs (a new `.env` key, an entry added to a
list, a line in a file setup writes). `scripts/stack-apply.sh migrate` runs
the ones not yet in `.boson/migrations.applied`, in order, once each, after
the release's files are in place and the image pins are set, with
`STACK_DIR`, `ENV_FILE` and `MIGRATIONS_LIB` (`migrations/lib.sh`). The
setup wizard runs them on every install too (on a new install they find
nothing to do).

Rules for writing one:

- **Idempotent**: on an install that already has the change (fixed by hand,
  or set up by a newer wizard) it does nothing. Check before every change.
- **Only add**: never change or remove a value someone set. `env_set_default
  KEY VALUE` adds a key that is missing or empty; `env_set` is for appending
  to a list the migration owns an entry of.
- **Never print a secret** (the panel keeps every line in the update's log);
  say what changed, not the value.
- Line 2 is one comment line saying what it does (the Updates page shows it).
- Exit 0 = done (recorded); **exit 3** (`defer "why"`) = not yet, e.g. it
  needs a newer evrd than the one pinned (runs again on the next update);
  anything else fails the update, which rolls back.
- A change compose can't see (a file a service reads) asks for the restart:
  `restart evrd`.
- Numbers only grow; a released migration is never edited (write a new
  one), except to add the recheck line.
- **Recheck**: a `# recheck: every update` line (in the first 20) makes it
  run again on every update after it is recorded, so its change comes back
  when it went missing (a `.env` put back from an older copy: a rollback of
  the first stack update used to keep the record and lose the change). Only
  for a migration whose check is cheap and whose change nobody would undo
  on purpose. It counts as "applied" again when it changed `.env`; a failure
  there is a warning, not a failed update.
- Add a case to `test/stack-apply/run.sh`, including "already done by hand".

Helpers (`migrations/lib.sh`): `env_get`, `env_has`, `env_set`,
`env_set_default`, `gen_secret [bytes]` (hex), `has_profile`,
`compose_default VAR`, `image_of VAR`, `image_tag`, `version_ge`,
`file_has_line`, `file_append`, `restart`, `say`, `defer`.

| Migration | What it does |
|---|---|
| `0001-bridge-client-webirc.sh` | Add-on installs (bridge profile) from before #55: `BRIDGE_CLIENT_WEBIRC_PASSWORD` generated, `,evrd-links=<it>` appended to `WEBIRC_PASSWORDS`, `managed_nick_guard_grace 60s` added to `bridge/evrd.conf` (evrd restarted). Waits while `EVRD_IMAGE` pins evrd < 0.61. |
| `0002-wellknown-web.sh` | Installs that set `WELLKNOWN_SECTIONS` by hand (add-on installs from before #58) with the web profile and `WEB_HOST`: `web` added, so `boson.json` names the web client (`web.url`) and other installs' voice, video and files accept it (directory-mode origins). Rechecked on every update. |

### Moving an existing install onto releases

An install from before releases (files copied or rsynced from `main`) has no
`VERSION`: the Updates page calls it "pre-release" and offers the newest
release. The one thing to do first: update the **admin panel** to
`min_admin` or later (its own row on the same page, or `ADMIN_IMAGE` in
`.env` and `docker compose up -d --no-deps admin`), with the `updates`
profile on. Then apply the stack update and read the file list before you
confirm: on such an install every file that differs from the release is
shown as "changed since install, can't tell if edited here". Edits you
want to keep belong in `compose.override.yaml`; the backup has the old
files either way. From then on the install records each release.
