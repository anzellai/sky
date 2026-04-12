-- | Environment for canonicalisation (name resolution).
-- Tracks imports, aliases, constructors, and local bindings.
module Sky.Canonicalise.Environment where

import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Sky.ModuleName as ModuleName


-- | The canonicalisation environment
data Env = Env
    { _home       :: !ModuleName.Canonical
    , _vars       :: !(Map.Map String VarHome)
    , _types      :: !(Map.Map String TypeHome)
    , _ctors      :: !(Map.Map String CtorHome)
    , _aliases    :: !(Map.Map String AliasInfo)
    , _qualVars   :: !(Map.Map String (Map.Map String VarHome))
    , _qualTypes  :: !(Map.Map String (Map.Map String TypeHome))
    , _qualCtors  :: !(Map.Map String (Map.Map String CtorHome))
    , _importAliases :: !(Map.Map String ModuleName.Canonical)  -- alias → full module name
    }
    deriving (Show)


-- | Where a variable lives
data VarHome
    = VarLocal
    | VarTopLevel !ModuleName.Canonical
    | VarKernel !String !String   -- kernel module, function
    deriving (Show)


-- | Where a type lives
data TypeHome = TypeHome
    { _th_home :: !ModuleName.Canonical
    , _th_name :: !String
    , _th_arity :: !Int
    }
    deriving (Show)


-- | Where a constructor lives
data CtorHome = CtorHome
    { _ch_home  :: !ModuleName.Canonical
    , _ch_type  :: !String       -- the union type it belongs to
    , _ch_name  :: !String       -- constructor name
    , _ch_index :: !Int          -- constructor index in the union
    , _ch_arity :: !Int          -- number of arguments
    , _ch_union :: !Can.Union    -- the full union info
    , _ch_annot :: !Can.Annotation  -- constructor type
    }
    deriving (Show)


-- | Type alias info
data AliasInfo = AliasInfo
    { _ai_home :: !ModuleName.Canonical
    , _ai_vars :: [String]
    , _ai_type :: !Can.Type
    }
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- CONSTRUCTION
-- ═══════════════════════════════════════════════════════════

-- | Create a base environment with Sky's built-in types and constructors
initialEnv :: ModuleName.Canonical -> Env
initialEnv home = Env
    { _home      = home
    , _vars      = Map.fromList builtinVars
    , _types     = Map.fromList builtinTypes
    , _ctors     = Map.fromList builtinCtors
    , _aliases   = Map.empty
    , _qualVars  = Map.empty
    , _qualTypes = Map.empty
    , _qualCtors = Map.empty
    , _importAliases = Map.empty
    }


-- | Add a local variable binding
addLocal :: String -> Env -> Env
addLocal name env =
    env { _vars = Map.insert name VarLocal (_vars env) }


-- | Add multiple local variable bindings
addLocals :: [String] -> Env -> Env
addLocals names env = foldr addLocal env names


-- | Add a qualified import alias
addQualifiedImport :: String -> ModuleName.Canonical -> [(String, VarHome)] -> [(String, CtorHome)] -> Env -> Env
addQualifiedImport alias modName vars ctors env = env
    { _qualVars = Map.insertWith Map.union alias (Map.fromList vars) (_qualVars env)
    , _qualCtors = Map.insertWith Map.union alias (Map.fromList ctors) (_qualCtors env)
    , _importAliases = Map.insert alias modName (_importAliases env)
    }


-- | Add exposed names from an import
addExposed :: [(String, VarHome)] -> [(String, CtorHome)] -> Env -> Env
addExposed vars ctors env = env
    { _vars = foldr (\(n, v) -> Map.insert n v) (_vars env) vars
    , _ctors = foldr (\(n, c) -> Map.insert n c) (_ctors env) ctors
    }


-- ═══════════════════════════════════════════════════════════
-- LOOKUP
-- ═══════════════════════════════════════════════════════════

lookupVar :: String -> Env -> Maybe VarHome
lookupVar name env = Map.lookup name (_vars env)


lookupQualVar :: String -> String -> Env -> Maybe VarHome
lookupQualVar qualifier name env = do
    modVars <- Map.lookup qualifier (_qualVars env)
    Map.lookup name modVars


lookupCtor :: String -> Env -> Maybe CtorHome
lookupCtor name env = Map.lookup name (_ctors env)


lookupQualCtor :: String -> String -> Env -> Maybe CtorHome
lookupQualCtor qualifier name env = do
    modCtors <- Map.lookup qualifier (_qualCtors env)
    Map.lookup name modCtors


lookupImportAlias :: String -> Env -> Maybe ModuleName.Canonical
lookupImportAlias alias env = Map.lookup alias (_importAliases env)


lookupType :: String -> Env -> Maybe TypeHome
lookupType name env = Map.lookup name (_types env)


lookupAlias :: String -> Env -> Maybe AliasInfo
lookupAlias name env = Map.lookup name (_aliases env)


-- ═══════════════════════════════════════════════════════════
-- BUILT-INS
-- ═══════════════════════════════════════════════════════════

