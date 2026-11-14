#!/usr/bin/env python3
"""Smoke test for the local stack (scripts/setup.sh --local, profiles irc + auth).

1. Traefik serves https://AUTH_HOST with the local CA's certificate.
2. Register a user through the auth service REST API, and log in.
3. IRC over TLS (also verified against the local CA): SASL PLAIN as that
   user gets RPL_LOGGEDIN (900) + RPL_SASLSUCCESS (903) and the welcome (001).
4. SASL PLAIN with a wrong password fails (904).
5. SASL OAUTHBEARER with the user's access token logs in too.
6. With --channel-creation: the logged-in user creating a new channel works
   (registered) or is refused with 403 (admins).

  scripts/smoke-irc.py [--addr 127.0.0.1] [--ca local-certs/ca.pem]
  scripts/smoke-irc.py --print-voice-token   # also print a voice token (for examples/bot)
  scripts/smoke-irc.py --channel-creation admins
"""
import argparse, base64, json, os, secrets, socket, ssl, sys, time

p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1", help="where the stack listens")
p.add_argument("--irc-host", default="irc.localhost")
p.add_argument("--auth-host", default="auth.localhost")
p.add_argument("--ca", default=os.path.join(os.path.dirname(__file__), "..", "local-certs", "ca.pem"),
               help="CA to verify TLS with (default: the local CA); 'none' skips verification")
p.add_argument("--print-voice-token", action="store_true")
p.add_argument("--channel-creation", choices=["registered", "admins"],
               help="check that a logged-in user can (registered) or can't (admins) create a channel")
args = p.parse_args()

if args.ca == "none":
    ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
else:
    ctx = ssl.create_default_context(cafile=args.ca)

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + detail if detail else ""))
    if not ok: failures.append(name)

def https(method, path, body=None, token=None):
    """Minimal HTTPS client that connects to --addr but sends SNI/Host = auth host."""
    raw = socket.create_connection((args.addr, 443), timeout=15)
    s = ctx.wrap_socket(raw, server_hostname=args.auth_host)
    data = json.dumps(body).encode() if body is not None else b""
    hdrs = [f"{method} {path} HTTP/1.1", f"Host: {args.auth_host}", "Connection: close",
            "Accept: application/json", f"Content-Length: {len(data)}"]
    if body is not None: hdrs.append("Content-Type: application/json")
    if token: hdrs.append(f"Authorization: Bearer {token}")
    s.sendall(("\r\n".join(hdrs) + "\r\n\r\n").encode() + data)
    resp = b""
    while True:
        chunk = s.recv(65536)
        if not chunk: break
        resp += chunk
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

# 1. auth over Traefik
deadline = time.time() + 90
while True:
    try:
        st, _ = https("GET", "/readyz");
        if st == 200: break
    except (OSError, ssl.SSLError) as e:
        err = e
    if time.time() > deadline:
        check("auth ready via Traefik", False, str(locals().get("err", "not ready"))); sys.exit(1)
    time.sleep(2)
check("auth ready via Traefik (TLS verified)" if args.ca != "none" else "auth ready via Traefik", True)

# 2. register + login
user = "smoke" + secrets.token_hex(3)
password = secrets.token_urlsafe(18)
st, r = https("POST", "/v1/register", {"username": user, "email": f"{user}@example.com", "password": password})
check("register", st == 201, f"{st} {r if st != 201 else user}")
st, r = https("POST", "/v1/login", {"login": user, "password": password})
check("login", st == 200 and isinstance(r, dict) and "access_token" in r, str(st))
access = r.get("access_token") if isinstance(r, dict) else None

def irc_session(mech, payload, nick, join=None):
    """Connect, try SASL (and JOIN join after the welcome), return (numerics seen, lines)."""
    raw = socket.create_connection((args.addr, 6697), timeout=20)
    s = ctx.wrap_socket(raw, server_hostname=args.irc_host)
    f = s.makefile("rb")
    send = lambda l: s.sendall((l + "\r\n").encode())
    send("CAP LS 302"); send(f"NICK {nick}"); send(f"USER {nick} 0 * :stack smoke test")
    seen, lines = set(), []
    end = time.time() + 25
    while time.time() < end:
        try:
            line = f.readline().decode(errors="replace").rstrip("\r\n")
        except (socket.timeout, TimeoutError):
            break
        if not line: break
        lines.append(line)
        parts = line.split()
        if parts[0] == "PING": send("PONG " + parts[1]); continue
        cmd = parts[1] if len(parts) > 1 else ""
        seen.add(cmd)
        if cmd == "CAP" and len(parts) > 4 and parts[3] == "LS" and parts[4] != "*":
            send("CAP REQ :sasl")  # after the last line of a multi-line CAP LS
        elif cmd == "CAP" and len(parts) > 3 and parts[3] == "ACK":
            send(f"AUTHENTICATE {mech}")
        elif parts[0] == "AUTHENTICATE" and parts[1] == "+":
            b = base64.b64encode(payload).decode()
            for i in range(0, len(b), 400): send("AUTHENTICATE " + b[i:i + 400])
            if len(b) % 400 == 0: send("AUTHENTICATE +")
        elif cmd == "903":
            send("CAP END")
        elif cmd in ("904", "905", "906", "908"):
            send("QUIT :sasl failed")
        elif cmd == "001" and join:
            send("JOIN " + join)
        elif cmd == "001" or (join and (cmd in ("JOIN", "403", "405", "471", "473", "474", "475", "477"))):
            send("QUIT :done")
    s.close()
    return seen, lines

# 3. SASL PLAIN, right password
seen, lines = irc_session("PLAIN", f"\0{user}\0{password}".encode(), user)
logged = [l for l in lines if " 900 " in l]
check("SASL PLAIN: RPL_LOGGEDIN 900", "900" in seen and "903" in seen, logged[0][:120] if logged else " | ".join(lines[-3:]))
check("welcome 001", "001" in seen)

# 4. wrong password
seen, lines = irc_session("PLAIN", f"\0{user}\0wrong-{password}".encode(), user)
check("SASL PLAIN wrong password fails (904)", "904" in seen and "900" not in seen)

# 5. OAUTHBEARER with the access token
if access:
    gs2 = f"n,a={user},\x01auth=Bearer {access}\x01\x01".encode()
    seen, lines = irc_session("OAUTHBEARER", gs2, user)
    check("SASL OAUTHBEARER (auth access token)", "900" in seen and "903" in seen,
          "" if "900" in seen else " | ".join(lines[-3:]))

# 6. channel creation policy (channel_creation from the auth service)
if args.channel_creation:
    chan = "#smoke-" + secrets.token_hex(3)
    seen, lines = irc_session("PLAIN", f"\0{user}\0{password}".encode(), user, join=chan)
    if args.channel_creation == "registered":
        check(f"channel_creation=registered: user creates {chan}", "JOIN" in seen, " | ".join(lines[-2:]))
    else:
        denied = [l for l in lines if " 403 " in l]
        check(f"channel_creation=admins: user can't create {chan} (403)", bool(denied) and "JOIN" not in seen,
              denied[0] if denied else " | ".join(lines[-2:]))

if args.print_voice_token and access:
    st, r = https("POST", "/v1/voice/token", {"rooms": []}, token=access)
    check("voice token", st == 200, str(st))
    if st == 200: print("VOICE_TOKEN", r["token"])

print("RESULT", "ok" if not failures else "failed: " + ", ".join(failures))
sys.exit(1 if failures else 0)
