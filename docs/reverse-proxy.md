# Behind a reverse proxy you already run

Boson's Traefik normally has ports 80 and 443 to itself. If this server
already runs a reverse proxy there for other sites (a Traefik, nginx, Caddy
or HAProxy), Boson goes behind it instead:

- Boson's Traefik publishes on two private addresses (default
  `127.0.0.1:8081` for HTTP and `127.0.0.1:8444` for HTTPS).
- Your proxy **passes TLS for Boson's names through untouched** (routed by
  SNI, never terminated: IRC on 6697 and voice use the same certificates,
  which Boson's Traefik gets from Let's Encrypt) **with a PROXY protocol v2
  header**, so Boson's Traefik, and auth, the api, files, the web gateway
  and everything else behind it, see each visitor's own address exactly as
  on a direct install: sign-in rate limits, bans, logs, WEBIRC.
- Your proxy forwards **port 80** for Boson's names to Boson's HTTP address
  (Let's Encrypt's HTTP-01 checks and the redirect to https).

Ports 6697/tcp, 7700/udp and 7710/udp stay Boson's own, published directly.

The setup wizard does this for you: on the TLS step (or the Firewall and
ports step, when 80/443 are taken) say you already run a reverse proxy. It
finds a proxy container on 80/443 and suggests addresses, checks Boson's
ports instead of 80/443, and shows the snippets below with your install's
names. It never edits your proxy; the install checks the result through it
(a proxy that isn't set up yet fails the "Check it all works" step with a
pointer to the snippets). The rest of this page is for setting it up by hand
or moving an existing install.

## The .env keys

| Key | Default | Behind a proxy |
|---|---|---|
| `TRAEFIK_HTTP_PUBLISH` | `80` | `127.0.0.1:8081`, or a Docker network gateway (below) |
| `TRAEFIK_HTTPS_PUBLISH` | `443` | `127.0.0.1:8444`, or a Docker network gateway |
| `TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS` | empty (off) | `10.203.53.1`: the first address of `STACK_SUBNET` |

`TRAEFIK_*_PUBLISH` are compose's `[ip:]port` (host side of `ports:`).
`TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS` (IPs or CIDRs, comma-separated) turns on
PROXY protocol on both of Traefik's entrypoints, believed only from those
addresses, and keeps `X-Forwarded-For` on port 80 from them
(`--entrypoints.web.forwardedheaders.trustedips`). Empty, the flags aren't
passed at all and Traefik behaves as before. A connection from anywhere
else, or one without a header, is taken as it is (so the wizard's own
checks, and `curl` on the server, still work); a header from an address
that isn't trusted is ignored, never believed.

**Which address to trust.** Not your proxy's own: Docker hands Boson's
Traefik every connection to a port it publishes from the **gateway of
Boson's own network**, the first address of `STACK_SUBNET`
(`10.203.53.1` by default), whether the proxy runs on the host or in a
container on another Docker network (Docker's userland proxy or its NAT
rewrites the source; checked on Docker 29, with and without
`userland-proxy`). That's the address to trust. Add your proxy's address
only if it runs on another machine and reaches Boson over the network.

**Which address to publish on.** A proxy running directly on the server
reaches `127.0.0.1`. A proxy in a container can't (its `127.0.0.1` is
itself): publish on the gateway of a Docker network that container is on,
this server's address on that network:

```sh
docker network inspect proxy -f '{{(index .IPAM.Config 0).Gateway}}'   # e.g. 172.18.0.1
```

## The snippets

The wizard shows these with your names and addresses filled in. Here for
`example.com` with the default features (names `irc.`, `auth.`, `chat.`,
`admin.`, `api.`, `voice.`, `files.`; add `video.`, `directory.` or the bare
domain if you run those) and Boson on `127.0.0.1:8081` / `127.0.0.1:8444`.
They list Boson's names one by one rather than `*.example.com`, so your
other sites under the same domain stay yours. When you turn a feature on
later, add its name. `images/setup/cmd/proxysnippets` prints them for any
domain and addresses:

```sh
cd images/setup && go run ./cmd/proxysnippets -software nginx -domain example.com \
    -http 127.0.0.1:8081 -https 127.0.0.1:8444 -out /tmp/boson-snippets
```

The Traefik, nginx and HAProxy ones are tested in CI against the real
proxies (`test/proxy/run.sh`, below).

### Traefik

A file in its file-provider directory:

