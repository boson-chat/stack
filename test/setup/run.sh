#!/usr/bin/env bash
# The setup wizard end to end, on a fresh (CI) machine with Docker:
#
#   1. install.sh --yes with the locally built stack-setup image;
#   2. Playwright drives the whole wizard (wizard.spec.ts), error cases
#      included, with the code install.sh printed. DNS is a real DNS server
#      with test records (pebble-challtestsrv, as in test/acme), the
#      certificates come from Pebble (as in test/acme/run.sh), mail goes to
#      Mailpit;
#   3. the result works (smoke-result.py: certificates, admin + a new user
#      over IRC, channel, history, voice token, upload; webclient.spec.ts);
#   4. install.sh again: same answers, same secrets, one admin (rerun.spec.ts).
#
# Needs sudo (install.sh runs as root; the hooks add an iptables rule).
# Cleans up everything it started, by name and label.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
DIR=/opt/boson-stack
IMAGE=boson-setup:ci
LABEL=org.boson.e2e=1
TMP=${RUNNER_TEMP:-$(mktemp -d)}
IP=$(hostname -I | awk '{print $1}')
DOMAIN=boson.test
BAD=net.elsewhere.test
SETUP_ADMIN_PASSWORD="e2e-$(openssl rand -hex 8)"
export SETUP_ADMIN=alice SETUP_ADMIN_PASSWORD
export SHOTS_DIR=$ROOT/test/setup/shots

dc() { sudo docker compose --project-directory "$DIR" -p boson "$@"; }
logs() {
	echo "::group::setup wizard logs"; sudo docker logs boson-setup 2>&1 | tail -150 || true; echo "::endgroup::"
	echo "::group::wizard state"; sudo cat "$DIR/.setup/state.json" 2>/dev/null | head -c 6000; echo "::endgroup::"
	echo "::group::stack containers"; dc ps -a 2>&1 || true; echo "::endgroup::"
	for s in ergo roleserv auth traefik; do echo "::group::$s logs"; dc logs --no-log-prefix --tail 60 "$s" 2>&1 || true; echo "::endgroup::"; done
	echo "::group::stack logs"; dc logs --tail 40 2>&1 | tail -400 || true; echo "::endgroup::"
}
cleanup() {
	set +e
	[ "${ok:-}" = 1 ] || logs
	bash test/setup/hooks.sh unblock-udp >/dev/null 2>&1
	bash test/setup/hooks.sh unblock-node >/dev/null 2>&1
	bash test/setup/hooks.sh free-6697 >/dev/null 2>&1
	sudo docker rm -f boson-setup >/dev/null 2>&1
	[ -f "$DIR/compose.yaml" ] && dc down -v --remove-orphans >/dev/null 2>&1
	for l in "$LABEL" org.boson.setup=probe; do
		sudo docker ps -aq --filter "label=$l" | xargs -r sudo docker rm -f >/dev/null
	done
}
trap cleanup EXIT

echo "== build $IMAGE"
docker build -q -f images/setup/Dockerfile -t "$IMAGE" . >/dev/null

echo "== test DNS (every name -> $IP; *.$BAD -> 203.0.113.9) and Mailpit"
docker run -d --name boson-e2e-dns --label "$LABEL" --network host ghcr.io/letsencrypt/pebble-challtestsrv:2.10.3 \
	-defaultIPv4 "$IP" -defaultIPv6 "" -dnsserver :8053 -management :8055 \
	-http01 "" -https01 "" -tlsalpn01 "" -doh "" >/dev/null
docker run -d --name boson-e2e-mail --label "$LABEL" --network host \
	-e MP_SMTP_AUTH_ACCEPT_ANY=1 -e MP_SMTP_AUTH_ALLOW_INSECURE=1 axllent/mailpit:v1.33.0 >/dev/null
for _ in $(seq 1 30); do curl -fsS -o /dev/null http://127.0.0.1:8055/ 2>/dev/null && break; curl -s -o /dev/null http://127.0.0.1:8055/ && break; sleep 1; done
for h in irc auth chat admin api voice files; do
	curl -fsS -X POST -d "{\"host\":\"$h.$BAD.\",\"addresses\":[\"203.0.113.9\"]}" http://127.0.0.1:8055/add-a
done

