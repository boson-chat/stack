# Friends and private calls

The boson app has friends (requests, blocks), privacy settings ("Who can
call me": Nobody / Friends / Everyone, Do not disturb, "Who can send me
friend requests") and private 1:1 voice and video calls. Calls ring **only
while the app is open**: no push notifications, no CallKit; missed calls are
shown the next time the app is opened.

Nothing new runs for it:

| Piece | Where | |
|---|---|---|
| Friends, blocks, settings, call state, the policy | `auth` (1.19+) | Postgres tables from auth's migration 12 (run automatically at start, `AUTO_MIGRATE`). Design and API: [auth docs/friends-calls.md](https://github.com/boson-chat/auth/blob/main/docs/friends-calls.md) |
| Ringing an open app | `auth` | A long-poll event feed, `GET https://AUTH_HOST/v1/events` (held up to 25 s). Traefik's timeouts (`TRAEFIK_READ_TIMEOUT` 35m, idle 180 s) already fit. In memory: run one `auth` replica (the stack does) |
| The call itself | `quicvoiced` (1.14+) + `quicvideod` | A private room `call:<call id>`: only the two people's tokens (minted by auth once the call is answered) get in; it's unlisted and gone when empty. Video works as in a voice channel (same room id on quicvideod) |

No new ports, DNS names or secrets. Config:

| Env (auth) | Default | |
|---|---|---|
| `ERGO_API_URL` | `http://ergo:8089` (`AUTH_ERGO_API_URL`) | Ergo's API, for "People in my servers" (two people share an IRC channel) |
| `ERGO_API_TOKEN` | `ERGO_API_TOKEN` | The same Ergo API token quicvoiced and RoleServ use. Empty: "People in my servers" lets nobody send requests |
| `EVENTS_POLL_WAIT` | `25s` | How long an event poll is held; keep it under 30 s |

Calls between people on different networks aren't possible: a call is
between two accounts of one auth service, on that network's voice server.
