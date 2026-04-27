# Changelog

Notable user-visible changes. Keep this file additive — never rewrite history.

## Unreleased

### Licence + attribution

- **Relicensed to Apache License 2.0** (was MIT). Existing MIT releases (v0.10.0 and earlier) keep their original MIT terms; v0.10.1 onwards ships under Apache 2.0. The relicense brings:
  - **Patent grant** from contributors (Apache 2.0 §3) — perpetual, irrevocable patent licence for what their contribution covers.
  - **Patent-retaliation clause** — anyone initiating patent litigation against Sky users for the contribution loses their grant.
  - **Trademark clause** (§6) — the licence does not grant rights to use the "Sky" name / trademarks.
  - **NOTICE file mechanism** (§4(d)) — a structured way to propagate prior-art attribution through forks. `NOTICE.md` at the repo root.
  Same permissive philosophy as MIT (commercial use, modify, fork, sublicense all allowed). See [CONTRIBUTING.md](CONTRIBUTING.md) for what this means for contributors. Same week, the [Std.Ui — Sky.Live polish + 4 compiler reliability fixes](https://github.com/anzellai/sky/pull/36) PR also lands.
- **Per-file derivative-work attribution** strengthened on the ten files in `src/Sky/` adapted from elm/compiler (BSD-3-Clause, © Evan Czaplicki). Each file's header now names the upstream module + licence + copyright, and `NOTICE.md` lists every adapted file with its origin and reproduces the full BSD-3-Clause licence text. This satisfies BSD-3-Clause clauses 1 + 2 (source-form + binary-form attribution).
- **Defensive endorsement-clause cleanup**: removed promotional uses of "Elm" (and the prior promotional uses of "elm-ui") from user-facing docs / READMEs / runtime comments. Factual technical references — "Elm-compatible syntax", "matches Elm's behaviour", "Elm convention", per-file derivative-work attribution — stay because they are descriptive, not promotional.

### Effect boundary (stdlib)

- **Breaking — `Std.Db.*` migrated from `Result Error a` to `Task Error a`.** `Db.connect`, `Db.open`, `Db.exec`, `Db.execRaw`, and `Db.query` now return `Task Error a`. Their runtime helpers (`runtime-go/rt/db_auth.go`) wrap their bodies in `func() any { ... }` thunks so the actual SQL defers to the goroutine spawned by `Cmd.perform` instead of blocking Sky.Live's `update()`.
  - **Why:** DB ops can take hundreds of milliseconds, can fail meaningfully, and compose naturally with `Task.parallel` / `Task.andThen` / `Cmd.perform`. Typing them as Result was a pre-Sky.Live legacy that forced every effectful pipeline to either bridge through `Task.fromResult` or block the dispatcher.
  - **Migration in this branch:** every `Lib/Db.sky` (08-notes-app, 12-skyvote, 17-skymon) and `Lib/Games.sky` (16-skychess) wrapper kept its Result-shaped public API by bridging through `Task.run` internally — consumers (Main.sky, Page/*.sky) need no changes. `examples/07-todo-cli/src/Main.sky` was rewritten as a proper Task-chained CLI demonstrating the canonical error-propagation pattern. `examples/18-job-queue/src/Main.sky` was simplified to drop the now-unnecessary bridge helpers in `saveSnapshot`/`loadHistory`. `examples/13-skyshop` is unaffected (it uses Firestore, not Std.Db).
  - **For new app code:** prefer composing Task-returning Db calls directly (`Db.exec db "INSERT..." [...] |> Task.andThen ...`) and dispatch via `Cmd.perform`. Use the Lib-layer `Task.run` bridge only when wrapping a singleton conn for synchronous case-pattern matching inside an existing update branch.

- **Added — `Task.onError` and `Task.mapError`.** Mirror their Result counterparts. `Task.onError : (e -> Task e2 a) -> Task e a -> Task e2 a` recovers from a Task error by producing a new Task — the canonical primitive for converting DB / FFI errors into 4xx/5xx HTTP responses, Sky.Live notifications, or CLI exit codes. `Task.mapError : (e -> e2) -> Task e a -> Task e2 a` adds context to an error before propagation.

- **Added — kernel sigs for `File.*`, `Process.*`, `Io.*`, `Crypto.randomBytes`, `Crypto.randomToken`** (Bucket A2 of the audit). Type-only addition: the runtime helpers already returned Task thunks, the docs/stdlib tables already promised Task; HM now enforces what the runtime had silently delivered. Net-zero migration.

- **Codegen fix — `coerceArg` now handles `SkyTask` params.** Previously, passing a value to a function expecting a typed `rt.SkyTask[E, A]` param emitted `any(arg).(rt.SkyTask[E, A])` direct assertion, which panicked at runtime against `func() any` from runtime helpers and against `SkyTask[any, any]` from cross-instantiation pass-through (Go generics are nominal). Fixed by routing parametric SkyTask param targets through `rt.TaskCoerceT`, mirroring the existing `SkyResult`/`SkyMaybe` handling. Also extended the same wrap to the `VarLocal` call-result path. This unblocked the entire Db.* migration.

- **Doctrine clarification in CLAUDE.md ("Effect Boundary: Task — two-tier in practice").** The audit considered migrating *every* effectful op to Task (println / Slog / Os.getenv / Os.getcwd / Time.now / Time.unixMillis) and concluded these stay sync. Reasons documented in CLAUDE.md under "Why theory ≠ practical here" — `let _ = println …` discard pattern, module-level `apiKey = Os.getenv "X" |> Result.withDefault ""` config reads, "stamp this row" timestamp use sites. Sky picks the Elm-pragmatic position over the Haskell-purist one: real I/O that benefits from composition goes through Task; sync convenience effects that don't benefit stay sync.

### Sky.Live

- **Breaking — default HTML template no longer loads Inter from Google Fonts.** The shell document emitted by `Live.app` previously preconnected to `fonts.googleapis.com` / `fonts.gstatic.com`, fetched the Inter family, and forced `font-family: 'Inter' … !important` on `body` and `.font-sans`. All four lines have been removed.
  - **Why:** third-party request on every cold page load (offline dev, GDPR, every visitor's IP logged with Google), plus an `!important` rule that fought app-level typography. There was no opt-out.
  - **Behaviour now:** the `<head>` ships only `<meta charset>` and `<meta viewport>`. Headings and body inherit the browser default (Times/Arial) until the app sets typography itself.
  - **Migration:** apps that want a webfont add it explicitly — e.g. a `Html.styleNode` in the view's head fragment, a self-hosted `@font-face` in a `Css.stylesheet`, or a `<link>` served from `Server.static`. Apps that were silently relying on the default Inter will look unstyled until they set their own font.
  - **Privacy/a11y wins:** no third-party network request from the runtime, and no `!important` override blocking accessibility-first apps that self-host (e.g. Atkinson Hyperlegible).
