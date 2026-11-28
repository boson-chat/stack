#!/bin/sh
# Installs a stack release bundle (scripts/release/bundle.sh) into an
# install directory: the files, then the migrations. The admin panel's
# Updates page runs it from the new bundle (docs/updates.md "Stack
# releases"), the setup wizard runs it on every install, and it works by
# hand:
#
#   STACK_DIR=/opt/boson-stack sh NEW/scripts/stack-apply.sh plan [--diff] NEW
#   STACK_DIR=/opt/boson-stack sh NEW/scripts/stack-apply.sh files NEW
#   STACK_DIR=/opt/boson-stack sh NEW/scripts/stack-apply.sh migrate NEW
#
# NEW is the extracted bundle (it has .bundle/manifest.json). Environment:
# STACK_DIR (the install), ENV_FILE (default STACK_DIR/.env), BACKUP_DIR
# (optional: files written here that didn't exist before are listed in
# BACKUP_DIR/stack-added.txt, so a rollback can remove them).
#
# files: every bundle file is written unless it is already the same. A file
# changed on this server since it was installed (.boson/stack-files.sha256
# records what was) is replaced too, and the edited copy is kept next to it
# as <file>.local. Files an earlier bundle had and this one doesn't are
# removed, unless they were edited here. Never touched, whatever a bundle
# says: .env, config/boson.yaml, compose.override.yaml, secrets/,
# local-certs/, bridge/, backups/, .setup/, .boson/ and every file that was
# never in a bundle. Then .boson/stack-files.sha256 and .boson/manifest.json
# record the new bundle (VERSION, a bundle file, says its version).
#
# migrate: migrations/NNNN-name.sh not yet in .boson/migrations.applied run
# in order (sh, with STACK_DIR, ENV_FILE and MIGRATIONS_LIB), each recorded
# when it exits 0. Exit 3 = not yet (left for the next update); anything
# else stops here with an error. A migration with a "# recheck: every
# update" line runs again on every migrate after it is recorded, so a change
# it made that went missing (a .env put back from an older copy) comes back;
# "applied" again when it changed .env, a failure there is only a warning.
#
# Output: plain lines for people, and lines for the panel:
#   @@STACKFILE <action> <path>   add | update | same | local | unknown |
#                                 remove | keep
#   @@DIFF <path> / @@D <line>    (plan --diff) a unified diff, line by line
#   @@MIGRATION <name> <state> [about]   pending | applied | done | deferred | failed
#   @@RESTART <service>           a migration changed a file the service reads
set -eu

die() { printf 'stack-apply.sh: %s\n' "$*" >&2; exit 1; }

