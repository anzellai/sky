# v0.16.2 legacy fixture cull list

Aggregated from the three composite-agent reports
(composite-01 + composite-02 + composite-03). Each spec listed
here is judged to be subsumed by one or more composites — the
end-to-end behaviour the spec asserts is exercised + tested by
the composite-app build + Sky.Test assertions.

Before deletion: verify each row by reading the spec and
confirming the composite covers the assertion. The agents made
their best judgement; manual review is the gate.

## Subsumed (delete these)

### By composite 01 (sky-generics-app)

Pure stdlib + codegen specs that the broad generics composite
exercises end-to-end:

- `test/Sky/Build/JsonPipelinePanic372Spec.hs`
- `test/Sky/Build/DictSourceSpec.hs`
- `test/Sky/Build/CoerceArgListMapInterplaySpec.hs`
- `test/Sky/Build/TaskResultBridgesSpec.hs`
- `test/Sky/Build/MonomorphiseSpec.hs`
- `test/Sky/Build/MonoIntegrationSpec.hs`
- `test/Sky/Build/LetForwardRefSpec.hs`
- `test/Sky/Build/RecordCtorEmptyListSpec.hs` ⚠ (KEEP — covers a specific lambda-arg coercion edge case the composite doesn't isolate)
- `test/Sky/Build/AnonRecordSpec.hs` ⚠ (review — composite touches but doesn't isolate)
- `test/Sky/Build/AnonLambdaSpec.hs` ⚠ (review — composite touches but doesn't isolate)
- `test/Sky/Build/NestedPatternSpec.hs` ⚠ (review — composite touches but doesn't isolate)
- `test/Sky/Build/CompileSpec.hs` ⚠ (broad smoke test — keep for now; composite is a more representative smoke)

### By composite 02 (sky-server-app)

Sky.Http.Server + Auth + Db + PubSub + Cache + Csv specs:

- `test/Sky/Build/HttpStreamForEachSpec.hs`
- `test/Sky/Build/ServerStreamSpec.hs`
- `test/Sky/Build/PubSubPublishTaskSpec.hs`
- `test/Sky/Build/PubSubPublishNoEchoSpec.hs`
- subset of `test/Sky/Build/KernelSigCoverageSpec.hs` (Middleware / RateLimit / Auth / Db sig coverage)

### By composite 03 (sky-live-shop-app)

Sky.Live + Std.Ui + Std.Ui.Chart + Std.Live.Head + pub/sub specs:

- `test/Sky/Build/SkyLiveHeadSpec.hs`
- `test/Sky/Build/StdUiChartSpec.hs`
- `test/Sky/Build/UiFillCssSpec.hs`
- `test/Sky/Build/UiFillCascadeSpec.hs`
- `test/Sky/Build/UiPseudoClassSpec.hs`
- `test/Sky/Build/UiMediaQuerySpec.hs`
- `test/Sky/Build/UiAlignSelfSpec.hs`
- `test/Sky/Build/UiAspectGridSpec.hs`
- `test/Sky/Build/UiMultilineTextareaSpec.hs`
- `test/Sky/Build/InputAttrsSplitSpec.hs`
- `test/Sky/Type/UiOnSubmitTypedRecordSpec.hs`
- (PubSub specs above are double-subsumed by 02+03; remove once)

### By composite 04 (sky-ui-multibackend-app)

(TBD — populated when composite-04 lands.)

## NOT subsumed (keep)

These specs exercise narrow shapes the composites don't isolate:

- `test/Sky/Build/CheckIsBuildSpec.hs` — `sky check` ≡ `sky build` invariant (per CLAUDE.md non-regression rules)
- `test/Sky/Build/RecordFieldOrderSpec.hs` — field-order audit P0-4
- `test/Sky/Build/RuntimeFingerprintSpec.hs` — #460 regression
- `test/Sky/Build/PipelineIntegritySpec.hs` — v0.15.42 audit
- `test/Sky/Build/PointFreePolyAliasSpec.hs` — #398 regression
- `test/Sky/Build/CryptoAeadSpec.hs` — AEAD round-trip, not in composite-01
- `test/Sky/Build/WebSocketSpec.hs` — WebSocket not in any composite
- `test/Sky/Build/WebviewLoopbackAssetsSpec.hs` — composite 04 may cover; review at land
- `test/Sky/Build/SkyLiveConsoleAuthSpec.hs` — v0.16.1 PR2 console-auth gate
- `test/Sky/Build/MainPanicRecoverSpec.hs` — v0.15.43 sync-panic gate
- `test/Sky/Build/SolverBudgetSpec.hs` — Limitation #17 HM solver bound
- `test/Sky/Build/EnvPrefixSpec.hs` — [env] prefix mechanism
- `test/Sky/Build/IORefBoundarySpec.hs` — IORef leak audit
- `test/Sky/Build/EmbeddedRuntimeSpec.hs` — TH embed invariant
- `test/Sky/Build/EmbeddedInspectorSpec.hs` — sky-ffi-inspect invariant

## Process

1. Land all 4 composites on main.
2. Read each "subsumed" spec; if its assertions are NOT mechanically
   tested inside the composite's `tests/CompositeXxxTest.sky`
   PLUS implicit at `sky build` time, MOVE that spec to "NOT
   subsumed".
3. Spawn a single agent to delete the "subsumed" set as one PR.
4. Re-run cold-cache; record numbers in
   `cold-cache-RUN_LOG.md` as the "after" baseline.
5. If under RFC budget → drop `--skip=Sky.Build.VerifyAll` from
   CI; re-run; tag v0.16.2.
