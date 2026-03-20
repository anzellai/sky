# ─────────────────────────────────────────────────────────────
# Sky Language — Multi-stage Dockerfile
#
# Builds the sky & sky-lsp binaries and produces a slim image
# with Node.js + Go ready for compiling and running Sky projects.
#
# Usage:
#   # Build the image
#   docker build -t sky .
#
#   # Run Sky commands
#   docker run --rm -v $(pwd)/my-app:/app -w /app sky sky build src/Main.sky
#   docker run --rm -v $(pwd)/my-app:/app -w /app sky sky run src/Main.sky
#
#   # Use as CI/CD base image
#   FROM sky:latest
#   COPY . /app
#   WORKDIR /app
#   RUN sky build src/Main.sky
#   CMD ["./dist/app"]
# ─────────────────────────────────────────────────────────────

# ── Stage 1: Build the Sky compiler ─────────────────────────
FROM node:22-bookworm AS builder

WORKDIR /sky

# Install dependencies first (layer cache)
COPY package.json package-lock.json* ./
RUN npm ci --ignore-scripts

# Copy source
COPY src/ src/
COPY tsconfig.json ./

# Build TypeScript
RUN npm run build

# Bundle into single-file CJS binaries with embedded stdlib
RUN node src/bin/build-binary.js

# ── Stage 2: Runtime image ──────────────────────────────────
# Node.js for running the Sky compiler + Go for compiling output
FROM node:22-bookworm-slim

# Install Go
ENV GO_VERSION=1.24.3
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" | \
    tar -C /usr/local -xzf - && \
    rm -rf /var/lib/apt/lists/*

ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/root/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Copy the bundled Sky compiler
COPY --from=builder /sky/dist/sky.cjs /usr/local/lib/sky/sky.cjs
COPY --from=builder /sky/dist/sky-lsp.cjs /usr/local/lib/sky/sky-lsp.cjs

# Create wrapper scripts
RUN printf '#!/bin/sh\nnode /usr/local/lib/sky/sky.cjs "$@"\n' > /usr/local/bin/sky && \
    chmod +x /usr/local/bin/sky && \
    printf '#!/bin/sh\nnode /usr/local/lib/sky/sky-lsp.cjs "$@"\n' > /usr/local/bin/sky-lsp && \
    chmod +x /usr/local/bin/sky-lsp

# Verify installation
RUN sky --help && go version

# Default working directory for user projects
WORKDIR /app

ENTRYPOINT []
CMD ["sky", "--help"]
