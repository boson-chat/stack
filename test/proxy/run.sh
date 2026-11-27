#!/usr/bin/env bash
# Boson behind an existing reverse proxy (docs/reverse-proxy.md), end to end:
#   test/proxy/run.sh traefik|nginx|haproxy
# An outer proxy holds "the internet's" 80/443, configured with the setup
# wizard's own snippets (images/setup/cmd/proxysnippets). Boson's Traefik
# publishes on the outer proxy's Docker network gateway (TRAEFIK_HTTP_PUBLISH
# / TRAEFIK_HTTPS_PUBLISH) and trusts PROXY protocol from the stack network's
# gateway (TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS). Checks: Pebble issues every
# certificate over HTTP-01 through the outer proxy; a client's own address
# reaches the services behind Boson's Traefik (whoami: X-Real-Ip and
# X-Forwarded-For) and auth; http redirects to https; a spoofed PROXY header
# from an untrusted address is ignored; Boson's own address still answers
# without a header (the wizard's checks); the outer proxy's own site still
# works. Needs Go and a .env from scripts/setup.sh --local. Cleans up.
set -euo pipefail
cd "$(dirname "$0")/../.."
SW=${1:?usage: test/proxy/run.sh traefik|nginx|haproxy}

PROXY_SNIPPETS=$(mktemp -d)
export PROXY_SNIPPETS
(cd images/setup && go run ./cmd/proxysnippets -software "$SW" -domain boson.test \
	-http 10.213.0.1:8081 -https 10.213.0.1:8444 -out "$PROXY_SNIPPETS")
