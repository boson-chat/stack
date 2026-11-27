#!/usr/bin/env bash
# The checks are eval'd strings: shellcheck sees neither their variables nor their quoting.
# shellcheck disable=SC2034,SC2016
# scripts/release/bundle.sh + scripts/stack-apply.sh + migrations/, without
# Docker: a release bundle of this commit ("1.1.0") over an install of an
# older one ("1.0.0", the same files with changes), over a pre-release
# install (no record), local edits, removed files, protected paths, and the
# migrations (once each, deferred, idempotent on an install that already
# has the change).
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fails=0
ok() { echo "ok   $*"; }
bad() { echo "FAIL $*"; fails=$((fails + 1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }
apply() { STACK_DIR=$DIR sh "$1/scripts/stack-apply.sh" "${@:2}"; }

# 1.1.0: this commit. 1.0.0: the same, with a different scrape.yml, a file
# 1.1.0 dropped, and no migrations.
scripts/release/bundle.sh --out "$T/new" --version 1.1.0 --release --commit test >/dev/null
cp -R "$ROOT" "$T/old-src"
rm -rf "$T/old-src/.git"
echo "# 1.0.0" >> "$T/old-src/monitoring/scrape.yml"
echo "gone in 1.1.0" > "$T/old-src/docs/old.md"
rm -f "$T/old-src/migrations/"0*.sh
sed -i.bak 's#^FILES="#FILES="\ndocs/old.md#' "$T/old-src/scripts/release/bundle.sh"
sh "$T/old-src/scripts/release/bundle.sh" --out "$T/old" --version 1.0.0 --release --commit old >/dev/null

check "manifest: version, ergo pinned to the release, min_admin, migrations" \
	"grep -q '\"version\": \"1.1.0\"' $T/new/.bundle/manifest.json && grep -q 'stack-ergo:1.1.0' $T/new/.bundle/manifest.json && grep -q '\"min_admin\"' $T/new/.bundle/manifest.json && grep -q '0001-bridge-client-webirc.sh' $T/new/.bundle/manifest.json"
check "manifest: AUTH_IMAGE default and the evrd.conf mount" \
	"grep -q '\"auth\": {.*\"env\": \"AUTH_IMAGE\"' $T/new/.bundle/manifest.json && grep -q '\"evrd\": {.*\"bridge/evrd.conf\"' $T/new/.bundle/manifest.json"
check "bundle: no tests, no CI, no .env, no install-specific files" \
	"! (cd $T/new && find . -name '*_test.go' -o -name '*.py' -o -path './test/*' -o -path './.github/*' -o -name .env -o -name boson.yaml | grep -q .)"
check "bundle: compose.yaml, the build contexts, the bind-mounted files" \
	"[ -f $T/new/compose.yaml ] && [ -f $T/new/images/ergo/Dockerfile ] && [ -f $T/new/images/wellknown/serve.sh ] && [ -f $T/new/monitoring/scrape.yml ] && [ -d $T/new/postgres/initdb ] && [ -f $T/new/scripts/stack-apply.sh ] && [ -f $T/new/migrations/lib.sh ]"
check "a dev bundle keeps main's image tags" \
	"scripts/release/bundle.sh --out $T/dev >/dev/null && grep -q 'stack-ergo:2.19.1' $T/dev/compose.yaml && grep -q 0.0.0-dev $T/dev/VERSION"

# ---- a 1.0.0 install, then 1.1.0 ------------------------------------------------
DIR=$T/install
mkdir -p "$DIR/config" "$DIR/bridge" "$DIR/secrets"
printf 'COMPOSE_PROFILES=auth,bridge,web,admin\nWEBIRC_PASSWORDS=irc.example.net=s3cret\nEVRD_IMAGE=ghcr.io/4evrd/evrd:0.61.0\n' > "$DIR/.env"
chmod 600 "$DIR/.env"
echo "mine: true" > "$DIR/config/boson.yaml"
echo "services: {}" > "$DIR/compose.override.yaml"
echo "managed_version Net" > "$DIR/bridge/evrd.conf"
echo key > "$DIR/secrets/k"
apply "$T/old" files "$T/old" >/dev/null
check "1.0.0 installed and recorded" "[ \"\$(cat $DIR/VERSION)\" = 1.0.0 ] && [ -f $DIR/.boson/stack-files.sha256 ] && [ -f $DIR/docs/old.md ]"

echo "# my tweak" >> "$DIR/traefik/dynamic/stack.yml"
plan=$(apply "$T/new" plan --diff "$T/new")
check "plan: scrape.yml update (with its diff), stack.yml edited here, old.md removed, migration pending" \
	"grep -q '^@@STACKFILE update monitoring/scrape.yml' <<<\"\$plan\" && grep -q '^@@D -# 1.0.0' <<<\"\$plan\" && grep -q '^@@STACKFILE local traefik/dynamic/stack.yml' <<<\"\$plan\" && grep -q '^@@STACKFILE remove docs/old.md' <<<\"\$plan\" && grep -q '^@@MIGRATION 0001-bridge-client-webirc.sh pending Bridge' <<<\"\$plan\""
check "plan changes nothing" "[ \"\$(cat $DIR/VERSION)\" = 1.0.0 ] && [ -f $DIR/docs/old.md ]"

mkdir -p "$T/backup"
out=$(BACKUP_DIR=$T/backup apply "$T/new" files "$T/new")
check "files: 1.1.0 in place" "[ \"\$(cat $DIR/VERSION)\" = 1.1.0 ] && cmp -s $DIR/compose.yaml $T/new/compose.yaml && cmp -s $DIR/monitoring/scrape.yml $T/new/monitoring/scrape.yml"
check "files: the local edit replaced, kept as .local" "grep -q 'my tweak' $DIR/traefik/dynamic/stack.yml.local && cmp -s $DIR/traefik/dynamic/stack.yml $T/new/traefik/dynamic/stack.yml"
check "files: a file 1.1.0 dropped is removed" "[ ! -e $DIR/docs/old.md ]"
check "files: .env, config/boson.yaml, compose.override.yaml, bridge/, secrets/ untouched" \
	"grep -q '^mine: true' $DIR/config/boson.yaml && grep -q 'services: {}' $DIR/compose.override.yaml && [ \"\$(cat $DIR/bridge/evrd.conf)\" = 'managed_version Net' ] && [ \"\$(cat $DIR/secrets/k)\" = key ] && grep -q '^WEBIRC_PASSWORDS=irc.example.net=s3cret\$' $DIR/.env"
check "files: added files listed for a rollback" "grep -qx 'traefik/dynamic/stack.yml.local' $T/backup/stack-added.txt"
check "files: the record is 1.1.0's" "cmp -s $DIR/.boson/stack-files.sha256 $T/new/.bundle/files.sha256"
check "files: executables stay executable" "[ -x $DIR/scripts/stack-apply.sh ] && [ -x $DIR/images/wellknown/serve.sh ]"

out=$(apply "$T/new" migrate "$T/new")
pw=$(sed -n 's/^BRIDGE_CLIENT_WEBIRC_PASSWORD=//p' "$DIR/.env")
check "migrate: password generated (48 hex), evrd-links appended, the grace added" \
	"[ \${#pw} = 48 ] && grep -qx \"WEBIRC_PASSWORDS=irc.example.net=s3cret,evrd-links=\$pw\" $DIR/.env && grep -qx 'managed_nick_guard_grace 60s' $DIR/bridge/evrd.conf"
check "migrate: recorded, evrd restart asked for, no secret in the output" \
	"grep -q '^0001-bridge-client-webirc.sh .* 1.1.0\$' $DIR/.boson/migrations.applied && grep -q '^@@RESTART evrd' <<<\"\$out\" && ! grep -q \"\$pw\" <<<\"\$out\" && ! grep -q s3cret <<<\"\$out\""
check "migrate: .env keeps mode 600" "[ \"\$(stat -c %a $DIR/.env 2>/dev/null || stat -f %Lp $DIR/.env)\" = 600 ]"
cp "$DIR/.env" "$T/env.after"
out=$(apply "$T/new" migrate "$T/new")
check "migrate again: done, nothing changes" "grep -q '^@@MIGRATION 0001-bridge-client-webirc.sh done' <<<\"\$out\" && cmp -s $DIR/.env $T/env.after && [ \$(wc -l < $DIR/.boson/migrations.applied) = 2 ]"
out=$(apply "$T/new" files "$T/new")
check "files again: everything the same" "! grep -v '^@@STACKFILE same' <<<\"\$out\" | grep -q '^@@STACKFILE'"

# ---- 0001 on installs that already have it, and ones that can't yet ------------
mig() { # mig ENV [EVRDCONF] -> runs 0001 on a scratch install, prints its exit code
	rm -rf "$T/m" && mkdir -p "$T/m/bridge" && printf '%b' "$1" > "$T/m/.env" && cp "$T/new/compose.yaml" "$T/m/"
	[ -z "${2:-}" ] || printf '%b' "$2" > "$T/m/bridge/evrd.conf"
	rc=0
	STACK_DIR=$T/m ENV_FILE=$T/m/.env MIGRATIONS_LIB=$T/new/migrations/lib.sh sh "$T/new/migrations/0001-bridge-client-webirc.sh" >"$T/m.out" 2>&1 || rc=$?
	echo "$rc"
}
done_env='COMPOSE_PROFILES=bridge\nBRIDGE_CLIENT_WEBIRC_PASSWORD=abc\nWEBIRC_PASSWORDS=irc.x=1,evrd-links=abc\n'
rc=$(mig "$done_env" 'managed_nick_guard_grace 30s\n')
check "0001 on an install fixed by hand: no-op (grace kept at 30s)" \
	"[ $rc = 0 ] && [ \"\$(printf '$done_env')\" = \"\$(cat $T/m/.env)\" ] && grep -qx 'managed_nick_guard_grace 30s' $T/m/bridge/evrd.conf && [ \$(grep -c grace $T/m/bridge/evrd.conf) = 1 ] && grep -q 'already done' $T/m.out"
rc=$(mig 'COMPOSE_PROFILES=irc,auth,web\n')
check "0001 without the bridge profile: nothing" "[ $rc = 0 ] && [ \"\$(cat $T/m/.env)\" = COMPOSE_PROFILES=irc,auth,web ]"
rc=$(mig 'COMPOSE_PROFILES=bridge\nEVRD_IMAGE=ghcr.io/4evrd/evrd:0.60.0\nWEBIRC_PASSWORDS=irc.x=1\n' 'x\n')
check "0001 with evrd 0.60 pinned: deferred (exit 3), nothing changed" "[ $rc = 3 ] && ! grep -q evrd-links $T/m/.env && ! grep -q grace $T/m/bridge/evrd.conf"
rc=$(mig 'COMPOSE_PROFILES=bridge\nBRIDGE_CLIENT_WEBIRC_PASSWORD=\nWEBIRC_PASSWORDS=\n')
check "0001 with empty values: filled in" "[ $rc = 0 ] && grep -q '^BRIDGE_CLIENT_WEBIRC_PASSWORD=[0-9a-f]\{48\}\$' $T/m/.env && grep -q '^WEBIRC_PASSWORDS=evrd-links=[0-9a-f]\{48\}\$' $T/m/.env && [ \$(grep -c '^BRIDGE_CLIENT_WEBIRC_PASSWORD=' $T/m/.env) = 1 ]"

# 0002: WELLKNOWN_SECTIONS set by hand gets "web" (stack #58).
mig2() {
	rm -rf "$T/m" && mkdir -p "$T/m" && printf '%b' "$1" > "$T/m/.env" && cp "$T/new/compose.yaml" "$T/m/"
	rc=0
	STACK_DIR=$T/m ENV_FILE=$T/m/.env MIGRATIONS_LIB=$T/new/migrations/lib.sh sh "$T/new/migrations/0002-wellknown-web.sh" >"$T/m.out" 2>&1 || rc=$?
	echo "$rc"
}
rc=$(mig2 'COMPOSE_PROFILES=auth,bridge,web\nWEB_HOST=chat.x\nWELLKNOWN_SECTIONS=irc,auth\n')
check "0002 on an add-on install: web added" "[ $rc = 0 ] && grep -qx 'WELLKNOWN_SECTIONS=irc,auth,web' $T/m/.env"
rc=$(mig2 'COMPOSE_PROFILES=auth,web\nWEB_HOST=chat.x\nWELLKNOWN_SECTIONS=irc,web,auth\n')
check "0002 with web listed: no-op" "[ $rc = 0 ] && grep -qx 'WELLKNOWN_SECTIONS=irc,web,auth' $T/m/.env && grep -q 'already done' $T/m.out"
rc=$(mig2 'COMPOSE_PROFILES=auth,web\nWEB_HOST=chat.x\n')
check "0002 without WELLKNOWN_SECTIONS: nothing (follows COMPOSE_PROFILES)" "[ $rc = 0 ] && ! grep -q WELLKNOWN $T/m/.env"
rc=$(mig2 'COMPOSE_PROFILES=auth,bridge\nWELLKNOWN_SECTIONS=irc,auth\n')
check "0002 without the web profile: nothing" "[ $rc = 0 ] && grep -qx 'WELLKNOWN_SECTIONS=irc,auth' $T/m/.env"

# A deferred migration isn't recorded and runs again on the next update.
DIR=$T/deferred
mkdir -p "$DIR/bridge"
printf 'COMPOSE_PROFILES=bridge\nEVRD_IMAGE=ghcr.io/4evrd/evrd:0.59.0\n' > "$DIR/.env"
apply "$T/new" files "$T/new" >/dev/null
out=$(apply "$T/new" migrate "$T/new")
check "deferred: reported, not recorded" "grep -q '^@@MIGRATION 0001-bridge-client-webirc.sh deferred' <<<\"\$out\" && ! grep -q 0001 $DIR/.boson/migrations.applied"
sed -i.bak 's/0.59.0/0.61.0/' "$DIR/.env"
out=$(apply "$T/new" migrate "$T/new")
check "deferred, then evrd updated: applied" "grep -q '^@@MIGRATION 0001-bridge-client-webirc.sh applied' <<<\"\$out\""

# A failing migration stops the run and isn't recorded.
cp -R "$T/new" "$T/broken"
printf '#!/bin/sh\n# Always fails.\nexit 1\n' > "$T/broken/migrations/0098-broken.sh"
printf '#!/bin/sh\n# After the broken one.\ntouch "$STACK_DIR/ran-0099"\n' > "$T/broken/migrations/0099-after.sh"
rc=0
apply "$T/broken" migrate "$T/broken" >/dev/null 2>&1 || rc=$?
check "a failing migration: non-zero, not recorded, the next one doesn't run" "[ $rc != 0 ] && ! grep -q 0098 $DIR/.boson/migrations.applied && [ ! -e $DIR/ran-0099 ]"

# ---- a pre-release install (files copied from main, no record) -----------------
DIR=$T/pre
mkdir -p "$DIR"
cp -R "$T/dev/." "$DIR/"
rm -rf "$DIR/.bundle" "$DIR/VERSION"
echo "# edited" >> "$DIR/README.md"
printf 'COMPOSE_PROFILES=irc\n' > "$DIR/.env"
plan=$(apply "$T/new" plan "$T/new")
check "pre-release: the changed files are 'unknown' (no record), nothing removed" \
	"grep -q '^@@STACKFILE unknown compose.yaml' <<<\"\$plan\" && grep -q '^@@STACKFILE unknown README.md' <<<\"\$plan\" && grep -q '^@@STACKFILE add VERSION' <<<\"\$plan\" && ! grep -q '^@@STACKFILE remove' <<<\"\$plan\""
apply "$T/new" files "$T/new" >/dev/null
check "pre-release: on 1.1.0, recorded" "[ \"\$(cat $DIR/VERSION)\" = 1.1.0 ] && cmp -s $DIR/.boson/stack-files.sha256 $T/new/.bundle/files.sha256"

# A bundle that lists a protected path is refused for that path.
cp -R "$T/new" "$T/evil"
echo "EVIL=1" > "$T/evil/.env"
printf '%s  .env\n' "$(sha256sum "$T/evil/.env" | cut -d' ' -f1)" >> "$T/evil/.bundle/files.sha256"
before=$(cat "$DIR/.env")
apply "$T/evil" files "$T/evil" >/dev/null
check "a bundle never writes .env" "[ \"\$(cat $DIR/.env)\" = \"\$before\" ]"

echo
if [ "$fails" -gt 0 ]; then
	echo "$fails failed"
	exit 1
fi
echo "all passed"
