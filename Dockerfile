# ─────────────────────────────────────────────────────────────
# Sky Language — Dockerfile (self-hosted, pure Go)
#
# Builds the sky compiler from Go source and produces a slim
# image with Go ready for compiling and running Sky projects.
#
# Usage:
#   docker build -t sky .
#   docker run --rm -v $(pwd)/my-app:/app -w /app sky sky build src/Main.sky
# ─────────────────────────────────────────────────────────────

# ── Stage 1: Build the Sky compiler ─────────────────────────
FROM golang:1.24-bookworm AS builder

WORKDIR /sky
COPY sky-out/ ./sky-out/

RUN cd sky-out && go build -ldflags="-s -w" -o /usr/local/bin/sky main.go

# ── Stage 2: Runtime image ──────────────────────────────────
FROM golang:1.24-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /usr/local/bin/sky /usr/local/bin/sky

RUN sky --version

WORKDIR /app

ENTRYPOINT []
CMD ["sky", "--help"]
