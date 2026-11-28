# Usage and health stats (telemetry, schema 1)

> This is the contract between the senders (the admin panel's install
> report, the Boson apps' daily ping) and the receiver (`telemetryd`,
> `images/telemetry/`, the `telemetry` compose profile that only
> boson.chat runs). A change to a field here is a schema change.

## Why

We collect a small amount of anonymous usage and health data to make Boson
better: to see which versions and features people use, and to catch failed
updates, crashes and struggling servers so we can fix them, often before
anyone has to report them.

Boson is made by a small team. Without this we can't tell how many servers
run Boson, which versions they're on (so we know when a release breaks
something, or when an old version is still widespread and needs care), which
features are worth our time, or when updates fail and servers run out of
CPU, memory or disk. Nothing here identifies a person, a server or a
network, and turning it off is one switch.

The short version of this text is what the admin panel's Settings, the setup
wizard, the apps' Settings and the website (`boson.chat/telemetry`) show:

> **Usage & health stats.** We collect a small amount of anonymous usage and
> health data to make Boson better: to see which versions and features
> people use, and to catch failed updates, crashes and struggling servers so
> we can fix them, often before anyone has to report them. No names, emails,
> messages, channel names, IP addresses or hostnames, ever.

## What is sent, by whom, when

| Sender | Where | When | Endpoint |
| --- | --- | --- | --- |
| **Install report**: the admin panel (`admin` profile) of every self-hosted install | server → `telemetry.boson.chat` | once a day at a random minute (24 h ± 1 h jitter); 5–15 min after the panel first starts on a new install; ~5 min after every update or rollback finishes | `POST https://telemetry.boson.chat/v1/report` (`TELEMETRY_URL` overrides the base) |
| **App ping**: the Boson desktop, iOS, Android and web apps | device → `telemetry.boson.chat` | once a day per device, the first time the app is open after 24 h | `POST https://telemetry.boson.chat/v1/app` |

Both are JSON bodies, `Content-Type: application/json`, at most **16 KiB**
(install report) and **2 KiB** (app ping). No cookies, no auth headers, no
query strings. A failed send is retried at the next scheduled time only; it
is never queued, never retried in a loop and never blocks or slows anything
else. Senders log failures at debug level only.

### Buckets

Every count is sent as a **bucket**, never an exact number:

| Bucket | Means |
| --- | --- |
| `"0"` | 0 |
| `"1"` | 1 |
| `"2-10"` | 2 to 10 |
| `"11-50"` | 11 to 50 |
| `"51-100"` | 51 to 100 |
| `"101-500"` | 101 to 500 |
| `"501-1k"` | 501 to 1,000 |
| `"1k-5k"` | 1,001 to 5,000 |
| `"5k+"` | more than 5,000 |

The panel sums buckets across installs as a range (sum of the lower bounds –
sum of the upper bounds, `5k+` counted as 5,001 – 5,001 for the upper bound
too, so totals over 5k are "at least") plus the midpoint as an estimate.

Health numbers that aren't counts of people (restart counts, update
attempts, percentages) are small integers or percentages, capped as stated.

### Install report (`POST /v1/report`)

