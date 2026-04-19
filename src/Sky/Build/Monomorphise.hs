-- | Monomorphisation pass for Sky's typed codegen.
--
-- Walks the canonical AST + solved types and collects every unique
-- instantiation of polymorphic functions at their call sites.
-- Produces a registry of specialised function names + their concrete
-- Go types. The codegen uses this to emit one concrete Go function
-- per instantiation, with zero `any`.
--
-- Example:
--   identity : a -> a
--   x = identity "hello"    → identity_String : string -> string
--   y = identity 42         → identity_Int : int -> int
module Sky.Build.Monomorphise
    ( MonoRegistry
    , emptyRegistry
    , collectInstantiations
    , lookupMono
    , allSpecialisations
    , MonoSpec(..)
    ) where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Canonical as Can
import qualified Sky.Type.Type as T
import qualified Sky.Reporting.Annotation as A


-- | A concrete instantiation of a polymorphic function.
data MonoSpec = MonoSpec
    { _ms_baseName   :: !String        -- "List_map", "identity"
    , _ms_goName     :: !String        -- "List_map_User_String"
    , _ms_argTypes   :: ![String]      -- ["User_R", "string"]
    , _ms_retType    :: !String        -- "[]string"
    , _ms_isKernel   :: !Bool          -- True for stdlib/kernel fns
    } deriving (Show, Eq, Ord)


-- | Registry of all monomorphised function instantiations.
type MonoRegistry = Map.Map String MonoSpec  -- goName → spec


emptyRegistry :: MonoRegistry
emptyRegistry = Map.empty


-- | Look up a monomorphised name for a call site.
lookupMono :: String -> [T.Type] -> MonoRegistry -> Maybe MonoSpec
lookupMono baseName argTypes reg =
    let goName = monoName baseName argTypes
    in Map.lookup goName reg


-- | All collected specialisations.
allSpecialisations :: MonoRegistry -> [MonoSpec]
allSpecialisations = Map.elems


-- | Generate a monomorphised function name from the base name
-- and concrete argument types.
monoName :: String -> [T.Type] -> String
monoName base argTypes =
    base ++ concatMap (\t -> "_" ++ typeTag t) argTypes


-- | Short tag for a type, used in monomorphised function names.
typeTag :: T.Type -> String
typeTag ty = case ty of
    T.TType _ "String" [] -> "String"
    T.TType _ "Int" []    -> "Int"
    T.TType _ "Float" []  -> "Float"
    T.TType _ "Bool" []   -> "Bool"
    T.TType _ "Char" []   -> "Char"
    T.TUnit               -> "Unit"
    T.TType _ name []     -> name
    T.TType _ name args   -> name ++ "_" ++ concatMap typeTag args
    T.TVar name           -> "T" ++ filter (/= '_') name
    T.TLambda from to     -> "Fn" ++ typeTag from ++ "_" ++ typeTag to
    T.TRecord _ _         -> "Rec"
    T.TTuple a b _        -> "Tup" ++ typeTag a ++ "_" ++ typeTag b
    T.TAlias _ name _ _   -> name
    _                     -> "Any"


-- | Walk a canonical module's declarations and collect all call-site
-- instantiations of polymorphic functions. Uses the SolvedTypes map
-- to determine argument types at each call.
collectInstantiations
    :: Map.Map String T.Type    -- SolvedTypes: name → type
    -> Can.Module
    -> MonoRegistry
collectInstantiations solvedTypes canMod =
    let decls = collectDefs (Can._decls canMod)
        initialReg = emptyRegistry
    in foldl (collectFromDef solvedTypes) initialReg decls


collectDefs :: Can.Decls -> [Can.Def]
collectDefs (Can.Declare d rest)       = d : collectDefs rest
collectDefs (Can.DeclareRec d ds rest) = d : ds ++ collectDefs rest
collectDefs Can.SaveTheEnvironment     = []


collectFromDef :: Map.Map String T.Type -> MonoRegistry -> Can.Def -> MonoRegistry
collectFromDef solved reg def = case def of
    Can.Def _ _ body          -> collectFromExpr solved reg body
    Can.TypedDef _ _ _ body _ -> collectFromExpr solved reg body
    Can.DestructDef _ body    -> collectFromExpr solved reg body


collectFromExpr :: Map.Map String T.Type -> MonoRegistry -> Can.Expr -> MonoRegistry
collectFromExpr solved reg (A.At _ expr) = case expr of
    Can.Call func args ->
        -- TODO: determine if func is polymorphic, resolve arg types,
        -- register the instantiation
        let reg1 = collectFromExpr solved reg func
        in foldl (collectFromExpr solved) reg1 args

    Can.If branches elseExpr ->
        let reg1 = foldl (\r (c, b) ->
                collectFromExpr solved (collectFromExpr solved r c) b) reg branches
        in collectFromExpr solved reg1 elseExpr

    Can.Let def body ->
        collectFromExpr solved (collectFromDef solved reg def) body

    Can.Case subj branches ->
        let reg1 = collectFromExpr solved reg subj
        in foldl (\r (Can.CaseBranch _ b) -> collectFromExpr solved r b) reg1 branches

    Can.Lambda _ body -> collectFromExpr solved reg body
    Can.List items -> foldl (collectFromExpr solved) reg items
    Can.Binop _ _ _ _ left right ->
        collectFromExpr solved (collectFromExpr solved reg left) right
    Can.Tuple a b cs ->
        foldl (collectFromExpr solved) reg (a:b:cs)
    Can.Access target _ -> collectFromExpr solved reg target
    Can.Negate inner -> collectFromExpr solved reg inner
    Can.Record fields ->
        foldl (\r (_, Can.FieldUpdate _ e) -> collectFromExpr solved r e) reg (Map.toList fields)

    _ -> reg  -- literals, vars, etc. — no sub-expressions
  where
    -- TODO: implement once we know how to extract per-expression types
    -- registerCall :: MonoRegistry -> Can.Expr -> [Can.Expr] -> MonoRegistry
    -- registerCall = undefined
