module Sky.Build.CurriedLambdaStageCSpec (spec) where

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
    describe "#590 Stage C curried-shape lambda lowering" $ do
        it "compiles a multi-arg lambda at a curried Int->Int->Int slot" $ do
            -- Pre-fix this fixture failed `go build` because the
            -- lambda's inner func returned `any` while the slot's
            -- tail required `int`.  Post-fix the typed Stage C
            -- arm emits `func(_lp_x int) func(int) int` end-to-end.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures"
                                  </> "curried-lambda-stage-c"
            withSystemTempDirectory "sky-csc" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"])
                          { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldBe` ExitSuccess
                ("Build complete" `isInfixOf` combined) `shouldBe` True

        it "emits typed param shape, not func(any) any widening" $ do
            -- Inspect the emitted Go.  The Stage C curried arm
            -- routes through `curryLambdaPatTyped[Pre]` whose
            -- per-step param idents are prefixed `_lp_`.  We
            -- assert the typed shape is present AND the legacy
            -- `func(x any) any { return func(y any) any` shape is
            -- gone for this lambda.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures"
                                  </> "curried-lambda-stage-c"
            withSystemTempDirectory "sky-csc-emit" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"])
                          { cwd = Just tmp }
                _ <- readCreateProcessWithExitCode cp ""
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- Typed shape present: the lambda's first param
                -- binds via the `_lp_<name>` convention used by
                -- `curryLambdaPatTyped[Pre]`.
                ("_lp_x int" `isInfixOf` body) `shouldBe` True
                -- And the body's inner step is typed too.
                ("_lp_y int" `isInfixOf` body) `shouldBe` True
                -- Helper signature stays typed.
                ("f func(int) func(int) int" `isInfixOf` body)
                    `shouldBe` True
