#!/usr/bin/env bash
# The setup wizard's add-on path end to end, against one IRC server fixture:
#
#   test/setup/run-addon.sh PAIRING      ngircd-anope | unreal-anope | insp-atheme | ngircd
#
#   1. test/ircds/run.sh up PAIRING (published on this machine's address, so
#      the stack's containers reach it too), smoke (registers the NickServ
#      account bosonprobe);
#   2. install.sh --yes, then Playwright drives the add-on path
#      (addon.spec.ts): the capability table against the golden one, the
#      bridge, accounts, domain/TLS/admin/features/ports, the snippets
#      applied to the real server with test/ircds/run.sh apply, Re-check
#      red then green, review, install (Pebble certificates, fake DNS);
#   3. the result works (addon-result.py): Boson users over the bridge,
#      replies/reactions/history, a classic client on the IRC server sees
#      them with their own address, NickServ sign-in where auth has it, and
#      nothing a Boson user receives says "evrd".
#
# Needs sudo (install.sh runs as root). Cleans up what it started, by name
# and label.
set -euo pipefail
cd "$(dirname "$0")/../.."
PAIRING=${1:?pairing}
ROOT=$PWD
DIR=/opt/boson-stack
IMAGE=boson-setup:ci
LABEL=org.boson.e2e=1
TMP=${RUNNER_TEMP:-$(mktemp -d)}
IP=$(hostname -I | awk '{print $1}')
DOMAIN=boson.test
SETUP_ADMIN_PASSWORD="e2e-$(openssl rand -hex 8)"
export SETUP_ADMIN=alice SETUP_ADMIN_PASSWORD
export SHOTS_DIR=$ROOT/test/setup/shots
export IRCDS_BIND=$IP

dc() { sudo docker compose --project-directory "$DIR" -p boson "$@"; }
logs() {
	echo "::group::setup wizard logs"; sudo docker logs boson-setup 2>&1 | tail -150 || true; echo "::endgroup::"
	echo "::group::stack containers"; dc ps -a 2>&1 || true; echo "::endgroup::"
	for s in evrd evrd-links bridge-sync auth traefik certsync webircgateway; do echo "::group::$s logs"; dc logs --no-log-prefix --tail 80 "$s" 2>&1 || true; echo "::endgroup::"; done
	echo "::group::IRC server ($PAIRING)"; LOG_TAIL=120 test/ircds/run.sh logs "$PAIRING" 2>&1 || true; echo "::endgroup::"
	for c in $(sudo docker ps -q --filter label=chat.boson.test=ircds); do
		echo "::group::$(sudo docker inspect -f '{{.Name}}' "$c") log files"
		sudo docker exec "$c" sh -c 'tail -n 80 logs/ircd.log ircd.log var/atheme.log 2>/dev/null' || true
		echo "::endgroup::"
	done
}
cleanup() {
	set +e
	[ "${ok:-}" = 1 ] || logs
	sudo docker rm -f boson-setup >/dev/null 2>&1
	[ -f "$DIR/compose.yaml" ] && dc down -v --remove-orphans >/dev/null 2>&1
	test/ircds/run.sh down "$PAIRING" >/dev/null 2>&1
	sudo docker network rm boson-setup-irc-probe >/dev/null 2>&1
	for l in "$LABEL" org.boson.setup=probe; do
		sudo docker ps -aq --filter "label=$l" | xargs -r sudo docker rm -f >/dev/null
	done
}
trap cleanup EXIT

echo "== build $IMAGE and bridge-sync (not on Harbor until this merges)"
docker build -q -f images/setup/Dockerfile -t "$IMAGE" . >/dev/null
sudo docker build -q -t harbor.floret.dev/boson/stack-bridgesync:0.1.0 images/bridgesync >/dev/null

echo "== the IRC server: $PAIRING on $IP"
test/ircds/run.sh up "$PAIRING"
if [ "$PAIRING" != ngircd ]; then
	SMOKE_ARGS="--nick bosonprobe --password probe-fixture-pass" test/ircds/run.sh smoke "$PAIRING"
fi
eval "$(test/ircds/run.sh env "$PAIRING")"

