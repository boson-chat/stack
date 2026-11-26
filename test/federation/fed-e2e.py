#!/usr/bin/env python3
"""Federated login between two Boson installs (test/federation/run.sh runs
this in a container on the shared network; names resolve to the right
Traefik through network aliases, TLS is Pebble's, /ca/roots.pem).

  --phase untrusted   home.test is listed in the remote's directory but not
                      verified: alice's sign-in on the remote is refused with
                      domain_not_trusted. Saves alice's home login in /state.
  --phase main        (after a restart of the remote's auth, which forgets
                      the "not listed" answer) the listing is verified, then:
    1. alice (signed up on home in phase untrusted) logs in on home;
    2. gets an assertion for the remote;
    3. logs into the remote: federated, identity alice@home.test;
    4. connects to the remote's Ergo with OAUTHBEARER as her handle, joins
       a channel;
    5. bob, a local user of the remote, sees her message;
    6. the remote's identity lookup says alice@home.test;
    7. she uploads a file; her quota is the remote-user quota (2MB);
    8. she gets a voice token that the remote's voice server takes;
    then the refusals: a replayed assertion (replayed), the remote's mode
    off (federation_off, config/boson.yaml edited live, then back), and
    9. the remote admin bans her: Ergo drops her connection, a new IRC login
       with her token and a new sign-in (identity_banned) are refused.
"""
import argparse, base64, http.client, json, os, queue, re, secrets, socket, ssl, sys, threading, time, urllib.parse

p = argparse.ArgumentParser()
p.add_argument("--phase", required=True, choices=["untrusted", "main"])
p.add_argument("--home", default="home.test")
p.add_argument("--remote", default="remote.test")
p.add_argument("--ca", default="/ca/roots.pem")
p.add_argument("--state", default="/state")
p.add_argument("--remote-config", default="/remote-config/boson.yaml")
args = p.parse_args()

HOME, REMOTE = args.home, args.remote
HOME_AUTH, REMOTE_AUTH = f"auth.{HOME}", f"auth.{REMOTE}"
REMOTE_ISSUER = f"https://{REMOTE_AUTH}"
ctx = ssl.create_default_context(cafile=args.ca)

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail != "" else ""), flush=True)
    if not ok: failures.append(name)
    return ok

def must(name, ok, detail=""):
    if not check(name, ok, detail): finish()

def finish():
    if failures:
        print(f"\n{len(failures)} check(s) failed: {', '.join(failures)}"); sys.exit(1)
    print(f"\nphase {args.phase}: all checks passed"); sys.exit(0)

def req(host, method, path, body=None, token=None, raw=None, headers=None, auth=None):
    """HTTPS to host (Traefik through the alias). JSON in and out."""
    c = http.client.HTTPSConnection(host, 443, context=ctx, timeout=20)
    h = {"User-Agent": "boson-fed-e2e", "Accept": "application/json"}
    data = raw
    if body is not None:
        data = json.dumps(body).encode(); h["Content-Type"] = "application/json"
    if token: h["Authorization"] = "Bearer " + token
    if auth: h["Authorization"] = auth
    h.update(headers or {})
    c.request(method, path, body=data, headers=h)
    r = c.getresponse(); b = r.read(); c.close()
    try: return r.status, json.loads(b or b"null")
    except ValueError: return r.status, b.decode(errors="replace")

def wait(name, fn, timeout=240, every=3):
    """Retry fn() -> (ok, detail) until ok or timeout."""
    end, last = time.time() + timeout, None
    while True:
        try:
            ok, last = fn()
            if ok: return check(name, True, last if isinstance(last, (str, int)) else "")
        except (OSError, ssl.SSLError, http.client.HTTPException) as e:
            last = repr(e)
        if time.time() > end:
            return check(name, False, last)
        time.sleep(every)

def code(body):
    return body.get("code") or body.get("error") if isinstance(body, dict) else body

STATE = os.path.join(args.state, "state.json")
def load_state():
    return json.load(open(STATE))
def save_state(st):
    json.dump(st, open(STATE, "w"))

