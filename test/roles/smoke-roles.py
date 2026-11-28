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

Channel lifecycle (patched Ergo, docs/irc-channels.md; IRC_CHANNEL_EXPIRE_AFTER=20s
in compose.yaml): a logged-in user's new channel is registered to them on JOIN,
up to max-channels-per-account (15); a guest's isn't; unused registered
channels expire (with a warning first) while a used one stays; a user deleted
in auth loses their Ergo account and channels, a disabled one keeps them.

Guest access (docs/irc-channels.md): with guest_access true a guest
connects; set to false (fake: /test/state, real: PATCH /v1/admin/settings)
Ergo is rehashed with require-sasl and refuses new guests (FAIL
ACCOUNT_REQUIRED) while SASL users, RoleServ, connected guests and the admin
panel's CAP LS health probe keep working; back to true lets guests in again.
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
    def __init__(self, user, password=None, bearer=None, nick=None, register=True, guest=False):
        self.s = socket.create_connection(IRC, timeout=10); self.buf = b""; self.nick = nick or user
        self.log = []  # every line received
        if guest:  # no SASL: not logged in
            self.sasl = ""; self.send(f"NICK {self.nick}"); self.send(f"USER {user} 0 * :{user}")
            self.burst = self.collect(lambda l: " 376 " in l or " 422 " in l, 15) + self.drain(1.0); return
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
        l, self.buf = self.buf.split(b"\r\n", 1); l = l.decode(errors="replace"); self.log.append(l)
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

def ergo_status():
    """Ergo's POST /v1/status (from inside the container: the API isn't published)."""
    r = compose("exec", "-T", "ergo", "wget", "-qO-", "--header", "Authorization: Bearer " + os.environ["ERGO_API_TOKEN"],
                "--post-data", "{}", "http://127.0.0.1:8089/v1/status")
    try:
        return json.loads(r.stdout)
    except ValueError:
        return {"error": r.stdout + r.stderr}

# ---- set up roles ---------------------------------------------------------
AUTH_PUBLIC = f"http://127.0.0.1:{os.environ.get('TEST_AUTH_PUBLIC_PORT', '28080')}"
AUTH_TEST = f"http://127.0.0.1:{os.environ.get('TEST_AUTH_PORT', '28081')}"
PW = {"alice": "pw-alice", "bob": "pw-bob", "dave": "pw-dave", "erin": "pw-erin", "frank": "pw-frank", "gina": "pw-gina", "hank": "pw-hank"}
owner_token = None
if MODE == "real":
    for u in ["olivia", "alice", "bob", "dave", "erin", "frank", "gina", "hank"]:
        r = compose("exec", "-T", "auth", "./auth", "user", "create", "--username", u, "--email", f"{u}@example.test",
                    "--password-stdin", *(["--role", "owner"] if u == "olivia" else []), stdin=f"pw-{u}-long-enough\n")
        check(f"auth CLI user create {u}", r.returncode == 0, r.stderr)
    PW = {u: f"pw-{u}-long-enough" for u in ["olivia", "alice", "bob", "dave", "erin", "frank", "gina", "hank"]}
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

# ---- channel lifecycle: auto-register, limit, guests, expiry, removed users ----
def cs(c, cmd):
    c.drain(0.2); c.send("PRIVMSG ChanServ :" + cmd)
    return " ".join([c.wait("NOTICE", 10)] + c.drain(0.7))
def join(c, ch):
    c.send("JOIN " + ch)
    return c.collect(lambda l: "ChanServ" in l and ch in l and "registered" in l, 5)
def keep_busy():  # activity in #frank-1, so it never expires
    frank.send("PRIVMSG #frank-1 :still here")
def real_user_id(name):
    st, body = http("GET", AUTH_PUBLIC + "/v1/admin/users?q=" + name, token=owner_token)
    return next(u["id"] for u in body["users"] if u["username"] == name)

frank = Client("frank", PW["frank"])
got = join(frank, "#frank-1") + frank.drain(0.5)
check("auto-register: frank's new #frank-1 is registered on JOIN (ChanServ notice)",
      any("Channel #frank-1 successfully registered" in l for l in got), got)
check("auto-register: frank gets founder (+q)", any("MODE #frank-1 +q frank" in l for l in got), got)
for i in range(2, 16): join(frank, f"#frank-{i}")
got = join(frank, "#frank-16")
check("auto-register: the 16th channel isn't registered (max-channels-per-account 15)",
      any("#frank-16 was not registered automatically" in l for l in got), got)
