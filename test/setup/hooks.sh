#!/usr/bin/env bash
# Error-case hooks for the wizard e2e (wizard.spec.ts calls them; CI only,
# needs sudo). Everything they change is undone by the matching hook and by
# run.sh's cleanup.
set -euo pipefail
PIDFILE=${RUNNER_TEMP:-/tmp}/boson-e2e-6697.pid
case "${1:?hook}" in
block-udp)
	# This server's own firewall drops voice (7700/udp).
	sudo iptables -I INPUT -p udp --dport 7700 -j DROP -m comment --comment boson-e2e
	;;
unblock-udp)
	while sudo iptables -D INPUT -p udp --dport 7700 -j DROP -m comment --comment boson-e2e 2>/dev/null; do :; done
	;;
occupy-6697)
	# Another program on the IRC port: Ergo can't start.
	nohup python3 -c 'import socket,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("0.0.0.0", 6697)); s.listen()
time.sleep(3600)' >/dev/null 2>&1 &
	echo $! >"$PIDFILE"
	for _ in $(seq 1 20); do ss -ltn | grep -q ':6697 ' && exit 0; sleep 0.25; done
	echo "occupy-6697: nothing listens on 6697" >&2; exit 1
	;;
free-6697)
	[ -f "$PIDFILE" ] && kill "$(cat "$PIDFILE")" 2>/dev/null || true
	rm -f "$PIDFILE"
	for _ in $(seq 1 20); do ss -ltn | grep -q ':6697 ' || exit 0; sleep 0.25; done
	echo "free-6697: 6697 is still taken" >&2; exit 1
	;;
*) echo "unknown hook $1" >&2; exit 2 ;;
esac
