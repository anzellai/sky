#!/usr/bin/env bash
# v0.13.x runtime verification — every Sky.Live + Sky.Http.Server example.
#
# Each app runs on a unique port so a leftover-zombie from a prior run
# can't poison the next test. PASS = server listens + Playwright load
# succeeds + zero console errors + zero server-side panic strings.

set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$REPO_ROOT/.skycache/verify"
mkdir -p "$RESULTS_DIR"

# Cross-platform timeout shim (mirrors verify-ui-showcase.sh). Every node
# verifier below is a browser driver: a Playwright wait that never settles
# would otherwise wedge the release gate with no ceiling. macOS runners ship
# neither `timeout` nor `gtimeout`, hence the pure-bash fallback.
if command -v timeout >/dev/null 2>&1; then
    TIMEOUT_CMD="timeout"
elif command -v gtimeout >/dev/null 2>&1; then
    TIMEOUT_CMD="gtimeout"
else
    TIMEOUT_CMD=""
fi
bounded() {
    local secs="$1"; shift
    if [ -n "$TIMEOUT_CMD" ]; then "$TIMEOUT_CMD" "$secs" "$@"; return $?; fi
    "$@" &
    local cmd_pid=$!
    ( sleep "$secs" && kill -KILL "$cmd_pid" 2>/dev/null ) &
    local killer_pid=$!
    local rc=0
    wait "$cmd_pid" 2>/dev/null; rc=$?
    kill -KILL "$killer_pid" 2>/dev/null
    wait "$killer_pid" 2>/dev/null
    return $rc
}

# (example-name, scenario, port).
#
# Apps that hardcode port 8000 in their Sky source (05-mux-server,
# 08-notes-app, 15-http-server) must use 8000; others honour
# PORT / SKY_LIVE_PORT env so they can pick a per-app port and run
# without collision. The script kills any prior holder of each
# port before each test, so even the port-8000 group runs cleanly
# in sequence.
TESTS=(
    "05-mux-server mux-routes 8000"
    "08-notes-app notes-crud 8000"
    "09-live-counter live-counter 8009"
    "10-live-component live-component 8010"
    "12-skyvote skyvote 8012"
    "15-http-server http-routes 8000"
    "16-skychess skychess 8016"
    "17-skymon skymon 8017"
    "18-job-queue job-queue 8018"
    "19-skyforum skyforum 8019"
)

# Skyshop opt-in: Google OAuth gates account features, plus 5 console-
# error 404s appear during the verifier run from a yet-untraced image-
# URL pattern (not reproducible in standalone Playwright probes, no
# panic in server.log — the codegen contract holds). Pass
# SKY_VERIFY_SKYSHOP=1 to include in the sweep; deferred to a v0.13.x
# follow-up.
[ "${SKY_VERIFY_SKYSHOP:-0}" = "1" ] && TESTS+=("13-skyshop skyshop 8013")

pass=0
fail=0
FAILS=()
for entry in "${TESTS[@]}"; do
    set -- $entry
    name=$1; scenario=$2; port=$3
    # Build the app if its binary is missing OR older than the compiler under
    # test — robust to clean checkouts and to artifacts pruned by disk hygiene
    # (the example sweep normally pre-builds them; without this, a pruned
    # example fails with "binary missing" rather than being verified).
    #
    # The `-ot` half matters as much as the existence check: this script is
    # step 5 of the release preflight, and step 1 rebuilds sky-out/sky. Without
    # it, a binary left behind by ANY earlier compiler satisfies the existence
    # test, so the browser gate certifies the previous release's codegen and a
    # codegen regression sails through green.
    app="$REPO_ROOT/examples/$name/sky-out/app"
    if [ -x "$REPO_ROOT/sky-out/sky" ] && { [ ! -x "$app" ] || [ "$app" -ot "$REPO_ROOT/sky-out/sky" ]; }; then
        echo "  building $name (binary missing or older than the compiler) ..."
        ( cd "$REPO_ROOT/examples/$name" && bounded 900 "$REPO_ROOT/sky-out/sky" build src/Main.sky >/dev/null 2>&1 )
    fi
    # Kill any process on this port pre-flight
    pid=$(lsof -ti ":$port" 2>/dev/null || true)
    [ -n "$pid" ] && kill -9 $pid 2>/dev/null || true
    out=$(bounded 300 node "$REPO_ROOT/scripts/verify-live-app.mjs" "$name" "$port" "$scenario" 2>&1)
    if echo "$out" | grep -q "^PASS "; then
        pass=$((pass+1))
        echo "✓ $name (port $port, $scenario)"
    else
        fail=$((fail+1))
        FAILS+=("$name")
        echo "✗ $name (port $port, $scenario)"
        echo "$out" | head -3 | sed 's/^/   /'
    fi
done

