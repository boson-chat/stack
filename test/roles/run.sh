#!/bin/sh
# Network roles smoke test against a real Ergo (the patched stack image).
#   test/roles/run.sh fake               # fake auth: full contract (RoleServ writes, grants feed)
#   AUTH_SRC=../auth test/roles/run.sh real   # a boson-chat/auth >= 1.5 checkout
# Leaves nothing running (KEEP=1 to keep it up for poking at).
set -eu
cd "$(dirname "$0")"
mode=${1:-fake}
case $mode in fake|real) ;; *) echo "usage: $0 fake|real" >&2; exit 2 ;; esac

tmp=$(mktemp -d)
export TEST_CERTS="$tmp/certs" TEST_CONFIG_DIR="$tmp/config" INTERNAL_API_TOKEN=$(openssl rand -hex 32) ERGO_API_TOKEN=$(openssl rand -hex 32) \
  ACTING_TOKEN=$(openssl rand -hex 32) AUTH_KEK=$(openssl rand -base64 32)
mkdir -p "$TEST_CERTS"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=irc.test \
  -keyout "$TEST_CERTS/privkey.pem" -out "$TEST_CERTS/fullchain.pem" 2>/dev/null
chmod 644 "$TEST_CERTS"/*.pem
# The stack config file (smoke-roles.py rewrites it). Plaintext is on here
# and off in the env: Ergo only listens on 6667 if the file is read.
mkdir -p "$TEST_CONFIG_DIR" && chmod 755 "$TEST_CONFIG_DIR"
printf 'version: 1\nnetwork: {name: Boson}\nirc:\n  plaintext: true\n' > "$TEST_CONFIG_DIR/boson.yaml"
chmod 644 "$TEST_CONFIG_DIR/boson.yaml"
[ "$mode" = real ] && AUTH_SRC=$(cd "${AUTH_SRC:?set AUTH_SRC to a boson-chat/auth checkout}" && pwd) && export AUTH_SRC

export COMPOSE="docker compose -f compose.yaml -f compose.$mode.yaml"
cleanup() {
  status=$?
  [ $status -eq 0 ] || $COMPOSE logs --no-color ergo roleserv auth 2>&1 | tail -n 150
  [ "${KEEP:-}" = 1 ] || $COMPOSE down -v --remove-orphans >/dev/null 2>&1
  rm -rf "$tmp"
}
trap cleanup EXIT
$COMPOSE up -d --build --wait --wait-timeout 300 ergo roleserv
python3 smoke-roles.py "$mode"
