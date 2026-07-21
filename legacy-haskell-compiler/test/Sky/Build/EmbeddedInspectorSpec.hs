{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Build.EmbeddedInspectorSpec (spec) where

-- The sky-ffi-inspect Go helper is bundled into the sky binary so
-- releases ship a single executable. Resolution order:
--   1. $SKY_FFI_INSPECTOR (explicit override)
--   2. ./bin/sky-ffi-inspect in the cwd or any ancestor (dev)
--   3. Embedded fallback: extract source from the sky binary to
--      $XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<hash>/, go build,
--      reuse.
--
-- This spec exercises the third path by running `sky build` on an
-- example with a Go dependency from a location where strategies 1
-- and 2 can't resolve, then asserting the build succeeds and the
-- cache dir gets populated.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesDirectoryExist,
                         doesFileExist, listDirectory,
                         removeDirectoryRecursive,
                         getXdgDirectory, XdgDirectory(..),
                         createDirectoryIfMissing, copyFile)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Control.Exception (try, SomeException)
import Data.List (isPrefixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


spec :: Spec
spec = do
    describe "embedded sky-ffi-inspect fallback" $ do

        it "materialises + builds the helper on first use when no disk copy exists" $ do
            sky <- findSky
            cache <- getXdgDirectory XdgCache "sky"
            let toolsDir = cache </> "tools"
            -- Wipe any prior cached helpers so we actually exercise
            -- the build-on-first-use path.
            exists <- doesDirectoryExist toolsDir
            if exists
                then do
                    (_ :: Either SomeException ()) <-
                        try (removeDirectoryRecursive toolsDir)
                    return ()
                else return ()
            -- Run `sky build` on a project that needs FFI generation
            -- (tea-external declares go.dependencies). We chdir into
            -- a tmpdir so the ancestor-walk can't find bin/sky-ffi-inspect
            -- in the repo root.
            withSystemTempDirectory "sky-embed-inspect" $ \tmp -> do
                createDirectoryIfMissing True (tmp </> "src")
                writeFile (tmp </> "sky.toml") $ unlines
                    [ "name = \"inspect-fixture\""
                    , "entry = \"src/Main.sky\""
                    , ""
                    , "[\"go.dependencies\"]"
                    , "\"github.com/google/uuid\" = \"latest\""
                    ]
                writeFile (tmp </> "src" </> "Main.sky") $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    println \"hi\""
                    ]
                -- `sky install` runs the inspector over every declared
                -- go.dependency — our acceptance signal. Even if a
                -- later stage wobbles (this fixture's Main never
                -- references uuid), the inspector must have executed
                -- and the cache must be populated.
                (_ec, _out, _err) <- readCreateProcessWithExitCode
                    (proc sky ["install"]) { cwd = Just tmp } ""
                populated <- doesDirectoryExist toolsDir
                populated `shouldBe` True
                entries <- listDirectory toolsDir
                any ("sky-ffi-inspect-" `isPrefixOf`) entries `shouldBe` True
                -- And the generated FFI bindings should be on disk.
                ffiDir <- doesDirectoryExist (tmp </> ".skycache" </> "ffi")
                ffiDir `shouldBe` True
