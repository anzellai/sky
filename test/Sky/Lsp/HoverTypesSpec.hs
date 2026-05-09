{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.HoverTypesSpec (spec) where

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson (Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
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
    writeFile toml "name = \"lsp-hover\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


hoverContent :: Aeson.Value -> Maybe T.Text
hoverContent v = case v of
    Object o -> case KM.lookup "result" o of
        Just (Object r) -> case KM.lookup "contents" r of
            Just (Object c) -> case KM.lookup "value" c of
                Just (String t) -> Just t
                _ -> Nothing
            Just (String t) -> Just t
            _ -> Nothing
        _ -> Nothing
    _ -> Nothing


hoverAt :: T.Text -> FilePath -> Int -> Int -> IO (Maybe T.Text)
hoverAt skyBin fixture line col = do
    withLsp (T.unpack skyBin) $ \hin hout -> do
        initializeLsp hin hout
        src <- readFile fixture
        didOpen hin fixture src
        sendMsg hin $ posRequest "textDocument/hover" 2 fixture line col
        resp <- recvResponseFor hout 2
        return (hoverContent resp)


spec :: Spec
spec = do
    describe "LSP hover shows type signatures" $ do

        it "annotated top-level function shows its annotation" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main, greet)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "greet : String -> String"
                    , "greet name ="
                    , "    \"Hello, \" ++ name"
                    , ""
                    , "main = println (greet \"world\")"
                    ]
            withSystemTempDirectory "sky-lsp-hover-annot" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    sendMsg hin $ posRequest "textDocument/hover" 2 fixture 6 0
                    resp <- recvResponseFor hout 2
                    let content = hoverContent resp
                    case content of
                        Just txt -> do
                            txt `shouldSatisfy` T.isInfixOf "String -> String"
                        Nothing -> expectationFailure "hover returned no content"

        it "inferred top-level function shows inferred type" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "add x y = x + y"
                    , ""
                    , "main = println (String.fromInt (add 1 2))"
                    ]
            withSystemTempDirectory "sky-lsp-hover-infer" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    sendMsg hin $ posRequest "textDocument/hover" 2 fixture 5 0
                    resp <- recvResponseFor hout 2
                    let content = hoverContent resp
                    case content of
                        Just txt -> do
                            txt `shouldSatisfy` \t ->
                                T.isInfixOf ":" t
                                && not (T.isInfixOf "add\n" t)
                        Nothing -> expectationFailure "hover returned no content"

        it "prelude builtin (println) shows type" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hi\""
                    ]
            withSystemTempDirectory "sky-lsp-hover-println" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    sendMsg hin $ posRequest "textDocument/hover" 2 fixture 4 7
                    resp <- recvResponseFor hout 2
                    let content = hoverContent resp
                    case content of
                        Just txt -> do
                            txt `shouldSatisfy` \t ->
                                T.isInfixOf ":" t
                                && T.isInfixOf "println" t
                        Nothing -> expectationFailure "hover returned no content for println"

        it "ADT constructor shows its type" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Colour = Red | Green | Blue"
                    , ""
                    , "main = println (toString Red)"
                    ]
            withSystemTempDirectory "sky-lsp-hover-ctor" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    sendMsg hin $ posRequest "textDocument/hover" 2 fixture 7 25
                    resp <- recvResponseFor hout 2
                    let content = hoverContent resp
                    case content of
                        Just txt -> do
                            txt `shouldSatisfy` \t ->
                                T.isInfixOf "Red" t
                                && T.isInfixOf "Colour" t
                        Nothing -> expectationFailure "hover returned no content for Red"

        it "field access (model.count) shows the field's type" $ do
            -- Hover on `.count` in `model.count`. New walker emits the
            -- field name as a `.field` ident; computeHoverIdx routes
            -- it through resolveFieldType which reads model's
            -- annotation, finds the Model alias, and returns Int.
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
                    , "    String.fromInt model.count"
                    , ""
                    , "main = println (stringify { count = 42, label = \"x\" })"
                    ]
            withSystemTempDirectory "sky-lsp-hover-field" $ \dir -> do
                fixture <- setupProject dir src
                content <- hoverAt (T.pack sky) fixture 10 26
                case content of
                    Just txt -> do
                        T.isInfixOf "count" txt `shouldBe` True
                        T.isInfixOf "Int" txt `shouldBe` True
                    Nothing  -> expectationFailure "no hover content for .count"

        it "binop operator (|>) shows its signature" $ do
            -- Hover on `|>` between two expressions.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println (\"x\" |> String.toUpper)"
                    ]
            withSystemTempDirectory "sky-lsp-hover-pipe" $ \dir -> do
                fixture <- setupProject dir src
                content <- hoverAt (T.pack sky) fixture 6 21
                case content of
                    Just txt ->
                        (T.isInfixOf "|>" txt || T.isInfixOf "->" txt) `shouldBe` True
                    Nothing  -> expectationFailure "no hover content for |>"
