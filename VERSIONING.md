# Versioning & API Stability Policy

This document states, honestly, what Sky's version numbers mean today, what
guarantees you can rely on, and how APIs are deprecated and removed. It is the
authority the README status banner points at.

> **TL;DR.** Sky is **pre-1.0 (0.x)**. Minor version bumps **may include
> breaking changes**. Every breaking change is documented in
> [`CHANGELOG.md`](CHANGELOG.md) under a **`⚠ Breaking`** heading with a
> concrete migration, and surfaced on `sky upgrade`. Semantic-versioning
> guarantees (breaking changes only in a new major) begin at **1.0.0** — not
> before.

## Where the version lives

The single source of truth for "what version is this" is the **newest
`## vX.Y.Z` heading in [`CHANGELOG.md`](CHANGELOG.md)**. The README status
banner and `AGENTS.md`'s "Current line" are checked against it by a gate
(`rust/crates/xtask/tests/docs_state_the_current_version.rs`), so they cannot
silently drift. Current line at the time of writing: **v0.23.x**.

## Pre-1.0 (0.x) — the reality today

Sky has **not reached 1.0**. During the 0.x line:

- **A minor bump (`0.X` → `0.(X+1)`) MAY contain breaking changes.** It is not
  a promise of purely-additive change. The 0.x series is where the language,
  stdlib, and tooling are still being shaped toward the v1 design.
- **A patch bump (`0.X.Y` → `0.X.(Y+1)`) is intended to be additive and
  bug-fix only** — no source-breaking API changes. (A security fix that must
  change behaviour is the rare exception, and is always called out under
  `⚠ Breaking` / `Security` in the changelog.)
- **Every breaking change is documented and migratable.** It appears in
  [`CHANGELOG.md`](CHANGELOG.md) under a heading containing the word
  **"Breaking"** or **"Migration"**, with concrete, copy-pasteable migration
  steps. That heading is not decorative: `sky upgrade` prints the notes for
  every version between the user's binary and the one they move to, so the
  migration text is what a user reads the moment they upgrade
  (`sky upgrade --notes` previews without upgrading).
- **Recent breaking changes, for reference:** v0.19.0 (TEA config became a
  typed builder), v0.20.0 (import/`Path.join` shape), v0.20.3 (Sky.Live pool
  sizing), v0.21.0 (`[auth]` section removed; dev binds loopback), v0.22.0
  (Sky.Live config precedence now takes effect), v0.23.0 (secrets are a typed
  `Sky.Core.Secret`; the five per-shape app front doors deprecated). Each has
  its `⚠ Breaking` section in the changelog.

What this means in practice: **pin your Sky version** for a project and read
the `⚠ Breaking` notes before moving a minor. `sky upgrade` makes that easy;
it will not hide a breaking change from you.

## What is (and isn't) covered by the stability policy

**Public surface** — the APIs this policy governs:

- The Sky language surface (syntax + semantics).
- The stdlib public API as reported by `sky doc <Module>` (typed signatures +
  documented behaviour).
- The CLI verbs and their documented flags (`docs/tooling/cli.md`).
- `sky.toml` keys and the documented `SKY_*` / standard environment variables
  (`docs/sky-toml.md`).

**Not public surface** — may change at any time, minor or patch, without a
deprecation window:

- Compiler internals (crate layout under `rust/`, IR, lowering, the query DAG).
- The emitted Go and the Go runtime shape under `runtime-go/rt/` (an
  implementation detail of "compiles to typed Go").
- Anything documented as **experimental** (e.g. `Native.bridge` / the
  cross-platform native extension layer) — experimental APIs may change or be
  removed in any release, and say so at their definition.
- Undocumented behaviour, internal test/xtask gates, and file layout of build
  artefacts (`sky-out/`, `.split/`, `.skydeps/`, …).

## Post-1.0 — the contract that begins at 1.0.0

