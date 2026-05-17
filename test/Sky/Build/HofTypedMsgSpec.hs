module Sky.Build.HofTypedMsgSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory, doesDirectoryExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


copyTree :: FilePath -> FilePath -> IO ()
copyTree src dst = do
    createDirectoryIfMissing True dst
    entries <- listDirectory src
    mapM_ (\e -> do
        let s = src </> e
            d = dst </> e
        isF <- doesFileExist s
        if isF
            then copyFile s d
            else do
                isD <- doesDirectoryExist s
                if isD then copyTree s d else return ()) entries


spec :: Spec
spec = do
    describe "Helper with (String -> Msg) typed callback (Limitation #18)" $ do
        it "compiles a helper that takes a (String -> Msg) callback" $ do
            -- Reproducer: `field : String -> (String -> Msg) -> Msg`
            -- with `field "alice" UserChanged` at the call site. Pre-fix
            -- the helper sig emitted `cb func(string) any` and `go build`
            -- rejected the `Msg_UserChanged : func(string) Msg` arg.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "hof-typed-msg"
            withSystemTempDirectory "sky-htm" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldBe` ExitSuccess
                ("Build complete" `isInfixOf` combined) `shouldBe` True

        it "passes typed Msg ctor RAW at the call site (post σ-pinning)" $ do
            -- v0.13 Stage 1 update: with ADT-ctor sigs registered in
            -- `_cg_funcParamTypes` (so `goExprGoType
            -- Msg_UserChanged` returns `func(string) Msg`) and
            -- σ-pinning preserving TVars in the substituteOnly
            -- path, the typed slot's `func(string) T1` substitutes
            -- to `func(string) Msg` and matches the ctor's own sig
            -- directly. coerceArg's short-circuit fires and no
            -- `rt.Coerce` wrap is emitted — the ctor flows raw,
            -- which is what closes the dominant adapter class.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "hof-typed-msg"
            withSystemTempDirectory "sky-htm-emit" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (_, _, _) <- readCreateProcessWithExitCode cp ""
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- Helper sig emits the typed return shape (D1).
                ("cb func(string) Msg" `isInfixOf` body) `shouldBe` True
                -- Call site passes Msg ctor RAW — no rt.Coerce wrap.
                ("rt.Coerce[func(string) Msg](Msg_UserChanged)"
                    `isInfixOf` body) `shouldBe` False
                -- The raw form IS what we want now.
                ("field(\"alice\", Msg_UserChanged)" `isInfixOf` body)
                    `shouldBe` True
