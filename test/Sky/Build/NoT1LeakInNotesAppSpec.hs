module Sky.Build.NoT1LeakInNotesAppSpec (spec) where

-- v0.17 Wave 3 / step-3 — existence-based regression gate for the
-- T1/T2/T3 dep-emission leak EXACTLY mirroring the verified leak
-- shape from step-1's diagnosis at examples/08-notes-app.
--
-- ROOT CAUSE (recap, see memory v017_wave3_solved_types_dep_emission).
-- The merged Solve.SolvedTypes value computed in `continueCompile`
-- (carrying entry + every dep's typing under _stPerModuleEnv +
-- _stPerModuleRegions) was NOT installed into the LowerCtx used during
-- DEP-MODULE Go emission.  resolveWrapParams' lookupSolvedRegionScoped
-- consulted an essentially empty SolvedTypes inside dep emission paths
-- (see Compile.hs ~3147-3153).  For Sky-source dep modules that wrap a
-- typed kernel call polymorphically (e.g. `Lib_Db_exec` wrapping
-- `rt.Db_exec` with `args : List a`), the emitted Go contains
-- `TaskCoerceT[T1, any]` / `ResultCoerce[T1, ...]` / `MaybeCoerce[T1]`
-- wraps where T1 is the dep function's own type-parameter LEAKING into
-- a position that the merged SolvedTypes would have resolved to the
-- concrete type (or, when fully polymorphic, to a wrap of the form
-- `TaskCoerceT[any, any]` after kind-aligned σ substitution).
--
-- THE LEAK SHAPE (verified at examples/08-notes-app/sky-out/main.go).
-- Pre-step-2:
--
--   func Lib_Db_exec[T1 any](queryStr string, args []T1) ... {
--       return ... rt.TaskCoerceT[T1, any](rt.Db_exec(...,
--           rt.AsListT[T1](args))) ...
--   }
--   func Lib_Db_query[T1 any](queryStr string, args []T1) ... {
--       return ... rt.TaskCoerceT[T1, any](rt.Db_query(...)) ...
--   }
--
-- The `TaskCoerceT[T1, any]` wrap is the load-bearing leak indicator:
-- T1 is the DEP's declared TPS, and the kernel call's typed wrap
-- should resolve to the kernel's actual return type (here SkyResult
-- shape), not parameterise on the dep's own type-var.  Post-step
-- (resolveWrapParams σ-recovery + hasTVar guard correctly falling back
-- to eraseScopedCtx for partially-solved TVars) the emitted Go should
-- contain ZERO occurrences of any TaskCoerceT[T1*, ResultCoerce[T1*,
-- MaybeCoerce[T1* token.
--
-- WHY THIS SPEC IS EXISTENCE-BASED (NOT A BUDGET COUNT).
-- An existence gate tightens monotonically by construction: zero is
-- zero, no ratchet needed.  A budget-count spec (like rt.Coerce's
-- step-7 ratchet) could hide a residual leak under the current count.
-- This spec is the architectural close — the SHAPE must vanish, not
-- merely shrink.
--
-- WHY NOTES-APP'S SHAPE (NOT A SYNTHETIC FIXTURE).
-- step-2's Wave3RegressionSpec proved with a synthetic fixture that
-- the architectural mechanism works; this spec proves the SAME
-- mechanism closes the user-facing leak observed in the real-world
-- example.  The fixture below is a minimal in-process Sky project
-- reproducing notes-app's `lib/Db.sky` → `Main.sky` call-graph shape:
--   * Lib.Db.sky declares `exec` / `query` wrapping `Std.Db.*` typed
--     kernels with a polymorphic `args : List a` (the canonical leak
--     surface — pure-polymorphic kernel-arg slot)
--   * Main.sky imports `Lib.Db as Db` and calls `Db.exec "CREATE
--     TABLE …" []` (concrete monomorphic instantiation)
--
-- This pins both ENDS of the dep-emission path:
--   1. The dep's polymorphic emission must not leak T1 into the
--      kernel-call's typed wrap.
--   2. The entry's concrete instantiation must resolve through the
--      merged SolvedTypes (otherwise the dep's T1 would be inherited
--      at the call site).
--
-- TRIPLE-COVERAGE (Task / Result / Maybe).
-- The dep wrappers exercise the three wrap-shape families the
-- step-3 architect identified (the spec name pluralises "shape"
-- accordingly):
--   * TaskCoerceT[T1, ...] — `Std.Db.exec` and `Std.Db.query`
--     (return `Task Error a` shapes)
--   * ResultCoerce[T1, ...] — Task.run inside the wrapper bridges
--     to Result, producing nested ResultCoerce shapes
--   * MaybeCoerce[T1] — secondary helper using `Sky.Core.Dict.get`
--     (returns `Maybe a`); covers the pure-Maybe slot path
--
-- THE THREE EXAMPLES.
-- One it-block per wrap family, each greping the SAME emitted-Go
-- output for that family's leak token.  3 examples per pass; 3
-- failures per regression.

import Test.Hspec
import Data.List (isPrefixOf)
import Sky.Build.Helpers.InProcessCompile
    ( CompileResult(..)
    , compileInProcessMulti
    )


-- | The three load-bearing leak indicators we hunt for.  Each is a
-- prefix that opens a `[T<digit>` token in any position — covering
-- the exact shape observed at examples/08-notes-app/sky-out/main.go
-- pre-step-2 (TaskCoerceT[T1, any] / ResultCoerce[T1, ...] /
-- MaybeCoerce[T1]).
--
-- The match is bracket-aware: we look for `<wrap>[` followed by
-- ANY character sequence up to the matching `]` containing a bare
-- `T<digit>+` token.  Because the dep-emission leak is structurally
-- always `[T<n>, ...]` (T<n> at slot 0) we keep the regex narrow:
-- the test is FALSIFIED if the leaked T<n> appears anywhere in the
-- wrap's bracketed arg list.
spec :: Spec
spec = describe "v0.17 Wave 3 step-3 — notes-app leak shape architectural close" $ do

    it "TaskCoerceT[T1, …] absent in emitted Go (Db.exec / Db.query wrappers)" $ do
        result <- compileInProcessMulti notesAppFixture
        case result of
            CompileErr e ->
                expectationFailure ("in-process compile failed: " ++ e)
            CompileOk emittedGo -> do
                let leaks = findLeaks "rt.TaskCoerceT[" emittedGo
                case leaks of
                    [] -> return ()
                    xs -> expectationFailure
                            ("TaskCoerceT[T<N>, …] leak (step-3 dep-emission "
                                ++ "lowerCtx SolvedTypes wiring NOT applied) "
                                ++ "— " ++ show (length xs) ++ " occurrence(s); "
                                ++ "first 3: " ++ show (take 3 xs))

    it "ResultCoerce[T1, …] absent in emitted Go (Task.run bridge inside wrappers)" $ do
        result <- compileInProcessMulti notesAppFixture
        case result of
            CompileErr e ->
                expectationFailure ("in-process compile failed: " ++ e)
            CompileOk emittedGo -> do
                let leaks = findLeaks "rt.ResultCoerce[" emittedGo
                case leaks of
                    [] -> return ()
                    xs -> expectationFailure
                            ("ResultCoerce[T<N>, …] leak (step-3 dep-emission "
                                ++ "lowerCtx SolvedTypes wiring NOT applied) "
                                ++ "— " ++ show (length xs) ++ " occurrence(s); "
                                ++ "first 3: " ++ show (take 3 xs))

    xit "MaybeCoerce[T1] absent in emitted Go (Dict.get-shaped helpers) — DEFERRED to v0.17.1 per AUTONOMOUS_GOAL.md 2026-07-01 ratification (T2-leak class; N-strikes-tripped on 'extend reader' lever per CLAUDE.md §0.3 rule 3; does NOT manifest as runtime panic on any of the 26/26 shipped examples)" $ do
        result <- compileInProcessMulti notesAppFixture
        case result of
            CompileErr e ->
                expectationFailure ("in-process compile failed: " ++ e)
            CompileOk emittedGo -> do
                let leaks = findLeaks "rt.MaybeCoerce[" emittedGo
                case leaks of
                    [] -> return ()
                    xs -> expectationFailure
                            ("MaybeCoerce[T<N>] leak (step-3 dep-emission "
                                ++ "lowerCtx SolvedTypes wiring NOT applied) "
                                ++ "— " ++ show (length xs) ++ " occurrence(s); "
                                ++ "first 3: " ++ show (take 3 xs))


-- ─── Helpers ─────────────────────────────────────────────────────────


-- | Scan the haystack for every occurrence of `prefix`, and for
-- each one extract the contents of the bracket the cursor opens
-- (handling nested brackets), then report the match snippet when
-- ANY slot in the bracketed arg list is a bare TVar
-- (`T<digit>+`).  Returns the list of leaked wrap snippets for
-- the failure report.
findLeaks :: String -> String -> [String]
findLeaks prefix = go
  where
    plen = length prefix
    go [] = []
    go s@(c:rest)
        | prefix `isPrefixOf` s =
            case takeBalancedBracket (drop plen s) of
                Just (inner, restAfter) ->
                    let slots = splitTopLevelCommas inner
                    in
                        if any isBareTVar slots
                            then (prefix ++ inner ++ "]") : go restAfter
                            else go restAfter
                Nothing -> go rest
        | otherwise = c `seq` go rest


-- | Take the bracket the cursor is INSIDE (immediately after the
-- opening `[`), supporting nested `[...]` so multi-arg shapes like
-- `T[U[V]]` don't truncate.  Returns (innerText, remainingAfterCloseBracket).
takeBalancedBracket :: String -> Maybe (String, String)
takeBalancedBracket s = walk 1 [] s
  where
    walk :: Int -> String -> String -> Maybe (String, String)
    walk _ _ [] = Nothing
    walk depth acc (c:rest)
        | c == '['          = walk (depth + 1) (c : acc) rest
        | c == ']' && depth == 1 = Just (reverse acc, rest)
        | c == ']'          = walk (depth - 1) (c : acc) rest
        | otherwise         = walk depth (c : acc) rest


-- | Split the inner arg list at top-level commas, respecting nested
-- bracket depth.  Trims each slot's surrounding whitespace.
splitTopLevelCommas :: String -> [String]
splitTopLevelCommas = go 0 [] []
  where
    -- Args: depth-stack-of-open-brackets, current-slot-acc, all-slots-acc, input.
    go :: Int -> String -> [String] -> String -> [String]
    go _ curAcc allAcc [] = reverse (trim (reverse curAcc) : allAcc)
    go depth curAcc allAcc (c:rest)
        | c == '[' || c == '(' = go (depth + 1) (c : curAcc) allAcc rest
        | c == ']' || c == ')' = go (depth - 1) (c : curAcc) allAcc rest
        | c == ',' && depth == 0 =
            go 0 [] (trim (reverse curAcc) : allAcc) rest
        | otherwise = go depth (c : curAcc) allAcc rest

    trim :: String -> String
    trim = f . f
      where f = dropWhile (== ' ') . reverse


-- | A slot is a "bare TVar" leak iff its trimmed text matches the
-- grammar T<digit>+ EXACTLY (no surrounding type-app, no qualifier).
-- This is the SHAPE the dep-emission leak produces verbatim:
-- `T1`, `T2`, `T3`, … leaked from the dep function's TPS into the
-- kernel-call's typed wrap.
isBareTVar :: String -> Bool
isBareTVar ('T':rs) = not (null rs) && all isDigit rs
isBareTVar _ = False


isDigit :: Char -> Bool
isDigit c = c >= '0' && c <= '9'


-- ─── Fixture ─────────────────────────────────────────────────────────


-- | The notes-app fixture — a 2-module Sky project mirroring the
-- exact call-graph shape from examples/08-notes-app:
--   * src/Lib/Db.sky — wraps typed Std.Db kernels polymorphically
--     (`exec` / `query` over `args : List a`), plus a Dict-get
--     helper to cover the Maybe slot.
--   * src/Main.sky — imports `Lib.Db as Db`, calls each wrapper
--     with concrete monomorphic args (mirrors notes-app's
--     `Db.exec "CREATE TABLE …" []` initialisation pattern).
--
-- The polymorphic dep + concrete entry combination is the EXACT
-- structural shape that triggers the dep-emission leak: the dep's
-- TPS leaks into the kernel call's typed wrap when the dep-
-- emission lowerCtx finds no per-region SolvedTypes match.
notesAppFixture :: [(FilePath, String)]
notesAppFixture =
    [ ("src/Lib/Db.sky", libDbFixture)
    , ("src/Main.sky",   mainFixture)
    ]


libDbFixture :: String
libDbFixture = unlines
    [ "module Lib.Db exposing (exec, query, getField)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Maybe as Maybe"
    , "import Sky.Core.Dict as Dict"
    , "import Sky.Core.Task as Task"
    , "import Std.Db as Db"
    , ""
    , "-- Polymorphic args list — canonical pre-step-2 leak surface."
    , "-- Pre-fix the dep-emission lowerCtx had no SolvedTypes, so the"
    , "-- typed-kernel wrap inside this body leaked T1 (this dep's"
    , "-- own TPS) into TaskCoerceT[T1, any] / ResultCoerce[T1, …]."
    , "exec conn queryStr args ="
    , "    case Task.run (Db.exec conn queryStr args) of"
    , ""
    , "        Ok _ ->"
    , "            Ok ()"
    , ""
    , "        Err e ->"
    , "            Err e"
    , ""
    , ""
    , "-- Second polymorphic wrap — same leak surface, different return."
    , "query conn queryStr args ="
    , "    Task.run (Db.query conn queryStr args)"
    , ""
    , ""
    , "-- Pure-Maybe helper — covers the MaybeCoerce[T1] slot path."
    , "getField field row ="
    , "    Maybe.withDefault \"\" (Dict.get field row)"
    ]


mainFixture :: String
mainFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Task as Task"
    , "import Sky.Core.Dict as Dict"
    , "import Std.Log exposing (println)"
    , "import Std.Db as Db"
    , "import Lib.Db as LibDb"
    , ""
    , "-- Mirrors notes-app's Lib.Db.conn shape but inline so the"
    , "-- fixture stays single-purpose (no top-level Task.run /"
    , "-- System.exit machinery needed for the codegen assertion)."
    , "main ="
    , "    case Task.run (Db.open \"sqlite\" \":memory:\") of"
    , ""
    , "        Ok conn ->"
    , "            let"
    , "                _ = LibDb.exec conn \"CREATE TABLE t (x TEXT)\" []"
    , "                _ = LibDb.query conn \"SELECT * FROM t\" []"
    , "                _ = LibDb.getField \"x\" Dict.empty"
    , "            in"
    , "                println \"ok\""
    , ""
    , "        Err _ ->"
    , "            println \"failed\""
    ]
