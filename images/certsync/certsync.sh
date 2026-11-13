#!/bin/bash
# Caddy obtains and renews every certificate in the stack. Caddy keeps keys
# 0600 under its own user, so this copies each host's cert+key into a
# per-service volume, owned by that service's uid, whenever they change.
# Services watch their files and hot-reload (UnrealIRCd reloadtls,
# quicvoiced re-reads on mtime change).
#
# CERTSYNC_TARGETS="irc.example.com:/certs/irc:10000:server.cert.pem:server.key.pem voice.example.com:/certs/voice:65532:tls.crt:tls.key"
set -euo pipefail

SRC="${CADDY_DATA:-/caddy}/caddy/certificates"
INTERVAL="${CERTSYNC_INTERVAL:-60}"

sync_one() {
	local host=$1 dir=$2 uid=$3 certname=$4 keyname=$5
	# Any issuer dir works: ACME (letsencrypt/zerossl) or "local" (tls internal).
	local crt key
	crt=$(find "$SRC" -path "*/$host/$host.crt" -print 2>/dev/null | head -1)
	key=${crt%.crt}.key
	[ -n "$crt" ] && [ -s "$crt" ] && [ -s "$key" ] || return 0
	mkdir -p "$dir"
	if ! cmp -s "$crt" "$dir/$certname" || ! cmp -s "$key" "$dir/$keyname"; then
		install -m 0644 -o "$uid" -g "$uid" "$crt" "$dir/$certname.tmp"
		install -m 0600 -o "$uid" -g "$uid" "$key" "$dir/$keyname.tmp"
		mv -f "$dir/$keyname.tmp" "$dir/$keyname"
		mv -f "$dir/$certname.tmp" "$dir/$certname"
		echo "certsync: updated $host -> $dir"
	fi
}

echo "certsync: watching $SRC every ${INTERVAL}s"
while true; do
	for t in ${CERTSYNC_TARGETS:?CERTSYNC_TARGETS is required}; do
		IFS=: read -r host dir uid certname keyname <<<"$t"
		sync_one "$host" "$dir" "$uid" "$certname" "$keyname"
	done
	sleep "$INTERVAL"
done
