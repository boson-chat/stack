#!/usr/bin/env python3
"""Smoke test for network branding and email templates (docs/branding.md),
against a running local stack (scripts/setup.sh --local, profile auth) with
Mailpit next to it (test/branding/compose.mailpit.yaml).

1. Writes a `branding:` section, an SVG logo and a PNG sign-in background
   (config/branding/), and `auth.email` + `auth.smtp` (-> Mailpit) into
   config/boson.yaml, the way a hand edit does (temp file + rename).
2. GET https://AUTH_HOST/v1/branding: the theme with absolute asset URLs,
   CORS *, an ETag, 304 for If-None-Match; unknown keys dropped.
   /v1/config has branding_url.
3. The assets: right Content-Type, nosniff, a long cache, the SVG's CSP, the
   same bytes as on disk; no way out of config/branding/ (../boson.yaml).
4. Hot reload: a new accent in the file shows up, with a new ETag.
5. POST http://auth:8081/internal/email/preview as an admin (an Owner made
   with `auth user create`, signed in through /v1/login; the endpoints need
   settings.manage): the template's subject/body, variables filled in,
   unknown ones kept, HTML escaped, footer and accent in the HTML;
   subject/body overrides.
6. POST /internal/email/test: the mail arrives in Mailpit with the sender
   name, subject and footer.
Then puts config/boson.yaml back and removes the images it added (the admin
account stays; it's a throwaway stack).

An auth without these endpoints (older than the branding release) is
skipped: exit 0 with a SKIP line. --require turns that into a failure (CI
does that when it builds auth from source).

  scripts/smoke-branding.py [--require] [--addr 127.0.0.1] [--ca local-certs/ca.pem]

Run it against an unreleased auth by building it from a checkout:
  COMPOSE_FILE=compose.yaml:compose.auth-src.yaml AUTH_SRC=../auth docker compose up -d --build --wait
  docker compose -f compose.yaml -f test/branding/compose.mailpit.yaml up -d --no-deps --wait mailpit
  scripts/smoke-branding.py --require
"""
import argparse, http.client, json, os, re, secrets, socket, ssl, struct, subprocess, sys, tempfile, time, urllib.parse, urllib.request, zlib

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1", help="where the stack listens")
p.add_argument("--auth-host", default="auth.localhost")
p.add_argument("--ca", default=os.path.join(ROOT, "local-certs", "ca.pem"),
               help="CA to verify TLS with (default: the local CA); 'none' skips verification")
p.add_argument("--config-dir", default=os.path.join(ROOT, "config"))
p.add_argument("--auth-internal", default="http://auth:8081", help="auth's internal listener, from the compose network")
p.add_argument("--network", help="the compose network (default: auth's)")
p.add_argument("--mailpit", default="http://127.0.0.1:8025", help="Mailpit's API; 'none' skips the test send")
p.add_argument("--smtp-host", default="mailpit", help="auth.smtp.host to send the test mail through")
p.add_argument("--smtp-port", type=int, default=1025)
p.add_argument("--require", action="store_true", help="fail (not skip) when auth has no branding/email endpoints")
p.add_argument("--timeout", type=int, default=45, help="seconds to wait for auth to reload the file")
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

class Conn(http.client.HTTPSConnection):
    """HTTPS to --addr with SNI/Host = the auth host (no DNS for *.localhost needed)."""
    def connect(self):
        raw = socket.create_connection((args.addr, self.port), self.timeout)
        self.sock = self._context.wrap_socket(raw, server_hostname=self.host)

def public(path, headers=None, method="GET", body=None):
    """https://AUTH_HOST<path> -> (status, headers (lowercase keys), body bytes)."""
    c = Conn(args.auth_host, 443, context=ctx, timeout=15)
    hdrs = {"User-Agent": "boson-stack-smoke", "Origin": "https://smoke.example", **(headers or {})}
    data = None
    if body is not None:
        data = json.dumps(body).encode(); hdrs["Content-Type"] = "application/json"
    c.request(method, path, body=data, headers=hdrs)
    r = c.getresponse(); body = r.read(); c.close()
    return r.status, {k.lower(): v for k, v in r.getheaders()}, body

