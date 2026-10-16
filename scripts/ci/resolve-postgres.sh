#!/usr/bin/env bash
#
# Resolve the PostgreSQL server binaries a CI job's live tests run, and export
# where they are.
#
# WHY THIS EXISTS
# ---------------
# Every workflow used to say `apt-get install postgresql-16` and then
# `SKY_POSTGRES_BIN: /usr/lib/postgresql/16/bin`. That holds only while
# `ubuntu-latest` is Ubuntu 24.04, whose archive carries PostgreSQL 16. The
# 26.04 image ships PostgreSQL 18 and its archive has no `postgresql-16`, so
# the moment `ubuntu-latest` moves, every live-test job fails on an apt error
# that says nothing about the code. The major is a fact about the runner image,
# so it is DISCOVERED here, in one place, and never written in a workflow.
#
# WHAT THIS DOES
# --------------
#   1. Uses the PostgreSQL the runner image already ships, if any. Otherwise
#      installs the distribution's default `postgresql` package (never a
#      numbered one) and stops the cluster the package starts, so nothing
#      listens on 5432 that a job's service container needs.
#   2. Picks the newest /usr/lib/postgresql/<major>/bin that holds initdb,
#      pg_ctl, postgres and psql. A directory without all four FAILS the job
#      with an ::error:: naming what is missing. The live tests must never
#      reach a "PostgreSQL is absent" branch and report green.
#   3. With --pgvector, installs `postgresql-<major>-pgvector` unless the
#      extension is already present, and FAILS if vector.control is absent
#      afterwards.
#   4. Exports SKY_POSTGRES_BIN and SKY_PG_MAJOR to $GITHUB_ENV, so every LATER
#      step of the job sees them. A step that sets either itself is a bug.
#
# Usage: bash scripts/ci/resolve-postgres.sh [--pgvector]
set -euo pipefail

want_vector=0
for arg in "$@"; do
  case "$arg" in
    --pgvector) want_vector=1 ;;
    *) echo "::error::resolve-postgres.sh: unknown argument '$arg'" >&2; exit 2 ;;
  esac
done

if [ -z "${GITHUB_ENV:-}" ]; then
  echo "::error::resolve-postgres.sh: GITHUB_ENV is not set (this script runs in a GitHub Actions job)" >&2
  exit 2
fi

required=(initdb pg_ctl postgres psql)

# The newest major whose bin dir holds every required binary, or nothing.
newest_complete_bin() {
  local d best="" best_major=-1 major ok b
  for d in /usr/lib/postgresql/*/bin; do
    [ -d "$d" ] || continue
    major="$(basename "$(dirname "$d")")"
    case "$major" in '' | *[!0-9]*) continue ;; esac
    ok=1
    for b in "${required[@]}"; do [ -x "$d/$b" ] || ok=0; done
    if [ "$ok" = 1 ] && [ "$major" -gt "$best_major" ]; then
      best="$d"
      best_major="$major"
    fi
  done
  printf '%s' "$best"
}

apt_updated=0
apt_install() {
  if [ "$apt_updated" = 0 ]; then
    sudo apt-get update -qq
    apt_updated=1
  fi
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@"
}

dir="$(newest_complete_bin)"
if [ -z "$dir" ]; then
  echo "No complete PostgreSQL under /usr/lib/postgresql on this image. Installing the distribution default."
  apt_install postgresql
  # The package starts a 'main' cluster on 5432. The runner images ship the
  # service disabled, and a job's postgres service container needs the port.
  if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl disable --now postgresql >/dev/null 2>&1 || true
  fi
  dir="$(newest_complete_bin)"
fi

if [ -z "$dir" ]; then
  missing=""
  for d in /usr/lib/postgresql/*/bin; do
    [ -d "$d" ] || continue
    for b in "${required[@]}"; do [ -x "$d/$b" ] || missing="$missing $d/$b"; done
  done
  if [ -z "$missing" ]; then
    missing=" /usr/lib/postgresql/<major>/bin (no PostgreSQL installed)"
  fi
  echo "::error::PostgreSQL binaries missing after install:${missing}. The live tests cannot run." >&2
  exit 1
fi

major="$(basename "$(dirname "$dir")")"

if [ "$want_vector" = 1 ]; then
  control="/usr/share/postgresql/$major/extension/vector.control"
  if [ ! -f "$control" ]; then
    apt_install "postgresql-$major-pgvector"
  fi
  if [ ! -f "$control" ]; then
    echo "::error::pgvector did not install for PostgreSQL $major (no $control)" >&2
    exit 1
  fi
fi

echo "SKY_POSTGRES_BIN=$dir" >>"$GITHUB_ENV"
echo "SKY_PG_MAJOR=$major" >>"$GITHUB_ENV"
"$dir/postgres" --version
echo "SKY_POSTGRES_BIN=$dir (major $major)"