# ---- helpers ----
def login(host, user, pw):
    st, r = req(host, "POST", "/v1/login", {"login": user, "password": pw})
    must(f"log in {user} on {host}", st == 200 and isinstance(r, dict) and r.get("access_token"), f"{st} {r}")
    return r["access_token"]

def register(host, user, pw):
    st, r = req(host, "POST", "/v1/register", {"username": user, "email": f"{user}@example.com", "password": pw})
    must(f"sign up {user} on {host}", st in (200, 201), f"{st} {r}")

def assertion(home_token):
    st, r = req(HOME_AUTH, "POST", "/v1/federation/assertions", {"audience": REMOTE_ISSUER}, token=home_token)
    must("home issues an assertion for the remote", st == 200 and isinstance(r, dict) and r.get("assertion")
         and r.get("expires_in") == 300, f"{st} {r}")
    return r["assertion"]

def fed_login(a):
    return req(REMOTE_AUTH, "POST", "/v1/federation/login", {"assertion": a})

def jwt_claims(tok):
    part = tok.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))

def directory_listing(key, verify):
    """Register irc.<home> in the remote's directory (network key), verify it if asked."""
    host = f"directory.{REMOTE}"
    st, r = req(host, "POST", "/v1/networks", {"hostname": f"irc.{HOME}", "port": 6697, "tls": True,
                "name": "Home", "description": "federation e2e home server", "languages": ["en"]}, auth=f"Network {key}")
    n = r.get("network", r) if isinstance(r, dict) else {}
    must(f"irc.{HOME} listed in the remote's directory", st in (200, 201) and n.get("id"), f"{st} {r}")
    if verify:
        st, r = req(host, "POST", f"/v1/networks/{urllib.parse.quote(n['id'])}/verify", auth=f"Network {key}")
        must(f"irc.{HOME} verified (fake DNS)", st == 200 and isinstance(r, dict) and r.get("success") is True, f"{st} {r}")
    q = urllib.parse.urlencode({"hostname": f"irc.{HOME}"})
    st, r = req(host, "GET", f"/v1/networks/lookup?{q}")
    want = 200 if verify else 404
    check(f"public lookup of irc.{HOME}: {want}", st == want, f"{st} {r}")

# ---- IRC ----
class IRC:
    """A TLS IRC client on the remote's Ergo (ergo.<remote>, certificate for irc.<remote>)."""
    def __init__(self, nick, mech, payload):
        self.lines, self.q, self.closed = [], queue.Queue(), threading.Event()
        raw = socket.create_connection((f"ergo.{REMOTE}", 6697), timeout=30)
        self.s = ctx.wrap_socket(raw, server_hostname=f"irc.{REMOTE}")
        self.s.settimeout(None)
        self.f = self.s.makefile("rb")
        self.mech, self.payload, self.nick = mech, payload, nick
        self.result = queue.Queue()
        threading.Thread(target=self.read, daemon=True).start()
        self.send("CAP LS 302"); self.send(f"NICK {nick}"); self.send(f"USER {nick} 0 * :federation e2e")

    def send(self, l):
        self.s.sendall((l + "\r\n").encode())

    def read(self):
        while True:
            try: line = self.f.readline().decode(errors="replace").rstrip("\r\n")
            except (OSError, ValueError): line = ""
            if not line:
                self.closed.set(); self.result.put("closed"); return
            self.lines.append(line)
            parts = line.split()
            if parts[0] == "PING": self.send("PONG " + parts[1]); continue
            cmd = parts[1] if len(parts) > 1 else ""
            if cmd == "CAP" and len(parts) > 4 and parts[3] == "LS" and parts[4] != "*":
                self.send("CAP REQ :sasl")
            elif cmd == "CAP" and len(parts) > 3 and parts[3] == "ACK":
                self.send(f"AUTHENTICATE {self.mech}")
            elif parts[0] == "AUTHENTICATE" and parts[1] == "+":
                b = base64.b64encode(self.payload.encode()).decode()
                for i in range(0, len(b), 400): self.send("AUTHENTICATE " + b[i:i + 400])
                if len(b) % 400 == 0: self.send("AUTHENTICATE +")
            elif cmd == "903":
                self.send("CAP END")
            elif cmd in ("904", "905", "906"):
                self.result.put("sasl-failed"); self.send("QUIT :sasl failed")
            elif cmd == "001":
                self.result.put("welcome")
            self.q.put(line)

    def outcome(self, timeout=30):
        try: return self.result.get(timeout=timeout)
        except queue.Empty: return "timeout"

    def wait(self, pattern, timeout=20):
        rx, end = re.compile(pattern), time.time() + timeout
        while time.time() < end:
            try: line = self.q.get(timeout=max(0.1, end - time.time()))
            except queue.Empty: break
            if rx.search(line): return line
        return None

    def close(self):
        try: self.send("QUIT :bye")
        except OSError: pass
        self.s.close()

