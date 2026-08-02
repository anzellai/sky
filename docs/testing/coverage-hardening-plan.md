# Test Coverage Hardening Plan

> Triggered by the v0.19.3 `Json.Decode.int` bug: a runtime **behavioral**
> regression, **platform-dependent** (macOS passed / Linux failed), invisible to
> every codegen/corpus/byte-determinism gate — caught only by the conformance
> suite, and only on Linux CI. This plan hardens the test surface against that
> whole class across compiler + stdlib + Sky.Live/console + LSP/tooling.
>
> Source: 4-agent parallel coverage audit (2026-08-02). Status: DRAFT for scope
> decision.

## The unifying lesson

Coverage is **strong on structure** (compiles, byte-matches the oracle,
no-panic, byte-determinism) and **thin on behavior** (does the emitted program
produce the *correct* value; does a stdlib fn behave correctly at runtime under
adversarial/boundary/platform-varying inputs; does a real browser session
*recover* from the failure modes we unit-test).

Every high-risk **financial/crypto/encoding** stdlib module (Decimal, Money,
Jwt/Auth, Bytes/Encoding, Csv, Compression, Time) is verified — if at all — only
by **Go-kernel tests**, which *by construction cannot* catch the int64 failure
mode (Go kernel correct, Sky-source lowering path wrong / platform-dependent).

## Tiers (ranked by leverage; each phase = its own commit + gate)

### Tier 1 — Behavioral conformance for the int64 bug class (HIGHEST — directly closes the triggering class)
New `tests/conformance/tests/*.sky` suites, run by `scripts/conformance.sh` (already a release gate + CI codegen-build step). Each drives the **Sky-source API** (not the Go kernel) with adversarial/boundary inputs.

- **T1.1 DecimalConformanceTest** — exactness (0.1+0.2==0.3), banker's rounding at .5, divide-by-zero → Err, `fromString` adversarial (`1e3`, `-0`, `1.2.3`), scale/precision.
- **T1.2 MoneyConformanceTest** — `allocate` sums EXACTLY (100→34/33/33), 0-decimal (JPY) + 3-decimal (BHD) minor-unit round-trips, FX round-trip, negatives.
- **T1.3 Jwt/AuthConformanceTest** — auth-bypass class: `alg:none` → Err, flipped-signature → Err, expired `exp` → Err, wrong-secret → Err, valid → Ok. (security-critical)
- **T1.4 EncodingConformanceTest** — base64/hex round-trips + padding + URL-safe + invalid-input reject + empty + non-UTF8.
- **T1.5 CsvConformanceTest** — embedded comma/quote/newline, CRLF, ragged rows, CSV-injection quoting, empty vs missing field. (module has ZERO tests today)
- **T1.6 CompressionConformanceTest** — round-trip fidelity (unicode/binary/empty/large), corrupt-input → Err not panic. (ZERO tests today)
- **T1.7 TimeConformanceTest** — DST spring-forward gap + fall-back overlap, `addMonths` month-end clamp (Jan31+1mo→Feb28/29), leap Feb29, bad zone → Err, large/negative epoch. (platform/tzdata-dependent — same class as int64)
- **T1.8 Extend JsonConformanceTest** — int64 boundary directly on `Decode.int` (not just via Codec): 2^53+1 exact, max int64 exact, 2^63 → Err; plus deep nesting (stack-safety), duplicate keys (pinned result), surrogate-pair `😀`, `1e400`.
- **T1.9 Random golden-sequence** — pin `seed 42 → [known ints]` (platform-determinism guard).
- **T1.10 Math / Dict-Set / Regex / Uuid conformance** — NaN/Inf/round-half; toList ordering determinism (Live view-determinism depends on it); regex anchors/invalid-pattern/bounded-backtrack; uuid v4 bits + round-trip.

### Tier 2 — CI enforcement gaps (tests that EXIST but CI never runs, or run on one platform only)
Cheap, high-value: wiring, not new tests.

