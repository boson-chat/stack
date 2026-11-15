# syntax=docker/dockerfile:1
# Ergo (https://github.com/ergochat/ergo) built from source with the patches
# in patches/ (account-bound operator status and vhosts from the auth-script,
# see docs/roles.md; channel auto-registration and expiry, and account
# removal over the API, see docs/irc-channels.md; account extbans and the
# channel access / sajoin API for role channels, see docs/roles.md), plus boson-ergo: renders ircd.yaml from the environment,
# checks SASL logins against the boson auth service, syncs roles and runs
# RoleServ.
ARG ERGO_VERSION=v2.19.1
# The commit ERGO_VERSION must resolve to (tags can move; this can't).
ARG ERGO_COMMIT=63c743a70644f0f19109508ade8580bf37f7d23d

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS ergo
RUN apk add --no-cache git
ARG ERGO_VERSION ERGO_COMMIT
RUN git clone -q --depth 1 --branch "$ERGO_VERSION" https://github.com/ergochat/ergo.git /src/ergo \
 && test "$(git -C /src/ergo rev-parse HEAD)" = "$ERGO_COMMIT" \
 || { echo "ergo $ERGO_VERSION is not commit $ERGO_COMMIT" >&2; exit 1; }
WORKDIR /src/ergo
COPY patches/ /patches/
RUN for p in /patches/*.patch; do echo "applying $p"; git apply --whitespace=nowarn "$p" || exit 1; done
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags "i18n sqlite" \
    -ldflags="-s -w -X main.commit=$ERGO_COMMIT -X main.version=$ERGO_VERSION" -o /out/ergo .

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS helper
WORKDIR /src
COPY helper/ ./
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/boson-ergo .

# Upstream's image for the layout (/ircd-bin with languages/, alpine); its
# ergo binary is replaced by the patched build.
FROM ghcr.io/ergochat/ergo:${ERGO_VERSION}
# Run unprivileged (upstream runs as root). uid 10000 matches the old
# UnrealIRCd image, so the certs_irc volume contract is unchanged.
# /roleserv: RoleServ's state (role channels it locked), a volume in compose.
RUN addgroup -g 10000 ergo && adduser -D -H -u 10000 -G ergo -h /ircd ergo \
 && chown -R ergo:ergo /ircd && mkdir -p /roleserv && chown ergo:ergo /roleserv
COPY --from=ergo /out/ergo /ircd-bin/ergo
COPY --from=helper /out/boson-ergo /usr/local/bin/boson-ergo
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
USER ergo
WORKDIR /ircd
# 6697 TLS, 6667 plaintext (IRC_PLAINTEXT=true), 8097 WebSocket (behind
# Traefik), 6680 plaintext for in-stack services (RoleServ; never published),
# 8098 boson-ergo's config status (compose network only; never published)
EXPOSE 6697 6667 8097 6680 8098
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["/ircd-bin/ergo", "run", "--conf", "/ircd/ircd.yaml"]
