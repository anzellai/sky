#!/usr/bin/env bash
# gcp-live.sh — the paid live tests of Std.Ai's Google Cloud backends, under a
# hard spending cap, with teardown and a leak check that ALWAYS run.
#
#   scripts/ai/gcp-live.sh --project <id> [--region us-central1]
#                          [--serve-region <region>]   (Cloud Run, l5; default --region)
#                          [--stages l0,l1,l2,l3,l4,l5] [--cap 40.00]
#
# Stages (each adds to the run's worst-case estimate, docs/ai/deploy-gcp.md):
#   l0  preflight: the credential's type (never the credential), the APIs, the
#       L4 quotas, the ledger's room                                  free
#   l1  setup: a bucket, an image repository, a Docker Hub remote repository,
#       the tier's token secret, and the trainer and tier images
#       built with Cloud Build in the project
#   l2  a budget refusal: plan with USD 0.01, and no job exists after
#   l3  the Fake driver on n2-standard-4 Vertex jobs: exactly-once launch,
#       a relaunch from a checkpoint, a cancel, the hard runtime ceiling
#   l4  a real Unsloth QLoRA run on one L4 (g2-standard-4), the adapter
#       collected and registered (needs the Vertex L4 training quota)
#   l5  the GPU tier on Cloud Run with one L4: cold start measured, two
#       adapters on one GPU, a promotion hot-loaded under load, the auth
#       layers (needs the Cloud Run L4 quota)
#
# The guard: a run starts only when the ledger's total plus 1.25 × this run's
# worst case stays at or under --cap (default 40.00, which keeps USD 10 of
# margin under a USD 50 limit for estimate error and billing lag).
#
# Four bounds stop spend if this process dies: each Vertex job's
# scheduling.timeout (from its budget), Cloud Run's maxInstances 1 and
# scale-to-zero, the EXIT trap, and this script's own 90-minute bound on the
# test run.
#
# Every resource carries sky-managed=true, live-run=<run id> and sky-kind. The
# trap cancels the run's Vertex jobs, deletes its Cloud Run services, secret,
# repositories and bucket, appends the ledger line, and runs the leak check,
# which fails the run (exit 3) if anything that can cost money remains.
#
# Credentials: GOOGLE_APPLICATION_CREDENTIALS (a service-account key file).
# When it is unset, the active gcloud account's service-account key from
# gcloud's own configuration is used. Neither the key, its path nor a token is
# ever printed.
set -euo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/lib/require-tool.sh"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/fresh-compiler.sh"
# The images, the teardown and the tests run sky-out/sky: it must be built
# from this tree (./scripts/build.sh), or a paid run measures old code.
require_fresh_compiler "$ROOT/sky-out/sky" "$ROOT"
require_tool gcloud "install the Google Cloud CLI (https://cloud.google.com/sdk/docs/install)"
require_tool openssl "install openssl"
require_tool cargo "install the Rust toolchain (rustup)"

PY="${SKY_PYTHON:-}"
if [ -z "$PY" ]; then
    if [ -x /opt/homebrew/bin/python3 ]; then PY=/opt/homebrew/bin/python3; else PY=python3; fi
fi

PROJECT=""
REGION="us-central1"
SERVE_REGION=""
STAGES="l0,l1,l2,l3"
CAP="40.00"
while [ $# -gt 0 ]; do
    case "$1" in
        --project) PROJECT="$2"; shift 2 ;;
        --region) REGION="$2"; shift 2 ;;
        --serve-region) SERVE_REGION="$2"; shift 2 ;;
        --stages) STAGES="$2"; shift 2 ;;
        --cap) CAP="$2"; shift 2 ;;
        *) echo "gcp-live: unknown option $1" >&2; exit 2 ;;
    esac
done
[ -n "$PROJECT" ] || { echo "gcp-live: --project <id> is required" >&2; exit 2; }
# Training (Vertex, the bucket, the images) runs in --region; the tier
# (Cloud Run) may run in another region where its L4 quota is.
SERVE_REGION="${SERVE_REGION:-$REGION}"
has_stage() { case ",$STAGES," in *",$1,"*) return 0 ;; esac; return 1; }