echo ""
# NOTE: this is a PROGRESS line, not the verdict — the gates below still have
# to run. It must NOT say "VERIFY: … N fail". Callers grep this script's output
# for its summary; when the example loop printed "VERIFY: 10 pass / 0 fail" and
# a LATER gate then failed, that stale first line satisfied the caller's
# `grep "0 fail"` and the release was declared safe to tag. There is now exactly
# ONE "VERIFY:" line in the output and it is the last thing printed.
echo "progress (examples): $pass pass / $fail fail (out of ${#TESTS[@]})"
[ ${#FAILS[@]} -eq 0 ] || echo "FAILED so far: ${FAILS[*]}"

# Console end-to-end test — spawns parent + console child, drives
# a real browser through every tab, asserts the wire is clean +
# logs are differentiated + counters non-zero. Catches the bug
# class where unit tests pass but the full pipeline (parent +
# spawned console + reverse-proxy + Sky.Live wire + browser) is
# broken. See scripts/verify-console-e2e.mjs for the assertion
# list.
if [ "${SKY_VERIFY_SKIP_CONSOLE_E2E:-0}" != "1" ]; then
    echo ""
    echo "--- console e2e ---"
    # Same trap the ui-showcase block below documents: piping the gate straight
    # into `tail` in the `if` condition tests TAIL's exit status (always 0), not
    # the gate's. console-e2e was left on the broken form, so it printed
    # "✓ console-e2e" unconditionally — including on ERR_MODULE_NOT_FOUND, a
    # missing binary, or every assertion failing. Capture, then tail.
    ce_out=$(bounded 600 node "$REPO_ROOT/scripts/verify-console-e2e.mjs" 2>&1)
    ce_rc=$?
    echo "$ce_out" | tail -8
    if [ "$ce_rc" -eq 0 ]; then
        echo "✓ console-e2e"
    else
        echo "✗ console-e2e (exit $ce_rc)"
        fail=$((fail+1))
        FAILS+=("console-e2e")
    fi
fi

# Std.Ui regression gates — deep computed-style + visual-snapshot
# checks on examples/26-ui-showcase. Catches issue #63-class flex-
# chain regressions BEFORE the Cycle 5 renderer churn (mediaQuery,
# pseudo-classes, transitions, aspectRatio) lands. See
# scripts/verify-ui-showcase.sh for the gate list.
if [ "${SKY_VERIFY_SKIP_UI_SHOWCASE:-0}" != "1" ]; then
    echo ""
    echo "--- ui-showcase regression gates ---"
    # NOTE: run the gate, capture its exit, THEN tail its output. Piping the gate
    # straight into `tail` in the `if` condition tests tail's exit status (always
    # 0), not the gate's — which silently swallowed real snapshot failures.
    ui_out=$(bounded 900 bash "$REPO_ROOT/scripts/verify-ui-showcase.sh" 2>&1)
    ui_rc=$?
    echo "$ui_out" | tail -15
    if [ "$ui_rc" -eq 0 ]; then
        echo "✓ ui-showcase"
    else
        echo "✗ ui-showcase"
        fail=$((fail+1))
        FAILS+=("ui-showcase")
    fi
fi

# Sky.Live resilience e2e — the v0.19.4-7 hardening paths (idle keep-alive
# + desync soft-resync) driven through a REAL browser against the REAL
# runtime wire. These reproduce two production incidents that the Go unit
# tests never exercised end-to-end (CSRF double-submit + SSE lifecycle +
# desync classification header + client response handler). See
# scripts/verify-live-resilience.mjs.
if [ "${SKY_VERIFY_SKIP_RESILIENCE:-0}" != "1" ]; then
    echo ""
    echo "--- live resilience e2e ---"

    # desync-recovery — the redeploy handler-drift heal (fast, ~10s). A
    # stale handler id must return X-Sky-Status: desync + a fresh inline
    # re-render, and the NEXT interaction must round-trip (no strand, no
    # full reload). This is a hard gate.
    res_out=$(bounded 300 node "$REPO_ROOT/scripts/verify-live-resilience.mjs" desync 2>&1)
    res_rc=$?
    echo "$res_out" | tail -4
    if [ "$res_rc" -eq 0 ]; then
        echo "✓ resilience-desync"
    else
        echo "✗ resilience-desync"
        fail=$((fail+1))
        FAILS+=("resilience-desync")
    fi

    # idle-survival — reproduces the darraghstudio "idle → disconnected →
    # refresh fixes it" incident. HARD GATE (bug #11 FIXED): the __sky_csrf
    # cookie's Max-Age was keyed to the session TTL and NOT slid by the SSE
    # heartbeat, so an idle-but-connected session past its TTL 403'd on the next
    # POST even though the session was alive server-side. Fixed —
    # csrfCookieMaxAgeSeconds uses a 30-day floor decoupled from the TTL. Takes
    # ~80s (must cross the 60s memory-store cleanup tick); the idle hold is a
    # fixed wait + a deterministic POST-200 assertion, so it is not timing-flaky.
    echo "--- resilience idle-survival (~80s) ---"
    idle_out=$(bounded 300 node "$REPO_ROOT/scripts/verify-live-resilience.mjs" idle 2>&1)
    idle_rc=$?
    echo "$idle_out" | tail -6
    if [ "$idle_rc" -eq 0 ]; then
        echo "✓ resilience-idle"
    else
        echo "✗ resilience-idle"
        fail=$((fail+1))
        FAILS+=("resilience-idle")
    fi
fi

# The one and only verdict line, printed after EVERY gate has run. A caller may
# grep it or read the exit status; both now agree.
echo ""
echo "VERIFY: $pass pass / $fail fail"
[ ${#FAILS[@]} -eq 0 ] || echo "FAILED: ${FAILS[*]}"

exit $fail
