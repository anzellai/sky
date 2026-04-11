-- | Sky type to Go type mapping.
-- Maps canonical Sky types to Go type strings for typed code generation.
-- Uses Go generics (1.18+): SkyList[T], SkyResult[E, T], etc.
module Sky.Generate.Go.Type where

import qualified Data.Map.Strict as Map
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName


-- | Convert a canonical Sky type to a Go type string
typeToGo :: T.Type -> String
typeToGo t = case t of
    T.TVar name ->
        goTypeParam name

    T.TUnit ->
        "struct{}"

    T.TLambda from to ->
        "func(" ++ typeToGo from ++ ") " ++ typeToGo to

    T.TTuple a b Nothing ->
        "rt.SkyTuple2[" ++ typeToGo a ++ ", " ++ typeToGo b ++ "]"

    T.TTuple a b (Just c) ->
        "rt.SkyTuple3[" ++ typeToGo a ++ ", " ++ typeToGo b ++ ", " ++ typeToGo c ++ "]"

    T.TRecord fields Nothing ->
        goRecordType fields

    T.TRecord fields (Just ext) ->
        -- Extensible record — fall back to interface
        "any /* extensible record */"

    T.TType home name args ->
        goNamedType home name args

    T.TAlias home name pairs (T.Hoisted inner) ->
        typeToGo inner

    T.TAlias home name pairs (T.Filled inner) ->
        typeToGo inner


-- | Map a type variable name to a Go type parameter
-- a -> A, b -> B, comparable -> C, etc.
goTypeParam :: String -> String
goTypeParam name = case name of
    [c] | c >= 'a' && c <= 'z' -> [toEnum (fromEnum c - 32)]  -- a -> A
    "comparable" -> "comparable"
    "number"     -> "rt.SkyNumber"
    "appendable" -> "rt.SkyAppendable"
    _            -> "T_" ++ name


-- | Map a named type constructor to Go
goNamedType :: ModuleName.Canonical -> String -> [T.Type] -> String
goNamedType home name args = case (ModuleName.toString home, name) of
    -- Primitives
    ("Sky.Core.Basics", "Int")    -> "int"
    ("Sky.Core.Basics", "Float")  -> "float64"
    ("Sky.Core.Basics", "Bool")   -> "bool"
    ("Sky.Core.Basics", "String") -> "string"
    ("Sky.Core.Basics", "Char")   -> "rune"
    (_, "Int")    -> "int"
    (_, "Float")  -> "float64"
    (_, "Bool")   -> "bool"
    (_, "String") -> "string"
    (_, "Char")   -> "rune"
    (_, "Bytes")  -> "[]byte"

    -- Parameterised core types
    (_, "List")   -> case args of
        [elem] -> "rt.SkyList[" ++ typeToGo elem ++ "]"
        _      -> "rt.SkyList[any]"

    (_, "Maybe")  -> case args of
        [inner] -> "rt.SkyMaybe[" ++ typeToGo inner ++ "]"
        _       -> "rt.SkyMaybe[any]"

    (_, "Result") -> case args of
        [err, ok] -> "rt.SkyResult[" ++ typeToGo err ++ ", " ++ typeToGo ok ++ "]"
        _         -> "rt.SkyResult[any, any]"

    (_, "Task") -> case args of
        [err, ok] -> "rt.SkyTask[" ++ typeToGo err ++ ", " ++ typeToGo ok ++ "]"
        _         -> "rt.SkyTask[any, any]"

    (_, "Dict") -> case args of
        [k, v] -> "rt.SkyDict[" ++ typeToGo k ++ ", " ++ typeToGo v ++ "]"
        _      -> "rt.SkyDict[any, any]"

    (_, "Set") -> case args of
        [elem] -> "rt.SkySet[" ++ typeToGo elem ++ "]"
        _      -> "rt.SkySet[any]"

    (_, "Cmd") -> case args of
        [msg] -> "rt.SkyCmd[" ++ typeToGo msg ++ "]"
        _     -> "rt.SkyCmd[any]"

    (_, "Sub") -> case args of
        [msg] -> "rt.SkySub[" ++ typeToGo msg ++ "]"
        _     -> "rt.SkySub[any]"

    -- User-defined types: Module_Name or Module_Name[T1, T2]
    _ ->
        let prefix = goModulePrefix home
            goName = prefix ++ "_" ++ name
        in case args of
            [] -> goName
            _  -> goName ++ "[" ++ commaJoin (map typeToGo args) ++ "]"


-- | Convert a record type to a Go anonymous struct
goRecordType :: Map.Map String T.FieldType -> String
goRecordType fields =
    let fieldStrs = map goFieldStr (Map.toList fields)
    in "struct{ " ++ unwords fieldStrs ++ " }"
  where
    goFieldStr (name, T.FieldType _ ty) =
        capitalize name ++ " " ++ typeToGo ty ++ ";"

    capitalize [] = []
    capitalize (c:cs) = toEnum (fromEnum c - 32) : cs


-- | Module name to Go prefix: Sky.Core.List -> Sky_Core_List
goModulePrefix :: ModuleName.Canonical -> String
goModulePrefix home =
    map (\c -> if c == '.' then '_' else c) (ModuleName.toString home)


-- HELPERS

commaJoin :: [String] -> String
commaJoin [] = ""
commaJoin [x] = x
commaJoin (x:xs) = x ++ ", " ++ commaJoin xs


unwords :: [String] -> String
unwords [] = ""
unwords [x] = x
unwords (x:xs) = x ++ " " ++ unwords xs
