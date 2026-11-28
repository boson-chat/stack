#!/bin/sh
# boson.json names this install's web client (web.url) when WELLKNOWN_SECTIONS lists sections by hand: "web" added (stack #58).
#
# With directory-mode origins (#58), other installs' voice, video and files
# accept a web client only when its boson.json says "web": {"url": ...}.
# An install that sets WELLKNOWN_SECTIONS by hand (add-on installs from an
# earlier wizard: irc,auth,...) never got the web section. Added when the
# web profile is on and WEB_HOST is set; an unset WELLKNOWN_SECTIONS follows
# COMPOSE_PROFILES and needs nothing.
#
# recheck: every update
# (it was lost once: a rollback put back .env from before it ran but kept
# its record in .boson/migrations.applied.)
set -eu
# shellcheck source=migrations/lib.sh
. "$MIGRATIONS_LIB"

has_profile web || exit 0
[ -n "$(env_get WEB_HOST)" ] || exit 0
cur=$(env_get WELLKNOWN_SECTIONS | tr -d ' ')
[ -n "$cur" ] || { say "WELLKNOWN_SECTIONS not set: follows COMPOSE_PROFILES"; exit 0; }
case ",$cur," in
*,web,*) say "already done" ;;
*)
	env_set WELLKNOWN_SECTIONS "$cur,web"
	say "WELLKNOWN_SECTIONS: web added"
	;;
esac
