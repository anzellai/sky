# ─────────────────────────────────────────────────────────────
# Sky Language — Dockerfile (Haskell-compiler edition)
#
# Builds the Sky compiler (Haskell/GHC) in a build stage, then ships it
# into a slim runtime image along with the Go toolchain (required at
# build time by `sky build` because Sky emits Go).
#
# Usage:
#   docker build -t sky .
#   docker run --rm -v $(pwd)/my-app:/app -w /app sky sky build src/Main.sky
# ─────────────────────────────────────────────────────────────

# ───── Stage 1: build the Haskell compiler ─────
FROM haskell:9.4.8 AS builder

WORKDIR /src

# Copy cabal manifest first for better layer caching
COPY sky-compiler.cabal ./
RUN cabal update && cabal build --only-dependencies --dry-run || true

# Now copy sources
COPY app ./app
COPY src ./src

RUN cabal update \
 && cabal build \
 && mkdir -p /out \
 && cp "$(cabal list-bin sky)" /out/sky

# ───── Stage 2: build supporting Go tool (FFI inspector) ─────
FROM golang:1.26-bookworm AS tools-builder
WORKDIR /t
COPY tools/sky-ffi-inspect ./sky-ffi-inspect
RUN cd sky-ffi-inspect && go build -ldflags="-s -w" -o /out/sky-ffi-inspect .

# ───── Stage 3: runtime image ─────
FROM golang:1.26-bookworm

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
    ca-certificates git \
    libgmp10 libffi8 libtinfo6 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/sky /usr/local/bin/sky
COPY --from=tools-builder /out/sky-ffi-inspect /usr/local/bin/sky-ffi-inspect

# Sky needs its runtime sources at build time. Ship them under SKY_RUNTIME_DIR.
RUN mkdir -p /opt/sky/runtime-go
COPY runtime-go /opt/sky/runtime-go
ENV SKY_RUNTIME_DIR=/opt/sky/runtime-go

# Templates (read by `sky init`)
COPY templates /opt/sky/templates

RUN sky --version

WORKDIR /app
ENTRYPOINT []
CMD ["sky", "--help"]
