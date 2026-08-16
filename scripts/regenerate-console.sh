#!/usr/bin/env bash
#
# regenerate-console.sh — produce runtime-go/rt/console_app/main.go
# by running the LOCAL sky binary against sky-bundled/console/.
#
# Why this exists (v0.16.0 PR 1):
# The old "console as subprocess + reverse-proxy" path OOMs e2-micro
# VMs because the bundled console's `sky build` runs a recursive
# `go build` on first launch. v0.16.0 inlines the console — the
# Sky-source UI is translated to Go ONCE at compiler build/release
# time and committed under runtime-go/rt/console_app/ as a
# subpackage of `sky-app/rt`. The user's app binary then has the
# console code statically linked, no subprocess required.
#
# This script:
#   1. Builds the local `sky` compiler via cabal (overwriting
#      sky-out/sky). Cabal is the source-of-truth toolchain — we
#      cannot use a pre-installed `sky` because we need to capture
#      whatever changes are in this checkout's compiler / stdlib.
#   2. Wipes sky-bundled/console/sky-out + skycaches so the run is
#      hermetic (no stale typed FFI cache).
#   3. Invokes `sky build` against sky-bundled/console/src/Main.sky
#      — this emits a fully-typed sky-bundled/console/sky-out/main.go.
#   4. Transforms that file into runtime-go/rt/console_app/main.go:
#        - `package main` → `package console_app`
#        - strips the leading `func init()` that calls
#          rt.SetPortDefault / SetSkyDefault (those would interfere
#          with the host app's runtime defaults).
#        - strips `func main()` (entry point belongs to the host app).
#        - prepends a DO-NOT-EDIT header pointing back here.
#   5. Drift detection: re-running the script must be a no-op when
#      the Sky source is unchanged. CI runs this + `git diff
#      --exit-code runtime-go/rt/console_app/`.
#
# Usage:
#   scripts/regenerate-console.sh             # full regenerate
#   SKY_REGEN_SKIP_BUILD=1 scripts/regenerate-console.sh
#                                             # skip the compiler
#                                             # rebuild (CI: use the
#                                             # binary cargo already
#                                             # built upstream in
#                                             # the workflow).
#
# Side effects:
#   - Overwrites sky-out/sky (the locally-built compiler).
#   - Wipes sky-bundled/console/sky-out + .skycache + .skydeps.
#   - Overwrites runtime-go/rt/console_app/main.go.
#
# Exit codes:
#   0 — regenerated cleanly
#   1 — cabal install failed, sky build failed, or transform
#       couldn't find expected anchors in the generated Go.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
# `require_fresh_compiler <bin>` — see the header of scripts/lib/fresh-compiler.sh.
source "$ROOT/scripts/lib/fresh-compiler.sh"
cd "$ROOT"

# ANSI colours for the script's own diagnostics — only when stderr
# is a terminal so CI logs stay plain.
if [ -t 2 ]; then
    _bold=$'\033[1m'; _dim=$'\033[2m'; _red=$'\033[31m'; _green=$'\033[32m'; _reset=$'\033[0m'
else
    _bold=""; _dim=""; _red=""; _green=""; _reset=""
fi
say() { printf '%s[regen-console]%s %s\n' "$_bold" "$_reset" "$*" >&2; }
warn() { printf '%s[regen-console]%s %s\n' "$_red" "$_reset" "$*" >&2; }

if [ "${SKY_REGEN_SKIP_BUILD:-0}" = "1" ]; then
    say "skipping compiler rebuild (SKY_REGEN_SKIP_BUILD=1)"
    if [ ! -x "sky-out/sky" ]; then
        warn "sky-out/sky missing — set SKY_REGEN_SKIP_BUILD=0 or pre-build it."
        exit 1
    fi
