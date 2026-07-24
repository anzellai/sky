-- | v0.13 Phase A3 integration — end-to-end monomorphisation
-- instance capture from a real Sky source build.
--
-- Spins up `sky build` with `SKY_MONO_TRACE=1` on a small fixture,
-- parses the trace output, and asserts the captured instance set.
-- Locks the data flow from solver → mangling → compile-pipeline
-- log so regressions surface here before they reach the user-
-- facing emission rewrite.
module Sky.Build.MonoIntegrationSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified System.Environment
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf, isPrefixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


runBuild :: FilePath -> String -> IO (ExitCode, String)
runBuild tmp src = do
    sky <- findSky
    createDirectoryIfMissing True (tmp </> "src")
    writeFile (tmp </> "sky.toml") "[project]\nname = \"mono\"\nbin = \"app\"\n"
    writeFile (tmp </> "src" </> "Main.sky") src
    parentEnv <- System.Environment.getEnvironment
    let buildEnv = ("SKY_MONO_TRACE", "1") :
                   filter ((/= "SKY_MONO_TRACE") . fst) parentEnv
    let cp = (proc sky ["build", "src/Main.sky"])
                { cwd = Just tmp, env = Just buildEnv }
    (ec, out, err) <- readCreateProcessWithExitCode cp ""
    return (ec, out ++ err)


-- | Parse the "Monomorphisation: N instances across M callees" line.
parseInstanceCount :: String -> Maybe (Int, Int)
parseInstanceCount out =
    case filter ("Monomorphisation:" `isInfixOf`) (lines out) of
        (line:_) -> case words line of
            -- Words: ["Monomorphisation:", N, "instances", "across",
            --         M, "polymorphic", "callees"]
            (_:n:_:_:m:_) -> case (reads n :: [(Int, String)],
                                   reads m :: [(Int, String)]) of
                ((nv, _):_, (mv, _):_) -> Just (nv, mv)
                _ -> Nothing
            _ -> Nothing
        [] -> Nothing


-- | Extract every mangled instance name from the SKY_MONO_TRACE=1 dump.
-- Trace lines look like:  "     Sky_Core_Maybe_withDefault__String"
-- — leading whitespace, then the mangled name.  The compile pipeline
-- emits one per captured instance.
parseInstanceNames :: String -> [String]
parseInstanceNames out =
    [ w
    | line <- lines out
    , let w = dropWhile (== ' ') line
    , not (null w)
    -- Heuristic: trace dump lines are mangled identifiers — pure
    -- ASCII alnum + underscore.  Skip phase headers, pipeline
    -- status, summary lines.
    , all isMangleChar w
    , not (any (`isPrefixOf` w) ["Monomorphisation", "Compilation",
                                  "Running", "Build", "--", "HM",
                                  "Wrote", "Types", "Found",
                                  "Names", "Main:", "Loaded",
                                  "DCE", "resolving", "Incremental"])
    ]
  where
    isMangleChar c =
        (c >= 'a' && c <= 'z')
     || (c >= 'A' && c <= 'Z')
     || (c >= '0' && c <= '9')
     || c == '_'


spec :: Spec
spec = do
    describe "End-to-end monomorphisation capture" $ do

        it "captures the Maybe.withDefault String instance from a real build" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Maybe as Maybe"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    println (Maybe.withDefault \"none\" (Just \"hello\"))"
                    ]
            withSystemTempDirectory "sky-mono-int" $ \tmp -> do
                (ec, out) <- runBuild tmp src
                ec `shouldBe` ExitSuccess
                -- The capture log line MUST appear.
                out `shouldSatisfy` ("Monomorphisation:" `isInfixOf`)
                -- One quantified callee (`Maybe.withDefault`), one
                -- instantiation (String).  But `println` and `Just`
                -- also count as foreign references — `Just` is the
                -- ADT constructor (polymorphic in `a`), `println` is
                -- monomorphic on String.  Expect ≥ 2 instances.
                case parseInstanceCount out of
                    Just (n, _) -> n `shouldSatisfy` (>= 1)
                    Nothing -> expectationFailure
                        ("could not parse Monomorphisation line in:\n" ++ out)
                -- The Maybe.withDefault__String mangled name must be
                -- in the captured set.
                let names = parseInstanceNames out
                any ("Maybe_withDefault" `isInfixOf`) names `shouldBe` True

        it "captures distinct Maybe instances when called with Int + String" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Maybe as Maybe"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    let"
                    , "        s = Maybe.withDefault \"none\" (Just \"hello\")"
                    , "        n = Maybe.withDefault 0 (Just 42)"
                    , "    in"
                    , "        println (s ++ \" \" ++ String.fromInt n)"
                    ]
            withSystemTempDirectory "sky-mono-int-2" $ \tmp -> do
                (ec, out) <- runBuild tmp src
                ec `shouldBe` ExitSuccess
                let names = parseInstanceNames out
                -- BOTH instances should be present.
                any ("Maybe_withDefault__String" `isInfixOf`) names `shouldBe` True
                any ("Maybe_withDefault__Int" `isInfixOf`) names `shouldBe` True

        it "captures stdlib instances from a Result-using program" $ do
            -- Annotate `parse` so the Result error type resolves to
            -- the concrete `Error` (vs staying as a free TVar that
            -- the isConcrete filter would reject).  Captured
            -- instance: `Result_withDefault__Error_String`.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Result as Result"
                    , "import Sky.Core.Error exposing (Error)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "parse : String -> Result Error String"
                    , "parse s = Ok s"
                    , ""
                    , "main ="
                    , "    println (Result.withDefault \"fallback\" (parse \"value\"))"
                    ]
            withSystemTempDirectory "sky-mono-int-3" $ \tmp -> do
                (ec, out) <- runBuild tmp src
                ec `shouldBe` ExitSuccess
                let names = parseInstanceNames out
                any ("Result_withDefault" `isInfixOf`) names `shouldBe` True

        it "captures 0 instances for a monomorphic program" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hello\""
                    ]
            withSystemTempDirectory "sky-mono-int-4" $ \tmp -> do
                (ec, out) <- runBuild tmp src
                ec `shouldBe` ExitSuccess
                case parseInstanceCount out of
                    Just (n, _) -> n `shouldBe` 0
                    Nothing -> expectationFailure
                        ("Monomorphisation line missing:\n" ++ out)