LEDGER="$ROOT/scripts/ai/live/gcp-ledger.jsonl"
PRICES="$ROOT/scripts/ai/live/gcp-prices.json"
RUN_ID="sky-live-$(date -u +%Y%m%d%H%M%S)-$(openssl rand -hex 3)"
SUFFIX="${RUN_ID##*-}"
PROJECT_TAG="$(printf '%s' "$PROJECT" | openssl dgst -sha256 | awk '{print substr($NF,1,8)}')"
STARTED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/sky-gcp-live.XXXXXX")"
echo "gcp-live: run $RUN_ID, stages $STAGES, region $REGION, serve region $SERVE_REGION"

# ─── the credential ──────────────────────────────────────────────────────
if [ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]; then
    account="$(gcloud config list --format='value(core.account)' 2>/dev/null)"
    key="$HOME/.config/gcloud/legacy_credentials/$account/adc.json"
    [ -f "$key" ] || { echo "gcp-live: set GOOGLE_APPLICATION_CREDENTIALS to a service-account key file" >&2; exit 2; }
    export GOOGLE_APPLICATION_CREDENTIALS="$key"
fi
kind="$("$PY" -I -c 'import json,sys; print(json.load(open(sys.argv[1])).get("type",""))' "$GOOGLE_APPLICATION_CREDENTIALS")"
case "$kind" in service_account|external_account) ;; *) echo "gcp-live: the credential is neither a service-account key nor workload identity federation (it is '$kind')" >&2; exit 2 ;; esac
echo "gcp-live: credential: a service-account key"

# ─── the ledger guard ────────────────────────────────────────────────────
# Worst cases per stage in USD (docs/ai/deploy-gcp.md, from gcp-prices.json).
RUN_WORST="$("$PY" -I - "$STAGES" "$PRICES" <<'PYEOF'
import json, sys
stages = sys.argv[1].split(",")
prices = json.load(open(sys.argv[2]))
worst = prices["stageWorstCaseUsd"]
print("%.2f" % sum(float(worst.get(s, 0)) for s in stages))
PYEOF
)"
"$PY" -I - "$LEDGER" "$RUN_WORST" "$CAP" <<'PYEOF'
import json, sys
total = 0.0
for line in open(sys.argv[1]):
    line = line.strip()
    if not line:
        continue
    r = json.loads(line)
    total += float(r["actualUsd"] if r.get("actualUsd") is not None else r["worstCaseUsd"])
need = total + 1.25 * float(sys.argv[2])
print("gcp-live: ledger total USD %.2f, this run's worst case USD %s, guard %.2f of %s" % (total, sys.argv[2], need, sys.argv[3]))
if need > float(sys.argv[3]):
    print("gcp-live: REFUSED: the ledger has no room for this run under the cap", file=sys.stderr)
    sys.exit(4)
PYEOF

BUCKET="$PROJECT-sky-live-$SUFFIX"
REPO="sky-live-$SUFFIX"
REMOTE="sky-dockerhub-$SUFFIX"
SECRET="sky-live-$SUFFIX-tier-token"
SERVICE="sky-live-$SUFFIX-tier"
LABELS="sky-managed=true,live-run=$RUN_ID"
TRAINER_SA="sky-trainer@$PROJECT.iam.gserviceaccount.com"
SERVE_SA="sky-serve@$PROJECT.iam.gserviceaccount.com"
LEAK="unknown"
# Not passed until the live tests say so: a setup step that fails leaves it 1.
TEST_STATUS=1
STAGE_LOG="$WORK/stages.jsonl"
: > "$STAGE_LOG"