check("ChanServ INFO #frank-15: registered to frank", "Founder: frank" in cs(frank, "INFO #frank-15"))
guest = Client("guest", nick="guest1", guest=True)
guest.send("JOIN #guest-chan"); guest.drain(1)
out = cs(frank, "INFO #guest-chan")
check("a guest's new channel isn't registered", "is not registered" in out, out)
out = cs(frank, "SET #frank-2 EXPIRY OFF")
check("CS SET EXPIRY OFF needs an oper with chanreg", "Insufficient privileges" in out, out)
out = cs(frank, "GET #frank-2 EXPIRY")
check("CS GET EXPIRY: expiry after 20s", "unregistered after 20s" in out, out)

keep_busy()
gina = Client("gina", PW["gina"]); join(gina, "#gina-chan")
hank = Client("hank", PW["hank"]); join(hank, "#hank-chan")
check("gina's and hank's channels registered", "Founder: gina" in cs(frank, "INFO #gina-chan") and "Founder: hank" in cs(frank, "INFO #hank-chan"))
if MODE == "real":
    r = compose("exec", "-T", "postgres", "psql", "-U", "auth", "-d", "auth", "-c", "DELETE FROM users WHERE username = 'gina'")
    check("delete gina in auth (SQL: auth has no delete API)", r.returncode == 0, r.stderr)
    st, _ = http("POST", AUTH_PUBLIC + f"/v1/admin/users/{real_user_id('hank')}/disable", token=owner_token)
    check("admin API: disable hank", st == 200, st)
else:
    http("POST", AUTH_TEST + "/test/state", {"delete": ["gina"], "disable": ["hank"]})
def disconnected(c):
    try: return [l for l in c.drain(1) if l.startswith("ERROR")]
    except (EOFError, OSError) as e: return [repr(e)]
keep_busy()
got = eventually(lambda: keep_busy() or disconnected(gina), 15)
check("gina deleted in auth: her IRC session is disconnected (Ergo account erased)", bool(got), got)
out = cs(frank, "INFO #gina-chan")
check("gina deleted in auth: #gina-chan is unregistered", "is not registered" in out, out)
keep_busy(); time.sleep(3)
out = cs(frank, "INFO #hank-chan")
check("hank disabled in auth: #hank-chan stays registered", "Founder: hank" in out, out)

# 20s expiry: #frank-1 is kept busy, the others are silent
end = time.time() + 40
while time.time() < end and not any("#frank-3 was unregistered" in l for l in frank.log):
    keep_busy(); frank.drain(3)
seen = frank.log
check("expiry: frank warned before #frank-3 expires", any("#frank-3 has had no activity" in l and "will be unregistered" in l for l in seen), seen[-5:])
check("expiry: frank told #frank-3 was unregistered", any("Channel #frank-3 was unregistered because it had no activity for 20s" in l for l in seen), seen[-5:])
out = cs(frank, "INFO #frank-3")
check("expiry: #frank-3 is no longer registered", "is not registered" in out, out)
out = cs(frank, "INFO #frank-1")
check("expiry: the busy #frank-1 is still registered", "Founder: frank" in out, out)

# ---- guest access ------------------------------------------------------------
def guest_attempt(nick, addr=IRC):
    """A client without SASL: 'welcome' (001) or the refusal line."""
    try: s = socket.create_connection(addr, timeout=10)
    except OSError as e: return repr(e)
    buf = b""
    s.sendall(f"NICK {nick}\r\nUSER {nick} 0 * :{nick}\r\n".encode())
    end = time.time() + 10
    try:
        while time.time() < end:
            try: d = s.recv(65536)
            except OSError as e: return repr(e)
            if not d: return "closed"
            buf += d
            for l in buf.decode(errors="replace").split("\r\n"):
                if " 001 " in l: return "welcome"
                if "ACCOUNT_REQUIRED" in l: return l
        return "timeout"
    finally:
        s.close()
def cap_ls_probe():
    """What the admin panel's IRC health check does first: CAP LS before registering."""
    s = socket.create_connection(IRC, timeout=10)
    try:
        s.sendall(b"CAP LS 302\r\n"); buf = b""
        while b" LS " not in buf:
            d = s.recv(65536)
            if not d: return False
            buf += d
        return True
    finally:
        s.close()
def set_guest_access(on):
    if MODE == "real":
        st, body = http("PATCH", AUTH_PUBLIC + "/v1/admin/settings", {"guest_access": on}, owner_token)
        check(f"admin API: guest_access={on}", st == 200 and (body or {}).get("guest_access") is on, body)
    else:
        http("POST", AUTH_TEST + "/test/state", {"settings": {"guest_access": on}})

