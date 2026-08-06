#!/usr/bin/env bash
# Std.Persist SQL≡KV parity gate (BlueDB Phase 3c §8).
#
# Runs the SAME Collection + Cond/Query/CRUD source on the embedded BlueDB engine
# AND a relational backend, asserting byte-identical results on the forced-semantics
# subset (the program self-asserts + exits non-zero on divergence).
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

./sky-out/app
