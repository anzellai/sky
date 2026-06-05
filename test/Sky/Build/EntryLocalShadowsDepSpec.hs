module Sky.Build.EntryLocalShadowsDepSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcessMulti)


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
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `compileInProcessMulti` (multi-file project: State.sky
-- + View.sky + Main.sky).  The `app` binary existence check is
-- dropped — in-process compile only emits main.go, never invokes
-- go build — but the load-bearing main.go inspection (the badShadow
-- predicate that gates the regression) is byte-identical.
spec :: Spec
spec = describe "entry-local does not shadow dep top-level in solvedTypes" $ do
    it "lets a dep call with the same name as an entry-local stay typed" $ do
        result <- compileInProcessMulti
            [ ("src/State.sky", stateSrc)
            , ("src/View.sky",  viewSrc)
            , ("src/Main.sky",  mainSrc)
            ]
        case result of
            CompileErr e -> expectationFailure ("compile failed:\n" ++ e)
            CompileOk body -> do
                -- The dep call `List.filter keep items` returns
                -- `List Item` and lives in a `let filtered = ...`
                -- binding inside View.visibleItems. Pre-fix the
                -- compiler emitted `filtered := rt.Coerce[
                -- State_Bucket_R](filter_result)` — coercing the
                -- list to the unrelated entry-local's type. Search
                -- for the binding's Go output and require that the
                -- coerce target (if any) be a List or Item shape,
                -- never the entry-local's record name.
                let filteredLines = filter ("filtered" `isInfixOf`)
                                    (lines body)
                    badShadow = any (\ln ->
                        "rt.Coerce[State_Bucket_R]" `isInfixOf` ln
                        && "filtered" `isInfixOf` ln) filteredLines
                badShadow `shouldBe` False


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
