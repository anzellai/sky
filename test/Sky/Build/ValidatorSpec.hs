-- | v0.13 Layer 2 — Codegen-stage validator regression fence.
--
-- Pins the validator's pattern-matchers against known-good +
-- known-bad emitted-Go shapes.  When a new codegen regression
-- introduces a typed-kernel call with a raw any-typed arg (Issue
-- #52's class), this spec must fail BEFORE go-build sees the
-- code.  Likewise, well-formed coerce-wrapped calls must not
-- false-positive.
module Sky.Build.ValidatorSpec (spec) where

import Test.Hspec
import qualified Data.Map.Strict as Map
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Reporting.Diagnostic as Diag
import Sky.Build.Validator


spec :: Spec
spec = do
    describe "validateEmittedGo — typed-kernel any-arg detector" $ do

        it "flags rt.List_dropT[T](bareIdent, ...) without coerce" $ do
            -- The exact Issue #52 shape: emitter forgot to wrap
            -- the first arg in rt.AsInt.
            let goSrc = unlines
                    [ "package main"
                    , "func main() {"
                    , "    xs := rt.AsList(model.items)"
                    , "    rt.List_dropT[any](i, xs)"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            length diags `shouldBe` 1
            case diags of
                (d:_) -> do
                    unDiagCodeText (Diag._diag_code d) `shouldBe` "E4001"
                    Diag._diag_severity d `shouldBe` Diag.SevError
                    Diag._diag_category d `shouldBe` Diag.CatCodegen
                [] -> fail "expected one diagnostic"

        it "accepts rt.List_dropT[T](rt.AsInt(x), rt.AsList(xs))" $ do
            -- Well-formed: both args are coerce-wrapped.
            let goSrc = unlines
                    [ "package main"
                    , "func main() {"
                    , "    rt.List_dropT[any](rt.AsInt(n), rt.AsList(xs))"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            diags `shouldBe` []

        it "accepts rt.List_dropT[int](42, xs) — literal arg" $ do
            -- Numeric literal is a valid Go-typed value.
            let goSrc = unlines
                    [ "package main"
                    , "func main() {"
                    , "    rt.List_dropT[int](42, xs)"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            diags `shouldBe` []

        it "accepts rt.List_dropT[T](rt.AsInt(x), bare) — only first arg gated" $ do
            -- The validator's pattern-1 scope is the first arg
            -- (positional Int / fn).  The list arg is handled by
            -- rt.AsListT inside the helper itself, so a bare ident
            -- there is fine.
            let goSrc = unlines
                    [ "package main"
                    , "func main() {"
                    , "    rt.List_dropT[any](rt.AsInt(n), xs)"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            diags `shouldBe` []

        it "does not flag non-typed-kernel rt.* calls" $ do
            -- rt.Println / rt.Eq / etc. are not in riskyTypedKernels.
            let goSrc = unlines
                    [ "package main"
                    , "func main() {"
                    -- Use REAL runtime functions: rt.Log_println +
                    -- rt.Eq both exist in runtime-go/rt/. Using
                    -- fictional names would also trigger the
                    -- E4005 undefined-kernel detector added later.
                    , "    rt.Log_println(x)"
                    , "    rt.Eq(a, b)"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            diags `shouldBe` []

    describe "validateEmittedGo — undefined-kernel detector (issue #56)" $ do

        it "flags rt.<Name> where the runtime doesn't export <Name>" $ do
            -- Issue #56: Sky kernel registry maps a name like
            -- `clamp -> Basics_clamp` but the runtime only has
            -- Basics_clampT. Pre-fix, the user got a `go build` error
            -- `undefined: rt.Basics_clamp` and was told to file an
            -- issue. Post-fix the validator catches it.
            let goSrc = unlines
                    [ "package main"
                    , "// SKY-ORIGIN: src/Main.sky:7:1"
                    , "func main() {"
                    , "    _ = rt.Foo_NeverDefinedAnywhere(1, 2, 3)"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            length diags `shouldBe` 1
            case diags of
                (d:_) -> do
                    unDiagCodeText (Diag._diag_code d) `shouldBe` "E4005"
                    Diag._diag_severity d `shouldBe` Diag.SevError
                    Diag._diag_category d `shouldBe` Diag.CatCodegen
                [] -> fail "expected one diagnostic"

        it "does NOT flag rt.<Name> where <Name> IS in the runtime" $ do
            -- Issue #56 fix: Basics_clamp added to runtime-go/rt/rt.go
            -- in same commit. The validator must NOT flag legitimate
            -- references. This pins the runtime-exports scan working
            -- correctly — if the scan misses `Basics_clamp` (e.g.
            -- regex regression), this test fails.
            let goSrc = unlines
                    [ "package main"
                    , "func main() {"
                    , "    _ = rt.Basics_clamp(0, 100, 200)"
                    , "    _ = rt.String_fromInt(42)"
                    , "    _ = rt.Log_println(\"hi\")"
                    , "}"
                    ]
            let diags = validateEmittedGo "main.go" Map.empty goSrc
            diags `shouldBe` []

    describe "parseOriginComments" $ do

        it "extracts SKY-ORIGIN comments into a line-keyed map" $ do
            let goSrc = unlines
                    [ "package main"
                    , "// SKY-ORIGIN: src/Main.sky:7:1"
                    , "func main() {"
                    , "    rt.Println(x)"
                    , "}"
                    , "// SKY-ORIGIN: src/Main.sky:12:5"
                    , "func helper(x any) {}"
                    ]
            let m = parseOriginComments goSrc
            Map.size m `shouldBe` 2
            Map.lookup 2 m `shouldBe` Just ("src/Main.sky", 7, 1)
            Map.lookup 6 m `shouldBe` Just ("src/Main.sky", 12, 5)

        it "ignores non-origin comments + malformed entries" $ do
            let goSrc = unlines
                    [ "// regular comment"
                    , "// SKY-ORIGIN: malformed-no-colon"
                    , "// SKY-ORIGIN: src/Main.sky:7:1"
                    , "// SKY-ORIGIN:"
                    ]
            let m = parseOriginComments goSrc
            Map.size m `shouldBe` 1
            Map.lookup 3 m `shouldBe` Just ("src/Main.sky", 7, 1)

    describe "parseGoBuildError" $ do

        it "parses main.go:NN:MM: <message>" $ do
            let err = "./main.go:42:18: cannot use n (any) as int in argument"
            case parseGoBuildError err of
                Just loc -> do
                    _gel_line loc `shouldBe` 42
                    _gel_col loc `shouldBe` 18
                    _gel_message loc `shouldSatisfy` (\m ->
                        take 10 m == "cannot use")
                Nothing -> fail "expected parsed location"

        it "skips lines without main.go:NN:MM shape" $ do
            let err = unlines
                    [ "go.mod requires Go 1.21"
                    , "build failed"
                    ]
            case parseGoBuildError err of
                Nothing -> return ()
                Just _  -> expectationFailure "expected no parsed location"

        it "picks the first .go error when many lines present" $ do
            let err = unlines
                    [ "compiling..."
                    , "./main.go:10:5: undefined: someName"
                    , "./other.go:99:1: cannot use ..."
                    ]
            case parseGoBuildError err of
                Just loc -> _gel_line loc `shouldBe` 10
                Nothing  -> fail "expected parsed location"

    describe "resolveGoErrorToSky" $ do

        it "maps a Go error line to the nearest preceding origin" $ do
            let originMap = Map.fromList
                    [ (2,  ("src/Main.sky", 7, 1))
                    , (10, ("src/Main.sky", 12, 5))
                    , (20, ("src/Main.sky", 25, 3))
                    ]
                gel = GoErrorLocation
                    { _gel_file = "./main.go"
                    , _gel_line = 15
                    , _gel_col = 8
                    , _gel_message = "cannot use ..."
                    }
            case resolveGoErrorToSky originMap gel of
                Just diag -> do
                    Diag._diag_file diag `shouldBe` "src/Main.sky"
                    let A.Region (A.Position l _) _ = Diag._diag_region diag
                    l `shouldBe` 12        -- line 12 origin precedes line 15
                Nothing -> fail "expected resolved diagnostic"

        it "returns Nothing when no origin precedes the error line" $ do
            let originMap = Map.fromList
                    [ (20, ("src/Main.sky", 25, 3)) ]
                gel = GoErrorLocation
                    { _gel_file = "./main.go"
                    , _gel_line = 15
                    , _gel_col = 8
                    , _gel_message = "x"
                    }
            case resolveGoErrorToSky originMap gel of
                Nothing -> return ()
                Just _  -> expectationFailure "expected Nothing — no preceding origin"

  where
    unDiagCodeText (Diag.DiagCode s) = s
