#!/usr/bin/env bash
#
# Prove the embedded-PostgreSQL delivery path end to end, locally — without a
# published `postgres-bundle-v*` release.
#
#   provision (fetch + checksum + extract)  →  build --embed (go:embed)
#   →  SKY_DB_OP=migrate  →  serve  →  assert the EMBEDDED engine answered.
#
# A locally built bundle exercises the identical resolution, checksum,
# extraction and launch code as a published one — the platform triple is only a
# string in the URL — so this run proves everything except the GitHub release
# URL itself and the platforms this machine cannot build.
#
# The release directory is simulated EXACTLY as
# .github/workflows/postgres-bundle.yml publishes it: the bundle dir tarred as
# postgres-<ver>-<platform>.tar.gz, sbom-<platform>.json beside it, and
# `sha256sum -- *.tar.gz *.json > SHA256SUMS`. `sky db provision --embed` is
# then pointed at it with SKY_POSTGRES_BUNDLE_URL=file://<dir>.
#
# The load-bearing assertion: this host may carry a system PostgreSQL on PATH,
# and the runtime's discovery falls through to PATH — so an app that embedded
# NOTHING would still start a cluster and look green. The proof the embedded
# branch ran is therefore (a) <data>/runtime/.sky-bundle exists, and (b) the
# SERVED `select version()` reports the bundle's pinned version, not the
# host's.
#
# Usage:
#   scripts/skydb/e2e-local-delivery.sh --bundle <dir> --sky <sky-binary>
#       [--workdir DIR]   scratch area (default: mktemp)
#       [--data-dir DIR]  cluster data dir — must NOT be under a temp root,
#                         the runtime refuses those (default:
#                         $HOME/.sky-e2e-embed-proof/data, wiped at start)
#       [--port N]        app port (default 8123)
#
# The bundle comes from scripts/skydb/build-postgres-bundle.sh. First run the
# licence gate against it — the release job refuses to upload otherwise:
#   scripts/skydb/test-licence-gate.sh --bundle <dir>
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
# shellcheck source=../lib/with-timeout.sh
source "${REPO_ROOT}/scripts/lib/with-timeout.sh"
# shellcheck source=../lib/require-tool.sh
source "${REPO_ROOT}/scripts/lib/require-tool.sh"

BUNDLE="" SKY_BIN="" WORKDIR="" PORT=8123
DATA_DIR="${HOME}/.sky-e2e-embed-proof/data"
while [ $# -gt 0 ]; do
  case "$1" in
    --bundle)   BUNDLE="$2"; shift 2 ;;
    --sky)      SKY_BIN="$2"; shift 2 ;;
    --workdir)  WORKDIR="$2"; shift 2 ;;
    --data-dir) DATA_DIR="$2"; shift 2 ;;
    --port)     PORT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -d "$BUNDLE" ] || { echo "--bundle <dir> required (build-postgres-bundle.sh output)" >&2; exit 2; }
[ -x "$SKY_BIN" ] || { echo "--sky <sky-binary> required" >&2; exit 2; }
require_tool curl "install curl"
require_tool tar "install tar"
require_tool go "install a Go toolchain (sky build needs it)"

# CI writes the manifest with coreutils sha256sum; shasum is the stock-macOS
# spelling of the same digest and the same `<hash>  <name>` line format.
if command -v sha256sum >/dev/null 2>&1; then SHA=sha256sum; else SHA="shasum -a 256"; fi

BUNDLE="$(cd "$BUNDLE" && pwd)"
NAME="$(basename "$BUNDLE")"                       # postgres-<ver>-<platform>
VER="$(sed -E 's/^postgres-([0-9.]+)-.*$/\1/' <<<"$NAME")"
PLATFORM="${NAME#postgres-${VER}-}"
[ -n "$VER" ] && [ -n "$PLATFORM" ] || { echo "bundle dir must be named postgres-<ver>-<platform>" >&2; exit 2; }

[ -n "$WORKDIR" ] || WORKDIR="$(mktemp -d)"
mkdir -p "$WORKDIR"
say() { printf '\033[1;34m[e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[e2e] FAIL\033[0m %s\n' "$*" >&2; exit 1; }

