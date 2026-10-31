# Sky docs

Live reference for the Sky language, its stdlib and its toolchain. The API of
every module is generated from the stdlib source: run `sky doc <Module>`, or
`sky doc --serve` for the same pages in a browser. Frozen material (per-version
plans, audits, the retired compilers) is in [`history/`](history/).

## Start here

* [`getting-started.md`](getting-started.md): install and your first app.
* [`learn/`](learn/): the "Learn Sky" tour, from values and types to a deployed
  web app.
* [`showcase.md`](showcase.md): the full programs behind the README samples.
* [`KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md): current gotchas and
  workarounds.
* [`migration/`](migration/): what changes between minors (`v0.27.md`,
  `v0.28.md`, `v0.29.md`), as loud, loud at run time and silent.
* [`sky-toml.md`](sky-toml.md): every `sky.toml` key and environment variable.

## One entry point: `Std.App`

`Std.App` is the one app builder. Write `App.app { init, update, view,
subscriptions }`, and pick the shape at build time with `--target`: `web`
(Sky.Live, the default), `web:app` (Sky.Spa, client-side wasm), `desktop`,
`terminal:tui`, `terminal:cli`, `mobile:ios`, `mobile:android` and the other
client targets. See [`skyapp/overview.md`](skyapp/overview.md) and
[`skyapp/native.md`](skyapp/native.md). The per-shape front doors (`Std.Live`,
`Std.Spa`, `Std.Tui`, `Std.Cli`, `Std.Webview`) are deprecated.

## Stdlib modules

[`stdlib.md`](stdlib.md) indexes every function by tier (pure, fallible-pure,
Task, diverging). This list is the output of `sky doc --list`, in groups.

**Core language (`Sky.Core`).** `Sky.Core.Prelude` (imported for you),
`Basics`, `Maybe`, `Result`, `Error`, `Task`, `List`, `Array` (indexed
sequences, v0.29.0), `Dict`, `Set`, `String`, `Char`, `Tuple`, `Math`,
`Regex` (a compiled `Regex` type, v0.29.0), `Bytes`, `Encoding`, `ToString`,
`Pure`, `Secret`, `Json.Decode`, `Json.Decode.Pipeline`, `Json.Encode`, `Jwt`,
`Crypto`, `Random`, `Uuid`, `Time`, `File`, `Path`, `Io`, `Process`, `System`,
`Http`, `Http.Stream` and `WebSocket`. Every collection function states its
cost in a `Cost:` line in `sky doc`.

**Apps and UI.** `Std.App`, `Std.Cmd`, `Std.Sub`, `Std.Nav`, `Std.Native`,
`Std.Bundle`; `Std.Ui` with `Std.Ui.Animation`, `Background`, `Border`,
`Canvas`, `Chart`, `Events`, `Font`, `Grid`, `Input`, `Keyed`, `Lazy`,
`Region`, `Responsive`, `Terminal`, `Transform` and `Transition`; `Std.Html`
with `Std.Html.Attributes` and `Std.Html.Events`; `Std.Css`;
`Std.Live.Console` and `Std.Live.Head`.

**HTTP servers.** `Sky.Http.Server`, `Sky.Http.Server.Stream`,
`Sky.Http.Server.WebSocket`, `Sky.Http.Middleware` and `Sky.Http.RateLimit`.

**Data and files.** `Std.Db`, `Std.Db.Store`, `Std.Db.Table`,
`Std.Db.Schema`, `Std.Db.Migrate`, `Std.Db.Decode`; `Std.Codec` (JSON, the
database, and XML through `Codec.toXml` / `Codec.fromXml`); `Std.Csv`,
`Std.Markdown`, `Std.Compression`, `Std.Cache`; and, new in v0.29.0,
`Std.Xml` with `Std.Xml.Decode` and `Std.Xml.Encode`, `Std.Zip`,
`Std.Office.Xlsx` (Excel workbooks) and `Std.Office.Docx` (Word documents).

**Images.** `Std.Image` (resize, crop, trim, colour to alpha, animated GIF,
lossless WebP, decode limits), `Std.Image.Canvas` (draw shapes, text and pasted
images on the server, v0.29.0) and `Std.Qr`.

**Security and money.** `Std.Auth`; `Std.Crypto.Sign`, `Std.Crypto.Kx`,
`Std.Crypto.Kdf`, `Std.Crypto.Noise` and `Std.Crypto.Cpace`; `Std.Decimal` and
`Std.Money`.

**Effects and operations.** `Std.Durable`, `Std.Jobs`, `Std.PubSub`,
`Std.Sync`, `Std.Watch`, `Std.Email`, `Std.Time`, `Std.System`, `Std.Config`,
`Std.Log`, `Std.Trace` and `Std.Analytics`.

**AI (`Std.Ai`).** Calling models: `Std.Ai.Provider`, `Std.Ai.Endpoint` (model
slots), `Std.Ai.Local` (a managed local engine), `Std.Ai.Device` (models on the
user's device) and `Std.Ai.Failure`. Agents: `Std.Ai.Agent`, `Std.Ai.Tool`,
`Std.Ai.Policy` (the action firewall), `Std.Ai.Trace` (cost and call ledger)
and `Std.Ai.Memory.Pg`. Training and evaluation: `Std.Ai.Dataset`,
`Std.Ai.Dataset.Scrub`, `Std.Ai.Dataset.Synthetic`, `Std.Ai.Eval`,
`Std.Ai.Registry`, `Std.Ai.Store`, `Std.Ai.Pipeline`, `Std.Ai.Train`,
`Std.Ai.Serve` and `Std.Ai.Cloud.Google`. Guides are in [`ai/`](ai/).

**Project configuration, tests and FFI.** `Sky.Config` (the `config` binding),
`Sky.Test`, and `Sky.Ffi` (stdlib-only: user code calls the typed stdlib or a
`sky add` binding).

**Short aliases.** `Auth`, `Basics`, `Char`, `Cmd`, `Context`, `Crypto`, `Db`,
`Dict`, `Encoding`, `Ffi`, `File`, `Fmt`, `Http`, `Io`, `JsonDec`, `JsonDecP`,
`JsonEnc`, `List`, `Log`, `Math`, `Maybe`, `Middleware`, `Path`, `Process`,
`Random`, `RateLimit`, `Regex`, `Result`, `Server`, `Set`, `String`, `Sub`,
`System`, `Task`, `Time` and `Uuid` are shorter names for the modules above.

**Deprecated, use `Std.App`.** `Std.Cli`, `Std.Live`, `Std.Spa`, `Std.Tui` and
`Std.Webview`.

## Per area

* [`skyapp/`](skyapp/): `Std.App`, the targets and native packaging.
* [`skylive/`](skylive/): Sky.Live (TEA, SSE, sessions, routing). Start with
  [`skylive/overview.md`](skylive/overview.md).
* [`skyspa/`](skyspa/): Sky.Spa, client-side TEA in wasm. Start with
  [`skyspa/overview.md`](skyspa/overview.md).
* [`skyui/`](skyui/): `Std.Ui`, the typed layout DSL with no CSS.
* [`skytui/`](skytui/): the terminal backend for `Std.Ui`.
* [`skywebview/`](skywebview/): the native desktop window.
* [`skyauth/`](skyauth/): `Std.Auth` (sessions, JWT, roles).
* [`skydb/`](skydb/): `Std.Db` (SQLite and PostgreSQL), embedded PostgreSQL.
* [`ai/`](ai/): the `Std.Ai` lifecycle (data, training, evaluation, registry,
  serving, device). Test it with [`ai/testing-guide.md`](ai/testing-guide.md).
* [`design/`](design/): design notes for durable execution, `Std.Ai`,
  `Std.App` and the diagrams.
* [`security/`](security/): the typed `Secret` and the security notes.

## Toolchain

* [`tooling/cli.md`](tooling/cli.md): every `sky <command>`.
* [`tooling/lsp.md`](tooling/lsp.md): the language server.
* [`tooling/testing.md`](tooling/testing.md): `sky test`, test mode and
  mocks, `sky fuzz`, and the compiler's own gates.
* [`tooling/gate-harness.md`](tooling/gate-harness.md): `xtask harness`, gate
  states, falsifying mutations and budgets.

## Operations

* [`observability.md`](observability.md): structured logs, tracing,
  Prometheus and the Sky Console.
* [`perf/sizing.md`](perf/sizing.md): measured memory, CPU and session
  figures for sizing a host.
* [`learn/17-deploying.md`](learn/17-deploying.md): Docker, VMs and the
  production settings.

## Language and interop

* [`language/`](language/): syntax, types, pattern matching, modules.
* [`errors/`](errors/): the error model and one page per diagnostic code.
* [`ffi/`](ffi/): Go interop and binding generation.

## Compiler internals (contributors)

The compiler is the Rust workspace under `rust/`. Start with
[`rust-rewrite/`](rust-rewrite/): architecture, pipeline, codegen, runtime and
FFI, testing, and the change-verification matrix. The stdlib correctness
reference is [`architecture/sky-stdlib-correctness.md`](architecture/sky-stdlib-correctness.md).
The earlier TypeScript, Sky-in-Sky and Haskell compilers left the tree in
v0.29.0: see [`history/legacy-compilers.md`](history/legacy-compilers.md).
