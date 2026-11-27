# syntax=docker/dockerfile:1
# stack-telemetry: telemetryd, Boson's anonymous usage and health stats
# receiver and the owner's panel (docs/telemetry.md). Only boson.chat runs
# it (the telemetry profile). Build from this directory:
#   docker build -t stack-telemetry images/telemetry
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
COPY --from=web /web/dist/ ./web/dist/
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/telemetryd . && mkdir -p /out/data

# The country database is not in the image: telemetryd downloads DB-IP
# Lite (CC BY 4.0) into /data on first start and refreshes it monthly.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/telemetryd /usr/local/bin/telemetryd
# A new named volume takes this directory's owner (the nonroot user).
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
EXPOSE 8080 8081
VOLUME /data
ENTRYPOINT ["/usr/local/bin/telemetryd"]
CMD ["serve"]
