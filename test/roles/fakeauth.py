#!/usr/bin/env python3
"""A fake boson auth service with the network-roles contract (docs/roles.md
"What the auth service provides"), for test/roles/run.sh. Stdlib only.

Same shapes as auth 1.5 (boson-chat/auth docs/roles.md) for:
  GET  /internal/v1/settings, POST /internal/v1/verify-password,
  GET  /internal/v1/roles (ETag), GET /internal/v1/users/{username}/irc
plus what the stack asks auth to add (docs/roles.md "Requested from auth"):
  GET  /internal/v1/roles/{id}/members       (the admin API's members list, internal)
  GET  /internal/v1/irc/grants               (ETag; every user with an oper class or vhost)
  GET  /internal/v1/irc/accounts/removed     (auth 1.7: users deleted/disabled after ?since=)
  /internal/v1/acting/...                    (the admin API as X-Acting-Account,
                                              X-Internal-Token = INTERNAL_ACTING_TOKEN; GET /me)
and a test control endpoint, POST /test/state {users: {name: [role]}, roles: {role: patch}, disable: [name], delete: [name],
settings: {field: value}} (settings: merged into GET /internal/v1/settings).
"""
import hashlib, json, os, re, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs, unquote

TOKEN = os.environ.get("INTERNAL_API_TOKEN", "")
ACTING = os.environ.get("INTERNAL_ACTING_TOKEN", "")
lock = threading.Lock()
SETTINGS = {"channel_creation": "registered", "channel_auto_register": True, "channel_expiry_days": 0, "guest_access": True}

CAPS = {"kill", "ban", "nofakelag", "relaymsg", "vhosts", "sajoin", "samode", "snomasks", "roleplay", "rehash",
        "accreg", "chanreg", "history", "defcon", "massmessage", "metadata"}
def mkrole(id, name, pos, builtin, perms, whois=None, vhost=None, oper=False, color=None, hoist=False):
    return {"id": id, "name": name, "slug": name.lower().replace(" ", "-"), "color": color, "position": pos, "builtin": builtin,
            "permissions": perms, "irc": {"whois_line": whois, "vhost": vhost, "oper": oper}, "hoist": hoist, "mentionable": False}
