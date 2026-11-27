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
`DIRECTORY_IMAGE`, `ADMIN_IMAGE`; the compose defaults otherwise). Ergo,
RoleServ and the web IRC gateway are pinned in `compose.yaml` and come with a
stack update (`git pull`), which the page lists but doesn't apply.

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
