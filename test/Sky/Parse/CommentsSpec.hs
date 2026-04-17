module Sky.Parse.CommentsSpec (spec) where

-- Audit P2-1: comments now live in Src.Module._comments (populated
-- by Parse.Module's post-scan). End-to-end check via `sky fmt --stdin`
-- that source with comments at various positions round-trips:
-- every comment in the input must be present in the output.
--
-- The exact emission layout is still handled by the preserveTopLevelComments
-- post-pass in app/Main.hs (follow-up work will retire it entirely
-- once Format.hs grows per-declaration comment slots); this spec
-- locks the invariant "comments are not dropped" regardless of
-- which stage places them.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Feed `src` to `sky fmt --stdin` with SKY_FMT_FORCE=1 so the
-- safety guard doesn't short-circuit the test harness.
fmtStdin :: String -> IO String
fmtStdin src = do
    sky <- findSky
    (_ec, out, _err) <- readCreateProcessWithExitCode
        (shell ("SKY_FMT_FORCE=1 " ++ sky ++ " fmt --stdin"))
        src
    return out


countOccurrences :: String -> String -> Int
countOccurrences needle haystack
    | length haystack < length needle = 0
    | take (length needle) haystack == needle =
        1 + countOccurrences needle (drop 1 haystack)
    | otherwise = countOccurrences needle (drop 1 haystack)


spec :: Spec
spec = do
    describe "comments survive sky fmt (audit P2-1)" $ do

        it "top-level comment above module preserved" $ do
            out <- fmtStdin $ unlines
                [ "-- Banner comment"
                , "module M exposing (..)"
                , ""
                , "x = 1"
                ]
            ("Banner comment" `isInfixOf` out) `shouldBe` True

        it "comment above a top-level value preserved" $ do
            out <- fmtStdin $ unlines
                [ "module M exposing (..)"
                , ""
                , "-- Doc for fn"
                , "fn = 42"
                ]
            ("Doc for fn" `isInfixOf` out) `shouldBe` True

        it "multiple comments all preserved (count invariant)" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "-- first"
                    , "a = 1"
                    , ""
                    , "-- second"
                    , "b = 2"
                    , ""
                    , "-- third"
                    , "c = 3"
                    ]
            out <- fmtStdin src
            countOccurrences "-- first"  out `shouldBe` 1
            countOccurrences "-- second" out `shouldBe` 1
            countOccurrences "-- third"  out `shouldBe` 1

        it "does not treat `--` inside a string as a comment" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "x = \"not -- a comment\""
                    ]
            out <- fmtStdin src
            -- The string literal must round-trip with its `--` intact.
            ("\"not -- a comment\"" `isInfixOf` out) `shouldBe` True
