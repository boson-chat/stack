#!/usr/bin/env python3
"""Smoke test for nick claims (docs/nick-claim.md) against a running local
stack (scripts/setup.sh --local, profiles auth,api) with a POP3 Mailpit next
to it (test/nickclaim/compose.mailpit.yaml) and, in .env,
NICK_CLAIM_POP3_PASSWORD=smoke-pop3-password (read when the api starts).

1. Default config: http://api:3001/internal/config/status (from the compose
   network) says nick_claim.enabled false, worker stopped; `api
   export-config` prints nick_claim with enabled: false; the internal port
   isn't routed by Traefik.
2. Signed in (auth /v1/register + /v1/login; the api takes auth's tokens):
   POST https://API_HOST/me/nick-claims -> 403 nick_claim_disabled.
3. Hot reload: writes a nick_claim section (enabled, inbound -> mailpit:1110,
   no TLS) into config/boson.yaml the way a hand edit does (temp file +
   rename); the status shows enabled + the worker running, no error.
4. Signed in: a claim -> 201 with a reg+...@<email_domain> address; a
   NickServ confirmation mailed to it (Mailpit SMTP) is picked up from POP3
   and GET /me/nick-claims/<id> returns its code.
5. Puts config/boson.yaml back; the status goes back to disabled.

An api without the feature (no internal status listener, or no nick_claim
in it: older than the nick_claim release) is skipped: exit 0 with a SKIP
line. --require turns that into a failure. If the smoke can't sign in to
the api (401), steps 2 and 4 are skipped with a note; the status checks
still run.

  scripts/smoke-nick-claim.py [--require] [--addr 127.0.0.1] [--ca local-certs/ca.pem]
"""
import base64
import argparse, http.client, json, os, re, secrets, smtplib, socket, ssl, subprocess, sys, tempfile, time
from email.message import EmailMessage

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1", help="where the stack listens")
p.add_argument("--auth-host", default="auth.localhost")
p.add_argument("--api-host", default="api.localhost")
p.add_argument("--ca", default=os.path.join(ROOT, "local-certs", "ca.pem"),
               help="CA to verify TLS with (default: the local CA); 'none' skips verification")
p.add_argument("--config-dir", default=os.path.join(ROOT, "config"))
p.add_argument("--api-internal", default="http://api:3001", help="the api's internal listener, from the compose network")
p.add_argument("--network", help="the compose network (default: the api's)")
p.add_argument("--pop3-host", default="mailpit")
p.add_argument("--pop3-port", type=int, default=1110)
p.add_argument("--pop3-user", default="reg@smoke.test")
p.add_argument("--email-domain", default="smoke.test")
p.add_argument("--smtp", default="127.0.0.1:1025", help="Mailpit's SMTP from here; 'none' skips the mail round trip")
p.add_argument("--require", action="store_true", help="fail (not skip) when the api has no nick_claim support")
p.add_argument("--timeout", type=int, default=45, help="seconds to wait for the api to reload / capture")
args = p.parse_args()

if args.ca == "none":
    ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
else:
    ctx = ssl.create_default_context(cafile=args.ca)

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail != "" else ""))
    if not ok: failures.append(name)
    return ok

def skip(why):
    if args.require:
        check(why, False, "--require")
        return
    print("SKIP " + why)

def envval(name):
    try:
        for line in reversed(open(os.path.join(ROOT, ".env")).read().splitlines()):
            if line.startswith(name + "="):
                return line.split("=", 1)[1].strip().strip("'\"")
    except OSError:
        pass
    return ""

class Conn(http.client.HTTPSConnection):
    """HTTPS to --addr with SNI/Host = the given host (no DNS for *.localhost needed)."""
    def connect(self):
        raw = socket.create_connection((args.addr, self.port), self.timeout)
        self.sock = self._context.wrap_socket(raw, server_hostname=self.host)

def https(host, method, path, body=None, token=None):
    """https://<host><path> -> (status, json|bytes)."""
    c = Conn(host, 443, context=ctx, timeout=15)
    hdrs = {"User-Agent": "boson-stack-smoke"}
    data = None
    if body is not None:
        data = json.dumps(body).encode(); hdrs["Content-Type"] = "application/json"
    if token: hdrs["Authorization"] = f"Bearer {token}"
    c.request(method, path, body=data, headers=hdrs)
    r = c.getresponse(); raw = r.read(); c.close()
    try: return r.status, json.loads(raw or b"null")
    except ValueError: return r.status, raw

