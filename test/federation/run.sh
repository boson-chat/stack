#!/usr/bin/env bash
# Federated login end to end between two full Boson installs on one machine
# (docs/federation.md):
#
#   home    home.test    alice's account; issues the assertion
#   remote  remote.test  accepts verified servers (auth.federation.mode:
#                        directory) from its own directory (directory
#                        profile, fake DNS verifier), where irc.home.test
#                        is listed
#
# Each install is a copy of this repo in its own directory and compose
# project (-p home / -p remote), with scripts/setup.sh --local's .env plus
# test/federation/compose.fed.yaml: no published ports, both Traefiks on a
# shared network (bosonfed) with their hostnames as aliases, which is the
# test's DNS. Certificates come from Pebble over real HTTP-01, as in
# test/acme. The checks (fed-e2e.py) run in a container on that network.
#
# Profiles: irc, auth, voice, files, wellknown (+ directory on the remote).
# Left out of the full set: api, web, admin, metrics, video (federation
# touches none of them; admin's Federation page is boson-chat/admin's e2e).
#
#   test/federation/run.sh          # needs docker (compose >= 2.24), git
#   KEEP=1 test/federation/run.sh   # leave everything running
#
# Cleans up everything it started, by project name and label.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
LABEL=org.boson.fed-e2e=1
NET=${FED_NETWORK:-bosonfed}
SUBNET=10.99.53.0/24
TMP=${RUNNER_TEMP:-$(mktemp -d)}/boson-fed
PY=python:3.13-alpine

dc() { local p=$1; shift; (cd "$TMP/$p" && docker compose -p "$p" "$@"); }
logs() {
	for p in home remote; do
		[ -f "$TMP/$p/.env" ] || continue
		echo "::group::$p containers"; dc "$p" ps -a 2>&1 || true; echo "::endgroup::"
		for s in auth ergo traefik wellknown files directory; do
			echo "::group::$p $s logs"; dc "$p" logs --no-log-prefix --tail 80 "$s" 2>&1 || true; echo "::endgroup::"
		done
	done
	echo "::group::pebble logs"; docker logs --tail 60 bosonfed-pebble 2>&1 || true; echo "::endgroup::"
}
cleanup() {
	set +e
	[ "${ok:-}" = 1 ] || logs
	[ "${KEEP:-}" = 1 ] && { echo "KEEP=1: left running (projects home, remote; network $NET)"; return; }
	for p in home remote; do [ -f "$TMP/$p/.env" ] && dc "$p" down -v --remove-orphans >/dev/null 2>&1; done
	docker ps -aq --filter "label=$LABEL" | xargs -r docker rm -f >/dev/null 2>&1
	docker network ls -q --filter "label=$LABEL" | xargs -r docker network rm >/dev/null 2>&1
	# config/ belongs to uid 65532 by now (config-init)
	docker run --rm --label "$LABEL" -v "$(dirname "$TMP"):/t" busybox:1.37.0 rm -rf "/t/$(basename "$TMP")" >/dev/null 2>&1
	rm -rf "$TMP"
}
trap cleanup EXIT

# setenv FILE KEY VALUE: replace (or add) one .env line.
setenv() { grep -v "^$2=" "$1" > "$1.tmp" || true; echo "$2=$3" >> "$1.tmp"; mv "$1.tmp" "$1"; }

echo "== shared network $NET and Pebble"
mkdir -p "$TMP/ca" "$TMP/state"
docker network create --label "$LABEL" --subnet "$SUBNET" "$NET" >/dev/null
docker run -d --name bosonfed-pebble --label "$LABEL" --network "$NET" --network-alias pebble \
	-e PEBBLE_VA_NOSLEEP=1 -v "$ROOT/test/acme/pebble.json:/pebble.json:ro" \
	ghcr.io/letsencrypt/pebble:2.10.3 -config /pebble.json -dnsserver 127.0.0.11:53 -strict >/dev/null
# Pebble makes a new root at every start: the remote's auth and the test
# client trust it (SSL_CERT_FILE / --ca).
docker pull -q "$PY" >/dev/null
for _ in $(seq 1 30); do
	docker run --rm --label "$LABEL" --network "$NET" "$PY" python3 -c '
import ssl, urllib.request
ctx = ssl._create_unverified_context()
print(urllib.request.urlopen("https://pebble:15000/roots/0", context=ctx, timeout=5).read().decode())' > "$TMP/ca/roots.pem" 2>/dev/null &&
		grep -q 'BEGIN CERTIFICATE' "$TMP/ca/roots.pem" && break
	sleep 1
done
grep -q 'BEGIN CERTIFICATE' "$TMP/ca/roots.pem" || { echo "no Pebble root"; exit 1; }
chmod 644 "$TMP/ca/roots.pem"

