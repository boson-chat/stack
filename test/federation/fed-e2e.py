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
    then a replayed assertion is refused (replayed). Saves bob's login and
    both voice tokens in /state for the QUIC voice check (run.sh, voice/).
  --phase ui          the remote's admin panel in Chromium (Playwright), with
                      alice and bob on IRC: the owner signs in; Federation's
                      home status is green and alice@home.test is listed;
                      Ban (Ergo drops her, IRC login and sign-in refused:
                      identity_banned); Unban (back in); "Remote users can
                      join voice" off (voice token: remote_voice_disabled)
                      and on; Block server (Ergo drops her at once, reconnect
                      and sign-in refused: domain_blocked) and
                      Unblock; mode Off (boson.json accepts off,
                      federation_off). Screenshots in --shots.
"""
import argparse, base64, http.client, json, os, queue, re, secrets, socket, ssl, sys, threading, time, urllib.parse

p = argparse.ArgumentParser()
p.add_argument("--phase", required=True, choices=["untrusted", "main", "ui"])
p.add_argument("--home", default="home.test")
p.add_argument("--remote", default="remote.test")
p.add_argument("--ca", default="/ca/roots.pem")
p.add_argument("--state", default="/state")
p.add_argument("--remote-config", default="/remote-config/boson.yaml")
p.add_argument("--shots", default="/shots")
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
    must(f"log in {user} on {host}", st == 200 and isinstance(r, dict) and r.get("access_token"), f"{st} {r if st != 200 else ''}")
    return r["access_token"]

def register(host, user, pw):
    st, r = req(host, "POST", "/v1/register", {"username": user, "email": f"{user}@example.com", "password": pw})
    must(f"sign up {user} on {host}", st in (200, 201), st if st in (200, 201) else f"{st} {r}")

def assertion(home_token):
    st, r = req(HOME_AUTH, "POST", "/v1/federation/assertions", {"audience": REMOTE_ISSUER}, token=home_token)
    must("home issues an assertion for the remote", st == 200 and isinstance(r, dict) and r.get("assertion")
         and 0 < r.get("expires_in", 0) <= 300, f"{st} {r if st != 200 else ''}")
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
    must(f"irc.{HOME} listed in the remote's directory", st in (200, 201) and n.get("id"), f"{st} {n.get('status') if n.get('id') else r}")
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
        # The greeting goes out before the reader starts: reading and writing
        # one TLS connection from two threads at once isn't safe during the
        # TLS 1.3 post-handshake messages.
        self.send("CAP LS 302"); self.send(f"NICK {nick}"); self.send(f"USER {nick} 0 * :federation e2e")
        threading.Thread(target=self.read, daemon=True).start()

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
            elif cmd == "AUTHENTICATE" or (parts[0] == "AUTHENTICATE" and parts[1] != "+"):
                # OAUTHBEARER refused (RFC 7628): an error challenge that the
                # client acknowledges with a single 0x01, then 904.
                self.send("AUTHENTICATE AQ==")
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
def remote_config():
    return open(args.remote_config).read()

def phase_main():
    st = load_state()
    wait(f"remote auth back after its restart", lambda: (lambda r: (r[0] == 200, r[0]))(req(REMOTE_AUTH, "GET", "/v1/config")), timeout=120)
    directory_listing(st["net_key"], verify=True)

    # 1-2. alice on home, an assertion for the remote
    home_tok = login(HOME_AUTH, "alice", st["alice_pw"])
    a = assertion(home_tok)
    # 3. sign in on the remote
    s, r = fed_login(a)
    must("federated sign-in on the remote", s == 200 and isinstance(r, dict) and r.get("access_token"), s if s == 200 else f"{s} {r}")
    shown = {k: r.get(k) for k in ("federated", "identity", "handle", "user")}
    check("login response: federated, identity alice@home.test, handle alice",
          r.get("federated") is True and r.get("identity") == f"alice@{HOME}" and r.get("handle") == "alice", shown)
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
    bob_name = "bob" + secrets.token_hex(2)
    register(REMOTE_AUTH, bob_name, bob_pw)
    bob_tok = login(REMOTE_AUTH, bob_name, bob_pw)
    chan = "#fed" + secrets.token_hex(3)
    bob = IRC(bob_name, "PLAIN", f"\0{bob_name}\0{bob_pw}")
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
    s, r = req(REMOTE_AUTH, "GET", f"/v1/users/{bob_name}/identity")
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
    check("alice gets a voice token", s == 200 and vt, s if vt else f"{s} {r}")
    if vt:
        s, r = req(f"voice.{REMOTE}", "GET", "/v1/rooms", token=vt)
        check("the remote's voice server takes it (GET /v1/rooms)", s == 200 and isinstance(r, dict) and "rooms" in r, f"{s} {r}")

    # for the QUIC voice check (run.sh, fedvoice) and the ui phase: bob's
    # login and both voice tokens (5 minutes; the voice check runs next)
    s, r = req(REMOTE_AUTH, "POST", "/v1/voice/token", {"rooms": []}, token=bob_tok)
    bvt = r.get("token") if isinstance(r, dict) else None
    check("bob (local) gets a voice token", s == 200 and bvt, s if bvt else f"{s} {r}")
    st.update(handle=handle, bob=bob_name, bob_pw=bob_pw, voice_alice=vt, voice_bob=bvt)
    save_state(st)
    alice.close(); bob.close()

# ---- phase ui: the remote's admin panel, in a browser ----
def phase_ui():
    from playwright.sync_api import sync_playwright, expect, Error as PWError
    st = load_state()
    handle, bob_name, bob_pw = st["handle"], st["bob"], st["bob_pw"]
    me = f"alice@{HOME}"
    os.makedirs(args.shots, exist_ok=True)
    home_tok = login(HOME_AUTH, "alice", st["alice_pw"])
    def signin():
        return fed_login(assertion(home_tok))
    chan = "#fedui" + secrets.token_hex(3)
    def on_irc(name, tok):
        """alice on IRC with tok, in chan with bob (once bob is there)."""
        c = IRC(handle, "OAUTHBEARER", oauthbearer(handle, tok))
        must(name, c.outcome() == "welcome", c.lines[-5:])
        if bob_in.is_set():
            c.send(f"JOIN {chan}")
            check(f"bob sees her join {chan}", bob.wait(rf":{re.escape(handle)}!\S+ JOIN :?{re.escape(chan)}") is not None, bob.lines[-5:])
        return c
    bob_in = threading.Event()
    s, r = signin()
    must("alice signs in on the remote", s == 200 and isinstance(r, dict) and r.get("access_token"), s if s == 200 else f"{s} {r}")
    tok = r["access_token"]
    bob = IRC(bob_name, "PLAIN", f"\0{bob_name}\0{bob_pw}")
    must("bob on the remote's Ergo", bob.outcome() == "welcome", bob.lines[-5:])
    bob.send(f"JOIN {chan}")
    must(f"bob joins {chan}", bob.wait(rf"JOIN :?{re.escape(chan)}") is not None, bob.lines[-5:])
    bob_in.set()
    alice = on_irc("alice on the remote's Ergo", tok)

    with sync_playwright() as pw:
        browser = pw.chromium.launch()
        # Chromium doesn't read SSL_CERT_FILE: Pebble's certificate is taken
        # as is here (the other clients verify it).
        bctx = browser.new_context(base_url=f"https://admin.{REMOTE}", ignore_https_errors=True,
                                   viewport={"width": 1280, "height": 900})
        page = bctx.new_page()
        page.set_default_timeout(20_000)
        expect.set_options(timeout=20_000)

        def shot(name):
            # the whole page at its height (the sidebar is sticky, so a
            # full_page capture would draw it mid-page)
            path = f"{args.shots}/{name}.png"
            try:
                page.wait_for_timeout(300)
                h = page.evaluate("document.documentElement.scrollHeight")
                page.set_viewport_size({"width": 1280, "height": max(900, h)})
                page.wait_for_timeout(200)
                page.screenshot(path=path)
                page.set_viewport_size({"width": 1280, "height": 900})
                print(f"screenshot {path}", flush=True)
            except PWError as e: print(f"screenshot {name}: {e}", flush=True)

        def ui(name, fn, critical=False):
            """One UI step: fn() raises on failure (expect, timeouts)."""
            try:
                fn(); return check(name, True)
            except (AssertionError, PWError) as e:
                shot("FAIL-" + re.sub(r"[^a-z0-9]+", "-", name.lower())[:60])
                (must if critical else check)(name, False, str(e).split("\n")[0])
                return False

        row = page.locator('tr[data-testid^="remote-"]').filter(has_text=me)
        toast = lambda text: expect(page.get_by_text(text).first).to_be_visible()

        def open_federation():
            page.goto("/federation")
            expect(page.get_by_role("heading", name="Federation", level=1)).to_be_visible()

        def save():
            page.get_by_role("button", name="Save changes").click()
            toast(re.compile(r"Federation saved"))

        # A. the owner signs in, opens Federation
        def sign_in():
            page.goto("/login")
            page.get_by_label(re.compile("username or email", re.I)).fill(os.environ["REMOTE_ADMIN"])
            page.get_by_label(re.compile("password", re.I)).fill(os.environ["REMOTE_ADMIN_PASSWORD"])
            page.get_by_role("button", name="Sign in").click()
            expect(page.get_by_role("heading", name="Stack", level=1)).to_be_visible()
        ui("admin panel: the remote's owner signs in", sign_in, critical=True)

        def home_green():
            open_federation()
            home = page.get_by_test_id("home-status")
            home.get_by_role("button", name="Check again").click()
            expect(home).to_have_attribute("data-tone", "ok")
            expect(home.get_by_text("Its issuer is this auth service")).to_be_visible()
        ui("Federation: \"Your server as a home\" is green (boson.json, issuer, keys, mode)", home_green)

        def listed():
            expect(row).to_be_visible()
            expect(row).to_contain_text(handle)
            shot("1-federation")
        ui(f"Federation: {me} is in Remote users", listed, critical=True)

        # B. ban through the UI: the same as the API ban
        def ban():
            row.get_by_role("button", name="Ban", exact=True).click()
            dlg = page.get_by_role("dialog", name=f"Ban {me}?")
            expect(dlg).to_contain_text("dropped from IRC")
            dlg.get_by_label(re.compile("Reason")).fill("federation e2e")
            shot("2-ban-confirm")
            dlg.get_by_role("button", name="Ban", exact=True).click()
            expect(row.get_by_text("Banned", exact=True)).to_be_visible()
            shot("3-banned")
        if ui(f"ban {me} in the panel", ban):
            check("Ergo drops her (account disabled -> disconnected)", alice.closed.wait(45), alice.lines[-5:])
            check("bob sees her quit", bob.wait(rf":{re.escape(handle)}!\S+ QUIT") is not None, bob.lines[-5:])
            again = IRC(handle, "OAUTHBEARER", oauthbearer(handle, tok))
            o = again.outcome()
            check("a new IRC login with her token is refused", o in ("sasl-failed", "closed"), f"{o} {again.lines[-5:]}")
            again.close()
            s, r = signin()
            check("a new sign-in: 403 identity_banned", s == 403 and code(r) == "identity_banned", f"{s} {r}")

        # C. unban through the UI
        def unban():
            row.get_by_role("button", name="Unban", exact=True).click()
            page.get_by_role("dialog", name=re.compile("Unban")).get_by_role("button", name="Unban", exact=True).click()
            expect(row.get_by_text("Banned", exact=True)).to_have_count(0)
        if ui(f"unban {me} in the panel", unban):
            s, r = signin()
            if check("she signs in again", s == 200 and isinstance(r, dict) and r.get("access_token"), f"{s} {code(r)}"):
                tok = r["access_token"]
                alice = on_irc("and connects to IRC again", tok)

        # D. remote voice off (the voice token is refused), then on again
        def voice_token():
            return req(REMOTE_AUTH, "POST", "/v1/voice/token", {"rooms": []}, token=tok)
        def voice(on):
            open_federation()
            page.get_by_text("Remote users can join voice").click()
            save()
            if not on: shot("4-remote-voice-off")
        if ui("remote voice off in the panel", lambda: voice(False)):
            check("config/boson.yaml: remote_voice: false", "remote_voice: false" in remote_config())
            wait("her voice token is refused: 403 remote_voice_disabled",
                 lambda: (lambda r: (r[0] == 403 and code(r[1]) == "remote_voice_disabled", r))(voice_token()), timeout=60)
            if ui("remote voice back on in the panel", lambda: voice(True)):
                wait("she gets a voice token again", lambda: (lambda r: (r[0] == 200, r[0]))(voice_token()), timeout=60)

        # E. block home.test: Ergo drops her at once, she can't sign in or
        # reconnect; unblock lets her back
        def block():
            open_federation()
            row.get_by_role("button", name="Block server").click()
            dlg = page.get_by_role("dialog", name=f"Block {HOME}?")
            shot("5-block-confirm")
            dlg.get_by_role("button", name="Block and end sessions").click()
            toast(f"{HOME} is blocked")
            expect(row.get_by_text("Server blocked", exact=True)).to_be_visible()
            shot("6-blocked")
        if ui(f"block {HOME} in the panel", block):
            # auth >= 1.26.0: the domain's accounts are in the removed-accounts
            # feed as disabled (the helper disconnects them) and its IRC
            # lookup says disabled while blocked (the auth-script refuses
            # them; needs IRC_ROLES=true, which run.sh sets)
            check("Ergo drops her within seconds", alice.closed.wait(15), alice.lines[-5:])
            check("bob sees her quit", bob.wait(rf":{re.escape(handle)}!\S+ QUIT") is not None, bob.lines[-5:])
            again = IRC(handle, "OAUTHBEARER", oauthbearer(handle, tok))
            o = again.outcome()
            check("an IRC reconnect with her token is refused", o in ("sasl-failed", "closed"), f"{o} {again.lines[-5:]}")
            again.close()
            wait(f"a new sign-in: 403 domain_blocked",
                 lambda: (lambda r: (r[0] == 403 and code(r[1]) == "domain_blocked", r))(signin()), timeout=30)
            def unblock():
                row.get_by_role("button", name=f"Unblock {HOME}").click()
                toast(f"{HOME} is off the blocklist.")
                expect(page.get_by_role("list", name="blocklist").get_by_text(HOME)).to_have_count(0)
                # The row's badge comes from auth, which reads the file
                # within SETTINGS_POLL_SECONDS: reload until it's gone.
                for _ in range(15):
                    if page.get_by_text("Server blocked", exact=True).count() == 0: break
                    page.wait_for_timeout(1000); open_federation(); expect(row).to_be_visible()
                expect(page.get_by_text("Server blocked", exact=True)).to_have_count(0)
                shot("7-unblocked")
            if ui(f"unblock {HOME} in the panel", unblock):
                def back_in():
                    s, r = signin()
                    return s == 200, (s, code(r))
                if wait("she signs in again", back_in, timeout=30):
                    s, r = signin()
                    alice.close()
                    alice = on_irc("and connects to IRC again", r["access_token"])

        # F. mode Off in the panel: boson.json follows, sign-ins are refused
        def mode_off():
            open_federation()
            page.get_by_role("radio", name=re.compile(r"^Off")).check()
            save()
            shot("8-mode-off")
        if ui("mode Off in the panel", mode_off):
            check("config/boson.yaml: auth.federation.mode: off", re.search(r"\n  federation:\n(    .*\n)*?    mode: [\"']?off[\"']?\n", remote_config()) is not None, remote_config().split("federation:")[-1][:200])
            wait(f"https://{REMOTE}/.well-known/boson.json: accepts off (WELLKNOWN_REFRESH_SECONDS)",
                 lambda: (lambda r: (r[0] == 200 and (r[1].get("federation") or {}).get("accepts") == "off", r))(req(REMOTE, "GET", "/.well-known/boson.json")),
                 timeout=30, every=1)
            wait("a sign-in: 403 federation_off",
                 lambda: (lambda r: (r[0] == 403 and code(r[1]) == "federation_off", r))(signin()), timeout=30)
        browser.close()
    alice.close(); bob.close()

if args.phase == "untrusted": phase_untrusted()
elif args.phase == "main": phase_main()
else: phase_ui()
finish()
