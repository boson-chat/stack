#!/usr/bin/env bash
# A second, "home" Boson install for the add-on path's federation e2e
# (run-addon.sh, addon-federation.py): a fresh install at home.test whose
# user alice signs in to the add-on install (boson.test) with her home
# account. Modelled on test/federation/run.sh (not shared: that one is
# another test's):
#
#   test/setup/addon-home.sh up     # network, Pebble, the home install
#   test/setup/addon-home.sh down   # everything it started
#
# The home is a copy of this repo in $ADDON_HOME_DIR with scripts/setup.sh
# --local's .env, compose project "bosonhome", trimmed to auth + wellknown
# (federation needs nothing else at home). Its Traefik sits on a shared
# network ($ADDON_FED_NETWORK) under its hostnames, which is how the add-on
# install's auth (joined to that network by run-addon.sh's override) and
# Pebble find it; certificates come from that Pebble over HTTP-01. The test
# reaches it on 127.0.0.1:$ADDON_HOME_PORT (TLS, SNI). Pebble's root goes to
# $ADDON_HOME_DIR-ca/roots.pem, which the add-on's auth trusts.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
LABEL=org.boson.addon-fed=1
NET=${ADDON_FED_NETWORK:-bosonaddonfed}
SUBNET=${ADDON_FED_SUBNET:-10.99.63.0/24}
HOME_SUBNET=${ADDON_HOME_SUBNET:-10.99.64.0/24}
DIR=${ADDON_HOME_DIR:-${RUNNER_TEMP:-/tmp}/boson-addon-home}
CA=$DIR-ca
PORT=${ADDON_HOME_PORT:-18443}
P=bosonhome
PY=python:3.13-alpine

dc() { (cd "$DIR" && docker compose -p "$P" "$@"); }
setenv() { grep -v "^$2=" "$1" > "$1.tmp" || true; echo "$2=$3" >> "$1.tmp"; mv "$1.tmp" "$1"; }

down() {
	set +e
	[ -f "$DIR/.env" ] && dc down -v --remove-orphans >/dev/null 2>&1
	docker ps -aq --filter "label=$LABEL" | xargs -r docker rm -f >/dev/null 2>&1
	docker network ls -q --filter "label=$LABEL" | xargs -r docker network rm >/dev/null 2>&1
	# config/ belongs to uid 65532 by now (config-init)
	[ -d "$DIR" ] && docker run --rm --label "$LABEL" -v "$(dirname "$DIR"):/t" busybox:1.37.0 \
		rm -rf "/t/$(basename "$DIR")" "/t/$(basename "$CA")" >/dev/null 2>&1
	rm -rf "$DIR" "$CA"
	return 0
}

logs() {
	[ -f "$DIR/.env" ] || return 0
	echo "::group::home containers"; dc ps -a 2>&1 || true; echo "::endgroup::"
	for s in auth traefik wellknown; do
		echo "::group::home $s logs"; dc logs --no-log-prefix --tail 60 "$s" 2>&1 || true; echo "::endgroup::"
	done
	echo "::group::home pebble logs"; docker logs --tail 40 bosonhome-pebble 2>&1 || true; echo "::endgroup::"
}

