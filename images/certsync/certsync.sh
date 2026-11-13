#!/bin/bash
# Traefik obtains and renews every certificate in the stack and keeps them in
# acme.json (0600, root). IRC and voice aren't HTTP, so they need the cert as
# files: certsync exports acme.json with traefik-certs-dumper and copies each
# host's cert+key into a per-service volume owned by that service's uid,
# whenever they change. Services hot-reload (Ergo rehashes on SIGHUP from its
# entrypoint's watcher, quicvoiced re-reads on mtime change).
#
# CERTSYNC_TARGETS="irc.example.com:/certs/irc:10000:fullchain.pem:privkey.pem voice.example.com:/certs/voice:65532:tls.crt:tls.key"
#
# Sources:
#   ACME_JSON   Traefik's acme.json (default /acme/acme.json)
#   LOCAL_CERTS a directory holding cert.pem + key.pem (the local dev CA's
#               certificate from scripts/setup.sh --local): that pair is used
#               for every host instead of acme.json. Defaults to /local-certs
#               when TLS_ISSUER is "local" (or "internal").
set -euo pipefail

ACME_JSON="${ACME_JSON:-/acme/acme.json}"
LOCAL_CERTS="${LOCAL_CERTS:-}"
case "${TLS_ISSUER:-}" in local|internal) LOCAL_CERTS="${LOCAL_CERTS:-/local-certs}" ;; esac
INTERVAL="${CERTSYNC_INTERVAL:-30}"
DUMP=/tmp/dump

# dump: refresh $DUMP/<domain>/{tls.crt,tls.key} from acme.json if it changed.
last_sum=""
dump() {
	[ -s "$ACME_JSON" ] || return 0
	local sum
	sum=$(sha256sum "$ACME_JSON" | cut -d' ' -f1)
	[ "$sum" != "$last_sum" ] || return 0
	# acme.json is written atomically by Traefik, but it may have no certs yet.
	if traefik-certs-dumper file --version v3 --source "$ACME_JSON" --dest "$DUMP.new" \
		--domain-subdir --crt-name tls --crt-ext .crt --key-name tls --key-ext .key >/dev/null 2>&1; then
		rm -rf "$DUMP" && mv "$DUMP.new" "$DUMP"
		last_sum=$sum
	else
		rm -rf "$DUMP.new"
	fi
}

sync_one() {
	local host=$1 dir=$2 uid=$3 certname=$4 keyname=$5 crt key
	if [ -n "$LOCAL_CERTS" ]; then
		crt="$LOCAL_CERTS/cert.pem" key="$LOCAL_CERTS/key.pem"
	else
		crt="$DUMP/$host/tls.crt" key="$DUMP/$host/tls.key"
	fi
	[ -s "$crt" ] && [ -s "$key" ] || return 0
	mkdir -p "$dir"
	if ! cmp -s "$crt" "$dir/$certname" || ! cmp -s "$key" "$dir/$keyname"; then
		install -m 0644 -o "$uid" -g "$uid" "$crt" "$dir/$certname.tmp"
		install -m 0600 -o "$uid" -g "$uid" "$key" "$dir/$keyname.tmp"
		# Key first: a reader that sees the new cert must also see its key.
		mv -f "$dir/$keyname.tmp" "$dir/$keyname"
		mv -f "$dir/$certname.tmp" "$dir/$certname"
		echo "certsync: updated $host -> $dir"
	fi
}

if [ -n "$LOCAL_CERTS" ]; then
	echo "certsync: local mode, using $LOCAL_CERTS/cert.pem for every host"
else
	echo "certsync: watching $ACME_JSON every ${INTERVAL}s"
fi
while true; do
	[ -n "$LOCAL_CERTS" ] || dump
	for t in ${CERTSYNC_TARGETS:?CERTSYNC_TARGETS is required}; do
		IFS=: read -r host dir uid certname keyname <<<"$t"
		[ -n "$host" ] || continue
		sync_one "$host" "$dir" "$uid" "$certname" "$keyname"
	done
	sleep "$INTERVAL"
done
