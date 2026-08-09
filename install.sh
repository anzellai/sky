#!/bin/sh
# Sky Language Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh
#    or: curl -fsSL ... | sh -s -- --dir ~/.local/bin
#    or: curl -fsSL ... | sh -s -- --version 0.8.1
#    or: curl -fsSL ... | sh -s -- --dir ~/.local/bin --version 0.8.1
#
# Environment variables (alternative to flags):
#   SKY_VERSION       - specific version to install (default: latest)
#   SKY_INSTALL_DIR   - installation directory (default: /usr/local/bin)
#   INSTALL_DIR       - same thing; SKY_INSTALL_DIR wins if both are set
#
# The install directory is CREATED if it does not exist — a fresh macOS, a slim
# container image and many CI runners have no /usr/local/bin — using sudo only
# when the parent is not writable. With no writable target and no sudo, the
# installer falls back to ~/.local/bin rather than failing.
set -e

# Parse arguments
while [ $# -gt 0 ]; do
    case "$1" in
        --dir=*)     SKY_INSTALL_DIR="${1#--dir=}" ;;
        --dir)       shift; SKY_INSTALL_DIR="$1" ;;
        --version=*) SKY_VERSION="${1#--version=}" ;;
        --version)   shift; SKY_VERSION="$1" ;;
    esac
    shift
done

REPO="anzellai/sky"
# SKY_INSTALL_DIR wins, then INSTALL_DIR, then the default.
INSTALL_DIR="${SKY_INSTALL_DIR:-${INSTALL_DIR:-/usr/local/bin}}"

RED='\033[0;31m'
GREEN='\033[0;32m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info() { printf "${CYAN}==>${NC} %s\n" "$1"; }
success() { printf "${GREEN}==>${NC} %s\n" "$1"; }
error() { printf "${RED}error:${NC} %s\n" "$1" >&2; exit 1; }

detect_platform() {
    OS="$(uname -s)"
    ARCH="$(uname -m)"

    case "$OS" in
        Linux)  PLATFORM="linux" ;;
        Darwin) PLATFORM="darwin" ;;
        MINGW*|MSYS*|CYGWIN*) PLATFORM="windows" ;;
        *) error "Unsupported OS: $OS" ;;
    esac

    case "$ARCH" in
        x86_64|amd64)  ARCH="x64" ;;
        arm64|aarch64) ARCH="arm64" ;;
        *) error "Unsupported architecture: $ARCH" ;;
    esac
}

