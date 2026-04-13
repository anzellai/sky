module Main (main) where

import Test.Hspec
import qualified Sky.Build.CompileSpec
import qualified Sky.Build.ExampleSweepSpec
import qualified Sky.Parse.PatternSpec
import qualified Sky.Canonicalise.ExposingSpec

main :: IO ()
main = hspec $ do
    describe "Sky.Build.Compile"        Sky.Build.CompileSpec.spec
    describe "Sky.Parse.Pattern"        Sky.Parse.PatternSpec.spec
    describe "Sky.Canonicalise.Exposing" Sky.Canonicalise.ExposingSpec.spec
    describe "Sky.Build.ExampleSweep"   Sky.Build.ExampleSweepSpec.spec
