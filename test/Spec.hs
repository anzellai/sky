module Main (main) where

import Test.Hspec
import qualified Sky.Build.CompileSpec
import qualified Sky.Build.ExampleSweepSpec
import qualified Sky.Build.TypedFfiSpec
import qualified Sky.ErrorUnificationSpec
import qualified Sky.Parse.PatternSpec
import qualified Sky.Canonicalise.ExposingSpec
import qualified Sky.Type.ExhaustivenessSpec
import qualified Sky.Format.FormatSpec
import qualified Sky.Build.NestedPatternSpec
import qualified Sky.Build.CheckIsBuildSpec
import qualified Sky.Build.RecordFieldOrderSpec
import qualified Sky.Build.UnreachableGateSpec
import qualified Sky.Parse.CommentsSpec
import qualified Sky.Lsp.HoverShadowingSpec
import qualified Sky.Lsp.RenameStableSpec
import qualified Sky.Build.VerifyScenarioSpec
import qualified Sky.Build.VerifyAllSpec
import qualified Sky.Lsp.ProtocolSpec
import qualified Sky.Build.EmbeddedRuntimeSpec
import qualified Sky.Build.EmbeddedInspectorSpec

main :: IO ()
main = hspec $ do
    describe "Sky.Build.Compile"         Sky.Build.CompileSpec.spec
    describe "Sky.Parse.Pattern"         Sky.Parse.PatternSpec.spec
    describe "Sky.Canonicalise.Exposing" Sky.Canonicalise.ExposingSpec.spec
    describe "Sky.Type.Exhaustiveness"   Sky.Type.ExhaustivenessSpec.spec
    describe "Sky.Format.Format"         Sky.Format.FormatSpec.spec
    describe "Sky.Build.NestedPattern"   Sky.Build.NestedPatternSpec.spec
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
    -- Audit P3-3: embedded runtime must track on-disk tree.
    describe "Sky.Build.EmbeddedRuntime"  Sky.Build.EmbeddedRuntimeSpec.spec
    -- Embedded sky-ffi-inspect: single-binary release shape.
    describe "Sky.Build.EmbeddedInspector" Sky.Build.EmbeddedInspectorSpec.spec