From the **1.0.0** release onward, Sky follows [Semantic
Versioning](https://semver.org):

- **MAJOR** (`X.0.0`) — may remove deprecated APIs and make breaking changes.
- **MINOR** (`1.Y.0`) — additive only: new APIs, no removals, no source-breaking
  changes to the public surface.
- **PATCH** (`1.Y.Z`) — bug fixes only, no API change.

At 1.0 a public API is **not** removed in the same major line it was deprecated
in. It is removed only in a subsequent **major** — never in a minor. Until 1.0,
the interim rule below applies instead.

## Deprecation policy

A deprecation is the mechanism for retiring a public API without breaking users
without warning. Whether pre- or post-1.0, a deprecated API:

1. **Keeps working** for at least the deprecation window (it still compiles and
   behaves as documented; `sky doc` marks it "deprecated").
2. **Names, in its deprecation notice, the version it was deprecated in and the
   earliest version it may be removed** — and that earliest-removal is **at
   least one minor later** than the deprecating version (post-1.0: at least the
   next major).
3. **Is listed in the [Deprecations](#deprecations) section below** and in the
   `⚠ Breaking` / deprecation notes of its changelog entry.
4. **Points at a migration** — the replacement API plus, for larger moves, a
   migration guide under `docs/` (e.g. `docs/security/secret-migration.md`) and
   a compiler hint that links to the public doc URL.

**Interim 0.x reality:** because a 0.x minor *may* break, a deprecation window
is a courtesy the project extends wherever practical rather than a hard SemVer
guarantee — but the commitment to *name the removal version* and *provide a
migration* holds regardless. When 0.x and post-1.0 rules disagree, the stricter
(post-1.0) contract is the goal we ratchet toward.

## Removal accounting inside the repo

Coverage and surface removals are mechanically accounted for, so a surface does
not quietly disappear:

- `docs/coverage/removals.toml` records every deliberate surface removal
  (`[[removal]]`) or weakening (`[[weakening]]`); `xtask denominators` /
  `xtask coverage-ledger --check` fail CI on an unaccounted decrease.

This is internal accounting, not a user-facing guarantee, but it is why a
removal cannot land silently.

## Migration guides & upgrade banners

- **Changelog first.** Every breaking change and deprecation is in
  [`CHANGELOG.md`](CHANGELOG.md); breaking sections carry copy-pasteable steps.
- **`sky upgrade` banners.** Upgrading prints the notes for every version
  jumped; `sky upgrade --notes` previews them. A changelog subsection whose
  heading contains "Breaking" or "Migration" becomes an upgrade banner.
- **Deep guides under `docs/`.** Larger migrations get a dedicated doc, e.g.
  [`docs/security/secret-migration.md`](docs/security/secret-migration.md).

---

## Deprecations

Public APIs that are deprecated but still shipping. Each names the version it was
deprecated in and the **proposed** earliest-removal version (proposals are for
the maintainer to confirm; nothing is removed before its listed version).

| API | Deprecated in | Replacement | Earliest removal (proposed) | Notes |
|---|---|---|---|---|
| `Std.Live`, `Std.Spa`, `Std.Tui`, `Std.Cli`, `Std.Webview` — the five per-shape app front doors, for **direct import** | v0.23.0 | `Std.App` (`App.app` / `App.web` / `App.cli` / `App.tui` + `App.run`, one `--target`) | **v0.25.0** *(proposal — maintainer to confirm; may instead be held until the first 1.0)* | They still compile; `sky doc` groups them under "deprecated — use Std.App". User code no longer needs to import them directly. See [`docs/skyapp/overview.md`](docs/skyapp/overview.md). |

### Completed breaking changes (not pending deprecations)

Changes that have already shipped and broken source — recorded here for
traceability, not awaiting removal:

| Change | Shipped in | What broke | Migration |
|---|---|---|---|
| Secrets are a typed `Sky.Core.Secret`, not `String` | v0.23.0 | Every secret-bearing stdlib argument (`Auth.signToken`/`verifyToken`/`signSlidingToken`, `Jwt.hs256`/`rs256`, `Crypto` AEAD keys, `Http.withBearer`/`withApiKey`, `Cli.readPassword`) changed signature; a committed string literal to any of these is now a compile error | Wrap at the boundary — `Secret.fromEnv "VAR"` / `Secret.fromString runtimeStr` — and unwrap only via `Secret.reveal`. Guide: [`docs/security/secret-migration.md`](docs/security/secret-migration.md) |

*(This section is a running index; each entry's authoritative detail lives in its
`CHANGELOG.md` `⚠ Breaking` section.)*