GUESTS = MODE == "fake" or "guest_access" in (http("GET", AUTH_PUBLIC + "/v1/config")[1] or {})
if not GUESTS:
    print("SKIP guest access: this auth has no guest_access setting (auth < 1.8)", flush=True)
else:
    check("guest_access true: a guest is welcomed", guest_attempt("guest2") == "welcome")
    set_guest_access(False)
    got = eventually(lambda: (lambda r: r if r != "welcome" else None)(guest_attempt("guest3")), 20, 2)
    check("guest_access false: a new guest is refused (FAIL * ACCOUNT_REQUIRED)",
          got and "FAIL * ACCOUNT_REQUIRED" in got and "You must log in with SASL" in got, got)
    check("guest_access false: the admin panel's health probe still gets CAP LS", cap_ls_probe())
    b2 = Client("bob", PW["bob"], nick="bob2")
    check("guest_access false: SASL login still works", b2.ok() and any(" 376 " in l or " 422 " in l for l in b2.burst), b2.burst[-3:])
    r = compose("restart", "roleserv")
    def rs_list():
        try: out = " ".join(b2.roleserv("LIST", 1.0))
        except TimeoutError: return None  # RoleServ not back yet (401, no NOTICE)
        return out if "Owner" in out else None
    out = eventually(rs_list, 30, 2) or ""
    check("guest_access false: RoleServ reconnects (SASL on the services listener) and answers", r.returncode == 0 and "Owner" in out, out)
    guest.drain(0.2); guest.send("PRIVMSG #guest-chan :still connected"); guest.send("PING :guestcheck")
    check("guest_access false: an already connected guest stays", any("guestcheck" in l for l in guest.drain(2)))
    set_guest_access(True)
    got = eventually(lambda: guest_attempt("guest4") == "welcome", 20, 2)
    check("guest_access true again: guests are welcomed", got)

# ---- stack config file (BOSON_CONFIG) ---------------------------------------
# run.sh starts Ergo with irc.plaintext: true in the file and IRC_PLAINTEXT=false
# in the env, so every check above already ran on a listener only the file
# turned on. Here the file is rewritten (atomically, like the admin panel)
# and boson-ergo must re-render ircd.yaml and rehash the real Ergo.
CONFIG = os.path.join(os.environ["TEST_CONFIG_DIR"], "boson.yaml")
STATUS = f"http://127.0.0.1:{os.environ.get('TEST_STATUS_PORT', '28098')}/internal/config/status"
IRC_ALT = ("127.0.0.1", int(os.environ.get("TEST_IRC_ALT_PORT", "26668")))
def write_config(text):
    tmp = CONFIG + ".tmp"
    with open(tmp, "w") as f: f.write(text)
    os.chmod(tmp, 0o644); os.replace(tmp, CONFIG)
def config_status():
    try: return http("GET", STATUS)[1] or {}
    except OSError: return {}
def applied_after(t0):
    """Status once the file written after t0 was applied (or failed)."""
    def done():
        st = config_status()
        la, mt = parse_ts(st.get("loaded_at")), parse_ts(st.get("file_mtime"))
        if st.get("ok") is False:  # a failure counts only for a file written after t0
            return st if mt and mt >= t0 else None
        return st if la and la >= t0 else None
    # fsnotify, or the 10 s mtime poll (Docker Desktop bind mounts)
    return eventually(done, 25, 0.5) or config_status()
def utcnow(): return time.time()
def parse_ts(v):
    """RFC 3339 (Go's, nanoseconds trimmed) to epoch seconds; None if unset."""
    if not v: return None
    import datetime
    base, _, frac = v.rstrip("Z").partition(".")
    t = datetime.datetime.strptime(base, "%Y-%m-%dT%H:%M:%S").replace(tzinfo=datetime.timezone.utc).timestamp()
    return t + (float("0." + frac) if frac else 0)

st = config_status()
check("config status: served by boson-ergo, file loaded", st.get("ok") is True and st.get("path") == "/config/boson.yaml"
      and st.get("loaded_at") and st.get("file_mtime") and st.get("restart_required") == [], st)
check("config file beats env: plaintext listener on (IRC_PLAINTEXT=false)", guest_attempt("cfg0") == "welcome")

# Listener change: plaintext moves to 6668, rehashed without a restart.
t0 = utcnow()
write_config("version: 1\nnetwork: {name: Boson}\nirc:\n  plaintext: true\n  plaintext_port: 6668\n")
st = applied_after(t0)
check("config reload: plaintext_port 6668 applied", st.get("ok") is True and st.get("restart_required") == [], st)
got = eventually(lambda: guest_attempt("cfg1", IRC_ALT) == "welcome", 10, 1)
check("config reload: Ergo listens on the new plaintext port (rehash)", got)
check("config reload: the old plaintext port is closed", guest_attempt("cfg2") != "welcome")
check("config reload: connected clients stay", (lambda: (frank.send("PING :cfgcheck"), any("cfgcheck" in l for l in frank.drain(2)))[1])())

