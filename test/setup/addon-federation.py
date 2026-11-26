#!/usr/bin/env python3
"""Federated login on an add-on install (test/setup/run-addon.sh, after
addon-result.py): alice@home.test, from a second Boson install
(addon-home.sh), uses the add-on install (boson.test, the bridge in front of
an existing IRC server) without signing up there.

0. Someone else registered the nicks alice (her home username) and
   alice-hometest (her handle here) with NickServ on the IRC server, with
   the services' enforcement on (Anope SET KILL 20, Atheme SET ENFORCE
   ON). An impostor using alice is renamed or killed: enforcement works.
1. The add-on's boson.json has the federation section; alice signs up at
   home, gets an assertion for the add-on's auth and signs in there:
   federated, alice@home.test, handle alice-hometest (the home domain is
   always in the handle next to an existing IRC network).
2. auth gives her a bridge account with no services login (method none).
3. She signs in to the bridge with her token; the bridge moves her off the
   registered alice-hometest to alice-hometest_ (her own client follows the
   NICK) before joining; a classic client sees her join and talk as
   alice-hometest_, never as alice or alice-hometest, not identified to
   NickServ (no 307/330).
4. Past the enforcement delay she is neither renamed nor killed and still
   connected; the bridge says currentNick alice-hometest_.
5. The add-on's admin bans alice@home.test: the bridge drops her, the
   classic client sees her quit, a new sign-in is refused (identity_banned).
"""
import argparse, base64, json, re, secrets, socket, ssl, subprocess, sys, time

p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1")
p.add_argument("--domain", required=True)
p.add_argument("--home", default="home.test")
p.add_argument("--home-port", type=int, default=18443)
p.add_argument("--irc", required=True, help="the IRC server itself, host:port (TLS)")
p.add_argument("--pairing", required=True)
p.add_argument("--dir", default="/opt/boson-stack")
p.add_argument("--admin", default="bosonprobe", help="the add-on's admin (NickServ mode: the NickServ account)")
p.add_argument("--admin-password", default="probe-fixture-pass")
args = p.parse_args()
D, HOME = args.domain, args.home
AUTH, BRIDGE = f"auth.{D}", f"irc.{D}"
HOME_AUTH = f"auth.{HOME}"
IRC_HOST, IRC_PORT = args.irc.rsplit(":", 1)
ATHEME = "atheme" in args.pairing
HANDLE = "alice-" + re.sub(r"[^a-z0-9]", "", HOME)  # alice-hometest
SAFE = HANDLE + "_"
# How long the services let an unidentified user keep a registered nick
# (Anope SET KILL 20; the Atheme fixture's enforce_delay: 10s).
ENFORCE = 10 if ATHEME else 20

ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail else ""), flush=True)
    if not ok: failures.append(name)
    return ok
def must(name, ok, detail=""):
    if not check(name, ok, detail):
        finish()

def finish():
    print("RESULT", "ok" if not failures else "failed: " + ", ".join(failures))
    sys.exit(1 if failures else 0)

def dc(*a):
    return subprocess.run(["sudo", "docker", "compose", "--project-directory", args.dir, "-p", "boson", *a],
                          capture_output=True, text=True)

def env():
    out = subprocess.run(["sudo", "cat", f"{args.dir}/.env"], capture_output=True, text=True).stdout
    return {k: v.strip("'") for k, v in re.findall(r"^([A-Z0-9_]+)=(.*)$", out, re.M)}
ENV = env()

def https(host, method, path, body=None, token=None, port=443):
    s = ctx.wrap_socket(socket.create_connection((args.addr, port), timeout=30), server_hostname=host)
    data = json.dumps(body).encode() if body is not None else b""
    h = [f"{method} {path} HTTP/1.1", f"Host: {host}", "Connection: close", "Accept: application/json",
         "User-Agent: boson-setup-e2e", f"Content-Length: {len(data)}"]
    if body is not None: h.append("Content-Type: application/json")
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

def home(method, path, body=None, token=None):
    return https(HOME_AUTH, method, path, body, token, port=args.home_port)

def code(r):
    if isinstance(r, dict):
        e = r.get("error")
        return e.get("code") if isinstance(e, dict) else r.get("code") or e
    return None

