# Roadmap: consolidate on `Std.App`, deprecate the per-shape front doors

> **Status: PLAN — nothing is deprecated yet.** This is the forward path we commit
> to *once* `Std.App` is complete and has rolled out successfully (the gate in §2).
> Until every box in §2 is checked, `Std.Live` / `Std.Spa` / `Std.Tui` / `Std.Cli`
> / `Std.Webview` remain fully supported public APIs. See
> `docs/design/unified-app-builder.md` (design) + `docs/skyapp/overview.md` (live
> API) for what `Std.App` is today.

## 1. Why

One front door instead of five. Today a user must *choose a framework* (`Live.app`
vs `Spa.app` vs `Tui.app` vs `Cli.program` vs `Webview.app`), each with its own
config shape, entry name, and view slot. That is surface area to learn, to
document, to keep consistent, and to keep from drifting. `Std.App` collapses it to
**one builder, one `main = App.run app`, one `--target`** — so:

- **Easy maintenance.** One config type, one set of `withX` builders, one view
  adapter layer. New capabilities and new targets are added in one place.
- **Everyone on the latest standard.** A single, current "this is how you build a
  Sky app" — no stale patterns, no "which module do I import?" fork. `sky init`,
  the docs, and the LSP all point at exactly one thing.
- **Correctness by construction.** The unified builder already enforces
  target-specific rules (e.g. `web` requires `withNotFound` at compile time) that a
  hand-written `Live.app` does not.

## 2. The gate — `Std.App` must be "done + rolled out" first

Deprecation of the per-shape front doors **does not begin** until ALL of these
hold. This protects existing apps (including live-traffic ones) from being nudged
onto an incomplete replacement.

- [ ] **Feature parity.** Every capability reachable via `Live.app` / `Spa.app` /
      `Tui.app` / `Cli.program` / `Webview.app` is reachable via `Std.App`
      (`App.app` + a `withX`), OR is explicitly documented as intentionally
      dropped. Known open item: **`App.withRequest`** (portable request access —
      needs a `Std.Live` runtime change; tracked in the unified-app-builder doc).
- [ ] **Every target verified end-to-end**, not just build-verified: `web`,
      `web:app`, `desktop` (the Live-in-a-window mode, run on a real display),
      `desktop:*` / `tablet:*` / `mobile:*` native, `terminal:tui|cli`.
- [ ] **A shipped release** whose `sky doc`, `sky --help`, `AGENTS.md`, and
      `docs/skyapp/` all present `Std.App` as *the* way to build an app.
