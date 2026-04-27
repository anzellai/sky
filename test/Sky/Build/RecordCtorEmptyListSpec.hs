module Sky.Build.RecordCtorEmptyListSpec (spec) where

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
    describe "Auto record ctor coerces typed-slice args at call site (Limitation #18)" $ do
        it "builds a record ctor call with [] in a List String slot without go build rejecting" $ do
            -- Reproducer: `type alias Item = { id : Int, name : String, tags : List String }`
            -- with `Item 1 "first" []` at the call site. Pre-fix this shipped
            -- `Item(1, "first", []any{})` which `go build` rejected with
            -- `cannot use []any{} as []string value in argument to Item`.
            -- Post-fix the empty list coerces via `rt.AsListT[string]([]any{})`.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "record-ctor-emptylist"
            withSystemTempDirectory "sky-rcel" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldBe` ExitSuccess
                -- "Build complete" line confirms `go build` ran and succeeded —
                -- not just that the Sky checker passed.
                ("Build complete" `isInfixOf` combined) `shouldBe` True

        it "emits rt.AsListT[string] coercion for the empty list arg" $ do
            -- The actual coercion call must appear in the emitted Go.
            -- Without it, the empty list lands as `[]any{}` in a `[]string`
            -- slot and `go build` rejects.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "record-ctor-emptylist"
            withSystemTempDirectory "sky-rcel-emit" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (_, _, _) <- readCreateProcessWithExitCode cp ""
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The Item ctor itself should be typed (p2 is []string).
                ("func Item(p0 int, p1 string, p2 []string)" `isInfixOf` body)
                    `shouldBe` True
                -- Both call sites should route through rt.AsListT[string].
                ("rt.AsListT[string]([]any{})" `isInfixOf` body)
                    `shouldBe` True
                -- And the bare-untyped form must NOT appear (would mean
                -- coerceArg silently skipped the empty-list).
                ("Item(rt.CoerceInt(1), rt.CoerceString(\"first\"), []any{})" `isInfixOf` body)
                    `shouldBe` False