def bridge_api(path):
    r = dc("exec", "-T", "evrd", "wget", "-q", "-O-", "--header", f"Authorization: Bearer {ENV['BRIDGE_TOKEN']}", f"http://127.0.0.1:8098{path}")
    try: return json.loads(r.stdout)
    except ValueError: return None

class IRC:
    def __init__(self, host, port, sni, nick, token=None, caps=""):
        self.s = ctx.wrap_socket(socket.create_connection((host, int(port)), timeout=30), server_hostname=sni)
        self.s.settimeout(1)
        self.buf, self.lines, self.nick, self.closed = b"", [], nick, False
        self.send("CAP LS 302"); self.send(f"NICK {nick}"); self.send(f"USER {nick[:9]} 0 * :e2e {nick}")
        self.ok = True
        if token:
            self.wait(lambda l: " CAP " in l and " LS " in l and " LS * " not in l)
            self.send("CAP REQ :sasl " + caps)
            self.wait(lambda l: " ACK " in l or " NAK " in l)
            self.send("AUTHENTICATE OAUTHBEARER")
            self.wait(lambda l: l.startswith("AUTHENTICATE +"))
            self.send("AUTHENTICATE " + base64.b64encode(f"n,,\x01auth=Bearer {token}\x01\x01".encode()).decode())
            self.ok = bool(self.wait(lambda l: " 903 " in l or " 904 " in l)) and " 903 " in self.lines[-1]
        self.send("CAP END")
        self.welcome = self.wait(lambda l: " 001 " in l, 40)
        self.wait(lambda l: " 376 " in l or " 422 " in l, 15)
    def send(self, l):
        try: self.s.sendall((l + "\r\n").encode())
        except OSError: self.closed = True
    def readline(self, end):
        while b"\n" not in self.buf:
            if time.time() > end: return None
            try: chunk = self.s.recv(65536)
            except (socket.timeout, TimeoutError, ssl.SSLWantReadError): continue
            except OSError: chunk = b""
            if not chunk:
                self.closed = True
                return ""
            self.buf += chunk
        line, _, self.buf = self.buf.partition(b"\n")
        return line.decode(errors="replace").rstrip("\r")
    def wait(self, pred, timeout=25):
        end = time.time() + timeout
        while True:
            line = self.readline(end)
            if not line: return None
            self.lines.append(line)
            bare = line.split(" ", 1)[1] if line.startswith("@") else line
            if bare.startswith("PING"): self.send("PONG " + bare.split(" ", 1)[1]); continue
            if pred(line): return line
    def drain(self, secs):
        self.wait(lambda l: False, secs)
    def close(self):
        try: self.send("QUIT :bye"); self.s.close()
        except OSError: pass

def whois(c, nick):
    c.send(f"WHOIS {nick}")
    out = []
    c.wait(lambda l: (out.append(l) or False) or " 318 " in l, 15)
    return out

def own(nick, pw):
    """Registers nick with NickServ from a classic client and turns the
    services' enforcement on for it; returns once it has quit."""
    c = IRC(IRC_HOST, IRC_PORT, IRC_HOST, nick)
    must(f"someone else connects to the IRC server as {nick}", c.welcome, c.lines[-3:])
    c.send(f"PRIVMSG NickServ :REGISTER {pw} {nick}-owner-{secrets.token_hex(2)}@example.org")
    r = c.wait(lambda l: "NickServ" in l and "NOTICE" in l and "registered" in l.lower(), 20)
    must(f"{nick} is registered with NickServ (by someone else)", r, c.lines[-4:])
    # Anope 2.1: SET KILL <seconds>; Atheme: SET ENFORCE ON
    c.send("PRIVMSG NickServ :SET ENFORCE ON" if ATHEME else f"PRIVMSG NickServ :SET KILL {ENFORCE}")
    r = c.wait(lambda l: "NickServ" in l and "NOTICE" in l and re.search(r"enforce|protection|kill", l, re.I), 15)
    check(f"{nick}: the services' enforcement is on ({'ENFORCE' if ATHEME else f'KILL {ENFORCE}s'})",
          r and not re.search(r"syntax|unknown|denied|not available|invalid", r, re.I), c.lines[-3:])
    c.close()

