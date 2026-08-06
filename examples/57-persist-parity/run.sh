#!/usr/bin/env bash
# Std.Persist SQL≡KV parity gate (BlueDB Phase 3c §8).
#
# Runs the SAME Collection + Cond/Query/CRUD source on the embedded BlueDB engine
# AND a relational backend, asserting byte-identical results on the forced-
# semantics subset. FAIL-CLOSED (F1): the program prints `PARITY PASS` + exits 0
# on agreement, or prints `PARITY FAIL` + exits NON-ZERO (System.exit 1) on any
# divergence. This harness additionally asserts BOTH — `PARITY PASS` present AND
# exit 0 — so a silently-swallowed divergence can never go green.
#
#   ./run.sh                 → embedded ≡ SQLite   (self-contained; always runnable)
#   DATABASE_URL=postgres://…/… ./run.sh
#                            → embedded ≡ Postgres (live-PG parity; CI-gated — needs a
#                              reachable Postgres, e.g. the sweep's docker PG)
set -euo pipefail
cd "$(dirname "$0")"

SKY="${SKY:-sky}"
"$SKY" build src/Main.sky >/dev/null

rm -rf data
mkdir -p data

if [ -n "${DATABASE_URL:-}" ]; then
    # Relational arm → the given Postgres (live-PG parity). A fresh table each run.
    export SKY_DB_PATH="$DATABASE_URL"
    echo "parity: embedded ≡ Postgres ($DATABASE_URL)"
else
    export SKY_DB_PATH="$PWD/data/parity.db"
    echo "parity: embedded ≡ SQLite ($SKY_DB_PATH)"
fi

# Capture output + exit code WITHOUT tripping `set -e` (we assert on both).
set +e
out="$(./sky-out/app)"
code=$?
set -e
echo "$out"

if [ "$code" -ne 0 ]; then
    echo "GATE FAIL: parity app exited non-zero ($code)" >&2
    exit 1
fi
if ! echo "$out" | grep -q "PARITY PASS"; then
    echo "GATE FAIL: 'PARITY PASS' not present in output (divergence or error)" >&2
    exit 1
fi
echo "GATE OK: PARITY PASS present + exit 0"
