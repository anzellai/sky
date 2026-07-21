module Sky.Type.AnyWildcardSpec (spec) where

-- Regression fence for the cross-branch HM `any` wildcard fix
-- (compiler bug #3).
--
-- Pre-fix bug: distinct occurrences of `T.TVar "any"` in source
-- types collapsed to a single fresh unification variable via the
-- solver's `_varCache`. Cross-branch case unification then resolved
-- the shared `any` slot to whatever concrete type appeared first
-- (typically from a sister constructor that didn't use `any`), and
-- subsequent uses at construction sites failed with
-- `Type mismatch: <actual> vs <other-branch's-type>`.
--
-- Fix: in `Sky.Type.Solve.typeToVar`, treat `T.TVar "any"` as a
-- WILDCARD — every occurrence gets its own fresh unification
-- variable, never shared via the cache. This restores the "any
-- unifies with anything, independently" semantics users expect.
--
-- Tier 1 (task #491): in-process via compileInProcess. The
-- pre-Tier-1 spec ran the produced binary to confirm the runtime
-- behaviour ("got something" / "both unwraps type-checked" /
-- "two-any-args ok") — the bug-shape was a compile-time HM type
-- error, so a clean CompileOk is the actual regression check.
-- Runtime behaviour of `any`-typed values is covered by
-- runtime-go/rt/*_test.go.

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "`any` is a wildcard, not a shared type variable" $ do

        it "ADT with mixed concrete + any-typed branches type-checks across both" $ do
            -- Pre-fix: `case` arm crossing String + any branches
            -- pinned `any` to String, then construction `AttrB 42`
            -- failed with `Int vs String`.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type MyAttr"
                    , "    = AttrA String"
                    , "    | AttrB any"
                    , ""
                    , "toMaybe : MyAttr -> Maybe any"
                    , "toMaybe a ="
                    , "    case a of"
                    , "        AttrA s -> Just s"
                    , "        AttrB v -> Just v"
                    , ""
                    , "main ="
                    , "    case toMaybe (AttrB 42) of"
                    , "        Just _  -> println \"got something\""
                    , "        Nothing -> println \"got nothing\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "same-name `any` in function sig + ctor arg do not share" $ do
            -- Function takes `Maybe any` and produces `any` —
            -- the input `any` and the return `any` must be
            -- independent. Pre-fix they would collapse and
            -- wrap-then-extract chains would fail.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Box = Box any"
                    , ""
                    , "unwrap : Box -> any"
                    , "unwrap b ="
                    , "    case b of"
                    , "        Box v -> v"
                    , ""
                    , "main ="
                    , "    let"
                    , "        s = unwrap (Box \"hello\")"
                    , "        n = unwrap (Box 42)"
                    , "        _ = println \"both unwraps type-checked\""
                    , "    in"
                    , "        println \"done\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "two ctor args both `any` get independent unification slots" $ do
            -- Both fields of the constructor are `any` — they must
            -- NOT share a type slot. Pre-fix the two `any` in
            -- `Pair any any` collapsed and the second arg was
            -- forced to match the first.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Pair = Pair any any"
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = Pair \"hello\" 42"
                    , "        _ = Pair 1 \"world\""
                    , "    in"
                    , "        println \"two-any-args ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()
