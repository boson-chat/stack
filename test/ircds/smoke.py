#!/usr/bin/env python3
"""Smoke test for an IRC fixture pairing (test/ircds/run.sh smoke PAIRING).

Connects over TLS, registers a nick with NickServ, reconnects and identifies
with IDENTIFY, then logs in with SASL PLAIN when the server offers it, and
checks the account over the services RPC endpoint (Anope: checkCredentials
with the bearer token; Atheme: atheme.login). With --services none it checks
there is no NickServ. Re-running with the same --nick is fine (an existing
registration is reused).
"""
import argparse
import base64
import json
import secrets
import socket
import ssl
import sys
import time
import urllib.request

TIMEOUT = 20


class Client:
    def __init__(self, host, port, nick):
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        raw = socket.create_connection((host, port), timeout=TIMEOUT)
        self.sock = ctx.wrap_socket(raw, server_hostname=host)
        self.buf = b""
        self.nick = nick
        self.caps = {}

    def send(self, line):
        shown = line if not line.upper().startswith(("AUTHENTICATE ", "PRIVMSG NICKSERV :IDENTIFY", "PRIVMSG NICKSERV :REGISTER")) else line.split(" :")[0] + " <redacted>"
        print(">>", shown)
        self.sock.sendall((line + "\r\n").encode())

    def lines(self, deadline):
        while time.time() < deadline:
            while b"\n" in self.buf:
                line, self.buf = self.buf.split(b"\n", 1)
                line = line.rstrip(b"\r").decode(errors="replace")
                if line.startswith("PING"):
                    self.send("PONG" + line[4:])
                    continue
                print("<<", line)
                yield parse(line)
            try:
                self.sock.settimeout(max(0.1, deadline - time.time()))
                data = self.sock.recv(4096)
            except socket.timeout:
                break
            if not data:
                raise SystemExit("server closed the connection")
            self.buf += data
        raise SystemExit("timed out")

    def wait(self, pred, timeout=TIMEOUT):
        for m in self.lines(time.time() + timeout):
            if pred(m):
                return m

    def register(self, sasl=None):
        self.send("CAP LS 302")
        self.send("NICK " + self.nick)
        self.send("USER smoke 0 * :Boson ircds smoke")
        done = False
        for m in self.lines(time.time() + TIMEOUT):
            cmd, params = m[1], m[2]
            if cmd == "CAP" and len(params) > 2 and params[1] == "LS":
                for c in params[-1].split():
                    k, _, v = c.partition("=")
                    self.caps[k] = v
                if params[2] != "*":
                    if sasl and "sasl" in self.caps:
                        self.send("CAP REQ :sasl")
                    else:
                        self.send("CAP END")
            elif cmd == "CAP" and len(params) > 1 and params[1] == "ACK":
                self.send("AUTHENTICATE PLAIN")
            elif cmd == "AUTHENTICATE" and params and params[0] == "+":
                user, pw = sasl
                self.send("AUTHENTICATE " + base64.b64encode(f"{user}\0{user}\0{pw}".encode()).decode())
            elif cmd in ("903", "904", "905", "906", "908"):
                if cmd != "903":
                    raise SystemExit(f"SASL failed: {cmd} {params}")
                print("ok: SASL PLAIN login (903)")
                self.send("CAP END")
            elif cmd == "433":
                raise SystemExit(f"nick {self.nick} is in use")
            elif cmd == "001":
                done = True
            elif cmd in ("376", "422") and done:
                return
            elif cmd == "ERROR":
                raise SystemExit("ERROR " + " ".join(params))

    def ns(self, text):
        self.send("PRIVMSG NickServ :" + text)

    def quit(self):
        try:
            self.send("QUIT :smoke done")
            self.sock.close()
        except OSError:
            pass


def parse(line):
    tags, src = "", ""
    if line.startswith("@"):
        tags, _, line = line.partition(" ")
    if line.startswith(":"):
        src, _, line = line.partition(" ")
    trailing = None
    if " :" in line:
        line, _, trailing = line.partition(" :")
    parts = line.split()
    params = parts[1:] + ([trailing] if trailing is not None else [])
    return (src[1:], parts[0].upper() if parts else "", params)