else
    say "building local sky binary via cargo build (this can take ~2 min on cold cache)..."
    ( cd rust && cargo build --release --locked -p sky ) >&2
    mkdir -p ./sky-out
    # Honour CARGO_TARGET_DIR — cargo puts the binary under $CARGO_TARGET_DIR
    # when set (a common dev setup), not the default rust/target. Fall back to
    # rust/target otherwise.
    CARGO_BIN="${CARGO_TARGET_DIR:-$ROOT/rust/target}/release/sky"
    if [ ! -x "$CARGO_BIN" ]; then
        warn "built sky binary not found at $CARGO_BIN (CARGO_TARGET_DIR=${CARGO_TARGET_DIR:-unset})"
        exit 1
    fi
    cp "$CARGO_BIN" ./sky-out/sky
fi

SKY="$ROOT/sky-out/sky"
# This script writes a CHECKED-IN generated file, so a stale compiler here does
# not merely mis-report — it commits wrong output to the repository, and the
# `SKY_REGEN_SKIP_BUILD=1` branch above accepts any `sky-out/sky` that happens
# to be executable. `require_fresh_compiler` covers both branches: the built one
# passes by construction, the skipped one is checked. See the header of
# scripts/lib/fresh-compiler.sh.
require_fresh_compiler "$SKY" "$ROOT"
say "using $($SKY --version 2>&1 | head -1)"

CONSOLE_SRC="${SKY_CONSOLE_SRC:-$ROOT/sky-bundled/console}"
if [ ! -f "$CONSOLE_SRC/src/Main.sky" ]; then
    warn "console Sky source not found at $CONSOLE_SRC/src/Main.sky."
    warn ""
    warn "v0.16.0 PR 2 deleted the in-tree sky-bundled/console/ tree —"
    warn "the canonical artefact is runtime-go/rt/console_app/main.go"
    warn "(committed). Set SKY_CONSOLE_SRC to point at an external"
    warn "checkout of the Sky source to re-run this script."
    warn ""
    warn "Drift detection (CI): when sky-bundled/console is absent,"
    warn "this script's exit-code-1 is treated as 'no-op' by the"
    warn "calling workflow."
    exit 2
fi

say "wiping previous console sky-out + skycache for hermetic build"
rm -rf "$CONSOLE_SRC/sky-out" "$CONSOLE_SRC/.skycache" "$CONSOLE_SRC/.skydeps"

say "running sky build against sky-bundled/console/src/Main.sky"
# `sky build` writes to <cwd>/sky-out — we cd into the project dir so
# the output lands inside sky-bundled/console/sky-out, not at the repo
# root (which would clobber the compiler binary; see CLAUDE.md
# "Never run sky build from the repo root").
#
# The console ships TWO `module Main` entries — Main.sky (Sky.Live) and
# MainTui.sky (Sky.Tui) — sharing State.sky / View.sky / the tab
# modules. The Rust compiler discovers EVERY `src/*.sky` as a project
# module, so if both entries are present it sees two `main`s and the
# Tui one can win — the emitted `func main()` calls `Tui_app` and the
# Live-only bindings (`viewWrapped`, `Ui.layout`) get DCE'd, producing a
# Tui-flavoured console_app that renders an empty body when mounted as a
# Live sub-app. `crates/sky/src/bundled.rs::materialise` handles this
# for the runtime `sky console` build by DROPPING the non-target entry;
# we mirror that here — move MainTui.sky aside for the duration of the
# Live build so the compiler sees exactly one `module Main`.
_tui_entry="$CONSOLE_SRC/src/MainTui.sky"
_tui_stash="$CONSOLE_SRC/MainTui.sky.stash"
if [ -f "$_tui_entry" ]; then
    mv -f "$_tui_entry" "$_tui_stash"
    # Always restore, even on build failure / early exit.
    trap 'mv -f "$_tui_stash" "$_tui_entry" 2>/dev/null || true' EXIT