```yaml
# Boson (example.com): TLS for its names passes through untouched, with a PROXY
# protocol v2 header so Boson sees each visitor's own address; port 80 for
# them goes to Boson too (Let's Encrypt's checks). Use your entrypoint names.
tcp:
  routers:
    boson-example-com:
      entryPoints: [websecure]
      rule: "HostSNI(`irc.example.com`) || HostSNI(`auth.example.com`) || HostSNI(`chat.example.com`) || HostSNI(`admin.example.com`) || HostSNI(`api.example.com`) || HostSNI(`voice.example.com`) || HostSNI(`files.example.com`)"
      tls:
        passthrough: true
      service: boson-example-com
  services:
    boson-example-com:
      loadBalancer:
        proxyProtocol:
          version: 2
        servers:
          - address: "127.0.0.1:8444"
http:
  routers:
    boson-example-com-http:
      entryPoints: [web]
      rule: "Host(`irc.example.com`) || Host(`auth.example.com`) || Host(`chat.example.com`) || Host(`admin.example.com`) || Host(`api.example.com`) || Host(`voice.example.com`) || Host(`files.example.com`)"
      # Above your own routers for these names.
      priority: 1000
      service: boson-example-com-http
  services:
    boson-example-com-http:
      loadBalancer:
        servers:
          - url: "http://127.0.0.1:8081"
```

Two things in your Traefik's static config can get in the way:

- An http→https redirect on the entrypoint
  (`entryPoints.web.http.redirections`) has the highest priority and takes
  Boson's port 80 too: set `priority: 1` on it
  (`--entrypoints.web.http.redirections.entrypoint.priority=1`). Your other
  sites still redirect; Boson's Traefik redirects its own names.
