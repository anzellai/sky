module Sky.Parse.MultilineInterpolationEscapeSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))

import Sky.Canonicalise.Expression (Chunk(..), splitInterpolation)


-- Regression: Cycle 4 audit D3 — triple-quoted strings unconditionally
-- treated `{{NAME}}` as Sky interpolation with no escape hatch. A
-- template intended as a placeholder for OTHER tooling (Mustache /
-- Handlebars / env-var substitution / shell scripts) was hijacked into
-- a Sky variable reference; codegen then emitted `undefined: NAME`
-- under `go build`.
--
-- Fix landed in `src/Sky/Canonicalise/Expression.hs:splitInterpolation`
-- — added `\{{` as the escape for a literal `{{`, plus `\\` collapsing
-- to a literal `\` so users can write `\` immediately before an
-- interpolation. Other `\X` sequences stay verbatim (preserves the
-- pre-existing "multiline preserves backslashes" contract).
spec :: Spec
spec = do
    describe "splitInterpolation chunk decomposition" $ do

        it "interpolates {{NAME}} unchanged when not escaped" $
            splitInterpolation "hello {{name}}!"
                `shouldChunk` [Lit "hello ", ExprChunk "name", Lit "!"]

        it "treats \\{{NAME}} as the literal text {{NAME}}" $
            splitInterpolation "Hello \\{{NAME}}, welcome"
                `shouldChunk` [Lit "Hello {{NAME}}, welcome"]

        it "treats \\\\ as literal \\ (single backslash)" $
            splitInterpolation "path \\\\here"
                `shouldChunk` [Lit "path \\here"]

        it "double escape: \\\\{{name}} produces \\ + interpolated name" $
            splitInterpolation "\\\\{{name}}"
                `shouldChunk` [Lit "\\", ExprChunk "name"]

        it "leaves \\X (non-escape) verbatim — preserves \\test, \\d, \\n" $ do
            splitInterpolation "\\test" `shouldChunk` [Lit "\\test"]
            splitInterpolation "\\d+"   `shouldChunk` [Lit "\\d+"]
            splitInterpolation "\\n"    `shouldChunk` [Lit "\\n"]

        it "mixes literal placeholders with real interpolation in the same string" $
            splitInterpolation "Run \\{{LITERAL}} and {{var}} together"
                `shouldChunk` [ Lit "Run {{LITERAL}} and "
                              , ExprChunk "var"
                              , Lit " together"
                              ]

        it "ignores stray { (single brace is literal — pre-existing rule)" $
            splitInterpolation "use { not {{x}}"
                `shouldChunk` [Lit "use { not ", ExprChunk "x"]

        it "treats an unclosed {{ as literal (pre-existing rule)" $
            splitInterpolation "broken {{abc"
                `shouldChunk` [Lit "broken {{abc"]

    -- End-to-end: the audit D3 reproducer must compile + run with the
    -- expected literal output once the escape is in place.
    describe "end-to-end: \\{{NAME}} in a triple-quoted string compiles + outputs literal text" $
        it "audit D3 reproducer builds + emits the templated placeholders verbatim" $ do
            sky <- findSky
            withSystemTempDirectory "sky-multiline-escape" $ \tmp -> do
                createDirectoryIfMissing True (tmp </> "src")
                writeFile (tmp </> "sky.toml")
                    ("name = \"multiline-escape\"\n"
                     ++ "version = \"0.0.0\"\n"
                     ++ "entry = \"src/Main.sky\"\n\n"
                     ++ "[source]\nroot = \"src\"\n")
                writeFile (tmp </> "src" </> "Main.sky") fixture
                let buildCp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, _, _) <- readCreateProcessWithExitCode buildCp ""
                ec `shouldBe` ExitSuccess
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                let runCp = (proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp }
                (runEc, runOut, _) <- readCreateProcessWithExitCode runCp ""
                runEc `shouldBe` ExitSuccess
                runOut `shouldBe` expectedOutput


-- ── helpers ─────────────────────────────────────────────────────────

shouldChunk :: [Chunk] -> [Chunk] -> Expectation
shouldChunk actual expected = chunkRepr actual `shouldBe` chunkRepr expected
  where
    chunkRepr = map repr
    repr (Lit s) = "Lit " ++ show s
    repr (ExprChunk s) = "Expr " ++ show s


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate)


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "template : String"
    , "template = \"\"\"Hello \\{{NAME}},"
    , "your account \\{{ACCOUNT_ID}} has balance \\{{BALANCE}}.\"\"\""
    , ""
    , "main ="
    , "    println template"
    ]


expectedOutput :: String
expectedOutput = unlines
    [ "Hello {{NAME}},"
    , "your account {{ACCOUNT_ID}} has balance {{BALANCE}}."
    ]
