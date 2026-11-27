#!/bin/sh
# Boson installer (served at https://get.boson.chat):
#
#   curl -fsSL https://get.boson.chat | sudo sh
#   curl -fsSL https://get.boson.chat | sudo sh -s -- --yes
#
# Checks the OS, installs Docker if it's missing (Docker's official
# convenience script, after asking), then starts the setup wizard
# (the stack-setup image) and prints where to open it and its one-time code.
# Running it again is safe: it restarts the wizard with a new code, and the
# wizard keeps an existing install's secrets and admin.
#
# The setup image carries the newest stack release (its bundle: compose
# files, migrations, docs/updates.md "Stack releases"); the wizard records
# the installed release so the admin panel can move it to newer ones.
#
# Options:
#   -y, --yes        don't ask (install Docker if needed)
#   --dir DIR        install directory (default /opt/boson-stack)
#   --image IMAGE    setup image (default harbor.floret.dev/boson/stack-setup:latest,
#                    the newest stack release; :<version> for an older one)
#   --edge           the setup image of main, unreleased (stack-setup:edge;
#                    for testing)
#   --port PORT      wizard port (default 8443)
#   --no-pull        use the image as it is locally (tests)
#   --firewall       only the host firewall step below, then exit (an install
#                    that already runs: curl -fsSL https://get.boson.chat | sudo sh -s -- --firewall)
#
# Host firewall: with ufw active, it allows the stack network to reach
# node-exporter (the metrics profile, host metrics: the admin panel's job
# "node"), which listens on the stack bridge's gateway; ufw's default-deny
# INPUT drops that otherwise. One rule, added once: from STACK_SUBNET to
# NODE_EXPORTER_ADDR port 9100/tcp (.env; default 10.203.53.0/24 and
# 10.203.53.1). Nothing else in the firewall changes.
set -eu

IMAGE=${BOSON_SETUP_IMAGE:-harbor.floret.dev/boson/stack-setup:latest}
DIR=${BOSON_INSTALL_DIR:-/opt/boson-stack}
PORT=${BOSON_SETUP_PORT:-8443}
YES=false
PULL=true
FIREWALL_ONLY=false
NAME=boson-setup

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	-y | --yes) YES=true ;;
	--dir) DIR=${2:?--dir needs a directory}; shift ;;
	--image) IMAGE=${2:?--image needs an image}; shift ;;
	--edge) IMAGE=${IMAGE%:*}:edge ;;
	--port) PORT=${2:?--port needs a number}; shift ;;
	--no-pull) PULL=false ;;
	--firewall) FIREWALL_ONLY=true ;;
	-h | --help) sed -n '2,35p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
	shift
