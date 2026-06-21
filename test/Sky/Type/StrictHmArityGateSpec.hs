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

        -- k-a: Uuid.v4 has Sky-side type `: String` (a bare value,
        -- NOT a function).  Calling it with `()` is a type error
        -- the gate must surface.  Today the call sneaks through.
        it "k-a: rejects Uuid.v4 () when v4 : String (kernel value)" $ do
            let _src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Uuid as Uuid"
                    , ""
                    , "main ="
                    , "    println (Uuid.v4 ())"
                    ]
            -- Post-step-4 flip:
            --   result <- compileInProcess _src
            --   case result of
            --       CompileOk _ -> expectationFailure
            --           "expected HM arity error, got CompileOk"
            --       CompileErr e ->
            --           (e `shouldSatisfy`
            --              \msg -> any (`isInfixOf` msg)
            --                  [ "cannot be called with ()"
            --                  , "declared as String"
            --                  ])
            pendingWith flipMarker

        -- k-b: Time.now has Sky-side type `() -> Task Error Int`.
        -- Reading it bare in a `Task Error Int` value-slot is a
        -- type error (the value IS the function, not the result
        -- of calling it).  The gate must catch this.
        it "k-b: rejects bare Time.now in Task Error Int slot (kernel arrow)" $ do
            let _src = unlines
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
            -- Post-step-4 flip:
            --   result <- compileInProcess _src
            --   case result of
            --       CompileOk _ -> expectationFailure
            --           "expected HM arity error, got CompileOk"
            --       CompileErr e ->
            --           (e `shouldSatisfy`
            --              \msg -> any (`isInfixOf` msg)
            --                  [ "must be called as Time.now ()"
            --                  , "declared () -> Task Error Int cannot flow into Task Error Int slot"
            --                  ])
            pendingWith flipMarker

        ------------------------------------------------------------
        -- NEGATIVE: user-binding mismatches
        ------------------------------------------------------------

        -- u-a: Symmetric to k-a but at a USER binding.  Today the
        -- call follows the binding's value-shape declaration; the
        -- gate must reject the unit-call against a bare-value sig.
        it "u-a: rejects foo () when foo : String (user value)" $ do
            let _src = unlines
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
            -- Post-step-4 flip:
            --   result <- compileInProcess _src
            --   case result of
            --       CompileOk _ -> expectationFailure
            --           "expected HM arity error, got CompileOk"
            --       CompileErr _ -> return ()
            pendingWith flipMarker

        -- u-b: Symmetric to k-b but at a USER binding.  Bare
        -- reference to `bar : () -> String` in a String value-slot
        -- is a type error — the gate must surface the arity
        -- mismatch, not silently degrade through the wildcard
        -- branch.
        it "u-b: rejects bare bar in String slot when bar : () -> String (user arrow)" $ do
            let _src = unlines
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
            -- Post-step-4 flip:
            --   result <- compileInProcess _src
            --   case result of
            --       CompileOk _ -> expectationFailure
            --           "expected HM arity error, got CompileOk"
            --       CompileErr _ -> return ()
            pendingWith flipMarker

        ------------------------------------------------------------
        -- POSITIVE: shapes that MUST keep compiling after step-4
        ------------------------------------------------------------

        -- h-a: HeadAlias positive — guards v0.16.4 PR #123 closure.
        -- `myHandler : Handler` (over `type alias Handler = Request ->
        -- Task Error Response`) must continue to compile.  The
        -- gate must NOT mis-classify the alias-head shape as an
        -- arity mismatch.
        --
        -- Iter 27 (2026-06-20): the gate's NEGATIVE arms (k-a / k-b /
        -- u-a / u-b) remain pendingWith because the strict-HM
        -- closure shape is multi-PR work and needs per-commit
        -- adversarial grilling per feedback_v017_per_commit_grill.
        -- Flipping the POSITIVES live LOCKS the four shapes that
        -- must NEVER regress once the gate lands — so any future
        -- closure attempt that breaks HeadAlias / Pure.* /
        -- real-polymorphism / wildcard-only fails fast here.
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