def jbody(b):
    try: return json.loads(b or b"null")
    except ValueError: return None

def docker(*a, stdin=None):
    return subprocess.run(["docker", *a], input=stdin, capture_output=True, check=False)

net = args.network
if not net:
    cid = subprocess.run(["docker", "compose", "ps", "-q", "auth"], cwd=ROOT, capture_output=True, text=True).stdout.strip()
    net = docker("inspect", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", cid).stdout.decode().split()[0] if cid else ""
    if not net: sys.exit("can't find auth's network (is the stack up? or pass --network)")

def internal(path, body, token=None):
    """POST <auth-internal><path> from inside the compose network -> (status, json|text)."""
    auth = ["-H", f"Authorization: Bearer {token}"] if token else []
    r = docker("run", "--rm", "-i", "--network", net, "curlimages/curl:8.22.0", "-sS", "-m", "20",
               "-o", "-", "-w", "\n%{http_code}", *auth,
               "-H", "Content-Type: application/json", "--data-binary", "@-", args.auth_internal + path,
               stdin=json.dumps(body).encode())
    out = r.stdout.decode(errors="replace")
    payload, _, code = out.rpartition("\n")
    try: code = int(code)
    except ValueError: return 0, out + r.stderr.decode(errors="replace")
    return code, (jbody(payload.encode()) if payload.strip().startswith("{") else payload)

# ---- writing into config/ (owned by uid 65532: sudo when we can't write) ----
CONF = os.path.join(args.config_dir, "boson.yaml")
BRAND = os.path.join(args.config_dir, "branding")

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

def remove(path):
    if os.access(os.path.dirname(path), os.W_OK): os.unlink(path)
    else: subprocess.run(["sudo", "rm", "-f", path], check=True)

def png(w, h, rgba):
    def chunk(t, d): return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
    raw = b"".join(b"\x00" + bytes(rgba) * w for _ in range(h))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 6, 0, 0, 0)) + \
        chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")

tag = secrets.token_hex(3)
LOGO, LOGIN = f"smoke-logo-{tag}.svg", f"smoke-login-{tag}.png"
logo_bytes = b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" rx="3" fill="#e11d48"/></svg>\n'
login_bytes = png(8, 8, (12, 10, 9, 255))
ACCENT1, ACCENT2 = "#e11d48", "#16a34a"
SENDER, FOOTER = f"Smoke Chat {tag}", f"Smoke Chat footer {tag}"
SUBJECT = "Welcome to {{network}} [smoke " + tag + "]"
BODY = "Hi {{username}},\n\n<b>bold</b> & {{not_a_variable}}\n\nGo: {{link}}"
TO = f"smoke-{tag}@example.com"

def branding_yaml(accent):
    return (f"branding:\n"
            f"  theme:\n"
            f"    base: dark\n"
            f"    colors: {{ accent: \"{accent}\", bg: \"#0c0a09\" }}\n"
            f"    radius: 4\n"
            f"    smoke_unknown_key: 1\n"
            f"    panes:\n"
            f"      channels: {{ bg: \"#1c1917\" }}\n"
            f"      messages: {{ dim: 0.5, blur: 4, position: top }}\n"
            f"  images:\n"
            f"    logo: {LOGO}\n"
            f"    login_background: {LOGIN}\n")

def auth_yaml():
    lines = ["  smtp:", f"    host: {args.smtp_host}", f"    port: {args.smtp_port}",
             f"    from: \"smoke@example.test\"", "    tls: none",
             "  email:", f"    sender_name: {json.dumps(SENDER)}", f"    accent: \"{ACCENT1}\"",
             f"    footer: {json.dumps(FOOTER)}", "    logo: true", "    templates:",
             "      verification:", f"        subject: {json.dumps(SUBJECT)}", f"        body: {json.dumps(BODY)}"]
    return "\n".join(lines) + "\n"