| Field | Type | Values |
| --- | --- | --- |
| `schema` | int | `1` |
| `install_id` | string | 32 lowercase hex characters: 128 random bits made once by the admin panel and kept in its data volume (`/data/telemetry-id`). Not derived from anything. Deleting the file makes a new one. |
| `reason` | enum | `daily`, `setup` (first send of a new install), `update` (after an update finished, failed or not), `rollback` (after a rollback) |
| `stack.version` | semver | the stack release (`VERSION` file), or `"unknown"` |
| `services` | object | service name → image version. Keys from the **service enum** below; values a semver or `"unknown"` (a `latest`/`edge`/digest-only image). The sender normalises image tags: a leading `v` and a `-alpine`-style suffix that isn't a pre-release are dropped, `X.Y` becomes `X.Y.0`, anything else is `"unknown"`. Only services that have a container. |
| `profiles` | array of enum | the enabled compose profiles: `irc`, `voice`, `video`, `auth`, `api`, `directory`, `files`, `minio`, `metrics`, `admin`, `updates`, `webroot`, `wellknown`, `web`, `bridge`, `pushrelay`, `telemetry` |
| `install.mode` | enum | `fresh` (the stack's own Ergo), `addon` (Boson in front of an existing IRC server through the bridge), `unknown` |
| `install.ircd` | enum | `ergo`, `unrealircd`, `inspircd`, `ngircd`, `solanum`, `charybdis`, `hybrid`, `other`, `unknown` |
| `install.arch` | enum | `amd64`, `arm64`, `other` |
| `counts.accounts` | bucket | accounts in auth (people, not bots/services). From auth's internal `GET /internal/v1/stats` (`X-Internal-Token`) on every report; on an auth without it, from auth's admin API while someone with `users.view` uses the panel (`null` once that read is over 7 days old) |
| `counts.active_7d` | bucket | accounts active in the last 7 days (`last_active_at`; same source as `accounts`) |
| `counts.active_30d` | bucket | accounts active in the last 30 days (same source as `accounts`) |
| `counts.irc_peak_24h` | bucket | most IRC users connected at once in the last 24 h, sampled every 15 minutes and at send time (`null` if unknown). `fresh`: Ergo's API (`POST /v1/status`, `users.total`); `addon`: the bridge's managed API (evrd `GET /api/managed/stats`, the IRC server's LUSERS). When that has no count, the panel's IRC check reads LUSERS, which needs guest access |
| `counts.channels` | bucket | registered channels (`null` if unknown). `fresh`: Ergo's API (`POST /v1/status`, `registered_channels`, from the stack's Ergo patch); `addon`: the IRC server's channel count from the bridge's managed API (LUSERS' channels formed, as no IRCd says how many are registered); `null` otherwise |
| `features.voice_minutes_24h` | bucket or `null` | voice minutes (people × minutes) in the last 24 h; `null` without the voice profile or metrics |
| `features.video_minutes_24h` | bucket or `null` | the same for video |
| `features.files_uploaded_24h` | bucket or `null` | files uploaded in the last 24 h |
| `features.push_sent_24h` | bucket or `null` | push notifications sent in the last 24 h: pushes the relay delivered to APNs / FCM (`pushrelay_upstream_requests_total{result="ok"}`, scraped as job `pushrelay` from `pushrelay:3001/metrics`); `null` without the `pushrelay` profile or metrics |
| `settings.registration` | enum | `open`, `invite`, `closed` |
| `settings.guest_access` | bool | guests may use IRC without an account |
| `settings.guest_first` | bool or `null` | the web client lets visitors in as guests first; `null` if the install's version doesn't have the setting |
| `settings.smtp` | bool | email (SMTP) is configured |
| `settings.sso` | array of enum | the *kinds* of enabled sign-in providers: `oidc`, `saml`, `oauth2`, `other` (never their names, URLs or client IDs); empty if none |
| `settings.federation_peers` | bucket | trusted federation peers |
| `settings.directory_listed` | bool | the network is listed in a directory |
| `health.updates.attempts` | int 0–100 | updates (component or stack) started in the last 24 h |
| `health.updates.failures` | int 0–100 | of those, how many failed |
| `health.updates.rollbacks` | int 0–100 | rollbacks (automatic or by hand) in the last 24 h |
| `health.updates.errors` | array of enum, ≤ 10 | the error class of each failure, from the **update error enum** |
| `health.services` | object | service name (service enum) → `{ "restarts": int 0–1000, "state": enum }`, `state` one of `running`, `restarting`, `unhealthy`, `exited`, `created`. `restarts` is Docker's restart count, as it is now. Only services that have a container. |
| `health.host` | object or `null` | `null` without the metrics profile (no VictoriaMetrics/node-exporter). Otherwise each of `cpu_p95`, `cpu_max`, `mem_p95`, `disk_used`, `load_per_cpu` (numbers, percentages 0–100 with one decimal, `load_per_cpu` 0–100: the 24 h p95 of load1 ÷ CPU count) or `null` when that one isn't available |
| `health.healthy` | bool | the panel's own verdict at send time: every running service healthy, nothing restarting, no update failure unresolved |
| `uptime_days` | int 0–3650 or `null` | days since the host booted (node-exporter), `null` without metrics |

**Service enum**: `traefik`, `certsync`, `ergo`, `roleserv`, `evrd`,
`evrd-links`, `postgres`, `auth`, `quicvoiced`, `quicvideod`,
`victoriametrics`, `node-exporter`, `admin`, `docker-proxy`,
`docker-proxy-updates`, `api`, `directory`, `files`, `pushrelay`, `minio`,
`webroot`, `wellknown`, `web`, `webircgateway`, `telemetry`,
`victoriametrics-telemetry`. One-shot init containers (`config-init`,
`*-init`, `api-migrate`) and services the install added itself are left
out.

**Update error enum** (the step an update failed at; never the error text):
`preflight` (checks before starting, e.g. disk space), `backup`, `pull`
(image or bundle download), `migrate` (database migrations), `recreate`
(starting the new containers), `health` (the new containers didn't become
healthy), `checks` (the post-update checks: IRC, auth, voice, …),
`self_update` (the panel updating itself), `rollback` (the rollback itself
failed), `timeout`, `other`.

**Versions** must match `^v?[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`
(a leading `v` is dropped at ingest) or be `"unknown"`. Anything else is a
400. Build metadata (`+…`) is not allowed.

### App ping (`POST /v1/app`)

| Field | Type | Values |
| --- | --- | --- |
| `schema` | int | `1` |
| `device_id` | string | 32 lowercase hex characters, random, made once per app install (desktop: the app's data dir; phones: app storage; web: `localStorage`). Not the push token, not a hardware ID. |
| `app_version` | semver | the app's version |
| `platform` | enum | `macos`, `windows`, `linux`, `ios`, `android`, `web` |
| `channel` | enum | `stable`, `beta`, `dev` (a development or e2e build) |
| `crashes` | int 0–1000 | crashes since the last ping: uncaught errors and unhandled rejections in the app, renderer/WebView crashes, and, on desktop, engine panics the app recovered from. Capped at 1000. |
| `servers` | bucket | how many servers (networks) the app is connected to now, never which |

### Never collected

Not in any field, not in logs, not stored:

- usernames, nicks, real names, emails, account IDs;
- channel names, topics, messages, message counts per person, who talks to whom;
- IP addresses (see below), hostnames, domains, network names, URLs;
- secrets: passwords, tokens, keys, `.env` values, SSO client IDs;
- free-text error messages or logs;
- anything about a specific person.

The sender test suites check that a report built from a config full of
secrets and hostnames contains none of them.

### Country

`telemetryd` looks up the **country** of the request's IP address in an
offline database ([DB-IP Lite](https://db-ip.com), CC BY 4.0) while handling
the request, as an ISO 3166-1 alpha-2 code (`ZZ` when unknown). The IP is
then dropped: it is not written to the database, to VictoriaMetrics or to
logs (Traefik's access log is off for the telemetry hosts). The per-IP rate
limit keeps a keyed hash of the IP in memory for at most an hour.

### Responses

| Status | When | Body |
| --- | --- | --- |
| `202` | accepted (counted or not) | `{"ok":true}` |
| `400` | not valid against this schema (unknown fields, wrong types, values outside the enums/patterns) | `{"ok":false,"error":"invalid","field":"<json path>"}` |
| `413` | over the size cap | `{"ok":false,"error":"too_large"}` |
| `415` | not `application/json` | `{"ok":false,"error":"content_type"}` |
| `429` | rate limited (per IP) | `{"ok":false,"error":"rate_limited"}` |

Senders ignore the body and never retry on any status.

## JSON Schema

### Install report

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://boson.chat/schemas/telemetry/report-1.json",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema", "install_id", "reason", "stack", "services", "profiles",
               "install", "counts", "features", "settings", "health"],
  "$defs": {
    "bucket": { "enum": ["0", "1", "2-10", "11-50", "51-100", "101-500", "501-1k", "1k-5k", "5k+"] },
    "nbucket": { "oneOf": [{ "$ref": "#/$defs/bucket" }, { "type": "null" }] },
    "version": { "type": "string", "maxLength": 64,
                 "pattern": "^(unknown|v?[0-9]{1,4}\\.[0-9]{1,4}\\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?)$" },
    "service": { "enum": ["traefik", "certsync", "ergo", "roleserv", "evrd", "evrd-links", "postgres",
                          "auth", "quicvoiced", "quicvideod", "victoriametrics", "node-exporter", "admin",
                          "docker-proxy", "docker-proxy-updates", "api", "directory", "files", "pushrelay",
                          "minio", "webroot", "wellknown", "web", "webircgateway", "telemetry",
                          "victoriametrics-telemetry"] },
    "pct": { "oneOf": [{ "type": "number", "minimum": 0, "maximum": 100 }, { "type": "null" }] },
    "small": { "type": "integer", "minimum": 0, "maximum": 100 }
  },
  "properties": {
    "schema": { "const": 1 },
    "install_id": { "type": "string", "pattern": "^[0-9a-f]{32}$" },
    "reason": { "enum": ["daily", "setup", "update", "rollback"] },
    "stack": { "type": "object", "additionalProperties": false, "required": ["version"],
               "properties": { "version": { "$ref": "#/$defs/version" } } },
    "services": { "type": "object", "maxProperties": 26,
                  "propertyNames": { "$ref": "#/$defs/service" },
                  "additionalProperties": { "$ref": "#/$defs/version" } },
    "profiles": { "type": "array", "uniqueItems": true, "maxItems": 17,
                  "items": { "enum": ["irc", "voice", "video", "auth", "api", "directory", "files", "minio",
                                      "metrics", "admin", "updates", "webroot", "wellknown", "web", "bridge",
                                      "pushrelay", "telemetry"] } },
    "install": { "type": "object", "additionalProperties": false, "required": ["mode", "ircd", "arch"],
                 "properties": {
                   "mode": { "enum": ["fresh", "addon", "unknown"] },
                   "ircd": { "enum": ["ergo", "unrealircd", "inspircd", "ngircd", "solanum", "charybdis",
                                      "hybrid", "other", "unknown"] },
                   "arch": { "enum": ["amd64", "arm64", "other"] } } },
    "counts": { "type": "object", "additionalProperties": false,
                "required": ["accounts", "active_7d", "active_30d", "irc_peak_24h", "channels"],
                "properties": {
                  "accounts": { "$ref": "#/$defs/nbucket" }, "active_7d": { "$ref": "#/$defs/nbucket" },
                  "active_30d": { "$ref": "#/$defs/nbucket" }, "irc_peak_24h": { "$ref": "#/$defs/nbucket" },
                  "channels": { "$ref": "#/$defs/nbucket" } } },
    "features": { "type": "object", "additionalProperties": false,
                  "required": ["voice_minutes_24h", "video_minutes_24h", "files_uploaded_24h", "push_sent_24h"],
                  "properties": {
                    "voice_minutes_24h": { "$ref": "#/$defs/nbucket" }, "video_minutes_24h": { "$ref": "#/$defs/nbucket" },
                    "files_uploaded_24h": { "$ref": "#/$defs/nbucket" }, "push_sent_24h": { "$ref": "#/$defs/nbucket" } } },
    "settings": { "type": "object", "additionalProperties": false,
                  "required": ["registration", "guest_access", "guest_first", "smtp", "sso",
                               "federation_peers", "directory_listed"],
                  "properties": {
                    "registration": { "enum": ["open", "invite", "closed"] },
                    "guest_access": { "type": "boolean" },
                    "guest_first": { "type": ["boolean", "null"] },
                    "smtp": { "type": "boolean" },
                    "sso": { "type": "array", "uniqueItems": true, "maxItems": 4,
                             "items": { "enum": ["oidc", "saml", "oauth2", "other"] } },
                    "federation_peers": { "$ref": "#/$defs/bucket" },
                    "directory_listed": { "type": "boolean" } } },
    "health": { "type": "object", "additionalProperties": false,
                "required": ["updates", "services", "host", "healthy"],
                "properties": {
                  "updates": { "type": "object", "additionalProperties": false,
                               "required": ["attempts", "failures", "rollbacks", "errors"],
                               "properties": {
                                 "attempts": { "$ref": "#/$defs/small" }, "failures": { "$ref": "#/$defs/small" },
                                 "rollbacks": { "$ref": "#/$defs/small" },
                                 "errors": { "type": "array", "maxItems": 10,
                                             "items": { "enum": ["preflight", "backup", "pull", "migrate", "recreate",
                                                                 "health", "checks", "self_update", "rollback",
                                                                 "timeout", "other"] } } } },
                  "services": { "type": "object", "maxProperties": 26,
                                "propertyNames": { "$ref": "#/$defs/service" },
                                "additionalProperties": {
                                  "type": "object", "additionalProperties": false, "required": ["restarts", "state"],
                                  "properties": {
                                    "restarts": { "type": "integer", "minimum": 0, "maximum": 1000 },
                                    "state": { "enum": ["running", "restarting", "unhealthy", "exited", "created"] } } } },
                  "host": { "oneOf": [{ "type": "null" }, {
                    "type": "object", "additionalProperties": false,
                    "required": ["cpu_p95", "cpu_max", "mem_p95", "disk_used", "load_per_cpu"],
                    "properties": {
                      "cpu_p95": { "$ref": "#/$defs/pct" }, "cpu_max": { "$ref": "#/$defs/pct" },
                      "mem_p95": { "$ref": "#/$defs/pct" }, "disk_used": { "$ref": "#/$defs/pct" },
                      "load_per_cpu": { "$ref": "#/$defs/pct" } } }] },
                  "healthy": { "type": "boolean" } } },
    "uptime_days": { "type": ["integer", "null"], "minimum": 0, "maximum": 3650 }
  }
}
```

Example:

```json
{
  "schema": 1,
  "install_id": "3f2a9c0e5b7d41e8a6c2f90b1d4e7a53",
  "reason": "daily",
  "stack": { "version": "1.48.0" },
  "services": { "traefik": "3.7.19", "ergo": "1.48.0", "auth": "1.9.2", "quicvoiced": "1.8.0",
                "admin": "1.32.0", "postgres": "17.6.0", "victoriametrics": "1.131.0" },
  "profiles": ["irc", "voice", "auth", "metrics", "admin", "updates"],
  "install": { "mode": "fresh", "ircd": "ergo", "arch": "amd64" },
  "counts": { "accounts": "11-50", "active_7d": "2-10", "active_30d": "11-50",
              "irc_peak_24h": "2-10", "channels": "2-10" },
  "features": { "voice_minutes_24h": "51-100", "video_minutes_24h": null,
                "files_uploaded_24h": null, "push_sent_24h": "11-50" },
  "settings": { "registration": "open", "guest_access": true, "guest_first": null, "smtp": true,
                "sso": ["oidc"], "federation_peers": "0", "directory_listed": true },
  "health": {
    "updates": { "attempts": 1, "failures": 1, "rollbacks": 1, "errors": ["checks"] },
    "services": { "auth": { "restarts": 0, "state": "running" }, "ergo": { "restarts": 7, "state": "restarting" } },
    "host": { "cpu_p95": 34.5, "cpu_max": 98.2, "mem_p95": 71.0, "disk_used": 62.3, "load_per_cpu": 40.1 },
    "healthy": false
  },
  "uptime_days": 41
}
```

### App ping

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://boson.chat/schemas/telemetry/app-1.json",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema", "device_id", "app_version", "platform", "channel", "crashes", "servers"],
  "properties": {
    "schema": { "const": 1 },
    "device_id": { "type": "string", "pattern": "^[0-9a-f]{32}$" },
    "app_version": { "type": "string", "maxLength": 64,
                     "pattern": "^v?[0-9]{1,4}\\.[0-9]{1,4}\\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$" },
    "platform": { "enum": ["macos", "windows", "linux", "ios", "android", "web"] },
    "channel": { "enum": ["stable", "beta", "dev"] },
    "crashes": { "type": "integer", "minimum": 0, "maximum": 1000 },
    "servers": { "enum": ["0", "1", "2-10", "11-50", "51-100", "101-500", "501-1k", "1k-5k", "5k+"] }
  }
}
```

Example: `{"schema":1,"device_id":"9b1e0c4d2a7f48e6b3c5d8a1f0e2b4c6","app_version":"0.106.0","platform":"macos","channel":"stable","crashes":0,"servers":"2-10"}`

## How to opt out

**A server (install report):** any one of these turns it off.

- Admin panel → **Settings** → **Usage & health stats** → off. Saved as
  `admin.telemetry: false` in `config/boson.yaml` (live, no restart). The
  same card has **See exactly what's sent** (the next report as JSON) and
  the last send's time and result.
- `.env`: `TELEMETRY=off` (then `docker compose up -d admin`). This wins
  over the panel: the switch shows as off and locked.
- The setup wizard's **Usage & health stats** note has a one-click
  **Turn off**, which writes `TELEMETRY=off`.
- Without the `admin` profile nothing is ever sent: the report comes from
  the admin panel.

**An app (app ping):** Settings → Privacy → **Usage & health stats** → off.
The web app starts **off** when the browser sends Do Not Track
(`navigator.doNotTrack == "1"`) or Global Privacy Control
(`navigator.globalPrivacyControl == true`); the switch can still turn it on.
A device that never pings simply drops out of the counts.

Turning it off stops the next send; nothing is sent to say it was turned
off. What was sent before ages out (below); the IDs are random, so there is
nothing to tie it back to an install or device. To have it removed sooner,
email `hi@boson.chat` with the install ID (Settings → See exactly what's
sent shows it) and we delete its rows.

## What is kept, and for how long

- **The raw request is never stored**: not the body, headers or IP.
- The install and device IDs are stored only as a SHA-256 hash; the panel
  shows the first 12 hex characters of that hash.
- Per install / device (SQLite on the `telemetry_data` volume): first seen,
  last seen, the latest versions, country, and the validated, bucketed
  report of each day for **90 days** (the Problems page's report history).
  An install or device unseen for 2 years is deleted.
- Daily aggregates (VictoriaMetrics `victoriametrics-telemetry`, its own
  container, `-retentionPeriod=2y`): **2 years** of daily points, labelled
  only with enums, versions and country codes (no IDs).

### Daily aggregates (VictoriaMetrics)

Written once a day for the previous UTC day (timestamp: that day 00:00
UTC) through `/api/v1/import`, from the stored reports of that day (each
install's last report that day counts once):

| Metric | Labels |
| --- | --- |
| `boson_installs_active` | — |
| `boson_installs_new` | — (first seen that day) |
| `boson_installs_version` | `service` (`stack` or the service enum), `version` |
| `boson_installs_country` | `country` |
| `boson_installs_profile` | `profile` |
| `boson_installs_mode` | `mode`, `ircd` |
| `boson_installs_setting` | `setting` (`registration`, `guest_access`, `guest_first`, `smtp`, `sso`, `directory_listed`, `federation`: peers > 0), `value` (enum value, `true`/`false`/`unknown`) |
| `boson_installs_feature` | `feature` (`voice`, `video`, `files`, `push`): installs whose bucket isn't `0`/`null` |
| `boson_installs_problem` | `problem` (`update_failed`, `rollback`, `crash_loop`, `unhealthy`, `cpu`, `memory`, `disk`) |
| `boson_people_lo` / `boson_people_hi` | `count` (`accounts`, `active_7d`, `active_30d`, `irc_peak_24h`): the bucket sums |
| `boson_app_devices` | `platform`, `version`, `channel` |
| `boson_app_crashes` | `platform`, `version` |
| `boson_app_servers` | `servers` (bucket) |

### Problem thresholds

An install shows on the Problems page (and counts in
`boson_installs_problem`) when its reports in the last 7 days have:

- `update_failed`: `health.updates.failures > 0` (with the error classes);
- `rollback`: `health.updates.rollbacks > 0`;
- `crash_loop`: a service in state `restarting`, or whose `restarts` grew by
  3 or more since the previous report;
- `unhealthy`: a service `unhealthy`, or the latest report's `healthy: false`;
- `cpu`: `cpu_p95 > 85`; `memory`: `mem_p95 > 90`; `disk`: `disk_used > 90`.

## Ingest rules (`telemetryd`)

- Size cap (16 KiB / 2 KiB, `http.MaxBytesReader`), strict decoding
  (unknown fields are a 400), then the checks above.
- **Per install / device:** the first report in a 20 h window is
  *counted* (a new day row). Further reports within 20 h are accepted
  (`202`) and update that day's row in place (so a report sent right after
  a failed update still shows on the Problems page) but never add a count.
  More than 48 accepted reports from one ID in 24 h are dropped silently.
- **Per IP:** 60 requests an hour, burst 20, then `429` (a keyed hash of
  the IP, in memory).
- Hosts: `TELEMETRY_HOST` (`telemetry.boson.chat`) answers only
  `POST /v1/report`, `POST /v1/app`, `GET /healthz`; everything else is a 404.
  `STATS_HOST` (`stats.boson.chat`) is the panel. CORS on `/v1/app` allows
  any origin (the web app), `POST` with `Content-Type` only, no credentials.

## Running it (boson.chat only)

Self-hosted installs never need this. On boson.chat:

```sh
# .env
COMPOSE_PROFILES=...,telemetry
TELEMETRY_HOST=telemetry.boson.chat
STATS_HOST=stats.boson.chat
CERT_HOSTS=...,telemetry.boson.chat,stats.boson.chat
STATS_USERNAME=owner
# bcrypt hash (cost 12), in single quotes so compose doesn't expand the $ signs.
# Make it with the image itself (asks twice, prints the hash):
#   docker compose run --rm --no-deps telemetry hash-password
STATS_PASSWORD_HASH='$2a$12$...'
```

DNS: `telemetry` and `stats` A/AAAA records pointing at the box. Then
`docker compose up -d telemetry victoriametrics-telemetry traefik`.

The country database is downloaded on first start (and refreshed monthly)
from `download.db-ip.com` into the `telemetry_data` volume; without it every
country is `ZZ`. IP to country data: [IP Geolocation by
DB-IP](https://db-ip.com), licensed under [CC BY
4.0](https://creativecommons.org/licenses/by/4.0/); the panel's footer
carries the attribution.

### The panel (`https://STATS_HOST`)

One user (`STATS_USERNAME` / `STATS_PASSWORD_HASH`), a random session token
in a cookie (`HttpOnly; Secure; SameSite=Strict`, 7 days; only its hash is
stored, and signing out deletes it), 5 failed logins per IP per 15 minutes.
Stopped-reporting installs per week are counted from the per-install table
(installs whose last report fell in that week and that have been silent
for over 7 days since). Pages: **Overview** (active installs 1d/7d/30d; new and
stopped-reporting installs per week; accounts and active users summed across
installs), **Versions** (adoption per release and per service; installs
older than N releases), **Features**, **Problems** (anonymous install IDs
linking to their 90-day report history), **Apps** (daily active devices by
platform and version, crashes per version) and **Countries**.
