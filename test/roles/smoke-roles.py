#!/usr/bin/env python3
"""Network roles on IRC, end to end against a real (patched) Ergo, the way a
plain client like mIRC sees them. Run by test/roles/run.sh.

  smoke-roles.py fake   # fake auth: the full contract incl. RoleServ writes and the grants feed
  smoke-roles.py real   # a real auth (>= 1.5) checkout: roles set up through its admin API + CLI

Checks: SASL PLAIN (and OAUTHBEARER on real auth) logs a role member in as
an IRC operator (381, MODE +o); WHOIS shows the role's line (313) and the
vhost in the hostmask (311) to everyone; a non-oper role gets only its vhost;
RoleServ answers LIST/INFO/ROLES/WHO; role edits (WHOIS line, oper powers)
reach connected users live; membership changes apply live (fake: grants
feed and RoleServ) or at the next login (real auth 1.5).
"""
import base64, json, os, socket, subprocess, sys, time, urllib.request

MODE = sys.argv[1] if len(sys.argv) > 1 else "fake"
IRC = ("127.0.0.1", int(os.environ.get("TEST_IRC_PORT", "26667")))
COMPOSE = os.environ.get("COMPOSE", "docker compose").split()
failures = []

def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail and not ok else ""), flush=True)
    if not ok: failures.append(name)

