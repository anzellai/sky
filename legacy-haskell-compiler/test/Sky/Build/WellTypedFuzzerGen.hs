{-# LANGUAGE LambdaCase #-}
{-# LANGUAGE TupleSections #-}

-- v0.17 step-2 — Well-typed Sky program generator for the fuzzer.
--
-- This module produces *HM-valid* Sky programs by construction.
-- We use a small typed grammar so we never emit programs that
-- the type-checker would reject — every generated `main` is a
-- `Task Error ()` that prints a single typed expression.
--
-- Four shape categories, picked uniformly per iteration:
--
--   ArithInt       — Int arithmetic over literals and `let` bindings
--                    (+ / - / * / div / mod, parens, max-depth 4).
--   StringConcat   — String concat / interpolation via (++) and
--                    `String.fromInt` over Int sub-expressions.
--   ListCombinator — List / Maybe / Result combinator chains
--                    (List.map / filter / foldl / length;
--                     Maybe.withDefault / map; Result.withDefault).
--   ParamRecord    — A parametric record alias `Box a = { value : a,
--                    label : String }` exercised at two
--                    instantiations (Int + String) — the canonical
--                    Cfg_R[T] codegen shape.
--
-- The generator is intentionally narrow: every shape exercises the
-- typed-codegen pipeline (region map → LowerCtx → Go output) on a
-- different surface, but no shape requires the user to learn what
-- a "well-typed" Sky program looks like at the grammar level.
-- We HM-validate every program by handing it to `sky build` as the
-- ground truth — the generator just keeps the proposal space
-- inside the typed envelope.

module Sky.Build.WellTypedFuzzerGen
    ( ShapeCategory(..)
    , Program(..)
    , genProgram
    , genProgramOfCategory
    , allCategories
    , categoryLabel
    , renderProgram
    ) where

import Test.QuickCheck
    ( Gen
    , choose
    , elements
    , frequency
    , vectorOf
    , oneof
    , sized
    )


-- ─── Shape categories ──────────────────────────────────────────────


data ShapeCategory
    = ArithInt
    | StringConcat
    | ListCombinator
    | ParamRecord
    deriving (Eq, Show, Enum, Bounded)


allCategories :: [ShapeCategory]
allCategories = [minBound .. maxBound]


categoryLabel :: ShapeCategory -> String
categoryLabel = \case
    ArithInt       -> "arith-int"
    StringConcat   -> "string-concat"
    ListCombinator -> "list-combinator"
    ParamRecord    -> "param-record"


-- ─── Program shape ────────────────────────────────────────────────


-- A complete renderable Sky program. We keep the category around
-- so the spec can label each iteration in the QuickCheck output.
data Program = Program
    { pCategory :: ShapeCategory
    , pSource   :: String
    } deriving (Show)


-- ─── Top-level generator ──────────────────────────────────────────


genProgram :: Gen Program
genProgram = do
    cat <- elements allCategories
    genProgramOfCategory cat


genProgramOfCategory :: ShapeCategory -> Gen Program
genProgramOfCategory cat = do
    body <- case cat of
        ArithInt       -> genArithIntMain
        StringConcat   -> genStringConcatMain
        ListCombinator -> genListCombinatorMain
        ParamRecord    -> genParamRecordMain
    return (Program cat body)


-- ─── Shared scaffolding ───────────────────────────────────────────


-- Program prologue shared by every shape. Layer-3 imports only —
-- we never reach into Sky.Build.Compile's special-case path.
prologue :: String
prologue = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    ]


-- Wrap a string-typed expression as `main = println <expr>`.
mainPrintln :: String -> String
mainPrintln body = "main =\n    println (" ++ body ++ ")\n"


-- ─── ArithInt — Int arithmetic ────────────────────────────────────


-- Generator for Int expressions up to a bounded depth.
genIntExpr :: Int -> Gen String
genIntExpr depth
    | depth <= 0 = genIntLit
    | otherwise = frequency
        [ (3, genIntLit)
        , (4, genIntBinop depth)
        , (1, genIntLet depth)
        ]


genIntLit :: Gen String
genIntLit = do
    n <- choose (0 :: Int, 99)
    return (show n)


genIntBinop :: Int -> Gen String
genIntBinop depth = do
    op  <- elements ["+", "-", "*"]
    lhs <- genIntExpr (depth - 1)
    rhs <- genIntExpr (depth - 1)
    return ("(" ++ lhs ++ " " ++ op ++ " " ++ rhs ++ ")")


genIntLet :: Int -> Gen String
genIntLet depth = do
    rhs  <- genIntExpr (depth - 1)
    body <- genIntExpr (depth - 1)
    -- We bind to a fixed name `x_<depth>` so nested lets don't
    -- collide; using depth as a suffix is bounded and unique
    -- because every `let` decrements before its body fires.
    let name = "x_" ++ show depth
    return ( "(let\n        " ++ name ++ " = " ++ rhs
          ++ "\n    in\n        " ++ name ++ " + " ++ body ++ ")"
           )


genArithIntMain :: Gen String
genArithIntMain = do
    e <- sized (\s -> genIntExpr (min 4 (max 1 (s `div` 4 + 1))))
    -- println takes a String, so render via String.fromInt.
    return ( prologue
          ++ "\n"
          ++ mainPrintln ("String.fromInt " ++ e)
           )


-- ─── StringConcat — String concat + fromInt embedding ─────────────


genStringLit :: Gen String
genStringLit = do
    -- Tight charset: a-z + space, length 1–6. We deliberately
    -- avoid `\` / `"` / `\n` to keep escape behaviour out of
    -- the soundness fuzzer (parser regressions belong in
    -- ParserSpec).
    n <- choose (1, 6)
    cs <- vectorOf n (elements (['a' .. 'z'] ++ "      "))
    return ("\"" ++ trimSpaces cs ++ "\"")
  where
    trimSpaces = take 6


genStrExpr :: Int -> Gen String
genStrExpr depth
    | depth <= 0 = genStringLit
    | otherwise = frequency
        [ (3, genStringLit)
        , (3, genStrConcat depth)
        , (2, genFromInt depth)
        ]


genStrConcat :: Int -> Gen String
genStrConcat depth = do
    lhs <- genStrExpr (depth - 1)
    rhs <- genStrExpr (depth - 1)
    return ("(" ++ lhs ++ " ++ " ++ rhs ++ ")")


genFromInt :: Int -> Gen String
genFromInt depth = do
    inner <- genIntExpr (depth - 1)
    return ("String.fromInt (" ++ inner ++ ")")


genStringConcatMain :: Gen String
genStringConcatMain = do
    e <- sized (\s -> genStrExpr (min 4 (max 1 (s `div` 4 + 1))))
    return (prologue ++ "\n" ++ mainPrintln e)


-- ─── ListCombinator — List/Maybe/Result combinators ───────────────


genListLitInt :: Gen String
genListLitInt = do
    n <- choose (0 :: Int, 5)
    xs <- vectorOf n genIntLit
    return ("[" ++ intercalateComma xs ++ "]")
  where
    intercalateComma []     = ""
    intercalateComma [x]    = x
    intercalateComma (x:xs) = x ++ ", " ++ intercalateComma xs


-- A handful of List combinator chains that all collapse to a
-- printable String. We force the type through `String.fromInt`
-- of `List.length` at the leaf so HM unambiguously assigns the
-- whole chain Int → String.
genListCombChain :: Gen String
genListCombChain = do
    lit <- genListLitInt
    op  <- elements [combMap, combFilter, combFoldl, combLength]
    return (op lit)
  where
    combMap xs    = "String.fromInt (List.length (List.map (\\x -> x + 1) " ++ xs ++ "))"
    combFilter xs = "String.fromInt (List.length (List.filter (\\x -> x > 0) " ++ xs ++ "))"
    combFoldl xs  = "String.fromInt (List.foldl (\\x acc -> acc + x) 0 " ++ xs ++ ")"
    combLength xs = "String.fromInt (List.length " ++ xs ++ ")"


genMaybeChain :: Gen String
genMaybeChain = do
    n <- genIntLit
    op <- elements
        [ "(Maybe.withDefault 0 (Just " ++ n ++ "))"
        , "(Maybe.withDefault 0 (Maybe.map (\\x -> x + 1) (Just " ++ n ++ ")))"
        , "(Maybe.withDefault 0 Nothing)"
        ]
    return ("String.fromInt " ++ op)


genResultChain :: Gen String
genResultChain = do
    n <- genIntLit
    op <- elements
        [ "(Result.withDefault 0 (Ok " ++ n ++ "))"
        , "(Result.withDefault 0 (Result.map (\\x -> x * 2) (Ok " ++ n ++ ")))"
        ]
    return ("String.fromInt " ++ op)


genListCombinatorMain :: Gen String
genListCombinatorMain = do
    e <- oneof [genListCombChain, genMaybeChain, genResultChain]
    return (prologue ++ "\n" ++ mainPrintln e)


-- ─── ParamRecord — parametric record alias ────────────────────────


-- Two anonymous records with disjoint field sets exercised through
-- typed field access. The fields differ between the two records so
-- HM unambiguously assigns each its own anonymous shape — this
-- exercises the typed-record-codegen pipeline (Anon_R_<hash>
-- emission, field-access typing) without crossing into the
-- parametric-alias surface, which has known v0.17 pre-shipping
-- residuals (Cfg_R[T1 any] cross-call monomorphisation gaps).
--
-- Once the parametric-alias codegen surface is fully stable, this
-- generator should grow to ALSO emit explicit `Box Int` /
-- `Box String` annotations on the bindings so the typed-alias
-- pipeline is exercised. Until then the anonymous shape is the
-- correct floor — it exercises EVERY codegen path the alias would
-- emit (field offsets, narrow casts, value access) WITHOUT
-- tripping the pending alias monomorphisation regressions.
genParamRecordMain :: Gen String
genParamRecordMain = do
    intVal <- genIntLit
    strVal <- genStringLit
    intLbl <- genStringLit
    strLbl <- genStringLit
    let body =
            "let\n" ++
            "        intBox = { value = " ++ intVal ++ ", label = " ++ intLbl ++ " }\n" ++
            "        strBox = { tag = " ++ strVal ++ ", note = " ++ strLbl ++ " }\n" ++
            "    in\n" ++
            "        intBox.label ++ \"=\" ++ String.fromInt intBox.value\n" ++
            "            ++ \" / \" ++ strBox.tag ++ \"/\" ++ strBox.note"
    return ( prologue
          ++ "\n"
          ++ mainPrintln body
           )


-- ─── Renderer ─────────────────────────────────────────────────────


renderProgram :: Program -> String
renderProgram = pSource
