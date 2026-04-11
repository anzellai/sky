-- | Source AST — the raw parse tree before name resolution.
-- Adapted from Elm's AST.Source with Sky extensions:
-- - MultilineStr for """...""" strings
-- - FfiImport for Go FFI declarations
-- - No Shader, Port, or Effect declarations
module Sky.AST.Source where

import qualified Sky.Reporting.Annotation as A


-- | A parsed module
data Module = Module
    { _name    :: Maybe (A.Located [String])  -- module name segments
    , _exports :: A.Located Exposing
    , _docs    :: Docs
    , _imports :: [Import]
    , _values  :: [A.Located Value]
    , _unions  :: [A.Located Union]
    , _aliases :: [A.Located Alias]
    , _binops  :: [A.Located Infix]
    }
    deriving (Show)


-- | Export specification
data Exposing
    = ExposingAll
    | ExposingList [A.Located Exposed]
    deriving (Show)


data Exposed
    = ExposedValue String
    | ExposedType String Privacy
    | ExposedOperator String
    deriving (Show)


data Privacy
    = Public
    | Private
    deriving (Show)


-- | Module documentation
data Docs
    = NoDocs
    | HasDocs String
    deriving (Show)


-- | Import declaration
data Import = Import
    { _importName    :: A.Located [String]   -- module name
    , _importAlias   :: Maybe String         -- as Alias
    , _importExposing :: A.Located Exposing
    }
    deriving (Show)


-- | Top-level value/function declaration
data Value = Value
    { _valueName    :: A.Located String
    , _valuePatterns :: [Pattern]
    , _valueBody    :: Expr
    , _valueType    :: Maybe (A.Located TypeAnnotation)
    }
    deriving (Show)


-- | Union type declaration
data Union = Union
    { _unionName   :: A.Located String
    , _unionVars   :: [A.Located String]
    , _unionCtors  :: [A.Located (String, [TypeAnnotation])]
    }
    deriving (Show)


-- | Type alias declaration
data Alias = Alias
    { _aliasName :: A.Located String
    , _aliasVars :: [A.Located String]
    , _aliasType :: A.Located TypeAnnotation
    }
    deriving (Show)


-- | Operator fixity declaration
data Infix = Infix
    { _infixOp    :: String
    , _infixAssoc :: Assoc
    , _infixPrec  :: Int
    , _infixFunc  :: String
    }
    deriving (Show)


data Assoc = AssocLeft | AssocRight | AssocNone
    deriving (Show)


-- | Expressions
data Expr_
    = Chr String
    | Str String
    | MultilineStr String              -- """content with {{interpolation}}"""
    | Int Int
    | Float Double
    | Var String
    | VarQual String String            -- Module.name
    | List [Expr]
    | Op String
    | Negate Expr
    | Binops [(Expr, A.Located String)] Expr
    | Lambda [Pattern] Expr
    | Call Expr [Expr]
    | If [(Expr, Expr)] Expr
    | Let [A.Located Def] Expr
    | Case Expr [(Pattern, Expr)]
    | Accessor String                  -- .field
    | Access Expr (A.Located String)   -- expr.field
    | Update (A.Located String) [(A.Located String, Expr)]
    | Record [(A.Located String, Expr)]
    | Unit
    | Tuple Expr Expr [Expr]
    deriving (Show)


type Expr = A.Located Expr_


-- | Local definition (in let-in)
data Def = Def
    { _defName     :: A.Located String
    , _defPatterns :: [Pattern]
    , _defBody     :: Expr
    , _defType     :: Maybe (A.Located TypeAnnotation)
    }
    deriving (Show)


-- | Patterns
data Pattern_
    = PAnything
    | PVar String
    | PRecord [A.Located String]
    | PAlias Pattern (A.Located String)
    | PUnit
    | PTuple Pattern Pattern [Pattern]
    | PCtor String [String] [Pattern]  -- module segments, name, sub-patterns
    | PCtorQual String String [Pattern]
    | PList [Pattern]
    | PCons Pattern Pattern
    | PChr String
    | PStr String
    | PInt Int
    | PFloat Double
    | PBool Bool
    deriving (Show)


type Pattern = A.Located Pattern_


-- | Type annotations
data TypeAnnotation
    = TLambda TypeAnnotation TypeAnnotation
    | TVar String
    | TType String [String] [TypeAnnotation]  -- module, name, args
    | TTypeQual String String [TypeAnnotation]
    | TRecord [(A.Located String, TypeAnnotation)] (Maybe String)
    | TUnit
    | TTuple TypeAnnotation TypeAnnotation [TypeAnnotation]
    deriving (Show)