done
case "$PORT" in *[!0-9]* | '') die "--port must be a number" ;; esac
case "$DIR" in /*) ;; *) die "--dir must be an absolute path" ;; esac

# confirm QUESTION: yes with --yes; otherwise ask on the terminal.
confirm() {
	$YES && return 0
	if [ ! -r /dev/tty ]; then
		die "$1 Re-run with --yes to answer yes without a terminal."
	fi
	printf '%s [y/N] ' "$1" >/dev/tty
	read -r reply </dev/tty || reply=
	case "$reply" in [yY] | [yY][eE][sS]) return 0 ;; esac
	return 1
}

# ---- the OS -------------------------------------------------------------------
[ "$(uname -s)" = Linux ] || die "Boson runs on Linux (this is $(uname -s))."
case "$(uname -m)" in
x86_64 | amd64 | aarch64 | arm64) ;;
*) die "unsupported CPU architecture $(uname -m): the images are built for amd64 and arm64." ;;
esac
OS_ID=unknown OS_VERSION=
if [ -r /etc/os-release ]; then
	# shellcheck disable=SC1091
	OS_ID=$(. /etc/os-release && printf '%s' "${ID:-unknown}")
	# shellcheck disable=SC1091
	OS_VERSION=$(. /etc/os-release && printf '%s' "${VERSION_ID:-}")
fi
$FIREWALL_ONLY || case "$OS_ID" in
ubuntu | debian | fedora | centos | rhel | rocky | almalinux | raspbian) ;;
*)
	say "This is $OS_ID $OS_VERSION: not one of the systems Boson is tested on (Ubuntu, Debian, Fedora, RHEL and friends)."
	confirm "Continue anyway?" || exit 1
	;;
esac
if [ "$(id -u)" != 0 ]; then
	if command -v sudo >/dev/null 2>&1 && [ -f "$0" ]; then
		flags=
		if $YES; then flags=--yes; fi
		if ! $PULL; then flags="$flags --no-pull"; fi
		if $FIREWALL_ONLY; then flags="$flags --firewall"; fi
		# shellcheck disable=SC2086 # fixed words
		exec sudo BOSON_SETUP_IMAGE="$IMAGE" BOSON_INSTALL_DIR="$DIR" BOSON_SETUP_PORT="$PORT" \
			BOSON_SETUP_DNS_SERVER="${BOSON_SETUP_DNS_SERVER:-}" BOSON_SETUP_GITHUB_TOKEN="${BOSON_SETUP_GITHUB_TOKEN:-}" sh "$0" $flags
	fi
	die "run it as root: curl -fsSL https://get.boson.chat | sudo sh"
fi

# ---- the host firewall (ufw) --------------------------------------------------
# env_get KEY: KEY's value in DIR/.env (the last one; "" when unset).
env_get() {
	[ -f "$DIR/.env" ] || return 0
	sed -n "s/^[[:space:]]*$1=//p" "$DIR/.env" | tail -n 1 |
		sed -e 's/[[:space:]]#.*$//' -e 's/[[:space:]]*$//' -e "s/^[\"']//" -e "s/[\"']\$//"
}

# ufw_node_exporter: with ufw active and the metrics profile on (a new
# install: the wizard always turns it on), allow STACK_SUBNET to reach
# node-exporter on NODE_EXPORTER_ADDR:9100/tcp. Idempotent; one line of
# output when ufw is active.
ufw_node_exporter() {
	command -v ufw >/dev/null 2>&1 || return 0
	ufw status 2>/dev/null | grep -q '^Status: active' || return 0
	if [ -f "$DIR/.env" ]; then
		case ",$(env_get COMPOSE_PROFILES | tr -d ' ')," in
		*,metrics,*) ;;
		*) say "Firewall (ufw): the metrics profile is off, no node-exporter rule needed."; return 0 ;;
		esac
	fi
	subnet=$(env_get STACK_SUBNET)
	subnet=${subnet:-10.203.53.0/24}
	addr=$(env_get NODE_EXPORTER_ADDR)
	addr=${addr:-10.203.53.1}
	if ufw status 2>/dev/null | awk -v a="$addr" -v s="$subnet" '$1 == a && $2 == "9100/tcp" && $3 == "ALLOW" && $4 == s { f = 1 } END { exit !f }'; then
		say "Firewall (ufw): $subnet already reaches node-exporter on $addr:9100/tcp (host metrics)."
	elif ufw allow from "$subnet" to "$addr" port 9100 proto tcp comment 'boson: metrics -> node-exporter' >/dev/null; then
		say "Firewall (ufw): allowed $subnet to reach node-exporter on $addr:9100/tcp (host metrics)."
	else
		say "Firewall (ufw): couldn't add the node-exporter rule; run: ufw allow from $subnet to $addr port 9100 proto tcp comment 'boson: metrics -> node-exporter'" >&2
	fi
}
if $FIREWALL_ONLY; then
	ufw_node_exporter
	exit 0
fi
mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
if [ "${mem_kb:-0}" -lt 1800000 ]; then
	say "Note: this server has $((mem_kb / 1024)) MB of memory; Boson wants about 2 GB."
fi

# ---- Docker -------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
	say "Docker isn't installed. It can be installed with Docker's official script (https://get.docker.com)."
	confirm "Install Docker now?" || die "Boson needs Docker. Install it (https://docs.docker.com/engine/install/) and run this again."
	command -v curl >/dev/null 2>&1 || die "installing Docker needs curl"
	tmp=$(mktemp)
	trap 'rm -f "$tmp"' EXIT
	curl -fsSL https://get.docker.com -o "$tmp"
	sh "$tmp"
	rm -f "$tmp"
	trap - EXIT
	if command -v systemctl >/dev/null 2>&1; then systemctl enable --now docker >/dev/null 2>&1 || true; fi
fi
docker info >/dev/null 2>&1 || die "Docker is installed but not running (try: systemctl start docker)."

ufw_node_exporter

# ---- the wizard ---------------------------------------------------------------
if docker inspect "$NAME" >/dev/null 2>&1; then
	say "Restarting the setup wizard (a new code)."
	docker rm -f "$NAME" >/dev/null
fi
if $PULL; then
	say "Downloading $IMAGE ..."
	docker pull -q "$IMAGE" >/dev/null || die "couldn't download $IMAGE"
fi
mkdir -p "$DIR"
chmod 755 "$DIR"

# Host network: the wizard checks this host's addresses and ports. The
# install dir is mounted at the same path, because compose (run from the
# wizard through the Docker socket) resolves the stack's bind mounts on
# the host.
set -- docker run -d --name "$NAME" --restart no --network host \
	--label org.boson.setup=wizard \
	-v /var/run/docker.sock:/var/run/docker.sock \
	-v "$DIR:$DIR" \
	-e BOSON_INSTALL_DIR="$DIR" -e BOSON_SETUP_PORT="$PORT" -e BOSON_SETUP_IMAGE="$IMAGE"
# Test hooks (CI): another DNS server for the wizard's checks.
[ -z "${BOSON_SETUP_DNS_SERVER:-}" ] || set -- "$@" -e BOSON_SETUP_DNS_SERVER="$BOSON_SETUP_DNS_SERVER"
# Optional: a GitHub token (no scopes) for the wizard's release checks (the
# newest version of each component); without one GitHub allows 60 an hour.
[ -z "${BOSON_SETUP_GITHUB_TOKEN:-}" ] || set -- "$@" -e BOSON_SETUP_GITHUB_TOKEN
"$@" "$IMAGE" >/dev/null

code=
for _ in $(seq 1 60); do
	if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" != true ]; then
		docker logs "$NAME" 2>&1 | tail -20 >&2
		die "the setup wizard stopped (see above)"
	fi
	code=$(docker exec "$NAME" stack-setup code 2>/dev/null || true)
	[ -n "$code" ] && break
	sleep 1
done
[ -n "$code" ] || die "the setup wizard didn't start (docker logs $NAME)"
fp=$(docker exec "$NAME" stack-setup fingerprint 2>/dev/null || true)

ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src") {print $(i + 1); exit}}')
[ -n "$ip" ] || ip=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$ip" ] || ip="<this server's IP>"

say ""
say "Boson setup is running."
say ""
say "  Open https://$ip:$PORT and enter code $code"
say ""
say "The page has a self-signed certificate, so your browser warns once."
[ -z "$fp" ] || say "Its SHA-256 fingerprint: $fp"
say "Port $PORT/tcp must be reachable; or tunnel it: ssh -L $PORT:localhost:$PORT root@$ip, then open https://localhost:$PORT"
say "The code works once. Run this script again for a new one."
