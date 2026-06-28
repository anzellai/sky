# Sky docs

Live reference for v0.17.x.  Compiler-journey history (per-version
design notes, audits, plans, postmortems) is preserved under
[`archive/`](archive/) and the per-version directories below.

## Start here

* [`getting-started.md`](getting-started.md) — install + your
  first app in 5 minutes.
* [`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md) — current-version
  gotchas + workarounds.
* [`sky-toml.md`](sky-toml.md) — every config key.

## Stdlib + APIs

* [`stdlib.md`](stdlib.md) — every module + every function, indexed
  by tier (Pure / Fallible-pure / Task / Diverging).  `sky doc
  --serve` is the same data in browsable form.

## Per-area

* [`skylive/`](skylive/) — Sky.Live runtime (TEA + SSE + sessions +
  routing).  Start with [`skylive/overview.md`](skylive/overview.md).
* [`skyui/`](skyui/) — `Std.Ui`, the typed no-CSS layout DSL.
* [`skytui/`](skytui/) — Sky.Tui (terminal backend for `Std.Ui`).
* [`skywebview/`](skywebview/) — Sky.Webview (native desktop window).
* [`skyauth/`](skyauth/) — `Std.Auth` (sessions + JWT + roles).
* [`skydb/`](skydb/) — `Std.Db` (SQLite + PostgreSQL).
* [`v0.16.x-console/`](v0.16.x-console/) — Sky Console embedded mode,
  hub mode (`sky console-serve`), HubExporter, telemetry flow.

## Toolchain

* [`tooling/cli.md`](tooling/cli.md) — every `sky <command>`.
* [`tooling/lsp.md`](tooling/lsp.md) — language server features
  (completion, hover, references, code actions, call-hierarchy).
* [`tooling/testing.md`](tooling/testing.md) — `sky test`, hspec
  cabal-tests, runtime verification.

## Operations

* [`observability.md`](observability.md) — tracing + structured
  logs + Prometheus + Sky Console as the user-facing surface.

## Compiler internals (contributors)

* [`compiler/architecture.md`](compiler/architecture.md) —
  pipeline overview.
* [`compiler/pipeline.md`](compiler/pipeline.md) — phase-by-phase
  data flow (parse → canonicalise → type → lower → emit).
* [`compiler/runtime-verification.md`](compiler/runtime-verification.md)
  — example sweep + Playwright drive.
* [`compiler/journey.md`](compiler/journey.md) — historical narrative
  of how Sky got here.  Kept for contributors.
* [`compiler/versions.md`](compiler/versions.md) — per-version
  feature ledger.
* [`language/`](language/) — syntax, types, pattern matching,
  modules.
* [`errors/`](errors/) — error model + diagnostic format.
* [`ffi/`](ffi/) — Go interop + binding generation.

## Compiler journey (full history, kept by design)

* [`architecture/`](architecture/) — canonical compiler + stdlib
  architecture references. First source consulted on any compiler
  or stdlib change.
* [`v0.17/`](v0.17/) — active v0.17.x release docs (release plan,
  rt.Coerce residual surface, Judge verdicts, session checkpoints).
* [`v0.17-roadmap/`](v0.17-roadmap/) — multi-PR roadmap for the
  v0.17 typed-emit close.
* [`v0.17.x/`](v0.17.x/), [`v1-rfc/`](v1-rfc/) — forward-looking
  release + RFC drafts.
* [`v0.16.x-console/`](v0.16.x-console/) — v0.16 Sky Console
  embedded mode, hub mode (`sky console-serve`), HubExporter,
  telemetry flow.
* [`archive/`](archive/) — pre-v0.16 design docs, v0.15.x hardening
  cycle log, v0.17 per-PR design notes (moved here 2026-06-28),
  superseded roadmaps. Useful as historical context; the live
  docs above are the source of truth.
