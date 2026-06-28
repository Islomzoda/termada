# Multi-stage build → small image with bash (needed for local PTY sessions) and
# an SSH client (for fleet/remote). Static CGO-free binary.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/termada ./cmd/termada

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates openssh-client tini \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/termada /usr/local/bin/termada
# dashboard. Bind 0.0.0.0 INSIDE the container — the default 127.0.0.1 only
# listens on the container's loopback and is unreachable from the host. Map it to
# the host's loopback so it isn't exposed on the network:
#   docker run -p 127.0.0.1:7717:7717 ghcr.io/islomzoda/termada
# The daemon still enforces a loopback Host/Origin check + token, so a 0.0.0.0
# bind reached via a non-loopback host is rejected.
EXPOSE 7717
ENTRYPOINT ["/usr/bin/tini", "--", "termada"]
CMD ["serve", "--bind", "0.0.0.0:7717"]
