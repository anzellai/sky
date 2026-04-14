module Sky.Build.TypedFfiSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)


-- | Regression fence for the P7 typed-FFI call-site migration.
-- The generalised rule in Compile.hs routes zero-arg FFI calls (and
-- literal-arg N-arg FFI calls where the typed wrapper's params are
-- Go primitives) to the T-suffix typed variant. We assert it on
-- the committed ex03-tea-external main.go, which uses
-- `Uuid.newString ()`. A regression would put
-- `Go_Uuid_newString(struct{}{})` back in the output.
spec :: Spec
spec = do
    describe "P7 typed-FFI call sites" $ do
        it "routes Uuid.newString through Go_Uuid_newStringT in ex03" $ do
            body <- readFile "examples/03-tea-external/sky-out/main.go"
            ("Go_Uuid_newStringT" `isInfixOf` body) `shouldBe` True
            -- Safety: the any/any form (with a unit arg) must be gone
            -- from this particular call site. The wrapper name still
            -- appears without `T` inside the wrapper file, but main.go
            -- should never call `Go_Uuid_newString(struct{}{})` again.
            ("Go_Uuid_newString(struct{}{}" `isInfixOf` body) `shouldBe` False

        it "emits Go_Uuid_newStringT at every ex13-skyshop call site" $ do
            body <- readFile "examples/13-skyshop/sky-out/main.go"
            -- skyshop has five call sites of Uuid.newString; each must
            -- reference the typed variant.
            let n = length (substrings "Go_Uuid_newStringT" body)
            n `shouldSatisfy` (>= 5)
            ("Go_Uuid_newString(struct{}{}" `isInfixOf` body) `shouldBe` False

        it "feeds typed results through Result.withDefault in skyshop" $ do
            body <- readFile "examples/13-skyshop/sky-out/main.go"
            -- Canonical pattern: `Result.withDefault "" (Uuid.newString ())`
            -- lowers to `rt.Result_withDefault("", rt.Go_Uuid_newStringT())`.
            -- Before the reflect-fallback fix in Result_withDefault this
            -- would have silently returned the whole SkyResult struct
            -- because the old type-asserted path rejected typed shapes.
            ("rt.Result_withDefault(\"\", rt.Go_Uuid_newStringT())" `isInfixOf` body)
                `shouldBe` True

        it "elides case-subject boxing for typed-FFI sources" $ do
            -- ex03's `case Uuid.newString () of Ok _ -> ... Err _ -> ...`
            -- must lower to a direct field access on the typed result,
            -- with no ResultCoerce / ResultAsAny wrap and no
            -- `any(__subject).(rt.SkyResult[any, any])` assertion.
            -- Regression catcher for the P7 typed-subject path.
            body <- readFile "examples/03-tea-external/sky-out/main.go"
            ("__subject_tFfi := rt.Go_Uuid_newStringT()" `isInfixOf` body)
                `shouldBe` True
            ("any(__subject_tFfi.OkValue)" `isInfixOf` body)
                `shouldBe` True
            -- And the wrapped path must NOT appear:
            ("rt.ResultAsAny(rt.Go_Uuid_newStringT())" `isInfixOf` body)
                `shouldBe` False

        it "registers a typed variant for every migrated call name" $ do
            -- Spot-check that regenerated bindings actually emit the T
            -- variant for the one hard-migrated function, across every
            -- example that imports it.
            let files =
                    [ "examples/03-tea-external/ffi/uuid_bindings.go"
                    , "examples/08-notes-app/ffi/uuid_bindings.go"
                    , "examples/13-skyshop/ffi/uuid_bindings.go"
                    ]
            mapM_ (\fp -> do
                contents <- readFile fp
                ("func Go_Uuid_newStringT()" `isInfixOf` contents)
                    `shouldBe` True) files

        it "keeps total typed variant coverage above the floor" $ do
            -- Floor chosen 500 below the current landed total so a
            -- minor-typed-variant regression caused by a future FFI
            -- generator edit trips the test before the sweep does.
            -- Update when the gate rises (e.g. to 3500 when more
            -- bindings migrate).
            let paths =
                    [ "examples/03-tea-external/ffi/uuid_bindings.go"
                    , "examples/05-mux-server/ffi/mux_bindings.go"
                    , "examples/05-mux-server/ffi/http_bindings.go"
                    , "examples/08-notes-app/ffi/uuid_bindings.go"
                    , "examples/11-fyne-stopwatch/ffi/app_bindings.go"
                    , "examples/11-fyne-stopwatch/ffi/fyne_bindings.go"
                    , "examples/11-fyne-stopwatch/ffi/widget_bindings.go"
                    , "examples/13-skyshop/ffi/auth_bindings.go"
                    , "examples/13-skyshop/ffi/customer_bindings.go"
                    , "examples/13-skyshop/ffi/firebase_bindings.go"
                    , "examples/13-skyshop/ffi/firestore_bindings.go"
                    , "examples/13-skyshop/ffi/iterator_bindings.go"
                    , "examples/13-skyshop/ffi/option_bindings.go"
                    , "examples/13-skyshop/ffi/session_bindings.go"
                    , "examples/13-skyshop/ffi/stripe_bindings.go"
                    , "examples/13-skyshop/ffi/uuid_bindings.go"
                    ]
            counts <- mapM typedVariantCount paths
            sum counts `shouldSatisfy` (>= 2500)


-- | Count `^func Go_.*T(p0` signatures in a Go file. Distinguishes
-- actual typed-wrapper emissions from the any/any accessors whose
-- Sky-facing name coincidentally ends in T (e.g. TypeACHDebit).
typedVariantCount :: FilePath -> IO Int
typedVariantCount fp = do
    contents <- readFile fp
    return (length (filter isTypedSig (lines contents)))
  where
    isTypedSig l =
        take 5 l == "func "
        && ("T()" `isInfixOf` l || "T(p0 " `isInfixOf` l)


-- | Count occurrences of a needle in a haystack (non-overlapping).
substrings :: String -> String -> [()]
substrings needle = go
  where
    n = length needle
    go s
        | length s < n = []
        | take n s == needle = () : go (drop n s)
        | otherwise          = go (drop 1 s)
