# ─────────────────────────────────────────────────────────────
# Sky Language — Dockerfile
#
# Downloads the pre-built Sky binary from GitHub releases and ships it
# with the Go toolchain (required by `sky build` since Sky emits Go).
#
# Usage:
#   docker build -t sky .
#   docker run --rm -v $(pwd)/my-app:/app -w /app sky sky build src/Main.sky
#
# Build args:
#   SKY_VERSION  — version to install (default: latest)
# ─────────────────────────────────────────────────────────────

FROM golang:1.26-bookworm

ARG SKY_VERSION=""
ARG TARGETARCH

RUN apt-get update \
 && apt-get install -y --no-install-recommends curl ca-certificates git \
 && rm -rf /var/lib/apt/lists/*

# Download sky binary from GitHub releases
RUN set -e; \
    ARCH=$(echo "${TARGETARCH:-amd64}" | sed 's/amd64/x64/'); \
    if [ -z "$SKY_VERSION" ]; then \
        SKY_VERSION=$(curl -fsSL https://api.github.com/repos/anzellai/sky/releases/latest \
            | grep '"tag_name"' | sed 's/.*"v\(.*\)".*/\1/'); \
    fi; \
    echo "Installing sky v${SKY_VERSION} for linux-${ARCH}"; \
    ARCHIVE_URL="https://github.com/anzellai/sky/releases/download/v${SKY_VERSION}/sky-linux-${ARCH}.tar.gz"; \
    RAW_URL="https://github.com/anzellai/sky/releases/download/v${SKY_VERSION}/sky-linux-${ARCH}"; \
    if curl -fsSL "$ARCHIVE_URL" -o /tmp/sky.tar.gz 2>/dev/null; then \
        cd /tmp && tar xzf sky.tar.gz; \
        mv sky-linux-${ARCH} /usr/local/bin/sky; \
        [ -f sky-ffi-inspect-sky-linux-${ARCH} ] && mv sky-ffi-inspect-sky-linux-${ARCH} /usr/local/bin/sky-ffi-inspect; \
        rm -f sky.tar.gz; \
    elif curl -fsSL "$RAW_URL" -o /usr/local/bin/sky 2>/dev/null; then \
        echo "Downloaded raw binary"; \
    else \
        echo "Failed to download sky v${SKY_VERSION}" && exit 1; \
    fi; \
    chmod +x /usr/local/bin/sky; \
    sky --version

WORKDIR /app
ENTRYPOINT ["sky"]
