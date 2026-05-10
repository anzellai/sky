{-# LANGUAGE OverloadedStrings #-}
-- | LSP completion tests for the post-grilling-session paths:
--
--   * `model.<Tab>`     — record-field completion on a value
--   * `case x of <Tab>` — constructor completion in pattern
--   * `import S<Tab>`   — module-name completion in import line
--   * `Ui.<Tab>`        — module-qualified completion (regression
--                          for the post-discovery-fix path)
--
-- Each spec sets up a tiny project, sends initialize + didOpen +
-- completion, and asserts the response contains the expected items.

module Sky.Lsp.CompletionSpec (spec) where

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson (Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)

import Sky.Lsp.Harness
    ( findSky, withLsp
    , sendMsg, recvResponseFor
    , initializeLsp, didOpen
    , posRequest
    )


setupProject :: FilePath -> String -> IO FilePath
setupProject dir src = do
    let srcDir = dir </> "src"
        fixture = srcDir </> "Main.sky"
        toml = dir </> "sky.toml"
    createDirectoryIfMissing True srcDir
    writeFile toml "name = \"lsp-completion\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


-- | Extract `result.items[].label` strings from a completion response.
completionLabels :: Aeson.Value -> [T.Text]
completionLabels v = case v of
    Object o -> case KM.lookup "result" o of
        Just (Object r) -> case KM.lookup "items" r of
            Just (Array arr) -> concatMap getLabel (V.toList arr)
            _ -> []
        _ -> []
    _ -> []
  where
    getLabel (Object o) = case KM.lookup "label" o of
        Just (String t) -> [t]
        _ -> []
    getLabel _ = []


hasItem :: T.Text -> [T.Text] -> Bool
hasItem needle = any (== needle)


completionAt :: FilePath -> FilePath -> String -> Int -> Int -> IO [T.Text]
completionAt sky fixture src line col =
    withLsp sky $ \hin hout -> do
        initializeLsp hin hout
        didOpen hin fixture src
        sendMsg hin $ posRequest "textDocument/completion" 2 fixture line col
        resp <- recvResponseFor hout 2
        return (completionLabels resp)


spec :: Spec
spec = describe "LSP completion" $ do

    it "field completion on a record-typed parameter (model.<Tab>)" $ do
        sky <- findSky
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "type alias Model = { count : Int, label : String }"
                , ""
                , "stringify : Model -> String"
                , "stringify model ="
                , "    String.fromInt model."
                , ""
                , "main = println (stringify { count = 0, label = \"\" })"
                ]
        withSystemTempDirectory "sky-lsp-cmp-field" $ \dir -> do
            fixture <- setupProject dir src
            -- "    String.fromInt model."
            --  0123456789012345678901234567
            --                            ^25 (right after `.`)
            labels <- completionAt sky fixture src 10 25
            -- Field completion items now use bare-name labels (the
            -- qualified form lives in `filterText`). See
            -- Sky.Lsp.Server.fieldsToCompletions.
            hasItem "count" labels `shouldBe` True
            hasItem "label" labels `shouldBe` True

    it "module-qualified completion (Ui.<Tab>) finds Std.Ui exports" $ do
        sky <- findSky
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Std.Ui as Ui"
                , ""
                , "x = Ui."
                , ""
                , "main = println (toString x)"
                ]
        withSystemTempDirectory "sky-lsp-cmp-mod" $ \dir -> do
            fixture <- setupProject dir src
            -- "x = Ui."
            --  0123456789
            --        ^7 (right after `.`)
            labels <- completionAt sky fixture src 6 7
            -- Debug: write labels to file for inspection if test fails.
            writeFile "/tmp/sky-lsp-mod-cmp-labels.log" (show labels)
            any (T.isPrefixOf "Ui.") labels `shouldBe` True

    it "import-statement completion suggests modules" $ do
        sky <- findSky
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import S"
                , ""
                , "main = 1"
                ]
        withSystemTempDirectory "sky-lsp-cmp-imp" $ \dir -> do
            fixture <- setupProject dir src
            -- "import S"
            --  012345678
            --         ^8 (after the `S`)
            labels <- completionAt sky fixture src 2 8
            -- Should suggest at least one Std.* or Sky.* module.
            any (\l -> T.isPrefixOf "Std." l || T.isPrefixOf "Sky." l) labels
                `shouldBe` True

    it "qualified completion works on an imported-but-unreferenced module (gap 11)" $ do
        -- Regression for the externals-scope union: a file that just
        -- typed `import Std.Ui as Ui` and nothing else MUST still get
        -- `Ui.<Tab>` completion before any reference exists. Pre-fix,
        -- `collectImportNames` walked only `Can.VarTopLevel` references
        -- so Ui's symbols were absent until the user wrote a usage —
        -- a chicken-and-egg problem since the user's about to write
        -- the first reference VIA completion.
        --
        -- Note: completion uses idxByQual (always populated) so even
        -- the pre-fix LSP would have served this path; the EXTERNALS
        -- side (used for diagnostics) is what gap 11 closed. Proving
        -- the completion path stays green here pins the contract that
        -- the union-scope didn't accidentally break the simpler
        -- code path. See `collectImportNames` in Sky.Lsp.Server.
        sky <- findSky
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Std.Ui as Ui"
                , ""
                , "-- Note: no reference to `Ui` anywhere in the body."
                , "x = Ui."
                , ""
                , "main = println (toString x)"
                ]
        withSystemTempDirectory "sky-lsp-cmp-import-only" $ \dir -> do
            fixture <- setupProject dir src
            -- Line 7 0-based = `x = Ui.`
            --                   012345678
            --                         ^7 right after the dot
            labels <- completionAt sky fixture src 7 7
            any (T.isPrefixOf "Ui.") labels `shouldBe` True
