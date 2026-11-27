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

# The stack the wizard installs: the release bundle of this commit
# (scripts/release/bundle.sh, docs/updates.md "Stack releases"). A release
# build passes its version (STACK_VERSION, STACK_RELEASE=true: the images
# this repository builds pinned to it); other builds are 0.0.0-dev with
# main's tags.
FROM alpine:3.22 AS stackfiles
ARG STACK_VERSION=0.0.0-dev
ARG STACK_RELEASE=false
ARG STACK_COMMIT=unknown
COPY . /src/
RUN set -eu; \
    flag=; [ "$STACK_RELEASE" != true ] || flag=--release; \
    sh /src/scripts/release/bundle.sh --out /stack --version "$STACK_VERSION" --commit "$STACK_COMMIT" $flag

FROM alpine:3.22
# The docker CLI and its compose plugin, for the host's Docker socket.
RUN apk add --no-cache ca-certificates docker-cli docker-cli-compose
COPY --from=build /out/stack-setup /usr/local/bin/stack-setup
COPY --from=stackfiles /stack/ /usr/share/boson-stack/
EXPOSE 8443
ENTRYPOINT ["/usr/local/bin/stack-setup"]
