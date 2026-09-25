#!/usr/bin/env bash
# scripts/lib/gate-build-cache.sh — a content-addressed cache of built Sky
# projects, shared by the gates that build the same projects.
#
# Why this exists
# ---------------
# The example sweep, the browser gate (verify-all-web), the e2e scripts and the
# `xtask build-run` Std.App path each run `sky build` on the same projects. A
# full local verification built many of them two or three times from nothing,
# with the same compiler and the same source. A clean-slate build is a function
# of its inputs (the `repro` gate proves the emitted Go is byte-stable across
# fresh processes), so the second build of an identical input produces nothing
# the first did not.
#
# The contract: an artefact is reused ONLY when every input that decides it is
# identical, and it is only ever STORED from a clean-slate build.
#
# The key
# -------
#   * the compiler: the SHA-256 of the binary (which carries the baked
#     `sky-embed-fp-v1` fingerprint of the embedded runtime and stdlib, so a
#     rebuilt compiler — or one whose embedded runtime changed — misses);
#   * the project: its absolute path, and every file under it except build
#     output (`sky-out*`, `.skycache`, `.skyapp`) and run-time state (`.sky/`,
#     `*.db*`, `*.log`), byte for byte — `sky.toml`, `sky.lock`, `src/`,
#     `static/`, `.skydeps/`, a generated `sky-ffi/`, everything;
#   * the build: the exact `sky` arguments (entry, `--target`, flags) and the
#     artefact paths requested;
#   * the toolchain: `go version` and the Go env that changes codegen (GOOS,
#     GOARCH, CGO_ENABLED, GOFLAGS, GOEXPERIMENT, GOAMD64, GOARM64, CC);
#   * the environment: every `SKY_*` and `CGO_*` variable (bar this cache's own,
#     and `SKY_RUNTIME_DIR`, which no current compiler reads).
#
# What is never cached
# --------------------
#   * A project with `[go.dependencies]`. Its Go dependencies float on
#     "latest" (`sky.lock` records `latest`), so its build is a function of the
#     network as well as of the tree, and no key over the tree can say when an
#     upstream release changed it. The clean-slate sweep exists partly to catch
#     exactly that, so those projects always build.
#   * A build that is not clean-slate. Only a call with `--clean` stores: on a
#     miss it first removes the artefacts and the compiler's own incremental
#     output (`.skycache/lowered`, `.skycache/go`), so what is stored is what a
#     clean checkout builds. A call without `--clean` may be SERVED from the
#     cache (a hit is a clean build of identical inputs), but never writes to it.
#
# Controls
# --------
#   SKY_GATE_CACHE=off          disable: every call builds, nothing is read or written
#                               (the default on a GitHub Actions runner; =on enables)
#   SKY_GATE_CACHE_DIR=<dir>    where entries live
#                               (default ${XDG_CACHE_HOME:-$HOME/.cache}/sky-gate-build)
#   SKY_GATE_CACHE_MAX_MB=<n>   size bound, pruned oldest-used first (default 8192;
#                               a built example is ~220 MB, and on APFS a restored
#                               copy is a clone that shares the entry's blocks)
#
# Use
# ---
#   source scripts/lib/gate-build-cache.sh
#   gate_cached_build <sky-binary> <project-dir> [--artefact <rel>]... [--clean] -- <sky args...>
#
# or, from a program that cannot source bash (the xtask build-run gate):
#   bash scripts/lib/gate-build-cache.sh build <sky-binary> <project-dir> [...] -- <sky args...>
#
# Both print one line on stderr — `gate-cache: HIT|MISS|OFF|UNCACHEABLE <project>`
# — so a run says which artefacts were reused. The exit status is the build's
# (0 on a hit). The default artefact is `sky-out`.
#
# Bash 3.2 compatible (stock macOS /bin/bash): no associative arrays, no mapfile.

if [ -n "${_SKY_GATE_BUILD_CACHE_SOURCED:-}" ]; then
    return 0 2>/dev/null || true
fi
_SKY_GATE_BUILD_CACHE_SOURCED=1

_gc_sha256() { # stdin -> hex digest
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | awk '{print $1}'
    else
        shasum -a 256 | awk '{print $1}'
    fi
}

_gc_file_sha256() { # <file> -> hex digest
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

gate_cache_dir() {
    printf '%s\n' "${SKY_GATE_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/sky-gate-build}"
}

gate_cache_enabled() {
    # Off by default on a GitHub Actions runner: each CI job builds a project
    # once on a fresh disk, so a store would only spend disk (ext4 has no clone,
    # and a built example is ~220 MB) for an entry nothing reads.
    # SKY_GATE_CACHE=on turns it on there.
    local default=on
    [ "${GITHUB_ACTIONS:-}" = "true" ] && default=off
    case "${SKY_GATE_CACHE:-$default}" in
        off | OFF | 0 | false | no) return 1 ;;
        *) return 0 ;;
    esac
}