- If your Traefik gets its own certificates with the HTTP challenge, its
  challenge router answers every `/.well-known/acme-challenge/` request
  (with a 404 for tokens it doesn't know): set `allowACMEBypass: true` on
  that entrypoint.

Traefik ≥ 3.4 also accepts PROXY protocol on a TCP `serversTransport`
instead of the service's `proxyProtocol` (deprecated there, still working).

### nginx

Needs the stream module (built in on most packages; Debian/Ubuntu:
`libnginx-mod-stream`). nginx's `stream` takes over 443 and sends
everything that isn't Boson's to your own HTTPS sites, which move to
`127.0.0.1:4443` with the same header.

At the top level of `nginx.conf` (next to `http { }`):

```nginx
# Boson (example.com): nginx reads the name a visitor asks for (SNI) and passes
# Boson's TLS through untouched, with a PROXY protocol header so Boson sees
# the visitor's own address. Everything else goes to your own HTTPS sites,
# which now listen on 127.0.0.1:4443 instead of 443 (see below).
stream {
    map $ssl_preread_server_name $boson_upstream {
        irc.example.com              boson_example_com;
        auth.example.com             boson_example_com;
        chat.example.com             boson_example_com;
        admin.example.com            boson_example_com;
        api.example.com              boson_example_com;
        voice.example.com            boson_example_com;
        files.example.com            boson_example_com;
        default                      own_https;
    }
    upstream boson_example_com { server 127.0.0.1:8444; }
    upstream own_https { server 127.0.0.1:4443; }
    server {
        listen 443;
        listen [::]:443;
        ssl_preread on;
        proxy_pass $boson_upstream;
        proxy_protocol on;
    }
}
```

Inside `http { }`:

```nginx
# Boson (example.com): port 80 for Boson's names (Let's Encrypt's checks and
# the redirect to https).
server {
    listen 80;
    listen [::]:80;
    server_name irc.example.com
                auth.example.com
                chat.example.com
                admin.example.com
                api.example.com
                voice.example.com
                files.example.com;
    location / {
        proxy_pass http://127.0.0.1:8081;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

# Your own HTTPS sites: the stream block above has 443 now, and sends them
# here with the same header. In each of your server blocks, instead of
# "listen 443 ssl;":
#     listen 127.0.0.1:4443 ssl proxy_protocol;
#     set_real_ip_from 127.0.0.1;
#     real_ip_header proxy_protocol;
```

### HAProxy

```haproxy
# Boson (example.com): TLS for Boson's names passes through untouched with a
# PROXY protocol v2 header (Boson sees each visitor's own address); port 80
# for them goes to Boson too (Let's Encrypt's checks). If you have frontends
# on :443 and :80 already, add the use_backend lines to them instead.
frontend https_in
    bind :443
    mode tcp
    tcp-request inspect-delay 5s
    tcp-request content accept if { req_ssl_hello_type 1 }
    use_backend boson_example_com_https if { req_ssl_sni -i irc.example.com auth.example.com chat.example.com admin.example.com api.example.com voice.example.com files.example.com }
    # default_backend your_https

frontend http_in
    bind :80
    mode http
    use_backend boson_example_com_http if { req.hdr(host),field(1,:) -i irc.example.com auth.example.com chat.example.com admin.example.com api.example.com voice.example.com files.example.com }
    # default_backend your_http

backend boson_example_com_https
    mode tcp
    server boson 127.0.0.1:8444 send-proxy-v2

backend boson_example_com_http
    mode http
    option forwardfor
    server boson 127.0.0.1:8081
```

### Caddy

Plain Caddy can't pass TLS through untouched; it needs the
[caddy-l4](https://github.com/mholt/caddy-l4) plugin
(`xcaddy build --with github.com/mholt/caddy-l4`). Without it Caddy would
have to terminate TLS for Boson's names, which doesn't work for Boson (IRC
and voice need the certificates Boson's Traefik gets). This one isn't tested
in CI: check it against caddy-l4's documentation for your version.

```caddyfile
# Boson (example.com): needs Caddy built with the layer4 app
# (xcaddy build --with github.com/mholt/caddy-l4). On Caddy's own :443 it
# passes TLS for Boson's names through untouched, with a PROXY protocol v2
# header; everything else goes on to your sites as before.
{
	servers :443 {
		listener_wrappers {
			layer4 {
				@boson tls sni irc.example.com auth.example.com chat.example.com admin.example.com api.example.com voice.example.com files.example.com
				route @boson {
					proxy {
						proxy_protocol v2
						upstream tcp/127.0.0.1:8444
					}
				}
			}
			tls
		}
	}
}
```

Port 80 works with plain Caddy:

```caddyfile
# Boson (example.com): port 80 for Boson's names (Let's Encrypt's checks and the
# redirect to https). Plain Caddy can do this part.
http://irc.example.com, http://auth.example.com, http://chat.example.com, http://admin.example.com, http://api.example.com, http://voice.example.com, http://files.example.com {
	reverse_proxy 127.0.0.1:8081
}
```

### Anything else

Port 443: route Boson's names by SNI, as raw TCP (no TLS termination), to
Boson's HTTPS address, with a PROXY protocol header (v2 or v1) on each
connection. Port 80: forward HTTP for the same names to Boson's HTTP
address, keeping `Host`.

## Checking that visitors' addresses get through

1. Boson's Traefik has the flags:
   `docker compose exec traefik ps -o args | tr ' ' '\n' | grep proxyprotocol`
   (or `docker compose config traefik | grep trustedips`).
2. From another machine (your phone on mobile data is ideal), open
   `https://auth.<domain>/v1/config`, then look for that machine's address
   in auth's log, which records every request's client address:
   `docker compose logs --since 2m auth | grep '"path":"/v1/config"'`
   should show `"ip":"<your address>"`. Seeing `10.203.53.1` or your
   proxy's address instead means the header isn't sent (the snippet's
   `proxyProtocol` / `proxy_protocol` / `send-proxy-v2`) or isn't trusted
   (`TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS`).
3. Or echo the headers Boson's services get: run a `traefik/whoami`
   container with a router for a name your proxy passes through (as
   `test/proxy` does) and look at `X-Real-Ip` / `X-Forwarded-For`.

If TLS to Boson's names fails right after this change, the proxy sends a
header Traefik doesn't expect (trusted list empty or wrong while the proxy
sends PROXY) or the other way round. Traefik's log shows
`tls: first record does not look like a TLS handshake` for the first.

## Moving an install from a compose.override.yaml

Installs that were put behind a proxy by hand overrode Traefik's ports in
`compose.override.yaml` (`ports: !override` with e.g.
`172.18.0.1:8081:80` and `172.18.0.1:8444:443`) and had the outer proxy pass
TLS through without a header, so every visitor showed up as the proxy. To
move to the supported setup:

1. In `.env`, the same addresses, plus the trusted gateway:

   ```sh
   TRAEFIK_HTTP_PUBLISH=172.18.0.1:8081
   TRAEFIK_HTTPS_PUBLISH=172.18.0.1:8444
   TRAEFIK_PROXY_PROTOCOL_TRUSTED_IPS=10.203.53.1   # first address of STACK_SUBNET
   ```

2. Delete the `traefik:` ports override from `compose.override.yaml` (the
   whole file if that's all it had).
3. Apply: `docker compose up -d traefik` (only Traefik restarts).
4. Add the header on the outer proxy, e.g. for Traefik, on Boson's TCP
   service:

   ```yaml
   tcp:
     services:
       boson:
         loadBalancer:
           proxyProtocol:
             version: 2
           servers:
             - address: "172.18.0.1:8444"
   ```

   Do 3 and 4 back to back: between them TLS to Boson's names fails (Boson
   expects a header the proxy doesn't send yet; Traefik's file provider
   picks the change up within a second or two).
5. Check as above. While at it, if the outer router matched
   `HostSNIRegexp(...)` for the whole domain, the wizard's snippet lists
   Boson's names one by one instead, which leaves other sites under that
   domain to the outer proxy.

## The end-to-end test

`test/proxy/run.sh traefik|nginx|haproxy` (CI job `proxy`): an outer proxy
on "the internet's" 80/443 configured with exactly the wizard's snippets
(`cmd/proxysnippets`), Boson's Traefik on the outer proxy's Docker network
gateway, Pebble issuing every certificate over HTTP-01 through the outer
proxy. It checks a client's own address reaches a `whoami` behind Boson's
Traefik and in auth's log, http redirects to https, a spoofed PROXY header from an untrusted
address is ignored, Boson still answers on its own address without a
header, and (Traefik) the outer proxy's own site keeps working. Needs Go
and a `.env` from `scripts/setup.sh --local`.
