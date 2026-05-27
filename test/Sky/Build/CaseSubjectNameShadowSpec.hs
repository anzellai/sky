module Sky.Build.CaseSubjectNameShadowSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- Regression (Cycle 3 task #330 / Dev P40 — examples/13-skyshop
-- runtime panic on every Db.read flow). The case-subject coercion
-- in `caseToGo` derived its concrete typed shape from
-- `inferExprType solvedTypes subject`. For a bare variable reference
-- (`r` inside `Result.withDefault def r = case r of …`), the lookup
-- routed through `Solve.lookupSolvedVar name solvedTypes`, which
-- reads the FLAT name -> type env shared across the whole compilation
-- unit. When a user-side helper bound a same-named local (`r` again)
-- to a concrete type (`Result Error String`), the env stored that
-- pinned shape and the polymorphic `Result.withDefault` body was
-- lowered with `__subject := rt.ResultCoerce[..., string](r)` baked
-- into the generic `Sky_Core_Result_withDefault[T1 any]` body.
-- Monomorphisation specialised the body per call site (`T1 -> int`,
-- `T1 -> map[string]any`, …) but the `string` was already hard-coded,
-- so every instance with `T1 != string` panicked at runtime with
-- `coerceInner: type mismatch — source map[string]interface {}
-- cannot be cast to target string`.
--
-- The fix prefers `Solve.lookupSolvedRegion subjectRegion solvedTypes`
-- for the case-subject's typed shape. Region keys are per source
-- location and immune to flat-name pollution.
spec :: Spec
spec = describe "case-of subject type doesn't leak across name-clashing locals" $ do
    it "polymorphic Result.withDefault body stays generic when an unrelated local r is pinned" $ do
        sky <- findSky
        withSystemTempDirectory "sky-case-subject-shadow" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure ("sky build failed:\n" ++ errOut)
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- Find the generic `Sky_Core_Result_withDefault[T1 any]`
                -- declaration; its body MUST coerce the case subject
                -- as `rt.ResultCoerce[any, any]` (the polymorphic
                -- shape). A concrete element type (e.g.
                -- `rt.ResultCoerce[..., string]`) inside the GENERIC
                -- body is the pollution this spec guards against —
                -- such a body emits a wrong-typed coercion for every
                -- monomorphised instance whose element type isn't
                -- `string`.
                let genericLines = takeBody
                        "func Sky_Core_Result_withDefault[T1 any]"
                        (lines body)
                let hasPolluted = any
                        (\ln ->
                            "rt.ResultCoerce[" `isInfixOf` ln &&
                            ", string](" `isInfixOf` ln)
                        genericLines
                hasPolluted `shouldBe` False


takeBody :: String -> [String] -> [String]
takeBody header ls =
    case dropWhile (not . (header `isInfixOf`)) ls of
        []     -> []
        (_:xs) -> takeWhile (not . isTopDecl) xs
  where
    isTopDecl ln = "func " `isInfixOf` take 5 ln
                || (not (null ln) && head ln == '}')


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


-- ─── Fixture ──────────────────────────────────────────────────────


writeFixture :: FilePath -> IO ()
writeFixture dir = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"case-subject-shadow\"\nversion = \"0.0.0\"\n"
         ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") mainSrc


mainSrc :: String
mainSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Result as Result"
    , "import Sky.Core.Dict as Dict"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- This helper binds a lambda param `r` to `Result Error String`."
    , "-- Pre-fix, that pinned shape leaked into the FLAT solvedTypes"
    , "-- name env at the key `\"r\"`, and the polymorphic"
    , "-- `Result.withDefault def r = case r of …` body picked THAT up"
    , "-- via `inferExprType` when caseToGo derived the subject coercion."
    , "helper d r ="
    , "    Result.withDefault d r"
    , ""
    , ""
    , "-- Forces `Result.withDefault` to be instantiated at `Dict String _`."
    , "-- Pre-fix the typed instance inherited the polluted `string`"
    , "-- coercion from the generic body and panicked at runtime."
    , "useDict v ="
    , "    Result.withDefault Dict.empty v"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        usedString ="
    , "            helper \"\" (Ok \"hello\")"
    , ""
    , "        usedDict ="
    , "            useDict (Ok Dict.empty)"
    , ""
    , "        _ ="
    , "            println usedString"
    , "    in"
    , "        println (String.fromInt (Dict.size usedDict))"
    ]
