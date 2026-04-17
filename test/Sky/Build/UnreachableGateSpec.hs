module Sky.Build.UnreachableGateSpec (spec) where

-- Audit P0-5: codegen case-fallback panics must route through
-- rt.Unreachable (catchable, logs site, converts to Err via rt
-- panic-recovery) instead of raw `panic("sky: internal …")` string
-- that crashes the process unless a specific outer handler is in
-- place. This spec greps the committed example builds for the
-- forbidden raw-panic string.

import Test.Hspec
import Data.List (isInfixOf)

spec :: Spec
spec = do
    describe "no raw 'unreachable case arm' panics in emitted Go (audit P0-5)" $ do
        it "examples/12-skyvote/sky-out/main.go uses rt.Unreachable, not raw panic" $
            assertGateClean "examples/12-skyvote/sky-out/main.go"
        it "examples/16-skychess/sky-out/main.go uses rt.Unreachable" $
            assertGateClean "examples/16-skychess/sky-out/main.go"
        it "examples/15-http-server/sky-out/main.go uses rt.Unreachable" $
            assertGateClean "examples/15-http-server/sky-out/main.go"


-- The ExampleSweep spec runs first and rebuilds every example, so by
-- the time this spec runs the sky-out/main.go files reflect the
-- current compiler.
assertGateClean :: FilePath -> Expectation
assertGateClean path = do
    contents <- readFile path
    let forbidden = "panic(\"sky: internal"
        required  = "rt.Unreachable("
    (forbidden `isInfixOf` contents) `shouldBe` False
    -- Every example has at least one case fallback, so a fresh build
    -- must produce at least one rt.Unreachable reference.
    (required `isInfixOf` contents) `shouldBe` True
