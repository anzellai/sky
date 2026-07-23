# Discovery sweep — real-world app surface (2026-07-23)

A fresh 5-prober + adversarial-grill workflow probed the compiler + shared
runtime from the perspective of building real apps (HTTP APIs, JSON pipelines,
error handling, sky.toml config). 16 grill-confirmed findings; the highest-value
ones closed this session with REAL repros. Runtime fixes land in the SHARED
`runtime-go/rt` so both the Rust compiler and the Haskell oracle benefit — the
differential byte-match is preserved (both sides move together).

## Closed

| Finding | Class | Fix | Regression |
|---------|-------|-----|-----------|
| **`Sky.Http.Server` `:param` routes never matched** — `Server.get "/users/:id"` registered `/users/:id` verbatim on Go's ServeMux (literal `:id`), so `/users/42` 404'd and `Server.param` always returned `Nothing` | **major, app-shape** (documented core feature non-functional) | `colonToMuxPattern` translates `:name` → Go 1.22+ `{name}` at registration; handler populates `Params` via `req.PathValue`. Single + multi-segment + mixed literal/param verified e2e | `TestColonToMuxPattern` + `TestServerParamRouteEndToEnd` |
| **Prelude `min`/`max`/`compare` type-checked but failed `go build`** (`min`/`max` had no lower mapping; `compare` → non-existent `rt.Basics_compare`) | **`check ≡ build`** | `kernel.rs` maps `Basics.min`/`max` → `rt.Math_min`/`max`; added `rt.Basics_compare` any-dispatch shim (reuses shared `cmp`) | `driver_prelude_min_max_compare_build_and_run` (build+run) |
| `Json.Decode.int` silently TRUNCATED fractional numbers (`3.5` → `3`) | correctness (data-corruption) | reject a fractional number (Elm semantics); integral floats (`3.0`, `1e2`) still decode | `TestJsonDecIntRejectsFractional` |
| `Json.Encode.object` REORDERED keys alphabetically (Go marshals maps sorted) | correctness (breaks signing/snapshots) | `jsonOrderedObject` (json.Marshaler) preserves insertion order | `TestJsonEncObjectPreservesOrder` |
| `errorToString`/`toString` DUMPED the raw Go struct for the Error ADT (`{0 Error [10 {boom <nil>}]}`) | DX (ubiquitous) | `renderSkyError` mirrors `Error.toString` → `"<KindLabel>: <message>"` | `TestErrorToStringRendersAdt` |
| `[log]` sky.toml default logged plain (rt's `logJSON`/`logThreshold` cached at package-init, before the app's `init()` seeds) | config | `SetSkyDefault` re-fires `envPrefixHooks` (same as `SetEnvPrefix`) | `TestSetSkyDefaultResyncsLogConfig` |
| `[database] url` ignored (only `path` recognised) | config | `url` aliases `path` → both seed `DB_PATH` (detectDriver routes postgres DSN) | `database_url_is_an_alias_for_path` |
| Type-mismatch messages TRUNCATED parametric types to the head constructor (`Maybe` vs `String`, hiding `Maybe Int`) | DX | `unify.rs` `describe_flat`/`describe_var` walk the union-find and render full applications (`Maybe Int`, nested `List (Maybe Int)` parenthesised) at both App-mismatch arms | `mismatch_message_keeps_type_arguments` |
| A **stdlib module typo** (`import Std.Lst`) was diagnosed as a missing Go-FFI package with a "run `sky install`" hint (which can never fetch a Sky module) | DX (misleading) | `lower.rs` — a `Std.*`/`Sky.*`-namespaced package reaching the Foreign fallthrough is an unknown Sky module (real Go FFI packages are never Sky-namespaced); emit "unknown Sky module — check spelling" instead | `driver_stdlib_module_typo_is_not_an_ffi_install_hint` |

## Open (tracked — fix sites identified, not yet closed)

### Config (see also sky-toml-config-sweep-2026-07-23.md §Follow-up)
- **`bin` output name** — hardcoded `app` across ~7 build/run sites; needs a
  single-pass `bin_name` thread through `BuildOptions` + `go build -o` + every
  run `Command::new("./app")`. Low real-world impact.
- **`[source] root`** — discovery hardcodes `join("src")` (build.rs:146/1026);
  requires reading sky.toml before discovery.
- **`[auth]` runtime consumer** — `AUTH_*` env seeds land, but the runtime auth
  layer doesn't yet READ them at each site. Deeper runtime work.

### Determinism
- **`Set.toList`/`union`/`intersect`/`diff` non-deterministic ordering** —
  backed by a Go map; iteration order varies run-to-run. Elm's `Set` is ordered
  (sorted). Fix: sort on `toList` (and the set-algebra outputs) via the shared
  `cmp`. Medium; touches `Set_*` in the runtime — verify no golden depends on
  the current arbitrary order first.

### Policy question (NOT a differential — needs user decision)
- `exposing` a **non-exported** name reports "Types OK" (e.g. `import Helper
  exposing (privateFn)` where `Helper` only exposes `publicFn`). **Verified
  2026-07-23: the oracle ALSO accepts this** — it's a SHARED leniency (same
  family as #576's kernel-implicit exposing acceptance), not a Rust bug.
  Enforcing Elm-strict export lists is a language-semantics decision that
  could break existing user code relying on the leniency; tightening Rust
  alone would break the differential byte-match. Escalate to the user before
  spending iterations.

### Diagnostics (DX)
- `Server.post` JSON endpoints return **403 CSRF by default** to machine
  clients (curl/fetch without the cookie+token) — correct-by-design but
  **undocumented**; at minimum a docs note + a clear 403 body naming the CSRF
  requirement + how to exempt an API route.
- (Future) The stdlib-typo diagnostic now says "unknown Sky module"; a
  did-you-mean against the bundled module set (`Std.Lst` → `Std.List`) would
  need the module registry threaded into `lower` — tracked as an enhancement.

## Notes
- Both compilers share `runtime-go/rt`, so the JSON/Error/`:param` fixes are
  correctness improvements for the oracle too — no differential drift.
- `Json.Decode.int` integral-float acceptance (`3.0`, `1e2`) matches Elm: JSON
  has no int/float distinction, so an integer literal arrives as `float64`.
