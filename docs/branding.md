# Network branding

A network can give the boson client its own look: theme colors, a
background color or image per pane, a logo, and a sign-in background. It
can also brand the account emails (logo, accent color, sender name, footer)
and rewrite their wording. All of it is non-secret config in
[`config/boson.yaml`](config.md) plus image files in `config/branding/`, so
it's hot-reloaded like every other setting: no restart.

| What | Where | Served by |
|---|---|---|
| Theme, pane backgrounds | `config/boson.yaml` `branding.theme` | auth: `GET https://AUTH_HOST/v1/branding` |
| Logo, wordmark, sign-in and pane images | `config/branding/<file>`, named in `branding.images` | auth: `GET https://AUTH_HOST/v1/branding/assets/<file>` |
| Email look and wording | `config/boson.yaml` `auth.email` | auth, when it sends mail |
| Where the client finds it | `/.well-known/boson.json` `"branding"`, auth's `/v1/config` `branding_url` | `wellknown` profile, auth |

Versions: auth and the admin panel releases that carry branding (see the
image defaults in `compose.yaml`). An older auth ignores the `branding:` and
`auth.email:` keys and answers `/v1/branding` with 404; clients then just use
their own theme.

## In the admin panel

**Branding** (`settings.manage`):

- The theme editor: base (dark or light), colors, the four panes (servers,
  channels, messages, members), corner radius and font. It uses the same
  color tokens as the client.
- A live preview: a mock client (server rail, channels, messages, members)
  and the sign-in screen, in dark and light.
- Image uploads per slot: logo, wordmark, sign-in background, and one
  background image per pane. The panel checks the type (by content, not the
  file name), size and dimensions, re-encodes everything except SVG to webp,
  and writes it atomically to `config/branding/<slot>-<hash>.<ext>`. Then it
  points `branding.images` at it and removes the file it replaces.
- A contrast warning when text on a background falls below 4.5:1. The client
  enforces the same rule (see below), so fix it rather than ignore it.

**Emails** (`settings.manage`): one editor per email (verification, password
reset, account setup, invite) with the variables as chips, a sandboxed
preview of the HTML, **Send test** (to any address; rate-limited and in the
audit log) and **Reset to default**.

Both pages save through the panel's config store, like every settings page:
your comments and hand edits in the file are kept, and each save is audited.

## By hand

Put the images in `config/branding/` and name them in `config/boson.yaml`.
The directory belongs to uid 65532, like the file, so use sudo:

```sh
sudo cp ~/logo.svg ~/login.webp config/branding/
sudo $EDITOR config/boson.yaml
```

```yaml
version: 1
branding:
  theme:
    base: dark
    colors: { accent: "#e11d48", bg: "#0c0a09" }
    radius: 4
    panes:
      channels: { bg: "#1c1917" }
      messages: { dim: 0.7, blur: 8, position: center }
  images:
    logo: logo.svg
    login_background: login.webp
    panes.messages.image: messages.webp
```

auth picks the change up within seconds. `config-init` makes hand-copied
files readable for auth and removable by the panel on the next
`docker compose up`; until then make sure they're world-readable
(`sudo chmod 644 config/branding/*`).

The rules (the same in auth, the admin panel, the directory and the client):

- Colors are `#rrggbb`. `dim` is 0–1, `blur` 0–24 px, `position` one of
  `center`, `top`, `bottom`, `left`, `right`. `radius` is 0–12, `font`
  `boson` or `system`, `nick.saturation`/`nick.lightness` 0–100.
- Unknown keys are dropped. Tokens you leave out come from Boson's built-in
  theme of the same `base`.
- Images: png, jpeg, webp, gif or svg, at most 2 MB and 4096 px on a side.
  An SVG is only ever shown as an image, never inlined, so scripts in it
  don't run. A file name in `branding.images` is a plain name inside
  `config/branding/` (no `/` or `..`).
- The whole theme document is at most 16 KB.

A theme that fails validation is treated like any broken config: auth keeps
the last good branding and reports the error in
`/internal/config/status`, and the admin panel shows it.

### What auth serves

`GET https://AUTH_HOST/v1/branding` is public (CORS `*`) and returns the
theme as a `boson-theme/1` document, with the images as absolute URLs:

```json
{
  "version": 1,
  "base": "dark",
  "colors": { "accent": "#e11d48", "bg": "#0c0a09" },
  "radius": 4,
  "panes": { "messages": { "image": "https://auth.example.com/v1/branding/assets/messages.webp?v=3f2a…", "dim": 0.7, "blur": 8, "position": "center" } },
  "logo": "https://auth.example.com/v1/branding/assets/logo.svg?v=9c1e…",
  "login_background": "https://auth.example.com/v1/branding/assets/login.webp?v=51d0…"
}
```

It has an `ETag`, and `If-None-Match` gets a `304`. Assets carry `?v=<hash>`
of their content and are cached for a long time. They're sent with
`X-Content-Type-Options: nosniff`, and an SVG also gets
`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`.

## Emails

`auth.email` in `config/boson.yaml` (the Emails page writes it):

```yaml
auth:
  email:
    sender_name: "Example Chat"        # From display name; default network.name
    accent: "#e11d48"                  # buttons and links
    footer: "Example Chat · example.com"
    logo: true                         # branding logo_wide, else logo
    templates:
      verification:
        subject: "Confirm your {{network}} account"
        body: "Hi {{username}},\n\nConfirm your email: {{link}}\n\nThe link works for {{expires}}."
```

- Templates: `verification`, `password_reset`, `account_setup`, `invite`.
  Set any of them, and the subject or the body alone; the rest keeps Boson's
  wording. Removing one (or **Reset to default**) restores the default.
- Variables: `{{network}}`, `{{username}}`, `{{link}}`, `{{expires}}`,
  `{{inviter}}` (invites). Anything else in `{{…}}` is left as typed.
- The body is plain text. A blank line starts a new paragraph and URLs
  become links. auth wraps it in Boson's HTML layout with your logo, accent,
  footer and sender name, and sends a plain-text part alongside. Everything
  is HTML-escaped, your text included, so HTML in a template shows up as
  text.
- The sending address and server stay in `auth.smtp` / `SMTP_*`, and the
  SMTP password stays in `.env`.

To try a template without the panel, use auth's internal API (compose
network only, `INTERNAL_API_TOKEN` from `.env`):

```sh
tok=$(sed -n 's/^INTERNAL_API_TOKEN=//p' .env)
docker run --rm --network boson_default curlimages/curl:8.22.0 -sS \
  -H "X-Internal-Token: $tok" -H 'Content-Type: application/json' \
  -d '{"template":"verification"}' http://auth:8081/internal/email/preview
# → {"subject": "…", "html": "…", "text": "…"}; add "subject"/"body" to preview unsaved text.
#   POST /internal/email/test {"template":"verification","to":"you@example.com"} sends one.
```

## In the client

The client looks for a network's theme in this order:

1. `branding` in the network's `/.well-known/boson.json` (the `wellknown`
   profile adds `"branding": "https://AUTH_HOST/v1/branding"` whenever it
   publishes the auth section);
2. else `branding_url` in auth's `/v1/config`;
3. else the `theme` of the network's listing in the directory.

It caches the theme per network (by ETag) and refreshes it on connect. Images
are fetched through the desktop app, not by the web view.

While the network is the active server, its theme is merged over the
user's own theme: colors, pane backgrounds and images, logo on the server's
rail tile and sidebar header, and the sign-in background and logo on
"Sign in to <network>". Switching servers switches themes.

**Readability guard:** if the network's text-on-background contrast is below
4.5:1, the client drops the network's text colors and keeps the user's.

**Opting out:** network themes are on by default. Users turn them off:

- for every network: Settings → Appearance → **Use network themes**;
- for one network: **Use this network's theme** in the server's context
  menu or its Server settings (stored on that device).

Guests see the theme too.

## In the directory

A stack network that's listed from the admin panel's **Directory** page
([docs/directory.md](directory.md)) has its branding pushed to its listing
when the panel syncs: the theme, plus the logo, sign-in and pane images
uploaded to the directory's media slots. The listing card and page then use
the network's accent, colors and banner, and the client applies the same
theme to people who found the network through the directory (step 3 above).
There's nothing to set up beyond the Directory page itself; edit the
branding in the panel and the next sync carries it.

Networks without the stack can set a theme on their listing in the
directory's web UI, or with its owner API
(`PUT /v1/networks/{id}/theme`, `PUT/DELETE /v1/networks/{id}/theme-images/{slot}`).