-- | Built-in variables (from Prelude)
builtinVars :: [(String, VarHome)]
builtinVars =
    [ ("identity",    VarKernel "Basics" "identity")
    , ("always",      VarKernel "Basics" "always")
    , ("not",         VarKernel "Basics" "not")
    , ("toString",    VarKernel "Basics" "toString")
    , ("modBy",       VarKernel "Basics" "modBy")
    , ("clamp",       VarKernel "Basics" "clamp")
    , ("fst",         VarKernel "Basics" "fst")
    , ("snd",         VarKernel "Basics" "snd")
    , ("errorToString", VarKernel "Basics" "errorToString")
    ]


-- | Built-in types
builtinTypes :: [(String, TypeHome)]
builtinTypes =
    [ ("Int",    TypeHome ModuleName.basics "Int" 0)
    , ("Float",  TypeHome ModuleName.basics "Float" 0)
    , ("Bool",   TypeHome ModuleName.basics "Bool" 0)
    , ("String", TypeHome ModuleName.basics "String" 0)
    , ("Char",   TypeHome ModuleName.basics "Char" 0)
    , ("List",   TypeHome ModuleName.list "List" 1)
    , ("Maybe",  TypeHome ModuleName.maybe_ "Maybe" 1)
    , ("Result", TypeHome ModuleName.result_ "Result" 2)
    , ("Task",   TypeHome ModuleName.task "Task" 2)
    ]


-- | Built-in constructors (Ok, Err, Just, Nothing, True, False)
builtinCtors :: [(String, CtorHome)]
builtinCtors =
    let
        boolUnion = Can.Union [] [Can.Ctor "True" 0 0 [], Can.Ctor "False" 1 0 []] 2 Can.Enum
        boolType = Can.TType ModuleName.basics "Bool" []

        maybeUnion = Can.Union ["a"]
            [ Can.Ctor "Just" 0 1 [Can.TVar "a"]
            , Can.Ctor "Nothing" 1 0 []
            ] 2 Can.Normal
        maybeAnnotJust = Can.Forall ["a"] (Can.TLambda (Can.TVar "a") (Can.TType ModuleName.maybe_ "Maybe" [Can.TVar "a"]))
        maybeAnnotNothing = Can.Forall ["a"] (Can.TType ModuleName.maybe_ "Maybe" [Can.TVar "a"])

        resultUnion = Can.Union ["e", "a"]
            [ Can.Ctor "Ok" 0 1 [Can.TVar "a"]
            , Can.Ctor "Err" 1 1 [Can.TVar "e"]
            ] 2 Can.Normal
        resultAnnotOk = Can.Forall ["e", "a"] (Can.TLambda (Can.TVar "a") (Can.TType ModuleName.result_ "Result" [Can.TVar "e", Can.TVar "a"]))
        resultAnnotErr = Can.Forall ["e", "a"] (Can.TLambda (Can.TVar "e") (Can.TType ModuleName.result_ "Result" [Can.TVar "e", Can.TVar "a"]))
    in
    [ ("True",    CtorHome ModuleName.basics "Bool" "True" 0 0 boolUnion (Can.Forall [] boolType))
    , ("False",   CtorHome ModuleName.basics "Bool" "False" 1 0 boolUnion (Can.Forall [] boolType))
    , ("Just",    CtorHome ModuleName.maybe_ "Maybe" "Just" 0 1 maybeUnion maybeAnnotJust)
    , ("Nothing", CtorHome ModuleName.maybe_ "Maybe" "Nothing" 1 0 maybeUnion maybeAnnotNothing)
    , ("Ok",      CtorHome ModuleName.result_ "Result" "Ok" 0 1 resultUnion resultAnnotOk)
    , ("Err",     CtorHome ModuleName.result_ "Result" "Err" 1 1 resultUnion resultAnnotErr)
    ]


-- | Kernel module mappings: Sky import path → kernel module name
kernelModules :: Map.Map String String
kernelModules = Map.fromList
    [ ("Sky.Core.Basics",  "Basics")
    , ("Sky.Core.String",  "String")
    , ("Sky.Core.List",    "List")
    , ("Sky.Core.Dict",    "Dict")
    , ("Sky.Core.Set",     "Set")
    , ("Sky.Core.Maybe",   "Maybe")
    , ("Sky.Core.Result",  "Result")
    , ("Sky.Core.Task",    "Task")
    , ("Sky.Core.Math",    "Math")
    , ("Sky.Core.Regex",   "Regex")
    , ("Sky.Core.Crypto",  "Crypto")
    , ("Sky.Core.Encoding","Encoding")
    , ("Sky.Core.Char",    "Char")
    , ("Sky.Core.Path",    "Path")
    , ("Std.Log",          "Log")
    , ("Std.Cmd",          "Cmd")
    , ("Std.Sub",          "Sub")
    , ("Std.Db",           "Db")
    , ("Std.Auth",         "Auth")
    , ("Sky.Core.Io",      "Io")
    , ("Sky.Core.File",    "File")
    , ("Sky.Core.Process", "Process")
    , ("Sky.Core.Time",    "Time")
    , ("Sky.Core.Random",  "Random")
    , ("Sky.Core.Http",    "Http")
    , ("Sky.Http.Server",  "Server")
    , ("Sky.Core.Prelude", "Basics")  -- Prelude maps to Basics
    ]
