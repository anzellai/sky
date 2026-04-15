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
