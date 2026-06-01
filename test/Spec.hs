module Main (main) where

import Test.Hspec
import qualified Sky.Build.CompileSpec
import qualified Sky.Build.MainPanicRecoverSpec
import qualified Sky.Build.IORefBoundarySpec
import qualified Sky.Build.DepHmFatalSpec
import qualified Sky.Build.ExampleSweepSpec
import qualified Sky.Build.ForeignFatalSpec
import qualified Sky.Build.TypedFfiSpec
import qualified Sky.ErrorUnificationSpec
import qualified Sky.Parse.PatternSpec
import qualified Sky.Parse.MultiLineExposingSpec
import qualified Sky.Parse.MultiLineParenAppSpec
import qualified Sky.Parse.MultiLineRecordFieldSpec
import qualified Sky.Canonicalise.ExposingSpec
import qualified Sky.Canonicalise.KernelFallbackSpec
import qualified Sky.Canonicalise.UnboundSpec
import qualified Sky.Canonicalise.QualifiedTypeAliasSpec
import qualified Sky.Canonicalise.DualImportCollisionSpec
import qualified Sky.Canonicalise.AliasNameCollisionSpec
import qualified Sky.Canonicalise.PipelineIntegritySpec
import qualified Sky.Type.ExhaustivenessSpec
import qualified Sky.Type.AnyWildcardSpec
import qualified Sky.Type.NumericBinopSpec
import qualified Sky.Type.TupleLambdaSpec
import qualified Sky.Type.UiOnSubmitTypedRecordSpec
import qualified Sky.Type.UfCycleGuardSpec
import qualified Sky.Type.RecordFieldExactnessSpec
import qualified Sky.Build.UiFillCascadeSpec
import qualified Sky.Build.UiMediaQuerySpec
import qualified Sky.Build.UiPseudoClassSpec
import qualified Sky.Build.UiTransitionAnimationSpec
import qualified Sky.Build.UiAspectGridSpec
import qualified Sky.Build.UiMultilineTextareaSpec
import qualified Sky.Build.InputAttrsSplitSpec
import qualified Sky.Build.ExposingTypeCtorsSpec
import qualified Sky.Build.LetForwardRefSpec
import qualified Sky.Build.EntryLocalShadowsDepSpec
import qualified Sky.Build.RtFieldAdtBug342Spec
import qualified Sky.Build.CaseSubjectNameShadowSpec
import qualified Sky.Build.FfiKernelAliasSpec
import qualified Sky.Build.HttpTypesSpec
import qualified Sky.Build.CryptoAeadSpec
import qualified Sky.Build.PubSubPublishTaskSpec
import qualified Sky.Build.PubSubPublishNoEchoSpec
import qualified Sky.Build.ServerStreamSpec
import qualified Sky.Build.HttpStreamForEachSpec
import qualified Sky.Build.WebviewAppSpec
import qualified Sky.Build.WebviewLoopbackAssetsSpec
import qualified Sky.Build.JsonPipelinePanic372Spec
import qualified Sky.Build.DictSourceSpec
import qualified Sky.Build.DbDecoderSpec
import qualified Sky.Build.WebSocketSpec
import qualified Sky.Parse.MultiLineCaseSubjectSpec
import qualified Sky.Parse.MultiLineCaseKeywordSpec
import qualified Sky.Parse.MultiLineSignatureSpec
import qualified Sky.Parse.RowPolyRecordAnnotationSpec
import qualified Sky.Parse.MultilineInterpolationEscapeSpec
import qualified Sky.Build.CaseCatchallSubjectDiscardSpec
import qualified Sky.Format.FormatSpec
import qualified Sky.Build.GoKeywordCollisionSpec
import qualified Sky.Build.NestedPatternSpec
import qualified Sky.Build.ConsCtorPatternSpec
import qualified Sky.Build.ConsPatternLengthSpec
import qualified Sky.Build.CtorConsPatternSpec
import qualified Sky.Build.EnvPrefixSpec
import qualified Sky.Build.FfiGenMultiSpec
import qualified Sky.Build.FfiTypeParserSpec
import qualified Sky.Build.TaskResultBridgesSpec
import qualified Sky.Build.CheckIsBuildSpec
import qualified Sky.Build.RecordFieldOrderSpec
import qualified Sky.Build.RecordCtorEmptyListSpec
import qualified Sky.Build.PointFreePolyAliasSpec
import qualified Sky.Build.HofTypedMsgSpec
import qualified Sky.Build.CoerceArgParametricSpec
import qualified Sky.Build.IsPlainIdentSpec
import qualified Sky.Build.InferExprTypeBinopSpec
import qualified Sky.Build.CoerceArgListMapInterplaySpec
import qualified Sky.Build.LowerCtxCascadeSpec
import qualified Sky.Build.LetBodyCascadeResumeSpec
import qualified Sky.Build.SnapshotCallerCtxSpec
import qualified Sky.Build.SkyshopCompilesSpec
import qualified Sky.Build.AnonLambdaSpec
import qualified Sky.Build.CrossModuleLambdaCollisionC_Spec
import qualified Sky.Build.AnonRecordSpec
import qualified Sky.Build.AuthUntypedBoundarySpec
import qualified Sky.Build.Issue52Spec
import qualified Sky.Build.ValidatorSpec
import qualified Sky.Build.GoBuildRefinerSpec
import qualified Sky.Build.MonomorphiseSpec
import qualified Sky.Build.MonoIntegrationSpec
import qualified Sky.Reporting.DiagnosticSpec
import qualified Sky.Diagnostics.CoverageSpec
import qualified Sky.Type.InstanceCaptureSpec
import qualified Sky.Type.SolvedTypesRegionMapSpec
import qualified Sky.Build.KernelSigCoverageSpec
import qualified Sky.Build.KernelStdlibCoverageSpec
import qualified Sky.Build.PureModuleSpec
import qualified Sky.Build.HeapBoundedHmSpec
import qualified Sky.Build.SolverBudgetSpec
import qualified Sky.Build.UnreachableGateSpec
import qualified Sky.Parse.CommentsSpec
import qualified Sky.Lsp.HoverShadowingSpec
import qualified Sky.Lsp.RenameStableSpec
import qualified Sky.Build.VerifyScenarioSpec
import qualified Sky.Build.VerifyAllSpec
import qualified Sky.Lsp.ProtocolSpec
import qualified Sky.Lsp.CapabilitiesSpec
import qualified Sky.Lsp.DiagnosticsSpec
import qualified Sky.Lsp.HoverTypesSpec
import qualified Sky.Lsp.CompletionSpec
import qualified Sky.Lsp.ScaleSpec
import qualified Sky.Lsp.NvimDriverSpec
import qualified Sky.Build.EmbeddedRuntimeSpec
import qualified Sky.Build.EmbeddedInspectorSpec
import qualified Sky.Cli.ExitCodesSpec
import qualified Sky.Cli.InitSpec
import qualified Sky.Cli.RunSpec
import qualified Sky.Cli.FmtSpec
import qualified Sky.Cli.CleanSpec
import qualified Sky.Cli.TestSpec
import qualified Sky.Cli.UpgradeClaudeSpec
import qualified Sky.Cli.WatchSpec
import qualified Sky.Cli.DoctorSpec

