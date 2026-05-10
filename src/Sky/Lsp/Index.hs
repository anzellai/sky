{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE BangPatterns #-}
-- | Workspace symbol index for the Sky LSP.
--
-- Built by parsing + canonicalising + type-checking every .sky file in
-- the project (including embedded stdlib Std.* materialised under
-- <projectRoot>/.skycache/stdlib/) and indexing every binding by
-- qualified name. Hover, goto-definition, references and completion
-- consult this index instead of the per-file lookup used previously.
module Sky.Lsp.Index
    ( Index(..)
    , Sym(..)
    , SymKind(..)
    , Import(..)
    , LocalBinding(..)
    , emptyIndex
    , buildIndex
    , lookupQualified
    , lookupAtCursor
    , collectLocalBindings
    , symFromTopLevel
    , externalsForLsp
    , externalsForFile
    ) where

import qualified Data.Map.Strict as Map
import Data.Map.Strict (Map)
import qualified Data.Set as Set
import Data.Maybe (mapMaybe, fromMaybe, listToMaybe)
import Data.List (sortOn, isPrefixOf, isSuffixOf, foldl')
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import qualified System.Directory as Dir
import System.FilePath ((</>), takeDirectory)
import Control.Exception (try, SomeException)

import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as Ty
import qualified Sky.Type.Solve as Solve
import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml
import qualified Data.Aeson as Aeson
import qualified Data.ByteString.Lazy as BL
import qualified Data.Time.Clock as Clock
import qualified Data.Time.Format as Tfmt
import System.IO (IOMode(AppendMode), hPutStrLn, hClose, openFile)


-- ─── Types ─────────────────────────────────────────────────────────────

data SymKind
    = SymFunction       -- top-level value/function
    | SymCtor           -- ADT constructor
    | SymType           -- type alias or union
    | SymLocal          -- let binding
    | SymParam          -- function parameter / lambda param / case binder
    deriving (Show, Eq)


data Sym = Sym
    { symQualName  :: !String        -- "Lib.Db.exec" or "Sky.Core.Error.io"
    , symLocalName :: !String        -- "exec" or "DbError"
    , symModule    :: !String        -- "Lib.Db"
    , symFile      :: !FilePath      -- absolute path
    , symRegion    :: !A.Region      -- declaration region (1-based)
    , symKind      :: !SymKind
    , symTypeSig   :: !(Maybe String)  -- "exec : String -> List Value -> Result Error ()"
    , symDoc       :: !(Maybe String)  -- preceding `--` comment block
    } deriving (Show)


data Import = Import
    { impModule    :: !String          -- "Sky.Core.Error"
    , impAlias     :: !(Maybe String)  -- "Db" if `import Lib.Db as Db`
    , impExposeAll :: !Bool            -- True for `exposing (..)`
    , impExposed   :: !(Set.Set String)-- explicit names from `exposing (a, b)`
    } deriving (Show)


-- | A let/lambda/case binding with its source region and the region of
-- its enclosing scope. Goto-definition on a name uses the smallest
-- enclosing scope that contains the binding.
data LocalBinding = LocalBinding
    { lbName       :: !String
    , lbRegion     :: !A.Region    -- where the binder appears
    , lbScope      :: !A.Region    -- enclosing scope (let body, lambda body, case branch)
    } deriving (Show)


data Index = Index
    { idxByQual    :: !(Map String Sym)
    , idxByLocal   :: !(Map String [Sym])
    , idxByFile    :: !(Map FilePath [Sym])
    , idxModules   :: !(Map String FilePath)
    , idxImports   :: !(Map FilePath [Import])
    , idxLocals    :: !(Map FilePath [LocalBinding])
    , idxLocalTypes :: !(Map FilePath (Map String [Ty.Type]))
    , idxRenaming  :: !(Map FilePath (Map String String))
      -- Audit P2-3: per-file stable TVar → human-letter renaming
      -- so the same solver-level TVar (t108) renders as the same
      -- letter ('a') across every hover in a single file. Prevents
      -- the two-hovers-two-letters confusion class.
      -- Per-file map of bound-name → inferred type, including local
      -- let / lambda / case-branch bindings. Populated from the
      -- solver's local-type accumulator (Solve._locals) so LSP hover
      -- on a let-binding or lambda param surfaces the inferred type
      -- instead of a "(local binding)" placeholder. Name collisions
      -- are resolved innermost-wins by the solver; for hover we also
      -- rely on lookupLocal's smallest-scope selection on top of this.
    , idxFileSrc   :: !(Map FilePath T.Text)
    , idxRoot      :: !(Maybe FilePath)
    , idxModTypes  :: !(Map String (Map String Ty.Type))
      -- Per-module solved types (raw, NOT generalised). Cross-module
      -- externals are computed on-demand from a SUBSET of these (only
      -- the modules the open file actually imports), keyed in
      -- `getExternalsForFile`. This avoids paying the
      -- generalise-the-world cost during buildIndex — critical for
      -- projects with very-large FFI deps (skyshop's Stripe SDK has
      -- ~1800 types whose deeply-nested Annotation generalisation
      -- pegged a CPU at 100% indefinitely on the eager path).
    , idxModCanons :: ![(String, Can.Module)]
      -- Kept for `buildGlobalTypeHomeMap` which needs every module's
      -- aliases + unions to fix unqualified type references during
      -- externals construction.
    , idxModDecls  :: !(Map String (Set.Set String))
      -- Per-module set of top-level declared names. Used by
      -- `getExternalsForFile` to filter out non-declarations the
      -- pass-1 solver may have dragged into _wm_types (constructors,
      -- imported names re-flowing through the module).
    } deriving (Show)


emptyIndex :: Index
emptyIndex = Index
    { idxByQual = Map.empty
    , idxByLocal = Map.empty
    , idxByFile = Map.empty
    , idxModules = Map.empty
    , idxImports = Map.empty
    , idxLocals = Map.empty
    , idxLocalTypes = Map.empty
    , idxRenaming  = Map.empty
    , idxFileSrc = Map.empty
    , idxRoot = Nothing
    , idxModTypes = Map.empty
    , idxModCanons = []
    , idxModDecls = Map.empty
    }


-- ─── Builder ───────────────────────────────────────────────────────────

-- | Build the workspace symbol index from a project root. Looks for
-- sky.toml in projectRoot to find the entry path, then runs the full
-- typecheck pipeline (which materialises stdlib + dep roots), and
-- transforms the per-module canonical+typed result into a flat lookup.
-- | Append a free-form info line to the per-project LSP debug log.
-- Used by both the success and the failure paths in buildIndex so
-- "did indexing run at all?" is observable from the log alone.
logBuildIndexInfo :: FilePath -> String -> IO ()
logBuildIndexInfo projectRoot msg = do
    let logDir = projectRoot </> ".skycache"
    Dir.createDirectoryIfMissing True logDir
    let logPath = logDir </> "lsp-error.log"
    now <- Clock.getCurrentTime
    let stamp = Tfmt.formatTime Tfmt.defaultTimeLocale "%Y-%m-%d %H:%M:%S" now
    h <- openFile logPath AppendMode
    hPutStrLn h ("[" ++ stamp ++ "] " ++ msg)
    hClose h


-- | Append an exception from buildIndex to a per-project debug log.
-- Without this, exceptions in `Compile.typecheckWorkspace` (parse
-- error in some module, solver budget overrun, OOM, etc.) cause the
-- LSP to silently fall through to an empty index — the LSP "works"
-- but every hover returns nil. The log lives in the project's
-- .skycache/ directory so it sits next to other build artefacts.
logBuildIndexError :: FilePath -> SomeException -> IO ()
logBuildIndexError projectRoot e =
    logBuildIndexInfo projectRoot ("buildIndex exception: " ++ show e)


buildIndex :: FilePath -> IO Index
buildIndex projectRoot = do
    let tomlPath = projectRoot </> "sky.toml"
    hasToml <- Dir.doesFileExist tomlPath
    config <- if hasToml
        then Toml.parseSkyToml <$> readFile tomlPath
        else return Toml.defaultConfig
    let entryPath = projectRoot </> Toml._entry config
    hasEntry <- Dir.doesFileExist entryPath
    baseIdx <-
        if not hasEntry
            then return emptyIndex { idxRoot = Just projectRoot }
            else do
                r <- try (Compile.typecheckWorkspace config entryPath)
                        :: IO (Either SomeException Compile.WorkspaceTypecheck)
                case r of
                    Left e   -> do
                        logBuildIndexError projectRoot e
                        return emptyIndex { idxRoot = Just projectRoot }
                    Right wt -> do
                        let !idx = fromTypecheck (Just projectRoot) wt
                        -- Force the spine of every Map-valued field so
                        -- subsequent accesses (during hover / completion /
                        -- diagnostics) never have to chase a lazy thunk.
                        -- Without this, an IO-timed-out externals
                        -- computation could leave a blackhole that hangs
                        -- the next reader. See CLAUDE.md "LSP indexing
                        -- on large FFI surfaces".
                        let !_ = Map.size (idxByQual idx)
                            !_ = Map.size (idxByLocal idx)
                            !_ = Map.size (idxByFile idx)
                            !_ = Map.size (idxModules idx)
                            !_ = Map.size (idxImports idx)
                            !_ = Map.size (idxLocals idx)
                            !_ = Map.size (idxModTypes idx)
                            !_ = Map.size (idxModDecls idx)
                            !_ = length (idxModCanons idx)
                        return idx
    -- Merge in FFI catalogue symbols so hover/definition work for
    -- auto-generated bindings as well. Force strictly to avoid
    -- thunked Maps that blow up in subsequent readers (skyshop's
    -- 76,141-symbol Stripe FFI symptom).
    ffiSyms <- loadFfiSymbols projectRoot
    let !merged = mergeFfi ffiSyms baseIdx
        !_ = Map.size (idxByQual merged)
        !_ = Map.size (idxByLocal merged)
        !_ = Map.size (idxByFile merged)
    return merged


-- | Convert a WorkspaceTypecheck into an Index. Pure — testable.
fromTypecheck :: Maybe FilePath -> Compile.WorkspaceTypecheck -> Index
fromTypecheck root wt =
    let modList = Map.toList (Compile._wt_modules wt)
        (allTops, allLocals, allImports, allFileSrc, modPaths, allLocalTypes) =
            foldr step ([], [], [], [], [], []) modList
        byQual = Map.fromList [ (symQualName s, s) | s <- allTops ]
        byLocal = Map.fromListWith (++)
            [ (symLocalName s, [s]) | s <- allTops ]
        byFile = Map.fromListWith (++)
            [ (symFile s, [s]) | s <- allTops ]
        -- Audit P2-3: precompute a stable TVar → human-letter
        -- renaming per file. Union every displayed type (top-level
        -- + local) so the same solver-level name renders
        -- identically across every hover in that file.
        -- renamingFor — collect every displayed type for a file
        -- (top-level `_wm_types` values + all entries in
        -- `_wm_localTypes`'s lists) and feed them to
        -- Solve.moduleRenaming so each fresh solver TVar gets a
        -- stable letter across every hover.
        wmForPath p =
            [ wm | (_, wm) <- Map.toList (Compile._wt_modules wt)
                 , Compile._wm_path wm == p ]
        renamingFor path =
            let wms = wmForPath path
                topTys = concatMap (Map.elems . Compile._wm_types) wms
                localTys = concatMap (concat . Map.elems . Compile._wm_localTypes) wms
            in Solve.moduleRenaming (topTys ++ localTys)
        renamings = Map.fromList
            [ (p, renamingFor p) | (_, p) <- modPaths ]
        -- Per-module storage for on-demand externals. Building the
        -- full cross-module externals map eagerly at index time hung
        -- the LSP for projects with very-large FFI deps (skyshop's
        -- Stripe SDK has ~1800 types whose deep-Annotation
        -- generalisation took an unbounded amount of CPU). Instead
        -- we keep the raw per-module data here and let
        -- `Server.getExternalsForFile` build a SCOPED externals map
        -- from only the modules the open file imports — typically
        -- 5-15 modules instead of 64. Same correctness, dramatically
        -- less work per request.
        modTypesMap = Map.fromList
            [ (n, Compile._wm_types wm) | (n, wm) <- modList ]
        modCanons = [ (n, Compile._wm_canon wm) | (n, wm) <- modList ]
        modDecls = Map.fromList
            [ (n, Compile.collectDeclNames (Can._decls (Compile._wm_canon wm)))
            | (n, wm) <- modList
            ]
    in Index
        { idxByQual    = byQual
        , idxByLocal   = byLocal
        , idxByFile    = byFile
        , idxModules   = Map.fromList modPaths
        , idxImports   = Map.fromList allImports
        , idxLocals    = Map.fromList allLocals
        , idxLocalTypes = Map.fromList allLocalTypes
        , idxRenaming  = renamings
        , idxFileSrc   = Map.fromList allFileSrc
        , idxRoot      = root
        , idxModTypes  = modTypesMap
        , idxModCanons = modCanons
        , idxModDecls  = modDecls
        }
  where
    step (modName, wmod) (tops, locals, imps, srcs, mods, ltypes) =
        let path = Compile._wm_path wmod
            srcMod = Compile._wm_src wmod
            types = Compile._wm_types wmod
            localTys = Compile._wm_localTypes wmod
            srcText = Compile._wm_source wmod
            tops' = symFromTopLevel modName path srcText types srcMod
            locals' = (path, collectLocalBindings srcMod)
            imps' = (path, fromImports (Src._imports srcMod))
            srcs' = (path, srcText)
            mods' = (modName, path)
            -- Local-binding types come from the solver's separate
            -- _locals accumulator so they don't bleed into SolvedTypes
            -- (which codegen uses and where they'd trigger spurious
            -- `.(T)` assertions on already-typed function params).
            ltypes' = (path, localTys)
        in (tops' ++ tops, locals' : locals, imps' : imps,
            srcs' : srcs, mods' : mods, ltypes' : ltypes)


-- | Extract top-level symbols (functions, type aliases, ADT ctors) for
-- a module, attaching inferred type signatures from the typecheck Map
-- and doc comments harvested from the raw source.
symFromTopLevel :: String -> FilePath -> T.Text -> Map String Ty.Type -> Src.Module -> [Sym]
symFromTopLevel modName path srcText types srcMod =
    let valueSyms =
            [ Sym
                { symQualName = modName ++ "." ++ n
                , symLocalName = n
                , symModule = modName
                , symFile = path
                , symRegion = nReg
                , symKind = SymFunction
                , symTypeSig = typeSigFor n (Src._valueType v)
                , symDoc = docCommentBefore srcText nReg
                }
            | A.At _ v <- Src._values srcMod
            , let A.At nReg n = Src._valueName v
            ]
        aliasSyms =
            [ Sym
                { symQualName = modName ++ "." ++ n
                , symLocalName = n
                , symModule = modName
                , symFile = path
                , symRegion = nReg
                , symKind = SymType
                , symTypeSig = Just ("type alias " ++ n)
                , symDoc = docCommentBefore srcText nReg
                }
            | A.At _ a <- Src._aliases srcMod
            , let A.At nReg n = Src._aliasName a
            ]
        unionSyms = concat
            [ Sym
                { symQualName = modName ++ "." ++ tn
                , symLocalName = tn
                , symModule = modName
                , symFile = path
                , symRegion = tnReg
                , symKind = SymType
                , symTypeSig = Just ("type " ++ tn ++ concatMap (" " ++) vars)
                , symDoc = docCommentBefore srcText tnReg
                } :
              [ Sym
                  { symQualName = modName ++ "." ++ cn
                  , symLocalName = cn
                  , symModule = modName
                  , symFile = path
                  , symRegion = ctorReg
                  , symKind = SymCtor
                  , symTypeSig = Just (ctorSig tn vars args)
                  , symDoc = Nothing
                  }
              | A.At ctorReg (cn, args) <- Src._unionCtors u
              ]
            | A.At _ u <- Src._unions srcMod
            , let A.At tnReg tn = Src._unionName u
                  vars = [v | A.At _ v <- Src._unionVars u]
            ]
    in valueSyms ++ aliasSyms ++ unionSyms
  where
    fmt = Solve.showType

    -- Type-signature lookup with fallback chain:
    --   1. User-written annotation (`fn : Type` line) — render from AST so
    --      we preserve exactly what the user wrote (constructors, aliases).
    --   2. Solver's inferred type if solver succeeded on this binding.
    --   3. Nothing (shows just the name on hover).
    typeSigFor n mAnn =
        case mAnn of
            Just (A.At _ annot) -> Just (renderAnnot annot)
            Nothing             -> fmt <$> Map.lookup n types


-- | Render a source-level TypeAnnotation the way the user wrote it, using
-- parens only where needed. Cross-module types are printed as `Mod.Name`.
renderAnnot :: Src.TypeAnnotation -> String
renderAnnot = go 0
  where
    -- Precedence: 0 = no parens, 1 = arrow-right, 2 = type-application
    go _ Src.TUnit = "()"
    go _ (Src.TVar n) = n
    go p (Src.TType _mod names args) =
        let name = case names of
                [n] -> n
                _   -> intercalateDots names
            inner = unwords (name : map (go 2) args)
        in if p >= 2 && not (null args) then "(" ++ inner ++ ")" else inner
    go p (Src.TTypeQual q name args) =
        let inner = unwords ((q ++ "." ++ name) : map (go 2) args)
        in if p >= 2 && not (null args) then "(" ++ inner ++ ")" else inner
    go p (Src.TLambda from to) =
        let inner = go 2 from ++ " -> " ++ go 1 to
        in if p >= 1 then "(" ++ inner ++ ")" else inner
    go _ (Src.TRecord fields ext) =
        let fs = [ n ++ " : " ++ go 0 ty | (A.At _ n, ty) <- fields ]
            body = case ext of
                Just e  -> e ++ " | " ++ joinCommas fs
                Nothing -> joinCommas fs
        in "{ " ++ body ++ " }"
    go _ (Src.TTuple a b cs) =
        "( " ++ joinCommas (map (go 0) (a : b : cs)) ++ " )"

    joinCommas = foldr1 (\a b -> a ++ ", " ++ b) . ensureNonEmpty
    ensureNonEmpty [] = [""]
    ensureNonEmpty xs = xs

    intercalateDots []     = ""
    intercalateDots [x]    = x
    intercalateDots (x:xs) = x ++ "." ++ intercalateDots xs


-- | Synthesize a ctor's type signature: "Error.io : String -> Error"
-- or "NotAsked : RemoteData a" or "Loaded : a -> RemoteData a".
ctorSig :: String -> [String] -> [Src.TypeAnnotation] -> String
ctorSig typeName vars args =
    let result = if null vars
                   then typeName
                   else typeName ++ " " ++ unwords vars
        arrow = foldr (\a rhs -> renderAnnot a ++ " -> " ++ rhs) result args
    in arrow


-- | Convert AST imports into the index's lighter Import record.
fromImports :: [Src.Import] -> [Import]
fromImports = map go
  where
    go imp =
        let A.At _ segs = Src._importName imp
            A.At _ exps = Src._importExposing imp
            (allFlag, names) = case exps of
                Src.ExposingAll       -> (True, [])
                Src.ExposingList xs   -> (False, mapMaybe exposedName xs)
        in Import
            { impModule = joinDots segs
            , impAlias = Src._importAlias imp
            , impExposeAll = allFlag
            , impExposed = Set.fromList names
            }
    exposedName (A.At _ e) = case e of
        Src.ExposedValue n  -> Just n
        Src.ExposedType n _ -> Just n
        _                   -> Nothing
    joinDots [] = ""
    joinDots [x] = x
    joinDots (x:xs) = x ++ "." ++ joinDots xs


-- ─── Local binding extraction (Stage 4) ────────────────────────────────

-- | Walk the module collecting every let-binding, lambda parameter and
-- case-pattern binder along with its enclosing scope region. Used by
-- goto-definition for names that aren't top-level.
collectLocalBindings :: Src.Module -> [LocalBinding]
collectLocalBindings srcMod = concatMap valueLocals (Src._values srcMod)
  where
    -- valReg is the value's outer region — but the parser sets that
    -- to JUST the name's region (e.g. line 10:1-10:10 for
    -- `stringify`), not the body. Using valReg as the scope for
    -- parameter binders means lookupLocal's regionContains check
    -- fails for any cursor inside the body. Use the body's region
    -- as the scope so parameters are visible exactly where they're
    -- usable. Same root cause as the field-hover regionContains
    -- bug fixed in Server.hs:findParamType.
    valueLocals (A.At _ v) =
        let body = Src._valueBody v
            A.At bodyReg _ = body
            paramBinders = concatMap (patBinders bodyReg) (Src._valuePatterns v)
        in paramBinders ++ exprLocals bodyReg body

    -- Every name introduced by a pattern, with the given enclosing scope.
    patBinders :: A.Region -> Src.Pattern -> [LocalBinding]
    patBinders scope (A.At reg p) = case p of
        Src.PVar n           -> [LocalBinding n reg scope]
        Src.PAlias inner (A.At nr n) ->
            LocalBinding n nr scope : patBinders scope inner
        Src.PCtor _ _ xs     -> concatMap (patBinders scope) xs
        Src.PCtorQual _ _ xs -> concatMap (patBinders scope) xs
        Src.PCons h t        -> patBinders scope h ++ patBinders scope t
        Src.PList xs         -> concatMap (patBinders scope) xs
        Src.PTuple a b cs    -> patBinders scope a ++ patBinders scope b
                              ++ concatMap (patBinders scope) cs
        Src.PRecord fields   -> [LocalBinding n fr scope | A.At fr n <- fields]
        _                    -> []

    exprLocals :: A.Region -> Src.Expr -> [LocalBinding]
    exprLocals scope (A.At eReg e) = case e of
        Src.Lambda ps body ->
            concatMap (patBinders eReg) ps ++ exprLocals eReg body
        Src.Call f xs ->
            exprLocals scope f ++ concatMap (exprLocals scope) xs
        Src.Binops pairs end ->
            concatMap (\(x, _) -> exprLocals scope x) pairs ++ exprLocals scope end
        Src.If arms els ->
            concatMap (\(c, b) -> exprLocals scope c ++ exprLocals scope b) arms
            ++ exprLocals scope els
        Src.Let defs body ->
            concatMap (defLocals eReg) defs ++ exprLocals eReg body
        Src.Case sub arms ->
            exprLocals scope sub ++ concatMap (caseArm scope) arms
        Src.Access t _   -> exprLocals scope t
        Src.Update _ fs  -> concatMap (exprLocals scope . snd) fs
        Src.Record fs    -> concatMap (exprLocals scope . snd) fs
        Src.Tuple a b cs ->
            exprLocals scope a ++ exprLocals scope b
            ++ concatMap (exprLocals scope) cs
        Src.List xs      -> concatMap (exprLocals scope) xs
        Src.Negate inner -> exprLocals scope inner
        _ -> []

    -- Each case arm is its own scope (the arm body), so binders from
    -- the pattern are visible only there.
    caseArm scope (pat, body) =
        let bodyReg = case body of A.At r _ -> r
        in patBinders bodyReg pat ++ exprLocals bodyReg body

    defLocals scope (A.At _ d) = case d of
        Src.Define (A.At nr n) ps body _ ->
            LocalBinding n nr scope :
            concatMap (patBinders scope) ps ++ exprLocals scope body
        Src.Destruct pat body ->
            patBinders scope pat ++ exprLocals scope body


-- ─── Doc-comment extraction ────────────────────────────────────────────

-- | Look at the lines of the raw source immediately preceding the given
-- region's start line. Collect contiguous `-- ...` lines (no blank gap
-- in between) and return them joined with newlines, or Nothing if no
-- comment block precedes the declaration.
docCommentBefore :: T.Text -> A.Region -> Maybe String
docCommentBefore src (A.Region s _) =
    let allLines = T.lines src
        startLine0 = max 0 (A._line s - 2)  -- 0-based index of line above
        before = take (startLine0 + 1) allLines
        commentLines = reverse (takeWhile isCommentLine (reverse before))
    in if null commentLines
       then Nothing
       else Just (unlines (map (T.unpack . T.dropWhile isCommentChar . T.stripStart) commentLines))
  where
    isCommentLine ln =
        let stripped = T.stripStart ln
        in T.isPrefixOf "--" stripped && not (T.isPrefixOf "----" stripped)
    isCommentChar c = c == '-'


-- ─── Lookup ────────────────────────────────────────────────────────────

-- | Look up a fully-qualified symbol like "Sky.Core.Error.io".
lookupQualified :: Index -> String -> Maybe Sym
lookupQualified idx q = Map.lookup q (idxByQual idx)


-- | Build a SCOPED cross-module externals map for the open file.
-- Only includes externals from modules the open file actually
-- imports AND keeps only modules whose declaration count is below
-- the LSP-externals cap (default 400 — generous for stdlib /
-- Std.Ui, while still cutting off pathological FFI re-exports).
-- The cap exists because `buildCrossModuleExternalsWithMods` calls
-- `generaliseToAnnotation` on every type, and deeply-nested FFI
-- types (Stripe SDK, Firebase) generalise into structures that take
-- minutes of CPU to force. A skipped module just means the LSP
-- can't surface cross-module type errors involving it; symbols
-- still resolve via the FFI catalogue + workspace symbol index.
--
-- `imports` should be the qualified module names extracted from the
-- open file's import list (e.g. ["Sky.Core.Prelude",
-- "Github.Com.Stripe.StripeGo.V84", ...]).
externalsForFile
    :: [String]
    -> Index
    -> Map (String, String) Ty.Annotation
