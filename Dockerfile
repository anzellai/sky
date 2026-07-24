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

# Debian Trixie (glibc 2.41), NOT Bookworm (glibc 2.36): the downloaded release
# binary links against the build host's glibc, and the v0.18.1 linux binaries
# were built on Ubuntu 24.04 (glibc 2.39), which Bookworm cannot run ("version
# GLIBC_2.39 not found"). Trixie runs them. From v0.18.2 the release binaries
# build on Ubuntu 22.04 (glibc 2.35) for portability, which Trixie also runs.
FROM golang:1.26-trixie

# Debian's default locale is POSIX/C (ASCII). Set a UTF-8 locale so the Go
# toolchain and any locale-sensitive IO handle .sky source files containing
# UTF-8 (currency symbols, non-Latin strings, multiline string content)
# without "invalid byte sequence" errors or silent corruption. C.UTF-8 ships
# with Debian ≥ buster so no `locales` package install is needed — zero
# image-size cost.
ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

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
    # Retry with backoff: this image builds in the SAME release run that just
    # published the assets (docker `needs: release`), so a fresh tag's asset can
    # 404 for a minute while GitHub's release-asset CDN propagates. Retry the
    # download (7 attempts, ~2m total) instead of failing the build on a race.
    ok=""; \
    for attempt in 1 2 3 4 5 6 7; do \
        if curl -fsSL "$ARCHIVE_URL" -o /tmp/sky.tar.gz 2>/dev/null; then \
            cd /tmp && tar xzf sky.tar.gz; \
            mv sky-linux-${ARCH} /usr/local/bin/sky; \
            [ -f sky-ffi-inspect-sky-linux-${ARCH} ] && mv sky-ffi-inspect-sky-linux-${ARCH} /usr/local/bin/sky-ffi-inspect; \
            rm -f sky.tar.gz; ok=1; break; \
        elif curl -fsSL "$RAW_URL" -o /usr/local/bin/sky 2>/dev/null; then \
            echo "Downloaded raw binary"; ok=1; break; \
        fi; \
        echo "download attempt ${attempt} failed (asset may still be propagating) — retrying in 20s"; \
        sleep 20; \
    done; \
    [ -n "$ok" ] || { echo "Failed to download sky v${SKY_VERSION} after retries" && exit 1; }; \
    chmod +x /usr/local/bin/sky; \
    sky --version

WORKDIR /app
ENTRYPOINT ["sky"]
