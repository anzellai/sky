#!/usr/bin/env bash
# scripts/lib/fresh-compiler.sh — a gate may not measure a compiler older than
# the source it claims to measure.
#
# Why this exists
# ---------------
# Fifteen scripts consume the compiler at `sky-out/sky`. Not one of them checked
# that it was built from the tree they were about to measure. `sky-out/sky` is
# installed there by exactly one line — `scripts/build.sh:80`
# (`install_binary "$(cargo_bin_path …)" "$ROOT/sky-out/sky"`) — so any workflow
# that builds with a bare `cargo build --release -p sky` leaves `rust/target/`
# fresh and `sky-out/` untouched, and every consumer then measures a compiler
# from whenever `build.sh` last ran.
#
# Both directions of that are bad, and only one of them is visible.
#
#   * The LOUD direction cost a full diagnosis on 2026-08-16. A sweep run after
#     `cargo build --release -p sky` reported every example failing and 22 of 22
#     conformance suites FAILED, on:
#
#         ./main.go:19:42: not enough arguments in call to rt.RegisterAdtTag
#             have (string, number)
#             want (string, string, int)
#
#     The tree was consistent — `rust/crates/lower/src/lower.rs` emits the
#     three-argument form and `runtime-go/rt/rt.go` declares it — and the
#     two-argument call came from a `sky-out/sky` built before that change. 22
#     red suites were an artefact of the binary, not a regression.
#
#   * The QUIET direction is the one that ships. A stale binary that happens to
#     PASS reports green for source it never compiled, on the repository's most
#     load-bearing verification, and nothing anywhere would catch it.
#     `scripts/build.sh:77` already carries a comment about an earlier incident
#     where the build "installed a pre-fix compiler" — so this has bitten
#     before and was closed by hand rather than by a gate.
#
# `scripts/regenerate-console.sh` is the sharpest case: it writes a CHECKED-IN
# generated file, so a stale compiler there commits wrong output to the repo.
#
# This is the shell counterpart of the freshness check in
# `rust/crates/xtask/src/config_matrix.rs` (`sky_binary` / `newest_source_mtime`),
# and it deliberately reuses that mechanism rather than inventing a second one.
# That gate had the identical defect until it was fixed: it measured whatever
# binary was on disk, so reverting the stage-3 precedence fix in `runtime-go/`
# WITHOUT rebuilding produced `config-matrix: OK` in 49 s, while the same edit
# after a 17.8 s `cargo build` produced six findings. CI was covered by the
# ORDERING of steps in a workflow, and an ordering nobody asserts is not a
# property.
#
# The difference in policy is deliberate: `config_matrix` REBUILDS, because it
# is one gate that owns its whole run. A script in a sweep does not get to spend
# a compiler build on the caller's behalf, and a sweep that silently rebuilds
# hides which tree it measured. So this one FAILS and names the fix — the same
# stance `scripts/lib/require-tool.sh` takes for a prerequisite that is missing,
# applied to a prerequisite that is out of date.
#
# Usage
# -----
#     source "$ROOT/scripts/lib/fresh-compiler.sh"
#     SKY="$ROOT/sky-out/sky"
#     require_fresh_compiler "$SKY"          # exits non-zero if stale/absent
#
# Contract
#   * Returns 0 when `$1` is an executable file at least as new as every source
#     input that determines what a `sky` binary does.
#   * EXITS 1 otherwise, naming the source file that is newer (the witness) and
#     the exact command that fixes it.
#   * EXITS 1 when the binary is absent, for the same reason and with the same
#     command.
#   * EXITS 2 when it cannot establish the answer at all — a declared source
#     root missing, or a walk that found implausibly few files. A check that
#     cannot see the sources would call every binary fresh, which is the
#     vacuity this file exists to refuse.
#
# There is NO opt-out, and that is a decision, not an oversight
# ------------------------------------------------------------
# `require_tool` takes `SKY_LIVE_TESTS=skip` because a host can genuinely lack
# PostgreSQL and there is nothing the operator can do about it in that run.
# There is no equivalent here: a tree that has the sources can always rebuild
# the compiler from them, so "I cannot fix this" is never true. `SKY_LIVE_TESTS`
# is therefore NOT read by this file — a run that skips the Postgres suites
# still may not measure a compiler from a different tree.
#
# What this does NOT catch
# ------------------------
#   * A binary from a DIFFERENT tree that happens to be newer — an installed
#     release copied in this morning, say. mtime answers "is this older than the
#     sources", not "was this built from them". That is the same instrument
#     `config_matrix.rs` uses, and replacing it belongs in one place, not two.
#   * A source edit that does not change the mtime (a restored backup with
#     `-p`, a clock moved backwards). `git checkout`/`git merge` DO update the
#     mtimes of the files they touch, which is the common case and is caught.
#   * Whether the build itself was correct. `scripts/lib/cargo-target.sh`
#     covers the neighbouring failure — `cp`ing a binary cargo did not just
#     produce.

