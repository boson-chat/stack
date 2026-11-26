# The federation e2e's QUIC voice client (main.go), built by run.sh.
FROM golang:1.27.4-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /fedvoice .

FROM busybox:1.37.0
COPY --from=build /fedvoice /fedvoice
ENTRYPOINT ["/fedvoice"]