fi
#
# v0.16.4 — SKY_BUILD_IS_INLINE_CONSOLE=1 tells the compiler to skip
# the otherwise-automatic `_ "sky-app/rt/console_app"` self-import
# in the emitted `main.go`. Without this, the post-transform
# `package console_app` would import its own future incarnation and
# `go build` rejects the cycle. The gate is in
# src/Sky/Build/Compile.hs (`globalIsInlineConsoleBuild`).
(
    cd "$CONSOLE_SRC"
    # SKY_RUNTIME_DIR points at the worktree-root runtime-go so the
    # compiler's `locateRuntimeDir` probe (which walks up from cwd
    # AND from the binary's path) finds the in-tree edits rather
    # than falling through to the embedded copy baked into the
    # binary at TH-time. Without this, edits to runtime-go/rt/*.go
    # under a worktree cwd silently fall through to the stale
    # embedded snapshot — visible as "undefined rt.NewSymbol" go
    # build failures even though `strings sky-out/sky` shows the
    # symbol IS in the binary.
    SKY_RUNTIME_DIR="$ROOT/runtime-go" \
        SKY_BUILD_IS_INLINE_CONSOLE=1 \
        with_timeout 600 "$SKY" build src/Main.sky
)

GENERATED="$CONSOLE_SRC/sky-out/main.go"
if [ ! -f "$GENERATED" ]; then
    warn "expected output at $GENERATED — sky build didn't write it"
    exit 1
fi

OUT_DIR="$ROOT/runtime-go/rt/console_app"
OUT="$OUT_DIR/main.go"
mkdir -p "$OUT_DIR"

say "transforming generated Go (package + entry trimming)"
# The transformation is a structural rewrite, not a regex hack — we
# anchor on lines we know the compiler emits verbatim:
#   - Line 1: "package main"
#   - First "func init() {" body contains rt.SetPortDefault.
#   - Last "func main() {" block at EOF.
# Use awk to walk line-by-line so we can do reliable block-level
# rewriting.
awk -v src_relative="sky-bundled/console/src/Main.sky" '
BEGIN {
    print "// Code generated by scripts/regenerate-console.sh — DO NOT EDIT."
    print "//"
    print "// Source: " src_relative " (compiled by the local `sky` binary)"
    print "// Regenerate with: scripts/regenerate-console.sh"
    print "//"
    print "// This file is the v0.16.0 inline console UI — a Std.Ui Sky.Live"
    print "// app translated to Go ONCE at compiler-release time and embedded"
    print "// as a sibling subpackage of `sky-app/rt`. The host application"
    print "// mounts it via rt.MountInlineConsole when SKY_CONSOLE_MODE=inline."
    print ""

    # State machine flags:
    #   stripped_init = 0     skip the FIRST init() block (port defaults)
    #   stripping     = 0     1 while we are skipping lines inside the
    #                         port-defaults init() or func main().
    #   skip_origin   = 0     1 to skip a leading "// SKY-ORIGIN:" comment
    #                         immediately preceding func main()
    stripped_init = 0
    stripping = 0
    skip_origin = 0
    saw_pkg = 0
}

# First line must be "package main"; rewrite to console_app.
NR == 1 {
    if ($0 != "package main") {
        print "regenerate-console.sh: expected \"package main\" at line 1, got: " $0 | "cat 1>&2"
        exit 1
    }
    print "package console_app"
    saw_pkg = 1
    next
}

