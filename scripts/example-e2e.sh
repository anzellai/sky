#!/usr/bin/env bash
# scripts/example-e2e.sh — behavioural end-to-end runner for examples.
#
# Unlike `sky verify` (which probes `/` for HTTP 200 or the CLI exit
# code), this runs each example against a behavioural contract
# declared in `examples/<n>/e2e.json`. The contract describes a
# sequence of steps — CLI invocations, HTTP requests, or Sky.Live
# event dispatches — with expected outputs/statuses/body substrings.
#
# Exit code 0 iff every example's contract passes. Designed to
# catch logic regressions (AI played nonsense), CLI flow bugs (args
# not dispatching), and silent DB errors (constraint violations
# logged but not surfaced) — classes the smoke-level runner misses.
#
# Usage:
#   scripts/example-e2e.sh             # run every example with an e2e.json
#   scripts/example-e2e.sh 07-todo-cli # run a single example
#
# Contract file schema is documented in .claude/prompts/end-to-end-testing.md

set -o pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SKY="$ROOT/sky-out/sky"
if [[ ! -x "$SKY" ]]; then
    echo "error: sky-out/sky not found. Run scripts/build.sh first." >&2
    exit 2
fi

pass=0
fail=0
declare -a fails=()
declare -a skips=()

examples=()
if [[ $# -gt 0 ]]; then
    for n in "$@"; do examples+=("$n"); done
else
    for d in examples/*/; do examples+=("$(basename "$d")"); done
fi

# ── helpers ──────────────────────────────────────────────────────
say()   { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()    { printf '  \033[1;32mok\033[0m     %s\n' "$*"; }
bad()   { printf '  \033[1;31mFAIL\033[0m   %s\n' "$*"; }
skip()  { printf '  \033[0;33mskip\033[0m   %s\n' "$*"; }

# jqor — read JSON field with default.
jqor() {
    local file="$1" path="$2" default="${3:-}"
    local v
    v="$(jq -r "$path // empty" "$file" 2>/dev/null)"
    [[ -z "$v" || "$v" == "null" ]] && v="$default"
    printf '%s' "$v"
}

# Run a CLI step: invoke ./sky-out/app with args, compare exit/stdout.
run_cli_step() {
    local stepfile="$1" example="$2"
    local args stdout exit_code expect_exit expect_stdout
    # shellcheck disable=SC2207
    IFS=$'\n' args=($(jq -r '.args[]' "$stepfile"))
    expect_exit="$(jqor "$stepfile" .expectExit 0)"
    stdout="$(./sky-out/app "${args[@]}" 2>&1)"
    exit_code=$?

    local step_name
    step_name="${args[*]:-list}"
    if [[ "$exit_code" != "$expect_exit" ]]; then
        bad "step '$step_name': exit $exit_code (expected $expect_exit)"
        printf '    stdout: %s\n' "$stdout" | head -5
        return 1
    fi
    # expectStdoutContains: array of substrings.
    local needles
    needles="$(jq -r '.expectStdoutContains[]? // empty' "$stepfile")"
    if [[ -n "$needles" ]]; then
        while IFS= read -r needle; do
            [[ -z "$needle" ]] && continue
            if ! grep -qF -- "$needle" <<<"$stdout"; then
                bad "step '$step_name': stdout missing '$needle'"
                printf '    stdout: %s\n' "$stdout" | head -5
                return 1
            fi
        done <<<"$needles"
    fi
    ok "cli '$step_name'"
    return 0
}

# Boot server and return its PID; pipe stderr to $2.
boot_server() {
    local app="$1" errlog="$2"
    "$app" > "${errlog}.out" 2> "$errlog" &
    echo $!
}

# Shut a server PID cleanly.
kill_server() {
    local pid="$1"
    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
}

# Run HTTP/Live step via curl. Uses $COOKIE_JAR for session persistence.
run_http_step() {
    local stepfile="$1" port="$2" errlog="$3"
    local method path expect_status body_needles
    method="$(jqor "$stepfile" .method GET)"
    path="$(jqor "$stepfile" .path /)"
    expect_status="$(jqor "$stepfile" .expectStatus 200)"
    local name
    name="$(jqor "$stepfile" .name "${method} ${path}")"

    local url="http://127.0.0.1:${port}${path}"
    local args=(-s -o /tmp/e2e-body -w '%{http_code}' -b "$COOKIE_JAR" -c "$COOKIE_JAR")
    args+=(-X "$method")

    # Form body?
    local form_json
    form_json="$(jq -c '.form? // empty' "$stepfile")"
    if [[ -n "$form_json" && "$form_json" != "null" ]]; then
        while IFS='=' read -r k v; do
            [[ -z "$k" ]] && continue
            args+=(-d "${k}=${v}")
        done < <(jq -r '.form | to_entries[] | "\(.key)=\(.value)"' "$stepfile")
    fi

    # JSON body?
    local json_body
    json_body="$(jq -c '.jsonBody? // empty' "$stepfile")"
    if [[ -n "$json_body" && "$json_body" != "null" ]]; then
        args+=(-H 'Content-Type: application/json' -d "$json_body")
    fi

    local actual_status
    actual_status="$(curl "${args[@]}" "$url")"
    local actual_body
    actual_body="$(cat /tmp/e2e-body)"

    if [[ "$actual_status" != "$expect_status" ]]; then
        bad "step '$name': HTTP $actual_status (expected $expect_status)"
        printf '    body: %s\n' "$(head -c 200 /tmp/e2e-body)"
        return 1
    fi

    # expectBodyContains
    local needles
    needles="$(jq -r '.expectBodyContains[]? // empty' "$stepfile")"
    if [[ -n "$needles" ]]; then
        while IFS= read -r needle; do
            [[ -z "$needle" ]] && continue
            if ! grep -qF -- "$needle" <<<"$actual_body"; then
                bad "step '$name': body missing '$needle'"
                printf '    body: %s\n' "$(head -c 300 /tmp/e2e-body)"
                return 1
            fi
        done <<<"$needles"
    fi

    # expectStderrAbsent — scan server stderr for patterns that must NOT be there.
    local forbidden
    forbidden="$(jq -r '.expectStderrAbsent[]? // empty' "$stepfile")"
    if [[ -n "$forbidden" ]]; then
        while IFS= read -r pattern; do
            [[ -z "$pattern" ]] && continue
            if grep -qE -- "$pattern" "$errlog" 2>/dev/null; then
                bad "step '$name': server log contains forbidden pattern '$pattern'"
                printf '    last-log: %s\n' "$(tail -c 500 "$errlog")"
                return 1
            fi
        done <<<"$forbidden"
    fi

    ok "http '$name' ${method} ${path} → $actual_status"
    return 0
}

# ── run one example ──────────────────────────────────────────────
run_example() {
    local name="$1"
    local dir="examples/${name}"
    local contract="${dir}/e2e.json"
    if [[ ! -f "$contract" ]]; then
        skips+=("$name (no e2e.json)")
        return 0
    fi

    say "${name}"

    local kind
    kind="$(jqor "$contract" .kind cli)"

    # Clean artefacts (databases, caches) the contract names.
    local cleanup_list
    cleanup_list="$(jq -r '.setup.cleanArtifacts[]? // empty' "$contract")"
    if [[ -n "$cleanup_list" ]]; then
        while IFS= read -r artefact; do
            [[ -z "$artefact" ]] && continue
            rm -f "${dir}/${artefact}"
        done <<<"$cleanup_list"
    fi

    # Build from a clean slate so the contract reflects a
    # from-scratch release install. Three-retry loop because
    # `sky build` on a large-SDK example (13-skyshop) can briefly
    # hit the Go module proxy mid-fetch and transient 502s bubble
    # through. Retries are the lazy fix; the cleanest is
    # deterministic resolution which is out of scope here.
    local build_log="/tmp/e2e-build-${name}.log"
    # Pre-warm Go module cache via sky install then build. Some
    # examples need the two-step flow to resolve transitive deps
    # deterministically across fresh checkouts; running both inside
    # one subshell ensures a consistent cwd.
    local build_rc=1
    for attempt in 1 2 3; do
        (
            cd "$dir"
            rm -rf sky-out .skycache
            "$SKY" install 2>/dev/null || true
            "$SKY" build src/Main.sky
        ) > "$build_log" 2>&1
        build_rc=$?
        if [[ $build_rc -eq 0 ]]; then
            break
        fi
        sleep 1
    done
    if [[ $build_rc -ne 0 ]]; then
        bad "build failed (rc=$build_rc)"
        printf '      %s\n' "$(tail -5 "$build_log")"
        fails+=("$name")
        fail=$((fail+1))
        return 1
    fi

    local tmp
    tmp="$(mktemp -d)"
    export COOKIE_JAR="${tmp}/cookies.txt"
    : > "$COOKIE_JAR"

    local all_ok=0

    if [[ "$kind" == "cli" ]]; then
        cd "$dir"
        local n
        n="$(jq -r '.steps | length' "e2e.json")"
        for ((i=0; i<n; i++)); do
            local stepfile
            stepfile="$(mktemp)"
            jq -c ".steps[${i}]" e2e.json > "$stepfile"
            if ! run_cli_step "$stepfile" "$name"; then
                all_ok=1
                break
            fi
            rm -f "$stepfile"
        done
        cd "$ROOT"
    else
        # server / live
        local port wait_ms
        port="$(jqor "$contract" .port 8000)"
        wait_ms="$(jqor "$contract" .startupWaitMs 2000)"

        # Ensure the port is free before boot — any leftover server
        # from a prior example would answer our curl and make every
        # downstream contract see the wrong app's body.
        local leftover
        leftover="$(lsof -ti tcp:"$port" 2>/dev/null || true)"
        if [[ -n "$leftover" ]]; then
            kill -9 $leftover 2>/dev/null || true
            sleep 1
        fi

        local errlog="${tmp}/server.err"
        (cd "$dir" && "./sky-out/app" > "${errlog}.out" 2> "$errlog") &
        local pid=$!
        local wait_s
        wait_s=$(awk -v ms="$wait_ms" 'BEGIN { printf "%.2f", ms/1000 }')
        # shellcheck disable=SC2086
        sleep "$wait_s"
        # Wait for port to respond
        local boot_ok=0
        for _ in 1 2 3 4 5; do
            if curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/" | grep -qE '^(2|3|4|5)'; then
                boot_ok=1; break
            fi
            sleep 0.5
        done
        if [[ $boot_ok -eq 0 ]]; then
            bad "server never came up on port ${port}"
            all_ok=1
        else
            local n
            n="$(jq -r '.steps | length' "$contract")"
            for ((i=0; i<n; i++)); do
                local stepfile
                stepfile="$(mktemp)"
                jq -c ".steps[${i}]" "$contract" > "$stepfile"
                if ! run_http_step "$stepfile" "$port" "$errlog"; then
                    all_ok=1
                    break
                fi
                rm -f "$stepfile"
            done
        fi
        kill_server "$pid" >/dev/null 2>&1
        wait "$pid" 2>/dev/null || true
        # Belt-and-braces: sometimes the child process is a shell
        # wrapper and the sky-out/app binary keeps running on the
        # port. Force-free the port after every example so the
        # next contract starts clean.
        local leftover2
        leftover2="$(lsof -ti tcp:"$port" 2>/dev/null || true)"
        if [[ -n "$leftover2" ]]; then
            kill -9 $leftover2 2>/dev/null || true
            sleep 1
        fi
    fi

    rm -rf "$tmp"

    if [[ $all_ok -eq 0 ]]; then
        pass=$((pass+1))
    else
        fails+=("$name")
        fail=$((fail+1))
    fi
}

say "pre-warming Go module cache (parallel sky install)"
for e in "${examples[@]}"; do
    dir="examples/${e}"
    [[ -d "$dir" ]] || continue
    ( cd "$dir" && "$SKY" install >/dev/null 2>&1 ) &
done
wait

say "running e2e contracts on ${#examples[@]} example(s)"
for e in "${examples[@]}"; do
    run_example "$e"
    # Brief settle so the OS reclaims port + file handles before
    # the next example starts fresh. Without this, backed-up
    # sockets in TIME_WAIT cause port-bind flakiness.
    sleep 1
done

say "summary: ${pass} passed, ${fail} failed, ${#skips[@]} no-contract"
if [[ ${#skips[@]} -gt 0 ]]; then
    printf '  no contract: %s\n' "${skips[*]}"
fi
if [[ ${#fails[@]} -gt 0 ]]; then
    printf '  failures:   %s\n' "${fails[*]}"
    exit 1
fi
exit 0
