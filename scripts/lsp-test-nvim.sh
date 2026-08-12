#!/usr/bin/env bash
# Run the Neovim-headless LSP test suite. Each test exercises a single
# user-visible LSP behaviour (hover / completion / goto-def) end-to-end
# through Neovim's real LSP client — so it catches editor-level bugs
# that synthetic JSON-RPC tests miss (label-vs-insertText, filterText,
# scope handling, etc.).
#
# Usage:  PATH="<dir holding the sky under test>:$PATH" scripts/lsp-test-nvim.sh [--json <path>]
#
# THE COMPILER UNDER TEST IS THE ONE ON `$PATH`, and there is no fallback. Both
# callers (`xtask lsp` and the `lsp` harness gate) put the binary they just built
# at the front of `$PATH`. Until 2026-08-12 the 17-case half of this suite
# preferred `$PWD/sky-out/sky` instead, so the two halves could measure two
# different compilers and report one verdict.
#
# `--json <path>` additionally writes {"total":N,"failures":[...]} to <path>.
# The gate harness reads that FILE rather than scraping this script's stdout —
# `docs/ci-test-architecture-v2.md` §5.3(d): no `grep` in a verdict path. The
# human-readable per-case lines are unchanged either way.
#
# Exit code: 0 if all tests pass, non-zero with first failure name.

set -u

JSON_OUT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --json) JSON_OUT="${2:?--json needs a path}"; shift 2 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

PROJECT_DIR="${LSP_NVIM_PROJECT:-/tmp/lsp-real-test}"
mkdir -p "$PROJECT_DIR/src"

# Minimal sky.toml so the LSP can find the project root.
if [ ! -f "$PROJECT_DIR/sky.toml" ]; then
    cat > "$PROJECT_DIR/sky.toml" <<'EOF'
[project]
name = "lsp-real-test"
version = "0.0.0"
EOF
fi

TESTS=(
    hover-task-run
    hover-field
    hover-type-name
    completion-qualified-insert-text
    completion-field
    completion-let-binding
    goto-def-type-name
    # v0.13 G — every USED symbol class
    hover-function-use
    goto-def-function
    hover-ctor-use
    hover-lambda-param
    hover-case-pattern
    hover-kernel-call
    # v0.13 G follow-up — goto-def for the remaining symbol classes
    goto-def-ctor
    goto-def-let-binding
    goto-def-lambda-param
    goto-def-field
)

# The CORPUS groups (scripts/lsp-corpus-nvim.lua). Each runs many cases against
# ONE LSP session and prints one PASS/FAIL line per case — the single-fixture
# suite above cannot express cross-module resolution, the import shapes, a
# diagnostic's editor-visible code+range, or a real app.
CORPUS_GROUPS=(
    multimodule
    diagnostics
    realapp
)

CORPUS_DIR="${LSP_NVIM_CORPUS:-/tmp/lsp-corpus-work}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

failures=()
total=0

for t in "${TESTS[@]}"; do
    out=$(nvim --headless -u NONE -l scripts/lsp-test-nvim.lua "$PROJECT_DIR" "$t" 2>&1)
    result=$(printf '%s' "$out" | grep -oE '(PASS|FAIL): [^"]*' | head -1)
    total=$((total + 1))
    if [ -z "$result" ]; then
        result="??: $t (no PASS/FAIL marker — check raw output)"
        failures+=("$t")
    elif [[ "$result" == FAIL:* ]]; then
        failures+=("$t")
    fi
    echo "$result"
done

for g in "${CORPUS_GROUPS[@]}"; do
    out=$(nvim --headless -u NONE -l scripts/lsp-corpus-nvim.lua \
              "$CORPUS_DIR" "$g" "$REPO_ROOT" 2>&1)
    # Per-case lines. `-a` because nvim can emit non-UTF8 bytes on its message
    # stream, which would make grep treat the whole stream as binary and print
    # nothing — a silent zero-case group.
    lines=$(printf '%s' "$out" | grep -aoE '(PASS|FAIL): [A-Za-z0-9_-]+' || true)
    parsed=$(printf '%s' "$lines" | grep -ac . || true)
    # The driver states how many cases it ran. If we parsed a different number,
    # a result line was swallowed — treat that as a failure of the GROUP rather
    # than quietly reporting fewer cases than actually ran.
    declared=$(printf '%s' "$out" | grep -aoE "CASES: $g [0-9]+" | grep -oE '[0-9]+$' || true)

    if [ -n "$lines" ]; then
        printf '%s\n' "$lines"
    fi

    if [ -z "$declared" ]; then
        echo "FAIL: corpus/$g: the group printed no CASES: line (it crashed, or nvim is broken)"
        failures+=("corpus/$g")
        total=$((total + parsed))
        continue
    fi
    if [ "$parsed" -ne "$declared" ]; then
        echo "FAIL: corpus/$g: parsed $parsed result lines but the group ran $declared cases"
        failures+=("corpus/$g")
    fi
    total=$((total + declared))
    while IFS= read -r l; do
        case "$l" in
            FAIL:*) failures+=("${l#FAIL: }") ;;
        esac
    done <<< "$lines"
done

if [ -n "$JSON_OUT" ]; then
    {
        printf '{"total":%d,"failures":[' "$total"
        sep=""
        for f in ${failures[@]+"${failures[@]}"}; do
            # Only the case NAME is emitted; a failure message can contain a
            # quote (the hover body is printed with %q) and would break the
            # document. The message stays on stdout, where a human reads it.
            printf '%s"%s"' "$sep" "${f%%:*}"
            sep=","
        done
        printf ']}\n'
    } > "$JSON_OUT"
fi

echo ""
if [ ${#failures[@]} -eq 0 ]; then
    echo "All $total tests passed."
    exit 0
else
    echo "FAILED (${#failures[@]} of $total): ${failures[*]}"
    exit 1
fi
