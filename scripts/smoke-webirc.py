#!/usr/bin/env python3
"""Smoke test for the web profile's IRC gateway (docs/web.md), through Traefik
like a browser: wss://WEB_HOST/webirc/websocket/.

Run against a local stack (scripts/setup.sh --local, WEB_HOST=chat.localhost,
profiles irc + auth + web) whose gateway has WEBIRC_MAX_PER_IP=3 and
WEBIRC_RATE_PER_MIN=8 (CI sets them; every connection here comes from one
address, Traefik's view of the host):

1. HOST <IRC_HOST>:+6697 (the alias to ergo:6680) registers (001) and Ergo
   sees the address the gateway passed in WEBIRC, not the gateway's own.
2. Twenty JOINs after registration go through in well under 10 s.
3. Refused before dialing, each with "ERROR :Gateway refused (<code>)":
   a non-IRC port (port), loopback (address), a compose service by name
   (address), no HOST at all (host).
4. A page from another origin can't open the socket.
5. Per-IP caps: a 4th concurrent connection is refused (limit); past 8 new
   connections a minute, the next is refused (rate).

  scripts/smoke-webirc.py [--web-host chat.localhost] [--irc-host irc.localhost]
"""
import argparse, base64, os, secrets, socket, ssl, struct, sys, time

p = argparse.ArgumentParser()
p.add_argument("--addr", default="127.0.0.1")
p.add_argument("--web-host", default="chat.localhost")
p.add_argument("--irc-host", default="irc.localhost")
p.add_argument("--gateway-ip", required=True, help="the gateway container's address (Ergo must not see it)")
p.add_argument("--ca", default=os.path.join(os.path.dirname(__file__), "..", "local-certs", "ca.pem"))
args = p.parse_args()
ctx = ssl.create_default_context(cafile=args.ca)
ORIGIN = "https://" + args.web_host

failures = []
def check(name, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + name + (": " + detail if detail else ""), flush=True)
    if not ok:
        failures.append(name)


class WS:
    """A minimal RFC 6455 client: text frames, masked, no extensions."""

    def __init__(self, origin=ORIGIN):
        raw = socket.create_connection((args.addr, 443), timeout=15)
        self.s = ctx.wrap_socket(raw, server_hostname=args.web_host)
        key = base64.b64encode(secrets.token_bytes(16)).decode()
        self.s.sendall((
            f"GET /webirc/websocket/ HTTP/1.1\r\nHost: {args.web_host}\r\nUpgrade: websocket\r\n"
            f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
            f"Origin: {origin}\r\n\r\n").encode())
        head = b""
        while b"\r\n\r\n" not in head:
            b = self.s.recv(1)
            if not b:
                break
            head += b
        self.status = int(head.split(b" ", 2)[1]) if head.startswith(b"HTTP/") else 0
        self.buf = b""
        self.closed = False

    def send(self, line):
        data = line.encode()
        mask = secrets.token_bytes(4)
        n = len(data)
        hdr = bytes([0x81]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack(">H", n))
        self.s.sendall(hdr + mask + bytes(c ^ mask[i % 4] for i, c in enumerate(data)))

    def _read(self, n):
        while len(self.buf) < n:
            b = self.s.recv(65536)
            if not b:
                raise EOFError
            self.buf += b
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def recv(self):
        """The next text message, or None once the server closed."""
        if self.closed:
            return None
        try:
            while True:
                b0, b1 = self._read(2)
                n = b1 & 0x7F
                if n == 126:
                    n = struct.unpack(">H", self._read(2))[0]
                elif n == 127:
                    n = struct.unpack(">Q", self._read(8))[0]
                payload = self._read(n)
                op = b0 & 0x0F
                if op == 0x8:
                    self.closed = True
                    return None
                if op in (0x1, 0x2):
                    return payload.decode(errors="replace")
        except (EOFError, OSError, ssl.SSLError):
            self.closed = True
            return None

    def lines_until(self, pred, timeout=15):
        """Messages (split into lines) until pred(line) or the socket closes."""
        self.s.settimeout(timeout)
        seen = []
        deadline = time.time() + timeout
        while time.time() < deadline:
            m = self.recv()
            if m is None:
                return seen, None
            for line in m.split("\n"):
                line = line.strip("\r")
                if not line:
                    continue
                seen.append(line)
                if line.startswith("PING "):
                    self.send("PONG " + line[5:])
                if pred(line):
                    return seen, line
        return seen, None

    def close(self):
        try:
            self.s.close()
        except OSError:
            pass