get_latest_version() {
    if ! command -v curl >/dev/null 2>&1; then
        error "curl is required but not installed"
    fi
    AUTH_HEADER=""
    if [ -n "$GITHUB_TOKEN" ]; then
        AUTH_HEADER="-H \"Authorization: token $GITHUB_TOKEN\""
    fi
    VERSION=$(eval curl -fsSL $AUTH_HEADER "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | grep '"tag_name"' | sed 's/.*"v\(.*\)".*/\1/')
    if [ -z "$VERSION" ]; then
        error "Could not determine latest version. Check https://github.com/$REPO/releases"
    fi
}

install_sky() {
    ARTIFACT="sky-${PLATFORM}-${ARCH}"
    EXT=""
    if [ "$PLATFORM" = "windows" ]; then
        EXT=".exe"
    fi

    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT

    info "Downloading sky v${VERSION}..."

    # Try archive format first (v0.8.1+), fall back to raw binary (v0.8.0 and earlier)
    DOWNLOADED=0
    if [ "$PLATFORM" = "windows" ]; then
        ARCHIVE="${ARTIFACT}.zip"
    else
        ARCHIVE="${ARTIFACT}.tar.gz"
    fi
    ARCHIVE_URL="https://github.com/$REPO/releases/download/v${VERSION}/${ARCHIVE}"
    RAW_URL="https://github.com/$REPO/releases/download/v${VERSION}/${ARTIFACT}${EXT}"

    if curl -fsSL "$ARCHIVE_URL" -o "$TMPDIR/$ARCHIVE" 2>/dev/null; then
        cd "$TMPDIR"
        if [ "$PLATFORM" = "windows" ]; then
            unzip -q "$ARCHIVE"
        else
            # --no-same-owner: never try to restore the archive's UID/GID. An
            # unprivileged container (no CAP_CHOWN) can't chown to the builder's
            # uid and tar would fail the whole extraction. We only need the files.
            tar --no-same-owner -xzf "$ARCHIVE"
        fi
        DOWNLOADED=1
    elif curl -fsSL "$RAW_URL" -o "$TMPDIR/${ARTIFACT}${EXT}" 2>/dev/null; then
        cd "$TMPDIR"
        DOWNLOADED=1
    fi

    if [ "$DOWNLOADED" = "0" ]; then
        error "Failed to download sky v${VERSION}\nCheck https://github.com/$REPO/releases"
    fi

    # Make sure the target directory exists and decide ONCE how to write to it.
    # A fresh macOS, a slim container image and many CI runners have no
    # /usr/local/bin at all — without this the -w test is false, we fall through
    # to `sudo mv` into a directory that does not exist, and the install dies
    # with a bare "No such file or directory".
    SUDO=""
    if [ ! -d "$INSTALL_DIR" ]; then
        if mkdir -p "$INSTALL_DIR" 2>/dev/null; then
            info "Created $INSTALL_DIR"
        elif command -v sudo >/dev/null 2>&1 && sudo mkdir -p "$INSTALL_DIR" 2>/dev/null; then
            info "Created $INSTALL_DIR (sudo)"
            SUDO="sudo"
        else
            INSTALL_DIR="$HOME/.local/bin"
            mkdir -p "$INSTALL_DIR"
            info "No writable system directory — installing to $INSTALL_DIR"
        fi
    fi

    # Existing directory we cannot write to: use sudo when it is available,
    # otherwise fall back rather than dying part-way through.
    if [ ! -w "$INSTALL_DIR" ]; then
        if command -v sudo >/dev/null 2>&1; then
            info "Requires sudo to install to $INSTALL_DIR"
            SUDO="sudo"
        else
            INSTALL_DIR="$HOME/.local/bin"
            mkdir -p "$INSTALL_DIR"
            info "Target not writable and sudo unavailable — installing to $INSTALL_DIR"
        fi
    fi

    # Install sky binary. chmod runs through the SAME privilege path as the mv:
    # a root-owned binary cannot be chmod'ed by the invoking user, so `set -e`
    # aborted AFTER the file had already landed — leaving a non-executable sky.
    $SUDO mv "${ARTIFACT}${EXT}" "$INSTALL_DIR/sky${EXT}"
    $SUDO chmod +x "$INSTALL_DIR/sky${EXT}"
    success "Installed sky -> ${INSTALL_DIR}/sky${EXT}"

    # Install sky-ffi-inspect if present in the archive
    FFI_BIN="sky-ffi-inspect-${ARTIFACT}${EXT}"
    if [ -f "$FFI_BIN" ]; then
        $SUDO mv "$FFI_BIN" "$INSTALL_DIR/sky-ffi-inspect${EXT}"
        $SUDO chmod +x "$INSTALL_DIR/sky-ffi-inspect${EXT}"
        success "Installed sky-ffi-inspect -> ${INSTALL_DIR}/sky-ffi-inspect${EXT}"
    fi

    # A binary that is not on PATH is not installed as far as the user is
    # concerned — the usual outcome for ~/.local/bin.
    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*) ;;
        *)
            echo ""
            printf "${BOLD}Note:${NC} %s is not on your PATH.\n" "$INSTALL_DIR"
            printf "Add it, then restart your shell:\n\n"
            printf "  ${CYAN}echo 'export PATH=\"%s:\$PATH\"' >> ~/.zshrc${NC}\n" "$INSTALL_DIR"
            printf "  (or ~/.bashrc / ~/.profile for bash)\n"
            ;;
    esac
}

check_go() {
    if command -v go >/dev/null 2>&1; then
        success "Go found: $(go version | head -1)"
    else
        echo ""
        printf "${RED}${BOLD}Go is required but not installed.${NC}\n"
        printf "Sky compiles to Go, so you need the Go toolchain.\n"
        echo ""
        printf "Install Go: ${CYAN}https://go.dev/dl/${NC}\n"
        echo ""
    fi
}

main() {
    printf "\n${BOLD}Sky Language Installer${NC}\n\n"

    detect_platform

    if [ -n "$SKY_VERSION" ]; then
        VERSION="$SKY_VERSION"
    else
        get_latest_version
    fi

    info "Platform: ${PLATFORM}/${ARCH}"
    info "Version:  v${VERSION}"
    echo ""

    install_sky

    echo ""
    check_go

    printf "\n${GREEN}${BOLD}Sky v${VERSION} installed successfully!${NC}\n\n"
    echo "  Get started:"
    echo "    sky init my-app"
    echo "    cd my-app"
    echo "    sky run"
    echo ""
    echo "  Built-in tools: sky lsp, sky fmt, sky test"
    echo ""
}

main "$@"
