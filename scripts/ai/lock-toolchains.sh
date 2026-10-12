#!/usr/bin/env bash
# lock-toolchains.sh — regenerate the hash locks of the Std.Ai.Train toolchains.
#
#   scripts/ai/lock-toolchains.sh            all locks
#   scripts/ai/lock-toolchains.sh mlx        one lock
#
# Each lock is `uv pip compile` of its `.in` file for ONE target platform and
# Python, with every wheel's SHA-256 (`--generate-hashes`), wheels only
# (`--only-binary :all:`: no source build runs a setup.py on the user's
# machine), and nothing published after the cut-off date (`--exclude-newer`),
# so running the script again gives the same lock. The runtime installs a lock
# with `uv pip sync --require-hashes --only-binary :all:`, and
# runtime-go/rt/ai_toolchain_test.go checks the lock policy (every line
# hashed, no URL, VCS or editable requirement, no AGPL Unsloth Studio).
#
# A lock change changes the toolchain id, so every host installs the new
# toolchain beside the old one on its next run. Review the diff.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
LOCKS="$ROOT/runtime-go/rt/ai_train_assets/locks"
source "$ROOT/scripts/lib/require-tool.sh"
source "$ROOT/scripts/lib/with-timeout.sh"
require_tool uv "install uv (https://docs.astral.sh/uv/) to regenerate the locks"

# The cut-off: a package published after it is not considered. Move it on
# purpose, in its own commit, with the lock diff.
CUTOFF="2026-09-25T00:00:00Z"
PYTHON="3.12"

lock() {
  local name="$1" platform="$2"
  echo "lock-toolchains: $name ($platform, Python $PYTHON, published before $CUTOFF)"
  UV_NO_CONFIG=1 with_timeout 1200 uv pip compile "$LOCKS/$name.in" \
    --generate-hashes --only-binary :all: --no-header --no-annotate \
    --python-version "$PYTHON" --python-platform "$platform" \
    --exclude-newer "$CUTOFF" --index-url https://pypi.org/simple \
    -o "$LOCKS/$name.txt"
}

want="${1:-all}"
if [ "$want" = all ] || [ "$want" = mlx ]; then
  lock mlx-darwin-arm64 aarch64-apple-darwin
fi
if [ "$want" = all ] || [ "$want" = unsloth ]; then
  lock unsloth-linux-x86_64-cuda13 x86_64-manylinux_2_28
fi
