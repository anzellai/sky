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
