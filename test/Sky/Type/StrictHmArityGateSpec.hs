module Sky.Type.StrictHmArityGateSpec (spec) where

-- v0.17 closure plan / step-3 — Strict HM arity gate spec.
--
-- This is the POST-FIX regression contract for the strict HM
-- arity gate (CLAUDE.md §Limitation #7).  Eight fixtures land
-- here in step-3 as `pending` placeholders; step-4 implements
-- the gate in Sky.Type and FLIPS the pendings to live assertions
-- (negative -> compileErr-asserting FAIL gate; positive ->
-- compileOk-asserting PASS gate).
--
-- ─────────────────────────────────────────────────────────────
-- WHAT THE GATE GUARDS
-- ─────────────────────────────────────────────────────────────
--
-- Today (pre-fix, documented as Limitation #7 in CLAUDE.md):
-- zero-arg calls and value-slot references follow the binding's
-- declared type, not its FFI-vs-kernel origin.  Mismatches
-- between "called with ()" and "declared : String", or "called
-- bare" and "declared () -> X" silently slip past HM:
--
--   Uuid.v4 ()              -- v4 : String        — should reject
--   Time.now                -- Time.now : () -> Task Error Int
--                           --   in a value-slot — should reject
--
-- Both shapes are caller mistakes that HM can catch the moment
-- the call shape and the declared shape disagree.  Step-4 adds
-- the gate; step-3 (this file) writes the regression contract
-- the gate has to satisfy when it lands.
--
-- ─────────────────────────────────────────────────────────────
-- WHAT MUST NOT REGRESS
-- ─────────────────────────────────────────────────────────────
--
-- The gate is a SHARPENING of HM arity-checking.  Three closed
-- behaviours must keep working byte-identical after step-4:
--
--   1. Head-alias unfold (v0.16.4 contributor PR #123) —
--      `myHandler : Handler` over
--      `type alias Handler = Request -> Task Error Response`
--      must still compile.  See
--      Sky.Canonicalise.HeadAliasFunctionSigSpec for the canonical
--      gate; we add a SECOND lock here at the value-slot arity
--      layer so step-4's gate cannot accidentally re-shadow the
--      type alias.
--
--   2. Pure.* canonical mitigation (v0.15.50 / task #395) —
--      `Pure.uuidV4 ()` is the user-directive `() -> Task Error T`
--      uniform surface that exists EXACTLY to be called with ().
--      Step-4's gate must exempt this shape.
--
--   3. Wildcard-`any` soundness (v0.15.x) — Forall with at least
--      one non-`any` free var is REAL polymorphism and must stay
--      flexible.  Wildcard-only Forall (every free var is `any`)
--      is NON-polymorphic for the gate's purposes and must be
--      treated EXACTLY like a monomorphic binding so the silent
--      acceptance of a wildcard return mismatch does NOT
--      re-open.
--
-- ─────────────────────────────────────────────────────────────
-- TIER 1 — IN-PROCESS COMPILATION
-- ─────────────────────────────────────────────────────────────
--
-- Uses Sky.Build.Helpers.InProcessCompile.compileInProcess to
-- avoid spawning `sky build` subprocesses (task #491).  When
-- step-4 flips the pendings, the negative arms assert on
-- `CompileErr` with diagnostic substring matching; the positive
-- arms assert on `CompileOk`.

import Data.List (isInfixOf)
import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess, compileInProcessMulti)


-- | Marker for step-4 — every pending case below carries this
-- string so the step-4 author can grep for the flip points.
flipMarker :: String
flipMarker = "TODO step-4: flip to live assertion when gate lands"


spec :: Spec
spec = do
    describe "Strict HM arity gate" $ do

        ------------------------------------------------------------
        -- NEGATIVE: kernel-side mismatches
        ------------------------------------------------------------

        -- k-a: Uuid.v4's Sky-side sig is `: Task Error String` (the
        -- runtime kernel returns a thunk; v0.17 Uuid.sky was retyped
        -- per the Limitation #7 audit so call shape is `Uuid.v4 |>
        -- Task.run`).  Calling it with `()` mistakes it for the
        -- companion `() -> Task Error String` arrow form and is a
        -- type error.  PR-C (iter 31) wires the gate at
        -- 'constrainCall' — flipped to live CompileErr assertion.
        it "k-a: rejects Uuid.v4 () (kernel value, declared 0-arg)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Uuid as Uuid"
                    , ""
                    , "main ="
                    , "    println (Uuid.v4 ())"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure
                    "expected E2007 arity mismatch, got CompileOk"
                CompileErr e -> do
                    e `shouldSatisfy` ("[E2007]" `isInfixOf`)
                    e `shouldSatisfy` ("Arity mismatch" `isInfixOf`)
                    e `shouldSatisfy` ("Uuid" `isInfixOf`)
                    e `shouldSatisfy` ("0-arg" `isInfixOf`)
                    e `shouldSatisfy` ("1 args" `isInfixOf`)

        -- k-b: Time.now has Sky-side type `() -> Task Error Int`.
        -- Reading it bare in a `Task Error Int` value-slot is a
        -- type error (the value IS the function, not the result
        -- of calling it).  PR-D (iter 32) wires the gate at the
        -- 'Can.VarTopLevel' arm via 'valueSlotGateForTopLevel' —
        -- flipped to live CompileErr assertion.
        it "k-b: rejects bare Time.now in Task Error Int slot (kernel arrow)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Time as Time"
                    , "import Sky.Core.Task as Task"
                    , ""
                    , "doNow : Task Error Int"
                    , "doNow ="
                    , "    Time.now"
                    , ""
                    , "main ="
                    , "    let _ = doNow"
                    , "    in"
                    , "        println \"done\""
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure
                    "expected E2007 arity mismatch, got CompileOk"
                CompileErr e -> do
                    e `shouldSatisfy` ("[E2007]" `isInfixOf`)
                    e `shouldSatisfy` ("Arity mismatch" `isInfixOf`)
                    e `shouldSatisfy` ("Time" `isInfixOf`)
                    e `shouldSatisfy` ("1-arg" `isInfixOf`)
                    e `shouldSatisfy` ("0 args" `isInfixOf`)

        ------------------------------------------------------------
        -- NEGATIVE: user-binding mismatches
        ------------------------------------------------------------

        -- u-a: Symmetric to k-a but at a USER binding.  PR-C (iter
        -- 31) reads the same-module annotation via
        -- 'globalSameModAnnots' so the user binding's declared
        -- 0-arity surfaces the same E2007 diagnostic as kernel bindings.
        it "u-a: rejects foo () when foo : String (user value)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "foo : String"
                    , "foo = \"hi\""
                    , ""
                    , "main ="
                    , "    println (foo ())"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure
                    "expected E2007 arity mismatch, got CompileOk"
                CompileErr e -> do
                    e `shouldSatisfy` ("[E2007]" `isInfixOf`)
                    e `shouldSatisfy` ("Arity mismatch" `isInfixOf`)
                    e `shouldSatisfy` ("foo" `isInfixOf`)
                    e `shouldSatisfy` ("0-arg" `isInfixOf`)
                    e `shouldSatisfy` ("1 args" `isInfixOf`)

        -- u-b: Symmetric to k-b but at a USER binding.  Bare
        -- reference to `bar : () -> String` in a String value-slot
        -- is a type error — the gate must surface the arity
        -- mismatch, not silently degrade through the wildcard
        -- branch.
        it "u-b: rejects bare bar in String slot when bar : () -> String (user arrow)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "bar : () -> String"
                    , "bar () = \"hi\""
                    , ""
                    , "msg : String"
                    , "msg = bar"
                    , ""
                    , "main ="
                    , "    println msg"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure
                    "expected E2007 arity mismatch, got CompileOk"
                CompileErr e -> do
                    e `shouldSatisfy` ("[E2007]" `isInfixOf`)
                    e `shouldSatisfy` ("Arity mismatch" `isInfixOf`)
                    e `shouldSatisfy` ("bar" `isInfixOf`)
                    e `shouldSatisfy` ("1-arg" `isInfixOf`)
                    e `shouldSatisfy` ("0 args" `isInfixOf`)

        ------------------------------------------------------------
        -- POSITIVE: shapes that MUST keep compiling after step-4
        ------------------------------------------------------------

        -- h-a: HeadAlias positive — guards v0.16.4 PR #123 closure.
        -- `myHandler : Handler` (over `type alias Handler = Request ->
        -- Task Error Response`) must continue to compile.  The
        -- gate must NOT mis-classify the alias-head shape as an
        -- arity mismatch.
        --
        -- Status update — iter 87 (2026-06-23):
        -- All negative arms (k-a / k-b / u-a / u-b) are now LIVE
        -- per the PR-A → PR-D multi-PR plan shipped iters 29-32
        -- (commits `ccf3c010`, `53d529f4`, `d1394fbc`, `389883cb`).
        -- The earlier "remain pendingWith" framing is historical.
        -- The four POSITIVES below still guard against regressing
        -- HeadAlias / Pure.* / real-polymorphism / wildcard-only —
        -- so any future close attempt that re-tightens the gate
        -- and accidentally over-fires fails fast here.
        it "h-a: HeadAlias positive — myHandler : Handler compiles" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Task as Task"
                    , "import Sky.Http.Server as Server"
                    , "import Sky.Http.Server exposing (Handler, Request, Response)"
                    , ""
                    , "myHandler : Handler"
                    , "myHandler req ="
                    , "    Task.succeed (Server.text \"ok\")"
                    , ""
                    , "main ="
                    , "    println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure
                    ("HeadAlias positive must compile: " ++ e)
                CompileOk _ -> return ()

        -- p-a: Pure.* positive — guards user-directive canonical
        -- mitigation surface (CLAUDE.md §Limitation #7 +
        -- v0.15.50 / task #395).  `Pure.uuidV4 ()` is the uniform
        -- `() -> Task Error T` surface that EXISTS to be called
        -- with `()`; the closure shape's gate must exempt it.
        it "p-a: Pure.* positive — Pure.uuidV4 () compiles" $ do
            -- The pre-flip stub fixture used `Task.perform task cb`
            -- (2 args) which is the Cmd.perform shape, not Task.perform's
            -- 1-arg `Task e a -> Result e a` shape.  The post-flip
            -- fixture stores `Pure.uuidV4 ()` directly as a
            -- `Task Error String` value (the load-bearing assertion
            -- for the canonical-surface guard) — what matters is the
            -- () call typechecks, not how it's subsequently consumed.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Pure as Pure"
                    , ""
                    , "uuidTask : Task Error String"
                    , "uuidTask ="
                    , "    Pure.uuidV4 ()"
                    , ""
                    , "main ="
                    , "    let _ = uuidTask"
                    , "    in"
                    , "        println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure
                    ("Pure.* canonical surface must compile: " ++ e)
                CompileOk _ -> return ()

        -- wp-a: Wildcard-any-with-real-poly positive.  `foo : a ->
        -- a` is REAL polymorphism (the free var `a` is non-`any`)
        -- — the gate must keep this flexible across instantiation.
        -- The closure shape's gate must check `any (/= "any")
        -- freeVars` (per the wildcard-any soundness rule in
        -- CLAUDE.md), NOT `not (null freeVars)`.
        it "wp-a: wildcard-any positive — Forall with non-`any` var stays polymorphic" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "foo : a -> a"
                    , "foo x = x"
                    , ""
                    , "main ="
                    , "    println (foo \"hi\")"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure
                    ("real polymorphism must stay flexible: " ++ e)
                CompileOk _ -> return ()

        -- h-a-cross: HeadAlias positive — cross-module variant.
        -- v0.16.4 PR #123 unfolded the head TAlias inside
        -- 'Sky.Canonicalise.Module' so 'myHandler : Handler' compiles
        -- when 'Handler' is the alias.  This anchor verifies that
        -- the SAME-MODULE-CROSS-FILE shape (dep module declares the
        -- alias + the handler; entry imports + calls it) also
        -- survives — load-bearing for PR-B step 2's externals trace
        -- (Compile.hs:7866 'buildCrossModuleExternalsWithMods' →
        -- generaliseToAnnotation over post-canonicalisation
        -- 'T.Type' — meaning 'globalExternals' annotations are
        -- already head-alias-unfolded when PR-C's gate consults
        -- 'declaredArity').
        --
        -- See docs/v0.17-roadmap/strict-hm-arity-gate-design.md for
        -- the full trace.
        it "h-a-cross: HeadAlias positive — cross-module myHandler : Handler compiles" $ do
            let entrySrc = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Task as Task"
                    , "import Lib.Handlers as Handlers"
                    , ""
                    , "main ="
                    , "    let _ = Handlers.myHandler"
                    , "    in"
                    , "        println \"ok\""
                    ]
            let depSrc = unlines
                    [ "module Lib.Handlers exposing (myHandler)"
                    , ""
                    , "import Sky.Core.Task as Task"
                    , "import Sky.Http.Server as Server"
                    , "import Sky.Http.Server exposing (Handler, Request, Response)"
                    , ""
                    , "myHandler : Handler"
                    , "myHandler req ="
                    , "    Task.succeed (Server.text \"ok\")"
                    ]
            result <- compileInProcessMulti
                [ ("src/Main.sky", entrySrc)
                , ("src/Lib/Handlers.sky", depSrc)
                ]
            case result of
                CompileErr e -> expectationFailure
                    ("cross-module HeadAlias positive must compile: " ++ e)
                CompileOk _ -> return ()

        -- wa-a: Wildcard-any-only positive (preserved).  `view :
        -- Model -> any` where every free var is `any` is
        -- NON-polymorphic by the wildcard-any soundness rule.  The
        -- gate must treat it EXACTLY like a monomorphic binding —
        -- so a call shape like `view m` still compiles, and the
        -- silent acceptance of a return mismatch (already closed
        -- in v0.15.1) does NOT re-open.  This fixture verifies
        -- the wildcard-`any` branch with a SOUND program; the
        -- corresponding unsound shape is already locked by
        -- Sky.Type.AnyWildcardSpec.
        it "wa-a: wildcard-any-only positive — `view : Model -> any` sound shape compiles" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , ""
                    , "view : Model -> any"
                    , "view m = m.count"
                    , ""
                    , "main ="
                    , "    let _ = view { count = 1 }"
                    , "    in"
                    , "        println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure
                    ("wildcard-only Forall sound shape must compile: " ++ e)
                CompileOk _ -> return ()


