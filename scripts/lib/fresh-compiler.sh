#!/usr/bin/env bash
# scripts/lib/fresh-compiler.sh — a gate may not measure a compiler older than
# the source it claims to measure.
#
# Why this exists
# ---------------
# Sixteen scripts consume the compiler at `sky-out/sky`. Not one of them checked
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
# Two instruments, because mtime lies in both directions
# ------------------------------------------------------
# mtime comparison alone was wrong two ways at once:
#
#   * FALSE STALE — a legitimate prebuilt binary in a fresh checkout (Docker
#     multi-stage, artifact download, `git worktree add`) sits under sources
#     whose mtimes are all "now", so it failed even when its content matched the
#     tree byte-for-byte. The practical workaround became `touch sky-out/sky`,
#     which is the next bullet.
#   * FALSE FRESH — `touch`ing the binary (or copying it in without `-p`) makes
#     it mtime-newest while its content is from another tree entirely, and
#     mtime-only reported PASS.
#
# So the check now also compares CONTENT where content is provable: the build
# bakes a fingerprint of the embedded asset tree into the binary as
# `sky-embed-fp-v1:<sha256hex>` (`rust/crates/ffi/build.rs::fingerprint`), and
# `sky_embed_fingerprint_expected` recomputes the same value from the tree here
# in shell. A binary whose baked fingerprint MISMATCHES the tree fails no matter
# how new its mtime is; a binary whose fingerprint MATCHES passes even when
# embed-root mtimes postdate it.
#
# What remains mtime-based — stated plainly
# -----------------------------------------
# The fingerprint covers the EMBEDDED trees only (sky-stdlib/, runtime-go/,
# templates/, sky-bundled/, tools/sky-ffi-inspect/ — exactly what
# `rust/crates/ffi/build.rs` stages). The compiler's own Rust sources
# (`rust/…`) bake no content witness into the binary, so for them mtime is
# still the instrument: a prebuilt binary under rust/ sources that are
# mtime-newer FAILS even if it was in fact built from them, and a `touch`ed
# binary whose staleness is purely in rust/ sources still passes. Closing that
# would need a source fingerprint baked by the `sky` crate's own build — a
# separate change, not claimed here.
#
# Usage
# -----
#     source "$ROOT/scripts/lib/fresh-compiler.sh"
#     SKY="$ROOT/sky-out/sky"
#     require_fresh_compiler "$SKY"          # exits non-zero if stale/absent
#
# Contract
#   * Returns 0 when `$1` is an executable file that is (a) at least as new as
#     every source input that determines what a `sky` binary does, or (b) newer
#     only than EMBED-tree inputs while its baked embed fingerprint matches the
#     tree's content — and, in either case, whose baked fingerprint (when
#     present and computable) does not CONTRADICT the tree.
#   * EXITS 1 otherwise, naming the newest source file that postdates the
#     binary (the witness) and the exact command that fixes it.
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
#   * A binary from a DIFFERENT tree whose RUST sources differ but whose embed
#     trees are identical, when it is also mtime-newer than everything here.
#     The embed fingerprint clears the embedded content; nothing vouches for
#     the Rust (see "what remains mtime-based" above).
#   * A source edit that does not change the mtime (a restored backup with
#     `-p`, a clock moved backwards) in `rust/…`. In the embed trees the
#     fingerprint catches it.
#   * Whether the build itself was correct. `scripts/lib/cargo-target.sh`
#     covers the neighbouring failure — `cp`ing a binary cargo did not just
#     produce.
#
# NO source guard. There used to be one —
#
#     if [ -n "${_SKY_FRESH_COMPILER_SOURCED:-}" ]; then return 0 …
#
# — and it keyed on an ENVIRONMENT variable, which children inherit. Any
# process whose ancestor had sourced this file got a shell in which sourcing
# the library defined NOTHING, `require_fresh_compiler` was `command not
# found` (status 127), and the 14 of 16 consumers that run without `set -e`
# swallowed that status and measured the unverified binary anyway — the gate
# deleted by its own guard. Reproduced before removal: a consumer with the
# variable exported printed `command not found` and proceeded, rc=0. This file
# is a set of function definitions and `:=` defaults; sourcing it twice is
# harmless, so the guard bought nothing and could lose everything.
# `gates_measure_a_fresh_compiler.rs::an_inherited_source_guard_env_var_does_not_delete_the_gate`
# goes red if one comes back.
unset _SKY_FRESH_COMPILER_SOURCED

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
# `gates_measure_a_fresh_compiler.rs` parses build.rs's `stage(…)` calls and
# fails the build if this list, `config_matrix.rs::MEASURED_SOURCE_ROOTS` and
# build.rs stop naming the same trees.
#
# `rust` (the workspace manifest, `Cargo.lock`, `rust-toolchain.toml`) is its
# own root: those files pin every version compiled in, they live one level
# ABOVE `rust/crates`, and keeping them inside the `rust/crates` walk was a
# filter divergence from `config_matrix.rs`, which could not see them at all.
#
# The per-root minimum is the vacuity guard. A walk that finds nothing makes
# every binary look fresh, so a root that has moved or emptied must fail rather
# than quietly contribute zero. The floors are set well under the current counts
# — they catch a root that has vanished, not ordinary growth and pruning.
#
# `rust/crates/xtask` is excluded deliberately, matching
# `config_matrix.rs::MEASURED_SOURCE_ROOTS`: xtask is not linked into `sky`, so
# editing a gate does not change what an already-built compiler emits, and
# demanding a compiler rebuild for a comment in a test file would train people
# to bypass this check.
_SKY_COMPILER_INPUT_ROOTS='rust:2
rust/crates:100
sky-stdlib:50
runtime-go:50
templates:1
sky-bundled:5
tools/sky-ffi-inspect:1'

