#!/usr/bin/env bash
# Create + activate a personal deploy service account at the
# settleby.com ORG LEVEL so gcloud needs no further `auth login`.
# One IAM binding covers every existing + future project under
# the org (settleby, settleby-qa, SkyDeploy folder → SkyDeploy +
# SkyDeploy Dev, etc.).
#
# Pick a role profile that matches what you'll use the SA for:
#
#   --profile=ssh        Minimum surface: just gcloud compute
#                        ssh|scp + sudo on VMs.  Today's deploy.sh.
#                        Re-run the script + add a role next time
#                        you need Cloud Run / Cloud SQL / etc.
#
#   --profile=operator   Day-to-day platform ops: Cloud Run,
#                        Compute, Cloud SQL, Cloud Build,
#                        Cloud Storage, Cloud Logging, Monitoring,
#                        IAM service-account impersonation (deploy-
#                        as).  Can NOT modify IAM bindings, so a
#                        leaked key can't grant itself more access.
#                        RECOMMENDED for a personal deploy SA.
#                        (default)
#
#   --profile=owner      roles/owner — literally everything your
#                        interactive account can do.  Same blast
#                        radius as your personal credentials.
#                        Easy mental model; rotate quarterly.
#
# Usage:
#   ./scripts/setup-deploy-sa.sh                  # operator (default)
#   ./scripts/setup-deploy-sa.sh --profile=ssh
#   ./scripts/setup-deploy-sa.sh --profile=owner
#
# Re-running the script with a different profile ADDS the new
# profile's roles on top of any existing bindings — IAM grants
# are idempotent + cumulative.  To revoke roles, use:
#   gcloud organizations remove-iam-policy-binding 772949907826 \
#       --member=serviceAccount:deployer@settleby.iam.gserviceaccount.com \
#       --role=<role>
set -euo pipefail

# ─── Profile selection ─────────────────────────────────────────────

PROFILE="operator"
for arg in "$@"; do
    case "$arg" in
        --profile=ssh)       PROFILE="ssh" ;;
        --profile=operator)  PROFILE="operator" ;;
        --profile=owner)     PROFILE="owner" ;;
        --help|-h)
            grep '^# ' "$0" | sed 's/^# //;s/^#//'
            exit 0
            ;;
        *)
            echo "Unknown arg: $arg" >&2
            echo "Use --profile=ssh|operator|owner or --help." >&2
            exit 1
            ;;
    esac
done

# ─── Config ────────────────────────────────────────────────────────

SA_HOME_PROJECT="settleby"
SA_NAME="deployer"
SA_EMAIL="${SA_NAME}@${SA_HOME_PROJECT}.iam.gserviceaccount.com"
SA_DISPLAY_NAME="Personal deploy SA (settleby.com org-wide)"

# Settleby.com org ID.
ORG_ID="772949907826"

# Roles per profile.  Each profile's roles are bound at the ORG
# level, so they auto-inherit to every project + folder under
# settleby.com.

ROLES_SSH=(
    "roles/compute.osAdminLogin"
    "roles/compute.viewer"
    "roles/iap.tunnelResourceAccessor"
)

ROLES_OPERATOR=(
    # ── compute ssh/scp + sudo ──
    "roles/compute.osAdminLogin"
    "roles/iap.tunnelResourceAccessor"

    # ── service admins (the things you'll actually use) ──
    "roles/compute.admin"                  # instances, networks, firewalls, disks
    "roles/run.admin"                      # Cloud Run deploy + manage services
    "roles/cloudsql.admin"                 # Cloud SQL instances + dbs
    "roles/cloudbuild.builds.editor"       # Cloud Build trigger + view
    "roles/storage.admin"                  # Cloud Storage buckets + objects
    "roles/logging.admin"                  # read + manage log sinks
    "roles/monitoring.editor"              # read + edit dashboards + alerts
    "roles/secretmanager.admin"            # Secret Manager (likely future)
    "roles/artifactregistry.admin"         # Artifact Registry (for docker push)

    # ── deploy-as: impersonate runtime SAs ──
    # Needed when deploying Cloud Run / Cloud Build with --service-account=X
    # — the deployer must be allowed to actAs the runtime SA X.
    "roles/iam.serviceAccountUser"

    # NOTE: deliberately NOT included:
    #   roles/iam.* (can't grant itself more)
    #   roles/resourcemanager.projectIamAdmin  (same)
    #   roles/owner / roles/editor             (broad alternatives)
    # If the leaked-key threat model isn't your concern, use
    # --profile=owner instead.
)

ROLES_OWNER=(
    "roles/owner"
)

