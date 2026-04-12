-- | Go intermediate representation for typed code generation.
-- All types are explicit — no `any` in normal code.
-- Uses Go generics (1.18+) for polymorphic functions.
module Sky.Generate.Go.Ir where

import qualified Data.Map.Strict as Map


-- ═══════════════════════════════════════════════════════════
-- EXPRESSIONS
-- ═══════════════════════════════════════════════════════════

data GoExpr
    = GoIdent !String                              -- variable reference
    | GoQualified !String !String                   -- package.name
    | GoIntLit !Int                                 -- 42
    | GoFloatLit !Double                            -- 3.14
    | GoStringLit !String                           -- "hello"
    | GoRuneLit !String                             -- 'c'
    | GoBoolLit !Bool                               -- true / false
    | GoNil                                         -- nil
    | GoCall !GoExpr [GoExpr]                       -- f(a, b)
    | GoGenericCall !String [String] [GoExpr]       -- f[T1, T2](a, b)
    | GoSelector !GoExpr !String                    -- expr.field
    | GoIndex !GoExpr !GoExpr                       -- expr[index]
    | GoSliceLit !String [GoExpr]                   -- []T{a, b, c}
    | GoMapLit !String !String [(GoExpr, GoExpr)]   -- map[K]V{k: v, ...}
    | GoStructLit !String [(String, GoExpr)]        -- T{Field: val, ...}
    | GoFuncLit [GoParam] !String [GoStmt]          -- func(params) retType { body }
    | GoBinary !String !GoExpr !GoExpr              -- a op b
    | GoUnary !String !GoExpr                       -- op a
    | GoTypeAssert !GoExpr !String                  -- expr.(Type)
    | GoBlock [GoStmt] !GoExpr                      -- IIFE: func() T { stmts; return expr }()
    | GoRaw !String                                 -- raw Go code (escape hatch)
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- STATEMENTS
-- ═══════════════════════════════════════════════════════════

data GoStmt
    = GoExprStmt !GoExpr                            -- expr
    | GoAssign !String !GoExpr                      -- name = expr
    | GoShortDecl !String !GoExpr                   -- name := expr
    | GoVarDecl !String !String !(Maybe GoExpr)     -- var name Type = expr
    | GoReturn !GoExpr                              -- return expr
    | GoReturnVoid                                  -- return
    | GoIf !GoExpr [GoStmt] [GoStmt]                -- if cond { then } else { else }
    | GoSwitch !GoExpr [(GoExpr, [GoStmt])]         -- switch expr { case val: stmts }
    | GoTypeSwitch !String !GoExpr [(String, [GoStmt])] -- switch name := expr.(type) { case T: ... }
    | GoFor !String !GoExpr [GoStmt]                -- for _, name := range expr { stmts }
    | GoBlock_ [GoStmt]                             -- { stmts }
    | GoComment !String                             -- // comment
    | GoBlank                                       -- blank line
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

data GoDecl
    = GoDeclFunc !GoFuncDecl                        -- func name(...) T { ... }
    | GoDeclVar !String !String !(Maybe GoExpr)     -- var name Type = expr
    | GoDeclConst !String !String !GoExpr           -- const name Type = expr
    | GoDeclType !String !GoTypeDef                 -- type Name = ...
    | GoDeclInterface !String [(String, [GoParam], String)] -- type Name interface { ... }
    | GoDeclMethod !String !String !GoFuncDecl      -- func (r Recv) Name(...) T { ... }
    | GoDeclRaw !String                             -- raw Go declaration
    deriving (Show)


-- | Function declaration with Go generics support
data GoFuncDecl = GoFuncDecl
    { _gf_name       :: !String
    , _gf_typeParams :: [(String, String)]          -- [(T, any), (E, error)]
    , _gf_params     :: [GoParam]
    , _gf_returnType :: !String
    , _gf_body       :: [GoStmt]
    }
    deriving (Show)


-- | Function parameter
data GoParam = GoParam
    { _gp_name :: !String
    , _gp_type :: !String
    }
    deriving (Show)


-- | Type definition
data GoTypeDef
    = GoStructDef [(String, String)]                -- struct { Field Type; ... }
    | GoAliasDef !String                            -- = OtherType
    | GoEnumDef [String]                            -- iota-based enum
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- PACKAGE
-- ═══════════════════════════════════════════════════════════

-- | A complete Go package (single file output)
data GoPackage = GoPackage
    { _pkg_name    :: !String                       -- package name
    , _pkg_imports :: [GoImport]                    -- import statements
    , _pkg_decls   :: [GoDecl]                      -- declarations
    }
    deriving (Show)


-- | Go import
data GoImport = GoImport
    { _imp_path  :: !String                         -- "fmt"
    , _imp_alias :: !(Maybe String)                 -- as alias (or Nothing)
    }
    deriving (Show)
