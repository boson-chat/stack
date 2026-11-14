#!/usr/bin/env bash
# One-off for installs that predate config/boson.yaml: creates the file and
# fills it with the settings each service has today (its database and .env),
# so nothing changes when the services start reading it. docs/config.md.
#
#   scripts/migrate-config.sh              # keys already in the file are kept
#   scripts/migrate-config.sh --overwrite  # services' current values win
#
# Each service prints its own section with `export-config` (auth, directory,
# quicvoiced, admin, api; ergo's boson-ergo helper too). An image too old to
# have the command is skipped with a warning: its env values stay the
# defaults. The api is the exception: nick claims used to be on whenever
# POP3_HOST was set and are now off unless nick_claim.enabled says so, so
# with POP3_HOST in .env this writes `enabled: true` and the POP3_* values
# (from export-config, or from .env when the image has no export-config).
# The admin image merges the sections into the file (`admin config merge`),
# keeping its comments. Run it with the stack's .env in place; services that
# need their database (auth, directory) need postgres running.
set -euo pipefail
cd "$(dirname "$0")/.."

MERGE_FLAGS=()
case "${1:-}" in
"") ;;
--overwrite) MERGE_FLAGS+=(--overwrite) ;;
*) echo "usage: $0 [--overwrite]" >&2; exit 2 ;;
esac

[ -e .env ] || { echo "no .env here; run scripts/setup.sh first" >&2; exit 1; }
mkdir -p config/branding
if [ ! -e config/boson.yaml ]; then
	cp config/boson.example.yaml config/boson.yaml
	chmod 644 config/boson.yaml
	echo "Created config/boson.yaml from config/boson.example.yaml."
fi

# The services this install runs (COMPOSE_PROFILES decides).
SERVICES=" $(docker compose config --services | tr '\n' ' ') "
has() { case "$SERVICES" in *" $1 "*) return 0 ;; esac; return 1; }
has admin || { echo "the admin profile isn't enabled: its image does the merge. Add admin to COMPOSE_PROFILES." >&2; exit 1; }

# config-init hands the file to uid 65532 (the admin image's user).
docker compose run --rm --no-deps -T config-init >/dev/null

has postgres && docker compose up -d --wait postgres >/dev/null

# envval NAME: NAME's value in .env (last one wins; surrounding quotes off).
envval() {
	sed -n "s/^[[:space:]]*$1=//p" .env | tail -1 | sed -e "s/^'\(.*\)'\$/\1/" -e 's/^"\(.*\)"$/\1/'
}
# yq_str VALUE: a double-quoted YAML string.
yq_str() { local v=${1//\\/\\\\}; v=${v//\"/\\\"}; printf '"%s"' "$v"; }

# Nick claims were on whenever POP3_HOST was set; keep them on for such an
# install (unless .env says NICK_CLAIM_ENABLED itself).
NICK_CLAIM_WAS_ON=false
if [ -n "$(envval POP3_HOST)" ] && ! grep -qE '^[[:space:]]*NICK_CLAIM_ENABLED=' .env; then
	NICK_CLAIM_WAS_ON=true
fi

# nick_claim_from_env: the api's section built from .env, for an api image
# without export-config. Only when nick claims were on.
nick_claim_from_env() {
	local domain port tls user
	domain=$(envval NICK_CLAIM_EMAIL_DOMAIN); [ -n "$domain" ] || domain=$(envval DOMAIN)
	port=$(envval POP3_PORT); case "$port" in "" | *[!0-9]*) port=995 ;; esac
	tls=$(envval POP3_TLS); case "$tls" in true | false) ;; *) tls=true ;; esac
	user=$(envval POP3_USER)
	echo "nick_claim:"
	echo "  enabled: true"
	[ -z "$domain" ] || echo "  email_domain: $(yq_str "$domain")"
	echo "  rate_limit_per_hour: $(envval NICK_CLAIM_RATE_LIMIT_PER_HOUR | grep -E '^[0-9]+$' || echo 5)"
	echo "  inbound:"
	echo "    protocol: pop3"
	echo "    host: $(yq_str "$(envval POP3_HOST)")"
	echo "    port: $port"
	echo "    tls: $tls"
	[ -z "$user" ] || echo "    user: $(yq_str "$user")"
}

merge() { docker compose run --rm --no-deps -T admin config merge ${MERGE_FLAGS[@]+"${MERGE_FLAGS[@]}"} /config/boson.yaml; }

merged=0
for svc in admin auth directory quicvoiced ergo api; do
	has "$svc" || continue
	run_env=()
	# api: export-config reports enabled from NICK_CLAIM_ENABLED.
	[ "$svc" = api ] && [ "$NICK_CLAIM_WAS_ON" = true ] && run_env=(-e NICK_CLAIM_ENABLED=true)
	# ergo: assumes the boson-ergo helper's export-config (ergo image).
	if ! frag=$(docker compose run --rm --no-deps -T ${run_env[@]+"${run_env[@]}"} "$svc" export-config 2>/dev/null) || [ -z "$frag" ]; then
		if [ "$svc" = api ] && [ "$NICK_CLAIM_WAS_ON" = true ]; then
			echo "warning: api has no export-config (older image?); writing nick_claim from .env (POP3_HOST is set, so enabled: true)" >&2
			nick_claim_from_env | merge
			echo "Merged api's nick_claim settings from .env."
			merged=$((merged + 1))
			continue
		fi
		echo "warning: $svc has no export-config (older image?); its .env values stay the defaults" >&2
		continue
	fi
	printf '%s\n' "$frag" | merge
	echo "Merged $svc's settings."
	merged=$((merged + 1))
done

echo "Done ($merged services merged). Check config/boson.yaml, then: docker compose up -d"
