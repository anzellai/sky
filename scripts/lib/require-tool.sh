#!/usr/bin/env bash
# scripts/lib/require-tool.sh — a gate whose prerequisite is missing fails.
#
# Why this exists
# ---------------
# `timeout` was invisible until it vanished. Fourteen scripts assumed it, and
# when the nix shell that supplied it went away, three of them reported success
# for verifications that never ran (see scripts/lib/with-timeout.sh). The
# lesson is not about `timeout`: it is that every tool a gate shells out to is
# in that state until something checks.
#
# `node`, `curl`, `lsof`, `jq`, `psql`, `sqlite3`, `gcloud` are all reached for
# by scripts here, mostly unguarded. A missing one is at best a red run with a
# misleading message ("build failed" when the truth is "curl is not
# installed"), and at worst a probe that quietly measures nothing.
#
# This is the shell counterpart of `rust/crates/sky/src/live_gate.rs`, and it
# deliberately reuses that mechanism rather than inventing a second one:
#
#   * The default is REQUIRE. A missing tool is a hard failure naming what to
#     install, not a skip.
#   * `SKY_LIVE_TESTS=skip` is the ONE way to opt out, the same variable and
#     the same value the Rust live gate takes. `require_tool` then returns 1
#     after printing a marker the operator asked for, so the caller can skip
#     that section — a skip somebody CHOSE, by name.
#   * An unrecognised value of `SKY_LIVE_TESTS` is an error rather than a
#     silent fall to either side. `SKY_LIVE_TESTS=1` meaning "require" to its
#     author and "skip" to this function is exactly how a gate ends up not
#     running.
#
# Usage:
#   source "$ROOT/scripts/lib/require-tool.sh"
#   require_tool node "install Node 20+ (the Playwright verifiers are Node)"
#   require_tool curl "install curl" || exit 1

if [ -n "${_SKY_REQUIRE_TOOL_SOURCED:-}" ]; then
    return 0 2>/dev/null || true
fi
_SKY_REQUIRE_TOOL_SOURCED=1

# require_tool <name> [<how-to-get-it>]
#
# Returns 0 when the tool is on PATH. Otherwise EXITS the script non-zero,
# unless SKY_LIVE_TESTS=skip, in which case it returns 1 so the caller can
# skip the section that needs it.
require_tool() {
    local tool="${1:-}" hint="${2:-}"
    if [ -z "$tool" ]; then
        echo "require_tool: usage: require_tool <name> [<how-to-get-it>]" >&2
        exit 2
    fi
    if command -v "$tool" >/dev/null 2>&1; then
        return 0
    fi

    case "${SKY_LIVE_TESTS:-require}" in
        require) ;;
        skip)
            echo "SKIP: '$tool' is not installed and SKY_LIVE_TESTS=skip was set." >&2
            echo "  Whatever this gate would have proven is NOT proven by this run." >&2
            return 1
            ;;
        *)
            echo "require_tool: SKY_LIVE_TESTS='${SKY_LIVE_TESTS}' is not a value I know." >&2
            echo "  Use 'require' (the default) or 'skip'. Guessing is how a gate stops running." >&2
            exit 2
            ;;
    esac

    echo "FAIL: this gate needs '$tool' and it is not on PATH." >&2
    [ -n "$hint" ] && echo "  $hint" >&2
    echo "  A gate cannot pass on a prerequisite it does not have. If you genuinely" >&2
    echo "  cannot install it, say so out loud:  SKY_LIVE_TESTS=skip $0" >&2
    exit 1
}