# install NAME DOMAIN SUBNET PROFILES: a copy of the repo with its .env and config.
install() {
	local name=$1 d=$2 dir=$TMP/$1
	mkdir -p "$dir"
	git ls-files -z -co --exclude-standard | tar --null -T - -cf - | tar -xf - -C "$dir"
	(cd "$dir" && scripts/setup.sh --local >/dev/null)
	local env=$dir/.env hosts="$d,irc.$d,auth.$d,voice.$d,files.$d"
	case ",$4," in *,directory,*) hosts="$hosts,directory.$d" ;; esac
	for kv in "DOMAIN=$d" "NETWORK_NAME=$name" "IRC_HOST=irc.$d" "AUTH_HOST=auth.$d" "VOICE_HOST=voice.$d" \
		"FILES_HOST=files.$d" "DIRECTORY_HOST=directory.$d" "ADMIN_HOST=admin.$d" "API_HOST=api.$d" \
		"VIDEO_HOST=" "TLS_ISSUER=admin@$d" "ACME_CA_SERVER=https://pebble:14000/dir" "CERT_HOSTS=$hosts" \
		"STACK_SUBNET=$3" "NODE_EXPORTER_ADDR=${3%.0/24}.1" "COMPOSE_PROFILES=$4" \
		"REQUIRE_EMAIL_VERIFICATION=false" "FILES_ENABLED=true" \
		"SETTINGS_POLL_SECONDS=3" "WELLKNOWN_REFRESH_SECONDS=5" \
		"FED_NETWORK=$NET" "FED_CA_DIR=$TMP/ca" "STACK_PROJECT=$name"; do
		setenv "$env" "${kv%%=*}" "${kv#*=}"
	done
}

echo "== home (home.test) and remote (remote.test)"
install home home.test 10.99.54.0/24 irc,auth,voice,files,wellknown
setenv "$TMP/home/.env" COMPOSE_FILE compose.yaml:test/federation/compose.fed.yaml
install remote remote.test 10.99.55.0/24 irc,auth,voice,files,wellknown,directory
setenv "$TMP/remote/.env" COMPOSE_FILE compose.yaml:test/federation/compose.fed.yaml:test/federation/compose.fed-remote.yaml
for kv in DIRECTORY_ENV=development DIRECTORY_DNS_VERIFIER=fake DIRECTORY_INCLUDE_MAIN=false; do
	setenv "$TMP/remote/.env" "${kv%%=*}" "${kv#*=}"
done

# config/boson.yaml: uploads on (account quota 10MB) on both; the remote
# trusts verified servers in its own directory and gives remote users 2MB.
python3 - "$TMP/home/config/boson.yaml" "$TMP/remote/config/boson.yaml" <<'PY'
import sys
home, remote = sys.argv[1:]
def edit(path, fed):
    lines = open(path).read().split("\n")
    i = lines.index("auth:")  # setup.sh --local writes an auth: section
    lines[i + 1:i + 1] = fed
    lines += ["files:", "  enabled: true", "  account: { quota: 10MB, max_file: 5MB }", ""]
    open(path, "w").write("\n".join(lines))
edit(home, ["  federation:", "    mode: directory"])
edit(remote, ["  federation:", "    mode: directory", "    directory_url: https://directory.remote.test",
              "    remote_upload_quota: 2MB"])
PY

for p in home remote; do
	echo "== start $p"
	dc "$p" up -d --build --wait --wait-timeout 420
done

echo "== remote: an owner (the remote admin)"
ADMIN_PW="fed-$(openssl rand -hex 10)"
echo "$ADMIN_PW" | dc remote exec -T auth ./auth user create --username remoteadmin \
	--email admin@remote.test --role owner --password-stdin >/dev/null

# fed-e2e.py PHASE: the checks, in a container on the shared network. The
# remote's config/boson.yaml is mounted so the test can switch its mode.
check() {
	docker run --rm --label "$LABEL" --network "$NET" \
		-v "$ROOT/test/federation:/t:ro" -v "$TMP/ca:/ca:ro" -v "$TMP/state:/state" \
		-v "$TMP/remote/config:/remote-config" \
		-e REMOTE_ADMIN=remoteadmin -e REMOTE_ADMIN_PASSWORD="$ADMIN_PW" \
		"$PY" python3 -u /t/fed-e2e.py --phase "$1"
}

echo "== phase 1: home not verified in the directory -> domain_not_trusted"
check untrusted
# The remote's auth remembers "not listed" for 2 minutes: restart it rather
# than wait.
dc remote restart auth >/dev/null
echo "== phase 2: verified -> sign-in, IRC, identity, upload, voice; refusals; ban"
check main

ok=1
echo "federation e2e passed"