- [ ] **`sky init` scaffolds `Std.App`** by default.
- [ ] **Real apps migrated and running on `Std.App`** — the repo examples, and at
      least one production app (e.g. sky-lang.org), rebuilt on `Std.App` with no
      regression. (darraghstudio migrates on the owner's schedule — live traffic.)
- [ ] **A mechanical migration path exists** (`sky migrate app` / a codemod, §5)
      that rewrites `Live.app`-style entries to `Std.App` and is proven on the
      examples.

Only when this list is green do we start §3.

## 3. Phased deprecation (after the gate)

The per-shape modules are **not deleted** — `Std.App` *composes* them (it calls
`Live.app`, `Spa.app`, … internally). Consolidation means demoting them from
**public app front doors** to **`Std.App` internals**. Phases are additive and
each is reversible until the last.

**Phase A — Default + lead (no warnings).** `sky init` defaults to `Std.App`; all
docs lead with `Std.App`; the per-shape module docs gain a "Prefer `Std.App`" note
with a one-line migration. Existing code is untouched and unwarned.

**Phase B — Soft deprecation (opt-in visibility).** Building an entry that uses a
per-shape front door directly (`main = Live.app …` etc.) emits a **non-fatal**
deprecation note: what to run to migrate (`sky migrate app <file>`), and the
`Std.App` equivalent. `sky doctor` lists per-shape entries as "migrate to
`Std.App`". Nothing breaks; the note is self-extinguishing once migrated.

**Phase C — Hard deprecation (warning-as-default).** The note becomes a standard
compiler **warning** on a per-shape app entry, with a `[allow]`/config escape hatch
for teams that need more time. The migration codemod is stable and documented. A
release's notes call it out.

**Phase D — Internalize the surface.** The per-shape `app`/`program` entry points
are moved behind an `internal`/`Sky.Internal.*` boundary (or marked
`@internal` so `sky doc` / the LSP no longer surface them as public), leaving
`Std.App` as the only *documented, completion-suggested* app builder. The modules
still exist and still power `Std.App`; they are simply no longer a public front
door. This is the **major-version** boundary.

**Phase E — Steady state.** `Std.App` is the sole public app builder.
`Std.Live`/`Spa`/`Tui`/`Cli`/`Webview` live on as `Std.App`'s composed backends,
maintained as internals. Raw `Std.Html` + `Std.Live` remain available for the
deliberate "I am hand-writing server-rendered HTML" case (documented as the
escape hatch, not the default).

> **Never a silent break.** No phase removes a working entry point without a
> release-noted warning window preceding it. An app that compiles today keeps
> compiling until at least a `[deprecated]` warning has shipped and a migration
> tool has been available for a full release cycle.

## 4. What stays vs what goes

| Surface | Fate |
|---|---|
| `App.app` / `App.run` / `App.withX` / the `--target` axis | **The** public app API |
| `Live.app` / `Spa.app` / `Tui.app` / `Cli.program` / `Webview.app` (as public *entry points*) | Deprecated → internalized (Phases C–D) |
| `Std.Live`/`Spa`/`Tui`/`Cli`/`Webview` **modules** + their kernels | **Kept** — `Std.App` composes them internally |
| `Std.Ui`, `Std.Html`, `Std.Db`, `Std.Auth`, `Std.Codec`, … | **Unaffected** — orthogonal to the app front door |
| Raw `Std.Html` + `Std.Live` for hand-written server HTML | **Kept** as the documented escape hatch |

## 5. Tooling — keeping everyone on the standard

- **`sky migrate app <file>`** (a codemod): rewrite `main = Live.app (Live.config
  { … })` → `app = App.app { … } |> App.withRoutes … |> App.withNotFound …` +
  `main = App.run app`; likewise for the other shapes. Proven on the repo examples
  before Phase B. Idempotent; prints a diff; never touches non-app code.
- **`sky doctor`**: flags per-shape app entries with the exact migrate command.
- **`sky init`**: `Std.App` scaffold is the default template.
- **Deprecation lint**: the Phase-B note → Phase-C warning, with `[allow]`.
- **Template + doc sync** (already a repo rule): `templates/CLAUDE.md` /
  `templates/AGENTS.md` and `docs/*` update in lockstep so AI agents and humans
  learn only the current standard.
- **`sky upgrade`**: on a major bump, point users at the migration guide.

## 6. Migration shape (illustrative)

```elm
-- BEFORE — a Sky.Live app (one of five front doors)
main =
    Live.app (Live.config { init = init, update = update, view = view
                          , subscriptions = subscriptions, routes = routes, notFound = NotFound })

-- AFTER — Std.App (the one front door)
app =
    App.app { init = init, update = update, view = view, subscriptions = subscriptions }
        |> App.withRoutes routes
        |> App.withNotFound NotFound

main =
    App.run app
```

The codemod does exactly this transform (and the analogous ones for Spa/Tui/Cli/
Webview), so migration is mechanical, not manual.

## 7. Risks + non-goals

- **Non-goal: deleting the kernels.** They are the substrate `Std.App` runs on.
  "Deprecate the modules" means *the public front doors*, not the runtimes.
- **Live-traffic apps migrate on their owners' schedule.** darraghstudio and any
  production deployment are never force-migrated; the warning window + codemod +
  the "compiles-today-compiles-tomorrow" guarantee exist precisely for them.
- **Escape hatch preserved.** A user hand-writing `Std.Html` for server-rendered
  web keeps `Std.Live` directly — consolidation is about the *default path*, not
  removing capability.
- **Don't start early.** Beginning deprecation before §2 is green would push users
  onto an incomplete replacement — the opposite of "everyone on the correct
  standard."

## 8. Success criteria

`sky init` → `Std.App`; the docs teach one app builder; `sky doc` surfaces one app
front door; the examples + a production app run on `Std.App`; a mechanical
migration exists and has been through a full deprecation cycle; and the per-shape
front doors are internal implementation detail. At that point there is exactly one
current, correct way to build a Sky app — which is the whole point.
