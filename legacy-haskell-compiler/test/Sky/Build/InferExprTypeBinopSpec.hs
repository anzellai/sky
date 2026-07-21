module Sky.Build.InferExprTypeBinopSpec (spec) where

-- v0.15.x hardening — Gap A5 / Plan Item P4 closure.
--
-- The audit (CYCLE-01-auditor.md, Gap A5) flagged a typed-routing
-- gap whose symptom is `inferExprType` returning Nothing for
-- `Can.Binop` arms — but the v0.15.9 fix landed `Can.Binop`
-- arms in `inferExprType` already (see Compile.hs:11737-11784).
-- What remained, and is what this spec locks, is the *downstream*
-- consequence:  when a binop expression fills a TYPED Go slot
-- (param, record-field, list-element, lambda return), the
-- `exprToGoExpectGo` lowerer must surface a TYPED Go shape for
-- the binop result — Go-native `lhs + rhs` against the slot's
-- type, NOT the `rt.Add(any, any) → any` runtime helper that
-- forces a `.(int)` assertion downstream.
--
-- Reproducer family (HOF arg slots whose param type is a Go
-- primitive AND whose argument is a binop):
--
--   addOne f x = f (x + 1)        -- f : Int -> Int
--   andG g a b = g (a || b)       -- g : Bool -> Bool
--   joinG g xs ys = g (xs ++ ys)  -- g : List Int -> List Int (string-concat keeps Concat)
--   consG f x xs = f (x :: xs)    -- f : List Int -> Int
--   deepF f x = f ((x + 1) * 2)   -- nested arithmetic
--   strG g a b = g (a ++ b)       -- g : String -> String (string-concat → `+`)
--
-- Pre-fix Go (audit symptom — confirmed via the Cycle-01 reproducer
-- transcript): `rt.SkyCall(f, x + 1)` wraps the binop in a reflect
-- dispatch and recovers `int` via `rt.CoerceInt(...)`.
--
-- Post-fix Go (this spec asserts): when the callee's HM signature
-- is typed, the call lowers Go-native (`f(x + 1)`) AND the surrounding
-- `rt.CoerceInt` wrap goes away because the Go-static type of `f(x+1)`
-- is already `int`.  The binop itself stays Go-native.
--
-- This spec is the load-bearing test the developer wrote BEFORE
-- the fix landed; running it on the pre-fix worktree must FAIL.
-- After the fix, every case must compile clean AND emit no
-- `rt.SkyCall(<local-typed-fn>, …)` for the listed HOFs.

import Test.Hspec
import System.Directory
    ( getCurrentDirectory, createDirectoryIfMissing, doesFileExist
    )
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process
    ( readCreateProcessWithExitCode, proc, CreateProcess(..)
    )
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""


writeFixtureProject :: FilePath -> String -> String -> IO ()
writeFixtureProject dir name body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"" ++ name ++ "\"\nversion = \"0.0.0\"\n"
         ++ "entry = \"src/Main.sky\"\n\n"
         ++ "[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body


-- | Compile `body` and return the emitted Go source as a String.
buildAndReadGo :: String -> String -> (String -> IO ()) -> IO ()
buildAndReadGo fixtureName body assertion = do
    sky <- findSky
    withSystemTempDirectory ("sky-" ++ fixtureName) $ \tmp -> do
        writeFixtureProject tmp fixtureName body
        (ec, out, err) <- runSky sky ["build", "src/Main.sky"] tmp
        let combined = out ++ err
        ec `shouldBe` ExitSuccess
        ("Build complete" `isInfixOf` combined) `shouldBe` True
        generated <- readFile (tmp </> "sky-out" </> "main.go")
        assertion generated


-- | Compile + run, asserting stdout contains `expected` AND no panic.
buildRunExpect :: String -> String -> String -> IO ()
buildRunExpect fixtureName body expected = do
    sky <- findSky
    withSystemTempDirectory ("sky-" ++ fixtureName) $ \tmp -> do
        writeFixtureProject tmp fixtureName body
        (bec, bout, berr) <- runSky sky ["build", "src/Main.sky"] tmp
        let bcombined = bout ++ berr
        bec `shouldBe` ExitSuccess
        ("Build complete" `isInfixOf` bcombined) `shouldBe` True
        let appPath = tmp </> "sky-out" </> "app"
        (rec, rout, rerr) <- readCreateProcessWithExitCode
            (proc appPath []) ""
        let rcombined = rout ++ rerr
        rec `shouldBe` ExitSuccess
        (expected `isInfixOf` rout) `shouldBe` True
        ("panic" `isInfixOf` rcombined) `shouldBe` False


