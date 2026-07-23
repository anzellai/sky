# v1 Discovery Sweep — CLOSE-NOW queue (PR #154)

From the 17-agent discovery+grill sweep (2026-07-22). 14 grill-confirmed,
self-contained, achievable-in-patch items. **Bold = `check ≡ build` violation**
(highest v1 value). Work in ROI order; verify each repro before fixing.

| # | Item | Eff | Status |
|---|------|-----|--------|
| 1 | String.repeat negative → "" | S | ✅ |
| 2 | sky doctor auth-secret skips commented headers | S | ✅ |
| 3 | sky test uses assets_root_for (standalone works) | S | ✅ |
| 4 | arity-0 CAF call-site no longer double-forces | S | ✅ |
| 5 | call-arg type mismatch anchored at whole call, not the arg (infer.rs:587) | S | ⬜ (subsumed by B4 diag agent) |
| 6 | E1001 shows line/caret/excerpt (+drop redundant reason) | S | ✅ |
| 7 | pattern-ctor recorded as ref (hover/goto/refs/rename) | S | ✅ |
| 8 | `List.sum/product/maximum/minimum` missing from stdlib | S/M | ⬜ (see reclassification below) |
| 9 | Basics.abs/negate/sqrt repointed to rt.Math_*/rt.Negate | S | ✅ |
| 10 | `String.left/right`, `List.sort*/filterMap` exist but not exposed | S | ✅ (already resolve+build clean via kernel.rs — verified 2026-07-23; `exposing` list is cosmetic, kernel names are globally resolvable) |
| 11 | **Char literals lowered as Go strings → build-fail** (lower.rs) | M | ✅ (2026-07-23 — lower to `rune(<cp>)`; regression `driver_char_pattern_go_builds`) |
| 12 | **`let` forward refs → non-compiling Go** (lower.rs topo-sort) | M | ✅ (2026-07-23 — `collect_local_refs` + `order_let_defs`; regression `driver_let_forward_ref_go_builds`) |
| 13 | **entry module hardcoded to `Main`** — sky.toml entry/CLI file arg ignored (build.rs:188/348) | M | ✅ (2026-07-23 — `BuildOptions.entry_module` derived from the file header; regression `driver_honours_non_main_entry_module`) |
| 14 | typo of kernel member → E4005 "please report" not name error | S/M | ✅ (2026-07-23 — reframed at ABI-guard emit site w/ did-you-mean, since HIR's KERNEL_FUNCTIONS is an incomplete subset; regressions in `abi_guard`) |

**Ordering constraint (revised):** #10 is already closed (kernel-anchored). #8
is reclassified below (not a quick add). #14 stands alone now.

## #8 reclassification (2026-07-23) — NOT a quick stdlib add

`List.maximum/minimum/sum/product` are genuinely absent from BOTH compilers
(shared `sky-stdlib/`). Adding them properly is a distinct workstream, not a
one-line exposing-list edit, because:

- They need a **runtime Go impl** (`rt.List_maximum` …) + a **kernel.rs** entry
  (lower) + optionally a **sig.rs** HM type — the `List.sort` pattern.
- `maximum`/`minimum` are polymorphic via the runtime `compare` (clean).
- **`sum`/`product` need a `number`-polymorphism decision** tied to Limitation
  #1 (no typeclasses). `+` is number-polymorphic across *separately annotated*
  sites (verified: `sumI : List Int` and `sumF : List Float` both build), but a
  SINGLE polymorphic `sum : List number -> number` def is not expressible in
  Sky's HM without a `number` pseudo-class. Shipping a monomorphic `sum : List
  Int -> Int` would be asymmetric/surprising.
- Adding to the Rust compiler only creates a **Rust-ahead-of-oracle** divergence
  (differential sanity-gate flags it) until the oracle's kernel registry matches.

→ Track as a dedicated stdlib-expansion item (runtime + both-compiler kernel +
number-polymorphism design). Not shipped in the PR #154 CLI/DX sweep.

Deferred: `toString` custom-ADT constructor-name rendering (needs codegen tag→name
table threaded to runtime — scoped follow-on). Composite-shape stringify half is
close-able (STRETCH).

## Surfaced during #4 (new, separate — kernel partial application)

- **Partial application of a KERNEL emits an under-applied call** (check≡build).
  `let g = String.append "hi " in g "bob"` → emitted `rt.String_append("hi ")`
  (1 arg to a 2-arg kernel) → `go build: not enough arguments in call to
  rt.String_append`. Distinct from #4 (which was the arity-0 CAF call-site
  double-force, now fixed + verified via a user-fn point-free = 42). A kernel
  under-application should emit a partial closure. Effort M.
  **CLOSED 2026-07-23 (runtime-arity approach)** — a first attempt keyed the
  eta-expander on `count_arrows` of the callee's curried HM type and regressed
  `examples/36-composite-server`: `Middleware.withCors : List String -> Handler
  -> Handler` where `Handler = Request -> Task` unfolds to 3 spine arrows, so a
  FULL 2-arg application was mis-eta-expanded into a 3-arg over-application. The
  HM arrow count is fundamentally insufficient (a "2-arg function returning a
  function" is indistinguishable from a "3-arg function" once alias-unfolded).
  The shipped fix uses the kernel's TRUE **runtime param count**:
  `abi_guard::runtime_arities()` scans the runtime `func` signatures (depth-aware
  param counter, cached), threaded through `LowerConfig.kernel_arity` into the
  lowerer; `lower_call` eta-expands iff `runtime_arity > given`. Single source of
  truth, no hand-maintained table. Regressions
  `driver_kernel_partial_application_go_builds` +
  `abi_guard::func_arity_counts_params_robustly`; 50/50 sweep green.

## Status after the compiler-sweep thrust (2026-07-23)

All CLOSE-NOW compiler items are shipped: three check≡build violations (#11
char literals, #12 let forward refs, kernel partial-application) plus #13 entry
module and #14 diagnostic, each with driver-level regressions and green
reject/infer/roundtrip/golden/build-run gates + a full 50/50 non-skyshop
example sweep (skyshop verified separately). Remaining known non-close-now
work: #8 stdlib expansion (runtime + both-compiler kernel + number-polymorphism),
B2 incremental short-circuit, A2/A3 deprecated-console product question.

## CLI-completeness scan (2026-07-23) — `sky` verbs vs Haskell oracle

Full agent audit of every `sky` verb. Only `upgrade` was genuinely stubbed;
the dominant gap was build-pipeline logging. Closed this session:

| Item | What | Status |
|------|------|--------|
| A1 | `sky upgrade` self-update wired (GitHub releases → download → atomic replace; dev refuses w/o `--force`) | ✅ |
| B1 | build/run/check phased progress log (Discovering/Parsing/Canonicalising/Type Checking/Generating Go/Sky lowering succeeded) via `BuildOptions.progress` | ✅ |
| B3 | `sky update` tidies `sky-out/go.mod` instead of bare no-op when no surfaces | ✅ |
| B5 | `sky doc --list` grouped under project/stdlib headers | ✅ |
| C1 | `sky watch` honours `--clear`/`--debounce`/`--interval`/`--kill-timeout`/`--watch=PATH` (were silently ignored) | ✅ |
| C2 | `sky clean` removes `.skydeps` too | ✅ |
| C3 | `sky init` scaffolds the helpful commented sky.toml keys | ✅ |
| C4 | `Build complete:` path relative not absolute | ✅ |
| B4 | type-error diagnostic: RHS-anchored span + filename + source-context window + `TYPE ERROR` header | ✅ (reject 59/59, infer 49/49) |
| #5 | call-arg mismatch span (subsumed by B4 — app-arg spans now filename+context) | ✅ |

Remaining scan items (lower priority, not check≡build violations):
- **B2** — CLI-level incremental short-circuit / `-- Incremental: source unchanged`
  message (salsa exists in LSP; the `build` CLI path shows no source-hash reuse). M.
- **B7** — `sky doctor` trigger heuristics differ (Rust's `auth-secret` more precise,
  no `check-smoke` info note). Low.
- **B8** — `sky add` on-disk FFI layout (`sky-ffi/` vs oracle `ffi/`) + binding
  enumeration output differ. Low; Rust self-consistent.
- **A2/A3** — `sky console` inline app is a legacy-HTML fallback; `console --tui`
  builds a STALE bundled `sky-bundled/console/src/MainTui.sky` that fails E2001 on
  BOTH compilers (out of sync with `State.sky`'s `Model` — a bundled-source bug,
  not a Rust regression). The oracle dodges it only because `console`/`console --tui`
  are deprecated (print guidance, never build). Fix = repair MainTui.sky OR make
  Rust match the deprecation notice. M/L. Not a Rust compiler defect.
