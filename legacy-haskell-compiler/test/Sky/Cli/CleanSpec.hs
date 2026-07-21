module Sky.Cli.CleanSpec (spec) where

-- `sky clean` MUST remove only the regenerable artefact dirs:
-- sky-out/, .skycache/, .skydeps/, dist/. User-authored files
-- (src/, sky.toml, README.md, etc.) MUST be preserved. A bug here
-- would silently delete users' work — high-impact even if rare.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, doesDirectoryExist,
                         createDirectoryIfMissing)
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
    describe "sky clean" $ do

        it "removes generated dirs but preserves user files" $ do
            sky <- findSky
            withSystemTempDirectory "sky-clean" $ \tmp -> do
                -- Set up a project-shaped tree with a mix of
                -- managed and user-authored files.
                writeFile (tmp </> "sky.toml")
                    "name = \"clean-spec\"\nentry = \"src/Main.sky\"\n"
                createDirectoryIfMissing True (tmp </> "src")
                writeFile (tmp </> "src" </> "Main.sky")
                    "module Main exposing (main)\nmain = ()\n"
                writeFile (tmp </> "README.md") "# user readme\n"
                createDirectoryIfMissing True (tmp </> "sky-out")
                writeFile (tmp </> "sky-out" </> "stale.go") "// stale\n"
                createDirectoryIfMissing True (tmp </> ".skycache" </> "ffi")
                writeFile (tmp </> ".skycache" </> "ffi" </> "x.skyi") "x\n"
                createDirectoryIfMissing True (tmp </> "dist")
                writeFile (tmp </> "dist" </> "old.tar.gz") "binary\n"

                (ec, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["clean"]) { cwd = Just tmp } ""
                ec `shouldBe` ExitSuccess

                -- Managed dirs gone
                doesDirectoryExist (tmp </> "sky-out")    `shouldReturn` False
                doesDirectoryExist (tmp </> ".skycache")  `shouldReturn` False
                doesDirectoryExist (tmp </> "dist")       `shouldReturn` False
                -- User files preserved
                doesFileExist (tmp </> "sky.toml")          `shouldReturn` True
                doesFileExist (tmp </> "README.md")         `shouldReturn` True
                doesFileExist (tmp </> "src" </> "Main.sky") `shouldReturn` True
