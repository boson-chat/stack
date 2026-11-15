#!/usr/bin/env python3
"""Smoke test for the files profile (scripts/setup.sh --local, profiles
auth + files + wellknown, config/boson.yaml with a files: section that has
enabled: true and short test limits, see .github/workflows/ci.yml):

  retention 20s, window 30s, account quota 3MB / max_file 2MB,
  guest quota 1MB / max_file 512KB (decimal: 1MB = 1,000,000 bytes)

1. https://FILES_HOST/v1/config through Traefik: enabled, the test limits.
2. An account uploads (POST /v1/files, raw body): 201 + fields, image size.
3. GET /f/{id}/{name}: nosniff, CSP sandbox, inline for png, Cache-Control;
   a Range request is 206; svg and html are attachments.
4. Over max_file: 413 file_too_large; over quota: 413 quota_exceeded with
   used, quota, window_seconds, frees_at. Deleting doesn't refund.
5. GET /v1/me/usage shape.
6. Guests: POST /v1/guest-token, upload with Authorization: Guest; guest
   max_file applies.
7. The internal API (:3001) isn't routed by Traefik.
8. Discovery: boson.json files.url, auth /v1/config files_url.
   Storage guard: storage.min_free above the free space -> 507 storage_full.
9. Expiry: the png is gone (404/410) after retention.
10. Rolling window: the refused upload fits once the old uploads leave it.

  scripts/smoke-files.py [--addr 127.0.0.1] [--ca local-certs/ca.pem]
"""
import argparse, http.client, subprocess, json, os, secrets, socket, ssl, struct, sys, time, urllib.parse, zlib

ROOT = os.path.join(os.path.dirname(__file__), "..")
p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1", help="where the stack listens")
p.add_argument("--files-host", default="files.localhost")
p.add_argument("--auth-host", default="auth.localhost")
p.add_argument("--domain", default="localhost")
p.add_argument("--ca", default=os.path.join(ROOT, "local-certs", "ca.pem"),
               help="CA to verify TLS with (default: the local CA); 'none' skips verification")
# The limits written by CI (bytes, seconds).
p.add_argument("--quota", type=int, default=3_000_000)
p.add_argument("--max-file", type=int, default=2_000_000)
p.add_argument("--guest-max-file", type=int, default=512_000)
p.add_argument("--retention", type=int, default=20)
p.add_argument("--window", type=int, default=30)
args = p.parse_args()

if args.ca == "none":
    ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
else:
    ctx = ssl.create_default_context(cafile=args.ca)

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail else ""))
    if not ok: failures.append(name)
    return ok

class Conn(http.client.HTTPSConnection):
    """HTTPS to --addr with SNI/Host = the given host (no DNS for *.localhost needed)."""
    def connect(self):
        raw = socket.create_connection((args.addr, self.port), self.timeout)
        self.sock = self._context.wrap_socket(raw, server_hostname=self.host)

def https(host, method, path, body=None, headers=None, auth=None):
    """-> (status, headers (lower-case keys), json|bytes)."""
    c = Conn(host, 443, context=ctx, timeout=30)
    hdrs = {"User-Agent": "boson-stack-smoke", **(headers or {})}
    if isinstance(body, (dict, list)):
        body = json.dumps(body).encode(); hdrs.setdefault("Content-Type", "application/json")
    if auth: hdrs["Authorization"] = auth
    if method in ("POST", "PUT") and body is None: hdrs["Content-Length"] = "0"
    c.request(method, path, body=body, headers=hdrs)
    r = c.getresponse(); raw = r.read(); c.close()
    h = {k.lower(): v for k, v in r.getheaders()}
    if "json" in h.get("content-type", ""):
        try: return r.status, h, json.loads(raw or b"null")
        except ValueError: pass
    return r.status, h, raw

def files(method, path, **kw): return https(args.files_host, method, path, **kw)