def oauthbearer(user, token):
    return f"n,a={user},\x01auth=Bearer {token}\x01\x01"

# ---- discovery (both phases) ----
def discovery():
    def fed_section():
        st, d = req(HOME, "GET", "/.well-known/boson.json")
        f = d.get("federation") if isinstance(d, dict) else None
        return bool(st == 200 and f and f.get("issuer") == f"https://{HOME_AUTH}"), (st, d)
    wait(f"https://{HOME}/.well-known/boson.json has the federation section (Pebble certificate, wellknown from auth)", fed_section)
    st, d = req(HOME, "GET", "/.well-known/boson.json")
    f = d["federation"]
    check("federation section: version, jwks, endpoints, accepts", f.get("version") == 1
          and f.get("jwks_uri") == f"https://{HOME_AUTH}/.well-known/jwks.json"
          and f.get("assertion_endpoint") == f"https://{HOME_AUTH}/v1/federation/assertions"
          and f.get("login_endpoint") == f"https://{HOME_AUTH}/v1/federation/login"
          and f.get("accepts") == "directory", f)
    wait(f"https://{REMOTE}/.well-known/boson.json accepts directory",
         lambda: (lambda r: ((r[0] == 200 and (r[1].get("federation") or {}).get("accepts") == "directory"), r))(req(REMOTE, "GET", "/.well-known/boson.json")))
    wait(f"https://directory.{REMOTE} up", lambda: (lambda r: (r[0] == 200, r))(req(f"directory.{REMOTE}", "GET", "/.well-known/boson-directory.json")))
    wait(f"https://files.{REMOTE} up", lambda: (lambda r: (r[0] == 200 and isinstance(r[1], dict) and r[1].get("enabled") is True, r))(req(f"files.{REMOTE}", "GET", "/v1/config")))

# ---- phase untrusted ----
def phase_untrusted():
    discovery()
    st = {"alice_pw": secrets.token_urlsafe(16), "net_key": secrets.token_urlsafe(32)}
    save_state(st)
    # 1. alice signs up on home
    register(HOME_AUTH, "alice", st["alice_pw"])
    tok = login(HOME_AUTH, "alice", st["alice_pw"])
    # home's listing is pending (not verified) in the remote's directory
    directory_listing(st["net_key"], verify=False)
    a = assertion(tok)
    c = jwt_claims(a)
    check("assertion claims (aud, boson_domain, preferred_username, typ)", c.get("aud") == REMOTE_ISSUER
          and c.get("boson_domain") == HOME and c.get("preferred_username") == "alice" and c.get("exp", 0) - c.get("iat", 0) <= 300, c)
    hdr = json.loads(base64.urlsafe_b64decode(a.split(".")[0] + "=="))
    check("assertion header typ boson-fed+jwt, EdDSA", hdr.get("typ") == "boson-fed+jwt" and hdr.get("alg") == "EdDSA", hdr)
    s, r = fed_login(a)
    check("not verified in the directory: 403 domain_not_trusted", s == 403 and code(r) == "domain_not_trusted", f"{s} {r}")

