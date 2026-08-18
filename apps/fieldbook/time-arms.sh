#!/bin/sh
# Wall-clock per backend arm.
cd "20 20 12 61 79 80 81 98 33 100 204 250 395 398 399 400 701dirname "-e")" || exit 1

# `with_timeout <secs> <cmd...>` — the one time bound. A missing `timeout`
# used to make every arm below measure ~0 ms and report it as a fact.
. ../../scripts/lib/with-timeout.sh
for arm in "--dump-view live" "--dump-view tui" "--dump-view webview" "--dump-view diff" "--export"; do
    printf '%-24s ' "$arm"
    S=$(date +%s%N 2>/dev/null || echo 0)
    # shellcheck disable=SC2086
    with_timeout 60 ./sky-out/app $arm >/dev/null 2>&1
    E=$(date +%s%N 2>/dev/null || echo 0)
    echo "$(( (E - S) / 1000000 )) ms"
done
