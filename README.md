# boson stack

A self-hostable chat server in one `docker compose up`: IRC, IRC services,
voice, and automatic TLS, wired together for your own domain.

| Service | What it does | Ports |
|---|---|---|
| `caddy` | Gets and renews TLS certs for every hostname (Let's Encrypt, or a local CA for dev) | 80, 443 |
| `certsync` | Copies Caddy's certs to IRC and voice with the right ownership; they hot-reload | – |
| `unrealircd` | IRC server (UnrealIRCd 6) | 6697 (TLS), 6667 (optional) |
| `anope` | IRC services: NickServ, ChanServ, … (Anope 2.0), linked to unrealircd over TLS | internal |
| `quicvoiced` | Voice rooms over QUIC ([boson-chat/quicvoice](https://github.com/boson-chat/quicvoice)) | 7700/udp |

Planned: an auth service (accounts, SSO via OIDC/SAML, voice tokens, IRC SASL
against the same accounts), video, an admin web panel, and the desktop
client configured from your domain.

## Quick start

```sh
scripts/setup.sh            # asks for your domain etc., generates every secret into .env
docker compose up -d --build
```

Before starting on a real server:

1. DNS A records for your IRC and voice hostnames → this server.
2. Open 80/tcp and 443/tcp (certificates), 6697/tcp (IRC), 7700/udp (voice).
3. Optional: SMTP settings in `.env` so NickServ can send registration email.

Then connect any IRC client to `irc.<your domain>:6697` with TLS.

### Trying it locally

```sh
scripts/setup.sh --local    # irc.localhost / voice.localhost, self-signed TLS
docker compose up -d --build
scripts/smoke-irc.py        # TLS connect + NickServ reply
```

## Configuration

Everything lives in `.env` (see `.env.example`). The IRC and services config
files are generated inside their containers at every start from those values,
so changing `.env` and running `docker compose up -d` applies it:

- **UnrealIRCd:** `images/unrealircd/unrealircd.conf.tmpl`, rendered by
  `entrypoint.sh`. Mount extra config and set `EXTRA_CONF` to include it.
- **Anope:** Anope's stock `example.conf` plus a handful of substitutions in
  `images/anope/entrypoint.sh`. The entrypoint fails loudly if a substitution
  stops matching after an Anope upgrade.

Data (channel/ban DBs, Anope's NickServ/ChanServ DB, certificates) is kept in
named Docker volumes.

### Voice tokens

quicvoiced only accepts signed room tokens. Until the auth service exists,
mint them with the server binary and `VOICE_JWT_SECRET` from `.env`:

```sh
docker compose exec quicvoiced ./quicvoiced token --user alice --room lobby
```

## Notes

- **UDP buffers:** quicvoiced logs a warning unless the host allows larger UDP
  buffers: `sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000`.
- **Server links use TLS** even inside the compose network, because UnrealIRCd
  denies plaintext server links.
- **Images** are built from `images/` and published to
  `ghcr.io/boson-chat/stack-*` by CI on every push to `main`.