# Back to 6667 (the rest of the run and the healthcheck use it).
t0 = utcnow()
write_config("version: 1\nnetwork: {name: Boson}\nirc:\n  plaintext: true\n")
st = applied_after(t0)
got = eventually(lambda: guest_attempt("cfg3") == "welcome", 10, 1)
check("config reload: plaintext back on 6667", st.get("ok") is True and got, st)

# require_sasl_exempt: with guests off, exempting every address lets a guest in.
if GUESTS:
    set_guest_access(False)
    got = eventually(lambda: (lambda r: r if r != "welcome" else None)(guest_attempt("cfg4")), 20, 2)
    check("exempt list: guest refused while guests are off", got and "ACCOUNT_REQUIRED" in got, got)
    t0 = utcnow()
    write_config("version: 1\nirc:\n  plaintext: true\n  require_sasl_exempt: [0.0.0.0/0, \"::/0\"]\n")
    st = applied_after(t0)
    got = eventually(lambda: guest_attempt("cfg5") == "welcome", 10, 1)
    check("exempt list from the config file: guest let in (rehash)", st.get("ok") is True and got, st)
    t0 = utcnow()
    write_config("version: 1\nirc:\n  plaintext: true\n")
    applied_after(t0)
    got = eventually(lambda: guest_attempt("cfg6") != "welcome", 10, 1)
    check("exempt list removed: back to IRC_REQUIRE_SASL_EXEMPT's default, guest refused", got)
    set_guest_access(True)
    eventually(lambda: guest_attempt("cfg7") == "welcome", 20, 2)

# Invalid file: last good config stays, the error has a line and column.
write_config("version: 1\nirc:\n  plaintext: false\n  require_sasl_exempt: [nope]\n")
st = eventually(lambda: (lambda s: s if s.get("ok") is False else None)(config_status()), 25, 0.5) or {}
check("invalid config file: ok=false with line/column", st.get("error_line") == 4 and st.get("error_column") and "nope" in st.get("error", ""), st)
check("invalid config file: Ergo keeps the last good config (plaintext still on)", guest_attempt("cfg8") == "welcome")

# Restart-only key: reported, not rendered.
t0 = utcnow()
write_config("version: 1\nirc:\n  plaintext: true\n  persistent_history: false\n")
st = applied_after(t0)
check("persistent_history change: restart_required", st.get("ok") is True and st.get("restart_required") == ["irc.persistent_history"], st)
r = compose("exec", "-T", "ergo", "grep", "-c", "database-path", "/ircd/ircd.yaml")
check("persistent_history change: ircd.yaml keeps the running value", r.returncode == 0, r.stdout + r.stderr)
write_config("version: 1\nnetwork: {name: Boson}\nirc:\n  plaintext: true\n")

# ---- network channels (fake auth: the feed's "network") ----------------------
# RoleServ registers them to the founder (patched Ergo /v1/channel_ensure),
# joins guests on connect (CONNECT snomask, /v1/sajoin by nick; once per nick
# per 24 h) and accounts on their first login (recorded in auth).
def lobby_joined(c, ch="#lobby", timeout=8):
    end = time.time() + timeout
    while time.time() < end:
        if any(" JOIN " in l and l.rstrip().lower().endswith(ch) for l in c.log): return True
        c.drain(0.5)
    return False
