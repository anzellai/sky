# `docs/archive/`

Historical design proposals, audit notes, multi-cycle plans, and
session briefs that landed (or were superseded).  Kept for context;
no longer maintained.

For the current live docs see [`../README.md`](../README.md).

## What's in here

* **Pre-v0.16 design + audit pipeline** —
  `fragility-audit-v0.15.3.md`, `improvement-plan-v0.16.md`,
  `parametric-record-aliases-bugs.md`,
  `typed-codegen-design.md`, `TYPED_CODEGEN_GAPS.md`,
  `V013_TYPED_CODEGEN_COMPLETION.md`,
  `V1_TYPED_CODEGEN_FINISH.md`, `v013-*.md`, `v012-*.md`.
  All of this drove the v0.13-v0.15 type-soundness work that
  shipped as type-directed lowering + Go generics on parametric
  record aliases.  Live state: [`../KNOWN_LIMITATIONS.md`](../KNOWN_LIMITATIONS.md).
* **Pre-v0.16 production-readiness audits** —
  `AUDIT_REMEDIATION.md`, `PRODUCTION_READINESS.md`,
  `NEXT_SESSION_BRIEF.md`, `tea-backends.md`,
  `std-ui-cross-platform.md`, `sky-live-*.md`,
  `sky-tui-coverage.md`.  Live state: per-area
  `../skylive/` / `../skyui/` / `../skytui/` overviews.
* **v0.15.x compiler-fragility hardening narrative** —
  [`v0.15.x-hardening/`](v0.15.x-hardening/) holds the per-cycle
  Auditor / Planner / Developer agent notes, the cycle log, and
  the Cycle 7 Std.Ui audit.  The closures themselves are reflected
  in shipped commits + per-version markers in `../stdlib.md`.
* **v0.16.x console release narrative** —
  [`v0.16.x-console/`](v0.16.x-console/) holds the v0.16.0 / v0.16.1
  per-PR audits, RFCs, release notes, and v0.16.4 implementation
  plan.  Live docs (EMBEDDED, EXPORTER, HUB, HUB-UI, OPS,
  OVERVIEW, SERVERLESS, TELEMETRY_FLOW) stay in
  [`../v0.16.x-console/`](../v0.16.x-console/).
* **Pre-v0.16 observability design** —
  `observability-design.md`.  Superseded by the live
  [`../observability.md`](../observability.md).
* **v1 roadmap (pre-v0.16 snapshot)** — `v1-roadmap.md`.  The live
  status is now captured by `../KNOWN_LIMITATIONS.md` + per-version
  release notes; the roadmap remains here as a snapshot of how the
  plan looked when it was authored.
* **Older legacy README** — `legacy-README.md` (the pre-v0.10 README
  snapshot, kept as a curiosity).

## When to look here

* You're trying to understand *why* a v0.13-v0.15 design landed the
  way it did and want the original audit / plan.
* You're tracking a closure citation in a commit message back to
  its source plan.
* You're a historian of the compiler's evolution.

## When NOT to look here

* You want the current state of any module or surface — look in
  [`../README.md`](../README.md).
* You hit a bug at HEAD — check [`../KNOWN_LIMITATIONS.md`](../KNOWN_LIMITATIONS.md)
  first; the archive describes a state that may have moved.
* You want the canonical Sky v1.0 surface contract — read the live
  per-area docs ([`../skylive/`](../skylive/), [`../skyui/`](../skyui/),
  [`../skyauth/`](../skyauth/), [`../skydb/`](../skydb/), etc.).