# The ADT registries are keyed by the PACKAGE-QUALIFIED ADT name, which
# codegen emits as `main.<Type>` (one hardcoded package clause, see
# rust/crates/codegen/src/lib.rs). This file is `package console_app`, so
# its keys must say so too — `reflect.Type.String()` on a console type
# reports `console_app.State_Msg`, and that is what the runtime compares
# against (runtime-go/rt/live.go, msgAdtFromUpdate).
#
# This rewrite is load-bearing, not cosmetic. The console is compiled
# from sky-bundled/console/src/State.sky, so its Msg IS `State_Msg` —
# the same Go type name a user app whose own module is `State.sky` gets
# (examples 12-skyvote, 13-skyshop, 16-skychess all do). Without the
# package qualifier the two collide in one process-global key and a
# client-supplied wire string resolves against whichever init() ran
# first. rt.RegisterAdtTag panics on the conflicting tag rather than
# picking a winner, so dropping this rewrite fails loudly at startup.
{
    gsub(/rt\.RegisterAdtTag\("main\./, "rt.RegisterAdtTag(\"console_app.")
    gsub(/rt\.RegisterAdtVariant\("main\./, "rt.RegisterAdtVariant(\"console_app.")
}

# When we hit the FIRST `func init() {` whose body is the port-default
# setup, strip the entire block (until matching closing brace at column
# 0). We detect it by looking ahead for `rt.SetPortDefault`.
stripping == 0 && stripped_init == 0 && $0 ~ /^func init\(\) \{$/ {
    # Read the body to detect the port-default signature.
    # Hold the lines so we can either emit or drop them.
    block = $0
    while ((getline line) > 0) {
        block = block "\n" line
        if (line ~ /^\}$/) {
            break
        }
    }
    if (block ~ /rt\.SetPortDefault/) {
        # Drop this block; mark it handled.
        stripped_init = 1
        # Suppress the trailing blank line that originally separated
        # this init() from the next decl.
        if ((getline line) > 0) {
            if (line != "") {
                print line
            }
        }
        next
    } else {
        # Not the port-defaults block; emit it as-is.
        print block
        next
    }
}

# Strip the `// SKY-ORIGIN: src/Main.sky:NNN:1` comment that
# immediately precedes the final `func main() {`. We hold it in a
# one-line buffer.
$0 ~ /^\/\/ SKY-ORIGIN: src\/Main\.sky:[0-9]+:1$/ {
    pending_origin = $0
    next
}

# Func main → drop the whole block including any deferred SKY-ORIGIN
# comment we held above.
$0 ~ /^func main\(\) \{$/ {
    # Discard pending_origin if it was for this func.
    pending_origin = ""
    # Read until matching `^}$`.
    while ((getline line) > 0) {
        if (line ~ /^\}$/) {
            break
        }
    }
    next
}

# OBSOLETE MITIGATION — kept only because it is now inert. Do not
# rely on it, and do not "repair" it; the collision it defended
# against is fixed at the source.
#
# v0.16.0 PR 2 added this to drop init() blocks whose body is entirely
# rt.RegisterAdtTag() calls. Those calls would otherwise pollute the
# NOTE: no apostrophes below this line. This comment block sits INSIDE the
# single-quoted awk program opened at the top of this command, so an
# apostrophe closes that quote and bash then parses the rest of the awk
# source as shell. It did: three of them (binary+s, console+s, app+s) made
# this whole file unparseable — `bash -n scripts/regenerate-console.sh`
# reported a syntax error near an unexpected token — so the generator that
# writes a CHECKED-IN file could not run at all.
#
# the host binary global rt.adtTagRegistry (rt.go) with the inline
# console ADT tags — colliding with user-app Msg names sharing any of
# {Tick, SelectTab, GotOverview, ...} and silently mis-routing wire
# event dispatch. "PR 3 reintroduces these via namespaced
# Register/Lookup APIs" — PR 3 never landed.
#
# TWO things then happened, and the second is why this block is dead:
#
#  1. The strip is defeated by design. It only fires on a block whose
#     EVERY statement is a `rt.RegisterAdtTag(...)` call; the comment
#     below even anticipated "a future combined RegisterAdtTag +
#     RegisterGobType". `rt.RegisterMsgVariant` became exactly that
#     future addition — codegen now emits it beside every
#     RegisterAdtTag — so every generated init() block is "mixed" and
#     is preserved verbatim. The mitigation stopped firing silently,
#     and console_app/main.go carries 100 registrations today.
#
#  2. The collision is fixed properly. rt.adtTagRegistry and
#     rt.adtVariantRegistry are keyed by (owning ADT, ctor), not by the
#     bare ctor name (runtime-go/rt/adt_variant_factory.go, AdtCtorKey),
#     and the wire path resolves a client-supplied Msg string only
#     within the Msg ADT owned by the app. Console registrations can no longer
#     shadow a user Msg constructor, so there is nothing to strip.
#
# Left in place rather than deleted because it is provably inert (no
# generated block is strip-eligible) and removing awk state machinery
# from a generator carries more risk than it removes.
#
# We detect the pattern by buffering the entire init() block, then
# checking that EVERY non-brace / non-blank line is exactly a
# `rt.RegisterAdtTag(...)` invocation. Mixed init blocks are preserved
# verbatim — drop is conservative.
$0 ~ /^func init\(\) \{/ {
    block = $0
    adt_lines = 0
    other_lines = 0
    # Single-line form: `func init() { rt.RegisterAdtTag(...) }`
    if ($0 ~ /\}$/) {
        # Strip the func init() { and trailing }; check body
        body = $0
        sub(/^func init\(\) \{[[:space:]]*/, "", body)
        sub(/[[:space:]]*\}$/, "", body)
        # Split on `;` and check each statement
        n = split(body, parts, /;/)
        all_adt = (n > 0)
        for (i = 1; i <= n; i++) {
            stmt = parts[i]
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", stmt)
            if (stmt == "") continue
            if (stmt !~ /^rt\.RegisterAdtTag\(/) {
                all_adt = 0
                break
            }
        }
        if (all_adt) {
            # Skip the block entirely; consume the trailing blank line
            # if any so we dont leave a gap.
            if ((getline line) > 0) {
                if (line != "") {
                    print line
                }
            }
            next
        }
        # Mixed / no RegisterAdtTag — fall through to default print
    } else {
        # Multi-line form: read until matching `}`
        while ((getline line) > 0) {
            block = block "\n" line
            if (line ~ /^\}$/) {
                break
            }
            stripped = line
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", stripped)
            if (stripped == "") continue
            if (stripped ~ /^rt\.RegisterAdtTag\(/) {
                adt_lines++
            } else {
                other_lines++
            }
        }
        if (adt_lines > 0 && other_lines == 0) {
            # Pure RegisterAdtTag block — drop it.
            if ((getline line) > 0) {
                if (line != "") {
                    print line
                }
            }
            next
        }
        # Mixed or no RegisterAdtTag — emit the buffered block verbatim.
        print block
        next
    }
}

# Default emit. Flush any pending SKY-ORIGIN comment first (for the
# case where it belonged to some other top-level decl, not main).
{
    if (pending_origin != "") {
        print pending_origin
        pending_origin = ""
    }
    print
}

END {
    if (saw_pkg == 0) {
        print "regenerate-console.sh: never saw \"package main\" — input was empty?" | "cat 1>&2"
        exit 1
    }
    if (stripped_init == 0) {
        print "regenerate-console.sh: never found the port-defaults init() block — was the Sky source rewritten?" | "cat 1>&2"
        exit 1
    }
}
' "$GENERATED" > "$OUT.tmp"

# `awk` exit codes inside END/print piped to cat 1>&2 do NOT propagate
# in some awks; double-check the output is non-trivial.
if [ ! -s "$OUT.tmp" ]; then
    warn "transformed output is empty — bailing"
    rm -f "$OUT.tmp"
    exit 1
fi
mv "$OUT.tmp" "$OUT"

# Sanity check: gofmt the result so trailing-whitespace / brace fixes
# don't show up in drift detection. (gofmt is idempotent and ships
# with every Go installation; this is just hygienic, not transformative.)
if command -v gofmt >/dev/null 2>&1; then
    gofmt -w "$OUT"
fi

say "${_green}wrote $OUT (${_dim}$(wc -l <"$OUT") lines${_reset}${_green})${_reset}"
say "drift check: 'git diff --exit-code runtime-go/rt/console_app/' should be clean"
