#!/usr/bin/env python3
"""Smoke test for role channels (docs/roles.md "Role channels"): the local
stack (scripts/setup.sh --local, profiles irc + auth), RoleServ running.

Through the auth admin API (an Owner made with auth's CLI) and real IRC
clients over TLS:

1. A private channel is joinable by role members only: a guest-free user
   without the role gets 473 (+i), a member joins (account invite
   exception +I a:<account>, set by RoleServ through Ergo's API).
2. Auto-join on login (roles.channels.autojoin: true in config/boson.yaml):
   a member logging in is joined to the role's auto-join channel without
   sending JOIN.
3. Auto-join on role grant: a connected user given the role is joined.
4. rejoin: once: after a /part, the next login doesn't join again.
5. Network channels (irc.channels; auth with the feed's "network", else
   skipped): a fresh guest (no SASL) is joined to the auto-join channel on
   connect; a new account on its first login, and not again after /part.
6. A network channel restricted to a role (irc.channels[].roles; auth with
   "restricted" in the feed, else skipped): the guest and the new account
   aren't auto-joined to it, and a /join gets 473 saying which role it
   takes; a member of the role is auto-joined at login, and removed (KICK)
   when the role is taken away.

Puts config/boson.yaml back at the end. auth without the role channel API
(404) is skipped: exit 0 with a SKIP line; --require makes that a failure.

  scripts/smoke-role-channels.py [--require] [--addr 127.0.0.1] [--ca local-certs/ca.pem]
"""
import argparse, base64, http.client, json, os, queue, re, secrets, socket, ssl, subprocess, sys, tempfile, threading, time

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1", help="where the stack listens")
p.add_argument("--auth-host", default="auth.localhost")
p.add_argument("--irc-host", default="irc.localhost")
p.add_argument("--ca", default=os.path.join(ROOT, "local-certs", "ca.pem"),
               help="CA to verify TLS with (default: the local CA); 'none' skips verification")
p.add_argument("--config-dir", default=os.path.join(ROOT, "config"))
p.add_argument("--require", action="store_true", help="fail (not skip) when auth has no role channel API")
p.add_argument("--timeout", type=int, default=60, help="seconds to wait for auth + RoleServ to apply a change")
p.add_argument("--poll", type=int, default=10, help="RoleServ's ROLES_POLL_SECONDS")
args = p.parse_args()

if args.ca == "none":
    ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
else:
    ctx = ssl.create_default_context(cafile=args.ca)

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail != "" else ""), flush=True)
    if not ok: failures.append(name)
    return ok

class Conn(http.client.HTTPSConnection):
    def connect(self):
        raw = socket.create_connection((args.addr, self.port), self.timeout)
        self.sock = self._context.wrap_socket(raw, server_hostname=self.host)

def api(method, path, body=None, token=None):
    c = Conn(args.auth_host, 443, context=ctx, timeout=15)
    hdrs = {"User-Agent": "boson-stack-smoke"}
    data = None
    if body is not None:
        data = json.dumps(body).encode(); hdrs["Content-Type"] = "application/json"
    if token: hdrs["Authorization"] = "Bearer " + token
    c.request(method, path, body=data, headers=hdrs)
    r = c.getresponse(); raw = r.read(); c.close()
    try: return r.status, json.loads(raw or b"null")
    except ValueError: return r.status, raw.decode(errors="replace")

tag = secrets.token_hex(3)

def create_user(name, role="user"):
    pw = secrets.token_urlsafe(18)
    r = subprocess.run(["docker", "compose", "exec", "-T", "auth", "./auth", "user", "create", "--username", name,
                        "--email", f"{name}@example.com", "--role", role, "--password-stdin"],
                       cwd=ROOT, input=(pw + "\n").encode(), capture_output=True)
    if r.returncode != 0:
        sys.exit(f"auth user create {name}: {r.stdout.decode(errors='replace')} {r.stderr.decode(errors='replace')}")
    st, d = api("POST", "/v1/login", {"login": name, "password": pw})
    if st != 200: sys.exit(f"login {name}: {st} {d}")
    return pw, d["access_token"]

# ---- config/boson.yaml (owned by uid 65532: sudo when we can't write) ----
CONF = os.path.join(args.config_dir, "boson.yaml")

def put(path, data):
    d = os.path.dirname(path)
    if os.access(d, os.W_OK):
        tmp = path + ".smoke-tmp"
        with open(tmp, "wb") as f: f.write(data)
        os.chmod(tmp, 0o644); os.replace(tmp, path)
        return
    with tempfile.NamedTemporaryFile(delete=False) as f: f.write(data)
    subprocess.run(["sudo", "install", "-m", "644", "-o", "65532", "-g", "65532", f.name, path + ".smoke-tmp"], check=True)
    subprocess.run(["sudo", "mv", path + ".smoke-tmp", path], check=True)
    os.unlink(f.name)

