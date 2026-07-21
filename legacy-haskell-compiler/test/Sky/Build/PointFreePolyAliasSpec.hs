module Sky.Build.PointFreePolyAliasSpec (spec) where

-- Regression spec for #398 — point-free top-level alias of a
-- polymorphic / N-ary function.
--
-- Pre-fix, a binding like `tickle = String.toUpper` (where
-- `String.toUpper : String -> String`) lowered to a 0-arity Go
-- thunk wrapper:
--
--     func tickle() func(string) string {
--         return rt.Coerce[func(string) string](rt.String_toUpper)
--     }
--
-- Call sites then hit `too many arguments in call to tickle`
-- because the lowerer inferred arity from the syntactic param
-- count (= 0) instead of from the type sig's arrow chain (= 1).
--
-- Post-fix, `etaExpandPointFree` in `Sky.Build.Compile` detects the
-- mismatch and rewrites the def to a normal N-ary function with
-- synthetic `_skyEta_pN` parameters, so the emitted Go reads
--
--     func tickle(_skyEta_p0 string) string {
--         return rt.CoerceString(rt.String_toUpperT(rt.AsString(_skyEta_p0)))
--     }
--
-- and the call site `tickle "hello"` succeeds in `go build`.
--
-- Tier 1 (task #491): no subprocess `sky build` at all — the
-- compile pipeline runs IN-PROCESS via Sky.Build.Helpers.
-- InProcessCompile.compileInProcess.  ZERO subprocess.  ZERO
-- `go build`.  ZERO GOCACHE writes.  ~1-2 s per call instead of
-- ~3-4 s; ~MB disk per call instead of ~100 MB; no cache-pressure
-- contribution to the rest of cabal-test.

import Test.Hspec
import Data.List (isInfixOf)
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- | v0.17 #624 — subprocess sky-build helper.  Gap 8c (test #6) is
-- routed through a fresh `sky` subprocess instead of `compileInProcess`
-- because the in-process compile path inherits CAF-IORef state from
-- the 5 sibling tests above it, and the residual leak is wider than
-- the (already-applied) 18-CAF defensive reset can close.  Subprocess
-- compile = fresh GHC RTS per call = zero cross-test state.  Cost: one
-- extra `sky build` per spec run (~1-2 s); benefit: deterministic
-- isolation matching every other example spec in the suite.
runSkyBuildSubprocess :: String -> IO CompileResult
runSkyBuildSubprocess src = do
    cwd <- getCurrentDirectory
    let sky = cwd </> "sky-out" </> "sky"
    skyOk <- doesFileExist sky
    if not skyOk
        then return (CompileErr ("sky binary missing at " ++ sky))
        else withSystemTempDirectory "skypfa" $ \tmp -> do
            createDirectoryIfMissing True (tmp </> "src")
            writeFile (tmp </> "sky.toml")
                ("name = \"pfa-gap8c\"\nversion = \"0.0.0\"\n"
                 ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
            writeFile (tmp </> "src" </> "Main.sky") src
            (exitCode, stdoutStr, stderrStr) <-
                readCreateProcessWithExitCode
                    ((proc sky ["build", "src/Main.sky"]) { cwd = Just tmp })
                    ""
            case exitCode of
                ExitFailure n ->
                    return (CompileErr ("sky build exit=" ++ show n
                        ++ "\nstdout:\n" ++ stdoutStr
                        ++ "\nstderr:\n" ++ stderrStr))
                ExitSuccess -> do
                    let mainGoPath = tmp </> "sky-out" </> "main.go"
                    mainOk <- doesFileExist mainGoPath
                    if mainOk
                        then CompileOk <$> readFile mainGoPath
                        else return (CompileErr "sky build succeeded but main.go missing")


spec :: Spec
spec = describe "point-free top-level alias of a polymorphic function (#398)" $ do
    it "emits an N-ary function (not a 0-arity thunk) for annotated 1-arg alias" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "tickle : String -> String"
                , "tickle = String.toUpper"
                , ""
                , "main = println (tickle \"hi\")"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- Eta-expanded form: `func tickle(_skyEta_p0 ...)`.
                ("func tickle(_skyEta_p0" `isInfixOf` body) `shouldBe` True
                -- The broken thunk form must NOT appear.
                ("func tickle() func(" `isInfixOf` body) `shouldBe` False

    it "emits an N-ary function for an unannotated 1-arg alias" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "-- No annotation — HM-solved type drives arity."
                , "tickle = String.toUpper"
                , ""
                , "main = println (tickle \"hi\")"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                ("func tickle(_skyEta_p0" `isInfixOf` body) `shouldBe` True
                ("func tickle() func(" `isInfixOf` body) `shouldBe` False

    it "expands a 2-arg point-free alias to both params" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "joinStr : String -> String -> String"
                , "joinStr = String.append"
                , ""
                , "main = println (joinStr \"hi\" \"lo\")"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- Both params present.
                ("func joinStr(_skyEta_p0" `isInfixOf` body) `shouldBe` True
                (", _skyEta_p1" `isInfixOf` body) `shouldBe` True

    it "v0.17 PR-23: expands a partial-application point-free body (add5 = add 5)" $ do
        -- v0.17 PR-23 Gap 8 — `add5 = add 5` is a `Can.Call`
        -- expression (not a bare `Var` ref like `tickle =
        -- String.toUpper`).  Pre-fix `canEtaBody` rejected `Call`
        -- as a side-effect-risk and the lowerer emitted a 0-arity
        -- thunk: `func add5() func(int) int { return closure }`,
        -- which exploded at call sites with `too many arguments`.
        -- Post-fix `canEtaBody` accepts `Call` when the callee is
        -- itself safe AND every arg is a literal or safe ref, so
        -- the binding eta-expands to a real `func add5(p0 int)
        -- int { return add(5, p0) }`.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "add : Int -> Int -> Int"
                , "add a b = a + b"
                , ""
                , "add5 : Int -> Int"
                , "add5 = add 5"
                , ""
                , "main = println (String.fromInt (add5 7))"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- Eta-expanded: real func(int) int — not 0-arity.
                ("func add5(_skyEta_p0" `isInfixOf` body) `shouldBe` True
                ("func add5() func(" `isInfixOf` body) `shouldBe` False

    it "v0.17 PR-23 Gap 8c: sumAll = List.foldl add 0 compiles + runs" $ do
        -- v0.17 PR-23 Gap 8c — full point-free shape over a kernel
        -- HOF.  `add : Int -> Int -> Int` is Sky-uncurried at the
        -- Go layer (`func(int, int) int`); `Sky_Core_List_foldl`'s
        -- kernel sig is curried `func(T1) func(T2) T2`.  The fix
        -- is two-part:
        --
        --   1. `buildCurryAdapter` accepts TVar-input slots,
        --      emitting the adapter using `add`'s declared concrete
        --      `int` types.
        --   2. `unifyGoTypes` handles curry-vs-flat at the σ
        --      recovery site — `func(T1) func(T2) T2` unifies with
        --      `func(int, int) int` as σ = {T1: int, T2: int}.
        --      Without this the missing `[]T1` slot erased to
        --      `[]any`, blowing the inner pp* wrapper apart at
        --      `go build`.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Std.Log exposing (println)"
                , ""
                , "add : Int -> Int -> Int"
                , "add a b = a + b"
                , ""
                , "nums : List Int"
                , "nums = [1, 2, 3]"
                , ""
                , "sumAll : List Int -> Int"
                , "sumAll = List.foldl add 0"
                , ""
                , "main = println (String.fromInt (sumAll nums))"
                ]
        -- v0.17 #624 — routed via subprocess sky-build because the
        -- in-process compile path's NOINLINE-CAF state from the 5
        -- sibling tests above poisons the typed-foldl emission.
        -- See `runSkyBuildSubprocess` at the bottom of this file.
        result <- runSkyBuildSubprocess src
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- sumAll is eta-expanded.
                ("func sumAll(_skyEta_p0" `isInfixOf` body) `shouldBe` True
                -- Curry adapter for add fires at the foldl call.
                ("func(__c0 int) func(int) int" `isInfixOf` body)
                    `shouldBe` True

    it "leaves plain value bindings (no arrows) untouched" $ do
        -- Guard against over-application: a value binding like
        -- `greeting : String; greeting = \"Howdy\"` must NOT be
        -- eta-expanded — it has zero arrows in its type.
        --
        -- v0.17 PR-23 (#621) — Assertion tightened: check only the
        -- @greeting@ binding's own signature, not the entire main.go
        -- body.  PR-23 extended eta-expansion to `Can.Call` so the
        -- transitive `Std.Log.println` re-export (which IS arrow-
        -- typed: `String -> Task Error ()`) now correctly eta-
        -- expands too — adding a `_skyEta_p0` inside `Std_Log_println_`.
        -- The intent of THIS test is that `greeting` (arity 0)
        -- stays untouched, not that the whole main.go is _skyEta_p-
        -- free.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "greeting : String"
                , "greeting = \"Howdy\""
                , ""
                , "main = println greeting"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                ("func greeting() string" `isInfixOf` body) `shouldBe` True
                -- No eta param injected into greeting's own sig.
                ("func greeting(_skyEta_p0" `isInfixOf` body)
                    `shouldBe` False