main :: IO ()
main = hspec $ do
    describe "Sky.Build.Compile"         Sky.Build.CompileSpec.spec
    -- v0.15.43 Cycle 6 PC — top-level `func main()` MUST start with
    -- `defer rt.LogPanicAndExit()`. Regression here re-exposes the
    -- synchronous-panic class (Sky.Cli / Sky.Tui / batch jobs).
    describe "Sky.Build.MainPanicRecover" Sky.Build.MainPanicRecoverSpec.spec
    -- v0.15.5 PR 2/6 — regression gate for the retired per-scope
    -- IORef pair (mechanical string match on Compile.hs).
    describe "Sky.Build.IORefBoundary"   Sky.Build.IORefBoundarySpec.spec
    -- v0.10.0: dep module HM errors must abort the build (used to
    -- silently degrade to `any`-typed bindings, hiding real type
    -- bugs that surfaced as func-pointer-as-string at runtime).
    describe "Sky.Build.DepHmFatal"      Sky.Build.DepHmFatalSpec.spec
    -- v0.10.0: foreign-call mismatches at the constraint solver are
    -- fatal (was silently swallowed). Surfaced as runtime panics
    -- like rt.AsBool: expected bool, got rt.SkyResult[…].
    describe "Sky.Build.ForeignFatal"    Sky.Build.ForeignFatalSpec.spec
    describe "Sky.Parse.Pattern"         Sky.Parse.PatternSpec.spec
    -- Multi-line `module/import ... exposing (…)` parser fix +
    -- parse-error-is-fatal regression fence (compiler bug #1).
    describe "Sky.Parse.MultiLineExposing" Sky.Parse.MultiLineExposingSpec.spec
    -- Multi-line function application inside grouping parens. Pre-fix
    -- the next-line continuation check anchored against the inner
    -- func's column; if the inner func sat far from column 1 (because
    -- of `outer (`), valid continuations on smaller columns failed
    -- with "Expected , or )". Sister fix: keyword-aware exprStart so
    -- the relaxed rule doesn't gobble `else`/`then`/`in`/`of`.
    describe "Sky.Parse.MultiLineParenApp"
                                         Sky.Parse.MultiLineParenAppSpec.spec
    -- First record-literal field's value on a new line. Pre-fix
    -- the first-field path used `spaces` after the `=` (no newline)
    -- while subsequent fields used `freshLine` (newline OK), so a
    -- hand-written shape like
    --   { system =
    --         "..."
    --   , user = "..."
    --   }
    -- failed with PARSE ERROR: DeclarationError pointing at the
    -- `=`. `sky fmt` doesn't produce this shape today (it always
    -- puts the first field's value on the same line as `{`), but
    -- humans and other formatters do — and the inconsistency
    -- between first-field and subsequent-field rules was a real
    -- foot-gun.
    describe "Sky.Parse.MultiLineRecordField"
                                         Sky.Parse.MultiLineRecordFieldSpec.spec
    describe "Sky.Canonicalise.Exposing" Sky.Canonicalise.ExposingSpec.spec
    -- Regression: kernel qualifiers (Crypto, Encoding, Hex, …) used
    -- without an explicit `import Sky.Core.<Mod>` must resolve as
    -- VarKernel, not VarTopLevel — otherwise the lowerer ships
    -- `Crypto_sha256(arg)` (no `rt.` prefix) and `go build` fails.
    describe "Sky.Canonicalise.KernelFallback" Sky.Canonicalise.KernelFallbackSpec.spec
    describe "Sky.Canonicalise.Unbound"  Sky.Canonicalise.UnboundSpec.spec
    -- Qualified type annotation under `import M as Alias` must
    -- resolve through the alias map. Pre-fix `Ui.Color` (under
    -- `import Std.Ui as Ui`) became `Canonical "Ui"` while bare
    -- `Color` (via exposing) became `Canonical "Std.Ui"` — HM
    -- rejected with the cryptic "Color vs Color" message.
    describe "Sky.Canonicalise.QualifiedTypeAlias"
                                         Sky.Canonicalise.QualifiedTypeAliasSpec.spec
    -- Cycle 4 D5: two imports with the same default qualifier (e.g.
    -- `import State` + `import App.State` — both last-segment `State`)
    -- silently miscompiled — the `_importAliases` last-wins vs
    -- `_qualVars` union mismatch produced the dishonest "Model vs
    -- Model" type error. Now rejected at canonicalisation time with
    -- an explicit fix-it suggesting `as Alias`.
    describe "Sky.Canonicalise.DualImportCollision"
                                         Sky.Canonicalise.DualImportCollisionSpec.spec
    -- Cycle 4 #350 / #361 v2: cross-module type-alias NAME collision.
    -- Two deps each exposing `Model` under disambiguating `as Alias`
    -- clauses (#350) — closes the dep-alias map collapsing on bare
    -- name. AND qualified type reference through a re-exporting
    -- transit module (#361) — the Dashboard regression that reverted
    -- PR #111. Both close in one shot: (home, name) primary lookup +
    -- bare-name fallback for unique bodies.
    describe "Sky.Canonicalise.AliasNameCollision"
                                         Sky.Canonicalise.AliasNameCollisionSpec.spec
    -- v0.15.42 Cycle 6: 3 pipeline-integrity bugs called out in the
    -- v0.15.41 audit (§3.1 unknown qualifier silently passing,
    -- §3.4 "Compilation successful" banner before go build runs,
    -- §3.2 Prelude shadowing of stdlib types). One regression spec
    -- per bug — see PipelineIntegritySpec.hs header for context.
    describe "Sky.Canonicalise.PipelineIntegrity"
                                         Sky.Canonicalise.PipelineIntegritySpec.spec
    describe "Sky.Type.Exhaustiveness"   Sky.Type.ExhaustivenessSpec.spec
    -- Cross-branch HM `any` wildcard fix (compiler bug #3). Distinct
    -- occurrences of `any` in source types must NOT share a single
    -- unification variable; each gets its own fresh var.
    describe "Sky.Type.AnyWildcard"      Sky.Type.AnyWildcardSpec.spec
    -- Tuple-pattern in lambda arg fix + `/=` operator codegen fix.
    -- Surfaced together when investigating why `sky test` for a
    -- passing module was xfailing.
    describe "Sky.Type.TupleLambda"      Sky.Type.TupleLambdaSpec.spec
    describe "Sky.Type.NumericBinop"     Sky.Type.NumericBinopSpec.spec
    -- Std.Ui.onSubmit widening: in-module typed-record-arg case
    -- (`Ui.onSubmit DoSignIn` where `DoSignIn : LoginForm -> Msg`).
    -- Pre-fix the kernel sig forced `msg = (record -> msg)` and the
    -- enclosing `Element Msg` annotation rejected it; post-fix the
    -- wrapper is `(a -> Attribute b)`.
    describe "Sky.Type.UiOnSubmitTypedRecord"
                                         Sky.Type.UiOnSubmitTypedRecordSpec.spec
    -- v0.13.1 UF cycle guard: importing Std.Ui.Events (or any
    -- Sky-source stdlib module that brings the recursive Element /
    -- Attribute ADT through cross-module externals) used to OOM
    -- the compiler during the dep-fixpoint round-1 solve. Pre-fix
    -- a missing occurs check in Unify.actuallyUnify spliced a
    -- self-referential cycle into the UF graph; variableToType
    -- then recursed forever through cyclic App1 args.
    describe "Sky.Type.UfCycleGuard"     Sky.Type.UfCycleGuardSpec.spec
    -- v0.13.3 Std.Ui Fill cross-axis cascade fix: widthCss/heightCss
    -- used to emit `flex-grow:1; align-self:stretch;` for any Fill
    -- regardless of parent flex-direction. In a column parent every
    -- child marked `width: fill` then competed for vertical space,
    -- breaking the typical header/main/footer layout.
    describe "Sky.Build.UiFillCascade"   Sky.Build.UiFillCascadeSpec.spec
    -- Std.Ui.mediaQuery / Ui.breakpoint — issue #376. Compiles a
    -- tiny project + checks the lowered Go contains the runtime
    -- marker attrs (data-sky-mq-q / data-sky-mq-rules) + the
    -- breakpoint expansion (max-width / prefers-color-scheme).
    describe "Sky.Build.UiMediaQuery"    Sky.Build.UiMediaQuerySpec.spec
    -- Std.Ui pseudo-class primitive (issue #377). Same shape as
    -- UiMediaQuerySpec — builds a tiny project + checks the lowered
    -- Go contains the runtime marker attr (data-sky-pc-rules) +
    -- per-pseudo wire tags (h|, v|, f|, a|, d|).
    describe "Sky.Build.UiPseudoClass"    Sky.Build.UiPseudoClassSpec.spec
    -- Std.Ui transitions + animations DSL (issue #378). Compile-side
    -- fence — checks the new helper symbols
    -- (Transition.attribute / Animation.attribute) lower to AttrTransition /
    -- AttrAnimation ctors + the runtime marker attrs
    -- (data-sky-tr-rules / data-sky-anim-rules) appear in the
    -- emitted Go. The runtime injection side
    -- (injectTransitionStyles / injectAnimationStyles +
    -- reduced-motion auto-gate) is covered by
    -- runtime-go/rt/live_transition_animation_test.go.
    describe "Sky.Build.UiTransitionAnimation"
                                       Sky.Build.UiTransitionAnimationSpec.spec
    -- Std.Ui aspect-ratio + content-aware grid tracks (#379) — the
    -- compile-side regression fence. Checks that the new helper
    -- symbols (Ui.aspectRatio / Ui.aspectRatioWH / Std.Ui.Grid.tracks
    -- / Grid.columns / Grid.rows) lower to the expected literal CSS
    -- strings + marker keys in the emitted Go. The runtime side is
    -- a pure inline-style emission via the existing AttrStyle channel
    -- (no new injection pass needed) — verified by the visual gates
    -- in scripts/verify-ui-showcase.mjs.
    describe "Sky.Build.UiAspectGrid"  Sky.Build.UiAspectGridSpec.spec
    -- Std.Ui.Input.multiline used to call `inputBase "textarea"` which
    -- built a `Ui.input` element with type="textarea" — invalid HTML
    -- that browsers silently degrade to single-line text input. Fix
    -- routes through a real <textarea> element with the value-attr
    -- → text-content splice the Live runtime already supports.
    describe "Sky.Build.UiMultilineTextarea" Sky.Build.UiMultilineTextareaSpec.spec
    -- Input.* attrs partition between wrapper + inner control —
    -- GitHub issue #63 follow-up: layout/size/alignment attrs
    -- hoist to the wrapWithLabel wrapper so the layout chain
    -- propagates; form / event / visual attrs stay on the inner
    -- control. Pre-fix: textarea-fill-height inside a row
    -- collapsed because the wrapper carried no layout attrs.
    describe "Sky.Build.InputAttrsSplit" Sky.Build.InputAttrsSplitSpec.spec
    describe "Sky.Build.ExposingTypeCtors" Sky.Build.ExposingTypeCtorsSpec.spec
    describe "Sky.Build.LetForwardRef"     Sky.Build.LetForwardRefSpec.spec
    describe "Sky.Build.EntryLocalShadowsDep" Sky.Build.EntryLocalShadowsDepSpec.spec
    describe "Sky.Build.RtFieldAdtBug342" Sky.Build.RtFieldAdtBug342Spec.spec
    describe "Sky.Build.CaseSubjectNameShadow" Sky.Build.CaseSubjectNameShadowSpec.spec
    describe "Sky.Build.FfiKernelAlias" Sky.Build.FfiKernelAliasSpec.spec
    describe "Sky.Build.HttpTypes" Sky.Build.HttpTypesSpec.spec
    describe "Sky.Build.CryptoAead" Sky.Build.CryptoAeadSpec.spec
    -- Cycle 4 PT: Task-shaped Std.PubSub.publish — callable from any
    -- context (raw Sky.Http.Server api handlers / post-init goroutines
    -- / scheduled jobs), complements Cmd.publish which is bound to
    -- the Sky.Live update-return tuple.
    describe "Sky.Build.PubSubPublishTask" Sky.Build.PubSubPublishTaskSpec.spec
    -- Cycle 4 NE / issue #359: Cmd.publishNoEcho + PubSub.publishNoEcho —
    -- opt-out echo for "instant feedback for publisher" pattern. Saves
    -- the broker round-trip; in v0.16+ cross-process broker tiers the
    -- saved hop is 10-100ms+ of latency.
    describe "Sky.Build.PubSubPublishNoEcho" Sky.Build.PubSubPublishNoEchoSpec.spec
    -- Cycle 4 HS-Server / issue #362: Sky.Http.Server.Stream — server-side
    -- streaming HTTP response primitive (mirror of Sky.Core.Http.Stream).
    -- Unblocks LLM token-stream proxying + SSE endpoints without
    -- hand-rolled chunk plumbing on the Sky side.
    describe "Sky.Build.ServerStream" Sky.Build.ServerStreamSpec.spec
    -- Issue #373: Sky.Core.Http.Stream.forEachChunk — synchronous
    -- chunk-iterator that bridges the Sub-based client-side stream
    -- consumer with Sky.Http.Server.Stream producers inside the
    -- same handler goroutine (the SkyDeploy /generate/stream
    -- relay shape).
    describe "Sky.Build.HttpStreamForEach" Sky.Build.HttpStreamForEachSpec.spec
    -- Issue #356 / v0.1 MVP: Sky.Webview backend. Pins the
    -- Std.Webview.app type-checker contract + kernel routing.
    describe "Sky.Build.WebviewApp" Sky.Build.WebviewAppSpec.spec
    -- Bug #370: Sky.Webview can't load relative-path assets — the
    -- runtime now spawns a 127.0.0.1 loopback http server when
    -- sky.toml `[live].static` is set, and falls through to
    -- SetHtml (no regression) when unset.
    describe "Sky.Build.WebviewLoopbackAssets"
        Sky.Build.WebviewLoopbackAssetsSpec.spec
    -- Bug #372: user-defined Decoder pipeline (Decode.andThen +
    -- Decode.map over a curried record ctor) panicked with
    -- `rt.Coerce: expected func(interface {}) interface {}, got
    -- Spec_R` at the final stage.  Runtime fix: adaptFuncValue now
    -- currys instead of zero-pads when target's return is `any`.
    describe "Sky.Build.JsonPipelinePanic372"
        Sky.Build.JsonPipelinePanic372Spec.spec
    -- v0.15.45 — Dict + Set Layer 3 contract + typed-key routing.
    describe "Sky.Build.DictSource" Sky.Build.DictSourceSpec.spec
    -- v0.15.45 — Std.Db.Decode typed row decoder pipeline.
    describe "Sky.Build.DbDecoder" Sky.Build.DbDecoderSpec.spec
    -- v0.15.46 — Sky.Core.WebSocket + Sky.Http.Server.WebSocket
    -- kernel routing + Sky-side type-checking.
    describe "Sky.Build.WebSocket" Sky.Build.WebSocketSpec.spec
    describe "Sky.Parse.MultiLineCaseSubject" Sky.Parse.MultiLineCaseSubjectSpec.spec
    describe "Sky.Parse.MultiLineCaseKeyword"
        Sky.Parse.MultiLineCaseKeywordSpec.spec
    describe "Sky.Parse.MultiLineSignature" Sky.Parse.MultiLineSignatureSpec.spec
    describe "Sky.Parse.RowPolyRecordAnnotation"
        Sky.Parse.RowPolyRecordAnnotationSpec.spec
    -- Cycle 4 D3: `\{{NAME}}` escape for literal `{{NAME}}` placeholders
    -- in triple-quoted strings (Mustache / Handlebars / shell-script
    -- templating). Pre-fix the desugarer ate every `{{ident}}` as a Sky
    -- variable reference; codegen then emitted `undefined: NAME`.
    describe "Sky.Parse.MultilineInterpolationEscape"
        Sky.Parse.MultilineInterpolationEscapeSpec.spec
    describe "Sky.Build.CaseCatchallSubjectDiscard"
        Sky.Build.CaseCatchallSubjectDiscardSpec.spec
    -- Closed-record exactness + cross-module externals registration:
    --   1. unifyRecords (Sky.Type.Unify) used to silently merge field-
    --      mismatched closed records under a fresh extension. Now
    --      rejects when either side is closed and the other has
    --      extras.
    --   2. buildCrossModuleExternalsWithMods (Sky.Build.Compile) used
    --      to filter externals to function-typed names only, so
    --      bare values like `Ui.fill : Length` were dropped and
    --      `Ui.fill 1` type-checked silently. Now all top-level
    --      decls register.
    -- Both surfaced from a real-world Std.Ui port (Border.shadow
    -- with wrong record shape passed sky check + sky build then
    -- panicked at runtime; Ui.fill 1 likewise).
    describe "Sky.Type.RecordFieldExactness"
                                         Sky.Type.RecordFieldExactnessSpec.spec
    describe "Sky.Format.Format"         Sky.Format.FormatSpec.spec
    -- Sky function names that match Go reserved words must sanitise
    -- at the CALL site too, not only at the definition site (see
    -- comment in Sky.Build.Compile near the Can.Call/VarTopLevel
    -- branch). Pre-fix `go` defined in Main emitted `func go_(...)`
    -- but the call site emitted `go(...)` — Go's parser interpreted
    -- it as a goroutine launch and rejected the build.
    describe "Sky.Build.GoKeywordCollision"
                                         Sky.Build.GoKeywordCollisionSpec.spec
    describe "Sky.Build.NestedPattern"   Sky.Build.NestedPatternSpec.spec
    -- Cons-with-constructor pattern fix (compiler bug #2). The
    -- lowerer now emits a head-discriminator check on `(Ctor x) :: rest`
    -- so the body only fires when the head's actual ctor matches.
    describe "Sky.Build.ConsCtorPattern" Sky.Build.ConsCtorPatternSpec.spec
    -- Cons-pattern length-guard regression (#402). Walks the
    -- cons-chain so `a :: b :: c :: _` emits `len >= 3` and
    -- `a :: b :: []` emits `len == 2`, not the buggy `len >= 1 &&
    -- len(tail) >= 1` (which collapsed to `>= 2` regardless of arm).
    describe "Sky.Build.ConsPatternLength" Sky.Build.ConsPatternLengthSpec.spec
    -- Inverse of ConsCtorPattern: cons / fixed-length-list pattern
    -- INSIDE a ctor arg (`Just (h :: _)`, `Ok [a, b]`). Pre-fix,
    -- argPatternCondition only narrowed for ctor / literal sub-
    -- patterns; PCons / PList fell through to no-condition and the
    -- destructure binding panicked at runtime when the inner list
    -- was the wrong length. Surfaced from a sendcrafts I18n.regionOf
    -- panic on `regionOf ["en"]` (List.tail returns Just []).
    describe "Sky.Build.CtorConsPattern" Sky.Build.CtorConsPatternSpec.spec
    -- sky.toml [env] prefix: namespacing for runtime SKY_* env-var
    -- reads. Default unchanged ("SKY"). Setting `[env] prefix = "X"`
    -- emits rt.SetEnvPrefix at the top of init() and switches every
    -- internal os.Getenv("SKY_*") to read X_*. Backwards-compat
    -- when the key is absent. Plus System.setenv / System.unsetenv
    -- stdlib helpers so users can mutate env without Go FFI.
    describe "Sky.Build.EnvPrefix"      Sky.Build.EnvPrefixSpec.spec
    -- v0.11.x install perf: multi-package inspector mode + chunked
    -- parallel calls. Spec asserts the JSON-array decode contract +
    -- the empty-list fast-path that lets `sky install` skip the
    -- inspector entirely on warm caches.
    describe "Sky.Build.FfiGenMulti"    Sky.Build.FfiGenMultiSpec.spec
    -- Phase B regression fence for the FFI Sky-type parser used
    -- by Sky.Build.FfiRegistry to lift kernel.json's `skyType`
    -- field into a typed AST. Locks the closed grammar against
    -- producer/consumer drift.
    describe "Sky.Build.FfiTypeParser"  Sky.Build.FfiTypeParserSpec.spec
    -- Result/Task bridge helpers (Task.fromResult, Task.andThenResult,
    -- Result.andThenTask) — runtime + canonicaliser + kernel sigs gate.
    describe "Sky.Build.TaskResultBridges" Sky.Build.TaskResultBridgesSpec.spec
    describe "Sky.ErrorUnification"      Sky.ErrorUnificationSpec.spec
    -- ExampleSweep must run before TypedFfi: the typed-FFI checks
    -- read `examples/*/sky-out/main.go` and `.skycache/go/*` which
    -- only exist after the sweep has built them.
    describe "Sky.Build.ExampleSweep"    Sky.Build.ExampleSweepSpec.spec
    describe "Sky.Build.TypedFfi"        Sky.Build.TypedFfiSpec.spec
    -- Audit P0-1: sky check must be ≥ sky build.
    describe "Sky.Build.CheckIsBuild"    Sky.Build.CheckIsBuildSpec.spec
    -- Audit P0-4: record auto-ctor respects declaration order.
    describe "Sky.Build.RecordFieldOrder" Sky.Build.RecordFieldOrderSpec.spec
    -- Limitation #18: auto-ctor's typed-slice param coerces empty-list
    -- arg via rt.AsListT[T]. Pre-fix, `Item 1 "first" []` shipped
    -- `Item(1, "first", []any{})` and go build rejected.
    describe "Sky.Build.RecordCtorEmptyList" Sky.Build.RecordCtorEmptyListSpec.spec
    -- #398: point-free top-level alias of a polymorphic / N-ary
    -- function. Pre-fix, `tickle = String.toUpper` emitted a
    -- 0-arity Go thunk wrapper; call sites failed `go build`.
    describe "Sky.Build.PointFreePolyAlias" Sky.Build.PointFreePolyAliasSpec.spec
    -- Limitation #18 (other half): renderHofParamTy used to hardcode
    -- the inner-function return as `any`, breaking helpers with typed
    -- (String -> Msg) callbacks. Now routes via typeStrWithAliasesReg.
    describe "Sky.Build.HofTypedMsg"        Sky.Build.HofTypedMsgSpec.spec
    -- v0.15.x hardening / Gap A1 / Plan Item P1 — coerceArg's
    -- parametric-alias short-circuit was gated on `goExprGoType e`
    -- returning Just. For let-bound polymorphic-call results the
    -- registry has no entry; the arm didn't fire; codegen emitted
    -- `any(arg).(Cfg_R[any])`, panicking with `interface {} is
    -- main.Cfg_R[int], not main.Cfg_R[interface {}]`. The
    -- structural-fallback arm closes this by resolving the
    -- source's `Can.Expr` through `inferExprType` and matching
    -- alias bases.
    describe "Sky.Build.CoerceArgParametric"
                                            Sky.Build.CoerceArgParametricSpec.spec
    -- v0.15.x hardening / Gap A4 / Plan Item P3 — `isPlainIdent`
    -- structural unit table.  Locks the recursion invariants of
    -- the "plain user-ident chain" classifier used by `coerceArg`
    -- at the generic-param-bearing target arm.  The legacy
    -- recursion correctness for kernel-call-rooted selector
    -- chains is the spec's load-bearing case; companion typed
    -- gate is exercised by CoerceArgParametricSpec at runtime.
    describe "Sky.Build.IsPlainIdent"       Sky.Build.IsPlainIdentSpec.spec
    -- v0.15.x hardening / Gap A5 / Plan Item P4 — typed-primitive
    -- binop fast-path in HOF arg slots.  Locks the codegen
    -- invariant that `f (x + 1)` (where `f : Int -> Int` is a
    -- typed local) lowers Go-native (`f(x + 1)`) instead of the
    -- legacy `rt.CoerceInt(rt.SkyCall(f, x + 1))` reflect-wrap.
    -- 8 cases cover arithmetic, logical, list / string concat,
    -- cons, deeply nested arithmetic, chained typed-HOF, two-arg
    -- siblings.  Spec runs the real `sky build` end-to-end +
    -- inspects the emitted main.go AND runs the compiled binary
    -- to assert no panic.
    describe "Sky.Build.InferExprTypeBinop"
                                            Sky.Build.InferExprTypeBinopSpec.spec
    -- v0.15.x hardening / Cycle 1 P2-followup — LOCK spec for the
    -- three-way σ/erasure/coerceArg consensus.  See the spec
    -- module header + `docs/v0.15.x-hardening/arbitrations/HEAD-
    -- CYCLE-01-P2.md` for the architectural rationale.  Without
    -- this lock the canonical `List.map fn (List.take 6 xs)`
    -- pattern regresses under any future Compile.hs edit that
    -- threads positive `goExprGoType` information into the
    -- `coerceArg` skip-check vote.
    describe "Sky.Build.CoerceArgListMapInterplay"
                                            Sky.Build.CoerceArgListMapInterplaySpec.spec
    -- v0.15.x hardening / Cycle 1 P6 — LowerCtx cascade Phase 2.
    -- Promotes `lowerExpr` / `lowerExprExpectGo` from no-op
    -- delegates into REAL ctx-installing wrappers, and migrates
    -- four structural-backbone slots (lambda body / record-field
    -- init / list element / call arg) to route through them.
    -- Lock fires on the constructor surface + the byte-identical
    -- compile contract for a four-slot exercise.
    describe "Sky.Build.LowerCtxCascade"    Sky.Build.LowerCtxCascadeSpec.spec
    -- v0.15.x hardening / Cycle 3 P37b — LowerCtx cascade Phase 3
    -- resume.  `letBindingType` is now pure; the three slots P6
    -- deferred (record-field init / list element / let body) now
    -- route through the ctx-aware wrapper.  Lock fires on (a) the
    -- pure signature, (b) `Solve.lookupSolvedRegion` consumption,
    -- (c) the typed-coerce emission shape on a let-body fixture.
    describe "Sky.Build.LetBodyCascadeResume"
                                            Sky.Build.LetBodyCascadeResumeSpec.spec
    -- v0.15.x hardening / Cycle 3 P38 — audit gap C10 closure.
    -- The three P37b-resumed cascade slots (record-field init,
    -- list element, let body) now share a single
    -- `snapshotCallerCtx` helper that reads scopeStateRef and
    -- forces the resulting LowerCtx to WHNF before returning it.
    -- Lock fires on (a) the helper's source-level signature +
    -- NOINLINE pragma + load-bearing `seq`, (b) exactly three
    -- call sites, (c) the P37b PR #91 thunk-hazard provenance
    -- comment, (d) end-to-end build of the three-slot fixture.
    describe "Sky.Build.SnapshotCallerCtx"
                                            Sky.Build.SnapshotCallerCtxSpec.spec
    -- v0.15.x hardening / Cycle 1 P2-followup STANDING lock —
    -- examples/13-skyshop is the Stripe-SDK-scale benchmark
    -- (76k FFI symbols) and is the canary that catches
    -- "looks fine in 26 small examples, breaks at scale"
    -- regressions in compiler edits.
    describe "Sky.Build.SkyshopCompiles"    Sky.Build.SkyshopCompilesSpec.spec
    -- v0.13 D-Lambda-Lowerer regression: Sky lambdas at user-
    -- defined HOF slots lower to typed `func(X) Y` shapes via
    -- curryLambdaPatTyped (was only kernel HOFs pre-v0.13).
    describe "Sky.Build.AnonLambda"         Sky.Build.AnonLambdaSpec.spec
    -- v0.15.6 #365 — cross-module local lambda collision.
    describe "Sky.Build.CrossModuleLambdaCollisionC" Sky.Build.CrossModuleLambdaCollisionC_Spec.spec
    -- v0.13 E regression: synthAnonRecordName registers shapes
    -- into globalAnonRecords; generateAnonRecordDecls emits
    -- `type Anon_R_<hash> = struct{...}` so the typed Go name
    -- resolves. Removed the pre-E `sanitiseTypedDeep` cover-up.
    describe "Sky.Build.AnonRecord"         Sky.Build.AnonRecordSpec.spec
    -- v0.15.12 P5 / Gap A6 — security-critical Auth kernels gate
    -- on String typing at the Sky type level; bridging an `any`
    -- typed binding into Auth.hashPassword / signToken / etc. is
    -- a compile-time E4006 / Sky.Auth.UntypedBoundary error.
    describe "Sky.Build.AuthUntypedBoundary" Sky.Build.AuthUntypedBoundarySpec.spec
    -- Issue #52 regression: (1) List.drop with any-typed Int arg
    -- needs rt.AsInt coercion at the typed-kernel boundary, and
    -- (2) record update `{ m | n = X }` must HM-check the new value
    -- against the existing field type. Both used to slip past Sky
    -- and surface as cryptic Go-build / runtime panics.
    describe "Sky.Build.Issue52"             Sky.Build.Issue52Spec.spec
    -- v0.13 Layer 2: codegen-stage validator regression fence.
    -- Pins the typed-kernel-any-arg detector + the SKY-ORIGIN
    -- comment parser + the go-build error → Sky-region mapper.
    -- Fires BEFORE go build when a known-bad shape is emitted.
    describe "Sky.Build.Validator"           Sky.Build.ValidatorSpec.spec
    -- v0.13 Layer 2 integration: full sky build → corruption →
    -- re-build → [E5001] Diagnostic round-trip.  Catches the
    -- go-build error refiner end-to-end.
    describe "Sky.Build.GoBuildRefiner"      Sky.Build.GoBuildRefinerSpec.spec
    -- v0.13 Layer 1: structured Diagnostic AST + CLI/LSP renderers.
    -- Locks the AST shape, the diagnostic code registry, and the
    -- renderer output for all consumers (CLI, LSP, future docgen).
    describe "Sky.Reporting.Diagnostic"      Sky.Reporting.DiagnosticSpec.spec
    -- v0.13 overall guarantee: one regression test per error
    -- category, asserting the CLI surfaces the stable code +
    -- prefix and the build never reaches the runtime.
    describe "Sky.Diagnostics.Coverage"      Sky.Diagnostics.CoverageSpec.spec
    -- v0.13 Phase A1: monomorphisation instance capture.  Locks
    -- the solver's CForeign instance-recording mechanism that the
    -- monomorphisation pass consumes downstream.
    describe "Sky.Type.InstanceCapture"      Sky.Type.InstanceCaptureSpec.spec
    -- v0.15.x P37a: SolvedTypes carries the per-region HM type map
    -- as pure data.  Locks the populate-time contract so the
    -- IORef-backed `lookupRegionType` reader (still load-bearing in
    -- Compile.hs) and the pure `Solve.lookupSolvedRegion` query
    -- key off ONE solver-side write.  P37b consumes the field via
    -- `letBindingType` and drops the IORef.
    describe "Sky.Type.SolvedTypesRegionMap"
                                            Sky.Type.SolvedTypesRegionMapSpec.spec
    -- v0.13 Phase A2: monomorphisation type-level pieces.  Locks
    -- the mangling encoding + substitution semantics that the
    -- downstream emission pass relies on.
    describe "Sky.Build.Monomorphise"        Sky.Build.MonomorphiseSpec.spec
    -- v0.13 Phase A3: end-to-end monomorphisation capture from a
    -- real `sky build` run with SKY_MONO_TRACE=1.  Locks the
    -- data flow from solver → mangling → compile-pipeline log.
    describe "Sky.Build.MonoIntegration"     Sky.Build.MonoIntegrationSpec.spec
    -- Limitation #16: kernel-sig coverage for the dangerous-class
    -- gaps (returns Maybe/Result/Task wrappers OR opaque FFI types).
    -- Without HM sigs, user pattern-matching against the wrapper
    -- silently degrades to `any` and surfaces as runtime panics.
    describe "Sky.Build.KernelSigCoverage" Sky.Build.KernelSigCoverageSpec.spec
    -- Cycle 4 D1: every Ffi.kernel "Name" declaration in
    -- sky-stdlib/ must have a matching Kernel.lookup entry. Closes
    -- the `String.toList undefined` / `Math.abs undefined` class.
    describe "Sky.Build.KernelStdlibCoverage" Sky.Build.KernelStdlibCoverageSpec.spec
    -- v0.15.50: Sky.Core.Pure additive `() -> Task Error a` mirror module.
    -- Spec pins the typed-Go shape (no `any` widening) + kernel reuse.
    describe "Sky.Build.PureModule"          Sky.Build.PureModuleSpec.spec
    -- Limitation #17: Std.Ui-cascading HM constraint pathology that
    -- pre-fix OOMed at 4-5 GB. Spec re-runs sky check on the bak
    -- reproducer under a tight heap cap.
    describe "Sky.Build.HeapBoundedHm"      Sky.Build.HeapBoundedHmSpec.spec
    -- Limitation #17 hardening: defensive bound on the HM solver.
    -- Caps total solveHelp invocations per `solve` call; trips
    -- with TYPE ERROR before unbounded heap consumption can OOM
    -- the host. See SolverBudgetSpec for the env-var override
    -- (SKY_SOLVER_BUDGET) and the escape-hatch behaviour.
    describe "Sky.Build.SolverBudget"       Sky.Build.SolverBudgetSpec.spec
    -- Audit P0-5: no raw `panic("sky: internal…)` in emitted Go.
    -- Runs AFTER ExampleSweep so the sky-out/main.go files are fresh.
    describe "Sky.Build.UnreachableGate"  Sky.Build.UnreachableGateSpec.spec
    -- Audit P2-1: parser captures comments into Src._comments.
    describe "Sky.Parse.Comments"         Sky.Parse.CommentsSpec.spec
    -- Audit P2-2: LSP local-type shadowing guard.
    describe "Sky.Lsp.HoverShadowing"     Sky.Lsp.HoverShadowingSpec.spec
    -- Audit P2-3: module-stable TVar renaming.
    describe "Sky.Lsp.RenameStable"       Sky.Lsp.RenameStableSpec.spec
    -- Audit P2-4: sky verify scenario support.
    describe "Sky.Build.VerifyScenario"   Sky.Build.VerifyScenarioSpec.spec
    -- Audit P3-1: sky verify covers all examples for CI.
    describe "Sky.Build.VerifyAll"        Sky.Build.VerifyAllSpec.spec
    -- Audit P3-2: LSP protocol integration.
    describe "Sky.Lsp.Protocol"           Sky.Lsp.ProtocolSpec.spec
    -- LSP per-capability extensions (definition, documentSymbol, formatting)
    describe "Sky.Lsp.Capabilities"       Sky.Lsp.CapabilitiesSpec.spec
    -- Gap 2 (soundness): LSP publishDiagnostics parity with sky check.
    describe "Sky.Lsp.Diagnostics"        Sky.Lsp.DiagnosticsSpec.spec
    describe "Sky.Lsp.HoverTypes"         Sky.Lsp.HoverTypesSpec.spec
    describe "Sky.Lsp.Completion"         Sky.Lsp.CompletionSpec.spec
    -- v0.12 gap 6: pin the externals-scope cap with a real benchmark.
    describe "Sky.Lsp.Scale"              Sky.Lsp.ScaleSpec.spec
    -- v0.13 G: end-to-end LSP coverage via headless Neovim driver.
    -- Exercises every USED symbol class: function, type alias, ADT
    -- ctor, record-field access, kernel call, lambda param, let-
    -- binding, case-pattern binder. Pending if nvim not installed.
    describe "Sky.Lsp.NvimDriver"         Sky.Lsp.NvimDriverSpec.spec
    -- Audit P3-3: embedded runtime must track on-disk tree.
    describe "Sky.Build.EmbeddedRuntime"  Sky.Build.EmbeddedRuntimeSpec.spec
    -- Embedded sky-ffi-inspect: single-binary release shape.
    describe "Sky.Build.EmbeddedInspector" Sky.Build.EmbeddedInspectorSpec.spec
    -- Per-subcommand CLI exit-code contracts.
    describe "Sky.Cli.ExitCodes"           Sky.Cli.ExitCodesSpec.spec
    describe "Sky.Cli.Init"                Sky.Cli.InitSpec.spec
    describe "Sky.Cli.Run"                 Sky.Cli.RunSpec.spec
    describe "Sky.Cli.Fmt"                 Sky.Cli.FmtSpec.spec
    describe "Sky.Cli.Clean"               Sky.Cli.CleanSpec.spec
    describe "Sky.Cli.Test"                Sky.Cli.TestSpec.spec
    -- `sky upgrade-claude` refreshes the cwd's CLAUDE.md from the
    -- binary's embedded template. Solves the staleness gap between
    -- compiler self-upgrade and project doc, which used to leave
    -- AI assistants reading deprecated API names (e.g. `Ui.max`).
    describe "Sky.Cli.UpgradeClaude"       Sky.Cli.UpgradeClaudeSpec.spec
    -- v0.11.x: `sky watch` file-watch + rebuild + restart loop.
    -- Asserts the load-bearing UX promises: initial-build banner,
    -- edit-triggers-rebuild, broken-save keeps previous binary
    -- running (the most user-visible policy).
    describe "Sky.Cli.Watch"               Sky.Cli.WatchSpec.spec
    describe "Sky.Cli.Doctor"              Sky.Cli.DoctorSpec.spec
