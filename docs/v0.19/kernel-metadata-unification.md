# v0.19.x — Kernel-metadata unification + builder cfg

**Status:** ACTIVE (autonomous mandate 2026-07-28). **Branch:**
`feat/std-analytics`. Ships together with the completed-and-held
`Std.Analytics` work as one v0.19.x release.

## Context (why)

Kernel/runtime functions had their Sky metadata split across **three
hand-maintained registries**, and the two *display* consumers read different
ones — so LSP hover and `sky doc` were two competing logics, drift-prone:

| Consumer | Read from | Failure mode |
|---|---|---|
| type-checker + **LSP hover** | `ty` `kernel_sigs: HashMap<(mod,fn), Scheme>` | missing → `uf.fresh_flex()` → renders as `?` |
| **`sky doc`** | `project` `kernel_api.rs` (sig string + doc + example) | separate table, drifts |
| names only | `hir` `KERNEL_FUNCTIONS` | — |

**The fix (user-directed):** make the `.sky` source file the SINGLE source of
truth. A kernel binding is just an `Ffi.kernel "Sym"` alias with a real HM sig +
`-- |` doc — the type-checker, LSP, and `sky doc` then all read the SAME `.sky`
file. This is the existing **Layer-3 pattern**; `Sky.Http.Server` (Server.sky)
is already fully migrated and is the living proof it works end-to-end.

## Inventory (what's left to migrate)

Already Layer-3 (done): `Sky.Http.Server` verbs (Server.sky), Uuid, Config,
Http, most stdlib. `Std.Webview` has a .sky file (Webview.sky) but `app` is a
row-open record literal.

Kernel-only, NOT yet `.sky`:

| Module | Bindings | Kind | Runtime syms |
|---|---|---|---|
| `Std.Jobs` | define, enqueue, enqueueIn, cancel | ordinary HM | `Jobs_define/enqueue/enqueueIn/cancel` |
| `Std.Live` | route, api, lifecycle | ordinary HM | `Live_route/api/lifecycle` |
| `Std.Live` | **app** | ROW-OPEN → Path A builder | `Live_app` (reflect-reads `Field(cfg,"Init"...)`) |
| `Std.Tui` | **app** | ROW-OPEN → Path A builder | `Tui_app` |
| `Std.Webview` | **app** | ROW-OPEN → Path A builder | `Webview_app` |

Types referenced by sigs: `Job a`, `JobId`, `Route`, `Element`, `Html`,
`Request`, `Response`, `Cmd`, `Sub`. `Route`/`Request`/`Response`/`Cmd`/`Sub`
are already in `KERNEL_IMPLICIT_TYPES`. `Job`/`JobId`/`Element`/`Html`/`KeyEvent`
need to be reachable from the new `.sky` files (add to `KERNEL_IMPLICIT_TYPES`
or declare in-module).

## Path A — typed-builder cfg (the row-open holdouts)

Record literal `Live.app { init, update, view, ... , head = f }` becomes:

```elm
Live.app
    (Live.config { init = init, update = update, view = view
                 , subscriptions = subscriptions, routes = routes, notFound = Home }
        |> Live.withHead headFor
        |> Live.withConsoleAuth authGate
        |> Live.withStatic "./public")
```

**Design decision (GRILL THIS): opaque runtime-built `AppConfig` (A2) vs
closed all-fields record (A1).**

- **A2 (leading candidate):** `AppConfig model msg` is an opaque kernel-implicit
  type. `Live_config` builds a runtime cfg object of the SAME shape `Live_app`
  already reflect-reads (`Field(cfg,"Init")` …), optionals nil. `Live_withHead`
  etc. set one field and return the object. **`Live_app`'s reflect-read stays
  byte-identical** — the whole optional-field-nil contract is preserved for
  free. New kernels: `config`, `withHead`, `withConsoleAuth`, `withStatic`,
  `withGuard`, `withOnNavigate`, `withAnalytics`, `withStatusStrings`.
- **A1:** closed record alias, optionals as `Maybe`. Forces `Live_app` to
  unwrap `SkyMaybe` on every optional read (runtime churn) and exposes the
  field spelling. Rejected unless grill finds A2 unsound.

