# 74 · Train in the cloud, serve from a GPU tier

`Std.Ai.Train` on Google Cloud Vertex AI and `Std.Ai.Serve` on Cloud Run, in
one web app: freeze a support dataset, fine-tune a QLoRA adapter on one
NVIDIA L4 under a budget, register it, deploy a private GPU tier that serves
it with vLLM, and ask it questions.

```bash
sky test tests/CloudTest.sky   # the whole lifecycle offline, against the emulator in the runtime
sky run src/Main.sky           # then open http://localhost:8074
```

The test needs no Google account and spends nothing: `.env.test` points
`SKY_AI_GCP_ENDPOINT` at the emulator, which runs the Vertex job with the
Fake trainer and keeps the Cloud Run service in memory.

## What the app does

1. At start it freezes `data/support.jsonl` into a dataset version under
   `.sky/ai/datasets/support` (80 % trains, 20 % measures).
2. **Train on Vertex AI** starts the durable training run. Before anything
   launches, the plan checks the job, the trainer image digest, the staging
   bucket's region, the L4 quota and the estimate against the budget. The
   page then shows the step, the losses, the Vertex job's state and the spend
   against the budget.
3. The adapter comes back to the machine, is converted to GGUF and is
   registered as `support` with its lineage. The PEFT files are kept as the
   version's companions. The page points `dev` at the version.
4. **Deploy the tier** puts the GPU tier on Cloud Run (`Serve.deploy`). One
   instance runs the tier and vLLM with one L4, and scales to zero. The tier
   loads `support@prod` and `support@qa` and hot-loads each promotion.
5. **Ask** calls the `support` slot. With `SKY_AI_SUPPORT_URL` set it calls
   the tier. Without it, it runs the managed local engine with the version's
   GGUF file.

The job, the tier, the target and the slot are in `src/Cloud.sky`. The
`sky ai` verbs run `src/Ai.sky`:

```bash
sky ai run train                    # the training job, from the command line
sky ai eval support --candidate support@v1 --against prod --promote-to prod
sky ai run deploy-tier
sky ai run teardown-tier
```

The app's database is `cloud.db` (SQLite), connected in code with
`Config.withDatabase`. It holds the durable runs and the registry's records.

## A real run

Do the one-time setup in [`docs/ai/deploy-gcp.md`](../../docs/ai/deploy-gcp.md):
the APIs, the two service accounts, one L4 of quota for Vertex AI training and
one for Cloud Run, a staging bucket in `us-central1`, the two images built in
your project, and the tier's token in Secret Manager. Then:

```bash
export GOOGLE_APPLICATION_CREDENTIALS=<a service-account key file>
export SKY_AI_TRAIN_PROJECT=<id> SKY_AI_SERVE_PROJECT=<id>
export SKY_AI_TRAIN_STAGING=gs://<bucket>/train SKY_AI_REGISTRY=gs://<bucket>/registry
export SKY_AI_TRAIN_IMAGE=<trainer ref>@sha256:<hex> SKY_AI_SERVE_IMAGE=<tier ref>@sha256:<hex>
sky run src/Main.sky
```

Every setting is a builder in the code with a variable that overrides it.
Nothing is in `sky.toml`.

## What it costs

At the list prices of 2026-10-12 in `us-central1`: the training job runs on a
`g2-standard-4` (one L4) at about USD 0.95 an hour, and the plan counts 15
minutes of provisioning and 5 of export on top of the steps. The budget in
the code is USD 2, and it is also the job's hard runtime ceiling. The tier
costs about USD 1.14 an hour while an instance runs and nothing at zero.
Delete everything with `sky ai run teardown-tier` and
`sky ai teardown --all-sky`.
