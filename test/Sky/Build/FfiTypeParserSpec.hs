module Sky.Build.FfiTypeParserSpec (spec) where

-- Phase B regression fence for the FFI Sky-type parser. The
-- producer side (Sky.Build.FfiGen.wrapperSkyType) emits a closed
-- subset of Sky type-string syntax; this spec locks the parser to
-- that grammar so a producer/consumer drift is caught at test
-- time, not at "skyshop suddenly types every FFI call as `any`".
--
-- Inputs are sampled from real .skycache/ffi/*.kernel.json across
-- examples/12-skyvote and examples/13-skyshop.

import Test.Hspec
import Sky.Build.FfiTypeParser (FtyAst(..), parseFty)


-- Helper builders to keep test cases readable.
unit, str, int_, bool_, float_, errTy, anyTy :: FtyAst
unit  = FtyUnit
str   = FtyApp "String" []
int_  = FtyApp "Int" []
bool_ = FtyApp "Bool" []
float_ = FtyApp "Float" []
errTy = FtyApp "Error" []
anyTy = FtyVar "any"

opaque :: String -> FtyAst
opaque n = FtyApp n []

result :: FtyAst -> FtyAst
result inner = FtyApp "Result" [errTy, inner]

list :: FtyAst -> FtyAst
list inner = FtyApp "List" [inner]

dict :: FtyAst -> FtyAst -> FtyAst
dict k v = FtyApp "Dict" [k, v]

maybe_ :: FtyAst -> FtyAst
maybe_ inner = FtyApp "Maybe" [inner]


spec :: Spec
spec = do
    describe "FtyAst parser (Phase B)" $ do

        it "parses unit -> Result Error R (zero-arg constructor)" $
            parseFty "() -> Result Error ActionCodeSettings"
                `shouldBe` Just (FtyArrow unit (result (opaque "ActionCodeSettings")))

        it "parses single-arg getter (Token -> Result Error String)" $
            parseFty "Token -> Result Error String"
                `shouldBe` Just (FtyArrow (opaque "Token") (result str))

        it "parses curried setter (String -> T -> Result Error T)" $
            parseFty "String -> ActionCodeSettings -> Result Error ActionCodeSettings"
                `shouldBe`
                    Just (FtyArrow str
                            (FtyArrow (opaque "ActionCodeSettings")
                                (result (opaque "ActionCodeSettings"))))

        it "parses Bool / Int / Float result types" $ do
            parseFty "X -> Result Error Bool"  `shouldBe`
                Just (FtyArrow (opaque "X") (result bool_))
            parseFty "X -> Result Error Int"   `shouldBe`
                Just (FtyArrow (opaque "X") (result int_))
            parseFty "X -> Result Error Float" `shouldBe`
                Just (FtyArrow (opaque "X") (result float_))

        it "parses Dict String any (interface{} mapped through goTypeToSky)" $
            parseFty "Token -> Result Error (Dict String any)"
                `shouldBe`
                    Just (FtyArrow (opaque "Token") (result (dict str anyTy)))

        it "parses List X" $
            parseFty "() -> Result Error (List Item)"
                `shouldBe` Just (FtyArrow unit (result (list (opaque "Item"))))

        it "parses comma-ok Maybe shape" $
            parseFty "Map -> String -> Result Error (Maybe Value)"
                `shouldBe`
                    Just (FtyArrow (opaque "Map")
                            (FtyArrow str
                                (result (maybe_ (opaque "Value")))))

        it "parses tuple result (T, U)" $
            parseFty "Reader -> Result Error (Token, String)"
                `shouldBe`
                    Just (FtyArrow (opaque "Reader")
                            (result (FtyTuple [opaque "Token", str])))

        it "parses tuple result (T, U, V)" $
            parseFty "() -> Result Error (A, B, C)"
                `shouldBe`
                    Just (FtyArrow unit
                            (result (FtyTuple
                                [opaque "A", opaque "B", opaque "C"])))

        it "treats lowercase identifiers as type vars" $ do
            parseFty "a -> Result Error b"
                `shouldBe` Just (FtyArrow (FtyVar "a") (result (FtyVar "b")))
            -- 'any' is the wildcard; same parse shape, different
            -- semantics downstream (Sky.Type.Solve mints fresh
            -- unification var per occurrence).
            parseFty "() -> Result Error any"
                `shouldBe` Just (FtyArrow unit (result anyTy))

        it "rejects strings with non-ident punctuation (channel arrows, braces)" $ do
            -- The producer-side isSkyParseable is the primary gate
            -- against pathological FFI shapes — the parser is the
            -- secondary guard that fails fast on Go syntax that
            -- leaks past the gate. `<-` (channel direction) and
            -- `{` (struct/interface literal) are the markers we
            -- can detect at lex time.
            parseFty "Context -> Result Error <-chan struct{}"  `shouldBe` Nothing
            parseFty "X -> Result Error interface{}"            `shouldBe` Nothing
            -- Note: 'chan Int' parses as a valid (if nonsensical)
            -- application — `chan` is just a lowercase TVar to
            -- the parser. The producer's isSkyParseable check is
            -- what prevents that string ever landing in JSON.

        it "rejects empty input" $ do
            parseFty ""              `shouldBe` Nothing
            -- "() -> Foo Bar X garbage" IS legal grammar — Foo
            -- applied to Bar, X, and the lowercase TVar 'garbage'.
            -- The producer never emits this shape, but the parser
            -- accepts it (and HM downstream will reject the
            -- nonsense application if the constructor's kind
            -- doesn't accept that many args).
            parseFty "() -> Foo Bar X garbage" `shouldBe`
                Just (FtyArrow unit
                        (FtyApp "Foo" [opaque "Bar", opaque "X", FtyVar "garbage"]))

        it "is total on malformed input" $ do
            parseFty "->"            `shouldBe` Nothing
            parseFty "(),"           `shouldBe` Nothing
            parseFty "Result Error"  `shouldBe`
                Just (FtyApp "Result" [errTy])
                -- ^ this is "valid" — partial application of Result
                -- to one type arg. The producer never emits this
                -- shape, but the parser's grammar accepts it.