# The embed roots — the subset of the roots above that build.rs stages into the
# binary and the fingerprint therefore covers. Order matters ONLY for the walk;
# the fingerprint sorts.
_SKY_EMBED_ROOTS='sky-stdlib
runtime-go
templates
sky-bundled
tools/sky-ffi-inspect'

# Walk one directory with EXACTLY the staging filters of
# `rust/crates/ffi/build.rs::{skip_dir,skip_file}` — one definition of "what is
# embedded", spelled twice, gated together (the fingerprint-parity test fails
# if they drift). Extra `find` predicates are passed through before `-print`.
#
# Hidden entries are pruned as a CLASS, mirroring build.rs and
# `config_matrix.rs::walk_newest` (`name.starts_with('.')`). That is both a
# filter-alignment fix and a correctness fix here: running a bundled console
# writes a runtime `sky-bundled/<app>/.sky/console-token`, and counting it as a
# "source" made every gate report the compiler stale until the next rebuild —
# a red caused by the check's own instrument, and the same class of file the
# embed must never contain (it is a SECRET).
_sky_embed_files() { # <dir> [find predicates...]
    local dir="$1"
    shift
    [ -d "$dir" ] || return 0
    find "$dir" \
        \( -name '.*' -o -name sky-out -o -name testdata -o -name node_modules \) -prune -o \
        -type f \
        ! -name '.*' ! -name '*_test.go' \
        ! -name 'sky-ffi-inspect' ! -name 'sky-ffi-inspect.exe' \
        ! -name '*.bak' ! -name '*.swp' ! -name '*~' \
        "$@" -print
}

# Print every source file under one root, applying that root's filters.
#
# The same walk serves three passes: no predicates counts the inputs,
# `-newer <bin>` lists the ones that postdate the binary, and the embed roots'
# output feeds the fingerprint.
_sky_compiler_inputs_in_root() { # <repo-root> <root-rel> [find predicates...]
    local repo="$1" rel="$2"
    shift 2
    case "$rel" in
        rust)
            # The workspace manifest, lockfile and toolchain/format pins —
            # maxdepth 1, matching what actually lives there.
            find "$repo/rust" -maxdepth 1 \
                -type f \( -name '*.toml' -o -name 'Cargo.lock' \) "$@" -print
            ;;
        rust/crates)
            # The compiler crates plus their manifests: a dependency bump in a
            # Cargo.toml changes the binary as surely as a line of Rust.
            find "$repo/rust/crates" \
                \( -name '.*' -o -name target -o -name xtask -o -name node_modules \) -prune -o \
                -type f ! -name '.*' \( -name '*.rs' -o -name 'Cargo.toml' \) "$@" -print
            ;;
        runtime-go)
            # Exactly what `stage_runtime` copies: go.mod, go.sum, rt/, cmd/.
            find "$repo/runtime-go" -maxdepth 1 \
                -type f \( -name 'go.mod' -o -name 'go.sum' \) "$@" -print
            _sky_embed_files "$repo/runtime-go/rt" "$@"
            _sky_embed_files "$repo/runtime-go/cmd" "$@"
            ;;
        *)
            # sky-stdlib, templates, sky-bundled, tools/sky-ffi-inspect: the
            # plain staging walk. Committed build outputs (`sky-out/` under
            # `sky-bundled/*`) and the committed prebuilt inspector binary are
            # dropped by the shared filters, exactly as build.rs drops them
            # from the embed.
            _sky_embed_files "$repo/$rel" "$@"
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

