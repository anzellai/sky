# ─────────────────────────────────────────────────────────────
# Sky Language — Dockerfile
#
# Uses pre-built binary from release (no compilation in Docker).
#
# Usage:
#   docker build -t sky .
#   docker run --rm -v $(pwd)/my-app:/app -w /app sky sky build src/Main.sky
# ─────────────────────────────────────────────────────────────

FROM golang:1.26-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git && \
    rm -rf /var/lib/apt/lists/*

# Accept pre-built binary from build arg, or fall back to building from source
ARG SKY_BINARY=""
COPY ${SKY_BINARY:-sky-out/} /tmp/sky-src/

RUN if [ -f /tmp/sky-src/sky ]; then \
        cp /tmp/sky-src/sky /usr/local/bin/sky && chmod +x /usr/local/bin/sky; \
    elif [ -f /tmp/sky-src/main.go ]; then \
        cd /tmp/sky-src && go build -ldflags="-s -w" -o /usr/local/bin/sky . ; \
    fi && \
    rm -rf /tmp/sky-src && \
    sky --version

# Build companion tools from source
COPY tools/ /tmp/tools/
RUN go build -o /usr/local/bin/sky-ffi-gen /tmp/tools/sky_ffi_gen.go && \
    go build -o /usr/local/bin/sky-dce /tmp/tools/sky_dce.go && \
    go build -o /usr/local/bin/skyi-filter /tmp/tools/skyi_filter.go && \
    rm -rf /tmp/tools

WORKDIR /app

ENTRYPOINT []
CMD ["sky", "--help"]