# ─── teardown and the leak check (always) ────────────────────────────────
leak_check() {
    local tries=0 left
    while [ $tries -lt 3 ]; do
        left=0
        n="$(gcloud ai custom-jobs list --project "$PROJECT" --region "$REGION" \
            --filter='labels.sky-managed=true AND (state=JOB_STATE_RUNNING OR state=JOB_STATE_PENDING OR state=JOB_STATE_QUEUED OR state=JOB_STATE_CANCELLING)' \
            --format='value(name.basename())' 2>/dev/null | grep -c . || true)"
        echo "gcp-live: leak check: $n live Vertex jobs"; left=$((left + n))
        n="$(gcloud run services list --project "$PROJECT" --region "$SERVE_REGION" --filter='metadata.labels.sky-managed=true' \
            --format='value(metadata.name)' 2>/dev/null | grep -c . || true)"
        echo "gcp-live: leak check: $n Cloud Run services"; left=$((left + n))
        n="$(gcloud artifacts repositories list --project "$PROJECT" --location "$REGION" --filter='labels.sky-managed=true' \
            --format='value(name.basename())' 2>/dev/null | grep -c . || true)"
        echo "gcp-live: leak check: $n repositories"; left=$((left + n))
        n="$(gcloud storage buckets list --project "$PROJECT" --filter='labels.sky-managed=true' \
            --format='value(name)' 2>/dev/null | grep -c . || true)"
        echo "gcp-live: leak check: $n buckets"; left=$((left + n))
        n="$(gcloud secrets list --project "$PROJECT" --filter='labels.sky-managed=true' \
            --format='value(name.basename())' 2>/dev/null | grep -c . || true)"
        echo "gcp-live: leak check: $n secrets"; left=$((left + n))
        if [ "$left" -eq 0 ]; then LEAK="clean"; return 0; fi
        tries=$((tries + 1))
        [ $tries -lt 3 ] && sleep 20
    done
    LEAK="FAILED"
    return 3
}

teardown() {
    local code=$?
    set +e
    echo "gcp-live: teardown"
    for job in $(gcloud ai custom-jobs list --project "$PROJECT" --region "$REGION" \
        --filter="labels.live-run=$RUN_ID AND (state=JOB_STATE_RUNNING OR state=JOB_STATE_PENDING OR state=JOB_STATE_QUEUED)" \
        --format='value(name.basename())' 2>/dev/null); do
        gcloud ai custom-jobs cancel "$job" --project "$PROJECT" --region "$REGION" >/dev/null 2>&1 || true
    done
    for i in $(seq 1 60); do
        n="$(gcloud ai custom-jobs list --project "$PROJECT" --region "$REGION" \
            --filter="labels.live-run=$RUN_ID AND (state=JOB_STATE_RUNNING OR state=JOB_STATE_PENDING OR state=JOB_STATE_QUEUED OR state=JOB_STATE_CANCELLING)" \
            --format='value(name.basename())' 2>/dev/null | grep -c . || true)"
        [ "$n" = "0" ] && break
        sleep 10
    done
    # Job records cost nothing; Sky deletes the ended ones it made (gcloud has no delete).
    # Only this run's: the project may hold other Sky resources.
    with_timeout 300 "$ROOT/sky-out/sky" ai teardown --all-sky --label "live-run=$RUN_ID" --project "$PROJECT" --region "$REGION" >/dev/null 2>&1 || true
    if [ "$SERVE_REGION" != "$REGION" ]; then
        with_timeout 300 "$ROOT/sky-out/sky" ai teardown --all-sky --label "live-run=$RUN_ID" --project "$PROJECT" --region "$SERVE_REGION" >/dev/null 2>&1 || true
    fi
    if has_stage l5; then
        gcloud run services delete "$SERVICE" --project "$PROJECT" --region "$SERVE_REGION" --quiet >/dev/null 2>&1 || true
        gcloud secrets delete "$SECRET" --project "$PROJECT" --quiet >/dev/null 2>&1 || true
    fi
    gcloud artifacts repositories delete "$REPO" --project "$PROJECT" --location "$REGION" --quiet >/dev/null 2>&1 || true
    has_stage l5 && gcloud artifacts repositories delete "$REMOTE" --project "$PROJECT" --location "$REGION" --quiet >/dev/null 2>&1 || true
    gcloud storage rm -r "gs://$BUCKET" --quiet >/dev/null 2>&1 || true
    gcloud storage buckets delete "gs://$BUCKET" --quiet >/dev/null 2>&1 || true
    leak_check
    local leak_code=$?
    "$PY" -I - "$LEDGER" "$RUN_ID" "$STARTED" "$STAGES" "$RUN_WORST" "$LEAK" "$PROJECT_TAG" "$STAGE_LOG" "$TEST_STATUS" "$(git -C "$ROOT" rev-parse --short HEAD)" <<'PYEOF'
import json, sys, datetime
ledger, run, started, stages, worst, leak, tag, stage_log, status, sha = sys.argv[1:11]
details = [json.loads(l) for l in open(stage_log) if l.strip()]
estimate = round(sum(float(d.get("estimateUsd", 0)) for d in details), 4)
line = {"run": run, "utc": started, "ended": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "gitSha": sha, "project": tag, "stages": stages.split(","), "resources": details,
        "estimateUsd": estimate, "worstCaseUsd": float(worst), "leakCheck": leak,
        "testsPassed": status == "0", "actualUsd": None}
with open(ledger, "a") as f:
    f.write(json.dumps(line, sort_keys=True) + "\n")
print("gcp-live: ledger: estimate USD %.4f (worst case USD %s), leak check %s" % (estimate, worst, leak))
PYEOF
    rm -rf "$WORK"
    if [ "$leak_code" -ne 0 ]; then exit 3; fi
    exit "$code"
}
trap teardown EXIT
trap 'exit 130' INT TERM HUP