def register(ws, host_line):
    nick = "w" + secrets.token_hex(4)
    ws.send(host_line)
    ws.send(f"NICK {nick}")
    ws.send(f"USER {nick} 0 * :web smoke")
    return nick


def refusal(host_line):
    """The ERROR the gateway answers with (or the welcome, if it let us in)."""
    ws = WS()
    if ws.status != 101:
        return f"handshake {ws.status}"
    register(ws, host_line) if host_line else (ws.send("NICK x" + secrets.token_hex(3)), ws.send("USER x 0 * :x"))
    seen, hit = ws.lines_until(lambda l: l.startswith("ERROR") or " 001 " in l, timeout=20)
    ws.close()
    return hit or ("no ERROR; got " + " | ".join(seen[-3:]))


# 1 + 2: our own network through the alias, with WEBIRC.
main = WS()
check("WebSocket handshake through Traefik", main.status == 101, f"HTTP {main.status}")
nick = register(main, f"HOST {args.irc_host}:+6697")
seen, welcome = main.lines_until(lambda l: " 001 " in l or l.startswith("ERROR"), timeout=30)
check("registered on Ergo through the gateway alias (001)", bool(welcome and " 001 " in welcome), welcome or " | ".join(seen[-3:]))
main.send(f"WHOIS {nick}")
seen, _ = main.lines_until(lambda l: " 318 " in l, timeout=10)
actual = [l for l in seen if " 338 " in l or " 378 " in l]
# Ergo's view of where we connect from: with WEBIRC it's the address the
# gateway saw (Traefik's view of us), without it the gateway's own address.
check("Ergo got the client address through WEBIRC",
      bool(actual) and not any(f" {args.gateway_ip} " in l or f"@{args.gateway_ip} " in l for l in actual),
      " | ".join(actual) or "no 338 in WHOIS")
# One JOIN line per channel would mostly measure Ergo's own fakelag (5 lines,
# then 2 a second, for every client); a list measures the gateway's path.
t0 = time.time()
chans = [f"#web-{secrets.token_hex(3)}-{i}" for i in range(20)]
main.send("JOIN " + ",".join(chans))
joined = set()
def all_joined(l):
    parts = l.split(" ")
    if len(parts) >= 3 and parts[1] == "JOIN":
        joined.add(parts[2].lstrip(":"))
    return len(joined) == len(chans)
main.lines_until(all_joined, timeout=12)
dt = time.time() - t0
check("20 channels joined after registration in under 10 s", len(joined) == len(chans) and dt < 10, f"{len(joined)} joined in {dt:.1f}s")

# 3: refusals (each a short-lived connection; main stays open).
for name, line, code in [
    ("non-IRC port", "HOST 127.0.0.1:22", "port"),
    ("loopback", "HOST 127.0.0.1:6667", "address"),
    ("compose service by name", "HOST ergo:6697", "address"),
]:
    got = refusal(line)
    check(f"refused: {name}", got.startswith(f"ERROR :Gateway refused ({code})"), got)
# No HOST: webircgateway's own refusal (nothing is configured to fall back to).
got = refusal(None)
check("refused: no HOST", got.startswith("ERROR :"), got)

# 4: origins.
other = WS(origin="https://evil.example")
check("another origin can't open the socket", other.status != 101, f"HTTP {other.status}")
other.close()

# 5: per-IP caps. main is 1 of 3; the refusals above were 4 of the 8 a minute.
extra = [WS(), WS()]
check("2nd and 3rd concurrent connections accepted", all(w.status == 101 for w in extra))
for w in extra:
    register(w, f"HOST {args.irc_host}:+6697")
time.sleep(1)
got = refusal(f"HOST {args.irc_host}:+6697")
check("4th concurrent connection refused (limit)", got.startswith("ERROR :Gateway refused (limit)"), got)
for w in extra:
    w.close()
main.close()
time.sleep(2)  # the gateway notices the closes
# New connections refill at 8 a minute (one per 7.5 s) up to a burst of 8:
# opening them back to back, one is refused before a dozen get through.
results = []
for _ in range(12):
    got = refusal(f"HOST {args.irc_host}:+6697 ")
    results.append(got)
    if "(rate)" in got:
        break
check("new connections past the per-minute rate refused (rate)", "(rate)" in results[-1] and len(results) < 12,
      f"{len(results)} tries, last: {results[-1]}")

if failures:
    print(f"{len(failures)} check(s) failed: {', '.join(failures)}")
    sys.exit(1)
print("web gateway smoke OK")
