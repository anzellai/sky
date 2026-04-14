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


-- | Count occurrences of a needle in a haystack (non-overlapping).
substrings :: String -> String -> [()]
substrings needle = go
  where
    n = length needle
    go s
        | length s < n = []
        | take n s == needle = () : go (drop n s)
        | otherwise          = go (drop 1 s)