for q in "irc.$DOMAIN A" "irc.$DOMAIN AAAA" "irc.$BAD A" "irc.$BAD AAAA"; do
	# shellcheck disable=SC2086
	echo "dig $q: $(dig +time=2 +tries=1 @127.0.0.1 -p 8053 $q | grep -E 'status:|IN\s+(A|AAAA)\s' | tr -s ' \t' ' ' | tr '\n' ' ')"
done

echo "== Pebble as the ACME CA (compose.override.yaml in $DIR, like test/acme)"
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
pw() { (cd test/setup && npx playwright test "$@"); }
start_wizard() { # start_wizard OUTFILE: install.sh, then CODE and URL from what it printed
	sudo BOSON_SETUP_DNS_SERVER=127.0.0.1:8053 BOSON_SETUP_GITHUB_TOKEN="${BOSON_SETUP_GITHUB_TOKEN:-}" ./install.sh --yes --image "$IMAGE" --no-pull | tee "$1"
	CODE=$(sed -n 's/.*enter code \([A-Z0-9-]*\).*/\1/p' "$1")
	URL=$(sed -n 's/.*Open \(https:[^ ]*\) and.*/\1/p' "$1")
	[ -n "$CODE" ] && [ -n "$URL" ] || { echo "install.sh printed no code/URL"; exit 1; }
	grep -q "^  Open https://$IP:8443 and enter code [A-Z0-9]\{4\}-[A-Z0-9]\{4\}$" "$1" || { echo "unexpected install.sh output"; exit 1; }
	# The code is never in the wizard's logs.
	if sudo docker logs boson-setup 2>&1 | grep -qF "$CODE"; then echo "the code is in the logs"; exit 1; fi
}

echo "== 1. install.sh --yes"
start_wizard "$TMP/install1.out"
[ "$(sudo docker inspect -f '{{.HostConfig.NetworkMode}} {{.HostConfig.RestartPolicy.Name}}' boson-setup)" = "host no" ]

echo "== 2. the wizard (Playwright)"
SETUP_URL=$URL SETUP_CODE=$CODE SETUP_DOMAIN=$DOMAIN SETUP_BAD_DOMAIN=$BAD \
	SETUP_ACME_URL=https://pebble:14000/dir SETUP_SMTP_HOST=$IP SETUP_SMTP_PORT=1025 pw wizard.spec.ts
curl -fsS "http://127.0.0.1:8025/api/v1/search?query=subject:%22test%20email%20from%20Boson%20setup%22" |
	python3 -c 'import sys,json; m=json.load(sys.stdin)["messages"]; assert m, "no test email in Mailpit"; print("test email in Mailpit:", m[0]["Subject"])'
for _ in $(seq 1 20); do [ "$(sudo docker inspect -f '{{.State.Running}}' boson-setup 2>/dev/null)" = true ] || break; sleep 1; done
[ "$(sudo docker inspect -f '{{.State.Running}}' boson-setup)" = false ] || { echo "the wizard is still running after Close"; exit 1; }
if curl -sk -m 3 "$URL/api/session" >/dev/null; then echo "the wizard still answers after Close"; exit 1; fi
echo "PASS the wizard switched itself off"

echo "== files and permissions"
[ "$(sudo stat -c %a "$DIR/.env")" = 600 ] || { echo ".env is not 0600"; exit 1; }
[ "$(sudo stat -c %a "$DIR/.setup/state.json")" = 600 ] || { echo "state.json is not 0600"; exit 1; }
[ "$(sudo stat -c %a "$DIR/.setup")" = 700 ] || { echo ".setup is not 0700"; exit 1; }
python3 - "$DIR" <<'PY'
import json, re, subprocess, sys
d = sys.argv[1]
env = subprocess.run(["sudo", "cat", f"{d}/.env"], capture_output=True, text=True).stdout
state = subprocess.run(["sudo", "cat", f"{d}/.setup/state.json"], capture_output=True, text=True).stdout
logs = subprocess.run(["sudo", "docker", "logs", "boson-setup"], capture_output=True, text=True)
logs = logs.stdout + logs.stderr
secret_keys = ("PASSWORD", "TOKEN", "SECRET", "KEY", "HASH")
vals = [v.strip("'") for k, v in re.findall(r"^([A-Z0-9_]+)=(.*)$", env, re.M) if any(s in k for s in secret_keys) and len(v.strip("'")) >= 8]
assert len(vals) >= 15, vals
for v in vals:
    assert v not in state, "a secret is in state.json"
    assert v not in logs, "a secret is in the wizard's logs"
