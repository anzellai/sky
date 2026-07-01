module Sky.Build.DepSolvedTypesWiringSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- v0.17 Wave 3 / step-1 — dep-emission lowerCtx SolvedTypes wiring.
--
-- BEFORE THE FIX.  The merged SolvedTypes computed in `continueCompile`
-- (carrying entry + every dep's typing under _stPerModuleEnv +
-- _stPerModuleRegions) was NOT installed into the LowerCtx used during
-- dep-module Go emission.  resolveWrapParams' lookupSolvedRegionScoped
-- consulted an essentially empty SolvedTypes inside dep emission paths
-- (see Compile.hs comment at ~3147-3153: "resolveWrapParams now
-- consults the per-module scoped region map but currently finds empty
-- SolvedTypes in dep emission").  The fall-through silently widened
-- any T1/T2 references in the dep's body to `any` via the
-- eraseUndeclaredTVarsInGoSource band-aid at line 3154 — sound under
-- Go's universal interface, but a type-erasure leak that compounds
-- across the module graph.
--
-- AFTER THE FIX.  The merged Solve.SolvedTypes value (carrying the
-- per-module env + region ledgers) is threaded into each dep's
-- LowerCtx — every dep's emission can lookupSolvedRegionScoped for
-- its own region map under its installed _stCurrentModule hint.
-- Cross-module HOFs that reach a Sky-source dep no longer leak T1
-- tokens into the dep's emitted body; the typed shapes survive
-- end-to-end without the eraseUndeclaredTVarsInGoSource floor
-- needing to fire.
--
-- ASSERTION.  Build a 2-module fixture with a polymorphic dep view
-- function over a parametric record alias, then assert the dep's
-- emitted Go function body has ZERO T1/T2 leak tokens.
spec :: Spec
spec = do
    describe "v0.17 Wave 3 — dep SolvedTypes wiring (T1-leak detector)" $ do
        it "polymorphic dep view function emits without raw T1/T2 leak" $ do
            sky <- findSky
            withSystemTempDirectory "sky-wave3-wiring" $ \tmp -> do
                writeFixture tmp
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                let goPath = tmp </> "sky-out" </> "main.go"
                hasGo <- doesFileExist goPath
                hasGo `shouldBe` True
                body <- readFile goPath
                -- The dep's view function (Lib.View.render) emits as
                -- a Go function whose body should contain NO bare T1
                -- / T2 / T3 identifier references — every type-arg
                -- position should be either the enclosing func's
                -- declared TPS (which is in scope) or a concrete
                -- type from the per-region map.
                --
                -- Pre-fix: bodies leak `T1` / `T2` literal tokens
                -- because the dep ctx finds no per-region match and
                -- the residual TVar falls through to render.
                let depFnStart = "func Lib_View_render"
                case findInfix depFnStart body of
                    Nothing -> fail $
                        "Expected `" ++ depFnStart ++ "` in emitted Go"
                    Just afterStart -> do
                        -- Skip the generic signature to check only the function body.
                        let Just bodyOnly = findInfix "{" afterStart
                        -- Take up to the next top-level `func ` to
                        -- scope the body-check to just this function.
                        let (depFnBody, _rest) = scopeFnBody bodyOnly
                            -- Count bare T1/T2/T3 word references
                            -- (preceded + followed by a non-ident
                            -- char so `T1234` is fine, only literal
                            -- `T1` / `T2` / `T3` etc trigger).
                            leakTokens =
                                countBareTVarToken "T1" depFnBody
                                  + countBareTVarToken "T2" depFnBody
                                  + countBareTVarToken "T3" depFnBody
                        -- ARCHITECTURAL CLOSE GATE: zero leak.
                        -- Pre-fix this can be >= 1; post-fix it
                        -- must be 0.
                        leakTokens `shouldBe` 0

  where
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

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src" </> "Lib")
        writeFile (dir </> "sky.toml")
            (unlines
                [ "name = \"wave3wiring\""
                , "[bin]"
                , "main = \"src/Main.sky\""
                ])
        writeFile (dir </> "src" </> "Main.sky") mainFixture
        writeFile (dir </> "src" </> "Lib" </> "View.sky") libViewFixture

    findInfix :: String -> String -> Maybe String
    findInfix needle = go
      where
        nlen = length needle
        go [] = Nothing
        go s@(_:rest)
            | take nlen s == needle = Just (drop nlen s)
            | otherwise = go rest

    -- Take everything up to the next top-level `\nfunc ` or end-of-input.
    scopeFnBody :: String -> (String, String)
    scopeFnBody = go []
      where
        go acc [] = (reverse acc, [])
        go acc s@(_:rest)
            | take 6 s == "\nfunc " = (reverse acc, s)
            | otherwise = go (head s : acc) rest

    -- Count occurrences of `needle` in `s` where both the char
    -- before AND after are non-ident chars.  Treats start/end of
    -- string as non-ident.
    countBareTVarToken :: String -> String -> Int
    countBareTVarToken needle s = go 0 ' ' s
      where
        nlen = length needle
        isIdent c = (c >= 'a' && c <= 'z')
                 || (c >= 'A' && c <= 'Z')
                 || (c >= '0' && c <= '9')
                 || c == '_'
        go n _    [] = n
        go n prev cur@(c:rest)
            | take nlen cur == needle
            , not (isIdent prev)
            , let after = drop nlen cur
            , case after of
                []     -> True
                (a:_)  -> not (isIdent a)
            = go (n+1) (last needle) (drop nlen cur)
            | otherwise = go n c rest


-- ─── Fixtures ──────────────────────────────────────────────────────

mainFixture :: String
mainFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , "import Lib.View as View"
    , ""
    , "main ="
    , "    let"
    , "        cfg = { label = \"hi\", payload = 42 }"
    , "        _ = println (View.render cfg)"
    , "    in"
    , "    println \"done\""
    ]


libViewFixture :: String
libViewFixture = unlines
    [ "module Lib.View exposing (render)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.String as String"
    , ""
    , "-- Parametric record alias — the codegen surface that"
    , "-- historically leaked T1 into the dep body before the dep"
    , "-- SolvedTypes wiring landed."
    , "type alias Cfg msg ="
    , "    { label : String"
    , "    , payload : msg"
    , "    }"
    , ""
    , "render : Cfg msg -> String"
    , "render cfg ="
    , "    cfg.label"
    ]