def from_nickserv(m):
    return m[1] == "NOTICE" and m[0].lower().startswith("nickserv!")


def rpc(url, method, params, token=None):
    body = json.dumps({"jsonrpc": "2.0", "id": "smoke", "method": method, "params": params}).encode()
    req = urllib.request.Request(url, data=body, headers={"Content-Type": "application/json"})
    if token:
        # Anope 2.1 base64-decodes the bearer token.
        req.add_header("Authorization", "Bearer " + base64.b64encode(token.encode()).decode())
    with urllib.request.urlopen(req, timeout=TIMEOUT) as r:
        return json.load(r)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--services", choices=["anope", "atheme", "none"], required=True)
    ap.add_argument("--nick", default="smk" + secrets.token_hex(3))
    ap.add_argument("--password", default="smoke-" + secrets.token_hex(6))
    ap.add_argument("--rpc-url")
    ap.add_argument("--rpc-token", default="fixture-rpc-token")
    a = ap.parse_args()

    c = Client(a.host, a.port, a.nick)
    c.register()
    print("ok: registered as", a.nick, "caps:", " ".join(sorted(c.caps)) or "(none)")

    if a.services == "none":
        c.send("WHOIS NickServ")
        m = c.wait(lambda m: m[1] in ("311", "401", "318"))
        if m[1] == "311":
            raise SystemExit("NickServ exists on a pairing without services")
        print("ok: no NickServ")
        c.quit()
        return

    c.ns(f"REGISTER {a.password} {a.nick}@example.org")
    m = c.wait(lambda m: from_nickserv(m) and any(w in m[2][-1].lower() for w in ("registered", "already", "please wait")))
    text = m[2][-1].lower()
    if "please wait" in text:
        raise SystemExit("NickServ: " + m[2][-1])
    print("ok: NickServ REGISTER:", m[2][-1])
    c.quit()

    c = Client(a.host, a.port, a.nick)
    c.register()
    c.ns(f"IDENTIFY {a.password}")
    m = c.wait(lambda m: (from_nickserv(m) and any(w in m[2][-1].lower() for w in ("recognized", "now logged in", "identified", "password accepted", "invalid", "incorrect"))) or m[1] == "900")
    if m[1] != "900" and any(w in m[2][-1].lower() for w in ("invalid", "incorrect")):
        raise SystemExit("IDENTIFY failed: " + m[2][-1])
    print("ok: NickServ IDENTIFY")
    c.quit()

    if "sasl" in c.caps:
        c = Client(a.host, a.port, a.nick)
        c.register(sasl=(a.nick, a.password))
        c.quit()

    if a.rpc_url:
        if a.services == "anope":
            out = rpc(a.rpc_url, "anope.checkCredentials", [a.nick, a.password], a.rpc_token)
            assert "result" in out and out["result"].get("account", "").lower() == a.nick.lower(), out
            bad = rpc(a.rpc_url, "anope.checkCredentials", [a.nick, "wrong-password"], a.rpc_token)
            assert bad.get("error", {}).get("code") == -32098, bad
            noauth = rpc(a.rpc_url, "anope.checkCredentials", [a.nick, a.password])
            assert "error" in noauth, noauth
        else:
            out = rpc(a.rpc_url, "atheme.login", [a.nick, a.password, "smoke"])
            assert isinstance(out.get("result"), str) and out["result"], out
            rpc(a.rpc_url, "atheme.logout", [out["result"], a.nick])
            bad = rpc(a.rpc_url, "atheme.login", [a.nick, "wrong-password", "smoke"])
            assert "error" in bad, bad
        print("ok: services RPC accepts the account,", "refuses a wrong password")
    print("SMOKE OK")


if __name__ == "__main__":
    try:
        main()
    except SystemExit as e:
        if e.code not in (None, 0):
            print("SMOKE FAILED:", e.code, file=sys.stderr)
            sys.exit(1)
