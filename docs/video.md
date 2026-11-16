# Video in voice channels

The `video` profile runs [quicvideod](https://github.com/boson-chat/quicvideo):
camera and screen share for voice channels. Each voice channel has a video
room with the same id. In the app it is one feature: join a voice channel,
then turn on your camera or share your screen.

```
app ── in voice channel "general" ── VideoTicketRequest ──▶ quicvoiced
app ◀── ticket (5 min, rooms=[general]) + quic://VIDEO_HOST:7710 ──
app ── QUIC / WebTransport, ticket ──▶ quicvideod  (checks quicvoiced's JWKS)
quicvoiced ── leaves, deleted channels, snapshots every 30 s ──▶ quicvideod
```

- **Who can be in a video room**: whoever is in the voice channel. quicvoiced
  only hands tickets to people in a channel, for that channel. People who can
  speak there may publish; listeners can only watch.
- **Leaving voice leaves video**: quicvoiced tells quicvideod straight away
  (the control link), and resends full membership every 30 s so a restart
  of either side heals.
- **Tokens**: quicvideod trusts only quicvoiced's video tickets, signed with
  an Ed25519 key kept in the `voice_data` volume (`/data/video-ticket.key`).
  The public key is at `https://VOICE_HOST/.well-known/video-jwks.json`.

Needs quicvoice **1.8.0** or later (`QUICVOICE_IMAGE`) and quicvideo
**1.7.2** or later (`QUICVIDEO_IMAGE`).

## On the same server

In `.env` (`scripts/setup.sh` asks for the host when you pick `video`):

```sh
COMPOSE_PROFILES=irc,voice,auth,video
VIDEO_HOST=video.example.com          # DNS → this server
CERT_HOSTS=...,video.example.com
VIDEO_CONTROL_TOKEN=<openssl rand -hex 32>
```

then `docker compose up -d`. Open **7710/udp** (QUIC for the apps, and
HTTP/3 WebTransport at `https://VIDEO_HOST:7710/wt` for browsers).
`VIDEO_HOST` being set is what turns video on in quicvoiced: leave it unset
and nothing changes.

quicvoiced reaches quicvideod's control listener at `http://quicvideod:8091`
on the compose network; it is never routed. `https://VIDEO_HOST` serves
quicvideod's `/healthz` and `/v1/info`.

## On its own box (video node)

Video is the heavy part (bandwidth goes up with every viewer, about 1-2
Mbps per camera per viewer), so it can run elsewhere. On the video box,
with this repo checked out:

```sh
COMPOSE_FILE=compose.yaml:compose.video-node.yaml
COMPOSE_PROFILES=video
VIDEO_HOST=video.example.com
CERT_HOSTS=video.example.com
TLS_ISSUER=admin@example.com
VOICE_HOST=voice.example.com          # the voice server, for its JWKS
VIDEO_CONTROL_TOKEN=<same as the voice server>
```

Open 80/tcp, 443/tcp (certificate, control link) and 7710/udp. The node
fetches the ticket key from `https://VOICE_HOST/.well-known/video-jwks.json`,
and serves the control listener at `https://VIDEO_HOST/control/`
(bearer token).

On the voice server, without the `video` profile:

```sh
VIDEO_HOST=video.example.com
VIDEO_CONTROL_URL=https://video.example.com/control
VIDEO_CONTROL_TOKEN=<same>
```

## Settings

| `.env` | Default | |
|---|---|---|
| `VIDEO_HOST` | – | quicvideod's public host. Set = video on |
| `VIDEO_CONTROL_TOKEN` | – | Shared secret for the control link. quicvideod won't start without it |
| `VIDEO_CONTROL_URL` | `http://quicvideod:8091` | Where quicvoiced sends membership |
| `VIDEO_QUIC_URL`, `VIDEO_WEBTRANSPORT_URL` | `quic://VIDEO_HOST:7710`, `https://VIDEO_HOST:7710/wt` | What apps and browsers are told |
| `VIDEO_TICKET_JWKS_URL` | `http://quicvoiced:8090/...` (node: `https://VOICE_HOST/...`) | Where quicvideod gets the ticket key |
| `VIDEO_MAX_PARTICIPANTS` | 20 | Per video room |
| `VIDEO_WEBTRANSPORT_ORIGINS` | `*` | Browser origins allowed to open WebTransport sessions |
| `QUICVIDEO_IMAGE` | `harbor.floret.dev/boson/quicvideo:1.8.1` | |

Metrics: the `metrics` profile scrapes `quicvideod:8080/metrics`.

## Checking it

```sh
curl -s https://VOICE_HOST/.well-known/video-jwks.json   # one Ed25519 key
curl -s https://VIDEO_HOST/v1/info                        # quic_port, webtransport_path
docker compose logs quicvoiced | grep "video for voice channels on"
```
