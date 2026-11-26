#!/usr/bin/env python3
"""After the wizard (test/setup/run.sh): does the installed stack work?

1. Every certificate is the ACME CA's (Pebble in CI), not Traefik's default.
2. The admin the wizard created logs in to auth and over IRC (SASL PLAIN),
   is the Owner, and exists once.
3. A new user signs up (POST /v1/register) and logs in over IRC.
4. Both join a channel; a message from one reaches the other.
5. After reconnecting, CHATHISTORY returns that message (history).
6. auth issues the new user a voice token (POST /v1/voice/token).
7. The new user uploads a file to https://FILES_HOST (POST /v1/files).

  test/setup/smoke-result.py --domain boson.test --admin alice --password '...'
"""
import argparse, base64, json, os, secrets, socket, ssl, subprocess, sys, time

p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1")
p.add_argument("--domain", required=True)
p.add_argument("--admin", required=True)
p.add_argument("--password", required=True)
p.add_argument("--dir", default="/opt/boson-stack")
p.add_argument("--issuer", default="Pebble", help="expected certificate issuer (substring)")
p.add_argument("--user-file", default="", help="reuse/save the second user (re-run checks)")
args = p.parse_args()
D = args.domain
AUTH, IRC, FILES, WEB = f"auth.{D}", f"irc.{D}", f"files.{D}", f"chat.{D}"

ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail else ""), flush=True)
    if not ok: failures.append(name)
    return ok

def https(host, method, path, body=None, token=None, raw=None, headers=None):
    s = ctx.wrap_socket(socket.create_connection((args.addr, 443), timeout=20), server_hostname=host)
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else b"")
    h = [f"{method} {path} HTTP/1.1", f"Host: {host}", "Connection: close", "Accept: application/json",
         "User-Agent: boson-setup-e2e", f"Content-Length: {len(data)}"]
    if body is not None: h.append("Content-Type: application/json")
    for k, v in (headers or {}).items(): h.append(f"{k}: {v}")
    if token: h.append(f"Authorization: Bearer {token}")
    s.sendall(("\r\n".join(h) + "\r\n\r\n").encode() + data)
    resp = b""
    while True:
        c = s.recv(65536)
        if not c: break
        resp += c
    s.close()
    head, _, payload = resp.partition(b"\r\n\r\n")
    status = int(head.split()[1])
    if b"transfer-encoding: chunked" in head.lower():
        out, rest = b"", payload
        while rest:
            n, _, rest = rest.partition(b"\r\n")
            n = int(n, 16)
            if n == 0: break
            out, rest = out + rest[:n], rest[n + 2:]
        payload = out
    try: return status, json.loads(payload or b"null")
    except ValueError: return status, payload.decode(errors="replace")

def issuer(host, port=443):
    s = ctx.wrap_socket(socket.create_connection((args.addr, port), timeout=10), server_hostname=host)
    der = s.getpeercert(binary_form=True); s.close()
    out = subprocess.run(["openssl", "x509", "-inform", "DER", "-noout", "-issuer"], input=der, capture_output=True).stdout.decode()
    return out.strip()

# 1. certificates
for host in (AUTH, WEB, f"admin.{D}", f"api.{D}", f"voice.{D}", FILES):
    iss = issuer(host)
    check(f"{host}: certificate from the ACME CA", args.issuer in iss, iss)
iss = issuer(IRC, 6697)
check(f"{IRC}:6697: Ergo has the ACME certificate", args.issuer in iss, iss)

# 2. the admin
st, r = https(AUTH, "POST", "/v1/login", {"login": args.admin, "password": args.password})
check("admin logs in to auth", st == 200 and isinstance(r, dict) and r.get("access_token"), st)
out = subprocess.run(["sudo", "docker", "compose", "--project-directory", args.dir, "-p", "boson", "exec", "-T", "auth",
                      "./auth", "user", "list", args.admin], capture_output=True, text=True).stdout
rows = [l.split() for l in out.splitlines()[1:] if len(l.split()) >= 5 and l.split()[1] == args.admin]
check("exactly one admin account", len(rows) == 1, out.strip().splitlines()[-1] if out.strip() else "no output")
check("the admin is the Owner", bool(rows) and rows[0][-2] == "owner", rows)

# 3. a new user (or the one from the first run)
if args.user_file and os.path.exists(args.user_file):
    user, pw = open(args.user_file).read().split()
    print(f"reusing user {user}")