**Soundness rule:** no kernel raw-asserts a Sky callback to a Go func type —
same defect class as the Db.withTransaction / config-decoder fixes this session.
`Live_config` stores callbacks as `any`; `Live_app` already invokes via the
existing dispatch. Verify the builder object flows through `Field()` (handles
map + struct).

## Phases

- **P0 — design grill** (architecture-consult + adversarial): validate A2 vs A1,
  the gate redesign, LSP path, resolution (Res::Def alias vs Res::Kernel),
  KERNEL_IMPLICIT_TYPES additions. Output: locked design. ← IN PROGRESS
- **P1 — expressible `.sky` migration (non-breaking):** create `Std/Jobs.sky` +
  `Std/Live.sky` (route/api/lifecycle) as Layer-3 aliases; remove those from
  `KERNEL_FUNCTIONS`/`kernel_api.rs`; add needed implicit types. Verify hover +
  `sky doc` + build/run a Jobs example. Commit.
- **P2 — Path A runtime + stdlib:** `Live.config`/`with*` kernels + `AppConfig`
  type + `Std/Live.sky` `app`/`config`/`with*` aliases. Same for `Tui`
  (`Std/Tui.sky`) + `Webview` (extend Webview.sky). Runtime `Live_config` etc.
  Commit.
- **P3 — migrate all call-sites:** every `examples/*` Live/Tui/Webview app +
  `sky-bundled/console` + `sky-lang.org`/`skydeploy` (downstream, separate) to
  the builder form. Full example sweep green. Commit.
- **P4 — delete `kernel_api.rs` + flip the gate:** gate = "every kernel-only
  module has a `.sky` file exposing its bindings; no kernel fn lacks a `.sky`
  decl." Remove the `kernel_sigs` kernel-only entries now sourced from `.sky`.
  LSP hover smoke: no `?` for any kernel fn. Commit.
- **P5 — docs + migration + README breakage note:** CLAUDE.md +
  templates/CLAUDE.md + docs/skylive + docs/skytui + docs/skywebview + README;
  `docs/v0.19/migration-builder-cfg.md`. Commit.
- **P6 — Judge verification:** fresh-context adversarial Judge vs the verbatim
  goal; full rt suite + xtask gates + sweep + verify scripts green.

## LOCKED design (P0 grill complete 2026-07-28 — wf_359ca9fc)

Grill verdict: `readyToImplement=true`, no blockers, floorTouch=false. Decisions:

- **A2 (opaque runtime-built `AppConfig`) — LOCKED.** `rt.Field()`
  (rt.go:5128-5179) has a `map[string]any` arm (exact-key hit 5153) that reads a
  builder-produced object byte-identically to today's lowered Sky-record struct;
  `liveAppRun` reads every field/optional through nil-guarded `Field()`
  (live.go:3450-3536), so an all-`any` map with the exact PascalCase keys flows
  through `Live_app` UNCHANGED. **Four MANDATORY runtime guards** (drift on any
  re-approaches the §8 floor): (1) build the object as `map[string]any` with the
  exact PascalCase keys `Live_app`'s `Field()` calls use, values typed `any` —
  never a concrete Go func/ptr type (unset optional MUST read as untyped nil so
  `!= nil` gates stay false); (2) `Live_withX(fn any, cfg any) any` STORE the
  callback verbatim — NEVER assert `fn.(func(any)any)` (the db_auth.go:1307
  defect class); invocation stays on existing `sky_call` dispatch (live.go:4119);
  (3) each `Live_withX` SHALLOW-CLONES the map before set (Go maps are refs —
  else siblings alias one base); (4) nested sub-records (withStatus/withAnalytics)
  are their own Field-readable `map[string]any`. Rt tests: `Field(obj,"Head")==nil`
  for unset optional; sibling-config isolation; func-stored-verbatim.

- **BREAK, not additive (diverge from grill).** Grill recommended keeping
  `Live.app` accepting the record literal (stays `fresh_flex`) — but that leaves
  `Live.app` itself at `?` on hover, FAILING goal outcome #3. User chose "builder
  cfg with breakage." So: `Live.app : AppConfig model msg -> Task Error ()` gets
  a PRECISE sig, record literal REMOVED, all call-sites migrated, breakage
  documented. Same for `Tui.app`. `Webview.app` is ALREADY a closed `AppCfg`
  record (no optional fields) → precise sig already possible → **out of Path A**.

