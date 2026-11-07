#!/usr/bin/env bash
# One fixed collection domain per independently budgeted release gate.
set -euo pipefail
collection_gate_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$collection_gate_dir/spa-step-cost-collections-run.sh" "$@" --collection dict
