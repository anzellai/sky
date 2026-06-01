{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.CallHierarchySpec (spec) where

-- v0.15.50 — end-to-end specs for the three LSP call-hierarchy
-- methods plus the capability advert. Spawns `sky lsp`, sets up a
-- small project with caller / callee relationships, and asserts
-- the response shape on:
--
--   * initialize advertises callHierarchyProvider = true
--   * textDocument/prepareCallHierarchy on a callable returns a
--     CallHierarchyItem with the right name + selectionRange
--   * callHierarchy/incomingCalls returns the calling top-level
--     binding's item with the correct caller name + non-empty
--     fromRanges
--   * callHierarchy/outgoingCalls returns the called top-level
--     binding's item with the correct callee name
--   * an out-of-place cursor returns an empty array (no crash)

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson ((.=), Value(..))
import qualified Data.Aeson.Key as AK
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


-- ── Fixture project ──────────────────────────────────────────────

setupProject :: FilePath -> String -> IO FilePath
setupProject dir src = do
    let srcDir = dir </> "src"
        fixture = srcDir </> "Main.sky"
        toml = dir </> "sky.toml"
    createDirectoryIfMissing True srcDir
    writeFile toml "name = \"lsp-ch\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


-- Two callers (`shout`, `whisper`) of one callee (`greet`),
-- and one external (`println`) on the main entry. Gives us
-- a 2-caller / 1-callee shape so the test isn't trivially
-- symmetric.
sampleSrc :: String
sampleSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "greet : String -> String"
    , "greet name ="
    , "    \"Hello, \" ++ name"
    , ""
    , "shout : String -> String"
    , "shout n ="
    , "    greet n ++ \"!\""
    , ""
    , "whisper : String -> String"
    , "whisper n ="
    , "    \"(\" ++ greet n ++ \")\""
    , ""
    , "main = println (shout \"world\")"
    ]


-- ── Helpers ──────────────────────────────────────────────────────

resultObject :: Value -> Maybe Value
resultObject v = case v of
    Object o -> KM.lookup "result" o
    _        -> Nothing


resultArray :: Value -> [Value]
resultArray v = case resultObject v of
    Just (Array a) -> V.toList a
    _              -> []


itemName :: Value -> Maybe T.Text
itemName v = case v of
    Object o -> case KM.lookup "name" o of
        Just (String s) -> Just s
        _               -> Nothing
    _ -> Nothing


-- Keyed lookup on an Object value.
lookupKey :: T.Text -> Value -> Maybe Value
lookupKey k v = case v of
    Object o -> KM.lookup (AK.fromText k) o
    _ -> Nothing


-- Extract fromRanges / fromRanges array from an incoming/outgoing
-- entry — both shapes carry it under "fromRanges".
fromRanges :: Value -> [Value]
fromRanges v = case lookupKey "fromRanges" v of
    Just (Array a) -> V.toList a
    _ -> []


