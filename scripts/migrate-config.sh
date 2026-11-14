#!/usr/bin/env bash
# One-off for installs that predate config/boson.yaml: creates the file and
# fills it with the settings each service has today (its database and .env),
# so nothing changes when the services start reading it. docs/config.md.
#
#   scripts/migrate-config.sh              # keys already in the file are kept
#   scripts/migrate-config.sh --overwrite  # services' current values win
#
# Each service prints its own section with `export-config` (auth, directory,
# quicvoiced, admin; ergo's boson-ergo helper too). An image too old to have
# the command is skipped with a warning: its env values stay the defaults.
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

merged=0
for svc in admin auth directory quicvoiced ergo; do
	has "$svc" || continue
	# ergo: assumes the boson-ergo helper's export-config (ergo image).
	if ! frag=$(docker compose run --rm --no-deps -T "$svc" export-config 2>/dev/null) || [ -z "$frag" ]; then
		echo "warning: $svc has no export-config (older image?); its .env values stay the defaults" >&2
		continue
	fi
	printf '%s\n' "$frag" | docker compose run --rm --no-deps -T admin config merge ${MERGE_FLAGS[@]+"${MERGE_FLAGS[@]}"} /config/boson.yaml
	echo "Merged $svc's settings."
	merged=$((merged + 1))
done

echo "Done ($merged services merged). Check config/boson.yaml, then: docker compose up -d"
