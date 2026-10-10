# Stage 1: build
FROM golang:1.26 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO off: the SQLite driver (modernc.org/sqlite) is pure Go, so the binary
# is fully static and portable into the slim runtime image.
RUN CGO_ENABLED=0 go build -o mdi ./cmd/medical-device-intelligence-pp-cli

# Stage 2: minimal runtime
FROM debian:trixie-slim
# CA certificates for the HTTPS API calls (openFDA, ClinicalTrials.gov, PubMed).
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
# The unprivileged user the server runs as (see USER below). UID 10001 is the
# same as pubvera-bibliovera and pubvera-grantvera, so host-side checks use one
# number everywhere.
RUN useradd --uid 10001 --create-home --shell /usr/sbin/nologin app
WORKDIR /app
COPY --from=builder /app/mdi .
# The serve command listens on every interface, on $PORT when it is set and
# on 8080 otherwise. On the Hetzner box (pubvera-01) docker-compose.yml sets
# PORT=8092 and publishes it only on the host's 127.0.0.1:8092, behind Caddy.
# This comment used to say Render provides PORT; that was true before the move.
# The server ran as root (UID 0, measured with docker top on 2026-10-10). The
# container has no mounts and writes nothing (docker diff = 0 lines), and /app
# is root-owned and world-readable, which is enough for this user. CI checks
# the UID of PID 1 in the running image. Ports above 1024 need no privileges.
USER app
CMD ["./mdi", "serve"]