externalsForFile imports idx =
    let needed = Set.fromList imports
        cap = 400 :: Int
        depSolvedAll = Map.toList (idxModTypes idx)
        smallEnough name =
            case Map.lookup name (idxModDecls idx) of
                Just names -> Set.size names <= cap
                Nothing    -> True
        keep n = Set.member n needed && smallEnough n
        validDeps =
            [ (n, m) | (n, m) <- idxModCanons idx, keep n ]
        depSolved =
            [ (n, ts) | (n, ts) <- depSolvedAll, keep n ]
        rawExternals = Compile.buildCrossModuleExternalsWithMods
                        validDeps depSolved
        decls = idxModDecls idx
    in Map.filterWithKey
        (\(m, n) _ -> case Map.lookup m decls of
            Just names -> n `Set.member` names
            Nothing    -> False)
        rawExternals


-- | Backwards-compat alias: callers that pre-date the per-file
-- scoping pass `[]` (no imports) and get an empty map. The LSP's
-- production path uses externalsForFile directly.
externalsForLsp :: Index -> Map (String, String) Ty.Annotation
externalsForLsp _ = Map.empty


-- | Look up the symbol referenced at (file, line, col) for hover/jump.
-- Resolves:
--   * Module.name → uses imports' alias map → workspace index
--   * unqualified name → searches imports' (..) exposing for the source module
--   * unqualified that's a local binding in the file's scope tree → returns local
--   * fallback: any same-named top-level in the workspace
lookupAtCursor :: Index -> FilePath -> Int -> Int -> String -> Maybe Sym
lookupAtCursor idx file line col name
    | '.' `elem` name =
        let (modOrAlias, '.':local) = break (== '.') name
            -- Resolve alias → real module path via imports for THIS file
            imports = fromMaybe [] (Map.lookup file (idxImports idx))
            realMod = aliasToModule imports modOrAlias
            qual = realMod ++ "." ++ local
        in Map.lookup qual (idxByQual idx)
    | otherwise =
        -- 1. Local binding at cursor scope?
        case lookupLocal idx file line col name of
            Just sym -> Just sym
            Nothing ->
                -- 2. Search imports `exposing (..)` and explicit
                let imports = fromMaybe [] (Map.lookup file (idxImports idx))
                    candidates =
                        [ q
                        | imp <- imports
                        , impExposeAll imp || Set.member name (impExposed imp)
                        , let q = impModule imp ++ "." ++ name
                        ]
                in case mapMaybe (`Map.lookup` idxByQual idx) candidates of
                    (s:_) -> Just s
                    []    ->
                        -- 3. Same-file top-level
                        let here = fromMaybe [] (Map.lookup file (idxByFile idx))
                        in listToMaybe [ s | s <- here, symLocalName s == name ]