# 0. the nicks someone else owns
own("alice", "owner-" + secrets.token_hex(6))
own(HANDLE, "owner-" + secrets.token_hex(6))
time.sleep(2)
impostor = IRC(IRC_HOST, IRC_PORT, IRC_HOST, "alice")
check("an impostor connects to the IRC server as alice (unidentified)", impostor.welcome, impostor.lines[-2:])
impostor_since = time.time()

# 1. discovery, home sign-up, assertion, sign-in on the add-on
st, bj = https(D, "GET", "/.well-known/boson.json")
fed = (bj or {}).get("federation") if isinstance(bj, dict) else None
check("the add-on's boson.json has the federation section (accepts: allowlist)",
      isinstance(fed, dict) and fed.get("issuer") == f"https://{AUTH}" and fed.get("accepts") == "allowlist", fed)
pw = "home-" + secrets.token_hex(8)
st, r = home("POST", "/v1/register", {"username": "alice", "email": "alice@example.com", "password": pw})
must("alice signs up at home (home.test)", st == 201, (st, r))
st, r = home("POST", "/v1/login", {"login": "alice", "password": pw})
must("alice signs in at home", st == 200 and isinstance(r, dict) and r.get("access_token"), (st, r))
home_tok = r["access_token"]

def assertion():
    st, r = home("POST", "/v1/federation/assertions", {"audience": f"https://{AUTH}"}, token=home_tok)
    must("home issues her an assertion for the add-on's auth", st == 200 and isinstance(r, dict) and r.get("assertion"), (st, r))
    return r["assertion"]

st, r = https(AUTH, "POST", "/v1/federation/login", {"assertion": assertion()})
must("alice@home.test signs in on the add-on install", st == 200 and isinstance(r, dict) and r.get("access_token"), (st, r))
check("federated, alice@home.test, handle alice-hometest (the home domain in the handle)",
      r.get("federated") is True and r.get("identity") == f"alice@{HOME}" and r.get("handle") == HANDLE,
      {k: r.get(k) for k in ("federated", "identity", "handle")})
tok = r["access_token"]
handle = r.get("handle") or HANDLE

# 2. her bridge account
acct = None
for _ in range(30):
    acct = bridge_api(f"/api/managed/users/{handle.lower()}")
    if isinstance(acct, dict) and acct.get("subject"): break
    time.sleep(1)
login = (acct or {}).get("auth") or {} if isinstance(acct, dict) else {}
check("auth gives her a bridge account", isinstance(acct, dict) and acct.get("subject"), acct)
check("with no services login (method none, no password)", login.get("method") == "none" and not login.get("hasPassword"), login)
check(f"its nick is her handle ({handle}), alternate {handle}_", (acct or {}).get("nick") == handle and (acct or {}).get("altNick") == handle + "_", acct)

# 3. on the IRC server through the bridge
chan = "#fed-" + secrets.token_hex(3)
watch = IRC(IRC_HOST, IRC_PORT, IRC_HOST, "watch" + secrets.token_hex(2))
check("a classic client connects to the IRC server", watch.welcome, watch.lines[-2:])
watch.send("OPER admin fixture-oper")
watch.wait(lambda l: " 381 " in l or " 491 " in l or " 464 " in l, 10)
watch.send(f"JOIN {chan}"); watch.wait(lambda l: " JOIN " in l and chan in l)
CAPS = "message-tags server-time batch echo-message"
a = IRC(args.addr, 6697, BRIDGE, handle, tok, CAPS)
connected_at = time.time()
must(f"alice signs in to the bridge with her token (OAUTHBEARER)", a.ok and a.welcome, a.lines[-3:])
moved = a.wait(lambda l: re.search(rf" NICK :?{re.escape(SAFE)}\b", l) is not None, 30)
check(f"the bridge moves her off the registered {handle} to {SAFE}; her client gets the NICK", moved, a.lines[-6:])
a.send(f"JOIN {chan}")
joined = a.wait(lambda l: " JOIN " in l and chan in l, 20)
check(f"she joins {chan} through the bridge", joined, a.lines[-3:])
seen = watch.wait(lambda l: " JOIN " in l and chan in l and not l.startswith(f":{watch.nick}!"), 20)
check(f"the classic client sees her join as {SAFE}", seen and seen.startswith(f":{SAFE}!"), seen)
text = "hello from another Boson server " + secrets.token_hex(3)
a.send(f"PRIVMSG {chan} :{text}")
got = watch.wait(lambda l: text in l, 15)
check(f"the classic client receives her message from {SAFE}", got and got.startswith(f":{SAFE}!"), got)
w = whois(watch, SAFE)
ident = [x for x in w if re.search(r" (307|330) ", x)]
check(f"WHOIS {SAFE}: there, not identified to NickServ (no 307/330)", any(" 311 " in x for x in w) and not ident, w)