def err_code(body):
    """Errors are {error, message, code, ...}."""
    if not isinstance(body, dict): return ""
    return body.get("code") or body.get("error") or ""

def png(w=1, h=1):
    def chunk(t, d): return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
    raw = b"".join(b"\x00" + b"\xff\x00\x00" * w for _ in range(h))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))

def upload(data, name, ctype, auth):
    return files("POST", "/v1/files", body=data, auth=auth, headers={
        "Content-Type": ctype, "X-Filename": urllib.parse.quote(name), "Content-Length": str(len(data))})

def path_of(url):
    u = urllib.parse.urlsplit(url)
    return u.path + (("?" + u.query) if u.query else "")

def wait(what, fn, timeout):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        ok, last = fn()
        if ok: return True, last
        time.sleep(2)
    return False, last

# 1. up, through Traefik, with the test config
ok, cfg = wait("files /v1/config", lambda: (lambda r: (r[0] == 200 and isinstance(r[2], dict) and r[2].get("enabled") is True, r))(files("GET", "/v1/config")), 120)
check("files /v1/config through Traefik (enabled)", ok, cfg and (cfg[0], cfg[2]))
if not ok: sys.exit(1)
c = cfg[2]
acc = (c.get("limits") or {}).get("account") or {}
gst = (c.get("limits") or {}).get("guest") or {}
check("config: account limits from config/boson.yaml", acc.get("quota_bytes") == args.quota and acc.get("max_file_bytes") == args.max_file
      and acc.get("window_seconds") == args.window, acc)
check("config: guest limits, guest_enabled, accounts_enabled, retention", c.get("guest_enabled") is True and c.get("accounts_enabled") is True and gst.get("max_file_bytes") == args.guest_max_file
      and c.get("retention_seconds") == args.retention, c)

# 2. an account
tag = secrets.token_hex(3)
user, pw = f"filesmoke{tag}", secrets.token_urlsafe(18)
st, _, r = https(args.auth_host, "POST", "/v1/register", {"username": user, "email": f"{user}@example.com", "password": pw})
check("auth register", st == 201, st)
st, _, r = https(args.auth_host, "POST", "/v1/login", {"login": user, "password": pw})
tok = r.get("access_token") if isinstance(r, dict) else None
if not check("auth login", st == 200 and tok, st): sys.exit(1)
bearer = "Bearer " + tok

st, _, r = files("POST", "/v1/files", body=b"x", headers={"Content-Type": "text/plain", "X-Filename": "a.txt", "Content-Length": "1"})
check("upload without a token: 401 unauthenticated", st == 401 and err_code(r) == "unauthenticated", (st, r))

img = png(3, 2)
st, _, f_png = upload(img, "smoke pic.png", "image/png", bearer)
t_png = time.time()
check("account upload 201", st == 201 and isinstance(f_png, dict), (st, f_png))
if st != 201: sys.exit(1)
check("upload fields", all(k in f_png for k in ("id", "url", "name", "size", "content_type", "expires_at"))
      and f_png["size"] == len(img) and f_png["name"] == "smoke pic.png" and len(f_png["id"]) >= 22, f_png)
check("image width/height recorded", f_png.get("width") == 3 and f_png.get("height") == 2, f_png)
check("url is on FILES_HOST under /f/", f_png["url"].startswith(f"https://{args.files_host}/f/{f_png['id']}/"), f_png["url"])

