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

main :: IO ()
main = hspec $ do
    describe "Sky.Build.Compile"         Sky.Build.CompileSpec.spec
    describe "Sky.Parse.Pattern"         Sky.Parse.PatternSpec.spec
    describe "Sky.Canonicalise.Exposing" Sky.Canonicalise.ExposingSpec.spec
    describe "Sky.Type.Exhaustiveness"   Sky.Type.ExhaustivenessSpec.spec
    describe "Sky.Format.Format"         Sky.Format.FormatSpec.spec
    describe "Sky.Build.NestedPattern"   Sky.Build.NestedPatternSpec.spec
    describe "Sky.Build.TypedFfi"        Sky.Build.TypedFfiSpec.spec
    describe "Sky.ErrorUnification"      Sky.ErrorUnificationSpec.spec
    describe "Sky.Build.ExampleSweep"    Sky.Build.ExampleSweepSpec.spec
