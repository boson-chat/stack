#!/usr/bin/env python3
"""After the wizard's add-on path (test/setup/run-addon.sh): does it work?

1. The bridge (irc.DOMAIN:6697) and the web names have the ACME CA's
   certificates; the bridge is healthy (/api/managed/health).
2. Boson users (the admin, or the NickServ account in NickServ mode, and a
   new user who signs up) get bridge accounts (auth provisions them), sign in to the
   bridge with their access token (SASL OAUTHBEARER), join a channel.
3. A message reaches the other Boson user with msgid and server-time; a
   reply (+draft/reply, text with the "nick: " fallback) and a reaction
   (+draft/react) reach them where the server relays client tags (the
   capability table says), and history (CHATHISTORY) has the message after
   reconnecting.
4. A classic client connected straight to the IRC server sees the Boson
   users' messages (with the "nick: " reply text) and their own address
   (WEBIRC), not the bridge's.
5. NickServ mode (auth with ACCOUNT_BACKEND=nickserv): the existing NickServ
   account bosonprobe signs in to Boson with its NickServ password; auth
   hands the bridge that login (method BRIDGE_NICKSERV_METHOD: nickserv on
   a server without SASL, else sasl), and the classic client's WHOIS shows
   the Boson users identified to NickServ.
6. Nothing a Boson user receives (IRC lines, boson.json, auth's config, the
   web client's config) says "evrd".
7. Guests and the web gateway (wss://chat.DOMAIN/webirc/websocket/, which
   sends WEBIRC to the bridge with BRIDGE_CLIENT_WEBIRC_PASSWORD): auth
   drives the bridge's guest setting from guest_access (evrd's
   /api/managed/settings; with an auth that doesn't yet, the test sets it
   itself and says so); a guest from the web gateway gets in when guests are
   allowed (refused otherwise), and the classic client sees the guest, and a
   new Boson user who first connects through the web gateway, with their
   own address: neither the bridge's nor the gateway container's.
"""
import argparse, base64, json, re, secrets, socket, ssl, struct, subprocess, sys, time, urllib.request

p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1")
p.add_argument("--domain", required=True)
p.add_argument("--admin", required=True)
p.add_argument("--password", required=True)
p.add_argument("--irc", required=True, help="the IRC server itself, host:port (TLS)")
p.add_argument("--pairing", required=True)
p.add_argument("--dir", default="/opt/boson-stack")
p.add_argument("--issuer", default="Pebble")
p.add_argument("--nickserv-account", default="bosonprobe")
p.add_argument("--nickserv-password", default="probe-fixture-pass")
args = p.parse_args()
D = args.domain
AUTH, BRIDGE, WEB = f"auth.{D}", f"irc.{D}", f"chat.{D}"
IRC_HOST, IRC_PORT = args.irc.rsplit(":", 1)
GOLDEN = json.load(open(f"pkg/probe/testdata/golden/{args.pairing}.webirc.json"))
STATUS = {f["feature"]: f["status"] for f in GOLDEN["features"]}
REACT = args.pairing == "unreal-anope"  # the wizard loaded boson-react there
if REACT:
    STATUS["reactions"] = "native"

ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail else ""), flush=True)
    if not ok: failures.append(name)
    return ok

def dc(*a, stdin=None):
    return subprocess.run(["sudo", "docker", "compose", "--project-directory", args.dir, "-p", "boson", *a],
                          capture_output=True, text=True, input=stdin)

def env():
    out = subprocess.run(["sudo", "cat", f"{args.dir}/.env"], capture_output=True, text=True).stdout
    return {k: v.strip("'") for k, v in re.findall(r"^([A-Z0-9_]+)=(.*)$", out, re.M)}
ENV = env()

def https(host, method, path, body=None, token=None):
    s = ctx.wrap_socket(socket.create_connection((args.addr, 443), timeout=20), server_hostname=host)
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

def issuer(host, port=443, addr=None):
    s = ctx.wrap_socket(socket.create_connection((addr or args.addr, port), timeout=10), server_hostname=host)
    der = s.getpeercert(binary_form=True); s.close()
    return subprocess.run(["openssl", "x509", "-inform", "DER", "-noout", "-issuer"], input=der, capture_output=True).stdout.decode().strip()

def bridge_api(path):
    r = dc("exec", "-T", "evrd", "wget", "-q", "-O-", "--header", f"Authorization: Bearer {ENV['BRIDGE_TOKEN']}", f"http://127.0.0.1:8098{path}")
    try: return json.loads(r.stdout)
    except ValueError: return None

