#!/bin/sh
# wellknown profile: writes /.well-known/boson.json (client discovery) from
# the environment and serves it with busybox httpd on :8080 (Traefik routes
# https://DOMAIN/.well-known/boson.json here). Runs as nobody on a read-only
# root with a tmpfs at /run.
#
#   WELLKNOWN_SECTIONS  which sections to publish: irc, voice, auth, files, web
#                       (comma-separated; compose defaults it to COMPOSE_PROFILES)
#   IRC_HOST, IRC_PORT  irc section ({"host","port":6697,"tls":true})
#   AUTH_HOST           auth section ({"url":"https://AUTH_HOST"}), plus the
#                       network's branding: "branding":"https://AUTH_HOST/v1/branding"
#                       (docs/branding.md)
#   VOICE_HOST, VOICE_PORT  voice section ({"addr":"VOICE_HOST:7700","url":"https://VOICE_HOST"})
#   FILES_HOST          files section ({"url":"https://FILES_HOST"}): uploads
#                       (docs/files.md)
#   PUSH_RELAY_URL      with the irc section: push notifications
#                       ({"version":1,"relay":"PUSH_RELAY_URL"}, docs/push.md):
#                       the relay the phone apps should use with this install
#                       (compose default https://push.boson.chat). Empty: the
#                       block has no relay (phone push off; browser push still
#                       works, straight from Ergo).
#   WEB_HOST            web section ({"url":"https://WEB_HOST"}): this
#                       install's web client (docs/web.md). Other installs'
#                       auth lists it in /v1/web-origins, so their voice,
#                       video and files accept its calls. Left out when
#                       WEB_HOST is empty.
#   WELLKNOWN_PROFILES  COMPOSE_PROFILES, only for a warning: the web profile
#                       on with WEB_HOST set but WELLKNOWN_SECTIONS without
#                       web means other installs refuse this web client.
#   WELLKNOWN_FEDERATION_URL  with the auth section: auth's federation
#                       metadata (http://auth:8080/.well-known/boson-federation.json),
#                       published verbatim as "federation" (docs/federation.md).
#                       Fetched again every WELLKNOWN_REFRESH_SECONDS (60), since
#                       its "accepts" follows the admin setting; when auth can't
#                       be reached the last good copy stays, and until the first
#                       fetch the section is left out. Empty: no federation section.
set -eu

root=/run/www
mkdir -p "$root/.well-known"

has() { case ",$(echo "${WELLKNOWN_SECTIONS:-}" | tr -d ' ')," in *",$1,"*) return 0 ;; esac; return 1; }

# Hostnames and ports go into JSON unescaped, so only allow what a hostname
# or port can contain.
host() {
	case "$2" in
	"" | *[!A-Za-z0-9.-]*) echo "wellknown: $1=\"$2\" is not a hostname" >&2; exit 1 ;;
	esac
	printf '%s' "$2"
}
port() {
	case "$2" in
	"" | *[!0-9]*) echo "wellknown: $1=\"$2\" is not a port" >&2; exit 1 ;;
	esac
	printf '%s' "$2"
}

# An https URL with nothing JSON would need escaped.
url() {
	case "$2" in
	https://?*) ;;
	*) echo "wellknown: $1=\"$2\" is not an https:// URL" >&2; exit 1 ;;
	esac
	case "$2" in
	*[!A-Za-z0-9.:/_~%-]*) echo "wellknown: $1=\"$2\" is not an https:// URL" >&2; exit 1 ;;
	esac
	printf '%s' "$2"
}

# One substitution per assignment, so set -e sees a failed check.
json='{"version":1'
if has irc; then
	h=$(host IRC_HOST "${IRC_HOST:-}")
	p=$(port IRC_PORT "${IRC_PORT:-6697}")
	json="$json,\"irc\":{\"host\":\"$h\",\"port\":$p,\"tls\":true}"
	# Push goes with the IRC server (Ergo sends it); clients still check
	# that it advertises draft/webpush.
	if [ -n "${PUSH_RELAY_URL:-}" ]; then
		r=$(url PUSH_RELAY_URL "$PUSH_RELAY_URL")
		json="$json,\"push\":{\"version\":1,\"relay\":\"$r\"}"
	else
		json="$json,\"push\":{\"version\":1}"
	fi
fi
if has auth; then
	h=$(host AUTH_HOST "${AUTH_HOST:-}")
	json="$json,\"auth\":{\"url\":\"https://$h\"},\"branding\":\"https://$h/v1/branding\""
fi
if has voice; then
	h=$(host VOICE_HOST "${VOICE_HOST:-}")
	p=$(port VOICE_PORT "${VOICE_PORT:-7700}")
	json="$json,\"voice\":{\"addr\":\"$h:$p\",\"url\":\"https://$h\"}"
fi
if has files; then
	h=$(host FILES_HOST "${FILES_HOST:-}")
	json="$json,\"files\":{\"url\":\"https://$h\"}"
fi
if has web && [ -n "${WEB_HOST:-}" ]; then
	h=$(host WEB_HOST "$WEB_HOST")
	json="$json,\"web\":{\"url\":\"https://$h\"}"
elif [ -n "${WEB_HOST:-}" ]; then
	case ",$(echo "${WELLKNOWN_PROFILES:-}" | tr -d ' ')," in
	*,web,*) echo "wellknown: warning: the web profile is on and WEB_HOST is set, but WELLKNOWN_SECTIONS ($WELLKNOWN_SECTIONS) has no web: boson.json doesn't name the web client, so other installs' voice, video and files refuse it. Add web to WELLKNOWN_SECTIONS in .env." >&2 ;;
	esac
fi

fed_url=""
if has auth; then fed_url=${WELLKNOWN_FEDERATION_URL:-}; fi
every=$(port WELLKNOWN_REFRESH_SECONDS "${WELLKNOWN_REFRESH_SECONDS:-60}")

# federation: auth's metadata on one line, or nothing (unreachable, or not a
# JSON object with an https issuer: an auth older than 1.24 answers 404).
federation() {
	body=$(wget -q -T 5 -O - "$fed_url" 2>/dev/null | tr -d '\r\n') || return 1
	case "$body" in
	'{'*'"issuer":"https://'*'}') printf '%s' "$body" ;;
	*) return 1 ;;
	esac
}

fed=""
# write: boson.json with the current federation section, replaced atomically.
write() {
	doc=$json
	[ -z "$fed" ] || doc="$doc,\"federation\":$fed"
	printf '%s}\n' "$doc" > "$root/.well-known/boson.json.tmp"
	mv "$root/.well-known/boson.json.tmp" "$root/.well-known/boson.json"
	echo "wellknown: /.well-known/boson.json = $doc}"
}

write
# busybox httpd has no built-in type for .json.
printf '.json:application/json\n' > /run/httpd.conf
httpd -f -v -p 8080 -h "$root" -c /run/httpd.conf &
httpd=$!
trap 'kill "$httpd" 2>/dev/null; exit 0' TERM INT

[ -n "$fed_url" ] || { wait "$httpd"; exit $?; }
# Every few seconds until auth first answers, then every $every seconds.
while kill -0 "$httpd" 2>/dev/null; do
	if new=$(federation); then
		[ "$new" = "$fed" ] || { fed=$new; write; }
		pause=$every
	else
		[ -n "$fed" ] && echo "wellknown: $fed_url didn't answer; keeping the last federation section" >&2
		pause=5
		[ -z "$fed" ] || pause=$every
	fi
	sleep "$pause" &
	wait $! || true
done
wait "$httpd"
