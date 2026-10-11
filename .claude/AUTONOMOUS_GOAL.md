# Autonomous mandate: Std.Ai full production AI lifecycle (v0.28.0 line)

Started 2026-10-11. Branch `feat/std-ai-lifecycle`. Design: `docs/design/std-ai-lifecycle.md`.
Private context (cloud project, budget): `.claude/AUTONOMOUS_GOAL.private.md` (gitignored).
The previous mandate (single v0.27.0 release) is complete: v0.27.0 to v0.27.8 shipped.

## The user's goal, verbatim

> i want our Ai module to provide the full suite to build/train/eval completely with sky only, so 1 language for everything + production AI application complete development lifecycle

> i'd like you to start the full implementation in a branch/worktree for this work.
> ensure we have example apps (full eval + train + synthetic dataset) for e2e, using with fullstack sky app so users understand how it's done, and for us as testing how it works in practical scenario?

> ok please... in fact all phases implementation e2e + 100% unattended + autonomous + PIV mode please.
> if you have questions you need answered from me, ask now, otherwise please proceed 100% e2e.
>
> when you stop, it's only when you're genuinely stuck, or e2e fully implemented, tested + ready for me to test

## The user's binding decisions

- Configuration: NO sky.toml keys for AI. Everything in code via carefully constructed `withX` builders with env-var fallback, so code always holds the local default and QA/production override it with .env files / env vars.
- Errors: typed. One generic `Domain DomainFailure` variant added to `ErrorDetails`, designed so the global error type never needs touching again; typed per-module views (`Std.Ai.Failure`); generic helpers (`Error.domain`, `Error.isRetryable`, `Error.retryAfter`); every AI failure also sets a sensible existing `ErrorKind`.
- Base model family: Qwen (Apache-2.0) for dev, CI and examples.
- Eval judge: a local pinned model by default, switchable by env var.
- Dev adapter mode: quantised real base.
- Cloud: live GCP tests allowed, tiny models, hard budget cap (see private file), everything torn down after each test. GCP first.
- Registry: Std.Db is the source of truth for version records, aliases and eval history (artefacts in local dir / GCS / S3).
- Fine-tuning: adapters only (LoRA / QLoRA).
- Delivery: on the branch, no merge, no tag. All gates and the full CI release suite green, plus a test guide for the user.

## Scope (all phases of the design)

P1 Endpoint + Failure + Provider additions + Local (managed llama-server) + Eval core + console panel.
P2 Dataset (incl. synthetic generation) + Scrub + Registry (Std.Db truth) + Pipeline + promote/rollback/lineage + judge calibration.
P3 Train local (MLX first, Unsloth built and fixture-tested) + managed Python toolchains + durable training + GGUF export.
P4 Cloud train (GCP Vertex first) + Serve GPU tier (multi-LoRA) + deployment targets, live-tested under the cap.
P5 Browser and device bridges.
Examples: a full-stack Sky app showing synthetic dataset -> train -> eval -> register/promote -> serve end to end, plus per-module examples, used as practical e2e tests.

## Done means

An independent Judge verifies the verbatim goal literally: every phase implemented, every example app works end to end, tests and the full release suite green, and a test guide ready for the user.