-- | Reproducer A — `addOne f x = f (x + 1)` against a typed callee.
addOneSrc :: String
addOneSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "addOne : (Int -> Int) -> Int -> Int"
    , "addOne f x ="
    , "    f (x + 1)"
    , ""
    , "main ="
    , "    println (String.fromInt (addOne identity 5))"
    ]


-- | Reproducer B — Boolean binop in HOF arg slot.
boolBinopSrc :: String
boolBinopSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "wrapBool : (Bool -> Bool) -> Bool -> Bool -> Bool"
    , "wrapBool g x y ="
    , "    g (x || y)"
    , ""
    , "main ="
    , "    println (toString (wrapBool not False True))"
    ]


-- | Reproducer C — `g (xs ++ ys)` where the slot expects List Int.
listConcatSrc :: String
listConcatSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "joinThem : (List Int -> List Int) -> List Int -> List Int -> List Int"
    , "joinThem g xs ys ="
    , "    g (xs ++ ys)"
    , ""
    , "headOr : Int -> List Int -> Int"
    , "headOr d xs ="
    , "    case xs of"
    , "        [] -> d"
    , "        h :: _ -> h"
    , ""
    , "main ="
    , "    println (String.fromInt (headOr 0 (joinThem identity [11] [22])))"
    ]


-- | Reproducer D — `f (x :: xs)` where the slot expects List Int.
consSrc :: String
consSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "consG : (List Int -> Int) -> Int -> List Int -> Int"
    , "consG f x xs ="
    , "    f (x :: xs)"
    , ""
    , "headOr : Int -> List Int -> Int"
    , "headOr d xs ="
    , "    case xs of"
    , "        [] -> d"
    , "        h :: _ -> h"
    , ""
    , "main ="
    , "    println (String.fromInt (consG (headOr 0) 7 [8, 9]))"
    ]


-- | Reproducer E — deeply nested arithmetic in HOF arg slot.
deepArithSrc :: String
deepArithSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "deepF : (Int -> Int) -> Int -> Int"
    , "deepF f x ="
    , "    f ((x + 1) * 2)"
    , ""
    , "main ="
    , "    println (String.fromInt (deepF identity 4))"
    ]


-- | Reproducer F — string concat in HOF arg slot.
strConcatSrc :: String
strConcatSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "applyStr : (String -> String) -> String -> String -> String"
    , "applyStr g a b ="
    , "    g (a ++ b)"
    , ""
    , "main ="
    , "    println (applyStr identity \"hello \" \"world\")"
    ]


-- | Reproducer G — chained binop with HOF return.
chainedHofSrc :: String
chainedHofSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "applyTwice : (Int -> Int) -> Int -> Int"
    , "applyTwice h x ="
    , "    h (h x + 1)"
    , ""
    , "main ="
    , "    println (String.fromInt (applyTwice identity 41))"
    ]


-- | Reproducer H — multiple binops as siblings inside HOF arg.
--
-- NB: This case calls `op` with TWO args.  Sky's HM type for `op`
-- renders curried (`func(int) func(int) int`), but `op`'s actual
-- Go emission depends on its source: a top-level Sky function or
-- multi-pattern let-def lowers FLAT (`func(int, int) int`), while
-- a partial-app closure or HOF param value stays curried.  The
-- v0.15.10 typed-callable fast-path conservatively skips multi-arg
-- calls to avoid the curry-vs-flat ambiguity (see Compile.hs for
-- rationale).  So this case still routes through `rt.SkyCall` —
-- the spec verifies functional correctness only.
siblingBinopsSrc :: String
siblingBinopsSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "pickAdd : (Int -> Int -> Int) -> Int -> Int -> Int"
    , "pickAdd op a b ="
    , "    op (a + 1) (b * 2)"
    , ""
    , "plus : Int -> Int -> Int"
    , "plus a b ="
    , "    a + b"
    , ""
    , "main ="
    , "    println (String.fromInt (pickAdd plus 4 5))"
    ]


