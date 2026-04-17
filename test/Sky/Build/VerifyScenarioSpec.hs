module Sky.Build.VerifyScenarioSpec (spec) where

-- Audit P2-4: `sky verify <example>` used to just start the server
-- and probe `GET /` for a 2xx/3xx. A handler that returned an
-- empty 200 or the wrong content passed silently — that's the
-- regression class the audit flagged as M6.
--
-- Post-fix, `examples/<n>/verify.json` declares a request sequence
-- with `expectStatus` and `expectBody` substring lists. `sky
-- verify` runs each request and fails if any assertion is broken.
--
-- This spec spawns `sky verify` with both a correct scenario and
-- a deliberately-broken one and asserts the expected outcomes.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, removeFile, copyFile)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import Data.List (isInfixOf)
import qualified Control.Exception as E


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


withScenario :: FilePath -> String -> IO a -> IO a
withScenario scenarioPath newContents action = do
    prev <- do
        ok <- doesFileExist scenarioPath
        if ok then Just <$> readFile scenarioPath else return Nothing
    -- Force strict read so we can overwrite while the handle is closed.
    _ <- E.evaluate (length (concat prev))
    writeFile scenarioPath newContents
    E.finally action $
        case prev of
            Just old -> writeFile scenarioPath old
            Nothing  -> do
                still <- doesFileExist scenarioPath
                if still then removeFile scenarioPath else return ()


spec :: Spec
spec = do
    describe "sky verify scenario support (audit P2-4)" $ do

        it "honest scenario → runtime ok" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let scenarioPath = cwd </> "examples" </> "15-http-server" </> "verify.json"
            goodScenario <- readFile scenarioPath
            _ <- E.evaluate (length goodScenario)
            (_ec, out, _err) <- readCreateProcessWithExitCode
                (shell (sky ++ " verify 15-http-server"))
                ""
            ("runtime ok" `isInfixOf` out) `shouldBe` True
            ("scenario: 2 requests" `isInfixOf` out) `shouldBe` True

        it "failing body-substring expectation → FAIL scenario" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let scenarioPath = cwd </> "examples" </> "15-http-server" </> "verify.json"
            let bogus = "{\"requests\":[{\"method\":\"GET\",\"path\":\"/\"," ++
                        "\"expectStatus\":200," ++
                        "\"expectBody\":[\"AbsolutelyNotInTheActualResponse_xyzzy\"]}]}\n"
            withScenario scenarioPath bogus $ do
                (_ec, out, _err) <- readCreateProcessWithExitCode
                    (shell (sky ++ " verify 15-http-server"))
                    ""
                ("FAIL scenario" `isInfixOf` out) `shouldBe` True
                ("AbsolutelyNotInTheActualResponse_xyzzy" `isInfixOf` out) `shouldBe` True

        it "failing status expectation → FAIL scenario" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let scenarioPath = cwd </> "examples" </> "15-http-server" </> "verify.json"
            let bogus = "{\"requests\":[{\"method\":\"GET\",\"path\":\"/\",\"expectStatus\":418}]}\n"
            withScenario scenarioPath bogus $ do
                (_ec, out, _err) <- readCreateProcessWithExitCode
                    (shell (sky ++ " verify 15-http-server"))
                    ""
                ("FAIL scenario" `isInfixOf` out) `shouldBe` True
                ("expected 418" `isInfixOf` out) `shouldBe` True
