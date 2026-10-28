#!/usr/bin/env bash
# gcp-bootstrap.sh — one-time, free setup of a Google Cloud project for
# Std.Ai's cloud backends and their live tests. Idempotent: running it again
# changes nothing that is already in place.
#
#   scripts/ai/gcp-bootstrap.sh --project <id>
#
# It turns on the APIs Std.Ai calls (Vertex AI, Cloud Run, Cloud Storage,
# Artifact Registry, Secret Manager, Cloud Quotas, Cloud Logging, Cloud Build,
# Cloud Billing for the price catalogue) and creates two service accounts
# with NO project-wide role:
#
#   sky-trainer   what a Vertex training job runs as; the live wrapper grants
#                 it objectAdmin on the run's own bucket only
#   sky-serve     what the Cloud Run tier runs as; the wrapper grants it
#                 objectViewer on the registry bucket and secretAccessor on
#                 the tier's token secret only
#
# The account running this needs to be allowed to enable services and create
# service accounts. Nothing here costs money: enabled APIs and idle service
# accounts are free. A Cloud Billing budget alert is optional and is set on
# the billing account (console: Billing > Budgets & alerts); it alerts and
# never caps, so the live wrapper's ledger is the cap.
#
# It prints what it did, never a credential.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/lib/require-tool.sh"
source "$ROOT/scripts/lib/with-timeout.sh"
require_tool gcloud "install the Google Cloud CLI (https://cloud.google.com/sdk/docs/install)"

PROJECT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --project) PROJECT="$2"; shift 2 ;;
        *) echo "gcp-bootstrap: unknown option $1" >&2; exit 2 ;;
    esac
done
[ -n "$PROJECT" ] || { echo "gcp-bootstrap: --project <id> is required" >&2; exit 2; }

APIS="aiplatform.googleapis.com run.googleapis.com storage.googleapis.com
artifactregistry.googleapis.com secretmanager.googleapis.com
cloudquotas.googleapis.com logging.googleapis.com cloudbuild.googleapis.com
cloudbilling.googleapis.com iam.googleapis.com"

enabled="$(with_timeout 120 gcloud services list --enabled --project "$PROJECT" --format='value(config.name)')"
for api in $APIS; do
    if grep -qx "$api" <<<"$enabled"; then
        echo "gcp-bootstrap: $api is on"
    else
        echo "gcp-bootstrap: turning on $api"
        with_timeout 600 gcloud services enable "$api" --project "$PROJECT"
    fi
done

for sa in sky-trainer sky-serve; do
    email="$sa@$PROJECT.iam.gserviceaccount.com"
    if with_timeout 60 gcloud iam service-accounts describe "$email" --project "$PROJECT" >/dev/null 2>&1; then
        echo "gcp-bootstrap: service account $sa exists"
    else
        echo "gcp-bootstrap: creating service account $sa"
        with_timeout 120 gcloud iam service-accounts create "$sa" --project "$PROJECT" \
            --display-name "Sky Std.Ai $sa (no project-wide role)"
    fi
done

echo "gcp-bootstrap: done. The L4 quotas are separate: see docs/ai/deploy-gcp.md (Quotas)."
