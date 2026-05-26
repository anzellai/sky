module Sky.Build.AuthUntypedBoundarySpec (spec) where

-- P5 (Gap A6) — compile-time gate for the security-critical Auth
-- kernel boundary.
--
-- Every public Auth/security kernel — Auth.hashPassword,
-- Auth.hashPasswordCost, Auth.passwordStrength, Auth.signToken,
-- Auth.verifyToken, Auth.register, Auth.login, Auth.setRole — must
-- receive `String` arguments at the Sky type level for each String
-- parameter slot. Bridging an `any`-typed binding into any of
-- those slots is a soundness gap: the runtime `mustStringTyped`
-- check would catch it at execution, but the user gets a runtime
-- error (no static contract) AND, pre-P5, that error leaked the
-- actual Go type into the user-visible message (`got <Go-type>`).
--
-- The compile-time gate emits `Sky.Auth.UntypedBoundary` (DiagCode
-- E4006) when a `Can.VarKernel "Auth" name` call sees an arg whose
-- inferred HM type contains `any` (or an unresolved TVar) at a
-- slot the kernel signature declares as `String`.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


spec :: Spec
spec = do
    describe "Auth kernel typed-boundary gate (P5 / Gap A6)" $ do
        it "rejects an `any`-typed binding flowing into Auth.hashPassword" $ do
            sky <- findSky
            withSystemTempDirectory "sky-auth-untyped-hash" $ \tmp -> do
                writeFixture tmp untypedHashFixture
                (ec, out, err) <- runSky sky ["build", "src/Main.sky"] tmp
                let combined = out ++ err
                ec `shouldNotBe` ExitSuccess
                -- The new diagnostic code identifies the gate uniquely;
                -- grep-stable, language-agnostic.
                combined `shouldSatisfy`
                    ("E4006" `isInfixOf`)
                combined `shouldSatisfy`
                    ("Sky.Auth.UntypedBoundary" `isInfixOf`)
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` False

        it "accepts a properly String-typed binding into Auth.hashPassword" $ do
            -- Baseline: identical shape minus the `any` annotation
            -- must STILL compile. Confirms the gate isn't blanket-
            -- rejecting valid uses.
            sky <- findSky
            withSystemTempDirectory "sky-auth-typed-hash-ok" $ \tmp -> do
                writeFixture tmp typedHashFixture
                (ec, out, err) <- runSky sky ["build", "src/Main.sky"] tmp
                let combined = out ++ err
                if ec /= ExitSuccess
                    then expectationFailure
                        ("typed Auth.hashPassword fixture should build:\n"
                            ++ combined)
                    else return ()


-- ─── shared scaffolding ────────────────────────────────────────────


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


writeFixture :: FilePath -> String -> IO ()
writeFixture dir body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"auth-boundary\"\nversion = \"0.0.0\"\n"
         ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body


-- ─── Fixtures ──────────────────────────────────────────────────────


-- The bad case: `bridge` is bound to a Sky.Ffi.kernel reference
-- with NO type annotation. Without a signature, HM picks a fresh
-- TVar for the binding's type. That polymorphic shape unifies
-- with String at the Auth.hashPassword call site (HM is happy)
-- BUT carries no typed contract that the runtime value is
-- actually a String — at runtime the kernel could return any Go
-- value and the `mustStringTyped` check would fire.
--
-- Sky.Ffi.kernel itself is the public way for user code to bind
-- a raw kernel reference; using it without an annotation is the
-- documented escape hatch this gate must close at the Auth
-- boundary.
untypedHashFixture :: String
untypedHashFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Ffi as Ffi"
    , "import Std.Auth as Auth"
    , "import Std.Log exposing (println)"
    , ""
    , "-- Annotated `: any` so HM doesn't impose any constraint."
    , "-- The body is also an any-returning expression (the raw"
    , "-- Ffi.kernel reference without a typed signature)."
    , "-- Together the binding carries NO typed contract."
    , "bridge : any"
    , "bridge ="
    , "    Ffi.kernel \"Time_unixMillis\""
    , ""
    , "main ="
    , "    case Auth.hashPassword bridge of"
    , "        Ok h -> println h"
    , "        Err _ -> println \"bad\""
    ]


-- The good case: identical structure, properly typed. Must still
-- compile (regression guard that the gate isn't over-strict).
typedHashFixture :: String
typedHashFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Auth as Auth"
    , "import Std.Log exposing (println)"
    , ""
    , "bridge : String"
    , "bridge ="
    , "    \"correct horse battery staple\""
    , ""
    , "main ="
    , "    case Auth.hashPassword bridge of"
    , "        Ok h -> println h"
    , "        Err _ -> println \"bad\""
    ]