# 4. past the enforcement delay
time.sleep(max(0, connected_at + ENFORCE + 12 - time.time()))
a.send("PING :still-here")
pong = a.wait(lambda l: "still-here" in l, 20)
check(f"{ENFORCE + 12}s on she is still connected (no kill)", pong and not a.closed, a.lines[-4:])
bad = [l for l in a.lines if re.search(r"NICK :?Guest|\bKILL\b|^ERROR", l)]
check("she was never renamed to a guest nick or killed", not bad, bad)
on_registered = [l for l in watch.lines if re.match(rf":({re.escape(handle)}|alice)!\S+ (JOIN|PRIVMSG|NICK)", l)]
check(f"the classic client never saw her as alice or {handle}", not on_registered, on_registered)
w = whois(watch, SAFE)
check(f"WHOIS {SAFE}: still there, still not identified", any(" 311 " in x for x in w) and not any(re.search(r" (307|330) ", x) for x in w), w)
acct = bridge_api(f"/api/managed/users/{handle.lower()}")
check(f"the bridge: connected, currentNick {SAFE}, still no services login",
      isinstance(acct, dict) and acct.get("status") == "connected" and acct.get("currentNick") == SAFE and (acct.get("auth") or {}).get("method") == "none", acct)
impostor.drain(max(1, impostor_since + ENFORCE + 15 - time.time()))
enforced = [l for l in impostor.lines if re.search(r"NICK :?Guest|\bKILL\b|^ERROR", l)] or ([] if not impostor.closed else ["closed"])
check("the impostor on alice was renamed or killed meanwhile (the services enforce)", enforced, impostor.lines[-4:])
impostor.close()

# 5. banned on the add-on
st, r = https(AUTH, "POST", "/v1/login", {"login": args.admin, "password": args.admin_password})
must(f"the add-on's admin ({args.admin}) signs in", st == 200 and isinstance(r, dict) and r.get("access_token"), (st, r))
admin = r["access_token"]
st, r = https(AUTH, "GET", "/v1/admin/federation/users?q=alice", token=admin)
users = r.get("users", []) if isinstance(r, dict) else []
ru = next((x for x in users if x.get("identity") == f"alice@{HOME}"), None)
must("the admin lists alice@home.test as a remote user", st == 200 and ru, (st, r))
check("remote user: handle, home domain", ru.get("handle") == handle and ru.get("home_domain") == HOME, ru)
st, r = https(AUTH, "POST", f"/v1/admin/federation/users/{ru['id']}/ban", {"reason": "e2e"}, token=admin)
check("ban alice@home.test", st == 200 and isinstance(r, dict) and r.get("banned") is True, (st, r))
a.wait(lambda l: l.startswith("ERROR") or " KILL " in l, 45)
check("the bridge drops her", a.closed or any(l.startswith("ERROR") for l in a.lines[-3:]), a.lines[-4:])
q = next((l for l in watch.lines if " QUIT " in l and l.startswith(f":{SAFE}!")), None) or \
    watch.wait(lambda l: l.startswith(f":{SAFE}!") and " QUIT " in l, 30)
check("the classic client sees her quit", q, watch.lines[-4:])
acct = bridge_api(f"/api/managed/users/{handle.lower()}")
check("her bridge account is disabled", isinstance(acct, dict) and acct.get("disabled") is True, acct)
st, r = https(AUTH, "POST", "/v1/federation/login", {"assertion": assertion()})
check("a new sign-in is refused: 403 identity_banned", st == 403 and code(r) == "identity_banned", (st, r))
watch.close()
finish()