if [ -n "${_SKY_FRESH_COMPILER_SOURCED:-}" ]; then
    return 0 2>/dev/null || true
fi
_SKY_FRESH_COMPILER_SOURCED=1

# The command that fixes every failure this file reports. Named once.
: "${SKY_FRESH_COMPILER_FIX:=./scripts/build.sh}"

# The source roots whose contents decide what a compiled `sky` binary DOES,
# with the minimum number of files each must contribute.
#
# Derived from how the binary is actually assembled, not from a guess:
# `rust/crates/ffi/build.rs` stages `sky-stdlib/`, `runtime-go/` (go.mod, go.sum,
# `rt/`, `cmd/`), `tools/sky-ffi-inspect/`, `templates/` and `sky-bundled/` into
# `$OUT_DIR/embedded-assets/`, and `rust/crates/ffi/src/assets.rs` embeds that
# tree with `include_dir!`. An edit to any of them with no recompile is exactly
# as stale as an edit to the compiler's own Rust.
#
# The per-root minimum is the vacuity guard. A walk that finds nothing makes
# every binary look fresh, so a root that has moved or emptied must fail rather
# than quietly contribute zero. The floors are set well under the current counts
# (rust/crates 132, sky-stdlib 87, runtime-go 127, templates 2, sky-bundled 15,
# tools/sky-ffi-inspect 4) — they catch a root that has vanished, not ordinary
# growth and pruning.
#
# `rust/crates/xtask` is excluded deliberately, matching
# `config_matrix.rs::MEASURED_SOURCE_ROOTS`: xtask is not linked into `sky`, so
# editing a gate does not change what an already-built compiler emits, and
# demanding a compiler rebuild for a comment in a test file would train people
# to bypass this check.
_SKY_COMPILER_INPUT_ROOTS='rust/crates:100
sky-stdlib:50
runtime-go:50
templates:1
sky-bundled:5
tools/sky-ffi-inspect:1'

# Print every source file under one root, applying that root's filters.
#
# Extra `find` predicates are passed through before `-print`, so the same walk
# serves both passes: no predicates counts the inputs, `-newer <bin>` lists the
# ones that postdate the binary.
_sky_compiler_inputs_in_root() { # <repo-root> <root-rel> [find predicates...]
    local repo="$1" rel="$2"
    shift 2
    case "$rel" in
        rust/crates)
            # The compiler crates plus their manifests: a dependency bump in a
            # Cargo.toml changes the binary as surely as a line of Rust.
            find "$repo/rust/crates" \
                \( -name target -o -name xtask -o -name node_modules -o -name .git \) -prune -o \
                -type f \( -name '*.rs' -o -name 'Cargo.toml' \) "$@" -print
            # The workspace manifest and lockfile pin every version compiled in.
            find "$repo/rust" -maxdepth 1 \
                -type f \( -name 'Cargo.toml' -o -name 'Cargo.lock' \) "$@" -print
            ;;
        runtime-go)
            # Exactly what `stage_runtime` copies: go.mod, go.sum, rt/, cmd/.
            # `*_test.go` and `testdata/` are dropped at stage time, so they are
            # not inputs to the binary (`skip_file` / `skip_dir` in build.rs).
            find "$repo/runtime-go" -maxdepth 1 \
                -type f \( -name 'go.mod' -o -name 'go.sum' \) "$@" -print
            find "$repo/runtime-go/rt" "$repo/runtime-go/cmd" \
                \( -name testdata -o -name node_modules -o -name .git \) -prune -o \
                -type f ! -name '*_test.go' ! -name '.DS_Store' "$@" -print
            ;;
        sky-bundled)
            # Source only. The committed build outputs under each bundled app
            # are dropped by `skip_dir`, and they change on every local build —
            # counting them would make the check fire on its own artefacts.
            find "$repo/sky-bundled" \
                \( -name sky-out -o -name .skycache -o -name .skydeps -o -name node_modules -o -name .git \) -prune -o \
                -type f ! -name '.DS_Store' "$@" -print
            ;;
        tools/sky-ffi-inspect)
            # The Go introspector source. The committed prebuilt binary of the
            # same name is an output, dropped by `skip_file`.
            find "$repo/tools/sky-ffi-inspect" \
                \( -name node_modules -o -name .git \) -prune -o \
                -type f ! -name 'sky-ffi-inspect' ! -name 'sky-ffi-inspect.exe' ! -name '.DS_Store' "$@" -print
            ;;
        *)
            find "$repo/$rel" \
                \( -name node_modules -o -name .git \) -prune -o \
                -type f ! -name '.DS_Store' "$@" -print
            ;;
    esac
}