APP_PID=""
cleanup() {
  [ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null || true
}
trap cleanup EXIT

# ── 1. Simulate the release directory exactly as CI publishes it ──────────
RELEASE="${WORKDIR}/release"
say "simulating release dir at ${RELEASE}"
mkdir -p "$RELEASE"
[ -f "${BUNDLE}/sbom.json" ] || fail "bundle has no sbom.json — run scan-bundle-licences.sh --sbom-out first (CI does)"
cp "${BUNDLE}/sbom.json" "${RELEASE}/sbom-${PLATFORM}.json"
tar -czf "${RELEASE}/${NAME}.tar.gz" -C "$(dirname "$BUNDLE")" "$NAME"
( cd "$RELEASE" && $SHA -- *.tar.gz *.json > SHA256SUMS )

# ── 2. Scratch project: [database] embedded = true, one table, two routes ──
PROJ="${WORKDIR}/e2e-embed"
say "writing scratch project at ${PROJ}"
mkdir -p "${PROJ}/src"
cat > "${PROJ}/sky.toml" <<EOF
name = "e2e-embed"
version = "0.1.0"
entry = "src/Main.sky"

[source]
root = "src"

[database]
embedded = true
EOF
cat > "${PROJ}/src/Main.sky" <<EOF
module Main exposing (main, db)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Error as Error exposing (Error)
import Sky.Http.Server as Server
import Sky.Http.Server exposing (Request, Response, Handler)
import Std.Db as Db
import Std.Db.Schema as Schema
import Std.Db.Store as Store


notesTable : Schema.Table
notesTable =
    Schema.table
        "notes"
        [ Schema.serial "id"
        , Schema.text "title" |> Schema.notNull
        ]


db : Store.Project
db =
    Schema.toProject [ notesTable ]


main =
    Server.listen
        ${PORT}
        [ Server.get "/version" handleVersion
        , Server.get "/notes" handleNotes
        ]


handleVersion : Handler
handleVersion req =
    Db.connect ()
        |> Task.andThen (\\c -> Db.query c "select version() as v" [])
        |> Task.map
            (\\rows ->
                case rows of
                    r :: _ ->
                        Server.text (Db.getString "v" r)

                    [] ->
                        Server.text "no rows")


handleNotes : Handler
handleNotes req =
    Db.connect ()
        |> Task.andThen
            (\\c ->
                Db.exec c "insert into notes (title) values (?)" [ "hello from e2e" ]
                    |> Task.andThen
                        (\\_ -> Db.query c "select count(*) as n from notes" [])
            )
        |> Task.map
            (\\rows ->
                case rows of
                    r :: _ ->
                        Server.text ("notes=" ++ Db.getString "n" r)

                    [] ->
                        Server.text "no rows")
EOF

# ── 3. The delivery path proper ────────────────────────────────────────────
export SKY_HOME="${WORKDIR}/sky-home"
export SKY_POSTGRES_BUNDLE_URL="file://${RELEASE}"

say "sky db provision --embed  (from ${SKY_POSTGRES_BUNDLE_URL})"
( cd "$PROJ" && with_timeout 300 "$SKY_BIN" db provision --embed )
"${SKY_HOME}/postgres/${VER}/bin/postgres" --version | grep -F "$VER" \
  || fail "provisioned tree does not report ${VER}"

say "sky db init + migrate --gen"
( cd "$PROJ" && "$SKY_BIN" db init && with_timeout 600 "$SKY_BIN" db migrate --gen init )
ls "${PROJ}/db/migrations/"*.json >/dev/null || fail "no migration generated"

say "sky build --embed"
( cd "$PROJ" && with_timeout 900 "$SKY_BIN" build --embed src/Main.sky )
[ -s "${PROJ}/sky-out/postgres-bundle.tar.gz" ] || fail "sky-out/postgres-bundle.tar.gz missing"
[ -s "${PROJ}/sky-out/pg_embed_bundle_gen.go" ] || fail "sky-out/pg_embed_bundle_gen.go missing"

say "one-shot migrate (SKY_DB_OP=migrate ./app --embed)"
rm -rf "$DATA_DIR"; mkdir -p "$(dirname "$DATA_DIR")"
( cd "$PROJ" && SKY_DB_OP=migrate with_timeout 300 ./sky-out/app --embed --data-dir "$DATA_DIR" )
[ -f "${DATA_DIR}/runtime/.sky-bundle" ] \
  || fail "${DATA_DIR}/runtime/.sky-bundle missing — the PATH fallback served, not the embedded bundle"

say "serving on :${PORT}"
( cd "$PROJ" && exec ./sky-out/app --embed --data-dir "$DATA_DIR" ) > "${WORKDIR}/serve.log" 2>&1 &
APP_PID=$!
ok=""
for _ in $(seq 1 30); do
  if v="$(curl -sf --max-time 5 "http://127.0.0.1:${PORT}/version" 2>/dev/null)"; then ok=1; break; fi
  sleep 1
done
[ -n "$ok" ] || { cat "${WORKDIR}/serve.log" >&2; fail "app never answered /version"; }

# ── 4. The assertions that decide the verdict ──────────────────────────────
say "served version: ${v}"
grep -F "PostgreSQL ${VER}" <<<"$v" >/dev/null \
  || fail "server reports '${v}' — not the embedded ${VER} (PATH fallback?)"

n1="$(curl -sf --max-time 5 "http://127.0.0.1:${PORT}/notes")"
n2="$(curl -sf --max-time 5 "http://127.0.0.1:${PORT}/notes")"
[ "$n1" = "notes=1" ] && [ "$n2" = "notes=2" ] \
  || fail "migrated table did not take writes (got '${n1}' then '${n2}')"

say "restart persistence"
kill "$APP_PID"; wait "$APP_PID" 2>/dev/null || true; APP_PID=""
( cd "$PROJ" && exec ./sky-out/app --embed --data-dir "$DATA_DIR" ) >> "${WORKDIR}/serve.log" 2>&1 &
APP_PID=$!
n3=""
for _ in $(seq 1 30); do
  if n3="$(curl -sf --max-time 5 "http://127.0.0.1:${PORT}/notes" 2>/dev/null)"; then break; fi
  sleep 1
done
[ "$n3" = "notes=3" ] || fail "rows did not survive a restart (got '${n3}')"
kill "$APP_PID"; wait "$APP_PID" 2>/dev/null || true; APP_PID=""

say "PASS — provision → build --embed → migrate → serve all ran against the embedded ${VER}"
say "workdir kept at ${WORKDIR}; data dir at ${DATA_DIR} (rm -rf both when done)"
