# Learn Sky

Sky is a **pure-functional, Elm-family language that compiles to typed Go**. One
language for the whole stack — web apps, APIs, CLIs, TUIs, desktop — with a
single guiding promise: **if it compiles, it works.** No null, no user-written
FFI, no runtime panics from well-typed code.

This is the guided path. Read it top to bottom if Sky is new to you.

1. **[Coming from another language](learn-coming-from-other-languages.html)** —
   the mental-model shifts from JavaScript / Python / Go / Rust, side by side.
2. **[Your first app](getting-started.html)** — install, `sky init`, build, run.
3. **The language** — [syntax essentials](language-syntax.html) and
   [modules](language-modules.html).
4. **Build something real** — a web app with [Sky.Live](skylive-overview.html),
   data with [Std.Db](skydb-overview.html), UI with [Std.Ui](skyui-overview.html).
5. **[Using AI tools with Sky](learn-ai-tooling.html)** — Sky projects ship an
   `AGENTS.md` so Claude, Copilot, Cursor, etc. write correct Sky out of the box.

Everything here is verified: every full example in these pages is compiled in CI
(so it can't rot), and the [API reference](../index.html) for every module is
generated from the standard-library source on each build — always current.

## Why Sky

- **One language, whole stack.** The same view code renders on the web
  (Sky.Live), the terminal (Sky.Tui), and the desktop (Sky.Webview). No separate
  front-end language, no serialization glue.
- **Errors are values, effects are explicit.** Fallible things return
  `Result Error a`; side effects return `Task Error a`. The type tells you what
  can go wrong and what touches the outside world.
- **Batteries included.** Auth, DB (with a codec that drives JSON *and* the
  database from one definition), UI, HTTP, money/decimals, jobs, observability —
  all in the standard library, all reviewed for security and scale.
- **It compiles to Go.** You get Go's deployment story (a single static binary)
  and ecosystem (any Go package via FFI, no hand-written bindings).
