#!/bin/bash
# Renders unrealircd.conf from the environment, waits for the TLS cert that
# certsync hands over, and reloads TLS whenever that cert changes.
set -euo pipefail

BASE=/home/unrealircd/unrealircd
CONF="$BASE/conf/unrealircd.conf"

die() { echo "entrypoint: $*" >&2; exit 1; }

for v in IRC_HOST NETWORK_NAME ADMIN_EMAIL OPER_NAME OPER_PASSWORD_HASH \
         CLOAK_KEY1 CLOAK_KEY2 CLOAK_KEY3 SERVICES_HOST SERVICES_LINK_PASSWORD; do
	[ -n "${!v:-}" ] || die "$v is not set (see .env.example)"
done

export IRC_SID="${IRC_SID:-001}"
export HELP_CHANNEL="${HELP_CHANNEL:-#help}"
export CLOAK_PREFIX="${CLOAK_PREFIX:-$NETWORK_NAME}"
export DRPASS="${DRPASS:-$(tr -dc A-Za-z0-9 </dev/urandom | head -c 32)}"

# TLS: the cert comes from Caddy via certsync. Wait for it; on first start
# Caddy may still be getting it from Let's Encrypt.
CERT_DIR="${CERT_DIR:-/certs}"
export TLS_CERT="$CERT_DIR/server.cert.pem" TLS_KEY="$CERT_DIR/server.key.pem"
waited=0
until [ -s "$TLS_CERT" ] && [ -s "$TLS_KEY" ]; do
	if (( waited % 30 == 0 )); then
		echo "entrypoint: waiting for $TLS_CERT from certsync (check: docker compose logs caddy certsync)"
	fi
	sleep 2; waited=$((waited + 2))
done

# Only substitute our variables; the config also contains literal $ip etc.
VARS='${IRC_HOST} ${NETWORK_NAME} ${ADMIN_EMAIL} ${OPER_NAME} ${OPER_PASSWORD_HASH}
${CLOAK_KEY1} ${CLOAK_KEY2} ${CLOAK_KEY3} ${SERVICES_HOST} ${SERVICES_LINK_PASSWORD}
${IRC_SID} ${HELP_CHANNEL} ${CLOAK_PREFIX} ${DRPASS} ${TLS_CERT} ${TLS_KEY}'
umask 077
envsubst "$VARS" < /home/unrealircd/templates/unrealircd.conf.tmpl > "$CONF"

if [ "${IRC_PLAINTEXT:-false}" = "true" ]; then
	printf '\n/* Plaintext IRC (IRC_PLAINTEXT=true) */\nlisten { ip *; port 6667; }\n' >> "$CONF"
fi
if [ -n "${EXTRA_CONF:-}" ] && [ -f "$EXTRA_CONF" ]; then
	printf '\ninclude "%s";\n' "$EXTRA_CONF" >> "$CONF"
fi

# Reload TLS without dropping clients when certsync drops in a renewed cert.
(
	last=""
	while sleep "${CERT_CHECK_SECONDS:-300}"; do
		[ -s "$CERT_DIR/server.cert.pem" ] || continue
		sum=$(sha256sum "$CERT_DIR/server.cert.pem" | cut -d' ' -f1)
		if [ -n "$last" ] && [ "$sum" != "$last" ]; then
			echo "entrypoint: certificate changed, reloading TLS"
			"$BASE/bin/unrealircdctl" reloadtls || echo "entrypoint: reloadtls failed" >&2
		fi
		last="$sum"
	done
) &

exec "$@"
