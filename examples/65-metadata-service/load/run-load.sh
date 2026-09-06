#!/usr/bin/env bash
# run-load.sh — drive the metadata-service load harness.
#
# Assumes the app is already running (sky run src/Main.sky from the project dir,
# embedded PostgreSQL). Sweeps concurrency levels against the two read paths and
# prints throughput / p50 / p90 / p99 / error% per level.
#
#   ./load/run-load.sh                       # default sweep, localhost:8137
#   URL=http://host:8137 ./load/run-load.sh  # a remote target
set -euo pipefail
export _ZO_DOCTOR=0

URL="${URL:-http://127.0.0.1:8137}"
LEVELS="${LEVELS:-1,8,16,32,64,128,256,512}"
DUR="${DUR:-8s}"
HERE="$(cd "$(dirname "$0")" && pwd)"

echo "############################################################"
echo "# Endpoint A: GET /metadata/:key  (indexed single-row read)"
echo "############################################################"
go run "$HERE/loadgen.go" -url "$URL" -path '/metadata/{key}' -c "$LEVELS" -d "$DUR"

echo
echo "############################################################"
echo "# Endpoint B: GET /metadata?limit=50  (range read, 50 rows)"
echo "############################################################"
go run "$HERE/loadgen.go" -url "$URL" -path '/metadata?limit=50' -c "$LEVELS" -d "$DUR"

echo
echo "############################################################"
echo "# Endpoint C: GET /healthz  (no DB touch — server ceiling)"
echo "############################################################"
go run "$HERE/loadgen.go" -url "$URL" -path '/healthz' -c "$LEVELS" -d "$DUR"