case $SW in
traefik) cp test/proxy/own-site.traefik.yml "$PROXY_SNIPPETS/own.yml"; mv "$PROXY_SNIPPETS/proxy-traefik" "$PROXY_SNIPPETS/boson.yml" ;;
haproxy) cat test/proxy/haproxy.head.cfg "$PROXY_SNIPPETS/proxy-haproxy" > "$PROXY_SNIPPETS/haproxy.cfg" ;;
esac
chmod -R a+rX "$PROXY_SNIPPETS"
echo "== the outer proxy's config (from the wizard's snippets)"
for f in "$PROXY_SNIPPETS"/*; do echo "--- $(basename "$f")"; cat "$f"; done

export COMPOSE_FILE=compose.yaml:test/proxy/compose.proxy.yaml:test/proxy/compose.outer-$SW.yaml
! grep -q '^COMPOSE_FILE=.*compose.auth-src.yaml' .env || COMPOSE_FILE="$COMPOSE_FILE:compose.auth-src.yaml"
dc() { docker compose --env-file .env --env-file test/acme/acme.env --env-file test/proxy/proxy.env "$@"; }
cleanup() { dc down -v --remove-orphans >/dev/null 2>&1 || true; rm -rf "$PROXY_SNIPPETS"; }
trap cleanup EXIT
fail() {
	echo "FAIL: $*"
	dc logs --tail 60 outer traefik auth pebble 2>&1 | tail -200
	exit 1
}
wait_for() { # wait_for SECONDS DESCRIPTION CMD...
	local t=$1 what=$2; shift 2
	for _ in $(seq 1 "$t"); do "$@" >/dev/null 2>&1 && { echo "PASS $what"; return 0; }; sleep 1; done
	fail "$what"
}
# From "the internet" (10.214.0.99), through the outer proxy (10.214.0.10).
client() { dc exec -T client curl -sS --max-time 10 "$@"; }
via() { # via HOST [curl args...]: https://HOST through the outer proxy
	local host=$1; shift
	client -k --resolve "$host:443:10.214.0.10" --resolve "$host:80:10.214.0.10" "$@"
}
export -f client via dc

grep -q "TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS" compose.yaml || fail "compose.yaml has no proxy settings"
grep -q -- '--entrypoints.websecure.proxyprotocol.trustedips=10.203.53.1' <<<"$(dc config traefik)" || fail "PROXY protocol flags not rendered"

echo "== up: outer ($SW) + Boson (traefik, certsync, postgres, auth) + Pebble"
dc up -d --build pebble challtestsrv traefik certsync postgres auth whoami client outer
[ "$SW" != traefik ] || dc up -d ownsite

issuer() { local v; v=$(via "$1" -v -o /dev/null "https://$1/" 2>&1); grep -i 'issuer:' <<<"$v"; }
export -f issuer
for h in auth.boson.test api.boson.test irc.boson.test; do
	wait_for 180 "$h: a Pebble certificate, issued over HTTP-01 through the outer proxy, served through it" \
		bash -c "issuer $h | grep -q Pebble"
done
wait_for 120 "auth answers through the outer proxy" bash -c "via auth.boson.test -f -o /dev/null https://auth.boson.test/v1/config"

echo "== the client's own address behind Boson's Traefik"
body=$(via api.boson.test -f https://api.boson.test/ | tr -d '\r') || fail "whoami through the outer proxy"
echo "$body" | grep -E '^(RemoteAddr|X-Real-Ip|X-Forwarded-For):'
grep -qx 'X-Real-Ip: 10.214.0.99' <<<"$body" || fail "X-Real-Ip isn't the client's address"
grep -qx 'X-Forwarded-For: 10.214.0.99' <<<"$body" || fail "X-Forwarded-For isn't the client's address"
echo "PASS whoami sees 10.214.0.99"

# auth: TRUSTED_PROXIES (the stack subnet) takes X-Forwarded-For from
# Traefik; it logs every request with the address it counted (sign-in rate
# limits, bans).
via auth.boson.test -f -o /dev/null https://auth.boson.test/v1/config || fail "auth through the outer proxy"
sleep 1
alog=$(dc logs auth 2>&1 | grep '"path":"/v1/config"' || true)
echo "$alog" | tail -1
if grep -q '"ip":"10.214.0.99"' <<<"$alog"; then
	echo "PASS auth sees the client's address (10.214.0.99)"
elif grep -q '"ip":' <<<"$alog"; then
	fail "auth logged another address than the client's"
else
	echo "SKIP this auth doesn't log request addresses"
fi

echo "== port 80 through the outer proxy"
loc=$(via auth.boson.test -o /dev/null -w '%{http_code} %{redirect_url}' http://auth.boson.test/v1/config)
echo "$loc"
[[ $loc =~ ^30[178]\ https://auth.boson.test/v1/config$ ]] || fail "http isn't Boson's redirect to https: $loc"
echo "PASS http → https"

echo "== a spoofed PROXY header from an untrusted address is ignored"
net=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$(dc ps -q postgres)")
spoof=$(docker run --rm --network "$net" curlimages/curl:8.11.1 -sSk --max-time 10 --haproxy-protocol --haproxy-clientip 192.0.2.66 \
	--resolve api.boson.test:443:10.203.53.10 https://api.boson.test/ | tr -d '\r') || fail "spoofed request"
echo "$spoof" | grep -E '^X-Real-Ip:'
! grep -q '192.0.2.66' <<<"$spoof" || fail "Traefik believed an untrusted PROXY header"
echo "PASS untrusted PROXY header ignored"

echo "== Boson's own address, without a PROXY header (the wizard's checks)"
direct=$(curl -sSk --max-time 10 --resolve api.boson.test:8444:10.213.0.1 https://api.boson.test:8444/ | tr -d '\r') || fail "Boson's Traefik on 10.213.0.1:8444"
grep -q '^X-Real-Ip:' <<<"$direct" || fail "no answer on Boson's own address"
echo "PASS Boson answers on its own address without a header"

if [ "$SW" = traefik ]; then
	echo "== the outer Traefik's own site"
	own=$(via own.test -f https://own.test/ | tr -d '\r') || fail "own.test"
	grep -qx 'X-Real-Ip: 10.214.0.99' <<<"$own" || fail "own.test: $own"
	code=$(via own.test -o /dev/null -w '%{http_code}' http://own.test/)
	[[ $code =~ ^30[178]$ ]] || fail "own.test http: $code"
	echo "PASS own site and its redirect still work"
fi
echo "proxy test ($SW) passed"