# ─── l0: preflight ───────────────────────────────────────────────────────
if has_stage l0; then
    # Only the APIs the asked stages use: Cloud Run and Secret Manager for l5.
    apis="aiplatform storage artifactregistry cloudbuild cloudquotas"
    if has_stage l5; then apis="$apis run secretmanager"; fi
    for api in $apis; do
        gcloud services list --enabled --project "$PROJECT" --format='value(config.name)' | grep -qx "$api.googleapis.com" \
            || { echo "gcp-live: the $api API is off; run scripts/ai/gcp-bootstrap.sh --project <id>" >&2; exit 2; }
    done
    train_l4="$(gcloud beta quotas info describe CustomModelTrainingL4GPUsPerProjectPerRegion --service=aiplatform.googleapis.com \
        --project "$PROJECT" --format=json 2>/dev/null | "$PY" -I -c 'import json,sys
d=json.load(sys.stdin); r=sys.argv[1]
print(next((i.get("details",{}).get("value","0") for i in d.get("dimensionsInfos",[]) if i.get("dimensions",{}).get("region")==r), "0"))' "$REGION")"
    run_l4="$(gcloud beta quotas info describe NvidiaL4GpuAllocNoZonalRedundancyPerProjectRegion --service=run.googleapis.com \
        --project "$PROJECT" --format=json 2>/dev/null | "$PY" -I -c 'import json,sys
d=json.load(sys.stdin); r=sys.argv[1]
v=[i.get("details",{}).get("value") for i in d.get("dimensionsInfos",[]) if i.get("dimensions",{}).get("region") in (r, None)]
print(next((x for x in v if x), "0"))' "$SERVE_REGION")"
    echo "gcp-live: L4 quota: Vertex training $train_l4 in $REGION, Cloud Run $run_l4 in $SERVE_REGION"
    if has_stage l4 && [ "${train_l4:-0}" = "0" ]; then
        echo "gcp-live: stage l4 needs the Vertex L4 training quota (it is 0 in $REGION)" >&2; exit 5
    fi
    if has_stage l5 && [ "${run_l4:-0}" = "0" ]; then
        echo "gcp-live: stage l5 needs the Cloud Run L4 quota (it is 0 in $SERVE_REGION)" >&2; exit 5
    fi
    echo '{"stage":"l0","estimateUsd":0}' >> "$STAGE_LOG"
fi

export SKY_AI_LIVE_RUN_ID="$RUN_ID" SKY_AI_LIVE_STAGES="$STAGES" SKY_AI_LIVE_STAGE_LOG="$STAGE_LOG"
export SKY_AI_TRAIN_PROJECT="$PROJECT" SKY_AI_TRAIN_REGION="$REGION" SKY_AI_TRAIN_STAGING="gs://$BUCKET/train"
export SKY_AI_TRAIN_SERVICE_ACCOUNT="$TRAINER_SA" SKY_AI_TRAIN_LABELS="live-run=$RUN_ID"
export SKY_AI_SERVE_PROJECT="$PROJECT" SKY_AI_SERVE_REGION="$SERVE_REGION"
export SKY_AI_LIVE_REGISTRY="gs://$BUCKET/registry" SKY_AI_LIVE_SERVICE="$SERVICE" SKY_AI_LIVE_SECRET="$SECRET"
export SKY_AI_LIVE_SERVE_SA="$SERVE_SA"