else:
    user, pw = "e2e" + secrets.token_hex(3), secrets.token_urlsafe(16)
    st, r = https(AUTH, "POST", "/v1/register", {"username": user, "email": f"{user}@{D}", "password": pw})
    check("new user signs up", st == 201, (st, r))
    if args.user_file:
        with open(args.user_file, "w") as f: f.write(f"{user} {pw}\n")
st, r = https(AUTH, "POST", "/v1/login", {"login": user, "password": pw})
access = r.get("access_token") if isinstance(r, dict) else None
check("new user logs in to auth", st == 200 and access, st)

class IRCConn:
    def __init__(self, login, password, nick):
        self.s = ctx.wrap_socket(socket.create_connection((args.addr, 6697), timeout=30), server_hostname=IRC)
        self.f = self.s.makefile("rb")
        self.lines = []
        self.send("CAP LS 302"); self.send(f"NICK {nick}"); self.send(f"USER {nick} 0 * :e2e")
        # the last line of a multi-line CAP LS: ":srv CAP * LS :..." (not "LS * :")
        self.wait(lambda l: len(l.split()) > 4 and l.split()[1] == "CAP" and l.split()[3] == "LS" and l.split()[4] != "*")
        self.send("CAP REQ :sasl draft/chathistory batch server-time message-tags")
        self.wait(lambda l: " ACK " in l)
        self.send("AUTHENTICATE PLAIN")
        self.wait(lambda l: l.startswith("AUTHENTICATE +"))
        self.send("AUTHENTICATE " + base64.b64encode(f"\0{login}\0{password}".encode()).decode())
        self.ok = self.wait(lambda l: " 903 " in l or " 904 " in l) and " 903 " in self.lines[-1]
        self.send("CAP END")
        self.welcome = self.wait(lambda l: " 001 " in l)
    def send(self, l): self.s.sendall((l + "\r\n").encode())
    def wait(self, pred, timeout=25):
        end = time.time() + timeout
        while time.time() < end:
            try: line = self.f.readline().decode(errors="replace").rstrip("\r\n")
            except (socket.timeout, TimeoutError): return False
            if not line: return False
            self.lines.append(line)
            if line.startswith("PING"): self.send("PONG " + line.split(" ", 1)[1]); continue
            if pred(line): return True
        return False
    def close(self):
        try: self.send("QUIT :bye"); self.s.close()
        except OSError: pass

chan = "#e2e-" + secrets.token_hex(3)
a = IRCConn(args.admin, args.password, args.admin)
check("admin: IRC SASL login + welcome", a.ok and a.welcome, " | ".join(a.lines[-3:]))
u = IRCConn(user, pw, user)
check("new user: IRC SASL login + welcome", u.ok and u.welcome, " | ".join(u.lines[-3:]))
a.send(f"JOIN {chan}"); check("admin joins " + chan, a.wait(lambda l: " JOIN " in l and chan in l))
u.send(f"JOIN {chan}"); check("new user joins " + chan, u.wait(lambda l: " JOIN " in l and chan in l))
a.wait(lambda l: " JOIN " in l and l.startswith(f":{user}!"), 10)
text = "hello from the setup e2e " + secrets.token_hex(4)
a.send(f"PRIVMSG {chan} :{text}")
check("new user receives the admin's message", u.wait(lambda l: "PRIVMSG" in l and text in l))
u.send(f"PRIVMSG {chan} :reply {text}")
check("admin receives the reply", a.wait(lambda l: "PRIVMSG" in l and "reply " + text in l))
u.close()
time.sleep(1)
u2 = IRCConn(user, pw, user)
u2.send(f"JOIN {chan}")
u2.wait(lambda l: " JOIN " in l and chan in l)
u2.send(f"CHATHISTORY LATEST {chan} * 20")
got = u2.wait(lambda l: "PRIVMSG" in l and text in l, 15)
check("history after reconnect (CHATHISTORY)", got, " | ".join(u2.lines[-4:]))
u2.close(); a.close()

# 6. voice token
st, r = https(AUTH, "POST", "/v1/voice/token", {"rooms": []}, token=access)
check("voice token issued", st == 200 and isinstance(r, dict) and r.get("token", "").count(".") == 2, st)

# 7. file upload
png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
st, r = https(FILES, "GET", "/v1/config")
check("files service answers (enabled)", st == 200 and isinstance(r, dict) and r.get("enabled") is True, (st, r))
st, r = https(FILES, "POST", "/v1/files", raw=png, token=access,
              headers={"Content-Type": "image/png", "X-Filename": "e2e.png"})
check("file upload", st == 201 and isinstance(r, dict) and r.get("id"), (st, r))

print("RESULT", "ok" if not failures else "failed: " + ", ".join(failures))
sys.exit(1 if failures else 0)