up() {
	down
	echo "== home: network $NET and Pebble"
	mkdir -p "$CA"
	docker network create --label "$LABEL" --subnet "$SUBNET" "$NET" >/dev/null
	docker run -d --name bosonhome-pebble --label "$LABEL" --network "$NET" --network-alias pebble \
		-e PEBBLE_VA_NOSLEEP=1 -v "$ROOT/test/acme/pebble.json:/pebble.json:ro" \
		ghcr.io/letsencrypt/pebble:2.10.3 -config /pebble.json -dnsserver 127.0.0.11:53 -strict >/dev/null
	docker pull -q "$PY" >/dev/null
	for _ in $(seq 1 30); do
		docker run --rm --label "$LABEL" --network "$NET" "$PY" python3 -c '
import ssl, urllib.request
ctx = ssl._create_unverified_context()
print(urllib.request.urlopen("https://pebble:15000/roots/0", context=ctx, timeout=5).read().decode())' > "$CA/roots.pem" 2>/dev/null &&
			grep -q 'BEGIN CERTIFICATE' "$CA/roots.pem" && break
		sleep 1
	done
	grep -q 'BEGIN CERTIFICATE' "$CA/roots.pem" || { echo "no Pebble root"; exit 1; }
	chmod 755 "$CA"; chmod 644 "$CA/roots.pem"

	echo "== home: home.test (auth, wellknown)"
	mkdir -p "$DIR"
	git ls-files -z -co --exclude-standard | tar --null -T - -cf - | tar -xf - -C "$DIR"
	(cd "$DIR" && scripts/setup.sh --local >/dev/null)
	local d=home.test env=$DIR/.env
	for kv in "DOMAIN=$d" "NETWORK_NAME=Home" "IRC_HOST=irc.$d" "AUTH_HOST=auth.$d" "VOICE_HOST=voice.$d" \
		"FILES_HOST=files.$d" "DIRECTORY_HOST=directory.$d" "ADMIN_HOST=admin.$d" "API_HOST=api.$d" \
		"VIDEO_HOST=" "TLS_ISSUER=admin@$d" "ACME_CA_SERVER=https://pebble:14000/dir" "CERT_HOSTS=$d,auth.$d" \
		"STACK_SUBNET=$HOME_SUBNET" "NODE_EXPORTER_ADDR=${HOME_SUBNET%.0/24}.1" "COMPOSE_PROFILES=auth,wellknown" \
		"REQUIRE_EMAIL_VERIFICATION=false" "WELLKNOWN_REFRESH_SECONDS=5" "STACK_PROJECT=$P" \
		"ADDON_FED_NETWORK=$NET" "ADDON_HOME_PORT=$PORT" \
		"COMPOSE_FILE=compose.yaml:test/setup/compose.addon-home.yaml"; do
		setenv "$env" "${kv%%=*}" "${kv#*=}"
	done
	# config/boson.yaml: home's users may sign in elsewhere (any mode but off).
	python3 - "$DIR/config/boson.yaml" <<'PY'
import sys
path = sys.argv[1]
lines = open(path).read().split("\n")
i = lines.index("auth:")  # setup.sh --local writes an auth: section
lines[i + 1:i + 1] = ["  federation:", "    mode: allowlist", "    allow: [boson.test]"]
open(path, "w").write("\n".join(lines))
PY
	dc up -d --build --wait --wait-timeout 420
	echo "== home: certificates and boson.json"
	for _ in $(seq 1 60); do
		if python3 - "$PORT" <<'PY'
import json, socket, ssl, sys
port = int(sys.argv[1])
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
for host in ("home.test", "auth.home.test"):
    s = ctx.wrap_socket(socket.create_connection(("127.0.0.1", port), timeout=5), server_hostname=host)
    der = s.getpeercert(binary_form=True); s.close()
    if b"Pebble" not in der: sys.exit(1)
s = ctx.wrap_socket(socket.create_connection(("127.0.0.1", port), timeout=5), server_hostname="home.test")
s.sendall(b"GET /.well-known/boson.json HTTP/1.1\r\nHost: home.test\r\nConnection: close\r\n\r\n")
body = b""
while True:
    c = s.recv(65536)
    if not c: break
    body += c
fed = json.loads(body.partition(b"\r\n\r\n")[2] or b"{}").get("federation") or {}
sys.exit(0 if fed.get("issuer") == "https://auth.home.test" else 1)
PY
		then echo "home is up"; return 0; fi
		sleep 3
	done
	echo "home: no Pebble certificate or no federation section in boson.json"; exit 1
}

case ${1:-} in
up) up ;;
down) down ;;
logs) logs ;;
*) echo "usage: $0 up|down|logs" >&2; exit 2 ;;
esac