# ─── l1: setup ───────────────────────────────────────────────────────────
if has_stage l1; then
    gcloud storage buckets create "gs://$BUCKET" --project "$PROJECT" --location "$REGION" \
        --uniform-bucket-level-access --public-access-prevention --soft-delete-duration=0 >/dev/null
    gcloud storage buckets update "gs://$BUCKET" --update-labels="$LABELS,sky-kind=staging" >/dev/null
    gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" --member "serviceAccount:$TRAINER_SA" --role roles/storage.objectAdmin >/dev/null
    gcloud artifacts repositories create "$REPO" --project "$PROJECT" --location "$REGION" --repository-format docker \
        --labels "$LABELS,sky-kind=image" >/dev/null
    # The tier's resources only when l5 runs: a shared project gets nothing a
    # stage does not use.
    if has_stage l5; then
        gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" --member "serviceAccount:$SERVE_SA" --role roles/storage.objectViewer >/dev/null
        gcloud artifacts repositories create "$REMOTE" --project "$PROJECT" --location "$REGION" --repository-format docker \
            --mode remote-repository --remote-docker-repo DOCKER-HUB --labels "$LABELS,sky-kind=image" >/dev/null
        openssl rand -hex 32 | tr -d '\n' | gcloud secrets create "$SECRET" --project "$PROJECT" --data-file=- \
            --labels "$LABELS,sky-kind=secret" >/dev/null
        gcloud secrets add-iam-policy-binding "$SECRET" --project "$PROJECT" --member "serviceAccount:$SERVE_SA" \
            --role roles/secretmanager.secretAccessor >/dev/null
    fi
    echo "gcp-live: l1: the staging bucket and the image repository are in place"
    t0=$(date +%s)
    SKY_AI_TRAIN_IMAGE="$(with_timeout 3600 "$ROOT/sky-out/sky" ai image build --train --project "$PROJECT" --region "$REGION" \
        --repo "$REPO" --staging "gs://$BUCKET/build" | tail -1)"
    export SKY_AI_TRAIN_IMAGE
    t1=$(date +%s)
    if has_stage l5; then
        SKY_AI_SERVE_IMAGE="$(with_timeout 1800 "$ROOT/sky-out/sky" ai image build --tier --project "$PROJECT" --region "$REGION" \
            --repo "$REPO" --staging "gs://$BUCKET/build" | tail -1)"
        export SKY_AI_SERVE_IMAGE
    fi
    t2=$(date +%s)
    export SKY_AI_LIVE_ENGINE_IMAGE="$REGION-docker.pkg.dev/$PROJECT/$REMOTE/vllm/vllm-openai@sha256:b18abb2df97b8f798e81862bd93f872ea18613372e2c3adc0cc2ac21e66ac12f"
    "$PY" -I - "$PRICES" "$((t1 - t0))" "$((t2 - t1))" >> "$STAGE_LOG" <<'PYEOF'
import json, sys
p = json.load(open(sys.argv[1]))["rates"]["cloudBuildPerMinute"]
for name, secs in (("trainer-image", int(sys.argv[2])), ("tier-image", int(sys.argv[3]))):
    print(json.dumps({"stage": "l1", "kind": "cloud-build", "name": name, "seconds": secs,
                      "ratePerMinute": p, "estimateUsd": round(secs / 60.0 * float(p), 4)}))
PYEOF
    echo "gcp-live: l1: images built (trainer $((t1 - t0)) s, tier $((t2 - t1)) s)"
fi

# ─── l2 to l5: the cargo live tests ──────────────────────────────────────
export SKY_AI_GCP_LIVE=1
set +e
(cd "$ROOT/rust" && with_timeout 5400 cargo test -p sky --test ai_gcp_live -- --ignored --nocapture --test-threads 1)
TEST_STATUS=$?
set -e
echo "gcp-live: the live tests ended with status $TEST_STATUS"
exit "$TEST_STATUS"