original = open(CONF, "rb").read()
def set_roles(autojoin, rejoin):
    put(CONF, original.rstrip(b"\n") + f"\n\nroles:\n  channels:\n    autojoin: {str(autojoin).lower()}\n    rejoin: {rejoin}\n".encode())

def env_value(key):
    for line in open(os.path.join(ROOT, ".env")):
        if line.startswith(key + "="): return line.split("=", 1)[1].strip()
    return ""

def feed():
    """GET auth's /internal/v1/irc/channel-access from the compose network."""
    cid = subprocess.run(["docker", "compose", "ps", "-q", "auth"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    net = subprocess.run(["docker", "inspect", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", cid],
                         capture_output=True, text=True).stdout.split()[0]
    r = subprocess.run(["docker", "run", "--rm", "--network", net, "busybox:1.37.0", "wget", "-qO-", "-T", "10",
                        "--header", "X-Internal-Token: " + env_value("INTERNAL_API_TOKEN"),
                        "http://auth:8081/internal/v1/irc/channel-access"], capture_output=True, text=True)
    try: return json.loads(r.stdout)
    except ValueError: return {}

def wait_settings(autojoin, rejoin):
    """Until auth serves the settings, then one RoleServ poll (ROLES_POLL_SECONDS, 10 s) more."""
    end = time.time() + args.timeout
    while time.time() < end:
        f = feed()
        if f.get("autojoin") == autojoin and f.get("rejoin") == rejoin:
            time.sleep(args.poll + 3)
            return True
        time.sleep(2)
    return False

# ---- IRC ----
class Client:
    """A logged-in IRC client (SASL PLAIN; pw None: a guest) that records what it sees."""
    def __init__(self, user, pw):
        self.user, self.lines, self.q = user, [], queue.Queue()
        raw = socket.create_connection((args.addr, 6697), timeout=30)
        self.s = ctx.wrap_socket(raw, server_hostname=args.irc_host)
        self.s.settimeout(None)  # idle clients stay (Ergo PINGs them)
        self.f = self.s.makefile("rb")
        self.send("CAP LS 302"); self.send(f"NICK {user}"); self.send(f"USER {user} 0 * :role channels smoke")
        self.registered = threading.Event()
        threading.Thread(target=self.read, args=(pw,), daemon=True).start()
        if not self.registered.wait(30):
            raise RuntimeError(f"{user}: no welcome: {self.lines[-5:]}")

    def send(self, l):
        self.s.sendall((l + "\r\n").encode())

    def read(self, pw):
        while True:
            try: line = self.f.readline().decode(errors="replace").rstrip("\r\n")
            except (OSError, ValueError): return
            if not line: return
            self.lines.append(line)
            parts = line.split()
            if parts[0] == "PING": self.send("PONG " + parts[1]); continue
            cmd = parts[1] if len(parts) > 1 else ""
            if cmd == "CAP" and len(parts) > 4 and parts[3] == "LS" and parts[4] != "*":
                self.send("CAP REQ :sasl" if pw is not None else "CAP END")
            elif cmd == "CAP" and len(parts) > 3 and parts[3] == "ACK":
                self.send("AUTHENTICATE PLAIN")
            elif parts[0] == "AUTHENTICATE" and parts[1] == "+":
                self.send("AUTHENTICATE " + base64.b64encode(f"\0{self.user}\0{pw}".encode()).decode())
            elif cmd == "903":
                self.send("CAP END")
            elif cmd in ("904", "905"):
                self.send("QUIT :sasl failed")
            elif cmd == "001":
                self.registered.set()
            self.q.put(line)

    def wait(self, pattern, timeout):
        """Wait for a line matching pattern (regex) seen after now or already queued."""
        rx = re.compile(pattern)
        end = time.time() + timeout
        while time.time() < end:
            try: line = self.q.get(timeout=max(0.1, end - time.time()))
            except queue.Empty: break
            if rx.search(line): return line
        return None

    def drain(self):
        while not self.q.empty(): self.q.get_nowait()

    def close(self):
        try: self.send("QUIT :bye")
        except OSError: pass
        self.s.close()

def joined(chan):
    # ":nick!user@host JOIN #chan" (or with extended-join params)
    return r"^(@\S+ )?:\S+ JOIN :?" + re.escape(chan) + r"(\s|$)"

def run():
    _, owner = create_user(f"rcowner{tag}", "owner")
    st, _ = api("GET", "/v1/admin/channel-access", token=owner)
    if st == 404:
        if args.require: check("auth has the role channel API", False, "404 with --require")
        else: print("SKIP auth has no /v1/admin/channel-access (older than the role channels release)")
        return
    check("auth has the role channel API", st == 200, st)

    private, aj = f"#rc-staff-{tag}", f"#rc-aj-{tag}"
    st, r = api("POST", "/v1/admin/roles", {"name": f"RC Staff {tag}", "position": 50}, owner)
    if not check("create role", st == 201, f"{st} {r}"): return
    role = r["role"]["id"]
    st, r = api("PUT", f"/v1/admin/roles/{role}/channels",
                {"channels": [{"channel": private, "autojoin": False}, {"channel": aj, "autojoin": True}]}, owner)
    check("role channels", st == 200, f"{st} {r}")

    alice_pw, _ = create_user(f"rcalice{tag}")
    bob_pw, _ = create_user(f"rcbob{tag}")
    carol_pw, _ = create_user(f"rccarol{tag}")
    users = {}
    for n in ("alice", "bob", "carol"):
        st, r = api("GET", f"/v1/admin/users?q=rc{n}{tag}", token=owner)
        users[n] = r["users"][0]["id"]
    st, r = api("PUT", f"/v1/admin/users/{users['alice']}/roles", {"role_ids": [role]}, owner)
    check("alice gets the role", st == 200, st)

    # 1. private channel: carol (no role) creates it, then it's marked private.
    carol = Client(f"rccarol{tag}", carol_pw)
    carol.send("JOIN " + private)
    check(f"carol creates {private}", bool(carol.wait(joined(private), 15)))
    st, r = api("PUT", "/v1/admin/channel-access", {"channels": [{"channel": private, "private": True}]}, owner)
    check("mark private", st == 200, f"{st} {r}")
    locked = carol.wait(r"MODE " + re.escape(private) + r" \+[a-zA-Z]*i", args.timeout)
    check("RoleServ makes it +i", bool(locked), locked or "no MODE +i")
    invex = carol.wait(r"MODE " + re.escape(private) + r" .*a:rcalice" + tag, 15) or \
        any(re.search(r"a:rcalice" + tag, l) for l in carol.lines)
    check("RoleServ adds +I a:alice", bool(invex))

    bob = Client(f"rcbob{tag}", bob_pw)
    bob.send("JOIN " + private)
    denied = bob.wait(r" 473 \S+ " + re.escape(private), 15)
    check("bob (no role) can't join: 473", bool(denied), denied or bob.lines[-3:])

    # 2. auto-join on login (rejoin: login)
    set_roles(True, "login")
    check("auth serves autojoin: true, rejoin: login", wait_settings(True, "login"))
    alice = Client(f"rcalice{tag}", alice_pw)
    check(f"auto-join on login: alice is joined to {aj}", bool(alice.wait(joined(aj), 20)), alice.lines[-3:])
    alice.send("JOIN " + private)
    check(f"alice (role member) joins private {private}", bool(alice.wait(joined(private), 15)), alice.lines[-3:])

    # 3. auto-join on role grant: bob is connected.
    bob.drain()
    st, _ = api("PUT", f"/v1/admin/users/{users['bob']}/roles", {"role_ids": [role]}, owner)
    check("bob gets the role", st == 200, st)
    check(f"auto-join on role grant: bob is joined to {aj}", bool(bob.wait(joined(aj), args.timeout)), bob.lines[-3:])
    bob.send("JOIN " + private)
    check("bob can join the private channel now", bool(bob.wait(joined(private), args.timeout)), bob.lines[-3:])

    # 4. rejoin: once: dave gets the role, logs in (joined), parts, logs in again (not joined).
    set_roles(True, "once")
    dave_pw, _ = create_user(f"rcdave{tag}")
    st, r = api("GET", f"/v1/admin/users?q=rcdave{tag}", token=owner)
    st, _ = api("PUT", f"/v1/admin/users/{r['users'][0]['id']}/roles", {"role_ids": [role]}, owner)
    check("auth serves rejoin: once", wait_settings(True, "once"))
    dave = Client(f"rcdave{tag}", dave_pw)
    check("once: first login joins", bool(dave.wait(joined(aj), 20)), dave.lines[-3:])
    dave.send("PART " + aj + " :bye")
    dave.wait(r" PART " + re.escape(aj), 10)
    dave.close()
    time.sleep(2)  # RoleServ records the join right after it
    again = Client(f"rcdave{tag}", dave_pw)
    rejoined = again.wait(joined(aj), 15)
    check("once: after /part, the next login doesn't rejoin", rejoined is None, rejoined or "")
    for c in (carol, bob, alice, again):
        c.close()
    network_channels(owner, role, f"RC Staff {tag}", users["alice"], alice_pw)

def network_channels(owner, role, role_name, alice_id, alice_pw):
    lobby, opers = f"#nc-lobby-{tag}", f"#nc-opers-{tag}"
    entry = (f"  channels:\n    - {{name: \"{lobby}\", topic: smoke, autojoin: true}}\n"
             f"    - {{name: \"{opers}\", autojoin: true, roles: [\"{role_name}\"]}}\n").encode()
    if re.search(rb"^irc:[ \t]*(#.*)?$", original, re.M):  # an irc: block already: add the key to it
        put(CONF, re.sub(rb"^irc:[ \t]*(#.*)?\n", lambda m: m.group(0) + entry, original, count=1, flags=re.M))
    else:
        put(CONF, original.rstrip(b"\n") + b"\n\nirc:\n" + entry)
    end, f = time.time() + args.timeout, {}
    while time.time() < end:
        f = feed()
        if "network" not in f: break
        if any(c.get("channel") == lobby for c in f["network"].get("channels") or []): break
        time.sleep(2)
    if "network" not in f:
        if args.require: check("auth serves network channels", False, "no network in the feed with --require")
        else: print("SKIP network channels: auth's feed has no network channels (older than the network channels release)")
        return
    if not check("auth serves the network channel", any(c.get("channel") == lobby for c in f["network"].get("channels") or []), f.get("network")):
        return
    restricted = next((c for c in f["network"]["channels"] if c.get("channel") == opers), {})
    roles_on = "restricted" in restricted
    if roles_on:
        check("auth serves the restricted channel", restricted.get("restricted") is True and restricted.get("roles") == [role_name], restricted)
    elif args.require:
        check("auth serves restricted network channels", False, "no restricted in the feed with --require")
    else:
        print("SKIP restricted network channels: auth's feed has no \"restricted\" (older than the channel roles release)")
    time.sleep(args.poll + 3)  # RoleServ ensures it
    guest = Client(f"ncguest{tag}", None)
    got = guest.wait(joined(lobby), 15)
    check("network channels: a fresh guest is joined on connect", got is not None, guest.lines[-5:])
    if roles_on:
        check("restricted: the guest isn't joined to it", not any(re.search(joined(opers), l) for l in guest.lines), guest.lines[-5:])
    guest.close()
    pw, _ = create_user(f"ncuser{tag}")
    u = Client(f"ncuser{tag}", pw)
    got = u.wait(joined(lobby), 15)
    check("network channels: a new account is joined on its first login", got is not None, u.lines[-5:])
    if roles_on:
        time.sleep(2)
        check("restricted: an account without the role isn't auto-joined", not any(re.search(joined(opers), l) for l in u.lines), u.lines[-5:])
        u.send("JOIN " + opers)
        denied = u.wait(r" 473 \S+ " + re.escape(opers) + r" :.*" + re.escape(role_name), 15)
        check("restricted: /join without the role gets 473 naming the role", bool(denied), denied or u.lines[-3:])
    u.send("PART " + lobby); u.wait(r" PART " + re.escape(lobby), 10); u.close()
    time.sleep(2)
    u = Client(f"ncuser{tag}", pw)
    again = u.wait(joined(lobby), 12)
    check("network channels: not joined again after /part", again is None, again or "")
    u.close()
    if not roles_on:
        return
    # alice has the role: auto-joined at login; losing the role removes her.
    alice = Client(f"rcalice{tag}", alice_pw)
    got = alice.wait(joined(opers), 20)
    check("restricted: a member of the role is auto-joined at login", got is not None, alice.lines[-5:])
    alice.drain()
    st, _ = api("PUT", f"/v1/admin/users/{alice_id}/roles", {"role_ids": []}, owner)
    check("alice loses the role", st == 200, st)
    kicked = alice.wait(r" KICK " + re.escape(opers) + r" rcalice" + tag + r" :.*" + re.escape(role_name), args.timeout)
    check("restricted: losing the role removes her (KICK naming the role)", bool(kicked), kicked or alice.lines[-5:])
    alice.send("JOIN " + opers)
    denied = alice.wait(r" 473 \S+ " + re.escape(opers), 15)
    check("restricted: and she can't rejoin", bool(denied), denied or alice.lines[-3:])
    alice.close()

try:
    run()
finally:
    put(CONF, original)
print("RESULT", "ok" if not failures else "failed: " + ", ".join(failures))
sys.exit(1 if failures else 0)
