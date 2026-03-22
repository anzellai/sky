#!/bin/sh
# Sky Language Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh
#    or: curl -fsSL ... | SKY_INSTALL_DIR=~/.local/bin sh
#    or: curl -fsSL ... | sh -s -- --dir ~/.local/bin
#
# Environment variables:
#   SKY_VERSION       - specific version to install (default: latest)
#   SKY_INSTALL_DIR   - installation directory (default: /usr/local/bin)
set -e

# Parse --dir argument
for arg in "$@"; do
    case "$arg" in
        --dir=*) SKY_INSTALL_DIR="${arg#--dir=}" ;;
        --dir)   shift; SKY_INSTALL_DIR="$1" ;;
    esac
    shift 2>/dev/null || true
done

REPO="anzellai/sky"
INSTALL_DIR="${SKY_INSTALL_DIR:-/usr/local/bin}"

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
    # Use GITHUB_TOKEN if available (avoids API rate limits in CI)
    AUTH_HEADER=""
    if [ -n "$GITHUB_TOKEN" ]; then
        AUTH_HEADER="-H \"Authorization: token $GITHUB_TOKEN\""
    fi
    VERSION=$(eval curl -fsSL $AUTH_HEADER "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | grep '"tag_name"' | sed 's/.*"v\(.*\)".*/\1/')
    if [ -z "$VERSION" ]; then
        error "Could not determine latest version. Check https://github.com/$REPO/releases"
    fi
}

install_binary() {
    local name="$1"
    local target_name="$2"
    local url="https://github.com/$REPO/releases/download/v${VERSION}/${name}"

    info "Downloading ${target_name} v${VERSION}..."

    TMPFILE=$(mktemp)
    trap 'rm -f "$TMPFILE"' EXIT

    if ! curl -fsSL "$url" -o "$TMPFILE"; then
        error "Failed to download $url\nCheck that v${VERSION} exists at https://github.com/$REPO/releases"
    fi

    chmod +x "$TMPFILE"

    if [ -w "$INSTALL_DIR" ]; then
        mv "$TMPFILE" "$INSTALL_DIR/$target_name"
    else
        info "Requires sudo to install to $INSTALL_DIR"
        sudo mv "$TMPFILE" "$INSTALL_DIR/$target_name"
    fi

    success "Installed ${target_name} -> ${INSTALL_DIR}/${target_name}"
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

    EXT=""
    if [ "$PLATFORM" = "windows" ]; then
        EXT=".exe"
    fi

    install_binary "sky-${PLATFORM}-${ARCH}${EXT}" "sky${EXT}"
    install_binary "sky-lsp-${PLATFORM}-${ARCH}${EXT}" "sky-lsp${EXT}"

    echo ""
    check_go

    printf "\n${GREEN}${BOLD}Sky v${VERSION} installed successfully!${NC}\n\n"
    echo "  Get started:"
    echo "    sky init my-project"
    echo "    cd my-project"
    echo "    sky run src/Main.sky"
    echo ""
}

main "$@"
