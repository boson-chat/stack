# syntax=docker/dockerfile:1
# Ergo (https://github.com/ergochat/ergo) plus boson-ergo: renders ircd.yaml
# from the environment and checks SASL logins against the boson auth service.
ARG ERGO_VERSION=v2.19.1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS helper
WORKDIR /src
COPY helper/ ./
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/boson-ergo .

FROM ghcr.io/ergochat/ergo:${ERGO_VERSION}
# Run unprivileged (upstream runs as root). uid 10000 matches the old
# UnrealIRCd image, so the certs_irc volume contract is unchanged.
RUN addgroup -g 10000 ergo && adduser -D -H -u 10000 -G ergo -h /ircd ergo \
 && chown -R ergo:ergo /ircd
COPY --from=helper /out/boson-ergo /usr/local/bin/boson-ergo
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
USER ergo
WORKDIR /ircd
# 6697 TLS, 6667 plaintext (IRC_PLAINTEXT=true), 8097 WebSocket (behind Traefik)
EXPOSE 6697 6667 8097
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/ircd-bin/ergo", "run", "--conf", "/ircd/ircd.yaml"]
