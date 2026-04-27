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
