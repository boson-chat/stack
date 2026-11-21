#!/bin/sh
# Renders /etc/webircgateway/config.conf from the environment and runs the
# gateway. The policy (ports, aliases, lists, per-IP caps, WEBIRC passwords)
# is read by the gateway itself from WEBIRC_* variables (src/zz_boson_policy.go,
# docs/web.md); this file only holds webircgateway's own settings:
#
#   WEBIRC_ORIGINS          pages allowed to connect, comma-separated
#                           ("https://chat.boson.chat"); empty = any page
#   WEBIRC_TRUSTED_PROXIES  CIDRs whose X-Forwarded-For is believed
#                           (default 10.203.53.0/24, the stack network)
#   WEBIRC_THROTTLE         lines/s a registered client may send (default 4;
#                           WEBIRC_THROTTLE_BURST lines at once, default 20)
#   WEBIRC_TIMEOUT          seconds to connect to a network (default 10)
#   WEBIRC_GATEWAY_NAME     the name in WEBIRC lines (default boson)
#   WEBIRC_LOG_LEVEL        1 debug (logs IRC lines!) 2 info 3 warn (default 2)
#   WEBIRC_SECRET           signs EXTJWT tokens (default: random per start)
set -eu

conf=${WEBIRC_CONFIG:-/etc/webircgateway/config.conf}
secret=${WEBIRC_SECRET:-$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')}
level=${WEBIRC_LOG_LEVEL:-2}
case "$level" in 1|2|3) ;; *) echo "entrypoint: WEBIRC_LOG_LEVEL must be 1, 2 or 3" >&2; exit 2;; esac
if [ "$level" = 1 ]; then
	echo "entrypoint: WEBIRC_LOG_LEVEL=1 logs IRC traffic, passwords included; never in production" >&2
fi

list() { # comma list → one quoted line each
	printf '%s\n' "$1" | tr ',' '\n' | while read -r v; do
		[ -n "$v" ] && printf '"%s"\n' "$v"
	done
	return 0
}

umask 077
{
	cat <<CONF
logLevel = $level
identd = false
gateway_name = "${WEBIRC_GATEWAY_NAME:-boson}"
secret = "$secret"
send_quit_on_client_close = "Web client closed"

[verify]
required = false

[clients]

[server.1]
bind = "0.0.0.0"
port = 8080

[fileserving]
enabled = false

[transports]
websocket

[allowed_origins]
$(list "${WEBIRC_ORIGINS:-}")

[reverse_proxies]
$(list "${WEBIRC_TRUSTED_PROXIES:-10.203.53.0/24}")

[gateway]
enabled = true
timeout = ${WEBIRC_TIMEOUT:-10}
throttle = ${WEBIRC_THROTTLE:-4}
protocol = tcp

[gateway.whitelist]

[gateway.webirc]

[dnsbl]
action = verify

[dnsbl.servers]
CONF
} > "$conf"

exec /usr/local/bin/webircgateway -config "$conf" "$@"
