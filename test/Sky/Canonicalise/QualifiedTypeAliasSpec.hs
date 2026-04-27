module Sky.Canonicalise.QualifiedTypeAliasSpec (spec) where

-- Regression fence for "qualified type annotations under an import
-- alias resolve through the alias map".
--
-- Pre-fix bug: `resolveTypeQual` in src/Sky/Canonicalise/Type.hs
-- handled only a hardcoded set of built-in module qualifiers
-- (List/Maybe/Result/Task/Dict/Set) and fell through to
-- `Canonical qualifier` for everything else. Under
-- `import Std.Ui as Ui`, an annotation like `mkColor : Ui.Color`
-- canonicalised to `TType (Canonical "Ui") "Color" []`, while the
-- bare `Color` (via `import Std.Ui exposing (Color)`) canonicalised
-- to `TType (Canonical "Std.Ui") "Color" []`. HM then rejected the
-- two as different types with the cryptic message
--   `Type mismatch: Color vs Color`
-- (same display, different identity).
--
-- Surfaced from a real-world Std.Ui port: every typed helper using
-- the qualified `Ui.Color` form failed unification.
--
-- Fix: thread an `aliasMap : alias-segment → full module name` map
-- through canonicaliseTypeAnnotationWithAliases so resolveTypeQualWith
-- consults it before the literal-qualifier fallback.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


checkOnly :: String -> IO (Int, String)
checkOnly src =
    withSystemTempDirectory "sky-qual-alias" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"qual-alias-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " check src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = do
    describe "Qualified type annotations resolve through `import M as Alias`" $ do

        it "Ui.Color in annotation type-checks identically to bare Color" $ do
            -- Pre-fix: `Ui.Color` resolves to Canonical "Ui" but
            -- `Ui.rgb` returns Canonical "Std.Ui" → "Color vs Color"
            -- type error.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Ui as Ui"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "mkColor : Int -> Ui.Color"
                    , "mkColor n = Ui.rgb n n n"
                    , ""
                    , "main = let _ = mkColor 100 in println \"ok\""
                    ]
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldNotSatisfy` ("Color vs Color" `isInfixOf`)
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "qualified type from a user dep module resolves under alias" $ do
            -- Sister case using a custom dep — confirms the fix isn't
            -- specific to Std.Ui kernel types.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Box = Box Int"
                    , ""
                    , "wrap : Int -> Box"
                    , "wrap n = Box n"
                    , ""
                    , "main = let _ = wrap 42 in println \"ok\""
                    ]
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldSatisfy` ("No errors found" `isInfixOf`)