# 3. serving
st, h, body = files("GET", path_of(f_png["url"]))
check("GET /f/: 200 and the bytes", st == 200 and body == img, st)
check("nosniff", h.get("x-content-type-options", "").lower() == "nosniff", h)
csp = h.get("content-security-policy", "")
check("CSP sandbox; default-src 'none'", "sandbox" in csp and "default-src 'none'" in csp, csp)
check("png inline", h.get("content-disposition", "").lower().startswith("inline"), h.get("content-disposition"))
check("CORS *, X-File-Expires-At, ETag", h.get("access-control-allow-origin") == "*" and h.get("x-file-expires-at") and h.get("etag"), h)
check("Cache-Control public, max-age", "public" in h.get("cache-control", "") and "max-age=" in h.get("cache-control", ""), h.get("cache-control"))
st, h, body = files("GET", path_of(f_png["url"]), headers={"Range": "bytes=0-7"})
check("Range request: 206, 8 bytes", st == 206 and body == img[:8] and h.get("content-range", "").startswith("bytes 0-7/"), (st, h.get("content-range")))

for name, ctype, data in (("x.svg", "image/svg+xml", b'<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>'),
                          ("x.html", "text/html", b"<html><script>alert(1)</script></html>")):
    st, _, f = upload(data, name, ctype, bearer)
    if not check(f"upload {name}", st == 201, (st, f)): continue
    st, h, _ = files("GET", path_of(f["url"]))
    check(f"{name} is served as an attachment with the sandbox CSP",
          st == 200 and h.get("content-disposition", "").lower().startswith("attachment") and "sandbox" in h.get("content-security-policy", "")
          and h.get("content-type", "").startswith("application/octet-stream"),
          (st, h.get("content-disposition"), h.get("content-type")))

# 4. limits
st, _, r = upload(b"\0" * (args.max_file + 1), "big.bin", "application/octet-stream", bearer)
check("over max_file: 413 file_too_large {max}", st == 413 and err_code(r) == "file_too_large" and r.get("max") == args.max_file, (st, r))

chunk1 = args.max_file - 64
st, _, f_big = upload(b"\1" * chunk1, "fill.bin", "application/octet-stream", bearer)
t_big = time.time()
check("upload up to max_file", st == 201, (st, f_big))
second = args.quota - chunk1 + 1024  # doesn't fit next to the first
second = min(second, args.max_file)
st, _, r = upload(b"\2" * second, "over.bin", "application/octet-stream", bearer)
check("over quota: 413 quota_exceeded {used, quota, window_seconds, frees_at}",
      st == 413 and err_code(r) == "quota_exceeded" and all(k in r for k in ("used", "quota", "window_seconds", "frees_at"))
      and r["quota"] == args.quota, (st, r))
if isinstance(f_big, dict) and f_big.get("id"):
    st, _, _ = files("DELETE", f"/v1/files/{f_big['id']}", auth=bearer)
    check("DELETE own file", st in (200, 204), st)
    st, _, _ = files("GET", path_of(f_big["url"]))
    check("deleted file is gone at once", st in (404, 410), st)
    st, _, r = upload(b"\2" * second, "over.bin", "application/octet-stream", bearer)
    check("deleting doesn't refund: still quota_exceeded", st == 413 and err_code(r) == "quota_exceeded", (st, r))

# 5. usage
st, _, u = files("GET", "/v1/me/usage", auth=bearer)
check("/v1/me/usage shape", st == 200 and isinstance(u, dict) and all(k in u for k in ("used", "quota", "window_seconds", "max_file", "frees"))
      and u["quota"] == args.quota and u["used"] >= chunk1 and isinstance(u["frees"], list), (st, u))
st, _, l = files("GET", "/v1/me/files", auth=bearer)
ids = [x.get("id") for x in l["files"]] if st == 200 and isinstance(l, dict) and isinstance(l.get("files"), list) else []
check("/v1/me/files lists live files only", st == 200 and f_png["id"] in ids and (not f_big or f_big.get("id") not in ids), (st, ids))

