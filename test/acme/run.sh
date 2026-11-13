#!/usr/bin/env bash
# End-to-end ACME test against Pebble, in two phases that mirror the
# production upgrade (docs/deploy-boson-chat.md):
#   A. voice + auth + webroot: Traefik gets certs for voice/auth over
#      HTTP-01, certsync exports voice's; irc.<domain> challenges are served
#      from a webroot (a host certbot's), not by Traefik.
#   B. irc: Traefik gets irc's cert, certsync hands it to Ergo, and the IRC
#      smoke test (auth signup + SASL) passes over that cert.
# Needs a .env from scripts/setup.sh --local. Cleans up with `down -v`.
set -euo pipefail
cd "$(dirname "$0")/../.."

export COMPOSE_FILE=compose.yaml:test/acme/compose.pebble.yaml
# Keep building auth from source if .env says so (no Harbor release yet).
! grep -q '^COMPOSE_FILE=.*compose.auth-src.yaml' .env || COMPOSE_FILE="$COMPOSE_FILE:compose.auth-src.yaml"
WEBROOT=$(mktemp -d)
mkdir -p "$WEBROOT/.well-known/acme-challenge"
echo certbot-ok > "$WEBROOT/.well-known/acme-challenge/token123"
chmod -R a+rX "$WEBROOT"
envf=$(mktemp)
{ cat test/acme/acme.env; echo "WEBROOT_DIR=$WEBROOT"; } > "$envf"
dc() { docker compose --env-file .env --env-file "$envf" "$@"; }
all="--profile irc --profile voice --profile auth --profile webroot"
cleanup() { dc $all down -v >/dev/null 2>&1 || true; rm -rf "$WEBROOT" "$envf"; }
trap cleanup EXIT
fail() { echo "FAIL: $*"; dc $all logs --tail 50 traefik certsync ergo auth 2>&1 | tail -120; exit 1; }
wait_for() { # wait_for SECONDS DESCRIPTION CMD...
	local t=$1 what=$2; shift 2
	for _ in $(seq 1 "$t"); do "$@" >/dev/null 2>&1 && { echo "PASS $what"; return 0; }; sleep 1; done
	fail "$what"
}
issuer() { echo | openssl s_client -connect "127.0.0.1:$1" -servername "$2" 2>/dev/null | openssl x509 -noout -issuer; }
export -f issuer

echo "== phase A: voice + auth + webroot (IRC cert managed elsewhere)"
export COMPOSE_PROFILES=voice,auth,webroot CERT_HOSTS=voice.boson.test,auth.boson.test
dc up -d --build pebble challtestsrv traefik certsync postgres auth quicvoiced webroot
wait_for 120 "auth cert from Pebble" bash -c "issuer 443 auth.boson.test | grep -q Pebble"
wait_for 90 "certsync exported voice cert" bash -c "docker compose --env-file .env --env-file $envf logs certsync | grep -q 'updated voice.boson.test'"
wait_for 10 "webroot serves certbot's challenge for irc host" bash -c \
	"curl -fsS -H 'Host: irc.boson.test' http://127.0.0.1/.well-known/acme-challenge/token123 | grep -q certbot-ok"
wait_for 10 "http redirects to https" bash -c \
	"curl -s -o /dev/null -w '%{http_code}' -H 'Host: auth.boson.test' http://127.0.0.1/v1/config | grep -q 301"

echo "== phase B: irc on Ergo"
dc $all stop webroot >/dev/null
export COMPOSE_PROFILES=irc,voice,auth CERT_HOSTS=irc.boson.test,voice.boson.test,auth.boson.test
dc up -d --build
wait_for 150 "Ergo serves the ACME cert" bash -c "issuer 6697 irc.boson.test | grep -q Pebble"
python3 scripts/smoke-irc.py --ca none --irc-host irc.boson.test --auth-host auth.boson.test || fail "IRC smoke"
echo "ACME test passed"
