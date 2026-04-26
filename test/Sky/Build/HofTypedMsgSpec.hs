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

        it "coerces the typed Msg ctor at the call site via rt.Coerce" $ do
            -- The helper sig stays `cb func(string) any` (this is
            -- load-bearing — Sky lambdas always lower to func(any) any
            -- and Go has no function-type covariance, so widening to
            -- `any` lets the same helper accept BOTH lambdas and typed
            -- ctors). The fix is at the call site: a typed Msg ctor
            -- like `Msg_UserChanged : func(string) Msg` gets adapted
            -- via `rt.Coerce[func(string) any]` to fit. Pre-fix the
            -- registry didn't know `field`'s param was a func type, so
            -- coerceArg short-circuited (ty = "any") and the typed
            -- ctor went through unwrapped — go build then rejected.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "hof-typed-msg"
            withSystemTempDirectory "sky-htm-emit" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (_, _, _) <- readCreateProcessWithExitCode cp ""
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- Helper sig uses widened `func(string) any` (NOT
                -- `func(string) Msg` — the widening is correct, see
                -- renderHofParamTy and the CompileSpec "Result-typed
                -- lambda params" test).
                ("cb func(string) any" `isInfixOf` body) `shouldBe` True
                -- Call site MUST route the typed Msg ctor through
                -- rt.Coerce so the func-type adapter fires.
                ("rt.Coerce[func(string) any](Msg_UserChanged)"
                    `isInfixOf` body) `shouldBe` True
                -- Bare-pass form (pre-fix shape) must be GONE.
                ("field(\"alice\", Msg_UserChanged)" `isInfixOf` body)
                    `shouldBe` False
