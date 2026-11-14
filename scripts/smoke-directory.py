#!/usr/bin/env python3
"""Smoke test for the directory profile (scripts/setup.sh --local, profiles
auth + directory, DIRECTORY_ENV=development DIRECTORY_DNS_VERIFIER=fake
DIRECTORY_SERVES_LEGACY=true DIRECTORY_INCLUDE_MAIN=false).

1. https://DIRECTORY_HOST/.well-known/boson-directory.json through Traefik.
2. Register three networks as stack networks (Authorization: Network <key>),
   the way the admin panel does: 201 + the TXT record to add; the same key
   again is idempotent (200, same id); another key gets 409.
3. Pending listings are not public; the owner's lookup sees them.
4. Verify each (the fake DNS verifier passes every TXT check).
5. Page GET /v1/networks?limit=2 by next_cursor: every network exactly once.
6. The legacy GET https://API_HOST/servers is answered by the directory.
7. DELETE with the key removes a listing.

  scripts/smoke-directory.py [--addr 127.0.0.1] [--ca local-certs/ca.pem]
"""
import argparse, json, os, secrets, socket, ssl, sys, time, urllib.parse

p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1", help="where the stack listens")
p.add_argument("--directory-host", default="directory.localhost")
p.add_argument("--api-host", default="api.localhost")
p.add_argument("--ca", default=os.path.join(os.path.dirname(__file__), "..", "local-certs", "ca.pem"),
               help="CA to verify TLS with (default: the local CA); 'none' skips verification")
args = p.parse_args()

if args.ca == "none":
    ctx = ssl.create_default_context(); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
else:
    ctx = ssl.create_default_context(cafile=args.ca)

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + str(detail) if detail else ""))
    if not ok: failures.append(name)

def https(method, path, body=None, auth=None, host=None):
    """Minimal HTTPS client: connects to --addr, SNI/Host = host."""
    host = host or args.directory_host
    raw = socket.create_connection((args.addr, 443), timeout=15)
    s = ctx.wrap_socket(raw, server_hostname=host)
    data = json.dumps(body).encode() if body is not None else b""
    hdrs = [f"{method} {path} HTTP/1.1", f"Host: {host}", "Connection: close",
            "Accept: application/json", "User-Agent: boson-stack-smoke", f"Content-Length: {len(data)}"]
    if body is not None: hdrs.append("Content-Type: application/json")
    if auth: hdrs.append(f"Authorization: {auth}")
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

def net(body):
    """{network: …} or a bare network."""
    return body.get("network", body) if isinstance(body, dict) else {}

# 1. up, through Traefik
deadline, err = time.time() + 120, "not ready"
while True:
    try:
        st, wk = https("GET", "/.well-known/boson-directory.json")
        if st == 200: break
        err = f"{st} {wk}"
    except (OSError, ssl.SSLError) as e:
        err = e
    if time.time() > deadline:
        check("directory ready via Traefik", False, err); sys.exit(1)
    time.sleep(2)
check("well-known document", isinstance(wk, dict) and wk.get("federation") is True and bool(wk.get("name")) and bool(wk.get("api")), wk)

# 2. register as stack networks
run = secrets.token_hex(3)
nets = []  # (hostname, key, id)
for i in range(1, 4):
    host, key = f"irc{i}-{run}.ci.boson.chat", secrets.token_urlsafe(32)
    st, body = https("POST", "/v1/networks", {"hostname": host, "port": 6697, "tls": True,
                     "name": f"Smoke {i}", "description": "CI smoke network", "languages": ["en"]}, auth=f"Network {key}")
    n = net(body)
    ver = body.get("verification") if isinstance(body, dict) else None
    check(f"register {host}", st == 201 and n.get("id") and n.get("status") == "pending"
          and ver and ver.get("record_name") == f"_boson.{host}" and ver.get("record_value", "").startswith("boson-verify="), f"{st} {body}")
    nets.append((host, key, n.get("id")))
host0, key0, id0 = nets[0]
st, body = https("POST", "/v1/networks", {"hostname": host0, "port": 6697, "tls": True, "name": "Smoke 1"}, auth=f"Network {key0}")
check("register again with the same key is idempotent", st == 200 and net(body).get("id") == id0, f"{st} {body}")
st, body = https("POST", "/v1/networks", {"hostname": host0, "port": 6697, "tls": True, "name": "Thief"},
                 auth="Network " + secrets.token_urlsafe(32))
check("another key can't take the hostname", st == 409, f"{st} {body}")

# 3. pending: owner only
q = urllib.parse.urlencode({"hostname": host0})
st, body = https("GET", f"/v1/networks/lookup?{q}")
check("pending listing isn't public", st == 404, f"{st} {body}")
st, body = https("GET", f"/v1/networks/lookup?{q}", auth=f"Network {key0}")
check("owner lookup sees the pending listing", st == 200 and net(body).get("id") == id0, f"{st} {body}")

# 4. verify (fake DNS)
for host, key, nid in nets:
    st, body = https("POST", f"/v1/networks/{urllib.parse.quote(nid or '')}/verify", auth=f"Network {key}")
    check(f"verify {host}", st == 200 and isinstance(body, dict) and body.get("success") is True, f"{st} {body}")
st, body = https("GET", f"/v1/networks/lookup?{q}")
check("verified listing is public", st == 200 and net(body).get("status") == "verified", f"{st} {body}")

# 5. page through by next_cursor
seen, cursor, pages = [], None, 0
while pages < 20:
    path = "/v1/networks?limit=2" + (f"&cursor={urllib.parse.quote(cursor)}" if cursor else "")
    st, body = https("GET", path)
    if st != 200 or not isinstance(body, dict):
        check("GET /v1/networks", False, f"{st} {body}"); break
    pages += 1
    check(f"page {pages} has at most 2 networks", len(body.get("networks", [])) <= 2, len(body.get("networks", [])))
    seen += [n["hostname"] for n in body.get("networks", [])]
    cursor = body.get("next_cursor")
    if not cursor: break
ours = sorted(h for h in seen if h.endswith(f"-{run}.ci.boson.chat"))
check("paging returns every network once", ours == sorted(h for h, _, _ in nets) and len(seen) == len(set(seen)) and pages >= 2,
      f"{pages} pages, {seen}")

# 6. legacy /servers through the api host
st, body = https("GET", "/servers?limit=100", host=args.api_host)
legacy = [s.get("hostname") for s in body.get("servers", [])] if isinstance(body, dict) else []
check("legacy https://API_HOST/servers is the directory", st == 200 and host0 in legacy, f"{st} {str(body)[:300]}")

# 7. unlist
host2, key2, id2 = nets[2]
st, body = https("DELETE", f"/v1/networks/{urllib.parse.quote(id2 or '')}", auth=f"Network {key0}")
check("another network's key can't delete it", st in (403, 404), f"{st} {body}")
st, body = https("DELETE", f"/v1/networks/{urllib.parse.quote(id2 or '')}", auth=f"Network {key2}")
check("owner deletes the listing", st in (200, 204), f"{st} {body}")
st, body = https("GET", "/v1/networks/lookup?" + urllib.parse.urlencode({"hostname": host2}))
check("deleted listing is gone", st == 404, f"{st} {body}")

if failures:
    print(f"\n{len(failures)} check(s) failed: {', '.join(failures)}"); sys.exit(1)
print("\nall directory checks passed")