# Print the repo-root-relative roots this file measures, one per line. Exposed
# so `rust/crates/xtask/tests/gates_measure_a_fresh_compiler.rs` can assert that
# this list and `config_matrix.rs`'s have not drifted apart.
sky_compiler_input_roots() {
    printf '%s\n' "$_SKY_COMPILER_INPUT_ROOTS" | while IFS=: read -r rel _min; do
        [ -n "$rel" ] && printf '%s\n' "$rel"
    done
}

# Resolve the repo root the way every caller means it: this file's grandparent
# (`scripts/lib/..`). Correct however the caller was invoked, and needs no `git`.
_sky_fresh_repo_root() {
    local self="${BASH_SOURCE[0]:-$0}"
    (cd "$(dirname "$self")/../.." && pwd)
}

# sky_compiler_freshness <binary> [<repo-root>]
#
# The whole decision, and it prints nothing — so a caller that wants to REBUILD
# a stale compiler (scripts/test-ci.sh's phase 1) uses the same answer as a
# caller that wants to REFUSE one, rather than growing a second predicate that
# can disagree with this one.
#
#   0  fresh
#   1  stale or absent      — $SKY_FRESH_REASON, $SKY_FRESH_WITNESS, $SKY_FRESH_COUNT
#   2  cannot be determined — $SKY_FRESH_REASON
#
# $SKY_FRESH_BIN is the resolved binary path in every case.
sky_compiler_freshness() {
    local bin="${1:-}" repo="${2:-}"
    SKY_FRESH_REASON=""; SKY_FRESH_WITNESS=""; SKY_FRESH_COUNT=0; SKY_FRESH_BIN="$bin"
    if [ -z "$bin" ]; then
        SKY_FRESH_REASON="no binary given"
        return 2
    fi
    [ -n "$repo" ] || repo="$(_sky_fresh_repo_root)"

    # A bare name is resolved on PATH first. `scripts/conformance.sh` and
    # `scripts/sky-suites.sh` fall back to `sky` when the tree has no build, and
    # an unresolved name would make this check answer "absent" for a compiler
    # that is very much present. Resolving it means the mtime comparison
    # actually runs against the binary the suite is about to use — an installed
    # `sky` older than the tree is the "certifies a months-old installed binary"
    # defect those scripts' own comments describe, and it fails here.
    case "$bin" in
        */*) ;;
        *)
            local _resolved
            _resolved=$(command -v "$bin" 2>/dev/null || true)
            case "$_resolved" in
                /*) bin="$_resolved"; SKY_FRESH_BIN="$bin" ;;
            esac
            ;;
    esac

    if [ ! -f "$bin" ] || [ ! -x "$bin" ]; then
        SKY_FRESH_REASON="absent"
        return 1
    fi

    # Pass 1 — count the inputs. A walk that finds nothing would call every
    # binary fresh, so an empty or missing root is a hard error, not a pass.
    local rel min count
    while IFS=: read -r rel min; do
        [ -n "$rel" ] || continue
        if [ ! -d "$repo/$rel" ]; then
            SKY_FRESH_REASON="source root '$rel' does not exist under $repo"
            return 2
        fi
        count=$( { _sky_compiler_inputs_in_root "$repo" "$rel" || true; } | wc -l | tr -d ' ')
        if [ "$count" -lt "$min" ]; then
            SKY_FRESH_REASON="the source walk found only $count file(s) under '$rel' (expected >= $min)"
            return 2
        fi
    done <<EOF
$_SKY_COMPILER_INPUT_ROOTS
EOF

    # Pass 2 — anything newer than the binary. `find -newer` compares mtimes and
    # is POSIX; `stat` is not portable between macOS and GNU and this file is
    # run on both.
    #
    # The trailing `|| true` is not decoration. Under `set -e` a command
    # substitution whose last command exits non-zero kills the CALLER, and
    # `find` does exactly that for a path it cannot read. The script would die
    # here — correctly refusing to proceed, and saying nothing about why.
    local newer
    newer=$(
        while IFS=: read -r rel min; do
            [ -n "$rel" ] || continue
            _sky_compiler_inputs_in_root "$repo" "$rel" -newer "$bin"
        done <<EOF
$_SKY_COMPILER_INPUT_ROOTS
EOF
        true
    ) || true

    if [ -z "$newer" ]; then
        return 0
    fi

    SKY_FRESH_REASON="stale"
    SKY_FRESH_COUNT=$(printf '%s\n' "$newer" | wc -l | tr -d ' ')
    SKY_FRESH_WITNESS=$(printf '%s\n' "$newer" | head -1)
    SKY_FRESH_WITNESS="${SKY_FRESH_WITNESS#"$repo"/}"
    return 1
}

# require_fresh_compiler <binary> [<repo-root>]
#
# Exits the script on anything other than "fresh". See the contract at the top.
require_fresh_compiler() {
    local bin="${1:-}" repo="${2:-}"
    [ -n "$repo" ] || repo="$(_sky_fresh_repo_root)"

    # `|| rc=$?`, never a bare call. Under `set -e` — which
    # `scripts/build-docs-site.sh` and `examples/39-hub-demo/run-demo.sh` both
    # set — a function that RETURNS non-zero in command position kills the
    # script on the spot, before the message below is ever printed. Measured
    # while writing this: the consumer exited 1 with completely empty output,
    # which is a correct verdict delivered as an unexplained failure. The
    # neighbouring bug (`&&` short-circuiting an install under `set -uo
    # pipefail` with no `-e`) is recorded at scripts/test-ci.sh:75.
    local rc=0
    sky_compiler_freshness "$bin" "$repo" || rc=$?
    case "$rc" in
        0) return 0 ;;
        2)
            echo "FAIL: this check cannot establish whether '${SKY_FRESH_BIN:-$bin}' was built from this tree." >&2
            echo "  $SKY_FRESH_REASON" >&2
            echo "  A walk that finds nothing makes any binary look fresh, and a measurement" >&2
            echo "  of an unknown compiler is not a measurement. Either the tree moved and" >&2
            echo "  scripts/lib/fresh-compiler.sh needs updating, or this checkout is" >&2
            echo "  incomplete." >&2
            exit 2
            ;;
    esac

    if [ "$SKY_FRESH_REASON" = "absent" ]; then
        echo "FAIL: the compiler this gate measures is not at '$SKY_FRESH_BIN'." >&2
        echo "  A gate cannot pass on a compiler it does not have." >&2
        echo "  Build and install it:  $SKY_FRESH_COMPILER_FIX" >&2
        exit 1
    fi

    echo "FAIL: '$SKY_FRESH_BIN' is older than the source it would be measuring." >&2
    echo "  $SKY_FRESH_COUNT input file(s) have changed since it was built. First one:" >&2
    echo "    $SKY_FRESH_WITNESS" >&2
    echo "  Measuring this binary would report a verdict about a different tree." >&2
    echo "  That is how a sweep once reported 22 of 22 conformance suites FAILED on a" >&2
    echo "  consistent tree — and, in the direction nobody notices, how a green run can" >&2
    echo "  certify source that was never compiled." >&2
    echo "" >&2
    echo "  Rebuild and install it:  $SKY_FRESH_COMPILER_FIX" >&2
    echo "  (a bare 'cargo build --release -p sky' writes rust/target/release/sky and" >&2
    echo "   does NOT install it to sky-out/sky — that gap is this failure.)" >&2
    exit 1
}

# Run directly — `bash scripts/lib/fresh-compiler.sh <binary> [<repo-root>]` —
# so the consumers that are not shell can reach the SAME check rather than each
# reimplementing it in its own language.
#
# `scripts/verify-live-resilience.mjs` and `scripts/verify-stdui-matrix.mjs` are
# Node; `scripts/lsp-test-skyshop.lua` runs inside nvim. Three languages, three
# chances for three subtly different notions of "fresh" — which is the shape
# `with-timeout.sh` found six times over when it replaced six hand-rolled
# fallbacks. There is one spelling, and it lives here.
if [ "${BASH_SOURCE[0]:-}" = "${0:-}" ]; then
    require_fresh_compiler "${1:-}" "${2:-}"
    echo "fresh-compiler: ${1:-} is current with the tree."
fi
