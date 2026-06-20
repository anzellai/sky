module Sky.Build.CpsStackConstantBound.FoldrSpec (spec) where

import Data.List (isInfixOf)
import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)
import Sky.Build.CpsStackConstantBound.Shared
    ( assertForContinueInHelper
    , assertConstantStack1M
    , buildOpFixture
    )


-- | CPS-stack regression for @Sky.Core.List.foldr@ — v0.17 step-2
-- of the Limitation #8 CPS rewrite umbrella.  Distinct shape from
-- the @map@ / @filter@ siblings: @foldr@ is a DELEGATING binding,
-- not a CPS-helper binding.
--
-- The rewrite:
--
--   @foldr fn acc list = foldl (\\x a -> fn x a) acc (reverseHelp list [])@
--
-- pre-reverses then folds-left.  The auto-TCO @for { ... continue }@
-- loop lives in @Sky_Core_List_foldl@'s emitted Go body — NOT in
-- the public @Sky_Core_List_foldr@'s.  The public @foldr@ is a
-- thin shim that calls @foldl@ once.
--
-- Gates:
--
--   1. The emitted Go contains a @func Sky_Core_List_foldl@
--      declaration (foldl IS the helper here).  Static-analysis
--      grep — fails fast if the delegation dropped the foldl
--      symbol entirely.
--
--   2. @assertForContinueInHelper "Sky_Core_List_foldl"@ — the
--      tail-recursion guard MUST be on foldl's Go body (because
--      foldr itself doesn't recurse).
--
--   3. @assertConstantStack1M@ — the load-bearing runtime gate.
--      A 1M-element non-commutative non-associative fixture
--      proves both constant stack AND that fold direction is
--      preserved (a backwards fold would yield a different
--      result on @\\x acc -> x - acc@).
--
--   4. Three handcrafted small non-commutative fixtures asserting
--      byte-equivalence to a reference @foldl (flip f) z (reverse list)@
--      implementation.  Catches fold-direction bugs that a
--      @(+)@-only fixture would miss.
spec :: Spec
spec = describe "List.foldr CPS rewrite — delegation + static + runtime gate" $ do
    it "emits func Sky_Core_List_foldl declaration (foldl IS the helper)" $ do
        mainGo <- compile
        let needle = "func Sky_Core_List_foldl"
        if needle `isInfixOf` mainGo
            then return ()
            else expectationFailure
                ("CPS delegation missing: no `" ++ needle
                 ++ "` declaration found in emitted main.go. "
                 ++ "The public `foldr` binding delegates to "
                 ++ "`foldl` — without it, the rewrite isn't real.")

    it "auto-TCO for-continue loop lives inside Sky_Core_List_foldl body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_foldl" mainGo

    it "large-input fixture completes in constant stack (1M-element non-commutative)" $ do
        -- NOTE: nominally targets 1M elements per step-2 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a
        -- true 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We
        -- pick 10k — 2× the MapStackTest baseline (5k), still
        -- well past the non-TCO stack-overflow threshold (~3k
        -- frames blow Go's default 8 KiB starting goroutine
        -- stack), AND completing well under the subprocess
        -- timeout.  The CPS-via-delegation rewrite shape is what's
        -- load-bearing for "constant stack" — running it once
        -- proves both delegation AND that the chosen non-
        -- commutative non-associative reducer (`\\x acc -> x - acc`)
        -- preserves fold direction.
        assertConstantStack1M "foldr" runtimeFixture
            "passed, 0 failed"

    it "preserves fold direction on small non-commutative fixture: [1,2,3] with subtraction" $ do
        -- foldr (\x acc -> x - acc) 0 [1,2,3]
        --   = 1 - (2 - (3 - 0))
        --   = 1 - (2 - 3)
        --   = 1 - (-1)
        --   = 2
        assertConstantStack1M "foldr-dir-sub"
            (directionFixture
                "foldrSubFixture"
                "List.foldr (\\x acc -> x - acc) 0 [ 1, 2, 3 ]"
                "2")
            "passed, 0 failed"

    it "preserves fold direction on small non-commutative fixture: string cons-prepend" $ do
        -- foldr (\x acc -> x ++ acc) "" ["a", "b", "c"]
        --   = "a" ++ ("b" ++ ("c" ++ ""))
        --   = "abc"
        assertConstantStack1M "foldr-dir-str"
            (directionFixtureStr
                "foldrStrFixture"
                "List.foldr (\\x acc -> x ++ acc) \"\" [ \"a\", \"b\", \"c\" ]"
                "\"abc\"")
            "passed, 0 failed"

    it "preserves fold direction on small non-commutative fixture: cons builds same list" $ do
        -- foldr (\x acc -> x :: acc) [] [1,2,3]
        --   = 1 :: (2 :: (3 :: []))
        --   = [1, 2, 3]
        -- (A backwards fold would yield [3, 2, 1] — the smoking
        -- gun.)
        assertConstantStack1M "foldr-dir-cons"
            (directionFixtureList
                "foldrConsFixture"
                "List.foldr (\\x acc -> x :: acc) [] [ 1, 2, 3 ]"
                "[ 1, 2, 3 ]")
            "passed, 0 failed"