echo "== test DNS (every name -> $IP) and Pebble"
docker run -d --name boson-e2e-dns --label "$LABEL" --network host ghcr.io/letsencrypt/pebble-challtestsrv:2.10.3 \
	-defaultIPv4 "$IP" -defaultIPv6 "" -dnsserver :8053 -management :8055 \
	-http01 "" -https01 "" -tlsalpn01 "" -doh "" >/dev/null
for _ in $(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:8055/ && break; sleep 1; done
sudo mkdir -p "$DIR/e2e"
sudo cp test/acme/pebble.json test/acme/pebble.minica.pem "$DIR/e2e/"
sudo tee "$DIR/compose.override.yaml" >/dev/null <<'YAML'
# setup e2e only: Pebble (Let's Encrypt's test CA), resolving through the
# test DNS server on the host (the stack network's gateway).
services:
  traefik:
    environment:
      LEGO_CA_CERTIFICATES: /pebble-ca/pebble.minica.pem
    volumes:
      - ./e2e/pebble.minica.pem:/pebble-ca/pebble.minica.pem:ro
  pebble:
    image: ghcr.io/letsencrypt/pebble:2.10.3
    command: ["-config", "/pebble.json", "-dnsserver", "10.203.53.1:8053", "-strict"]
    environment:
      PEBBLE_VA_NOSLEEP: "1"
    volumes:
      - ./e2e/pebble.json:/pebble.json:ro
YAML

(cd test/setup && npm ci --no-audit --no-fund >/dev/null && npx playwright install --with-deps chromium >/dev/null)

echo "== install.sh --yes"
sudo BOSON_SETUP_DNS_SERVER=127.0.0.1:8053 ./install.sh --yes --image "$IMAGE" --no-pull | tee "$TMP/install.out"
CODE=$(sed -n 's/.*enter code \([A-Z0-9-]*\).*/\1/p' "$TMP/install.out")
URL=$(sed -n 's/.*Open \(https:[^ ]*\) and.*/\1/p' "$TMP/install.out")
[ -n "$CODE" ] && [ -n "$URL" ] || { echo "install.sh printed no code/URL"; exit 1; }

# The account mode: the decision order's pick; NickServ accounts need an
# auth with the NickServ backend (addon-result.py checks what auth says).
# WANT: what the decision order must recommend; MODE: what's installed
# ("" = the recommendation). unreal-anope overrides it, so both modes run
# against a server with services; ngircd has none (Boson accounts).
case $PAIRING in
ngircd-anope) NETWORK=NgircdNet; WANT=nickserv; MODE= ; REACT=0 ;;
unreal-anope) NETWORK=UnrealNet; WANT=nickserv; MODE=boson; REACT=1 ;;
insp-atheme) NETWORK=InspNet; WANT=nickserv; MODE= ; REACT=0 ;;
ngircd) NETWORK=NgircdNet; WANT=boson; MODE= ; REACT=0 ;;
esac
ACCOUNTS=${ACCOUNTS:-$MODE}

echo "== the wizard's add-on path (Playwright)"
(cd test/setup && SETUP_URL=$URL SETUP_CODE=$CODE SETUP_DOMAIN=$DOMAIN SETUP_ACME_URL=https://pebble:14000/dir \
	SETUP_PAIRING=$PAIRING SETUP_IRC_HOST=$IRC_HOST SETUP_IRC_PORT=$IRC_TLS_PORT \
	SETUP_RPC_URL=${SERVICES_RPC_URL:-} SETUP_RPC_TOKEN=${SERVICES_RPC_TOKEN:-} SETUP_NETWORK=$NETWORK \
	SETUP_WANT_RECOMMENDED=$WANT SETUP_ACCOUNTS=$ACCOUNTS SETUP_REACT=$REACT SETUP_NICKSERV_ADMIN=bosonprobe \
	npx playwright test addon.spec.ts)

echo "== the installed stack works"
python3 test/setup/addon-result.py --domain "$DOMAIN" --admin "$SETUP_ADMIN" --password "$SETUP_ADMIN_PASSWORD" \
	--irc "$IRC_HOST:$IRC_TLS_PORT" --pairing "$PAIRING"
ok=1
echo "setup add-on e2e ($PAIRING) passed"