cmd=${1:-}
[ $# -gt 0 ] && shift
DIFF=false
if [ "${1:-}" = --diff ]; then DIFF=true; shift; fi
NEW=${1:-}
[ -n "$cmd" ] && [ -n "$NEW" ] || die "usage: stack-apply.sh plan [--diff] | files | migrate BUNDLE_DIR"
[ -n "${STACK_DIR:-}" ] || die "STACK_DIR is not set"
[ -d "$STACK_DIR" ] || die "STACK_DIR $STACK_DIR is not a directory"
NEW=$(cd "$NEW" && pwd)
STACK_DIR=$(cd "$STACK_DIR" && pwd)
ENV_FILE=${ENV_FILE:-$STACK_DIR/.env}
[ -f "$NEW/.bundle/manifest.json" ] && [ -f "$NEW/.bundle/files.sha256" ] || die "$NEW is not a stack bundle (no .bundle/manifest.json)"
[ "$NEW" != "$STACK_DIR" ] || die "the bundle can't be the install directory"
STATE=$STACK_DIR/.boson
RECORD=$STATE/stack-files.sha256
APPLIED=$STATE/migrations.applied
VERSION=$(head -n 1 "$NEW/VERSION" 2>/dev/null | tr -dc '0-9A-Za-z.+-')

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# protected PATH: never written by a bundle.
protected() {
	case "$1" in
	.env | .env.* | config/boson.yaml | compose.override.yaml | compose.override.yml | \
		secrets/* | local-certs/* | bridge/* | backups/* | .setup/* | .boson/* | .bundle/*)
		return 0 ;;
	/* | ../* | */../* | *' '*) return 0 ;;
	esac
	return 1
}

# recorded PATH: the hash this install recorded for it ("" = none).
recorded() {
	[ -f "$RECORD" ] || return 0
	awk -v p="$1" '$2 == p { print $1; exit }' "$RECORD"
}

# action PATH NEWHASH: what files does with one bundle file.
action() {
	dst=$STACK_DIR/$1
	if [ ! -e "$dst" ]; then echo add; return; fi
	cur=$(sha256 "$dst")
	if [ "$cur" = "$2" ]; then echo same; return; fi
	base=$(recorded "$1")
	if [ -z "$base" ]; then
		# No record of the installed release (a pre-release install, or a
		# file a newer bundle added): it may be an older version or an edit.
		echo unknown
	elif [ "$cur" = "$base" ]; then
		echo update
	else
		echo local
	fi
}

diff_file() {
	echo "@@DIFF $1"
	if [ -e "$STACK_DIR/$1" ]; then old=$STACK_DIR/$1; else old=/dev/null; fi
	n=0
	{ diff -u "$old" "$NEW/$1" 2>/dev/null || true; } | {
		while IFS= read -r line; do
			n=$((n + 1))
			if [ "$n" -gt 600 ]; then
				echo "@@D ... (diff cut at 600 lines)"
				break
			fi
			printf '@@D %s\n' "$line"
		done
		cat > /dev/null
	}
}

# write PATH: the bundle's file in place (temp file + rename, mode kept).
write() {
	dst=$STACK_DIR/$1
	mkdir -p "$(dirname "$dst")"
	cp -p "$NEW/$1" "$dst.stack-new"
	mv -f "$dst.stack-new" "$dst"
}

added() {
	[ -n "${BACKUP_DIR:-}" ] || return 0
	mkdir -p "$BACKUP_DIR"
	echo "$1" >> "$BACKUP_DIR/stack-added.txt"
}

do_files() {
	mode=$1 # plan | files
	while read -r hash path; do
		[ -n "$path" ] || continue
		if protected "$path"; then
			echo "skipping $path: a bundle never writes it"
			continue
		fi
		[ -f "$NEW/$path" ] || die "the bundle lists $path but doesn't have it"
		a=$(action "$path" "$hash")
		echo "@@STACKFILE $a $path"
		if [ "$mode" = plan ]; then
			if $DIFF && [ "$a" != same ]; then diff_file "$path"; fi
			continue
		fi
		case "$a" in
		same)
			# Same content: make sure the mode is the bundle's too.
			if [ -x "$NEW/$path" ]; then chmod 755 "$STACK_DIR/$path"; fi
			;;
		add)
			write "$path"
			added "$path"
			;;
		update | unknown)
			write "$path"
			;;
		local)
			[ -e "$STACK_DIR/$path.local" ] || added "$path.local"
			cp -p "$STACK_DIR/$path" "$STACK_DIR/$path.local"
			write "$path"
			echo "$path was changed on this server: replaced, your copy is $path.local"
			;;
		esac
	done < "$NEW/.bundle/files.sha256"
	# Files the installed bundle had and this one doesn't.
	if [ -f "$RECORD" ]; then
		while read -r hash path; do
			[ -n "$path" ] || continue
			protected "$path" && continue
			awk -v p="$path" '$2 == p { f = 1 } END { exit !f }' "$NEW/.bundle/files.sha256" && continue
			dst=$STACK_DIR/$path
			[ -e "$dst" ] || continue
			if [ "$(sha256 "$dst")" = "$hash" ]; then
				echo "@@STACKFILE remove $path"
				[ "$mode" = plan ] || rm -f "$dst"
			else
				echo "@@STACKFILE keep $path"
			fi
		done < "$RECORD"
	fi
	[ "$mode" = plan ] && return 0
	mkdir -p "$STATE"
	chmod 755 "$STATE"
	cp "$NEW/.bundle/files.sha256" "$RECORD.tmp" && mv -f "$RECORD.tmp" "$RECORD"
	cp "$NEW/.bundle/manifest.json" "$STATE/manifest.json.tmp" && mv -f "$STATE/manifest.json.tmp" "$STATE/manifest.json"
	echo "stack files: release ${VERSION:-?} in $STACK_DIR"
}

is_applied() {
	[ -f "$APPLIED" ] && awk -v m="$1" '$1 == m { found = 1 } END { exit !found }' "$APPLIED"
}

# recheck FILE: runs again on every migrate, recorded or not.
recheck() { sed -n '1,20p' "$1" | grep -q '^# recheck: every update'; }

env_sum() { if [ -f "$ENV_FILE" ]; then sha256 "$ENV_FILE"; fi; }

about() {
	# The first comment line after the shebang says what it does.
	sed -n '2{s/^# *//;p;q;}' "$1"
}

do_migrate() {
	mode=$1 # plan | migrate
	[ -d "$NEW/migrations" ] || return 0
	for m in "$NEW"/migrations/[0-9][0-9][0-9][0-9]-*.sh; do
		[ -f "$m" ] || continue
		name=$(basename "$m")
		if is_applied "$name"; then
			if [ "$mode" = migrate ] && recheck "$m"; then
				echo "== migration $name (recheck): $(about "$m")"
				before=$(env_sum)
				rc=0
				STACK_DIR=$STACK_DIR ENV_FILE=$ENV_FILE MIGRATIONS_LIB=$NEW/migrations/lib.sh \
					sh "$m" || rc=$?
				if [ "$rc" = 0 ] && [ "$(env_sum)" != "$before" ]; then
					echo "$name: its change had gone missing, made again"
					echo "@@MIGRATION $name applied"
					continue
				fi
				[ "$rc" = 0 ] || [ "$rc" = 3 ] || echo "warning: recheck of $name failed (exit $rc), left as it is"
			fi
			echo "@@MIGRATION $name done"
			continue
		fi
		if [ "$mode" = plan ]; then
			echo "@@MIGRATION $name pending $(about "$m")"
			continue
		fi
		echo "== migration $name: $(about "$m")"
		rc=0
		STACK_DIR=$STACK_DIR ENV_FILE=$ENV_FILE MIGRATIONS_LIB=$NEW/migrations/lib.sh \
			sh "$m" || rc=$?
		case "$rc" in
		0)
			mkdir -p "$STATE"
			printf '%s %s %s\n' "$name" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${VERSION:-?}" >> "$APPLIED"
			echo "@@MIGRATION $name applied"
			;;
		3)
			echo "@@MIGRATION $name deferred"
			;;
		*)
			echo "@@MIGRATION $name failed"
			die "migration $name failed (exit $rc)"
			;;
		esac
	done
}

case "$cmd" in
plan)
	do_files plan
	do_migrate plan
	;;
files)
	do_files files
	;;
migrate)
	[ -f "$ENV_FILE" ] || die "no $ENV_FILE"
	do_migrate migrate
	;;
*) die "unknown command: $cmd" ;;
esac
