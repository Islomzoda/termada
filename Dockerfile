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
# dashboard
EXPOSE 7717
ENTRYPOINT ["/usr/bin/tini", "--", "termada"]
CMD ["serve"]