print(f"PASS {len(vals)} secrets: none in state.json or the wizard's logs")
PY

echo "== image versions and applying updates in .env"
python3 - "$DIR" "$ROOT/compose.yaml" <<'PY'
import re, subprocess, sys
d, compose = sys.argv[1], open(sys.argv[2]).read()
env = dict(re.findall(r"^([A-Z0-9_]+)=(.*)$", subprocess.run(["sudo", "cat", f"{d}/.env"], capture_output=True, text=True).stdout, re.M))
defaults = dict(re.findall(r"\$\{([A-Z0-9_]+_IMAGE):-([^}\s]+)\}", compose))
def ver(ref):
    return tuple(int(x) for x in ref.rsplit(":", 1)[1].split("-")[0].split("."))
# Every component this install runs is pinned in .env, at its compose
# default or newer (the wizard picks the newest release on the registry).
for k in ("AUTH_IMAGE", "API_IMAGE", "WEB_IMAGE", "ADMIN_IMAGE", "QUICVOICE_IMAGE", "FILES_IMAGE"):
    assert k in env, f"{k} not in .env"
    assert env[k].rsplit(":", 1)[0] == defaults[k].rsplit(":", 1)[0], (k, env[k])
    assert ver(env[k]) >= ver(defaults[k]), (k, env[k], defaults[k])
    print(f"PASS {k}={env[k]} (compose default {defaults[k].rsplit(':', 1)[1]})")
assert "updates" in env["COMPOSE_PROFILES"].split(","), env["COMPOSE_PROFILES"]
assert env.get("ADMIN_UPDATES_DOCKER_PROXY_URL") == "http://docker-proxy-updates:2375"
print("PASS applying updates is on (updates profile + ADMIN_UPDATES_DOCKER_PROXY_URL)")
PY
[ "$(sudo docker inspect -f '{{.State.Running}}' boson-docker-proxy-updates-1)" = true ] || { echo "docker-proxy-updates isn't running"; exit 1; }

echo "== app discovery: https://$DOMAIN/.well-known/boson.json"
curl -fsSk --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/.well-known/boson.json" | python3 -c '
import json, sys
d, dom = json.load(sys.stdin), sys.argv[1]
assert d["irc"]["host"] == "irc." + dom, d
assert d["auth"]["url"] == "https://auth." + dom, d
assert d.get("federation", {}).get("issuer") == "https://auth." + dom, d
print("PASS boson.json names irc, auth and federation")' "$DOMAIN"

echo "== 3. the installed stack works"
python3 test/setup/smoke-result.py --domain "$DOMAIN" --admin "$SETUP_ADMIN" --password "$SETUP_ADMIN_PASSWORD" --user-file "$TMP/user"
SETUP_DOMAIN=$DOMAIN pw webclient.spec.ts

echo "== 4. install.sh again: idempotent"
secrets() { sudo grep -E '^([A-Z]+_IMAGE|ADMIN_UPDATES_DOCKER_PROXY_URL|COMPOSE_PROFILES|POSTGRES_PASSWORD|AUTH_KEY_ENCRYPTION_KEY|INTERNAL_API_TOKEN|ERGO_API_TOKEN|OPER_PASSWORD_HASH|ADMIN_SESSION_KEY|FILES_DB_PASSWORD|VOICE_JWT_SECRET|WEBIRC_PASSWORD)=' "$DIR/.env" | sha256sum; }
before=$(secrets)
cfg_before=$(sudo sha256sum "$DIR/config/boson.yaml")
start_wizard "$TMP/install2.out"
SETUP_URL=$URL SETUP_CODE=$CODE SETUP_DOMAIN=$DOMAIN pw rerun.spec.ts
[ "$(secrets)" = "$before" ] || { echo "re-run changed secrets, image versions or profiles in .env"; exit 1; }
[ "$(sudo sha256sum "$DIR/config/boson.yaml")" = "$cfg_before" ] || { echo "re-run changed config/boson.yaml"; exit 1; }
echo "PASS re-run kept .env secrets and config/boson.yaml"
python3 test/setup/smoke-result.py --domain "$DOMAIN" --admin "$SETUP_ADMIN" --password "$SETUP_ADMIN_PASSWORD" --user-file "$TMP/user"

ok=1
echo "setup e2e passed"
