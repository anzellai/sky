-- | Structured diagnostic value flowing through every compiler phase.
--
-- v0.13 foundation: replace string-based errors with `Diagnostic`
-- values that ALL phases (Parse, Canonicalise, HM, Exhaustiveness,
-- Codegen-validation, Go-build) emit. ONE renderer for CLI + LSP.
--
-- Design goals:
--   1. Stable error CODES so cabal tests grep by code, not message.
--   2. Structured RELATED REGIONS for multi-region errors (record-
--      update attribution, function-def-site hints, etc.).
--   3. Optional SUGGESTED FIXES — LSP-compatible edits the user can
--      apply with one click.
--   4. PURE values (no compiler-internal types) so external tooling
--      can consume them.
--
-- See `docs/v013-diagnostics-architecture.md` for the full plan.
module Sky.Reporting.Diagnostic where

import qualified Sky.Reporting.Annotation as A


-- ═══════════════════════════════════════════════════════════
-- THE AST
-- ═══════════════════════════════════════════════════════════

-- | A single diagnostic emitted by any compiler phase.
data Diagnostic = Diagnostic
    { _diag_file     :: !FilePath           -- source file
    , _diag_region   :: !A.Region           -- primary source range
    , _diag_severity :: !Severity
    , _diag_category :: !Category
    , _diag_code     :: !DiagCode           -- stable across versions
    , _diag_message  :: !String             -- short user-facing summary
    , _diag_related  :: ![RelatedRegion]    -- secondary highlights
    , _diag_hints    :: ![Hint]             -- "Try X" suggestions
    }
    deriving (Show, Eq)


data Severity
    = SevError
    | SevWarning
    | SevInfo
    | SevHint
    deriving (Show, Eq, Ord)


-- | Category — used for sorting + grouping in editor UI + CLI.
-- Each category corresponds to a compiler phase or a logical
-- subsystem within a phase.
data Category
    = CatParse
    | CatCanonical
    | CatType
    | CatExhaustiveness
    | CatCodegen
    | CatGoBuild
    | CatRuntime
    deriving (Show, Eq, Ord)


-- | Stable diagnostic code. Format: `EnnnnX` where n is digits, X is
-- optional suffix letter. Numbers ranges:
--   E0001-E0999  — parse errors
--   E1000-E1999  — canonicalise errors (unbound, ambiguous, etc.)
--   E2000-E2999  — type errors (mismatch, occurs-check, etc.)
--   E3000-E3999  — exhaustiveness errors
--   E4000-E4999  — codegen validation errors
--   E5000-E5999  — go build / runtime
--
-- Codes are STABLE: same bug class = same code across versions.
-- Cabal tests grep by code, not message wording.
newtype DiagCode = DiagCode { unDiagCode :: String }
    deriving (Show, Eq, Ord)


-- | A secondary source region that contributes to the diagnostic.
-- Used for multi-region errors: e.g. "this type was inferred at
-- region X but conflicts with annotation at region Y".
data RelatedRegion = RelatedRegion
    { _rel_file    :: !FilePath
    , _rel_region  :: !A.Region
    , _rel_message :: !String
    }
    deriving (Show, Eq)


-- | A hint message, optionally with a machine-applicable fix.
data Hint = Hint
    { _hint_message       :: !String
    , _hint_suggested_fix :: !(Maybe SuggestedFix)
    }
    deriving (Show, Eq)


-- | An LSP-compatible suggested fix.
data SuggestedFix = SuggestedFix
    { _fix_description :: !String
    , _fix_edits       :: ![TextEdit]
    }
    deriving (Show, Eq)


-- | A single text replacement edit. Matches LSP's `TextEdit` shape.
data TextEdit = TextEdit
    { _edit_file       :: !FilePath
    , _edit_region     :: !A.Region   -- range to replace
    , _edit_newText    :: !String
    }
    deriving (Show, Eq)


-- ═══════════════════════════════════════════════════════════
-- CONSTRUCTORS — convenience for phase code
-- ═══════════════════════════════════════════════════════════

-- | An error diagnostic with just file + region + code + message.
-- The shape every phase needs at minimum.
mkError :: FilePath -> A.Region -> Category -> DiagCode -> String -> Diagnostic
mkError file region category code message = Diagnostic
    { _diag_file     = file
    , _diag_region   = region
    , _diag_severity = SevError
    , _diag_category = category
    , _diag_code     = code
    , _diag_message  = message
    , _diag_related  = []
    , _diag_hints    = []
    }


-- | Add a related region to an existing diagnostic.
withRelated :: FilePath -> A.Region -> String -> Diagnostic -> Diagnostic
withRelated file region message diag = diag
    { _diag_related = _diag_related diag ++
        [RelatedRegion file region message] }


-- | Add a hint (no fix) to an existing diagnostic.
withHint :: String -> Diagnostic -> Diagnostic
withHint message diag = diag
    { _diag_hints = _diag_hints diag ++ [Hint message Nothing] }


-- | Add a hint with a suggested machine-applicable fix.
withFix :: String -> SuggestedFix -> Diagnostic -> Diagnostic
withFix message fix diag = diag
    { _diag_hints = _diag_hints diag ++ [Hint message (Just fix)] }


-- ═══════════════════════════════════════════════════════════
-- CODE REGISTRY — single source of truth
-- ═══════════════════════════════════════════════════════════

-- | Parse phase codes (E0001-E0999).
-- See docs/diagnostics/parse.md (TODO) for per-code prose.
parseE_SyntaxError       :: DiagCode
parseE_SyntaxError       = DiagCode "E0001"

parseE_UnexpectedToken   :: DiagCode
parseE_UnexpectedToken   = DiagCode "E0002"

