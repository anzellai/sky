#!/usr/bin/env bash
# scripts/lib/cargo-target.sh — where cargo ACTUALLY put the binary.
#
# Why this exists
# ---------------
# Five scripts and workflows hardcoded `rust/target/release/sky`, then copied
# that path to `sky-out/sky`. Cargo does not always write there. It honours
# `CARGO_TARGET_DIR`, `CARGO_BUILD_TARGET_DIR`, and `build.target-dir` in any
# `.cargo/config.toml` — and when any of those is set, the hardcoded path names
# a DIFFERENT FILE, usually an older build of the same binary.
#
# That is not theoretical. On this machine `CARGO_TARGET_DIR` is set to
# `/Users/anzel/.cargo/bin`. `scripts/build.sh` reported success, `sky-out/sky`
# got a fresh mtime from the `cp`, and its CONTENTS were a compiler built before
# the fix under test. Two suites were then declared broken, a completed fix was
# sent back for rework, and the binary was the only thing wrong. The copy cannot
# fail loudly in that situation, because the stale file it reads really does
# exist.
#
# The failure mode is the worst kind: a build step that succeeds while shipping
# something other than what you just compiled. Everything downstream — gates,
# sweeps, release preflight — then certifies the wrong binary.
#
# Usage:
#   source "$ROOT/scripts/lib/cargo-target.sh"
#   target_dir="$(cargo_target_dir "$ROOT/rust")"
#   install_fresh_binary "$target_dir/release/sky" "$ROOT/sky-out/sky" "$stamp"

# Resolve the target directory for a cargo workspace. Asks cargo itself rather
# than reimplementing its precedence rules, and falls back to the conventional
# layout only when that fails (no cargo on PATH, a metadata error).
cargo_target_dir() { # cargo_target_dir <workspace-dir>
    local ws="$1" dir=""
    dir="$( (cd "$ws" && cargo metadata --no-deps --format-version 1 2>/dev/null) \
        | sed -n 's/.*"target_directory":"\([^"]*\)".*/\1/p' | head -1 )"
    if [ -n "$dir" ]; then
        printf '%s\n' "$dir"
        return 0
    fi
    # Fallbacks, in cargo's own order of precedence.
    if [ -n "${CARGO_TARGET_DIR:-}" ]; then
        printf '%s\n' "$CARGO_TARGET_DIR"
    elif [ -n "${CARGO_BUILD_TARGET_DIR:-}" ]; then
        printf '%s\n' "$CARGO_BUILD_TARGET_DIR"
    else
        printf '%s\n' "$ws/target"
    fi
}

# Ask CARGO which file it just produced, and copy that one.
#
# This deliberately does NOT use mtimes. The first version of this helper
# stamped a file before the build and rejected any binary not newer than the
# stamp — which correctly caught the stale copy, and then also rejected the
# ordinary case where nothing had changed, cargo did no work (`Finished in
# 0.18s`), and the existing binary was already the right one. mtime cannot tell
# "already current" from "left over from somewhere else"; it is the wrong
# instrument.
#
# `cargo build --message-format=json` emits a `compiler-artifact` record for the
# built package carrying the absolute path of its executable — including on a
# fully cached build, where the record simply says `"fresh":true`. That path is
# cargo's own answer to "where did it go", so it is correct under every
# CARGO_TARGET_DIR, `.cargo/config.toml`, workspace layout and profile without
# this script reimplementing any of those rules.
#
# Re-running the build to ask is cheap precisely because it is a no-op.
cargo_bin_path() { # cargo_bin_path <workspace-dir> <package> [extra cargo args...]
    local ws="$1" pkg="$2"; shift 2
    (cd "$ws" && cargo build --locked -p "$pkg" --message-format=json "$@" 2>/dev/null) \
        | tr ',' '\n' \
        | sed -n 's/.*"executable":"\([^"]*\)".*/\1/p' \
        | grep -v '^null$' \
        | tail -1
}

# Copy a built binary into place, refusing to install one that does not exist.
#
# The caller passes the path CARGO reported (see `cargo_bin_path`), so "stale"
# is no longer a thing this function has to detect — it cannot receive a path
# that the build did not just account for.
install_binary() { # install_binary <src> <dest>
    local src="$1" dest="$2"
    if [ -z "$src" ] || [ ! -f "$src" ]; then
        echo "build: cargo did not report an executable path for this package." >&2
        echo "       got: '${src:-<empty>}'" >&2
        echo "       CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-<unset>}" >&2
        return 1
    fi
    mkdir -p "$(dirname "$dest")"
    cp -f "$src" "$dest"
    chmod +x "$dest"
}