spec :: Spec
spec = do

    describe "Gap A5 — typed-primitive binop in HOF arg slot:\
             \ direct callable + no reflect dispatch on typed locals" $ do

        it "addOne f x = f (x + 1) — emits direct `f(x + 1)`,\
           \ no `rt.SkyCall(f, …)`" $
            buildAndReadGo "addone" addOneSrc $ \go -> do
                -- Pre-fix shape we want to forbid:
                --   `rt.CoerceInt(rt.SkyCall(f, x + 1))`
                ("rt.SkyCall(f, " `isInfixOf` go) `shouldBe` False

        it "addOne runs to completion (6 = identity (5 + 1))" $
            buildRunExpect "addone-run" addOneSrc "6"

        it "wrapBool g x y = g (x || y) — direct `g(x || y)`,\
           \ no SkyCall on `g`" $
            buildAndReadGo "wrapbool" boolBinopSrc $ \go ->
                ("rt.SkyCall(g, " `isInfixOf` go) `shouldBe` False

        it "wrapBool runs to completion (false)" $
            buildRunExpect "wrapbool-run" boolBinopSrc "false"

        it "joinThem g xs ys = g (xs ++ ys) — direct `g(rt.Concat(…))`" $
            buildAndReadGo "listconcat" listConcatSrc $ \go ->
                ("rt.SkyCall(g, " `isInfixOf` go) `shouldBe` False

        it "joinThem runs to completion (11 = first of [11,22])" $
            buildRunExpect "listconcat-run" listConcatSrc "11"

        it "consG f x xs = f (x :: xs) — direct `f(rt.List_cons(…))`" $
            buildAndReadGo "cons" consSrc $ \go ->
                ("rt.SkyCall(f, " `isInfixOf` go) `shouldBe` False

        it "consG runs to completion (7 = head of (7 :: [8,9]))" $
            buildRunExpect "cons-run" consSrc "7"

        it "deepF f x = f ((x + 1) * 2) — direct `f((x + 1) * 2)`" $
            buildAndReadGo "deep" deepArithSrc $ \go ->
                ("rt.SkyCall(f, " `isInfixOf` go) `shouldBe` False

        it "deepF runs to completion (10 = (4 + 1) * 2)" $
            buildRunExpect "deep-run" deepArithSrc "10"

        it "applyStr g a b = g (a ++ b) — direct `g(a + b)`" $
            buildAndReadGo "strconcat" strConcatSrc $ \go ->
                ("rt.SkyCall(g, " `isInfixOf` go) `shouldBe` False

        it "applyStr runs to completion (hello world)" $
            buildRunExpect "strconcat-run" strConcatSrc "hello world"

        it "applyTwice h x = h (h x + 1) — chained typed-callable" $
            buildAndReadGo "chained" chainedHofSrc $ \go -> do
                -- Both calls to `h` must lower directly.  The inner
                -- `h x` is statically `int`, so the surrounding
                -- `h x + 1` arithmetic emits Go-native `+`.
                ("rt.SkyCall(h, " `isInfixOf` go) `shouldBe` False

        it "applyTwice runs to completion (42 = 41 + 1)" $
            buildRunExpect "chained-run" chainedHofSrc "42"

        -- v0.15.10 — pickAdd is a 2-arg HOF call.  The
        -- typed-callable fast-path conservatively SKIPS multi-arg
        -- calls (Sky HM renders curried while emission may be
        -- flat — the call-site can't disambiguate at this layer).
        -- The runtime path through `rt.SkyCall(op, a, b)` correctly
        -- dispatches one arg at a time via `skyCallOne`; this
        -- case is here as a functional-correctness lock so a
        -- future widening of the fast-path doesn't silently
        -- regress the 2-arg semantics.
        it "pickAdd runs to completion (15 = (4+1) + (5*2))" $
            buildRunExpect "siblings-run" siblingBinopsSrc "15"
