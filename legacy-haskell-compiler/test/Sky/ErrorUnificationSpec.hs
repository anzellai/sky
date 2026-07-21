module Sky.ErrorUnificationSpec (spec) where

import Test.Hspec
import System.Process (readCreateProcessWithExitCode, shell)
import System.Exit (ExitCode(..))


-- | Forbidden-grep regression gate for the Sky v1 error unification.
-- Each pattern that should NEVER reappear in user-facing source after
-- the migration gets its own assertion. False positives stay possible
-- in vendored skydeps and generated sky-out artefacts; we exclude
-- those from the search.
spec :: Spec
spec = do
    describe "Sky.Core.Error is the single error surface" $ do
        it "no public Result String anywhere in src/sky-stdlib/examples src" $ do
            assertGrepClean "Result String"

        it "no public Task String anywhere in src/sky-stdlib/examples src" $ do
            assertGrepClean "Task String"

        it "Std.IoError is fully removed" $ do
            assertGrepClean "IoError"

        it "RemoteData is fully removed" $ do
            assertGrepClean "RemoteData"


-- | Run the literal grep used by the brief's acceptance gate. Any
-- match outside skydeps / sky-out / .skycache fails the test with the
-- offending lines so the regression is visible.
assertGrepClean :: String -> Expectation
assertGrepClean needle = do
    let cmd =
            "grep -rn " ++ shellEscape needle ++
            " src sky-stdlib examples 2>/dev/null" ++
            " | grep -v skydeps | grep -v sky-out | grep -v .skycache" ++
            " || true"
    (_, out, _) <- readCreateProcessWithExitCode (shell cmd) ""
    let cleaned = filter (\l -> not (null l)) (lines out)
        nonComment = filter (not . isPureComment) cleaned
    nonComment `shouldBe` []
  where
    -- Lines whose content (after the leading "path:lineno:") starts
    -- with `--` or `//` are documentation references, not active
    -- code. The brief allows comment references as long as no live
    -- code touches the symbol.
    isPureComment line =
        case dropWhile (/= ':') (drop 1 (dropWhile (/= ':') line)) of
            ':':rest ->
                let trimmed = dropWhile (\c -> c == ' ' || c == '\t') rest
                in take 2 trimmed == "--" || take 2 trimmed == "//"
            _ -> False

    shellEscape s = '\'' : concatMap esc s ++ "'"
      where
        esc '\'' = "'\\''"
        esc c    = [c]