aliasToModule :: [Import] -> String -> String
aliasToModule imports tag =
    case [impModule i | i <- imports, importTag i == tag] of
        (m:_) -> m
        []    -> tag   -- not aliased, treat as the module name itself
  where
    importTag i = case impAlias i of
        Just a  -> a
        Nothing -> lastSegment (impModule i)
    lastSegment s = case reverse (splitDots s) of
        (x:_) -> x
        _     -> s
    splitDots s = case break (== '.') s of
        (h, '.':t) -> h : splitDots t
        (h, _)     -> [h]


-- ─── FFI catalogue loader ──────────────────────────────────────────────

-- | Scan <projectRoot>/ffi/*.kernel.json + matching .skyi catalogue
-- comments. Each binding becomes a Sym with the type signature from
-- the .skyi file, file = the .skyi path, region = the line containing
-- the binding. Hover/definition thus work for auto-generated FFI
-- bindings (`Uuid.newString`, `Stripe.newCheckoutSessionParams`, etc.).
loadFfiSymbols :: FilePath -> IO [Sym]
loadFfiSymbols projectRoot = do
    let ffiDir = projectRoot </> ".skycache" </> "ffi"
    exists <- Dir.doesDirectoryExist ffiDir
    if not exists then return []
    else do
        files <- Dir.listDirectory ffiDir
        let jsonFiles = filter (".kernel.json" `isSuffixOf`) files
        concat <$> mapM (loadOne ffiDir) jsonFiles
  where
    loadOne ffiDir jsonName = do
        let jsonPath = ffiDir </> jsonName
            base = take (length jsonName - length (".kernel.json" :: String)) jsonName
            skyiPath = ffiDir </> (base ++ ".skyi")
        jbs <- BL.readFile jsonPath
        hasSkyi <- Dir.doesFileExist skyiPath
        skyiText <- if hasSkyi then TIO.readFile skyiPath else return T.empty
        -- Pre-index the .skyi by capitalised-name in a single linear
        -- pass over the file, instead of scanning the whole file
        -- per-symbol. For Stripe SDK (~8000 symbols × ~500K lines)
        -- this drops the cost of `findSkyiLine` from
        -- O(n×m) ≈ 4 × 10⁹ ops to O(n+m) ≈ 5 × 10⁵.
        let !skyiIndex = buildSkyiNameIndex skyiText
        case Aeson.eitherDecode jbs of
            Left _ -> return []
            Right reg ->
                return [ mkFfiSym skyiPath skyiIndex (ffiModule reg) fn
                       | fn <- ffiFunctions reg
                       ]

    -- Index .skyi lines by capitalised symbol name (the convention
    -- the catalogue uses: `-- [effect] FnName : Type`). Lines
    -- without that shape are skipped.
    buildSkyiNameIndex :: T.Text -> Map String (Int, T.Text)
    buildSkyiNameIndex content =
        let numbered = zip [1..] (T.lines content)
            entries = mapMaybe extractEntry numbered
        in Map.fromListWith (\_ kept -> kept) entries
      where
        extractEntry (lineNo, ln) =
            -- We're looking for `-- [effect] FnName : Type ...`
            -- (catalogue) OR fallback: `[effect] FnName : Type`.
            let stripped = T.stripStart ln
                afterBracket = T.dropWhile (/= ']') stripped
                rest = T.dropWhile (`elem` ("] " :: String)) afterBracket
                (nameTok, afterName) = T.break (== ' ') rest
                nameStr = T.unpack nameTok
            in if T.null nameTok
                || ":" `T.isPrefixOf` T.stripStart afterName
                then Nothing
                else if " : " `T.isInfixOf` afterName
                    then Just (nameStr, (lineNo, ln))
                    else Nothing

    mkFfiSym skyiPath skyiIndex modName fn =
        let nm = funcName fn
            (sigLine, sigText) = findSkyiLine skyiIndex nm
        in Sym
            { symQualName = modName ++ "." ++ nm
            , symLocalName = nm
            , symModule = modName
            , symFile = skyiPath
            , symRegion = A.Region
                (A.Position sigLine 1)
                (A.Position sigLine (max 1 (length (T.unpack sigText))))
            , symKind = SymFunction
            , symTypeSig = Just (extractTypeSig nm sigText)
            , symDoc = Just ("FFI binding (" ++ show (funcArity fn) ++
                             "-arg) — generated from " ++ modName)
            }

    -- O(log n) lookup against the pre-built index. Falls back to
    -- (0, "") when the name is missing — the LSP renders that as a
    -- bare `nm : (FFI binding)` placeholder, same as before.
    findSkyiLine skyiIndex nm =
        let cap = capitalise nm
        in fromMaybe (0, T.empty) (Map.lookup cap skyiIndex)

    -- Parse "-- [effect] FnName : Type Signature   -- runtime wrap: Task Error"
    -- into "name : Type Signature" (dropping the `-- runtime wrap` suffix).
    extractTypeSig nm ln
        | T.null ln = nm ++ " : (FFI binding)"
        | otherwise =
            let raw = T.unpack ln
                afterBracket = dropWhile (/= ']') raw  -- "] FnName : Type ..."
                afterSpace = dropWhile (`elem` ("] " :: String)) afterBracket
                afterColon = dropWhile (/= ':') afterSpace
                sigPart = dropWhile (== ':') afterColon
                trimmed = takeUntil "   --" sigPart
            in nm ++ " : " ++ dropWhile (== ' ') trimmed

    takeUntil needle s = case breakOn needle s of
        (a, _) -> a
    breakOn needle s
        | take (length needle) s == needle = ("", s)
        | null s = (s, s)
        | otherwise = let (a, b) = breakOn needle (tail s)
                      in (head s : a, b)

    capitalise [] = []
    capitalise (c:cs) = toUpper c : cs
    toUpper c | c >= 'a' && c <= 'z' = toEnum (fromEnum c - 32) | otherwise = c


-- Minimal JSON schema matching ffi/<pkg>.kernel.json as emitted by
-- Sky.Build.FfiGen — only the fields we need for indexing.
data FfiRegJson = FfiRegJson
    { ffiModule    :: String
    , ffiFunctions :: [FfiFuncJson]
    }

data FfiFuncJson = FfiFuncJson
    { funcName  :: String
    , funcArity :: Int
    }

instance Aeson.FromJSON FfiRegJson where
    parseJSON = Aeson.withObject "FfiRegJson" $ \o ->
        FfiRegJson <$> o Aeson..: "moduleName"
                   <*> o Aeson..: "functions"

instance Aeson.FromJSON FfiFuncJson where
    parseJSON = Aeson.withObject "FfiFuncJson" $ \o ->
        FfiFuncJson <$> o Aeson..: "name"
                    <*> o Aeson..: "arity"


-- | Merge FFI symbols into an existing Index. Project-local symbols
-- with the same qualified name win (so a user override shadows FFI).
mergeFfi :: [Sym] -> Index -> Index
mergeFfi syms idx =
    -- Strict foldl' avoids a deep thunk chain on big FFI catalogues
    -- (Stripe SDK ships ~8000 symbols). The original `foldr`-built
    -- Map looked fine to read at WHNF but forced into a 8000-deep
    -- thunk evaluation the first time anything traversed the result
    -- (e.g. atomicModifyIORef' storing the index). On skyshop this
    -- pegged the LSP at 100% CPU for 30+ seconds per hover request.
    let !newByQual  = foldl' (\m s -> Map.insertWith (\_ old -> old) (symQualName s) s m)
                             (idxByQual idx) syms
        !newByLocal = foldl' (\m s -> Map.insertWith (++) (symLocalName s) [s] m)
                             (idxByLocal idx) syms
        !newByFile  = foldl' (\m s -> Map.insertWith (++) (symFile s) [s] m)
                             (idxByFile idx) syms
    in idx
        { idxByQual = newByQual
        , idxByLocal = newByLocal
        , idxByFile = newByFile
        }


-- | Find the smallest scope containing (line, col) and check its bindings.
lookupLocal :: Index -> FilePath -> Int -> Int -> String -> Maybe Sym
lookupLocal idx file line col name =
    let bs = fromMaybe [] (Map.lookup file (idxLocals idx))
        matching =
            [ b | b <- bs, lbName b == name
                , regionContains (lbScope b) line col ]
        -- Prefer innermost scope (smallest)
        best = case sortOn (regionWidth . lbScope) matching of
            (b:_) -> Just b
            []    -> Nothing
    in fmap (\b ->
        let localTypes = fromMaybe Map.empty (Map.lookup file (idxLocalTypes idx))
            inferredTys = fromMaybe [] (Map.lookup (lbName b) localTypes)
            rename = fromMaybe Map.empty (Map.lookup file (idxRenaming idx))
            -- Audit P2-2: localTypes holds ALL captures for this name
            -- (innermost-first from the solver side). LSP's `best`
            -- is also the innermost matching LocalBinding by scope.
            -- For same-function shadowing, both agree on "index 0"
            -- for the binding the hover resolved to.
            -- Audit P2-3: render through the module's cached
            -- renaming so TVar letters stay stable across hovers.
            sig = case inferredTys of
                (ty:_) -> Just (lbName b ++ " : " ++ Solve.showTypeWith rename ty)
                []     -> Just (lbName b ++ " : (local binding)")
        in Sym
            { symQualName = "(local) " ++ lbName b
            , symLocalName = lbName b
            , symModule = ""
            , symFile = file
            , symRegion = lbRegion b
            , symKind = SymLocal
            , symTypeSig = sig
            , symDoc = Nothing
            }) best
  where
    regionContains (A.Region rs re) ln cl =
        let afterStart = (A._line rs < ln) || (A._line rs == ln && A._col rs <= cl)
            beforeEnd  = (A._line re > ln) || (A._line re == ln && A._col re >= cl)
        in afterStart && beforeEnd
    regionWidth (A.Region rs re) =
        (A._line re - A._line rs) * 1000 + (A._col re - A._col rs)