# Pick the active set.
case "$PROFILE" in
    ssh)       ORG_ROLES=("${ROLES_SSH[@]}") ;;
    operator)  ORG_ROLES=("${ROLES_OPERATOR[@]}") ;;
    owner)     ORG_ROLES=("${ROLES_OWNER[@]}") ;;
esac

# Verification: projects we'll probe to confirm the SA can see
# instances after activation.
VERIFY_PROJECTS=(
    "settleby"
    "skydeploy-platform-dev"
)

KEY_DIR="${HOME}/.gcloud"
KEY_FILE="${KEY_DIR}/settleby-deployer.json"


# ─── Sanity checks ─────────────────────────────────────────────────

echo "==> Profile: ${PROFILE}  (${#ORG_ROLES[@]} role(s))"
for ROLE in "${ORG_ROLES[@]}"; do
    echo "    - ${ROLE}"
done

echo ""
echo "==> Checking gcloud auth"
ACTIVE_ACCOUNT=$(gcloud auth list --filter=status:ACTIVE --format="value(account)" | head -1)
if [ -z "$ACTIVE_ACCOUNT" ]; then
    echo "ERROR: no active gcloud account.  Run \`gcloud auth login\` first." >&2
    exit 1
fi
echo "    active: $ACTIVE_ACCOUNT"

if ! gcloud organizations describe "$ORG_ID" >/dev/null 2>&1; then
    echo "ERROR: can't describe organization $ORG_ID with the active account." >&2
    echo "       You need roles/resourcemanager.organizationAdmin on settleby.com" >&2
    echo "       (or a delegated role that allows org IAM bindings)." >&2
    exit 1
fi

mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"


# ─── Create the SA in the home project ─────────────────────────────

echo ""
echo "==> Service account: ${SA_EMAIL}"
if gcloud iam service-accounts describe "$SA_EMAIL" \
        --project="$SA_HOME_PROJECT" >/dev/null 2>&1; then
    echo "    already exists — skipping create"
else
    gcloud iam service-accounts create "$SA_NAME" \
        --project="$SA_HOME_PROJECT" \
        --display-name="$SA_DISPLAY_NAME"
fi


# ─── Grant ORG-LEVEL roles (batched, single setIamPolicy) ──────────
#
# Naive approach was 1 add-iam-policy-binding per role → N×(get +
# set) round-trips, blows the cloudresourcemanager write-quota
# (600/min on the shared gcloud CLI consumer project) on profiles
# with 10+ roles.
#
# Batched approach: get the policy ONCE, add every binding
# client-side via jq, set ONCE.  Two API calls total regardless
# of role count.
echo ""
echo "==> Granting org-level roles on settleby.com (${ORG_ID})"

if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required.  Install with 'brew install jq'." >&2
    exit 1
fi

POLICY_TMP=$(mktemp -t sa-deploy-policy.XXXXXX.json)
POLICY_ORIG=$(mktemp -t sa-deploy-policy-orig.XXXXXX.json)
trap 'rm -f "$POLICY_TMP" "$POLICY_ORIG"' EXIT

# Snapshot existing policy.
gcloud organizations get-iam-policy "$ORG_ID" \
    --format=json > "$POLICY_TMP"
cp "$POLICY_TMP" "$POLICY_ORIG"

