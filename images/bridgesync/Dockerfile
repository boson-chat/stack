# syntax=docker/dockerfile:1
# bridge-sync (docs/bridge.md): Boson accounts → the bridge's accounts.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY *.go ./
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bridge-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bridge-sync /bridge-sync
ENTRYPOINT ["/bridge-sync"]
