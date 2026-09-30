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
    BASE="https://github.com/anzellai/sky/releases/download/v${SKY_VERSION}"; \
    ASSET="sky-linux-${ARCH}.tar.gz"; \
    # Retry with backoff: this image builds in the SAME release run that just
    # published the assets (docker `needs: release`), so a fresh tag's asset can
    # 404 for a minute while GitHub's release-asset CDN propagates. Retry the
    # download (7 attempts, ~2m total) instead of failing the build on a race.
    # Every download is checked against the release's checksums.txt with
    # `sha256sum -c` before it is unpacked: an asset the manifest does not
    # list, or whose digest does not match, fails the build.
    ok=""; \
    mkdir -p /tmp/sky-dl && cd /tmp/sky-dl; \
    for attempt in 1 2 3 4 5 6 7; do \
        if curl -fsSL "$BASE/checksums.txt" -o checksums.txt 2>/dev/null \
           && curl -fsSL "$BASE/$ASSET" -o "$ASSET" 2>/dev/null; then \
            ok=1; break; \
        fi; \
        echo "download attempt ${attempt} failed (asset may still be propagating) — retrying in 20s"; \
        sleep 20; \
    done; \
    [ -n "$ok" ] || { echo "Failed to download sky v${SKY_VERSION} after retries" && exit 1; }; \
    grep -E "^[0-9a-f]{64} [ *]${ASSET}\$" checksums.txt > want.sha256 \
        || { echo "checksums.txt does not list ${ASSET}" && exit 1; }; \
    sha256sum -c want.sha256; \
    tar xzf "$ASSET"; \
    mv sky-linux-${ARCH} /usr/local/bin/sky; \
    if [ -f sky-ffi-inspect-sky-linux-${ARCH} ]; then mv sky-ffi-inspect-sky-linux-${ARCH} /usr/local/bin/sky-ffi-inspect; fi; \
    cd / && rm -rf /tmp/sky-dl; \
    chmod +x /usr/local/bin/sky; \
    sky --version

WORKDIR /app
ENTRYPOINT ["sky"]