parseE_MissingExposing   :: DiagCode
parseE_MissingExposing   = DiagCode "E0003"


-- | Canonicalise phase codes (E1000-E1999).
canonE_UndefinedName     :: DiagCode
canonE_UndefinedName     = DiagCode "E1001"

canonE_AmbiguousName     :: DiagCode
canonE_AmbiguousName     = DiagCode "E1002"

canonE_MissingImport     :: DiagCode
canonE_MissingImport     = DiagCode "E1003"

canonE_ImportHiding      :: DiagCode
canonE_ImportHiding      = DiagCode "E1004"


-- | Type phase codes (E2000-E2999).
typeE_Mismatch           :: DiagCode
typeE_Mismatch           = DiagCode "E2001"

typeE_OccursCheck        :: DiagCode
typeE_OccursCheck        = DiagCode "E2002"

typeE_RecordFieldMissing :: DiagCode
typeE_RecordFieldMissing = DiagCode "E2003"

typeE_RecordFieldWrongType :: DiagCode
typeE_RecordFieldWrongType = DiagCode "E2004"

typeE_RecordUpdateMismatch :: DiagCode
typeE_RecordUpdateMismatch = DiagCode "E2005"

typeE_FunctionArity      :: DiagCode
typeE_FunctionArity      = DiagCode "E2006"


-- | Exhaustiveness codes (E3000-E3999).
exhaustE_NonExhaustive   :: DiagCode
exhaustE_NonExhaustive   = DiagCode "E3001"

exhaustE_RedundantArm    :: DiagCode
exhaustE_RedundantArm    = DiagCode "E3002"


-- | Codegen validation codes (E4000-E4999). NEW for v0.13 Layer 2.
codegenE_TypedKernelAnyArg :: DiagCode
codegenE_TypedKernelAnyArg = DiagCode "E4001"
-- ^ Issue #52: typed kernel call with any-typed primitive arg.
--   e.g. `rt.List_dropT[int](anyVar, ...)` where anyVar should be
--   wrapped in `rt.AsInt`.

codegenE_CoerceIncompatible :: DiagCode
codegenE_CoerceIncompatible = DiagCode "E4002"
-- ^ `rt.Coerce[ConcreteType]` on a known-incompatible source type.

codegenE_UnresolvedTVar  :: DiagCode
codegenE_UnresolvedTVar  = DiagCode "E4003"
-- ^ Generic instantiation `rt.X[any, T]` where T came from solver
--   fallback. Caught at codegen-validation rather than runtime.

codegenE_FfiArityMismatch :: DiagCode
codegenE_FfiArityMismatch = DiagCode "E4004"
-- ^ FFI binding called with wrong number of args.

codegenE_UndefinedKernel :: DiagCode
codegenE_UndefinedKernel = DiagCode "E4005"
-- ^ Codegen emitted `rt.X` for a kernel function but the runtime
--   doesn't export `X`. Closes the "Sky type-checks but Go-build
--   rejects with `undefined: rt.X`" class. Issue #56.

authE_UntypedBoundary :: DiagCode
authE_UntypedBoundary = DiagCode "E4006"
-- ^ v0.15.12 P5 / Gap A6: a security-critical Auth kernel call
--   (Auth.hashPassword / hashPasswordCost / passwordStrength /
--   signToken / verifyToken / register / login / setRole) sees
--   an `any`-typed argument at a slot the kernel signature
--   declares as String. The HM type system's wildcard-`any`
--   per-occurrence semantics would let this pass at the call
--   site (each `any` is a fresh UF var that unifies with String),
--   but the Sky binding feeding the value carries no typed
--   contract — at runtime the value could be ANY Go type. The
--   gate stops the program from compiling so the type confusion
--   surfaces at the source instead of as a runtime "expected
--   String" error in an audit log. Diagnostic kind:
--   `Sky.Auth.UntypedBoundary`.


-- | Go-build / runtime codes (E5000-E5999).
goE_BuildFailed          :: DiagCode
goE_BuildFailed          = DiagCode "E5001"

runtimeE_Panic           :: DiagCode
runtimeE_Panic           = DiagCode "E5002"


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

-- | Sort diagnostics by file, then region, then severity. Stable
-- ordering for tests + UI presentation.
sortDiagnostics :: [Diagnostic] -> [Diagnostic]
sortDiagnostics = sortBy compareDiags
  where
    compareDiags a b =
        compare (_diag_file a) (_diag_file b)
            <> compareRegion (_diag_region a) (_diag_region b)
            <> compare (_diag_severity a) (_diag_severity b)
    compareRegion ra rb =
        compare (A._start ra) (A._start rb)
            <> compare (A._end ra) (A._end rb)
    sortBy cmp xs = sortByImpl cmp xs


-- | Count diagnostics by severity.
countBySeverity :: [Diagnostic] -> (Int, Int, Int)
countBySeverity diags =
    let errs   = length [d | d <- diags, _diag_severity d == SevError]
        warns  = length [d | d <- diags, _diag_severity d == SevWarning]
        others = length diags - errs - warns
    in (errs, warns, others)


-- | True if any diagnostic in the list is an error. Compiler phases
-- use this to decide whether to abort or continue.
hasErrors :: [Diagnostic] -> Bool
hasErrors = any (\d -> _diag_severity d == SevError)


-- Mini sortBy avoiding Data.List import.
sortByImpl :: (a -> a -> Ordering) -> [a] -> [a]
sortByImpl _ [] = []
sortByImpl cmp (p:xs) =
    sortByImpl cmp [x | x <- xs, cmp x p == LT]
    ++ [p]
    ++ sortByImpl cmp [x | x <- xs, cmp x p /= LT]
