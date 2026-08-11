#!/bin/sh
# Wall-clock per backend arm.
cd "$(dirname "$0")" || exit 1
for arm in "--dump-view live" "--dump-view tui" "--dump-view webview" "--dump-view diff" "--export"; do
    printf '%-24s ' "$arm"
    S=$(date +%s%N 2>/dev/null || echo 0)
    # shellcheck disable=SC2086
    timeout 60 ./sky-out/app $arm >/dev/null 2>&1
    E=$(date +%s%N 2>/dev/null || echo 0)
    echo "$(( (E - S) / 1000000 )) ms"
done
