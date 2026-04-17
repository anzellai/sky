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
