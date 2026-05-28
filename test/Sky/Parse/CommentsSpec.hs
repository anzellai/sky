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

        -- Regression (2026-05-18): pre-fix `spaces` only consumed
        -- ' ' and '\t' — an inline `-- comment` after a token was
        -- left in the stream, and the expression parser then read
        -- the `--` as subtraction, producing baffling "Undefined
        -- name: foo" errors where `foo` was a word in the comment.
        --
        -- Fix lives in src/Sky/Parse/Space.hs (`spaces` now
        -- consumes inline `-- comment` content up to but not past
        -- the next newline, so layout still sees the line break).
        it "ignores `-- comment` after value on the same line" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "x ="
                    , "    \"hello\"   -- inline comment after string"
                    ]
            out <- fmtStdin src
            -- The literal survives + the formatter dropped the
            -- inline comment (formatter only carries top-level
            -- comments through). Crucially: this no longer errors
            -- with `Undefined name: inline`.
            ("\"hello\"" `isInfixOf` out) `shouldBe` True

        it "ignores `-- comment` inside record literal" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "settings ="
                    , "    { name = \"x\"        -- a label"
                    , "    , port = 8080        -- standard"
                    , "    , debug = False"
                    , "    }"
                    ]
            out <- fmtStdin src
            -- Each field roundtrips cleanly; no compile error.
            ("name = " `isInfixOf` out) `shouldBe` True
            ("port = 8080" `isInfixOf` out) `shouldBe` True
            ("debug = False" `isInfixOf` out) `shouldBe` True

        -- Regression (2026-05-20): `collectComments`' string scanner
        -- did not handle triple-quoted strings — `skipString` bailed
        -- on the first newline, so `--`-prefixed lines INSIDE a `"""`
        -- multiline string were mis-collected as comments AND
        -- duplicated on every `sky fmt` round-trip (a CLI's help text
        -- grew unboundedly). Fix: `collectComments` in
        -- src/Sky/Parse/Module.hs now has a `skipTriple` arm that
        -- treats newlines as content and ends only on `"""`.
        it "does not collect/duplicate `--` lines inside a multiline string" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "helpText ="
                    , "    \"\"\"USAGE"
                    , "--dry-run    print without running"
                    , "--verbose    extra logging"
                    , "\"\"\""
                    ]
            out <- fmtStdin src
            -- Each flag line appears exactly once — not lifted out
            -- as a comment, not duplicated.
            countOccurrences "--dry-run" out `shouldBe` 1
            countOccurrences "--verbose" out `shouldBe` 1

        it "is idempotent across two fmt passes for a multiline string" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "helpText ="
                    , "    \"\"\"USAGE"
                    , "--dry-run    print without running"
                    , "\"\"\""
                    ]
            once <- fmtStdin src
            twice <- fmtStdin once
            twice `shouldBe` once

        -- Regression (2026-05-20): `injectComments` matched `declKey`
        -- on every output line, so a top-level function's header
        -- comment was spliced before the first *call* of that
        -- function (an indented use site) rather than its
        -- definition. Fixed by gating `headerHit` on
        -- `isTopLevelDecl` in app/Main.hs.
        it "header comment stays above the definition, not a call site" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , "run x ="
                    , "    helper x"
                    , ""
                    , ""
                    , "-- doc for helper"
                    , "helper y ="
                    , "    y"
                    ]
            out <- fmtStdin src
            -- The comment sits directly above the definition.
            ("-- doc for helper\nhelper y" `isInfixOf` out) `shouldBe` True
            -- ...and NOT above the call site inside `run`.
            ("-- doc for helper\n    helper" `isInfixOf` out) `shouldBe` False

        -- Regression (2026-05-28, #353): body comments anchored to a
        -- preceding code line whose shape changes under `sky fmt`
        -- used to vanish from the output. The post-pass keyed
        -- comments by the stripped text of the previous code line;
        -- when that line got reflowed (e.g. a multi-segment string
        -- concat collapsed onto fewer lines, or a `case ... of`
        -- subject re-wrapped), the anchor never matched and the
        -- comment fell on the floor.
        --
        -- Fix: anchor body comments by EITHER the preceding code
        -- line OR the next code line (let-binding name, branch
        -- pattern, etc.) — the next-anchor survives reformatting of
        -- the previous expression. Reported via
        -- skydeploy/control-plane/src/Tools.sky, where the
        -- `-- Step 3` block above `tarCmd =` (inside a `let`) was
        -- being dropped because the multi-line `prepCmd` string
        -- above got reflowed.
        it "body comment above a let-binding survives reflow of the previous expression" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , ""
                    , "fn dir ="
                    , "    let"
                    , "        prepCmd ="
                    , "            \"rm -rf \" ++ dir"
                    , "                ++ \" && mkdir -p \" ++ dir"
                    , "                ++ \" && cp -a \" ++ dir ++ \"/. \" ++ dir ++ \"/\""
                    , ""
                    , "        -- Step 3: tar the WORKING COPY (not the original source)."
                    , "        -- Same excludes as before."
                    , "        tarCmd ="
                    , "            \"tar -czf -\""
                    , "    in"
                    , "        prepCmd ++ tarCmd"
                    ]
            out <- fmtStdin src
            -- Both comments survive — anchored to the `tarCmd =`
            -- line below them rather than the `prepCmd ++ "..."` lines above.
            countOccurrences "-- Step 3: tar the WORKING COPY" out `shouldBe` 1
            countOccurrences "-- Same excludes as before." out `shouldBe` 1
            -- And they appear ABOVE `tarCmd =`, not above some unrelated line.
            ("-- Same excludes as before.\n        tarCmd " `isInfixOf` out) `shouldBe` True

        -- Regression (2026-05-28, #353): idempotency for a file with
        -- body comments whose anchor needs the next-line fallback.
        -- A second `sky fmt` pass must produce byte-identical output.
        it "is idempotent for body comments anchored by the next line" $ do
            let src = unlines
                    [ "module M exposing (..)"
                    , ""
                    , ""
                    , "fn dir ="
                    , "    let"
                    , "        prepCmd ="
                    , "            \"rm -rf \" ++ dir"
                    , "                ++ \" && mkdir -p \" ++ dir"
                    , ""
                    , "        -- doc for tarCmd"
                    , "        tarCmd ="
                    , "            \"tar -czf -\""
                    , "    in"
                    , "        prepCmd ++ tarCmd"
                    ]
            once <- fmtStdin src
            twice <- fmtStdin once
            twice `shouldBe` once