spec :: Spec
spec = do
    describe "LSP call-hierarchy (v0.15.50)" $ do

        it "initialize advertises callHierarchyProvider" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-ch-cap" $ \dir -> do
                _ <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (1 :: Int)
                        , "method"  .= ("initialize" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "processId" .= Aeson.Null
                            , "rootUri"   .= Aeson.Null
                            , "capabilities" .= Aeson.object []
                            ]
                        ]
                    resp <- recvResponseFor hout 1
                    let provider = do
                            r <- resultObject resp
                            caps <- lookupKey "capabilities" r
                            lookupKey "callHierarchyProvider" caps
                    case provider of
                        Just (Bool True) -> return ()
                        other -> expectationFailure $
                            "expected callHierarchyProvider=true, got: " ++ show other

        it "prepareCallHierarchy returns a CallHierarchyItem for a top-level callable" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-ch-prep" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    -- `greet` declared at line 7 (0-based 6), col 0..5.
                    -- Place cursor mid-identifier (col 2 = inside "greet").
                    sendMsg hin $ posRequest "textDocument/prepareCallHierarchy"
                                              2 fixture 6 2
                    resp <- recvResponseFor hout 2
                    let items = resultArray resp
                    case items of
                        []     -> expectationFailure $
                            "prepare returned empty array, got: " ++ show resp
                        (i:_) -> case itemName i of
                            Just "greet" -> return ()
                            other -> expectationFailure $
                                "expected item.name=greet, got: " ++ show other

        it "incomingCalls returns the two callers of greet" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-ch-in" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    -- Build a CallHierarchyItem for greet by hand —
                    -- matches what prepareCallHierarchy would emit
                    -- on its declaration region (line 7 1-based).
                    let item = Aeson.object
                            [ "name" .= ("greet" :: T.Text)
                            , "kind" .= (12 :: Int)
                            , "uri"  .= ("file://" ++ fixture)
                            , "range" .= rangeFromTo 6 0 6 5
                            , "selectionRange" .= rangeFromTo 6 0 6 5
                            ]
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (3 :: Int)
                        , "method"  .= ("callHierarchy/incomingCalls" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "item" .= item ]
                        ]
                    resp <- recvResponseFor hout 3
                    let entries = resultArray resp
                        callerNames =
                            [ n
                            | e <- entries
                            , Just fromItem <- [lookupKey "from" e]
                            , Just n <- [itemName fromItem]
                            ]
                    -- Both shout and whisper call greet.
                    ("shout"   `elem` callerNames) `shouldBe` True
                    ("whisper" `elem` callerNames) `shouldBe` True
                    -- Each entry MUST carry a non-empty fromRanges array.
                    let allHaveRanges = all (not . null . fromRanges) entries
                    allHaveRanges `shouldBe` True

        it "outgoingCalls returns the callees of shout" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-ch-out" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    -- Hand-built CallHierarchyItem for shout (line 11
                    -- 1-based). The outgoing handler reads the
                    -- name + uri from this item and re-parses the
                    -- source to find shout's body.
                    let item = Aeson.object
                            [ "name" .= ("shout" :: T.Text)
                            , "kind" .= (12 :: Int)
                            , "uri"  .= ("file://" ++ fixture)
                            , "range" .= rangeFromTo 10 0 10 5
                            , "selectionRange" .= rangeFromTo 10 0 10 5
                            ]
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (4 :: Int)
                        , "method"  .= ("callHierarchy/outgoingCalls" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "item" .= item ]
                        ]
                    resp <- recvResponseFor hout 4
                    let entries = resultArray resp
                        calleeNames =
                            [ n
                            | e <- entries
                            , Just toItem <- [lookupKey "to" e]
                            , Just n <- [itemName toItem]
                            ]
                    -- shout calls greet. (`++` is an operator, not a
                    -- callable symbol; the resolver filters it out.)
                    ("greet" `elem` calleeNames) `shouldBe` True

        it "prepareCallHierarchy on whitespace returns []" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-ch-empty" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    -- line 0 col 0 = `module` keyword start —
                    -- not a callable. Server must return [] or null,
                    -- never crash.
                    sendMsg hin $ posRequest "textDocument/prepareCallHierarchy"
                                              5 fixture 4 0
                    resp <- recvResponseFor hout 5
                    case resultObject resp of
                        Just (Array a) -> V.toList a `shouldBe` []
                        Just Aeson.Null -> return ()  -- also acceptable
                        other -> expectationFailure $
                            "expected [] or null, got: " ++ show other


-- | Build a LSP Range object from (startLine, startChar, endLine, endChar).
rangeFromTo :: Int -> Int -> Int -> Int -> Value
rangeFromTo sl sc el ec = Aeson.object
    [ "start" .= Aeson.object
        [ "line" .= sl, "character" .= sc ]
    , "end"   .= Aeson.object
        [ "line" .= el, "character" .= ec ]
    ]
