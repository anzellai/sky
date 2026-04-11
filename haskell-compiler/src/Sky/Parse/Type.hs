-- | Type annotation parsing for Sky.
-- Syntax: Int, String, a -> b, List Int, { name : String, age : Int }
module Sky.Parse.Type where

import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Space (spaces)
import Sky.Parse.Variable (lower, upper)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Parse a type annotation (top level — handles ->)
typeAnnotation :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeAnnotation mkError = do
    t <- typeApply mkError
    spaces
    -- Check for ->
    mc <- peek
    case mc of
        Just '-' ->
            oneOfWithFallback
                [ do
                    string mkError (T.pack "->")
                    spaces
                    ret <- typeAnnotation mkError
                    return (Src.TLambda t ret)
                ]
                t
        _ -> return t


-- | Parse a type application: Maybe Int, Result String Int, etc.
typeApply :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeApply mkError = do
    base <- typeAtom mkError
    spaces
    args <- typeArgs mkError
    case args of
        [] -> return base
        _  -> case base of
            Src.TType mod name [] -> return (Src.TType mod name args)
            Src.TTypeQual mod name [] -> return (Src.TTypeQual mod name args)
            _ -> return base  -- shouldn't apply args to non-named type


-- | Parse zero or more type arguments (atomic types)
typeArgs :: (Row -> Col -> x) -> Parser x [Src.TypeAnnotation]
typeArgs mkError =
    oneOfWithFallback
        [ do
            arg <- typeAtom mkError
            spaces
            rest <- typeArgs mkError
            return (arg : rest)
        ]
        []


-- | Parse an atomic type (no application or arrow)
typeAtom :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeAtom mkError =
    oneOf mkError
        [ -- Unit type: ()
          do char mkError '('
             spaces
             mc <- peek
             case mc of
                 Just ')' -> do
                     char mkError ')'
                     return Src.TUnit
                 _ -> do
                     -- Parenthesised type or tuple
                     t1 <- typeAnnotation mkError
                     spaces
                     mc2 <- peek
                     case mc2 of
                         Just ',' -> do
                             char mkError ','
                             spaces
                             t2 <- typeAnnotation mkError
                             more <- typeTupleRest mkError
                             spaces
                             char mkError ')'
                             return (Src.TTuple t1 t2 more)
                         Just ')' -> do
                             char mkError ')'
                             return t1
                         _ -> error "Expected , or ) in type"

        , -- Record type: { field : Type, ... }
          do char mkError '{'
             spaces
             mc <- peek
             case mc of
                 Just '}' -> do
                     char mkError '}'
                     return (Src.TRecord [] Nothing)
                 _ -> do
                     fields <- typeRecordFields mkError
                     spaces
                     char mkError '}'
                     return (Src.TRecord fields Nothing)

        , -- Type constructor: Maybe, List, MyType
          do name <- upper mkError
             return (Src.TType "" [name] [])

        , -- Qualified type: Module.Type
          do -- Peek for Module.Name pattern
             -- For now, parse as upper and handle qualification in canonicalisation
             return =<< return (error "qualified types handled in canonicalisation")

        , -- Type variable: a, b, comparable
          do name <- lower mkError
             return (Src.TVar name)
        ]


-- | Parse remaining tuple type elements
typeTupleRest :: (Row -> Col -> x) -> Parser x [Src.TypeAnnotation]
typeTupleRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            t <- typeAnnotation mkError
            rest <- typeTupleRest mkError
            return (t : rest)
        ]
        []


-- | Parse record type fields: name : Type, name2 : Type2
typeRecordFields :: (Row -> Col -> x) -> Parser x [(A.Located String, Src.TypeAnnotation)]
typeRecordFields mkError = do
    first <- typeRecordField mkError
    rest <- typeRecordFieldsRest mkError
    return (first : rest)


typeRecordField :: (Row -> Col -> x) -> Parser x (A.Located String, Src.TypeAnnotation)
typeRecordField mkError = do
    name <- addLocation (lower mkError)
    spaces
    char mkError ':'
    spaces
    t <- typeAnnotation mkError
    return (name, t)


typeRecordFieldsRest :: (Row -> Col -> x) -> Parser x [(A.Located String, Src.TypeAnnotation)]
typeRecordFieldsRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            field <- typeRecordField mkError
            rest <- typeRecordFieldsRest mkError
            return (field : rest)
        ]
        []