# A project whose Go dependencies float on the network is never cached.
gate_cache_project_cacheable() { # <project-dir>
    local toml="$1/sky.toml"
    if [ -f "$toml" ] && grep -qE '^\["?go\.dependencies"?\]' "$toml"; then
        return 1
    fi
    return 0
}

# Every input file of a project, as `<sha256>  <relpath>` lines, sorted.
_gc_project_manifest() { # <project-dir>
    (
        cd "$1" || exit 1
        find . \
            \( -name 'sky-out' -o -name 'sky-out-*' -o -name '.skycache' -o -name '.skyapp' \
               -o -name '.sky' -o -name '.git' -o -name 'node_modules' \) -prune \
            -o -type f \
            ! -name '*.db' ! -name '*.db-shm' ! -name '*.db-wal' ! -name '*.db-journal' \
            ! -name '*.log' ! -name '.DS_Store' \
            -print | LC_ALL=C sort | while IFS= read -r f; do
            printf '%s  %s\n' "$(_gc_file_sha256 "$f")" "$f"
        done
    )
}

# The compiler's identity, computed once per process tree (a 100+ MB binary is
# not re-hashed for every project). Keyed by path, size and mtime so a rebuilt
# binary in the same process tree is re-hashed.
_gc_compiler_hash() { # <sky-binary>
    local bin="$1" stamp
    stamp="$bin:$(stat -c '%i:%s:%Y' "$bin" 2>/dev/null || stat -f '%i:%z:%m' "$bin")"
    if [ "${_SKY_GATE_CACHE_COMPILER_STAMP:-}" = "$stamp" ] && [ -n "${_SKY_GATE_CACHE_COMPILER_HASH:-}" ]; then
        printf '%s\n' "$_SKY_GATE_CACHE_COMPILER_HASH"
        return 0
    fi
    _SKY_GATE_CACHE_COMPILER_HASH="$(_gc_file_sha256 "$bin")"
    _SKY_GATE_CACHE_COMPILER_STAMP="$stamp"
    export _SKY_GATE_CACHE_COMPILER_HASH _SKY_GATE_CACHE_COMPILER_STAMP
    printf '%s\n' "$_SKY_GATE_CACHE_COMPILER_HASH"
}

_gc_toolchain() {
    if command -v go >/dev/null 2>&1; then
        go version 2>/dev/null
        go env GOOS GOARCH CGO_ENABLED GOFLAGS GOEXPERIMENT GOAMD64 GOARM64 CC 2>/dev/null
    else
        echo "go: absent"
    fi
}

# The full key text. Its SHA-256 is the entry name.
gate_cache_key_text() { # <sky-binary> <project-dir> <artefacts> <sky args...>
    local bin="$1" dir="$2" artefacts="$3"
    shift 3
    local abs
    abs="$(cd "$dir" && pwd -P)" || return 1
    printf 'gate-build-cache v1\n'
    printf 'compiler %s\n' "$(_gc_compiler_hash "$bin")"
    printf 'project %s\n' "$abs"
    printf 'artefacts %s\n' "$artefacts"
    printf 'args'
    local a
    for a in "$@"; do printf ' [%s]' "$a"; done
    printf '\n'
    printf 'toolchain\n'
    _gc_toolchain
    printf 'env\n'
    # SKY_RUNTIME_DIR is excluded: only the retired Haskell compiler read it (the
    # Rust compiler embeds its runtime, whose content the binary hash covers),
    # and the sweep exports it while the browser gate does not — keeping it
    # would stop the two from sharing a build. `tests/gate_build_cache.rs` fails
    # if any Rust compiler source starts reading it.
    env | LC_ALL=C sort | grep -E '^(SKY_|CGO_)' | grep -vE '^(SKY_GATE_CACHE|SKY_RUNTIME_DIR=)' || true
    printf 'files\n'
    _gc_project_manifest "$abs"
}

gate_cache_key() { # same arguments as gate_cache_key_text
    gate_cache_key_text "$@" | _gc_sha256
}

_gc_copy_tree() { # <src> <dst> — clone on APFS / reflink on btrfs+xfs when possible
    if [ "$(uname -s)" = "Darwin" ] && /bin/cp -Rc "$1" "$2" 2>/dev/null; then
        return 0
    fi
    rm -rf "$2"
    if cp --version >/dev/null 2>&1; then
        cp -R --reflink=auto "$1" "$2"
    else
        /bin/cp -R "$1" "$2"
    fi
}

# Bound the cache: drop the least-recently-used entries until it fits.
gate_cache_prune() {
    local root max_kb used
    root="$(gate_cache_dir)/entries"
    [ -d "$root" ] || return 0
    max_kb=$(( ${SKY_GATE_CACHE_MAX_MB:-8192} * 1024 ))
    used=$(du -sk "$root" 2>/dev/null | awk '{print $1}')
    used=${used:-0}
    [ "$used" -le "$max_kb" ] && return 0
    # Oldest-used last in `ls -t`; restore touches an entry, so this is LRU.
    local e
    for e in $(ls -t "$root" 2>/dev/null | awk '{a[NR]=$0} END {for (i=NR; i>0; i--) print a[i]}'); do
        [ "$used" -le "$max_kb" ] && break
        rm -rf "${root:?}/$e"
        used=$(du -sk "$root" 2>/dev/null | awk '{print $1}')
        used=${used:-0}
    done
}

