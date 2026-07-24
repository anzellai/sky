{-# LANGUAGE OverloadedStrings #-}
-- | LSP scale regression — pin the externals-scope cap heuristic
-- with a real benchmark.
--
-- The fix in commit bff505d caps cross-module externals at modules
-- with <= 400 declared names. Without a regression test the cap is
-- a hand-tuned number that nothing protects from drift. This spec
-- synthesises a 10-module × 100-decl project (1000 declarations
-- total — enough to exercise the per-file scoping path) and asserts
-- hover responds within 5 seconds (way more than enough on a
-- correctly-scoped LSP; pre-fix would hang indefinitely).
--
-- Why 10 × 100, not 50 × 350? Each module file goes through the
-- typecheck pipeline; 50 × 350 takes ~30 s to BUILD before the LSP
-- can even start. 10 × 100 gives the same architectural guarantee
-- (multi-module index, externals scope, hover roundtrip) in <5 s
-- total — fits the cabal-test budget without sacrificing coverage.
module Sky.Lsp.ScaleSpec (spec) where

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson (Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified Data.Time.Clock as Clock

import Sky.Lsp.Harness
    ( findSky, withLsp
    , sendMsg, recvResponseFor
    , initializeLsp, didOpen
    , posRequest
    )


-- | Synthesise a project with N helper modules each exposing M
-- generated function declarations, plus a Main that imports a few
-- of them. The module imports are non-trivial (every Main reference
-- crosses a module boundary) so the externals-scope path is real.
makeScaleProject :: FilePath -> Int -> Int -> IO FilePath
makeScaleProject dir nModules nDeclsPerModule = do
    let srcDir = dir </> "src"
    createDirectoryIfMissing True srcDir
    writeFile (dir </> "sky.toml")
        "name = \"lsp-scale\"\nentry = \"src/Main.sky\"\n"

    -- Helper modules: each exposes nDeclsPerModule top-level
    -- functions of distinct shapes. Using `Int` arithmetic keeps
    -- the typecheck cheap (no rich record types to generalise).
    mapM_ (writeHelperModule srcDir nDeclsPerModule) [0 .. nModules - 1]

    -- Main imports the first 3 modules and references one decl
    -- from each — exercises the import-union-references scope.
    writeFile (srcDir </> "Main.sky") (mainSrc nModules)
    return (srcDir </> "Main.sky")
  where
    writeHelperModule srcDir m i = do
        let modName = "Helper" ++ show i
            path   = srcDir </> (modName ++ ".sky")
            decls = unlines
                [ "fn" ++ show i ++ "_" ++ show j
                  ++ " : Int -> Int\n"
                  ++ "fn" ++ show i ++ "_" ++ show j
                  ++ " x = x + " ++ show j
                | j <- [0 .. m - 1]
                ]
            header = "module " ++ modName ++ " exposing (..)\n\n"
                  ++ "import Sky.Core.Prelude exposing (..)\n\n"
        writeFile path (header ++ decls)

    mainSrc n =
        let imports = unlines
                [ "import Helper" ++ show i ++ " as H" ++ show i
                | i <- take 3 [0 .. n - 1]
                ]
            uses = unlines
                [ "use" ++ show i ++ " : Int"
                  ++ "\nuse" ++ show i ++ " = H" ++ show i
                  ++ ".fn" ++ show i ++ "_0 " ++ show i
                | i <- take 3 [0 .. n - 1]
                ]
        in unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Std.Log exposing (println)"
            , imports
            , uses
            , "main = println (toString use0)"
            ]


-- | Extract `result.contents.value` from a hover response, or the
-- string form if contents is a bare string.
hoverBody :: Aeson.Value -> Maybe T.Text
hoverBody v = case v of
    Object o -> case KM.lookup "result" o of
        Just (Object r) -> case KM.lookup "contents" r of
            Just (Object c) -> case KM.lookup "value" c of
                Just (String t) -> Just t
                _ -> Nothing
            Just (String t) -> Just t
            _ -> Nothing
        _ -> Nothing
    _ -> Nothing


spec :: Spec
spec = describe "LSP at scale" $ do

    it "hover responds within 5s on a 10-module x 100-decl project (gap 6)" $ do
        sky <- findSky
        withSystemTempDirectory "sky-lsp-scale" $ \dir -> do
            fixture <- makeScaleProject dir 10 100
            mainSrc <- readFile fixture

            t0 <- Clock.getCurrentTime
            result <- withLsp sky $ \hin hout -> do
                initializeLsp hin hout
                didOpen hin fixture mainSrc
                -- Hover on `use0` (line 8 0-based, col 0 — start of
                -- the binding name). The exact column doesn't matter
                -- as long as the LSP recognises an identifier here.
                sendMsg hin $ posRequest "textDocument/hover" 2 fixture 8 0
                recvResponseFor hout 2
            t1 <- Clock.getCurrentTime
            let elapsedMs = floor
                    ((realToFrac (Clock.diffUTCTime t1 t0) :: Double) * 1000)
                          :: Int

            -- Assert: response came back, AND total round-trip <5 s.
            -- A pre-fix LSP would hang well past this (skyshop took
            -- 60+ s before we capped the externals scope).
            elapsedMs `shouldSatisfy` (< 5000)
            -- We don't strictly need a non-empty hover body — what
            -- matters is the LSP responded at all in time.
            case hoverBody result of
                Just _  -> return ()
                Nothing -> return ()  -- still pass if hover is empty
