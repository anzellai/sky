# Std.Ai on Google Cloud: training on Vertex AI, serving on Cloud Run

This page sets up a Google Cloud project for `Std.Ai.Train`'s `Vertex` backend
and `Std.Ai.Serve`'s Cloud Run target, says what each piece costs, and how
everything Sky makes is found again and deleted. Every call is Sky over HTTP
(`Std.Ai.Cloud.Google`); the `gcloud` CLI is needed only for the one-time
setup below.

## One-time setup (free)

```
scripts/ai/gcp-bootstrap.sh --project <id>
```

turns on the APIs (Vertex AI, Cloud Run, Cloud Storage, Artifact Registry,
Secret Manager, Cloud Quotas, Cloud Logging, Cloud Build, Cloud Billing) and
creates two service accounts with no project-wide role:

* `sky-trainer`, what a training job runs as. Give it `roles/storage.objectAdmin`
  on the staging bucket only.
* `sky-serve`, what the tier runs as. Give it `roles/storage.objectViewer` on
  the registry's bucket and `roles/secretmanager.secretAccessor` on the tier's
  token secret only.

The account that runs Sky's commands needs Vertex AI User, Cloud Run Admin,
Artifact Registry Administrator (or Writer on the repository), Storage Admin
on the buckets, Cloud Build Editor, and Service Account User on the two
accounts above.

### Credentials

Sky signs as a service account: `GOOGLE_APPLICATION_CREDENTIALS` names its key
file, or, on GCE, GKE, Cloud Run and Vertex AI, the metadata server gives the
machine's account. A user's own login (`gcloud auth application-default
login`, an `authorized_user` file) is refused (`NotConfigured`): what runs in
production is a service account, so the development machine is too.

### Quotas

A new project usually has **no** L4 quota for Vertex AI training and none for
Cloud Run. `Train.plan` reads the Vertex quota through Cloud Quotas and stops
at `QuotaExceeded` before anything launches. Ask for one L4 each (approval is
usually quick for one GPU):

```
gcloud beta quotas preferences create --project <id> \
  --service aiplatform.googleapis.com --quota-id CustomModelTrainingL4GPUsPerProjectPerRegion \
  --dimensions region=us-central1 --preferred-value 1 --preference-id sky-vertex-l4 \
  --justification "LoRA fine-tuning of small models with Sky's Std.Ai.Train"

gcloud beta quotas preferences create --project <id> \
  --service run.googleapis.com --quota-id NvidiaL4GpuAllocNoZonalRedundancyPerProjectRegion \
  --dimensions region=us-central1 --preferred-value 1 --preference-id sky-run-l4 \
  --justification "a GPU serving tier with one L4 that scales to zero"
```

A region with no free L4 at the moment is `CapacityUnavailable` (retryable).
Sky never moves a job to another region on its own: the data stays where you
put it.

## The images, built in your project

Nothing is pulled from a Sky registry. Two images are built with Cloud Build
in your own project, into your own Artifact Registry, from the Dockerfiles
this Sky carries, and pinned by the digest Cloud Build reports:

```
sky ai image build --train --project <id> --region us-central1 --repo sky --staging gs://<bucket>/build
sky ai image build --tier  --project <id> --region us-central1 --repo sky --staging gs://<bucket>/build
```

* **The trainer** (`runtime-go/rt/ai_train_assets/image/trainer.dockerfile`):
  NVIDIA's CUDA 13 base pinned by digest, the pinned CPython and uv, the
  Unsloth hash lock installed with `--require-hashes --only-binary`, and the
  Sky driver. Its entry point is the shim `sky_cloud.py`, which moves the
  run's data, checkpoints and adapter through the staging bucket. Set the
  printed reference as `SKY_AI_TRAIN_IMAGE` (or `Train.withImage`).
* **The tier** (`Serve.dockerfile`): the static linux/amd64 tier binary on
  distroless. Set it as `SKY_AI_SERVE_IMAGE` (or `Serve.withTierImage`).

vLLM's own image is not rebuilt: Cloud Run pulls `vllm/vllm-openai` by digest
(`Serve.defaultEngineImage`). To pull it inside your region, create an
Artifact Registry remote repository of Docker Hub and point
`Serve.withEngineImage` at it, with the same digest:

```
gcloud artifacts repositories create dockerhub --project <id> --location us-central1 \
  --repository-format docker --mode remote-repository --remote-docker-repo DOCKER-HUB
# us-central1-docker.pkg.dev/<id>/dockerhub/vllm/vllm-openai@sha256:<digest>
```

## Training on Vertex AI

```elm
supportJob : Train.Job
supportJob =
    Train.lora Train.devBase version
        |> Train.withQuantisedBase
        |> Train.withBackend Train.Vertex
        |> Train.withCloud { project = "", region = "us-central1", machine = "g2-standard-4", staging = "" }
        |> Train.withBudget (Money.fromMajor Money.USD 2)
        |> Train.withMachinePrices prices
```

