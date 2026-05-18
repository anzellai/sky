module Sky.Build.EntryLocalShadowsDepSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- Regression: when the entry module has a lambda param (or any
-- local) whose name matches a dep module's top-level function, the
-- pre-fix `typesWithDeps` merge in src/Sky/Build/Compile.hs short-
-- circuited on `k `Set.member` entryKeys` and returned the entry's
-- (local-polluted) type. Downstream `inferExprType` lookups for
-- `Can.VarTopLevel _ n` then resolved to the local's type, not the
-- dep's, and let-binding codegen emitted `rt.Coerce[<local-type>]`
-- around the dep call — silent wrong-typed coercion that panicked
-- at runtime.
--
-- Concrete repro that broke in `sky-bundled/console`:
--   • entry `Main.fetchLogs parent filter` — lambda param `filter
--     : LogFilter`
--   • dep `Sky.Core.List.filter : (a -> Bool) -> List a -> List a`
--   • dep `View.logsView` calls `let xs = List.filter f model.logs`
--   → inferExprType for the let returned `TAlias LogFilter`
--   → codegen emitted `rt.Coerce[State_LogFilter_R](filter_result)`
--   → runtime panic on tab click ("interface conversion: …").
--
-- The fix: when entry's key resolves to a type and any dep's same
-- key resolves to a structurally-distinct type, collapse to
-- `_ambig` so downstream codegen falls back to safe any-routing.
spec :: Spec
spec = describe "entry-local does not shadow dep top-level in solvedTypes" $ do
    it "lets a dep call with the same name as an entry-local stay typed" $ do
        sky <- findSky
        withSystemTempDirectory "sky-entry-local-shadow" $ \tmp -> do
            writeMultiModule tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure ("sky build failed:\n" ++ errOut)
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                -- The dep call `List.filter keep items` returns
                -- `List Item` and lives in a `let filtered = ...`
                -- binding inside View.visibleItems. Pre-fix the
                -- compiler emitted `filtered := rt.Coerce[
                -- State_Bucket_R](filter_result)` — coercing the
                -- list to the unrelated entry-local's type. Search
                -- for the binding's Go output and require that the
                -- coerce target (if any) be a List or Item shape,
                -- never the entry-local's record name.
                body <- readFile (tmp </> "sky-out" </> "main.go")
                let filteredLines = filter ("filtered" `isInfixOf`)
                                    (lines body)
                    badShadow = any (\ln ->
                        "rt.Coerce[State_Bucket_R]" `isInfixOf` ln
                        && "filtered" `isInfixOf` ln) filteredLines
                badShadow `shouldBe` False

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

    writeMultiModule :: FilePath -> IO ()
    writeMultiModule dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"shadow-fixture\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "State.sky") stateSrc
        writeFile (dir </> "src" </> "View.sky") viewSrc
        writeFile (dir </> "src" </> "Main.sky") mainSrc


-- ─── Fixtures ──────────────────────────────────────────────────

stateSrc :: String
stateSrc = unlines
    [ "module State exposing (Bucket, Item)"
    , ""
    , "type alias Bucket ="
    , "    { label : String"
    , "    , kind  : String"
    , "    }"
    , ""
    , "type alias Item ="
    , "    { name  : String"
    , "    , value : Int"
    , "    }"
    ]

-- The dep module that calls `List.filter` (a kernel top-level). HM
-- has to infer the filter result via inferExprType — the entry
-- module's lambda param named "filter" must NOT pollute this
-- lookup.
viewSrc :: String
viewSrc = unlines
    [ "module View exposing (visibleItems)"
    , ""
    , "import State exposing (Item)"
    , ""
    , "visibleItems : List Item -> List Item"
    , "visibleItems items ="
    , "    let"
    , "        filtered = List.filter keep items"
    , "    in"
    , "        filtered"
    , ""
    , "keep : Item -> Bool"
    , "keep i = i.value > 0"
    ]

-- The entry module — its `useBucket` helper binds a lambda param
-- named `filter` of type Bucket. Pre-fix this leaked into
-- solvedTypes["filter"] and shadowed Sky.Core.List.filter.
mainSrc :: String
mainSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , "import State exposing (Bucket, Item)"
    , "import View"
    , ""
    , "useBucket : String -> Bucket -> String"
    , "useBucket prefix filter ="
    , "    prefix ++ filter.label ++ \":\" ++ filter.kind"
    , ""
    , "sample : List Item"
    , "sample ="
    , "    [ { name = \"a\", value = 1 }"
    , "    , { name = \"b\", value = -1 }"
    , "    , { name = \"c\", value = 2 }"
    , "    ]"
    , ""
    , "main ="
    , "    let"
    , "        b = { label = \"hi\", kind = \"sample\" }"
    , "        msg = useBucket \"L:\" b"
    , "        kept = View.visibleItems sample"
    , "    in"
    , "        println (msg ++ \" \" ++ String.fromInt (List.length kept))"
    ]
