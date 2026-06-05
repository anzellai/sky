#!/usr/bin/env bash
#
# run-demo.sh — launch the v0.16.6 hub-demo cluster.
#
# Spawns:
#   - `sky console serve` on port 8025 (the central hub)
#   - billing-app on port 8039 (Sky.Live, service.name=customer-42-billing)
#   - frontend-app on port 8040 (Sky.Live, service.name=customer-42-frontend)
#
# Both apps push telemetry to the hub via the HubExporter
# (SKY_CONSOLE_HUB + SKY_CONSOLE_HUB_TOKEN).  Browse to
# http://localhost:8025/ to see them both on the Overview, then
# drill into Logs / Metrics / Traces for either service.
#
# Tenant scoping (v0.16.6 #493): when the hub is configured with
# `SKY_CONSOLE_AUTH=app` and a `consoleAuth` callback returns
# `claims.tenant = "customer-42-"`, the SQL WHERE clause
# narrows every query to `service_name LIKE 'customer-42-%'`.
# This demo uses `SKY_CONSOLE_AUTH=token` for simplicity — both
# apps' data is visible without per-tenant scoping. Set
# SKY_CONSOLE_AUTH=app + add a consoleAuth callback in your own
# hub wrapper to enable the multi-tenant path.

set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"
SKY="$ROOT/sky-out/sky"

if [ ! -x "$SKY" ]; then
    echo "✗ $SKY not found. Run 'cabal install ... exe:sky' from the repo root first." >&2
    exit 1
fi

# Shared hub bearer (32 bytes minimum per the v0.16.1 PR4
# HubExporter contract).
export SKY_CONSOLE_HUB_TOKEN="${SKY_CONSOLE_HUB_TOKEN:-hub-demo-token-must-be-at-least-32-bytes-long}"
export SKY_CONSOLE_HUB="http://localhost:8025/v1/otlp"
export SKY_CONSOLE_BATCH_INTERVAL_MS=2000

# Per-app log/metric tags pinned via OTel resource attributes.
# `service.name` defaults to the sky.toml `name` field — set by
# Sky.Live's HubExporter init.

PIDS=()
cleanup() {
    echo ""
    echo "==> shutting down..."
    for pid in "${PIDS[@]:-}"; do
        kill "$pid" 2>/dev/null || true
    done
    # belt-and-braces: hard-kill any lingering child by port
    for port in 8025 8039 8040; do
        lsof -ti tcp:"$port" 2>/dev/null | xargs -r kill -9 2>/dev/null || true
    done
}
trap cleanup EXIT INT TERM

# 1) Start the hub.  --port 8025 + --data-dir for the SQLite hot
#    store.  Hub also needs SKY_CONSOLE_HUB_TOKEN env so the
#    incoming POSTs from the apps validate.
HUB_DATA_DIR="$(mktemp -d -t sky-hub-demo)"
echo "==> starting hub on :8025 (data dir: $HUB_DATA_DIR)"
SKY_CONSOLE_HUB_TOKEN="$SKY_CONSOLE_HUB_TOKEN" \
    SKY_CONSOLE_AUTH="${SKY_CONSOLE_AUTH:-token}" \
    SKY_CONSOLE_TOKEN="${SKY_CONSOLE_TOKEN:-demo-console-token-must-be-at-least-32-bytes-long}" \
    "$SKY" console serve --port 8025 --data-dir "$HUB_DATA_DIR" >/tmp/sky-hub.log 2>&1 &
PIDS+=($!)

# Give the hub a moment to open its receiver port.
sleep 2

# 2) Build + start billing-app (port 8039).
echo "==> building billing-app..."
( cd billing-app && rm -rf sky-out .skycache .skydeps && "$SKY" build src/Main.sky >/tmp/sky-billing-build.log 2>&1 )
echo "==> starting billing-app on :8039"
( cd billing-app && SKY_LIVE_PORT=8039 ./sky-out/app >/tmp/sky-billing.log 2>&1 ) &
PIDS+=($!)

# 3) Build + start frontend-app (port 8040).
echo "==> building frontend-app..."
( cd frontend-app && rm -rf sky-out .skycache .skydeps && "$SKY" build src/Main.sky >/tmp/sky-frontend-build.log 2>&1 )
echo "==> starting frontend-app on :8040"
( cd frontend-app && SKY_LIVE_PORT=8040 ./sky-out/app >/tmp/sky-frontend.log 2>&1 ) &
PIDS+=($!)

sleep 2

echo ""
echo "==> cluster up:"
echo "      hub:           http://localhost:8025/"
echo "      billing-app:   http://localhost:8039/"
echo "      frontend-app:  http://localhost:8040/"
echo ""
echo "      logs: tail -f /tmp/sky-{hub,billing,frontend}.log"
echo ""
echo "Press Ctrl-C to tear down the cluster."

# Block until interrupted.
wait
