#!/usr/bin/env python3
"""REHASH over IRC as the fixture's oper (admin / fixture-oper) and report
the outcome: exit 0 on "Successfully rehashed", 1 with the server's error
lines otherwise. Used by run.sh apply for InspIRCd, which tells the
rehashing oper (and only its log file otherwise)."""
import argparse
import secrets
import socket
import sys
import time

ap = argparse.ArgumentParser()
ap.add_argument("--host", default="127.0.0.1")
ap.add_argument("--port", type=int, required=True)
ap.add_argument("--oper", default="admin")
ap.add_argument("--password", default="fixture-oper")
a = ap.parse_args()

s = socket.create_connection((a.host, a.port), timeout=20)
f = s.makefile("rwb")


def send(line):
    f.write((line + "\r\n").encode())
    f.flush()


nick = "rehash" + secrets.token_hex(2)
send(f"NICK {nick}")
send("USER rehash 0 * :run.sh apply")
errors, done, rehashing = [], None, False
deadline = time.time() + 30
while time.time() < deadline and done is None:
    raw = f.readline()
    if not raw:
        break
    line = raw.decode(errors="replace").rstrip("\r\n")
    parts = line.split(" ")
    if parts[0] == "PING":
        send("PONG " + " ".join(parts[1:]))
        continue
    cmd = parts[1] if len(parts) > 1 else ""
    text = line.split(" :", 1)[1] if " :" in line else ""
    if cmd in ("376", "422"):
        send(f"OPER {a.oper} {a.password}")
    elif cmd == "381":
        send("REHASH")
        rehashing = True
    elif cmd in ("491", "464"):
        done = False
        errors.append("OPER refused: " + text)
    elif cmd == "NOTICE" and rehashing and text.startswith("*** "):
        if "Successfully rehashed" in text:
            done = True
        elif "rehash" not in text.lower() or "error" in text.lower():
            errors.append(text[4:])
            if "error" in text.lower():
                done = False
send("QUIT :done")
for e in errors:
    print(e, file=sys.stderr)
if done:
    print("Successfully rehashed")
sys.exit(0 if done else 1)
