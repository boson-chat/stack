# certsync: exports Traefik's ACME certificates (acme.json) with
# traefik-certs-dumper and hands each service its own copy.
FROM ldez/traefik-certs-dumper:v2.11.4 AS dumper

FROM alpine:3.22
RUN apk add --no-cache bash coreutils findutils
COPY --from=dumper /usr/bin/traefik-certs-dumper /usr/local/bin/traefik-certs-dumper
COPY certsync.sh /usr/local/bin/certsync.sh
ENTRYPOINT ["/usr/local/bin/certsync.sh"]
