# syntax=docker/dockerfile:1
# webircgateway (https://github.com/kiwiirc/webircgateway): WebSocket in from
# the Boson web client, TCP/TLS out to IRC networks (docs/web.md). Built from
# a pinned commit with:
#   patches/  — verify upstream TLS certificates, refuse internal addresses at
#               dial time, take the client IP from the right of
#               X-Forwarded-For, read whole WebSocket messages, a throttle burst,
#               QUIT and close the network connection as soon as the client goes
#   src/      — zz_boson_policy.go: IRC ports only, aliases, allow/deny lists,
#               per-IP caps, WEBIRC passwords (all from the environment);
#               zz_boson_close.go: leaving the network when the client closes
# entrypoint.sh renders the config file from the environment.
ARG WEBIRCGATEWAY_COMMIT=be9fd5be5d995184d61fac64fc1742ceca25e05f

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache git
ARG WEBIRCGATEWAY_COMMIT
RUN git clone -q https://github.com/kiwiirc/webircgateway.git /src/wig \
 && git -C /src/wig checkout -q "$WEBIRCGATEWAY_COMMIT"
WORKDIR /src/wig
COPY patches/ /patches/
RUN for p in /patches/*.patch; do echo "applying $p"; git apply --whitespace=nowarn "$p" || exit 1; done
COPY src/ pkg/webircgateway/
# The policy's unit tests run in the build: an image is never built from a
# policy that fails them.
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go test -count=1 ./pkg/webircgateway/
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.GITCOMMIT=$WEBIRCGATEWAY_COMMIT" -o /out/webircgateway .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 wig && adduser -D -H -u 10001 -G wig wig \
 && mkdir -p /etc/webircgateway && chown wig:wig /etc/webircgateway
COPY --from=build /out/webircgateway /usr/local/bin/webircgateway
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
USER wig
# 8080: HTTP (WebSocket at /webirc/websocket/, health at /webirc/info);
# Traefik terminates TLS in front of it.
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:8080/webirc/info >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