if MODE == "fake":
    http("POST", AUTH_TEST + "/test/state", {"network": {"founder": "alice", "autojoin_guests": True, "channels": [
        {"channel": "#lobby", "name": "#Lobby", "topic": "Welcome to the smoke", "autojoin": True},
        {"channel": "#rules", "name": "#rules", "topic": "", "autojoin": False}]}})
    time.sleep(5)  # ROLES_POLL_SECONDS 2: RoleServ ensures them
    g = Client("netguest1", guest=True)
    check("network channels: a new guest is joined to #lobby on connect", lobby_joined(g), g.log[-5:])
    check("network channels: the guest sees #lobby's configured topic (332)",
          any(" 332 " in l and "Welcome to the smoke" in l for l in g.log), [l for l in g.log if "#Lobby" in l or "#lobby" in l][:5])
    check("network channels: not joined to #rules (no auto-join)", not any(" JOIN " in l and "#rules" in l.lower() for l in g.log))
    info = cs(g, "INFO #lobby")
    check("network channels: #lobby is registered to the founder (alice)", "alice" in info.lower(), info)
    info = cs(g, "INFO #rules")
    check("network channels: #rules is registered too", "alice" in info.lower() and "not registered" not in info, info)
    g.send("QUIT"); time.sleep(1)
    g = Client("netguest1", guest=True)
    g.drain(4)
    check("network channels: the same guest nick reconnecting within 24 h isn't joined again",
          not any(" JOIN " in l for l in g.log), [l for l in g.log if " JOIN " in l])
    g.send("QUIT")

    # an account: joined on its first login only (a /part sticks)
    http("POST", AUTH_TEST + "/test/state", {"users": {"ivy": []}})  # a new account (password pw-ivy)
    h = Client("ivy", "pw-ivy")
    check("network channels: an account is joined to #lobby on its first login", lobby_joined(h), h.log[-5:])
    h.send("PART #Lobby"); h.drain(1); h.send("QUIT"); time.sleep(1)
    h = Client("ivy", "pw-ivy"); h.drain(5)
    check("network channels: after /part, the next login doesn't join #lobby again",
          not any(" JOIN " in l and "#lobby" in l.lower() for l in h.log), [l for l in h.log if " JOIN " in l])

    # creation policy "Only admins" (auth channel_creation: admins): members
    # can join network channels, but can't create channels; admins can.
    http("POST", AUTH_TEST + "/test/state", {"settings": {"channel_creation": "admins"}})
    # frank (no roles) and alice (Admin: irc.chanreg) are still connected
    n = [0]
    def try_create(c, prefix):
        n[0] += 1; ch = f"#{prefix}-{n[0]}"
        c.drain(0.2); c.send("JOIN " + ch)
        return c.collect(lambda l: ch in l and (" 403 " in l or " JOIN " in l), 8)
    got = eventually(lambda: (lambda g: g if any(" 403 " in l for l in g) else None)(try_create(frank, "frank-new")), 20, 1) or []
    check("channel_creation=admins: a member can't create a channel (403)",
          any(" 403 " in l and "Only server operators can create new channels" in l for l in got), got[-3:])
    frank.drain(0.2); frank.send("JOIN #rules")
    got = frank.collect(lambda l: "#rules" in l and (" JOIN " in l or " 403 " in l or " 47" in l), 8)
    check("channel_creation=admins: a member joins a network channel (#rules)", any(" JOIN " in l for l in got), got[-3:])
    got = try_create(alice, "alice-new")
    check("channel_creation=admins: an admin (irc.chanreg) creates a channel", any(" JOIN " in l for l in got), got[-3:])
    http("POST", AUTH_TEST + "/test/state", {"settings": {"channel_creation": "registered"}})

    # removing #rules from the list: RoleServ registered it, so it's dropped
    http("POST", AUTH_TEST + "/test/state", {"network": {"channels": [
        {"channel": "#lobby", "name": "#Lobby", "topic": "Welcome to the smoke", "autojoin": True}]}})
    got = eventually(lambda: "not registered" in cs(frank, "INFO #rules"), 15, 2)
    check("network channels: a channel removed from the list is unregistered", got)
    check("network channels: its members weren't kicked", not any(" KICK " in l and "#rules" in l for l in frank.log))
    info = cs(frank, "INFO #lobby")
    check("network channels: #lobby stays registered", "alice" in info.lower(), info)
    # /v1/status counts registered channels (patch 0009; the admin panel's
    # usage stats): at least #lobby, at most every channel there is, and one
    # more once RoleServ registers another network channel.
    st = ergo_status()
    check("Ergo /v1/status: registered_channels counts the registered ones",
          isinstance(st.get("registered_channels"), int) and 1 <= st["registered_channels"] <= st.get("channels", -1), st)
    before = st.get("registered_channels")
    http("POST", AUTH_TEST + "/test/state", {"network": {"channels": [
        {"channel": "#lobby", "name": "#Lobby", "topic": "Welcome to the smoke", "autojoin": True},
        {"channel": "#counted", "name": "#counted", "topic": "", "autojoin": False}]}})
    got = eventually(lambda: isinstance(before, int) and ergo_status().get("registered_channels") == before + 1, 15, 2)
    check("Ergo /v1/status: a newly registered channel is counted", got, ergo_status())
    h.send("QUIT")

# RoleServ's account can't be taken over through auth
rs = Client("RoleServ", "anything", nick="RoleServ", register=False)
check("SASL as RoleServ with someone else's password fails", not rs.ok(), rs.sasl)

print(f"\n{len(failures)} failure(s)" if failures else "\nall passed")
sys.exit(1 if failures else 0)
