module Sky.Format.FormatSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc)
import System.Exit (ExitCode(..))
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BSC
import Control.Monad (when)
import qualified Data.List


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Run `sky fmt <path>` and return the resulting file contents.
runFmt :: FilePath -> String -> IO BS.ByteString
runFmt skyBin src =
    withSystemTempDirectory "sky-fmt-test" $ \dir -> do
        let file = dir </> "case.sky"
        writeFile file src
        (ec, _, err) <- readCreateProcessWithExitCode (proc skyBin ["fmt", file]) ""
        case ec of
            ExitSuccess   -> BS.readFile file
            ExitFailure n -> fail ("sky fmt exited " ++ show n ++ ": " ++ err)


-- Assert that formatting `src` twice yields the same bytes.
assertIdempotent :: FilePath -> String -> String -> Expectation
assertIdempotent skyBin label src = do
    once  <- runFmt skyBin src
    twice <- runFmt skyBin (BSC.unpack once)
    when (once /= twice) $
        expectationFailure $ unlines
            [ "Formatter not idempotent: " ++ label
            , "=== first pass ==="
            , BSC.unpack once
            , "=== second pass ==="
            , BSC.unpack twice
            ]


spec :: Spec
spec = do
    describe "Sky.Format idempotency" $ do
        sky <- runIO findSky

        -- Regression: pre-audit string-escape drop.
        it "round-trips embedded double-quotes in JSON string literals" $
            assertIdempotent sky "json-string" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "body ="
                , "    \"{\\\"status\\\":\\\"ok\\\"}\""
                ]

        -- Regression: pre-audit scientific-notation float drop.
        it "round-trips scientific-notation floats" $
            assertIdempotent sky "sci-float" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "alpha ="
                , "    5.0e-2"
                ]

        it "round-trips multiline strings with interpolation" $
            assertIdempotent sky "multiline-interp" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "render name ="
                , "    \"\"\"<h1>Hello {{name}}</h1>\"\"\""
                ]

        -- Regression for the 2026-05-18 issue: `\test` in a multiline
        -- string was being doubled to `\\test` by escapeMultilineLit.
        -- Multiline strings exist EXACTLY to preserve backslashes
        -- verbatim (JS regex, CSS, JSON, SQL) — the formatter must
        -- not interpret them.
        it "round-trips multiline strings with raw backslashes (\\test)" $
            assertIdempotent sky "multiline-backslash" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "x ="
                , "    \"\"\"\\test\"\"\""
                ]

        it "round-trips multiline strings with JS-shaped regex (\\d+)" $
            assertIdempotent sky "multiline-regex" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "x ="
                , "    \"\"\"const re = /\\d+/g\"\"\""
                ]

        it "round-trips multiline strings with JSON escapes (\\n, \\\")" $
            assertIdempotent sky "multiline-json" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "x ="
                , "    \"\"\"{\"line\":\"\\nbody\\n\",\"q\":\"\\\"\"}\"\"\""
                ]

        it "round-trips record updates" $
            assertIdempotent sky "record-update" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "update model ="
                , "    { model | count = model.count + 1 }"
                ]

        it "round-trips nested case expressions" $
            assertIdempotent sky "nested-case" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "describe x ="
                , "    case x of"
                , "        Just (Ok v) ->"
                , "            v"
                , ""
                , "        Just (Err _) ->"
                , "            \"\""
                , ""
                , "        Nothing ->"
                , "            \"\""
                ]

        it "round-trips long pipelines" $
            assertIdempotent sky "long-pipeline" $ unlines
                [ "module Test exposing (..)"
                , ""
                , ""
                , "normalise items ="
                , "    items"
                , "        |> List.filter (\\s -> True)"
                , "        |> List.map String.trim"
                , "        |> List.map String.toLower"
                ]

        -- Auto-break long imports + module exposing into multi-line.
        -- Long single-line forms (> 100 chars) get split into one
        -- export per line with leading commas; under that they stay
        -- single-line.
        it "auto-breaks an import past 100 chars into multi-line" $ do
            -- Round-trip first: format then verify the import is on
            -- multiple lines AND idempotent.
            let src = unlines
                    [ "module Test exposing (..)"
                    , ""
                    , "import Std.Html.Attributes exposing (class, id, style, type_, value, href, src, alt, name, checked, disabled, required)"
                    , ""
                    , ""
                    , "main = 1"
                    ]
            once <- runFmt sky src
            BSC.unpack once `shouldSatisfy` (\s ->
                "import Std.Html.Attributes exposing\n" `isPrefixOfLine` s)
            -- Idempotency: second pass produces identical bytes.
            twice <- runFmt sky (BSC.unpack once)
            once `shouldBe` twice

        it "leaves a short single-line import alone" $ do
            let src = unlines
                    [ "module Test exposing (..)"
                    , ""
                    , "import Std.Log exposing (println, debug)"
                    , ""
                    , ""
                    , "main = 1"
                    ]
            once <- runFmt sky src
            BSC.unpack once `shouldSatisfy`
                ("import Std.Log exposing (println, debug)" `isInfixOfLine`)

        it "auto-breaks a long module-header exposing list" $ do
            let src = unlines
                    [ "module Std.Big exposing (alpha, beta, gamma, delta, epsilon, zeta, eta, theta, iota, kappa, lambda, mu)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hi\""
                    ]
            once <- runFmt sky src
            BSC.unpack once `shouldSatisfy` (\s ->
                "module Std.Big exposing\n" `isPrefixOfLine` s)

        -- Trailing-comment relocation regression (sqlgen feedback,
        -- 2026-06-11). A `--` comment line that follows a tail-
        -- position body line used to be re-anchored to the NEXT
        -- top-level decl's header (because the walker treated any
        -- pre-decl block as a header block).  The formatter then
        -- wrote it BETWEEN the two decls with blank lines around,
        -- visually attaching to the wrong decl.  Post-fix the
        -- walker tracks `prevIsBody` and keeps trailing body
        -- comments anchored to the body line.
        it "preserves trailing body comments in tail position" $ do
            let src = unlines
                    [ "module Test exposing (..)"
                    , ""
                    , ""
                    , "foo : Int -> Int"
                    , "foo x ="
                    , "    x + 1"
                    , "    -- this trailing comment belongs to foo"
                    , ""
                    , ""
                    , "main = foo 5"
                    ]
            once <- runFmt sky src
            let body = BSC.unpack once
            -- The comment must follow `x + 1` directly, NOT land
            -- between `foo`'s body and `main`'s declaration.
            body `shouldSatisfy` (\s ->
                let ls = lines s
                    pairs = zip ls (drop 1 ls)
                in any (\(a, b) ->
                      "x + 1" `Data.List.isInfixOf` a
                          && "trailing comment" `Data.List.isInfixOf` b) pairs)
            -- Round-trip is byte-identical.
            twice <- runFmt sky body
            once `shouldBe` twice

        -- #572 regression. Pre-fix, comment blocks stranded BETWEEN
        -- list-element lines `, "foo" ++ x` were silently dropped:
        -- the prev-anchor key didn't match (formatter line-wrapped
        -- the long `++` expression) AND the next-anchor key was
        -- rejected because `nextAnchorKey` only accepted binding-
        -- shaped lines (`name = ...` / `name : T`), not list-element
        -- continuations. Furthermore, when block N's next-anchor
        -- AND block N+1's prev-anchor pointed at the same list-
        -- element line, the prev-anchor stole the line and starved
        -- block N's next-anchor. The fix accepts comma-prefixed
        -- lines as next-anchors (keyed on full stripped text), AND
        -- lets both anchors fire on the same line (next-anchor
        -- before line, prev-anchor after).
        it "preserves comment blocks stranded between list elements (#572)" $ do
            let src = unlines
                    [ "module Test exposing (..)"
                    , ""
                    , ""
                    , "envVars appId depId sourceBucket slug ="
                    , "    String.join"
                    , "        \",\""
                    , "        [ \"APP_ID=\" ++ String.fromInt appId"
                    , "        , \"DEPLOYMENT_ID=\" ++ String.fromInt depId"
                    , "        , \"SOURCE_BUCKET=\" ++ sourceBucket ++ \"-suffix-makes-it-wrap-past-100\""
                    , "        -- Block 1: shared admin secret doc."
                    , "        -- The runtime uses it to verify the JWT."
                    , "        -- Two-line doc anchored above the next list element."
                    , "        , \"SKY_ADMIN_SECRET_NAME=skydeploy-platform-admin-token\""
                    , "        -- Block 2: long-lived deploy flag."
                    , "        -- Set on every deploy alongside the console secret."
                    , "        , \"SKY_RUNTIME_MODE=longlived\""
                    , "        , \"ENV=production\""
                    , "        ]"
                    ]
            once <- runFmt sky src
            let body = BSC.unpack once
            -- Block 1 + Block 2 + Block 3 must all survive.
            body `shouldSatisfy`
                ("-- Block 1: shared admin secret doc." `isInfixOfLine`)
            body `shouldSatisfy`
                ("-- The runtime uses it to verify the JWT." `isInfixOfLine`)
            body `shouldSatisfy`
                ("-- Two-line doc anchored above the next list element." `isInfixOfLine`)
            body `shouldSatisfy`
                ("-- Block 2: long-lived deploy flag." `isInfixOfLine`)
            body `shouldSatisfy`
                ("-- Set on every deploy alongside the console secret." `isInfixOfLine`)
            -- Round-trip idempotency.
            twice <- runFmt sky body
            once `shouldBe` twice


-- Tiny helpers — `isPrefixOfLine` checks that some line in the
-- output starts with the given needle; `isInfixOfLine` does
-- substring containment line by line. The needle includes a
-- trailing "\n" in the prefix-form for readability; we strip it
-- before per-line matching.
isPrefixOfLine :: String -> String -> Bool
isPrefixOfLine needleNL hay =
    let needle = stripTrailingNL needleNL
    in any (needle `Data.List.isPrefixOf`) (lines hay)
  where
    stripTrailingNL s = case reverse s of
        '\n':rest -> reverse rest
        _ -> s


isInfixOfLine :: String -> String -> Bool
isInfixOfLine needle hay = any (needle `Data.List.isInfixOf`) (lines hay)
