# Ergo patch: account-bound operator status

The stack builds Ergo v2.19.1 from source (commit
`63c743a70644f0f19109508ade8580bf37f7d23d`, checked at build time) with
[images/ergo/patches/0001-accounts-account-bound-operator-status-and-vhosts.patch](../images/ergo/patches/0001-accounts-account-bound-operator-status-and-vhosts.patch)
applied (`git apply`, see [images/ergo/Dockerfile](../images/ergo/Dockerfile)).
It is what makes [network roles](roles.md) IRC operators without `/OPER`.

## Why a patch

Ergo 2.19.1 has no way to make an account an operator:

- `opers:` blocks authenticate with a password and/or a certfp. `auto: true`
  only works with a certfp (`attemptAutoOper`), not with an account.
- The auth-script's output is `{success, accountName, error}`; nothing else
  is read.
- The HTTP API (`/v1/*`) has no oper or vhost endpoint (`/v1/ns/saset` covers
  always-on, auto-away, email, replay-joins).
- HostServ vhosts can be set by an oper (`HS SET`) and apply to connected
  clients, but that needs an opered IRC connection and covers vhosts only.

Rendering one `opers:` block per staff member with a generated password
would need a password per user that the user never sees, and still a
`/OPER`. So the patch adds account-bound opers, in the place Ergo already
delegates account decisions to: the auth-script.

## What the patch does

One new file (`irc/accountopers.go`, ~270 lines), its tests (~185), and
~90 lines of hooks in existing files (`accounts.go`, `api.go`,
`authscript.go`, `client.go`, `config.go`, `handlers.go`, `server.go`):

1. **auth-script output** may include `operClass` (an `oper-classes` name;
   `""` = not an operator), `whoisLine` (overrides the class's) and `vhost`
   (stored as the account's vhost like `HS SET`; `""` removes it). All
   optional: an existing script changes nothing. Applies to every
   auth-script login path: passphrase (SASL PLAIN, `PASS`, NickServ
   IDENTIFY), OAUTHBEARER via the script, and certfp/cookies via the script.
2. The oper class is applied **when registration completes** (via the
   session, so it also works when the session attaches to an existing
   always-on client), or immediately for a login after registration. It is
   handed over only from `sendSuccessfulAccountAuth`, so a login that is
   undone afterwards (the nick-equals-account check) grants nothing. The
   vhost is written only when it changes, and a user's `HS OFF` is kept.
   Unknown classes and invalid vhosts are logged and skipped; the login
   still succeeds.
3. **`POST /v1/account_grants`** `{accountName, operClass?, whoisLine?,
   vhost?}`: the same grants, applied now to the account and all its
   connected clients (oper up with 381/MODE, de-oper with MODE -o, switch
   class silently). → `{success, clients, errorCode?}` with
   `ACCOUNT_DOES_NOT_EXIST`, `UNKNOWN_OPER_CLASS`, `INVALID_VHOST`,
   `FEATURE_DISABLED`.
4. **Rehash**: account-bound opers are re-resolved against the new
   `oper-classes`: capability and whois-line changes apply at once, and they
   lose operator status if their class is gone.
5. `/OPER` opers are never touched by any of it. Account-bound opers have no
   password, certfp or `auto`, are not `hidden`, and are named after the
   account in logs ("alice opered up as alice").
6. Docs: `docs/MANUAL.md` (auth-script section) and `docs/API.md`.

Tests (`irc/accountopers_test.go`, Ergo's style: table/unit tests on
`Config`): output parsing (absent vs `""`), building the oper (class
whois-line vs granted, control characters stripped, unknown class),
rehash refresh (new capabilities, granted whois-line kept, class removed →
de-oper; `/OPER` opers ignored), vhost validation. `go test ./...` passes on
the patched tree.

Exercised against a running Ergo (test/roles/run.sh, and by hand): SASL
PLAIN and OAUTHBEARER grants, NickServ IDENTIFY after registration, the
nick-fixup case (IDENTIFY as an account that is already connected: logged
out again, not opered), live grant/revoke/class switch through the API,
rehash with a changed and with a removed class.

An unrelated finding while testing: in `oper-classes`, the WHOIS line key is
`whoisline` (Ergo's `OperClassConfig.WhoisLine` has no yaml tag), while in
`opers` it is `whois-line`; `default.yaml` uses neither for classes. The
stack renders `whoisline`. Worth a separate upstream fix (accept both).

## Upstreaming

The patch is self-contained and written against upstream conventions (MIT
header, no boson specifics, docs in MANUAL/API). A clean branch is kept as
`account-opers` on a local clone of ergochat/ergo (one commit on top of
`v2.19.1`; `git format-patch` of it is the file above). It applies cleanly
to current upstream `master` (checked at fd87086a: builds, `go test ./irc/`
passes).
No upstream issue or PR has been opened: that is the owner's call.

### Proposal text (draft, for an issue on ergochat/ergo)

> **Account-bound operator status from the auth-script**
>
> When accounts live in an external system through `auth-script`, that
> system usually also knows who the network's staff are. Today the only way
> to make them IRC operators is an `opers:` block per person with a password
> or certfp and an explicit `/OPER`, kept in sync with the external system
> by hand.
>
> Proposal: let a successful auth-script response carry optional
> `operClass`, `whoisLine` and `vhost` keys. With `operClass` set to an
> existing `oper-classes` entry, the client is opered up into that class
> when it completes registration (or immediately, for `NS IDENTIFY` after
> registration). There is no oper name or credential: the status is bound
> to the login, the same way the account is. `vhost` sets the account's
> HostServ vhost. Scripts that don't send the keys are unaffected, and
> `/OPER` operators are never touched.
>
> Two companions make it manageable at runtime: an API endpoint,
> `POST /v1/account_grants {accountName, operClass, whoisLine, vhost}`,
> applying the same grants to an account's connected clients immediately
> (so a demotion in the external system takes effect without a
> reconnect), and re-resolving account-bound operators on rehash, so that
> editing or removing their oper class applies to them.
>
> The boson chat stack uses this patch with an auth service that has
> Discord-style roles (each role maps to an oper class). Happy to adjust the
> naming or split it up (e.g. auth-script part first, API second) if that's
> easier to review. Patch: (link)