with `SKY_AI_TRAIN_PROJECT`, `SKY_AI_TRAIN_STAGING=gs://<bucket>/train` and
`SKY_AI_TRAIN_IMAGE` set. See [train.md](train.md#training-on-vertex-ai) for
the workflow, exactly-once launch, the budget and cancel. From the CLI:

```
sky ai train --data .sky/ai/datasets/support --backend vertex --project <id> \
  --region us-central1 --machine g2-standard-4 --staging gs://<bucket>/train \
  --budget "USD 2.00" --image <ref>@sha256:<hex> \
  --price g2-standard-4@us-central1=0.95 --price-book 2026-10-12 --register
```

The staging bucket must be in the job's region (a multi-region bucket is
refused). Make it with uniform access, public access prevention, and soft
delete off, so deleted objects stop costing at once:

```
gcloud storage buckets create gs://<bucket> --location us-central1 \
  --uniform-bucket-level-access --public-access-prevention --soft-delete-duration=0
```

## Serving on Cloud Run

```elm
target : Serve.Target
target =
    Serve.cloudRun { project = "", region = "us-central1", service = "support-tier" }
        |> Serve.withTierImage "<ref>@sha256:<hex>"
        |> Serve.withTokenSecret "support-tier-token"
        |> Serve.withRegistryUrl "gs://<bucket>/registry"
        |> Serve.withServiceAccount "sky-serve@<id>.iam.gserviceaccount.com"
        |> Serve.withInvokers [ "serviceAccount:app@<id>.iam.gserviceaccount.com" ]

deploy = Serve.deploy target tier
```

One instance holds two containers: the tier (the ingress, port 8080, 1 vCPU,
4 GiB) and vLLM (one L4, 4 vCPU, 16 GiB), sharing an in-memory volume. vLLM
starts after the tier's startup probe passes, so the base is staged first.
The service is never public: `deploy` keeps IAM on and grants
`roles/run.invoker` to `withInvokers` only. The tier's token comes from
Secret Manager, never from text in the service:

```
openssl rand -hex 32 | gcloud secrets create support-tier-token --data-file=-
```

`deploy` creates the service or updates it (guarded by its etag), waits for
the revision to be ready (a revision that never is: `DeploymentFailed` with the
platform's reason), and grants the invokers. Every step is idempotent: run it
again after a failure. `Serve.render` (and `sky ai serve --image`) give the
same service as Admin API JSON and as Knative YAML for
`gcloud run services replace`.

## What it costs

Prices are the public list prices in us-central1, read from the Cloud Billing
catalogue on 2026-10-12 with `sky ai cloud prices` (each rate rounded up;
`scripts/ai/live/gcp-prices.json` lists every SKU):

| What | Rate |
|---|---|
| Vertex AI training, `g2-standard-4` (4 vCPU, 16 GiB, one L4, 100 GB SSD boot disk) | USD 0.95 an hour |
| Vertex AI training, `n2-standard-4` (no GPU: the Fake driver) | USD 0.28 an hour |
| Cloud Run, one L4 without zonal redundancy, 5 vCPU, 20 GiB (the tier's instance) | USD 1.14 an hour while an instance runs; nothing at zero |
| Cloud Build, default pool | USD 0.006 a build minute |

A job's estimate is 15 minutes of provisioning, the steps, and 5 minutes of
export, at the price book's rate; `Train.plan` refuses a job whose estimate is
above its budget. The budget also sets the job's hard runtime ceiling, so
Vertex stops it even when no Sky process is left.

Check the prices yourself before you rely on them:

```
sky ai cloud prices --service "Vertex AI" --region us-central1 --match Training --match L4
sky ai cloud prices --service "Cloud Run" --region us-central1 --match L4
```

## Finding and deleting what Sky made

Everything Sky creates carries `sky-managed=true` (and `sky-run`,
`sky-attempt`, `sky-kind`, `sky-deployment`), plus your own labels
(`Train.withLabels`, `Serve.withLabels`, `SKY_AI_TRAIN_LABELS`).

```
sky ai cloud ls --project <id> --region us-central1            # kinds and short names
sky ai teardown --run <key> --staging gs://<bucket>/train      # a run's jobs and staging prefix
sky ai teardown --service support-tier                         # the tier
sky ai teardown --all-sky                                      # every job and service Sky made
```

A training run deletes its own Vertex job records and staging prefix after
the adapter is registered (`Train.withKeepStaging` keeps them).

## Live tests

`scripts/ai/gcp-live.sh --project <id> --stages l0,l1,l2,l3,l4,l5` runs the
paid tests (`rust/crates/sky/tests/ai_gcp_live.rs`) under a hard cap: it
refuses to start unless the ledger (`scripts/ai/live/gcp-ledger.jsonl`) has
room for 1.25 times the run's worst case under USD 40, labels every resource,
and always tears everything down and checks for leaks. The CI workflow
`ai-cloud-live.yml` runs it on manual dispatch only, never on a commit or a
release. See [../tooling/testing.md](../tooling/testing.md).