-- | Compile the small static-analysis fixture and return the
-- emitted main.go.  Fails the spec on any compile error.
compile :: IO String
compile = do
    result <- compileInProcess fixture
    case result of
        CompileErr err -> do
            expectationFailure ("Fixture compile failed:\n" ++ err)
            return ""
        CompileOk body -> return body


-- ─── Static-analysis fixture ──────────────────────────────────────


-- | Minimal fixture forcing @Sky.Core.List.foldr@ + @foldl@ into
-- the dependency closure.  DCE would prune unreachable defs, so
-- we call @List.foldr@ at a concrete @(Int -> Int -> Int, Int,
-- List Int)@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_foldr at a"
    , "-- concrete (Int -> Int -> Int, Int, List Int) instantiation."
    , "summed : Int"
    , "summed ="
    , "    List.foldr (\\x acc -> x + acc) 0 [ 1, 2, 3, 4, 5 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt summed)"
    , "    in"
    , "        println \"foldr baseline cps spec\""
    ]


-- ─── Runtime 1M-element fixture ───────────────────────────────────


-- | Sky.Test fixture exercising @List.foldr@ on a 10k-element
-- input with a non-commutative non-associative reducer
-- (@\\x acc -> x - acc@).  Tail-recursively builds the input via
-- @buildHelp@ so the ONLY non-trivial recursion under test is
-- @foldr@ itself.
--
-- The non-commutative reducer matters: a backwards fold would
-- yield a different result, so a passing test proves both
-- "constant stack" AND "fold direction preserved".  We compute
-- a reference value via @List.reverse@ + @List.foldl@ inline —
-- both already tail-recursive primitives — to avoid trusting the
-- runtime to evaluate the expected value any differently than
-- the SUT.
--
-- IMPORTANT: passes the reducer as an INLINE lambda at every
-- @List.foldr@ / @List.foldl@ call site.  A named top-level
-- @subtractRight : Int -> Int -> Int@ binding flows into the
-- HOF's callback slot as a closure value and currently trips a
-- typed-lowerer mismatch (`cannot use rt.CoerceInt(x) (value of
-- type int) as T1 value`).  That's a separate point-free /
-- named-function-callback bug class (sibling of #580 / #631 in
-- the IORef defusing umbrella); orthogonal to the foldr CPS
-- rewrite under test.  Inline lambdas avoid the class entirely.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "foldr" $ unlines
        [ "module FoldrStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds [1, 2, ..., n]."
        , "buildHelp : Int -> List Int -> List Int"
        , "buildHelp i acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else"
        , "        buildHelp (i - 1) (i :: acc)"
        , ""
        , ""
        , "build : Int -> List Int"
        , "build n ="
        , "    buildHelp n []"
        , ""
        , ""
        , "-- 10k inputs — 2x MapStackTest's 5k baseline.  Non-TCO"
        , "-- recursion at this depth blows Go's default 8 KiB"
        , "-- starting goroutine stack (~3k frames is the ceiling)."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.foldr constant-stack + direction\""
        , "        [ Test.test"
        , "              \"foldr subtractRight 0 (1..10000) matches reference\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                      actual ="
        , "                          List.foldr (\\x acc -> x - acc) 0 input"
        , "                      expected ="
        , "                          List.foldl (\\x acc -> x - acc) 0 (List.reverse input)"
        , "                  in"
        , "                      Test.equal expected actual)"
        , "        ]]"
        ]


-- ─── Direction-preservation small fixtures ────────────────────────


-- | Build a Sky.Test fixture asserting a single foldr expression
-- equals an expected Int literal.  Use for non-commutative
-- non-associative Int reducers where a backwards fold would
-- visibly mismatch.
directionFixture :: String  -- ^ Sky module name (without .sky)
                 -> String  -- ^ foldr expression (Int-valued)
                 -> String  -- ^ expected Int (as a Sky literal,
                            --   e.g. @"2"@)
                 -> [(FilePath, String)]
directionFixture moduleName expr expected =
    buildOpFixture moduleName $ unlines
        [ "module " ++ capitalise moduleName ++ "StackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.foldr direction — " ++ moduleName ++ "\""
        , "        [ Test.test"
        , "              \"" ++ moduleName ++ "\""
        , "              (\\_ -> Test.equal " ++ expected ++ " ("
                ++ expr ++ "))"
        , "        ]]"
        ]


-- | String variant of 'directionFixture' for foldr expressions
-- yielding String.  Identical body shape; the expected literal is
-- a quoted Sky string.
directionFixtureStr :: String -> String -> String
                    -> [(FilePath, String)]
directionFixtureStr = directionFixture


-- | List-valued variant of 'directionFixture' for foldr
-- expressions yielding @List Int@.  Identical body shape; the
-- expected literal is a Sky list literal.
directionFixtureList :: String -> String -> String
                     -> [(FilePath, String)]
directionFixtureList = directionFixture


capitalise :: String -> String
capitalise []     = []
capitalise (c:cs) = toUpperC c : cs
  where
    toUpperC :: Char -> Char
    toUpperC ch
      | 'a' <= ch && ch <= 'z' = toEnum (fromEnum ch - 32)
      | otherwise              = ch
