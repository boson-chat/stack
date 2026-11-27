#!/usr/bin/env bash
# install.sh's host firewall step (--firewall) against a fake ufw, id and
# uname on PATH: the node-exporter rule is added once, from .env's
# STACK_SUBNET / NODE_EXPORTER_ADDR (or the defaults), only with ufw active
# and the metrics profile on. No root, no Docker, no real firewall.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fails=0
ok() { echo "ok   $*"; }
bad() { echo "FAIL $*"; fails=$((fails + 1)); }

mkdir -p "$T/bin"
# ufw: rules in $UFW_STATE/rules (one "To Action From # comment" line each),
# every "allow" call logged in $UFW_STATE/calls.
cat >"$T/bin/ufw" <<'UFW'
#!/bin/sh
set -eu
case "$1" in
status)
	if [ -f "$UFW_STATE/inactive" ]; then echo "Status: inactive"; exit 0; fi
	echo "Status: active"
	echo
	echo "To                         Action      From"
	echo "--                         ------      ----"
	[ -f "$UFW_STATE/rules" ] && cat "$UFW_STATE/rules"
	exit 0 ;;
allow)
	echo "$*" >>"$UFW_STATE/calls"
	# allow from S to A port P proto T comment C
	[ "$2" = from ] && [ "$4" = to ] && [ "$6" = port ] && [ "$8" = proto ] && [ "${10}" = comment ] || { echo "fake ufw: unexpected: $*" >&2; exit 1; }
	printf '%-26s ALLOW       %-26s # %s\n' "$5 $7/$9" "$3" "${11}" >>"$UFW_STATE/rules"
	echo "Rule added" ;;
*) echo "fake ufw: unexpected: $*" >&2; exit 1 ;;
esac
UFW
cat >"$T/bin/id" <<'ID'
#!/bin/sh
[ "${1:-}" = -u ] && { echo 0; exit 0; }
exec /usr/bin/id "$@"
ID
cat >"$T/bin/uname" <<'UNAME'
#!/bin/sh
case "${1:-}" in -m) echo x86_64 ;; *) echo Linux ;; esac
UNAME
chmod +x "$T/bin/"*

# run NAME: a fresh ufw state and install dir; NAME's .env from stdin ("" = none).
run() {
	export UFW_STATE=$T/$1
	DIR=$T/$1/dir
	mkdir -p "$DIR"
	env=$(cat)
	[ -z "$env" ] || printf '%s\n' "$env" >"$DIR/.env"
}
fw() { PATH="$T/bin:$PATH" sh "$ROOT/install.sh" --yes --firewall --dir "$DIR" 2>&1; }
calls() { [ -f "$UFW_STATE/calls" ] && wc -l <"$UFW_STATE/calls" | tr -d ' ' || echo 0; }

# A new install (no .env yet: the wizard turns metrics on): the defaults, once.
run fresh </dev/null
out=$(fw)
want="allow from 10.203.53.0/24 to 10.203.53.1 port 9100 proto tcp comment boson: metrics -> node-exporter"
if [ "$(calls)" = 1 ] && [ "$(cat "$UFW_STATE/calls")" = "$want" ] && [ "$(printf '%s\n' "$out" | wc -l | tr -d ' ')" = 1 ] &&
	[[ $out == "Firewall (ufw): allowed 10.203.53.0/24 to reach node-exporter on 10.203.53.1:9100/tcp (host metrics)." ]]; then
	ok "new install: the default rule, one line"
else bad "new install: calls=$(calls) out=$out"; fi
out=$(fw)
if [ "$(calls)" = 1 ] && [[ $out == *"already reaches node-exporter"* ]]; then
	ok "run again: not added twice"
else bad "run again: calls=$(calls) out=$out"; fi

# The rule added by hand earlier (the old docs' comment): left alone.
run byhand </dev/null
printf '%-26s ALLOW       %-26s # boson node-exporter\n' "10.203.53.1 9100/tcp" "10.203.53.0/24" >"$UFW_STATE/rules"
out=$(fw)
if [ "$(calls)" = 0 ] && [[ $out == *"already reaches"* ]]; then ok "a rule added by hand counts"; else bad "by hand: calls=$(calls) out=$out"; fi

# .env with metrics and another subnet (quoted, an inline comment).
run custom <<'ENV'
COMPOSE_PROFILES=irc,auth,api,web,admin,metrics,voice
STACK_SUBNET="10.99.7.0/24"
NODE_EXPORTER_ADDR=10.99.7.1 # the gateway
ENV
out=$(fw)
if [ "$(calls)" = 1 ] && grep -q '^allow from 10.99.7.0/24 to 10.99.7.1 port 9100 proto tcp ' "$UFW_STATE/calls" && [[ $out == *"allowed 10.99.7.0/24"* ]]; then
	ok ".env's STACK_SUBNET and NODE_EXPORTER_ADDR"
else bad "custom: calls=$(calls) out=$out"; fi
fw >/dev/null
if [ "$(calls)" = 1 ]; then ok "custom subnet: once"; else bad "custom subnet twice: calls=$(calls)"; fi

# Metrics off: nothing.
run nometrics <<'ENV'
COMPOSE_PROFILES=irc,auth,api,web
ENV
out=$(fw)
if [ "$(calls)" = 0 ] && [[ $out == *"metrics profile is off"* ]]; then ok "metrics off: no rule"; else bad "metrics off: calls=$(calls) out=$out"; fi

# ufw inactive: nothing, silent.
run inactive </dev/null
touch "$UFW_STATE/inactive"
out=$(fw)
if [ "$(calls)" = 0 ] && [ -z "$out" ]; then ok "ufw inactive: nothing"; else bad "inactive: calls=$(calls) out=$out"; fi

[ "$fails" = 0 ] || { echo "$fails failed"; exit 1; }
echo "all passed"