# 6. guests
st, _, g = files("POST", "/v1/guest-token")
gtok = g.get("token") if isinstance(g, dict) else None
check("POST /v1/guest-token: 201 {token, expires_at, limits}", st == 201 and gtok and "expires_at" in g and "limits" in g, (st, g))
if gtok:
    st, _, f = upload(b"hello guest\n", "guest.txt", "text/plain", "Guest " + gtok)
    check("guest upload 201", st == 201, (st, f))
    if st == 201:
        st, h, body = files("GET", path_of(f["url"]))
        check("text/plain is served inline as text/plain", st == 200 and h.get("content-type", "").startswith("text/plain")
              and h.get("content-disposition", "").lower().startswith("inline"), (st, h.get("content-type"), h.get("content-disposition")))
    st, _, r = upload(b"\0" * (args.guest_max_file + 1), "g.bin", "application/octet-stream", "Guest " + gtok)
    check("guest max_file: 413 file_too_large", st == 413 and err_code(r) == "file_too_large", (st, r))
    st, _, r = upload(b"x", "x.txt", "text/plain", "Guest " + gtok[:-2] + ("AA" if not gtok.endswith("AA") else "BB"))
    check("forged guest token: 401 invalid_guest_token", st == 401 and err_code(r) == "invalid_guest_token", (st, r))

# 6b. storage guard: storage.min_free bigger than any disk -> 507 storage_full
CONFIG = os.path.join(ROOT, "config", "boson.yaml")
def write_config(text):
    # config/boson.yaml belongs to uid 65532 (config-init): write through sudo when needed.
    try:
        with open(CONFIG, "w") as f: f.write(text)
    except PermissionError:
        subprocess.run(["sudo", "tee", CONFIG], input=text.encode(), stdout=subprocess.DEVNULL, check=True)
original = open(CONFIG).read()
if "\nfiles:\n" in "\n" + original:
    write_config(original.replace("\nfiles:\n", "\nfiles:\n  storage: { min_free: 1000TB }\n", 1))
    ok, last = wait("507", lambda: (lambda r: (r[0] == 507, (r[0], r[2])))(upload(b"tiny\n", "t.txt", "text/plain", bearer)), 60)
    check("min_free over the free space: 507 storage_full", ok and err_code(last[1]) == "storage_full", last)
    write_config(original)
    ok, last = wait("upload again", lambda: (lambda r: (r[0] == 201, (r[0], r[2])))(upload(b"tiny\n", "t.txt", "text/plain", bearer)), 60)
    check("min_free restored: uploads work again", ok, last)
else:
    check("config/boson.yaml has a files: section (CI writes it)", False)

# 7. internal API not routed
for path in ("/internal/files/status", "/internal/files", "/metrics", "/internal/config/status"):
    st, _, _ = files("GET", path)
    check(f"{path} isn't routed through Traefik", st in (401, 403, 404), st)

# 8. discovery
st, _, d = https(args.domain, "GET", "/.well-known/boson.json")
check("boson.json files.url", st == 200 and isinstance(d, dict) and (d.get("files") or {}).get("url") == f"https://{args.files_host}", (st, d))
st, _, a = https(args.auth_host, "GET", "/v1/config")
if isinstance(a, dict) and "files_url" in a:
    check("auth /v1/config files_url", a["files_url"] == f"https://{args.files_host}", a.get("files_url"))
else:
    print("NOTE auth has no files_url in /v1/config yet (auth release before files support): not checked")

# 9. expiry
ok, last = wait("expiry", lambda: (lambda s: (s in (404, 410), s))(files("GET", path_of(f_png["url"]))[0]),
                max(10, t_png + args.retention + 90 - time.time()))
check(f"the png is gone after retention ({args.retention}s)", ok, last)

# 10. rolling window: the deleted upload ages out, the refused one now fits
ok, last = wait("window", lambda: (lambda r: (r[0] == 201, (r[0], r[2])))(upload(b"\2" * second, "later.bin", "application/octet-stream", bearer)),
                max(10, t_big + args.window + 60 - time.time()))
check(f"after the window ({args.window}s) the upload fits again", ok, last)

print()
if failures:
    print(f"{len(failures)} check(s) failed: " + ", ".join(failures)); sys.exit(1)
print("files smoke: all checks passed")
