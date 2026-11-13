# Runs the boson API's scheduled jobs (cron --mode=verify|health) with
# busybox crond. The binary is copied from the same backend image the api
# service runs, so both always match: build with --build-arg API_IMAGE=...
ARG API_IMAGE=harbor.floret.dev/boson/backend:0.43.0
FROM ${API_IMAGE} AS api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=api /app/main /app/main
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
