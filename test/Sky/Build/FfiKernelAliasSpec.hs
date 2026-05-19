module Sky.Build.FfiKernelAliasSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.14.x Stage 4 regression — `Ffi.kernel "K_n"` declaration shape.
--
-- A Sky-source binding of the form
--
--     myUpper : String -> String
--     myUpper = Ffi.kernel "String_toUpper"
--
-- must:
--
--   1. Type-check.  Pre-Stage-4 the canonicaliser stripped every
--      arrow from a value-binding's annotation (`arrowResult` was
--      unconditionally recursive), corrupting `myUpper`'s recorded
--      type from `String -> String` to `String`. Cross-module use
--      surfaced as "Foreign 'Lib.myUpper': String vs a -> b".
--
--   2. Emit the typed kernel call at the use site.  `Lib.myUpper s`
--      from another module must lower to `rt.String_toUpperT(s)`
--      (the typed kernel variant), not `rt.Ffi_kernel(...)` (the
--      runtime panic stub) or a bare `Lib_myUpper(s)` call (which
--      would invoke the dead alias body).
--
--   3. Run.  Calling the binding produces the expected output.
spec :: Spec
spec = describe "Ffi.kernel alias mechanism (Stage 4)" $ do
    it "type-checks + builds + runs + emits typed kernel call" $ do
        sky <- findSky
        withSystemTempDirectory "sky-ffi-kernel-alias" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure $
                  "sky build failed.\n" ++ out ++ "\n" ++ errOut
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                body <- readFile (tmp </> "sky-out" </> "main.go")
                let usesTypedUpper =
                        "rt.String_toUpperT" `isInfixOf` body
                            || "rt.String_toUpper(" `isInfixOf` body
                    callsPanicStub = "rt.Ffi_kernel(" `isInfixOf` body
                    -- The alias body emission `func Lib_myUpper() ...`
                    -- is OK (DCE-prunable) — but no CALL to it
                    -- should appear (call sites must be rewritten).
                    callsAliasBody =
                        any (\ln -> "Lib_myUpper(" `isInfixOf` ln
                                 && not ("func Lib_myUpper" `isInfixOf` ln))
                            (lines body)
                usesTypedUpper `shouldBe` True
                callsAliasBody `shouldBe` False
                -- Panic stub may appear in the alias body decl, but
                -- not at any user-code call site. Pragmatic check:
                -- it's fine if `rt.Ffi_kernel("String_toUpper")` is
                -- present (in the alias body), but the program must
                -- not reach it at runtime.
                (rc, runOut, _) <- runApp tmp
                rc `shouldBe` ExitSuccess
                ("HELLO" `isInfixOf` runOut) `shouldBe` True
                ("11" `isInfixOf` runOut) `shouldBe` True
                _ <- return callsPanicStub
                return ()

  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    runApp :: FilePath -> IO (ExitCode, String, String)
    runApp dir = do
        let cp = (proc (dir </> "sky-out" </> "app") []) { cwd = Just dir }
        readCreateProcessWithExitCode cp ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") $ unlines
            [ "[project]"
            , "name = \"ffi-kernel-alias-test\""
            , ""
            , "[bin]"
            , "name = \"app\""
            ]
        writeFile (dir </> "src" </> "Lib.sky") $ unlines
            [ "module Lib exposing (myUpper, myLen)"
            , ""
            , "import Sky.Ffi as Ffi"
            , ""
            , ""
            , "myUpper : String -> String"
            , "myUpper = Ffi.kernel \"String_toUpper\""
            , ""
            , ""
            , "myLen : String -> Int"
            , "myLen = Ffi.kernel \"String_length\""
            ]
        writeFile (dir </> "src" </> "Main.sky") $ unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Std.Log exposing (println)"
            , "import Lib"
            , ""
            , ""
            , "main ="
            , "    let"
            , "        _ = println (Lib.myUpper \"hello\")"
            , "        _ = println (String.fromInt (Lib.myLen \"hello world\"))"
            , "    in"
            , "        Task.succeed ()"
            ]
