# Helpers for stack migrations (migrations/NNNN-name.sh, docs/updates.md
# "Migrations"). A migration starts with:
#
#   #!/bin/sh
#   # What it does, in one line (the Updates page shows it).
#   set -eu
#   # shellcheck source=migrations/lib.sh
#   . "$MIGRATIONS_LIB"
#
# It runs with STACK_DIR (the install) and ENV_FILE (its .env), after the
# new release's files are in place, and must be idempotent: running it on an
# install that already has the change does nothing. It never prints a
# secret's value (the panel keeps every output line in the update's log).
# shellcheck shell=sh

: "${STACK_DIR:?STACK_DIR is not set}"
: "${ENV_FILE:=$STACK_DIR/.env}"

# say TEXT: a line for the update's log.
say() { printf '%s\n' "$*"; }

# defer TEXT: not now (say why); the migration runs again on the next update.
defer() {
	say "not yet: $*"
	exit 3
}

# env_has KEY: .env has a KEY= line.
env_has() { grep -q "^$1=" "$ENV_FILE"; }

# env_get KEY: KEY's value in .env (the last KEY= line; "" when missing).
env_get() {
	awk -v k="$1" 'index($0, k "=") == 1 { v = substr($0, length(k) + 2) } END { print v }' "$ENV_FILE"
}

# env_set KEY VALUE: sets KEY in .env, in place (same file, owner and mode),
# replacing its line or appending one.
env_set() {
	umask 077
	_tmp=$ENV_FILE.migration.tmp
	if env_has "$1"; then
		awk -v k="$1" -v v="$2" 'index($0, k "=") == 1 { if (!done) print k "=" v; done = 1; next } { print }' "$ENV_FILE" > "$_tmp"
	else
		cat "$ENV_FILE" > "$_tmp"
		[ -z "$(tail -c 1 "$_tmp")" ] || echo >> "$_tmp"
		printf '%s=%s\n' "$1" "$2" >> "$_tmp"
	fi
	cat "$_tmp" > "$ENV_FILE"
	rm -f "$_tmp"
}

# env_set_default KEY VALUE: sets KEY only when .env doesn't have it or has
# it empty; a value someone set is never changed.
env_set_default() {
	[ -n "$(env_get "$1")" ] || env_set "$1" "$2"
}

# gen_secret [BYTES]: BYTES random bytes (default 24), hex.
gen_secret() {
	od -An -tx1 -N "${1:-24}" /dev/urandom | tr -d ' \n'
}

# has_profile NAME: COMPOSE_PROFILES in .env includes NAME.
has_profile() {
	case ",$(env_get COMPOSE_PROFILES | tr -d ' ')," in
	*",$1,"*) return 0 ;;
	esac
	return 1
}

# compose_default VAR: the default compose.yaml gives ${VAR:-...}.
compose_default() {
	sed -n "s/.*\${$1:-\([^}]*\)}.*/\1/p" "$STACK_DIR/compose.yaml" | head -n 1
}

# image_of VAR: the image VAR names (.env, else compose.yaml's default).
image_of() {
	_v=$(env_get "$1")
	[ -n "$_v" ] || _v=$(compose_default "$1")
	printf '%s\n' "$_v"
}

# image_tag REF: the tag of an image reference ("" without one).
image_tag() {
	_r=${1%%@*}
	case "${_r##*/}" in
	*:*) printf '%s\n' "${_r##*:}" ;;
	*) echo ;;
	esac
}

# version_ge A B: version A >= B (x.y.z; a leading v and a -pre are ignored).
# A that isn't a version counts as new (a local build).
version_ge() {
	awk -v a="${1#v}" -v b="${2#v}" 'BEGIN {
		sub(/[-+].*/, "", a); sub(/[-+].*/, "", b)
		if (a !~ /^[0-9]+(\.[0-9]+)*$/) exit 0
		na = split(a, x, "."); nb = split(b, y, ".")
		for (i = 1; i <= 3; i++) {
			if (x[i] + 0 > y[i] + 0) exit 0
			if (x[i] + 0 < y[i] + 0) exit 1
		}
		exit 0
	}'
}

# file_has_line FILE REGEX: a line of FILE matches REGEX (grep -E).
file_has_line() { [ -f "$1" ] && grep -Eq "$2" "$1"; }

# file_append FILE LINE...: appends the lines (FILE keeps its owner and mode).
file_append() {
	_f=$1
	shift
	[ -z "$(tail -c 1 "$_f" 2>/dev/null)" ] || echo >> "$_f"
	printf '%s\n' "$@" >> "$_f"
}

# restart SERVICE...: the update recreates these services afterwards (for a
# change compose can't see, such as a file they read).
restart() {
	for _s in "$@"; do echo "@@RESTART $_s"; done
}
