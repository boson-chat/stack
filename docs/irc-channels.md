# Text channels: who can create them, roles and locks (Ergo 2.19)

Text channels are plain IRC channels on Ergo. The stack adds one thing on top:
**who may create channels** follows the auth service's network settings.
Everything else (roles, private or moderated channels) is standard Ergo,
using the commands below. The boson client can surface these as UI.

## Who can create channels: `channel_creation`

The auth service keeps the network setting (admin panel), served at
`GET http://auth:8081/internal/v1/settings` (`X-Internal-Token:
$INTERNAL_API_TOKEN`) as `{"channel_creation": "registered" | "admins", ...}`.

| `channel_creation` | Ergo config | Effect |
|---|---|---|
| `registered` | `channels.operator-only-creation: false`, `channels.registration.operator-only: false` | Anyone can create a channel by joining it; logged-in users can register it with ChanServ (`/msg ChanServ REGISTER #chan`) and become its founder |
| `admins` | both `true` | Only IRC operators with the `chanreg` capability can create or register channels. Others get `403 #chan :Only server operators can create new channels`. Existing channels are unaffected |

How a change reaches Ergo:

1. At container start `boson-ergo render` fetches the setting (10 s timeout)
   and renders `/ircd/ircd.yaml` with it. It remembers the last value it got
   in `/ircd/boson-settings.json` (the `ergo_db` volume), so if auth is down at
   start Ergo comes up with the last known policy rather than a default.
   With nothing remembered, `IRC_CHANNEL_CREATION` (default `registered`) is
   used.
2. `boson-ergo watch-settings` (started by the entrypoint) polls every
   `SETTINGS_POLL_SECONDS` (default 30). When the rendered `ircd.yaml`
   changes, it rewrites it and sends Ergo `SIGHUP`, Ergo's REHASH, which
   applies the new policy without disconnecting anyone. Failed polls change
   nothing. Logs: `docker compose logs ergo | grep watch-settings`.

So an admin-panel change takes effect on IRC within about
`SETTINGS_POLL_SECONDS`.

### "Logged-in users only" and guests

Ergo 2.19 has no switch for "only logged-in users may create channels":
under `registered`, a guest (not logged in) can still create an unregistered
channel by joining it. Only ChanServ registration (ownership) needs an
account. To make the network logged-in-only, set `IRC_REQUIRE_SASL=true`
(`accounts.require-sasl.enabled`): every client must authenticate with SASL
or `PASS user:password`.

### Auth admins and IRC operators

Ergo's operators are defined in `ircd.yaml` and authenticate with a password
(`/OPER`) or a TLS client certificate fingerprint. They can't be bound to an
account, and the auth-script can't grant oper status (its output is only
success/account name). So **auth-service admins are not automatically IRC
operators**. Under `admins`, channels are created by someone who has done
`/OPER $OPER_NAME <password>` (class `server-admin`, which has `chanreg`).
They can then hand a channel to a user:

```
/OPER admin <password>
/JOIN #newchannel
/msg ChanServ REGISTER #newchannel
/msg ChanServ TRANSFER #newchannel alice           # prints a confirmation code
/msg ChanServ TRANSFER #newchannel alice <code>    # does it
```

(For an oper with `chanreg` the transfer happens at once; for anyone else
the recipient must accept with `/msg ChanServ TRANSFER accept #newchannel`.)

Voice is different: quicvoiced reads the auth role straight from the voice
token, so auth admins *are* admins for voice channels (see
[voice.md](voice.md)).

## Roles in a channel

Ergo's channel privilege levels, highest first. These are also the roles
voice channels can be locked to (`min_role` / `speak_role`).

| Role | Prefix | Mode | What it can do |
|---|---|---|---|
| founder | `~` | `+q` | Registered owner (`ChanServ REGISTER`); everything, incl. ChanServ settings and any `AMODE` |
| admin | `&` | `+a` | Op powers, can't be kicked/deopped by ops (registered channels) |
| op | `@` | `+o` | Modes, kick, ban, invite, topic, grant op and below |
| halfop | `%` | `+h` | Kick (no ban), topic, grant voice |
| voice | `+` | `+v` | Talk when the channel is `+m` |
| member | | | Join and talk (unless the modes below say otherwise) |

Live modes (`/MODE #chan +o bob`) last until the user leaves. **Persistent
roles** are ChanServ AMODEs, applied every time the account joins (needs a
registered channel; you can grant levels below your own persistent level,
the founder any):

```
/msg ChanServ AMODE #chan +o bob      # make bob a permanent op
/msg ChanServ AMODE #chan +a carol    # channel admin
/msg ChanServ AMODE #chan +h dave     # halfop
/msg ChanServ AMODE #chan +v erin     # permanent voice
/msg ChanServ AMODE #chan -o bob      # remove
/msg ChanServ AMODE #chan             # list (persistent halfop and up, founder, chanreg opers)
/msg ChanServ TRANSFER #chan newowner # change founder (code + accept, see above)
/msg ChanServ INFO #chan              # founder, registration date
```

Account names are used (the stack forces nick = account name for logged-in
users).

## Locks for text channels

No custom enforcement is needed: Ergo's channel modes give the same effects
as voice-channel locks. Set them with `/MODE` (op or higher):

| Want | Commands |
|---|---|
| Only logged-in users can join | `/MODE #chan +R` |
| Only logged-in users can talk (guests can read) | `/MODE #chan +M` (voiced guests excepted) |
| Only voice and above can talk ("speak_role: voice") | `/MODE #chan +m`, then `AMODE`/`MODE +v` the speakers |
| Only specific accounts can join ("min_role" / private) | `/MODE #chan +i`, then `/MODE #chan +I alice` per account, or give them a persistent role (`/msg ChanServ AMODE #chan +v alice`) |
| Let ops see and approve messages from unprivileged users | `/MODE #chan +U` |
| Announcement channel (quiet joins/parts for members) | `/MODE #chan +mu` |
| Hide from `/LIST` and `/WHOIS` | `/MODE #chan +s` |
| Join with a password | `/MODE #chan +k <key>` |
| Mute one user without kicking | `/MODE #chan +b m:bob!*@*` |
| Cap the member count | `/MODE #chan +l 50` |

Remove any of them with `-` instead of `+`. Modes on a registered channel
are kept across restarts.

## Names

Channel names are IRC names: start with `#`, at most 64 bytes
(`limits.channellen`), no spaces, commas or BEL, case-insensitive (ASCII
casemapping, so `#Boson` = `#boson`). Ergo also rejects names that are
confusable with an existing channel (e.g. Unicode look-alikes). A channel's
name can't be changed; the topic (`/TOPIC #chan :text`) is its description.
