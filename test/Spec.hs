module Main (main) where

import Test.Hspec
import qualified Sky.Build.CompileSpec
import qualified Sky.Build.DepHmFatalSpec
import qualified Sky.Build.ExampleSweepSpec
import qualified Sky.Build.ForeignFatalSpec
import qualified Sky.Build.TypedFfiSpec
import qualified Sky.ErrorUnificationSpec
import qualified Sky.Parse.PatternSpec
import qualified Sky.Parse.MultiLineExposingSpec
import qualified Sky.Canonicalise.ExposingSpec
import qualified Sky.Canonicalise.KernelFallbackSpec
import qualified Sky.Canonicalise.UnboundSpec
import qualified Sky.Type.ExhaustivenessSpec
import qualified Sky.Type.AnyWildcardSpec
import qualified Sky.Format.FormatSpec
import qualified Sky.Build.NestedPatternSpec
import qualified Sky.Build.ConsCtorPatternSpec
import qualified Sky.Build.TaskResultBridgesSpec
import qualified Sky.Build.CheckIsBuildSpec
import qualified Sky.Build.RecordFieldOrderSpec
import qualified Sky.Build.RecordCtorEmptyListSpec
import qualified Sky.Build.HofTypedMsgSpec
import qualified Sky.Build.KernelSigCoverageSpec
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
import qualified Sky.Build.EmbeddedRuntimeSpec
import qualified Sky.Build.EmbeddedInspectorSpec
import qualified Sky.Cli.ExitCodesSpec
import qualified Sky.Cli.InitSpec
import qualified Sky.Cli.RunSpec
import qualified Sky.Cli.FmtSpec
import qualified Sky.Cli.CleanSpec
import qualified Sky.Cli.TestSpec

main :: IO ()
main = hspec $ do
    describe "Sky.Build.Compile"         Sky.Build.CompileSpec.spec
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
    describe "Sky.Canonicalise.Exposing" Sky.Canonicalise.ExposingSpec.spec
    -- Regression: kernel qualifiers (Crypto, Encoding, Hex, …) used
    -- without an explicit `import Sky.Core.<Mod>` must resolve as
    -- VarKernel, not VarTopLevel — otherwise the lowerer ships
    -- `Crypto_sha256(arg)` (no `rt.` prefix) and `go build` fails.
    describe "Sky.Canonicalise.KernelFallback" Sky.Canonicalise.KernelFallbackSpec.spec
    describe "Sky.Canonicalise.Unbound"  Sky.Canonicalise.UnboundSpec.spec
    describe "Sky.Type.Exhaustiveness"   Sky.Type.ExhaustivenessSpec.spec
    -- Cross-branch HM `any` wildcard fix (compiler bug #3). Distinct
    -- occurrences of `any` in source types must NOT share a single
    -- unification variable; each gets its own fresh var.
    describe "Sky.Type.AnyWildcard"      Sky.Type.AnyWildcardSpec.spec
    describe "Sky.Format.Format"         Sky.Format.FormatSpec.spec
    describe "Sky.Build.NestedPattern"   Sky.Build.NestedPatternSpec.spec
    -- Cons-with-constructor pattern fix (compiler bug #2). The
    -- lowerer now emits a head-discriminator check on `(Ctor x) :: rest`
    -- so the body only fires when the head's actual ctor matches.
    describe "Sky.Build.ConsCtorPattern" Sky.Build.ConsCtorPatternSpec.spec
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
    -- Limitation #18 (other half): renderHofParamTy used to hardcode
    -- the inner-function return as `any`, breaking helpers with typed
    -- (String -> Msg) callbacks. Now routes via typeStrWithAliasesReg.
    describe "Sky.Build.HofTypedMsg"        Sky.Build.HofTypedMsgSpec.spec
    -- Limitation #16: kernel-sig coverage for the dangerous-class
    -- gaps (returns Maybe/Result/Task wrappers OR opaque FFI types).
    -- Without HM sigs, user pattern-matching against the wrapper
    -- silently degrades to `any` and surfaces as runtime panics.
    describe "Sky.Build.KernelSigCoverage" Sky.Build.KernelSigCoverageSpec.spec
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
