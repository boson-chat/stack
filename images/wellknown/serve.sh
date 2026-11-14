#!/bin/sh
# wellknown profile: writes /.well-known/boson.json (client discovery) from
# the environment and serves it with busybox httpd on :8080 (Traefik routes
# https://DOMAIN/.well-known/boson.json here). Runs as nobody on a read-only
# root with a tmpfs at /run.
#
#   WELLKNOWN_SECTIONS  which sections to publish: irc, voice, auth
#                       (comma-separated; compose defaults it to COMPOSE_PROFILES)
#   IRC_HOST, IRC_PORT  irc section ({"host","port":6697,"tls":true})
#   AUTH_HOST           auth section ({"url":"https://AUTH_HOST"})
#   VOICE_HOST, VOICE_PORT  voice section ({"addr":"VOICE_HOST:7700","url":"https://VOICE_HOST"})
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

# One substitution per assignment, so set -e sees a failed check.
json='{"version":1'
if has irc; then
	h=$(host IRC_HOST "${IRC_HOST:-}")
	p=$(port IRC_PORT "${IRC_PORT:-6697}")
	json="$json,\"irc\":{\"host\":\"$h\",\"port\":$p,\"tls\":true}"
fi
if has auth; then
	h=$(host AUTH_HOST "${AUTH_HOST:-}")
	json="$json,\"auth\":{\"url\":\"https://$h\"}"
fi
if has voice; then
	h=$(host VOICE_HOST "${VOICE_HOST:-}")
	p=$(port VOICE_PORT "${VOICE_PORT:-7700}")
	json="$json,\"voice\":{\"addr\":\"$h:$p\",\"url\":\"https://$h\"}"
fi
json="$json}"

printf '%s\n' "$json" > "$root/.well-known/boson.json"
# busybox httpd has no built-in type for .json.
printf '.json:application/json\n' > /run/httpd.conf
echo "wellknown: /.well-known/boson.json = $json"

exec httpd -f -v -p 8080 -h "$root" -c /run/httpd.conf