# ---- phase main ----
def set_remote_mode(mode):
    text = open(args.remote_config).read()
    new = re.sub(r"(\n  federation:\n    mode: )\w+", r"\g<1>" + mode, text, count=1)
    assert new != text or f"mode: {mode}" in text, "no auth.federation.mode in the remote's config"
    tmp = args.remote_config + ".e2e-tmp"
    open(tmp, "w").write(new)
    st = os.stat(args.remote_config)
    os.chmod(tmp, 0o644); os.chown(tmp, st.st_uid, st.st_gid)
    os.replace(tmp, args.remote_config)

def phase_main():
    st = load_state()
    wait(f"remote auth back after its restart", lambda: (lambda r: (r[0] == 200, r[0]))(req(REMOTE_AUTH, "GET", "/v1/config")), timeout=120)
    directory_listing(st["net_key"], verify=True)

    # 1-2. alice on home, an assertion for the remote
    home_tok = login(HOME_AUTH, "alice", st["alice_pw"])
    a = assertion(home_tok)
    # 3. sign in on the remote
    s, r = fed_login(a)
    must("federated sign-in on the remote", s == 200 and isinstance(r, dict) and r.get("access_token"), f"{s} {r}")
    check("login response: federated, identity alice@home.test, handle alice",
          r.get("federated") is True and r.get("identity") == f"alice@{HOME}" and r.get("handle") == "alice", r)
    tok, handle = r["access_token"], r["handle"]
    c = jwt_claims(tok)
    check("the remote's own token (iss remote, name = handle)", c.get("iss") == REMOTE_ISSUER and c.get("name") == handle, c)
    s, me = req(REMOTE_AUTH, "GET", "/v1/me", token=tok)
    check("/v1/me federation block", s == 200 and isinstance(me, dict) and (me.get("federation") or {}).get("home_domain") == HOME, f"{s} {me}")
    # a replayed assertion
    s, r = fed_login(a)
    check("the same assertion again: 401 replayed", s == 401 and code(r) == "replayed", f"{s} {r}")

    # 4-5. IRC: alice with OAUTHBEARER, bob (a local user of the remote) sees her
    bob_pw = secrets.token_urlsafe(16)
    register(REMOTE_AUTH, "bob", bob_pw)
    bob_tok = login(REMOTE_AUTH, "bob", bob_pw)
    chan = "#fed" + secrets.token_hex(3)
    bob = IRC("bob", "PLAIN", f"\0bob\0{bob_pw}")
    must("bob (local) on the remote's Ergo", bob.outcome() == "welcome", bob.lines[-5:])
    alice = IRC(handle, "OAUTHBEARER", oauthbearer(handle, tok))
    must(f"alice on the remote's Ergo with OAUTHBEARER as {handle}", alice.outcome() == "welcome", alice.lines[-5:])
    check("Ergo logged her in as her handle (900)", any(re.search(rf" 900 \S+ \S+ {re.escape(handle)} ", l) for l in alice.lines), alice.lines[-8:])
    bob.send(f"JOIN {chan}")
    check("bob joins", bob.wait(rf"JOIN :?{re.escape(chan)}") is not None)
    alice.send(f"JOIN {chan}")
    check(f"alice joins {chan}", alice.wait(rf"JOIN :?{re.escape(chan)}") is not None, alice.lines[-5:])
    check("bob sees alice join", bob.wait(rf":{re.escape(handle)}!\S+ JOIN :?{re.escape(chan)}") is not None, bob.lines[-5:])
    msg = "hello from home " + secrets.token_hex(4)
    alice.send(f"PRIVMSG {chan} :{msg}")
    line = bob.wait(rf":{re.escape(handle)}!\S+ PRIVMSG {re.escape(chan)} :{re.escape(msg)}")
    check("bob (remote) sees alice's message", line is not None, bob.lines[-5:])

    # 6. identity lookup
    s, r = req(REMOTE_AUTH, "GET", f"/v1/users/{handle}/identity")
    check("identity lookup: alice@home.test", s == 200 and isinstance(r, dict) and r.get("federated") is True
          and r.get("identity") == f"alice@{HOME}" and r.get("home_domain") == HOME, f"{s} {r}")
    s, r = req(REMOTE_AUTH, "GET", "/v1/users/bob/identity")
    check("identity lookup: bob is local", s == 200 and isinstance(r, dict) and r.get("federated") is False, f"{s} {r}")

    # 7. upload with the remote-user quota
    data = b"federated upload " + secrets.token_bytes(64)
    s, r = req(f"files.{REMOTE}", "POST", "/v1/files", raw=data, token=tok,
               headers={"Content-Type": "application/octet-stream", "X-Filename": "fed.bin", "Content-Length": str(len(data))})
    check("alice uploads a file on the remote", s == 201 and isinstance(r, dict) and r.get("id"), f"{s} {r}")
    s, u = req(f"files.{REMOTE}", "GET", "/v1/me/usage", token=tok)
    check("her quota is the remote-user quota (2MB)", s == 200 and isinstance(u, dict) and u.get("quota") == 2_000_000 and u.get("used", 0) >= len(data), f"{s} {u}")
    s, u = req(f"files.{REMOTE}", "GET", "/v1/me/usage", token=bob_tok)
    check("a local user keeps the account quota (10MB)", s == 200 and isinstance(u, dict) and u.get("quota") == 10_000_000, f"{s} {u}")

    # 8. voice
    s, r = req(REMOTE_AUTH, "POST", "/v1/voice/token", {"rooms": []}, token=tok)
    vt = r.get("token") if isinstance(r, dict) else None
    check("alice gets a voice token", s == 200 and vt, f"{s} {r}")
    if vt:
        s, r = req(f"voice.{REMOTE}", "GET", "/v1/rooms", token=vt)
        check("the remote's voice server takes it (GET /v1/rooms)", s == 200 and isinstance(r, dict) and "rooms" in r, f"{s} {r}")

    # federation off on the remote (its config file, applied live), then back
    set_remote_mode("off")
    def off():
        s, r = fed_login(assertion(home_tok))
        return s == 403 and code(r) == "federation_off", (s, r)
    wait("remote mode off: 403 federation_off", off, timeout=60, every=3)
    wait(f"https://{REMOTE}/.well-known/boson.json follows: accepts off",
         lambda: (lambda r: (r[0] == 200 and (r[1].get("federation") or {}).get("accepts") == "off", r))(req(REMOTE, "GET", "/.well-known/boson.json")), timeout=60)
    set_remote_mode("directory")
    def back():
        s, r = fed_login(assertion(home_tok))
        return s == 200, (s, code(r))
    wait("remote mode directory again: signs in", back, timeout=60, every=3)

    # 9. ban
    admin = login(REMOTE_AUTH, os.environ["REMOTE_ADMIN"], os.environ["REMOTE_ADMIN_PASSWORD"])
    s, r = req(REMOTE_AUTH, "GET", f"/v1/admin/federation/users?q=alice", token=admin)
    users = r.get("users", []) if isinstance(r, dict) else []
    ru = next((x for x in users if x.get("identity") == f"alice@{HOME}"), None)
    must("the remote admin lists alice@home.test as a remote user", s == 200 and ru, f"{s} {r}")
    check("remote user: handle, home domain", ru.get("handle") == handle and ru.get("home_domain") == HOME, ru)
    s, r = req(REMOTE_AUTH, "POST", f"/v1/admin/federation/users/{ru['id']}/ban", {"reason": "e2e"}, token=admin)
    check("ban alice@home.test", s == 200 and isinstance(r, dict) and r.get("banned") is True, f"{s} {r}")
    check("Ergo drops her (account disabled -> disconnected)", alice.closed.wait(45), alice.lines[-5:])
    check("bob sees her quit", bob.wait(rf":{re.escape(handle)}!\S+ QUIT") is not None, bob.lines[-5:])
    again = IRC(handle, "OAUTHBEARER", oauthbearer(handle, tok))
    o = again.outcome()
    check("a new IRC login with her token is refused", o in ("sasl-failed", "closed"), f"{o} {again.lines[-5:]}")
    again.close()
    s, r = fed_login(assertion(home_tok))
    check("a new sign-in: 403 identity_banned", s == 403 and code(r) == "identity_banned", f"{s} {r}")
    bob.close()

if args.phase == "untrusted": phase_untrusted()
else: phase_main()
finish()