- **T2.1** Wire `scripts/example-sweep.sh` (FFI/Std.Db examples — skyvote/skychess) into `rust-ci.yml`. Today the entire Std.Db + `List (Dict String String)` + row-var-sharing class has NO automated CI gate (only a manual sweep).
- **T2.2** macOS `macos-determinism` job runs only repro+coerce-floor+rt-tests. Add `cargo test -p project` (the #164/#166 `go build` regressions) + `build-run --shape cli --golden`. Today all go-build integration regressions are Linux-only.
- **T2.3** Wire `verify-pubsub-multitab.mjs` (+ streaming) into `verify-all-web.sh` / `test-ci.sh` — they exist but no gate runs them.
- **T2.4** `scripts/test-ci.sh` add `xtask fmt` + `xtask lsp` gates (local↔CI parity — today they only run in Actions).
- **T2.5** Runtime-correctness for non-CLI shapes: a golden deterministic SSE-frame assertion for one Live example; a Tui model gob round-trip test (serialize→deserialize→assert-equal — roovo's bug class, currently no assertion).

### Tier 3 — Production-incident e2e (the darraghstudio class)
- **T3.1** Postgres session store real-engine round-trip via testcontainers (`//go:build integration`): Set→Get→Delete + `lastSeen` touch. Today Postgres — the store the prod shop runs on — is NEVER tested against a real engine, only its fail-loud branch.
- **T3.2** L10a cross-**process** gob decode: process A writes a session with a populated `any`-field Model to sqlite/postgres; a fresh `os/exec` process reads it back. Plus a codegen golden that `RegisterSkyGobTypes([]any{…})` is emitted.
- **T3.3** Browser desync-recovery after redeploy (Playwright): the exact original bug — load page, capture handler id, reboot binary with changed view, click stale control, assert `X-Sky-Status: desync` + DOM heals + next click round-trips, no reload, session preserved. (client-side heal path is untested by anything today)
- **T3.4** Playwright: SSE drop-resync (buffer overflow → full-body resync), idle-survival (`SKY_LIVE_TTL=5s` → interact → Model intact), SSE reconnect after network drop.
- **T3.5** CORS + BasicAuth middleware Go tests (both have ZERO tests today).
- **T3.6** Note: `firestoreStore` is documented but does NOT exist in the runtime (`chooseStore` has no firestore branch) — decide implement-or-delete from docs.

### Tier 4 — Tooling regression gaps
- **T4.1** `sky db` flow + dialect DDL e2e (init→gen→migrate→status→seed; SQLite vs Postgres INTEGER/BIGINT/SERIAL). Destructive verbs (push/reset/drop) have zero flow coverage.
- **T4.2** `sky fmt` string-interpolation paren-drop regression (known past bug, no guard) — fixtures + token-multiset corpus check.
- **T4.3** `sky watch` — zero tests (WatchOpts parse, debounce, allowlist).
- **T4.4** LSP diagnostics editor-parity + Go-FFI-alias false-positive assertion; extend nvim gate with rename/references/semantic-tokens.
- **T4.5** `sky add/remove/install` verb orchestration; `sky doctor`/`sky doc`/`--profile` smoke.

### Tier 5 — Compiler-internal depth
- **T5.0 (found by T1 Math suite)** Bare nullary kernel constants (`Math.pi`, `Math.e`, `Math.inf`, `Math.nan`, and likely other `Ffi.kernel` value constants) lower to Go `any`, so passing one DIRECTLY to a monomorphic user function (`f : Float -> Float`) or a Go inline operator (`Math.inf > x`, `Math.nan == Math.nan`) fails `go build` (type-assertion / "operator not defined on interface"). Routing through an arithmetic op first (`Math.pi - x`) yields a float64-typed expr that compiles. "Should-compile, fails-to-compile" lowering gap — safe (compile error, not silent runtime) but a real DX/soundness gap for a typed constant. Fix: lower a nullary kernel constant with its declared scalar Go type, not `any`.
- **T5.1** `codegen` crate: 1 unit test for the whole Go emitter → snapshot tests per `GoExprKind`/`GoStmt` (record-literal/update/tuple/coercion).
- **T5.2** `lower` crate goty tests: concrete-record-sharing-row-var, Dict-field record, record-update-in-tuple assert emitted GoTy is nominal `_R` not `any`.
- **T5.3** `infer` gate: type-EQUALITY vs oracle (not just "zero errors") — catches wrong-but-accepted inferences (#166 shape).
- **T5.4** `fuzz` gate: add oracle-diff mode for well-typed inputs (accept/reject must agree with oracle).
- **T5.5** `divergences` gate: a fixture per `known-divergences.toml` entry (today only 1 fixture).

## Cadence
Per CLAUDE.md §0.2: narrow gate per change, full sweep at milestone boundaries.
Each Tier-1 suite ships its own commit + a `scripts/conformance.sh` run. Tiers
2-5 batch by sub-area. Push at tier/phase boundaries, not per-commit.
