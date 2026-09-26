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
FROM debian:stable-slim
# CA certificates for the HTTPS API calls (openFDA, ClinicalTrials.gov, PubMed).
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /app/mdi .
# The serve command listens on every interface, on $PORT when it is set and
# on 8080 otherwise. On the Hetzner box (pubvera-01) docker-compose.yml sets
# PORT=8092 and publishes it only on the host's 127.0.0.1:8092, behind Caddy.
# This comment used to say Render provides PORT; that was true before the move.
CMD ["./mdi", "serve"]
