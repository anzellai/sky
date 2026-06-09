module Sky.Build.CharPredicateAsRuneSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


-- v0.16.17 follow-up — `Char.isAlpha`/`Char.isDigit`/`Char.isUpper`/
-- `Char.isLower`/`Char.toUpper`/`Char.toLower` lower to typed
-- kernels (`rt.Char_isAlphaT(c rune) bool`, ...). The kernel-arg
-- coercion table used to route the lambda parameter through
-- `rt.AsInt`, which returns `int` and mismatches the kernel's
-- `rune` parameter, so `go build` rejected programs like
--
--     List.all (\c -> Char.isAlpha c || Char.isDigit c)
--              (String.toList s)
--
-- with `cannot use rt.AsInt(c) as rune value in argument to
-- rt.Char_isAlphaT`. The fix added `rt.AsRune` to the runtime
-- and routed the six Char predicates through it.
--
-- This spec is the regression guard. It feeds a fixture that
-- pairs `Char.isAlpha c || Char.isDigit c` (the exact disjunction
-- that triggered the panic in skydeploy's slug-validation path)
-- with a single-predicate `String.toList >> List.all Char.isUpper`
-- shape for coverage, then asserts both compile clean AND produce
-- correct results at runtime.
spec :: Spec
spec = describe "Char.is* predicates coerce via rt.AsRune" $ do
    it "compiles and runs a String.toList lambda that disjuncts isAlpha+isDigit" $ do
        sky <- findSky
        withSystemTempDirectory "sky-char-asrune" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            case ec of
                ExitFailure _ ->
                    expectationFailure ("sky build failed:\n" ++ errOut)
                ExitSuccess -> return ()
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

            (rc, stdoutS, _) <- readCreateProcessWithExitCode
                (proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp } ""
            rc `shouldBe` ExitSuccess
            -- "abc-123" → every char is alpha | digit | hyphen.
            ("slugCharsValid abc-123 = True" `List.isInfixOf` stdoutS)
                `shouldBe` True
            -- "abc!" contains a non-slug character.
            ("slugCharsValid abc! = False" `List.isInfixOf` stdoutS)
                `shouldBe` True
            -- "FOO" — every char is upper.
            ("allUpper FOO = True" `List.isInfixOf` stdoutS) `shouldBe` True
            ("allUpper Foo = False" `List.isInfixOf` stdoutS) `shouldBe` True

  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir =
        readCreateProcessWithExitCode
            (proc sky args) { cwd = Just workDir } ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"char-asrune\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Char as Char"
    , "import Sky.Core.List as List"
    , "import Sky.Core.String as String"
    , "import Std.Log exposing (println)"
    , ""
    , "slugCharsValid : String -> Bool"
    , "slugCharsValid s ="
    , "    List.all"
    , "        (\\c -> Char.isAlpha c || Char.isDigit c || c == '-')"
    , "        (String.toList s)"
    , ""
    , "allUpper : String -> Bool"
    , "allUpper s ="
    , "    List.all Char.isUpper (String.toList s)"
    , ""
    , "boolToString : Bool -> String"
    , "boolToString b ="
    , "    if b then \"True\" else \"False\""
    , ""
    , "main ="
    , "    let"
    , "        _ = println (\"slugCharsValid abc-123 = \" ++ boolToString (slugCharsValid \"abc-123\"))"
    , "        _ = println (\"slugCharsValid abc! = \" ++ boolToString (slugCharsValid \"abc!\"))"
    , "        _ = println (\"allUpper FOO = \" ++ boolToString (allUpper \"FOO\"))"
    , "        _ = println (\"allUpper Foo = \" ++ boolToString (allUpper \"Foo\"))"
    , "    in"
    , "    println \"done\""
    ]
