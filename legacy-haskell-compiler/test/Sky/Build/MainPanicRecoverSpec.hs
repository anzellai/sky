-- MainPanicRecoverSpec — Cycle 6 PC (v0.15.43) verifies the emitted
-- Go `func main()` starts with `defer rt.LogPanicAndExit()`.
--
-- This is the codegen anchor that wires the top-level panic→Err
-- recovery. Regressions here would silently re-expose the
-- synchronous-panic class (CLI / Tui / batch process crashes with
-- a Go stack dump instead of a structured-log line).
--
-- The runtime-side classifyPanic / compressStack / newErrId
-- behaviour is covered by runtime-go/rt/panic_recover_test.go.
-- This spec pins the COMPILER side: a vanilla Sky main builds
-- a main.go whose func main()'s first statement is the deferred
-- call.
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `compileInProcess`.  The companion runtime test
-- ("div-by-zero at top level exits 1 with a structured-log line")
-- has been REMOVED here — that assertion fundamentally needs to
-- actually run the binary (so needs `go build` + execution).  Its
-- coverage moves to runtime-go/rt/panic_recover_test.go (already
-- exercises the runtime-side classifyPanic / structured-log paths)
-- + the example sweep (which runs every example and would catch a
-- regression).
module Sky.Build.MainPanicRecoverSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "func main() panic-recover wrapper" $ do
        it "emits `defer rt.LogPanicAndExit()` as the first stmt of func main()" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hello\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk contents -> do
                    -- The defer is injected as the very first statement
                    -- of the emitted func main(). Order matters — a deferred
                    -- LogPanicAndExit after any other panicking call would
                    -- miss panics fired before the defer is registered.
                    let lns = lines contents
                    let mainStart =
                            dropWhile (\l -> not ("func main()" `isInfixOf` l)) lns
                    case mainStart of
                        (_funcLine : firstStmt : _) ->
                            ("defer rt.LogPanicAndExit()" `isInfixOf` firstStmt)
                                `shouldBe` True
                        _ -> expectationFailure ("no func main() found in:\n" ++ contents)