# The sha256 line tool available on this host, or nothing. GNU `sha256sum` and
# BSD/perl `shasum -a 256` print the identical `"<hex>  <path>"` line format,
# which is exactly the manifest-line format build.rs constructs.
_sky_sha256_tool() {
    if command -v sha256sum >/dev/null 2>&1; then
        echo "sha256sum"
    elif command -v shasum >/dev/null 2>&1; then
        echo "shasum -a 256"
    fi
}

# sky_embed_fingerprint_expected <repo-root>
#
# The sha256 fingerprint (bare 64-hex) the embedded asset tree of a binary
# built from THIS tree would carry — the same construction as
# `rust/crates/ffi/build.rs::fingerprint`, computed from the sources:
# per staged file one line `"<sha256(bytes)>  <relpath>"`, lines sorted by
# relpath as bytes (`LC_ALL=C`), fingerprint = sha256 of that manifest.
# Returns 1 (prints nothing) when no sha256 tool exists or the walk is empty.
sky_embed_fingerprint_expected() {
    local repo="${1:?repo root required}" tool
    tool=$(_sky_sha256_tool)
    [ -n "$tool" ] || return 1
    # Walk with repo="." from inside the repo so the paths are relpaths — the
    # manifest must contain relpaths, and stripping an absolute prefix with sed
    # would make the strip pattern out of the repo PATH, which may contain
    # regex metacharacters.
    local rels
    rels=$(
        cd "$repo" || exit 1
        while IFS= read -r r; do
            [ -n "$r" ] || continue
            _sky_compiler_inputs_in_root "." "$r"
        done <<EOF
$_SKY_EMBED_ROOTS
EOF
    ) || return 1
    rels=$(printf '%s\n' "$rels" | sed 's|^\./||' | LC_ALL=C sort)
    [ -n "$rels" ] || return 1
    (
        cd "$repo" || exit 1
        printf '%s\n' "$rels" | tr '\n' '\0' | xargs -0 $tool | $tool | awk '{print $1}'
    )
}

