# Multi-stage build → small image with bash (needed for local PTY sessions) and
# an SSH client (useful to commands run inside sessions). Termada's own remote
# backend uses x/crypto/ssh. Static CGO-free binary.
FROM golang:1.26.5-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/termada ./cmd/termada

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates openssh-client tini \
 && groupadd --gid 10001 termada \
 && useradd --uid 10001 --gid termada --home-dir /home/termada --create-home --shell /bin/bash termada \
 && mkdir -p /home/termada/.config/termada \
 && chown -R termada:termada /home/termada/.config \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/termada /usr/local/bin/termada
ENV HOME=/home/termada
WORKDIR /home/termada
USER termada
# dashboard. Bind 0.0.0.0 INSIDE the container — the default 127.0.0.1 only
# listens on the container's loopback and is unreachable from the host. Map it to
# the host's loopback so it isn't exposed on the network:
#   docker run -p 127.0.0.1:7717:7717 ghcr.io/islomzoda/termada
# The daemon still enforces a loopback Host/Origin check + token, so a 0.0.0.0
# bind reached via a non-loopback host is rejected.
EXPOSE 7717
ENTRYPOINT ["/usr/bin/tini", "--", "termada"]
CMD ["serve", "--bind", "0.0.0.0:7717"]