def container_ip(service):
    cid = dc("ps", "-q", service).stdout.strip()
    out = subprocess.run(["sudo", "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", cid],
                         capture_output=True, text=True).stdout.split()
    return out[0] if out else None

def bridge_put(path, body):
    """PUT on the managed API (evrd-links' address on the compose network,
    reachable from this host; the evrd image's wget can't PUT)."""
    req = urllib.request.Request(f"http://{container_ip('evrd-links')}:8098{path}", data=json.dumps(body).encode(), method="PUT",
                                 headers={"Authorization": f"Bearer {ENV['BRIDGE_TOKEN']}", "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read())

class WS:
    """The web gateway like a browser (scripts/smoke-webirc.py's client):
    RFC 6455 text frames, one IRC line each."""
    def __init__(self, host):
        self.s = ctx.wrap_socket(socket.create_connection((args.addr, 443), timeout=20), server_hostname=host)
        key = base64.b64encode(secrets.token_bytes(16)).decode()
        self.s.sendall((f"GET /webirc/websocket/ HTTP/1.1\r\nHost: {host}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                        f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nOrigin: https://{host}\r\n\r\n").encode())
        head = b""
        while b"\r\n\r\n" not in head:
            c = self.s.recv(1)
            if not c: break
            head += c
        self.status = int(head.split(b" ", 2)[1]) if head.startswith(b"HTTP/") else 0
        self.buf, self.pending, self.lines = b"", [], []
    def send(self, line):
        data, mask = line.encode(), secrets.token_bytes(4)
        n = len(data)
        hdr = bytes([0x81]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack(">H", n))
        self.s.sendall(hdr + mask + bytes(c ^ mask[i % 4] for i, c in enumerate(data)))
    def _frame(self):
        """A whole frame from the buffer (op, payload), or None for more bytes."""
        if len(self.buf) < 2: return None
        n, i = self.buf[1] & 0x7F, 2
        if n == 126:
            if len(self.buf) < 4: return None
            n, i = struct.unpack(">H", self.buf[2:4])[0], 4
        elif n == 127:
            if len(self.buf) < 10: return None
            n, i = struct.unpack(">Q", self.buf[2:10])[0], 10
        if len(self.buf) < i + n: return None
        op, payload, self.buf = self.buf[0] & 0x0F, self.buf[i:i + n], self.buf[i + n:]
        return op, payload
    def _message(self):
        while True:
            f = self._frame()
            if f is None:
                c = self.s.recv(65536)  # a timeout leaves the buffer whole
                if not c: raise EOFError
                self.buf += c
                continue
            if f[0] == 0x8: raise EOFError
            if f[0] in (0x1, 0x2): return f[1].decode(errors="replace")
    def wait(self, pred, timeout=25):
        end = time.time() + timeout
        while time.time() < end:
            if not self.pending:
                self.s.settimeout(max(0.1, end - time.time()))
                try: self.pending = [l.strip("\r") for l in self._message().split("\n") if l.strip("\r")]
                except (socket.timeout, TimeoutError, ssl.SSLWantReadError): continue
                except (EOFError, OSError, ssl.SSLError): return None
                continue
            line = self.pending.pop(0)
            self.lines.append(line); boson_lines.append(line)
            bare = line.split(" ", 1)[1] if line.startswith("@") else line
            if bare.startswith("PING"): self.send("PONG " + bare.split(" ", 1)[1]); continue
            if pred(line): return line
        return None
    def close(self):
        try: self.send("QUIT :bye"); self.s.close()
        except OSError: pass

def web_irc(nick, token=None):
    """A web client through the gateway to the bridge (HOST irc.DOMAIN:+6697,
    the alias to evrd-links:6667): a guest without a token, else SASL
    OAUTHBEARER. Returns the socket and its 001 (or None) and last lines."""
    w = WS(WEB)
    w.send(f"HOST {BRIDGE}:+6697")
    w.send("CAP LS 302")
    if token:
        w.send("CAP REQ :sasl")
        w.wait(lambda l: " ACK " in l or " NAK " in l)
        w.send("AUTHENTICATE OAUTHBEARER")
        w.wait(lambda l: l.startswith("AUTHENTICATE +") or " 90" in l)
        w.send("AUTHENTICATE " + base64.b64encode(f"n,,\x01auth=Bearer {token}\x01\x01".encode()).decode())
        w.wait(lambda l: " 903 " in l or " 904 " in l)
    w.send(f"NICK {nick}"); w.send(f"USER {nick[:9]} 0 * :e2e web {nick}")
    w.send("CAP END")
    welcome = w.wait(lambda l: " 001 " in l or " 464 " in l or l.startswith("ERROR"), 40)
    if welcome and " 001 " not in welcome: welcome = None
    return w, welcome

# Every line Boson users receive, for the "evrd" check.
boson_lines = []

class IRC:
    def __init__(self, host, port, sni, nick, token=None, caps="", boson=True):
        self.boson = boson
        self.s = ctx.wrap_socket(socket.create_connection((host, int(port)), timeout=30), server_hostname=sni)
        self.s.settimeout(1)
        self.buf, self.lines, self.nick = b"", [], nick
        self.send("CAP LS 302"); self.send(f"NICK {nick}"); self.send(f"USER {nick[:9]} 0 * :e2e {nick}")
        self.ok = True
        if token:
            self.wait(lambda l: " CAP " in l and " LS " in l and " LS * " not in l)
            self.send("CAP REQ :sasl " + caps)
            self.wait(lambda l: " ACK " in l or " NAK " in l)
            self.send("AUTHENTICATE OAUTHBEARER")
            self.wait(lambda l: l.startswith("AUTHENTICATE +"))
            self.send("AUTHENTICATE " + base64.b64encode(f"n,,\x01auth=Bearer {token}\x01\x01".encode()).decode())
            self.ok = self.wait(lambda l: " 903 " in l or " 904 " in l) and " 903 " in self.lines[-1]
        self.send("CAP END")
        self.welcome = self.wait(lambda l: " 001 " in l, 40)
        self.wait(lambda l: " 376 " in l or " 422 " in l, 15)
    def send(self, l): self.s.sendall((l + "\r\n").encode())
    def readline(self, end):
        while b"\n" not in self.buf:
            if time.time() > end: return None
            try: chunk = self.s.recv(65536)
            except (socket.timeout, TimeoutError, ssl.SSLWantReadError): continue
            if not chunk: return ""
            self.buf += chunk
        line, _, self.buf = self.buf.partition(b"\n")
        return line.decode(errors="replace").rstrip("\r")
    def wait(self, pred, timeout=25):
        end = time.time() + timeout
        while True:
            line = self.readline(end)
            if not line: return None
            self.lines.append(line)
            if self.boson: boson_lines.append(line)
            bare = line.split(" ", 1)[1] if line.startswith("@") else line
            if bare.startswith("PING"): self.send("PONG " + bare.split(" ", 1)[1]); continue
            if pred(line): return line
    def close(self):
        try: self.send("QUIT :bye"); self.s.close()
        except OSError: pass

def tags(line):
    if not line or not line.startswith("@"): return {}
    out = {}
    for kv in line[1:].split(" ", 1)[0].split(";"):
        k, _, v = kv.partition("=")
        out[k] = v
    return out

# 1. certificates and the bridge's health
for host in (AUTH, WEB, f"admin.{D}"):
    iss = issuer(host)
    check(f"{host}: certificate from the ACME CA", args.issuer in iss, iss)
iss = issuer(BRIDGE, 6697)
check(f"{BRIDGE}:6697 (the bridge) has the ACME certificate", args.issuer in iss, iss)
h = bridge_api("/api/managed/health")
check("the bridge is healthy (/api/managed/health)", isinstance(h, dict) and h.get("ok") and h.get("webirc") and h.get("tokens"), h)

# 2. accounts
st, cfg = https(AUTH, "GET", "/v1/config")
backend = ((cfg or {}).get("accounts") or {}).get("backend", "local") if isinstance(cfg, dict) else "local"
nickserv_mode = ENV.get("ACCOUNT_BACKEND") == "nickserv"
print(f"accounts: ACCOUNT_BACKEND={ENV.get('ACCOUNT_BACKEND')} auth says backend={backend}")
if nickserv_mode:
    check("auth runs the NickServ backend", backend == "nickserv", cfg)
    st, r = https(AUTH, "POST", "/v1/login", {"login": args.nickserv_account, "password": args.nickserv_password})
    check("an existing NickServ account signs in to Boson with its NickServ password", st == 200 and isinstance(r, dict) and r.get("access_token"), (st, r))
    first, first_pw = args.nickserv_account, args.nickserv_password
    st, r2 = https(AUTH, "POST", "/v1/login", {"login": args.nickserv_account, "password": "wrong-" + secrets.token_hex(3)})
    check("a wrong NickServ password is refused", st == 401, st)
else:
    first, first_pw = args.admin, args.password
    st, r = https(AUTH, "POST", "/v1/login", {"login": first, "password": first_pw})
    check("the admin signs in to Boson", st == 200 and isinstance(r, dict) and r.get("access_token"), st)
tok_a = r.get("access_token") if isinstance(r, dict) else None

def new_user(what):
    """A new Boson user (NickServ mode: a NickServ account registered on IRC,
    then Boson): its name and access token."""
    if not nickserv_mode:
        name, pw = "e2e" + secrets.token_hex(3), secrets.token_urlsafe(16)
        st, rr = https(AUTH, "POST", "/v1/register", {"username": name, "email": f"{name}@{D}", "password": pw})
        check(f"{what} signs up", st == 201, (st, rr))
    else:
        name, pw = "e2ens" + secrets.token_hex(2), "pw-" + secrets.token_hex(6)
        reg = IRC(IRC_HOST, IRC_PORT, IRC_HOST, name, boson=False)
        reg.send(f"PRIVMSG NickServ :REGISTER {pw} {name}@{D}")
        reg.wait(lambda l: "NOTICE" in l and "NickServ" in l and ("registered" in l.lower()), 20)
        reg.close()
        time.sleep(2)
    st, rr = https(AUTH, "POST", "/v1/login", {"login": name, "password": pw})
    check(f"{what} signs in to Boson ({name})", st == 200, (st, rr))
    return name, (rr.get("access_token") if isinstance(rr, dict) else None)

second, tok_b = new_user("a new Boson user" if not nickserv_mode else "a second NickServ account")

ns_method = ENV.get("BRIDGE_NICKSERV_METHOD") or "sasl"
if nickserv_mode:
    print(f"bridge NickServ login method: {ns_method} (BRIDGE_NICKSERV_METHOD={ENV.get('BRIDGE_NICKSERV_METHOD', '')!r})")
for name in (first, second):
    acct = None
    for _ in range(30):
        acct = bridge_api(f"/api/managed/users/{name.lower()}")
        login = (acct or {}).get("auth") or {} if isinstance(acct, dict) else {}
        if isinstance(acct, dict) and acct.get("subject") and (not nickserv_mode or login.get("hasPassword")): break
        time.sleep(1)
    check(f"{name} has a bridge account (auth provisions it)", isinstance(acct, dict) and acct.get("subject"), acct)
    if nickserv_mode and isinstance(acct, dict):
        login = acct.get("auth") or {}
        check(f"{name}: auth handed the bridge the NickServ login ({ns_method})",
              login.get("method") == ns_method and login.get("hasPassword") is True and (login.get("account") or "").lower() == name.lower(), login)

CAPS = "message-tags server-time draft/chathistory batch echo-message"
chan = "#e2e-" + secrets.token_hex(3)
a = IRC(args.addr, 6697, BRIDGE, first, tok_a, CAPS)
check(f"{first}: signs in to the bridge with the access token (OAUTHBEARER)", a.ok and a.welcome, " | ".join(a.lines[-3:]))
b = IRC(args.addr, 6697, BRIDGE, second, tok_b, CAPS)
check(f"{second}: signs in to the bridge", b.ok and b.welcome, " | ".join(b.lines[-3:]))
bad = IRC(args.addr, 6697, BRIDGE, "e2ebad", "not.a.token", CAPS)
check("a bad token is refused", not bad.ok, " | ".join(bad.lines[-2:]))
bad.close()

# The classic client, straight on the IRC server.
c = IRC(IRC_HOST, IRC_PORT, IRC_HOST, "classic" + secrets.token_hex(2), boson=False)
check("a classic client connects to the IRC server itself", c.welcome, " | ".join(c.lines[-2:]))
c.send(f"JOIN {chan}"); c.wait(lambda l: " JOIN " in l and chan in l)
a.send(f"JOIN {chan}"); check(f"{first} joins {chan} through the bridge", a.wait(lambda l: " JOIN " in l and chan in l))
b.send(f"JOIN {chan}"); check(f"{second} joins {chan}", b.wait(lambda l: " JOIN " in l and chan in l))
c.wait(lambda l: " JOIN " in l and l.startswith(f":{second}!"), 15)
a.wait(lambda l: " JOIN " in l and f":{second}!" in l, 10)

# 3. a message, a reply, a reaction
text = "hello from the add-on e2e " + secrets.token_hex(4)
a.send(f"PRIVMSG {chan} :{text}")
got = b.wait(lambda l: "PRIVMSG" in l and text in l)
check(f"{second} receives {first}'s message (with msgid and server-time)", got and "msgid" in tags(got) and "time" in tags(got), got)
cm = c.wait(lambda l: "PRIVMSG" in l and text in l)
check("the classic client receives it", cm, cm)
msgid = tags(got).get("msgid", "")
reply = f"{first}: a reply " + secrets.token_hex(3)
b.send(f"@+draft/reply={msgid} PRIVMSG {chan} :{reply}")
ra = a.wait(lambda l: "PRIVMSG" in l and reply in l)
if STATUS["replies"] == "native":
    check(f"{first} receives the reply as a reply (+draft/reply)", ra and tags(ra).get("+draft/reply"), ra)
else:
    check(f"{first} receives the reply's text (the server drops +draft/reply: shown as a plain message)", ra, ra)
rc = c.wait(lambda l: "PRIVMSG" in l and reply in l)
check("the classic client sees the reply with the 'nick: ' text", rc and f":{reply}" in rc and reply.startswith(f"{first}: "), rc)
b.send(f"@+draft/react=👍;+draft/reply={msgid} TAGMSG {chan}")
if STATUS["reactions"] == "native":
    rx = a.wait(lambda l: "TAGMSG" in l and "+draft/react" in l, 15)
    check(f"{first} receives the reaction (+draft/react)", rx, rx)
else:
    print(f"SKIP reactions: {args.pairing} drops +draft/react (capability table: {STATUS['reactions']})")

# History, from the bridge (or the server) after reconnecting.
b.close()
time.sleep(1)
text2 = "while you were away " + secrets.token_hex(4)
a.send(f"PRIVMSG {chan} :{text2}")
c.wait(lambda l: text2 in l, 10)
b2 = IRC(args.addr, 6697, BRIDGE, second, tok_b, CAPS)
b2.send(f"CHATHISTORY LATEST {chan} * 50")
hist = b2.wait(lambda l: "PRIVMSG" in l and text2 in l, 20)
check("history after reconnecting (CHATHISTORY) has the message sent meanwhile", hist, " | ".join(b2.lines[-4:]))
b2.send(f"CHATHISTORY LATEST {chan} * 50")
check("history has the first message too", b2.wait(lambda l: "PRIVMSG" in l and text in l, 20))

# 4. WEBIRC: the classic client sees the Boson user's own address, not the bridge's.
src = None
st_out = subprocess.run(["sudo", "cat", f"{args.dir}/.setup/state.json"], capture_output=True, text=True).stdout
try: src = json.loads(st_out)["answers"]["addon"]["source_ip"]
except (ValueError, KeyError): pass
# As an operator (the fixtures' admin / fixture-oper) WHOIS shows the real
# address even where hosts are cloaked.
c.send("OPER admin fixture-oper")
c.wait(lambda l: " 381 " in l or " 491 " in l or " 464 " in l, 10)
c.send(f"WHOIS {second}")
whois = []
c.wait(lambda l: (whois.append(l) or False) or " 318 " in l, 15)
ips = set(re.findall(r"\b\d{1,3}(?:\.\d{1,3}){3}\b", " ".join(x for x in whois if " 311 " in x or " 378 " in x or " 338 " in x)))
check(f"the classic client sees {second}'s own address via WEBIRC, not the bridge's ({src})", ips and src not in ips, whois)
if nickserv_mode:
    # The bridge identified their connections to NickServ: 330 (logged in
    # as) or 307 (identified/registered nick), whichever the server sends.
    for name in (first, second):
        found = None
        for _ in range(10):
            c.send(f"WHOIS {name}")
            w = []
            c.wait(lambda l: (w.append(l) or False) or " 318 " in l, 15)
            found = [x for x in w if re.search(r" (307|330) ", x)]
            if found: break
            time.sleep(2)
        check(f"{name} is identified to NickServ on the IRC server (WHOIS 307/330)", bool(found), found or w)

# 7. guests and the web gateway's WEBIRC to the bridge
def whois_ips(nick):
    c.send(f"WHOIS {nick}")
    w = []
    c.wait(lambda l: (w.append(l) or False) or " 318 " in l, 15)
    return set(re.findall(r"\b\d{1,3}(?:\.\d{1,3}){3}\b", " ".join(x for x in w if " 311 " in x or " 378 " in x or " 338 " in x))), w
gw_ip = container_ip("webircgateway")
not_ours = {x for x in (src, gw_ip) if x}
auth_guests = bool(cfg.get("guest_access")) if isinstance(cfg, dict) else False
settings = None
for _ in range(30):
    settings = bridge_api("/api/managed/settings")
    if isinstance(settings, dict) and "guests" in (settings.get("set") or []): break
    time.sleep(1)
check("the bridge has the settings API (evrd 0.61+)", isinstance(settings, dict) and "guests" in settings, settings)
if isinstance(settings, dict) and "guests" in (settings.get("set") or []):
    check(f"auth drives the bridge's guests: {settings.get('guests')} = guest_access {auth_guests}", settings.get("guests") == auth_guests, settings)
else:
    print(f"SKIP auth driving guests: this auth doesn't set the bridge's guests yet; setting guests={auth_guests} on the bridge here", flush=True)
    try: settings = bridge_put("/api/managed/settings", {"guests": auth_guests})
    except OSError as e: settings = str(e)
    check("guests set on the bridge through the managed API", isinstance(settings, dict) and settings.get("guests") == auth_guests, settings)
check("evrd.conf leaves managed_guests to auth", "managed_guests " not in subprocess.run(["sudo", "cat", f"{args.dir}/bridge/evrd.conf"], capture_output=True, text=True).stdout)
gnick = "e2eguest" + secrets.token_hex(2)
g, gwel = web_irc(gnick)
check("the web gateway's socket opens (wss://chat.DOMAIN/webirc/websocket/)", g.status == 101, g.status)
if auth_guests:
    check(f"a guest from the web gateway gets through the bridge ({gnick})", gwel, " | ".join(g.lines[-3:]))
    g.send(f"JOIN {chan}")
    check("the guest joins the channel", g.wait(lambda l: " JOIN " in l and chan in l))
    c.wait(lambda l: " JOIN " in l and l.startswith(f":{gnick}"), 15)
    h = bridge_api("/api/managed/health") or {}
    check("the bridge counts the guest (health guests.active)", ((h.get("guests") or {}).get("active") or 0) >= 1, h.get("guests"))
    ips, w = whois_ips(gnick)
    check(f"the classic client sees the guest with their own address via the gateway's WEBIRC, not the bridge's or the gateway's ({sorted(not_ours)})",
          ips and not (ips & not_ours), w)
else:
    check("guests not allowed: a guest from the web gateway is refused", not gwel and any(" 464 " in l for l in g.lines), " | ".join(g.lines[-3:]))
g.close()
third, tok_c = new_user("a Boson user who connects through the web gateway")
wu, wwel = web_irc(third, tok_c)
check(f"{third} signs in to the bridge through the web gateway (OAUTHBEARER)", wwel, " | ".join(wu.lines[-3:]))
wu.send(f"JOIN {chan}")
wu.wait(lambda l: " JOIN " in l and chan in l)
ips = set()
for _ in range(10):
    ips, w = whois_ips(third)
    if ips: break
    time.sleep(1)
check(f"the classic client sees {third} with their own address via the web gateway, not the bridge's or the gateway's ({sorted(not_ours)})",
      ips and not (ips & not_ours), w)
wu.close()
for x in (a, b2, c): x.close()

# 5/6. nothing Boson users see says evrd
st, bj = https(D, "GET", "/.well-known/boson.json")
lines = list(boson_lines)
for name, obj in (("auth /v1/config", cfg), ("boson.json", bj)):
    if obj is not None: lines.append(name + ": " + json.dumps(obj))
st, wc = https(WEB, "GET", "/boson-config.js")
lines.append("web /boson-config.js: " + str(wc))
leaks = [l for l in lines if "evrd" in l.lower()]
check(f"nothing Boson users receive says 'evrd' ({len(lines)} lines checked)", not leaks, leaks[:5])
if isinstance(bj, dict):
    check("boson.json advertises the bridge as the IRC server", (bj.get("irc") or {}).get("host") == BRIDGE, bj.get("irc"))

print("RESULT", "ok" if not failures else "failed: " + ", ".join(failures))
sys.exit(1 if failures else 0)