def http(method, url, body=None, token=None, headers=None):
    h = {"Content-Type": "application/json", "Accept": "application/json"}
    if token: h["Authorization"] = "Bearer " + token
    h.update(headers or {})
    req = urllib.request.Request(url, json.dumps(body).encode() if body is not None else None, h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            data = r.read(); return r.status, json.loads(data) if data else None
    except urllib.error.HTTPError as e:
        data = e.read(); return e.code, json.loads(data) if data else None

class Client:
    def __init__(self, user, password=None, bearer=None, nick=None, register=True):
        self.s = socket.create_connection(IRC, timeout=10); self.buf = b""; self.nick = nick or user
        self.send("CAP REQ :sasl"); self.wait(" ACK ")
        if bearer:
            self.send("AUTHENTICATE OAUTHBEARER"); self.wait("AUTHENTICATE +")
            blob = f"n,a={user},\x01auth=Bearer {bearer}\x01\x01".encode()
        else:
            self.send("AUTHENTICATE PLAIN"); self.wait("AUTHENTICATE +")
            blob = f"{user}\0{user}\0{password}".encode()
        b64 = base64.b64encode(blob).decode()
        for i in range(0, len(b64), 400): self.send("AUTHENTICATE " + b64[i:i + 400])
        if len(b64) % 400 == 0: self.send("AUTHENTICATE +")
        self.sasl = self.wait((" 903 ", " 904 ", " 905 "))
        if not register:
            self.s.close(); return
        self.send("CAP END"); self.send(f"NICK {self.nick}"); self.send(f"USER {user} 0 * :{user}")
        self.burst = self.collect(lambda l: " 376 " in l or " 422 " in l, 15)
        self.burst += self.drain(1.0)
    def ok(self): return " 903 " in self.sasl
    def send(self, l): self.s.sendall((l + "\r\n").encode())
    def line(self, timeout=10):
        self.s.settimeout(timeout)
        while b"\r\n" not in self.buf:
            d = self.s.recv(65536)
            if not d: raise EOFError("connection closed")
            self.buf += d
        l, self.buf = self.buf.split(b"\r\n", 1); l = l.decode(errors="replace")
        if l.startswith("PING"): self.send("PONG" + l[4:])
        return l
    def wait(self, needle, timeout=10):
        needles = (needle,) if isinstance(needle, str) else needle
        end = time.time() + timeout
        while time.time() < end:
            l = self.line(max(0.1, end - time.time()))
            if any(n in l for n in needles): return l
        raise TimeoutError(needles)
    def collect(self, done, timeout=10):
        out, end = [], time.time() + timeout
        while time.time() < end:
            l = self.line(max(0.1, end - time.time())); out.append(l)
            if done(l): break
        return out
    def drain(self, t=0.5):
        out = []
        try:
            while True: out.append(self.line(t))
        except (socket.timeout, TimeoutError): pass
        return out
    def whois(self, nick):
        self.drain(0.2); self.send(f"WHOIS {nick}")
        lines = self.collect(lambda l: " 318 " in l)
        host = next((l.split()[5] for l in lines if " 311 " in l), None)
        line313 = next((l.split(" :", 1)[1] for l in lines if " 313 " in l), None)
        return host, line313
    def roleserv(self, cmd, quiet=1.5):
        self.drain(0.2); self.send("PRIVMSG RoleServ :" + cmd)
        return [self.wait("NOTICE", 10)] + self.drain(quiet)
    def opered(self, lines): return any(" 381 " in l for l in lines) or any(" MODE " in l and "+o" in l for l in lines)

def eventually(cond, timeout=15, every=1.0):
    end = time.time() + timeout
    while time.time() < end:
        v = cond()
        if v: return v
        time.sleep(every)
    return cond()

def compose(*args, stdin=None):
    return subprocess.run(COMPOSE + list(args), input=stdin, capture_output=True, text=True)

# ---- set up roles ---------------------------------------------------------
AUTH_PUBLIC = f"http://127.0.0.1:{os.environ.get('TEST_AUTH_PUBLIC_PORT', '28080')}"
AUTH_TEST = f"http://127.0.0.1:{os.environ.get('TEST_AUTH_PORT', '28081')}"
PW = {"alice": "pw-alice", "bob": "pw-bob", "dave": "pw-dave", "erin": "pw-erin"}
owner_token = None
if MODE == "real":
    for u in ["olivia", "alice", "bob", "dave", "erin"]:
        r = compose("exec", "-T", "auth", "./auth", "user", "create", "--username", u, "--email", f"{u}@example.test",
                    "--password-stdin", *(["--role", "owner"] if u == "olivia" else []), stdin=f"pw-{u}-long-enough\n")
        check(f"auth CLI user create {u}", r.returncode == 0, r.stderr)
    PW = {u: f"pw-{u}-long-enough" for u in ["olivia", "alice", "bob", "dave", "erin"]}
    st, body = http("POST", AUTH_PUBLIC + "/v1/login", {"login": "olivia", "password": PW["olivia"]})
    owner_token = (body or {}).get("access_token")
    check("owner login (admin API)", st == 200 and owner_token, body)
    st, body = http("GET", AUTH_PUBLIC + "/v1/admin/roles", token=owner_token)
    ids = {r["name"]: r["id"] for r in (body or {}).get("roles", [])}
    st, _ = http("PATCH", AUTH_PUBLIC + f"/v1/admin/roles/{ids['Admin']}", {"irc": {"whois_line": "is a Network Administrator", "vhost": "staff/{username}"}}, owner_token)
    check("admin API: Admin role gets a WHOIS line + vhost", st == 200, st)
    st, body = http("POST", AUTH_PUBLIC + "/v1/admin/roles", {"name": "DJ", "permissions": ["voice.create"], "irc": {"vhost": "dj/{username}"}}, owner_token)
    check("admin API: create DJ (non-oper, vhost)", st == 201, body)
    for u, rl in [("alice", "Admin"), ("dave", "DJ"), ("erin", "Moderator")]:
        r = compose("exec", "-T", "auth", "./auth", "user", "set-roles", u, rl)
        check(f"auth CLI user set-roles {u} {rl}", r.returncode == 0 and f"roles={rl}" in r.stdout, r.stdout + r.stderr)
    time.sleep(5)  # ROLES_POLL_SECONDS=2: watch-settings renders the classes

# ---- logins, WHOIS, vhosts --------------------------------------------------
alice = Client("alice", PW["alice"])
check("alice: SASL PLAIN", alice.ok(), alice.sasl)
check("alice (Admin role, oper): opered up at login (381 / MODE +o)", alice.opered(alice.burst), alice.burst[-5:])
bob = Client("bob", PW["bob"])
check("bob: SASL PLAIN", bob.ok(), bob.sasl)
check("bob (no roles): not an operator", not bob.opered(bob.burst))
host, l313 = bob.whois("alice")
check("WHOIS alice (seen by bob): vhost staff/alice in the hostmask", host == "staff/alice", host)
check("WHOIS alice: role line 'is a Network Administrator' (313)", l313 == "is a Network Administrator", l313)
dave = Client("dave", PW["dave"])
host, l313 = bob.whois("dave")
check("WHOIS dave (DJ: no oper): vhost dj/dave", host == "dj/dave", host)
check("WHOIS dave: no oper line", l313 is None, l313)
host, l313 = alice.whois("bob")
check("WHOIS bob: cloaked host, no oper line", host and "/" not in host and l313 is None, (host, l313))
erin = Client("erin", PW["erin"])
check("erin (Moderator, oper by its irc.* permissions): opered up", erin.opered(erin.burst))
host, l313 = bob.whois("erin")
check("WHOIS erin: Moderator's default line", l313 == "is a Moderator", l313)

# ---- RoleServ --------------------------------------------------------------
out = " ".join(eventually(lambda: bob.roleserv("LIST"), 20) or [])
check("RoleServ LIST", all(n in out for n in ("Owner", "Admin", "Moderator", "DJ")), out)
out = " ".join(bob.roleserv("INFO Admin"))
check("RoleServ INFO Admin", "IRC operator: yes" in out and "staff/{username}" in out, out)
out = " ".join(bob.roleserv("ROLES alice"))
check("RoleServ ROLES alice", "Admin" in out, out)
out = " ".join(bob.roleserv("HELP"))
check("RoleServ HELP (no roles.manage: read-only commands)", "LIST" in out and "ASSIGN" not in out, out)
FULL = MODE == "fake" or os.environ.get("AUTH_FULL") == "1"  # auth with acting API + members + grants
if not FULL:
    out = " ".join(alice.roleserv("ASSIGN bob Moderator"))
    check("RoleServ writes wait for auth's acting API", "isn't available on this network yet" in out, out)
    out = " ".join(alice.roleserv("WHO Admin"))
    check("RoleServ WHO without an internal members list", "isn't available" in out, out)
else:
    out = " ".join(alice.roleserv("WHO Admin"))
    check("RoleServ WHO Admin", "alice" in out, out)
    out = " ".join(erin.roleserv("ASSIGN bob DJ"))
    check("RoleServ: erin (no roles.manage) can't assign", "Permission denied" in out, out)
    out = " ".join(alice.roleserv("ASSIGN bob Admin"))
    check("RoleServ: alice can't assign her own rank (hierarchy)", "below your highest role" in out, out)
    out = alice.roleserv("ASSIGN bob Moderator")
    check("RoleServ: alice ASSIGN bob Moderator", "bob now has" in " ".join(out), out)
    got = bob.drain(2)
    check("bob opered up live by the assignment (no reconnect)", bob.opered(got), got)
    host, l313 = alice.whois("bob")
    check("WHOIS bob now: 'is a Moderator'" + (" + mod/bob" if MODE == "fake" else ""),
          (host == "mod/bob" or MODE == "real") and l313 == "is a Moderator", (host, l313))
    out = alice.roleserv("REMOVE bob Moderator")
    got = bob.drain(2)
    check("RoleServ REMOVE: bob de-opered live", any("MODE bob -o" in l for l in got), got)

# ---- role edits reach connected users (rehash + account_grants) -----------
if MODE == "real":
    st, _ = http("PATCH", AUTH_PUBLIC + f"/v1/admin/roles/{ids['Admin']}", {"irc": {"whois_line": "runs this place"}}, owner_token)
else:
    http("POST", AUTH_TEST + "/test/state", {"roles": {"Admin": {"irc": {"whois_line": "runs this place"}}}})
line = eventually(lambda: (lambda h, l: l if l == "runs this place" else None)(*bob.whois("alice")), 15)
check("Admin's WHOIS line edited: alice's 313 changes live", line == "runs this place", bob.whois("alice"))

if MODE == "real":
    st, body = http("GET", AUTH_PUBLIC + f"/v1/admin/roles/{ids['Moderator']}", token=owner_token)
    keep = [p for p in body["role"]["permissions"] if not p.startswith("irc.")]
    st, _ = http("PATCH", AUTH_PUBLIC + f"/v1/admin/roles/{ids['Moderator']}", {"permissions": keep, "irc": {"oper": False}}, owner_token)
    check("admin API: Moderator loses its irc.* permissions", st == 200, st)
else:
    http("POST", AUTH_TEST + "/test/state", {"roles": {"Moderator": {"permissions": ["users.view"]}}})
got = eventually(lambda: [l for l in erin.drain(1) if "MODE erin -o" in l], 15)
check("erin de-opered live when Moderator stops being an oper role", bool(got), got)

# ---- membership changes ------------------------------------------------------
if MODE == "real" and FULL:
    r = compose("exec", "-T", "auth", "./auth", "user", "set-roles", "dave")
    check("auth CLI: dave loses DJ", r.returncode == 0 and "roles=" in r.stdout, r.stdout + r.stderr)
    host = eventually(lambda: (lambda h, l: h if h != "dj/dave" else None)(*bob.whois("dave")), 15)
    check("dave loses DJ (CLI): vhost removed live via the grants feed", host and host != "dj/dave", host)
    r = compose("exec", "-T", "auth", "./auth", "user", "set-roles", "bob", "Admin")
    got = eventually(lambda: [l for l in bob.drain(1) if " 381 " in l], 15)
    check("bob gets Admin (CLI): opered up live via the grants feed", bool(got), got)
    st, body = http("GET", AUTH_PUBLIC + "/v1/admin/users?q=dave", token=owner_token)
    did = next(u["id"] for u in body["users"] if u["username"] == "dave")
    st, _ = http("POST", AUTH_PUBLIC + f"/v1/admin/users/{did}/disable", token=owner_token)
    check("admin API: disable dave", st == 200, st)
    d2 = Client("dave", PW["dave"], nick="dave2", register=False)
    check("disabled dave can't log in", not d2.ok(), d2.sasl)
elif MODE == "real":
    r = compose("exec", "-T", "auth", "./auth", "user", "set-roles", "dave")
    check("auth CLI: dave loses DJ", r.returncode == 0 and "roles=" in r.stdout, r.stdout + r.stderr)
    host, _ = bob.whois("dave")
    check("auth 1.5 (no grants feed): dave keeps dj/dave until he reconnects", host == "dj/dave", host)
    dave.send("QUIT"); time.sleep(1)
    dave = Client("dave", PW["dave"])
    host, _ = bob.whois("dave")
    check("dave reconnects without DJ: vhost gone", host != "dj/dave", host)
    st, body = http("POST", AUTH_PUBLIC + "/v1/login", {"login": "alice", "password": PW["alice"]})
    tok = (body or {}).get("access_token")
    alice.send("QUIT"); time.sleep(1)  # so the bearer login has to grant oper itself
    a2 = Client("alice", bearer=tok, nick="alice")
    check("alice: SASL OAUTHBEARER with an auth access token", a2.ok(), a2.sasl)
    check("alice via OAUTHBEARER: opered up (roles looked up via /internal/v1/users/alice/irc)", a2.opered(a2.burst), a2.burst[-5:])
else:
    http("POST", AUTH_TEST + "/test/state", {"users": {"dave": []}})
    host = eventually(lambda: (lambda h, l: h if h != "dj/dave" else None)(*bob.whois("dave")), 15)
    check("dave loses DJ (admin panel): vhost removed live via the grants feed", host and host != "dj/dave", host)
    http("POST", AUTH_TEST + "/test/state", {"users": {"bob": ["Admin"]}})
    got = eventually(lambda: [l for l in bob.drain(1) if " 381 " in l], 15)
    check("bob gets Admin (admin panel): opered up live via the grants feed", bool(got), got)
    http("POST", AUTH_TEST + "/test/state", {"disable": ["dave"]})
    d2 = Client("dave", PW["dave"], nick="dave2", register=False)
    check("disabled dave can't log in", not d2.ok(), d2.sasl)

# RoleServ's account can't be taken over through auth
rs = Client("RoleServ", "anything", nick="RoleServ", register=False)
check("SASL as RoleServ with someone else's password fails", not rs.ok(), rs.sasl)

print(f"\n{len(failures)} failure(s)" if failures else "\nall passed")
sys.exit(1 if failures else 0)
