-- | Canonical AST — name-resolved, ready for type checking.
--
-- Derivative work adapted from elm/compiler's @AST.Canonical@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- All variables are fully qualified. Imports resolved. No
-- syntactic sugar. Adapted with Sky module names.
module Sky.AST.Canonical where

import qualified Data.Map.Strict as Map
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName


-- ═══════════════════════════════════════════════════════════
-- MODULE
-- ═══════════════════════════════════════════════════════════

data Module = Module
    { _name    :: !ModuleName.Canonical
    , _exports :: !Exports
    , _decls   :: !Decls
    , _unions  :: !(Map.Map String Union)
    , _aliases :: !(Map.Map String Alias)
    }
    deriving (Show)


data Exports
    = ExportEverything
    | ExportExplicit (Map.Map String A.Region)
    deriving (Show)


-- | All declarations in a module, grouped by dependency
data Decls
    = Declare !Def !Decls
    | DeclareRec !Def [Def] !Decls
    | SaveTheEnvironment         -- sentinel: end of declarations
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- DEFINITIONS
-- ═══════════════════════════════════════════════════════════

data Def
    = Def !(A.Located String) [Pattern] !Expr
    | TypedDef !(A.Located String) !FreeVars [TypedPattern] !Expr !Type
    -- | Pattern destructure at let-binding position — `let (a,b) = e in …`.
    -- Carries the pattern so the lowerer can emit actual field bindings
    -- (tuple / record / constructor destructure), not just a single name.
    | DestructDef !Pattern !Expr
    deriving (Show)


type FreeVars = [(String, ())]  -- free type variables from annotation


type TypedPattern = (Pattern, Type)


-- ═══════════════════════════════════════════════════════════
-- EXPRESSIONS
-- ═══════════════════════════════════════════════════════════

type Expr = A.Located Expr_


data Expr_
    -- Literals
    = VarLocal !String
    | VarTopLevel !ModuleName.Canonical !String
    | VarKernel !String !String          -- module, function (stdlib kernel)
    | VarCtor !CtorOpts !ModuleName.Canonical !String !String !Annotation
    | Chr !String
    | Str !String
    | Int !Int
    | Float !Double
    | List [Expr]
    | Negate !Expr
    | Binop !String !ModuleName.Canonical !String !Annotation !Expr !Expr
    | Lambda [Pattern] !Expr
    | Call !Expr [Expr]
    | If [(Expr, Expr)] !Expr
    | Let !Def !Expr
    | LetRec [Def] !Expr
    | LetDestruct !Pattern !Expr !Expr
    | Case !Expr [CaseBranch]
    | Accessor !String
    | Access !Expr !(A.Located String)
    | Update !(A.Located String) !Expr (Map.Map String FieldUpdate)
    | Record (Map.Map String Expr)
    | Unit
    | Tuple !Expr !Expr ![Expr]   -- e1, e2, then zero or more further elems
    deriving (Show)


data CaseBranch = CaseBranch !Pattern !Expr
    deriving (Show)


data FieldUpdate = FieldUpdate !A.Region !Expr
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- PATTERNS
-- ═══════════════════════════════════════════════════════════

type Pattern = A.Located Pattern_


data Pattern_
    = PAnything
    | PVar !String
    | PRecord [String]
    | PAlias !Pattern !String
    | PUnit
    | PTuple !Pattern !Pattern ![Pattern]
    | PList [Pattern]
    | PCons !Pattern !Pattern
    | PBool !Bool
    | PChr !String
    | PStr !String
    | PInt !Int
    | PCtor
        { _p_home    :: !ModuleName.Canonical
        , _p_type    :: !String      -- type name
        , _p_union   :: !Union       -- the union this belongs to
        , _p_name    :: !String      -- constructor name
        , _p_index   :: !Int         -- constructor index
        , _p_args    :: [PatternCtorArg]
        }
    deriving (Show)


data PatternCtorArg = PatternCtorArg
    { _pca_index :: !Int   -- positional index
    , _pca_type  :: !Type  -- expected type
    , _pca_pat   :: !Pattern
    }
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- TYPES
-- ═══════════════════════════════════════════════════════════

-- Note: these mirror the definitions in Sky.Type.Type.
-- The constraint system uses T.Type; conversion is done in Constrain modules.

data Type
    = TLambda !Type !Type
    | TVar !String
    | TType !ModuleName.Canonical !String [Type]
    | TRecord !(Map.Map String FieldType) !(Maybe String)
    | TUnit
    | TTuple !Type !Type ![Type]
    | TAlias !ModuleName.Canonical !String [(String, Type)] !AliasType
    deriving (Eq, Ord, Show)


data FieldType = FieldType
    { _fieldIndex :: {-# UNPACK #-} !Int
    , _fieldType  :: !Type
    }
    deriving (Eq, Ord, Show)


data AliasType
    = Hoisted !Type
    | Filled !Type
    deriving (Eq, Ord, Show)


-- | A type annotation with quantified variables
data Annotation = Forall [String] Type
    deriving (Eq, Ord, Show)

-- ═══════════════════════════════════════════════════════════
-- UNION TYPES
-- ═══════════════════════════════════════════════════════════

data Union = Union
    { _u_vars  :: [String]
    , _u_alts  :: [Ctor]
    , _u_numAlts :: !Int
    , _u_opts  :: !CtorOpts
    }
    deriving (Show)


data Ctor = Ctor !String !Int !Int [Type]
    -- name, index, arity, arg types
    deriving (Show)


data CtorOpts
    = Normal
    | Enum       -- all constructors are zero-arg
    | Unbox      -- single constructor, single arg
    deriving (Eq, Ord, Show)


-- ═══════════════════════════════════════════════════════════
-- TYPE ALIASES
-- ═══════════════════════════════════════════════════════════

data Alias = Alias [String] Type
    deriving (Show)