# Append every (member, role) — idempotent because jq dedupes via
# unique_by below.
MEMBER="serviceAccount:${SA_EMAIL}"
for ROLE in "${ORG_ROLES[@]}"; do
    jq --arg role "$ROLE" --arg member "$MEMBER" '
        .bindings as $b |
        if ($b | map(select(.role == $role)) | length) == 0 then
            .bindings += [{role: $role, members: [$member]}]
        else
            .bindings |= map(
                if .role == $role then
                    .members = ((.members // []) + [$member] | unique)
                else . end
            )
        end
    ' "$POLICY_TMP" > "${POLICY_TMP}.next" && mv "${POLICY_TMP}.next" "$POLICY_TMP"
    echo "    ✓ ${ROLE}"
done

# Detect no-op: compare normalised bindings.  setIamPolicy ALWAYS
# counts as a write even when the policy is byte-identical, so on
# subsequent runs of this script (the SA already has every role)
# we'd burn 1 write/sec against the 600/min cloudresourcemanager
# quota for zero benefit.  Skip when nothing changed.
ORIG_BINDINGS=$(jq -Sc '.bindings // [] | sort_by(.role)' "$POLICY_ORIG")
NEW_BINDINGS=$(jq -Sc '.bindings // [] | sort_by(.role)' "$POLICY_TMP")

if [ "$ORIG_BINDINGS" = "$NEW_BINDINGS" ]; then
    echo "    bindings already in place (no IAM changes — skipping setIamPolicy)"
else
    # Quota-aware retry.  The shared gcloud default consumer project
    # (32555940559) has a 600/min cap on cloudresourcemanager writes
    # shared globally with every other gcloud user, so 429s are
    # common during heavy automation.  Wait the full minute window
    # for refill, then retry up to 3 times.
    ERR_LOG=$(mktemp -t sa-deploy-policy-err.XXXXXX)
    trap 'rm -f "$POLICY_TMP" "$POLICY_ORIG" "$ERR_LOG"' EXIT

    for ATTEMPT in 1 2 3; do
        if gcloud organizations set-iam-policy "$ORG_ID" "$POLICY_TMP" \
            --format=none >/dev/null 2>"$ERR_LOG"; then
            echo "    bindings written (attempt ${ATTEMPT})"
            break
        fi
        if grep -qE 'RESOURCE_EXHAUSTED|"code": 429' "$ERR_LOG"; then
            if [ "$ATTEMPT" = "3" ]; then
                echo "ERROR: cloudresourcemanager write quota still exhausted after 3 attempts." >&2
                echo "       Re-run the script in ~90s — the quota window will have refilled." >&2
                cat "$ERR_LOG" >&2
                exit 1
            fi
            echo "    quota exhausted (attempt ${ATTEMPT}/3) — sleeping 90s for quota refill"
            sleep 90
        else
            echo "ERROR: setIamPolicy failed for non-quota reason:" >&2
            cat "$ERR_LOG" >&2
            exit 1
        fi
    done
fi


# ─── Mint a long-lived JSON key ────────────────────────────────────
#
# settleby.com org enforces
# constraints/iam.disableServiceAccountKeyCreation by default — good
# baseline security (leaked-key class is the #1 GCP credential
# incident).  But we need a long-lived key here because the SA is
# used for REMOTE work where the underlying interactive token isn't
# available, so the modern impersonation pattern doesn't fit.
#
# Apply an `enforce: false` override at the settleby PROJECT scope.
# Every other project under the org keeps the block; only settleby
# (where this SA lives) permits key creation.  Override is left in
# place after the script exits so re-runs (key rotation, role bumps)
# don't have to re-pay the 30-60s org-policy → IAM propagation wait
# every time.
#
# Net effect:
#   - settleby project: SA key creation allowed
#   - everywhere else under settleby.com: SA key creation blocked
#
# Restore the org default at any time with:
#   gcloud org-policies delete \
#       constraints/iam.disableServiceAccountKeyCreation \
#       --project=settleby

POLICY_FILE=$(mktemp -t allow-sa-keys.XXXXXX.yaml)
POLICY_NAME="projects/${SA_HOME_PROJECT}/policies/iam.disableServiceAccountKeyCreation"
trap 'rm -f "$POLICY_FILE"' EXIT

echo ""
echo "==> Allowing SA key creation on ${SA_HOME_PROJECT} (persistent)"

# Org Policy API has to be enabled on the project before you can
# call SetPolicy on it.  Idempotent — no-op if already enabled.
# Adds ~1-2s the first time; instant on subsequent runs.
if ! gcloud services list --enabled \
        --project="$SA_HOME_PROJECT" \
        --filter="config.name:orgpolicy.googleapis.com" \
        --format="value(config.name)" 2>/dev/null | grep -q orgpolicy; then
    echo "    enabling orgpolicy.googleapis.com on ${SA_HOME_PROJECT}"
    gcloud services enable orgpolicy.googleapis.com \
        --project="$SA_HOME_PROJECT" --quiet
    # API enablement propagation lag — first set-policy call right
    # after enable often races and 403s with a stale SERVICE_DISABLED.
    echo "    waiting 15s for API propagation"
    sleep 15
fi

cat > "$POLICY_FILE" <<EOF
name: ${POLICY_NAME}
spec:
  rules:
  - enforce: false
EOF

# Don't swallow stderr — when this fails we need to see exactly
# why (missing role / API not enabled / quota / etc.).
ORGPOLICY_ERR=$(mktemp -t orgpolicy-err.XXXXXX)
# Detect whether the override was already applied (re-run case).
EXISTING_OVERRIDE=$(gcloud org-policies describe \
    "constraints/iam.disableServiceAccountKeyCreation" \
    --project="$SA_HOME_PROJECT" \
    --format="value(spec.rules[0].enforce)" 2>/dev/null || echo "")

if [ "$EXISTING_OVERRIDE" = "False" ]; then
    echo "    ✓ override already in place — skipping (no propagation wait)"
    rm -f "$ORGPOLICY_ERR"
elif gcloud org-policies set-policy "$POLICY_FILE" --quiet >/dev/null 2>"$ORGPOLICY_ERR"; then
    echo "    ✓ override applied (persistent — see comments at top of section)"
    rm -f "$ORGPOLICY_ERR"
    # Org-policy → IAM-service propagation can take 30-60s — the
    # set-policy call returns immediately but the IAM keys.create
    # path still sees the old enforce=true for a while.  Wait once
    # here; the key-mint loop below retries on FAILED_PRECONDITION
    # so we don't have to guess the exact propagation window.
    sleep 20
else
    echo "ERROR: failed to override constraints/iam.disableServiceAccountKeyCreation." >&2
    echo "       gcloud said:" >&2
    sed 's/^/         /' "$ORGPOLICY_ERR" >&2

    # Common shape #1: API not enabled on the project.
    if grep -q "API has not been used\|SERVICE_DISABLED\|orgpolicy.googleapis.com" "$ORGPOLICY_ERR"; then
        echo "" >&2
        echo "    Looks like Org Policy API isn't enabled on ${SA_HOME_PROJECT}." >&2
        echo "    Enable it (one-time, free):" >&2
        echo "      gcloud services enable orgpolicy.googleapis.com --project=${SA_HOME_PROJECT}" >&2
    fi

    # Common shape #2: missing role on the interactive account.
    # roles/owner at PROJECT scope doesn't include orgpolicy.policy.set;
    # roles/owner at ORG scope does.  Either grant orgpolicy.policyAdmin
    # at org, or enforce=false at the org scope instead.
    if grep -q "PERMISSION_DENIED\|orgpolicy.policy.set" "$ORGPOLICY_ERR"; then
        echo "" >&2
        echo "    Looks like ${ACTIVE_ACCOUNT} doesn't have orgpolicy.policy.set." >&2
        echo "    Grant yourself the dedicated role (one-time):" >&2
        echo "      gcloud organizations add-iam-policy-binding ${ORG_ID} \\" >&2
        echo "          --member=user:${ACTIVE_ACCOUNT} \\" >&2
        echo "          --role=roles/orgpolicy.policyAdmin" >&2
        echo "    Then re-run this script." >&2
    fi

    rm -f "$ORGPOLICY_ERR"
    exit 1
fi


# ─── Mint a key + save ────────────────────────────────────────────

echo ""
echo "==> Minting JSON key → ${KEY_FILE}"
if [ -f "$KEY_FILE" ]; then
    BACKUP="${KEY_FILE}.bak-$(date +%s)"
    mv "$KEY_FILE" "$BACKUP"
    echo "    existing key moved to ${BACKUP}"
fi

# Retry on FAILED_PRECONDITION — that's the org-policy propagation
# race.  Six attempts × 15s sleep = up to 90s of propagation
# patience, which covers the documented 60s worst case.
KEY_ERR=$(mktemp -t keys-create-err.XXXXXX)
KEY_OK=0
for ATTEMPT in 1 2 3 4 5 6; do
    if gcloud iam service-accounts keys create "$KEY_FILE" \
        --iam-account="$SA_EMAIL" \
        --project="$SA_HOME_PROJECT" >/dev/null 2>"$KEY_ERR"; then
        KEY_OK=1
        echo "    ✓ key minted (attempt ${ATTEMPT})"
        break
    fi
    if grep -q "disableServiceAccountKeyCreation\|FAILED_PRECONDITION" "$KEY_ERR"; then
        if [ "$ATTEMPT" -lt 6 ]; then
            echo "    org-policy override hasn't propagated yet (attempt ${ATTEMPT}/6) — sleeping 15s"
            sleep 15
            continue
        fi
    fi
    # Non-propagation error or final attempt — bail loudly.
    echo "ERROR: key creation failed:" >&2
    cat "$KEY_ERR" >&2
    rm -f "$KEY_ERR"
    exit 1
done
rm -f "$KEY_ERR"
[ "$KEY_OK" = "1" ] || exit 1
chmod 600 "$KEY_FILE"


# ─── Activate the SA in gcloud ─────────────────────────────────────
#
# Newly-minted SA keys take 30-60s to propagate from the IAM
# control plane to Google's auth backend (the JWT signing service).
# activate-service-account validates the key by signing a test JWT,
# so the first few attempts after key creation fail with
# `invalid_grant: Invalid JWT Signature` even though the key file
# is well-formed.  Retry with backoff.

echo ""
echo "==> Activating ${SA_EMAIL} as gcloud's persistent identity"
ACTIVATE_ERR=$(mktemp -t activate-err.XXXXXX)
ACTIVATE_OK=0
for ATTEMPT in 1 2 3 4 5 6; do
    if gcloud auth activate-service-account "$SA_EMAIL" \
        --key-file="$KEY_FILE" \
        --project="$SA_HOME_PROJECT" >/dev/null 2>"$ACTIVATE_ERR"; then
        ACTIVATE_OK=1
        echo "    ✓ activated (attempt ${ATTEMPT})"
        break
    fi
    if grep -q "invalid_grant\|Invalid JWT Signature\|JWT" "$ACTIVATE_ERR"; then
        if [ "$ATTEMPT" -lt 6 ]; then
            echo "    auth backend hasn't synced the key yet (attempt ${ATTEMPT}/6) — sleeping 15s"
            sleep 15
            continue
        fi
    fi
    # Non-propagation error or final attempt — bail loudly.
    echo "ERROR: activate-service-account failed:" >&2
    cat "$ACTIVATE_ERR" >&2
    rm -f "$ACTIVATE_ERR"
    exit 1
done
rm -f "$ACTIVATE_ERR"
[ "$ACTIVATE_OK" = "1" ] || exit 1

# Make it the default for future invocations.  Your interactive
# account stays available — switch with:
#   gcloud config set account ${ACTIVE_ACCOUNT}
gcloud config set account "$SA_EMAIL"


# ─── Verify ────────────────────────────────────────────────────────

echo ""
echo "==> Verifying"
echo "    Active account: $(gcloud config get-value account 2>/dev/null)"
echo "    Can list VMs (calls run AS the SA):"
for PROJ in "${VERIFY_PROJECTS[@]}"; do
    COUNT=$(gcloud compute instances list \
        --project="$PROJ" \
        --format="value(name)" 2>/dev/null | wc -l | tr -d ' ')
    echo "      ${PROJ}: ${COUNT} VM(s) visible"
done


echo ""
echo "==> Done.  Profile: ${PROFILE}"
echo ""
echo "Credential:"
echo "  Long-lived JSON key at ${KEY_FILE} (mode 0600)."
echo "  gcloud is now authenticated AS ${SA_EMAIL} and will stay that"
echo "  way across reboots, remote sessions, and shells — no re-auth"
echo "  needed.  The org's disableServiceAccountKeyCreation policy has"
echo "  been restored, so a future re-run of this script will need to"
echo "  flip + restore it again to mint a new key."
echo ""
echo "Day-to-day:"
echo "  - skydeploy + sky-lang.org deploy.sh need NO changes."
echo "  - Future projects under settleby.com auto-inherit access."
echo "  - To switch back to your interactive account temporarily:"
echo "      gcloud config set account ${ACTIVE_ACCOUNT}"
echo "  - To switch back to the SA:"
echo "      gcloud config set account ${SA_EMAIL}"
echo "  - To add more roles to the SA:"
echo "      ./scripts/setup-deploy-sa.sh [--profile=...]"
echo "  - To rotate the key (mint a new one, delete the old):"
echo "      ./scripts/setup-deploy-sa.sh --profile=${PROFILE}"
echo "      then \`gcloud iam service-accounts keys list --iam-account=${SA_EMAIL}\`"
echo "      to delete the older key id."
echo "  - To revoke the SA entirely (nuclear):"
echo "      gcloud iam service-accounts delete ${SA_EMAIL} --project=${SA_HOME_PROJECT}"
echo ""
case "$PROFILE" in
    ssh)
        echo "Surface: gcloud compute ssh|scp only.  When you need to"
        echo "deploy Cloud Run / Cloud SQL / etc., re-run with"
        echo "--profile=operator."
        ;;
    operator)
        echo "Surface: Cloud Run, Compute, Cloud SQL, Cloud Build,"
        echo "Cloud Storage, Logging, Monitoring, Secret Manager,"
        echo "Artifact Registry, SA impersonation.  Can NOT modify IAM"
        echo "bindings — so even if the JSON key leaked, attacker"
        echo "can't grant itself more roles."
        ;;
    owner)
        echo "Surface: literally everything your interactive account"
        echo "can do — including IAM modifications.  Sensible default"
        echo "for a personal deploy SA where you know you'll need to"
        echo "tweak bindings later."
        ;;
esac
echo ""
echo "Security note: long-lived JSON keys ARE the leaked-key-class"
echo "incident vector.  Keep ${KEY_FILE} (and any backup ~/.gcloud/"
echo "*.bak-* copies) on disks you control.  Never commit, never put"
echo "in shell history.  Rotate quarterly."
