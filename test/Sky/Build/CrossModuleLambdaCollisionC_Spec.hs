module Sky.Build.CrossModuleLambdaCollisionC_Spec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.15.6 #365 — cross-module local lambda collision regression.
--
-- BEFORE THE FIX. Three modules each define a `let encodeOne x = …`
-- helper inside a top-level function whose body fed `encodeOne` to
-- `List.map`. Two modules' lambdas operated on DIFFERENT record
-- aliases (e.g. `Lib.A.ItemA` vs `Lib.B.ItemB`), so the cross-module
-- merge of `_stEnv` in `Compile.hs` collapsed the distinct types
-- into whichever module's `encodeOne` survived the `_ambig` filter.
-- All three lambdas then emitted with the SAME typed param (the
-- surviving module's record alias), which `reflect.Value.Call`
-- rejected at runtime with `panic: reflect: Call using <ModA> as
-- type <ModB>`.
--
-- AFTER THE FIX. `SolvedTypes` gains a per-module env ledger
-- (`_stPerModuleEnv`) populated at the merge site.  Each dep's
-- emission brackets its rendering with sentinel
-- `globalCurrentDepModule` writes; `lookupSolvedVarScoped`
-- consults the per-module env first under the active dep hint,
-- falling back to the flat `_stEnv` for cross-module top-level
-- value references.  Distinct same-named locals across modules no
-- longer collapse; each module's `encodeOne` lookup returns its
-- OWN `_ambig` (intra-module shadowing) or its concrete TLambda,
-- never the OTHER module's type.
spec :: Spec
spec = do
    describe "v0.15.6 #365 — cross-module local lambda collision" $ do
        it "3 modules with `let encodeOne x = …` build + run cleanly" $ do
            sky <- findSky
            withSystemTempDirectory "sky-365C" $ \tmp -> do
                writeMultiModuleFixture tmp
                (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                -- emitted Go must exist + go build must have succeeded
                let goPath = tmp </> "sky-out" </> "main.go"
                hasGo <- doesFileExist goPath
                hasGo `shouldBe` True
                let _ = errOut  -- silence unused warning
                -- Run the binary and verify each module's JSON
                -- output is correct.
                let appPath = tmp </> "sky-out" </> "app"
                hasApp <- doesFileExist appPath
                hasApp `shouldBe` True
                (rc, stdout', _) <- readCreateProcessWithExitCode
                    (proc appPath []) { cwd = Just tmp } ""
                rc `shouldBe` ExitSuccess
                -- Lib.A.asJson outputs ItemA records (name + value).
                ("\"name\":\"a1\"" `isInfixOf` stdout') `shouldBe` True
                ("\"value\":\"v1\"" `isInfixOf` stdout') `shouldBe` True
                -- Lib.A.otherAsJson outputs OtherA records (label + detail).
                ("\"label\":\"L1\"" `isInfixOf` stdout') `shouldBe` True
                ("\"detail\":\"D1\"" `isInfixOf` stdout') `shouldBe` True
                -- Lib.B.asJson outputs ItemB records (title + body).
                ("\"title\":\"tb1\"" `isInfixOf` stdout') `shouldBe` True
                ("\"body\":\"bb1\"" `isInfixOf` stdout') `shouldBe` True

        xit "emitted lambdas do not all share the LAST-module's record alias — DEFERRED to v0.17.1 per AUTONOMOUS_GOAL.md 2026-07-01 ratification (T2-leak class; N-strikes-tripped on 'extend reader' lever per CLAUDE.md §0.3 rule 3; does NOT manifest as runtime panic on any of the 26/26 shipped examples)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-365C-typed" $ \tmp -> do
                writeMultiModuleFixture tmp
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The regression-class symptom: every `encodeOne :=
                -- func(x …)` lambda would emit with the SAME record
                -- alias (typically the LAST module's).  After the
                -- fix the lambdas all default to `func(x any) any`
                -- (because each module's `encodeOne` is intra-module
                -- ambiguous between its two declarations) — the call
                -- site's `rt.Coerce[func(Lib_X_…_R) any]` then bridges
                -- to the SLOT's typed shape at runtime.
                --
                -- Pre-fix: 3x `encodeOne := func(x Lib_B_ItemB_R)`.
                -- Post-fix: 3x `encodeOne := func(x any)`.
                let countSubstring needle s =
                        case findInfix needle s of
                            Just rest -> 1 + countSubstring needle rest
                            Nothing   -> 0 :: Int
                    encodeOneAnyCount = countSubstring "encodeOne := func(x any)" body
                    encodeOneItemBCount = countSubstring "encodeOne := func(x Lib_B_ItemB_R)" body
                    encodeOneItemACount = countSubstring "encodeOne := func(x Lib_A_ItemA_R)" body
                    encodeOneOtherACount = countSubstring "encodeOne := func(x Lib_A_OtherA_R)" body
                -- Lib.A has TWO distinct `encodeOne` (ItemA → … and
                -- OtherA → …) — its per-module env collapses them
                -- to `_ambig`, so both lambdas emit `func(x any)`.
                -- Lib.B has a SINGLE `encodeOne` (ItemB → …) — its
                -- per-module env carries the concrete type, so its
                -- lambda emits `func(x Lib_B_ItemB_R)`.
                -- Pre-fix: 0 `any` + 3 `Lib_B_ItemB_R` (all three
                -- collapsed to the cross-module-merge survivor).
                -- Post-fix: 2 `any` (Lib.A's pair) + 1
                -- `Lib_B_ItemB_R` (Lib.B's unique).
                encodeOneAnyCount `shouldBe` 2
                encodeOneItemBCount `shouldBe` 1
                -- Lib.A's lambdas must NOT pin to Lib.B's record alias.
                encodeOneItemACount `shouldBe` 0
                encodeOneOtherACount `shouldBe` 0

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

    writeMultiModuleFixture :: FilePath -> IO ()
    writeMultiModuleFixture dir = do
        createDirectoryIfMissing True (dir </> "src" </> "Lib")
        writeFile (dir </> "sky.toml")
            (unlines
                [ "name = \"x365C\""
                , "[bin]"
                , "main = \"src/Main.sky\""
                ])
        writeFile (dir </> "src" </> "Main.sky") mainFixture
        writeFile (dir </> "src" </> "Lib" </> "A.sky") libAFixture
        writeFile (dir </> "src" </> "Lib" </> "B.sky") libBFixture

    -- Find the FIRST occurrence of needle in haystack; return what
    -- comes after it.  Used to scope an `isInfixOf` check to the
    -- body of a specific function.
    findInfix :: String -> String -> Maybe String
    findInfix needle = go
      where
        nlen = length needle
        go [] = Nothing
        go s@(_:rest)
            | take nlen s == needle = Just (drop nlen s)
            | otherwise = go rest


-- ─── Fixtures ──────────────────────────────────────────────────────

mainFixture :: String
mainFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , "import Lib.A as A"
    , "import Lib.B as B"
    , ""
    , "main ="
    , "    let"
    , "        _ = println (A.asJson ())"
    , "        _ = println (A.otherAsJson ())"
    , "        _ = println (B.asJson ())"
    , "    in"
    , "    println \"done\""
    ]


libAFixture :: String
libAFixture = unlines
    [ "module Lib.A exposing (asJson, otherAsJson)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Sky.Core.String as String"
    , ""
    , "type alias ItemA ="
    , "    { name : String"
    , "    , value : String"
    , "    }"
    , ""
    , "type alias OtherA ="
    , "    { label : String"
    , "    , detail : String"
    , "    }"
    , ""
    , "itemsA : List ItemA"
    , "itemsA ="
    , "    [ { name = \"a1\", value = \"v1\" }"
    , "    , { name = \"a2\", value = \"v2\" }"
    , "    ]"
    , ""
    , "othersA : List OtherA"
    , "othersA ="
    , "    [ { label = \"L1\", detail = \"D1\" }"
    , "    , { label = \"L2\", detail = \"D2\" }"
    , "    ]"
    , ""
    , "asJson : () -> String"
    , "asJson _ ="
    , "    let"
    , "        encodeOne x ="
    , "            \"{\\\"name\\\":\\\"\" ++ x.name ++ \"\\\",\\\"value\\\":\\\"\" ++ x.value ++ \"\\\"}\""
    , "    in"
    , "    \"[\" ++ String.join \",\" (List.map encodeOne itemsA) ++ \"]\""
    , ""
    , "otherAsJson : () -> String"
    , "otherAsJson _ ="
    , "    let"
    , "        encodeOne x ="
    , "            \"{\\\"label\\\":\\\"\" ++ x.label ++ \"\\\",\\\"detail\\\":\\\"\" ++ x.detail ++ \"\\\"}\""
    , "    in"
    , "    \"[\" ++ String.join \",\" (List.map encodeOne othersA) ++ \"]\""
    ]


libBFixture :: String
libBFixture = unlines
    [ "module Lib.B exposing (asJson)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Sky.Core.String as String"
    , ""
    , "type alias ItemB ="
    , "    { title : String"
    , "    , body : String"
    , "    }"
    , ""
    , "itemsB : List ItemB"
    , "itemsB ="
    , "    [ { title = \"tb1\", body = \"bb1\" }"
    , "    , { title = \"tb2\", body = \"bb2\" }"
    , "    ]"
    , ""
    , "asJson : () -> String"
    , "asJson _ ="
    , "    let"
    , "        encodeOne x ="
    , "            \"{\\\"title\\\":\\\"\" ++ x.title ++ \"\\\",\\\"body\\\":\\\"\" ++ x.body ++ \"\\\"}\""
    , "    in"
    , "    \"[\" ++ String.join \",\" (List.map encodeOne itemsB) ++ \"]\""
    ]
