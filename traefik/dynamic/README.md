Traefik's file provider loads every file in this directory (and reloads on
change). `stack.yml` is part of the stack; put install-specific routers,
services and middlewares in your own `*.yml` files here (they are
gitignored). Files are Go templates, so `{{ env "IRC_HOST" }}` works for
variables passed to the `traefik` container.

Example (`example.yml`): serve another site through the stack's Traefik.

```yaml
http:
  routers:
    blog:
      rule: Host(`blog.example.com`)
      entryPoints: [websecure]
      service: blog
      tls:
        certResolver: le      # or add the host to CERT_HOSTS and use `tls: {}`
  services:
    blog:
      loadBalancer:
        servers:
          - url: http://10.0.0.5:8080
```

Containers can instead be added in `compose.override.yaml` with
`traefik.*` labels (`traefik.enable=true` is required; nothing is exposed
by default). The `webroot` service in `compose.yaml` is a small complete example.
