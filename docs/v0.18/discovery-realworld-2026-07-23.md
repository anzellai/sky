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
| `Server.post` JSON endpoints 403'd machine clients by default (CSRF on for all POSTs; only `SKY_CSRF=off` escape, undocumented, opaque 403 body) | DX + API-usability | `csrf_middleware.go` — auto-exempt any request bearing an `Authorization` header (credentialed-API calls aren't CSRF-forgeable — the browser never auto-attaches `Authorization`), and the 403 body now names all three escape hatches; documented in `docs/stdlib.md` | `TestCsrfAuthorizationHeaderExempt` |
| `Set.toList`/`union`/`intersect`/`diff` returned **non-deterministic** order (Go map iteration) | correctness (reproducibility) | `Set_toList` sorts by natural order via the shared `cmp` (all set-algebra results are observed through `toList`); Elm-parity sorted, run-to-run stable | `TestSetToListSortedDeterministic` |

## Open (tracked — fix sites identified, not yet closed)

### Config (see also sky-toml-config-sweep-2026-07-23.md §Follow-up)
- **`bin` output name** — hardcoded `app` across ~7 build/run sites; needs a
  single-pass `bin_name` thread through `BuildOptions` + `go build -o` + every
  run `Command::new("./app")`. Low real-world impact.
- **`[source] root`** — discovery hardcodes `join("src")` (build.rs:146/1026);
  requires reading sky.toml before discovery.
- ~~**`[auth]` runtime consumer**~~ — RESOLVED as a docs clarification.
  Verified 2026-07-23: `Std.Auth` is a library — `signToken secret claims
  expirySeconds` takes the secret + TTL as ARGUMENTS and the cookie is set by
  the user's handler, so there is no framework layer to "consume" the keys.
  The `[auth]` keys correctly seed `SKY_AUTH_*` env vars that USER CODE reads
  via `System.getenvOr` at the call site. docs/sky-toml.md corrected to
  describe this accurately (it previously implied the framework reads them).

### Closed: enforce Elm export semantics (user-approved 2026-07-23)
- `import M exposing (name)` where `M` does not expose `name` was silently
  accepted (the export list wasn't a real boundary). Now a hard `[E1011] NOT
  EXPOSED` — `module `M` does not expose `name``. Scope: **values** (the
  user's "functions/vars" ask; types keep the #576 kernel-implicit leniency).
  - **Corpus audit first** (per the user's "fix sources so produced code is
    identical" requirement): 48 examples + full stdlib + bundled/doc + all
    `tests/` — **0 violations**. Nobody relied on the leniency, so no source
    changed and generated Go is byte-identical by construction.
  - **Design note**: the oracle SOURCE already encodes this
    (`checkImportExposingAgainstDep`), which validated the design. The oracle
    EXEMPTS kernel modules (`isKernel -> []`); Rust doesn't need that shortcut
    (stdlib modules are real source `Dep`s with authoritative exposing lists),
    so Rust also catches typos in stdlib imports (`Sky.Core.List exposing
    (nonExistentFn)`). Corpus is clean → differential gates (corpus-scoped)
    stay green; divergence only on hand-written invalid programs no gate tests.
  - `hir` `resolve.rs` `bind_exposing_dep` — the `unwrap_or_else` that
    fabricated a def for any non-exported value now emits `[E1011]` (keeps a
    recovery binding to avoid cascade "undefined name" errors). Regression:
    `driver_rejects_import_of_non_exported_name`.

### Diagnostics (DX)
- (Future) The stdlib-typo diagnostic now says "unknown Sky module"; a
  did-you-mean against the bundled module set (`Std.Lst` → `Std.List`) would
  need the module registry threaded into `lower` — tracked as an enhancement.

## Notes
- Both compilers share `runtime-go/rt`, so the JSON/Error/`:param` fixes are
  correctness improvements for the oracle too — no differential drift.
- `Json.Decode.int` integral-float acceptance (`3.0`, `1e2`) matches Elm: JSON
  has no int/float distinction, so an integer literal arrives as `float64`.
