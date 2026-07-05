module Sky.Cli.FmtSpec (spec) where

-- `sky fmt` contracts:
--   1. Idempotent — formatting twice produces byte-identical output.
--      Existing FormatSpec.hs covers this for known fixtures; this
--      spec exercises the CLI wrapper end-to-end on a real file.
--   2. Refuses on data loss — if formatting would lose >1/3 of
--      lines (signal of a partial parse), sky fmt MUST refuse to
--      overwrite. Codified in src/Sky/Format/Format.hs at the
--      .formatPath safety guard.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
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


spec :: Spec
spec = do
    describe "sky fmt" $ do

        it "produces byte-identical output on a second pass" $ do
            sky <- findSky
            withSystemTempDirectory "sky-fmt" $ \tmp -> do
                createDirectoryIfMissing True (tmp </> "src")
                let path = tmp </> "src" </> "Main.sky"
                writeFile path $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , ""
                    , "greet : String -> String"
                    , "greet name ="
                    , "    \"Hello, \" ++ name"
                    , ""
                    , ""
                    , "main = println (greet \"world\")"
                    ]
                (ec1, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["fmt", path]) ""
                ec1 `shouldBe` ExitSuccess
                pass1 <- readFile path
                length pass1 `seq` return ()
                (ec2, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["fmt", path]) ""
                ec2 `shouldBe` ExitSuccess
                pass2 <- readFile path
                pass2 `shouldBe` pass1

        -- Regression for #144: `sky fmt <file>` used to check
        -- SKY_FMT_FORCE ONLY in `--stdin` mode.  In file mode the
        -- refusal was unconditional — the escape hatch the message
        -- itself advertised was a no-op.  Both branches now honour
        -- the same contract.  The fixture below (a lambda-body
        -- comment) is the exact shape that dropped 3 of 9 comments
        -- in `StatsTest.sky` on the issue's attachment.
        it "SKY_FMT_FORCE=1 file mode: writes despite the safety guard" $ do
            sky <- findSky
            withSystemTempDirectory "sky-fmt-force" $ \tmp -> do
                let path = tmp </> "F.sky"
                writeFile path $ unlines
                    [ "module F exposing (tests)"
                    , ""
                    , ""
                    , "tests ="
                    , "    [ pair 1 (\\_ ->"
                    , "        -- head comment on the lambda body"
                    , "        step 5.0)"
                    , "    , pair 2 (\\_ ->"
                    , "        -- second dropped comment"
                    , "        step 10.0)"
                    , "    , pair 3 (\\_ ->"
                    , "        -- third dropped comment"
                    , "        step 15.0)"
                    , "    , pair 4 (\\_ ->"
                    , "        -- fourth dropped comment"
                    , "        step 20.0)"
                    , "    ]"
                    ]
                original <- readFile path
                -- Without force: should refuse (exit 1) and leave
                -- the file untouched.
                (ec1, _, err1) <- readCreateProcessWithExitCode
                    (proc sky ["fmt", path]) ""
                ec1 `shouldBe` ExitFailure 1
                ("refusing to format" `isInfixOf` err1) `shouldBe` True
                after1 <- readFile path
                after1 `shouldBe` original
                -- With SKY_FMT_FORCE=1: should exit 0 and write
                -- despite the drop.  Message reflects "wrote
                -- despite" instead of the confusing "re-run with
                -- SKY_FMT_FORCE=1" that the same env-var-already-
                -- set caller had just seen.
                let cp = (proc sky ["fmt", path])
                        { env = Just [("SKY_FMT_FORCE", "1"), ("PATH", "/usr/bin:/bin")] }
                (ec2, _, err2) <- readCreateProcessWithExitCode cp ""
                ec2 `shouldBe` ExitSuccess
                ("SKY_FMT_FORCE=1" `isInfixOf` err2) `shouldBe` True
                ("wrote formatted output despite" `isInfixOf` err2)
                    `shouldBe` True
                after2 <- readFile path
                (after2 /= original) `shouldBe` True
