# Sky docs

Live reference for the Sky language, stdlib, and toolchain.  The
primary compiler is the Rust implementation (`rust/`) — see
[`rust-rewrite/`](rust-rewrite/).  Compiler-journey history
(per-version design notes, audits, plans, postmortems) is preserved
under [`history/`](history/).

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
* `Std.Xml` (with `Std.Xml.Decode`, `Std.Xml.Encode`), `Codec.toXml` /
  `Codec.fromXml` and `Std.Zip`: safe XML and ZIP on the server. See the
  sections in [`stdlib.md`](stdlib.md) and `sky doc Std.Xml` / `sky doc
  Std.Zip`.
* `Std.Office.Xlsx` and `Std.Office.Docx`: read and write Excel workbooks
  and Word documents on the server. See the sections in
  [`stdlib.md`](stdlib.md) and `sky doc Std.Office.Xlsx` / `sky doc
  Std.Office.Docx`.
* `Std.Image` (resize, crop, trim, colour to alpha, animated GIF, lossless
  WebP, decode limits) and `Std.Image.Canvas` (draw shapes, text and pasted
  images on the server). See the sections in [`stdlib.md`](stdlib.md) and
  `sky doc Std.Image` / `sky doc Std.Image.Canvas`.

## Per-area

* [`skylive/`](skylive/) — Sky.Live runtime (TEA + SSE + sessions +
  routing).  Start with [`skylive/overview.md`](skylive/overview.md).
* [`skyui/`](skyui/) — `Std.Ui`, the typed no-CSS layout DSL.
* [`skytui/`](skytui/) — Sky.Tui (terminal backend for `Std.Ui`).
* [`skywebview/`](skywebview/) — Sky.Webview (native desktop window).
* [`skyauth/`](skyauth/) — `Std.Auth` (sessions + JWT + roles).
* [`skydb/`](skydb/) — `Std.Db` (SQLite + PostgreSQL).
* [`ai/`](ai/): the `Std.Ai` lifecycle (data, training, evaluation,
  registry, serving, device).  Test it with
  [`ai/testing-guide.md`](ai/testing-guide.md).
* [`history/v0.16.x-console/`](history/v0.16.x-console/) — Sky Console embedded mode,
  hub mode (`sky console-serve`), HubExporter, telemetry flow.

## Toolchain

* [`tooling/cli.md`](tooling/cli.md) — every `sky <command>`.
* [`tooling/lsp.md`](tooling/lsp.md) — language server features
  (completion, hover, references, code actions, call-hierarchy).
* [`tooling/testing.md`](tooling/testing.md) — `sky test`,
  `cargo test` + xtask gates, runtime verification.
* [`tooling/gate-harness.md`](tooling/gate-harness.md) — `xtask harness`:
  gate states, falsifying mutations, the canary, budget enforcement.

## Operations

* [`observability.md`](observability.md) — tracing + structured
  logs + Prometheus + Sky Console as the user-facing surface.

## Compiler internals (contributors)

The primary compiler is the Rust implementation.  Start with
[`rust-rewrite/`](rust-rewrite/) — architecture, pipeline, codegen,
runtime + FFI, testing, and milestones.

The earlier TypeScript, Sky-in-Sky and Haskell compilers were removed
from the tree in v0.29.0 ([`history/legacy-compilers.md`](history/legacy-compilers.md);
tag `v0.28.1` is the last that has them). The Haskell-era compiler docs
are kept as **legacy reference** under
[`history/compiler/`](history/compiler/):

* [`history/compiler/architecture.md`](history/compiler/architecture.md) —
  pipeline overview (legacy Haskell).
* [`history/compiler/pipeline.md`](history/compiler/pipeline.md) — phase-by-phase
  data flow (legacy Haskell).
* [`history/compiler/runtime-verification.md`](history/compiler/runtime-verification.md)
  — example sweep + Playwright drive (legacy Haskell).
* [`history/compiler/journey.md`](history/compiler/journey.md) — historical narrative
  of how Sky got here.  Kept for contributors.
* [`history/compiler/versions.md`](history/compiler/versions.md) — per-version
  feature ledger.

Language + interop references (backend-neutral):

* [`language/`](language/) — syntax, types, pattern matching,
  modules.
* [`errors/`](errors/) — error model + diagnostic format.
* [`ffi/`](ffi/) — Go interop + binding generation.

## Compiler journey (full history, kept by design)

* [`architecture/`](architecture/) — the stdlib correctness reference.
  The compiler's own architecture reference is
  [`rust-rewrite/`](rust-rewrite/).
* [`history/`](history/) — frozen material: per-version release docs
  and roadmaps (v0.16 to v0.27), RFC drafts, the superseded CI design
  (`history/ci-redesign-2026-08/`), and the archive of earlier design
  docs. Useful as historical context; the live docs above are the
  source of truth.
