#!/usr/bin/env bash
# pg-up.sh — a throwaway local PostgreSQL for the session-store memory runs.
#
# `sky db provision --embed` resolves a release from
# .github/workflows/postgres-bundle.yml and no `postgres-bundle-v*` tag has
# been cut, so it 404s (AGENTS.md, "Bundles are not published yet"). The
# documented fallback is a system PostgreSQL, which is what this uses.
#
# The cluster is deliberately configured like the small instance the capacity
# question is about -- shared_buffers = 32MB, the same value the AGENTS.md
# sizing table costs at ~36 MB base -- so the app's RSS is measured beside a
# realistically-sized database rather than a default 128MB one.
set -euo pipefail
BASE=/private/tmp/claude-501/-Users-anzel-works-playground-sky/ba286308-b681-4813-93c0-d314aeae3cc9/scratchpad/forumperf
PGDATA="$BASE/pgdata"
PGPORT="${PGPORT:-55433}"
PGBIN=/opt/homebrew/opt/postgresql@14/bin
[ -x "$PGBIN/initdb" ] || PGBIN=/opt/homebrew/opt/libpq/bin
export PATH="$PGBIN:$PATH"

if [ ! -d "$PGDATA" ]; then
  "$PGBIN/initdb" -D "$PGDATA" -U skyperf --auth=trust >| "$BASE/pg-initdb.log" 2>&1
  {
    echo "shared_buffers = 32MB"
    echo "max_connections = 100"
    echo "listen_addresses = '127.0.0.1'"
    echo "port = $PGPORT"
    echo "fsync = off"           # a throwaway bench cluster; never a real one
    echo "synchronous_commit = off"
  } >> "$PGDATA/postgresql.conf"
fi

if ! "$PGBIN/pg_ctl" -D "$PGDATA" status >/dev/null 2>&1; then
  "$PGBIN/pg_ctl" -D "$PGDATA" -l "$BASE/pg.log" -w start
fi
"$PGBIN/psql" -h 127.0.0.1 -p "$PGPORT" -U skyperf -d postgres \
  -c "SELECT 1" >/dev/null
"$PGBIN/psql" -h 127.0.0.1 -p "$PGPORT" -U skyperf -d postgres -tAc \
  "SELECT 1 FROM pg_database WHERE datname='skylive'" | grep -q 1 ||
  "$PGBIN/createdb" -h 127.0.0.1 -p "$PGPORT" -U skyperf skylive
echo "postgres://skyperf@127.0.0.1:$PGPORT/skylive?sslmode=disable"