- **ATOMICITY (my correction to grill's phase split).** Creating `Std/Live.sky`
  makes `Std.Live` a Dep module → `import Std.Live exposing (app)` fires E1011
  unless the `.sky` exposes `app`. So Live's `.sky` + the `app` builder land
  ATOMICALLY (one phase). `Std.Jobs` is standalone → safe to migrate first.

- **Resolution: nothing changes at call-sites.** `Live.route` as an `Ffi.kernel`
  alias resolves `Res::Def` (qual_vars shadows kernel_pseudo, resolve.rs:1910
  before 1928) and lowers to the identical Go symbol (lower.rs:918,2872-2891) —
  proven by `Server.get` today. Removing from `KERNEL_FUNCTIONS` changes neither
  resolution nor emitted Go. No `route` value / `Route` type collision (separate
  namespaces, resolve.rs:494).

- **LSP auto-fix CONFIRMED.** `Res::Def` hover renders the declared `.sky` sig
  via `def_sig_string`→`declared_anno_text` (lib.rs:428) — migration auto-fixes
  hover, eliminates the `Res::Kernel`→`?` fallback (lib.rs:387). Residual: add an
  LSP startup assertion that the embedded stdlib loaded (else blank hovers).

- **Gate: new xtask `kernel-surface`** driven by a canonical
  `KERNEL_SURFACE: &[(module, &[bindings])]`. Leg1: parse each `.sky` +
  `hir::compute_exports`, assert every listed binding is an exported value.
  Leg2: scan every stdlib `.sky` for `Ffi.kernel "Sym"`, assert
  `runtime-go/rt` defines `func <Sym>(`. Leg3: example-sweep backstop. Delete the
  vacuous `kernel_api_covers_registered_kernel_functions`; strip migrated entries
  from `kernel_api.rs` (also fixes a live doc double-render: Server `redirect`
  says 302 in .sky vs 303 in kernel_api.rs:217); rewrite doc.rs:754-762 +
  796-812 to assert `.sky`-source rendering; add reverse no-duplicate-name check.

- **Implicit types:** add `AppConfig` (phantom-parametric opaque; MANDATORY
  explicit `("AppConfig",_) => GoTy::Any` arm in lower/goty.rs — the `_=>`
  fallthrough does NOT default con:None to Any), `Element`, `Html`, `KeyEvent` to
  `KERNEL_IMPLICIT_TYPES`. Declare `type Job a` + `type JobId` IN-MODULE in
  Std/Jobs.sky (not implicit). Route/Request/Response/Cmd/Sub already implicit.

- **Also in scope (grill completeness):** `Std.Cli.program`, `Std.Tui.program`
  also lack `.sky` — fold into the migration. Vendored `sky-tailwind`
  (ex13/.skydeps) migrates upstream separately. Downstream skydeploy +
  sky-lang.org migrate post-release (P8, separate PRs, then SKY_VERSION bump).

## P-Tui/Cli DONE 2026-07-28

Std/Tui.sky (app/program/config/withOnKey/withGuard/withCanvasWidth/
withCanvasHeight) + Std/Cli.sky (program/config/withOnLine/readPassword) landed
as Layer-3 builders; runtime cli_config.go added (tui_config.go was P-Tui-runtime
271b3adb). `config`'s `view` is POLYMORPHIC (`model -> render`) so it fits both
`Tui.app`'s `Element` and `Tui.program`/`Cli.program`'s `String` views. Stripped
Std.Tui from kernel_api; rewrote the 2 doc tests to assert `.sky`-source
rendering (`migrated_kernel_module_renders_full_sigs_from_sky_source` reads
Std/{Tui,Live,Cli,Jobs}.sky + asserts real sigs, no `?`); fixed the issue164
typecheck_gate fixture to the builder form. Migrated 8 call-sites (20 Cli, 21
Tui.program, 22/23/24/38 Tui.app, bundled console+doc MainTui). VERIFIED: sweep
29/0; post-fmt stdlib builds; `sky doc Std.Tui`/`Std.Cli` render builder API from
source (no `?`); project tests green; coerce-floor PASS (no widening). Now only
Webview.app remains on a record — but it's a CLOSED AppCfg (no optionals) with a
precise sig already, so it's OUT of Path A by design.

## Revised phases (BREAK + atomicity)

- **P1 — Std.Jobs → Std/Jobs.sky** ✅ DONE + verified (commit 6f382eda).
  `sky doc Std.Jobs` renders from source; e2e fixture builds+runs (`enqueued ok`,
  2 kernel calls in codegen); phantom `a` constrains (`enqueue greet 42` →
  `E2001 record vs Int`); doc gate + hir resolve green; zero example blast radius.
  Proven: opaque-kernel-value flows as `any` end-to-end (runtime mechanism for
  the Path A configs). NOTE: `Live_config` returns `map[string]any` typed as an
  OPAQUE config type → must flow as `any` (KERNEL_IMPLICIT_TYPES + `GoTy::Any`
  arm, OR in-module opaque decl like `type Job a` which proved to flow as any).
- **P2 — Path A runtime kernels + `AppConfig` type**: `Live_config`/`Live_withX`
  + `Tui_config`/`Tui_withX` (4 guards); AppConfig implicit + goty.rs arm; rt
  tests. Commit.
- **P3 — Std/Live.sky + Std/Tui.sky (ATOMIC)**: app (precise `AppConfig` sig) +
  config + withX + route/api/lifecycle (Live) / onKey builder (Tui); remove from
  KERNEL_FUNCTIONS + kernel_api; add implicit types; sky fmt ×2. Std/Cli.sky
  (program). Commit.
  **DESIGN VALIDATED 2026-07-28** via the throwaway `Std.LiveBuilder` probe:
  the full `config {...} |> withHead |> withGuard |> app` chain BUILDS + RUNS +
  serves HTTP 200, `<head>` renders, and omitted optionals do NOT crash (guard 1
  holds). Confirmed: `config` sig accepts the examples' `init : a -> ...` +
  `view : model -> Html msg`; in-module `type AppConfig model msg = AppConfig_OPAQUE`
  flows as `any` (no goty.rs arm / KERNEL_IMPLICIT_TYPES needed — same as `Job`).
  **TURNKEY P3 EXECUTION:**
  1. `mv docs/v0.19/staging-Std-Live.sky sky-stdlib/Std/Live.sky` (validated full
     content — all withX + route/api/lifecycle; `Html` via `import Std.Html
     exposing (Html)`).
  2. Remove `("Live", &["app","route","api","lifecycle"])` from KERNEL_FUNCTIONS
     (kernel.rs) + strip the `Std.Live` kernel_api.rs entry (mirror commit
     6f382eda's Jobs style). Rebuild `cargo build -p sky`.
  3. Migrate ALL Live call-sites (atomic — tree won't build until done). The 18
     Live SPAs use `import Std.Live exposing (app, route)` + bare `app { init=…,
     …optionals… }`; rewrite each to `app (config { <required 6> } |> withHead …
     |> withGuard … )`, moving ONLY optionals (head/guard/analytics/status/store
     — the used set) into `withX`, and extend each `exposing (...)` with `config`
     + the `withX` used. Multi-backend ex 24/38: migrate their `Live.app` record;
     Tui.app there is still kernel-only (fresh_flex) so its record still builds
     until P-Tui. Bundled console (sky-bundled/console/src/Main.sky) too.
  4. `sky fmt` ×2 each; full `scripts/example-sweep.sh` green; commit.
  Runtime kernels already shipped (P2, commit 88a9dde0) + guard-tested.
  **P3 DONE 2026-07-28.** Std/Live.sky in place; app precise sig; removed from
  KERNEL_FUNCTIONS + kernel_api (+ repointed 2 doc tests to Std.Tui). Migrated 20
  call-sites (16 required-only via perl wrap; 37 head / 52 analytics / 13 guard;
  24/26/38 qualified) + bundled console + heap-bound fixture. VERIFIED: full
  example-sweep 29/0; 5 sweep-uncovered examples + bundled console clean;
  coerce-floor PASS (builder widened NOTHING — 37=616/38=465/etc. unchanged; only
  blessed the new 52-blog-analytics); doc/hir/ty tests green; `sky doc Std.Live`
  renders builder API from source, no `?`. NOTE for P7: regenerate the embedded
  console (scripts/regenerate-console.sh) so runtime-go/rt/console_app matches the
  migrated sky-bundled/console source.
- **P4 — migrate all ~25-30 call-sites** to builder form (both `exposing (app)`
  bare + qualified `Live.app`/`Tui.app`); add fixtures for the zero-coverage
  optionals (consoleAuth/onNavigate/static/api/status); re-bless coerce_floor
  golden with per-example justification; full example-sweep green. Commit.
- **P5 — gate flip + doc-crate**: land `kernel-surface`; delete
  `kernel_api_covers…`; strip migrated kernel_api entries; rewrite doc tests;
  remove migrated modules from KERNEL_MODULES (close E1011 typo hole, guarded).
  Commit.
- **P6 — docs + README breakage migration**: CLAUDE.md + templates + docs/skylive
  + docs/skytui + README + `docs/v0.19/migration-builder-cfg.md`. Commit.
- **P7 — milestone verify + Judge**: cargo test --workspace + all xtask gates +
  full sweep + verify-cli + verify-all-web; fresh-context adversarial Judge.
- **P8 — downstream (separate)**: skydeploy + sky-lang.org + sky-tailwind.

## Original open questions (answered above by grill)

1. A2 vs A1 — is the opaque runtime-built object sound + does it keep
   `Live_app` byte-identical? Does `Field()` read the builder object?
2. Resolution: when `Live.route` becomes a `.sky` alias, does removing it from
   `KERNEL_FUNCTIONS` change nothing at call-sites (Res::Def → kernel_alias
   lowering)? Any collision with the kernel-implicit `Route` type?
3. Gate redesign: what exactly enforces "no kernel fn without a `.sky` decl"
   once `KERNEL_FUNCTIONS` no longer lists them?
4. Does the LSP already hover-render a Layer-3 alias sig (Server.get) correctly
   today? (If yes, migration auto-fixes hover — confirm.)
5. `sky fmt` idempotency on the new `.sky` files; `Ffi` import present.
6. Any example whose behaviour depends on the record-literal cfg shape beyond
   the documented optional fields?

## Verify commands (per CLAUDE.md release checklist)

- narrow: `cargo test -p <crate> <name>`; `sky doc Std.Live`; LSP hover smoke
- phase close: `cargo test --workspace` + `scripts/example-sweep.sh` (FULL) +
  build/run a migrated app
- milestone: xtask gates (roundtrip/resolve/infer/reject/build-run/coerce-floor/
  repro/golden) + `scripts/verify-cli.sh` + `scripts/verify-all-web.sh`

## Residual outcomes (2026-07-28)

- **kernel_api.rs DELETED (P5-follow, 054f6d26).** The last entry
  (Sky.Http.Server) fully duplicated Server.sky (which already declares every
  verb as an Ffi.kernel alias + 9 more) and was the source of a redirect
  302-vs-303 doc contradiction. Now there is genuinely ONE source (the .sky
  file) for EVERY stdlib module — kernel-only AND dual. `render_module` renders
  from .sky alone.
- **Runtime verified:** `verify-cli.sh` 13/0 (migrated Cli/Tui 20/21/22/23/24 run
  no-panic); `verify-all-web.sh` 10/0 Live/Server + console-e2e PASS.
- **Full `cargo test --workspace` green (exit 0);** example-sweep 29/0 (×2);
  coerce-floor PASS (×2, builder widened nothing); kernel-surface gate green.

### Known PRE-EXISTING issue (NOT v0.19 — separate follow-up)

`verify-ui-showcase.sh` fails one snapshot: `hover-button-state-desktop`
(99.58% pixel diff, CONSISTENT across re-runs). PROVEN unrelated to v0.19: the
only diff to `examples/26-ui-showcase/src` is the entry-point builder migration
(0 rendering-affecting lines), the golden was last recorded at v0.16.11
(`4ee869f4`), and all 30+ other ui-showcase snapshots are byte-identical
(0.00% diff). A pre-v0.19 change to `:hover` rendering (Std.Ui hoverColor CSS
emission, or a headless-browser bump) drifted the hover state without
re-recording the golden. **Fix separately** — investigate whether the current
hover render is correct (then re-bless) or a real regression (then fix); do NOT
blind-rebless, as that could hide a real `:hover` bug. Not a v0.19 blocker.

### Still open

- **Independent adversarial Judge** — could not run (subagent budget 200/200
  exhausted). Multi-gate verification stands in; a fresh session can run the Judge.
- **Downstream (P8)** — skydeploy + sky-lang.org need the same mechanical builder
  migration (separate PRs, post Sky-release + SKY_VERSION bump).
