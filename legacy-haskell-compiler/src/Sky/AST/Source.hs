{-# LANGUAGE PatternSynonyms #-}
-- | Source AST — the raw parse tree before name resolution.
--
-- Derivative work adapted from elm/compiler's @AST.Source@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- Sky extensions over the upstream shape:
-- - MultilineStr for """...""" strings
-- - FfiImport for Go FFI declarations
-- - No Shader, Port, or Effect declarations
module Sky.AST.Source where

import qualified Sky.Reporting.Annotation as A


-- | A parsed module.
--
-- Audit P2-1: `_comments` holds every `--` line comment and
-- `{- ... -}` block comment with its source region. Populated by
-- `Parse.Module.parseModule` via a post-parse raw-text scan so the
-- rest of the parser combinators stay unchanged. The formatter
-- reads this list and interleaves comments by row into its output,
-- giving `sky fmt` proper round-trip preservation without the
-- text-heuristic post-pass that used to live in app/Main.hs.
--
-- v0.17.8 (#144 rewrite): each comment now carries structured
-- metadata (`ParsedComment`) so downstream consumers can
-- discriminate line-vs-block comments and own-line-vs-trailing
-- position without re-tokenising. `_commentText` is the raw body
-- without delimiters (`--` / `{-` / `-}`); the formatter adds
-- them back on emit. Leading whitespace on line comments is
-- preserved (previous `trimStart` was destructive for round-trip).
-- See `docs/v0.17-roadmap/fmt-ast-comments.md` for the full
-- Phase 1-4 rollout.
data Module = Module
    { _name     :: Maybe (A.Located [String])  -- module name segments
    , _exports  :: A.Located Exposing
    , _docs     :: Docs
    , _imports  :: [Import]
    , _values   :: [A.Located Value]
    , _unions   :: [A.Located Union]
    , _aliases  :: [A.Located Alias]
    , _binops   :: [A.Located Infix]
    , _comments :: [A.Located ParsedComment]  -- audit P2-1 / v0.17.8 #144
    }
    deriving (Show)


-- | Line comment (`--`) vs block comment (`{- ... -}`).
-- Populated at parse time so the formatter can re-emit the right
-- delimiters without inferring from the body's shape. A single-line
-- block (`{- foo -}`) is indistinguishable from a line (`-- foo`)
-- at the region level alone; this tag is load-bearing for
-- round-trip correctness.
data CommentKind
    = CommentLine
    | CommentBlock
    deriving (Show, Eq)


-- | Position class within its source row.
-- Own-line: only whitespace precedes the comment on its row.
-- Trailing: non-whitespace code precedes the comment on its row
-- (e.g. `x = 1  -- inline`). The formatter uses this to decide
-- whether to append the comment to a preceding expression's line
-- or emit it as its own line above the following node.
data CommentPos
    = CommentOwnLine
    | CommentTrailing
    deriving (Show, Eq)


-- | A parsed comment carried by `_comments`.
--
-- `_commentText` is the raw body verbatim (no delimiter, no
-- whitespace trimming — the formatter can normalise the leading
-- space if desired, but the raw form is what enables round-trip).
-- `_commentCol` is the 1-based source start column (`_start._col`
-- from the containing `Region`) — used to recover indentation
-- when the enclosing AST node's indent doesn't match.
data ParsedComment = ParsedComment
    { _commentKind :: !CommentKind
    , _commentPos  :: !CommentPos
    , _commentText :: !String
    , _commentCol  :: !Int
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
    = Public                -- Type(..) — all constructors exposed
    | Private               -- Type     — opaque type alias only
    | PublicCtors [String]  -- Type(CtorA, CtorB) — selective expose
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
    | Paren Expr                       -- (e) — explicit grouping, prevents
                                       -- precedence-climbing from flattening
                                       -- a nested Binops subtree. Canonicalises
                                       -- transparently as the inner expression.
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


-- | Local definition (in let-in). Two forms (Elm-compatible):
--   Define    — a named value/function  (x = …   or   f a b = …)
--   Destruct  — pattern-based destructure  ((a, b) = …   or   { x, y } = …)
data Def
    = Define !(A.Located String) [Pattern] !Expr !(Maybe (A.Located TypeAnnotation))
    | Destruct !Pattern !Expr
    deriving (Show)


-- | Back-compat constructor name so existing code keeps compiling during
-- the migration. Matches the original `Def` field order.
pattern Def :: A.Located String -> [Pattern] -> Expr -> Maybe (A.Located TypeAnnotation) -> Def
pattern Def n ps b ty = Define n ps b ty

-- | Back-compat accessors that match the original record field names.
-- They return sensible defaults for Destruct defs so existing callers
-- degrade gracefully until they learn to pattern-match on both variants.
_defName :: Def -> A.Located String
_defName (Define n _ _ _) = n
_defName (Destruct _ _)   = A.At A.one "__destruct__"

_defPatterns :: Def -> [Pattern]
_defPatterns (Define _ ps _ _) = ps
_defPatterns (Destruct _ _)    = []

_defBody :: Def -> Expr
_defBody (Define _ _ b _) = b
_defBody (Destruct _ b)   = b

_defType :: Def -> Maybe (A.Located TypeAnnotation)
_defType (Define _ _ _ t) = t
_defType (Destruct _ _)   = Nothing


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
