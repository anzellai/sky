#!/usr/bin/env bash
# Std.Persist RELATIONAL-ONLY app (BlueDB Phase-3 F2). Builds + runs on the SQL
# arm only; proves a connectRelational-only Persist app works (Pebble-linked —
# honest materialisation, see phase3-status.md §3c-4).
#
#   ./run.sh                          → SQLite
#   DATABASE_URL=postgres://…/… ./run.sh  → Postgres
set -euo pipefail
cd "$(dirname "$0")"

SKY="${SKY:-sky}"
"$SKY" build src/Main.sky >/dev/null

rm -rf data
mkdir -p data

if [ -n "${DATABASE_URL:-}" ]; then
    export SKY_DB_PATH="$DATABASE_URL"
    echo "relational-only: Postgres ($DATABASE_URL)"
else
    export SKY_DB_PATH="$PWD/data/relational.db"
    echo "relational-only: SQLite ($SKY_DB_PATH)"
fi

out="$(./sky-out/app)"
echo "$out"

# Alice 100-25=75, Bob 50+25=75 — the serializable transfer committed atomically.
if ! echo "$out" | grep -q "a1 Alice => 75"; then
    echo "FAIL: expected 'a1 Alice => 75' (transfer did not commit correctly)" >&2
    exit 1
fi
if ! echo "$out" | grep -q "a2 Bob => 75"; then
    echo "FAIL: expected 'a2 Bob => 75'" >&2
    exit 1
fi
# F6 injection gate: the malicious ORDER BY column was rejected before SQL.
if ! echo "$out" | grep -q "IDENT GUARD OK"; then
    echo "FAIL: expected 'IDENT GUARD OK' (F6 identifier guard did not reject a bad column)" >&2
    exit 1
fi
echo "OK: relational-only Persist app committed the serializable transfer + rejected the injection column"
