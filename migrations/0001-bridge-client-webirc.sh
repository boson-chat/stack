#!/bin/sh
# Bridge (add-on installs): the web gateway's WEBIRC password to evrd-links, and the nick guard's 60s grace in bridge/evrd.conf (stack #55).
#
# Installs from an earlier wizard (docs/bridge.md "Upgrading an older add-on
# install"): BRIDGE_CLIENT_WEBIRC_PASSWORD generated (kept when set),
# ",evrd-links=<it>" appended to WEBIRC_PASSWORDS (unless an evrd-links
# entry is there), and "managed_nick_guard_grace 60s" added to
# bridge/evrd.conf (unless it has a managed_nick_guard_grace line). Needs
# evrd 0.61 or later (older ones refuse the directive): with an older
# EVRD_IMAGE pinned it waits for the next update.
set -eu
# shellcheck source=migrations/lib.sh
. "$MIGRATIONS_LIB"

has_profile bridge || exit 0

tag=$(image_tag "$(image_of EVRD_IMAGE)")
if [ -n "$tag" ] && ! version_ge "$tag" 0.61.0; then
	defer "evrd $tag is older than 0.61.0; update evrd (EVRD_IMAGE) first"
fi

changed=false
pw=$(env_get BRIDGE_CLIENT_WEBIRC_PASSWORD)
if [ -z "$pw" ]; then
	pw=$(gen_secret 24)
	env_set BRIDGE_CLIENT_WEBIRC_PASSWORD "$pw"
	say "BRIDGE_CLIENT_WEBIRC_PASSWORD: generated"
	changed=true
fi
cur=$(env_get WEBIRC_PASSWORDS)
case ",$cur," in
*,evrd-links=*) ;;
*)
	env_set WEBIRC_PASSWORDS "${cur:+$cur,}evrd-links=$pw"
	say "WEBIRC_PASSWORDS: evrd-links added"
	changed=true
	;;
esac

conf=$STACK_DIR/bridge/evrd.conf
if [ -f "$conf" ] && ! file_has_line "$conf" '^[[:space:]]*managed_nick_guard_grace[[:space:]]'; then
	file_append "$conf" \
		"# Time to identify with NickServ on a registered nick before being moved off it." \
		"managed_nick_guard_grace 60s"
	say "bridge/evrd.conf: managed_nick_guard_grace 60s added"
	# evrd reads its config at start; compose doesn't see a file change.
	restart evrd
	changed=true
fi
$changed || say "already done"