original = open(CONF, "rb").read()

def config_text(accent):
    text = original.decode()
    m = re.search(r"^auth:\s*\n((?:(?:[ #].*)?\n)*)", text, re.M)  # the auth: block, up to the next top-level key
    if m and re.search(r"^  (smtp|email):", m.group(1), re.M):
        sys.exit("config/boson.yaml already sets auth.smtp or auth.email; run this on a fresh install")
    if re.search(r"^branding:", text, re.M): sys.exit("config/boson.yaml already has branding:; run this on a fresh install")
    # branding right under `version:`, auth.smtp/auth.email under `auth:` (or a new auth:).
    text = re.sub(r"^(version:.*\n)", lambda m: m.group(1) + branding_yaml(accent), text, count=1, flags=re.M)
    if re.search(r"^auth:\s*$", text, re.M):
        text = re.sub(r"^(auth:\s*\n)", lambda m: m.group(1) + auth_yaml(), text, count=1, flags=re.M)
    else:
        text = re.sub(r"^(version:.*\n)", lambda m: m.group(1) + "auth:\n" + auth_yaml(), text, count=1, flags=re.M)
    return text.encode()

def wait_branding(accent, old_etag=None):
    """Poll /v1/branding until it serves `accent` -> (status, headers, doc)."""
    deadline, last = time.time() + args.timeout, None
    while time.time() < deadline:
        try:
            st, h, b = public("/v1/branding")
            d = jbody(b); last = (st, h, d)
            if st == 200 and isinstance(d, dict) and (d.get("colors") or {}).get("accent", "").lower() == accent:
                return last
        except (OSError, ssl.SSLError, http.client.HTTPException) as e:
            last = (0, {}, str(e))
        time.sleep(1)
    return last

def asset_path(url):
    u = urllib.parse.urlsplit(url)
    return u.path + ("?" + u.query if u.query else "")

def max_age(h):
    m = re.search(r"max-age=(\d+)", h.get("cache-control", ""))
    return int(m.group(1)) if m else 0

def admin_token():
    """An Owner account (auth's CLI in the auth container), signed in -> access token."""
    user, pw = f"smokeadmin{tag}", secrets.token_urlsafe(18)
    r = subprocess.run(["docker", "compose", "exec", "-T", "auth", "./auth", "user", "create", "--username", user,
                        "--email", f"{user}@example.com", "--role", "owner", "--password-stdin"],
                       cwd=ROOT, input=(pw + "\n").encode(), capture_output=True)
    if r.returncode != 0:
        print("auth user create:", r.stdout.decode(errors="replace"), r.stderr.decode(errors="replace"))
        return None
    st, _, b = public("/v1/login", method="POST", body={"login": user, "password": pw})
    d = jbody(b) or {}
    if st != 200: print("login:", st, d)
    return d.get("access_token")

