module Sky.Build.ForeignFatalSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- Regression: foreign-call mismatches are fatal (v0.10.0+).  Pre-fix
-- the solver swallowed CForeign mismatches with the comment
-- `Continue past foreign mismatch for now`.  The bug shipped as
-- `rt.AsBool: expected bool, got rt.SkyResult[…]` runtime panics in
-- sky-log's Webhook.matchesFilter — `Regexp.regexpMatchString`
-- returned `Result Error Bool` but was used as bare `Bool`.  Now the
-- build aborts with a TYPE ERROR.
--
-- Tier 1 (task #491): migrated from subprocess `sky build` of an
-- on-disk fixture to in-process `compileInProcess` with the fixture
-- source inlined.  Source is byte-identical to
-- `test/fixtures/foreign-fatal/src/Main.sky`.
spec :: Spec
spec = do
    describe "Foreign-call mismatches are fatal (v0.10.0+)" $ do
        it "build aborts with a Type mismatch when an FFI / kernel return-shape doesn't unify" $ do
            result <- compileInProcess mainSrc
            case result of
                CompileOk _ ->
                    expectationFailure "expected foreign-mismatch failure but compile succeeded"
                CompileErr combined -> do
                    combined `shouldSatisfy` ("TYPE ERROR" `isInfixOf`)
                    -- One of these phrases must appear — either the
                    -- direct Foreign mismatch report, or the equivalent
                    -- Type mismatch surfaced via the CEqual constraint
                    -- on the same expression.
                    combined `shouldSatisfy` \s ->
                        "Foreign" `isInfixOf` s
                        || "Type mismatch" `isInfixOf` s


mainSrc :: String
mainSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "{-|"
    , "Regression for \"Foreign mismatches silently swallowed at HM time\""
    , "(v0.10.0 fix). Pre-fix, calling a kernel/FFI function with a"
    , "return-shape that didn't unify with the call-site's expected type"
    , "was permitted by the constraint solver — the error report was"
    , "commented out. The bug surfaced as a runtime panic like"
    , ""
    , "    rt.AsBool: expected bool, got rt.SkyResult[interface {}, bool]"
    , ""
    , "(sky-log's `Webhook.matchesFilter` shape) or worse, silent data"
    , "corruption."
    , ""
    , "This fixture asks for `String.length : String -> Int` to be used as"
    , "`Bool`. The CForeign unification fails — and now that's a fatal"
    , "build error rather than a silently-swallowed mismatch."
    , "-}"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.System as System"
    , ""
    , ""
    , "-- System.cwd : () -> Task Error String — annotation here forces the"
    , "-- consumer's expectation. Pre-fix this body type-errored at the"
    , "-- foreign-unify step (`Task` returned by System.cwd vs `String`"
    , "-- demanded by the annotation), but the solver swallowed the error."
    , "-- The function then shipped with broken types and the runtime"
    , "-- panicked when callers tried to use the value."
    , "fetch : () -> String"
    , "fetch _ ="
    , "    System.cwd ()"
    , ""
    , ""
    , "main ="
    , "    System.exit 0"
    ]
