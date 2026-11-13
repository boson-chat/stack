#!/usr/bin/env python3
"""Smoke test: TLS connect to IRC, get the welcome, and get a reply from NickServ."""
import socket, ssl, sys, time
ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
s = ctx.wrap_socket(socket.create_connection((sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1", 6697), timeout=15), server_hostname="irc.localhost")
print("TLS:", s.version(), s.cipher()[0])
f = s.makefile("rwb", buffering=0)
def send(l): s.sendall((l + "\r\n").encode())
send("NICK stacktest"); send("USER stacktest 0 * :stack smoke test")
seen = {"001": False, "nickserv": False}
deadline = time.time() + 25
while time.time() < deadline:
    line = f.readline().decode(errors="replace").rstrip()
    if not line: break
    if line.startswith("PING"): send("PONG" + line[4:])
    parts = line.split()
    if len(parts) > 1 and parts[1] == "001":
        seen["001"] = True; print("welcome:", line[:120])
        send("PRIVMSG NickServ :HELP REGISTER")
    if "NickServ" in line and "NOTICE" in line and not seen["nickserv"]:
        seen["nickserv"] = True; print("nickserv:", line[:140])
        send("QUIT :done")
    if all(seen.values()): break
print("RESULT", seen)
sys.exit(0 if all(seen.values()) else 1)