def run():
    # 1. images + config
    put(os.path.join(BRAND, LOGO), logo_bytes)
    put(os.path.join(BRAND, LOGIN), login_bytes)
    put(CONF, config_text(ACCENT1))
    print(f"wrote branding (accent {ACCENT1}), {LOGO}, {LOGIN}, auth.email and auth.smtp -> {args.smtp_host}:{args.smtp_port}")

    # 2. /v1/branding
    st, h, doc = wait_branding(ACCENT1)
    if st == 404:
        return skip(f"auth has no GET /v1/branding (older than the branding release; 404 after {args.timeout}s)")
    if not check("GET /v1/branding serves the file's theme", st == 200 and isinstance(doc, dict)
                 and doc.get("colors", {}).get("accent", "").lower() == ACCENT1, st if isinstance(doc, dict) else f"{st} {doc}"):
        return
    print(json.dumps(doc))
    check("version 1, base dark, radius 4", doc.get("version") == 1 and doc.get("base") == "dark" and doc.get("radius") == 4)
    check("panes kept", (doc.get("panes") or {}).get("channels", {}).get("bg", "").lower() == "#1c1917"
          and (doc.get("panes") or {}).get("messages", {}).get("position") == "top", doc.get("panes"))
    check("unknown theme key dropped", "smoke_unknown_key" not in doc, list(doc))
    check("JSON + CORS *", h.get("content-type", "").startswith("application/json")
          and h.get("access-control-allow-origin") == "*", {k: h.get(k) for k in ("content-type", "access-control-allow-origin")})
    etag = h.get("etag", "")
    check("ETag", bool(etag), etag or h)
    base = f"https://{args.auth_host}/v1/branding/assets/"
    logo, login = doc.get("logo", ""), doc.get("login_background", "")
    check("logo is an absolute asset URL", logo.startswith(base) and LOGO in logo, logo)
    check("login_background is an absolute asset URL", login.startswith(base) and LOGIN in login, login)
    st2, _, b2 = public("/v1/branding", {"If-None-Match": etag})
    check("If-None-Match -> 304", st2 == 304 and not b2, st2)
    st3, h3, b3 = public("/v1/config")
    cfg = jbody(b3) or {}
    check("/v1/config branding_url", st3 == 200 and cfg.get("branding_url") == f"https://{args.auth_host}/v1/branding",
          cfg.get("branding_url", f"{st3}, missing"))

    # 3. assets
    if logo.startswith(base):
        st, h, b = public(asset_path(logo))
        check("SVG logo served, same bytes", st == 200 and b == logo_bytes, f"{st} {len(b)} bytes")
        check("SVG Content-Type", h.get("content-type", "").startswith("image/svg+xml"), h.get("content-type"))
        check("SVG nosniff", h.get("x-content-type-options", "").lower() == "nosniff", h.get("x-content-type-options"))
        csp = h.get("content-security-policy", "")
        check("SVG CSP default-src 'none'", "default-src 'none'" in csp, csp)
        check("SVG long cache (?v=)", "v=" in logo and max_age(h) >= 86400, h.get("cache-control"))
    if login.startswith(base):
        st, h, b = public(asset_path(login))
        check("PNG served, same bytes", st == 200 and b == login_bytes, f"{st} {len(b)} bytes")
        check("PNG Content-Type + nosniff", h.get("content-type", "").startswith("image/png")
              and h.get("x-content-type-options", "").lower() == "nosniff", {k: h.get(k) for k in ("content-type", "x-content-type-options")})
    for path in ("/v1/branding/assets/..%2Fboson.yaml", "/v1/branding/assets/%2e%2e/boson.yaml",
                 "/v1/branding/assets/../../boson.yaml", "/v1/branding/assets/%2e%2e%2fboson.yaml"):
        st, _, b = public(path)
        check(f"no escape from config/branding: {path}", st != 200 and b"version:" not in b, st)
    st, _, _ = public("/v1/branding/assets/no-such-file.png")
    check("missing asset -> 404", st == 404, st)

    # 4. hot reload
    put(CONF, config_text(ACCENT2))
    st, h, doc = wait_branding(ACCENT2)
    check(f"hot reload: accent {ACCENT2} within {args.timeout}s", st == 200 and (doc.get("colors") or {}).get("accent", "").lower() == ACCENT2,
          f"{st} {(doc or {}).get('colors') if isinstance(doc, dict) else doc}")
    check("new ETag after the change", h.get("etag") and h.get("etag") != etag, f"{etag} -> {h.get('etag')}")
    st2, _, _ = public("/v1/branding", {"If-None-Match": etag})
    check("old ETag -> 200", st2 == 200, st2)

    # 5. email preview, as an admin (settings.manage)
    st, pv = internal("/internal/email/preview", {"template": "verification"})
    if st == 404:
        return skip("auth has no POST /internal/email/preview (older than the branding release)")
    check("email preview needs a token", st == 401, st)
    tok = admin_token()
    if not check("admin signed in", bool(tok)):
        return
    st, pv = internal("/internal/email/preview", {"template": "verification"}, tok)
    if not check("POST /internal/email/preview", st == 200 and isinstance(pv, dict), st if isinstance(pv, dict) else f"{st} {pv}"):
        return
    subj, html, text = pv.get("subject", ""), pv.get("html", ""), pv.get("text", "")
    check("subject from the template, {{network}} filled in", f"[smoke {tag}]" in subj and "{{network}}" not in subj, subj)
    check("unknown {{variable}} kept as typed", "{{not_a_variable}}" in text, text if "{{not_a_variable}}" not in text else "")
    check("template text is HTML-escaped", "<b>bold</b>" not in html and "&lt;b&gt;bold&lt;/b&gt;" in html,
          re.findall(r".{0,20}bold.{0,20}", html))
    check("{{username}} / {{link}} filled in", "{{username}}" not in text and "{{link}}" not in text,
          text.splitlines()[0] if text else "")
    check("footer in HTML and text", FOOTER in html and FOOTER in text, FOOTER)
    check("accent in the HTML", ACCENT1 in html.lower(), ACCENT1)
    st, pv2 = internal("/internal/email/preview", {"template": "verification", "subject": "Draft {{network}} " + tag, "body": "Unsaved draft"}, tok)
    check("preview with subject/body overrides", st == 200 and isinstance(pv2, dict) and pv2.get("subject", "").startswith("Draft ")
          and "{{network}}" not in pv2.get("subject", "") and "Unsaved draft" in pv2.get("text", ""),
          pv2.get("subject") if isinstance(pv2, dict) else f"{st} {pv2}")
    st, bad = internal("/internal/email/preview", {"template": "no_such_template"}, tok)
    check("unknown template -> 4xx", 400 <= st < 500, st)

    # 6. test send -> Mailpit
    if args.mailpit == "none":
        return print("SKIP test send (--mailpit none)")
    st, sent = internal("/internal/email/test", {"template": "verification", "to": TO}, tok)
    check("POST /internal/email/test: sent", 200 <= st < 300 and isinstance(sent, dict) and sent.get("status") == "sent", f"{st} {sent}")
    msg, deadline = None, time.time() + 30
    while time.time() < deadline and not msg:
        try:
            q = urllib.parse.quote(f"to:{TO}")
            found = json.load(urllib.request.urlopen(f"{args.mailpit}/api/v1/search?query={q}", timeout=5))
            msg = (found.get("messages") or [None])[0]
        except OSError as e:
            print("mailpit:", e)
        if not msg: time.sleep(1)
    if not check(f"test mail reached Mailpit ({TO})", bool(msg)):
        return
    check("From display name = auth.email.sender_name", (msg.get("From") or {}).get("Name") == SENDER, msg.get("From"))
    check("Subject from the template", f"[smoke {tag}]" in msg.get("Subject", ""), msg.get("Subject"))
    full = json.load(urllib.request.urlopen(f"{args.mailpit}/api/v1/message/{msg['ID']}", timeout=5))
    check("HTML + text parts with the footer", FOOTER in full.get("HTML", "") and FOOTER in full.get("Text", ""),
          {k: len(full.get(k, "")) for k in ("HTML", "Text")})
    check("HTML part escaped", "<b>bold</b>" not in full.get("HTML", ""), "")

try:
    run()
finally:
    try:
        put(CONF, original)
        for f in (LOGO, LOGIN):
            if os.path.exists(os.path.join(BRAND, f)): remove(os.path.join(BRAND, f))
        print("restored config/boson.yaml, removed the smoke images")
    except Exception as e:  # never hide the test's own result
        print("cleanup failed:", e)

if failures:
    print(f"{len(failures)} failed: {', '.join(failures)}"); sys.exit(1)
print("branding smoke: ok")
