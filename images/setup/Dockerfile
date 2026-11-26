# syntax=docker/dockerfile:1
# stack-setup: Boson's web setup wizard (install.sh starts it). Build from
# the repo root, it carries this repo's stack files:
#   docker build -f images/setup/Dockerfile -t stack-setup .
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
# The probe module (go.mod: replace => ../../pkg/probe, so /pkg/probe here).
COPY pkg/probe/ /pkg/probe/
COPY images/setup/go.mod images/setup/go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY images/setup/ ./
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/stack-setup .

# The stack the wizard installs: what a clone of this repo has, minus tests.
FROM alpine:3.22 AS stackfiles
COPY compose.yaml compose.video-node.yaml .env.example README.md LICENSE NOTICE /stack/
COPY config/boson.example.yaml /stack/config/
COPY config/branding/.gitkeep /stack/config/branding/
COPY traefik/dynamic/stack.yml traefik/dynamic/directory.yml traefik/dynamic/README.md /stack/traefik/dynamic/
COPY postgres/ /stack/postgres/
COPY monitoring/ /stack/monitoring/
COPY scripts/ /stack/scripts/
COPY docs/ /stack/docs/
COPY images/certsync/ /stack/images/certsync/
COPY images/ergo/ /stack/images/ergo/
COPY images/webircgateway/ /stack/images/webircgateway/
COPY images/wellknown/ /stack/images/wellknown/
COPY images/bridgesync/ /stack/images/bridgesync/
RUN rm -rf /stack/scripts/__pycache__

FROM alpine:3.22
# The docker CLI and its compose plugin, for the host's Docker socket.
RUN apk add --no-cache ca-certificates docker-cli docker-cli-compose
COPY --from=build /out/stack-setup /usr/local/bin/stack-setup
COPY --from=stackfiles /stack/ /usr/share/boson-stack/
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/stack-setup"]
