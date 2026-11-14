#!/bin/sh
# Renders /ircd/ircd.yaml from the environment (and the auth service's
# channel_creation setting), waits for the TLS cert that certsync hands over,
# then runs Ergo as PID 1. Background loops send SIGHUP (rehash: reloads
# config + TLS without dropping clients) whenever certsync replaces the
# certificate, and re-render + SIGHUP when channel_creation changes
# (boson-ergo watch-settings, every SETTINGS_POLL_SECONDS, default 30).
set -eu

CERT_DIR=${CERT_DIR:-/certs}
export TLS_CERT="$CERT_DIR/fullchain.pem" TLS_KEY="$CERT_DIR/privkey.pem"

umask 077
/usr/local/bin/boson-ergo render

waited=0
until [ -s "$TLS_CERT" ] && [ -s "$TLS_KEY" ]; do
	if [ $((waited % 30)) -eq 0 ]; then
		echo "entrypoint: waiting for $TLS_CERT from certsync (check: docker compose logs traefik certsync)"
	fi
	sleep 2; waited=$((waited + 2))
done

# The datastore must exist before `ergo run`. On the very first start it can
# be created from an Anope/Atheme export instead (anope2json.py output):
# ERGO_IMPORT_JSON=/path/in/container.json. See docs/deploy-boson-chat.md.
if [ ! -s /ircd/ircd.db ]; then
	if [ -n "${ERGO_IMPORT_JSON:-}" ]; then
		echo "entrypoint: importing $ERGO_IMPORT_JSON into a new datastore"
		/ircd-bin/ergo importdb "$ERGO_IMPORT_JSON" --conf /ircd/ircd.yaml --quiet
	else
		/ircd-bin/ergo initdb --conf /ircd/ircd.yaml --quiet
	fi
elif [ -n "${ERGO_IMPORT_JSON:-}" ]; then
	echo "entrypoint: ERGO_IMPORT_JSON ignored: /ircd/ircd.db already exists (import only works into a new datastore)" >&2
fi

(
	sum() { cat "$TLS_CERT" "$TLS_KEY" 2>/dev/null | sha256sum | cut -d' ' -f1; }
	last=$(sum)
	while sleep "${CERT_CHECK_SECONDS:-30}"; do
		now=$(sum)
		if [ "$now" != "$last" ] && [ -s "$TLS_CERT" ] && [ -s "$TLS_KEY" ]; then
			echo "entrypoint: certificate changed, rehashing ergo"
			kill -HUP 1 || echo "entrypoint: rehash signal failed" >&2
			last=$now
		fi
	done
) &

if [ "${IRC_WATCH_SETTINGS:-true}" = true ]; then
	/usr/local/bin/boson-ergo watch-settings &
fi

exec "$@"