# _sky_embed_fp_from_binary <binary>
#
# The embed fingerprint baked into the binary (bare 64-hex), recovered by
# scanning for the `sky-embed-fp-v1:` marker `build.rs` emits. Returns 1 when
# the binary carries no marker (a build from before the marker existed) or —
# refusing to guess — more than one DISTINCT value.
_sky_embed_fp_from_binary() {
    local vals
    vals=$(LC_ALL=C grep -a -o 'sky-embed-fp-v1:[0-9a-f]\{64\}' "$1" 2>/dev/null | LC_ALL=C sort -u) || true
    [ -n "$vals" ] || return 1
    [ "$(printf '%s\n' "$vals" | wc -l | tr -d ' ')" = "1" ] || return 1
    printf '%s\n' "${vals#sky-embed-fp-v1:}"
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
# $SKY_FRESH_REASON on 1 is "absent", "stale" (mtime), or "embed-mismatch"
# (the binary's baked embed fingerprint contradicts the tree's content — a
# `touch`ed or foreign binary). $SKY_FRESH_BIN is the resolved binary path in
# every case.
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
    # that is very much present. Resolving it means the comparison actually
    # runs against the binary the suite is about to use — an installed `sky`
    # older than the tree is the "certifies a months-old installed binary"
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

    # Pass 3 — content, where content is provable. The baked fingerprint, when
    # the binary carries one, settles the embed trees in BOTH directions.
    local baked=""
    baked=$(_sky_embed_fp_from_binary "$bin") || baked=""

    if [ -z "$newer" ]; then
        # mtime says fresh. A `touch`ed or copied-in binary looks exactly like
        # this, so when the binary can testify about its own content, make it:
        # a fingerprint mismatch is a stale embed no matter what the mtimes say.
        if [ -n "$baked" ]; then
            local expected=""
            expected=$(sky_embed_fingerprint_expected "$repo") || expected=""
            if [ -n "$expected" ] && [ "$baked" != "$expected" ]; then
                SKY_FRESH_REASON="embed-mismatch"
                return 1
            fi
        fi
        return 0
    fi

    # mtime says stale. When every newer file is in an EMBED root and the
    # binary's baked fingerprint matches the tree's content byte-for-byte, the
    # mtimes are the artefact (fresh checkout, prebuilt binary) — the binary
    # embeds exactly this tree. Rust sources carry no content witness, so any
    # newer file under rust/ keeps the mtime verdict.
    local rust_newer=0 f
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        case "$f" in
            "$repo/rust/"*) rust_newer=1 ;;
        esac
    done <<EOF
$newer
EOF
    if [ "$rust_newer" -eq 0 ] && [ -n "$baked" ]; then
        local expected=""
        expected=$(sky_embed_fingerprint_expected "$repo") || expected=""
        if [ -n "$expected" ] && [ "$baked" = "$expected" ]; then
            return 0
        fi
    fi

    SKY_FRESH_REASON="stale"
    SKY_FRESH_COUNT=$(printf '%s\n' "$newer" | wc -l | tr -d ' ')
    # The witness is the NEWEST changed file — the traversal-order head of the
    # list was whatever root happened to be walked first, which pointed readers
    # at a bystander. (`config_matrix.rs::newest_source_mtime` tracks newest for
    # the same reason.)
    local best=""
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        if [ -z "$best" ] || [ "$f" -nt "$best" ]; then
            best="$f"
        fi
    done <<EOF
$newer
EOF
    SKY_FRESH_WITNESS="${best#"$repo"/}"
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

    if [ "$SKY_FRESH_REASON" = "embed-mismatch" ]; then
        echo "FAIL: '$SKY_FRESH_BIN' is mtime-current but its EMBEDDED CONTENT is not from this tree." >&2
        echo "  The fingerprint baked into the binary (sky-embed-fp-v1:…) does not match the" >&2
        echo "  fingerprint of this tree's embeddable sources. mtime cannot see this — a" >&2
        echo "  touched or copied-in binary looks brand new — which is exactly why the check" >&2
        echo "  compares content. Measuring this binary would certify a different tree." >&2
        echo "" >&2
        echo "  Rebuild and install it:  $SKY_FRESH_COMPILER_FIX" >&2
        exit 1
    fi

    echo "FAIL: '$SKY_FRESH_BIN' is older than the source it would be measuring." >&2
    echo "  $SKY_FRESH_COUNT input file(s) have changed since it was built. Newest:" >&2
    echo "    $SKY_FRESH_WITNESS" >&2
    echo "  Measuring this binary would report a verdict about a different tree." >&2
    echo "  That is how a sweep once reported 22 of 22 conformance suites FAILED on a" >&2
    echo "  consistent tree — and, in the direction nobody notices, how a green run can" >&2
    echo "  certify source that was never compiled." >&2
    echo "" >&2
    echo "  Rebuild and install it:  $SKY_FRESH_COMPILER_FIX" >&2
    echo "  (a bare 'cargo build --release -p sky' writes rust/target/release/sky and" >&2
    echo "   does NOT install it to sky-out/sky — that gap is this failure.)" >&2
    echo "  A prebuilt binary whose embedded assets match this tree passes by content" >&2
    echo "  even with these mtimes — but a change under rust/ has no content witness," >&2
    echo "  so only a rebuild can clear it. Do not touch(1) the binary: a fingerprint" >&2
    echo "  mismatch fails regardless of mtimes." >&2
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