def docker(*a, stdin=None):
    return subprocess.run(["docker", *a], input=stdin, capture_output=True, check=False)

net = args.network
if not net:
    cid = subprocess.run(["docker", "compose", "ps", "-q", "api"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    net = docker("inspect", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", cid).stdout.decode().split()[0] if cid else ""
    if not net: sys.exit("can't find the api's network (is the stack up with the api profile? or pass --network)")

INTERNAL_TOKEN = envval("API_INTERNAL_TOKEN")

def status():
    """GET <api-internal>/internal/config/status from inside the compose network -> (code, json|text)."""
    hdr = []
    if INTERNAL_TOKEN:
        hdr = ["-H", f"Authorization: Bearer {INTERNAL_TOKEN}", "-H", f"X-Internal-Token: {INTERNAL_TOKEN}"]
    r = docker("run", "--rm", "--network", net, "curlimages/curl:8.22.0", "-sS", "-m", "10",
               "-o", "-", "-w", "\n%{http_code}", *hdr, args.api_internal + "/internal/config/status")
    out = r.stdout.decode(errors="replace")
    payload, _, code = out.rpartition("\n")
    try: code = int(code)
    except ValueError: code = 0
    try: return code, json.loads(payload)
    except ValueError: return code, (payload + r.stderr.decode(errors="replace")).strip()

def nick(d):
    return d.get("nick_claim") if isinstance(d, dict) and isinstance(d.get("nick_claim"), dict) else None

def worker_state(nc):
    w = nc.get("worker")
    if isinstance(w, dict): w = w.get("state") or ("running" if w.get("running") else "stopped")
    return w

def wait_status(pred):
    deadline, last = time.time() + args.timeout, None
    while time.time() < deadline:
        code, d = status(); last = (code, d)
        if code == 200 and nick(d) is not None and pred(d):
            return True, last
        time.sleep(1)
    return False, last

# ---- writing into config/ (owned by uid 65532: sudo when we can't write) ----
CONF = os.path.join(args.config_dir, "boson.yaml")

def put(path, data):
    """Atomic write (temp file + rename, like an editor or the admin panel)."""
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
if re.search(rb"^nick_claim:", original, re.M):
    sys.exit("config/boson.yaml already has nick_claim:; run this on a fresh install")

def enabled_text():
    sec = ("nick_claim:\n"
           "  enabled: true\n"
           f"  email_domain: {args.email_domain}\n"
           "  rate_limit_per_hour: 5\n"
           "  inbound:\n"
           "    protocol: pop3\n"
           f"    host: {args.pop3_host}\n"
           f"    port: {args.pop3_port}\n"
           "    tls: false\n"
           f"    user: {json.dumps(args.pop3_user)}\n")
    return re.sub(r"^(version:.*\n)", lambda m: m.group(1) + sec, original.decode(), count=1, flags=re.M).encode()

tag = secrets.token_hex(3)

def sign_in():
    """A throwaway auth account -> access token (None if auth refuses)."""
    user, pw = f"nicksmoke{tag}", secrets.token_urlsafe(18)
    st, r = https(args.auth_host, "POST", "/v1/register", {"username": user, "email": f"{user}@example.com", "password": pw})
    if st != 201: print("register:", st, r); return None
    st, r = https(args.auth_host, "POST", "/v1/login", {"login": user, "password": pw})
    if st != 200 or not isinstance(r, dict): print("login:", st, r); return None
    tok = r.get("access_token")
    # The api keys claims to the Boson profile (users row), which a real
    # user creates at handle setup — POST /me. The secret is opaque to the api.
    if tok:
        st, r = https(args.api_host, "POST", "/me", {"handle": f"nicksmoke{tag}",
                      "encrypted_user_secret": base64.b64encode(secrets.token_bytes(48)).decode()}, tok)
        if st not in (200, 201, 409): print("profile:", st, r)
    return tok

def claim(tok):
    return https(args.api_host, "POST", "/me/nick-claims", {"server_id": "irc.smoke.test", "account_nick": f"smoke{tag}"}, tok)

def run():
    # 1. defaults
    code, d = status()
    if code in (0, 404) or not isinstance(d, dict):
        return skip(f"the api has no {args.api_internal}/internal/config/status (older than the nick_claim release): {code} {str(d)[:200]}")
    print("status:", json.dumps(d))
    if nick(d) is None:
        return skip("the api's config status has no nick_claim (older than the nick_claim release)")
    check("config status ok", d.get("ok") is True, d.get("error", ""))
    nc = nick(d)
    check("default: nick_claim disabled", nc.get("enabled") is False, nc)
    check("default: worker not running", worker_state(nc) in ("stopped", "disabled", None), nc)

    r = subprocess.run(["docker", "compose", "run", "--rm", "--no-deps", "-T", "api", "export-config"],
                       cwd=ROOT, capture_output=True, text=True)
    out = r.stdout
    if check("api export-config prints nick_claim", r.returncode == 0 and re.search(r"^nick_claim:", out, re.M) is not None,
             (out or r.stderr)[-300:]):
        check("export-config: enabled: false", re.search(r"^\s+enabled:\s*false\b", out, re.M) is not None, out[-300:])

    st, _ = https(args.api_host, "GET", "/internal/config/status")
    check("internal status isn't routed by Traefik", st != 200, st)

    # 2. disabled -> 403
    tok = sign_in()
    st, r = claim(tok) if tok else (0, None)
    signed_in = tok is not None and st != 401
    if not signed_in:
        print(f"NOTE can't sign in to the api ({st} {r}): the /me/nick-claims checks are skipped, status only")
    else:
        check("disabled: POST /me/nick-claims -> 403 nick_claim_disabled",
              st == 403 and isinstance(r, dict) and "nick_claim_disabled" in (r.get("error"), r.get("kind"), r.get("code")), f"{st} {r}")

    # 3. hot reload: on, against Mailpit POP3
    put(CONF, enabled_text())
    print(f"wrote nick_claim (enabled, {args.pop3_host}:{args.pop3_port}, no TLS) into config/boson.yaml")
    ok, last = wait_status(lambda d: d.get("ok") and nick(d).get("enabled") is True and worker_state(nick(d)) == "running"
                           and not nick(d).get("last_error"))
    if not check(f"enabled: worker running against Mailpit POP3 within {args.timeout}s", ok, last):
        return
    print("status:", json.dumps(last[1]))

    # 4. a claim, a confirmation mail, the code back
    if signed_in:
        st, r = claim(tok)
        email = r.get("email", "") if isinstance(r, dict) else ""
        check("enabled: POST /me/nick-claims -> 201", st == 201 and bool(email), f"{st} {r}")
        check(f"address is reg+...@{args.email_domain}", email.startswith("reg+") and email.endswith("@" + args.email_domain), email)
        if st == 201 and email and args.smtp != "none":
            code_ = "SMK" + secrets.token_hex(5)
            m = EmailMessage()
            m["From"], m["To"], m["Subject"] = "NickServ <services@irc.smoke.test>", email, f"smoke{tag} registration"
            m.set_content(f"Hi,\n\nTo complete your registration, type:\n/msg NickServ VERIFY REGISTER smoke{tag} {code_}\n")
            h, port = args.smtp.rsplit(":", 1)
            with smtplib.SMTP(h, int(port), timeout=10) as s: s.send_message(m)
            got, deadline = None, time.time() + args.timeout
            while time.time() < deadline:
                st2, r2 = https(args.api_host, "GET", f"/me/nick-claims/{r['id']}", token=tok)
                if st2 == 200 and isinstance(r2, dict) and r2.get("code"): got = r2; break
                time.sleep(1)
            check("confirmation code captured from POP3", bool(got) and got.get("code") == code_, got or f"{st2} {r2}")

def restore():
    put(CONF, original)
    ok, last = wait_status(lambda d: nick(d).get("enabled") is False)
    check("restored config/boson.yaml: nick_claim disabled again", ok, last)

try:
    run()
finally:
    try:
        if open(CONF, "rb").read() != original:
            restore()
    except Exception as e:  # never hide the test's own result
        print("cleanup failed:", e)

if failures:
    print(f"{len(failures)} failed: {', '.join(failures)}"); sys.exit(1)
print("nick-claim smoke: ok")
