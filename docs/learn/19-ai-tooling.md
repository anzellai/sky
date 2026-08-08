# Using AI tools with Sky

Sky is designed to be written *with* an AI assistant. Because the language is
small, explicit, and "if it compiles it works", a model that follows a few house
rules produces correct, production-shaped Sky the first time.

## Every Sky project ships an `AGENTS.md`

`sky init` scaffolds two files into your project:

- **`AGENTS.md`** — the agent-agnostic coding guide (works with Claude, Copilot,
  Cursor, Codex, …). It's the source of truth: the language, the app-building
  decisions, the preferred defaults, and the non-negotiable rules.
- **`CLAUDE.md`** — a thin entry point that imports `AGENTS.md` (`@AGENTS.md`) so
  Claude Code picks it up automatically.

Most editors' AI features read `AGENTS.md` (or `CLAUDE.md`) from the repo root
automatically, so your assistant starts already knowing how to write Sky.

Keep it current with `sky upgrade-claude` — it refreshes both files from the
installed compiler's template.

## What the guide teaches your assistant

- **Interview first.** A good Sky assistant asks *what you're building* and *what
  tier* it is (a weekend prototype vs a production service) before writing code —
  because the tier drives the architecture (SQLite + memory sessions for a pet
  project; Postgres + a shared session store + auth for production).
- **Preferred defaults.** `Std.Ui` (cross-platform) over raw HTML; `Std.Db.Store`
  + `Std.Codec` for data; internal `Std.Auth`; `Result Error`/`Task Error` (never
  `String`) for errors; `Std.Money` (never `Float`) for currency.
- **The live API.** For any module, `sky doc <Module>` prints the current typed
  signatures — the same content as the [API reference](../reference.html) here,
  generated from source, so the assistant never works from a stale table.

## A good prompt

> "Build me a small internal tool to track team book recommendations. It's for
> ~10 people on one VM; losing data on restart is not OK."

A Sky-aware assistant hears *internal / small / must survive restart* and reaches
for Sky.Live + SQLite + `Std.Db.Store`, a memory-or-sqlite session store, and a
single deployable binary — not Postgres, Redis, and Kubernetes. That's the point:
the defaults are right, so you describe the problem and get production-shaped Sky.