ROLES = [
    mkrole("rol_00000000000000000000000001", "Owner", 1000, "owner", ["roles.manage", "roles.owner", "admin.panel", "irc.kill", "irc.ban", "irc.samode", "irc.chanreg", "irc.rehash"], color="#f1c40f", hoist=True),
    mkrole("rol_00000000000000000000000002", "Admin", 900, "admin", ["roles.manage", "admin.panel", "irc.kill", "irc.ban", "irc.samode", "irc.vhosts", "irc.chanreg"],
           whois="is a Network Administrator", vhost="staff/{username}", color="#e74c3c", hoist=True),
    mkrole("rol_00000000000000000000000003", "Moderator", 500, "moderator", ["users.view", "irc.kill", "irc.ban", "irc.nofakelag", "irc.samode"],
           vhost="mod/{username}", color="#3498db", hoist=True),
    mkrole("rol_dj", "DJ", 10, None, ["voice.create"], vhost="dj/{username}"),
    mkrole("rol_00000000000000000000000004", "Member", 0, "member", ["channel.create", "voice.create"]),
]
USERS = {  # username -> password, assigned role ids (Member is implicit)
    "alice": ("pw-alice", ["rol_00000000000000000000000002"]),
    "bob": ("pw-bob", []),
    "dave": ("pw-dave", ["rol_dj"]),
    "erin": ("pw-erin", ["rol_00000000000000000000000003"]),
    "frank": ("pw-frank", []), "gina": ("pw-gina", []), "hank": ("pw-hank", []),
}
DISABLED = set()
REMOVED = []  # (seq, username, reason, at), the removed-accounts feed
def removed(name, reason):
    import datetime
    REMOVED.append((len(REMOVED) + 1, name, reason, datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")))
version = [1]

def slug(s): return s.lower().replace(" ", "-")
def role(ref):
    for r in ROLES:
        if r["id"] == ref or r["name"].lower() == ref.lower() or r["slug"] == ref: return r
def is_oper(r): return r["irc"]["oper"] or any(p.startswith("irc.") for p in r["permissions"])
def caps(r): return [p[4:] for p in r["permissions"] if p.startswith("irc.") and p[4:] in CAPS]
def eff_whois(r):
    w = r["irc"]["whois_line"]
    return w if w else ("is an " if r["name"][0].lower() in "aeiou" else "is a ") + r["name"]
def internal_role(r):
    o = is_oper(r)
    return dict(r, irc={"whois_line": eff_whois(r), "vhost": r["irc"]["vhost"], "oper": o,
                        "oper_class": r["slug"] if o else None, "capabilities": caps(r)},
                member_count=sum(1 for u in USERS if r["id"] in USERS[u][1]))
def user_roles(u, member=True):
    ids = USERS.get(u, ("", []))[1]
    return sorted([r for r in ROLES if r["id"] in ids or (member and r["builtin"] == "member")], key=lambda r: -r["position"])
def top(u): return max([r["position"] for r in user_roles(u)] or [-1])
def perms(u):
    out = []
    for r in user_roles(u):
        for p in r["permissions"]:
            if p not in out: out.append(p)
    return out
def vhost_name(u): return re.sub(r"[\[\]\\`^{|}]", "-", u.lower())
def user_irc(u):
    rs = user_roles(u)
    oper = next((r for r in rs if is_oper(r)), None)
    vh = next((r for r in rs if r["irc"]["vhost"]), None)
    return {"oper_class": oper["slug"] if oper else None, "whois_line": eff_whois(oper) if oper else None,
            "vhost": vh["irc"]["vhost"].replace("{username}", vhost_name(u)) if vh else None}
def user_doc(u):
    return {"user_id": "usr_" + u, "username": u, "disabled": u in DISABLED, "irc": user_irc(u),
            "roles": [r["name"] for r in user_roles(u, member=False)], "permissions": perms(u)}

def etag(obj): return '"' + hashlib.sha256(json.dumps(obj, sort_keys=True).encode()).hexdigest()[:16] + '"'
def grants():
    return {"users": [dict(username=u, **user_irc(u)) for u in sorted(USERS)
                      if u not in DISABLED and (user_irc(u)["oper_class"] or user_irc(u)["vhost"])]}

class H(BaseHTTPRequestHandler):
    def log_message(self, fmt, *a): sys.stderr.write("fakeauth: " + fmt % a + "\n")
    def reply(self, code, body=None, headers=None):
        data = b"" if body is None else json.dumps(body).encode()
        self.send_response(code)
        for k, v in (headers or {}).items(): self.send_header(k, v)
        if body is not None: self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"null") if n else None
    def err(self, code, c, msg): self.reply(code, {"error": msg, "code": c})

    def route(self, method):
        u = urlparse(self.path); p = u.path
        internal = self.server.server_port == 8081
        if not internal:
            with lock:
                if method == "GET" and p == "/v1/roles":
                    return self.reply(200, {"roles": [{k: r[k] for k in ("id", "name", "color", "position", "hoist", "mentionable")} for r in ROLES]})
            return self.err(404, "", "no route")
        if p.startswith("/internal/v1/acting/"):
            if not ACTING or self.headers.get("X-Internal-Token") != ACTING: return self.err(403, "forbidden", "acting token required")
        elif self.headers.get("X-Internal-Token") != TOKEN and not p.startswith("/test/"):
            return self.err(401, "unauthorized", "bad token")
        with lock:
            if method == "GET" and p == "/internal/v1/settings":
                return self.reply(200, SETTINGS)
            if method == "GET" and p == "/internal/v1/irc/accounts/removed":
                q = parse_qs(u.query); since = int((q.get("since") or ["0"])[0] or 0); limit = int((q.get("limit") or ["500"])[0])
                page = [r for r in REMOVED if r[0] > since][:limit]
                # like auth: entries for usernames that exist (again) / are enabled again are left out
                out = [{"username": n, "reason": why, "at": at} for _, n, why, at in page
                       if (why == "deleted" and n not in USERS) or (why == "disabled" and n in DISABLED)]
                return self.reply(200, {"accounts": out, "next": str(page[-1][0]) if page else (str(since) if since else "")})
            if method == "POST" and p == "/internal/v1/verify-password":
                b = self.body() or {}; name = b.get("username", "").lower()
                if name in USERS and USERS[name][0] == b.get("password") and name not in DISABLED:
                    d = user_doc(name)
                    return self.reply(200, {"ok": True, "user_id": d["user_id"], "username": name, "irc": d["irc"],
                                            "roles": d["roles"], "permissions": d["permissions"]})
                return self.reply(200, {"ok": False, "reason": "invalid_credentials"})
            if method == "GET" and p == "/internal/v1/roles":
                body = {"roles": [internal_role(r) for r in sorted(ROLES, key=lambda r: -r["position"])], "version": str(version[0])}
                tag = etag(body)
                if self.headers.get("If-None-Match") == tag: return self.reply(304, None, {"ETag": tag})
                return self.reply(200, body, {"ETag": tag})
            if method == "GET" and p == "/internal/v1/irc/grants":
                body = grants(); tag = etag(body)
                if self.headers.get("If-None-Match") == tag: return self.reply(304, None, {"ETag": tag})
                return self.reply(200, body, {"ETag": tag})
            m = re.fullmatch(r"/internal/v1/roles/([^/]+)/members", p)
            if method == "GET" and m:
                r = role(unquote(m[1]))
                if not r: return self.err(404, "not_found", "no such role")
                return self.reply(200, {"users": [{"id": "usr_" + n, "username": n, "disabled": n in DISABLED}
                                                  for n in sorted(USERS) if r["id"] in USERS[n][1]], "next_cursor": None})
            m = re.fullmatch(r"/internal/v1/users/([^/]+)/irc", p)
            if method == "GET" and m:
                name = unquote(m[1]).lower()
                if name not in USERS: return self.err(404, "not_found", "no such user")
                return self.reply(200, user_doc(name))
            if method == "POST" and p == "/test/state":
                b = self.body() or {}
                for name, ids in (b.get("users") or {}).items():
                    USERS[name] = (USERS.get(name, ("pw-" + name, []))[0], [role(i)["id"] for i in ids])
                for name in b.get("disable") or []: DISABLED.add(name); removed(name, "disabled")
                for name in b.get("delete") or []:
                    USERS.pop(name, None); DISABLED.discard(name); removed(name, "deleted")
                SETTINGS.update(b.get("settings") or {})
                for rid, patch in (b.get("roles") or {}).items():
                    rid = role(rid)["id"]
                    r = role(rid)
                    for k, v in patch.items():
                        if k == "irc": r["irc"].update(v)
                        else: r[k] = v
                version[0] += 1
                return self.reply(204)
            if p.startswith("/internal/v1/acting/"): return self.acting(method, p[len("/internal/v1/acting"):])
        return self.err(404, "", "no route")

    def acting(self, method, p):
        """The admin API (docs: auth docs/roles.md "Admin API") mirrored under
        /internal/v1/acting, as X-Acting-Account."""
        actor = (self.headers.get("X-Acting-Account") or "").lower()
        if actor not in USERS or actor in DISABLED: return self.err(403, "forbidden", "unknown or disabled acting account")
        if method == "GET" and p == "/me": return self.reply(200, {"username": actor, "permissions": perms(actor)})
        if "roles.manage" not in perms(actor): return self.err(403, "forbidden", "missing permission roles.manage")
        owner = any(r["builtin"] == "owner" for r in user_roles(actor))
        def below(r): return owner or r["position"] < top(actor)
        def held(ps): return owner or all(x in perms(actor) for x in ps)
        if method == "POST" and p == "/roles":
            b = self.body() or {}; name = (b.get("name") or "").strip()
            if not re.fullmatch(r"[A-Za-z0-9 _-]{1,32}", name): return self.err(400, "invalid_request", "bad name")
            if role(name) or role(slug(name)): return self.err(409, "role_name_taken", "a role with that name exists")
            r = mkrole("rol_" + slug(name), name, 1, None, [])
            ROLES.append(r); version[0] += 1; return self.reply(201, {"role": internal_role(r)})
        m = re.fullmatch(r"/roles/([^/]+)", p)
        if m:
            r = role(unquote(m[1]))
            if not r: return self.err(404, "not_found", "no such role")
            if not below(r): return self.err(403, "role_hierarchy", "role hierarchy")
            if method == "DELETE":
                if r["builtin"]: return self.err(409, "builtin_role", "built-in roles can't be deleted")
                ROLES.remove(r)
                for n in USERS: USERS[n] = (USERS[n][0], [i for i in USERS[n][1] if i != r["id"]])
                version[0] += 1; return self.reply(204)
            if method == "PATCH":
                b = self.body() or {}
                if "permissions" in b:
                    if not held(set(b["permissions"]) - set(r["permissions"])): return self.err(403, "permission_not_held", "permission not held")
                    r["permissions"] = b["permissions"]
                for k in ("color", "position", "hoist", "mentionable"):
                    if k in b: r[k] = b[k]
                if "position" in b and not below(r): return self.err(403, "role_hierarchy", "role hierarchy")
                r["irc"].update(b.get("irc") or {}); version[0] += 1; return self.reply(200, {"role": internal_role(r)})
        m = re.fullmatch(r"/users/([^/]+)/roles", p)
        if m and method == "PUT":
            name = unquote(m[1]).removeprefix("usr_")
            if name not in USERS: return self.err(404, "not_found", "no such user")
            if name != actor and top(name) >= top(actor) and not owner: return self.err(403, "role_hierarchy", "role hierarchy")
            new = [i for i in (self.body() or {}).get("role_ids", []) if role(i) and role(i)["builtin"] != "member"]
            for rid in set(new) ^ set(USERS[name][1]):
                if not below(role(rid)): return self.err(403, "role_hierarchy", "role hierarchy")
            USERS[name] = (USERS[name][0], new); version[0] += 1
            return self.reply(200, {"roles": [{k: role(i)[k] for k in ("id", "name", "color", "position")} for i in new]})
        return self.err(404, "", "no route")

    def do_GET(self): self.route("GET")
    def do_POST(self): self.route("POST")
    def do_PATCH(self): self.route("PATCH")
    def do_PUT(self): self.route("PUT")
    def do_DELETE(self): self.route("DELETE")

for port in (8080, 8081):
    srv = ThreadingHTTPServer(("0.0.0.0", port), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
print("fakeauth: listening on 8080 (public) and 8081 (internal)", flush=True)
threading.Event().wait()