gate_cached_build() { # <sky-binary> <project-dir> [--artefact <rel>]... [--clean] -- <sky args...>
    local bin="$1" dir="$2"
    shift 2
    local artefacts="" clean=0
    while [ $# -gt 0 ]; do
        case "$1" in
            --artefact) artefacts="$artefacts $2"; shift 2 ;;
            --clean) clean=1; shift ;;
            --) shift; break ;;
            *) echo "gate_cached_build: unknown option $1" >&2; return 2 ;;
        esac
    done
    [ -n "$artefacts" ] || artefacts=" sky-out"
    artefacts="${artefacts# }"
    local name
    name="$(basename "$dir")"

    local mode=""
    if ! gate_cache_enabled; then
        mode="OFF"
    elif ! gate_cache_project_cacheable "$dir"; then
        mode="UNCACHEABLE"
    fi
    if [ -n "$mode" ]; then
        echo "gate-cache: $mode $name" >&2
        ( cd "$dir" && "$bin" "$@" )
        return $?
    fi

    local key root entry
    _gc_compiler_hash "$bin" >/dev/null || return 1
    key="$(gate_cache_key "$bin" "$dir" "$artefacts" "$@")" || {
        echo "gate-cache: OFF $name (key could not be computed)" >&2
        ( cd "$dir" && "$bin" "$@" )
        return $?
    }
    root="$(gate_cache_dir)/entries"
    entry="$root/$key"

    local a
    if [ -f "$entry/.complete" ]; then
        for a in $artefacts; do
            rm -rf "${dir:?}/$a"
            if [ -e "$entry/$a" ]; then
                mkdir -p "$(dirname "$dir/$a")"
                _gc_copy_tree "$entry/$a" "$dir/$a" || {
                    echo "gate-cache: restore of $a failed; building" >&2
                    rm -rf "${dir:?}/$a"
                    ( cd "$dir" && "$bin" "$@" )
                    return $?
                }
            fi
        done
        touch "$entry"
        echo "gate-cache: HIT $name ($key)" >&2
        return 0
    fi

    echo "gate-cache: MISS $name ($key)" >&2
    if [ $clean -eq 1 ]; then
        for a in $artefacts; do rm -rf "${dir:?}/$a"; done
        rm -rf "${dir:?}/.skycache/lowered" "${dir:?}/.skycache/go"
    fi
    local rc=0
    ( cd "$dir" && "$bin" "$@" ) || rc=$?
    if [ $rc -ne 0 ] || [ $clean -eq 0 ]; then
        return $rc
    fi

    # Store atomically: build the entry in a private directory, then rename it
    # into place. A concurrent writer of the same key loses the rename and
    # discards its copy — both copies came from the same inputs.
    mkdir -p "$root" || return 0
    local tmp
    tmp="$(mktemp -d "$root/.tmp.XXXXXX")" || return 0
    for a in $artefacts; do
        if [ -e "$dir/$a" ]; then
            mkdir -p "$(dirname "$tmp/$a")"
            _gc_copy_tree "$dir/$a" "$tmp/$a" || { rm -rf "$tmp"; return 0; }
        fi
    done
    gate_cache_key_text "$bin" "$dir" "$artefacts" "$@" >"$tmp/.key" 2>/dev/null || true
    : >"$tmp/.complete"
    mv "$tmp" "$entry" 2>/dev/null || rm -rf "$tmp"
    gate_cache_prune
    return 0
}

# A STABLE scratch directory for an e2e fixture:
# `<cache-dir>/e2e/<worktree-tag>/<name>` (not $TMPDIR, which a nix shell sets
# to a fresh directory per shell).
# The cache key includes the project's absolute path (a built app may resolve
# files relative to where it was built), so a fixture copied into a fresh
# `mktemp -d` on every run could never hit. One directory per worktree keeps
# two checkouts apart; the caller empties it before copying the fixture in.
gate_e2e_dir() { # <repo-root> <name>
    local tag
    tag="$(printf '%s' "$1" | _gc_sha256 | cut -c1-12)"
    printf '%s/e2e/%s/%s\n' "$(gate_cache_dir)" "$tag" "$2"
}

# Executable mode, for callers that cannot source bash.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    case "${1:-}" in
        build)
            shift
            gate_cached_build "$@"
            exit $?
            ;;
        key)
            shift
            gate_cache_key "$@"
            exit $?
            ;;
        prune)
            gate_cache_prune
            exit $?
            ;;
        *)
            echo "usage: $0 build <sky-binary> <project-dir> [--artefact <rel>]... [--clean] -- <sky args...>" >&2
            echo "       $0 key <sky-binary> <project-dir> <artefacts> <sky args...>" >&2
            echo "       $0 prune" >&2
            exit 2
            ;;
    esac
fi
