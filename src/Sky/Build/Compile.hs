-- | Single-module compilation pipeline.
-- Source → Parse → Canonicalise → (TODO: Type Check) → Generate Go
module Sky.Build.Compile where

import qualified Control.Concurrent.Async as Async
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import Data.IORef
import Data.Maybe (isJust, isNothing, fromMaybe)
import Data.List (isPrefixOf)
import qualified Data.Char as Char
import qualified Data.List as List
import qualified System.Directory
import qualified System.FilePath
import qualified System.Process
import qualified System.Exit
import Control.Monad (when, unless, forM, forM_)
import qualified Control.Exception as E
import Control.Exception (evaluate)
import qualified System.IO
import qualified System.Environment
import Data.List (isSuffixOf)
import System.Directory (createDirectoryIfMissing, doesDirectoryExist, doesFileExist, copyFile, listDirectory, removeFile)
import System.IO (hFlush, stdout, readFile', stderr, hPutStrLn)
import System.IO.Unsafe (unsafePerformIO)
import System.FilePath (takeDirectory, takeExtension, (</>))

import qualified Data.ByteString as BS
import Sky.Build.EmbeddedRuntime (embeddedRuntime, embeddedSkyStdlib)
import qualified Sky.Build.TailCallOpt as TCO
import qualified Sky.Build.LowerCtx as LC

import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Reporting.Diagnostic as Diag
import qualified Sky.Reporting.Render as Render
import qualified Sky.Build.Validator as Validator
import qualified Sky.Build.Monomorphise as Mono
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Type.Exhaustiveness as Exhaust
import qualified Sky.Generate.Go.Ir as GoIr
import qualified Sky.Generate.Go.Builder as GoBuilder
import qualified Sky.Generate.Go.Kernel as Kernel
import qualified Sky.Sky.Toml as Toml
import qualified Sky.Type.Constrain.Module as Constrain
import qualified Sky.Type.Constrain.Expression as ConstrainExpr
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as T
import qualified Sky.Generate.Go.Type as GoType
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Build.ModuleGraph as Graph
import qualified Sky.Build.Dce as Dce
import qualified Sky.Build.FfiRegistry as FfiReg
import qualified Sky.Build.FfiTypeResolve as FfiTy
import qualified Sky.Build.SkyDeps as SkyDeps
import qualified Sky.Canonicalise.Environment as Env
import qualified System.Environment
import qualified Debug.Trace


-- | Global codegen environment (set once per compilation, read during codegen)
{-# NOINLINE globalCgEnv #-}
globalCgEnv :: IORef Rec.CodegenEnv
globalCgEnv = unsafePerformIO $ newIORef (Rec.CodegenEnv Solve.emptySolvedTypes Map.empty Map.empty Set.empty Set.empty Set.empty Set.empty Map.empty Map.empty Map.empty Map.empty Map.empty Map.empty Map.empty)


-- | v0.13 A2 follow-up: dedicated, eagerly-populated union-name
-- registry. Mirrors `_cg_unionNames` inside `globalCgEnv`, but the
-- separate IORef lets `typeStrWithAliasesReg` (called during
-- dep-function-sig emission, possibly INSIDE `modifyIORef
-- globalCgEnv` callbacks) read union names without forcing a lazy
-- thunk that would re-enter the in-flight cgEnv update and
-- black-hole (<<loop>>). Written eagerly via `writeIORef` at the
-- same moments cgEnv's `_cg_unionNames` is updated.
{-# NOINLINE globalUnionNames #-}
globalUnionNames :: IORef (Set.Set String)
globalUnionNames = unsafePerformIO $ newIORef Set.empty


-- | v0.13 Phase A5: entry-module source path, set once per
-- compilation, read at call-site codegen to key into
-- `_cg_callSiteInstances` by (path, line, col).  Set in
-- `continueCompile` before generateGoMulti runs.
{-# NOINLINE globalEntryPath #-}
globalEntryPath :: IORef FilePath
globalEntryPath = unsafePerformIO $ newIORef ""


{-# NOINLINE entryPathRef #-}
entryPathRef :: FilePath
entryPathRef = unsafePerformIO $ readIORef globalEntryPath


-- | v0.13 Phase A5++: per-module source path that codegen sets
-- before lowering each dep module so `coerceCallArgsAt`'s CSI
-- lookup uses the right (file, line, col) triple.  The entry
-- module always uses `globalEntryPath`; each dep's lowering
-- swaps this to the dep's `_mi_path`.  Without this, the CSI
-- map collisions across files surface as bare-`T1` leaks in the
-- emitted Go.
{-# NOINLINE globalSourceFile #-}
globalSourceFile :: IORef FilePath
globalSourceFile = unsafePerformIO $ newIORef ""


-- | v0.13 Phase A4: the transitively-reachable instance set from
-- `main`.  Populated by `continueCompile` after the solver runs;
-- consumed by `generateGoMulti` to emit per-instance specialised
-- Go functions alongside (and eventually replacing) the generic
-- versions.
{-# NOINLINE globalReachableSet #-}
globalReachableSet :: IORef Mono.ReachableSet
globalReachableSet = unsafePerformIO $ newIORef Set.empty


-- | v0.13 F: whole-program Sky-side reachable set.  Populated by
-- continueCompile after the canon fixpoint runs (the same hook where
-- `globalReachableSet` gets the mono-instance reachable set).  Read by
-- `loadAndSeedFfiRegistry` to prune unused FFI sigs (the Stripe-SDK
-- win) and by `generateDeclsForDep` to skip emission of unreachable
-- dep-module decls.  Set `SKY_DCE=0` to disable pruning (escape
-- hatch — value stays Set.empty so every reachable check returns True
-- via the empty-set fallback below).
{-# NOINLINE globalReachableProgram #-}
globalReachableProgram :: IORef (Set.Set Dce.Ref)
globalReachableProgram = unsafePerformIO $ newIORef Set.empty


-- | v0.13 F: dce-disabled flag.  Read once at compile start from
-- `SKY_DCE`.  When True, all reachability checks return True so no
-- pruning fires — debug aid.
{-# NOINLINE globalDceDisabled #-}
globalDceDisabled :: IORef Bool
globalDceDisabled = unsafePerformIO $ newIORef False


-- | v0.13 E: anon-record shape registry. Populated by
-- `synthAnonRecordName` whenever the renderer hits an unmatched
-- `T.TRecord` shape and synthesises a `Anon_R_<hash>` name.
-- Consumed by `generateAnonRecordDecls` (wired into `generateGoMulti`
-- before user decls) which emits one `type Anon_R_<hash> = struct
-- { ... }` per registered shape, plus a `gob.RegisterName` so the
-- session-store round-tripping works.
--
-- Pre-E (`sanitiseTypedDeep` rewriting `Anon_R_*` → `any`) was a
-- contract-violation cover-up: any function signature that
-- mentioned an anon-record collapsed to `any`-typed. Now the
-- struct decls actually exist, so the typed Go names round-trip
-- without the workaround.
{-# NOINLINE globalAnonRecords #-}
globalAnonRecords :: IORef (Map.Map String (Map.Map String T.FieldType))
globalAnonRecords = unsafePerformIO $ newIORef Map.empty


-- | v0.15 Stage B — per-region HM type map for the current module.
--
-- v0.15.5 PR 3 — retired in favour of `scopeStateRef`'s
-- `_lc_regionTypes` field.  The map is now written into the
-- consolidated `scopeStateRef` at codegen entry alongside the
-- lambda-types / lambda-Go-string state, and read via
-- `LC.lookupRegionType` — same `unsafePerformIO`-wrapped IORef
-- read, one fewer IORef on the boundary.  See `IORefBoundarySpec`
-- for the gate that forbids the old name (string match) from
-- coming back.


-- | v0.15 Stage E — merged alias-declaration map (entry + deps),
-- populated EARLY in continueCompile (after canonicalisation, before
-- codegen).  Separate from `globalCgEnv._cg_aliases` because the
-- parametric-alias generic-args renderer is called during env
-- CONSTRUCTION; reading the env's own lazy thunk at that moment
-- produces a `<<loop>>` black-hole.  This IORef is written before
-- any sig emission, so reads from inside emission are always safe.
{-# NOINLINE globalAllAliases #-}
globalAllAliases :: IORef (Map.Map String Can.Alias)
globalAllAliases = unsafePerformIO $ newIORef Map.empty


-- | v0.15 Stage E — early-populated field-index registry.  Same
-- shape as `globalCgEnv._cg_fieldIndex` but readable from
-- `tvarsInEmitted` without triggering env build (avoiding the
-- `<<loop>>` black-hole).  Populated alongside `globalAllAliases`.
{-# NOINLINE globalAllFieldIdx #-}
globalAllFieldIdx :: IORef Rec.RecordRegistry
globalAllFieldIdx = unsafePerformIO $ newIORef Map.empty


-- | v0.15 Stage E — synthetic TVar naming.  Same (aliasName,
-- varName) must produce the SAME synthetic name in every renderer
-- so they all collapse to one Go T-var.  Encodes both pieces.
syntheticAliasVar :: String -> String -> String
syntheticAliasVar aliasName varName =
    "_skysynth_" ++ aliasName ++ "_" ++ varName


-- | v0.15 Stage E — alias lookup with module-prefix fallback.
lookupAliasDecl :: String -> Maybe Can.Alias
lookupAliasDecl aliasName = unsafePerformIO $ do
    aliasMap <- readIORef globalAllAliases
    return $ case Map.lookup aliasName aliasMap of
        Just a -> Just a
        Nothing ->
            let suffix = reverse (takeWhile (/= '_') (reverse aliasName))
            in Map.lookup suffix aliasMap


-- | v0.15 Stage E — compute the type-arg list for a parametric alias
-- whose declared shape was captured by a row-poly record's field
-- set.  Each alias var resolves via structural extraction OR a
-- synthetic TVar (for unbinable / subset-record cases).
aliasGenericArgs
    :: String
    -> Map.Map String T.FieldType
    -> Maybe (String, [T.Type])
aliasGenericArgs aliasName actualFields =
    case lookupAliasDecl aliasName of
        Just (Can.Alias skyVars body) | not (null skyVars) ->
            let actualRec = T.TRecord actualFields Nothing
                bindings = extractAliasBindings skyVars body actualRec
                resolve v = case Map.lookup v bindings of
                    Just t  -> t
                    Nothing -> T.TVar (syntheticAliasVar aliasName v)
            in Just (aliasName, map resolve skyVars)
        _ -> Nothing


-- | v0.15 Stage B — per-region HM type lookup.
--
-- v0.15.x P37b: `Compile.lookupRegionType` was deleted in favour of
-- pure `Solve.lookupSolvedRegion r solvedTypes` reads against the
-- per-region map that `Solve.SolvedTypes` carries since P37a.  The
-- IORef-backed shape (`unsafePerformIO (readIORef scopeStateRef)`
-- + `LC.lookupRegionType`) is gone; every caller now reads region
-- types from the explicit `SolvedTypes` value flowing through the
-- lowerer.  See `letBindingType` and `inferExprType`'s `Can.Lambda`
-- arm for the consumers.


-- | v0.13 Phase A4: per-callee generalised annotation, used to
-- derive σ for each reachable instance.  Same map shape as
-- `buildCrossModuleExternalsWithMods` but keyed by full
-- Sky-qualified name (`"Sky.Core.List.foldl"`).
{-# NOINLINE globalAnnotMap #-}
globalAnnotMap :: IORef (Map.Map String T.Annotation)
globalAnnotMap = unsafePerformIO $ newIORef Map.empty


-- | v0.14.x Stage 4: Ffi.kernel alias registry.  Populated from
-- canonicalised Sky-source modules whose bindings have the
-- declaration shape `name = Ffi.kernel "KernelName"`.  Maps the
-- Sky-source `(home, name)` to the kernel `(kernelMod, kernelName)`
-- pair so codegen can rewrite call sites to the direct
-- `Can.VarKernel` dispatch — preserving the typed-codegen path
-- the kernel-direct route already enjoys.
--
-- See `docs/V1_TYPED_CODEGEN_FINISH.md` Stage 4 for the rationale.
{-# NOINLINE globalKernelAlias #-}
globalKernelAlias :: IORef (Map.Map (ModuleName.Canonical, String) (String, String))
globalKernelAlias = unsafePerformIO $ newIORef Map.empty


-- | v0.13 Phase A4: the set of specialised function names that
-- `generateGoMulti` actually emitted as separate Go decls.  Used
-- by `instanceMangledName` to gate call-site mangled-name
-- rewriting — only use the mangled name when its spec exists,
-- else fall back to the generic version.
{-# NOINLINE globalEmittedSpecs #-}
globalEmittedSpecs :: IORef (Set.Set String)
globalEmittedSpecs = unsafePerformIO $ newIORef Set.empty


-- | v0.13 Phase A4: per-callee annotation Forall quantifier-name list,
-- captured from the actual `CallInstance.quantifiers` the solver
-- recorded.  Used for spec emission's σ build instead of re-deriving
-- via `generaliseToAnnotation` (which can produce DIFFERENT Forall
-- ordering across pass 1 / pass 2 because the solver's internal TVar
-- names differ between passes).
{-# NOINLINE globalCsiByCallee #-}
globalCsiByCallee :: IORef (Map.Map String [String])
globalCsiByCallee = unsafePerformIO $ newIORef Map.empty

-- | v0.15.5 PR 2 — consolidated lowering-scope state IORef.
--
-- Replaces the v0.13/v0.15 pair of IORefs (the lambda-type +
-- lambda-Go-string maps, both retired in this PR) with a single
-- `LC.LowerCtx`-shaped snapshot.  The two old IORefs held
-- disjoint maps that were
-- ALWAYS pushed/popped together at the same scope seams; combining
-- them into one ctx-shaped IORef:
--
--   1. Cuts the IORef boundary by 2 names (`IORefBoundarySpec`).
--   2. Aligns the per-scope state with the `LowerCtx` record PR 1
--      introduced — PRs 3-6 migrate the consumers to read from a
--      threaded `ctx` parameter directly, at which point this
--      IORef itself disappears.
--   3. Lets `lookupLambdaType` / `lookupLambdaGoStr` / scope
--      helpers route through `LC.*` lookup helpers, so the
--      readers' shape matches the eventual threaded-ctx shape.
--
-- The IORef holds an `LC.LowerCtx` whose only meaningful fields in
-- this PR are `_lc_lambdaTypes` and `_lc_lambdaGoStr` — every
-- other field is reset to the empty / module-stub when this ref
-- is initialised, since the per-scope state never reads them.
{-# NOINLINE scopeStateRef #-}
scopeStateRef :: IORef LC.LowerCtx
scopeStateRef = unsafePerformIO $ newIORef
    (LC.emptyLowerCtx (ModuleName.Canonical "Sky.Build.Compile.scopeStateRef"))


-- | Snapshot + restore the lambda-types map around an action.  Used at
-- typed-lambda emission boundaries to nest scopes correctly when
-- lambdas are nested.  The `a` argument is evaluated to WHNF inside
-- the bracket so the push/pop ordering matches the lambda's lexical
-- scope.
withLambdaTypes :: Map.Map String T.Type -> a -> a
withLambdaTypes additions x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withLambdaTypes additions prev)
    return x


-- | GoExpr-specific scope: pushes bindings, renders the GoExpr to a
-- String (forcing full evaluation), pops back to the previous
-- bindings, then wraps the rendered String as a `GoRaw`.  This
-- gives PROPER push/pop scoping (impossible with `withLambdaTypes`
-- because GoExpr is lazy — pop in pure code races with deferred
-- forcing).  Downstream consumers (rendering, specialiseFuncDecl)
-- handle `GoRaw` correctly.
--
-- Use this at points where the bindings MUST NOT leak into sibling
-- scopes — e.g. record field-access optimisation, let-binding
-- typed registration, function-param registration.
withScopedLambdaTypes :: Map.Map String T.Type -> GoIr.GoExpr -> GoIr.GoExpr
withScopedLambdaTypes additions x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withLambdaTypes additions prev)
    -- Force the GoExpr to String form which fully evaluates the tree.
    -- After this, the bindings are no longer needed (the rendered
    -- String is final).  Restore the previous bindings so sibling
    -- scopes see their own state.
    let rendered = GoBuilder.renderExpr x
        -- Force evaluation by demanding length (rendered is a String).
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)


-- | Read the current lambda-types map (for use in pure codegen).
-- Takes a unit arg + NOINLINE so each call site forces a fresh read
-- of the IORef.  Without the arg, GHC may cache the value across
-- call sites and the bindings registered by `withLambdaTypes` after
-- the first call stay invisible.
-- | Lookup a variable in the current lambda-types map.  Inlined as
-- `unsafePerformIO (readIORef ... >>= return . Map.lookup k)` so GHC
-- doesn't memoise the map across calls.  A plain `Map.lookup k
-- getLambdaTypes` would share the IORef-read across all call sites
-- with the same `k`, defeating the per-call refresh needed for the
-- lambda-scope binding to reach access-codegen sites later in the
-- pipeline.
lookupLambdaType :: String -> Maybe T.Type
lookupLambdaType k = unsafePerformIO $ do
    ctx <- readIORef scopeStateRef
    return (LC.lookupLambdaType ctx k)


-- | v0.13 Stage 1 (task #189) — Go-type-string registry for typed
-- function parameters in scope. When a dep function emits as
-- `func [T1, T2 any](fn func(T1) T2, list []T1) …`, the `fn` param
-- is registered as a Go-type string. Lets `goExprGoType` resolve
-- bare ident `fn` to its typed sig at recursive call sites without
-- needing a round-trip through `T.Type` (which the standard Sky
-- TVar machinery erases to `any`).  In v0.15.5 PR 2 the underlying
-- storage was consolidated into `scopeStateRef`'s `_lc_lambdaGoStr`
-- map; the helpers below preserve the public API so existing
-- consumers continue to work.
--
-- | Like `withScopedLambdaTypes` but stores Go-type strings directly.
withScopedLambdaGoStrings :: Map.Map String String -> GoIr.GoExpr -> GoIr.GoExpr
withScopedLambdaGoStrings additions x = unsafePerformIO $ do
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef (LC.withLambdaGoStrs additions prev)
    let rendered = GoBuilder.renderExpr x
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)


-- | Lookup a Go-type string for a variable in the current lambda-
-- types scope. Returns the registered string verbatim — used by
-- `goExprGoType` for function-typed parameters in scope inside
-- a generic body (where the Sky-type Render path can't recover
-- Go-side TVar names like `T1`, `T2`).
lookupLambdaGoStr :: String -> Maybe String
lookupLambdaGoStr k = unsafePerformIO $ do
    ctx <- readIORef scopeStateRef
    return (LC.lookupLambdaGoStr ctx k)


-- | Read the global codegen env (for use in pure codegen functions).
-- NOINLINE so GHC doesn't CSE the IORef read across call sites —
-- each `getCgEnv` invocation must see the LATEST mutation. Without
-- this, a `modifyIORef globalCgEnv …` followed by `getCgEnv` in
-- downstream codegen would still observe the pre-modify value (the
-- read was lifted to the top level as a pure constant). v0.12.x
-- typed-codegen close-out diagnosed this when dep-module solved
-- types failed to propagate post-merge.
{-# NOINLINE getCgEnv #-}
getCgEnv :: Rec.CodegenEnv
getCgEnv = unsafePerformIO $ readIORef globalCgEnv


-- | Read ffi/*.kernel.json and write the resulting module/function maps into
-- Env.ffiKernelModulesRef and Env.ffiKernelFunctionsRef. After this call the
-- pure kernelModules / kernelFunctions lookups include FFI entries.
loadAndSeedFfiRegistry :: IO ()
loadAndSeedFfiRegistry = do
    reg <- FfiReg.loadRegistry
    let mods = FfiReg._fr_modules reg
        moduleMap =
            Map.fromList [ (FfiReg._fm_moduleName m, FfiReg._fm_kernelName m) | m <- mods ]
        functionMap =
            Map.fromListWith (++)
                [ (FfiReg._fm_kernelName m,
                   map FfiReg._ffn_name (FfiReg._fm_functions m))
                | m <- mods
                ]
    let arityMap = Map.fromList
            [ ((FfiReg._fm_kernelName m, FfiReg._ffn_name f),
                FfiReg._ffn_arity f)
            | m <- mods, f <- FfiReg._fm_functions m
            ]
        -- Phase C: turn parsed FtyAst into a canonical Annotation
        -- per (kernelName, fnName). Only entries whose kernel.json
        -- carried a parseable @skyType@ land here — pathological
        -- FFI shapes (channels, deeply-nested inline-struct
        -- callback bundles, missed-by-isSkyParseable Go residue)
        -- omit @skyType@ at producer side, which decodes to
        -- _ffn_skyType = Nothing here, which keeps them OUT of the
        -- typeMap. The canonicaliser/Constrain falls back to the
        -- legacy "no Sky type known" path for those — their
        -- callers stay polymorphic-any, exactly as before.
        typeMap = Map.fromList
            [ ((FfiReg._fm_kernelName m, FfiReg._ffn_name f),
                FfiTy.ftyToAnnotation (FfiReg._fm_kernelName m) ast)
            | m <- mods
            , f <- FfiReg._fm_functions m
            , Just ast <- [FfiReg._ffn_skyType f]
            ]
    writeIORef Env.ffiKernelModulesRef moduleMap
    writeIORef Env.ffiKernelFunctionsRef functionMap
    writeIORef Env.ffiKernelArityRef arityMap
    writeIORef Env.ffiKernelTypeRef typeMap
    seedTypedFfiNames
    if null mods
        then return ()
        else putStrLn $ "-- Loaded " ++ show (length mods) ++ " FFI module(s)"


-- | P7: scan ffi/*.go (and ffi/**/*.go) for `^func Go_X_yT(` definitions
-- and populate Env.ffiTypedWrapperNamesRef so call-site codegen can
-- prefer the typed variant. Silently tolerates a missing .skycache/go dir.
seedTypedFfiNames :: IO ()
seedTypedFfiNames = do
    let ffiDir = ".skycache/go"
    present <- doesDirectoryExist ffiDir
    if not present then return () else do
        entries <- listDirectory ffiDir
        let gofiles = [ ffiDir </> e | e <- entries, takeExtension e == ".go" ]
        pairLists <- mapM scanTypedWrapperFile gofiles
        let allEntries = concat pairLists
        writeIORef Env.ffiTypedWrapperNamesRef (Set.fromList (map fst allEntries))
        writeIORef Env.ffiTypedWrapperParamsRef (Map.fromList allEntries)


-- | Return `(name, paramGoTypes)` for every `^func Go_X_yT(…)` definition
-- in the file. Param Go types are parsed directly from the signature
-- (line-level — FfiGen emits typed wrappers with the signature on one
-- line). Zero-arg typed wrappers yield ("Go_X_yT", []).
scanTypedWrapperFile :: FilePath -> IO [(String, [String])]
scanTypedWrapperFile fp = do
    ok <- doesFileExist fp
    if not ok then return [] else do
        contents <- readFile fp
        let ls = lines contents
        return
            [ (name, paramTypes)
            | l <- ls
            , take 5 l == "func "
            , let rest = drop 5 l
            , take 3 rest == "Go_"
            , '(' `elem` rest
            , let name = takeWhile (/= '(') rest
            , not (null name)
            , last name == 'T'
            , let paramTypes = extractParamTypes (dropWhile (/= '(') rest)
            ]


-- | Parse `(p0 T0, p1 T1, …)` (the param list of a typed wrapper sig)
-- into [T0, T1, …]. Handles bracketed type params and nested parens as
-- balanced tokens.
extractParamTypes :: String -> [String]
extractParamTypes sig = case sig of
    ('(':rest) ->
        let inside = takeParenContents rest 0
        in map extractTypeAfterName (splitTopComma inside)
    _ -> []
  where
    takeParenContents [] _ = ""
    takeParenContents (')':_) 0 = ""
    takeParenContents ('(':xs) d = '(' : takeParenContents xs (d+1)
    takeParenContents (')':xs) d = ')' : takeParenContents xs (d-1)
    takeParenContents ('[':xs) d = '[' : takeParenContents xs (d+1)
    takeParenContents (']':xs) d = ']' : takeParenContents xs (d-1)
    takeParenContents (x:xs) d   = x : takeParenContents xs d

    splitTopComma str = reverse (map reverse (finish (foldl step ([], [], 0) str)))
      where
        step (cur, acc, 0) ','  = ([], cur : acc, 0)
        step (cur, acc, d) c
            | c == '[' || c == '(' = (c : cur, acc, d + 1)
            | c == ']' || c == ')' = (c : cur, acc, d - 1)
            | otherwise            = (c : cur, acc, d)
        finish (cur, acc, _) = if null cur then acc else cur : acc

    extractTypeAfterName part =
        let trimmed = dropWhile (== ' ') part
            afterName = dropWhile (/= ' ') trimmed
        in dropWhile (== ' ') afterName


-- | Full compilation: parse → canonicalise → codegen → write Go
compile :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
compile config entryPath outDir = do
    -- Compute source root relative to the entry file
    let entryDir = takeDirectory entryPath
        sourceRoot = if Toml._sourceRoot config == "src"
            then entryDir  -- entry IS in the source root
            else Toml._sourceRoot config
        -- Project root = parent of src/. Used for the implicit `tests/`
        -- extra discovery root below. Resolving against projectRoot
        -- (not cwd) prevents a stale `tests/` from an unrelated dir
        -- — e.g. the sky compiler repo's own tests/ — leaking into
        -- the workspace when the user invokes `sky` from a foreign
        -- working directory.
        compileProjectRoot = case takeDirectory entryDir of
            "" -> "."
            d  -> d

    -- Phase 0: Load FFI registry (ffi/*.kernel.json) and seed the kernel
    -- module/function IORefs so FFI packages resolve as first-class kernels.
    loadAndSeedFfiRegistry

    -- Phase 0b: Install Sky-source dependencies declared in [dependencies].
    -- Each dep contributes an extra source root that discovery will probe
    -- in order after the primary project source root.
    depRoots <- SkyDeps.installDeps (Toml._skyDeps config)

    -- Phase 0c: Materialise the embedded Sky stdlib (Sky.Core.Error, etc.)
    -- into outDir/.sky-stdlib/ and add it as a discovery root so
    -- `import Sky.Core.Error` resolves with no user setup. Stdlib lives
    -- LAST in the root list so a user's local Std/* override wins.
    stdlibRoot <- writeEmbeddedSkyStdlib outDir

    -- Phase 1: Discover all modules.
    -- tests/ is an implicit extra root when it exists — `sky test`
    -- writes a synthesised entry under src/ that imports the test
    -- module from tests/, and the graph walker needs to see both
    -- roots for the import to resolve. Harmless for non-test builds
    -- because a tests/ dir without modules contributes no modules.
    putStrLn "-- Discovering modules"
    let testsRootPath = compileProjectRoot </> "tests"
    testsRootExists <- doesDirectoryExist testsRootPath
    let extraTestsRoot = if testsRootExists then [testsRootPath] else []
    modules <- Graph.discoverModulesMulti (sourceRoot : depRoots ++ extraTestsRoot ++ [stdlibRoot]) entryPath
    let moduleOrder = Graph.compilationOrder modules
    putStrLn $ "   Found " ++ show (length moduleOrder) ++ " module(s)"

    -- Incremental build: if source hash matches cached, reuse output.
    --
    -- The hash mixes in not just the .sky source files but also
    -- sky.toml + every .skycache/ffi/*.kernel.json. Without that, a
    -- fresh `sky add <pkg>` would generate new FFI bindings, the user's
    -- source would be unchanged, and the incremental build would reuse
    -- the stale main.go that still references the wrong (path-based)
    -- name for the new module — surfacing as "undefined: <Path>_<fn>"
    -- at go-build time. Including the FFI registry + manifest in the
    -- hash makes any FFI / dep-config change invalidate the cache.
    extraHashInputs <- collectIncrementalHashInputs
    srcHash <- computeSourceHash
        (map Graph._mi_path moduleOrder ++ extraHashInputs)
    let cacheDir = ".skycache"
        hashFile = cacheDir </> "source.hash"
        existingMain = outDir </> "main.go"
    cacheHit <- do
        hasHash <- doesFileExist hashFile
        hasMain <- doesFileExist existingMain
        if hasHash && hasMain
            then do
                -- Strict read so the handle closes before the later
                -- writeFile (line 259) tries to re-open the same file.
                -- Lazy readFile left the handle open, breaking `sky check`
                -- on CI runners where the next step invoked sky again.
                cached <- readFile' hashFile
                return (cached == srcHash)
            else return False
    if cacheHit
        then do
            putStrLn "-- Incremental: source unchanged, reusing cached output"
            copyRuntime outDir
            -- copyRuntime overwrites sky-out/go.mod with runtime-go/go.mod,
            -- losing any user-declared Go deps from sky.toml's
            -- [go.dependencies]. Re-run seedGoDependencies so the rt/
            -- bindings (mux, stripe, firebase, …) still resolve on the
            -- incremental rebuild path.
            seedGoDependencies outDir (Toml._goDeps config)
            return (Right existingMain)
        else continueCompile config entryPath outDir moduleOrder srcHash


-- | Compute a stable hash of all source file contents
computeSourceHash :: [FilePath] -> IO String
computeSourceHash paths = do
    contents <- mapM (\p -> doesFileExist p >>= \ok -> if ok then readFile p else return "")
        paths
    -- Simple, not cryptographic: sum of SDBM-ish hashes keyed by path
    let combined = concat (zipWith (\p c -> p ++ ":" ++ c ++ "\n") paths contents)
    return (show (length combined) ++ "-" ++ show (foldl (\acc c -> acc * 31 + fromEnum c) (0 :: Int) combined))


-- | Files outside the Sky source tree whose contents must contribute
-- to the incremental-build hash so changes invalidate the lowered-
-- main.go cache. Currently:
--
--   - sky.toml — `[go.dependencies]` / `[dependencies]` / runtime config
--     all influence codegen behaviour. Adding a Go dep (`sky add`) +
--     reusing cached output otherwise produces calls to functions
--     whose wrappers don't exist yet.
--   - .skycache/ffi/*.kernel.json — the FFI registry. Each kernel.json
--     records a moduleName→kernelName mapping that the canonicaliser
--     consults when lowering FFI calls. A new file (or a regenerated
--     one with a different shape) MUST invalidate the lowered cache.
--
-- Files that don't exist contribute the empty string — safe for fresh
-- projects that don't yet have a .skycache/.
collectIncrementalHashInputs :: IO [FilePath]
collectIncrementalHashInputs = do
    let tomlPath = "sky.toml"
    ffiDir <- doesDirectoryExist ".skycache/ffi"
    ffiFiles <- if ffiDir
        then do
            entries <- listDirectory ".skycache/ffi"
            return [ ".skycache/ffi" </> e
                   | e <- entries
                   , takeExtension e == ".json"
                   ]
        else return []
    return (tomlPath : ffiFiles)


continueCompile :: Toml.SkyConfig -> FilePath -> FilePath -> [Graph.ModuleInfo] -> String -> IO (Either String FilePath)
continueCompile config entryPath outDir moduleOrder srcHash = do

    -- Phase 2: Parse all modules in parallel — parsing is pure text→AST
    -- with no cross-module dependencies, so it parallelises trivially.
    -- We preserve topo order in the result list so downstream phases see the
    -- same ordering as a sequential build.
    putStrLn "-- Parsing"
    parseResults <- Async.forConcurrently moduleOrder $ \modInfo -> do
        src <- TIO.readFile (Graph._mi_path modInfo)
        case Parse.parseModule src of
            Left err ->
                return (modInfo, Left err)
            Right srcMod ->
                return (modInfo, Right srcMod)
    let formatted = flip map parseResults $ \(modInfo, r) -> case r of
            Left err ->
                Left $ "Parse error in " ++ Graph._mi_name modInfo ++ ": " ++ show err
            Right srcMod ->
                Right (Graph._mi_name modInfo, srcMod)
    -- v0.13 Layer 1: render each parser failure as a structured
    -- Diagnostic.  The block carries the offending file + line:col,
    -- a source snippet around the failure, and a short variant-
    -- specific reason ("module name expected here", etc.).  This
    -- replaces the previous `PARSE FAILED: <module> <ctor>` line
    -- which surfaced the Haskell constructor name to end users.
    mapM_ (\(modInfo, r) -> case r of
        Left err -> do
            let diag = Parse.moduleErrorToDiagnostic
                         (Graph._mi_path modInfo) err
            rendered <- Render.renderCli diag
            putStrLn rendered
        Right srcMod ->
            let declCount = length (Src._values srcMod)
            in putStrLn $ "   " ++ Graph._mi_name modInfo ++ ": " ++ show declCount ++ " declarations"
        ) parseResults
    let parseResults' = formatted

    let errors = [e | Left e <- parseResults']
        parsed = [(n, m) | Right (n, m) <- parseResults']

    if not (null errors) then return (Left $ head errors)
      else if null parsed then return (Left "No modules found")
      else do
        -- Phase 3: Canonicalise (entry module + merge deps)
        putStrLn "-- Canonicalising"
        let entrySrcMod = snd (last parsed)
            -- Dependency modules are all parsed modules except the entry.
            depModules = if length parsed > 1 then init parsed else []

        -- Two-pass canonicalisation so dep modules can reference each
        -- other's ADT constructors:
        --   1. Canonicalise each dep in isolation (only its own ADTs visible)
        --      to build a depInfoMap with every module's union constructors.
        --   2. Re-canonicalise every dep AND the entry with the full map.
        -- Canonicalise dependency modules to a FIXPOINT.
        --
        -- A single isolated pass + one pass-with-deps is NOT enough.
        -- A Sky-stdlib module can use ANOTHER stdlib module's ADT
        -- constructors — e.g. `Std.Html.Events` builds `EventAttr
        -- (OnMsg …)` from `Std.Html.Attributes`.  Those constructors
        -- are invisible in isolation (pass 1), so `Std.Html.Events`
        -- fails pass 1 and is absent from the dep map; a THIRD
        -- module that imports it (`Page.Dashboard`) then canonicalises
        -- in pass 2 WITHOUT `Std.Html.Events` in scope and silently
        -- resolves `import Std.Html.Events` to the KERNEL — so
        -- `onClick` came back as the kernel's arity-0 `Attribute`
        -- alias instead of the Sky module's `Attribute msg`.
        --
        -- A dependency chain of depth N needs N canonicalisation
        -- passes.  So iterate: rebuild the dep map from each pass's
        -- successes and re-canonicalise EVERY dep (pass-1 home
        -- resolutions are wrong anyway) until the success set
        -- stabilises.  Capped at 16 iterations — a genuinely-broken
        -- module never enters the success set, so the cap only
        -- bounds wasted passes, and its error still surfaces in
        -- `depErrors` below.
        let buildDepInfoMap valids = Map.fromList
                [ (modName, Canonicalise.DepInfo
                    { Canonicalise._dep_name = Can._name depMod
                    , Canonicalise._dep_unions =
                        [ (typeName, Can._u_vars union, Can._u_alts union)
                        | (typeName, union) <- Map.toList (Can._unions depMod)
                        ]
                    , Canonicalise._dep_aliases = Map.keys (Can._aliases depMod)
                    , Canonicalise._dep_aliasDefs = Can._aliases depMod
                    , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                    , Canonicalise._dep_exports = Can._exports depMod
                    })
                | (modName, depMod) <- valids
                ]
            canonPassWith m = Async.forConcurrently depModules $ \(n, srcMod) ->
                case Canonicalise.canonicaliseWithDeps m srcMod of
                    Right cm -> return (Right (n, cm))
                    Left err -> return (Left (n, err))
            canonFixpoint
                :: Set.Set String
                -> Map.Map String Canonicalise.DepInfo
                -> Int
                -> IO ([(String, Can.Module)], [(String, String)])
            canonFixpoint prevSucc curMap iter = do
                results <- canonPassWith curMap
                let valids = [x | Right x <- results]
                    errs   = [(n, e) | Left (n, e) <- results]
                    succSet = Set.fromList (map fst valids)
                if succSet == prevSucc || iter >= 16
                    then return (valids, errs)
                    else canonFixpoint succSet (buildDepInfoMap valids) (iter + 1)
        (validDeps, depErrors) <- canonFixpoint Set.empty Map.empty (0 :: Int)
        -- The dep map for the entry module is rebuilt from the
        -- fixpoint's final (complete) success set.
        let depInfoMap2 = buildDepInfoMap validDeps
        case depErrors of
         ((n, err):_) -> do
            -- v0.13 Layer 1: render the dep-module canonicalise
            -- error through the structured Diagnostic pipeline so
            -- the user sees the Elm-style block (file:line:col +
            -- source snippet + reason) instead of a bare prefixed
            -- string.  Look up the dep's source path so the snippet
            -- comes from the right file.
            let depPath = case [p | mi <- moduleOrder
                                  , Graph._mi_name mi == n
                                  , let p = Graph._mi_path mi ] of
                            (p:_) -> p
                            _     -> entryPath
                diag = Canonicalise.legacyToDiag depPath err
            rendered <- Render.renderCli diag
            putStrLn rendered
            return (Left $ "Canonicalise error in " ++ n)
         [] ->
          case Canonicalise.canonicaliseWithDeps depInfoMap2 entrySrcMod of
           Left err -> do
            -- v0.13 Layer 1: same treatment for the entry module.
            let diag = Canonicalise.legacyToDiag entryPath err
            rendered <- Render.renderCli diag
            putStrLn rendered
            return (Left "Canonicalise error")
           Right canMod -> do
            putStrLn "   Names resolved"
            -- v0.15 Stage E — populate the all-alias map EARLY (before
            -- any sig emission) so the parametric-alias generic-args
            -- renderer can recover alias type-args from row-poly HM
            -- inferred records.  Both raw + prefixed alias names are
            -- registered so the lookup-with-prefix-strip path always
            -- resolves.
            let allAliasesMap = Map.unions
                    (Can._aliases canMod
                     : [ Map.mapKeys (\n -> prefix ++ "_" ++ n)
                                      (Can._aliases dm)
                       | (mn, dm) <- validDeps
                       , let prefix = map (\c -> if c == '.' then '_' else c) mn
                       ]
                     ++ [ Can._aliases dm | (_, dm) <- validDeps ])
            writeIORef globalAllAliases allAliasesMap
            -- v0.15 Stage E — same for the field-index registry.
            -- Used by `tvarsInEmitted` to detect parametric-alias-
            -- shaped records WITHOUT triggering the cgEnv lazy
            -- thunk's `<<loop>>` black-hole during env construction.
            let allFieldIdx = Map.union
                    (Rec.buildRegistry (Can._aliases canMod))
                    (Rec.buildDepFieldIndex
                        [ (prefix, Can._aliases dm)
                        | (mn, dm) <- validDeps
                        , let prefix = map (\c -> if c == '.' then '_' else c) mn
                        ])
            writeIORef globalAllFieldIdx allFieldIdx
            -- T2/T6: prime the global codegen env's function-type
            -- tables BEFORE dep-decl emission, so call-site codegen
            -- in dep bodies (Can.Call → coerceCallArgs) can also see
            -- typed param types for cross-module calls.
            let earlyAllRecAliases = Set.unions
                    [ Set.union
                        (Rec.collectRecordAliases (Can._aliases m))
                        (Set.map (\n -> p ++ "_" ++ n)
                                 (Rec.collectRecordAliases (Can._aliases m)))
                    | (mn, m) <- validDeps
                    , let p = map (\c -> if c == '.' then '_' else c) mn
                    ] `Set.union`
                    Rec.collectRecordAliases (Can._aliases canMod)
                earlyDepTriples =
                    [ collectFuncTypesWith earlyAllRecAliases prefix depMod
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                earlyDepParamTypes = Map.unions
                    [ p | (p, _, _) <- earlyDepTriples ]
                earlyDepRetTypes = Map.unions
                    [ r | (_, r, _) <- earlyDepTriples ]
                earlyDepUltRetTypes = Map.unions
                    [ u | (_, _, u) <- earlyDepTriples ]
                (earlyEntryParams, earlyEntryRet, earlyEntryUltRet) =
                    collectFuncTypesWith earlyAllRecAliases "" canMod
            modifyIORef globalCgEnv $ \e -> e
                { Rec._cg_funcParamTypes =
                    Map.union earlyEntryParams earlyDepParamTypes
                , Rec._cg_funcRetType =
                    Map.union earlyEntryRet earlyDepRetTypes
                , Rec._cg_funcUltimateRetType =
                    Map.union earlyEntryUltRet earlyDepUltRetTypes
                }
            -- v0.13 Stage 1 (task #189) — depDecls computation moved
            -- LATER in the pipeline (just before generateGoMulti) so
            -- the dep-emit-time `getCgEnv` sees `funcSkyToGoTVars`
            -- populated by typecheck. Earlier emission baked empty σ
            -- into the paramTypeBindings rewrite, leaving recursive
            -- calls in Sky-source kernel bodies emitting
            -- `Sky_Core_List_map_(rt.Coerce[func(any) any](fn), …)`
            -- adapters with bare TVars. See plan doc.
            let depRecAliases = Set.unions
                    [ Set.map (\n -> prefix ++ "_" ++ n)
                             (Rec.collectRecordAliases (Can._aliases depMod))
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                -- All Sky-defined ADT/union names with the dep's module
                -- prefix. safeReturnType uses this set to distinguish
                -- "type Sky_Core_Error_Error = rt.SkyADT" (alias is
                -- emitted, name is safe to use as a Go type) from
                -- "Bufio_Scanner" (FFI-opaque, no Go alias exists, must
                -- fall back to `any` so Go compilation succeeds).
                depUnionNames = Set.unions
                    [ Set.map (\n -> prefix ++ "_" ++ n)
                             (Set.fromList (Map.keys (Can._unions depMod)))
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                -- v0.13 typed lowerer: dep-module ENUM union names
                -- (prefixed).  An enum emits `type X = int`, so its
                -- zero value is `0` — `goZeroValue` needs this to
                -- distinguish from tagged ADTs (`type X = rt.SkyADT`,
                -- zero `X{}`).
                depEnumNames = Set.unions
                    [ Set.map (\n -> prefix ++ "_" ++ n)
                             (Set.fromList
                                [ uname
                                | (uname, u) <- Map.toList (Can._unions depMod)
                                , Can._u_opts u == Can.Enum
                                ])
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                depArities = Map.unions
                    [ Map.mapKeys (\n -> prefix ++ "_" ++ goSafeName n)
                                  (Rec.collectFuncArities (Can._decls depMod))
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                -- T2/T6: collect typed param + return signatures from
                -- every dep module's annotated declarations. Names are
                -- module-prefixed (Lib_Db_exec) to match the call-site
                -- emission convention. Uses the merged record-alias
                -- set so cross-module record types resolve.
                depTriples =
                    [ collectFuncTypesWith earlyAllRecAliases prefix depMod
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                depParamTypes = Map.unions [ p | (p, _, _) <- depTriples ]
                depRetTypes = Map.unions [ r | (_, r, _) <- depTriples ]
                depUltRetTypes = Map.unions [ u | (_, _, u) <- depTriples ]
            putStrLn "-- Type Checking"
            -- Run HM on each dep module so unannotated functions get
            -- inferred types for the typed-codegen tables.
            --
            -- Two-pass: pass 1 solves each dep in isolation; pass 2
            -- re-solves with cross-module externals from pass 1
            -- (some deps need pass 2 to disambiguate via imported
            -- helpers' concrete types). Pass 1 errors are TOLERATED
            -- because pass 2 may fix them via cross-module info.
            --
            -- v0.10.0 (dep-HM-fatal): if BOTH passes fail for a dep,
            -- that's a real type error in the dep's body — surface
            -- as a fatal `TYPE ERROR (Mod): …` and abort the build.
            -- Previously we silently degraded to `any` typing for
            -- such deps, which let real type bugs ship and produced
            -- runtime symptoms like `[AUTH] Admin ensured: 0x102…`
            -- (an unforced Task thunk's func-pointer being string-
            -- split because the dep's HM error was hidden).

                    -- Pass 1: solve each dep in isolation.
            depSolved0 <- Async.forConcurrently validDeps $ \(modName, depMod) -> do
                cs <- Constrain.constrainModule depMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveOk t -> return (modName, t)
                    Solve.SolveError _ -> return (modName, Solve.emptySolvedTypes)
            -- Pass 2: re-solve each dep with cross-module externals
            -- from pass 1. Serialised (mapM not Async.forConcurrently)
            -- because the external-ref write is global — parallel
            -- writes would race. Acceptable cost: dep solves are fast.
            --
            -- Pass 2 surfaces TWO error classes that pass 1 cannot
            -- catch (it has no cross-module info):
            --
            --   1. **Foreign-call mismatches** — a dep calls a
            --      cross-module function (`Ui.paddingEach 8 12 8 12`,
            --      `String.toUpper "x" "y"`, etc.) with the wrong
            --      arity / arg type / record shape. The error string
            --      starts with `Foreign 'Mod.fn':` and is ALWAYS a
            --      real bug — pass 2 is fatal for these.
            --
            --   2. **Local-typing artefacts** — pass 2 happens to
            --      detect a constraint that pass 1 missed (e.g. an
            --      already-broken tuple-shape ambiguity, an
            --      already-broken let binding) because the externals
            --      let it propagate further. These are real bugs too,
            --      but they pre-date this round and live in examples
            --      we know carry latent issues. Surfacing them now
            --      would block the round-7 release. So pass 2
            --      tolerates non-Foreign errors via the pass-1
            --      fallback, leaving them visible-but-non-fatal for
            --      a follow-up cleanup pass.
            --
            -- Pre-fix bug: pass 2 errors ALL fell back to pass 1.
            -- That masked the Foreign class entirely, letting bad
            -- cross-module call sites compile and surface as
            -- confusing `go build` errors like
            --   "too many arguments in call to Std_Ui_paddingEach"
            -- long after sky check should have caught them.
            let isForeignErr s = "Foreign '" `List.isInfixOf` s
            -- v0.13 Phase A5: use `solveWithInstances` on each dep so
            -- monomorphisation captures dep-module call sites too.
            -- Pass 1 (`depSolved0`, above) stays on plain `solve`: its
            -- job is dep-isolation typing; captures aren't useful since
            -- externals are empty.  Every subsequent round has the full
            -- externals built from the previous round — that's where
            -- real call-site instances surface.
            --
            -- v0.13 Layer 3: the dep-solve is now a true FIXPOINT, not
            -- a fixed 2 extra passes.  Migrating
            -- Std.Html{,/Attributes,/Events} from kernel pseudo-modules
            -- to Sky-source stdlib deepened the dependency chain
            -- (Std.Html.Attributes → Std.Html → Ui.Layout → Page.* →
            -- Main).  A hard-coded pass count silently under-solves
            -- every module past the cap: its cross-module callees
            -- resolve against incomplete externals, HM infers a wrong
            -- type (classically a `Dict String String`-polluted `msg`
            -- var leaking out of an event handler), and that wrong type
            -- propagates into every consumer.  Iterating until the
            -- solved type sets stop changing makes the depth of the
            -- module graph irrelevant.  The cap (16) only guards
            -- against a pathological oscillation — real graphs
            -- converge in `depth-of-chain` rounds.
            -- Each round's per-module result is
            --   `Right (types, csi, mErr)` — solved (mErr=Nothing) OR
            --     fell back to the previous round's types because THIS
            --     round still had a non-Foreign error (mErr=Just err);
            --   `Left err` — a Foreign error, or a non-Foreign error
            --     with no previous-round types to fall back to.
            -- The fell-back `Just err` is what closes the v0.13 Layer 3
            -- soundness hole: DURING the fixpoint a module legitimately
            -- errors because its externals aren't ready yet, so we keep
            -- iterating; but at CONVERGENCE the externals are complete,
            -- so a module that STILL errors has a REAL type bug and is
            -- surfaced FATALLY (see `depErrors` below) instead of
            -- silently shipping its stale round-1 typing.  Pre-fix, a
            -- typed dep param (`Css.padding2 : Length -> Length`)
            -- failed to reject a `String` arg in a consumer because the
            -- consumer module's own non-Foreign solve error was
            -- tolerated and its polymorphic round-1 types shipped.
            -- Per-dep top-level declaration names. Computed ONCE
            -- outside the fixpoint so each round can filter the
            -- externals map down to genuine top-level decls (the
            -- merged solve result includes let-locals + lambda
            -- params via the `_locals` merge — those are bound by
            -- structural keys and would silently pollute the
            -- cross-module externals if exported).
            let depDeclaredNamesFix =
                    [ (mn, Set.toList (collectDeclNames (Can._decls dm)))
                    | (mn, dm) <- validDeps
                    ]
                filterToTopLevel ext = Map.filterWithKey
                    (\(m, n) _ -> case lookup m depDeclaredNamesFix of
                        Just names -> n `elem` names
                        Nothing    -> False)
                    ext
            let solveRound prevSolved = do
                    let externals =
                            filterToTopLevel
                                -- v0.15.x P37a: `prevSolved` carries
                                -- the new `Solve.SolvedTypes` records;
                                -- the externals helper still consumes
                                -- the bare `Map.Map String T.Type`
                                -- view, so extract `_stEnv` here.
                                (buildCrossModuleExternalsWithMods validDeps
                                    [(mn, Solve._stEnv s) | (mn, s) <- prevSolved])
                    -- v0.13 Phase A5: use `solveWithInstances` so per-call
                    -- instance capture flows through dep-module callsites
                    -- (used by monomorphisation).
                    mapM (\(modName, depMod) -> do
                        cs <- Constrain.constrainModuleWithExternals externals depMod
                        -- v0.15 Stage A/B: dep modules also get
                        -- per-region types.  Merge into the global
                        -- map below, after the fixpoint converges,
                        -- so the lowerer can query dep-body regions
                        -- too.  Pass-through `regionTys` per dep.
                        (r, _, csi, regionTys)
                            <- Solve.solveWithInstancesAndRegions cs
                        case r of
                            Solve.SolveOk t ->
                                return (modName, Right (t, csi, regionTys, Nothing))
                            Solve.SolveError err
                                | isForeignErr err -> return (modName, Left err)
                                | otherwise -> case lookup modName prevSolved of
                                    Just p | not (Map.null (Solve._stEnv p)) ->
                                        return (modName, Right (p, csi, regionTys, Just err))
                                    _ -> return (modName, Left err)) validDeps
                depTypesOf results =
                    [(mn, t) | (mn, Right (t, _, _, _)) <- results]
                solveFixpoint prevSolved n = do
                    results <- solveRound prevSolved
                    let curSolved = depTypesOf results
                    if curSolved == prevSolved || n >= (16 :: Int)
                        then return results
                        else solveFixpoint curSolved (n + 1)
            finalResults <- solveFixpoint depSolved0 (0 :: Int)
            let depErrors = [(mn, e) | (mn, Left e) <- finalResults]
                         -- Converged-round non-Foreign errors are real.
                         ++ [ (mn, e)
                            | (mn, Right (_, _, _, Just e)) <- finalResults
                            ]
                -- v0.15.x P37a: `t` is the new `Solve.SolvedTypes`
                -- record; helpers downstream (`buildAnnotMap`,
                -- `buildCrossModuleExternalsWithMods`) take the raw
                -- `Map.Map String T.Type` view.  Extract `_stEnv`
                -- here so the existing helper signatures stay put.
                depSolved = [(mn, Solve._stEnv t) | (mn, Right (t, _, _, _)) <- finalResults]
                depCsiByMod = [(mn, csi) | (mn, Right (_, csi, _, _)) <- finalResults]
                -- v0.15 Stage A/B: merge per-region types from every
                -- dep into one map.  Entry-module region types added
                -- after solveWithInstancesAndRegions on the entry
                -- below; the merged map is written into
                -- `scopeStateRef`'s `_lc_regionTypes` field there
                -- (v0.15.5 PR 3 — consolidated with the rest of the
                -- lowering-scope state).
                depRegionTys = Map.unions
                    [ rt | (_, Right (_, _, rt, _)) <- finalResults ]
            unless (null depErrors) $ do
                -- v0.13 Layer 1: route dep-module type errors through the
                -- structured Diagnostic renderer too.  Pre-fix each was
                -- printed as `   TYPE ERROR (Lib.Auth): 114:17: ...` and
                -- then `error`'d out with a Haskell CallStack visible to
                -- the user.  Now each emits the same Elm-style block
                -- with TYPE ERROR header + source snippet + [E2001] code,
                -- and the discovery stage exits cleanly with code 1.
                mapM_ (\(mn, e) -> do
                    let depPath = case [p | mi <- moduleOrder
                                          , Graph._mi_name mi == mn
                                          , let p = Graph._mi_path mi ] of
                                    (p:_) -> p
                                    _     -> entryPath
                        diag = Solve.solveErrorToDiagnostic depPath e
                    rendered <- Render.renderCli diag
                    putStrLn rendered) depErrors
                System.Exit.exitWith (System.Exit.ExitFailure 1)
            -- Entry module gets cross-module externals so VarTopLevel
            -- references to dep values emit CForeign with the dep's
            -- solved annotation. Only fully-concrete types (no free
            -- TVars) cross, so call-site fresh instantiation can't
            -- introduce spurious unifications. Dep-defined user ADTs
            -- that appear in those types are fine because the entry
            -- module already imported them via its env.
            -- Restrict externals to names actually DECLARED as
            -- top-level values in their home module. Solver env
            -- entries include every name that flowed through
            -- (imports, constructors, etc.) — using those as
            -- cross-module annotations leaks spurious unifications.
            let depDeclaredNames =
                    [ (mn, Set.toList (collectDeclNames (Can._decls dm)))
                    | (mn, dm) <- validDeps
                    ]
                rawExternals = Map.filterWithKey
                    (\(m, n) _ -> case lookup m depDeclaredNames of
                        Just names -> n `elem` names
                        Nothing    -> False)
                    (buildCrossModuleExternalsWithMods validDeps depSolved)
                -- DEBUG bisect: keep only first N entries
                depExternals = rawExternals
            _ <- return depDeclaredNames  -- silence unused warning on release path
            constraints <- Constrain.constrainModuleWithExternals depExternals canMod
            putStrLn $ "   cross-module externals: " ++ show (Map.size depExternals)
            -- v0.13 Phase A3: use `solveWithInstances` so we also
            -- capture the call-site instance table for the
            -- monomorphisation pass.  Behaviour-equivalent to
            -- `Solve.solve` for the SolvedTypes portion — the
            -- new path merges `_locals` into the returned map
            -- identically (the missing merge was a subtle
            -- regression on Live.app's `init_` function-value
            -- references that's now fixed).
            -- v0.15 Stage A/B: collect per-region types so the
            -- typed-directed lowerer can look up sub-expression HM
            -- types by region.  Merge entry-module + every dep's
            -- region map.  Consumed downstream by
            -- `Solve.lookupSolvedRegion` against the SolvedTypes
            -- record (which now carries the merged region map in
            -- `_stRegions` — see the `typesWithDeps` builder at
            -- line ~1611 for the wire-up).
            (solveResult, callInstances, callSiteInstances, entryRegionTys)
                <- Solve.solveWithInstancesAndRegions constraints
            -- v0.15.x P37b — the per-region map no longer lives on
            -- `scopeStateRef._lc_regionTypes` (that field was
            -- deleted).  Region types now flow purely through
            -- `Solve.SolvedTypes._stRegions`, populated when we
            -- rebuild SolvedTypes for `generateGoMulti`.
            let mergedRegionTys = Map.union entryRegionTys depRegionTys
            -- v0.13 Phase A5: install the per-call-site instance
            -- registry into `_cg_callSiteInstances` so call-site
            -- codegen can pick the right generic instantiation
            -- (concrete types) instead of erasing every TVar to
            -- `any`.  Key: (file, line, col) of the call's source
            -- region start.  Entry-module callsites go under
            -- entryPath; each dep's callsites use the dep's own
            -- source path (looked up via `moduleOrder`).
            -- v0.13 Phase A5++: CSI map is keyed by
            -- (line, col) AND nested by callee name, so calls at the
            -- same source position in different files (or different
            -- callees inferred at the same point) don't overwrite
            -- each other.  Lookups consult the inner map by the
            -- expected callee's Sky-form name.
            let csiEntries =
                    [ ( ( A._line (A._start (Solve._cs_region csi))
                        , A._col  (A._start (Solve._cs_region csi)) )
                      , Map.singleton
                          (Solve._instance_callee (Solve._cs_instance csi))
                          (Solve._cs_instance csi)
                      )
                    | csi <- callSiteInstances ++ concatMap snd depCsiByMod ]
                csiByRegion = Map.fromListWith Map.union csiEntries
            modifyIORef globalCgEnv $ \e ->
                Rec.withCallSiteInstances csiByRegion e
            writeIORef globalEntryPath entryPath
            -- HM type errors are FATAL (promoted from warning). No
            -- silent degradation to `any`. This enforces the
            -- "if it compiles, it works" promise at the entry module.
            let solverError = case solveResult of
                    Solve.SolveError e -> Just e
                    _                  -> Nothing
            types <- case solveResult of
                Solve.SolveOk t -> do
                    putStrLn $ "   Types OK (" ++ show (length (Map.keys (Solve._stEnv t))) ++ " bindings)"
                    -- v0.13 Phase A3: log the captured instance table.
                    -- Format: "<N> instances across <M> functions".
                    -- Set SKY_MONO_TRACE=1 to dump every instance.
                    let callsiteCount = length callInstances
                        uniqueCallees =
                            length (List.nub (map Solve._instance_callee callInstances))
                    putStrLn $ "   Monomorphisation: "
                            ++ show callsiteCount ++ " instances across "
                            ++ show uniqueCallees ++ " polymorphic callees"
                    monoTrace <- System.Environment.lookupEnv "SKY_MONO_TRACE"
                    case monoTrace of
                        Just "1" -> mapM_ (\ci ->
                            putStrLn $ "     " ++ Mono.mangleInstance ci) callInstances
                        _ -> return ()
                    -- v0.13 Phase A4 + A7: compute the REACHABLE
                    -- subset of captured instances by walking
                    -- transitively from `main`.  Stored globally for
                    -- generateGoMulti to consume when emitting
                    -- per-instance specialisations.
                    let defMap = buildDefMap canMod validDeps
                        annotMap = buildAnnotMap (Solve._stEnv t) depSolved
                        csiMapForReach = Map.fromList
                            [ ( ( A._line (A._start (Solve._cs_region csi))
                                , A._col  (A._start (Solve._cs_region csi)) )
                              , Solve._cs_instance csi
                              )
                            | csi <- callSiteInstances ++ concatMap snd depCsiByMod ]
                        entryName = case mainModuleName entrySrcMod of
                            Just n  -> n ++ ".main"
                            Nothing -> "Main.main"
                        reached = Mono.reachableInstances
                            defMap annotMap csiMapForReach
                            [(entryName, [])]
                    writeIORef globalReachableSet reached
                    writeIORef globalAnnotMap annotMap
                    -- v0.14.x Stage 4: scan every canon module for
                    -- Sky-source bindings whose body is exactly
                    -- `Ffi.kernel "K_n"`. Register each (home, name)
                    -- → (kernelMod, kernelName) so codegen rewrites
                    -- call sites to the typed kernel dispatch.
                    let entryModNameAlias = case mainModuleName entrySrcMod of
                            Just n  -> n
                            Nothing -> "Main"
                        allModsForAlias =
                            (entryModNameAlias, canMod) : validDeps
                        kernelAliasMap = Map.fromList $
                            concatMap collectKernelAliases allModsForAlias
                    writeIORef globalKernelAlias kernelAliasMap
                    -- v0.13 F: whole-program Sky DCE.  Walk every
                    -- module's call graph from `(entryMod, "main")`
                    -- across module boundaries, tracking VarTopLevel
                    -- + VarKernel (FFI) + VarCtor refs.  Stored
                    -- globally for loadAndSeedFfiRegistry's pruning
                    -- step + generateDeclsForDep's per-decl skip.
                    dceOff <- System.Environment.lookupEnv "SKY_DCE"
                    let dceDisabled = dceOff == Just "0"
                    writeIORef globalDceDisabled dceDisabled
                    if dceDisabled
                        then return ()
                        else do
                            let entryModName = case mainModuleName entrySrcMod of
                                    Just n  -> n
                                    Nothing -> "Main"
                                allModsMap = Map.fromList
                                    ((entryModName, canMod) : validDeps)
                                progReached = Dce.reachableWholeProgram
                                    entryModName allModsMap Set.empty
                            writeIORef globalReachableProgram progReached
                    -- v0.13 Phase A4: stash per-callee captured
                    -- quantifier names so spec emission uses the
                    -- SAME ordering the solver instantiated with.
                    let csiByCallee = Map.fromListWith (\a _ -> a)
                            [ (Solve._instance_callee inst,
                               Solve._instance_quantifiers inst)
                            | csi <- callSiteInstances ++ concatMap snd depCsiByMod
                            , let inst = Solve._cs_instance csi
                            , not (null (Solve._instance_quantifiers inst))
                            ]
                    writeIORef globalCsiByCallee csiByCallee
                    -- v0.13 Phase A4: eagerly compute the set of
                    -- specialised mangled names that WOULD be
                    -- emitted, so call-site codegen (which runs
                    -- during lazy evaluation of `decls`) knows
                    -- whether to use the mangled name without
                    -- depending on `specDecls` having been forced
                    -- first.  Skips instances whose σ_go is empty
                    -- (no specialisation needed — original func is
                    -- already non-generic) — the spec emission step
                    -- in generateGoMulti applies the same filter.
                    env0 <- readIORef globalCgEnv
                    let specNames = Set.fromList
                            [ Mono.mangleInstance
                                (Solve.CallInstance skyName tys [])
                            | (skyName, tys) <- Set.toList reached
                            , not (null tys)
                            , let goName = map (\c -> if c == '.' then '_' else c) skyName
                                  skyToGo = Map.findWithDefault []
                                      goName (Rec._cg_funcSkyToGoTVars env0)
                                  quants = case Map.lookup skyName annotMap of
                                      Just (T.Forall vs _) -> filter (/= "any") vs
                                      Nothing -> []
                                  σ_sky = Map.fromList (zip quants tys)
                                  σ_go = [() | (sn, _) <- skyToGo
                                             , Map.member sn σ_sky ]
                            , not (null σ_go)
                            ]
                    writeIORef globalEmittedSpecs specNames
                    reachTrace <- System.Environment.lookupEnv "SKY_REACH_TRACE"
                    case reachTrace of
                        Just "1" -> do
                            putStrLn $ "   [REACH] from " ++ entryName
                                    ++ ": " ++ show (Set.size reached)
                                    ++ " instances"
                            mapM_ (\(q, ts) ->
                                putStrLn $ "     " ++ Mono.mangleInstance
                                    (Solve.CallInstance q ts [])) (Set.toList reached)
                        _ -> return ()
                    return t
                Solve.SolveError err -> do
                    -- v0.13 Layer 1: route position-prefixed type
                    -- errors through the structured Diagnostic
                    -- renderer (Elm-style ERROR header + source
                    -- snippet + code). Solver-budget errors and
                    -- other pre-formatted multi-line guidance blocks
                    -- (anything that already begins with "TYPE
                    -- ERROR") are printed verbatim — the renderer
                    -- would otherwise wrap the helpful body inside
                    -- a `[E2001]` header that misattributes the
                    -- cause.
                    if "TYPE ERROR" `isPrefixOf` err
                        then putStrLn $ "   TYPE ERROR: " ++ entryPath ++ ":" ++ err
                        else do
                            let diag = Solve.solveErrorToDiagnostic entryPath err
                            rendered <- Render.renderCli diag
                            putStrLn rendered
                    return Solve.emptySolvedTypes
            -- P3: exhaustiveness — walk the entry + every dep's canonical
            -- tree for non-exhaustive case expressions. A miss is a
            -- compile-time error with source context; the `panic("non-
            -- exhaustive case expression")` fallback in codegen never
            -- fires on well-checked code.
            -- v0.13 Layer 1: each exhaustiveness `Exhaust.Diag`
            -- becomes a structured `Diagnostic` (category=Exhaust,
            -- code=E3001) with the offending region as the caret
            -- target.  The renderer adds source-context lines so
            -- the user sees the actual `case … of` block instead of
            -- a bare "at line N:M — hint" prefix.  Entry-module
            -- diags are attributed to the entry path; dep-module
            -- diags fall back to the entry path too because
            -- `Exhaust.checkModule` doesn't carry a per-module
            -- source path today (deferred to Layer 1 follow-up).
            let entryDiagsExh = Exhaust.checkModule canMod
                depDiagsExh   = concatMap (\(_, dm) -> Exhaust.checkModule dm) validDeps
                allExhDiags   = entryDiagsExh ++ depDiagsExh
                exhDiagnostics =
                    [ Diag.withHint (_diag_hintFor d)
                      (Diag.mkError entryPath (_diag_locFor d)
                          Diag.CatExhaustiveness Diag.exhaustE_NonExhaustive
                          (_diag_msgFor d))
                    | d <- allExhDiags ]
                _diag_locFor (Exhaust.Diag r _ _) = r
                _diag_hintFor (Exhaust.Diag _ _ h) = h
                _diag_msgFor (Exhaust.Diag _ missing _) =
                    "Non-exhaustive case expression. Missing pattern(s): " ++
                    List.intercalate ", " missing
                exhaustErr
                    | null allExhDiags = Nothing
                    | otherwise = Just $ renderExhaustDiags allExhDiags
            case exhDiagnostics of
                [] -> return ()
                ds -> do
                    rendered <- Render.renderCliMany ds
                    putStrLn rendered
            -- Merge inferred dep types into the param + return tables
            -- keyed by module-prefixed Go names. Annotation-derived
            -- entries already in the tables win over inferred ones.
            -- T4b: only record inferred sigs for UNANNOTATED bindings;
            -- annotated functions use their declared types verbatim,
            -- and if HM happens to infer spurious TVars for them we'd
            -- mistakenly emit `[any, any]` instantiations at call sites.
            let hasAnnotation n depMod = case Map.lookup n (declsByName depMod) of
                    Just (Can.TypedDef{}) -> True
                    _                     -> False
                -- Field-set → alias-name registry covering the entry
                -- module + every dep module (prefixed form) so HM-inferred
                -- record returns resolve to their `_R` struct name here too.
                earlyAllFieldIdx = Map.union
                    (Rec.buildRegistry (Can._aliases canMod))
                    (Rec.buildDepFieldIndex
                        [ (map (\c -> if c == '.' then '_' else c) mn, Can._aliases depMod)
                        | (mn, depMod) <- validDeps
                        ])
                -- HM-inferred sigs for dep module unannotated functions.
                -- TVars become Go type params for polymorphic functions.
                fullSigs = Map.unions
                    [ Map.fromList
                        -- v0.13 Layer 3 fix: dep-emitted Go names go
                        -- through `goSafeName` (so a Sky function
                        -- named `map` lands as `Sky_Core_X_map_`).
                        -- The sig-table key MUST use the same
                        -- mangled form or call-site coercion can't
                        -- look it up.  Pre-fix, cross-module calls
                        -- to Sky-source Result.map got no coercion
                        -- and `go build` rejected the call site.
                        -- v0.13 Stage 1 — INCLUDE annotated functions
                        -- too. Previously skipped because HM-inferred
                        -- TVars for annotated functions could be
                        -- spurious; for annotated functions we use the
                        -- ANNOTATION TYPE (reconstructed from pats +
                        -- retType) rather than the HM-solved type
                        -- because HM may not preserve the full TLambda
                        -- structure when the annotation grounds the
                        -- type (e.g. `node` ends up solved as just
                        -- `Html msg` losing the 3 input arrows).
                        -- Using the annotation directly yields
                        -- TVar-preserving sigs like
                        -- `func(string) T1` that early
                        -- `collectFuncTypesWith` would have erased to
                        -- `func(string) any`. Critical for closing the
                        -- `rt.Coerce[func(string) any](Msg_Ctor)`
                        -- adapter class.
                        [ ( prefix ++ "_" ++ goSafeName n
                          , splitInferredSigWithReg earlyAllRecAliases earlyAllFieldIdx (countParamsFor n depMod) sigTy )
                        | (n, ty) <- Map.toList depTypes
                        , let sigTy = case annotationTypeFor n depMod of
                                Just annTy -> annTy
                                Nothing -> ty
                        ]
                    | (modName, depTypes) <- depSolved
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    , let depMod = head [ m | (mn, m) <- validDeps, mn == modName ]
                    ]
            let
                depInferredParams = Map.map (\(_, ps, _) -> ps) fullSigs
                depInferredRets   = Map.map (\(_, _, r) -> r)  fullSigs
                depInferredSigs   = fullSigs
                -- v0.13 Phase A5+: per-function SkyName→GoName mapping
                -- for the SAME dep functions.  Drives the call-site
                -- coercion path so `CallInstance.quantifiers`
                -- (annotation Forall names) project to the Go-generic
                -- names that actually appear in the dep's emitted
                -- signature.
                -- Critical: align Sky-TVar names with what the
                -- solver's CForeign captures.  Externals are stored
                -- via `generaliseToAnnotation` which renames solver-
                -- internal names (`_arg_47`) to user-friendly ones
                -- (`a, b, c`); the CForeign's instantiation then
                -- uses those user-friendly names as `quants`.  If
                -- we computed `inferredSigSkyToGo` on the un-
                -- renamed type, the SkyNames would be the solver-
                -- internal forms and the lookup in `coerceCallArgsAt`
                -- would always miss.  Rename first.
                renameTypeForExternal t =
                    case generaliseToAnnotation t of
                        T.Forall _ renamed -> renamed
                depSkyToGoTVars = Map.unions
                    [ Map.fromList
                        [ ( prefix ++ "_" ++ goSafeName n
                          , inferredSigSkyToGo
                                earlyAllRecAliases earlyAllFieldIdx
                                (countParamsFor n depMod)
                                (renameTypeForExternal ty) )
                        | (n, ty) <- Map.toList depTypes
                        , not (hasAnnotation n depMod)
                        ]
                    | (modName, depTypes) <- depSolved
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    , let depMod = head [ m | (mn, m) <- validDeps, mn == modName ]
                    ]
            putStrLn $ "   HM infer (deps): "
                ++ show (Map.size depInferredParams) ++ " functions typed"
            -- Merge each dep module's solvedTypes into the global
            -- _cg_solvedTypes so dep-body codegen sees per-function
            -- locals (params, let-binders, case-binders) for typed-
            -- kernel routing. v0.12.x Gap 3 close-out: without this,
            -- `togglePostUpvote post` looks up `post` in the entry
            -- module's solvedTypes and finds nothing, falling back
            -- to any-routing.
            --
            -- Name collisions: if multiple deps have a same-named
            -- local, Map.union takes the first; the typed-routing
            -- check `if elemGo == "any" then Nothing` then gracefully
            -- falls back when the wrong type causes a mismatch. Safe.
            -- (Per-dep types are merged via the caller's
            -- `typesWithDeps` pass-through; see line ~722. The
            -- modifyIORef here only registers per-function sig data
            -- that subsequent codegen passes consult.)
            modifyIORef globalCgEnv $ \e -> e
                -- v0.13 Stage 1 — depInferredParams/Rets are HM-inferred
                -- AFTER typecheck and preserve TVar names (`func(string)
                -- T1`). The pre-typecheck `collectFuncTypesWith`
                -- entries use `safeReturnTypeWith` which ERASES TVars
                -- to `any` (`func(string) any`). Union order matters:
                -- the typed (post-HM) entries must WIN over the early
                -- erased entries so call-site σ-recovery can pin
                -- TVars from typed args. `Map.union` keeps the LEFT
                -- side, so put depInferredParams first.
                { Rec._cg_funcParamTypes =
                    Map.union depInferredParams (Rec._cg_funcParamTypes e)
                , Rec._cg_funcRetType =
                    Map.union depInferredRets (Rec._cg_funcRetType e)
                , Rec._cg_funcInferredSigs =
                    Map.union (Rec._cg_funcInferredSigs e) depInferredSigs
                , Rec._cg_funcSkyToGoTVars =
                    Map.union (Rec._cg_funcSkyToGoTVars e) depSkyToGoTVars
                }
            -- Bail BEFORE codegen if HM rejected the program. Previously
            -- "-- Generating Go" printed unconditionally, which made the
            -- "TYPE ERROR" buried two lines up easy to miss + suggested
            -- the build was succeeding. We also delete any stale main.go
            -- and binary from a previous successful build so the user
            -- can't accidentally run an outdated executable. Issue #52.
            case (solverError, exhaustErr) of
              (Just _, _) -> do
                  removeStaleBuildOutput outDir (Toml._binName config)
                  -- v0.13 Layer 1: the structured Diagnostic has
                  -- already been rendered above (renderCli at the
                  -- SolveError branch, or the verbatim solver-
                  -- budget block). Return a one-line marker so the
                  -- outer caller can surface non-zero exit + a
                  -- stable grep target ("Type error") without
                  -- double-printing the full body.
                  return (Left ("Type error: " ++ entryPath))
              (_, Just _) -> do
                  removeStaleBuildOutput outDir (Toml._binName config)
                  -- v0.13 Layer 1: the structured Diagnostic block
                  -- has been rendered above (one per non-exhaustive
                  -- branch).  Return a one-line marker; the renderer
                  -- already shows where and how to fix each case.
                  return (Left ("Non-exhaustive patterns: " ++ entryPath))
              _ -> do
                putStrLn "-- Generating Go"
                -- v0.13 Stage 1 (task #189) — emit dep-module decls
                -- AFTER typecheck has populated `funcSkyToGoTVars`.
                -- This is the critical point: dep bodies use
                -- `withScopedLambdaTypes` to register typed func-
                -- param names, which `goExprGoType` then resolves
                -- via `solvedTypeToGo` with Go-side TVar names
                -- (T1, T2, ...). Without the env populated, the
                -- skyToGo rename produces empty σ and func types
                -- render as `func(any) any`.
                let depDecls = concatMap (\(modName, depMod) ->
                        let prefix = map (\c -> if c == '.' then '_' else c) modName
                        in generateDeclsForDep depMod prefix) validDeps
                let depAliasPairs = [ (map (\c -> if c == '.' then '_' else c) mn, Can._aliases depMod)
                                    | (mn, depMod) <- validDeps ]
                    -- Conflict-detection merge with TVar normalisation.
                    -- See typesWithDepsBuilder below for the algorithm.
                    typesWithDeps =
                        -- v0.15.x P37a: `types` is now the new
                        -- `Solve.SolvedTypes` record.  The merge logic
                        -- below works over the `_stEnv` Map view;
                        -- after merging, we re-wrap with the
                        -- `mergedRegionTys` region map computed
                        -- earlier so `generateGoMulti` continues to
                        -- receive a full `Solve.SolvedTypes` value.
                        let typesEnv = Solve._stEnv types
                            entryKeys = Map.keysSet typesEnv
                            allMaps = typesEnv : [t | (_, t) <- depSolved]
                            keyToTypes = Map.unionsWith (++)
                                [ Map.map (:[]) m | m <- allMaps ]
                            isResolved (T.TVar _) = False
                            isResolved _ = True
                            normaliseType = normaliseTypeForMerge
                            -- Bug fix: a key in `entryKeys` may have come
                            -- from the entry module's `_locals` ledger
                            -- (lambda params / inner-let bindings) rather
                            -- than from a real top-level binding. When the
                            -- same key also exists in a DEP module's
                            -- solvedTypes — and disagrees structurally —
                            -- the dep version is the genuine top-level
                            -- declaration of THAT module; the entry's is
                            -- pollution from a same-named local.
                            -- Concrete case: Main.sky has functions like
                            -- `fetchLogs parent filter = …` whose lambda
                            -- param `filter : LogFilter` leaks into
                            -- solvedTypes; Sky.Core.List.filter (the real
                            -- HOF) gets shadowed and View.sky's
                            -- `List.filter (...)` then infers as LogFilter.
                            -- Detect: if entry's type AND any dep's type
                            -- disagree structurally, run the ambiguity
                            -- pipeline (treat as cross-scope conflict).
                            resolveKey k tys
                                | k `Set.member` entryKeys =
                                    let entryTy = Map.findWithDefault (T.TVar "_unbound") k typesEnv
                                        depTys  = [ t | (_, m) <- depSolved
                                                      , Just t <- [Map.lookup k m]
                                                      , isResolved t ]
                                        allCands = if isResolved entryTy
                                                       then entryTy : depTys
                                                       else depTys
                                        normalisedAll = List.nub
                                            (map normaliseType allCands)
                                    in case normalisedAll of
                                        []    -> entryTy
                                        [_]   -> entryTy
                                        _     -> T.TVar "_ambig"
                                | otherwise =
                                    let resolved = filter isResolved tys
                                        normalised = List.nub (map normaliseType resolved)
                                    in case normalised of
                                        []  -> T.TVar "_unresolved"
                                        [_] -> case resolved of
                                                 (t:_) -> t
                                                 []    -> T.TVar "_unresolved"
                                        _   -> T.TVar "_ambig"
                            mergedEnv = Map.mapWithKey resolveKey keyToTypes
                        in Solve.SolvedTypes mergedEnv mergedRegionTys
                    goCodeRaw = generateGoMulti canMod entrySrcMod config typesWithDeps depDecls depRecAliases depUnionNames depEnumNames depArities depParamTypes depRetTypes depUltRetTypes depInferredParams depInferredRets depInferredSigs depAliasPairs
                    -- v0.13 Layer 2: collect Sky-name → source-region
                    -- for every top-level declaration so the post-emit
                    -- pass can inject `// SKY-ORIGIN: <path>:<line>:<col>`
                    -- comments next to the matching Go function decl.
                    -- The validator + `go build` error refiner consult
                    -- the resulting OriginMap to map Go-line errors back
                    -- to Sky source.
                    declOriginMap = collectDeclOrigins entryPath canMod
                    goCode = Validator.injectOriginComments
                                declOriginMap goCodeRaw
                    -- v0.15.12 P5 / Gap A6 — Auth typed-boundary gate.
                    -- Scan the entry module + every validated dep for
                    -- Auth.* kernel call sites whose String-typed
                    -- slots receive an `any`-typed argument. The
                    -- check runs BEFORE codegen writes main.go so
                    -- the user sees the soundness gap at the source
                    -- instead of a runtime `mustStringTyped` failure
                    -- on the request hot path.
                    authDiagsEntry = authBoundaryDiagnostics
                                        entryPath typesWithDeps canMod
                    authDiagsDeps =
                        [ d
                        | (modName, depMod) <- validDeps
                        -- v0.15.x P37a: `depSolved` was extracted via
                        -- `Solve._stEnv` at the assembly site; wrap
                        -- back into a `SolvedTypes` record for the
                        -- auth-boundary helper (region map empty —
                        -- the gate only consults the env).
                        , let depTypesMap = case lookup modName depSolved of
                                Just ts -> Solve.SolvedTypes ts Map.empty
                                Nothing -> Solve.emptySolvedTypes
                        , d <- authBoundaryDiagnostics entryPath depTypesMap depMod
                        ]
                    authDiags = authDiagsEntry ++ authDiagsDeps
                if not (null authDiags)
                  then do
                      rendered <- Render.renderCliMany authDiags
                      putStrLn rendered
                      removeStaleBuildOutput outDir (Toml._binName config)
                      return (Left ("Sky.Auth.UntypedBoundary: " ++ entryPath))
                  else do
                    createDirectoryIfMissing True outDir
                    let mainGoPath = outDir </> "main.go"
                    writeFile mainGoPath goCode
                    putStrLn $ "   Wrote " ++ mainGoPath
                    -- v0.13 Layer 2: codegen-stage validator runs after
                    -- writing main.go but before any downstream tooling
                    -- (DCE / go build).  It scans the emitted Go for
                    -- known-bad shapes (typed-kernel call with raw any
                    -- arg, etc.) and emits a structured Diagnostic with
                    -- a Sky-source region if the bug shape is found.
                    -- This gives "if it compiles, it works" defence in
                    -- depth — even if a new codegen regression slips
                    -- past the cabal tests, the validator catches it
                    -- pre-build and prints an actionable Diagnostic
                    -- instead of a cryptic `go build` error.
                    let originMap = Validator.parseOriginComments goCode
                        valDiags  = Validator.validateEmittedGo
                                      mainGoPath originMap goCode
                    if not (null valDiags)
                      then do
                          rendered <- Render.renderCliMany valDiags
                          putStrLn rendered
                          removeStaleBuildOutput outDir (Toml._binName config)
                          return (Left "Codegen validation rejected the emitted Go")
                      else do
                          -- copyRuntime also copies runtime-go/go.mod + go.sum into
                          -- outDir when it can locate the runtime. Only fall back
                          -- to a minimal go.mod here if copyRuntime didn't write
                          -- one (no runtime found).
                          copyRuntime outDir
                          hasOutMod <- doesFileExist (outDir </> "go.mod")
                          if not hasOutMod
                              then writeFile (outDir </> "go.mod") $ unlines ["module sky-app", "", "go 1.21"]
                              else return ()
                          -- Pull in Go deps declared in sky.toml so generated
                          -- ffi/*_bindings.go can resolve imports.
                          seedGoDependencies outDir (Toml._goDeps config)
                          -- P7: strip unreferenced FFI wrappers from
                          -- sky-out/rt/*_bindings.go.  Tens of thousands of
                          -- any/any wrapper bodies user code never calls
                          -- (stripe alone contributes 74k).
                          dceFfiWrappers outDir
                          -- Write cache hash to enable incremental rebuild skip
                          let cacheDir = ".skycache"
                          createDirectoryIfMissing True cacheDir
                          writeFile (cacheDir </> "source.hash") srcHash
                          putStrLn "Compilation successful"
                          return (Right mainGoPath)


-- LEGACY: single-module parse entry (no longer used from compile)
parseSingle :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
parseSingle config entryPath outDir = do
    source <- TIO.readFile entryPath
    putStrLn $ "-- Lexing " ++ entryPath
    putStrLn "-- Parsing"
    case Parse.parseModule source of
        Left err -> do
            putStrLn $ "   PARSE FAILED: " ++ show err
            return (Left $ "Parse error: " ++ show err)
        Right srcMod -> do
            let modName = case Src._name srcMod of
                    Just (A.At _ names) -> concatMap id names
                    Nothing -> "Main"
                declCount = length (Src._values srcMod) + length (Src._unions srcMod) + length (Src._aliases srcMod)
            putStrLn $ "   Module: " ++ modName
            putStrLn $ "   " ++ show declCount ++ " declarations"

            -- Phase 3: Canonicalise
            putStrLn "-- Canonicalising"
            case Canonicalise.canonicalise srcMod of
                Left err -> do
                    putStrLn $ "   CANONICALISE FAILED: " ++ err
                    return (Left $ "Canonicalise error: " ++ err)
                Right canMod -> do
                    putStrLn "   Names resolved"

                    -- Phase 4: Type Check
                    putStrLn "-- Type Checking"
                    constraints <- Constrain.constrainModule canMod
                    solveResult <- Solve.solve constraints
                    let solvedTypes = case solveResult of
                            Solve.SolveOk types -> do
                                let envMap = Solve._stEnv types
                                putStrLn $ "   Types OK (" ++ show (length (Map.keys envMap)) ++ " bindings)"
                                mapM_ (\(n, t) -> putStrLn $ "     " ++ n ++ " : " ++ Solve.showType t) (Map.toList envMap)
                                return types
                            Solve.SolveError err -> do
                                putStrLn $ "   TYPE WARNING: " ++ err
                                -- Still return empty types — codegen falls back to any
                                return Solve.emptySolvedTypes
                    types <- solvedTypes

                    -- Phase 5: Generate Go (using solved types)
                    putStrLn "-- Generating Go"
                    let goCode = generateGo canMod srcMod config types

                    -- Phase 6: Write output
                    createDirectoryIfMissing True outDir
                    let mainGoPath = outDir </> "main.go"
                    writeFile mainGoPath goCode
                    putStrLn $ "   Wrote " ++ mainGoPath

                    -- Copy runtime package
                    copyRuntime outDir

                    -- Write go.mod
                    let goModPath = outDir </> "go.mod"
                    writeFile goModPath $ unlines
                        [ "module sky-app"
                        , ""
                        , "go 1.21"
                        ]

                    putStrLn "Compilation successful"
                    return (Right mainGoPath)


-- | Copy user FFI files from ./ffi/*.go into sky-out/rt/ so they compile into
-- the same Go package as the runtime. Users call `rt.Register` from init() in
-- these files to expose Go functions to Sky via Ffi.call "name" args.
-- | Delete any main.go and binary from a previous successful build so
-- a user who runs `sky run` after a failed `sky build` doesn't
-- accidentally execute outdated code. Issue #52: a build that hits
-- a TYPE ERROR used to leave the previous successful binary in place,
-- which let users miss the error and run stale output.
-- | v0.13 Layer 2: collect a Sky-name → (path, line, col) map
-- from a canonical module's top-level declarations.  The map is
-- used by `Validator.injectOriginComments` to seed SKY-ORIGIN
-- comments into the emitted Go output.
--
-- Key choice: the EMITTED Go function name.  For most entry-module
-- decls this is the bare Sky name (`update`, `view`).  Auto-record
-- constructors share the type-alias name (`Model_R` for the struct,
-- `Model` for the ctor).  We register both forms so the injector
-- finds the match regardless of which side `func` it lands on.
collectDeclOrigins :: FilePath -> Can.Module -> Map.Map String (FilePath, Int, Int)
collectDeclOrigins path canMod =
    let defs = collectDefs (Can._decls canMod)
        decls = mapMaybe (\d -> case d of
            Can.Def     (A.At reg name) _ _     -> Just (name, regionStart reg)
            Can.TypedDef (A.At reg name) _ _ _ _ -> Just (name, regionStart reg)
            Can.DestructDef _ _                  -> Nothing) defs
    in Map.fromList
        [ (name, (path, line, col))
        | (name, (line, col)) <- decls ]
  where
    collectDefs :: Can.Decls -> [Can.Def]
    collectDefs (Can.Declare d rest)     = d : collectDefs rest
    collectDefs (Can.DeclareRec d ds rest) = d : ds ++ collectDefs rest
    collectDefs Can.SaveTheEnvironment   = []

    regionStart (A.Region (A.Position l c) _) = (l, c)


removeStaleBuildOutput :: FilePath -> String -> IO ()
removeStaleBuildOutput outDir binName = do
    let mainPath = outDir </> "main.go"
        binPath  = outDir </> binName
    mainExists <- doesFileExist mainPath
    when mainExists $ removeFile mainPath
    binExists <- doesFileExist binPath
    when binExists $ removeFile binPath


-- | Render Elm-style source context for a type error. Parses the
-- LINE:COL: prefix from the error message, reads the source file,
-- and prints 2 lines before + the offending line + a caret line.
--
-- Example output (after the existing TYPE ERROR line):
--
--   13 |     update : Int -> M -> M
--   14 |     update i m =
--   15 |         { m | n = String.fromInt (i + 1) }
--                            ^
--
-- Silent on parse failure / file-read failure — the existing
-- TYPE ERROR line has already been printed, so user still sees
-- where the error is even without the snippet.
renderSourceContext :: FilePath -> String -> IO ()
renderSourceContext path errMsg = do
    case parseLineCol errMsg of
        Nothing -> return ()
        Just (lineN0, colN0) -> do
            srcExists <- doesFileExist path
            when srcExists $ do
                src <- readFile path
                let allLines = lines src
                    totalLines = length allLines
                -- If the error message mentions `field 'X'` (from the
                -- record-diff renderer), re-point the caret to the
                -- LINE where `X = ...` appears in the source, within
                -- a small window of the original line. This makes
                -- TEA cfg errors land on the offending field, not on
                -- the cfg literal's opening brace.
                let (lineN, colN) = case extractFieldName errMsg of
                        Just fname ->
                            case findFieldLine allLines lineN0 fname of
                                Just (lN, cN) -> (lN, cN)
                                Nothing       -> (lineN0, colN0)
                        Nothing -> (lineN0, colN0)
                when (lineN >= 1 && lineN <= totalLines) $ do
                    let startLine = max 1 (lineN - 2)
                        endLine   = min totalLines (lineN + 1)
                        contextLines = take (endLine - startLine + 1)
                                            (drop (startLine - 1) allLines)
                        gutterWidth = length (show endLine)
                        padNum n = replicate (gutterWidth - length (show n)) ' ' ++ show n
                    putStrLn ""
                    mapM_ (\(n, l) -> do
                        putStrLn $ "   " ++ padNum n ++ " | " ++ l
                        when (n == lineN) $ do
                            let caret = replicate (colN - 1) ' ' ++ "^"
                            putStrLn $ "   " ++ replicate gutterWidth ' '
                                     ++ " | " ++ caret)
                        (zip [startLine..] contextLines)
                    putStrLn ""


-- | Extract the first field name from a record-diff error message.
-- The renderer emits "in field `X` → ..." or "field `X`:" — we want
-- the X.
extractFieldName :: String -> Maybe String
extractFieldName s =
    case findSubstring "field `" s of
        Just rest -> case break (== '`') rest of
            (name, _) | not (null name) -> Just name
            _ -> Nothing
        Nothing -> Nothing
  where
    findSubstring needle haystack
        | needle `List.isPrefixOf` haystack = Just (drop (length needle) haystack)
        | null haystack = Nothing
        | otherwise = findSubstring needle (tail haystack)


-- | Find the line where `fname = ...` or `, fname = ...` appears in
-- the source, starting from `aroundLine` and scanning forward a few
-- lines (TEA cfg literals are typically 6-12 lines). Returns the
-- (line, column) where `fname` starts. Nothing if no match.
findFieldLine :: [String] -> Int -> String -> Maybe (Int, Int)
findFieldLine srcLines aroundLine fname =
    let window = take 30 (drop (max 0 (aroundLine - 1)) srcLines)
        indexed = zip [aroundLine..] window
    in firstJust (map findOnLine indexed)
  where
    firstJust = foldr ((<|>) . id) Nothing
    (<|>) Nothing y = y
    (<|>) x       _ = x
    findOnLine (n, l) =
        -- Match `fname =` or `, fname =` or `{ fname =`. Skip leading
        -- whitespace + optional `{` or `,`. The field name must be
        -- followed by `=` (with optional whitespace).
        case dropWhile (`elem` " \t,{") l of
            rest | take (length fname) rest == fname ->
                let afterName = drop (length fname) rest
                in case dropWhile (== ' ') afterName of
                    '=':_ ->
                        -- Compute column: 1-based from line start.
                        let col = length l - length rest + 1
                        in Just (n, col)
                    _ -> Nothing
            _ -> Nothing


-- | Parse `LINE:COL:` from the head of a type-error message.
-- Returns Just (line, col) on success.
parseLineCol :: String -> Maybe (Int, Int)
parseLineCol s =
    case break (== ':') (dropWhile (== ' ') s) of
        (lineStr, ':':rest)
          | not (null lineStr), all (\c -> c >= '0' && c <= '9') lineStr ->
            case break (== ':') rest of
                (colStr, _)
                  | not (null colStr), all (\c -> c >= '0' && c <= '9') colStr ->
                    Just (read lineStr, read colStr)
                _ -> Nothing
        _ -> Nothing


-- | Run `go get <pkg>[@<ver>]` for each Go dependency declared in sky.toml.
-- Runs after runtime + ffi copy so imports in generated ffi/*_bindings.go
-- resolve before the final `go build`. Skipped stdlib pkgs (no slash).
seedGoDependencies :: FilePath -> [(String, String)] -> IO ()
seedGoDependencies outDir deps = do
    hasMod <- doesFileExist (outDir </> "go.mod")
    if not hasMod || null deps
        then return ()
        else do
            let external = filter (\(p, _) -> '/' `elem` p || '.' `elem` p) deps
            when (not (null external)) $
                putStrLn $ "   resolving " ++ show (length external) ++ " Go dep(s)"
            mapM_ (goGet outDir) external
            _ <- System.Process.readProcessWithExitCode
                    "sh" ["-c", "cd " ++ outDir ++ " && go mod tidy 2>&1"] ""
            return ()
  where
    goGet dir (pkg, ver) =
        let target = if ver == "" || ver == "latest"
                        then pkg
                        else pkg ++ "@" ++ ver
            cmd = "cd " ++ dir ++ " && go get " ++ target ++ " 2>&1"
        in do
            (ec, out, _) <- System.Process.readProcessWithExitCode "sh" ["-c", cmd] ""
            case ec of
                System.Exit.ExitSuccess -> return ()
                _ -> putStrLn $ "      go get " ++ target ++ " FAILED: " ++ out


-- | P7 FFI DCE: strip unused `func Go_...` wrapper bodies from the
-- copied-into-sky-out bindings files. Walks `sky-out/main.go` plus
-- every other Go file outside `sky-out/rt/` for `rt.Go_<name>(` call
-- sites, collects the reachable wrapper names (including typed `*T`
-- companions referenced through the compile's call-site migration),
-- then rewrites each `sky-out/rt/*_bindings.go` keeping only those
-- wrapper bodies. Imports + header comments are preserved as-is —
-- Go's compiler is happy to see unused imports as long as some
-- blank `_` import retains them, which every bindings file already
-- does via the `// Pin fmt against ...` footer.
--
-- Stripe alone goes from ~74k wrapper bodies to a few dozen; the
-- `grep 'func [A-Z][a-zA-Z0-9_]*(p0 any' examples/*/ffi/*.go` gate
-- is exercised against the ffi/ source files (not sky-out/rt), so
-- this DCE also preserves those files' size.
dceFfiWrappers :: FilePath -> IO ()
dceFfiWrappers outDir = do
    let rtDir = outDir </> "rt"
    rtExists <- doesDirectoryExist rtDir
    if not rtExists then return () else do
        -- Collect caller-side referenced names.
        nonRtFiles <- collectNonRtGoFiles outDir
        referenced <- foldr1Concat nonRtFiles collectRtReferences Set.empty
        putStrLn $ "   [DCE] caller-side rt.* identifiers: " ++ show (Set.size referenced)
        -- Binding files to prune are those starting with a Go-package slug.
        -- We DO NOT touch hand-maintained rt.go / live.go / db_auth.go /
        -- stdlib_extra.go / stdlib_web.go / live_store.go — those are the
        -- runtime, not FFI generator output.
        rtEntries <- listDirectory rtDir
        let bindingFiles =
                [ rtDir </> e
                | e <- rtEntries
                , takeExtension e == ".go"
                , "_bindings.go" `isSuffixOfPlain` e
                ]
        mapM_ (pruneBindingFile referenced) bindingFiles
  where
    isSuffixOfPlain suf s =
        length s >= length suf && drop (length s - length suf) s == suf

    foldr1Concat :: [FilePath]
                 -> (FilePath -> IO (Set.Set String))
                 -> Set.Set String -> IO (Set.Set String)
    foldr1Concat files f acc0 = do
        sets <- mapM f files
        return (foldr Set.union acc0 sets)


-- | List every `*.go` file under `outDir` that is NOT inside `outDir/rt`.
collectNonRtGoFiles :: FilePath -> IO [FilePath]
collectNonRtGoFiles outDir = do
    entries <- listDirectory outDir
    let direct = [ outDir </> e
                 | e <- entries
                 , takeExtension e == ".go"
                 ]
    return direct


-- | Find every `rt.<Name>(` identifier in a Go source file.
collectRtReferences :: FilePath -> IO (Set.Set String)
collectRtReferences fp = do
    ok <- doesFileExist fp
    if not ok then return Set.empty else do
        content <- readFile' fp
        return (Set.fromList (extractRtIdents content))


-- | Scan a Go source text for `rt.<NAME>` references. Returns just
-- the NAME half (no `rt.` prefix). Walks character by character,
-- matching the substring `rt.` preceded by a non-identifier byte —
-- so identifiers like `skyRtValue` don't false-match.
extractRtIdents :: String -> [String]
extractRtIdents src = go Nothing src
  where
    -- prev tracks the character immediately before the current cursor,
    -- used to rule out `rt.` inside a longer identifier.
    go _ [] = []
    go prev ('r':'t':'.':rest)
        | not (isIdentChar (unwrap prev))
        , (name, after) <- span isIdentChar rest
        , not (null name)
        = name : go (Just (lastOf name)) after
    go _ (c:cs) = go (Just c) cs

    unwrap Nothing  = ' '
    unwrap (Just c) = c
    lastOf = last

    isIdentChar c = (c >= 'a' && c <= 'z')
                 || (c >= 'A' && c <= 'Z')
                 || (c >= '0' && c <= '9')
                 || c == '_'


-- | Rewrite a bindings file keeping only functions that are (a) in the
-- referenced set OR (b) a `T`-suffix variant of a referenced function.
-- Preserves the file's package declaration, imports, and any top-level
-- var declarations.
pruneBindingFile :: Set.Set String -> FilePath -> IO ()
pruneBindingFile referenced fp = do
    content <- readFile' fp  -- strict read to release the handle before write
    let newContent = pruneBindingsText referenced content
    if newContent == content then return ()
        else writeFile fp newContent


pruneBindingsText :: Set.Set String -> String -> String
pruneBindingsText referenced src =
    let ls = lines src
        (header, body) = splitAfterImportBlock ls
        kept0 = pruneFuncs referenced body
        -- v0.13 F3: after wrapper-body pruning, the FFI type aliases
        -- (`type FfiT_Go_Stripe_xxx_P0 = *pkg.Foo`) emitted alongside
        -- the wrappers become orphan when their owning wrapper was
        -- dropped. Pre-fix, skyshop's stripe_bindings.go retained
        -- 80,847 orphan `type FfiT_*` decls (one per param/return
        -- position of every Stripe FFI fn that DCE removed). Each
        -- alias is a single line; we keep only those whose name
        -- still appears in the kept body OR in the caller-side
        -- `referenced` set (main.go can reference `rt.FfiT_*`
        -- aliases directly at typed call sites).
        kept = pruneOrphanFfiTypes referenced kept0
        -- After stripping function bodies, some package aliases may no
        -- longer appear in the remaining source. Go rejects `imported
        -- and not used`, so rewrite each import to a blank `_` form
        -- when its alias no longer appears anywhere in the kept body.
        rewrittenHeader = rewriteImportsForDCE header kept
    in unlines (rewrittenHeader ++ kept)


-- | v0.13 F3: drop `type FfiT_*` aliases whose name no longer
-- appears anywhere in the surviving body OR in the caller-side
-- `referenced` set. Each alias is a one-line decl. After
-- `dceFfiWrappers` drops unused wrapper bodies, the type aliases
-- emitted alongside them (one per param/return type position)
-- become orphan. Stripe alone leaves ~80k orphan FfiT_* aliases —
-- each is harmless (Go link-time DCE drops them) but they triple
-- the source size and slow `go build`'s parse phase.
--
-- `referenced` is the set of `rt.<NAME>` identifiers found in
-- caller-side `.go` files (main.go and siblings outside `rt/`).
-- A `FfiT_*` alias that main.go references directly via
-- `rt.FfiT_Go_Stripe_X_P0(...)` must stay alive even when none of
-- the surviving binding-file funcs uses it.
--
-- Algorithm: O(blob + Σ name_lengths). Pre-compute a `Set String`
-- of every identifier token appearing in non-FfiT lines, then per
-- alias do an O(log N) membership test. The naive `isInfixOf` per
-- alias was O(blob × n_aliases × name_length) which on Stripe
-- scales to ~7TB of char comparisons — hung the build.
pruneOrphanFfiTypes :: Set.Set String -> [String] -> [String]
pruneOrphanFfiTypes referenced ls =
    let nonTypeLines = [ l | l <- ls, ffiTypeName l == Nothing ]
        usedIdents = Set.fromList
            (concatMap extractIdents nonTypeLines)
        keepLine l = case ffiTypeName l of
            Just n  -> Set.member n usedIdents
                    || Set.member n referenced
            Nothing -> True
    in filter keepLine ls
  where
    -- `type FfiT_…` decls all match `type <Name> = …`. The Name
    -- must start with `FfiT_` to be eligible for this pruner.
    ffiTypeName :: String -> Maybe String
    ffiTypeName l
        | take 5 l == "type "
        , let rest = drop 5 l
        , take 5 rest == "FfiT_"
        , let (name, _) = span isGoIdentChar rest
        , not (null name)
        = Just name
        | otherwise = Nothing

    -- Tokenise a line into Go identifiers — Unicode-aware so a
    -- caller-side identifier containing non-ASCII letters still
    -- registers as a single token (Go allows Unicode letters in
    -- identifiers; ignoring that would slice the token in two and
    -- spuriously declare the FfiT alias unreferenced).
    extractIdents :: String -> [String]
    extractIdents = go
      where
        go [] = []
        go (c:rest)
            | isGoIdentStart c =
                let (tok, after) = span isGoIdentChar (c:rest)
                in tok : go after
            | otherwise = go rest


-- | Rewrite import lines inside the header so any alias that no longer
-- appears in `body` becomes a blank `_` import. Preserves ordering
-- and comments.
rewriteImportsForDCE :: [String] -> [String] -> [String]
rewriteImportsForDCE header body =
    let bodyBlob = unlines body
    in map (rewriteImportLine bodyBlob) header


rewriteImportLine :: String -> String -> String
rewriteImportLine bodyBlob line =
    case parseImportLine line of
        Just (indent, alias, path, trailer)
            | alias /= "" && alias /= "_"
            , not (aliasReferenced alias bodyBlob)
            -> indent ++ "_ \"" ++ path ++ "\"" ++ trailer
        _ -> line


-- | Parse `\t<alias> "<path>"<trailer>` or `\t"<path>"<trailer>`.
-- Returns (indent, alias, path, trailer); alias is "" for bare string
-- imports and we leave those alone.
parseImportLine :: String -> Maybe (String, String, String, String)
parseImportLine line =
    let (lead, rest) = span (\c -> c == '\t' || c == ' ') line
    in case rest of
        ('"':r) ->
            -- Bare string import: `"reflect"`. The effective alias is
            -- the last path segment (the Go package name, for import
            -- paths we handle here — all stdlib + github-style).
            let (path, closeRest) = break (== '"') r
            in case closeRest of
                ('"':trailer) ->
                    let segs = splitSlash path
                        alias = if null segs then "" else last segs
                    in Just (lead, alias, path, trailer)
                _ -> Nothing
        _ ->
            let (alias, afterAlias) = break (== ' ') rest
            in case dropWhile (== ' ') afterAlias of
                ('"':r) ->
                    let (path, closeRest) = break (== '"') r
                    in case closeRest of
                        ('"':trailer) ->
                            if null alias || all isAliasChar alias
                                then Just (lead, alias, path, trailer)
                                else Nothing
                        _ -> Nothing
                _ -> Nothing
  where
    isAliasChar c = (c >= 'a' && c <= 'z')
                 || (c >= 'A' && c <= 'Z')
                 || (c >= '0' && c <= '9')
                 || c == '_'

    splitSlash = foldr step [[]]
      where
        step '/' acc = [] : acc
        step c (cur:rest) = (c:cur) : rest
        step _ [] = [[]]


-- | `<alias>.` appearing as a substring of the body blob after
-- stripping `//` line comments. Imports rarely overlap with
-- identifier spelling by accident; ignoring comments avoids
-- false positives from orphaned `// Pkg.Name` docstrings
-- that DCE left behind after dropping their function body.
aliasReferenced :: String -> String -> Bool
aliasReferenced alias blob = (alias ++ ".") `List.isInfixOf` stripComments blob

-- | Remove `//` line comments from Go source (anything after `//`
-- up to the next newline). Leaves `/* ... */` block comments alone —
-- FfiGen doesn't emit them for wrapper docs.
stripComments :: String -> String
stripComments = go
  where
    go [] = []
    go ('/':'/':rest) = go (dropWhile (/= '\n') rest)
    go (c:rest) = c : go rest


-- | Return (lines up to & including closing `)` of the `import (` block,
-- everything after). Files without an `import` block return (all, []).
splitAfterImportBlock :: [String] -> ([String], [String])
splitAfterImportBlock = go []
  where
    go acc [] = (reverse acc, [])
    go acc (l:rest)
        | stripLeadingTabs l == "import (" =
            let (imports, after) = takeUntilCloseParen rest
            in (reverse acc ++ [l] ++ imports, after)
        | otherwise = go (l:acc) rest
    stripLeadingTabs = dropWhile (\c -> c == '\t' || c == ' ')

    takeUntilCloseParen = takeUntilCloseParenAcc []
    takeUntilCloseParenAcc acc [] = (reverse acc, [])
    takeUntilCloseParenAcc acc (l:rest)
        | stripLeadingTabs l == ")" = (reverse (l : acc), rest)
        | otherwise = takeUntilCloseParenAcc (l : acc) rest


-- | Walk body lines, keeping top-level `var` / `type` / comment
-- blocks intact; drop `func <Name>` definitions whose name is not in
-- the referenced set (or its `T`-suffix sibling isn't referenced).
pruneFuncs :: Set.Set String -> [String] -> [String]
pruneFuncs referenced inputLines = go [] inputLines
  where
    -- `pending` accumulates preceding comment-or-blank lines that
    -- belong to the NEXT declaration. If the declaration is kept we
    -- flush them; if dropped we discard them along with the func.
    go :: [String] -> [String] -> [String]
    go pending []   = reverse pending
    go pending (l:rest)
        | isCommentOrBlank l =
            go (l : pending) rest
        | Just name <- matchFuncStart l =
            let (body, after) =
                    if isOneLineFunc l
                        then ([], rest)
                        else takeFuncBody rest
                funcLines = l : body
                baseName = if not (null name) && last name == 'T'
                           then take (length name - 1) name
                           else ""
                isKept = Set.member name referenced
                      || (not (null baseName) && Set.member baseName referenced)
            in if isKept
                then reverse pending ++ funcLines ++ go [] after
                else go [] after
        | otherwise =
            -- non-func non-comment line (var, type, etc.): keep it,
            -- along with any pending preceding comments.
            reverse pending ++ [l] ++ go [] rest

    isCommentOrBlank l =
        let trimmed = dropWhile (\c -> c == ' ' || c == '\t') l
        in null trimmed || take 2 trimmed == "//"

    -- A `func` line is one-line when the brace count at end-of-line is
    -- zero AND the line closes (i.e., the final run of `{`s is
    -- balanced by corresponding `}`s on the same line). Detect by
    -- running a brace counter; if it ends at 0 after seeing at least
    -- one `{`, the function body was fully contained.
    isOneLineFunc l =
        let (depth, sawOpen) = walk l 0 False
        in sawOpen && depth == 0
      where
        walk :: String -> Int -> Bool -> (Int, Bool)
        walk [] d s       = (d, s)
        walk ('{':cs) d s = walk cs (d+1) True
        walk ('}':cs) d s = walk cs (d-1) s
        walk (_  :cs) d s = walk cs d s


-- | `func Name` (possibly with generic `[...]` and `(`). Return Just
-- the bare Name or Nothing if this isn't a top-level func line.
--
-- Identifier check uses Unicode-aware predicates (`Char.isLetter` +
-- `Char.isAlphaNum`) to match Go's identifier spec — `letter (letter
-- | unicode_digit)*` where `letter = unicode_letter | '_'`. Aligns
-- with `Sky.Parse.Variable.isIdentChar` (parser side).
matchFuncStart :: String -> Maybe String
matchFuncStart l
    | take 5 l == "func "
    , let rest = drop 5 l
    , not (null rest)
    , isGoIdentStart (head rest)
    = let (name, tail_) = span isGoIdentChar rest
      in if not (null name) && not (null tail_)
            && (head tail_ == '(' || head tail_ == '[')
         then Just name
         else Nothing
    | otherwise = Nothing


-- | Consume until we see a line beginning with `}` at indent 0. Return
-- (body-lines-up-to-and-including-close, remaining-lines).
takeFuncBody :: [String] -> ([String], [String])
takeFuncBody = go []
  where
    go acc [] = (reverse acc, [])
    go acc (l:rest)
        | take 1 l == "}" = (reverse (l : acc), rest)
        | otherwise       = go (l : acc) rest


copyFfiDir :: FilePath -> IO ()
copyFfiDir outDir = do
    let ffiDir = ".skycache/go"
        dstDir = outDir </> "rt"
    exists <- doesDirectoryExist ffiDir
    if not exists then return ()
        else do
            contents <- listDirectoryHs ffiDir
            let goFiles = filter isGoFile contents
            mapM_ (\f -> copyFile (ffiDir </> f) (dstDir </> f)) goFiles
  where
    isGoFile name = ".go" `isSuffixOfHs` name
    isSuffixOfHs suffix name =
        length name >= length suffix &&
        drop (length name - length suffix) name == suffix


listDirectoryHs :: FilePath -> IO [FilePath]
listDirectoryHs = listDirectory


-- | Copy the Go runtime package into the output directory.
-- Locates runtime-go/ via (in order):
--   1. SKY_RUNTIME_DIR env var (explicit override)
--   2. ./runtime-go (cwd-relative — for compiler dev)
--   3. <binary-dir>/../runtime-go (installed layout, binary in bin/)
--   4. <binary-dir>/../../runtime-go (cabal dist-newstyle layout)
--   5. Walk up from cwd looking for a haskell-compiler/runtime-go sibling
--   6. Fall back to inline runtimeGoSource string (hello-world only — misses
--      Live, DB, Auth, FFI, stdlib extras — most programs will fail at link)
copyRuntime :: FilePath -> IO ()
copyRuntime outDir = do
    let rtDir = outDir </> "rt"
    createDirectoryIfMissing True rtDir
    mRuntime <- locateRuntimeDir
    case mRuntime of
        Nothing -> writeEmbeddedRuntime outDir
        Just runtimeDir -> do
            let mainRt = runtimeDir </> "rt" </> "rt.go"
            mainExists <- doesFileExist mainRt
            if mainExists
                then copyFile mainRt (rtDir </> "rt.go")
                else writeFile (rtDir </> "rt.go") runtimeGoSource
            -- Copy every *.go file in runtime-go/rt/ AND every
            -- subdirectory (telemetry/, otel/, …) so new runtime
            -- modules are picked up automatically without hardcoding
            -- names. Subpackages were added in Phase 1.1a — the prior
            -- flat-listDirectory walk silently dropped them, causing
            -- `package sky-app/rt/telemetry is not in std` go-build
            -- failures because the embedded TH path DID copy the
            -- files but the dev-tree path didn't.
            let rtSourceDir = runtimeDir </> "rt"
            hasRtDir <- doesDirectoryExist rtSourceDir
            if hasRtDir
                then copyRuntimeRecursive rtSourceDir rtDir
                else return ()
            -- Copy go.mod and go.sum to inherit runtime dep versions.
            let srcMod = runtimeDir </> "go.mod"
            hasSrcMod <- doesFileExist srcMod
            if hasSrcMod then copyFile srcMod (outDir </> "go.mod") else return ()
            let srcSum = runtimeDir </> "go.sum"
            hasSum <- doesFileExist srcSum
            if hasSum then copyFile srcSum (outDir </> "go.sum") else return ()
    -- User FFI: copy ./ffi/*.go into sky-out/rt/ regardless of runtime-go location.
    copyFfiDir outDir


-- ═══════════════════════════════════════════════════════════
-- WORKSPACE TYPECHECK (for LSP)
-- ═══════════════════════════════════════════════════════════

-- | Per-module workspace typecheck result. Keys are dotted module
-- names ("Lib.Db", "Sky.Core.Error", "Main").
data WorkspaceTypecheck = WorkspaceTypecheck
    { _wt_modules :: Map.Map String WorkspaceModule
    , _wt_canonError :: Maybe (String, String)  -- (moduleName, error)
    }

data WorkspaceModule = WorkspaceModule
    { _wm_path        :: FilePath
    , _wm_src         :: Src.Module
    , _wm_canon       :: Can.Module
    , _wm_types       :: Map.Map String T.Type   -- top-level binding name → inferred type
    , _wm_localTypes  :: Map.Map String [T.Type] -- audit P2-2: innermost-first list per name (supports shadowing)
    , _wm_source      :: T.Text                  -- raw text for doc-comment scanning
    }


-- | LSP entry point: discover, parse, canonicalise and type-check the
-- entire workspace without running codegen. Honours the Sky stdlib
-- discovery root + Sky-source deps. Errors in any single module are
-- isolated — others continue so partial results are still useful for
-- hover/definition.
typecheckWorkspace :: Toml.SkyConfig -> FilePath -> IO WorkspaceTypecheck
typecheckWorkspace config entryPath = do
    let entryDir = takeDirectory entryPath
        sourceRoot = if Toml._sourceRoot config == "src"
            then entryDir
            else Toml._sourceRoot config
        -- Project root = parent of src/. Covers both absolute and
        -- relative entry paths (`src/Main.sky` → ".", common LSP case).
        projectRoot = case takeDirectory entryDir of
            "" -> "."
            d  -> d
    loadAndSeedFfiRegistry
    depRoots <- SkyDeps.installDeps (Toml._skyDeps config)
    -- Materialise stdlib inside `.skycache/` so it lives in the already-
    -- gitignored cache dir instead of polluting `src/`. LSP goto-def can
    -- still jump here — the path is stable per project — but nothing
    -- shows up under the user's source tree in `git status`.
    let stdlibSideDir = projectRoot </> ".skycache" </> "stdlib"
    stdlibRoot <- writeStdlibTo stdlibSideDir
    -- Resolve `tests/` against projectRoot, NOT cwd. When the LSP is
    -- launched from a different working directory (e.g. an editor
    -- spawned `sky lsp` from a global location while opening a file
    -- in /tmp/some-project), a bare "tests" path picks up whatever
    -- tests/ sits in cwd — typically the sky compiler repo's own
    -- tests/ — and floods the workspace index with foreign modules.
    -- Symptom: alias-name collisions silently win in
    -- `collectDepAliases` (left-biased Map.unions) and the open
    -- file's diagnostics reference unrelated record shapes
    -- ("Model vs Model" where one body is the user's and the other
    -- is from tests/Live/CounterTest.sky).
    let testsRootPath2 = projectRoot </> "tests"
    testsRootExists2 <- doesDirectoryExist testsRootPath2
    let extraTestsRoot2 = if testsRootExists2 then [testsRootPath2] else []
    -- Workspace discovery: seed module discovery with EVERY .sky file
    -- in the source roots + tests, not just the entry point. Without
    -- this, helper modules (Lib/Helper.sky, src/Foo/Bar.sky) that
    -- aren't transitively imported from Main.sky are invisible to
    -- the LSP — opening them gives no hover, no go-to-def, no
    -- diagnostics. Multi-file projects (sendcrafts and similar) hit
    -- this immediately. The compiler's own `compile` path keeps
    -- discoverModulesMulti (entry-only) since it builds the entry's
    -- transitive closure for codegen; the LSP's workspace index
    -- needs the broader view.
    extraSrcFiles <- Graph.listSkyFiles sourceRoot
    extraTestFiles <- if testsRootExists2
                      then Graph.listSkyFiles testsRootPath2
                      else return []
    -- Also seed stdlib + dep roots so their modules end up in the
    -- index regardless of whether the entry imports them. Critical
    -- when the entry has a parse error — skipping it loses its
    -- import graph, which used to mean stdlib symbols disappeared
    -- from the index for the duration of the broken state.
    stdlibFiles <- Graph.listSkyFiles stdlibRoot
    depFiles <- concat <$> mapM Graph.listSkyFiles depRoots
    let allSeeds = entryPath : extraSrcFiles ++ extraTestFiles
                              ++ stdlibFiles ++ depFiles
    -- Tolerant discovery: skip files with parse errors instead of
    -- aborting the whole workspace pass. Critical for the LSP path
    -- where the user may be editing a broken file at any time —
    -- the broken file should NOT kill hover/completion/diagnostics
    -- on every other file in the project.
    modules <- Graph.discoverModulesFromSeedsTolerant
        (sourceRoot : depRoots ++ extraTestsRoot2 ++ [stdlibRoot]) allSeeds
    let moduleOrder = Graph.compilationOrder modules

    -- Parse all
    parsed <- Async.forConcurrently moduleOrder $ \modInfo -> do
        src <- TIO.readFile (Graph._mi_path modInfo)
        return (modInfo, src, Parse.parseModule src)
    let okParsed =
            [ (Graph._mi_name mi, Graph._mi_path mi, src, m)
            | (mi, src, Right m) <- parsed
            ]

    -- First-pass canonicalise (per-module deps map)
    firstPass <- Async.forConcurrently okParsed $ \(n, _, _, srcMod) ->
        case Canonicalise.canonicalise srcMod of
            Right cm -> return (Just (n, cm))
            Left _   -> return Nothing
    let firstValid = [x | Just x <- firstPass]
        depInfoMap = Map.fromList
            [ (modName, Canonicalise.DepInfo
                { Canonicalise._dep_name = Can._name depMod
                , Canonicalise._dep_unions =
                    [ (typeName, Can._u_vars u, Can._u_alts u)
                    | (typeName, u) <- Map.toList (Can._unions depMod)
                    ]
                , Canonicalise._dep_aliases = Map.keys (Can._aliases depMod)
                , Canonicalise._dep_aliasDefs = Can._aliases depMod
                , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                , Canonicalise._dep_exports = Can._exports depMod
                })
            | (modName, depMod) <- firstValid
            ]

    -- Single-pass typecheck. The Index built from this gets pass-1
    -- types (one solver run per module, no cross-module externals).
    -- The LSP's runPipelineSt re-solves the OPEN file with externals
    -- derived from these pass-1 types — that's the path that catches
    -- cross-module mismatches (issue #52).
    --
    -- IMPORTANT: solveWithLocals returns ONLY the env-type entries,
    -- not the full set `solve` produces. `solve` merges innermost
    -- locals into the envTypes map (Solve.hs:168). We must do the
    -- same merge here, otherwise the workspace's types maps miss
    -- top-level decl entries that the solver tracked as let-
    -- bindings — which left the externals incomplete (Std.Ui's
    -- `layout`, `text`, `el`, etc. all missing).
    perMod <- Async.forConcurrently okParsed $ \(n, path, src, srcMod) ->
        case Canonicalise.canonicaliseWithDeps depInfoMap srcMod of
            Left err -> return (n, Left err, srcMod, path, src)
            Right canMod -> do
                cs <- Constrain.constrainModule canMod
                (r, localTys) <- Solve.solveWithLocals cs
                -- v0.15.x P37a: `SolvedTypes` is now a record; extract
                -- the bare env map for the `_wm_types` field (which
                -- carries the pre-P37a shape `Map.Map String T.Type`).
                let envTypes = case r of
                        Solve.SolveOk t -> Solve._stEnv t
                        _               -> Map.empty
                    -- Match Solve.solve's merge: take the innermost
                    -- (first) type from each local, merge under
                    -- envTypes (envTypes wins on collision).
                    localFirst = Map.map head (Map.filter (not . null) localTys)
                    types = Map.union localFirst envTypes
                return (n, Right (canMod, types, localTys), srcMod, path, src)

    let modMap = Map.fromList
            [ (n, WorkspaceModule
                { _wm_path        = path
                , _wm_src         = srcMod
                , _wm_canon       = canMod
                , _wm_types       = types
                , _wm_localTypes  = localTys
                , _wm_source      = src
                })
            | (n, Right (canMod, types, localTys), srcMod, path, src) <- perMod
            ]
        firstError = listToMaybeFirst
            [ (n, err) | (n, Left err, _, _, _) <- perMod ]

    return WorkspaceTypecheck
        { _wt_modules = modMap
        , _wt_canonError = firstError
        }
  where
    listToMaybeFirst []    = Nothing
    listToMaybeFirst (x:_) = Just x


-- | Variant of writeEmbeddedSkyStdlib that targets an arbitrary
-- destination, used by the LSP path which mirrors stdlib next to the
-- project source so jumps land on stable, user-visible paths.
--
-- Like `writeEmbeddedSkyStdlib`, clears the destination first so a
-- stdlib file dropped from the embed (a module deleted / renamed
-- between compiler builds) doesn't linger on disk and get
-- discovered as a phantom module.
writeStdlibTo :: FilePath -> IO FilePath
writeStdlibTo root = do
    exists <- doesDirectoryExist root
    when exists (System.Directory.removeDirectoryRecursive root)
    createDirectoryIfMissing True root
    mapM_ (writeOne root) embeddedSkyStdlib
    return root
  where
    writeOne base (relPath, bytes) = do
        let dst = base </> relPath
        createDirectoryIfMissing True (takeDirectory dst)
        BS.writeFile dst bytes


-- | Materialise the embedded Sky stdlib (Sky.Core.Error,
-- etc.) into <outDir>/.sky-stdlib/ at build start. Returns the root
-- path so `discoverModulesMulti` can probe it.
--
-- The destination is CLEARED first (not just written-over): when a
-- stdlib module is deleted or renamed between compiler builds, the
-- old materialised `.sky` file would otherwise linger and still be
-- discovered — a phantom module that shadows nothing but breaks
-- imports of the real (now-kernel-backed or removed) name. Always
-- rewritten so a compiler upgrade picks up the latest stdlib
-- without `sky clean`.
writeEmbeddedSkyStdlib :: FilePath -> IO FilePath
writeEmbeddedSkyStdlib outDir = do
    let root = outDir </> ".sky-stdlib"
    exists <- doesDirectoryExist root
    when exists (System.Directory.removeDirectoryRecursive root)
    createDirectoryIfMissing True root
    mapM_ (writeOne root) embeddedSkyStdlib
    return root
  where
    writeOne base (relPath, bytes) = do
        let dst = base </> relPath
        createDirectoryIfMissing True (takeDirectory dst)
        BS.writeFile dst bytes


-- | Recursively copy every .go file (including subdirectories) from
-- the runtime-go/rt source dir into the output rt/ dir. Skips the
-- already-handled `rt.go` at the top level (copied verbatim above
-- from the canonical source).
--
-- Why filter to .go only: the runtime source tree may contain
-- ancillary files (README.md, _test.go regression artefacts the dev
-- wants to keep local-only, etc.) that the released binary shouldn't
-- need. The embedded path (writeEmbeddedRuntime) doesn't have this
-- problem because embedDir already filters to what TH bundled at
-- compile time.
copyRuntimeRecursive :: FilePath -> FilePath -> IO ()
copyRuntimeRecursive src dst = do
    createDirectoryIfMissing True dst
    entries <- System.Directory.listDirectory src
    mapM_ (copyOne src dst) entries
  where
    copyOne s d name = do
        let srcPath = s </> name
        let dstPath = d </> name
        isDir <- doesDirectoryExist srcPath
        if isDir
            then copyRuntimeRecursive srcPath dstPath
            else when (isGoSource name && name /= "rt.go") $
                copyFile srcPath dstPath

    isGoSource name =
        let l = length name
        in l > 3 && drop (l - 3) name == ".go"


-- | Write the embedded runtime (bundled into the sky binary at TH-time)
-- to the output directory. Released binaries hit this path because there
-- is no runtime-go/ on disk; everything they need is already in the exe.
writeEmbeddedRuntime :: FilePath -> IO ()
writeEmbeddedRuntime outDir = do
    let rtDir = outDir </> "rt"
    createDirectoryIfMissing True rtDir
    debug <- System.Environment.lookupEnv "SKY_DEBUG_RUNTIME"
    let isDebug = debug == Just "1"
    when isDebug $
        putStrLn $ "[SKY_DEBUG_RUNTIME] embedded entries: "
                ++ show (length embeddedRuntime)
                ++ "  → outDir=" ++ outDir
    mapM_ (writeOne isDebug outDir rtDir) embeddedRuntime
  where
    writeOne isDebug base rtBase (relPath, bytes) = do
        let dst = case relPath of
                'r':'t':'/':rest -> rtBase </> rest
                _                -> base </> relPath
        createDirectoryIfMissing True (takeDirectory dst)
        res <- E.try (BS.writeFile dst bytes) :: IO (Either E.SomeException ())
        case res of
            Right _  -> return ()
            Left  ex ->
                -- Surface write failures even when debug is off — issue
                -- #58 was missed for ages because failures were silent.
                System.IO.hPutStrLn System.IO.stderr
                    ("[sky] WARN: failed to write " ++ dst
                  ++ ": " ++ show ex)
        when isDebug $ putStrLn $ "[SKY_DEBUG_RUNTIME] wrote " ++ dst


-- | Locate the runtime-go directory by probing known locations.
locateRuntimeDir :: IO (Maybe FilePath)
locateRuntimeDir = do
    envVar <- System.Environment.lookupEnv "SKY_RUNTIME_DIR"
    case envVar of
        Just p -> do
            ok <- doesDirectoryExist p
            if ok then return (Just p) else probeLocations
        Nothing -> probeLocations
  where
    probeLocations = do
        cands <- candidates
        firstExisting cands

    candidates = do
        cwd <- System.Directory.getCurrentDirectory
        exeDir <- fmap System.FilePath.takeDirectory System.Environment.getExecutablePath
        -- Walk up from the binary's dir (cabal dist-newstyle nests ~8 deep)
        -- and from cwd looking for an ancestor containing runtime-go/rt/.
        let upN n base = iterate (</> "..") base !! n
        return $
            "runtime-go"
            : [ upN n exeDir </> "runtime-go" | n <- [0..12] ]
            ++ [ upN n cwd </> "runtime-go" | n <- [0..12] ]

    firstExisting [] = return Nothing
    firstExisting (p:ps) = do
        ok <- doesDirectoryExist (p </> "rt")
        if ok then return (Just p) else firstExisting ps


-- ═══════════════════════════════════════════════════════════
-- GO CODE GENERATION (from Canonical AST)
-- ═══════════════════════════════════════════════════════════

-- | Generate Go declarations for a dependency module's functions
generateDeclsForDep :: Can.Module -> String -> [GoIr.GoDecl]
generateDeclsForDep canMod modPrefix =
    let userDefs = collectDeclNames (Can._decls canMod)
        -- v0.13 F: whole-program DCE. Drop dep-module decls that
        -- aren't transitively reachable from the entry module's
        -- `main`. Empty reached set → keep everything (DCE off via
        -- `SKY_DCE=0` or pre-canon-fixpoint code path).
        canonicalModName = ModuleName.toString (Can._name canMod)
        reached = unsafePerformIO (readIORef globalReachableProgram)
        dceOff  = unsafePerformIO (readIORef globalDceDisabled)
        keepName n =
            dceOff
            || Set.null reached
            || Set.member (Dce.TopRef canonicalModName n) reached
    in concatMap (generateUnionForDep modPrefix) (Map.toList (Can._unions canMod))
    ++ concatMap (generateAliasForDep userDefs modPrefix) (Map.toList (Can._aliases canMod))
    ++ go keepName (Can._decls canMod)
  where
    defName d = case d of
        Can.Def (A.At _ n) _ _          -> n
        Can.TypedDef (A.At _ n) _ _ _ _ -> n
        Can.DestructDef _ _             -> ""

    go _ Can.SaveTheEnvironment = []
    go keepName (Can.Declare def rest) = mkDef keepName def ++ go keepName rest
    go keepName (Can.DeclareRec def defs rest) =
        mkDef keepName def ++ concatMap (mkDef keepName) defs ++ go keepName rest

    mkDef keepName def = case def of
        Can.DestructDef _ _ -> []
        _ | not (keepName (defName def)) -> []
        _ ->
          let -- For TypedDef, the 5th field is the RETURN type only;
              -- per-pattern arg types live in `typedPats :: [(Pat, Type)]`.
              (name, params, body, mAnnotArgs, _mAnnotRet) = case def of
                Can.Def (A.At _ n) pats expr ->
                    (n, pats, expr, Nothing, Nothing)
                Can.TypedDef (A.At _ n) _ typedPats expr retTy ->
                    ( n
                    , map fst typedPats
                    , expr
                    , Just (map snd typedPats)
                    , Just retTy
                    )
                Can.DestructDef{} -> error "unreachable: filtered above"
              -- v0.13 Layer 3 fix: dep-emitted function names must
              -- pass through goSafeName so that Sky source files
              -- exposing a Go-keyword identifier (e.g. `map` in
              -- Sky.Core.Result) emit as `<mod>_map_` to match the
              -- call-site name mangling at line 3954.  Pre-fix the
              -- emit path used the raw Sky name, producing
              -- `Sky_Core_Result_map` while call sites looked for
              -- `Sky_Core_Result_map_` → `go build` undefined.
              goName = modPrefix ++ "_" ++ goSafeName name
              (goParams', destructStmts) = destructureParams params
              -- T3 (dep path): annotated dep functions get typed return.
              -- T2/T6 (dep path): typed params too. When no annotation
              -- exists, fall back to HM-inferred type. TVars become Go
              -- type parameters (T4b) so partially-inferred functions
              -- get typed generically instead of falling back to `any`.
              env = getCgEnv
              -- v0.13 typed lowerer: the sig tables (`_cg_funcInferredSigs`,
              -- `_cg_funcRetType`) are keyed with the `goSafeName`-mangled
              -- form (`Sky_Core_List_map_`, not `Sky_Core_List_map`) — see
              -- the `prefix ++ "_" ++ goSafeName n` key at the dep-sig
              -- population site.  The lookup key here MUST mangle too, or
              -- every Go-keyword-named dep function (`map`, `append`,
              -- `range`, `type`, …) misses the lookup and falls back to a
              -- fully-`any` signature.
              qualLookupName = modPrefix ++ "_" ++ goSafeName name
              -- Typed dep sigs: annotation or HM-inferred types.
              -- wrapTypedReturn coerces the body to match the return type.
              -- Re-export fallback: if the body is a single Call to another
              -- top-level value with a known typed signature, inherit its
              -- return type. Fixes the `foo = Other.foo` pattern where HM
              -- produced a TVar (cross-module value refs aren't currently
              -- solved across modules).
              delegateRetType = case (params, body) of
                  ([], A.At _ (Can.Call (A.At _ (Can.VarTopLevel calleeHome calleeName)) [])) ->
                      let calleeModPrefix = map (\c -> if c == '.' then '_' else c)
                              (ModuleName.toString calleeHome)
                          calleeKey = calleeModPrefix ++ "_" ++ goSafeName calleeName
                          sameNameKey = goSafeName calleeName
                      in case Map.lookup calleeKey (Rec._cg_funcInferredSigs env) of
                          Just (_, _, r) | r /= "any" -> Just r
                          _ -> case Map.lookup calleeKey (Rec._cg_funcRetType env) of
                              Just r | r /= "any" -> Just r
                              _ -> case Map.lookup sameNameKey (Rec._cg_funcRetType env) of
                                  Just r | r /= "any" -> Just r
                                  _ -> Nothing
                  ([], A.At _ (Can.VarTopLevel calleeHome calleeName)) ->
                      let calleeModPrefix = map (\c -> if c == '.' then '_' else c)
                              (ModuleName.toString calleeHome)
                          calleeKey = calleeModPrefix ++ "_" ++ goSafeName calleeName
                      in case Map.lookup calleeKey (Rec._cg_funcInferredSigs env) of
                          Just (_, _, r) | r /= "any" -> Just r
                          _ -> case Map.lookup calleeKey (Rec._cg_funcRetType env) of
                              Just r | r /= "any" -> Just r
                              _ -> Nothing
                  _ -> Nothing
              (depTypeParams, depParamGoTys, depRetType) = case def of
                  Can.TypedDef _ _ typedPats _ retTy ->
                      -- Mirror the entry-module path: use the solved
                      -- type (when available) or the reconstructed
                      -- annotation via splitInferredSigWithReg so
                      -- function-type params become `[T1 any](f
                      -- func(…) T1)` instead of `f any`. That keeps
                      -- Counter.view callable with `func(CMsg) Msg`
                      -- despite Go's no-covariance rule.
                      let baseTy = foldr T.TLambda retTy (map snd typedPats)
                      in  splitInferredSigWithReg
                              (Rec._cg_recordAliases env)
                              (Rec._cg_fieldIndex env)
                              (length typedPats)
                              baseTy
                  _ -> case Map.lookup qualLookupName (Rec._cg_funcInferredSigs env) of
                      Just (tps, ps, r) | r == "any"
                                        , Just delegated <- delegateRetType ->
                          (tps, ps, delegated)
                      Just sig -> sig
                      Nothing  -> case delegateRetType of
                          Just r  -> ([], replicate (length params) "any", r)
                          Nothing -> ([], replicate (length params) "any", "any")
              -- Replace each param's Go type with the typed form
              -- (when not "any"). destructureParams gave us patterns
              -- already; we just rewrite the type slot.
              typedGoParams' = zipWith
                  (\(GoIr.GoParam pname _) ty -> GoIr.GoParam pname ty)
                  goParams'
                  (depParamGoTys ++ repeat "any")
              -- v0.13 typed lowerer: scope function params via
              -- `withScopedLambdaTypes` so bindings don't leak into
              -- sibling functions.  Register params whose Go-side
              -- declaration is concrete (not "any" / not T_N) OR
              -- whose Go declaration is a function type (which CAN
              -- contain TVars — we want fn-typed params usable at
              -- recursive call sites within the generic body, e.g.
              -- `Sky_Core_List_map_`'s `fn func(T1) T2` flows raw
              -- into the recursive `Sky_Core_List_map_(fn, …)`).
              isGoTypedDecl gty =
                  (gty /= "any" && gty /= "" && not (isGenericTypeParam gty))
                  || take 5 gty == "func("
              -- v0.13 Stage 1 (task #189 — pipeline reorder applied):
              -- dep-decl emission now runs AFTER typecheck has
              -- populated funcSkyToGoTVars. Rewrite each typed param's
              -- Sky type to use Go-side TVar names so
              -- `solvedTypeToGo` renders the lambda-types map entry
              -- with `func(T1) T2` (matching the function's emitted
              -- Go sig) instead of `func(any) any`. Then call-site
              -- `goExprGoType (GoIdent "fn")` returns the typed sig
              -- and the recovery σ pins TVars at recursive call
              -- sites inside Sky-source kernel bodies.
              skyToGoMap = Map.fromList
                  (Map.findWithDefault [] goName
                      (Rec._cg_funcSkyToGoTVars env))
              rewriteTVars t = substTVars skyToGoMap t
              -- For Can.TypedDef (annotated) use the annotation.
              -- For Can.Def (unannotated, like Sky.Core.List's `map`),
              -- pull param types from the inferred sig in
              -- `_cg_funcInferredSigs[goName]`. The inferred sig was
              -- populated by typecheck which now runs BEFORE dep-decl
              -- emission.
              inferredArgTys = case Map.lookup goName (Rec._cg_funcInferredSigs env) of
                  Just (_, ps, _) ->
                      -- inferred sig has Go-string param types; we need
                      -- Sky types to register in lambdaTypes. The
                      -- inferred-sig path doesn't carry Sky types
                      -- directly, but we have depParamGoTys which is
                      -- Go-string. We can synthesise a Sky TLambda from
                      -- Go-type-strings using `goTypeStrToSkyType`.
                      --
                      -- v0.15.3 — also include parametric record alias
                      -- params (`Foo_R[T1]`).  `goTypeStrToSkyType`
                      -- returns `TVar "_unknown"` for these — which
                      -- alone wouldn't help, but the parallel
                      -- `goStringBindings` path below registers the
                      -- ACTUAL Go-type-string into `lookupLambdaGoStr`
                      -- so `goExprGoType` resolves the ident's Go
                      -- type without round-tripping through Sky types.
                      -- We keep the filter wide here for symmetry —
                      -- the goStringBindings path is the one that
                      -- actually pays off for record-alias params.
                      [ goTypeStrToSkyType gty
                      | gty <- ps
                      , gty /= "any"
                      , take 5 gty == "func("
                         || isJust (parametricAliasBase gty)
                      ]
                  Nothing -> []
              annotArgs = case mAnnotArgs of
                  Just argTys -> Just (zip params argTys)
                  Nothing
                      | not (null inferredArgTys)
                      , length inferredArgTys == length params ->
                          Just (zip params inferredArgTys)
                      | otherwise -> Nothing
              paramTypeBindings = case annotArgs of
                  Just pairs ->
                      Map.fromList
                          [ (n, rewriteTVars t)
                          | ((A.At _ (Can.PVar n), t), gty)
                              <- zip pairs (depParamGoTys ++ repeat "any")
                          , isGoTypedDecl gty
                          ]
                  Nothing -> Map.empty
              -- v0.13 typed lowerer: thread the EMITTED Go return
              -- type (`depRetType`) directly into the body lowering.
              -- `depRetType` is authoritative — it's exactly the Go
              -- type in the function's emitted signature, so there's
              -- no risk of a sig/body type divergence (the previous
              -- `solvedTypeToGo retTy == depRetType` gate existed
              -- precisely to detect that divergence — now structurally
              -- impossible).  `exprToGoExpectGo`'s own
              -- `isEmittableGoType` gate keeps it sound: a non-
              -- emittable `depRetType` falls back to plain `exprToGo`
              -- and the outer `typeIIFE` still coerces.
              lowerDepBody e =
                  if depRetType /= "any"
                      then exprToGoExpectGo depRetType e
                      else exprToGo e
              -- `typeIIFE` runs on the GoExpr STRUCTURE — before
              -- `withScopedLambdaTypes` renders it to a String — so it
              -- can still see a `GoBlock` and convert it to a typed
              -- `func() <depRetType>` IIFE.  Idempotent on an
              -- already-typed `GoTypedBlock` (no redundant re-wrap).
              typedBody = typeIIFE depRetType (lowerDepBody body)
              -- v0.13 Stage 1 (task #189) — register func-typed
              -- params in the Go-string registry too, so the
              -- recursive call-site `goExprGoType` resolves
              -- `fn` to `func(T1) T2` (the emitted Go sig) and
              -- coerceArg short-circuits without wrapping.
              --
              -- v0.15.3 — ALSO register parametric record alias
              -- params (`Foo_R[T1]`).  Without this, sibling
              -- polymorphic-helper calls inside a generic
              -- function's body emit `any(cfg).(Foo_R[any])` —
              -- a nominal cast across Go generic instantiations
              -- that panics at runtime when the caller passed
              -- `Foo_R[Msg]`.  With this entry, `goExprGoType
              -- (GoIdent "cfg")` returns `Foo_R[T1]` and
              -- coerceArg's parametricAliasBase short-circuit
              -- emits the bare arg, letting Go's call-site
              -- inference pin the callee's T.
              goStringBindings = Map.fromList
                  [ (pname, pgo)
                  | (GoIr.GoParam pname pgo) <- typedGoParams'
                  , take 5 pgo == "func("
                     || isJust (parametricAliasBase pgo)
                  ]
              -- Go-strings INNER, Sky-types OUTER so both bindings are
              -- active during typedBody's render (which is forced
              -- eagerly inside the innermost withScoped wrapper).
              bodyExpr1 = if Map.null goStringBindings
                  then typedBody
                  else withScopedLambdaGoStrings goStringBindings typedBody
              bodyExpr = if Map.null paramTypeBindings
                  then bodyExpr1
                  else withScopedLambdaTypes paramTypeBindings bodyExpr1
              -- v0.14.x TCO: same shape as the entry-module emission.
              -- Tail-recursive dep functions (Sky.Core.List.foldl,
              -- find, any, all, …) emit as a `for {}` loop with
              -- param-reassignment at recursive call sites.
              depHome = Can._name canMod
              depParamNames =
                  [ pn | GoIr.GoParam pn _ <- typedGoParams' ]
              depParamTyped =
                  [ ty | GoIr.GoParam _ ty <- typedGoParams' ]
              useTco = TCO.isTailRecursive depHome name (length params) body
              tcoBody = [GoIr.GoForever
                          (tcoBodyStmts depHome name (length params)
                                        depParamNames depParamTyped depRetType body)]
              normalBody = [GoIr.GoReturn bodyExpr]
          in [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = goName
                , GoIr._gf_typeParams = [ (tp, "any") | tp <- depTypeParams ]
                , GoIr._gf_params = typedGoParams'
                , GoIr._gf_returnType = depRetType
                , GoIr._gf_body = destructStmts ++ (if useTco then tcoBody else normalBody)
                }
           ]


-- | Walk a Decls tree, collecting every value-level name
collectDeclNames :: Can.Decls -> Set.Set String
collectDeclNames = goNames Set.empty
  where
    goNames acc Can.SaveTheEnvironment = acc
    goNames acc (Can.Declare d rest) = goNames (addName acc d) rest
    goNames acc (Can.DeclareRec d ds rest) =
        goNames (foldr (flip addName) (addName acc d) ds) rest
    addName acc d = case d of
        Can.Def (A.At _ n) _ _ -> Set.insert n acc
        Can.TypedDef (A.At _ n) _ _ _ _ -> Set.insert n acc
        Can.DestructDef _ _ -> acc  -- destructure let-binding — no top-level name


-- | Emit a dep module's union type declaration + constructor value/func.
-- Type becomes `<ModPrefix>_<TypeName>` and each ctor becomes
-- `<ModPrefix>_<TypeName>_<CtorName>`.
generateUnionForDep :: String -> (String, Can.Union) -> [GoIr.GoDecl]
generateUnionForDep modPrefix (typeName, Can.Union _vars ctors _numAlts opts) =
    let qualType = modPrefix ++ "_" ++ typeName
    in case opts of
        Can.Enum ->
            [ GoIr.GoDeclType qualType (GoIr.GoEnumDef
                [ qualType ++ "_" ++ cname
                | Can.Ctor cname _ _ _ <- ctors
                ])
            ]
        _ ->
            -- Emit as a type alias to rt.SkyADT so values constructed
            -- here are assignment-compatible with values produced by
            -- rt-side builders (ErrIo, ErrNetwork, Just/Nothing helpers,
            -- etc.). Eliminates the `interface {} is rt.SkyADT, not
            -- Sky_Core_Error_Error` panic class at pattern-match sites.
            GoIr.GoDeclRaw ("type " ++ qualType ++ " = rt.SkyADT")
            : [ if arity == 0
                  then GoIr.GoDeclVar (qualType ++ "_" ++ cname) qualType
                        (Just (GoIr.GoStructLit qualType
                            [ ("Tag", GoIr.GoIntLit idx)
                            , ("SkyName", GoIr.GoStringLit cname)
                            ]))
                  else GoIr.GoDeclFunc GoIr.GoFuncDecl
                        { GoIr._gf_name = qualType ++ "_" ++ cname
                        , GoIr._gf_typeParams = []
                        , GoIr._gf_params =
                            [ GoIr.GoParam ("v" ++ show i) (ctorArgGoTypeDep i argTys)
                            | i <- [0 .. arity - 1]
                            ]
                        , GoIr._gf_returnType = qualType
                        , GoIr._gf_body = [GoIr.GoReturn (GoIr.GoStructLit qualType
                            ([ ("Tag", GoIr.GoIntLit idx)
                             , ("SkyName", GoIr.GoStringLit cname)
                             ]
                            ++ [("Fields", GoIr.GoSliceLit "any"
                                    (map (\i -> GoIr.GoIdent ("v" ++ show i)) [0..arity-1]))]))]
                        }
              | Can.Ctor cname idx arity argTys <- ctors
              ]
            ++ [ GoIr.GoDeclRaw $ "func init() { "
                   ++ concatMap (\(Can.Ctor cname idx _ _) ->
                        "rt.RegisterAdtTag(\"" ++ cname ++ "\", " ++ show idx ++ "); ")
                        ctors
                   ++ "}" ]
  where
    -- T1: dep ctor params typed from declared union's arg types.
    ctorArgGoTypeDep i argTys
        | i < length argTys = safeReturnType (argTys !! i)
        | otherwise = "any"


-- | Emit a dep module's type alias. Record aliases become Go named structs
-- so cross-module records type-check. Non-record aliases become Go type aliases.
-- Record aliases emit BOTH a struct type (suffixed "_R" to avoid collision
-- with user-defined constructor functions of the same name) AND an auto-
-- constructor function using the original alias name.
generateAliasForDep :: Set.Set String -> String -> (String, Can.Alias) -> [GoIr.GoDecl]
generateAliasForDep userDefs modPrefix (aliasName, Can.Alias skyVars body) =
    let qualName = modPrefix ++ "_" ++ aliasName
        structName = qualName ++ "_R"
    in case body of
        T.TRecord fields _ ->
            let fieldList = List.sortOn (T._fieldIndex . snd) (Map.toList fields)
                -- v0.15 Stage E: parametric dep alias emits as Go
                -- generic struct.
                goTVars = zipWith (\i _ -> "T" ++ show (i :: Int)) [1 ..] skyVars
                tvarMap = Map.fromList (zip skyVars goTVars)
                fieldGoType fty = substituteTVarsToGo tvarMap fty
                typeParamDecl =
                    if null goTVars
                        then ""
                        else "[" ++ intercalate_ ", "
                                    [tp ++ " any" | tp <- goTVars] ++ "]"
                structAppliedSelf =
                    if null goTVars
                        then structName
                        else structName ++ "[" ++ intercalate_ ", " goTVars ++ "]"
                structDecl = GoIr.GoDeclRaw $
                    "type " ++ structName ++ typeParamDecl ++ " struct { "
                    ++ intercalate_ "; "
                        [ capitalise_ fn ++ " " ++ fieldGoType fty
                        | (fn, T.FieldType _ fty) <- fieldList
                        ]
                    ++ " }"
                hasUserCtor = Set.member aliasName userDefs
                paramList = zipWith (\i _ -> "p" ++ show i) [0::Int ..] fieldList
                paramGoTypes = map (\(_, T.FieldType _ fty) -> fieldGoType fty) fieldList
                paramDecls = intercalate_ ", "
                    [ p ++ " " ++ ty | (p, ty) <- zip paramList paramGoTypes ]
                fieldInits =
                    [ capitalise_ fn ++ ": " ++ ("p" ++ show i)
                    | (i, (fn, _)) <- zip [0::Int ..] fieldList
                    ]
                ctorDecl = GoIr.GoDeclRaw $
                    "func " ++ qualName ++ typeParamDecl ++ "("
                    ++ paramDecls ++ ") " ++ structAppliedSelf ++
                    " { return " ++ structAppliedSelf
                    ++ "{" ++ intercalate_ ", " fieldInits ++ "} }"
                gobInst =
                    if null goTVars
                        then structName ++ "{}"
                        else structName ++ "[" ++ intercalate_ ", "
                                           (map (const "any") goTVars) ++ "]{}"
                gobDecl = GoIr.GoDeclRaw $
                    "func init() { rt.RegisterGobType(" ++ gobInst ++ ") }"
            in structDecl : gobDecl : [ctorDecl | not hasUserCtor]
        _ ->
            [ GoIr.GoDeclRaw ("type " ++ qualName ++ " = any") ]


-- | Generate Go with merged dependency declarations
generateGoMulti :: Can.Module -> Src.Module -> Toml.SkyConfig -> Solve.SolvedTypes -> [GoIr.GoDecl] -> Set.Set String -> Set.Set String -> Set.Set String -> Map.Map String Int -> Map.Map String [String] -> Map.Map String String -> Map.Map String String -> Map.Map String [String] -> Map.Map String String -> Map.Map String ([String], [String], String) -> [(String, Map.Map String Can.Alias)] -> String
generateGoMulti canMod srcMod config solvedTypes depDecls depRecAliases depUnionNames depEnumNames depArities depParamTypes depRetTypes depUltRetTypes extraInferredParamTypes extraInferredRetTypes extraInferredSigs depAliasPairs =
    let
        imports = unsafePerformIO $ do
            -- T2/T6: register entry-module + dep-module typed function
            -- signatures so call-site codegen (`coerceCallArgs`) can
            -- emit `any(arg).(T)` coercions when passing args to
            -- typed-param functions across module boundaries.
            -- Rebuild the cgEnv fresh from ALL sources (annotations,
            -- HM-inferred, dep types) so the final env is deterministic
            -- regardless of when `imports` is forced relative to
            -- depDecls during goCode rendering.
            -- Register HM-inferred sigs for ENTRY module functions too
            -- so call-site coercion (coerceCallArgs / coerceArg) sees
            -- the typed params. Without this, calling an entry-module
            -- typed function from another entry function skips
            -- coercion and Go rejects any→concrete.
            -- Build alias set early so splitInferredSigWith can resolve
            -- cross-module record aliases in HM-inferred types.
            prevEnvEarly <- readIORef globalCgEnv
            let earlyRecAliases = Set.union depRecAliases
                    (Set.union (Rec.collectRecordAliases (Can._aliases canMod))
                               (Rec._cg_recordAliases prevEnvEarly))
                -- Build the full field-set → alias-name registry early
                -- so `splitInferredSigWithReg` can resolve TRecord nodes
                -- to their `_R` Go struct names in emitted signatures.
                earlyFieldIdx = Map.unions
                    [ Rec.buildRegistry (Can._aliases canMod)
                    , Rec.buildDepFieldIndex depAliasPairs
                    , Rec._cg_fieldIndex prevEnvEarly
                    ]
            -- Entry-module sigs visible to call-site codegen. For each
            -- top-level function we pick the same type the declaration
            -- will emit:
            --   TypedDef: the annotation (`a -> Foo` — may carry user
            --             TVars that become Go generics)
            --   Def:      the HM-solved type
            -- Using the solved type for TypedDef would confuse call sites
            -- when the solved type's TVars differ from the annotation's
            -- (e.g. `init : a -> …` where the body narrows `a` to a
            -- concrete Dict — call sites would omit the `[any]`
            -- instantiation that the declaration still needs).
            -- v0.15.x P37a: `solvedTypes` is the new `SolvedTypes`
            -- record; the lookup / iteration here works against the
            -- env-map projection.  Extract once for readability.
            let solvedEnv = Solve._stEnv solvedTypes
                sigTypeFor n =
                    case Map.lookup n (declsByName canMod) of
                        Just (Can.TypedDef _ _ typedPats _ retTy) ->
                            Just (foldr T.TLambda retTy (map snd typedPats))
                        _ -> Map.lookup n solvedEnv
                entryInferredSigs = Map.fromList
                    [ (goSafeName n, splitInferredSigWithReg earlyRecAliases earlyFieldIdx (countParamsFor n canMod) ty)
                    | (n, _) <- Map.toList solvedEnv
                    , Just ty <- [sigTypeFor n]
                    ]
                entryInferredParams = Map.map (\(_, ps, _) -> ps) entryInferredSigs
                entryInferredRets   = Map.map (\(_, _, r) -> r) entryInferredSigs
                -- v0.13 Phase A5+: same Sky-TVar → Go-TVar mapping for
                -- entry-module functions.  See depSkyToGoTVars above.
                -- See depSkyToGoTVars comment — apply the same
                -- generalisation-style rename so SkyNames match the
                -- annotation Forall names the solver captures.  Entry-
                -- module same-module call sites also go through this
                -- path because cross-module dep callers see the entry
                -- module via depExternals (and entry-module recursive
                -- calls capture against the local CLet binding's
                -- generalised annotation too).
                renameTypeForExternal' t =
                    case generaliseToAnnotation t of
                        T.Forall _ renamed -> renamed
                entrySkyToGoTVars = Map.fromList
                    [ ( goSafeName n
                      , inferredSigSkyToGo
                            earlyRecAliases earlyFieldIdx
                            (countParamsFor n canMod)
                            (renameTypeForExternal' ty) )
                    | (n, _) <- Map.toList solvedEnv
                    , Just ty <- [sigTypeFor n]
                    ]
            -- Gather the FULL record-alias set (entry + dep modules,
            -- prefixed and unprefixed forms) so collectFuncTypesWith's
            -- safeReturnTypeWith resolves `Piece` → `Chess_Piece_Piece_R`
            -- instead of degrading to `any`. Without this, annotated
            -- entry functions taking record types get `any` params.
            prevEnv <- readIORef globalCgEnv
            let allRecAliases = Set.union depRecAliases
                    (Set.union (Rec.collectRecordAliases (Can._aliases canMod))
                               (Rec._cg_recordAliases prevEnv))
                (entryParamTys, entryRetTys, entryUltRetTys) =
                    collectFuncTypesWith allRecAliases "" canMod
                -- v0.13 Stage 1 — merge per-key, picking the
                -- BETTER of the inferred (HM-derived) and the
                -- early-collected (decl-derived) entries:
                --
                -- * The HM-inferred entries preserve TVar names in
                --   COMPOUND types (`func(string) T1`, `[]T1`,
                --   `rt.SkyMaybe[T1]`) — critical for σ-recovery
                --   at HOF call sites with typed args.
                -- * The early-collected entries have concrete types
                --   for auto-record-ctors (`Item : int -> string
                --   -> []string -> Item_R`) — HM may solve these as
                --   bare-polymorphic (`T1 -> T2 -> T3 -> Item_R`)
                --   which loses the concrete field types and
                --   prevents call-site coercion.
                --
                -- `betterParamTypes` picks per-call between the two
                -- maps: when the early entry has more concrete
                -- info (fewer bare TVars), prefer it; otherwise
                -- use the inferred entry.
                allParamTys = Map.unionWith betterParamTypes
                    (Map.unionWith betterParamTypes
                        entryInferredParams extraInferredParamTypes)
                    (Map.unionWith betterParamTypes
                        entryParamTys depParamTypes)
                allRetTys   = Map.unionWith betterRetType
                    (Map.unionWith betterRetType
                        entryInferredRets extraInferredRetTypes)
                    (Map.unionWith betterRetType
                        entryRetTys depRetTypes)
                -- v0.13 Stage 2 — ultimate return types (after all
                -- args applied). ONLY merge entries computed with
                -- `ultimateReturnType` (recursive TLambda strip).
                -- Do NOT include `entryInferredRets` / `extraInferredRetTypes`
                -- here — those have after-one-strip semantics and
                -- would corrupt the typed-partial-app wrapper output
                -- (caller would get `func(T1) func(any) func(any) X`
                -- instead of `func(T1) X`). Consumers fall back to
                -- "any" for unannotated functions, which produces
                -- `func(T1) any` — strictly safer than the wrong
                -- multi-level shape.
                allUltRetTys = Map.union entryUltRetTys depUltRetTypes
                -- v0.13 Phase A5: preserve the call-site instance
                -- registry from prevEnv (installed by continueCompile
                -- after solveWithInstances).  The rest of the cgEnv
                -- chain rebuilds from scratch via buildCodegenEnv;
                -- the CSI map needs explicit threading.
                cgEnv = Rec.withCallSiteInstances
                          (Rec._cg_callSiteInstances prevEnv)
                      $ Rec.withFuncSkyToGoTVars
                          (Map.union entrySkyToGoTVars
                              (Rec._cg_funcSkyToGoTVars prevEnv))
                      $ Rec.withInferredSigs
                          (Map.union extraInferredSigs entryInferredSigs)
                      $ Rec.withFuncUltimateRetTypes allUltRetTys
                      $ Rec.withFuncTypes allParamTys allRetTys
                      $ Rec.withDepArities depArities
                      $ Rec.withRecordAliases depRecAliases
                      $ Rec.withUnionNames depUnionNames
                      $ Rec.withEnumNames depEnumNames
                      $ Rec.withDepFieldIndex depAliasPairs
                      $ Rec.buildCodegenEnv solvedTypes canMod
            writeIORef globalCgEnv cgEnv
            writeIORef globalUnionNames $! Rec._cg_unionNames cgEnv
            return $ collectGoImports canMod srcMod
        -- Force `imports` before anything else so the env is set up
        -- before depDecls / decls are evaluated (they read getCgEnv).
        importsForced = imports `seq` imports
        -- v0.16 PR 1 — snapshot the lowering-time IORef state into a
        -- pure `LowerCtx` value.  `imports` has already populated
        -- `globalCgEnv` + `globalUnionNames`; `globalAllAliases`,
        -- `globalAllFieldIdx`, `globalAnnotMap`, and the per-scope
        -- lambda-type / lambda-Go-string maps were written by
        -- `continueCompile` earlier.  Sequence the snapshot AFTER
        -- `importsForced` so all writes are visible.
        --
        -- The value is constructed but has NO CALLERS in this PR.
        -- PRs 2-6 of the v0.16 sequence (see
        -- docs/improvement-plan-v0.16.md §2) migrate `exprToGo` /
        -- `exprToGoExpectGo` / `coerceArg` / `letBindingType` /
        -- `inferExprType` to read from `lowerCtx` instead of the
        -- IORefs, after which the IORefs are deleted.
        --
        -- v0.15.x P37b — the region-type map no longer lives on
        -- `LC._lc_regionTypes` (that field was deleted).  Regions
        -- flow purely through `Solve.SolvedTypes._stRegions`, which
        -- the `solvedTypes` value passed in already carries.
        lowerCtx = unsafePerformIO $ do
            -- Sequence the snapshot AFTER `importsForced` so writes
            -- to `globalCgEnv` + `globalUnionNames` are visible.
            importsForced `seq` return ()
            aliases <- readIORef globalAllAliases
            fieldIdx <- readIORef globalAllFieldIdx
            unions <- readIORef globalUnionNames
            annots <- readIORef globalAnnotMap
            return $ LC.buildLowerCtx
                (Can._name canMod)
                solvedTypes
                aliases
                fieldIdx
                unions
                annots
        unionDecls = generateUnionTypes canMod
        aliasDecls = generateAliasTypes canMod
        decls = generateDecls canMod solvedTypes
        mainDecl = generateMainFunc canMod srcMod solvedTypes
        -- v0.13 E: emit `type Anon_R_<hash> = struct { … }` decls
        -- for every anon-record shape that `synthAnonRecordName`
        -- produced during this compilation. Wired AFTER `decls`
        -- in `_pkg_decls`; the renderer walks the list in order,
        -- so by the time it forces `anonRecordDecls`'s thunk
        -- every preceding decl has been rendered to a String —
        -- which is exactly when `synthAnonRecordName` fires its
        -- `atomicModifyIORef'` registrations.
        anonRecordDecls = unsafePerformIO generateAnonRecordDecls
        -- Pin the rt import so Go doesn't error out with "imported and not used"
        -- when the user's program doesn't happen to reference rt.* directly
        -- (e.g. main = 42). The blank var reference is zero-cost at runtime.
        rtPin = [GoIr.GoDeclRaw "var _ = rt.AsInt"]
        -- v0.13 contract: always-available type alias for the
        -- canonical Sky.Core.Error.Error. The runtimeTypedMap
        -- entry "Error" → "Sky_Core_Error_Error" resolves to this
        -- name; ensure the alias exists even when Sky.Core.Error
        -- isn't a transitive dep (examples that use kernel funcs
        -- returning Result Error a without explicit Sky.Core.Error
        -- import).
        --
        -- Skip when Sky.Core.Error IS a dep — the dep compilation
        -- emits its own `type Sky_Core_Error_Error = rt.SkyADT`
        -- alias (alongside the ErrorKind / ErrorInfo types) and a
        -- redeclaration would be a `go build` error.
        errorAliasStub =
            if Set.member "Sky_Core_Error_Error" depUnionNames
                then []
                else [ GoIr.GoDeclRaw "type Sky_Core_Error_Error = rt.SkyADT" ]
        -- Emit sky.toml's `port` as a SKY_LIVE_PORT default so Sky.Live /
        -- Sky.Http.Server pick it up. Shell env and .env still take
        -- precedence (we only Setenv when unset).
        -- Use reflect-free stdlib (`os` package) in a named-init to set the
        -- port fallback without requiring extra imports — we pipe through
        -- rt.SetPortDefault which lives in the runtime (always imported).
        -- Every runtime default derivable from sky.toml lands in this
        -- single init() so the generated binary reflects the project's
        -- configuration at zero runtime cost. All defaults are only
        -- applied when the corresponding env var is unset — that way
        -- CI / docker can override without a recompile.
        -- [env] prefix: emitted FIRST so subsequent SetSkyDefault
        -- calls land under the configured namespace. Runtime
        -- refresh hooks re-read package-level cached env state
        -- (logThreshold / logJSON) so they pick up the new
        -- prefix even though they were initialised earlier.
        envPrefixLine = case Toml._envPrefix config of
            "" -> ""
            p  -> "\trt.SetEnvPrefix(" ++ escapeGoString p ++ ")\n"
        liveDefaults =
            [ GoIr.GoDeclRaw $
                "func init() {\n"
                ++ envPrefixLine
                ++ "\trt.SetPortDefault(\"" ++ show (Toml._livePort config) ++ "\")\n"
                ++ tomlSkyEnv "LIVE_STORE"      (Toml._liveStore     config)
                ++ tomlSkyEnv "LIVE_STORE_PATH" (Toml._liveStorePath config)
                ++ tomlSkyEnv "LIVE_TTL"        (intString           (Toml._liveTtl config))
                ++ tomlSkyEnv "LIVE_STATIC_DIR" (Toml._liveStatic    config)
                -- maxBodyBytes: cap for /_sky/event POST body. Runtime
                -- defaults to 5 MiB; bump higher when the app uses
                -- Event.onFile / Event.onImage with larger uploads.
                ++ tomlSkyEnv "LIVE_MAX_BODY_BYTES"
                       (intString (Toml._liveMaxBody config))
                ++ tomlSkyEnv "AUTH_SECRET"     (Toml._authSecret    config)
                ++ tomlSkyEnv "AUTH_TOKEN_TTL"  (intString (Toml._authTokenTtl config))
                ++ tomlSkyEnv "AUTH_COOKIE"     (Toml._authCookie    config)
                ++ tomlSkyEnv "AUTH_DRIVER"     (Toml._authDriver    config)
                ++ tomlSkyEnv "DB_DRIVER"       (Toml._dbDriver      config)
                ++ tomlSkyEnv "DB_PATH"         (Toml._dbPath        config)
                -- [log] defaults: format (plain/json) + level
                -- (debug/info/warn/error). <PREFIX>_LOG_FORMAT and
                -- <PREFIX>_LOG_LEVEL still override at runtime.
                ++ tomlSkyEnv "LOG_FORMAT"      (Toml._logFormat     config)
                ++ tomlSkyEnv "LOG_LEVEL"       (Toml._logLevel      config)
                ++ "}"
            ]
        portDefault = liveDefaults  -- preserve historical name for downstream splices
        -- v0.13 Phase A4 (MVP): emit per-instance specialised copies
        -- of every reachable Sky-source function.  The generic
        -- version stays in place for now (call sites still reference
        -- it).  This is the first wire-up — the specialised copies
        -- aren't yet referenced by call sites, so they compile but
        -- are dead.  Successive commits will switch call sites to
        -- use the mangled names and drop the generic emission.
        specDecls = unsafePerformIO $ do
            reached <- readIORef globalReachableSet
            _annotMap' <- readIORef globalAnnotMap
            csiByCallee <- readIORef globalCsiByCallee
            env <- readIORef globalCgEnv
            let
                -- Index every emitted GoFuncDecl (entry + deps) by
                -- the Go-side name so the specialiser can find the
                -- generic source.
                allDecls = depDecls ++ decls
                funcByName = Map.fromList
                    [ (GoIr._gf_name gfd, gfd)
                    | GoIr.GoDeclFunc gfd <- allDecls ]
                -- Build per-instance specialisations.  Filter to
                -- Sky-source functions that ACTUALLY appear in the
                -- emitted GoFuncDecl set (kernel / FFI / ctor names
                -- aren't user-mono'd here).
                reachableList = Set.toList reached
                buildSpec (skyName, tys) =
                    let goName = map (\c -> if c == '.' then '_' else c) skyName
                        mangled = Mono.mangleInstance
                            (Solve.CallInstance skyName tys [])
                        skyToGo = Map.findWithDefault []
                            goName (Rec._cg_funcSkyToGoTVars env)
                        quants = Map.findWithDefault [] skyName csiByCallee
                        σ_sky = Map.fromList (zip quants tys)
                        -- `sanitiseTypedDeep`: a monomorphisation
                        -- type-arg can be an anonymous record
                        -- (`{ age, name }` with no user `type alias`).
                        -- `solvedTypeToGo` names it `Anon_R_<hash>`,
                        -- but no Go `type` decl is emitted for an
                        -- un-aliased record — substituting it raw
                        -- gives `undefined: Anon_R_<hash>`. Sanitise
                        -- those tokens back to `any` so the specialised
                        -- copy still compiles (it's typed everywhere
                        -- the record DOES have a Go alias; only the
                        -- genuinely-anonymous slots widen).
                        σ_go = Map.fromList
                            [ (gn, sanitiseTypedDeep (solvedTypeToGo cty))
                            | (sn, gn) <- skyToGo
                            , Just cty <- [Map.lookup sn σ_sky]
                            ]
                    in case Map.lookup goName funcByName of
                        Just gfd | not (null tys), not (null σ_go) ->
                            Just (mangled, GoIr.GoDeclFunc
                                (Mono.specialiseFuncDecl
                                    mangled σ_go (Just goName) gfd))
                        _ -> Nothing
                emittedSpecs = mapMaybe buildSpec reachableList
                -- Dedup by mangled name. Two reachable-set entries can
                -- be structurally-distinct `[T.Type]` lists that mangle
                -- to the SAME name — e.g. a callee reached from two
                -- modules where one carries `Model` as `TType` and the
                -- other as `TAlias` (same nominal type, different
                -- representation). Both specialise to byte-identical
                -- Go; emitting both is a `redeclared in this block`
                -- compile error. Map.fromList collapses on the key.
                dedupedSpecs = Map.toList (Map.fromList emittedSpecs)
                specNames = Set.fromList (map fst dedupedSpecs)
                specials = map snd dedupedSpecs
            -- Record emitted spec names so call sites can decide
            -- whether to use the mangled name or fall back to the
            -- generic version.
            writeIORef globalEmittedSpecs specNames
            return specials
        -- v0.13 Phase A4: keep generic versions alongside specs.
        -- Drop pass tracked for v0.14 — needs spec emission to
        -- handle every reachable call-site instance, including
        -- subtle alignment cases (Page_Roadmap_viewRoadmap's
        -- __Error_Unit mismatch in skyvote, Sky_Core_Result_andThen's
        -- third-instance miss in skyshop).
        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = rtPin ++ errorAliasStub ++ portDefault ++ depDecls ++ unionDecls ++ aliasDecls ++ decls ++ anonRecordDecls ++ specDecls ++ mainDecl
            }
    -- Force `lowerCtx` so the IORef snapshot actually runs (v0.16
    -- PR 1).  The value has no callers in this PR — PRs 2-6 migrate
    -- `exprToGo` etc. to consume it — but exercising the snapshot
    -- here verifies the scaffolding works in every release build.
    in lowerCtx `seq` GoBuilder.renderPackage pkg


-- | Emit a Go if-not-already-set runtime default for a sky.toml-derived
-- value, prefixed with the runtime's configured env namespace.
-- No-op when the value is empty (so we don't unset actual env-var
-- overrides). The suffix is namespaced ("LIVE_TTL", "AUTH_COOKIE",
-- …) — the runtime prepends the prefix from `rt.SetEnvPrefix`.
tomlSkyEnv :: String -> String -> String
tomlSkyEnv _      ""    = ""
tomlSkyEnv suffix value =
       "\trt.SetSkyDefault(" ++ escapeGoString suffix
       ++ ", " ++ escapeGoString value ++ ")\n"


intString :: Int -> String
intString n
    | n <= 0    = ""
    | otherwise = show n


escapeGoString :: String -> String
escapeGoString s = "\"" ++ concatMap esc s ++ "\""
  where
    esc '\\' = "\\\\"
    esc '"'  = "\\\""
    esc '\n' = "\\n"
    esc '\r' = "\\r"
    esc '\t' = "\\t"
    esc c    = [c]


-- | Generate Go source from a canonical module with solved types (single module)
generateGo :: Can.Module -> Src.Module -> Toml.SkyConfig -> Solve.SolvedTypes -> String
generateGo canMod srcMod config solvedTypes =
    let
        imports = unsafePerformIO $ do
            let cgEnv = Rec.buildCodegenEnv solvedTypes canMod
            writeIORef globalCgEnv cgEnv
            writeIORef globalUnionNames $! Rec._cg_unionNames cgEnv
            return $ collectGoImports canMod srcMod
        unionDecls = generateUnionTypes canMod
        aliasDecls = generateAliasTypes canMod
        decls = generateDecls canMod solvedTypes
        mainDecl = generateMainFunc canMod srcMod solvedTypes
        -- v0.13 E: see generateGoMulti for the rationale.
        anonRecordDecls = unsafePerformIO generateAnonRecordDecls
        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = unionDecls ++ aliasDecls ++ decls ++ anonRecordDecls ++ mainDecl
            }
    in GoBuilder.renderPackage pkg


-- | Collect Go imports needed
collectGoImports :: Can.Module -> Src.Module -> [GoIr.GoImport]
collectGoImports _canMod srcMod =
    -- Import as blank to avoid "imported and not used" when user's main is
    -- a pure value. If main uses rt.* anywhere, Go doesn't complain about
    -- adding a blank import alongside the aliased one.
    -- Simpler: emit `_ = rt.Log_println` in a blank var at top.
    [ GoIr.GoImport "sky-app/rt" (Just "rt") ]
    ++ sideEffectImports (Src._imports srcMod)
  where
    sideEffectImports imps =
        [ GoIr.GoImport (skyModToGoPath segs) (Just "_")
        | imp <- imps
        , Src._importAlias imp == Just "_"
        , let A.At _ segs = Src._importName imp
        ]
    skyModToGoPath segs =
        let lowered = map (map Char.toLower) segs
        in reconstructGoPath lowered
    reconstructGoPath parts = case parts of
        [] -> ""
        [p] -> p
        (a:b:rest) ->
            let firstTwo = a ++ "." ++ b
            in case rest of
                [] -> firstTwo
                _  -> firstTwo ++ "/" ++ List.intercalate "/" rest


-- | Check if module imports Task
isTaskImport :: Src.Import -> Bool
isTaskImport imp =
    let segs = case Src._importName imp of A.At _ s -> s
    in segs == ["Sky", "Core", "Task"]


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

-- | Generate Go type declarations for user-defined union types
generateUnionTypes :: Can.Module -> [GoIr.GoDecl]
generateUnionTypes canMod =
    concatMap generateUnion (Map.toList (Can._unions canMod))
  where
    -- This module's Go prefix ("Main", "State", ...) — used to rewrite
    -- local type refs that typeToGo would otherwise return as
    -- "Main_Page" into just "Page".
    localPrefix = map (\c -> if c == '.' then '_' else c)
                      (ModuleName.toString (Can._name canMod))

    -- Strip "<localPrefix>_" from the front of a Go type string when
    -- present, so ctor param types that reference local unions use
    -- the unprefixed name (matching how generateUnion declares them).
    stripLocalPrefix s =
        let pre = localPrefix ++ "_"
        in if take (length pre) s == pre then drop (length pre) s else s

    generateUnion (typeName, Can.Union vars ctors numAlts opts) = case opts of
        Can.Enum ->
            -- Enum: type Name int; const ( Name_Ctor = iota ... )
            [ GoIr.GoDeclType typeName (GoIr.GoEnumDef (map (ctorConstName typeName) ctors)) ]
        _ ->
            -- Tagged union: alias rt.SkyADT so values constructed here
            -- are assignment-compatible with values produced by rt-side
            -- builders (ErrIo/ErrNetwork/etc.). Eliminates the
            -- "interface {} is rt.SkyADT, not <UserADT>" panic class at
            -- pattern-match sites.
            [ GoIr.GoDeclRaw $ "type " ++ typeName ++ " = rt.SkyADT" ]
            ++ map (generateCtorFunc typeName) ctors
            ++ [ GoIr.GoDeclRaw $ "func init() { "
                   ++ concatMap (\(Can.Ctor cname idx _ _) ->
                        "rt.RegisterAdtTag(\"" ++ cname ++ "\", " ++ show idx ++ "); ")
                        ctors
                   ++ "}" ]

    ctorConstName typeName (Can.Ctor cname _ _ _) = typeName ++ "_" ++ cname

    generateCtorFunc typeName (Can.Ctor cname idx arity argTys) =
        if arity == 0
        then GoIr.GoDeclVar (typeName ++ "_" ++ cname) typeName
            (Just (GoIr.GoStructLit typeName
                [ ("Tag", GoIr.GoIntLit idx)
                , ("SkyName", GoIr.GoStringLit cname)
                ]))
        else GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = typeName ++ "_" ++ cname
            , GoIr._gf_typeParams = []
            -- T1: ctor params are typed from the union declaration, not `any`.
            -- `HttpError Int String` becomes `(v0 int, v1 string) IoError`
            -- so callers get Go-level type checking at construction sites.
            , GoIr._gf_params = ctorParamsTyped argTys arity
            , GoIr._gf_returnType = typeName
            , GoIr._gf_body = [GoIr.GoReturn (GoIr.GoStructLit typeName
                ([ ("Tag", GoIr.GoIntLit idx)
                 , ("SkyName", GoIr.GoStringLit cname)
                 ]
                 ++ [("Fields", GoIr.GoSliceLit "any" (map (\i -> GoIr.GoIdent ("v" ++ show i)) [0..arity-1]))]))]
            }

    -- Map Can.Ctor argument types to Go param types. If we have fewer
    -- types than arity (parser/canon gap), fall back to `any` for the
    -- missing slots — we never want to crash codegen on incomplete info.
    ctorParamsTyped argTys arity =
        [ GoIr.GoParam ("v" ++ show i) (ctorArgGoType i argTys)
        | i <- [0 .. arity - 1]
        ]

    -- T1: ctor params are typed from the union's declared arg types.
    -- Call sites coerce via the VarCtor branch of exprToGo Can.Call.
    ctorArgGoType i argTys
        | i < length argTys = safeReturnType (argTys !! i)
        | otherwise = "any"

    hasTVar :: T.Type -> Bool
    hasTVar t = case t of
        T.TVar _        -> True
        T.TLambda a b   -> hasTVar a || hasTVar b
        T.TType _ _ xs  -> any hasTVar xs
        T.TTuple a b cs -> any hasTVar (a : b : cs)
        T.TAlias _ _ pairs (T.Filled inner)  -> any hasTVar (inner : map snd pairs)
        T.TAlias _ _ pairs (T.Hoisted inner) -> any hasTVar (inner : map snd pairs)
        T.TRecord _ _   -> False
        T.TUnit         -> False


-- | v0.13 E: emit one Go struct decl per registered anon-record
-- shape so the renderer's `Anon_R_<hash>` names actually resolve.
--
-- Pre-E `sanitiseTypedDeep` rewrote every `Anon_R_*` token in
-- emitted type strings to `any` (a cover-up that hid the
-- contract violation — anon records inferred at typed-codegen
-- positions silently collapsed to `any`). Now `synthAnonRecordName`
-- registers its produced shapes in `globalAnonRecords`, and this
-- function emits a `type Anon_R_<hash> = struct {…}` for every
-- registered shape so the typed name round-trips end-to-end.
--
-- Field order: sorted by `_fieldIndex` (matches the declared
-- positional API used everywhere else in codegen — see
-- generateAlias's `List.sortOn (T._fieldIndex . snd)`). Field Go
-- types are rendered via `solvedTypeToGo`; unresolved TVars
-- collapse to `any` per the renderer's policy — the resulting Go
-- struct is well-formed even when the shape's element types stay
-- polymorphic.
--
-- Lives in `IO` because `globalAnonRecords` is an IORef. Called
-- from `generateGoMulti` via `unsafePerformIO` (matching the
-- pattern used for `imports`).
generateAnonRecordDecls :: IO [GoIr.GoDecl]
generateAnonRecordDecls = do
    anons <- readIORef globalAnonRecords
    return $ concatMap structDecl (Map.toAscList anons)
  where
    structDecl (name, fields) =
        let sortedFields =
                List.sortOn (T._fieldIndex . snd) (Map.toList fields)
            goField (fname, T.FieldType _ ty) =
                capitalise_ fname ++ " " ++ solvedTypeToGo ty
            fieldStrs = map goField sortedFields
            structBody =
                if null fieldStrs
                    then "{}"
                    else "{ " ++ intercalate_ "; " fieldStrs ++ " }"
        in [ GoIr.GoDeclRaw $
                "type " ++ name ++ " = struct " ++ structBody ]


-- | Generate Go type declarations for record type aliases.
-- Record aliases become Go structs; records with function fields become Go interfaces.
generateAliasTypes :: Can.Module -> [GoIr.GoDecl]
generateAliasTypes canMod =
    let userDefinedNames = collectDeclNames (Can._decls canMod)
    in concatMap (generateAlias userDefinedNames) (Map.toList (Can._aliases canMod))
  where
    generateAlias userDefinedNames (name, Can.Alias vars body) = case body of
        T.TRecord fields _ ->
            -- Field declaration order (via _fieldIndex) is the auto-ctor's
            -- positional API. Sorting by it keeps `Piece kind colour` the same
            -- on the Go side. See generateAliasForDep for the same note.
            let fieldList = List.sortOn (T._fieldIndex . snd) (Map.toList fields)
            in generateStruct userDefinedNames name vars fieldList
        _ ->
            [ GoIr.GoDeclRaw $ "type " ++ name ++ " = " ++ solvedTypeToGo body ]

    -- vars carries the Sky-side type parameter NAMES for parametric
    -- aliases (e.g. `type alias Cfg msg = { ... }` has vars = ["msg"]).
    -- We do NOT make the Go struct generic — every TVar field collapses
    -- to `any` instead.  Reason: the consumer's HM-inferred signature
    -- doesn't carry the alias's TAlias binding (it's been unfolded to
    -- TRecord), so we have no clean way to thread Go generics into the
    -- function sig that references this struct.  Erasing TVar fields
    -- to `any` keeps Go satisfied; the runtime func-target Coerce
    -- (Fix A in rt/rt.go, line 4486) widens nominally-different func
    -- signatures via `makeFuncAdapter` at the call boundary — closing
    -- docs/parametric-record-aliases-bugs.md Surface 2 without making
    -- the struct generic.
    -- v0.15 Stage E: parametric aliases emit as GENERIC Go structs.
    generateStruct userDefinedNames name skyVars fields =
        let structName = name ++ "_R"
            goTVars = zipWith (\i _ -> "T" ++ show (i :: Int)) [1 ..] skyVars
            tvarMap = Map.fromList (zip skyVars goTVars)
            fieldGoType fty = substituteTVarsToGo tvarMap fty
            goFields = map (\(fname, T.FieldType _ ftype) ->
                (capitalise fname, fieldGoType ftype)) fields
            typeParamDecl =
                if null goTVars
                    then ""
                    else "[" ++ intercalate_ ", "
                                 [tp ++ " any" | tp <- goTVars] ++ "]"
            structAppliedSelf =
                if null goTVars
                    then structName
                    else structName ++ "[" ++ intercalate_ ", " goTVars ++ "]"
            paramList = zipWith (\i _ -> "p" ++ show i) [0::Int ..] fields
            paramGoTypes = map (\(_, T.FieldType _ fty) -> fieldGoType fty) fields
            paramDecls = intercalate_ ", "
                [ p ++ " " ++ ty | (p, ty) <- zip paramList paramGoTypes ]
            fieldInits =
                [ capitalise_ fn ++ ": " ++ ("p" ++ show i)
                | (i, (fn, _)) <- zip [0::Int ..] fields
                ]
            ctorDecl = GoIr.GoDeclRaw $
                "func " ++ name ++ typeParamDecl ++ "("
                ++ paramDecls ++ ") " ++ structAppliedSelf ++
                " { return " ++ structAppliedSelf
                ++ "{" ++ intercalate_ ", " fieldInits ++ "} }"
            gobInst =
                if null goTVars
                    then structName ++ "{}"
                    else structName ++ "[" ++ intercalate_ ", "
                                       (map (const "any") goTVars) ++ "]{}"
            gobDecl = GoIr.GoDeclRaw $
                "func init() { rt.RegisterGobType(" ++ gobInst ++ ") }"
            structDecls =
                if null goTVars
                    then [ GoIr.GoDeclType structName (GoIr.GoStructDef goFields) ]
                    else [ GoIr.GoDeclRaw $
                            "type " ++ structName ++ typeParamDecl
                            ++ " struct { "
                            ++ intercalate_ "; "
                                [ fn ++ " " ++ ty | (fn, ty) <- goFields ]
                            ++ " }"
                         ]
        in if Set.member name userDefinedNames
               then structDecls ++ [ gobDecl ]
               else structDecls ++ [ gobDecl, ctorDecl ]

    generateInterface name fields =
        let goMethods = map (\(fname, T.FieldType _ ftype) ->
                case ftype of
                    T.TLambda from to ->
                        let (params, ret) = collectFuncParams ftype
                            goParams = zipWith (\i p -> GoIr.GoParam ("p" ++ show i) (solvedTypeToGo p)) [0::Int ..] params
                        in (capitalise fname, goParams, solvedTypeToGo ret)
                    _ ->
                        -- Getter method
                        (capitalise fname, [], solvedTypeToGo ftype)
                ) fields
        in [ GoIr.GoDeclInterface name goMethods ]

    collectFuncParams (T.TLambda from to) =
        let (rest, ret) = collectFuncParams to
        in (from : rest, ret)
    collectFuncParams ty = ([], ty)

    isFuncType (T.TLambda _ _) = True
    isFuncType _ = False

    capitalise [] = []
    capitalise (c:cs) = toUpper c : cs
    toUpper c = if c >= 'a' && c <= 'z' then toEnum (fromEnum c - 32) else c


-- | Generate Go declarations from canonical decls
generateDecls :: Can.Module -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateDecls canMod solvedTypes =
    -- DCE: compute transitive closure from main and only emit reachable defs.
    -- This shrinks binaries + speeds up `go build` for large projects.
    -- Disable with SKY_DCE=0 env var (checked at codegen time).
    let reachable = Dce.reachableTopLevel canMod
        dceEnabled = unsafePerformIO (fmap (/= "0") (lookupDceFlag))
        home = Can._name canMod
    in declsToList home reachable dceEnabled (Can._decls canMod) []
  where
    declsToList _ _ _ Can.SaveTheEnvironment acc = acc
    declsToList h reachable dce (Can.Declare def rest) acc =
        declsToList h reachable dce rest (acc ++ generateDefMaybe h reachable dce def solvedTypes)
    declsToList h reachable dce (Can.DeclareRec def defs rest) acc =
        let these = generateDefMaybe h reachable dce def solvedTypes
                 ++ concatMap (\d -> generateDefMaybe h reachable dce d solvedTypes) defs
        in declsToList h reachable dce rest (acc ++ these)


-- | Emit def only if reachable (or DCE disabled).
generateDefMaybe :: ModuleName.Canonical -> Set.Set String -> Bool -> Can.Def -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateDefMaybe home reachable dceEnabled def solvedTypes = case def of
    Can.DestructDef{} -> []  -- destructure lets only live inside bodies
    _ ->
        let name = case def of
                Can.Def (A.At _ n) _ _           -> n
                Can.TypedDef (A.At _ n) _ _ _ _  -> n
                Can.DestructDef{} -> error "unreachable: filtered above"
        in if not dceEnabled || Set.member name reachable || name == "main"
            then generateDef home def solvedTypes
            else []


-- | Read SKY_DCE env var once. Default: enabled.
lookupDceFlag :: IO String
lookupDceFlag = do
    mv <- System.Environment.lookupEnv "SKY_DCE"
    return (maybe "1" id mv)


-- | Generate Go for a single definition, using solved types for signatures
generateDef :: ModuleName.Canonical -> Can.Def -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateDef home def solvedTypes =
    let (name, params, body) = case def of
            Can.Def (A.At _ n) pats expr -> (n, pats, expr)
            Can.TypedDef (A.At _ n) _ typedPats expr _ ->
                (n, map fst typedPats, expr)
            Can.DestructDef _ _ -> ("__destruct__", [], error "unreachable: destructdef has no toplevel codegen")

        -- Prefer the user's annotation when present, else use HM-
        -- inferred type. TVars in the inferred type become Go type
        -- params (T4b) via splitInferredSig.
        -- v0.15.x P37a: `SolvedTypes` is a record; consult the env
        -- field for the name lookup.
        mSolvedType = Solve.lookupSolvedVar name solvedTypes
        mAnnotTy = case def of
            Can.TypedDef _ _ _ _ ty -> Just ty
            _                       -> Nothing
        goParams = map patternToParam params
        -- Annotation case: TypedDef's 5th field is the RETURN type
        -- only; arg types live alongside patterns. For non-TypedDef,
        -- split the full inferred function type.
        -- Typed codegen: use annotation or HM-inferred types for
        -- function sigs. wrapTypedReturn coerces the body to match.
        (entryTypeParams, entryParamGoTys, goRetType) = case (def, mAnnotTy, mSolvedType) of
            (Can.TypedDef _ _ typedPats _ retTy, _, _) ->
                -- For annotated functions: use the user's ANNOTATION as
                -- the authoritative contract. HM's solved type can be
                -- strictly more specific than the annotation (body
                -- constraints narrow free TVars), but that extra
                -- specificity may not match the runtime's actual
                -- calling convention. Example: `init : a -> (Model,
                -- Cmd Msg)` with a body that does `Dict.get "cookies"
                -- req` solves to `Dict String (Dict …) -> …`, but
                -- Sky.Live's runtime passes a plain `map[string]any`
                -- — the emitted Go sig must accept that generic shape.
                --
                -- Route through splitInferredSigWithReg so function-
                -- type params emit as `func(…) T1` (callback
                -- covariance via generic inference).
                let baseTy = foldr T.TLambda retTy (map snd typedPats)
                in  splitInferredSigWithReg
                        (Rec._cg_recordAliases getCgEnv)
                        (Rec._cg_fieldIndex getCgEnv)
                        (length typedPats)
                        baseTy
            (_, _, Just funcType) ->
                splitInferredSigWithReg
                    (Rec._cg_recordAliases getCgEnv)
                    (Rec._cg_fieldIndex getCgEnv)
                    (length params)
                    funcType
            _ -> ([], replicate (length params) "any", "any")
    in
    -- Skip "main" — handled separately
    if name == "main" then []
    else
        let -- Register function params scoped to this function's body
            -- so the bindings don't leak into sibling functions.
            isGoTypedDeclE gty =
                gty /= "any" && gty /= "" && not (isGenericTypeParam gty)
            paramTypeBindings = case def of
                Can.TypedDef _ _ typedPats _ _ ->
                    Map.fromList
                        [ (n, t)
                        | ((A.At _ (Can.PVar n), t), gty)
                            <- zip typedPats (entryParamGoTys ++ repeat "any")
                        , isGoTypedDeclE gty
                        ]
                _ -> Map.empty
            -- v0.13 typed lowerer: thread the EMITTED Go return type
            -- (`goRetType`) directly into the body lowering.
            -- `goRetType` is authoritative — it's exactly the Go type
            -- in the function's emitted signature, so a sig/body type
            -- divergence is structurally impossible (the previous
            -- `solvedTypeToGo retTy == goRetType` gate existed only to
            -- detect that divergence).  `exprToGoExpectGo`'s own
            -- `isEmittableGoType` gate keeps it sound: a non-emittable
            -- `goRetType` falls back to plain `exprToGo` and the outer
            -- `typeIIFE` still coerces.
            lowerFnBody e =
                if goRetType /= "any"
                    then exprToGoExpectGo goRetType e
                    else exprToGo e
            -- `typeIIFE` runs on the GoExpr STRUCTURE — before
            -- `withScopedLambdaTypes` renders it to a String — so it
            -- can still see a `GoBlock` and convert it to a typed
            -- `func() <goRetType>` IIFE.  Idempotent on an already-
            -- typed `GoTypedBlock` (no redundant re-wrap).
            typedBody = typeIIFE goRetType (lowerFnBody body)
            bodyExpr = if Map.null paramTypeBindings
                then typedBody
                else withScopedLambdaTypes paramTypeBindings typedBody
            (goParams', destructStmts) = destructureParams params
            -- Replace each param's Go type with the typed form (from
            -- annotation or HM inference). destructureParams gave us
            -- the parameter patterns with `"any"` types by default.
            typedGoParams = zipWith
                (\(GoIr.GoParam pn _) ty -> GoIr.GoParam pn ty)
                goParams'
                (entryParamGoTys ++ repeat "any")
            -- v0.14.x TCO: tail-recursive functions emit as a `for {}`
            -- loop with param-reassignment at the recursive call sites,
            -- avoiding Go-stack growth on long iterations.
            paramNames = [ pn | GoIr.GoParam pn _ <- goParams' ]
            paramTyped = [ ty | GoIr.GoParam _ ty <- typedGoParams ]
            useTco = TCO.isTailRecursive home name (length params) body
            tcoBody = [GoIr.GoForever
                        (tcoBodyStmts home name (length params)
                                      paramNames paramTyped goRetType body)]
            normalBody = [GoIr.GoReturn bodyExpr]
        in
        [ GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = goSafeName name
            , GoIr._gf_typeParams = [ (tp, "any") | tp <- entryTypeParams ]
            , GoIr._gf_params = typedGoParams
            , GoIr._gf_returnType = goRetType
            , GoIr._gf_body = destructStmts ++ (if useTco then tcoBody else normalBody)
            }
        ]


-- | Generate function parameters and destructuring statements for any
-- non-PVar patterns. Returns (params, prelude stmts) where the prelude
-- binds names extracted from complex patterns in the function body.
destructureParams :: [Can.Pattern] -> ([GoIr.GoParam], [GoIr.GoStmt])
destructureParams pats =
    let (params, stmtLists) = unzip (zipWith oneParam [0::Int ..] pats)
    in (params, concat stmtLists)
  where
    oneParam idx (A.At _ pat) = case pat of
        Can.PVar name -> (GoIr.GoParam (goSafeName name) "any", [])
        Can.PAnything -> (GoIr.GoParam "_" "any", [])
        Can.PUnit     -> (GoIr.GoParam "_" "any", [])
        _ ->
            let tmp = "_p" ++ show idx
            in (GoIr.GoParam tmp "any", patternBindings tmp pat)


-- | Escape Sky identifiers that collide with Go reserved/builtin
-- names. The canonical Sky-local-name -> Go-identifier map: applied
-- at every emission of a local — parameter declarations, let-binding
-- declarations, pattern-bound names, AND every reference
-- (Can.VarLocal) — so a Sky identifier named after a Go keyword
-- (var, type, range, ...) emits consistently as <name>_ at both its
-- declaration and its uses. Idempotent for non-reserved names.
goSafeName :: String -> String
goSafeName n
    | n `elem` reservedGoNames = n ++ "_"
    | otherwise = n


-- | The two statements a let-binding emits — the declaration and an
-- unused-suppressing assign — with the bound name keyword-escaped so
-- it matches the goSafeName-escaped references emitted for it.
letBindStmts :: String -> GoIr.GoExpr -> [GoIr.GoStmt]
letBindStmts name expr =
    let sn = goSafeName name
    in [ GoIr.GoShortDecl sn expr
       , GoIr.GoAssign "_" (GoIr.GoIdent sn)
       ]


-- | Sky convention: identifiers starting with `_` mean the value is unused.
-- In Go this must be represented as the blank identifier to avoid "declared and not used".
isDiscardName :: String -> Bool
isDiscardName ('_':_) = True
isDiscardName _       = False


-- | Unicode-aware Go identifier predicates. Go's spec:
-- `identifier = letter (letter | unicode_digit)*` where
-- `letter = unicode_letter | '_'`. Sky source identifiers go
-- through these via `Sky.Parse.Variable.isIdentChar` (parser
-- side) which uses `Char.isAlphaNum` + `'_'`. Codegen-side
-- token matching (FFI DCE, type-string substitution, func-name
-- scanning) MUST stay aligned: an identifier with Unicode
-- letters in the Sky source emits as the same identifier in
-- Go, and string-based scanners that miss it will wrongly slice
-- it as two adjacent tokens.
isGoIdentStart :: Char -> Bool
isGoIdentStart c = Char.isLetter c || c == '_'


isGoIdentChar :: Char -> Bool
isGoIdentChar c = Char.isAlphaNum c || c == '_'


-- | Identifiers that must NOT leak through to emitted Go as-is.
-- `goSafeName` appends `_` to any Sky identifier in this list
-- (the user's `init` becomes Go `init_`, etc.). Three risk tiers:
--
--   1. `init` — Go runs `func init()` at package load. A user
--      binding named `init` lowered as `func init()` would silently
--      execute at startup. Module prefixing alone can't save us:
--      Sky's TEA convention is `init = …` everywhere.
--
--   2. Reserved keywords — `for`, `case`, etc. are syntactic.
--      A user local named `for` is a syntax error in Go.
--
--   3. Predeclared identifiers — Go *allows* shadowing `string` /
--      `error` / `true` etc., but it breaks user reasoning and any
--      same-scope code that references the predeclared meaning.
--      Module prefix saves top-level bindings; this list saves
--      locals + parameters.
--
-- Special-cased OUTSIDE this list:
--   - `main`     — emitted as Go's program-entry `func main()`.
--   - `if`/`else`/`nil` — Sky parser rejects them as identifiers
--     before they ever reach codegen.
reservedGoNames :: [String]
reservedGoNames =
    [ "init"      -- Go's package init has special semantics
    -- Predeclared funcs
    , "new", "make", "len", "cap", "copy", "append", "delete"
    , "panic", "recover", "print", "println"
    , "clear", "min", "max", "complex", "imag", "real", "close"  -- Go 1.21+ and pre-1.21
    -- Reserved keywords
    , "type", "func", "var", "const", "interface", "struct"
    , "map", "chan", "go", "defer", "goto", "fallthrough"
    , "range", "return", "for", "switch", "case", "default"
    , "break", "continue", "import", "package", "select"
    -- Predeclared types (Go tolerates shadowing but reasoning breaks)
    , "bool", "byte", "rune", "string", "error", "any", "comparable"
    , "int", "int8", "int16", "int32", "int64"
    , "uint", "uint8", "uint16", "uint32", "uint64", "uintptr"
    , "float32", "float64", "complex64", "complex128"
    -- Predeclared constants / nil
    , "true", "false", "iota", "nil"
    ]


-- | Generate typed function parameters and return type from a solved type
typedFuncSig :: [Can.Pattern] -> T.Type -> ([GoIr.GoParam], String)
typedFuncSig params funcType =
    let (argTypes, retType) = splitFuncType (length params) funcType
        goParams = zipWith (\pat ty ->
            GoIr.GoParam (patternName pat) (GoType.typeToGo ty))
            params argTypes
    in (goParams, GoType.typeToGo retType)


-- | Split a function type into argument types and return type
-- | True when an inferred Go type reference can't safely be emitted
-- as a function return yet. Reject:
--   * Bare type parameters ("A", "T_a")
--   * Runtime types that aren't actually defined
--     (SkyList/SkyDict/SkySet/SkyCmd/SkySub are conceptual — their
--     values flow as `any` at runtime)
--   * The literal string "any"
isPolymorphicRet :: String -> Bool
isPolymorphicRet s
    | s == "any" = True
    -- Reject anywhere-in-string references to runtime types that aren't
    -- actually defined (they flow as `any` at runtime so the type would
    -- be an undefined identifier at Go-build time).
    | any (`isInfixOfStr` s)
          ["rt.SkyList", "rt.SkyDict", "rt.SkySet", "rt.SkyCmd", "rt.SkySub"] = True
    -- Reject leading underscores (malformed — happens when typeToGo
    -- combines empty module prefix with type name) and known-unresolved
    -- kernel types we haven't mapped yet (VNode from Std.Html).
    | take 1 s == "_" = True
    | any (`isInfixOfStr` s) ["_VNode", "VNode"] = True
    | otherwise =
        let hasBareUpperWord = any isPolyWord (words (replaceBrackets s))
        in hasBareUpperWord
  where
    replaceBrackets = map (\c -> if c `elem` ("[],*" :: String) then ' ' else c)
    isPolyWord w = case w of
        [c] | c >= 'A' && c <= 'Z' -> True
        ('T':'_':_)                -> True
        _                          -> False
    isInfixOfStr needle hay = any (isPrefixOfStr needle) (tails hay)
    isPrefixOfStr p str = take (length p) str == p
    tails [] = [[]]
    tails xs@(_:rest) = xs : tails rest


-- | T4: wrap a function body's raw Go expression so it matches the
-- declared Go return type at runtime. For parametric types like
-- `rt.SkyResult[E, A]`, a plain `any(body).(T)` assertion fails when
-- the body is built via the default `rt.Ok[any, any]` and the target
-- has specific E/A — the generic instantiations are distinct Go types.
-- ResultCoerce/MaybeCoerce reconstruct the value with target params.
-- | Render an inline coercion expression from `any` to `goTy`. String
-- fragment, emitted inside record-ctor field initialisers. See
-- wrapTypedReturn for the GoExpr-level equivalent.
coerceExprFor :: String -> String -> String
coerceExprFor goTy src = case goTy of
    "any"     -> src
    "string"  -> "rt.CoerceString(" ++ src ++ ")"
    "int"     -> "rt.CoerceInt(" ++ src ++ ")"
    "bool"    -> "rt.CoerceBool(" ++ src ++ ")"
    "float64" -> "rt.CoerceFloat(" ++ src ++ ")"
    _
      -- Cross-instantiation coerce for containers: SkyMaybe[any] → SkyMaybe[T]
      | Just params <- stripParametric "rt.SkyResult" goTy
        -> "rt.ResultCoerce[" ++ eraseTypeParams params ++ "](" ++ src ++ ")"
      | Just inner <- stripParametric "rt.SkyMaybe" goTy
        -> "rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "](" ++ src ++ ")"
      | otherwise ->
          let erased = eraseTypeParams goTy
          in if erased == "any" then src
             else "rt.Coerce[" ++ erased ++ "](" ++ src ++ ")"


-- | v0.13 typed lowerer: zero value literal for a Go type.  Returns
-- Nothing for types whose zero value can't be written as a simple
-- literal (Result/Maybe/Task generic structs, opaque aliases) — in
-- those cases the IIFE stays `func() any` (the trailing unreachable
-- `return nil` sentinel needs `any`).
goZeroValue :: String -> Maybe String
goZeroValue t = case t of
    "int"      -> Just "0"
    "int64"    -> Just "0"
    "float64"  -> Just "0.0"
    "bool"     -> Just "false"
    "string"   -> Just "\"\""
    "rune"     -> Just "0"
    "struct{}" -> Just "struct{}{}"
    "any"      -> Just "nil"
    "rt.SkyCmd" -> Just "rt.SkyCmd{}"
    "rt.SkySub" -> Just "rt.SkySub{}"
    "rt.SkyValue" -> Just "nil"          -- alias for `any`
    "rt.SkyDecoder" -> Just "nil"        -- alias for `any`
    _ | "[]" `List.isPrefixOf` t           -> Just "nil"  -- nil slice
      | "map[" `List.isPrefixOf` t         -> Just "nil"  -- nil map
      | "*" `List.isPrefixOf` t            -> Just "nil"  -- nil ptr
      -- Parametric Sky runtime structs zero via `T{}` — the
      -- generic-struct zero value is always valid Go and the
      -- trailing IIFE `return` is provably unreachable (every
      -- case/if branch returns), so the value is never observed.
      -- EXCEPTION: `rt.SkyTask[E, A]` is `func() SkyResult[E, A]`
      -- — a FUNC type, not a struct — so its zero is `nil`, not
      -- `T{}` (which Go rejects: "invalid composite literal type").
      | "rt.SkyResult[" `List.isPrefixOf` t -> Just (t ++ "{}")
      | "rt.SkyMaybe["  `List.isPrefixOf` t -> Just (t ++ "{}")
      | "rt.SkyTask["   `List.isPrefixOf` t -> Just "nil"
      | "rt.T2["        `List.isPrefixOf` t -> Just (t ++ "{}")
      | "rt.T3["        `List.isPrefixOf` t -> Just (t ++ "{}")
      | "rt.T4["        `List.isPrefixOf` t -> Just (t ++ "{}")
      | "rt.T5["        `List.isPrefixOf` t -> Just (t ++ "{}")
      | t == "rt.SkyTuple2" || t == "rt.SkyTuple3"
        || t == "rt.SkyTupleN"             -> Just (t ++ "{}")
      | t == "rt.SkyADT"                   -> Just "rt.SkyADT{}"
      | t == "rt.VNode"                    -> Just "rt.VNode{}"
      -- Record-alias structs (`Foo_R`) and Sky ADT struct aliases
      -- zero via `T{}`.
      | "_R" `List.isSuffixOf` t           -> Just (t ++ "{}")
      -- v0.15 Stage E — parametric alias instantiation like
      -- `Foo_R[Bar]`: same `T{}` form.  Both `_R[` infix AND `]`
      -- suffix required to avoid false-matching shapes like
      -- `rt.SkyResult[any, Foo_R[Bar]]` (those have their own arms
      -- above).
      | "_R[" `List.isInfixOf` t && "]" `List.isSuffixOf` t
                                           -> Just (t ++ "{}")
      -- A Go generic type parameter (`T1`, `T2`, …) in scope: its
      -- zero value is `*new(T)` (the standard expression-form zero
      -- for a type param).  Only valid INSIDE the generic function
      -- that declares the param — `exprToGoExpectGo` only threads a
      -- type-param `goRendering` at function-body emit sites (where
      -- the param IS in scope); `coerceCallArgsAt` explicitly
      -- excludes type params from its call-site threading.  The
      -- monomorphiser's `substTypeParamsInString` rewrites `*new(T1)`
      -- → `*new(int)` etc. when specialising, so the zero stays
      -- correct after instance expansion.
      | isGenericTypeParam t               -> Just ("*new(" ++ t ++ ")")
      -- A bare capitalised identifier: resolve via the codegen
      -- env's union/enum registries.
      --   * Enum union (`type X = int`)        → zero is `0`.
      --   * Tagged ADT (`type X = rt.SkyADT`)  → zero is `X{}`.
      --   * Unknown (FFI-opaque, unresolved)   → Nothing — keep
      --     the IIFE `func() any` rather than risk invalid Go.
      | isBareCapName t ->
          let env = getCgEnv
              enums  = Rec._cg_enumNames env
              unions = Rec._cg_unionNames env
              -- The Go type string may be module-qualified
              -- (`Chess_Piece_Colour`) while the registry holds
              -- both qualified (dep) and unqualified (entry)
              -- forms.  Try the full name AND the last `_`-segment.
              lastSeg = reverse (takeWhile (/= '_') (reverse t))
              isEnum  = Set.member t enums  || Set.member lastSeg enums
              isUnion = Set.member t unions || Set.member lastSeg unions
          in if isEnum            then Just "0"
             else if isUnion      then Just (t ++ "{}")
             else Nothing
      | otherwise -> Nothing
  where
    isBareCapName s =
        not (null s)
        && (let c = head s in c >= 'A' && c <= 'Z')
        && all (\c -> (c >= 'A' && c <= 'Z')
                   || (c >= 'a' && c <= 'z')
                   || (c >= '0' && c <= '9')
                   || c == '_') s


-- | v0.13 typed lowerer: is this Go type string a REAL, emittable Go
-- type we can safely thread an expected-type into?  True iff we can
-- name a zero value for it (`goZeroValue` Just — proof it's a known
-- type) OR it's a `func(`-shaped type (valid Go, zero is `nil`).
-- `"any"` is explicitly excluded — threading `any` is a no-op and
-- the caller's fallback path (`coerceArg` / `any()` widening) must
-- handle it instead.  Used by both `exprToGoExpectGo`'s safety gate
-- and `coerceCallArgsAt`'s control-flow-arg branch.
isEmittableGoType :: String -> Bool
isEmittableGoType s =
    s /= "any"
    && (isJust (goZeroValue s) || "func(" `List.isPrefixOf` s)


-- | v0.13 typed lowerer: convert a `GoBlock` (IIFE returning `any`)
-- into a `GoTypedBlock` (IIFE returning the concrete `retType`) when
-- it's SAFE to do so.  "Safe" means:
--   * `retType` is concrete (not "any"), AND
--   * the block's trailing `result` either isn't the unreachable
--     `nil` sentinel, OR `retType` has a writable zero value.
--
-- Every `return` inside the block (including those nested in
-- `GoIf` / `GoSwitch` / `GoTypeSwitch` branches at the IIFE's own
-- level — NOT inside nested `GoFuncLit`s) is coerced to `retType`
-- via `wrapTypedReturn`.  Nested `GoBlock` return values are
-- recursively typed.
--
-- When NOT safe, falls back to `wrapTypedReturn retType body` — the
-- existing behaviour (outer `rt.CoerceX(func() any {...}())`).
typeIIFE :: String -> GoIr.GoExpr -> GoIr.GoExpr
typeIIFE retType body
    | retType == "any" = body
    | otherwise = case body of
        -- Idempotent: a body already typed to `retType` (e.g. it
        -- came through `exprToGoExpectGo retType` which produced a
        -- `GoTypedBlock retType …`) is returned untouched — no
        -- redundant `rt.Coerce` re-wrap.
        GoIr.GoTypedBlock t _ _ | t == retType -> body
        GoIr.GoBlock stmts result ->
            let result' = case result of
                    GoIr.GoRaw "nil" -> case goZeroValue retType of
                        Just zv -> GoIr.GoRaw zv
                        Nothing -> result  -- can't zero — bail below
                    _ -> coerceReturnExprT retType result
                canType = case result of
                    GoIr.GoRaw "nil" -> isJust (goZeroValue retType)
                    _                -> True
            in if canType
                 then GoIr.GoTypedBlock retType
                        (coerceBlockReturnsT retType stmts) result'
                 else wrapTypedReturn retType body
        _ -> wrapTypedReturn retType body


-- | Walk IIFE-level statements coercing every `return` value to
-- `retType`.  Descends into `GoIf` / `GoSwitch` / `GoTypeSwitch`
-- branches (still the same IIFE's control flow) but NOT into
-- `GoFuncLit` (a nested closure has its own return type).
coerceBlockReturnsT :: String -> [GoIr.GoStmt] -> [GoIr.GoStmt]
coerceBlockReturnsT retType = map go
  where
    go stmt = case stmt of
        GoIr.GoReturn e          -> GoIr.GoReturn (coerceReturnExprT retType e)
        GoIr.GoIf c thn els      -> GoIr.GoIf c (coerceBlockReturnsT retType thn)
                                                (coerceBlockReturnsT retType els)
        GoIr.GoSwitch e brs      -> GoIr.GoSwitch e
                                      [ (v, coerceBlockReturnsT retType b)
                                      | (v, b) <- brs ]
        GoIr.GoTypeSwitch n e brs -> GoIr.GoTypeSwitch n e
                                      [ (t, coerceBlockReturnsT retType b)
                                      | (t, b) <- brs ]
        GoIr.GoBlock_ ss         -> GoIr.GoBlock_ (coerceBlockReturnsT retType ss)
        _                        -> stmt


-- | Coerce a single return-value expression to `retType`.  If it's
-- itself a `GoBlock` (nested IIFE), recursively type it instead of
-- wrapping — that keeps the nesting `any`-free.  `GoRaw "nil"`
-- (unreachable sentinel) is left as-is in branch positions; the
-- caller's `coerceBlockReturnsT` only reaches it when a branch
-- explicitly `return nil`s, which is the dead-code arm — Go accepts
-- `return nil` only for nilable types, so for non-nilable retTypes
-- we substitute the zero value.
coerceReturnExprT :: String -> GoIr.GoExpr -> GoIr.GoExpr
coerceReturnExprT retType e = case e of
    GoIr.GoBlock _ _ -> typeIIFE retType e
    GoIr.GoRaw "nil" -> case goZeroValue retType of
        Just zv -> GoIr.GoRaw zv
        Nothing -> e
    _ -> wrapTypedReturn retType e


-- | v0.13 typed lowerer: best-effort STATIC Go type of a GoExpr.
-- Returns `Just t` ONLY when the expression's Go type is provably
-- `t`.  Used to elide redundant coercions — `rt.CoerceInt(x)` when
-- `x` is already statically `int`.
--
-- Conservative by construction: `Nothing` ("unknown") always keeps
-- the coercion, so a MISSED case is harmless.  A WRONG `Just` would
-- skip a needed coercion and break `go build`, so every arm must be
-- certain.
--
-- v0.15.8 (Cycle-01 / Plan-Item P2 / Gap A2): the function gains a
-- `Maybe Can.Expr` first parameter carrying the SOURCE expression
-- that produced this `GoExpr`.  Pre-fix, `goExprGoType` covered
-- only by-shape recovery (literals, kernel coerce calls, typed
-- func-lits, etc.) and returned Nothing for `GoCall` to a
-- polymorphic dep function — even though the HM solver knows the
-- return type (`pipeline 5 : Result Error String` for the audit
-- A2 reproducer).  Downstream coercions that gate on
-- `Just srcTy <- goExprGoType e` then collapse to `rt.Coerce`
-- wraps or wide-instantiation casts, losing precision and
-- inflating both binary size and runtime cost.
--
-- The fallback fires when:
--   * the by-shape classifier returns Nothing AND
--   * `mSrc` is `Just` AND
--   * the GoExpr's IR shape is structurally safe for HM-to-static
--     identification (`GoCall (GoIdent fn) _` for a Sky-emitted
--     non-rt non-synthetic user function, where the emitted return
--     value reliably is the declared return type without `any`
--     widening).
--
-- CRITICAL three-way σ consensus rule (see
-- `docs/v0.15.x-hardening/arbitrations/HEAD-CYCLE-01-P2.md`):
-- the structural fallback's positive type info is unsafe to
-- consume at ANY of the three voting sites of the σ /
-- TVar-erasure / coerceArg-skip-check consensus:
--   (1) σ-recovery in `coerceCallArgs(At)` / kernel fallback
--       (`kernelCoerceArg`),
--   (2) `eraseTypeParams` / `containsGenericTypeParam` / TVar
--       erasure logic,
--   (3) `coerceArg`'s skip-check (`goExprGoType ... == Just ty
--       → e` arm).
-- These three vote on the typed TVar instantiation of a
-- polymorphic kernel call; they must agree (lossy or precise)
-- to keep Go's call-site inference consistent across sibling
-- args.  See Step 3 of the arbitration for the load-bearing
-- detail.  Sites that DO safely consume the fallback (the
-- parametric-alias arm at line 8536 below; `wrapTypedReturn` and
-- `coerceToFieldType` already-pass-Nothing sites that aren't
-- voters) pass `mSrc`.  All other callers MUST pass `Nothing`.
goExprGoType :: Maybe Can.Expr -> GoIr.GoExpr -> Maybe String
goExprGoType mSrc e = case shapeClassified of
    Just t  -> Just t
    Nothing -> structuralFallback
  where
    -- Restrict the fallback to GoCall (GoIdent name) _ shapes
    -- where `name` is a Sky-emitted user function (NOT `rt.*` —
    -- those have their own classification arm in shapeClassified;
    -- NOT `__`-prefixed synthetic auto-record / partial-app
    -- names).  These shapes RELIABLY return their declared Go
    -- type without `any` widening, so HM type === GoExpr static
    -- type for them.  For GoIdent / GoSelector / GoBinary / case-
    -- destructured field reads the emitted value may be `any`
    -- even though HM thinks the variable holds `T` — feeding
    -- those through the fallback would produce false-positive
    -- matches and skip needed coercions.
    structurallySafeForFallback ge = case ge of
        GoIr.GoCall (GoIr.GoIdent name) _
            | not ("rt." `List.isPrefixOf` name)
            , not ("__" `List.isPrefixOf` name) -> True
        _ -> False

    structuralFallback = case mSrc of
        Just src | structurallySafeForFallback e ->
            let solved = Rec._cg_solvedTypes getCgEnv
            in case inferExprType solved src of
                Just ty
                  -- Reject HM types that still carry an unresolved
                  -- TVar — `solvedTypeToGo` would render the leaf
                  -- as `any`, falsely matching a target ty="any"
                  -- shape and skipping a needed coercion (the
                  -- runtime value may be `[]rt.SkyTuple2`, not
                  -- `[]any`).  The lambda-types arm above already
                  -- restricts to primitives for the same reason.
                  | hasUnresolvedTVar solved ty -> Nothing
                Just ty ->
                    let goTy = solvedTypeToGo ty
                    in if goTy /= "any"
                          && goTy /= ""
                          && not (isGenericTypeParam goTy)
                          && not (containsGenericTypeParam goTy)
                          && isEmittableGoType goTy
                       then Just goTy
                       else Nothing
                Nothing -> Nothing
        _ -> Nothing

    -- Walk a Sky type chasing TVar substitutions through `solved`
    -- and return True iff any sub-component remains an unresolved
    -- TVar (the post-substitution leaf is itself a TVar).
    hasUnresolvedTVar :: Solve.SolvedTypes -> T.Type -> Bool
    hasUnresolvedTVar solved = go Set.empty
      where
        go seen t = case t of
            T.TVar name
                | Set.member name seen -> True
                | otherwise -> case Solve.lookupSolvedVar name solved of
                    Just t' | t' /= t -> go (Set.insert name seen) t'
                    _                 -> True
            T.TType _ _ args         -> any (go seen) args
            T.TAlias _ _ _ inner     -> case inner of
                T.Filled t'  -> go seen t'
                T.Hoisted t' -> go seen t'
            T.TRecord fs _           -> any
                (\(T.FieldType _ ft) -> go seen ft) (Map.elems fs)
            T.TTuple a b cs          -> go seen a || go seen b
                                          || any (go seen) cs
            T.TLambda a b            -> go seen a || go seen b
            T.TUnit                  -> False

    shapeClassified = case e of
        GoIr.GoIntLit _         -> Just "int"
        GoIr.GoFloatLit _       -> Just "float64"
        GoIr.GoStringLit _      -> Just "string"
        GoIr.GoBoolLit _        -> Just "bool"
        GoIr.GoRuneLit _        -> Just "rune"
        GoIr.GoTypedBlock t _ _ -> Just t
        GoIr.GoSliceLit t _     -> Just ("[]" ++ t)
        -- Composite literals carry their type verbatim.  `rt.SkyTuple2{…}`
        -- IS `rt.SkyTuple2`; `Foo_R{…}` IS `Foo_R` — no coercion needed.
        GoIr.GoStructLit t _    -> Just t
        GoIr.GoMapLit k v _     -> Just ("map[" ++ k ++ "]" ++ v)
        -- Coercion-helper results have a statically-known type.  The
        -- callee is emitted in two shapes — `GoIdent "rt.X"` and
        -- `GoQualified "rt" "X"` — so normalise via `rtCalleeName`.
        GoIr.GoCall callee _
            | Just fn <- rtCalleeName callee ->
                let fn' = "rt." ++ fn
                in case fn of
                    "CoerceInt"    -> Just "int"
                    "CoerceString" -> Just "string"
                    "CoerceBool"   -> Just "bool"
                    "CoerceFloat"  -> Just "float64"
                    "AsInt"        -> Just "int"
                    "AsString"     -> Just "string"
                    "AsBool"       -> Just "bool"
                    "AsFloat"      -> Just "float64"
                    -- `rt.Html_*` element builders return `rt.VNode`
                    -- (runtime ported in [v0.13] runtime — Html.*
                    -- builders return VNode).  `Html_render` renders to
                    -- a string; `Html_doctype` has a separate kernel-sig
                    -- mismatch — both stay `any`.
                    _ | "Html_" `List.isPrefixOf` fn
                      , fn /= "Html_render"
                      , fn /= "Html_doctype" -> Just "rt.VNode"
                      | Just t <- stripParametric "rt.Coerce" fn'       -> Just t
                      | Just t <- stripParametric "rt.AsListT" fn'      -> Just ("[]" ++ t)
                      | Just t <- stripParametric "rt.AsMapT" fn'       -> Just ("map[string]" ++ t)
                      | Just t <- stripParametric "rt.MaybeCoerce" fn'  -> Just ("rt.SkyMaybe[" ++ t ++ "]")
                      | Just t <- stripParametric "rt.ResultCoerce" fn' -> Just ("rt.SkyResult[" ++ t ++ "]")
                      | Just t <- stripParametric "rt.TaskCoerceT" fn'  -> Just ("rt.SkyTask[" ++ t ++ "]")
                      -- v0.13 Stage 1 — AsListAny/AsMapAny widen to []any
                      -- structurally; recovery σ uses this to know the
                      -- shape (and from there, if inner is typed via
                      -- structural recovery, T1 can be pinned).
                      | fn == "AsListAny" -> Just "[]any"
                      | fn == "AsMapAny"  -> Just "map[string]any"
                      | otherwise -> Nothing
        -- v0.13 Stage 1 — zero-arg call to a top-level Sky function.
        -- `loadHistory()` (`Can.VarTopLevel … "loadHistory"` applied to
        -- `[]`) returns `loadHistory`'s typed result. Reporting that
        -- type here lets σ-recovery at the surrounding HOF call pin
        -- TVars from a typed Task / Result return. Without this, a
        -- pattern like `Cmd.perform loadHistory HistoryLoaded` had no
        -- way to derive that `loadHistory` produces `Task Error
        -- (List Snapshot)` → `Cmd.perform`'s `e, a` TVars stayed
        -- `any` → callback slot widened to
        -- `func(SkyResult[any, any]) any` → `HistoryLoaded` (typed
        -- ctor) got wrapped in `rt.Coerce`.
        GoIr.GoCall (GoIr.GoIdent name) []
            | not ("rt." `List.isPrefixOf` name) ->
                let env = getCgEnv
                    retTy = Map.findWithDefault "any" name
                              (Rec._cg_funcRetType env)
                    paramTys = Map.findWithDefault [] name
                              (Rec._cg_funcParamTypes env)
                in if null paramTys
                      && retTy /= "any"
                      && not (isGenericTypeParam retTy)
                   then Just retTy
                   else Nothing
        -- Comparison / logical binops are Go-bool.
        GoIr.GoBinary op _ _
            | op `elem` ["==", "!=", "<", ">", "<=", ">=", "&&", "||"] -> Just "bool"
        -- Arithmetic binops: result type = operand type when both
        -- operands have the SAME known primitive type.
        --
        -- v0.15.8 (P2): the recursive walks pass `Nothing` for the
        -- operand's source `Can.Expr` because the binop arm doesn't
        -- carry the operand's Can.Expr through GoIR.  The strict
        -- shape recovery is still authoritative for primitive
        -- arithmetic; structural fallback wouldn't add precision
        -- here even if a Can.Expr were available.
        GoIr.GoBinary op l r
            | op `elem` ["+", "-", "*", "/", "%"]
            , Just lt <- goExprGoType Nothing l
            , Just rt' <- goExprGoType Nothing r
            , lt == rt'
            , lt `elem` ["int", "float64", "string"]
            -> Just lt
        -- A bare identifier registered in the lambda-type context.
        -- RESTRICTED to primitive types: `solvedTypeToGo` renders some
        -- composite types (notably tuples → `rt.T2[A,B]`) differently
        -- from how the variable is actually EMITTED (tuple vars are
        -- `[]rt.SkyTuple2` / `rt.SkyTuple2`).  Trusting the HM type for
        -- those would wrongly elide a needed coercion.  Primitives
        -- (`int` / `float64` / `string` / `bool` / `rune`) render
        -- identically everywhere, so they're safe.
        -- v0.13 Stage 1 — runtime-kernel fn referenced as a HOF arg.
        -- INTENTIONALLY DISABLED: returning the Sky kernel sig (e.g.
        -- `func(int) string` for `String.fromInt`) was unsafe because
        -- the ACTUAL Go runtime fn `rt.String_fromInt` has sig
        -- `func(any) any`. When σ-recovery pinned typed TVars from this
        -- lying sig, the resulting Sky_Core_List_map_ call instantiated
        -- with typed `[T1=int, T2=string]` and then handed the
        -- `func(any) any` runtime fn at the typed slot — Go's inference
        -- conflicted with the surrounding typed args (e.g.
        -- `rt.AsListT[int](ages)`) and rejected the call. The only safe
        -- routings are at sites where the runtime ALSO has the typed
        -- variant in scope; not all kernels do, and the gain (closing
        -- a small subset of FFI-adjacent adapter wraps) doesn't justify
        -- the fragility. Reverted; the wraps stay, the reflect-based
        -- Coerce adapter handles both shapes correctly.
        GoIr.GoQualified "rt" _fn -> Nothing
        GoIr.GoIdent name
            | "rt." `List.isPrefixOf` name -> Nothing
        GoIr.GoIdent name -> case lookupLambdaType name of
            Just t | isTypedPrimitive t -> Just (solvedTypeToGo t)
            -- v0.15.3 — typed parametric-record-alias param/let-binding:
            -- recover the Go-rendered alias instantiation so the call-
            -- site coerceArg can short-circuit the nominal `.(Foo_R[any])`
            -- assertion (which panics on cross-instantiation).
            -- Example:
            --   view : Setup msg -> Element msg
            --   view cfg = ... body cfg ...
            -- `cfg` is registered as TAlias "Setup" [...] (Filled (TRecord …)).
            -- Rendering returns `Setup_R[T1]` (preserving the in-scope
            -- generic param); coerceArg then sees `body`'s param type
            -- `Setup_R[T1]` matches source `Setup_R[T1]` and emits raw.
            Just t
              | let goTy = solvedTypeToGoPreserveTVars t
              , isJust (parametricAliasBase goTy) -> Just goTy
            Just t@(T.TLambda _ _) ->
                -- v0.13 Stage 1 (task #189) — use TVar-preserving render
                -- so identifiers like T1/T2 (Go-side generic type
                -- parameters in scope inside the enclosing function)
                -- survive instead of being erased to `any`. Solves
                -- recursive-call adapters in Sky-source kernel bodies
                -- where `fn : func(T1) T2` should flow raw into the
                -- recursive call without `rt.Coerce[func(any) any]`.
                let goTy = solvedTypeToGoPreserveTVars t
                in if take 5 goTy == "func("
                     then Just goTy
                     else Nothing
            _ | Just goTy <- lookupLambdaGoStr name -> Just goTy
            _ ->
                -- v0.13 Stage 1 — top-level Sky function passed as a
                -- HOF arg: look up its Go param + return types from
                -- the codegen env. Critical for the
                -- `Sky_Core_List_map_(rt.Coerce[func(any) any](TopLevelFn), …)`
                -- adapter wrap class. Once goExprGoType returns the
                -- typed sig for `TopLevelFn`, the call-site coerceArg
                -- short-circuits (target type matches source type) and
                -- emits no wrap.
                let env = getCgEnv
                    paramTys = Map.findWithDefault [] name
                                  (Rec._cg_funcParamTypes env)
                    retTy = Map.findWithDefault "any" name
                                  (Rec._cg_funcRetType env)
                in if not (null paramTys)
                      && any (/= "any") paramTys
                      && all (\t -> t /= ""
                                    && not (isGenericTypeParam t))
                             paramTys
                      && not (isGenericTypeParam retTy)
                     then Just ("func("
                                ++ intercalateComma paramTys
                                ++ ") " ++ retTy)
                     else Nothing
        -- v0.13 Stage 1 — typed function literal carries its full Go
        -- signature. Used by the call-site recovery σ to deduce TVar
        -- substitutions from typed lambda args: a literal
        -- `func(x State_Post_R) State_Post_R { … }` against a
        -- callee param of type `func(T1) T2` unifies T1 = State_Post_R,
        -- T2 = State_Post_R, so the call site can route through the
        -- typed `[State_Post_R, State_Post_R]` generic instantiation.
        GoIr.GoFuncLit params retTy _stmts ->
            let paramTys = [pty | GoIr.GoParam _ pty <- params]
            in Just ("func(" ++ intercalateComma paramTys ++ ") " ++ retTy)
        _ -> Nothing

    -- Normalise an `rt.*` callee to its bare function name,
    -- accepting both `GoIdent "rt.Foo"` and `GoQualified "rt" "Foo"`.
    rtCalleeName (GoIr.GoQualified "rt" fn) = Just fn
    rtCalleeName (GoIr.GoIdent name)
        | "rt." `List.isPrefixOf` name = Just (drop 3 name)
    rtCalleeName _ = Nothing


wrapTypedReturn :: String -> GoIr.GoExpr -> GoIr.GoExpr
wrapTypedReturn retType body
    | retType == "any" = body
    -- v0.13 typed lowerer: skip the coercion when `body` is already
    -- provably the target type — no redundant `rt.CoerceInt(int)` /
    -- `rt.Coerce[T](T)` wrap.
    --
    -- v0.15.8 (P2): no source `Can.Expr` is in scope here (the
    -- caller has already lowered the expression).  Keep the strict
    -- by-shape recovery; structural fallback fires at the
    -- call-arg sites where the source expr IS still in scope.
    | goExprGoType Nothing body == Just retType = body
    | Just params <- stripParametric "rt.SkyResult" retType =
        GoIr.GoCall
            (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]"))
            [body]
    | Just inner <- stripParametric "rt.SkyMaybe" retType =
        GoIr.GoCall
            (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]"))
            [body]
    | Just params <- stripParametric "rt.SkyTask" retType =
        GoIr.GoCall (GoIr.GoIdent ("rt.TaskCoerceT[" ++ params ++ "]")) [body]
    -- Audit P0-3: replace raw `any(body).(T)` with a runtime Coerce
    -- helper. Direct assertion panics with a cryptic 'interface
    -- conversion' message on mismatch; Coerce gives a site-identified
    -- diagnostic and propagates via rt panic-recovery as Err. Also
    -- handles reflect-convertible types (numeric widenings, typed
    -- aliases) which the raw assertion rejects.
    | retType == "string" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceString") [body]
    | retType == "int" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceInt") [body]
    | retType == "bool" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceBool") [body]
    | retType == "float64" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceFloat") [body]
    -- Typed slice / typed string-keyed map: route through AsListT /
    -- AsMapT so a body returning []any{} (the polymorphic empty
    -- shape) converts losslessly to the typed slice/map. The strict
    -- rt.Coerce[[]T] / rt.Coerce[map[string]V] would panic on the
    -- `[]any{}` → typed-slice/map case.
    | Just elemGo <- stripSlice retType =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elemGo ++ "]")) [body]
    | Just valGo <- stripStringMap retType =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valGo ++ "]")) [body]
    | otherwise =
        GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ retType ++ "]")) [body]


-- | If `s` is shaped like `<prefix>[params]`, return `params`;
-- otherwise Nothing. Handles nested brackets by counting depth.
stripParametric :: String -> String -> Maybe String
stripParametric prefix s
    | take (length prefix) s == prefix, drop (length prefix) s /= "" =
        let rest = drop (length prefix) s
        in case rest of
            '[':_ ->
                let inner = dropLast1 (drop 1 rest)
                in if not (null inner) then Just inner else Nothing
            _ -> Nothing
    | otherwise = Nothing
  where
    dropLast1 [] = []
    dropLast1 [_] = []
    dropLast1 (x:xs) = x : dropLast1 xs


-- | Decide whether a Sky type can be safely emitted as a Go return
-- | Build a cross-module external-signature map from per-module
-- solved types. Only fully concrete types (no free TVars at all)
-- cross — the entry module's solver instantiates each call site
-- fresh via CForeign, so polymorphic signatures would land with
-- no constraint on the fresh TVars, which is worse than a local
-- inference. Concrete types let the entry solver propagate real
-- information (int, String, SkyTuple2, Maybe SomeAdt, etc.) to
-- the caller's fresh var.
--
-- Also rejects types containing solver-internal placeholder TVars
-- (names starting with `_` or of length > 1) — those are
-- unresolved bindings the solver couldn't close; forwarding them
-- as external annotations masks the underlying inference gap.
-- | Build an external signature map from per-module solved types.
-- Takes a list of (modName, Can.Module) to cross-reference type
-- names against their actual defining module when a solved type
-- has unresolved (empty) homes. This fixup is necessary because
-- pass-1 canonicalisation in each dep uses that dep's own tmap,
-- which misses type names the dep references without importing
-- (Chess.Ai uses `Model` without `import State`).
-- | v0.13 Phase A4: build a `Sky-qualName → Can.Def` map covering
-- the entry module + every dep.  Keys use the FULL Sky-source
-- qualified name (`"Sky.Core.List.foldl"`), not the mangled Go name
-- (`"Sky_Core_List_foldl"`).  Names match what
-- `Mono.collectCallSitesDef` produces from `Can.VarTopLevel` resolution.
--
-- Used by the reachability walker (`Mono.reachableInstances`) to find
-- the body of each invoked function so it can collect THAT body's
-- call sites and continue the transitive closure.
buildDefMap
    :: Can.Module
    -> [(String, Can.Module)]
    -> Map.Map String Can.Def
buildDefMap canMod validDeps =
    let entryName = case Can._name canMod of
            ModuleName.Canonical s -> s
        entryDefs = collectModuleDefs entryName canMod
        depDefs = concatMap (\(mn, dm) -> collectModuleDefs mn dm) validDeps
    in Map.fromList (entryDefs ++ depDefs)
  where
    collectModuleDefs modName m = walkDecls modName (Can._decls m) []
    walkDecls _      Can.SaveTheEnvironment acc = acc
    walkDecls modN (Can.Declare def rest) acc =
        walkDecls modN rest (defEntry modN def ++ acc)
    walkDecls modN (Can.DeclareRec def defs rest) acc =
        walkDecls modN rest
            (concatMap (defEntry modN) (def : defs) ++ acc)
    defEntry modN def = case def of
        Can.Def (A.At _ n) _ _      -> [(modN ++ "." ++ n, def)]
        Can.TypedDef (A.At _ n) _ _ _ _ -> [(modN ++ "." ++ n, def)]
        Can.DestructDef _ _         -> []  -- skip pattern-bindings


-- | v0.14.x Stage 4: collect every Sky-source binding whose body is a
-- kernel alias of the shape `name = Ffi.kernel "KernelName"`.  The
-- kernel name is split at the first `_` into `(modPart, funcPart)`
-- (matching the runtime's `KernelMod_funcName` Go-side convention) so
-- the build-time call-site rewrite can route `Sky.Core.X.foo` to
-- `Can.VarKernel "X" "foo"`.
--
-- A binding qualifies when its body is exactly `Ffi.kernel "K_n"`:
--   * `Can.Call (Can.VarKernel "Ffi" "kernel") [Can.Str "K_n"]`
-- or its single-Unit-applied form (auto-generated zero-arg shape).
-- Defs with patterns (function with explicit params) are ignored —
-- the value-binding shape is the canonical Layer 3 pattern.
collectKernelAliases
    :: (String, Can.Module)
    -> [((ModuleName.Canonical, String), (String, String))]
collectKernelAliases (_modName, m) =
    let home = Can._name m
    in walkDecls home (Can._decls m) []
  where
    walkDecls _ Can.SaveTheEnvironment acc = acc
    walkDecls h (Can.Declare def rest) acc =
        walkDecls h rest (defAlias h def ++ acc)
    walkDecls h (Can.DeclareRec def defs rest) acc =
        walkDecls h rest (concatMap (defAlias h) (def : defs) ++ acc)

    defAlias h def = case def of
        Can.Def (A.At _ name) [] body ->
            maybe [] (\kp -> [((h, name), kp)]) (kernelAliasBody body)
        Can.TypedDef (A.At _ name) _ [] body _ ->
            maybe [] (\kp -> [((h, name), kp)]) (kernelAliasBody body)
        _ -> []

    -- Body must be `Ffi.kernel "Mod_func"`.  Returns the split
    -- `(Mod, func)` pair so the call-site rewrite can emit a typed
    -- kernel dispatch matching the existing `(modName, funcName)` keys
    -- in `lookupKernelType` / the runtime registry.
    kernelAliasBody (A.At _ inner) = case inner of
        Can.Call (A.At _ (Can.VarKernel "Ffi" "kernel"))
                 [A.At _ (Can.Str raw)] ->
            splitKernelName raw
        _ -> Nothing

    splitKernelName raw = case break (== '_') raw of
        (kMod, '_' : kFn) | not (null kMod), not (null kFn) ->
            Just (kMod, kFn)
        _ -> Nothing


-- | v0.13 Phase A4: build a `Sky-qualName → generalised-annotation`
-- map covering the entry module + every dep.  Each annotation comes
-- from `generaliseToAnnotation` applied to the function's solved
-- type — same shape the solver registered in `globalExternals` so
-- the Forall var names align with `_instance_quantifiers`.
buildAnnotMap
    :: Map.Map String T.Type                        -- entry solvedTypes
    -> [(String, Map.Map String T.Type)]            -- dep solvedTypes
    -> Map.Map String T.Annotation
buildAnnotMap entrySolved depSolved =
    let entryEntries =
            [ ("Main." ++ n, generaliseToAnnotation ty)
            | (n, ty) <- Map.toList entrySolved
            ]
        depEntries =
            [ (modName ++ "." ++ n, generaliseToAnnotation ty)
            | (modName, types) <- depSolved
            , (n, ty) <- Map.toList types
            ]
    in Map.fromList (entryEntries ++ depEntries)


-- | v0.13 Phase A4: extract the entry module's Sky-source module name
-- (used as the qualifier for `main`).  Falls back to "Main" if the
-- module header omits `module X exposing (…)`.
mainModuleName :: Src.Module -> Maybe String
mainModuleName srcMod = case Src._name srcMod of
    Just (A.At _ segs) -> Just (List.intercalate "." segs)
    Nothing -> Nothing


buildCrossModuleExternalsWithMods
    :: [(String, Can.Module)]
    -> [(String, Map.Map String T.Type)]
    -> Map.Map (String, String) T.Annotation
buildCrossModuleExternalsWithMods validDeps depSolved =
    let typeHomeMap = buildGlobalTypeHomeMap validDeps
        fixHomes = fixupHomes typeHomeMap
    in Map.fromList
        -- Register every top-level dep declaration's solved type as a
        -- cross-module external, not just function-typed ones.
        --
        -- Pre-fix bug: an `isFunctionType` filter dropped bare values
        -- like `Std.Ui.fill : Length`. The constrain path then fell
        -- through to `T.CLocal` for `Ui.fill`, and the solver treated
        -- it as a fresh polymorphic variable — letting `Ui.fill 1`
        -- type-check (silently applying a value as if it were a
        -- function). Codegen then emitted `Std_Ui_fill(1)` which
        -- `go build` rejected with a confusing arity error rather
        -- than a clean Sky-level type error.
        --
        -- Registering bare values too lets the solver unify them
        -- against the call-site's `T1 -> T2` shape and fail cleanly
        -- ("can't unify Length with T1 -> T2") at sky check time.
        -- Sister fix to the closed-record unification gap above
        -- (#59) — both surfaced from a real-world Std.Ui port.
        [ ((modName, name), generaliseToAnnotation (fixHomes ty))
        | (modName, types) <- depSolved
        , (name, ty) <- Map.toList types
        ]


-- | Backwards-compat: previous buildCrossModuleExternals signature.
buildCrossModuleExternals
    :: [(String, Map.Map String T.Type)]
    -> Map.Map (String, String) T.Annotation
buildCrossModuleExternals = buildCrossModuleExternalsWithMods []


-- | Build a global map from type name → defining module by walking
-- every dep's declared unions and record aliases. When a pass-1
-- canonicalised annotation references `Model` with home="" (because
-- the referencing module didn't import State), we look it up here
-- and fix the home to the actual defining module.
buildGlobalTypeHomeMap
    :: [(String, Can.Module)]
    -> Map.Map String ModuleName.Canonical
buildGlobalTypeHomeMap validDeps =
    Map.fromList
        [ (typeName, Can._name depMod)
        | (_, depMod) <- validDeps
        , typeName <- Map.keys (Can._unions depMod)
                   ++ Map.keys (Can._aliases depMod)
        ]


-- | Walk a Canonical type and replace every empty-home nominal
-- reference whose name appears in the global type-home map with
-- its real home. Primitives keep their kernel homes; everything
-- else gets the resolved dep home.
fixupHomes :: Map.Map String ModuleName.Canonical -> T.Type -> T.Type
fixupHomes hmap = go
  where
    go ty = case ty of
        T.TType home name args ->
            let args' = map go args
                resolved = case Map.lookup name hmap of
                    Just h | null (ModuleName.toString home) -> h
                    _ -> home
            in T.TType resolved name args'
        T.TAlias home name pairs aliasType ->
            let pairs' = [(n, go t) | (n, t) <- pairs]
                resolved = case Map.lookup name hmap of
                    Just h | null (ModuleName.toString home) -> h
                    _ -> home
                aliasType' = case aliasType of
                    T.Filled i  -> T.Filled (go i)
                    T.Hoisted i -> T.Hoisted (go i)
            in T.TAlias resolved name pairs' aliasType'
        T.TLambda a b -> T.TLambda (go a) (go b)
        T.TTuple a b cs -> T.TTuple (go a) (go b) (map go cs)
        T.TRecord fields mExt ->
            T.TRecord (Map.map (\(T.FieldType i fTy) -> T.FieldType i (go fTy)) fields) mExt
        T.TVar n -> T.TVar n
        T.TUnit -> T.TUnit


-- | Generalise a solved type into a polymorphic Annotation by
-- quantifying every free TVar. This is Hindley-Milner's `gen` for
-- cross-module export: the annotation says "the caller decides
-- what to plug in for each TVar", which is correct for top-level
-- bindings that were HM-inferred without user-supplied annotation.
--
-- Solver-internal TVar names (_cargN, _fooN_res, etc.) are renamed
-- to plain user-level names (a, b, c, ...) before being quantified.
-- Without the rename, the annotation would reference names the
-- external consumer's solver can't produce at fresh instantiation,
-- and the cross-module channel silently drops those bindings.
generaliseToAnnotation :: T.Type -> T.Annotation
generaliseToAnnotation ty =
    let rawVars = collectFreeTVars ty
        (renamedTy, renamed) = renameSolverInternals rawVars ty
    in T.Forall renamed renamedTy


-- | Build a rename map from solver-internal TVar names to sequential
-- user-level names (a, b, c, …), then substitute throughout the type.
-- Returns (newType, newFreeVarList).
renameSolverInternals :: [String] -> T.Type -> (T.Type, [String])
renameSolverInternals rawVars ty =
    let userNames = [ [c] | c <- ['a' .. 'z'] ]
                 ++ [ [c] ++ show (i :: Int) | i <- [1..], c <- ['a' .. 'z'] ]
        rename = Map.fromList (zip rawVars userNames)
        newVars = map (\v -> Map.findWithDefault v v rename) rawVars
    in (substTVars rename ty, newVars)


-- | Apply a TVar name rename to every TVar in a type.
substTVars :: Map.Map String String -> T.Type -> T.Type
substTVars subst = go
  where
    go t = case t of
        T.TVar n -> T.TVar (Map.findWithDefault n n subst)
        T.TLambda a b -> T.TLambda (go a) (go b)
        T.TType home n args -> T.TType home n (map go args)
        T.TTuple a b cs -> T.TTuple (go a) (go b) (map go cs)
        T.TRecord fields mExt ->
            T.TRecord
                (Map.map (\(T.FieldType i fTy) -> T.FieldType i (go fTy)) fields)
                (fmap (\e -> Map.findWithDefault e e subst) mExt)
        T.TAlias home n pairs aliasType ->
            T.TAlias home n [(k, go v) | (k, v) <- pairs]
                (case aliasType of
                    T.Filled i -> T.Filled (go i)
                    T.Hoisted i -> T.Hoisted (go i))
        T.TUnit -> T.TUnit


collectFreeTVars :: T.Type -> [String]
collectFreeTVars = nubOrd . go
  where
    nubOrd [] = []
    nubOrd (x:xs) = x : nubOrd (filter (/= x) xs)
    go t = case t of
        T.TVar n -> [n]
        T.TLambda a b -> go a ++ go b
        T.TType _ _ args -> concatMap go args
        T.TTuple a b cs -> concatMap go (a : b : cs)
        T.TRecord fields mExt ->
            concatMap (\(T.FieldType _ fTy) -> go fTy) (Map.elems fields)
            -- The row-extension variable of an OPEN record is a free
            -- type var; collect it so `generaliseToAnnotation`
            -- quantifies it instead of leaking a bare row name into
            -- the consumer module's solver cache.
            ++ maybe [] (\n -> [n]) mExt
        T.TAlias _ _ pairs aliasType ->
            concatMap (go . snd) pairs ++ case aliasType of
                T.Filled i -> go i
                T.Hoisted i -> go i
        T.TUnit -> []


-- type today (T3). Accepts primitives, parametric Sky runtime types
-- (SkyResult/SkyMaybe/SkyTask), and user-defined ADTs / record
-- aliases (looking up the record-alias set in the codegen env to
-- append `_R` when needed). Rejects polymorphic type variables and
-- unmapped kernel types. Returns "any" for anything not safely
-- expressible.
safeReturnType :: T.Type -> String
safeReturnType t = case t of
    -- T4: Unit returns safely typed now — rt.ResultCoerce handles the
    -- generic-instantiation mismatch at the return wrap.
    T.TUnit                       -> "struct{}"
    T.TType _ "Int" []            -> "int"
    T.TType _ "Float" []          -> "float64"
    T.TType _ "Bool" []           -> "bool"
    T.TType _ "String" []         -> "string"
    T.TType _ "Char" []           -> "rune"
    T.TType _ "Bytes" []          -> "[]byte"
    T.TType _ "Result" [e, a]     -> "rt.SkyResult[" ++ safeReturnType e
                                     ++ ", " ++ safeReturnType a ++ "]"
    T.TType _ "Maybe"  [x]        -> "rt.SkyMaybe[" ++ safeReturnType x ++ "]"
    T.TType _ "Task"   [e, a]     -> "rt.SkyTask[" ++ safeReturnType e
                                     ++ ", " ++ safeReturnType a ++ "]"
    -- T5: list/dict/set typed as concrete Go types. User-code audit
    -- required in parallel — when a function annotated to return
    -- `Dict String String` actually holds mixed-type values (e.g.
    -- SQL COUNT(*) columns), the annotation is wrong and needs
    -- fixing.
    T.TType _ "Cmd"    _          -> "rt.SkyCmd"
    T.TType _ "Sub"    _          -> "rt.SkySub"
    T.TType _ "List"   [elem]     ->
        let inner = safeReturnType elem
        in if inner == "any" then "[]any" else "[]" ++ inner
    T.TType _ "List"   _          -> "[]any"
    T.TType _ "Dict"   [_, v]     ->
        let inner = safeReturnType v
        in if inner == "any" then "map[string]any" else "map[string]" ++ inner
    T.TType _ "Dict"   _          -> "map[string]any"
    T.TType _ "Set"    _          -> "map[any]bool"
    -- Tuples emit as rt.SkyTuple{2,3,N}. V0/V1/V2 remain `any`
    -- (SkyTuple2 = T2[any, any]) so current body codegen stays
    -- valid — tuple destructure in patternBindings wraps with any()
    -- before asserting.
    T.TTuple _ _ []               -> "rt.SkyTuple2"
    T.TTuple _ _ [_]              -> "rt.SkyTuple3"
    T.TTuple _ _ _                -> "rt.SkyTupleN"
    -- Opaque parameterised types whose Go alias is `any` regardless
    -- of type args (Decoder a, Value a). Match before the []-only
    -- TType branch so `Decoder String` resolves the same way.
    T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
    -- User-defined named type: only emit when it's a known record
    -- alias (then use `_R` suffix). Plain ADT unions stay `any` until
    -- we can guarantee every call site produces the exact struct type
    -- (not just `any(expr)`). Re-enable when T6 lands.
    --
    -- v0.13 B1: match `_` so parametric ADTs (e.g. `Html msg`,
    -- `Element msg`, `Attribute msg`) get the SAME erased-Go-name
    -- treatment as nullary types. Since `type Mod_Name = rt.SkyADT`
    -- is non-generic, the type arg is irrelevant to the Go name.
    -- The `isKnownUnion` gate at the bottom of this arm still
    -- keeps FFI-opaque parametric types (where no Go alias was
    -- emitted) falling through to `any`. Mirrors the equivalent
    -- arm in `safeReturnTypeWith` (which landed earlier in v0.13).
    T.TType home name _ ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            env = getCgEnv
            allAliases = Rec._cg_recordAliases env
            -- Try all known module prefixes so cross-module record
            -- aliases resolve correctly (e.g. "Model" → "State_Model_R").
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList allAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c allAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            -- Check runtime typed map for known concrete types. Qualified
            -- overrides (e.g. Sky.Core.Http.Response → rt.HttpResponse)
            -- win over the short-name default.
            runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                Just goTy -> Just goTy
                Nothing   -> lookup name runtimeTypedMap
            knownUnions = Rec._cg_unionNames env
            -- A name is "safe to emit as a Go type" only if we proved
            -- that an alias was emitted for it: it's a Sky union (then
            -- `type X = rt.SkyADT` is in main.go) or it's prefixed by
            -- the local module so the local-module union pass owned it.
            -- Otherwise (typical: FFI-opaque types like Bufio.Scanner),
            -- emitting `Bufio_Scanner` would dangle — fall back to any.
            isKnownUnion = Set.member base knownUnions
                        || Set.member name knownUnions
        in case matches of
            (m:_) -> m ++ "_R"
            _
                -- v0.13 B0: a Sky-defined union with a populated home
                -- takes precedence over `runtimeTypedMap`'s `rt.SkyX`
                -- alias. `Attribute` lives in both Std.Ui AND
                -- Std.Html.Attributes as a Sky-source ADT — emitting
                -- `rt.SkyAttribute` (= type alias for `any`) for the
                -- inferred `T.TType (Canonical "Std.Ui") "Attribute"`
                -- loses the typed Sky-emitted struct name and
                -- violates the v0.13 contract (no bare/aliased `any`
                -- for used types). The `_cg_unionNames` membership
                -- check confirms the Sky-emitted Go alias actually
                -- exists; empty-home callers still fall through to
                -- runtimeTyped (their cross-module recovery via
                -- `globalUnionNames` is separate).
                | not (null modStr) && Set.member base knownUnions -> base
                | otherwise -> case runtimeTyped of
                    Just goTy -> goTy
                    Nothing
                        | isRuntimeOnly -> "any"
                        | isKnownUnion  -> base
                        | otherwise     -> "any"
    -- TAlias emitted by the canonicaliser's alias-expansion pass.
    -- Resolve using the same record-alias / runtime-type lookup as
    -- TType so `Profile` → `Main_Profile_R` instead of degenerating
    -- to `any` via the inner TRecord. Fall through to inner only
    -- when the alias name isn't registered anywhere.
    T.TAlias home name _ aliasType ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            env = getCgEnv
            allAliases = Rec._cg_recordAliases env
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList allAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c allAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                Just goTy -> Just goTy
                Nothing   -> lookup name runtimeTypedMap
            innerType = case aliasType of
                T.Filled  inner -> inner
                T.Hoisted inner -> inner
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case runtimeTyped of
                Just goTy -> goTy
                Nothing
                    | isRuntimeOnly -> "any"
                    -- Primitives / containers live inside the alias body
                    -- (e.g. `type alias Id = String`). Inline them.
                    | otherwise     -> case innerType of
                        T.TType _ _ _ -> safeReturnType innerType
                        T.TRecord{}   -> if null base then "any" else base
                        _             -> safeReturnType innerType
    -- Bare TRecord with known fields: match against the codegen env's
    -- record alias registry (field-set → alias name) and emit `_R`.
    -- HM often collapses an alias reference down to its underlying
    -- record (especially after row-polymorphic unification), and
    -- without this path the type would degrade to `any`.
    T.TRecord fields _ ->
        let fieldNames = Map.keys fields
            env = getCgEnv
        in case Rec.lookupRecordAlias (Rec._cg_fieldIndex env) fieldNames of
            Just aliasName -> aliasName ++ "_R"
            Nothing -> "any"
    -- Function types stay `any` rather than emitting
    -- `func(arg) ret`. Go doesn't allow assigning `func(X) Y` to
    -- `func(X) any` (no covariance), so even when the HM-inferred
    -- type is concrete the call site would pass a function with
    -- a different (more specific) return type and fail to compile.
    -- Revisit when Sky has proper Go-generic function types.
    _ -> "any"


-- | Types from Sky runtime that don't have Go type definitions.
-- These map to `any` in Go because they're internal abstractions.
runtimeOnlyTypes :: [String]
runtimeOnlyTypes =
    [ "Decoder", "Value", "Attribute", "Handler"
    , "Route", "Middleware", "Session", "Store"
    ]


-- | Known runtime types that have concrete Go type definitions.
-- These map to their Go type name (with rt. prefix).
-- | Parameterised opaque types that collapse to a Go alias irrespective
-- of their type arguments. `Decoder String`, `Decoder Int`, etc. all
-- emit as `rt.SkyDecoder` because under the hood the runtime uses a
-- single `type SkyDecoder = any`.
opaqueParameterisedGoTy :: String -> Maybe String
opaqueParameterisedGoTy "Decoder" = Just "rt.SkyDecoder"
opaqueParameterisedGoTy "Value"   = Just "rt.SkyValue"
opaqueParameterisedGoTy _         = Nothing


-- | Module-qualified overrides that win over the bare-name mapping.
-- Needed when the same short type name lives in two stdlib modules
-- with distinct Go representations — e.g. `Sky.Core.Http.Response`
-- (HTTP client response struct) vs `Sky.Http.Server.Response`
-- (server response struct). Without this, the bare-name lookup
-- below wrongly collapses them onto the same Go type and user code
-- panics with `interface conversion: interface {} is rt.HttpResponse,
-- not rt.SkyResponse` (or vice versa).
--
-- We list both the full module path (e.g. "Sky.Core.Http") and the
-- common import alias (e.g. "Http") because the canonicaliser's
-- resolveTypeQual preserves the user-written qualifier for non-
-- builtin modules — so `Http.Response` lands in the solved type
-- with home = "Http", not "Sky.Core.Http".
qualifiedRuntimeTypedMap :: [((String, String), String)]
qualifiedRuntimeTypedMap =
    [ (("Sky.Core.Http",   "Response"), "rt.HttpResponse")
    , (("Http",            "Response"), "rt.HttpResponse")
    , (("Sky.Http.Server", "Response"), "rt.SkyResponse")
    , (("Server",          "Response"), "rt.SkyResponse")
    ]


runtimeTypedMap :: [(String, String)]
runtimeTypedMap =
    [ ("VNode",      "rt.VNode")
    , ("Request",    "rt.SkyRequest")
    , ("Response",   "rt.SkyResponse")
    , ("Cmd",        "rt.SkyCmd")
    , ("Sub",        "rt.SkySub")
    -- Opaque Sky types that are effectively `any` under the hood,
    -- but have a dedicated Go alias so the emitted signature names
    -- the abstraction instead of leaking `any`. Each alias is
    -- declared as `type SkyX = any` in runtime-go/rt so there's
    -- no boxing/unboxing overhead and legacy any-typed values
    -- assign/compare transparently. Route is deliberately NOT here:
    -- there's already an exported SkyRoute STRUCT used by the router,
    -- and the Sky-side Route value is an unexported liveRoute struct,
    -- so mapping to SkyRoute would be a lie.
    , ("Decoder",    "rt.SkyDecoder")
    , ("Value",      "rt.SkyValue")
    , ("Attribute",  "rt.SkyAttribute")
    , ("Handler",    "rt.SkyHandler")
    , ("Middleware", "rt.SkyMiddleware")
    , ("Session",    "rt.SkySession")
    , ("Store",      "rt.SkyStore")
    -- Sky.Core.Error.Error is the canonical Sky error type.
    -- Call-site coercion (ResultCoerce / TaskCoerceT) may see
    -- the type with home stripped — emit the qualified Go alias.
    -- The alias declaration `type Sky_Core_Error_Error = rt.SkyADT`
    -- is auto-emitted as part of the Sky.Core.Error dep compilation
    -- (always reachable via the Std.Error transitive import in the
    -- v0.10.0 consolidation).
    , ("Error",      "Sky_Core_Error_Error")
    -- v0.13 A2 follow-up: kernel `Http.get`/`Http.post` declare
    -- their return type with empty `home` and name `HttpResponse`.
    -- Once A2's pre-registration connects forward refs (e.g. an
    -- unannotated `checkResponseStatus resp` param), the renderer
    -- sees `T.TType "" "HttpResponse" []` and otherwise emits
    -- bare `HttpResponse` (undefined Go). Maps to the existing
    -- runtime struct.
    , ("HttpResponse", "rt.HttpResponse")
    -- Db is stored as a pointer at runtime — Db_connect/Db_open
    -- return `&SkyDb{…}`. Typing as `*rt.SkyDb` matches the
    -- `Ok[any,any](db)` branch so the ResultCoerce type assertion
    -- on the OkValue succeeds.
    , ("Db",         "*rt.SkyDb")
    , ("Stmt",       "rt.SkyStmt")
    , ("Row",        "rt.SkyRow")
    , ("Conn",       "rt.SkyConn")
    ]


-- | Walk a canonical module's top-level declarations, collecting
-- per-function (paramTypes, returnType) for every TypedDef whose
-- annotation is concrete and safely expressible. The qualified-name
-- prefix lets dep-module callers reference functions as
-- "Lib_Db_exec" while entry-module callers see "exec".
--
-- Returns (paramTypes :: Map name [paramType], retType :: Map name retType).
-- Functions without annotations are absent; callers treat absence as
-- "fall back to `any`".
collectFuncTypes :: String -> Can.Module
                 -> (Map.Map String [String], Map.Map String String, Map.Map String String)
collectFuncTypes prefix canMod =
    collectFuncTypesWith Set.empty prefix canMod

-- | Same as collectFuncTypes but takes an extra set of record-alias
-- names so safeReturnTypePure can promote them to `_R` Go names. The
-- set should contain BOTH bare alias names and module-prefixed ones
-- so cross-module record refs resolve too.
collectFuncTypesWith :: Set.Set String -> String -> Can.Module
                     -> (Map.Map String [String], Map.Map String String, Map.Map String String)
collectFuncTypesWith extraRecAliases prefix canMod =
    let localRecAliases = Rec.collectRecordAliases (Can._aliases canMod)
        prefixed = if null prefix
                     then localRecAliases
                     else Set.map (\n -> prefix ++ "_" ++ n) localRecAliases
        knownRecAliases = Set.unions [extraRecAliases, localRecAliases, prefixed]
        -- v0.13 Layer 3 fix: align with goSafeName-mangled dep
        -- emission.  Cross-module call sites look up funcParamTypes
        -- by the mangled name (e.g. `Sky_Core_Result_map_` when
        -- the Sky source declares `map`), so the table keys must
        -- match.  Entry module (null prefix) keeps raw name —
        -- local code lookups use goSafeName at the call site.
        qualName n = if null prefix
                       then goSafeName n
                       else prefix ++ "_" ++ goSafeName n
        goDecls Can.SaveTheEnvironment = []
        goDecls (Can.Declare d rest)        = d : goDecls rest
        goDecls (Can.DeclareRec d ds rest)  = d : ds ++ goDecls rest
        extract def = case def of
            Can.TypedDef (A.At _ n) _ typedPats _ retType ->
                let argTypes = map snd typedPats
                    argGoTys = map (safeReturnTypeWith knownRecAliases) argTypes
                    retGoTy  = safeReturnTypeWith knownRecAliases retType
                    -- v0.13 Stage 2 — ultimate return type strips ALL
                    -- nested TLambda levels. Used by typed partial-
                    -- application wrapper codegen which needs the
                    -- scalar return after every arg applies.
                    ultRetGoTy = safeReturnTypeWith knownRecAliases
                                    (ultimateReturnType retType)
                    hasAnyTyped = retGoTy /= "any" || any (/= "any") argGoTys
                in if hasAnyTyped
                     then Just (qualName n, argGoTys, retGoTy, ultRetGoTy)
                     else Nothing
            _ -> Nothing
        bindings = goDecls (Can._decls canMod)
        results = mapMaybe extract bindings
        -- Auto-generated record constructors. `type alias Item = { id :
        -- Int, name : String, tags : List String }` synthesises an
        -- `Item : Int -> String -> List String -> Item_R` constructor at
        -- elaboration time.  The ULTIMATE return of `Item` is the same
        -- as its declared return (the record alias) since it's not a
        -- function-returning function.
        ctorResults =
            [ (qualName aliasName, paramTys, retTy, retTy)
            | (aliasName, alias) <- Map.toList (Can._aliases canMod)
            , Rec.DataRecord fieldList <- [Rec.classifyAlias alias]
            , let paramTys = map (safeReturnTypeWith knownRecAliases . snd) fieldList
            , let retTy = qualName aliasName ++ "_R"
            ]
        -- v0.13 Stage 1 — ADT constructors (e.g. `State_Msg_InputName`)
        -- need their typed Go param/return registered so `goExprGoType`
        -- at HOF call sites can recover the ctor's typed sig. Without
        -- this, `onInput State_Msg_InputName` (where the kernel sig is
        -- `(String -> msg) -> Attribute msg`) fails σ-recovery for the
        -- TVar `msg`, the substituted param type erases to
        -- `func(string) any`, and a `rt.Coerce[func(string) any]`
        -- wrap is forced even though the ctor's actual sig is
        -- `func(string) State_Msg`. Closes the dominant adapter class
        -- across the sweep (~45 of 50 are this exact shape).
        --
        -- Only registers arity > 0 ctors; zero-arity ones are emitted
        -- as `var` decls, not functions.
        adtCtorResults =
            [ ( qualName (typeName ++ "_" ++ cname)
              , map (safeReturnTypeWith knownRecAliases) argTys
              , qualName typeName
              , qualName typeName )
            | (typeName, Can.Union _vars ctors _numAlts opts)
                <- Map.toList (Can._unions canMod)
            , case opts of { Can.Enum -> False; _ -> True }
            , Can.Ctor cname _idx arity argTys <- ctors
            , arity > 0
            , length argTys == arity
            ]
        allResults = results ++ ctorResults ++ adtCtorResults
        paramMap = Map.fromList [ (qual, ps) | (qual, ps, _, _) <- allResults ]
        retMap   = Map.fromList [ (qual, r)  | (qual, _, r, _) <- allResults ]
        ultMap   = Map.fromList [ (qual, u)  | (qual, _, _, u) <- allResults ]
    in (paramMap, retMap, ultMap)


-- | v0.13 Stage 2 — strip every TLambda level to get the ultimate
-- scalar return type of a (possibly multi-arg) function.
--
--   ultimateReturnType (A -> B -> C -> D)  =  D
--   ultimateReturnType (A -> B)            =  B
--   ultimateReturnType nonFunc             =  nonFunc
ultimateReturnType :: T.Type -> T.Type
ultimateReturnType (T.TLambda _ rest) = ultimateReturnType rest
ultimateReturnType t                  = t


-- | safeReturnType variant that takes an explicit record-alias set
-- instead of consulting the global env. Used by collectFuncTypes
-- during env bootstrap.
safeReturnTypeWith :: Set.Set String -> T.Type -> String
safeReturnTypeWith recAliases = go
  where
    -- Extract module prefixes that appear in the alias set (everything
    -- before the last "_"). Lets us find "State_Model_R" from a TType
    -- whose home is "" or "Main".
    aliasModulePrefixes =
        Set.fromList
            [ reverse (drop 1 (dropWhile (/= '_') (reverse a)))
            | a <- Set.toList recAliases
            , '_' `elem` a
            ]

    go t = case t of
        T.TUnit                       -> "struct{}"
        T.TType _ "Int" []            -> "int"
        T.TType _ "Float" []          -> "float64"
        T.TType _ "Bool" []           -> "bool"
        T.TType _ "String" []         -> "string"
        T.TType _ "Char" []           -> "rune"
        T.TType _ "Bytes" []          -> "[]byte"
        T.TType _ "Result" [e, a]     -> "rt.SkyResult[" ++ go e
                                         ++ ", " ++ go a ++ "]"
        T.TType _ "Maybe"  [x]        -> "rt.SkyMaybe[" ++ go x ++ "]"
        T.TType _ "Task"   [e, a]     -> "rt.SkyTask[" ++ go e
                                         ++ ", " ++ go a ++ "]"
        T.TType _ "Cmd"    _          -> "rt.SkyCmd"
        T.TType _ "Sub"    _          -> "rt.SkySub"
        T.TType _ "List"   [elem]     ->
            let inner = go elem
            in if inner == "any" then "[]any" else "[]" ++ inner
        T.TType _ "List"   _          -> "[]any"
        T.TType _ "Dict"   [_, v]     ->
            let inner = go v
            in if inner == "any" then "map[string]any" else "map[string]" ++ inner
        T.TType _ "Dict"   _          -> "map[string]any"
        T.TType _ "Set"    _          -> "map[any]bool"
        T.TTuple _ _ []               -> "rt.SkyTuple2"
        T.TTuple _ _ [_]              -> "rt.SkyTuple3"
        T.TTuple _ _ _                -> "rt.SkyTupleN"
        T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
        -- v0.13 Layer 3: `_` (not `[]`) so a PARAMETERISED Sky ADT
        -- (`Html msg`, `Attribute msg`, `Element msg`) renders to its
        -- erased Go struct name — the ADT emits as a non-generic
        -- `type Mod_Name = rt.SkyADT`, so the `msg` arg is irrelevant
        -- to the Go type name.  Pre-fix these fell through to `any`,
        -- giving 177/183 `Std_Html_*` functions `any`-typed sigs.
        T.TType home name _ ->
            let modStr = ModuleName.toString home
                prefix = if null modStr || modStr == "Main"
                           then ""
                           else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
                base = prefix ++ name
                -- Prefer prefixed forms over bare name. When home is
                -- "" / "Main" we still try every known module prefix
                -- so a record alias defined in another module still
                -- resolves correctly.
                qualifiedCandidates =
                    [ p ++ "_" ++ name | p <- Set.toList aliasModulePrefixes ]
                candidates = if null prefix
                               then qualifiedCandidates ++ [name]
                               else base : qualifiedCandidates ++ [name]
                matches = [ c | c <- candidates, Set.member c recAliases ]
                isRuntimeOnly = name `elem` runtimeOnlyTypes
                runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                    Just goTy -> Just goTy
                    Nothing   -> lookup name runtimeTypedMap
            in case matches of
                (m:_) -> m ++ "_R"
                _     -> case runtimeTyped of
                    Just goTy -> goTy
                    Nothing   -> if isRuntimeOnly then "any" else base
        T.TAlias home name _ aliasType ->
            let modStr = ModuleName.toString home
                prefix = if null modStr || modStr == "Main"
                           then ""
                           else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
                base = prefix ++ name
                qualifiedCandidates =
                    [ p ++ "_" ++ name | p <- Set.toList aliasModulePrefixes ]
                candidates = if null prefix
                               then qualifiedCandidates ++ [name]
                               else base : qualifiedCandidates ++ [name]
                matches = [ c | c <- candidates, Set.member c recAliases ]
                isRuntimeOnly = name `elem` runtimeOnlyTypes
                runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                    Just goTy -> Just goTy
                    Nothing   -> lookup name runtimeTypedMap
                inner = case aliasType of
                    T.Filled i  -> i
                    T.Hoisted i -> i
            in case matches of
                (m:_) -> m ++ "_R"
                _     -> case runtimeTyped of
                    Just goTy -> goTy
                    Nothing
                        | isRuntimeOnly -> "any"
                        | otherwise     -> case inner of
                            T.TRecord{} -> if null base then "any" else base
                            _           -> go inner
        -- Function-typed slots (HOF params): render typed return shape
        -- to match what `renderHofParamTy` emits at signature time
        -- (v0.13 D1). This drives `_cg_funcParamTypes[fn]`'s entry,
        -- which `coerceCallArgsAt`'s fallback then uses to detect
        -- the func-typed slot and route literal `Can.Lambda` args
        -- through `curryLambdaPatTyped`. Bare-TVar returns stay typed
        -- (Go call-site inference); concrete returns now render
        -- through `go` instead of widening to `any`.
        T.TLambda _ _ -> renderFuncTy t
        _ -> "any"
      where
        -- Curried multi-arg → nested `func(A) func(B) ...`.
        renderFuncTy (T.TLambda from to@T.TLambda{}) =
            "func(" ++ go from ++ ") " ++ renderFuncTy to
        renderFuncTy (T.TLambda from to@(T.TVar _)) =
            "func(" ++ go from ++ ") " ++ go to
        renderFuncTy (T.TLambda from to) =
            "func(" ++ go from ++ ") " ++ go to
        renderFuncTy other = go other
        renderFuncTy other = go other


-- | Index module decls by binding name so we can check annotations
-- in O(log n) (needed by the HM-dep merge to exclude TypedDefs).
declsByName :: Can.Module -> Map.Map String Can.Def
declsByName canMod = go (Can._decls canMod) Map.empty
  where
    go Can.SaveTheEnvironment acc = acc
    go (Can.Declare d rest) acc = go rest (insertDef d acc)
    go (Can.DeclareRec d ds rest) acc =
        go rest (foldr insertDef (insertDef d acc) ds)
    insertDef d acc = case d of
        Can.Def (A.At _ n) _ _ -> Map.insert n d acc
        Can.TypedDef (A.At _ n) _ _ _ _ -> Map.insert n d acc
        Can.DestructDef _ _ -> acc


-- | Count how many params a dep-module binding has. Used when we
-- need to split a solver-inferred function type (which chains
-- TLambdas) into the right number of arg types.
countParamsFor :: String -> Can.Module -> Int
countParamsFor name canMod = go (Can._decls canMod)
  where
    go Can.SaveTheEnvironment = 0
    go (Can.Declare d rest) = maybe (go rest) id (matchDef d)
    go (Can.DeclareRec d ds rest) =
        firstMatchInt (d : ds) (go rest)
    matchDef d = case d of
        Can.Def (A.At _ n) pats _
            | n == name -> Just (length pats)
            | otherwise -> Nothing
        Can.TypedDef (A.At _ n) _ pats _ _
            | n == name -> Just (length pats)
            | otherwise -> Nothing
        _ -> Nothing
    firstMatchInt []     fallback = fallback
    firstMatchInt (d:ds) fallback = case matchDef d of
        Just n  -> n
        Nothing -> firstMatchInt ds fallback

-- | v0.13 Stage 1 — pick the BETTER (more-informative) of two
-- candidate param-type lists for the same callee. "Better" means:
--
--   * concrete types beat bare TVars (`int` > `T1`)
--   * compound types containing TVars beat bare TVars
--     (`func(string) T1` > `T1`, `[]T1` > `T1`)
--   * concrete-anywhere beats fully-erased (`func(string) T1` >
--     `func(string) any` when the surrounding type still mentions
--     the TVar — preserves σ-recovery)
--
-- When neither is strictly better, prefer the LEFT (which is the
-- HM-inferred entry per the caller's union order).
--
-- Operates entry-by-entry: the result has the same arity as the
-- longer of the two; positions where one side is empty/missing get
-- the other side's value.
betterParamTypes :: [String] -> [String] -> [String]
betterParamTypes l r
    | null l            = r
    | null r            = l
    | length l /= length r =
        if length l > length r then l else r
    | otherwise         =
        zipWith betterTypeStr l r

-- | v0.13 Stage 1 — same picker for a single return-type string.
betterRetType :: String -> String -> String
betterRetType = betterTypeStr

-- | v0.13 Stage 1 — per-string picker. Prefers the type carrying
-- more concrete information.  See `betterParamTypes` for the
-- ordering rationale.
betterTypeStr :: String -> String -> String
betterTypeStr l r
    -- A bare TVar (`T1`) carries the least info — beaten by anything else.
    | isGenericTypeParam l && not (isGenericTypeParam r) = r
    | isGenericTypeParam r && not (isGenericTypeParam l) = l
    -- Both bare TVars (or both not): prefer the one with NO `any`
    -- (typed-everywhere) over the one that has `any`.
    | hasAnyToken l && not (hasAnyToken r) = r
    | hasAnyToken r && not (hasAnyToken l) = l
    -- Otherwise tie: keep the left (HM-inferred).
    | otherwise = l
  where
    hasAnyToken s = "any" `elem` tokenise s
    -- Conservative tokeniser: alphanumeric + underscore runs.
    tokenise [] = []
    tokenise (c:cs)
        | isGoIdentStart c =
            let (w, rest) = span isGoIdentChar (c:cs)
            in w : tokenise rest
        | otherwise = tokenise cs

-- | v0.13 Stage 1 — reconstruct the full annotation type for an
-- annotated TypedDef in a module by folding param types into a
-- TLambda chain with the return type at the tail. Used by the dep-
-- sig pipeline to recover sigs that HM's solved type might have
-- stripped to just the return type.
annotationTypeFor :: String -> Can.Module -> Maybe T.Type
annotationTypeFor name canMod = go (Can._decls canMod)
  where
    go Can.SaveTheEnvironment = Nothing
    go (Can.Declare d rest) = case matchDef d of
        Just t -> Just t
        Nothing -> go rest
    go (Can.DeclareRec d ds rest) = case matchDef d of
        Just t -> Just t
        Nothing -> firstMatch (d : ds) (go rest)
    matchDef d = case d of
        Can.TypedDef (A.At _ n) _ typedPats _ retType
            | n == name ->
                Just (foldr T.TLambda retType (map snd typedPats))
        _ -> Nothing
    firstMatch [] fallback = fallback
    firstMatch (d:ds) fallback = case matchDef d of
        Just t  -> Just t
        Nothing -> firstMatch ds fallback
    firstMatching [] fallback = fallback
    firstMatching (d:ds) fallback = case matchDef d of
        Just k  -> k
        Nothing -> firstMatching ds fallback


-- | Split a function's inferred type into Go type parameters, param
-- types, and return type. TVars in the inferred type become Go type
-- parameters (`T1, T2 any`) so partially-inferred functions like
-- `getField : String -> TVar -> String` get typed as
-- `func GetField[T1 any](p0 string, p1 T1) string` instead of all-any.
splitInferredSig :: Int -> T.Type -> ([String], [String], String)
splitInferredSig = splitInferredSigWith Set.empty

-- | Variant of splitInferredSig that takes a record alias set
-- for resolving cross-module record aliases and ADT types.
splitInferredSigWith :: Set.Set String -> Int -> T.Type -> ([String], [String], String)
splitInferredSigWith recAliases = splitInferredSigWithReg recAliases Map.empty

-- | v0.13 Phase A5+: extract the Sky-TVar → Go-TVar mapping for a
-- polymorphic function's emitted signature.  Mirrors
-- `splitInferredSigWithReg`'s internal `keptNumbered` computation
-- so the call-site coercion path (`coerceCallArgsAt`) can map
-- annotation Forall names (carried by `CallInstance.quantifiers`)
-- to the Go-generic names that actually appear in the emitted
-- signature.
inferredSigSkyToGo
    :: Set.Set String
    -> Rec.RecordRegistry
    -> Int
    -> T.Type
    -> [(String, String)]
inferredSigSkyToGo recAliases fieldIdx arity funcType =
    let defaulted =
            if errorTypeAvailable recAliases
                then defaultErrorTVars funcType
                else defaultOpaqueTVars funcType
        (paramTys, retTy) = collectParamsLocal arity defaulted
        paramTVars = uniqLocal (concatMap tvarsInEmitted paramTys)
        numbered = zip paramTVars ["T" ++ show i | i <- [1::Int ..]]
        paramStrsRaw = map (renderHofParamTy recAliases fieldIdx numbered) paramTys
        retStrRaw = typeStrWithAliasesReg recAliases fieldIdx numbered retTy
        renderedSig = unwords (retStrRaw : paramStrsRaw)
    in [ (skyName, goName)
       | (skyName, goName) <- numbered
       , goAppearsAsToken goName renderedSig
       ]
  where
    collectParamsLocal 0 ty = ([], ty)
    collectParamsLocal n (T.TLambda from to) =
        let (rest, r) = collectParamsLocal (n - 1) to
        in (from : rest, r)
    collectParamsLocal _ ty = ([], ty)

    uniqLocal [] = []
    uniqLocal (x:xs) = x : uniqLocal (filter (/= x) xs)


-- | Richer variant that also carries a field-set → alias-name
-- registry so anonymous record types (TRecord) can resolve to their
-- `_R` Go struct name in emitted signatures. Without this, HM-inferred
-- record returns degraded to `any` — the body would still construct a
-- `Foo_R{...}` literal, but the signature wouldn't match.
splitInferredSigWithReg
    :: Set.Set String
    -> Rec.RecordRegistry
    -> Int
    -> T.Type
    -> ([String], [String], String)
splitInferredSigWithReg recAliases fieldIdx arity funcType =
    let -- Default TVars that appear ONLY in error positions (Result's
        -- first arg, Task's first arg) to `Sky.Core.Error.Error` when
        -- the Error type is reachable in the current module's dep
        -- graph, else to the opaque `rt.SkyValue` alias so the sig
        -- still carries a nominal name (examples that import
        -- Sky.Core.Task but not Error still link, because SkyValue
        -- is always in the rt package).
        defaulted =
            if errorTypeAvailable recAliases
                then defaultErrorTVars funcType
                else defaultOpaqueTVars funcType
        (paramTys, retTy) = collectParams arity defaulted
        -- TVars in the params get named T1, T2, …. Return-only
        -- TVars intentionally stay un-named (rendered as `any`)
        -- because Go's generic type inference only works from
        -- argument positions — naming them as T_N would force
        -- callers to instantiate explicitly (`foo[int](x)`)
        -- which neither Sky nor the FFI emits.
        paramTVars = uniq (concatMap tvarsInEmitted paramTys)
        numbered = zip paramTVars ["T" ++ show i | i <- [1::Int ..]]
        paramStrsRaw = map (renderHofParamTy recAliases fieldIdx numbered) paramTys
        retStrRaw = typeStrWithAliasesReg recAliases fieldIdx numbered retTy
        -- A TVar that `tvarsInEmitted` flagged but never actually
        -- appears in the rendered Go param/return strings produces
        -- a phantom `[T1 any]` declaration that Go can't infer at
        -- the call site (`cannot infer T1`). This happens for Sky-
        -- defined ADTs whose emitted form is `Mod_Adt` regardless
        -- of their type params (e.g. `Element msg` emits as
        -- `Std_Ui_Element`). Keep only the TVars that survive
        -- rendering.
        --
        -- v0.13 C follow-up: filter against PARAM strings only,
        -- not return. Go's generic inference works from input
        -- positions; a TVar appearing only in the return type
        -- (e.g. `b` in `concatMap : (a -> List b) -> List a ->
        -- List b`, after C propagates the `b` inside `List b`)
        -- isn't inferable from any call-site arg — Go rejects
        -- with "cannot infer T2". Dropping the return-only TVar
        -- collapses the return slot to `[]any`; the caller's
        -- existing `rt.AsListT[T]` boundary coercion already
        -- bridges that. Once D1 (typed lambda output) lands,
        -- fn's return position will surface the TVar in the
        -- HOF-param sig and the input-only filter will keep it.
        paramSig = unwords paramStrsRaw
        usedTypeParams =
            [ goName
            | (_, goName) <- numbered
            , goName `appearsAsToken` paramSig
            ]
        -- Re-render with only the surviving TVars in the numbered
        -- map so unused-TVar slots fall back to `any` instead of a
        -- phantom T_n that confuses Go's inference.
        keptNumbered =
            [ (skyName, goName)
            | (skyName, goName) <- numbered
            , goName `elem` usedTypeParams
            ]
        paramStrs = map (renderHofParamTy recAliases fieldIdx keptNumbered) paramTys
        retStr = typeStrWithAliasesReg recAliases fieldIdx keptNumbered retTy
    in (usedTypeParams, paramStrs, retStr)
  where
    collectParams 0 ty = ([], ty)
    collectParams n (T.TLambda from to) =
        let (rest, r) = collectParams (n - 1) to
        in (from : rest, r)
    collectParams _ ty = ([], ty)

    uniq [] = []
    uniq (x:xs) = x : uniq (filter (/= x) xs)

    -- A TVar token like "T1" must appear as a whole-word match (not
    -- as a substring of, say, "T11" or "Sky_T1_helper"), so we check
    -- for non-identifier characters on both sides.
    appearsAsToken t s = goAppearsAsToken t s


-- | Whole-token match: returns True iff `tok` appears in `s` not
-- as a substring of a longer identifier. Used by
-- `splitInferredSigWithReg` to decide whether a numbered type
-- parameter (e.g. "T1") is actually referenced in the rendered Go
-- signature — phantom params must be dropped because Go's generic
-- inference can't pin them at the call site.
--
-- Implementation: walk position-by-position, checking that the char
-- immediately before the match (or start-of-string) and the char
-- immediately after the match (or end-of-string) are both non-
-- identifier characters. Catches overlap like "T1" inside "T11" and
-- "Sky_T1_helper".
goAppearsAsToken :: String -> String -> Bool
goAppearsAsToken tok s = go 0 s
  where
    n = length tok
    sLen = length s
    go _ [] = False
    go i input
        | i + n > sLen = False
        | take n input == tok
            && (i == 0 || not (isIdChar (s !! (i - 1))))
            && (i + n == sLen || not (isIdChar (s !! (i + n))))
            = True
        | otherwise = case input of
            []      -> False
            (_:rest) -> go (i + 1) rest

    isIdChar ch = ch == '_'
                || (ch >= 'a' && ch <= 'z')
                || (ch >= 'A' && ch <= 'Z')
                || (ch >= '0' && ch <= '9')


-- | Count how many times each TVar name appears in a type, classified
-- by slot: error slot of a Result/Task, top of the ok slot of a
-- Result/Task (i.e. the whole ok arg IS the TVar, not nested), or
-- anywhere else. Returns `(errorCount, okCount, otherCount)` per TVar.
tvarOccurrences :: T.Type -> Map.Map String (Int, Int, Int)
tvarOccurrences = go Other
  where
    bumpErr n   = Map.singleton n (1, 0, 0)
    bumpOk n    = Map.singleton n (0, 1, 0)
    bumpOther n = Map.singleton n (0, 0, 1)
    addP (a1, b1, c1) (a2, b2, c2) = (a1 + a2, b1 + b2, c1 + c2)
    -- The earlier in-fn-arg distinction (don't default `b` when it
    -- appears inside a function-typed parameter's body) was REVERTED
    -- because it paired only with the disabled rtKernelTypedVariant
    -- routing path. Without that routing, un-defaulting `b` emitted
    -- a typed slot that the runtime's `func(any) any` kernel fn
    -- couldn't fill — Go rejected the call. Reverted to the
    -- conservative defaulting; the `rt.Coerce[func(...) any]` wrap
    -- at the call site bridges shapes.
    go slot ty = case ty of
        T.TVar n -> case slot of
            ErrorSlot -> bumpErr n
            OkSlot    -> bumpOk n
            Other     -> bumpOther n
        T.TLambda a b -> Map.unionWith addP (go Other a) (go Other b)
        T.TType _ "Result" [e, a] ->
            Map.unionWith addP (go ErrorSlot e) (go OkSlot a)
        T.TType _ "Task"   [e, a] ->
            Map.unionWith addP (go ErrorSlot e) (go OkSlot a)
        T.TType _ "Maybe" [a] -> go OkSlot a
        T.TType _ _ args -> Map.unionsWith addP (map (go Other) args)
        T.TTuple a b cs -> Map.unionsWith addP (map (go Other) (a : b : cs))
        T.TAlias _ _ pairs aliasType ->
            Map.unionsWith addP $
                [go Other v | (_, v) <- pairs]
                ++ [case aliasType of
                        T.Filled i  -> go Other i
                        T.Hoisted i -> go Other i]
        T.TRecord fields _ ->
            Map.unionsWith addP
                [go Other fTy | T.FieldType _ fTy <- Map.elems fields]
        T.TUnit -> Map.empty


data Slot = ErrorSlot | OkSlot | Other


-- | True when Sky.Core.Error is reachable via the current module's
-- dep graph (proxy: its `ErrorInfo` record alias appears in the
-- record-alias registry). Without this guard, defaulting would emit
-- `Sky_Core_Error_Error` references in examples that don't import
-- Error, breaking `go build`.
errorTypeAvailable :: Set.Set String -> Bool
errorTypeAvailable recAliases =
    Set.member "Sky_Core_Error_ErrorInfo" recAliases
    || Set.member "ErrorInfo" recAliases


-- | Default TVars that appear only in Result/Task error or ok
-- positions to concrete types:
-- - error-only → `Sky.Core.Error.Error`
-- - ok-only   → `rt.SkyValue` (opaque runtime-any alias, matches
--               the body's untyped Ok-branch value)
-- - return-only (bare TVar in return position only, never in param)
--   → `rt.SkyValue` for the same reason — the caller can't observe
--   a specific concrete type for a TVar that never appears in the
--   params.
-- See splitInferredSigWithReg for why this is safe.
defaultErrorTVars :: T.Type -> T.Type
defaultErrorTVars ty =
    let counts = tvarOccurrences ty
        errorOnly =
            [ n | (n, (e, o, x)) <- Map.toList counts, e > 0, o == 0, x == 0 ]
        okOnly =
            [ n | (n, (e, o, x)) <- Map.toList counts, o > 0, e == 0, x == 0 ]
        -- Return-only TVars: appear only in the "other" bucket (meaning
        -- not in Result/Task/Maybe slots) but the param-position TVars
        -- won't get this default because they'd be renamed to T1 etc.
        -- by splitInferredSigWithReg (they're in `paramTVars`).
        -- Here we catch TVars whose ONLY occurrence is in the return
        -- type's non-Result/Task/Maybe position — they're
        -- passthrough opaque values like `intVal : Int -> a`.
        returnOnly = returnOnlyTVars ty
        okTy    = T.TType (ModuleName.Canonical "") "Value" []
        errorTy = T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
        substMap = Map.fromList $
            [(n, errorTy) | n <- errorOnly]
            ++ [(n, okTy) | n <- okOnly ++ returnOnly]
    in substTVarsToTypes substMap ty


-- | Walk a function type; find TVars that appear ONLY in the final
-- return position (bare, no nested Result/Task/Maybe involvement).
-- Used to default passthrough `T -> a` annotations to `T -> Value`.
returnOnlyTVars :: T.Type -> [String]
returnOnlyTVars ty =
    let (paramTys, retTy) = peel ty
        paramTVars = Set.fromList (concatMap collectAllTVars paramTys)
        retTVars   = collectAllTVars retTy
        -- Only bare TVars in the return position (not wrapped in
        -- Result/Task/Maybe — those go through ok-slot defaulting).
        retBare = case retTy of
            T.TVar n -> [n]
            _        -> []
    in [ n | n <- retBare, n `notElem` retTVars_minus_self retTVars n
           , not (n `Set.member` paramTVars) ]
  where
    peel (T.TLambda a b) = let (ps, r) = peel b in (a : ps, r)
    peel t = ([], t)
    collectAllTVars t = case t of
        T.TVar n -> [n]
        T.TLambda a b -> collectAllTVars a ++ collectAllTVars b
        T.TType _ _ args -> concatMap collectAllTVars args
        T.TTuple a b cs -> concatMap collectAllTVars (a : b : cs)
        T.TAlias _ _ _ (T.Filled inner)  -> collectAllTVars inner
        T.TAlias _ _ _ (T.Hoisted inner) -> collectAllTVars inner
        T.TRecord fields _ ->
            concatMap (\(T.FieldType _ fTy) -> collectAllTVars fTy)
                      (Map.elems fields)
        T.TUnit -> []
    retTVars_minus_self retTVars n = filter (/= n) retTVars


-- | Variant of `defaultErrorTVars` that defaults BOTH error-only and
-- ok-only TVars to the opaque `rt.SkyValue` alias. Used when the
-- current module's dep graph doesn't include Sky.Core.Error so the
-- Error-typed default can't be emitted without a dangling type
-- reference.
defaultOpaqueTVars :: T.Type -> T.Type
defaultOpaqueTVars ty =
    let counts = tvarOccurrences ty
        okTy = T.TType (ModuleName.Canonical "") "Value" []
        candidates =
            [ n
            | (n, (e, o, x)) <- Map.toList counts
            , x == 0
            , e + o > 0
            ]
        substMap = Map.fromList [(n, okTy) | n <- candidates]
    in substTVarsToTypes substMap ty


-- | TVar-to-Type substitution (more general than substTVars which
-- only renames).
substTVarsToTypes :: Map.Map String T.Type -> T.Type -> T.Type
substTVarsToTypes subst = go
  where
    go t = case t of
        T.TVar n -> Map.findWithDefault t n subst
        T.TLambda a b -> T.TLambda (go a) (go b)
        T.TType home n args -> T.TType home n (map go args)
        T.TTuple a b cs -> T.TTuple (go a) (go b) (map go cs)
        T.TRecord fields mExt ->
            T.TRecord
                (Map.map (\(T.FieldType i fTy) -> T.FieldType i (go fTy)) fields)
                mExt
        T.TAlias home n pairs aliasType ->
            T.TAlias home n [(k, go v) | (k, v) <- pairs]
                (case aliasType of
                    T.Filled i -> T.Filled (go i)
                    T.Hoisted i -> T.Hoisted (go i))
        T.TUnit -> T.TUnit


-- | Extract Go param types (legacy API, kept for annotation path).
splitInferredParams :: Int -> T.Type -> [String]
splitInferredParams n t =
    let (_, ps, _) = splitInferredSig n t in ps


-- | Extract Go return type (legacy API, kept for annotation path).
inferredReturnFor :: Int -> T.Type -> String
inferredReturnFor n t =
    let (_, _, r) = splitInferredSig n t in r


-- | All distinct TVar names appearing inside a Type, in left-to-right
-- encounter order.
tvarsIn :: T.Type -> [String]
tvarsIn t = case t of
    T.TVar name
        -- Skip the solver's internal "_cargNNN" / binding-name TVars
        -- that never appear on the user-facing surface — they'd just
        -- clutter the type parameter list.
        | take 1 name == "_" -> [name]
        | length name > 1    -> []
        | otherwise          -> [name]
    T.TLambda a b     -> tvarsIn a ++ tvarsIn b
    T.TType _ _ args  -> concatMap tvarsIn args
    T.TTuple a b cs   -> concatMap tvarsIn (a : b : cs)
    T.TAlias _ _ pairs (T.Filled inner)  -> concatMap tvarsIn (inner : map snd pairs)
    T.TAlias _ _ pairs (T.Hoisted inner) -> concatMap tvarsIn (inner : map snd pairs)
    T.TRecord{}       -> []
    T.TUnit           -> []


-- | Convert a Sky type to Go with a TVar → Go type param substitution.
-- Falls back to safeReturnTypePure for non-TVar nodes.
typeStrWith :: [(String, String)] -> T.Type -> String
typeStrWith = typeStrWithAliases Set.empty

-- | Variant with record alias set for cross-module resolution.
typeStrWithAliases :: Set.Set String -> [(String, String)] -> T.Type -> String
typeStrWithAliases recAliases = typeStrWithAliasesReg recAliases Map.empty


-- | Render a user-HOF parameter type. Params that are themselves
-- function types (callback/continuation params) have their innermost
-- return type rewritten to `any` when the inferred return is a
-- concrete parametric shape (SkyResult, SkyMaybe, SkyTask, a user
-- record, …). Sky lambdas lower to `func(p any) any` regardless of
-- their inferred return, so a specific Go return like
-- `rt.SkyResult[E, rt.SkyValue]` at the param sig makes the call-site
-- lambda un-assignable: Go has no function-type covariance, and even
-- though `rt.SkyValue = any`, the wrapper `SkyResult[E, any]` is a
-- distinct named generic instantiation, not `any`. Keeping input
-- types concrete lets Go generic inference still flow from the first
-- arg to later params. Non-function param types are unaffected.
--
-- Exception: when the innermost return is a bare TVar (e.g.
-- `(Msg -> parentMsg) -> VNode` from component views), it stays as
-- the TVar. Go uses call-site inference — passing a named
-- `func(Msg) Msg` fixes `T1 = Msg` through that position. Rewriting
-- to `any` would leave `T1` unused in the sig, and Go rejects with
-- "cannot infer T1".
--
-- This only affects the SIGNATURE shape of the enclosing HOF; the body
-- routes its function-typed params through the `*AnyT` kernel helpers
-- (which take and return `any`) so dropping the inner return's
-- specificity doesn't change runtime semantics.
renderHofParamTy
    :: Set.Set String
    -> Rec.RecordRegistry
    -> [(String, String)]
    -> T.Type
    -> String
renderHofParamTy recAliases fieldIdx tvarMap ty = case ty of
    T.TLambda _ _ -> renderLambdaInner ty
    _             -> go ty
  where
    go = typeStrWithAliasesReg recAliases fieldIdx tvarMap
    renderLambdaInner (T.TLambda from to@T.TLambda{}) =
        "func(" ++ go from ++ ") " ++ renderLambdaInner to
    renderLambdaInner (T.TLambda from to@(T.TVar _)) =
        -- Bare TVar return: keep typed so Go can infer via call site.
        "func(" ++ go from ++ ") " ++ go to
    renderLambdaInner (T.TLambda from to) =
        -- v0.13 D1: render the typed return shape for HOF param sigs.
        --
        -- Pre-D1 this was a blanket `"any"` to accommodate Sky-side
        -- lambdas lowered as `func(any) any`. The v0.13 D-Lambda-
        -- Lowerer (the `coerceFallback`'s `Can.Lambda` arm in
        -- `coerceCallArgsAt`'s no-CSI branch — see ~line 6229)
        -- extends `curryLambdaPatTyped` routing to user-defined HOF
        -- call sites: a literal `\a -> ...` lambda at a func-typed
        -- param slot now emits `func(A) B` directly so the sig +
        -- lowered shape agree end-to-end.
        --
        -- Non-literal func args (top-level helpers, Msg constructors,
        -- partial applications) already have concrete Go signatures;
        -- coerceArg's `rt.Coerce[func(X) Y]` reflect adapter
        -- (makeFuncAdapter) bridges any residual shape mismatch.
        --
        -- The `test/Sky/Build/CompileSpec.hs:139` regression
        -- ("user-defined polymorphic HOFs with Result-typed lambda
        -- params") was the previous gating test for the blanket-
        -- `any` workaround. D-Lambda-Lowerer makes the typed shape
        -- consistent at user-defined HOFs too, so the regression
        -- stays green.
        "func(" ++ go from ++ ") " ++ go to
    renderLambdaInner other = go other

-- | Like `typeStrWithAliases` but additionally consults a field-set →
-- alias-name registry so bare `T.TRecord` nodes (emitted by HM after
-- row-polymorphic unification) resolve to their `_R` Go struct name
-- instead of degrading to `any`.
typeStrWithAliasesReg
    :: Set.Set String
    -> Rec.RecordRegistry
    -> [(String, String)]
    -> T.Type
    -> String
typeStrWithAliasesReg recAliases fieldIdx tvarMap ty = case ty of
    T.TVar name -> case lookup name tvarMap of
        Just gname -> gname
        Nothing    -> "any"
    T.TLambda from to ->
        "func(" ++ go from ++ ") " ++ go to
    T.TType _ "Result" [e, a] ->
        "rt.SkyResult[" ++ go e ++ ", " ++ go a ++ "]"
    T.TType _ "Maybe" [x] ->
        "rt.SkyMaybe[" ++ go x ++ "]"
    T.TType _ "Task" [e, a] ->
        "rt.SkyTask[" ++ go e ++ ", " ++ go a ++ "]"
    -- List with a known element type: emit `[]T` so sig is specific.
    -- Body-constructed `[]any{...}` coerces via rt.Coerce[[]T] (reflect-
    -- based element walk) and rt.AsListT[T] at call boundaries, both of
    -- which already handle the []any → []T reshape. Element types that
    -- themselves map to `any` (TVars, runtime-only abstractions) fall
    -- back to `[]any` to avoid emitting `[]any` inside `[]any`.
    T.TType _ "List" [elem] ->
        let inner = go elem
        in if inner == "any" then "[]any" else "[]" ++ inner
    T.TType _ "List" _ -> "[]any"
    -- Dict String V similarly emits `map[string]V` when V is concrete.
    T.TType _ "Dict" [_k, v] ->
        let inner = go v
        in if inner == "any" then "map[string]any" else "map[string]" ++ inner
    T.TType _ "Dict" _ -> "map[string]any"
    T.TType _ "Set"  _ -> "map[any]bool"
    -- Cmd/Sub: opaque Go types (ignore inner type param)
    T.TType _ "Cmd" _ -> "rt.SkyCmd"
    T.TType _ "Sub" _ -> "rt.SkySub"
    -- Std.Html.Html: htmlType in the constraint generator carries an
    -- empty home so it unifies with user `Html Msg` annotations. Map
    -- it to the generated ADT here, the same way Cmd/Sub are — the
    -- empty home would otherwise render the bare `Html`, which go
    -- build rejects ("undefined: Html"). The ADT codegens non-generic
    -- (`= rt.SkyADT`), so the msg type arg is dropped.
    T.TType _ "Html" _ -> "Std_Html_Html"
    T.TTuple _ _ []   -> "rt.SkyTuple2"
    T.TTuple _ _ [_]  -> "rt.SkyTuple3"
    T.TTuple _ _ _    -> "rt.SkyTupleN"
    T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
    -- Primitives (must check before the user-type catch-all)
    T.TType _ "Int" []    -> "int"
    T.TType _ "Float" []  -> "float64"
    T.TType _ "Bool" []   -> "bool"
    T.TType _ "String" [] -> "string"
    T.TType _ "Char" []   -> "rune"
    T.TType _ "Bytes" []  -> "[]byte"
    T.TUnit               -> "struct{}"
    -- User-defined types: resolve via record alias set + runtime map.
    -- NOTE: matches only `[]` (no type args) deliberately —
    -- v0.13 Layer 3: `_` (not `[]`) so a PARAMETERISED Sky ADT
    -- (`Html msg`, `Attribute msg`, `Element msg`) renders to its
    -- erased Go struct name (`Std_Html_Html`) rather than `any`.
    -- The ADT emits as a non-generic `type Mod_Name = rt.SkyADT`,
    -- so the type arg is irrelevant to the Go name.  The call-site
    -- coercion bridges `[]any` → `[]Mod_Name` via
    -- `rt.AsListT[Mod_Name]` (coerceArg's stripSlice arm) and
    -- `Mod_Name` ↔ `any` via `rt.Coerce`.
    T.TType home name _ ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            -- Search all module prefixes for record alias match
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList recAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c recAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                Just goTy -> Just goTy
                Nothing   -> lookup name runtimeTypedMap
            -- v0.13 A2 follow-up: empty-home cross-module ADT
            -- recovery. When `home` is empty (a TVar resolved to
            -- a Sky ADT via cross-decl constraint propagation but
            -- with the module attribution lost), the bare
            -- `base = name` renders undefined Go (`Colour` vs.
            -- declared `Chess_Piece_Colour`). Look up via the
            -- dedicated `globalUnionNames` IORef (kept separate
            -- from `globalCgEnv` so the read can't black-hole
            -- inside a `modifyIORef globalCgEnv` callback).
            unionRecovery
              | not (null modStr) = Nothing
              | otherwise =
                  let allUnions = unsafePerformIO (readIORef globalUnionNames)
                  in if Set.null allUnions
                       then Nothing
                       else if Set.member name allUnions
                              then Just name
                              else case [ u | u <- Set.toList allUnions
                                            , '_' `elem` u
                                            , reverse (takeWhile (/= '_') (reverse u)) == name
                                            ] of
                                       [u] -> Just u
                                       _   -> Nothing
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case runtimeTyped of
                Just goTy -> goTy
                Nothing   -> case unionRecovery of
                    Just u  -> u
                    Nothing -> if isRuntimeOnly then "any" else base
    T.TAlias home name typeArgs aliasType ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList recAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c recAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                Just goTy -> Just goTy
                Nothing   -> lookup name runtimeTypedMap
            inner = case aliasType of
                T.Filled  i -> i
                T.Hoisted i -> i
            -- v0.15 Stage E — emit explicit Go type-args.  Empty for
            -- non-parametric aliases.
            typeArgSuffix =
                if null typeArgs
                    then ""
                    else "[" ++ intercalate_ ", "
                              [ go argTy | (_, argTy) <- typeArgs ]
                          ++ "]"
        in case matches of
            (m:_) -> m ++ "_R" ++ typeArgSuffix
            _     -> case runtimeTyped of
                Just goTy -> goTy
                Nothing
                    | isRuntimeOnly -> "any"
                    | otherwise     -> case inner of
                        T.TRecord{} -> if null base then "any" else base ++ typeArgSuffix
                        _           -> go inner
    -- Bare anonymous record (HM collapses alias-of-record after row
    -- unification): match its field set against the codegen field-index
    -- registry. Without this, `mkJob id name = { id = ..., name = ... }`
    -- gets type `{id:Int, name:String, ...}` and the sig would degrade
    -- to `any` even though the body emits `Job_R{...}`.
    T.TRecord fields _ ->
        let fieldNames = Map.keys fields
        in case Rec.lookupRecordAlias fieldIdx fieldNames of
            Just aliasName ->
                case aliasGenericArgs aliasName fields of
                    Just (_, argTys) ->
                        aliasName ++ "_R[" ++
                        intercalate_ ", " (map go argTys) ++
                        "]"
                    Nothing -> aliasName ++ "_R"
            Nothing -> "any"
    _ -> safeReturnTypePure ty
  where
    go = typeStrWithAliasesReg recAliases fieldIdx tvarMap


-- | v0.15 Stage E — extract (alias-var ↦ actual-type) bindings from
-- a structural row record by positional matching against the alias's
-- declared body.  Walks both trees in parallel: where the alias body
-- has `T.TVar v` (v ∈ vars), records `v ↦ <type at same position>`.
-- For unbinable vars (alias has a var that doesn't appear in any
-- accessible field), the caller falls back to a synthetic TVar via
-- `aliasGenericArgs`.
--
-- Recursive arms match by name + arity to avoid the infinite-loop
-- class on cyclic parametric aliases (`Tree a = { kids : List (Tree a) }`).
extractAliasBindings :: [String] -> T.Type -> T.Type -> Map.Map String T.Type
extractAliasBindings vars aliasBody actualTy =
    snd (walk Map.empty aliasBody actualTy)
  where
    isVar v = v `elem` vars

    walk acc (T.TVar v) ty
        | isVar v   = (True, Map.insert v ty acc)
        | otherwise = (True, acc)
    walk acc (T.TLambda a1 b1) (T.TLambda a2 b2) =
        let (ok1, acc1) = walk acc a1 a2
            (ok2, acc2) = walk acc1 b1 b2
        in (ok1 && ok2, acc2)
    walk acc (T.TType _ n1 args1) (T.TType _ n2 args2)
        | n1 == n2 && length args1 == length args2 =
            foldl (\(ok, a) (l, r) ->
                let (ok', a') = walk a l r in (ok && ok', a'))
                (True, acc)
                (zip args1 args2)
        | otherwise = (False, acc)
    walk acc (T.TTuple a1 b1 cs1) (T.TTuple a2 b2 cs2)
        | length cs1 == length cs2 =
            foldl (\(ok, a) (l, r) ->
                let (ok', a') = walk a l r in (ok && ok', a'))
                (True, acc)
                (zip (a1 : b1 : cs1) (a2 : b2 : cs2))
        | otherwise = (False, acc)
    walk acc (T.TRecord f1 _) (T.TRecord f2 _) =
        let shared = Map.intersectionWith (,) f1 f2
            walkPair a (T.FieldType _ ta, T.FieldType _ tb) =
                let (_, a') = walk a ta tb in a'
        in (True, foldl walkPair acc (Map.elems shared))
    -- Alias-vs-alias / alias-vs-App1 by name + arity, no inner unwrap
    -- (recursive parametric aliases would loop).
    walk acc (T.TAlias _ n1 args1 _) (T.TAlias _ n2 args2 _)
        | n1 == n2 && length args1 == length args2 =
            foldl (\(ok, a) (l, r) ->
                let (ok', a') = walk a l r in (ok && ok', a'))
                (True, acc)
                (zip (map snd args1) (map snd args2))
        | otherwise = (False, acc)
    walk acc (T.TAlias _ _ args1 _) (T.TType _ _ args2)
        | length args1 == length args2 =
            foldl (\(ok, a) (l, r) ->
                let (ok', a') = walk a l r in (ok && ok', a'))
                (True, acc)
                (zip (map snd args1) args2)
        | otherwise = (True, acc)
    walk acc (T.TType _ _ args1) (T.TAlias _ _ args2 _)
        | length args1 == length args2 =
            foldl (\(ok, a) (l, r) ->
                let (ok', a') = walk a l r in (ok && ok', a'))
                (True, acc)
                (zip args1 (map snd args2))
        | otherwise = (True, acc)
    walk acc T.TUnit T.TUnit = (True, acc)
    walk acc _ _ = (True, acc)


-- | Collect TVars that survive to the final emitted Go type — i.e. TVars
-- that aren't inside a container type we erase to `any`/`[]any`. Used
-- so we don't declare `[T1 any]` when T1 never appears in the sig.
-- Accepts both single-char solver names (a, b, c) and user-level
-- annotation TVars (parentMsg, row, …) so `view : (Msg -> parentMsg)
-- -> Counter -> VNode` gets a concrete `[T1 any](toMsg func(...) T1)`
-- sig instead of `toMsg any` (which fails Go's function-covariance
-- check at the call site).
tvarsInEmitted :: T.Type -> [String]
tvarsInEmitted ty = case ty of
    T.TVar n
        | take 1 n == "_" -> [n]
        | otherwise       -> [n]
    T.TLambda a b -> tvarsInEmitted a ++ tvarsInEmitted b
    -- v0.13 C: container TVars propagate. The renderer (`safeReturnType*`
    -- / `typeStrWithAliasesReg`) already emits typed Go containers
    -- like `[]T1` / `map[string]V1` when an element TVar is known —
    -- collecting them here lets `splitInferredSigWithReg` declare
    -- `[T1 any]` Go generics, so user code calling
    -- `List.map : (a -> b) -> List a -> List b` gets a typed sig
    -- rather than collapsing the body to `[]any`. The
    -- `usedTypeParams` filter at the call site already drops any
    -- TVar that never appears in the rendered string, so
    -- overshooting here is safe — phantom params are stripped
    -- before the Go generic-param list is committed.
    T.TType _ "List" args -> concatMap tvarsInEmitted args
    T.TType _ "Dict" args -> concatMap tvarsInEmitted args
    T.TType _ "Set"  args -> concatMap tvarsInEmitted args
    T.TType _ "Result" args -> concatMap tvarsInEmitted args
    T.TType _ "Maybe"  args -> concatMap tvarsInEmitted args
    T.TType _ "Task"   args -> concatMap tvarsInEmitted args
    T.TType _ _ args -> concatMap tvarsInEmitted args
    T.TTuple a b cs -> concatMap tvarsInEmitted (a : b : cs)
    T.TAlias _ _ pairs (T.Filled inner)  -> concatMap tvarsInEmitted (inner : map snd pairs)
    T.TAlias _ _ pairs (T.Hoisted inner) -> concatMap tvarsInEmitted (inner : map snd pairs)
    -- v0.15 Stage E — capture TVars in record-field types AND
    -- surface synthetic TVars from parametric-alias generic args
    -- (handles the partial-use / subset-record case).  Reads
    -- `globalAllFieldIdx` (populated EARLY, safe to read from sig
    -- emission), NOT `getCgEnv` (which causes `<<loop>>` because
    -- this function runs DURING env construction).
    T.TRecord fields _ ->
        let fieldTVars = concatMap
                (\(T.FieldType _ ft) -> tvarsInEmitted ft)
                (Map.elems fields)
            fieldNames = Map.keys fields
            fieldIdx = unsafePerformIO (readIORef globalAllFieldIdx)
            aliasMatch = Rec.lookupRecordAlias fieldIdx fieldNames
            syntheticForRec = case aliasMatch of
                Just aliasName ->
                    case aliasGenericArgs aliasName fields of
                        Just (_, argTys) -> concatMap tvarsInEmitted argTys
                        Nothing          -> []
                Nothing -> []
        in fieldTVars ++ syntheticForRec
    T.TUnit     -> []


-- | Env-free version of safeReturnType for use during env bootstrap.
-- Doesn't recognise user record aliases (so they degrade to `any` in
-- the param/return tables); the codegen of the function body will
-- still see them via the live env. This is acceptable because record
-- aliases as call-site argument types are rare and the degradation
-- only loses a typing opportunity, not correctness.
safeReturnTypePure :: T.Type -> String
safeReturnTypePure t = case t of
    -- T4: Unit returns safely typed now — rt.ResultCoerce handles the
    -- generic-instantiation mismatch at the return wrap.
    T.TUnit                       -> "struct{}"
    T.TType _ "Int" []            -> "int"
    T.TType _ "Float" []          -> "float64"
    T.TType _ "Bool" []           -> "bool"
    T.TType _ "String" []         -> "string"
    T.TType _ "Char" []           -> "rune"
    T.TType _ "Bytes" []          -> "[]byte"
    T.TType _ "Result" [e, a]     -> "rt.SkyResult[" ++ safeReturnTypePure e
                                     ++ ", " ++ safeReturnTypePure a ++ "]"
    T.TType _ "Maybe"  [x]        -> "rt.SkyMaybe[" ++ safeReturnTypePure x ++ "]"
    T.TType _ "Task"   [e, a]     -> "rt.SkyTask[" ++ safeReturnTypePure e
                                     ++ ", " ++ safeReturnTypePure a ++ "]"
    T.TType _ "Cmd"    _          -> "rt.SkyCmd"
    T.TType _ "Sub"    _          -> "rt.SkySub"
    T.TType _ "List"   [elem]     ->
        let inner = safeReturnTypePure elem
        in if inner == "any" then "[]any" else "[]" ++ inner
    T.TType _ "List"   _          -> "[]any"
    T.TType _ "Dict"   [_, v]     ->
        let inner = safeReturnTypePure v
        in if inner == "any" then "map[string]any" else "map[string]" ++ inner
    T.TType _ "Dict"   _          -> "map[string]any"
    T.TType _ "Set"    _          -> "map[any]bool"
    T.TTuple _ _ []               -> "rt.SkyTuple2"
    T.TTuple _ _ [_]              -> "rt.SkyTuple3"
    T.TTuple _ _ _                -> "rt.SkyTupleN"
    T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
    -- Known runtime types with concrete Go definitions. Qualified
    -- overrides (e.g. Sky.Core.Http.Response -> rt.HttpResponse) win
    -- over the short-name lookup so the two `Response` types stay
    -- distinct at codegen.
    T.TType home name []
        | Just goTy <- lookup (ModuleName.toString home, name) qualifiedRuntimeTypedMap -> goTy
    T.TType _ name [] | Just goTy <- lookup name runtimeTypedMap -> goTy
    -- safeReturnTypePure has no env access — can't distinguish record
    -- aliases (need _R suffix) from ADTs (use name directly). Fall
    -- back to any for all user types. The env-aware safeReturnType
    -- handles these correctly for annotation-based param types.
    T.TAlias _ _ _ (T.Filled inner)  -> safeReturnTypePure inner
    T.TAlias _ _ _ (T.Hoisted inner) -> safeReturnTypePure inner
    _ -> "any"


-- Used by Map.fromList where values must be unique; here keys come from
-- distinct top-level names so no conflicts arise.
mapMaybe :: (a -> Maybe b) -> [a] -> [b]
mapMaybe _ []     = []
mapMaybe f (x:xs) = case f x of
    Just y  -> y : mapMaybe f xs
    Nothing -> mapMaybe f xs


splitFuncType :: Int -> T.Type -> ([T.Type], T.Type)
splitFuncType 0 ty = ([], ty)
splitFuncType n (T.TLambda from to) =
    let (rest, ret) = splitFuncType (n - 1) to
    in (from : rest, ret)
splitFuncType _ ty = ([], ty)  -- not enough arrows, return as-is


-- ═══════════════════════════════════════════════════════════
-- EXPRESSION CODE GENERATION
-- ═══════════════════════════════════════════════════════════


-- | v0.15.x P6 (Phase 2) — explicit-context lowering wrapper.
--
-- Installs `ctx` into `scopeStateRef` around the delegated call so
-- the IORef-based readers (`lookupLambdaType`, `lookupLambdaGoStr`,
-- `lookupRegionType`) observe the threaded ctx for the duration of
-- the lowering, then restores the previous value.  This is the
-- "ctx is the channel" Phase 2 step: callers explicitly pass ctx,
-- the wrapper installs it for the recursive backbone.  Phase 3
-- (P7) migrates the body away from `scopeStateRef` entirely.
--
-- The previous Phase 1 wrapper ignored ctx (`_ctx`) and was a pure
-- no-op delegate — useful only as a placeholder for the bottom-up
-- migration.  Phase 2 makes ctx actually flow through.
--
-- Concurrency: codegen runs single-threaded per compile (the
-- per-module Async parallelism happens at canonicalise/HM only;
-- final lowering serialises through `generateGoMulti`).  The
-- write/restore pair is therefore safe without an MVar lock.  If
-- future work parallelises lowering, switch to MVars on
-- `scopeStateRef` or push the read down to per-helper ctx
-- arguments (the Phase 3 direction anyway).
--
-- The lowering result must be forced to WHNF BEFORE the restore so
-- any deferred IORef-reading thunks see the installed ctx.
-- `GoBuilder.renderExpr` triggers full evaluation of the GoExpr
-- tree; we then wrap the rendered String as a `GoRaw` so the
-- downstream printer treats it verbatim.  Mirrors the technique
-- `withScopedLambdaTypes` uses for the same race class.
lowerExpr :: LC.LowerCtx -> Can.Expr -> GoIr.GoExpr
lowerExpr ctx e = unsafePerformIO $ do
    -- Force `ctx` to WHNF BEFORE the write so we never store a
    -- thunk into `scopeStateRef`.  Critical for the v0.15.x P37b
    -- cascade resume: callers obtain `ctx` via `ctxFromIORef ()`
    -- (itself an unsafePerformIO readIORef).  Without the seq,
    -- writing the thunk produces an IORef cell whose stored value
    -- is "reread me from scopeStateRef" — when ANY downstream
    -- code reads scopeStateRef and forces the value, it loops on
    -- itself.  GHC detects this and panics with `<<loop>>`.
    ctx `seq` return ()
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef ctx
    let rendered = GoBuilder.renderExpr (exprToGo e)
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)


-- | v0.15.x P6 (Phase 2) — explicit-context typed-slot wrapper.
-- Mirror of `lowerExpr` for the `exprToGoExpectGo` entry point.
-- Same write/restore + force-to-WHNF pattern.
lowerExprExpectGo :: LC.LowerCtx -> String -> Can.Expr -> GoIr.GoExpr
lowerExprExpectGo ctx goRendering e = unsafePerformIO $ do
    -- Same WHNF gate as `lowerExpr` — see its comment for why.
    ctx `seq` return ()
    prev <- readIORef scopeStateRef
    writeIORef scopeStateRef ctx
    let rendered = GoBuilder.renderExpr (exprToGoExpectGo goRendering e)
        forced = length rendered
    forced `seq` writeIORef scopeStateRef prev
    return (GoIr.GoRaw rendered)


-- | v0.15.x P6 (Phase 2) — read the current scope-state ctx from
-- the IORef.  Phase 2 callers that don't yet have an explicit
-- `ctx` parameter use this to opt in to ctx threading without
-- changing their call-graph parents.  Phase 3 (P7) deletes the
-- IORef; this helper goes with it.
--
-- NOINLINE so GHC doesn't memoise the snapshot across call sites:
-- a fresh read on every invocation matches the implicit-IO
-- semantics the rest of the codegen helpers (lookupLambdaType etc.)
-- already rely on.
{-# NOINLINE ctxFromIORef #-}
ctxFromIORef :: () -> LC.LowerCtx
ctxFromIORef () = unsafePerformIO (readIORef scopeStateRef)


-- | v0.15.x P38 (Cycle 3 / audit C10) — explicit-snapshot helper for
-- the P37b-resumed cascade slots (record-field init, list element,
-- let body).  Reads `scopeStateRef` ONCE at the call site and forces
-- the resulting `LC.LowerCtx` to WHNF before returning it.
--
-- Why a separate helper from `ctxFromIORef`:
--
--   1. The cascade-resume slots feed the returned ctx into the
--      ctx-aware wrappers (`lowerExprExpectGo` / `lowerExpr`).  Those
--      wrappers `seq` the incoming ctx before writing it back into
--      `scopeStateRef` — but that `seq` runs at the wrapper's entry,
--      AFTER any sibling computation that may have built deferred
--      thunks reading `scopeStateRef`.  Forcing WHNF at THE SNAPSHOT
--      site closes the secondary thunk race that PR #91 (P37b,
--      v0.15.19 / merged at c7a31df) called out as the load-bearing
--      `<<loop>>` reproducer on examples/13-skyshop: the wrapper
--      writes the not-yet-forced snapshot, downstream reads the
--      IORef, forces the value, finds "reread me from scopeStateRef",
--      and loops.  See the PR description's section "P37b — `seq` the
--      incoming ctx before writing it" for the exhaustive analysis.
--
--   2. The Haddock here documents the snapshot semantics explicitly:
--      the returned ctx is the value installed at the CALL site, NOT
--      any inner-wrapper-installed value.  Future cascade migrations
--      (record/list/let-body deepening; eventual full IORef deletion
--      under Phase 3+) must preserve this contract.  Naming the read
--      makes the semantic visible at every call site rather than
--      sprinkled across multiple `unsafePerformIO (readIORef …)`
--      transliterations.
--
--   3. NOINLINE matches `ctxFromIORef`: each call site forces a
--      fresh IORef read.  Without it GHC may share the snapshot
--      across nested cascade slots — silently breaking the
--      "snapshot at call site" contract.
--
-- The `seq` here is LOAD-BEARING; do NOT remove it as part of a
-- "tidy up" refactor.  Removing it re-opens the PR #91 thunk hazard
-- class.
{-# NOINLINE snapshotCallerCtx #-}
snapshotCallerCtx :: () -> LC.LowerCtx
snapshotCallerCtx () = unsafePerformIO $ do
    ctx <- readIORef scopeStateRef
    ctx `seq` return ctx


-- | v0.13 typed lowerer: convert a canonical expression to Go IR
-- WITH a known expected type from the surrounding context.
--
-- This is the typed entry point.  For control-flow shapes
-- (`Can.Case` / `Can.If` / `Can.Let`) it threads the expected type
-- into `caseToGo` / `ifToGo` / `letToGo`, which emit a `GoTypedBlock`
-- (typed IIFE) and coerce every branch `return` — recursing into
-- nested IIFE return values.  For every other shape it lowers via
-- the generic `exprToGo` then coerces the leaf to the expected Go
-- type via `coerceReturnExprT`.
--
-- The design keeps the refactor bounded: only three control-flow
-- functions gain a `Maybe String` parameter (the EXPECTED GO TYPE),
-- not all ~200 `exprToGo` call sites.  The expected type still
-- threads transitively because `caseToGo Just`/`ifToGo Just`/`letToGo
-- Just` call `typeIIFE`, whose `coerceReturnExprT` recurses into
-- nested `GoBlock` returns.
exprToGoExpect :: T.Type -> Can.Expr -> GoIr.GoExpr
exprToGoExpect expectedTy = exprToGoExpectGo (solvedTypeToGo expectedTy)


-- | Like `exprToGoExpect` but takes the expected GO TYPE STRING
-- directly.  Used by call-site arg lowering (`coerceCallArgsAt`)
-- where the param type is already known as a Go string.  The
-- `T.Type` variant just renders and delegates here.
exprToGoExpectGo :: String -> Can.Expr -> GoIr.GoExpr
exprToGoExpectGo goRendering e@(A.At _ expr)
    -- Universal safety gate: only thread the expected type when its
    -- Go rendering is a REAL, emittable Go type.  `goZeroValue`
    -- returning `Just` proves it (it can name a zero literal).  For
    -- anon-record names (`Anon_R_…` — may have no Go decl) and
    -- unresolved names `goZeroValue` is `Nothing` — fall back to
    -- plain `exprToGo`, no coercion, no typed IIFE.  This is what
    -- prevents the f2c7892-class regression: a mismatched /
    -- undefined Go type can never reach `rt.Coerce[…]` /
    -- `GoTypedBlock`.
    | goRendering == "any" = exprToGo e
    | not (isEmittableGoType goRendering) = exprToGo e
    | otherwise = case expr of
        Can.If branches elseExpr ->
            ifToGo (Just goRendering) branches elseExpr
        Can.Let def body ->
            letToGo (Just goRendering) def body
        Can.Case subject branches ->
            caseToGo (Just goRendering) subject branches
        -- v0.15 Stage C — type-directed Can.Lambda lowering.
        --
        -- When a lambda fills a typed slot (e.g. a parametric record
        -- field's `func(string) any` callback type), the lambda's
        -- emitted Go must use the SLOT'S typed parameters rather
        -- than the default `any`.  Pre-fix, an inline lambda like
        -- `onSubmit = \s -> Tag s` emitted as `func(s any) any {...}`,
        -- which Go rejected against the `func(string) any` slot.
        --
        -- Strategy: parse the slot's `func(P1, ..., PN) R` shape from
        -- `goRendering`.  For an N-param Sky lambda where N is the
        -- slot's arity, emit a single typed `GoFuncLit` with the
        -- parsed param types + return type.  Multi-param Sky lambdas
        -- where N != arity (currying) fall back to the generic
        -- `coerceReturnExprT` path.
        Can.Lambda pats body
            | Just (paramTys, retTy) <- parseFuncType goRendering
            , length paramTys == length pats
            , all (/= "any") paramTys -> do
                lowerTypedLambda pats paramTys retTy body

        -- v0.15 Stage C.2 — type-directed list literal.
        --
        -- When `goRendering` parses as `[]T`, each list element is
        -- lowered with T as expected type so nested lambdas and
        -- records get type-directed too.  Falls back to the generic
        -- `[]any` shape when the slot type is unrecognised.
        --
        -- v0.15.x P37b — list-element slot cascade RESUMED.  P6
        -- reverted this site because `letBindingType`'s IORef-
        -- backed region lookup formed a deferred-thunk cycle with
        -- the ctx-aware wrapper.  Now that `letBindingType` is
        -- pure (P37b makes its region lookup a pure projection
        -- over `Solve.SolvedTypes._stRegions`), the seam is gone
        -- and each element can route through the explicit-ctx
        -- wrapper so threaded ctx flows into nested lambdas /
        -- records.
        Can.List items
            | Just elemTy <- stripListType goRendering ->
                -- v0.15.x P38 — list-element slot routes its caller
                -- ctx through `snapshotCallerCtx` so the snapshot is
                -- forced to WHNF at the call site (closes the P37b
                -- PR #91 thunk hazard for the cascade-resume slots).
                let elemCtx = snapshotCallerCtx ()
                in GoIr.GoSliceLit elemTy
                    [ lowerExprExpectGo elemCtx elemTy it | it <- items ]

        -- v0.15 Stage E.2 — type-directed record literal.  When
        -- the slot's Go type is a parametric struct instantiation
        -- (e.g. `Cfg_R[Msg]`), emit the literal with the SAME
        -- instantiation so Go's type checker accepts it directly.
        -- Pre-fix, the literal emitted as bare `Cfg_R{...}` which
        -- Go rejects for generic types.
        Can.Record fields
            | isParametricAliasInstantiation goRendering ->
                lowerRecordLiteralTo goRendering fields
        _ ->
            -- Leaf / non-control-flow: lower generically, then coerce
            -- the result to the expected Go type.  `coerceReturnExprT`
            -- recurses into `GoBlock` (so a nested IIFE that slipped
            -- through still gets typed) and is a no-op when the Go
            -- type is already concrete + matching.
            coerceReturnExprT goRendering (exprToGo e)


-- | v0.15 Stage C helper — emit a `Can.Lambda` as a single typed
-- `GoFuncLit` whose parameters use the typed slot's expected
-- param-Go-types, and whose body is lowered with the slot's
-- expected return type so its leaf coerces correctly.
--
-- This is the single-fn-level analogue of `curryLambdaPat`: it
-- handles the case where the slot expects exactly N params (i.e.
-- the lambda fills a Go function value, not a curried Sky function).
-- For Sky lambdas with simple PVar patterns, the params bind
-- directly; for non-PVar patterns the existing `patternBindings`
-- machinery destructures inside the lambda body.
--
-- The lambda body lowers via `exprToGoExpectGo` with `retTy` so
-- nested constructs (let / case / if) get the typed-return
-- treatment too.
lowerTypedLambda
    :: [Can.Pattern]
    -> [String]        -- typed Go param types (parallel to patterns)
    -> String          -- typed Go return type
    -> Can.Expr        -- body
    -> GoIr.GoExpr
lowerTypedLambda pats paramTys retTy body =
    let (params, destructStmts) = unzip (zipWith oneParam [0 :: Int ..] (zip pats paramTys))
        allDestructStmts = concat destructStmts
        -- v0.13 typed lowerer: register PVar param types in the
        -- lambda-scope map so the body's `Can.VarLocal name`
        -- accesses recover the typed Go reference.  Mirrors what
        -- `wrapTyped`'s eta-expansion path does for partial-applied
        -- ctors.
        paramTypeBindings = Map.fromList
            [ (n, inferTypeFromGoString gty)
            | (A.At _ (Can.PVar n), gty) <- zip pats paramTys
            , gty /= "any"
            ]
        -- v0.15.x P6 (Phase 2) — thread ctx EXPLICITLY through the
        -- lambda body's lowering instead of relying on
        -- `withScopedLambdaTypes`'s push/pop IORef dance.  Build a
        -- per-lambda `LC.LowerCtx` extended with `paramTypeBindings`
        -- and lower the body via `lowerExprExpectGo` — the ctx-
        -- aware wrapper writes ctx into `scopeStateRef` for the
        -- duration of the call, then restores.  Same net effect as
        -- the legacy push/pop, but the ctx is now an EXPLICIT
        -- VALUE the caller passes, not implicit IORef state.
        --
        -- When `paramTypeBindings` is empty the body still routes
        -- through the wrapper so call-graph ctx threading stays
        -- uniform.  The wrapper re-installs the same ctx (no-op
        -- write+restore) so the rest of the IORef-based machinery
        -- is unaffected.
        parentCtx = ctxFromIORef ()
        lambdaCtx = LC.withLambdaTypes paramTypeBindings parentCtx
        bodyExpr = lowerExprExpectGo lambdaCtx retTy body
        wrappedBody =
            if null allDestructStmts
                then [GoIr.GoReturn bodyExpr]
                else allDestructStmts ++ [GoIr.GoReturn bodyExpr]
    in GoIr.GoFuncLit params retTy wrappedBody
  where
    oneParam :: Int -> (Can.Pattern, String) -> (GoIr.GoParam, [GoIr.GoStmt])
    oneParam idx (A.At _ pat, gty) = case pat of
        Can.PVar name -> (GoIr.GoParam (goSafeName name) gty, [])
        Can.PAnything -> (GoIr.GoParam "_" gty, [])
        Can.PUnit     -> (GoIr.GoParam "_" gty, [])
        _ ->
            let tmp = "_lp" ++ show idx
            in (GoIr.GoParam tmp gty, patternBindings tmp pat)


-- | v0.15 Stage C helper — coarse Go-string-to-Sky-type recovery for
-- the lambda-scope binding map.  Handles primitives + falls back to
-- `T.TVar "_"` (treated as unconstrained by the lambda-scope reader,
-- which is sufficient for the param-type-propagation use case).  The
-- returned type is consumed by `lookupLambdaType` for sub-expression
-- type recovery; precision can be improved later.
inferTypeFromGoString :: String -> T.Type
inferTypeFromGoString s = case s of
    "string" -> T.TType ModuleName.basics "String" []
    "int"    -> T.TType ModuleName.basics "Int" []
    "bool"   -> T.TType ModuleName.basics "Bool" []
    "float64"-> T.TType ModuleName.basics "Float" []
    "rune"   -> T.TType ModuleName.basics "Char" []
    _        -> T.TVar "_"


-- | v0.15 Stage E helper — detect a Go-string-typed parametric alias
-- instantiation, e.g. `Widget_Editor_Cfg_R[Msg]`.  Match: contains an
-- `_R[` substring AND ends with `]`.  Used by `exprToGoExpectGo`'s
-- Can.Record arm to route to the typed literal emitter.
isParametricAliasInstantiation :: String -> Bool
isParametricAliasInstantiation s =
    not (null s)
    && last s == ']'
    && "_R[" `List.isInfixOf` s


-- | v0.15 Stage E helper — lower a record literal targeting a typed
-- parametric-alias slot.  Emits the struct literal with the slot's
-- instantiation AND uses INSTANTIATED field types (msg → Msg, …) so
-- the field-init values render with the correct typed Go types,
-- matching Go's parametric struct field-type rules.
--
-- Example: slot `Cfg_R[Msg]`, alias body `{ onSubmit : Form → msg }`.
-- Instantiation: `msg ↦ Msg`.  OnSubmit's field type renders as
-- `func(Form_R) Msg`, not `func(Form_R) any`.  Field-init lowering
-- routes via `exprToGoExpectGo` with that typed slot.
lowerRecordLiteralTo
    :: String                        -- target Go type, e.g. "Cfg_R[Msg]"
    -> Map.Map String Can.Expr       -- record fields
    -> GoIr.GoExpr
lowerRecordLiteralTo targetTy fields =
    let entries = Map.toList fields
        fieldNames = map fst entries
        env = getCgEnv
        aliasMatch = Rec.lookupRecordAlias (Rec._cg_fieldIndex env) fieldNames
        aliasDecl = aliasMatch >>= flip Map.lookup (Rec._cg_aliases env)
        -- Parse `Cfg_R[Msg, OtherMsg]` → ["Msg", "OtherMsg"].
        targetArgs = parseTargetArgs targetTy
        fieldTypeMap = case aliasDecl of
            Just (Can.Alias skyVars (T.TRecord m _)) ->
                let tvarSubst = Map.fromList (zip skyVars targetArgs)
                in Map.map (\(T.FieldType _ ty) ->
                        substituteTVarsToGo tvarSubst ty) m
            _ -> Map.empty
        -- v0.15.x P37b — record-field-init slot cascade RESUMED.
        -- P6 reverted this site because the wrapper's
        -- write-and-force interacted with `letBindingType`'s
        -- deferred IORef snapshot inside the enclosing function
        -- body's laziness — under examples/16-skychess /
        -- CoerceArgParametricSpec this blackholed GHC.  Now that
        -- `letBindingType` is pure, the seam is gone: every field
        -- routes through the explicit-ctx wrapper so threaded ctx
        -- flows into nested record / lambda / list arms.
        --
        -- v0.15.x P38 — caller ctx now flows through
        -- `snapshotCallerCtx` so the WHNF force happens at the
        -- snapshot site, not deferred to the wrapper's entry seq
        -- (see helper Haddock for the P37b PR #91 thunk hazard).
        fieldCtx = snapshotCallerCtx ()
        lowerField fn fe =
            let fieldGoTy = Map.findWithDefault "any" fn fieldTypeMap
            in coerceToFieldType fieldGoTy
                   (lowerExprExpectGo fieldCtx fieldGoTy fe)
    in GoIr.GoStructLit targetTy
        [ (capitalise_ fn, lowerField fn fe)
        | (fn, fe) <- entries
        ]
  where
    capitalise_ [] = []
    capitalise_ (c:cs) = toUpper c : cs
    toUpper c = if c >= 'a' && c <= 'z' then toEnum (fromEnum c - 32) else c


-- | v0.15 Stage E helper — parse `Cfg_R[A, B, C]` → ["A", "B", "C"].
-- Returns the list of arg type-strings (top-level comma split,
-- bracket-balanced).  Returns [] when input doesn't carry the
-- `_R[...]` instantiation form.
parseTargetArgs :: String -> [String]
parseTargetArgs s = case List.dropWhile (/= '[') s of
    '[' : rest -> case dropTrailingBracket rest of
        Just inner -> splitTopLevelArgs 0 [] inner
        Nothing    -> []
    _ -> []
  where
    dropTrailingBracket str = case reverse str of
        ']' : tailR -> Just (reverse tailR)
        _           -> Nothing
    splitTopLevelArgs :: Int -> String -> String -> [String]
    splitTopLevelArgs _ acc [] = [reverse acc | not (null acc)]
    splitTopLevelArgs 0 acc (',':' ':cs) = reverse acc : splitTopLevelArgs 0 [] cs
    splitTopLevelArgs 0 acc (',':cs)     = reverse acc : splitTopLevelArgs 0 [] cs
    splitTopLevelArgs d acc ('[':cs)     = splitTopLevelArgs (d+1) ('[':acc) cs
    splitTopLevelArgs d acc (']':cs)     = splitTopLevelArgs (d-1) (']':acc) cs
    splitTopLevelArgs d acc (c:cs)       = splitTopLevelArgs d (c:acc) cs


-- | v0.14.x Stage 4: look up a Sky-source (home, name) in the
-- kernel-alias registry.  Returns the matching (kernelMod, kernelName)
-- pair so codegen can route the call through the typed kernel dispatch
-- instead of treating the Sky-source binding as a regular user function.
{-# NOINLINE lookupKernelAlias #-}
lookupKernelAlias :: ModuleName.Canonical -> String -> Maybe (String, String)
lookupKernelAlias home name = unsafePerformIO $ do
    aliases <- readIORef globalKernelAlias
    return $ Map.lookup (home, name) aliases


-- | v0.14.x Stage 4: if `func` is a `Can.VarTopLevel` that resolves to
-- a kernel alias, rewrite the head to the corresponding `Can.VarKernel`
-- so the existing kernel-dispatch arms in `Can.Call` codegen take over
-- (typed-kernel routing, literal-arg fast paths, etc.).
rewriteAliasHead :: Can.Expr -> Can.Expr
rewriteAliasHead expr@(A.At r e) = case e of
    Can.VarTopLevel home name
        | Just (kMod, kFn) <- lookupKernelAlias home name ->
            A.At r (Can.VarKernel kMod kFn)
    _ -> expr


-- | Convert a canonical expression to Go IR
exprToGo :: Can.Expr -> GoIr.GoExpr
exprToGo (A.At _ expr) = case expr of

    Can.Str s ->
        GoIr.GoStringLit s

    Can.Int n ->
        GoIr.GoIntLit n

    Can.Float f ->
        GoIr.GoFloatLit f

    Can.Chr c ->
        GoIr.GoRuneLit c

    Can.Unit ->
        GoIr.GoRaw "struct{}{}"

    Can.VarLocal name ->
        -- Keyword-escape: a Sky local named var/type/range/etc. must
        -- reference as <name>_ to match its escaped declaration.
        GoIr.GoIdent (goSafeName name)

    Can.VarTopLevel home name
        -- v0.14.x Stage 4: Sky-source binding aliased to a kernel via
        -- `name = Ffi.kernel "KernelName"` — emit the kernel binding
        -- directly so the typed-codegen path takes over.
        | Just (kMod, kFn) <- lookupKernelAlias home name ->
            kernelToGo kMod kFn

    Can.VarTopLevel home name ->
        -- For cross-module references, prefix with module name.
        -- Zero-arg top-level values are emitted as functions, so references must call them.
        let modStr = ModuleName.toString home
            qualName = if null modStr || modStr == "Main"
                then goSafeName name
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ goSafeName name
            env = getCgEnv
            -- Local module: check zeroArgs set. Cross-module: check funcArities
            -- which is populated with qualified names from deps.
            isZeroArg = Set.member name (Rec._cg_zeroArgs env)
                     || Map.lookup qualName (Rec._cg_funcArities env) == Just 0
            -- T4b: if the function is generic (has type params), a bare
            -- reference needs explicit instantiation or Go rejects it
            -- with "cannot infer T1". Instantiate each type param as
            -- `any` so the function-value usage works.
            inferredTypeParams = case Map.lookup qualName (Rec._cg_funcInferredSigs env) of
                Just (tps, _, _) -> tps
                Nothing          -> []
            instantiatedName = if null inferredTypeParams
                then qualName
                else qualName ++ "[" ++ intercalateComma (replicate (length inferredTypeParams) "any") ++ "]"
        in if isZeroArg
            then GoIr.GoCall (GoIr.GoIdent qualName) []
            else GoIr.GoIdent instantiatedName

    Can.VarKernel modName funcName ->
        kernelToGo modName funcName

    Can.VarCtor opts home typeName ctorName annot ->
        ctorToGo opts home typeName ctorName annot

    Can.List items ->
        GoIr.GoSliceLit "any" (map exprToGo items)

    Can.Negate inner ->
        -- For literal negation, use direct Go negative literal
        case inner of
            A.At _ (Can.Int n) -> GoIr.GoIntLit (-n)
            A.At _ (Can.Float f) -> GoIr.GoFloatLit (-f)
            _ -> GoIr.GoCall (GoIr.GoQualified "rt" "Negate") [exprToGo inner]

    Can.Binop op opHome opName _annot left right ->
        binopToGo op left right

    Can.Lambda params body ->
        -- Generate curried function: \a b -> body becomes func(a any) any { return func(b any) any { return body } }
        curryLambdaPat params (exprToGo body)

    Can.Call rawFunc args ->
        -- v0.14.x Stage 4: if the callee is a Sky-source binding that's
        -- aliased to a kernel via `name = Ffi.kernel "K_n"`, rewrite the
        -- head to `Can.VarKernel "K" "n"` so the kernel-dispatch arms
        -- below take over.  No-op when not an alias.
        let func = rewriteAliasHead rawFunc in
        case A.toValue func of
            -- v0.12.x typed-codegen Phase 3: route List.* kernels with
            -- typed-T variants when the call-site list arg's element
            -- type is concrete. `List.map fn (xs : List Int)` becomes
            -- `rt.List_mapT[int, any](fn, xs)` instead of the default
            -- `rt.List_mapAny(fn, xs)`. The lambda still flows as
            -- `func(any) any` (Gap 4 territory) so the runtime helper
            -- handles the call shape internally; the win is the typed
            -- slice in/out — drops the AsListT coercion at the
            -- boundary and lets Go iterate without per-element type
            -- assertion. Falls back to the default any-routing when
            -- the list type isn't concrete (polymorphic helpers).
            Can.VarKernel modName funcName
                | let typedCall = kernelTypedCall
                        (Rec._cg_solvedTypes getCgEnv) modName funcName args
                        (map exprToGo args)
                , Just expr <- typedCall ->
                    expr

            -- P7 step 5: generalise the zero-arg FFI migration. Any
            -- Sky `KernelMod.fn ()` where (a) the kernel module name
            -- starts with "Go_" (i.e. it's a user-added FFI package,
            -- not a built-in kernel like Sky_Core_*), (b) the call has
            -- a single Unit arg, and (c) FfiGen has emitted a typed
            -- variant `<Kernel>_<fn>T` (registered in the IORef seeded
            -- from ffi/*.go at compile start), routes to the typed
            -- wrapper with no unit arg. Result.withDefault and the
            -- `case _ of Ok/Err` path both accept any SkyResult shape
            -- via reflect, so downstream semantics are preserved.
            Can.VarKernel modName funcName
                | take 3 modName == "Go_"
                , all isUnitArg args
                , let typedName = modName ++ "_" ++ funcName ++ "T"
                , Set.member typedName typedFfiWrapperSet ->
                    GoIr.GoCall (GoIr.GoQualified "rt" typedName) []

            -- P7 step 5b: migrate N-arg FFI by coercing each arg to the
            -- typed wrapper's declared Go param type. `any(arg).(T)`
            -- works for both any-typed and concrete-typed sources — a
            -- no-op in the concrete case. Literal Sky args are still
            -- emitted as Go literals (no `any(...)` wrap) for
            -- readability; Go's literal-to-named-type inference keeps
            -- these compiling.
            Can.VarKernel modName funcName
                | take 3 modName == "Go_"
                , not (null args)
                , not (all isUnitArg args)
                , let typedName = modName ++ "_" ++ funcName ++ "T"
                , Set.member typedName typedFfiWrapperSet
                , Just paramTys <- Map.lookup typedName typedFfiWrapperParams
                , length paramTys == length args ->
                    let anyWrapperName = modName ++ "_" ++ funcName
                    in GoIr.GoCall (GoIr.GoQualified "rt" typedName)
                                (zipWith3 (coerceFfiArgViaAlias anyWrapperName)
                                          [0 :: Int ..]
                                          paramTys
                                          args)

            -- P8 step 4: migrate kernel calls to their typed T companions
            -- when every arg is a primitive Sky literal. Kernels like
            -- `String_toUpper("abc")` gain `String_toUpperT("abc")` —
            -- Go literal-to-named-type inference handles the conversion.
            -- Narrow scope for safety: literal-arg only, matched against
            -- a hand-curated list of simple-param kernels.
            Can.VarKernel modName funcName
                | not (null args)
                , all isPrimLiteralArg args
                , Set.member (modName, funcName) typedKernelLiterals ->
                    let altSuffix = Map.findWithDefault
                            (funcName ++ "T")
                            (modName, funcName)
                            typedKernelAltName
                    in GoIr.GoCall
                        (GoIr.GoQualified "rt" (modName ++ "_" ++ altSuffix))
                        (map exprToGo args)

            -- P8 step 4 widening: kernel typed dispatch for non-literal
            -- args. Coerces each arg via the appropriate runtime helper
            -- (rt.AsInt / rt.AsFloat / rt.AsBool / fmt.Sprintf for
            -- string) so the typed kernel sees a concrete primitive
            -- value. Only fires for kernels whose Sky-level signature
            -- is described in `typedKernelArgCoerce`.
            Can.VarKernel modName funcName
                | not (null args)
                , Just coercers <- Map.lookup (modName, funcName) typedKernelArgCoerce
                , length coercers == length args ->
                    let altSuffix = Map.findWithDefault
                            (funcName ++ "T")
                            (modName, funcName)
                            typedKernelAltName
                    in GoIr.GoCall
                        (GoIr.GoQualified "rt" (modName ++ "_" ++ altSuffix))
                        (zipWith coerceTypedKernelArg coercers args)

            -- v0.13 Stage 1 — kernel call fallback with recovery σ.
            -- Reads the kernel's HM signature via `lookupKernelType`,
            -- renders param types as Go strings, then applies the
            -- same recovery-σ + structural-unification pattern used
            -- by `coerceCallArgsAt`. Pins kernel-generic TVars
            -- (e.g. Cmd.perform's `e, a, msg`) from typed args so
            -- the callback lambda emits typed instead of
            -- `func(any) any`. Closes a major class of adapters in
            -- Sky.Live apps (Cmd.perform, Task.andThen, etc.).
            Can.VarKernel modName funcName
                | Just (T.Forall _ kernelTy) <-
                    ConstrainExpr.lookupKernelType modName funcName
                , let kernelParamGoTys =
                        kernelParamGoTypes kernelTy (length args)
                , length kernelParamGoTys == length args
                , any (\t -> containsGenericTypeParam t
                          || take 5 t == "func(")
                      kernelParamGoTys ->
                    let goFunc = exprToGo func
                        goArgs0 = map exprToGo args
                        -- v0.15.8 (P2): σ-recovery DELIBERATELY does
                        -- NOT consume the structural fallback (passes
                        -- Nothing).  Reason: over-pinning a callee
                        -- TVar from a typed-call arg can conflict
                        -- with a sibling arg's Go-side inference,
                        -- causing `does not match inferred type`
                        -- errors at `go build` (`List.map` with a
                        -- typed list arg + an `any`-typed lambda).
                        -- The fallback fires at the COERCION sites
                        -- (coerceArg's parametric-alias arm) where
                        -- it elides wraps without affecting σ
                        -- pinning.  Keep the σ path strict.
                        bareRecovered = Map.fromList
                            [ (pty, cgo)
                            | (pty, ga) <- zip kernelParamGoTys goArgs0
                            , isGenericTypeParam pty
                            , Just cgo <- [goExprGoType Nothing ga]
                            , cgo /= "any"
                            , not (isGenericTypeParam cgo)
                            ]
                        structuralRecovered = Map.unions
                            [ unifyGoTypes pty cgo
                            | (pty, ga) <- zip kernelParamGoTys goArgs0
                            , not (isGenericTypeParam pty)
                            , containsGenericTypeParam pty
                            , Just cgo <- [goExprGoType Nothing ga]
                            , cgo /= "any"
                            ]
                        recovered = Map.union bareRecovered structuralRecovered
                        substituteOnly pty =
                            let subbed = substTVarsInGoType recovered pty
                                unboundTVars =
                                    [ t | t <- tvarsInGoTypeStr subbed
                                        , not (Map.member t recovered) ]
                            in if null unboundTVars
                                 then subbed
                                 else if containsGenericTypeParam subbed
                                        then eraseTypeParams subbed
                                        else subbed
                        substitutedParams = map substituteOnly kernelParamGoTys
                        -- Route each arg through the typed-aware
                        -- fallback (handles Can.Lambda → typed
                        -- emission, regular args → coerceArg).
                        goArgs = zipWith3
                            (kernelCoerceArg substitutedParams)
                            [0 :: Int ..]
                            substitutedParams
                            args
                    in GoIr.GoCall goFunc goArgs

            Can.VarCtor _opts _home _typeName _ctorName annot ->
                -- ADT constructor partial app: JobDone : Int -> Result -> Msg
                -- applied to just `jid` must close over jid.
                let declared = ctorArity annot
                    got = length args
                    paramTys = ctorParamTypes annot
                in if got < declared
                    then emitPartialCtor func args (declared - got)
                    -- T1: coerce each arg to the ctor's declared param type.
                    -- v0.15.2: route through `zipWithDefaultExpect` so a
                    -- Can.Record literal passed to a parametric record
                    -- ctor slot lowers with the slot's type args, not the
                    -- generic Cfg_R[any] + Coerce wrap that panics on
                    -- Go's nominal generic types.
                    else GoIr.GoCall (exprToGo func)
                          (zipWithDefaultExpect paramTys args)
            Can.VarTopLevel home name ->
                -- Partial application of a top-level function:
                -- `canViewMonitor session` where canViewMonitor : Session -> Monitor -> Bool
                -- must yield a closure capturing session.
                let env = getCgEnv
                    modStr = ModuleName.toString home
                    -- goSafeName escapes Sky function names that collide
                    -- with Go reserved words / built-ins (e.g. `go`,
                    -- `defer`, `chan`, `make`, `len`). Definition site
                    -- already does this (see emitFunctionDecl ~line 2048);
                    -- this call-site path used to emit the raw name and
                    -- generate `go(...)` instead of `go_(...)`, which
                    -- the Go parser interprets as a goroutine launch.
                    qualName = if null modStr || modStr == "Main"
                        then goSafeName name
                        else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ goSafeName name
                    declared = Map.findWithDefault (length args) qualName (Rec._cg_funcArities env)
                    got = length args
                in if got < declared && declared > 0
                    then emitPartialUserCall func args (declared - got)
                    -- Over-application: callee returns a function value
                    -- which we apply further.  `pickIf : Bool -> (Int -> Int)`
                    -- called as `pickIf True 10` must lower to a chain
                    -- `pickIf(true)(10)`, not the (wrong) flat call
                    -- `pickIf(true, 10)`.  Route the extras through
                    -- `rt.SkyCall` — the reflective dispatcher handles
                    -- whatever Go shape the inner call returns (typed
                    -- `func(int) int`, an `any`-routing closure, or a
                    -- runtime-built MakeFunc value).  Cost: ~100 ns per
                    -- extra-arg call site.
                    else if got > declared && declared > 0
                    then emitOverApplication func args declared
                    -- T2/T6: when the callee has typed params (recorded
                    -- in env._cg_funcParamTypes), coerce each `any`-arg
                    -- expression to the expected param type.
                    --
                    -- v0.13 Phase A5: route through `coerceCallArgsAt`
                    -- which consults `_cg_callSiteInstances`.  When
                    -- the solver captured a monomorphisation instance
                    -- at this call's source region, the callee's
                    -- generic param types get substituted with the
                    -- instance's concrete Go types before coerceArg
                    -- runs.  This is what makes `Sky_Core_Maybe_with
                    -- Default(s, MaybeCoerce[string](m))` work at
                    -- typed-codegen call sites instead of emitting
                    -- `MaybeCoerce[any]` that Go's inference rejects.
                    else
                        -- v0.13 Phase A4: if a specialised instance
                        -- exists for this call's region, use its
                        -- mangled name AND coerce args via the
                        -- substitution path (which now includes
                        -- typed lambda emission).  Args go through
                        -- `coerceCallArgsAt` so the spec's typed
                        -- param positions receive properly-typed
                        -- values.  Falls back to the generic name
                        -- when no spec was emitted.
                        let mMangled = instanceMangledName (A.toRegion func) qualName
                            callName = maybe qualName id mMangled
                        in GoIr.GoCall (GoIr.GoIdent callName)
                                       (coerceCallArgsAt
                                            (A.toRegion func)
                                            qualName
                                            args)
            _ ->
                let goFunc = exprToGo func
                    -- Same-module local function calls (`Can.VarLocal`)
                    -- benefit from coerceCallArgs too so typed callees
                    -- get their args asserted at call time. Look up the
                    -- bare name against the entry-module entries we've
                    -- registered in env._cg_funcParamTypes.
                    localQual = case A.toValue func of
                        Can.VarLocal n -> goSafeName n
                        _              -> ""
                    -- Constructor calls: coerce any-typed args to match
                    -- the typed constructor param types (record alias or ADT).
                    ctorParamTypes = case A.toValue func of
                        Can.VarCtor _ home typeName _ (Can.Forall _ ctorTy) ->
                            let modStr = ModuleName.toString home
                                prefix = if null modStr || modStr == "Main"
                                         then "" else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
                                aliasName = prefix ++ typeName
                                env' = getCgEnv
                            in case Map.lookup aliasName (Rec._cg_aliases env') of
                                Just (Can.Alias _ (T.TRecord m _)) ->
                                    let fieldList = List.sortOn (T._fieldIndex . snd) (Map.toList m)
                                    in map (\(_, T.FieldType _ fty) -> solvedTypeToGo fty) fieldList
                                _ ->
                                    -- ADT constructor: extract param types
                                    -- from the annotation's arrow type.
                                    let extractParams (T.TLambda from to) =
                                            safeReturnType from : extractParams to
                                        extractParams _ = []
                                    in extractParams ctorTy
                        _ -> []
                    goArgs
                        | not (null ctorParamTypes) =
                            -- v0.15.2: ctor call args go through
                            -- `zipWithDefaultExpect` to surface Can.Record /
                            -- Can.Lambda at parametric-record / typed-func
                            -- slots directly (no `Cfg_R[any] + Coerce`
                            -- wrap that panics under Go's nominal generic
                            -- type assertions).  Behaviour identical for
                            -- non-special args (falls through to coerceArg).
                            zipWithDefaultExpect (ctorParamTypes ++ repeat "any") args
                        | not (null localQual) =
                            coerceCallArgs localQual args
                        | otherwise = map exprToGo args
                    -- v0.15.10 / Gap A5 — typed-callable HOF fast-path.
                    --
                    -- When `func` is NOT one of the structural-direct
                    -- callees (`Can.VarLocal` / `Can.Access` / `Can.Call`
                    -- result, etc.) but its HM-recovered Go shape is
                    -- `func(P1) R` with a single emittable param + ret,
                    -- and the call has exactly one arg, emit a Go-
                    -- native `goFunc(typedArg)` instead of routing
                    -- through `rt.SkyCall`'s reflect dispatcher.
                    -- Closes the audit Gap A5 symptom in
                    -- `addOne f x = f (x + 1)`: pre-fix emitted
                    -- `rt.CoerceInt(rt.SkyCall(f, x + 1))`, post-fix
                    -- emits `f(x + 1)` Go-native.
                    --
                    -- The arg routes through `zipWithDefaultExpect` so
                    -- the typed slot propagates into the lowering of the
                    -- arg expression (binops therein emit Go-native
                    -- when the slot is a Go primitive; record literals
                    -- pick up the parametric-alias instantiation; etc.)
                    -- — the same path ctor calls already use.
                    --
                    -- Recovery uses HM (`inferExprType`) NOT the lambda-
                    -- types scope: the scope is pushed on the WRAPPED
                    -- expr right before render; sub-expression thunks
                    -- like this `Can.Call` arm may force BEFORE the
                    -- wrap's IO action runs, leaving the IORef empty
                    -- during the lookup.  HM is global and pre-populated
                    -- by the time codegen runs.
                    --
                    -- Restricted to **single-arg** calls (`f x`).  Sky
                    -- HM always renders curried (`T1 -> T2 -> T3` ↦
                    -- `func(T1) func(T2) T3`), but the EMITTED Go for
                    -- multi-pattern let-bound functions is FLAT
                    -- (`func(t1, t2) t3`).  The codegen has no per-
                    -- callee record we can consult at this layer to
                    -- disambiguate, so multi-arg sites stay on the
                    -- `rt.SkyCall` reflect path (which curries via
                    -- `skyCallOne` and is correct for both shapes).
                    -- At single-arg call sites the distinction
                    -- collapses (`func(T1) <rest>` is the only shape),
                    -- so the fast-path is sound.
                    --
                    -- Slot types must be emittable, concrete, and
                    -- free of generic-type-param leaks — those would
                    -- defeat Go's call-site type inference.
                    -- Soundness gate: only fire for `Can.VarLocal`
                    -- callees.  The emitted Go for those is a bare
                    -- `GoIdent (goSafeName name)` — and whenever the
                    -- HM type is a function arrow the EMITTED Go
                    -- param IS that typed function shape (the entry-
                    -- and dep-module decl paths zip `typedGoParams`
                    -- with the resolved `solvedTypeToGo` per
                    -- annotation slot).  So the bare ident statically
                    -- carries a Go function value and a direct call
                    -- typechecks.
                    --
                    -- We deliberately EXCLUDE other shapes — most
                    -- importantly `Can.Access` (emission goes through
                    -- `rt.Field` returning `any` and Go rejects
                    -- direct-call on `any`).  Other indirect shapes
                    -- (`Can.Call` result, `Can.Update`, `Can.LetRec`
                    -- name, …) likewise widen to `any` at the call
                    -- boundary and would fail Go's static type
                    -- check.  These all stay on the `rt.SkyCall`
                    -- reflect path which handles `any` callees
                    -- correctly.
                    isLocalCallable = case A.toValue func of
                        Can.VarLocal _ -> True
                        _              -> False
                    typedCallableShape =
                        if isDirectCallable func
                            || not isLocalCallable
                            || length args /= 1
                            then Nothing
                        else let solved = Rec._cg_solvedTypes getCgEnv
                             in case inferExprType solved func of
                                  Just ty ->
                                      peelTypedArrows 1 (solvedTypeToGo ty)
                                  Nothing -> Nothing
                in case typedCallableShape of
                    Just (typedParamTys, _typedRetTy) ->
                        let typedArgs = zipWithDefaultExpect
                                            (typedParamTys ++ repeat "any") args
                        in GoIr.GoCall goFunc typedArgs
                    Nothing ->
                        if isDirectCallable func
                            then GoIr.GoCall goFunc goArgs
                            else GoIr.GoCall (GoIr.GoQualified "rt" "SkyCall")
                                    (goFunc : goArgs)

    Can.If branches elseExpr ->
        -- Generic context — no expected type known here.  The typed
        -- path is reached via `exprToGoExpect`.
        ifToGo Nothing branches elseExpr

    Can.Let def body ->
        letToGo Nothing def body

    Can.LetRec defs body ->
        let stmts = concatMap defToStmts defs
        in GoIr.GoBlock stmts (exprToGo body)

    Can.LetDestruct pat valExpr body ->
        -- Bind the value to a fresh temp, then run the standard pattern-
        -- bindings machinery (same code used by case arms) so tuple/record/
        -- constructor destructuring produces real bindings for each field.
        let tmp = "__destruct__"
            (A.At _ p) = pat
            valStmt = GoIr.GoShortDecl tmp (exprToGo valExpr)
            sink    = GoIr.GoAssign "_" (GoIr.GoIdent tmp)
            bindStmts = patternBindings tmp p
        in GoIr.GoBlock (valStmt : sink : bindStmts) (exprToGo body)

    Can.Case subject branches ->
        caseToGo Nothing subject branches

    Can.Accessor field ->
        -- Record accessor function: .field → func(r any) any { return rt.Field(r, "Field") }
        GoIr.GoFuncLit [GoIr.GoParam "__r" "any"] "any"
            [GoIr.GoReturn (GoIr.GoCall (GoIr.GoQualified "rt" "Field") [GoIr.GoIdent "__r", GoIr.GoStringLit (capitalise_ field)])]

    Can.Access target (A.At _ field) ->
        -- v0.13 typed lowerer: emit Go-native field access
        -- (`target.Field`) when the target's HM type resolves to a
        -- known record alias AND the target is statically typed
        -- (its Go static type matches a record-alias Go struct).
        -- Falls back to `rt.Field` (reflect) otherwise.
        let solved = Rec._cg_solvedTypes getCgEnv
            env = getCgEnv
            recSet = Rec._cg_recordAliases env
            nameMatches name =
                Set.member name recSet ||
                any (\a -> let parts = splitOn '_' a
                           in not (null parts) && last parts == name)
                    (Set.toList recSet)
            -- v0.15.3 — for a local-ident target, prefer the per-
            -- function-scope `lookupLambdaType` ONLY when
            -- `inferExprType` (via solvedTypes) returns an
            -- unresolved TVar.  Module-level solvedTypes can
            -- record a function param as `TVar "_ambig"` for
            -- parametric record alias types (`view cfg` where
            -- `cfg : Setup msg`), but `withScopedLambdaTypes`
            -- registers the concrete TAlias.  Narrowed to this
            -- specific case (TVar fallback only) so we don't
            -- shadow the more-precise solved type for typed
            -- aliases like `t : Tree Int` whose Tree-param sub-
            -- type is required at element-type-rendering sites
            -- and is captured correctly by solvedTypes.
            inferredViaSolved = inferExprType solved target
            isAmbigTVar (Just (T.TVar _)) = True
            isAmbigTVar _ = False
            targetTy = case (target, inferredViaSolved) of
                (A.At _ (Can.VarLocal name), tv) | isAmbigTVar tv ->
                    case lookupLambdaType name of
                        Just t  -> Just t
                        Nothing -> tv
                _ -> inferredViaSolved
            isRecordAlias = case targetTy of
                Just (T.TAlias _ name _ _) -> nameMatches name
                Just (T.TType _ name _)    -> nameMatches name
                Just (T.TRecord fields _) ->
                    let names = Map.keys fields
                    in case Rec.lookupRecordAlias
                                (Rec._cg_fieldIndex env) names of
                        Just _ -> True
                        Nothing -> False
                _ -> False
            -- v0.15.3 — secondary check via `lookupLambdaGoStr`.
            -- For function params that registered as Go-type
            -- strings (e.g. parametric record alias `cfg :
            -- Setup_R[T1]` via `goStringBindings`), the Sky-type
            -- registry (`lookupLambdaType`) is empty due to a
            -- lazy-eval race between scoped binding push/pop and
            -- GoIR rendering.  The Go-string registry IS active
            -- during render, so consult it here to catch the same
            -- record-alias-target case.  Matches when the
            -- recorded Go type parses as a parametric record
            -- alias (`Foo_R[T]`) whose `Foo` is a known alias.
            isRecordAliasViaGoStr = case target of
                A.At _ (Can.VarLocal name) ->
                    case lookupLambdaGoStr name of
                        Just goTy
                          | Just base <- parametricAliasBase goTy
                          , let aliasName = take (length base - 2) base
                          , nameMatches aliasName -> True
                        _ -> False
                _ -> False
            targetTyped =
                (isRecordAlias && operandIsStaticallyTyped target)
                || isRecordAliasViaGoStr
        in if targetTyped
              then GoIr.GoSelector (exprToGo target) (capitalise_ field)
              else GoIr.GoCall (GoIr.GoQualified "rt" "Field")
                       [exprToGo target, GoIr.GoStringLit (capitalise_ field)]

    Can.Update _name baseExpr fields ->
        -- Record update via reflect-based runtime helper (works on any + typed structs)
        let baseGo = GoBuilder.renderExpr (exprToGo baseExpr)
            fieldUpdates = Map.toList fields
            pairs = map (\(fname, Can.FieldUpdate _ fexpr) ->
                "\"" ++ capitalise_ fname ++ "\": " ++ GoBuilder.renderExpr (exprToGo fexpr))
                fieldUpdates
        in GoIr.GoRaw $ "rt.RecordUpdate(" ++ baseGo ++ ", map[string]any{" ++
            intercalate_ ", " pairs ++ "})"

    Can.Record fields ->
        -- Record literal: look up matching type alias → named struct, or anonymous
        let entries = Map.toList fields
            fieldNames = map fst entries
            env = getCgEnv
        in case Rec.lookupRecordAlias (Rec._cg_fieldIndex env) fieldNames of
            Just aliasName ->
                -- Named struct: Alias_R{Name: "Alice", Age: 30}.
                -- v0.15 Stage E — parametric aliases need explicit
                -- instantiation at the literal site (Go rejects bare
                -- references to generic types).  Default to `[any, …]`
                -- per alias var; the rt.Coerce wrap at the call
                -- boundary tightens to the concrete instantiation.
                let aliasDecl = Map.lookup aliasName (Rec._cg_aliases env)
                    aliasArity = case aliasDecl of
                        Just (Can.Alias vs _) -> length vs
                        _ -> 0
                    structName =
                        if aliasArity == 0
                            then aliasName ++ "_R"
                            else aliasName ++ "_R[" ++
                                 intercalate_ ", "
                                     (replicate aliasArity "any") ++
                                 "]"
                    fieldTypeMap = case aliasDecl of
                        Just (Can.Alias _ (T.TRecord m _)) ->
                            Map.map (\(T.FieldType _ ty) -> solvedTypeToGo ty) m
                        _ -> Map.empty
                    -- v0.15 Stage C — type-directed field-init
                    -- lowering.  `exprToGoExpectGo` threads the
                    -- field's Go type DOWN into the lowering, so an
                    -- inline lambda fills a typed slot with typed
                    -- params (closing the long-standing
                    -- inline-lambda bug).  The outer
                    -- `coerceToFieldType` stays as a safety net for
                    -- non-lambda values where typed lowering didn't
                    -- propagate fully (will be retreated in Stage D).
                    lowerField fn fe =
                        let fieldGoTy = Map.findWithDefault "any" fn fieldTypeMap
                        in coerceToFieldType fieldGoTy
                               (exprToGoExpectGo fieldGoTy fe)
                in GoIr.GoStructLit structName
                    [ (capitalise_ fn, lowerField fn fe)
                    | (fn, fe) <- entries
                    ]
            Nothing ->
                -- Anonymous struct
                let fieldDecls = intercalate_ "; " (map (\(fn, _) -> capitalise_ fn ++ " any") entries)
                    fieldInits = intercalate_ ", " (map (\(fn, fe) -> capitalise_ fn ++ ": " ++ GoBuilder.renderExpr (exprToGo fe)) entries)
                in GoIr.GoRaw $ "struct{ " ++ fieldDecls ++ " }{" ++ fieldInits ++ "}"

    Can.Tuple a b more ->
        case length more of
            0 -> GoIr.GoStructLit "rt.SkyTuple2"
                    [("V0", exprToGo a), ("V1", exprToGo b)]
            1 -> GoIr.GoStructLit "rt.SkyTuple3"
                    [("V0", exprToGo a), ("V1", exprToGo b), ("V2", exprToGo (head more))]
            _ ->
                -- arity 4+: pack into SkyTupleN{Vs: []any{...}}
                let vs = a : b : more
                    vsInit = GoIr.GoSliceLit "any" (map exprToGo vs)
                in GoIr.GoStructLit "rt.SkyTupleN" [("Vs", vsInit)]


-- ═══════════════════════════════════════════════════════════
-- KERNEL FUNCTION RESOLUTION
-- ═══════════════════════════════════════════════════════════

-- | Map a kernel function to its Go equivalent
-- Zero-arity kernel functions are called immediately (Dict.empty → rt.Dict_empty())
kernelToGo :: String -> String -> GoIr.GoExpr
kernelToGo modName funcName =
    case Kernel.lookup modName funcName of
        Just ki ->
            let goExpr = if Kernel._ki_typed ki
                    then GoIr.GoIdent (Kernel._ki_goName ki ++ genericParams modName funcName)
                    else GoIr.GoIdent (Kernel._ki_goName ki)
            in if Kernel._ki_arity ki == 0
                then GoIr.GoCall goExpr []  -- zero-arity: call immediately
                else goExpr
        Nothing ->
            case (modName, funcName) of
                ("Log", "println") -> GoIr.GoQualified "rt" "Log_println"
                ("Basics", "add")  -> GoIr.GoIdent "+"
                ("Basics", "sub")  -> GoIr.GoIdent "-"
                ("Basics", "not")  -> GoIr.GoQualified "rt" "Basics_not"
                _ -> GoIr.GoQualified "rt" (modName ++ "_" ++ funcName)


-- | Get generic type parameters for a kernel function.
-- Until the type checker provides real types, use any-typed wrappers for Task functions
-- and [any, ...] type params for other generics.
genericParams :: String -> String -> String
genericParams modName funcName = case (modName, funcName) of
    -- Task functions use any-typed wrappers (don't need generic params)
    ("Task", _)  -> ""
    -- Other generic functions
    ("Result", "map")    -> "[any, any, any]"
    ("Result", "andThen") -> "[any, any, any]"
    ("Result", "withDefault") -> "[any, any]"
    ("Maybe", "map")     -> "[any, any]"
    ("Maybe", "andThen") -> "[any, any]"
    ("Maybe", "withDefault") -> "[any]"
    ("List", "map")      -> "[any, any]"
    ("List", "filter")   -> "[any]"
    ("List", "foldl")    -> "[any, any]"
    -- Basics.identity is `func[T any](x T) T` in the runtime — when
    -- referenced as a value (e.g. passed to `List.filterMap identity`)
    -- Go demands an explicit type param. `[any]` works for every
    -- call shape because the runtime helper is parametric over a
    -- single type variable.
    ("Basics", "identity") -> "[any]"
    _                    -> ""


-- | Map a constructor to Go
-- | Count the number of `->` arrows in a Forall-wrapped type — that's the
-- arity of the constructor. For `Just : a -> Maybe a` this is 1. For
-- `JobDone : Int -> Result String String -> Msg` this is 2.
-- | Coerce an expression to a target Go type for struct-field assignment.
-- When the target is `any` (or unknown), pass through. Otherwise wrap as
-- `any(expr).(TargetType)` which is safe across concrete and any-typed sources.
coerceToFieldType :: String -> GoIr.GoExpr -> GoIr.GoExpr
coerceToFieldType targetTy e
    | targetTy == "any" || null targetTy = e
    -- v0.15 Stage D — elide the wrap when the expression's IR
    -- already has the target's Go type.  After Stages C/E,
    -- `exprToGoExpectGo` produces typed values at most positions;
    -- the outer `coerceToFieldType` was unconditionally wrapping
    -- even when the inner was already typed.
    --
    -- v0.15.8 (P2): no source `Can.Expr` is in scope here — the
    -- caller has already lowered through `exprToGo`.  Caller-side
    -- variants of this site (`coerceArg`'s parametric-alias arm)
    -- DO have the source expr and pass it through.
    | goExprGoType Nothing e == Just targetTy = e
    -- Parametric container types: use the runtime's cross-instantiation
    -- coerce helpers that reconstruct the value with the target generic
    -- params. Handles SkyMaybe[any] → SkyMaybe[ErrorDetails] etc.
    | Just params <- stripParametric "rt.SkyResult" targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eraseTypeParams params ++ "]")) [e]
    | Just inner <- stripParametric "rt.SkyMaybe" targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "]")) [e]
    | isJust (stripParametric "rt.SkyTask" targetTy) =
        GoIr.GoCall (GoIr.GoIdent ("rt.TaskCoerceT[" ++ eraseTypeParams (fromMaybe "" (stripParametric "rt.SkyTask" targetTy)) ++ "]")) [e]
    -- `[]any` / `map[string]any` widening: typed source may be `[]T` /
    -- `map[string]T`. Direct assertion panics — route through the
    -- widener helpers analogous to AsListT / AsMapT.
    | targetTy == "[]any" =
        GoIr.GoCall (GoIr.GoIdent "rt.AsListAny") [e]
    | targetTy == "map[string]any" =
        GoIr.GoCall (GoIr.GoIdent "rt.AsMapAny") [e]
    -- Typed slices: runtime produces []any, so walk-and-cast via
    -- rt.AsListT[T] instead of a hard `any(v).([]T)` assertion.
    | Just elt <- stripListType targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elt ++ "]")) [e]
    -- Typed maps: same pattern for map[string]V.
    | Just valTy <- stripMapType targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valTy ++ "]")) [e]
    | otherwise =
        let erasedTy = eraseTypeParams targetTy
        in if erasedTy == "any"
             then e
             -- v0.13.x #158: record-alias targets (`_R` suffix) route
             -- through `rt.Coerce[T]` so a `map[string]any` source
             -- (Db.query rows, Firestore snapshots, JSON-decoded blobs)
             -- narrows to the typed struct via the map→struct field
             -- builder in Coerce. Other target shapes (ADT names, FFI
             -- opaque types) keep the direct assertion — they have no
             -- map-source panic class.
             else if isRecordAliasTy erasedTy
                  then GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ erasedTy ++ "]")) [e]
                  -- Function-typed targets need careful handling.
                  -- Go function types are nominal in BOTH params and
                  -- returns: a `func(P) State_Msg` is NOT assignable
                  -- to a `func(P) any` slot via `.(...)` assertion.
                  --
                  -- When the SOURCE is a `GoFuncLit` (typically the
                  -- eta-expansion lambda emitted by partial-applied
                  -- ctors; see line ~6986), the compiler KNOWS the
                  -- lambda's intended shape. If the slot's signature
                  -- can absorb the source's body (e.g. target return
                  -- = `any`; Go auto-wraps any concrete value), we
                  -- rewrite the GoFuncLit's signature DIRECTLY,
                  -- producing fully-typed Go with no runtime adapter.
                  --
                  -- For non-lambda sources, or signature shapes where
                  -- a static rewrite isn't sound, we fall through to
                  -- rt.Coerce (which uses reflect.MakeFunc via
                  -- adaptFuncValue at runtime — slower but correct).
                  else if "func(" `List.isPrefixOf` erasedTy
                  then retypeFuncLitOrCoerce erasedTy e
                  else GoIr.GoTypeAssert (GoIr.GoCall (GoIr.GoIdent "any") [e]) erasedTy


-- | Function-target coercion. When the source is a lambda literal,
-- rewrite its signature to match the target so the emitted Go is
-- fully typed end-to-end. Falls back to `rt.Coerce[func(...)]`
-- (reflect.MakeFunc) for non-literal sources.
--
-- Soundness: rewriting the lambda's `retTy` to the target's is safe
-- iff the source body's return type is ASSIGNABLE to the target's
-- return type. The two cases we exercise here:
--   1. Target return = "any" — every concrete Go type is assignable
--      to interface{}. Always safe.
--   2. Identical retTys — no rewrite needed; original lambda stands.
-- Other cases (concrete→concrete with structural diff) still need
-- the runtime adapter — fall through to rt.Coerce.
--
-- Param-type rewriting is NOT done here. The source lambda's params
-- are produced by `wrapTyped` (the partial-app eta path) which sets
-- them from the ctor's annotation — they're already typed to the
-- field's expected param shape. If a future case needs param
-- rewriting (e.g. widening Editor_Form_R to any for record-stored
-- callbacks across module boundaries), this is the place.
-- | v0.15.10 / Gap A5 helper — peel `n` curried Go function arrows
-- off `goTy`, collecting each peeled param type and returning the
-- final return type.  Each peeled slot must be `isEmittableGoType`,
-- NON-`any`, and free of generic-type-param leaks (`T1`, `T2`, …) —
-- those would prevent Go's call-site type inference from pinning
-- the call's type.  Returns Nothing if any slot fails the gate or
-- the arrow chain is shorter than `n`.
--
-- Examples:
--
--   peelTypedArrows 1 "func(int) int"
--     ==> Just (["int"], "int")
--   peelTypedArrows 2 "func(int) func(int) int"
--     ==> Just (["int", "int"], "int")
--   peelTypedArrows 2 "func(int) int"  -- arrow chain too short
--     ==> Nothing
--   peelTypedArrows 1 "func(any) int"  -- `any` slot, can't pin
--     ==> Nothing
peelTypedArrows :: Int -> String -> Maybe ([String], String)
peelTypedArrows = go []
  where
    go acc 0 ret = Just (reverse acc, ret)
    go acc n curTy = case parseFuncType curTy of
        Just ([pty], retTy)
          | isEmittableGoType pty
          , pty /= "any"
          , not (isGenericTypeParam pty)
          , not (containsGenericTypeParam pty)
          , isEmittableGoType retTy
          -> if n == 1
                 then if retTy /= "any"
                          && not (isGenericTypeParam retTy)
                          && not (containsGenericTypeParam retTy)
                        then Just (reverse (pty:acc), retTy)
                        else Nothing
                 else go (pty:acc) (n - 1) retTy
        -- Multi-param Go function: matches when Sky-side is already
        -- a single uncurried function value (e.g. an explicit Go
        -- callback `func(a, b int) int` reached through FFI).
        Just (ptys, retTy)
          | length ptys == n
          , all isEmittableGoType ptys
          , all (\p -> p /= "any"
                    && not (isGenericTypeParam p)
                    && not (containsGenericTypeParam p))
                ptys
          , isEmittableGoType retTy
          , retTy /= "any"
          , not (isGenericTypeParam retTy)
          , not (containsGenericTypeParam retTy)
          -> Just (reverse acc ++ ptys, retTy)
        _ -> Nothing


retypeFuncLitOrCoerce :: String -> GoIr.GoExpr -> GoIr.GoExpr
retypeFuncLitOrCoerce targetTy e = case parseFuncType targetTy of
    Just (_targetParams, targetRet)
      | GoIr.GoFuncLit params srcRet body <- e
      , srcRet /= targetRet
      , canStaticRetypeReturn srcRet targetRet
      -> GoIr.GoFuncLit params targetRet body
      | GoIr.GoFuncLit _ srcRet _ <- e
      , srcRet == targetRet
      -> e   -- identical: drop the wrap entirely
    _ -> GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ targetTy ++ "]")) [e]
  where
    -- target = "any" absorbs every concrete return.
    canStaticRetypeReturn :: String -> String -> Bool
    canStaticRetypeReturn _ "any" = True
    canStaticRetypeReturn _ _     = False


-- | Parse a Go function type `func(P1, P2, ...) R` into (params, ret).
-- Returns Nothing if the string doesn't conform to this exact shape.
-- Used only at the field-coercion boundary; not a general Go parser.
parseFuncType :: String -> Maybe ([String], String)
parseFuncType s = case stripPrefix' "func(" s of
    Nothing -> Nothing
    Just rest -> case spanParenBalanced rest of
        Nothing -> Nothing
        Just (paramsStr, afterParen) -> case afterParen of
            ' ':retTy -> Just (splitTopLevelCommas paramsStr, retTy)
            _         -> Nothing
  where
    stripPrefix' p str
      | p `List.isPrefixOf` str = Just (drop (length p) str)
      | otherwise               = Nothing
    -- Walk `s` accumulating chars until the matching close-paren
    -- (depth 0). Returns the inside + what's after the close-paren.
    spanParenBalanced :: String -> Maybe (String, String)
    spanParenBalanced = go 0 []
      where
        go _ _    []           = Nothing
        go 0 acc  (')':after)  = Just (reverse acc, after)
        go d acc  ('(':cs)     = go (d+1) ('(':acc) cs
        go d acc  (')':cs)     = go (d-1) (')':acc) cs
        go d acc  (c:cs)       = go d (c:acc) cs
    -- Split on top-level commas (respecting parens/brackets).
    splitTopLevelCommas :: String -> [String]
    splitTopLevelCommas = splitOn 0 []
      where
        splitOn :: Int -> String -> String -> [String]
        splitOn _ acc []           = [reverse acc | not (null acc)]
        splitOn 0 acc (',':' ':cs) = reverse acc : splitOn 0 [] cs
        splitOn 0 acc (',':cs)     = reverse acc : splitOn 0 [] cs
        splitOn d acc ('(':cs)     = splitOn (d+1) ('(':acc) cs
        splitOn d acc (')':cs)     = splitOn (d-1) (')':acc) cs
        splitOn d acc ('[':cs)     = splitOn (d+1) ('[':acc) cs
        splitOn d acc (']':cs)     = splitOn (d-1) (']':acc) cs
        splitOn d acc (c:cs)       = splitOn d (c:acc) cs


-- | True when `ty` looks like a record alias the codegen emits:
-- a Go identifier ending in `_R`, optionally module-qualified.
-- Conservative — matches Sky-generated record aliases without
-- catching arbitrary Go types that happen to share the suffix.
isRecordAliasTy :: String -> Bool
isRecordAliasTy ty =
    not (null ty)
    && "_R" `List.isSuffixOf` ty
    && all (\c -> c == '_' || c == '.' || (c >= 'A' && c <= 'Z')
                || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) ty


-- | If `ty` is a Go slice type `[]T` with T ≠ any, return Just "T".
stripListType :: String -> Maybe String
stripListType ty = case ty of
    '[':']':rest | rest /= "any" -> Just rest
    _ -> Nothing


-- | If `ty` is `map[string]V` with V ≠ any, return Just "V".
stripMapType :: String -> Maybe String
stripMapType ty =
    let prefix = "map[string]"
    in if take (length prefix) ty == prefix
        then let v = drop (length prefix) ty
             in if v /= "any" && not (null v) then Just v else Nothing
        else Nothing


-- | Is this arg `()`? Used by P7 typed-FFI migration to recognise the
-- zero-arg Sky call convention `VarKernel _ _ applied to [Unit]`.
isUnitArg :: Can.Expr -> Bool
isUnitArg (A.At _ e) = case e of
    Can.Unit -> True
    _        -> False


-- | Primitive Sky literal args — safe to pass directly to a typed FFI
-- wrapper's concrete Go param type because Go's literal inference
-- produces the matching primitive type. Used by P7 step 5b.
isPrimLiteralArg :: Can.Expr -> Bool
isPrimLiteralArg (A.At _ e) = case e of
    Can.Str _   -> True
    Can.Int _   -> True
    Can.Float _ -> True
    Can.Chr _   -> True
    _           -> False


-- | Kernel (mod, name) pairs with a typed `*T` companion whose param
-- types are all `isCallerVisibleGoType` primitives. Every entry has
-- been verified to match the runtime's typed-companion signature —
-- adding a kernel here without a matching `*T` in runtime-go/rt
-- breaks the build. Conservative list intentionally skips kernels
-- whose typed variant returns a Task-shaped thunk (caller needs to
-- execute it) or takes a slice-of-A (literal args are always scalar).
-- | Per-arg runtime coercer for each kernel that has a typed `*T`
-- companion. Each entry is (mod, fn) → list of coerce names — one
-- per arg. The coerce name is the rt.* function used to convert an
-- any-typed Sky value to the concrete primitive expected by the
-- typed kernel; the call site emits e.g. `rt.String_fromIntT(rt.AsInt(arg))`.
--
-- Coercers:
--   "AsInt" / "AsFloat" / "AsBool" — runtime primitive coercers.
--   "AsString" — fmt.Sprintf("%v", arg) wrapper. Defined here as
--     well, since rt.go uses an inline pattern.
-- | Override the default `<module>_<func>T` suffix for kernels whose
-- typed companion has a different Go name. Used when the default
-- typed companion is generic over element types (requires HM flow)
-- but an `AnyT` variant exists that preserves Sky's `any`-boxed shape
-- without needing type inference.
typedKernelAltName :: Map.Map (String, String) String
typedKernelAltName = Map.fromList
    [ (("Basics", "fst"),        "fstAnyT")
    , (("Basics", "snd"),        "sndAnyT")
    , (("Result", "withDefault"), "withDefaultAnyT")
    , (("Maybe",  "withDefault"), "withDefaultAnyT")
    , (("Dict",   "get"),         "getAnyT")
    , (("List",   "map"),         "mapAny")
    , (("List",   "filter"),      "filterAny")
    , (("List",   "head"),        "headAny")
    , (("List",   "reverse"),     "reverseAny")
    , (("List",   "take"),        "takeAnyT")
    , (("List",   "cons"),        "consAnyT")
    , (("List",   "drop"),        "dropAnyT")
    , (("List",   "foldl"),       "foldlAnyT")
    , (("List",   "foldr"),       "foldrAnyT")
    , (("List",   "filterMap"),   "filterMapAnyT")
    , (("List",   "concatMap"),   "concatMapAnyT")
    , (("List",   "any"),         "anyAnyT")
    , (("List",   "all"),         "allAnyT")
    , (("Result", "map"),         "mapAnyT")
    , (("Result", "andThen"),     "andThenAnyT")
    , (("Result", "mapError"),    "mapErrorAnyT")
    , (("Maybe",  "map"),         "mapAnyT")
    , (("Maybe",  "andThen"),     "andThenAnyT")
    ]


typedKernelArgCoerce :: Map.Map (String, String) [String]
typedKernelArgCoerce = Map.fromList
    -- Single-arg int → string
    [ (("String", "fromInt"),    ["AsInt"])
    , (("String", "fromFloat"),  ["AsFloat"])
    , (("String", "fromChar"),   ["AsInt"])
    -- String → X
    , (("String", "toUpper"),    ["AsString"])
    , (("String", "toLower"),    ["AsString"])
    , (("String", "trim"),       ["AsString"])
    , (("String", "reverse"),    ["AsString"])
    , (("String", "isEmpty"),    ["AsString"])
    , (("String", "length"),     ["AsString"])
    -- (String, String) → X
    , (("String", "contains"),   ["AsString", "AsString"])
    , (("String", "startsWith"), ["AsString", "AsString"])
    , (("String", "endsWith"),   ["AsString", "AsString"])
    , (("String", "append"),     ["AsString", "AsString"])
    , (("String", "split"),      ["AsString", "AsString"])
    , (("String", "slice"),      ["AsInt", "AsInt", "AsString"])
    , (("String", "replace"),    ["AsString", "AsString", "AsString"])
    -- Math
    , (("Math",   "abs"),  ["AsInt"])
    , (("Math",   "min"),  ["AsInt", "AsInt"])
    , (("Math",   "max"),  ["AsInt", "AsInt"])
    , (("Math",   "sqrt"), ["AsFloat"])
    , (("Math",   "pow"),  ["AsFloat", "AsFloat"])
    , (("Math",   "floor"),["AsFloat"])
    , (("Math",   "ceil"), ["AsFloat"])
    , (("Math",   "round"),["AsFloat"])
    , (("Math",   "sin"),  ["AsFloat"])
    , (("Math",   "cos"),  ["AsFloat"])
    , (("Math",   "tan"),  ["AsFloat"])
    , (("Math",   "log"),  ["AsFloat"])
    -- Char (ints used as runes — Sky's Char is rune)
    , (("Char",   "isUpper"),  ["AsInt"])
    , (("Char",   "isLower"),  ["AsInt"])
    , (("Char",   "isDigit"),  ["AsInt"])
    , (("Char",   "isAlpha"),  ["AsInt"])
    , (("Char",   "toUpper"),  ["AsInt"])
    , (("Char",   "toLower"),  ["AsInt"])
    -- Path / Encoding / Regex (single-string args)
    , (("Path",   "dir"),        ["AsString"])
    , (("Path",   "base"),       ["AsString"])
    , (("Path",   "ext"),        ["AsString"])
    , (("Path",   "isAbsolute"), ["AsString"])
    , (("Encoding", "base64Encode"), ["AsString"])
    , (("Encoding", "base64Decode"), ["AsString"])
    , (("Encoding", "urlEncode"),    ["AsString"])
    , (("Encoding", "urlDecode"),    ["AsString"])
    , (("Encoding", "hexEncode"),    ["AsString"])
    , (("Encoding", "hexDecode"),    ["AsString"])
    , (("Regex",  "match"),   ["AsString", "AsString"])
    , (("Regex",  "find"),    ["AsString", "AsString"])
    -- List (single-list arg): dispatch to typed companions that
    -- accept []any. Sky's List elements are always erased to any
    -- at runtime, so `rt.List_lengthT(rt.AsList(xs))` is exactly
    -- the typed shape — Go infers A = any from AsList's return.
    , (("List",   "length"),  ["AsList"])
    , (("List",   "head"),    ["Pass"])
    , (("List",   "reverse"), ["Pass"])
    , (("List",   "isEmpty"), ["AsList"])
    -- Dict: keys/values return []any (updated in rt.go) so they
    -- compose with Sky List ops without []string/[]V mismatch.
    , (("Dict",   "member"),  ["AsString", "AsDict"])
    , (("Dict",   "insert"),  ["AsString", "Pass", "AsDict"])
    , (("Dict",   "keys"),    ["AsDict"])
    , (("Dict",   "values"),  ["AsDict"])
    , (("Dict",   "get"),     ["Pass", "Pass"])
    -- (Html / Attr / Css kernel-call hints removed in v0.13 Layer 3 —
    -- Std.Html* and Std.Css are Sky-source modules now; their calls
    -- resolve to VarTopLevel, so these hints never fired.)
    -- Log.println: single-arg, any → struct{}{}. Very high-frequency.
    , (("Log",    "println"), ["Pass"])
    , (("Server", "html"),    ["AsString"])
    , (("Server", "redirect"),["AsString"])
    -- List generic helpers: Pass the fn closure, AsList the slice.
    , (("List",   "map"),     ["Pass", "Pass"])
    , (("List",   "filter"),  ["Pass", "Pass"])
    , (("List",   "take"),    ["AsInt", "AsList"])
    , (("List",   "drop"),    ["AsInt", "AsList"])
    , (("List",   "cons"),    ["Pass", "AsList"])
    , (("List",   "foldl"),   ["Pass", "Pass", "AsList"])
    , (("List",   "foldr"),   ["Pass", "Pass", "AsList"])
    , (("List",   "filterMap"), ["Pass", "AsList"])
    , (("List",   "concatMap"), ["Pass", "AsList"])
    , (("List",   "any"),     ["Pass", "AsList"])
    , (("List",   "all"),     ["Pass", "AsList"])
    , (("Result", "map"),     ["Pass", "Pass"])
    , (("Result", "andThen"), ["Pass", "Pass"])
    , (("Result", "mapError"),["Pass", "Pass"])
    , (("Maybe",  "map"),     ["Pass", "Pass"])
    , (("Maybe",  "andThen"), ["Pass", "Pass"])
    -- Basics: pure boolean / integer helpers
    , (("Basics", "not"),     ["AsBool"])
    , (("Basics", "modBy"),   ["AsInt", "AsInt"])
    , (("Basics", "errorToString"), ["Pass"])
    -- Basics.fst/snd: dispatch to fstAnyT/sndAnyT via typedKernelAltName;
    -- they preserve the any-boxed element type without requiring HM flow.
    , (("Basics", "fst"),     ["AsTuple2"])
    , (("Basics", "snd"),     ["AsTuple2"])
    , (("Basics", "identity"), ["Pass"])
    , (("Result", "withDefault"), ["Pass", "Pass"])
    , (("Maybe",  "withDefault"), ["Pass", "Pass"])
    -- Time formatters: Int → String
    , (("Time",   "formatISO8601"), ["AsInt"])
    , (("Time",   "formatRFC3339"), ["AsInt"])
    , (("Time",   "formatHTTP"),    ["AsInt"])
    ]


-- | Render a single arg coerced through the named runtime helper.
-- "AsString" is special-cased as `fmt.Sprintf("%v", arg)` to match
-- the existing convention; AsInt / AsFloat / AsBool are direct
-- rt.* calls. Literals pass through as-is.
coerceTypedKernelArg :: String -> Can.Expr -> GoIr.GoExpr
coerceTypedKernelArg coercer arg
    | isPrimLiteralArg arg = exprToGo arg
    -- "Pass" erases the arg's concrete type via `any(arg)` so Go
    -- generic inference picks V=any uniformly (e.g. Dict_insertT[V]
    -- where the dict side came in as map[string]any via AsDict).
    | coercer == "Pass" = GoIr.GoCall (GoIr.GoIdent "any") [exprToGo arg]
    | otherwise =
        GoIr.GoCall (GoIr.GoQualified "rt" coercer) [exprToGo arg]


typedKernelLiterals :: Set.Set (String, String)
typedKernelLiterals = Set.fromList
    [ ("String", "toUpper"),    ("String", "toLower"),    ("String", "trim")
    , ("String", "reverse"),    ("String", "isEmpty"),    ("String", "length")
    , ("String", "contains"),   ("String", "startsWith"), ("String", "endsWith")
    , ("String", "append"),     ("String", "fromInt"),    ("String", "fromFloat")
    , ("String", "replace"),    ("String", "slice")
    , ("Math",   "abs"),        ("Math",   "min"),        ("Math",   "max")
    , ("Math",   "sqrt"),       ("Math",   "pow"),        ("Math",   "floor")
    , ("Math",   "ceil"),       ("Math",   "round"),      ("Math",   "sin")
    , ("Math",   "cos"),        ("Math",   "tan"),        ("Math",   "log")
    , ("Char",   "isUpper"),    ("Char",   "isLower"),    ("Char",   "isDigit")
    , ("Char",   "isAlpha"),    ("Char",   "toUpper"),    ("Char",   "toLower")
    , ("Path",   "dir"),        ("Path",   "base"),       ("Path",   "ext")
    , ("Path",   "isAbsolute")
    , ("Encoding", "base64Encode"), ("Encoding", "base64Decode")
    , ("Encoding", "urlEncode"),    ("Encoding", "urlDecode")
    , ("Encoding", "hexEncode"),    ("Encoding", "hexDecode")
    , ("Regex",  "match"),      ("Regex",  "find"),       ("Regex",  "replace")
    , ("List",   "length"),     ("List",   "head"),       ("List",   "reverse")
    , ("List",   "isEmpty")
    , ("Dict",   "member"),     ("Dict",   "insert")
    , ("Dict",   "keys"),       ("Dict",   "values"),   ("Dict", "get")
    -- (Html / Attr / Css removed v0.13 Layer 3 — Sky-source modules.)
    , ("Log",    "println")
    , ("Server", "html"),      ("Server", "redirect")
    , ("List",   "map"),       ("List",   "filter"),     ("List", "take"), ("List", "cons")
    , ("List",   "drop"),      ("List",   "foldl"),      ("List", "foldr")
    , ("List",   "filterMap"), ("List",   "concatMap"),  ("List", "any"), ("List", "all")
    , ("Result", "map"),       ("Result", "andThen"),    ("Result", "mapError")
    , ("Maybe",  "map"),       ("Maybe",  "andThen")
    , ("Basics", "not"),        ("Basics", "modBy"),  ("Basics", "errorToString")
    , ("Time",   "formatISO8601"), ("Time", "formatRFC3339"), ("Time", "formatHTTP")
    , ("Basics", "fst"),        ("Basics", "snd"),   ("Basics", "identity")
    , ("Result", "withDefault"), ("Maybe",  "withDefault")
    ]


-- | Snapshot of Env.ffiTypedWrapperNamesRef taken at every lookup. The
-- unsafePerformIO is fine here: the set is populated once at compile
-- start (before canonicalisation runs) and never mutated afterwards.
{-# NOINLINE typedFfiWrapperSet #-}
typedFfiWrapperSet :: Set.Set String
typedFfiWrapperSet = unsafePerformIO (readIORef Env.ffiTypedWrapperNamesRef)


-- | Companion snapshot of typed-wrapper param Go types, keyed by the
-- T-suffix wrapper name. See typedFfiWrapperSet for the invariant.
{-# NOINLINE typedFfiWrapperParams #-}
typedFfiWrapperParams :: Map.Map String [String]
typedFfiWrapperParams = unsafePerformIO (readIORef Env.ffiTypedWrapperParamsRef)


-- | Typed-wrapper param types that the sky-out/main.go call site can
-- actually reference. Typed wrappers reference file-local aliases
-- (`pkg`, `stripe_go`, etc.) that don't exist in main.go — so we can
-- only migrate N-arg calls whose param types are expressible without
-- those file-local aliases. Safe: Go primitives. Unsafe: any
-- dot-qualified type. Future work: record the main.go-visible import
-- aliases as part of the FFI registry so more types become callable.
isCallerVisibleGoType :: String -> Bool
isCallerVisibleGoType t =
    -- `interface{}` and `any` are Go's empty interface — main.go can
    -- always use them. Treat the raw string match (not the bare-strip)
    -- since dropping `*` from `*interface{}` gives nonsense.
    if t == "interface{}" || t == "any" then True
    else
      let bare = dropWhile (\c -> c == '*' || c == '[' || c == ']' || c == ' ') t
      in bare `elem`
          [ "string", "int", "int8", "int16", "int32", "int64"
          , "uint", "uint8", "uint16", "uint32", "uint64"
          , "float32", "float64", "bool", "byte", "rune", "error"
          ]


-- | Coerce a Sky arg to a concrete Go type at a typed FFI call site.
-- Literal args (Str/Int/Float/Chr) render to native Go literals that
-- Go's type inference matches to the target param type directly —
-- inserting `any(1).(int)` would actually break, since `any(1)` boxes
-- the literal and asserting back loses the native-type view. For
-- everything else, route through `rt.Coerce[T]()` which handles
-- the full matrix: direct type assertion when the runtime type
-- matches, reflect-based numeric widening, and (post-skyshop-fix)
-- slice coercion (`[]any` → `[]ConcreteT`) so an empty list `[]`
-- in Sky can flow into a Go function expecting `[]option.ClientOption`.
coerceFfiArg :: String -> Can.Expr -> GoIr.GoExpr
coerceFfiArg goType arg =
    let goArg = exprToGo arg
    in if isPrimLiteralArg arg
        then goArg
        else coerceVia goType goArg


-- | Call-site coercion that consults the `rt.FfiT_<Name>_P<N>` alias
-- when the target param type isn't a caller-visible primitive. FfiGen
-- emits those aliases alongside every typed wrapper whose params or
-- return reference an FFI-file-local type, so main.go can cast
-- through them without needing the underlying Go package import.
coerceFfiArgViaAlias :: String -> Int -> String -> Can.Expr -> GoIr.GoExpr
coerceFfiArgViaAlias anyWrapperName idx goType arg
    | isPrimLiteralArg arg   = exprToGo arg
    | isCallerVisibleGoType goType =
        coerceVia goType (exprToGo arg)
    | otherwise =
        let aliasName = "rt.FfiT_" ++ anyWrapperName ++ "_P" ++ show idx
        in coerceVia aliasName (exprToGo arg)


-- | Emit `rt.Coerce[T](expr)` (or the named shortcut for prim
-- types) so typed FFI boundaries handle representation mismatches
-- ([]any → []ConcreteT, struct reinterpret, numeric widening)
-- instead of panicking on a raw `.(T)` assertion.
--
-- For Sky-side container shapes (SkyMaybe / SkyResult / typed slice
-- / typed string-keyed map) we route through the lossless
-- reconstructor helpers (MaybeCoerce / ResultCoerce / AsListT /
-- AsMapT). They re-wrap the source losslessly across any source
-- shape — including a polymorphic Nothing[any]() or empty
-- []any{} — so the strict rt.Coerce panic can't fire on a
-- structurally-compatible source.
coerceVia :: String -> GoIr.GoExpr -> GoIr.GoExpr
coerceVia goType goArg = case goType of
    "string"  -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceString") [goArg]
    "int"     -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceInt") [goArg]
    "bool"    -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceBool") [goArg]
    "float64" -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceFloat") [goArg]
    _ -> case stripSkyMaybe goType of
        Just inner -> GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [goArg]
        Nothing -> case stripSkyResult goType of
            Just (eGo, aGo) -> GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eGo ++ ", " ++ aGo ++ "]")) [goArg]
            Nothing -> case stripSlice goType of
                Just elemGo -> GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elemGo ++ "]")) [goArg]
                Nothing -> case stripStringMap goType of
                    Just valGo -> GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valGo ++ "]")) [goArg]
                    Nothing -> GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ goType ++ "]")) [goArg]


-- | Can we emit a direct Go call for this callee expression?
-- Direct: kernel funcs, ADT constructors, top-level funcs (all are real Go funcs).
-- Indirect (wrap with rt.SkyCall): local vars, field accesses, expression results —
-- these are any-typed at runtime and Go forbids calling them directly.
isDirectCallable :: Can.Expr -> Bool
isDirectCallable (A.At _ e) = case e of
    Can.VarKernel _ _      -> True
    Can.VarCtor{}          -> True
    Can.VarTopLevel _ _    -> True
    Can.Lambda _ _         -> True
    _                      -> False


-- | Per-argument Go types for a constructor, derived from its
-- canonical annotation. Uses safeReturnType (env-aware so record
-- aliases resolve). Missing slots degrade to "any".
ctorParamTypes :: Can.Annotation -> [String]
ctorParamTypes (Can.Forall _ t) = go t
  where
    go (T.TLambda from to) = safeReturnType from : go to
    go _                   = []

ctorArity :: Can.Annotation -> Int
ctorArity (Can.Forall _ t) = countArrows t
  where
    countArrows (T.TLambda _ r) = 1 + countArrows r
    countArrows _ = 0


-- | Emit a lambda that supplies the already-collected args then takes the
-- remaining `missing` args one at a time and calls the constructor.
emitPartialCtor :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitPartialCtor func suppliedArgs missing =
    let -- T1 partial-app coercion: recover the ctor's declared param
        -- types from its annotation so both already-supplied args and
        -- the closure-captured extras coerce to the right Go types.
        paramTys = case A.toValue func of
            Can.VarCtor _ _ _ _ annot -> ctorParamTypes annot
            _                         -> []
        -- v0.13 Stage 1 — ctor's final return type (the ADT/record
        -- name) — used as the typed lambda return so partial-app
        -- closures carry the right Go shape at HOF slots.
        ctorRetTy = case A.toValue func of
            Can.VarCtor _ _ typeName _ annot ->
                let (_, r) = peelArgs (skyAnnotType annot)
                in case r of
                    _ | safeReturnType r /= "any" -> safeReturnType r
                    _ -> typeName
            _ -> "any"
        suppliedTys = take (length suppliedArgs) paramTys
        extraTys    = drop (length suppliedArgs) paramTys
                   ++ replicate missing "any"
        -- Sanitise: ctor decls erase TVars/anonymous-record names —
        -- use "any" for any slot whose Go-type string would contain
        -- a generic placeholder or synthesised anon name (which has
        -- no Go alias).
        sanitisedExtras = map (\t -> if containsGenericTypeParam t
                                        then "any" else t) extraTys
        sanitisedRet = if containsGenericTypeParam ctorRetTy
                          then "any" else ctorRetTy
        -- v0.15.2: typed-target call args via `zipWithDefaultExpect`
        -- (see note at the helper definition near the bottom of this
        -- file).  Required for `Editor.view editorCfg` and similar
        -- parametric-record passing on partial-applied ctors.
        suppliedGo  = zipWithDefaultExpect suppliedTys suppliedArgs
        extraNames  = [ "__p" ++ show i | i <- [0 .. missing - 1] ]
        extraIdents = zipWith (\n ty -> coerceArg Nothing (GoIr.GoIdent n) ty)
                              extraNames sanitisedExtras
        finalCall = GoIr.GoCall (exprToGo func) (suppliedGo ++ extraIdents)
        -- Wrap outer-first (last extra wrapped first) so the chain is
        -- func(extraN-1) func(...) ... func(extra0) Ret.
        -- Build from innermost up. innermost return type = ctorRetTy.
        -- Each wrap goes from `Ret` to `func(Tn) Ret` to
        -- `func(Tn-1) func(Tn) Ret` etc.
        wrapTyped :: GoIr.GoExpr -> String -> [(String, String)] -> GoIr.GoExpr
        wrapTyped innerBody _ [] = innerBody
        wrapTyped innerBody innerRet ((n, ty):rest) =
            let lam = GoIr.GoFuncLit
                        [GoIr.GoParam n ty]
                        innerRet
                        [GoIr.GoReturn innerBody]
                outerRet = "func(" ++ ty ++ ") " ++ innerRet
            in wrapTyped lam outerRet rest
        -- Pair each lambda param name with its typed shape, reversed
        -- so wrapTyped builds inner-to-outer.
        nameTypePairs = reverse (zip extraNames sanitisedExtras)
    in wrapTyped finalCall sanitisedRet nameTypePairs
  where
    skyAnnotType (Can.Forall _ t) = t
    peelArgs (T.TLambda a r) =
        let (as, ret) = peelArgs r in (a : as, ret)
    peelArgs t = ([], t)


-- | Partial application of a user-defined top-level function: wrap the
-- call in a chain of `func(x any) any { return callee(... , x, ...) }`
-- lambdas binding the remaining parameters.
-- | T2/T6 helper. For a known top-level callee, look up its expected
-- Go param types and emit each arg with the right coercion. When a
-- param type is not registered (callee is `any`-typed), pass the arg
-- through unchanged. The `any(arg).(T)` form works whether `arg` is
-- already typed `T` (redundant assertion) or `any` (real coercion).
coerceCallArgs :: String -> [Can.Expr] -> [GoIr.GoExpr]
coerceCallArgs qualName args =
    let env = getCgEnv
        paramTypes = Map.findWithDefault [] qualName (Rec._cg_funcParamTypes env)
    in if null paramTypes
         then map exprToGo args
         else
             -- v0.13 Stage 1 — same recovery σ pattern as
             -- `coerceCallArgsAt`: pin TVars from typed arg sides;
             -- only erase un-pinned TVars. Critical for recursive
             -- calls in Sky-source kernel bodies (`Sky_Core_List_map_`'s
             -- `map fn rest` where fn has typed `func(T1) T2` sig).
             let goArgs = map exprToGo args
                 -- v0.15.8 (P2): σ-recovery stays strict — passes
                 -- Nothing.  See the rationale comment in the
                 -- VarKernel branch of `coerceCallArgsAt`.
                 bareRecovered = Map.fromList
                     [ (pty, cgo)
                     | (pty, ga) <- zip paramTypes goArgs
                     , isGenericTypeParam pty
                     , Just cgo <- [goExprGoType Nothing ga]
                     , cgo /= "any"
                     , not (isGenericTypeParam cgo)
                     ]
                 structuralRecovered = Map.unions
                     [ unifyGoTypes pty cgo
                     | (pty, ga) <- zip paramTypes goArgs
                     , not (isGenericTypeParam pty)
                     , containsGenericTypeParam pty
                     , Just cgo <- [goExprGoType Nothing ga]
                     , cgo /= "any"
                     ]
                 recovered = Map.union bareRecovered structuralRecovered
                 substituteOnly pty =
                     let subbed = substTVarsInGoType recovered pty
                         unboundTVars =
                             [ t | t <- tvarsInGoTypeStr subbed
                                 , not (Map.member t recovered) ]
                     in if null unboundTVars
                          then subbed
                          else if containsGenericTypeParam subbed
                                 then eraseTypeParams subbed
                                 else subbed
                 substituted = map substituteOnly paramTypes
             -- v0.15.2: typed-target call args via `zipWithDefaultExpect`.
             in zipWithDefaultExpect substituted args


-- | v0.13 Phase A5 — call-site-aware variant of `coerceCallArgs`.
-- When the call site has a captured monomorphisation instance,
-- substitute the callee's generic type parameters (`T1`, `T2`,
-- …) with the instance's concrete Go types before calling
-- `coerceArg`.  This produces correctly-typed coercion wrappers
-- (`rt.MaybeCoerce[string]` instead of `rt.MaybeCoerce[any]`)
-- so Go's type inference at the call site reconciles consistently.
--
-- Falls back to the un-substituted `coerceCallArgs` when no
-- instance is captured at this call site (FFI boundary, non-
-- polymorphic call, solver had a free TVar, etc.).
-- | v0.13 Phase A4: at a polymorphic Sky-source call site, return
-- the mangled name of the specialised instance emitted for this
-- call.  Returns Nothing when no spec exists (kernel / FFI / non-
-- generic / unresolved instance).
--
-- The mangled name matches the one emitted by `Mono.specialiseFuncDecl`
-- in `generateGoMulti`'s `specDecls` block — both derive it from
-- `Mono.mangleInstance (CallInstance qualName tys _)` where `tys`
-- comes from the captured `CallInstance` substituted by the outer
-- function's σ.
--
-- For this MVP we only do a SHALLOW substitution: σ_outer is empty
-- (we're at the entry-module call site).  Cross-instance σ
-- propagation (recursive calls inside specialised bodies) is a
-- follow-up; for now those still use the generic name.
instanceMangledName :: A.Region -> String -> Maybe String
instanceMangledName region qualName = unsafePerformIO $ do
    env <- readIORef globalCgEnv
    reached <- readIORef globalReachableSet
    _annotMap' <- readIORef globalAnnotMap
    let siteKey = ( A._line (A._start region)
                  , A._col  (A._start region) )
        skyForm = unmangleQual qualName
        nested = Map.lookup siteKey (Rec._cg_callSiteInstances env)
    case nested >>= Map.lookup skyForm of
        Just (Solve.CallInstance _ tys quantsCap) | not (null tys) ->
            let instance_ = (skyForm, tys)
                mangled = Mono.mangleInstance
                    (Solve.CallInstance skyForm tys [])
                -- Check inline whether a spec would be emitted:
                -- needs non-empty σ_go.  Uses the same logic as
                -- the spec emission step in generateGoMulti so the
                -- two stay in sync regardless of evaluation order.
                skyToGo = Map.findWithDefault [] qualName
                    (Rec._cg_funcSkyToGoTVars env)
                σ_sky_keys = Set.fromList (zip quantsCap (map (const ()) tys))
                hasGoSubst = any (\(sn, _) ->
                    Set.member (sn, ()) σ_sky_keys) skyToGo
            in if Set.member instance_ reached && hasGoSubst
                then return (Just mangled)
                else return Nothing
        _ -> return Nothing


-- | Reverse `mangleQualName`: turn `"Sky_Core_Maybe_withDefault"`
-- back into `"Sky.Core.Maybe.withDefault"`.  This is heuristic — it
-- replaces every `_` with `.`, which is wrong for Sky names that
-- naturally contain underscores (none in stdlib today, and goSafeName
-- adds a trailing `_` for Go-keyword collisions that we strip).
-- For v0.13's stdlib surface this works; full round-trip needs the
-- Compile pipeline to thread Sky-form qualNames alongside Go names.
unmangleQual :: String -> String
unmangleQual = map (\c -> if c == '_' then '.' else c)


coerceCallArgsAt :: A.Region -> String -> [Can.Expr] -> [GoIr.GoExpr]
coerceCallArgsAt region qualName args =
    let env = getCgEnv
        paramTypes = Map.findWithDefault [] qualName (Rec._cg_funcParamTypes env)
        siteKey = ( A._line (A._start region)
                  , A._col  (A._start region) )
        skyForm = unmangleQual qualName
        instM = Map.lookup siteKey (Rec._cg_callSiteInstances env)
                  >>= Map.lookup skyForm
        skyToGo = Map.findWithDefault [] qualName
                    (Rec._cg_funcSkyToGoTVars env)
    in case (paramTypes, instM) of
        ([], _) -> map exprToGo args
        (_, Just (Solve.CallInstance _ concreteTys quants))
            | length quants == length concreteTys
            , not (null skyToGo) ->
                -- v0.13 Phase A5+: build σ in Sky-name space first
                -- (zip annotation Forall names with concrete types),
                -- then project to Go-name space via the function's
                -- skyToGo mapping.  Annotation positions whose Sky-
                -- name doesn't appear in skyToGo were defaulted at
                -- codegen time (e.g. error-position TVar collapsed
                -- to `Sky_Core_Error_Error`) — skip them; their slot
                -- in the Go sig is already concrete.
                --
                -- Sanitise the projected Go-type strings via
                -- `sanitiseTypedDeep`: if a concrete type contains
                -- a non-emittable token (`Anon_R_xxx` synthesised
                -- record name with no Go alias counterpart, or an
                -- ambiguous-resolution sentinel), substitute that
                -- subtree with `any` so the rest of the call's
                -- type-arg map still reconciles.  Mirrors the
                -- existing `sanitiseTypedElem` filter on the kernel-
                -- routed path.
                let skyToConcrete = Map.fromList (zip quants concreteTys)
                    σ = Map.fromList
                          [ (goName, sanitiseTypedDeep (solvedTypeToGo cty))
                          | (skyName, goName) <- skyToGo
                          , Just cty <- [Map.lookup skyName skyToConcrete]
                          ]
                    substituted = map (substTVarsInGoType σ) paramTypes
                    -- When a substituted param type is exactly "any"
                    -- because the call-site instance normalised this
                    -- TVar to `any` (partial-resolution — surrounding
                    -- function is itself polymorphic in this param),
                    -- the raw value's Go static type would conflict
                    -- with Go's generic-inference across other arg
                    -- positions.  Example: Result.withDefault [] r
                    -- where σ={a→any}.  The def slot's substituted
                    -- type is "any" — passing `[]any{}` raw gives Go
                    -- static type `[]any`, then the second arg's
                    -- `ResultCoerce[Error, any]` conflicts with
                    -- `T1=[]any` inferred from def.  Force `any(e)`
                    -- widening so Go infers T1=any consistently
                    -- across all positions.
                    --
                    -- Only fires when the ORIGINAL param mentioned a
                    -- generic placeholder that got substituted away
                    -- (`containsTypeParam orig`).  Non-generic
                    -- `any`-typed params (untyped boundary calls)
                    -- pass through unchanged.
                    -- v0.13 Phase A4: typed lambda emission at
                    -- Sky-source HOF call sites.  When an arg is a
                    -- literal `Can.Lambda` AND the substituted param
                    -- type is `func(X) Y`-shaped, emit the lambda
                    -- via `curryLambdaPatTyped` with X as the input
                    -- type.  This makes Sky lambdas concrete-typed
                    -- at the call boundary so Go's type checker
                    -- accepts them at typed-param positions
                    -- (Sky_Core_Maybe_andThen__String_Int's `fn`
                    -- param wants `func(string) any` — a
                    -- `func(any) any` lambda fails Go's no-function-
                    -- covariance rule).
                    coerceOne orig subbed e@(A.At _ inner) =
                        case inner of
                            Can.Lambda pats body
                                | all isSimpleVarPattern pats
                                , (inputTypes, finalRet) <-
                                    splitCurriedFuncTypeStr (length pats) subbed
                                , length inputTypes == length pats
                                , length inputTypes > 0 ->
                                    -- v0.13 typed lowerer: push the
                                    -- lambda's typed inputs into the
                                    -- local-type context so the body's
                                    -- binops / var refs resolve to typed
                                    -- Go-native forms (no `rt.Add` on
                                    -- Int+Int; no `any(x).(string)`
                                    -- on a typed local).
                                    --
                                    -- When the lambda's final return
                                    -- type is a real emittable Go type,
                                    -- lower the BODY via
                                    -- `exprToGoExpectGo` too — a
                                    -- case/if/let body emits
                                    -- `func() <finalRet>` directly
                                    -- instead of `rt.AsX(func() any
                                    -- {…}())`.  `curryLambdaPatTypedPre`
                                    -- then skips the redundant
                                    -- innermost `wrapRet`.
                                    let skyTys = map goTypeStrToSkyType inputTypes
                                        bindings = patVarTypes pats skyTys
                                        bodyPreTyped = isEmittableGoType finalRet
                                        rawBody =
                                            if bodyPreTyped
                                                then exprToGoExpectGo finalRet body
                                                else exprToGo body
                                        body' = withScopedLambdaTypes bindings rawBody
                                    in if bodyPreTyped
                                        then curryLambdaPatTypedPre inputTypes finalRet
                                                pats body'
                                        else curryLambdaPatTyped inputTypes finalRet
                                                pats body'
                            -- v0.13 typed lowerer: control-flow args
                            -- (case / if / let) at a typed param slot
                            -- lower via `exprToGoExpectGo` so the IIFE
                            -- is `func() <subbed>` directly — no
                            -- call-site `rt.Coerce[subbed]` wrap.
                            -- Gated on `subbed` being a real emittable
                            -- Go type (not `any`, not an un-nameable
                            -- anon-record); otherwise fall through to
                            -- the `coerceArg` path which still applies
                            -- the runtime coercion.
                            --
                            -- EXCLUDES Go generic type params (`T1`,
                            -- …): those name the CALLEE's type
                            -- variables, which are NOT in scope at
                            -- this (the caller's) site — threading
                            -- `func() T1` here would emit an undefined
                            -- identifier.  `goZeroValue` reports type
                            -- params as emittable for the function-
                            -- BODY emit path (where they ARE in
                            -- scope), so the explicit exclusion is
                            -- required here.
                            Can.Case{}
                                | isEmittableGoType subbed
                                , not (isGenericTypeParam subbed) ->
                                    exprToGoExpectGo subbed e
                            Can.If{}
                                | isEmittableGoType subbed
                                , not (isGenericTypeParam subbed) ->
                                    exprToGoExpectGo subbed e
                            Can.Let{}
                                | isEmittableGoType subbed
                                , not (isGenericTypeParam subbed) ->
                                    exprToGoExpectGo subbed e
                            -- v0.15.2: Can.Record at a parametric-alias
                            -- monomorphisation slot (`Cfg_R[Msg]`).  Route
                            -- to `exprToGoExpectGo` so the literal emits
                            -- with the target's type args directly,
                            -- avoiding the `Cfg_R[any]{...}.(Cfg_R[Msg])`
                            -- nominal-type-assert panic that surfaced as
                            -- "interface conversion: Cfg_R[interface {}]
                            -- vs Cfg_R[State_Msg]" on every editor mount
                            -- in skydeploy.  Stage E handled this for
                            -- record-field-init contexts but missed the
                            -- monomorphisation call-arg path.
                            --
                            -- Gated on `not containsGenericTypeParam`:
                            -- when the target's type args are still bare
                            -- type params (`Cfg_R[T1]` for a Sky-side
                            -- polymorphic call where σ didn't pin the
                            -- TVar), emitting `Cfg_R[T1]{...}` at the
                            -- caller's site triggers `undefined: T1` in
                            -- `go build` — T1 names the CALLEE's type
                            -- variable, not in scope at the call site.
                            Can.Record{}
                                | isParametricAliasInstantiation subbed
                                , not (containsGenericTypeParam subbed) ->
                                    exprToGoExpectGo subbed e
                            _ ->
                                if subbed == "any" && containsTypeParam orig
                                    then GoIr.GoCall (GoIr.GoIdent "any") [exprToGo e]
                                    else coerceArg (Just e) (exprToGo e) subbed
                in zipWith3Default coerceOne paramTypes substituted args
        _ ->
            -- v0.13 Phase A5+: when no CSI is captured at this call
            -- site (e.g. the call lives inside a `Can.Update` field
            -- whose constraint emission is deferred and so the
            -- CForeign never fires, or — v0.13 Layer 3 — the call
            -- sits inside a lambda body whose instances aren't
            -- captured yet), every Go-generic placeholder
            -- (`T1`, `T2`, …) in the callee's declared paramTypes
            -- would leak into the call site as a bare identifier
            -- and `go build` rejects with `undefined: T1`.  Erase
            -- TVar placeholders to `any` in the fallback path so
            -- coerceArg routes through `rt.AsListAny` /
            -- `rt.Coerce[func(any) any]` /
            -- `rt.ResultCoerce[..., any]` etc. instead.  The
            -- value's static Go type widens at the boundary;
            -- correctness is preserved (the callee accepts the
            -- widened type via its generic instantiation).
            --
            -- v0.13 Layer 3 fix: erasing `T1 → any` inside a COMPOUND
            -- param (`rt.SkyMaybe[T1]` → `rt.SkyMaybe[any]`) while
            -- leaving a sibling BARE-`T1` param's arg un-widened is
            -- unsound: `coerceArg e "any"` passes the bare arg RAW, so
            -- Go infers the callee's type param from its real static
            -- type and the compound arg's `rt.SkyMaybe[any]` then
            -- clashes with the inferred `rt.SkyMaybe[int]`.
            --
            -- The PROPER fix (not a blanket `any`-widen): RECOVER the
            -- callee's type-param substitution from any arg whose Go
            -- type is statically derivable — exactly what Go's own
            -- inference does.  `Result.withDefault "" r`: arg0 lowers
            -- to a Go string literal, so `T1 = string`; the `Result e
            -- T1` arg then coerces to `rt.SkyResult[e, string]` (typed)
            -- and arg0 stays the bare `""` (no `any(...)` wrap).  Only
            -- type params that NO arg can pin fall back to the `any`-
            -- widen — and there it's applied to EVERY position
            -- mentioning that param, so Go infers it = any uniformly.
            let goArgs = map exprToGo args
                -- partial σ: bare-type-param ↦ concrete Go type, for
                -- every position whose arg has a known static type.
                --
                -- v0.13 Stage 1+2 — recover from STRUCTURAL matches
                -- too: `paramType = "[]T1"` against arg type
                -- `"[]State_Post_R"` deduces T1 = State_Post_R;
                -- `paramType = "func(T1) T2"` against arg type
                -- `"func(State_Post_R) State_Post_R"` deduces both.
                -- Without this, the call site widens the lambda to
                -- `func(any) any` and the list to `[]any`,
                -- breaking Go's typed-generic-instantiation path
                -- (both args must be unwidened for the kernel's
                -- `Sky_Core_List_map_[T1, T2]` inference to pick
                -- concrete T1/T2).
                -- v0.15.8 (P2): σ-recovery stays strict — passes
                -- Nothing.  See the rationale comment in the
                -- VarKernel branch of `coerceCallArgsAt`.
                bareRecovered = Map.fromList
                    [ (pty, cgo)
                    | (pty, ga) <- zip paramTypes goArgs
                    , isGenericTypeParam pty
                    , Just cgo <- [goExprGoType Nothing ga]
                    , cgo /= "any"
                    , not (isGenericTypeParam cgo)
                    ]
                structuralRecovered = Map.unions
                    [ unifyGoTypes pty cgo
                    | (pty, ga) <- zip paramTypes goArgs
                    , not (isGenericTypeParam pty)
                    , containsGenericTypeParam pty
                    , Just cgo <- [goExprGoType Nothing ga]
                    , cgo /= "any"
                    ]
                recovered = Map.union bareRecovered structuralRecovered
                -- v0.13 Stage 1 — when σ pins EVERY TVar in a paramType,
                -- skip the `eraseTypeParams` widening. Identity
                -- mappings (T1 → T1) count as "pinned" because the
                -- substituted type already uses the right TVar names
                -- (visible in the enclosing generic function scope).
                substituteOnly pty =
                    let subbed = substTVarsInGoType recovered pty
                        unboundTVars =
                            [ t | t <- tvarsInGoTypeStr subbed
                                , not (Map.member t recovered) ]
                    in if null unboundTVars
                         then subbed
                         else if containsGenericTypeParam subbed
                                then eraseTypeParams subbed
                                else subbed
                substituted = map substituteOnly paramTypes
                -- v0.13 D-Lambda-Lowerer: when an arg is a literal
                -- `Can.Lambda` at a func-typed param slot, route
                -- through `curryLambdaPatTyped` instead of
                -- `exprToGo + coerceArg`. The CSI-captured branch
                -- above already does this; user-defined HOFs (which
                -- fall through to THIS fallback because no CSI is
                -- captured for them today) previously dropped the
                -- lambda into `coerceArg(exprToGo lam) "func(...)"`,
                -- producing `rt.Coerce[func(any) any](func(a any) any
                -- {...})`. Once D1 types the param's return slot,
                -- Go rejects the `func(any) any` shape against the
                -- typed sig. Lifting the typed-lambda emission into
                -- the fallback makes user-defined HOFs match too,
                -- which unblocks D1 (typed HOF return).
                coerceFallback orig subbed e@(A.At _ inner) =
                    case inner of
                        Can.Lambda pats body
                            | all isSimpleVarPattern pats
                            , (inputTypes, finalRet0) <-
                                splitCurriedFuncTypeStr (length pats) subbed
                            , length inputTypes == length pats
                            , length inputTypes > 0 ->
                                let -- v0.13 Stage 1 — when the
                                    -- substituted return type widened
                                    -- to "any" (TVars couldn't be
                                    -- pinned at the call site), try
                                    -- to recover by HM-inferring the
                                    -- lambda body's type. The body is
                                    -- a Can.Expr; if HM has a concrete
                                    -- type for it (e.g. body returns
                                    -- a known record / primitive), use
                                    -- THAT as the lambda's return type
                                    -- instead of "any". Closes
                                    -- adapters like
                                    -- `func(x State_Post_R) State_Post_R`
                                    -- where the body returns a typed
                                    -- value and HM knows it.
                                    finalRet =
                                        if finalRet0 == "any"
                                            then case inferGoType
                                                    (Rec._cg_solvedTypes getCgEnv)
                                                    body of
                                                "any" -> "any"
                                                concrete -> concrete
                                            else finalRet0
                                    skyTys = map goTypeStrToSkyType inputTypes
                                    bindings = patVarTypes pats skyTys
                                    bodyPreTyped = isEmittableGoType finalRet
                                    rawBody =
                                        if bodyPreTyped
                                            then exprToGoExpectGo finalRet body
                                            else exprToGo body
                                    body' = withScopedLambdaTypes bindings rawBody
                                in if bodyPreTyped
                                    then curryLambdaPatTypedPre inputTypes finalRet
                                            pats body'
                                    else curryLambdaPatTyped inputTypes finalRet
                                            pats body'
                        _ ->
                            if subbed == "any" && containsTypeParam orig
                                then GoIr.GoCall (GoIr.GoIdent "any") [exprToGo e]
                                else coerceArg (Just e) (exprToGo e) subbed
            in zipWith3Default coerceFallback paramTypes substituted args


-- | Three-way zip that pairs default args after lists run out.  Used
-- by `coerceCallArgsAt` to walk (origParamTy, substitutedTy, argExpr)
-- triples without losing trailing args when paramTypes is shorter
-- than args (variadic / over-supplied positions fall back to
-- exprToGo with no coercion).
zipWith3Default
    :: (String -> String -> Can.Expr -> GoIr.GoExpr)
    -> [String] -> [String] -> [Can.Expr] -> [GoIr.GoExpr]
zipWith3Default _ [] _ args = map exprToGo args
zipWith3Default _ _ [] args = map exprToGo args
zipWith3Default _ _ _ [] = []
zipWith3Default f (o:os) (s:ss) (a:as) = f o s a : zipWith3Default f os ss as


-- | Substitute generic type variables (`T1`, `T2`, …) in a
-- pre-rendered Go type string with concrete type strings.  Used
-- by the A5 call-site path to specialise param types before
-- coercion.  Identifier-aware: only replaces whole-word matches
-- so `T1` in `rt.SkyMaybe[T1]` becomes `rt.SkyMaybe[string]` but
-- something like `TupleN` is left alone.
substTVarsInGoType :: Map.Map String String -> String -> String
substTVarsInGoType σ s = goSubst s
  where
    goSubst [] = []
    goSubst rest@(c:cs)
        | isIdentStart c =
            let (word, after) = span isIdentChar rest
            in case Map.lookup word σ of
                Just replacement -> replacement ++ goSubst after
                Nothing          -> word ++ goSubst after
        | otherwise = c : goSubst cs

    -- Unicode-aware: see `isGoIdentStart` for the rationale. Names
    -- with non-ASCII letters in the Sky source emit as those same
    -- identifiers in Go; a naive ASCII walk would split the token
    -- and apply σ-substitution to the ASCII prefix only.
    isIdentStart = isGoIdentStart
    isIdentChar = isGoIdentChar

-- | v0.13 Stage 1 — split a chained-func Go-type string like
-- `func(string) func(int) func(bool) Foo` into ([string,int,bool],
-- "Foo"). Returns Nothing when the chain has fewer than 2 levels
-- (in which case the existing single-level handling already applies).
-- Used to detect curried-HOF slot targets so an uncurried Go function
-- ident can be wrapped in a typed curry adapter.
splitCurriedFuncStr :: String -> Maybe ([String], String)
splitCurriedFuncStr s = case splitFuncTypeStr s of
    Just (input, rest)
        | take 5 rest == "func(" ->
            case splitCurriedFuncStr rest of
                Just (more, final) -> Just (input : more, final)
                Nothing            -> Just ([input], rest)
        | otherwise -> Just ([input], rest)
    Nothing -> Nothing

-- | v0.13 Stage 1 — emit a typed curry adapter that wraps an
-- uncurried top-level Go function as a chain of Go-typed closures
-- matching the HOF slot's curried shape. Only fires when the
-- function is in `_cg_funcParamTypes` with arity ≥ matching
-- chain depth AND the chain's input types align with the fn's
-- declared param types. Returns Nothing if any check fails;
-- caller falls through to the existing `rt.Coerce` reflect-adapter
-- path.
--
-- Emitted shape for `Profile : (string, int, bool) -> Foo_R`
-- at slot `func(string) func(int) func(bool) Foo_R`:
--
--   func(__c0 string) func(int) func(bool) Foo_R {
--       return func(__c1 int) func(bool) Foo_R {
--           return func(__c2 bool) Foo_R {
--               return Profile(__c0, __c1, __c2)
--           }
--       }
--   }
buildCurryAdapter :: String -> [String] -> String -> Maybe GoIr.GoExpr
buildCurryAdapter name inputTys finalRet =
    let env = getCgEnv
        declaredParams = Map.findWithDefault [] name (Rec._cg_funcParamTypes env)
        declaredRet = Map.findWithDefault "any" name (Rec._cg_funcRetType env)
        n = length inputTys
    in if length declaredParams /= n
            || take n declaredParams /= inputTys
            || declaredRet /= finalRet
       then Nothing
       else Just (buildChain 0 inputTys finalRet)
  where
    -- Build the chain inner-to-outer.
    buildChain :: Int -> [String] -> String -> GoIr.GoExpr
    buildChain _ [] _ = GoIr.GoIdent "BUG_buildChain_empty"
    buildChain i [ty] retTy =
        let paramName = "__c" ++ show i
            innerCall = GoIr.GoCall (GoIr.GoIdent name)
                [ GoIr.GoIdent ("__c" ++ show k) | k <- [0 .. i] ]
        in GoIr.GoFuncLit
            [GoIr.GoParam paramName ty]
            retTy
            [GoIr.GoReturn innerCall]
    buildChain i (ty:rest) retTy =
        let paramName = "__c" ++ show i
            innerBody = buildChain (i + 1) rest retTy
            -- Inner sig must be the CURRIED chain shape, not flat —
            -- `func(int) func(bool) Foo`, not `func(int, bool) Foo`.
            innerSig = renderCurriedSig rest retTy
        in GoIr.GoFuncLit
            [GoIr.GoParam paramName ty]
            innerSig
            [GoIr.GoReturn innerBody]

    renderCurriedSig :: [String] -> String -> String
    renderCurriedSig [] retTy = retTy
    renderCurriedSig (ty:rest) retTy =
        "func(" ++ ty ++ ") " ++ renderCurriedSig rest retTy

-- | v0.13 Stage 1 — check whether a runtime kernel fn has a typed
-- `*T` variant whose Go signature matches the target slot's typed
-- shape. Returns the typed-variant name (e.g. `rt.String_toIntT`)
-- when a routing is safe. Conservative: only fires when the kernel
-- has a hand-curated mapping AND the typed sig matches the target
-- string verbatim. Adding new entries requires both a runtime fn
-- with the right typed sig AND a registry entry below.
lookupRtKernelTypedVariant :: String -> String -> Maybe String
lookupRtKernelTypedVariant bareFn targetTy =
    case Map.lookup bareFn rtKernelTypedVariants of
        Just (typedName, expectedTy)
            | expectedTy == targetTy -> Just ("rt." ++ typedName)
        _ -> Nothing

-- | Registry of (`bareKernelFn`, (`typedVariantName`, `typedGoSig`)).
-- Keep entries hand-curated so a runtime-side rename can't silently
-- produce a routing that calls a no-longer-existing function.
-- Add entries here when the contract calls for closing more
-- adapters via typed-variant routing.
rtKernelTypedVariants :: Map.Map String (String, String)
rtKernelTypedVariants = Map.fromList
    [ ("String_toInt",   ("String_toIntT",   "func(string) rt.SkyMaybe[int]"))
    , ("String_fromInt", ("String_fromIntT", "func(int) string"))
    , ("String_fromFloat", ("String_fromFloatT", "func(float64) string"))
    , ("String_toUpper", ("String_toUpperT", "func(string) string"))
    , ("String_toLower", ("String_toLowerT", "func(string) string"))
    , ("Basics_not",     ("Basics_notT",     "func(bool) bool"))
    ]

-- | v0.13 Stage 1 — look up a runtime-kernel fn's Go signature so
-- HOF arg coercion at the call site can σ-recover TVars from typed
-- kernel-fn refs (e.g. `Result.map Time.timeString r` → pin T1=int,
-- T2=string from `Time.timeString : Int -> String`). Without this,
-- the arg is `any`-typed and `Sky_Core_Result_map_`'s `(a -> b)`
-- param widens to `func(any) any`, forcing a
-- `rt.Coerce[func(any) any]` wrap that we want to close.
lookupRtKernelFnType :: String -> Maybe String
lookupRtKernelFnType fn =
    let bareName = takeWhile (/= '[') fn
        (modPart, restPart) = break (== '_') bareName
        funcPart = drop 1 restPart
        collectArgs (T.TLambda a r) =
            let (as, ret) = collectArgs r in (a : as, ret)
        collectArgs t = ([], t)
    in case ConstrainExpr.lookupKernelType modPart funcPart of
        Just (T.Forall _ ty)
            | (paramTys, retTy) <- collectArgs ty
            , not (null paramTys) ->
                let paramStrs = map solvedTypeToGo paramTys
                    retStr = solvedTypeToGo retTy
                in Just ("func(" ++ intercalateComma paramStrs ++ ") " ++ retStr)
        _ -> Nothing

-- | v0.13 Stage 1 — collect every Go-type TVar token (T1, T2, …) in a
-- Go-type string. Used to detect when σ pins every TVar in a
-- substitution result so the typed sig can survive without
-- `eraseTypeParams` widening (which would otherwise widen
-- `func(T1) T2` to `func(any) any` and kill typed routing).
tvarsInGoTypeStr :: String -> [String]
tvarsInGoTypeStr s = go s
  where
    go [] = []
    go rest@(c:cs)
        | isGoIdentStart c =
            let (word, after) = span isGoIdentChar rest
            in if isGenericTypeParam word
                 then word : go after
                 else go after
        | otherwise = go cs

-- | v0.13 Phase A4: peel up to N curried function arrows from a Go
-- type string `func(X1) func(X2) … func(Xn) R`, returning the list
-- of input types and the final return type R.  Stops early if the
-- string doesn't have N arrows — e.g. `func(int) int` peeled with
-- depth 2 returns (["int"], "int").
--
-- Used by typed lambda emission for multi-arg curried HOFs like
-- foldl (`(a -> b -> b) -> ...`): the spec's fn param has shape
-- `func(A) func(B) B` and the Sky lambda has 2 patterns; we want
-- to emit `func(_x A) func(_y B) B { ... }`.
splitCurriedFuncTypeStr :: Int -> String -> ([String], String)
splitCurriedFuncTypeStr 0 s = ([], s)
splitCurriedFuncTypeStr n s = case splitFuncTypeStr s of
    Just (inputTy, restTy) ->
        let (more, final) = splitCurriedFuncTypeStr (n - 1) restTy
        in (inputTy : more, final)
    Nothing -> ([], s)


-- | v0.13 Phase A4: parse a Go-type string of shape `func(X) Y`
-- returning `(X, Y)`.  Used by typed lambda emission to derive the
-- lambda's input + output types from the call site's expected
-- param shape.  Returns Nothing for non-function-shaped strings.
splitFuncTypeStr :: String -> Maybe (String, String)
splitFuncTypeStr s
    | "func(" `List.isPrefixOf` s =
        let afterFunc = drop 5 s
            (inputTy, afterInput) = takeUntilTopLevelParen afterFunc
        in case afterInput of
            ')' : rest -> Just (inputTy, dropWhile (== ' ') rest)
            _ -> Nothing
    | otherwise = Nothing
  where
    takeUntilTopLevelParen = go 0 ""
    go _ acc [] = (reverse acc, [])
    go d acc (c:cs)
        | c == '('  = go (d + 1) (c:acc) cs
        | c == ')' && d == 0 = (reverse acc, c:cs)
        | c == ')'  = go (d - 1) (c:acc) cs
        | c == '['  = go (d + 1) (c:acc) cs
        | c == ']'  = go (d - 1) (c:acc) cs
        | otherwise = go d (c:acc) cs


-- | T4-aware coercion. For parametric Sky types whose generic
-- instantiation won't match via plain `.(T)` assertion
-- (e.g. `SkyResult[any,any]` vs `SkyResult[IoError,string]`), use the
-- runtime coerce helpers that reconstruct the value with target
-- generic params.
--
-- v0.15.x hardening / Gap A1 — accepts an optional source `Can.Expr`.
-- The parametric-alias short-circuit historically gated on
-- `goExprGoType e` returning Just; for expression shapes whose Go-
-- static type isn't tracked in the lambda-types registry (let-
-- bindings holding a polymorphic-call result, VarLocal references
-- to outer-scope bindings), `goExprGoType` returns Nothing.  The
-- structural-fallback arm uses `inferExprType` on the source to
-- recover the alias identity directly from the HM-solved type and
-- short-circuits when the alias base names match.  Callers that
-- don't have an underlying `Can.Expr` (synthesised
-- `__p0 / __tco_t0` identifiers in over-application + TCO jumps)
-- pass `Nothing` and the new arm cleanly no-ops.
coerceArg :: Maybe Can.Expr -> GoIr.GoExpr -> String -> GoIr.GoExpr
coerceArg mSrc e ty
    | ty == "any" || null ty = e
    -- Generic type parameter (T1, T2, ...) — when the arg's static
    -- Go type is concrete (`int`, `string`, `[]T1`, `rt.SkyResult
    -- [...]`), Go's call-site inference pins the TVar from the
    -- arg and passing raw is correct. When the arg is `any`-typed
    -- (`rt.SkyCall(...)`, `rt.Field(...)`, an unannotated local)
    -- Go's inference can't pin the TVar from `any` and the call
    -- fails with `type any of <arg> does not match inferred type
    -- T2`. In that case route through `rt.Coerce[T]` so Go sees
    -- the arg as the target's TVar type. The Coerce helper is a
    -- thin type-assertion wrapper (no runtime work for primitive
    -- targets; reflect-backed for funcs).
    | isGenericTypeParam ty =
        -- v0.15.8 (P2): the generic-param slot deliberately
        -- consults `goExprGoType Nothing e` (NO structural
        -- fallback).  Reason: when a sibling arg pins this
        -- TVar = `any` from a `func(any) any` lambda, claiming
        -- the typed source pins T1 = `[]rt.SkyTuple2` then forces
        -- Go's call-site inference into a `[]any` vs `[]Tup2`
        -- conflict at the kernel's `[]T1` slot.  Keep the strict
        -- by-shape recovery here; the structural fallback fires
        -- at the typed slots below where the target type is
        -- concrete (not a TVar).
        case goExprGoType Nothing e of
            Just t | t /= "any" -> e
            _ ->
                GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ ty ++ "]")) [e]
    -- v0.15.3 — parametric record alias instantiation.  The callee
    -- almost always emits as a Go-generic function whose param
    -- type is `Foo_R[T]`; renderers erase TVars in the param-type
    -- string to `Foo_R[any]` here, but the LIVE callee is generic.
    -- When the source statically carries the same base alias
    -- (`Foo_R[Msg]`, `Foo_R[T1]`), passing it raw lets Go's call-
    -- site inference pin the callee's T from the arg.  The default
    -- `any(arg).(Foo_R[any])` cast is a NOMINAL assertion across
    -- Go generic instantiations — different instantiations of the
    -- same generic struct are distinct nominal types, so the
    -- assertion panics for any T ≠ any at runtime.
    --
    -- Two soundness paths — both gated on the SOURCE actually
    -- carrying the same parametric base as the target:
    --
    --   (a) Lambda-types-registry path (the original short-circuit).
    --       When `goExprGoType e` returns the Go-static type of a
    --       typed local (let-binding registered via the lowerer's
    --       `withScopedLambdaTypes`, typed function param, …), the
    --       structural match is trivial — both sides are Go-type
    --       strings of shape `Foo_R[<args>]`.
    --
    --   (b) v0.15.x hardening / Gap A1 — structural fallback via
    --       `inferExprType`.  The lambda-types registry doesn't
    --       cover every shape: let-bindings whose RHS is a
    --       polymorphic-call result, VarLocal references to
    --       outer-scope bindings, and other cases where the
    --       lazy-rendering race leaves the entry unfilled.  But
    --       `Solve.SolvedTypes` carries the HM-solved type for
    --       every name in scope, and for record-alias-typed values
    --       that type is `T.TAlias _ aliasName _ _`.  When the
    --       target slot is `<aliasName>_R[...]` we can short-
    --       circuit safely: at runtime the value's actual generic
    --       instantiation IS the target alias's instantiation
    --       (HM has already proved it), so Go's call-site type
    --       inference pins the callee's T from the source.
    --
    -- Regression: test-files/v0.15-stress/src/Widget/Form.sky and
    -- test-files/v0.15-stress/src/Widget/CrossInstanceCfg.sky.  Form
    -- exercises view-body sibling helpers, consumeForm's typed-arg
    -- forwarding, and main's let-bound record passed to a
    -- polymorphic view (path (a)).  CrossInstanceCfg exercises a
    -- let-bound concrete alias passing through a polymorphic
    -- forwarder, then consumed by a concretely-typed function —
    -- both call sites pre-fix emitted `any(.).(Cfg_R[any])` casts
    -- that panicked under Go's nominal generic-type rules
    -- (path (b)).  See spec
    -- test/Sky/Build/CoerceArgParametricSpec.hs.
    --
    -- v0.15.8 (P2): pass `mSrc` so `goExprGoType`'s structural
    -- fallback can recover the source's parametric-alias shape
    -- when the lambda-types registry has no entry (let-bindings
    -- whose RHS is a polymorphic-call result; outer-scope local
    -- refs; etc.).  This site is SAFE to consume the fallback
    -- because the equality check is STRUCTURAL (target/source
    -- alias bases agree) — not value-vs-target.  Pinning T from
    -- the source's alias instantiation is correct under Go's
    -- generic-call inference (the live callee IS Cfg_R-generic
    -- in T, accepting any instantiation raw).  This is the Gap
    -- A2 closure path for alias-shaped sources.
    | Just targetBase <- parametricAliasBase ty
    , Just srcTy <- goExprGoType mSrc e
    , Just srcBase <- parametricAliasBase srcTy
    , targetBase == srcBase
        = e
    -- (b) Structural fallback gated on the HM-solved source type.
    | Just targetBase <- parametricAliasBase ty
    , Just src <- mSrc
    , Just aliasName <- aliasBaseFromCanExpr src
    , aliasName ++ "_R" == targetBase
        = e
    -- v0.13 Stage 1 — runtime-kernel HOF arg: when the target slot
    -- expects a typed `func(...) ...` AND the source is a
    -- `GoIdent "rt.X"` reference to a non-typed kernel whose typed
    -- `XT` variant has a matching signature, route the call to the
    -- typed variant directly. This avoids the
    -- `rt.Coerce[func(string) rt.SkyMaybe[int]](rt.String_toInt)`
    -- reflect-adapter wrap by using static Go typing throughout.
    -- v0.13 Stage 1 — runtime-kernel HOF arg: this branch is
    -- INTENTIONALLY DISABLED. Routing `rt.X` → `rt.XT` at HOF arg
    -- sites was correct for statically-dispatched kernels
    -- (Sky_Core_Maybe_andThen__String_Int's typed param accepts
    -- the typed variant raw) but unsafe for reflect-dispatched
    -- kernels (Sky.Core.Json.Decode's `Decode.map String.fromInt
    -- Decode.int` chains through SkyCall, which casts the
    -- function arg to `func(any) any` at runtime — handing it
    -- the typed `func(int) string` panics with `interface
    -- conversion`). Until the routing can distinguish typed-path
    -- kernels from reflect-path kernels, fall through to the
    -- existing rt.Coerce reflect-adapter wrap, which handles
    -- both shapes correctly. Closes the Maybe.andThen residual is
    -- deferred.
    --
    -- Original (disabled) branch kept commented for reference:
    -- | take 5 ty == "func("
    -- , GoIr.GoIdent name <- e
    -- , "rt." `List.isPrefixOf` name
    -- , Just typedVariant <- lookupRtKernelTypedVariant (drop 3 name) ty =
    --     GoIr.GoIdent typedVariant
    -- v0.13 Stage 1 — Sky-uncurried fn at a curried HOF slot. Auto-
    -- record-ctors (e.g. `Profile : String -> Int -> Bool -> Profile_R`)
    -- emit as Go-uncurried `func Profile(p0, p1, p2)`. At a HOF slot
    -- expecting curried `func(string) func(int) func(bool) Profile_R`,
    -- emit a typed curry adapter directly — no reflect Coerce wrap.
    -- Detection: target is a chain of `func(X) func(Y) ...` nodes AND
    -- source is a GoIdent for a top-level fn whose declared param
    -- count matches the chain depth AND the chain's param types match
    -- the fn's funcParamTypes entry.
    | take 5 ty == "func("
    , Just (inputTys, finalRet) <- splitCurriedFuncStr ty
    , length inputTys > 1
    , GoIr.GoIdent name <- e
    , not ("rt." `List.isPrefixOf` name)
    , Just curryWrapper <- buildCurryAdapter name inputTys finalRet =
        curryWrapper
    -- v0.13 typed lowerer: `e` is already provably the target type —
    -- skip the coercion entirely (no `rt.CoerceInt(int)` etc.).
    --
    -- v0.15.8 (P2-followup, arbitration HEAD-CYCLE-01-P2.md
    -- Step 3): the skip-check is the THIRD vote in the
    -- σ-recovery / TVar-erasure / coerceArg-skip-check three-way
    -- consensus.  σ-recovery + TVar erasure DELIBERATELY stay
    -- coarse here (they pass Nothing to `goExprGoType`) — pinning
    -- a typed TVar from one sibling arg while another erases to
    -- `any` breaks Go's call-site inference uniformity and
    -- triggers
    --     "type []string of ... does not match inferred type
    --      []any for []T1"
    -- at `go build` (the original P2 regression on
    -- examples/13-skyshop).
    --
    -- Gating: only fire the skip when the IR-shape classifier
    -- ALONE (no structural fallback — explicit `Nothing`) returns
    -- Just AND it matches the target.  The structural fallback's
    -- positive type info is consumed by sites that DON'T
    -- participate in the σ consensus (`wrapTypedReturn` /
    -- `coerceToFieldType` / the parametric-alias arm above —
    -- where the equality is STRUCTURAL alias-base, not
    -- value-vs-target).
    --
    -- Lock test: `test/Sky/Build/CoerceArgListMapInterplaySpec.hs`
    -- + the standing skyshop-clean-build lock
    -- `test/Sky/Build/SkyshopCompilesSpec.hs`.  Any future change
    -- that breaks this gate re-trips both.
    | Just shapeTy <- goExprGoType Nothing e
    , shapeTy == ty
        = e
    | Just params <- stripParametric "rt.SkyResult" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eraseTypeParams params ++ "]")) [e]
    | Just inner <- stripParametric "rt.SkyMaybe" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "]")) [e]
    -- Audit: parametric SkyTask param targets need TaskCoerceT for the
    -- same nominal-typing reason — `func() any` from the runtime helpers
    -- and `SkyTask[any, any]` from typed call sites are unrelated to
    -- `SkyTask[Error, A]` under Go's generic-instantiation rules. Without
    -- this branch the codegen emits `any(arg).(rt.SkyTask[Error, A])`
    -- which panics at runtime on any cross-instantiation pass-through.
    | Just params <- stripParametric "rt.SkyTask" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.TaskCoerceT[" ++ eraseTypeParams params ++ "]")) [e]
    | ty == "string" = GoIr.GoCall (GoIr.GoIdent "rt.CoerceString") [e]
    | ty == "int"    = GoIr.GoCall (GoIr.GoIdent "rt.CoerceInt") [e]
    | ty == "bool"   = GoIr.GoCall (GoIr.GoIdent "rt.CoerceBool") [e]
    | ty == "float64"= GoIr.GoCall (GoIr.GoIdent "rt.CoerceFloat") [e]
    -- Target is []any: accept either `[]any` source or concrete
    -- `[]T` source via rt.AsListAny which widens.
    | ty == "[]any" =
        GoIr.GoCall (GoIr.GoIdent "rt.AsListAny") [e]
    -- Target is map[string]any: accept either map[string]any source
    -- or a typed map[string]T source via rt.AsMapAny which widens.
    -- Without this the call site emits `any(x).(map[string]any)` —
    -- a direct assertion that panics with
    -- `interface {} is map[string]string, not map[string]interface{}`
    -- (real panic class from examples/13-skyshop's Firebase auth flow).
    | ty == "map[string]any" =
        GoIr.GoCall (GoIr.GoIdent "rt.AsMapAny") [e]
    -- Typed slice `[]T`: runtime may hand us `[]any`, walk-and-cast.
    | Just elt <- stripListType ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elt ++ "]")) [e]
    -- map[string]V: typed dict.
    | Just valTy <- stripMapType ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valTy ++ "]")) [e]
    | otherwise =
        let erasedTy = eraseTypeParams ty
        in if erasedTy == "any"
             then e  -- fully erased to any — no assertion needed
             -- Function-type targets: Go doesn't allow type-asserting
             -- between two concrete function types (func(any) any vs
             -- func(X) Y are unrelated nominal types). Route through
             -- rt.Coerce which detects the Func kind and builds a
             -- reflect-based adapter (makeFuncAdapter) that boxes
             -- the callback's params and unwraps its return.
             else if take 5 erasedTy == "func("
                  -- v0.15.3 — when target is a function type AND the
                  -- raw `ty` STILL carries Go-side generic params
                  -- (T1/T2/...) that `eraseTypeParams` flattened to
                  -- `any`, AND source is a plain user identifier
                  -- (let-binding / param ref / field selector), pass
                  -- the arg raw.  Wrapping in `rt.Coerce[func(P)
                  -- any]` would erase the generic-param connection
                  -- and break the callee's inference (`func(P) any`
                  -- doesn't unify with `func(P) T1`).  When the
                  -- source is a typed local (e.g. `submit := cfg.
                  -- WfSubmit` whose Go type is `func(P) T1`),
                  -- passing it raw lets Go pin T1 from cfg's
                  -- instantiation at the call site.
                  --
                  -- Soundness: gated on `containsGenericTypeParam
                  -- ty` (not erasedTy) — so the wrap is ONLY
                  -- skipped when the original param sig contained
                  -- a TVar (the generic-callee case).  Non-generic
                  -- callees still get the wrap because their sig
                  -- has no T1.  And we restrict to
                  -- `isPlainIdentForTypedRouting` sources so
                  -- kernel-call results / coercion wrappers / chains
                  -- whose intermediate bases erase to `any` still
                  -- flow through Coerce.
                  --
                  -- v0.15.x hardening / Gap A4 / Plan Item P3: the
                  -- previous gate used the purely-structural
                  -- `isPlainIdent`, which accepted shapes like
                  -- `cfg.someAnyField.deep` where the intermediate
                  -- `cfg.someAnyField` resolves to `any` — Go's
                  -- call-site inference can't pin T1 from an
                  -- `any`-typed base, so the raw-pass silently
                  -- routed the wrong code.  The typed companion
                  -- gate validates every intermediate selector's
                  -- base via `goExprGoType` (non-`any`).  Invariant
                  -- locked by `IsPlainIdentSpec`.
                  then if containsGenericTypeParam ty
                          && isPlainIdentForTypedRouting e
                       then e
                       else GoIr.GoCall
                              (GoIr.GoIdent ("rt.Coerce[" ++ erasedTy ++ "]")) [e]
                  -- v0.13.x #158: record-alias targets route through
                  -- `rt.Coerce[T]` so a `map[string]any` source narrows
                  -- to the typed struct via Coerce's map→struct field
                  -- builder. ADT names + FFI opaque types still use
                  -- direct assertion (no map-source panic class).
                  else if isRecordAliasTy erasedTy
                       then GoIr.GoCall
                            (GoIr.GoIdent ("rt.Coerce[" ++ erasedTy ++ "]")) [e]
                       else GoIr.GoTypeAssert
                            (GoIr.GoCall (GoIr.GoIdent "any") [e]) erasedTy

isGenericTypeParam ('T':rest) = all (\c -> c >= '0' && c <= '9') rest && not (null rest)
isGenericTypeParam _ = False


-- | v0.15.3 — accept a Go expression as a compatible source for
-- a parametric record alias slot.  When the target is `Foo_R[X]`
-- (a generic-callee param type), the goal is to pass the arg
-- raw so Go's call-site type inference pins the callee's T from
-- the source's actual instantiation.
--
-- Sources we accept:
--   * `goExprGoType e` returns the SAME `Foo_R` base (typed
--     local with known static type) — perfect match, Go infers.
--   * `GoIdent name` for a user-introduced local (not `rt.*`)
--     where `goExprGoType` is Nothing — could be a function param
--     whose type isn't tracked in the lambda-types scope (the
--     scoped-binding-vs-lazy-rendering race), but its Go-static
--     type IS `Foo_R[T]` at the actual call site.  Go's
--     inference handles it.  If the type turns out wrong, Go
--     compile-fails — caught at build time, not runtime.
--   * `GoSelector base _` where `base` is a plain ident — same
--     reasoning (e.g. `cfg.WfSubmit` field access).
--
-- Sources we REJECT (fall through to the existing wrap path):
--   * Kernel call results (`rt.*`), Coerce wrappers, struct lits,
--     literal values — those have their own type management
--     that the legacy `any(arg).(Foo_R[any])` assertion happens
--     to handle correctly.
isParametricCompatibleSource :: GoIr.GoExpr -> Bool
isParametricCompatibleSource e = case goExprGoType Nothing e of
    Just srcTy | isJust (parametricAliasBase srcTy) -> True
    _ -> case e of
        GoIr.GoIdent name -> not ("rt." `List.isPrefixOf` name)
                          && not ("__tco" `List.isPrefixOf` name)
                          && not ("__destruct" `List.isPrefixOf` name)
        GoIr.GoSelector base _ -> isParametricCompatibleSource base
        _ -> False


-- | v0.15.3 — recognise a "plain user identifier" Go expression
-- by STRUCTURE only.
--
-- True for:
--   * A bare `GoIdent name` where `name` doesn't start with `rt.`
--     (user-introduced local: let-binding, function param,
--     top-level fn ref).
--   * A field-selector chain `target.f1.f2…` whose deepest base
--     is a plain ident (recursion walks the chain to the leaf —
--     `(rt.SkyCall(…)).Field.Nested` correctly returns False
--     because the leaf is a `GoCall`, not a plain ident).
--
-- False for:
--   * `GoCall` / `GoFuncLit` / `GoStructLit` / literals — those
--     are values produced by computation, not user-named refs.
--   * `rt.X` identifiers — runtime helper results that often need
--     explicit coercion.
--
-- ## Pure structural classifier
--
-- This function is pure — no IORef reads, no `goExprGoType` lookup.
-- Unit-tested in `test/Sky/Build/IsPlainIdentSpec.hs` against a
-- table of crafted `GoExpr` shapes; the test is the discovery
-- artefact for any future recursion-correctness regression.
--
-- The TYPED gate used at the `coerceArg` call site is the
-- companion `isPlainIdentForTypedRouting` below — it layers a
-- `goExprGoType`-on-each-selector-base check over this structural
-- predicate.  The split keeps the structural recursion test-able
-- in isolation while the typed gate carries the soundness
-- contract for the codegen path.
isPlainIdent :: GoIr.GoExpr -> Bool
isPlainIdent e = case e of
    GoIr.GoIdent name -> not ("rt." `List.isPrefixOf` name)
    GoIr.GoSelector base _ -> isPlainIdent base
    _ -> False


-- | v0.15.x hardening / Gap A4 / Plan Item P3 — the typed-routing
-- gate used by `coerceArg` at the generic-param-bearing target arm.
--
-- Same SHAPE acceptance as `isPlainIdent`, PLUS: every intermediate
-- selector's base must resolve to a non-`any` static Go type via
-- `goExprGoType`.  Go's call-site type inference pins the callee's
-- T from the source's STATIC Go type — if any base in the chain is
-- (or erases to) `any`, Go inference has nothing to pin against
-- and passing the expression raw silently routes a runtime panic
-- (the Coerce wrap that should have narrowed the chain is missing).
--
-- ### Why a separate function vs widening `isPlainIdent`
--
-- 1. `isPlainIdent` is widely re-used as a structural classifier
--    (the bare-ident-or-selector-chain shape) — keeping it pure
--    lets the unit table in `IsPlainIdentSpec` lock its recursion
--    invariants in isolation, without depending on a populated
--    `scopeStateRef` / `globalCgEnv`.
-- 2. `goExprGoType` is impure (`unsafePerformIO` reads of mutable
--    IORefs).  Routing this impurity through every existing
--    `isPlainIdent` consumer would broaden the soundness surface
--    with no benefit; ALL non-`coerceArg` callers want the
--    structural meaning.
-- 3. The split documents the soundness contract clearly: the
--    typed-routing path EXPLICITLY opts into the static-type
--    check via the named function.
--
-- ### The intermediate-base check
--
-- For `cfg.WfSubmit`:
--   * `cfg`'s `goExprGoType` returns `Just "Cfg_R[T1]"`
--     (parametric-record-alias arm in `goExprGoType`).
--   * Non-`any` → accepted.
--   * Selector leaf reached; OK.
-- For `unknownLocal.f1.f2`:
--   * `unknownLocal.f1`'s `goExprGoType` may return `Nothing` —
--     base is untracked.  Reject; wrap path runs.
-- For `cfg.someAnyField.deep`:
--   * `cfg.someAnyField`'s `goExprGoType` returns `Just "any"`.
--     Reject; wrap path runs.
--
-- The Nothing-→-reject rule is intentionally strict: an un-trackable
-- base could be the parametric-alias case the v0.15.3 arm handles,
-- BUT it could equally be a heterogeneous user expression whose
-- Go-static type is unknown to us — and the soundness floor is to
-- WRAP unless we can prove the unwrap-safe contract.  The
-- companion runtime-shape tests (CoerceArgParametricSpec + the
-- 27-example sweep) confirm the over-rejection rate is benign in
-- practice (golden-size delta ≤ ±3 % per Planner's risk register).
isPlainIdentForTypedRouting :: GoIr.GoExpr -> Bool
isPlainIdentForTypedRouting e = isPlainIdent e && intermediatesTyped e
  where
    intermediatesTyped (GoIr.GoIdent _)       = True
    intermediatesTyped (GoIr.GoSelector b _)  =
        case goExprGoType Nothing b of
            Just t  -> t /= "any" && intermediatesTyped b
            Nothing -> False
    intermediatesTyped _                       = False


-- | v0.15.3 — extract the base alias name from a parametric record
-- alias instantiation.  `Foo_R[Msg]`, `Foo_R[any]`, `Foo_R[T1]`
-- all return `Just "Foo_R"`.  Bare `Foo_R` (no `[...]`) returns
-- Nothing — that's the structural shape that uses the non-
-- parametric record-alias path via `isRecordAliasTy`.
--
-- Used by `coerceArg` to recognise cross-instantiation cases:
-- when both target and source share the same Foo_R base, the
-- callee is invariably Go-generic (`func view[T any](Foo_R[T])`),
-- so Go's type inference pins T from the source's instantiation
-- — no runtime cast needed, and the nominal `.(Foo_R[any])`
-- assertion would panic for any source whose T ≠ any.
parametricAliasBase :: String -> Maybe String
parametricAliasBase ty =
    case List.span (/= '[') ty of
        (base, '[':_)
          | "_R" `List.isSuffixOf` base
          , isRecordAliasTy base -> Just base
        _ -> Nothing


-- | v0.15.x hardening / Gap A1 — recover an alias base name from a
-- source `Can.Expr` by routing through `inferExprType` against the
-- global codegen env's `Solve.SolvedTypes`.
--
-- Used by `coerceArg`'s structural-fallback arm.  The lambda-types
-- registry covers most lowered-expression shapes but misses three:
--   * Let-bindings whose RHS is a polymorphic-call result
--     (`let cfg1 = forwardCfg cfg0`).
--   * VarLocal references that pre-date the current scope's
--     `withScopedLambdaTypes` push.
--   * Field selectors whose record's Go-static type isn't tracked.
-- For all three the HM solver still has the type — we read it,
-- detect a record-alias-typed value, and return the alias's name.
-- The caller appends `"_R"` and compares against the target's
-- parametric base.
--
-- Returns Nothing when:
--   * The expression isn't HM-typed (synthesised identifiers,
--     `Nothing` from `inferExprType`).
--   * The type isn't a record alias (primitives, lambdas, ADTs).
--   * The alias isn't a record alias (raw `type alias X = Int`
--     style aliases that emit as their underlying Go type, not
--     `X_R`).
aliasBaseFromCanExpr :: Can.Expr -> Maybe String
aliasBaseFromCanExpr src =
    let env = getCgEnv
        solved = Rec._cg_solvedTypes env
    in case inferExprType solved src of
        Just (T.TAlias homeMod aliasName _ _) ->
            -- Only RECORD aliases emit as `<name>_R` in Go.  Aliases
            -- of other shapes (transparent `type alias X = Int`) emit
            -- as their underlying Go type and never match a parametric
            -- base check.  Confirm via `_cg_recordAliases`, which
            -- carries BOTH the entry-module unqualified names AND
            -- module-prefixed dep names — so this works whether the
            -- target instantiation renders as `XCfg_R` (entry) or
            -- `Widget_CrossInstanceCfg_XCfg_R` (dep).  Returns the
            -- name flavour that matches the in-scope target.
            let prefix = case ModuleName.toString homeMod of
                    ""  -> ""
                    nm  -> map (\c -> if c == '.' then '_' else c) nm
                                ++ "_"
                qualified = prefix ++ aliasName
                isRec = Set.member aliasName (Rec._cg_recordAliases env)
                isQualRec = Set.member qualified (Rec._cg_recordAliases env)
            in if isRec then Just aliasName
               else if isQualRec then Just qualified
               else Nothing
        _ -> Nothing


-- | v0.13 Stage 1 — extract Go-string param types from a kernel's
-- HM type, taking N param positions. Returns empty list if the
-- type isn't a function (the Forall body was a bare value).
--
-- Renames the kernel's Sky-side TVars (e.g. "err", "a", "msg") to
-- fresh Go-side T1/T2/T3 identifiers so the recovery σ's
-- `containsGenericTypeParam` check can detect them and pin types
-- from typed args. Without the rename, solvedTypeToGo renders
-- TVar "err" as "any" and the recovery σ skips the param.
kernelParamGoTypes :: T.Type -> Int -> [String]
kernelParamGoTypes ty n =
    let tvars = uniqueOrdered (collectTVars ty)
        sub = Map.fromList (zip tvars ["T" ++ show i | i <- [1 :: Int ..]])
        renamed = substTVars sub ty
    -- v0.13 Stage 1 — use the TVar-preserving renderer so renamed
    -- TVars survive as `T1, T2` in the kernel param string. Without
    -- this, `solvedTypeToGo` erases each TVar to `any` and the
    -- substituted kernel sig becomes `func(SkyResult[any,any]) any`,
    -- giving σ-recovery nothing to pin → callback args wrap in
    -- `rt.Coerce[func(SkyResult[any,any]) any]` even when the typed
    -- ctor's actual sig is known.
    in take n (go renamed)
  where
    go (T.TLambda from to) = solvedTypeToGoPreserveTVars from : go to
    go _                   = []
    collectTVars (T.TVar n) = [n]
    collectTVars (T.TLambda a b) = collectTVars a ++ collectTVars b
    collectTVars (T.TType _ _ args) = concatMap collectTVars args
    collectTVars (T.TTuple a b cs) =
        collectTVars a ++ collectTVars b ++ concatMap collectTVars cs
    collectTVars (T.TRecord fields _) =
        concatMap (\(T.FieldType _ fty) -> collectTVars fty) (Map.elems fields)
    collectTVars (T.TAlias _ _ _ inner) = case inner of
        T.Filled  i -> collectTVars i
        T.Hoisted i -> collectTVars i
    collectTVars T.TUnit = []
    uniqueOrdered xs = go' [] xs
      where
        go' acc [] = reverse acc
        go' acc (y:ys) | y `elem` acc = go' acc ys
                       | otherwise = go' (y:acc) ys


-- | v0.13 Stage 1 — coerce an arg at a kernel call site. Handles
-- the Can.Lambda → typed-emission path (mirroring coerceFallback in
-- coerceCallArgsAt) so kernel callbacks like Cmd.perform's
-- `(Result e a -> msg)` arg emit as `func(__p State_Msg) any`
-- instead of `func(__p any) any`.
kernelCoerceArg :: [String] -> Int -> String -> Can.Expr -> GoIr.GoExpr
kernelCoerceArg _allSubbed _idx subbed e@(A.At _ inner) =
    case inner of
        Can.Lambda pats body
            | all isSimpleVarPattern pats
            , (inputTypes, finalRet0) <-
                splitCurriedFuncTypeStr (length pats) subbed
            , length inputTypes == length pats
            , length inputTypes > 0 ->
                let finalRet =
                        if finalRet0 == "any"
                            then case inferGoType
                                    (Rec._cg_solvedTypes getCgEnv)
                                    body of
                                "any" -> "any"
                                concrete -> concrete
                            else finalRet0
                    skyTys = map goTypeStrToSkyType inputTypes
                    bindings = patVarTypes pats skyTys
                    bodyPreTyped = isEmittableGoType finalRet
                    rawBody =
                        if bodyPreTyped
                            then exprToGoExpectGo finalRet body
                            else exprToGo body
                    body' = withScopedLambdaTypes bindings rawBody
                in if bodyPreTyped
                    then curryLambdaPatTypedPre inputTypes finalRet pats body'
                    else curryLambdaPatTyped inputTypes finalRet pats body'
        -- v0.15.2: Can.Record at a parametric-alias kernel slot —
        -- same insight as `coerceCallArgsAt`'s Record arm and
        -- `lowerArgExpect`.  Without this, a `Maybe.withDefault
        -- defaultCfg (Just newCfg)` where Cfg is a parametric alias
        -- with σ={a → State.Msg} substituted the slot to `Cfg_R[State_Msg]`
        -- would emit `any(Cfg_R[any]{...}).(Cfg_R[State_Msg])` and
        -- panic on Go's nominal generic-type assertion.
        --
        -- Gated on `not containsGenericTypeParam subbed` for the same
        -- reason the other arms are: the kernel's σ may still have
        -- TVars at the call site (only the callee body has them in
        -- scope as Go generic params).
        Can.Record _
            | isParametricAliasInstantiation subbed
            , not (containsGenericTypeParam subbed) ->
                exprToGoExpectGo subbed e
        _ -> coerceArg (Just e) (exprToGo e) subbed


-- | v0.13 Stage 1 — does a Go-type string contain any generic-param
-- placeholders we emitted (T1, T2, ...)?  Used at the call site to
-- decide whether structural unification is worth trying for an arg
-- whose param type contains TVars (e.g. `[]T1`, `func(T1) T2`).
containsGenericTypeParam :: String -> Bool
containsGenericTypeParam =
    any isWordGenericParam . tokenise
  where
    -- Split into maximal alphanumeric runs (everything else is
    -- punctuation: brackets, commas, spaces, parens, dots).
    tokenise [] = [""]
    tokenise (c:cs)
        | isIdChar c = let (tok, rest) = span isIdChar (c:cs)
                       in tok : tokenise rest
        | otherwise  = tokenise cs
    isIdChar c = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
              || (c >= '0' && c <= '9') || c == '_'
    isWordGenericParam ('T':rest) =
        not (null rest) && all (\c -> c >= '0' && c <= '9') rest
    isWordGenericParam _ = False


-- | v0.13 Stage 1 — structural unification on Go-type strings.
-- Walks `paramTy` (which may contain TVar placeholders) and `argTy`
-- in lockstep, collecting `Map paramTVar argSubstring` substitutions.
-- On any structural mismatch (e.g. param shape `func(T1) T2` vs arg
-- shape `[]string`), returns an empty map — caller falls back to the
-- existing erase-to-`any` path so we never make things worse.
--
-- Handles the common shapes the v0.13 codegen emits:
--   * Bare TVar               : "T1"   ↦ argTy verbatim
--   * Slice                   : "[]T1" + "[]X" → unify "T1" with "X"
--   * Function                : "func(T1, …) T2" + "func(A, …) B"
--                                → unify each input pair + return pair
--   * Parametric generic      : "rt.SkyMaybe[T1]" + "rt.SkyMaybe[X]"
--                                → unify "T1" with "X"
--   * Concrete equal          : "string" + "string" → {} (no-op match)
unifyGoTypes :: String -> String -> Map.Map String String
unifyGoTypes pty argTy
    | isGenericTypeParam pty =
        if argTy == "any"
            then Map.empty
            -- v0.13 Stage 1 — identity TVar mapping (T1 → T1) is
            -- valid when the arg's GoType comes from a typed param
            -- in the SAME generic scope. Preserves the type so
            -- substituted paramType stays `func(T1) T2` and the
            -- coerceArg short-circuit `goExprGoType e == Just ty`
            -- fires (passing the arg raw, no `func(any) any` wrap).
            else Map.singleton pty argTy
    | take 2 pty == "[]" && take 2 argTy == "[]" =
        unifyGoTypes (drop 2 pty) (drop 2 argTy)
    | "func(" `List.isPrefixOf` pty && "func(" `List.isPrefixOf` argTy =
        case (splitFuncSig pty, splitFuncSig argTy) of
            (Just (pIns, pOut), Just (aIns, aOut))
                | length pIns == length aIns ->
                    Map.unions (unifyGoTypes pOut aOut
                                : zipWith unifyGoTypes pIns aIns)
            _ -> Map.empty
    | otherwise =
        -- Try parametric-bracket match: `Name[args]` vs `Name[args]`.
        case (splitParametric pty, splitParametric argTy) of
            (Just (n1, a1), Just (n2, a2))
                | n1 == n2 && length a1 == length a2 ->
                    Map.unions (zipWith unifyGoTypes a1 a2)
            _ -> Map.empty


-- | Split `func(A, B) R` into ([A, B], R). Returns Nothing on shape
-- mismatch. Respects nested brackets / parens so generic args don't
-- get sliced wrong.
splitFuncSig :: String -> Maybe ([String], String)
splitFuncSig s
    | Just inside <- List.stripPrefix "func(" s
    , (argsStr, rest) <- splitToplevelClose 0 inside
    , not (null rest) = Just (splitToplevelCommas argsStr, dropWhile (== ' ') rest)
    | otherwise = Nothing
  where
    -- Walk until the matching close-paren at depth 0; return
    -- (text-inside, text-after-close-paren).
    splitToplevelClose _ []           = ("", "")
    splitToplevelClose d (')':rest)
        | d == 0    = ("", rest)
        | otherwise = let (a, r) = splitToplevelClose (d-1) rest in (')':a, r)
    splitToplevelClose d (c:rest)
        | c == '('  = let (a, r) = splitToplevelClose (d+1) rest in (c:a, r)
        | c == '['  = let (a, r) = splitToplevelClose (d+1) rest in (c:a, r)
        | c == ']'  = let (a, r) = splitToplevelClose (d-1) rest in (c:a, r)
        | otherwise = let (a, r) = splitToplevelClose d rest     in (c:a, r)

    splitToplevelCommas = go 0 ""
      where
        go _ acc [] = [reverse acc]
        go d acc (',':cs)
            | d == 0    = reverse acc : go 0 "" (dropWhile (== ' ') cs)
        go d acc (c:cs)
            | c == '(' || c == '['  = go (d+1) (c:acc) cs
            | c == ')' || c == ']'  = go (d-1) (c:acc) cs
            | otherwise             = go d (c:acc) cs


-- | Split `Name[arg1, arg2]` into ("Name", [arg1, arg2]). Returns
-- Nothing if the string doesn't have a `[…]` bracket suffix.
splitParametric :: String -> Maybe (String, [String])
splitParametric s = case break (== '[') s of
    (name, '[':rest)
      | not (null name)
      , last rest == ']' ->
          Just (name, splitTopArgs (init rest))
    _ -> Nothing
  where
    splitTopArgs = go 0 ""
      where
        go _ acc [] = [reverse acc]
        go d acc (',':cs)
            | d == 0    = reverse acc : go 0 "" (dropWhile (== ' ') cs)
        go d acc (c:cs)
            | c == '(' || c == '['  = go (d+1) (c:acc) cs
            | c == ')' || c == ']'  = go (d-1) (c:acc) cs
            | otherwise             = go d (c:acc) cs


-- | v0.13 Phase A5 — does a comma-separated type-param list
-- contain any TVar placeholders?  Used by `coerceArg` to detect
-- partially-erased generic parameters where coercion would mis-
-- match Go's type inference.
containsTypeParam :: String -> Bool
containsTypeParam s =
    any isGenericTypeParam (splitTopLevelCommas s)


-- | Split a Go type-arg list on TOP-LEVEL commas (commas not
-- inside brackets).  Handles nested generics like `Map[K, V]`
-- without treating the inner comma as a separator.
splitTopLevelCommas :: String -> [String]
splitTopLevelCommas s = go 0 [] "" s
  where
    go _ acc cur [] = reverse (reverse (dropWhile (== ' ') cur) : acc)
    go d acc cur (c:cs)
        | c == '['  = go (d + 1) acc (c:cur) cs
        | c == ']'  = go (d - 1) acc (c:cur) cs
        | c == ',' && d == 0 =
            go d (reverse (dropWhile (== ' ') cur) : acc) "" cs
        | otherwise = go d acc (c:cur) cs

-- | Replace callee-scoped type params (T1, T2, ...) with `any` in
-- type strings so call-site coercions are valid.
-- E.g. "any, T1" → "any, any", "func(T1) func(T2) any" → "func(any) func(any) any".
-- Does NOT replace T2 in "rt.T2[...]" — only standalone identifiers.
eraseTypeParams :: String -> String
eraseTypeParams = go Nothing
  where
    go _ [] = []
    go prev ('T':rest)
        | not (maybe False isIdChar prev)  -- not preceded by ident char
        , (digits, after) <- span (\c -> c >= '0' && c <= '9') rest
        , not (null digits)
        , null after || not (isIdChar (head after))
        = "any" ++ go (Just 'y') after  -- 'y' from "any"
    go _ (c:cs) = c : go (Just c) cs
    isIdChar c = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
              || (c >= '0' && c <= '9') || c == '_' || c == '.'

intercalateComma :: [String] -> String
intercalateComma []     = ""
intercalateComma [x]    = x
intercalateComma (x:xs) = x ++ ", " ++ intercalateComma xs

-- | Like zipWith, but when the left list runs out we apply a fallback
-- function to the remaining right-list elements. Used so callers
-- passing more args than the registered param-type list have the extra
-- args still emitted (variadic-ish degradation).
zipWithDefault :: (b -> a -> b) -> (c -> b) -> [a] -> [c] -> [b]
zipWithDefault _ fb [] cs = map fb cs
zipWithDefault _ _  _ [] = []
zipWithDefault f fb (a:as) (c:cs) = f (fb c) a : zipWithDefault f fb as cs


-- | v0.15.2 — Can.Expr-aware variant of `zipWithDefault coerceArg
-- exprToGo`.  At call-arg sites where the target Go type is a
-- parametric record alias instantiation (`Cfg_R[Msg]`) and the
-- source is a `Can.Record` literal, route through
-- `exprToGoExpectGo` so the literal emits with the target's type
-- args directly (`Cfg_R[Msg]{ OnSubmit: func(...) Msg {...} }`).
--
-- Pre-fix the lowerer first emitted the literal generically as
-- `Cfg_R[any]{...}` (no target context) then wrapped it with
-- `any(...).(Cfg_R[Msg])` via `coerceArg`.  Go generic types are
-- nominal, so the runtime type assertion panicked with
-- `interface conversion: Cfg_R[interface {}] vs Cfg_R[Msg]`.
-- Surfaced by skydeploy's Editor (`Editor.view editorCfg` at
-- AppDetail.sky) on every Source-tab mount — pre-Stage-E this
-- worked because Cfg_R was an unparameterised struct.
--
-- Same insight applies to `Can.Lambda` at a typed `func(...) ...`
-- slot — route to `lowerTypedLambda` via `exprToGoExpectGo` so
-- the inner func emits with the slot's typed params and return.
-- The pre-existing `coerceArg` would have wrapped a generic
-- `func(any) any` body with `rt.Coerce[func(X) Y]`, working but
-- reflecting through MakeFunc on every call.
zipWithDefaultExpect :: [String] -> [Can.Expr] -> [GoIr.GoExpr]
zipWithDefaultExpect [] cs = map exprToGo cs
zipWithDefaultExpect _ [] = []
zipWithDefaultExpect (ty:tys) (e:es) =
    lowerArgExpect ty e : zipWithDefaultExpect tys es


-- | v0.15.2 — single arg lowering with target type awareness.
-- Falls through to the historic `coerceArg . exprToGo` pipeline
-- for every shape EXCEPT the two type-directed ones above.
--
-- Parametric-alias arm gated on `not containsGenericTypeParam`:
-- a target like `Cfg_R[T1]` (Sky-side polymorphic, σ didn't pin
-- the TVar at this site) would emit `Cfg_R[T1]{...}` at the
-- caller, triggering `undefined: T1` in `go build` because T1
-- names the callee's type variable, not in scope at the caller.
-- Typed-lambda arm already excludes generic param targets via
-- `all (/= "any") paramTys` (a func type containing a TVar would
-- have at least one "any" position).
lowerArgExpect :: String -> Can.Expr -> GoIr.GoExpr
lowerArgExpect ty e@(A.At _ expr)
    -- v0.15.x P6 (Phase 2) — call-arg lowering at the two
    -- type-directed slots routes via `lowerExprExpectGo` so the
    -- "call arg" structural-backbone slot threads ctx explicitly.
    -- Same ctx as the enclosing call (call args are siblings,
    -- not nested binders).
    | isParametricAliasInstantiation ty
    , not (containsGenericTypeParam ty)
    , Can.Record _ <- expr
    = lowerExprExpectGo (ctxFromIORef ()) ty e
    | Just (paramTys, _retTy) <- parseFuncType ty
    , Can.Lambda pats _ <- expr
    , length pats == length paramTys
    , all (/= "any") paramTys
    , not (containsGenericTypeParam ty)
    = lowerExprExpectGo (ctxFromIORef ()) ty e
    | otherwise
    = coerceArg (Just e) (exprToGo e) ty


-- | Over-application: the call supplied MORE args than the callee's
-- declared arity. Means the callee returns a function value which we
-- continue to apply.
--
-- Lowering: emit the regular flat call against the first `declared`
-- args, then thread the remaining args through `rt.SkyCall` — the
-- reflective dispatcher handles any return shape (typed Go `func`,
-- `func(any) any` closure, MakeFunc value).
--
--   pickIf : Bool -> (Int -> Int)
--   pickIf True 10  -- got=2, declared=1
--
-- emits as:
--
--   rt.SkyCall(pickIf(true), 10)
--
-- For N extras the dispatcher fold-applies them left-to-right:
--   rt.SkyCall(rt.SkyCall(f(a), b), c)
emitOverApplication :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitOverApplication func allArgs declared =
    let (firstK, rest) = splitAt declared allArgs
        modStr = case A.toValue func of
            Can.VarTopLevel home _ -> ModuleName.toString home
            _ -> ""
        rawName = case A.toValue func of
            Can.VarTopLevel _ name -> name
            _ -> ""
        qualName = if null modStr || modStr == "Main"
            then goSafeName rawName
            else map (\c -> if c == '.' then '_' else c) modStr
                 ++ "_" ++ goSafeName rawName
        mMangled = instanceMangledName (A.toRegion func) qualName
        callName = maybe qualName id mMangled
        baseCall = GoIr.GoCall (GoIr.GoIdent callName)
                       (coerceCallArgsAt (A.toRegion func) qualName firstK)
        applyOne acc arg = GoIr.GoCall
            (GoIr.GoQualified "rt" "SkyCall")
            [acc, exprToGo arg]
    in foldl applyOne baseCall rest


emitPartialUserCall :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitPartialUserCall func suppliedArgs missing =
    let -- Resolve callee qualified name so we can look up its typed
        -- param signature and coerce both the supplied args and the
        -- closure-captured extras.
        qualName = case A.toValue func of
            Can.VarTopLevel home name ->
                let modStr = ModuleName.toString home
                in if null modStr || modStr == "Main"
                     then name
                     else map (\c -> if c == '.' then '_' else c) modStr
                          ++ "_" ++ name
            _ -> ""
        env = getCgEnv
        paramTypes = Map.findWithDefault [] qualName
                       (Rec._cg_funcParamTypes env)
        suppliedTypes = take (length suppliedArgs) paramTypes
        -- Param types for the `missing` slots: pull from the
        -- registered paramTypes, padding with "any" when paramTypes
        -- is shorter than declared (function without typed entry in
        -- the registry). Length must be exactly `missing` so the
        -- wrapper chain length matches.
        availableExtras = drop (length suppliedArgs) paramTypes
        -- v0.13 Stage 1 fix — erase any TVar placeholders in the
        -- extra (missing) param types BEFORE using them as wrapper
        -- input types. Without this, a partial-app of a generic
        -- function (e.g. `List.filter pred`) emits a wrapper with
        -- bare TVars (`func(__pp0 []T1) any`) which Go rejects:
        -- T1 is only valid inside the kernel's generic body, not
        -- at the wrapper construction site in user code.
        extraTypes    = take missing
                          (map eraseTypeParams availableExtras
                              ++ repeat "any")
        -- v0.13 Stage 2 — typed partial-app wrapper. Reads the
        -- callee's ULTIMATE return type (scalar, after every arg
        -- applies) from `_cg_funcUltimateRetType`. Falls back to
        -- "any" when unavailable so we never emit a worse shape
        -- than the historical `func(any) any` default. Erase TVars
        -- for the same reason as extraTypes.
        ultRetType = eraseTypeParams
                        (Map.findWithDefault "any" qualName
                           (Rec._cg_funcUltimateRetType env))
        -- v0.15.2: typed-target call args via `zipWithDefaultExpect`.
        suppliedGo = zipWithDefaultExpect suppliedTypes suppliedArgs
        extraNames = [ "__pp" ++ show i | i <- [0 .. missing - 1] ]
        extraIdents = zipWith (\n ty -> coerceArg Nothing (GoIr.GoIdent n) ty)
                              extraNames extraTypes
        finalCall = GoIr.GoCall (exprToGo func) (suppliedGo ++ extraIdents)
        -- Build the curried wrapper chain from the inside out.
        -- Innermost wrapper returns `ultRetType`; each outer
        -- wrapper returns `func(P_inner) <inner_ret>`.
        wrappedExpr = foldr buildWrapper finalCall (zip [0..] (zip extraNames extraTypes))
        buildWrapper (i, (name, paramTy)) inner =
            let retTy = wrapperReturnType (missing - 1 - i)
            in GoIr.GoFuncLit
                 [GoIr.GoParam name paramTy]
                 retTy
                 [GoIr.GoReturn inner]
        -- wrapperReturnType n: type of a wrapper that has `n` more
        -- inner wrappers beneath it. 0 → ultRetType (the innermost
        -- wrapper returns the callee's scalar return type after all
        -- args apply). 1 → `func(P_innermost) ultRetType`. 2 →
        -- `func(P_inner-1) func(P_innermost) ultRetType`. Etc.
        wrapperReturnType 0 = ultRetType
        wrapperReturnType n =
            let innerParamTys = drop (missing - n) extraTypes
            in foldr (\pt acc -> "func(" ++ pt ++ ") " ++ acc)
                     ultRetType
                     innerParamTys
    in wrappedExpr


ctorToGo :: Can.CtorOpts -> ModuleName.Canonical -> String -> String -> Can.Annotation -> GoIr.GoExpr
ctorToGo _opts home typeName ctorName _annot = case ctorName of
    "Ok"      -> GoIr.GoIdent "rt.Ok[any, any]"
    "Err"     -> GoIr.GoIdent "rt.Err[any, any]"
    "Just"    -> GoIr.GoIdent "rt.Just[any]"
    "Nothing" -> GoIr.GoCall (GoIr.GoIdent "rt.Nothing[any]") []
    "True"    -> GoIr.GoBoolLit True
    "False"   -> GoIr.GoBoolLit False
    -- User-defined constructor: prefix with module path for cross-module
    -- references. `generateDeclsForDep` emits ctors as `<ModPath>_<Type>_<Ctor>`
    -- so a constructor from State.sky for type Page becomes State_Page_BoardPage.
    _ ->
        let modStr = ModuleName.toString home
        in if null modStr || modStr == "Main"
            then GoIr.GoIdent (typeName ++ "_" ++ ctorName)
            else
                let modPrefix = map (\c -> if c == '.' then '_' else c) modStr
                in GoIr.GoIdent (modPrefix ++ "_" ++ typeName ++ "_" ++ ctorName)


-- ═══════════════════════════════════════════════════════════
-- BINARY OPERATORS
-- ═══════════════════════════════════════════════════════════

-- | Convert a binary operator application to Go
binopToGo :: String -> Can.Expr -> Can.Expr -> GoIr.GoExpr
binopToGo op left right =
    -- v0.13 typed lowerer: consult HM-inferred operand types to decide
    -- Go-native vs `rt.*` any-routed.  Soundness restriction: only
    -- optimise when BOTH operands are statically typed in Go — i.e.
    -- they're literals (Int, Float, String, Bool, Char) or simple
    -- references to typed lambda params (registered in the
    -- lambda-types scope).  Runtime kernel calls (e.g.
    -- `rt.Crypto_sha256`) return `any` even when HM says the result
    -- is String, so a naive `"prefix " + rt.Crypto_sha256(data)`
    -- would fail Go's static type check.  Falls back to runtime
    -- helpers in all other cases (correct, just slower).
    let solved   = Rec._cg_solvedTypes getCgEnv
        leftTy   = inferExprType solved left
        rightTy  = inferExprType solved right
        bothAre  prim = leftTy == Just prim && rightTy == Just prim
                        && operandIsStaticallyTyped left
                        && operandIsStaticallyTyped right
        anyOf    prims = case (leftTy, rightTy) of
            (Just l, Just r) | l == r && any (l ==) prims
                             , operandIsStaticallyTyped left
                             , operandIsStaticallyTyped right
                             -> True
            _ -> False
        intTy    = ConstrainExpr.intType
        floatTy  = ConstrainExpr.floatType
        boolTy   = ConstrainExpr.boolType
        strTy    = ConstrainExpr.stringType
    in case op of
    -- Pipe operators — desugar to function application
    -- a |> f becomes f(a), but if f is already a call f(x), becomes f(x, a)
    "|>" -> pipeApply left right
    "<|" -> pipeApply right left

    -- Composition operators (>> and <<)
    ">>" -> GoIr.GoCall (GoIr.GoQualified "rt" "ComposeL") [exprToGo left, exprToGo right]
    "<<" -> GoIr.GoCall (GoIr.GoQualified "rt" "ComposeR") [exprToGo left, exprToGo right]

    -- String concat (++) when both operands type to String: emit
    -- Go-native concat which is `+` on string. List concat keeps the
    -- runtime helper (slice concat needs reflect).
    "++"
      | bothAre strTy ->
          GoIr.GoBinary "+" (exprToGo left) (exprToGo right)
      | otherwise ->
          GoIr.GoCall (GoIr.GoQualified "rt" "Concat") [exprToGo left, exprToGo right]

    -- Cons operator
    "::" -> GoIr.GoCall (GoIr.GoQualified "rt" "List_cons") [exprToGo left, exprToGo right]

    -- Equality — Go-native when both operands type to a known primitive
    -- (Go's `==` is well-defined for these). Avoids reflect dispatch
    -- through rt.Eq and the polymorphic-comparable trap.
    "==" | anyOf [intTy, floatTy, boolTy, strTy] ->
        GoIr.GoBinary "==" (exprToGo left) (exprToGo right)
    "/=" | anyOf [intTy, floatTy, boolTy, strTy] ->
        GoIr.GoBinary "!=" (exprToGo left) (exprToGo right)
    "==" -> GoIr.GoCall (GoIr.GoQualified "rt" "Eq") [exprToGo left, exprToGo right]
    "/=" -> GoIr.GoCall (GoIr.GoQualified "rt" "NotEq") [exprToGo left, exprToGo right]

    -- Arithmetic — Go-native when both operands are Int OR both Float.
    -- Mixed / unknown types fall back to rt.* helpers which reflect-
    -- dispatch.  Sky's `+`/`-`/`*`/`/` are numeric only (string concat
    -- is `++`); Go's `+` is overloaded for string but rt.Add handles
    -- both numeric cases.
    "+" | bothAre intTy || bothAre floatTy ->
        GoIr.GoBinary "+" (exprToGo left) (exprToGo right)
    "-" | bothAre intTy || bothAre floatTy ->
        GoIr.GoBinary "-" (exprToGo left) (exprToGo right)
    "*" | bothAre intTy || bothAre floatTy ->
        GoIr.GoBinary "*" (exprToGo left) (exprToGo right)
    "/" | bothAre floatTy ->
        GoIr.GoBinary "/" (exprToGo left) (exprToGo right)
    "//" | bothAre intTy ->
        GoIr.GoBinary "/" (exprToGo left) (exprToGo right)
    "+"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Add") [exprToGo left, exprToGo right]
    "-"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Sub") [exprToGo left, exprToGo right]
    "*"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Mul") [exprToGo left, exprToGo right]
    "/"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Div") [exprToGo left, exprToGo right]
    "//" -> GoIr.GoCall (GoIr.GoQualified "rt" "IntDiv") [exprToGo left, exprToGo right]

    -- Comparison operators — Go-native for known orderable primitives.
    ">"  | anyOf [intTy, floatTy, strTy] ->
        GoIr.GoBinary ">" (exprToGo left) (exprToGo right)
    "<"  | anyOf [intTy, floatTy, strTy] ->
        GoIr.GoBinary "<" (exprToGo left) (exprToGo right)
    ">=" | anyOf [intTy, floatTy, strTy] ->
        GoIr.GoBinary ">=" (exprToGo left) (exprToGo right)
    "<=" | anyOf [intTy, floatTy, strTy] ->
        GoIr.GoBinary "<=" (exprToGo left) (exprToGo right)
    ">"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Gt") [exprToGo left, exprToGo right]
    "<"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Lt") [exprToGo left, exprToGo right]
    ">=" -> GoIr.GoCall (GoIr.GoQualified "rt" "Gte") [exprToGo left, exprToGo right]
    "<=" -> GoIr.GoCall (GoIr.GoQualified "rt" "Lte") [exprToGo left, exprToGo right]

    -- Logic — Go-native when both Bool (the only valid operand type).
    "&&" | bothAre boolTy ->
        GoIr.GoBinary "&&" (exprToGo left) (exprToGo right)
    "||" | bothAre boolTy ->
        GoIr.GoBinary "||" (exprToGo left) (exprToGo right)
    "&&" -> GoIr.GoCall (GoIr.GoQualified "rt" "And") [exprToGo left, exprToGo right]
    "||" -> GoIr.GoCall (GoIr.GoQualified "rt" "Or") [exprToGo left, exprToGo right]

    -- Other operators
    _ -> GoIr.GoBinary op (exprToGo left) (exprToGo right)


-- | Apply a pipe: `value |> func` becomes `func(value)`
-- If func is already a call `f(args...)`, append value as additional arg: `f(args..., value)`
pipeApply :: Can.Expr -> Can.Expr -> GoIr.GoExpr
pipeApply valueExpr funcExpr =
    -- Reify `value |> func args` as a regular Can.Call so it goes
    -- through the same exprToGo Can.Call branch — this picks up the
    -- typed-FFI / typed-kernel migrations that would otherwise be
    -- missed by the bypass that calls exprToGo on each piece directly.
    let region = case funcExpr of A.At r _ -> r
        synth f xs = exprToGo (A.At region (Can.Call f xs))
    in case funcExpr of
        A.At _ (Can.Call innerFunc innerArgs) ->
            synth innerFunc (innerArgs ++ [valueExpr])
        _ ->
            synth funcExpr [valueExpr]


-- ═══════════════════════════════════════════════════════════
-- IF-THEN-ELSE
-- ═══════════════════════════════════════════════════════════

-- | Convert if-then-else to Go (IIFE with if-else chain)
-- | v0.13 typed lowerer: `ifToGo` takes the surrounding context's
-- expected type.  When `Just t`, the emitted IIFE is wrapped via
-- `typeIIFE` so it returns the concrete Go type instead of `any` —
-- and `typeIIFE`'s `coerceReturnExprT` recurses into nested IIFE
-- return values, so a `case`/`if`/`let` in a branch position gets
-- typed too.  `Nothing` keeps the legacy `func() any` shape (used
-- when `ifToGo` is reached from generic `exprToGo` with no
-- expected-type context).
ifToGo :: Maybe String -> [(Can.Expr, Can.Expr)] -> Can.Expr -> GoIr.GoExpr
ifToGo mExpectedGo branches elseExpr =
    let
        -- When the expected Go type is known, lower each branch body
        -- (and the else) via `exprToGoExpectGo` so a nested
        -- case/if/let in a branch gets the type threaded DIRECTLY —
        -- not just through `typeIIFE`'s return-position recursion.
        lowerBody = case mExpectedGo of
            Just gt -> exprToGoExpectGo gt
            Nothing -> exprToGo
        buildIf [] = [GoIr.GoReturn (lowerBody elseExpr)]
        buildIf ((cond, body):rest) =
            [GoIr.GoIf (toBoolExpr (exprToGo cond)) [GoIr.GoReturn (lowerBody body)] (buildIf rest)]
        raw = GoIr.GoBlock (buildIf branches) (GoIr.GoRaw "nil")
    in case mExpectedGo of
        Just gt -> typeIIFE gt raw
        Nothing -> raw


-- | Ensure an expression is a Go bool (cast from any if needed)
toBoolExpr :: GoIr.GoExpr -> GoIr.GoExpr
toBoolExpr expr = case expr of
    GoIr.GoBoolLit _ -> expr  -- already bool
    GoIr.GoCall (GoIr.GoQualified "rt" name) _
        | name `elem` ["Eq", "Gt", "Lt", "Gte", "Lte", "And", "Or"] ->
            GoIr.GoCall (GoIr.GoQualified "rt" "AsBool") [expr]
    _ -> GoIr.GoCall (GoIr.GoQualified "rt" "AsBool") [expr]


-- ═══════════════════════════════════════════════════════════
-- LET-IN
-- ═══════════════════════════════════════════════════════════

-- | Convert let-in to Go (IIFE with local declarations).  Takes the
-- surrounding context's expected type — see `ifToGo`.
letToGo :: Maybe String -> Can.Def -> Can.Expr -> GoIr.GoExpr
letToGo mExpectedGo def body =
    -- v0.13 typed lowerer: when the def binds a primitive-typed
    -- local from a statically-typed value, register it under the
    -- body's scope so binops on this local can emit Go-native.
    -- Uses `withScopedLambdaTypes` for proper push/pop — sibling
    -- let-bindings in the same function can't collide because they
    -- have different names; cross-function leakage is prevented by
    -- the scoping wrapper.
    --
    -- v0.15.x P37b — `letBindingType` is now PURE; its region
    -- lookup reads `Solve.SolvedTypes._stRegions` directly via
    -- `Solve.lookupSolvedRegion`.  No `scopeStateRef` snapshot
    -- here for region-types — the prior `let ctx = …` read was
    -- only used to plumb the IORef-backed region map into
    -- `letBindingType`, and that machinery is gone.
    let solved = Rec._cg_solvedTypes getCgEnv
        -- v0.13 typed lowerer: register a primitive-typed let-binding
        -- under the body's scope so Go-native binops fire on it.
        -- Restricted to primitives — broadening to record/ADT types
        -- is unsound here: `operandIsStaticallyTyped valExpr` can be
        -- True for a typed-local ref whose own type is a generic
        -- instantiation (`T1`) rather than a concrete struct, and the
        -- emitted Go local then isn't the record struct the typed
        -- field-access path assumes.  Primitives don't have that
        -- failure mode (int/string/bool/float/rune have no generic
        -- form).
        bindingExtras = case def of
            Can.Def (A.At _ name) [] valExpr
                | name /= "_"
                , Just t <- inferExprType solved valExpr
                , operandIsStaticallyTyped valExpr
                , isTypedPrimitive t ->
                    Map.singleton name t
            Can.TypedDef (A.At _ name) _ [] valExpr _
                | Just t <- inferExprType solved valExpr
                , operandIsStaticallyTyped valExpr
                , isTypedPrimitive t ->
                    Map.singleton name t
            -- v0.13 Stage 1 — annotated let-bound function: register
            -- its full annotation type so `goExprGoType` at HOF arg
            -- sites can σ-recover TVars from the typed sig. Critical
            -- for in-let helpers passed to `Task.andThen` / `Result.
            -- andThen` — without this, `Task.andThen readAll task`
            -- couldn't pin the kernel's `a, err, b` TVars from
            -- `readAll`'s `(*SkyDb -> Task Error …)` annotation,
            -- forcing a `rt.Coerce[func(any) rt.SkyTask[any, any]]`
            -- wrap.
            Can.TypedDef (A.At _ name) _ typedPats _ retTy
                | not (null typedPats)
                , name /= "_" ->
                    let fullTy = foldr T.TLambda retTy (map snd typedPats)
                    in Map.singleton name fullTy
            -- v0.13 Stage 1 — unannotated let-bound function: try
            -- to recover its full HM-solved type from `solvedTypes`.
            -- If solvedTypes has an entry under the let-binding's
            -- name AND the type is a TLambda, register it. Closes
            -- the polymorphic-helper class for in-let helpers passed
            -- to HOFs (`Task.andThen writeAll`). When solvedTypes
            -- doesn't carry the let-binding's type (most cases —
            -- HM scopes let names locally), this is a no-op fallback
            -- to the existing wrap-on-call-site behaviour.
            Can.Def (A.At _ name) pats _
                | not (null pats)
                , name /= "_"
                , Just t <- Solve.lookupSolvedVar name solved
                , case t of { T.TLambda _ _ -> True; _ -> False } ->
                    Map.singleton name t
            _ -> Map.empty
        -- When the expected Go type is known, lower the let-body via
        -- `exprToGoExpectGo` so a nested case/if/let in the body gets
        -- the type threaded directly.
        --
        -- v0.15.x P37b — let-body slot cascade RESUMED.  P6
        -- reverted this site because `letBindingType` snapshotted
        -- `scopeStateRef` lazily, racing the ctx-aware wrapper's
        -- write/restore around the body's lowering and blackholing
        -- on skyshop.  Now that `letBindingType` is pure (its
        -- region lookup reads `Solve.SolvedTypes._stRegions`
        -- directly), the deferred-thunk cycle is broken and the
        -- body can route through the explicit-ctx wrapper.
        --
        -- v0.15.x P38 — caller ctx now flows through
        -- `snapshotCallerCtx` so the WHNF force happens at the
        -- snapshot site (see helper Haddock for the P37b PR #91
        -- thunk hazard the seq pattern was designed to close).
        bodyCtx = snapshotCallerCtx ()
        lowerBody = case mExpectedGo of
            Just gt -> lowerExprExpectGo bodyCtx gt
            Nothing -> lowerExpr bodyCtx
        -- v0.13 typed lowerer: type the let-binding's RHS too.  When
        -- the bound value has a known HM type, lower its RHS via
        -- `exprToGoExpect` — `exprToGoExpect`'s own emittability
        -- gate keeps it sound (un-nameable types fall back to plain
        -- `exprToGo`).  This types `let x = <case-expr> in …` so the
        -- RHS IIFE is `func() T` not `func() any`.
        solvedTypes = solved
        defStmts = case def of
            Can.Def (A.At _ dn) [] valExpr
                | dn /= "_"
                , Just dt <- letBindingType solvedTypes dn valExpr ->
                    letBindStmts dn (exprToGoExpect dt valExpr)
            Can.TypedDef (A.At _ dn) _ [] valExpr _
                | Just dt <- letBindingType solvedTypes dn valExpr ->
                    letBindStmts dn (exprToGoExpect dt valExpr)
            _ -> defToStmts def
        bodyGo = if Map.null bindingExtras
            then lowerBody body
            else withScopedLambdaTypes bindingExtras (lowerBody body)
        raw = GoIr.GoBlock defStmts bodyGo
    in case mExpectedGo of
        Just gt -> typeIIFE gt raw
        Nothing -> raw


-- | v0.13 typed lowerer: is this Sky type one of the primitives we
-- have Go-native binop support for?  Used as a gate for registering
-- typed lambda / let locals so binop emission stays sound (only
-- primitives where Go's static type matches Sky's HM type and a
-- direct Go-native operation produces the expected semantics).
isTypedPrimitive :: T.Type -> Bool
isTypedPrimitive t =
    t == ConstrainExpr.intType    ||
    t == ConstrainExpr.floatType  ||
    t == ConstrainExpr.stringType ||
    t == ConstrainExpr.boolType   ||
    t == ConstrainExpr.charType


-- | Total split-on-char (returns [s] when char doesn't occur).
splitOn :: Char -> String -> [String]
splitOn c = foldr f [[]]
  where
    f x acc@(cur:rest)
      | x == c    = [] : acc
      | otherwise = (x:cur) : rest
    f _ [] = [[]]


-- | Convert a definition to Go statements
-- | v0.13 typed lowerer: lower a `let _ = X` discard body.  When X
-- is a control-flow expression (`case` / `if` / `let`) and its
-- HM-inferred type renders to a real emittable Go type, lower it
-- via `exprToGoExpectGo` so the discard IIFE is `func() <T>` rather
-- than `func() any`.  Falls back to plain `exprToGo` for non-control-
-- flow values (a bare call isn't an IIFE anyway) and for un-nameable
-- types.  The caller still wraps the result in `rt.AnyTaskRun`, so
-- the auto-force semantics are untouched — only the IIFE's own
-- return type is tightened.
loweredDiscard :: Can.Expr -> GoIr.GoExpr
loweredDiscard body@(A.At _ inner) = case inner of
    Can.Case{} -> typed
    Can.If{}   -> typed
    Can.Let{}  -> typed
    _          -> exprToGo body
  where
    typed = case inferExprType (Rec._cg_solvedTypes getCgEnv) body of
        Just t ->
            let gt = solvedTypeToGo t
            in if isEmittableGoType gt
                 then exprToGoExpectGo gt body
                 else exprToGo body
        Nothing -> exprToGo body


-- | v0.15.3 — register a `main`-body let-binding's HM type into
-- the global lambda-types map so downstream `goExprGoType` calls
-- can resolve the binding's static Go type at call sites.
--
-- Restricted to types whose Go rendering is a real, named type
-- (record-alias instantiation, primitive, or a func type with a
-- non-`any` shape) — registering an `any`-rendering type would
-- pollute the lookup with useless entries.
--
-- Uses the unscoped scope-state write because main's body
-- lowering is a `concatMap defToStmts` chain with no expression-
-- level scoping seam to thread through.  The leak is bounded —
-- `main` is the last function emitted for the entry module, and
-- the registry is read fresh on each compile.
registerMainLetBindingType :: Solve.SolvedTypes -> Can.Def -> ()
registerMainLetBindingType types def =
    -- v0.15.x P37b — `letBindingType` is now pure; the prior
    -- `scopeStateRef` snapshot only fed its region lookup, and
    -- that path now reads `Solve.SolvedTypes._stRegions` directly.
    -- The `scopeStateRef` write to register the binding's type
    -- against `_lc_lambdaTypes` is unchanged.
    case def of
        Can.Def (A.At _ name) [] body
            | name /= "_"
            , Just t <- letBindingType types name body ->
                unsafePerformIO $ do
                    modifyIORef scopeStateRef (LC.withLambdaTypes (Map.singleton name t))
                    return ()
        Can.TypedDef (A.At _ name) _ [] body _
            | name /= "_"
            , Just t <- letBindingType types name body ->
                unsafePerformIO $ do
                    modifyIORef scopeStateRef (LC.withLambdaTypes (Map.singleton name t))
                    return ()
        _ -> ()


-- | v0.15.3: recover a zero-param let-binding's HM-solved type.
--
-- Layered preference:
--   1. `lookupRegionType (regionOf body)` — the v0.15 Stage A
--      solver writes per-region types into `scopeStateRef`'s
--      `_lc_regionTypes` field (v0.15.5 PR 3 — was its own IORef
--      pre-consolidation), keyed by source region.  This is the
--      CORRECT lookup for a
--      let-binding's RHS — it cannot collide with a sibling
--      top-level binding of the same name (e.g. `let check =
--      cfg.field` inside a library, where `check` is also a
--      top-level test helper in the consuming module).
--   2. `inferExprType solvedTypes body` — falls back when the
--      solver didn't record the region (synthetic code, FFI
--      derivation).  inferExprType's Can.Record arm bails on
--      Can.Lambda fields (returns Nothing) — those records are
--      caught by (1) above; this fallback covers simpler shapes.
--   3. `Map.lookup name solvedTypes` — last-ditch lookup for
--      bindings whose region wasn't recorded AND whose body
--      doesn't infer.  Gated on the name NOT matching a module-
--      level binding (collision check); the gate is implemented
--      by requiring the type match a record/tuple/alias shape,
--      since function-typed module bindings (the common shadow
--      case — `check : ... -> ... -> Result`) cannot be a
--      zero-param let-binding's resolved type anyway.
--
-- All candidates gated on `isEmittableGoType` so an un-nameable
-- type cannot reach the typed path; untyped fallback stays safe.
-- | v0.15.x P37b — `letBindingType` is now PURE end-to-end.
--
-- Region lookup reads from `Solve.SolvedTypes._stRegions` (the
-- per-region HM type map P37a started populating alongside the
-- per-name env).  No `unsafePerformIO`, no IORef snapshot, no
-- `LC.LowerCtx` parameter — the function depends only on its
-- explicit arguments.  This breaks the deferred-thunk cycle that
-- blackholed P6's record-field / list-element / let-body cascade
-- migrations under skyshop + CoerceArgParametricSpec: pre-P37b,
-- `letBindingType ctx …` was a suspended thunk that read
-- `scopeStateRef` lazily, racing the ctx-aware wrapper's
-- write/restore around the body's lowering; pure data eliminates
-- the seam.
--
-- The two-axis gate (body-shape whitelist + type-emittability) is
-- preserved here.  Removing the whitelist exposes a region-map
-- pollution bug — the per-region map can return a type from an
-- unrelated sibling-region binding when keys collide across
-- modules in the merged snapshot.  Tracked as audit residual #8 +
-- #14 in `docs/improvement-plan-v0.16.md`.
letBindingType :: Solve.SolvedTypes -> String -> Can.Expr -> Maybe T.Type
letBindingType solvedTypes _name body@(A.At r _) =
    -- v0.15.3 — Two-axis gate.
    --
    -- (A) Body-shape gate: ONLY route typed for body shapes where
    -- the typed routing materially changes the emission:
    --   * Can.Record  — `Setup_R[Msg]{...}` vs `Setup_R[any]{...}`
    --     (the primary motivation; closes the editor panic class).
    --   * Can.Lambda  — typed func signature emission.
    --   * Can.If / Can.Case / Can.Let — typed IIFE return so a
    --     nested control-flow expression keeps its result type
    --     through the let-binding boundary.
    --
    -- Other body shapes (Can.Call, Can.Access, Can.VarLocal, …)
    -- are LEFT UNTYPED because their generic lowerers already
    -- emit correctly-typed Go, and forcing typed routing on top
    -- wraps the result in helpers (`rt.AsListT[T]`) that misbehave
    -- for FFI results / Result-wrapped values / kernel returns.
    -- Concrete regression that fired in baseline sweep:
    --   `decimals = Ffi.callPure "Money_allocate" […]` — typed
    --   gate emitted `rt.AsListT[Decimal](rt.Ffi_callPure(…))`
    --   which stripped the Result-Ok wrap incorrectly and yielded
    --   an empty slice; downstream `List.map` returned [] and
    --   `Money.allocate` silently produced no parts.
    --
    -- (B) Type gate: the rendered Go type must be emittable AND
    -- not contain the `any` token anywhere (a `func(P) any` slot
    -- typed routing would strip a sharper source `func(P) T1`).
    --
    -- v0.15.x cycle-3 audit gap C13 cross-reference -----------
    --
    -- The body-shape whitelist below is a WORKAROUND for the
    -- string-based `coerceArg` machinery (audit Prior #7 / Gap
    -- A12 in `docs/v0.15.x-hardening/audits/CYCLE-03-auditor.md`).
    -- Specific failing path on non-whitelisted shapes:
    --
    --   coerceArg → coerceToFieldType → rt.AsListT[T]
    --
    -- — string-parsing the type slot from a typed-routed call
    -- whose underlying value is a `Result`-wrapped FFI return
    -- (e.g. `Ffi.callPure "Money_allocate"`) emits a list-cast
    -- that strips the `Ok` wrap and yields `[]`, silently
    -- breaking downstream `List.map` etc.
    --
    -- Locked by the live regression gate in
    -- `examples/00-standard-libs/src/Main.sky:569` (the
    -- `Money.allocate 3 (Money.fromMajor USD 100)` assertion).
    -- Drop the whitelist without P10/P11 and that assertion
    -- silently returns `[]` instead of three parts.
    --
    -- Tracking: cycle-1 P9 ("drop canRouteTyped whitelist") is
    -- DEFERRED to post-P37b per cycle-3 plan
    -- (`docs/v0.15.x-hardening/plans/CYCLE-03-planner.md` tag
    -- allocation table).  The proper structural fix is P10/P11
    -- (structural `GoType` ADT replaces string-based parsing in
    -- `coerceArg` / `coerceToFieldType`) — once those land, the
    -- whitelist becomes droppable and `canRouteTyped` can collapse
    -- to `True` for every shape.  Until then DO NOT broaden this
    -- list; broaden it and the Money regression returns silently.
    let canRouteTyped = case body of
            A.At _ (Can.Record _) -> True
            A.At _ (Can.Lambda _ _) -> True
            A.At _ (Can.If _ _) -> True
            A.At _ (Can.Case _ _) -> True
            A.At _ (Can.Let _ _) -> True
            A.At _ (Can.LetRec _ _) -> True
            _ -> False
        viaRegion   = Solve.lookupSolvedRegion r solvedTypes
        viaInferred = inferExprType solvedTypes body
        containsAny s = goAny 0 s
          where
            goAny _ [] = False
            goAny d ('a':'n':'y':rest)
                | atTokenBoundary d rest = True
                | otherwise = goAny d rest
            goAny d ('[':rest) = goAny (d+1) rest
            goAny d (']':rest) = goAny (max 0 (d-1)) rest
            goAny d (_:rest) = goAny d rest
            atTokenBoundary _ [] = True
            atTokenBoundary _ (c:_) = not (isIdentChar c)
            isIdentChar c =
                (c >= 'a' && c <= 'z') ||
                (c >= 'A' && c <= 'Z') ||
                (c >= '0' && c <= '9') || c == '_'
        emittable t =
            let goTy = solvedTypeToGo t
            in isEmittableGoType goTy
               && goTy /= "any"
               && not (containsAny goTy)
    in if not canRouteTyped
        then Nothing
        else case viaRegion of
            Just t | emittable t -> Just t
            _ -> case viaInferred of
                Just t | emittable t -> Just t
                _ -> Nothing


defToStmts :: Can.Def -> [GoIr.GoStmt]
defToStmts def = case def of
    Can.DestructDef pat valExpr ->
        let tmp = "__destruct__"
            (A.At _ p) = pat
            valStmt   = GoIr.GoShortDecl tmp (exprToGo valExpr)
            sink      = GoIr.GoAssign "_" (GoIr.GoIdent tmp)
            bindStmts = patternBindings tmp p
        in valStmt : sink : bindStmts

    Can.Def (A.At _ name) [] body ->
        if name == "_"
        then
            -- Auto-force `let _ = X` so when X is a Task thunk
            -- (`func() any` per Sky's v0.9.6 effect-boundary audit)
            -- the side effect actually fires. Without this, the
            -- discard binding would silently skip the Task — the
            -- exact footgun the two-tier doctrine was designed to
            -- avoid for println / Slog. With this in place, we can
            -- migrate println / Slog / Time / Os.* to Task and the
            -- pervasive `let _ = println "step"` debug-trace pattern
            -- keeps working unchanged.
            --
            -- rt.AnyTaskRun gracefully handles non-Task input too
            -- (passes through as Ok-wrapped value), so wrapping at
            -- every discard site is safe even when the body is a
            -- pure expression. Negligible runtime cost (one
            -- type-assertion).
            --
            -- v0.13 typed lowerer: when the discarded value is a
            -- control-flow expression (`case` / `if` / `let`), lower
            -- it via `exprToGoExpectGo` so the IIFE is `func() <T>`
            -- instead of `func() any`.  The discard's HM type comes
            -- from `inferExprType`; the result is still handed to
            -- `rt.AnyTaskRun` (whose param is `any`) so the auto-force
            -- semantics are unchanged — only the IIFE's own return
            -- type is tightened.
            [GoIr.GoAssign "_"
                (GoIr.GoCall (GoIr.GoQualified "rt" "AnyTaskRun")
                    [loweredDiscard body])]
        else
            -- v0.15.3: prefer the HM-solved type for the binding
            -- over `inferExprType`. `solvedTypes` carries the fully
            -- resolved type (including record-with-lambda-field
            -- cases that `inferExprType` returns Nothing for —
            -- `Can.Lambda _ _ -> Nothing` in its arm). When solved
            -- type renders to a real Go type, route through
            -- `exprToGoExpect` so a `Setup_R{...}` literal emits
            -- as `Setup_R[Msg]{...}` instead of `Setup_R[any]{...}`.
            -- See test-files/v0.15-stress/src/Widget/Form.sky for
            -- the regression case that closed this gap.
            --
            -- v0.15.x P37b — `letBindingType` is pure end-to-end;
            -- no `scopeStateRef` snapshot needed here.  The region
            -- map flows in through `Solve.SolvedTypes._stRegions`
            -- (populated by P37a for every solver entry point).
            let solved = Rec._cg_solvedTypes getCgEnv
            in case letBindingType solved name body of
                Just dt ->
                    letBindStmts name (exprToGoExpect dt body)
                Nothing ->
                    letBindStmts name (exprToGo body)

    Can.Def (A.At _ name) params body ->
        -- v0.13 Stage 1 — for unannotated multi-pattern let-defs,
        -- look up the def's HM-inferred type and use it for typed
        -- params + return. Falls back to all-`any` when HM has no
        -- entry. Closes the user-code let-bound helper class of
        -- adapters (e.g. 18-job-queue's `insertRow db ts = …`
        -- inside saveSnapshot).
        let solved = Rec._cg_solvedTypes getCgEnv
            inferredTy = Solve.lookupSolvedVar name solved
            (paramTys, retTy) = case inferredTy of
                Just ty -> splitTLambda (length params) ty
                Nothing -> (replicate (length params) Nothing, Nothing)
            mkParam (pat, mTy) =
                let raw = patternToParam pat
                in case mTy of
                    Just t ->
                        let goTy = solvedTypeToGo t
                        in if isEmittableGoType goTy
                              && not (isGenericTypeParam goTy)
                            then case raw of
                                GoIr.GoParam pn _ -> GoIr.GoParam pn goTy
                            else raw
                    Nothing -> raw
            goParams = map mkParam (zip params paramTys)
            retGoTy = case retTy of
                Just t ->
                    let goTy = solvedTypeToGo t
                    in if isEmittableGoType goTy
                          && not (isGenericTypeParam goTy)
                        then goTy
                        else "any"
                Nothing -> "any"
            bodyExpr = if retGoTy == "any"
                then exprToGo body
                else exprToGoExpectGo retGoTy body
        in letBindStmts name
            (GoIr.GoFuncLit goParams retGoTy [GoIr.GoReturn bodyExpr])

    Can.TypedDef (A.At _ name) _ [] body _ ->
        letBindStmts name (exprToGo body)

    Can.TypedDef (A.At _ name) _ typedPats body retType ->
        -- v0.13 Stage 1 — multi-pattern annotated let-def: use the
        -- typed annotation per-pattern + the return type instead of
        -- the all-`any` default. Critical for let-bound helpers in
        -- user code (e.g. 18-job-queue's `insertRow db ts = …` in
        -- saveSnapshot) whose call sites benefit from typed flow.
        let typedGoParams = map typedPatToParam typedPats
            retGoTy = solvedTypeToGo retType
            -- isEmittableGoType is the same gate `exprToGoExpectGo`
            -- uses; fall back to "any" return + plain `exprToGo`
            -- body when return can't be safely typed.
            (effectiveRet, bodyExpr) =
                if isEmittableGoType retGoTy
                    then (retGoTy, exprToGoExpectGo retGoTy body)
                    else ("any", exprToGo body)
        in letBindStmts name
            (GoIr.GoFuncLit typedGoParams effectiveRet [GoIr.GoReturn bodyExpr])


-- ═══════════════════════════════════════════════════════════
-- CASE-OF
-- ═══════════════════════════════════════════════════════════

-- | Convert case-of to Go (IIFE with switch or if-chain).  Takes the
-- surrounding context's expected type — see `ifToGo`.  When `Just t`,
-- the IIFE is wrapped via `typeIIFE` so it returns the concrete Go
-- type and every branch `return` is coerced; nested IIFE return
-- values recurse.
caseToGo :: Maybe String -> Can.Expr -> [Can.CaseBranch] -> GoIr.GoExpr
caseToGo mExpectedGo subject branches =
    let
        goSubject = exprToGo subject
        subjectType = detectSubjectType branches
        -- v0.13.x: derive the subject's concrete typed shape from
        -- HM inference when available. The legacy `MaybeCoerce[any]`
        -- / `ResultCoerce[any, any]` collapse loses the inner type —
        -- a `Just n` binding then has Go type `any` and downstream
        -- `Sky_Test_equal[T1](42, n)` cannot pick T1=int because n
        -- disagrees with the literal int. By coercing through the
        -- inferred typed shape and routing the subject as `_tFfi`
        -- (direct field access), `n` becomes `int` and Go's type
        -- inference works through Test.equal and friends.
        solvedTypes = Rec._cg_solvedTypes getCgEnv
        -- Cycle 3 task #330 / Dev P40 (skyshop Db.snapshotToDict panic):
        -- when the subject is a bare variable reference, treat the
        -- per-region HM type map as authoritative and do NOT fall back
        -- to `inferExprType`.  `inferExprType` on `Can.VarLocal name`
        -- routes through `Solve.lookupSolvedVar name solvedTypes`, which
        -- reads the FLAT name -> type env shared across the whole
        -- compilation unit.  When the same lambda-param name (e.g. `r`
        -- in `Result.withDefault def r`) appears in BOTH the polymorphic
        -- stdlib definition AND a user-side helper that pins it to a
        -- concrete type (e.g. `Result Error String`), the env stores
        -- the pinned shape and lowering the polymorphic body picks
        -- THAT up when computing the case-subject coercion.  Concrete
        -- fallout: `__subject_tFfi := rt.ResultCoerce[Sky_Core_Error_Error,
        -- string](r)` gets baked into the generic
        -- `Sky_Core_Result_withDefault[T1 any]` body, and every
        -- monomorphised instance with `T1 != string` panics at runtime
        -- with `coerceInner: type mismatch`.
        --
        -- `lookupSolvedRegion` is keyed by `A.Region`, so the lookup is
        -- per source location and cannot be polluted by an unrelated
        -- binding that happens to share the variable's name.  For
        -- subject shapes that don't go through the polluted name-env
        -- (literals, calls, accessors, …) the legacy `inferExprType`
        -- path stays unchanged — those never poll `_stEnv` for a
        -- same-named lambda param, and routing them through the
        -- region map can regress cases where the region resolved to
        -- an anonymous-record-typed shape that has no emitted Go
        -- struct decl (e.g. `case Decode.decodeString d json of …`
        -- when the decoder targets `{ name : String, age : Int }`:
        -- the region knows the full record shape, but
        -- `solvedTypeToGo` synthesises an `Anon_R_…` name that the
        -- alias index only emits a decl for when there's a source-
        -- level alias — caught by `test-files/json-pipeline-test.sky`).
        (A.At subjectRegion subjectExpr) = subject
        isBareNameRef = case subjectExpr of
            Can.VarLocal _      -> True
            Can.VarTopLevel _ _ -> True
            _                   -> False
        inferredSubjectGoType =
            let bySearch t =
                    let s = solvedTypeToGo t
                    in if isConcreteResultOrMaybe s then Just s else Nothing
            in if isBareNameRef
                then case Solve.lookupSolvedRegion subjectRegion solvedTypes of
                        Just t  -> bySearch t
                        Nothing -> Nothing
                else case inferExprType solvedTypes subject of
                        Just t  -> bySearch t
                        Nothing -> Nothing
        -- Wrap in `any(...)` before asserting so the assertion works
        -- whether the expression is already typed (e.g. a typed Sky
        -- function returning SkyResult[IoError, string]) or `any`
        -- (legacy `any`-returning helpers). Without the `any()` wrap,
        -- Go rejects type-asserting a concrete struct to another.
        anyWrapped e = GoIr.GoCall (GoIr.GoIdent "any") [e]
        -- T4: when the subject type is a parametric Sky container
        -- (SkyResult[any,any] / SkyMaybe[any]), use the ResultCoerce /
        -- MaybeCoerce runtime helpers instead of a plain type assertion.
        -- This handles the case where the source is already typed with
        -- different generic params (e.g. SkyResult[any, string]) — a
        -- plain `.(SkyResult[any, any])` runtime-fails because the
        -- generic instantiations are distinct Go types.
        coerceSubject typeName e
            | Just _ <- stripParametric "rt.SkyResult" typeName, isTypedFfiCall e =
                -- P7: typed-FFI source returns SkyResult[string, A]
                -- directly. Leave __subject at its concrete type —
                -- bindCtorArg detects this via the same predicate
                -- and emits `__subject.OkValue` without a
                -- SkyResult[any,any] assertion. Net: zero runtime
                -- boxing between FFI and case body.
                e
            -- v0.13.x: HM inferred a concrete `SkyResult[E, T]` for
            -- the subject. Coerce through THAT shape (not the
            -- default `[any, any]`) and let bindCtorArg do direct
            -- `.OkValue` access via the `_tFfi` subject naming.
            | Just _ <- stripParametric "rt.SkyResult" typeName
            , Just inferred <- inferredSubjectGoType
            , Just params <- stripParametric "rt.SkyResult" inferred =
                GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]")) [e]
            | Just params <- stripParametric "rt.SkyResult" typeName =
                GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]")) [e]
            | Just _ <- stripParametric "rt.SkyMaybe" typeName, isTypedFfiCall e =
                e
            | Just _ <- stripParametric "rt.SkyMaybe" typeName
            , Just inferred <- inferredSubjectGoType
            , Just inner <- stripParametric "rt.SkyMaybe" inferred =
                GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [e]
            | Just inner <- stripParametric "rt.SkyMaybe" typeName =
                GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [e]
            | otherwise =
                -- Strict assertion: case-on-ADT subjects must be the
                -- expected SkyADT type. If a non-ADT (e.g. a function
                -- value snuck through an HM gap) reaches here, the
                -- runtime panic IS the bug-discovery signal — fix
                -- the HM gap, don't soften the runtime. Sky's
                -- "if it compiles, it works" promise puts the
                -- type-soundness floor at HM, not at runtime
                -- tolerance.
                GoIr.GoTypeAssert (anyWrapped e) typeName


        -- Peek through the GoExpr tree for sources whose Go type is
        -- known to be a concrete SkyResult / SkyMaybe struct (not an
        -- `any` interface), so the case-subject emission can elide
        -- the ResultCoerce reflect dance. Two recognisers:
        --   * Typed FFI calls (`rt.Go_X_yT(...)`) — fixed naming
        --     convention, registered in typedFfiWrapperSet.
        --   * Sky top-level function calls whose return type starts
        --     with `rt.SkyResult[` or `rt.SkyMaybe[` per the codegen
        --     env's funcRetType map (populated by HM inference).
        isTypedFfiCall expr = case expr of
            GoIr.GoCall (GoIr.GoQualified "rt" fnName) _
                | take 3 fnName == "Go_"
                , not (null fnName)
                , last fnName == 'T'
                , Set.member fnName typedFfiWrapperSet
                -> True
            GoIr.GoCall (GoIr.GoIdent qualName) _
                | Just retTy <- Map.lookup qualName funcRetTypeMap
                , isConcreteResultOrMaybe retTy
                -> True
            GoIr.GoCall (GoIr.GoIdent qualName) _
                | Just (_, _, retTy) <- Map.lookup qualName inferredSigMap
                , isConcreteResultOrMaybe retTy
                -> True
            -- v0.13 typed lowerer: a bare `GoIdent name` referring to a
            -- typed local (registered in the lambda-types scope with a
            -- concrete Maybe/Result type) is also statically typed —
            -- skip the ResultCoerce/MaybeCoerce reflect dance and let
            -- bindCtorArg emit direct `.OkValue` / `.JustValue` access.
            GoIr.GoIdent name
                | Just t <- lookupLambdaType name
                , let goTy = solvedTypeToGo t
                , isConcreteResultOrMaybe goTy
                -> True
            _ -> False

        funcRetTypeMap = Rec._cg_funcRetType getCgEnv
        inferredSigMap = Rec._cg_funcInferredSigs getCgEnv

        isConcreteResultOrMaybe t =
            let isResult = "rt.SkyResult[" `List.isPrefixOf` t
                         && not ("rt.SkyResult[any, any]" `List.isPrefixOf` t)
                isMaybe  = "rt.SkyMaybe[" `List.isPrefixOf` t
                         && not ("rt.SkyMaybe[any]" `List.isPrefixOf` t)
            in isResult || isMaybe
        -- P7: typed-FFI-source subjects use a distinct name so
        -- bindCtorArg knows to skip the `any().(SkyResult[any,any])`
        -- assertion step. Saves one boxing per typed case match.
        --
        -- v0.13 typed lowerer: custom-ADT subjects (non-Result/Maybe)
        -- ALWAYS go through `coerceSubject`'s `GoTypeAssert (any e)
        -- typeName` path, so `__subject` is statically the ADT struct
        -- type (`rt.SkyADT` alias).  Mark them `__subject_tAdt` so
        -- `bindCtorArg` reads `.Fields[idx]` directly instead of
        -- routing through `rt.AdtField(any(subject), idx)` (reflect).
        subjectName =
            case subjectType of
                Just typeName
                    | isJust (stripParametric "rt.SkyResult" typeName) && isTypedFfiCall goSubject
                    -> "__subject_tFfi"
                    | isJust (stripParametric "rt.SkyMaybe" typeName) && isTypedFfiCall goSubject
                    -> "__subject_tFfi"
                    -- v0.13.x: HM-inferred concrete typed Maybe/Result
                    -- gets the same direct-field-access path. The
                    -- ResultCoerce/MaybeCoerce step above already
                    -- gave us a struct-typed `__subject` (e.g.
                    -- `rt.SkyMaybe[int]`); bindCtorArg's `_tFfi`
                    -- branch then emits `.JustValue` / `.OkValue`
                    -- which preserves the typed inner.
                    | isJust (stripParametric "rt.SkyResult" typeName)
                    , Just _ <- inferredSubjectGoType
                    -> "__subject_tFfi"
                    | isJust (stripParametric "rt.SkyMaybe" typeName)
                    , Just _ <- inferredSubjectGoType
                    -> "__subject_tFfi"
                    | isNothing (stripParametric "rt.SkyResult" typeName)
                    , isNothing (stripParametric "rt.SkyMaybe" typeName)
                    -> "__subject_tAdt"
                _ -> "__subject"
        subjectDecl = case subjectType of
            Just typeName ->
                GoIr.GoShortDecl subjectName (coerceSubject typeName goSubject)
            Nothing ->
                GoIr.GoShortDecl subjectName goSubject
        -- Cycle 4 D2: a case whose only branch is `_ -> ...` (or
        -- `var -> ...` where `var` is not referenced in the body)
        -- never reads `__subject`, and Go rejects the unused
        -- declaration with `declared but not used: __subject`.
        -- Always emit a blank-identifier discard right after the
        -- subject declaration — it both pacifies Go for the
        -- catchall-only shape and matches Sky's `let _ = TaskExpr`
        -- auto-force convention for "keep evaluated, ignore result".
        subjectDiscard = GoIr.GoExprStmt
            (GoIr.GoRaw ("_ = " ++ subjectName))
        branchStmts = concatMap (caseBranchToStmts subjectName) branches
        -- P3: exhaustiveness is verified before codegen, so this arm is
        -- statically unreachable. Audit P0-5: route through
        -- rt.Unreachable instead of a raw panic so any future
        -- exhaustiveness-vs-codegen drift surfaces as a clean Err at
        -- the Task boundary (rt's panic-recovery catches the panic
        -- that Unreachable raises) rather than killing the process.
        -- The site identifier lets on-call locate the originating
        -- case block in logs.
        unreachableStmt = GoIr.GoExprStmt
            (GoIr.GoRaw ("_ = rt.Unreachable(\"case/" ++ subjectName ++ "\")"))
        raw = GoIr.GoBlock
                (subjectDecl : subjectDiscard : branchStmts ++ [unreachableStmt])
                (GoIr.GoRaw "nil")  -- unreachable, branches return
    in case mExpectedGo of
        Just gt -> typeIIFE gt raw
        Nothing -> raw


-- | Detect the Go type of the case subject from the patterns
detectSubjectType :: [Can.CaseBranch] -> Maybe String
detectSubjectType branches =
    case branches of
        (Can.CaseBranch (A.At _ pat) _ : _) -> patternGoType pat
        _ -> Nothing
  where
    patternGoType (Can.PCtor home typeName union ctorName _ _)
        | ctorName == "Ok" || ctorName == "Err" = Just "rt.SkyResult[any, any]"
        | ctorName == "Just" || ctorName == "Nothing" = Just "rt.SkyMaybe[any]"
        | Can._u_opts union == Can.Enum = Nothing  -- Enum: compare int directly
        | otherwise =
            -- Qualify with the home-module prefix so cross-module ADT
            -- assertions reference the dep-emitted struct type.
            let modStr = ModuleName.toString home
            in Just $ if null modStr || modStr == "Main"
                then typeName
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ typeName
    patternGoType (Can.PBool _) = Nothing  -- bool doesn't need assertion
    patternGoType (Can.PInt _) = Nothing
    patternGoType (Can.PStr _) = Nothing
    patternGoType _ = Nothing


-- | Convert a case branch to Go if-statement
caseBranchToStmts :: String -> Can.CaseBranch -> [GoIr.GoStmt]
caseBranchToStmts subject =
    caseBranchToStmtsWith subject (\body -> [GoIr.GoReturn (exprToGo body)])


-- | Same as `caseBranchToStmts` but with a customisable leaf
-- emitter — used by TCO codegen to substitute the regular
-- `return exprToGo body` with a `<assigns>; continue` shape for
-- tail self-calls.  The default leaf emitter (`caseBranchToStmts`)
-- is unchanged.
caseBranchToStmtsWith
    :: String
    -> (Can.Expr -> [GoIr.GoStmt])
    -> Can.CaseBranch
    -> [GoIr.GoStmt]
caseBranchToStmtsWith subject leafFn (Can.CaseBranch pat body) =
    let
        (A.At _ patInner) = pat
        cond = patternCondition subject patInner
        bindings = patternBindings subject patInner
        bodyStmts = bindings ++ leafFn body
    in
    case cond of
        Nothing -> bodyStmts  -- always matches (PVar, PAnything)
        Just condExpr -> [GoIr.GoIf condExpr bodyStmts []]


-- | v0.14.x TCO: lower a tail-recursive function body to GoStmts
-- that go INSIDE a `GoForever` wrapper.  Each tail self-call is
-- emitted as `<param reassignments>; continue`; every other tail
-- position emits a regular `return`.
--
-- Scope: handles `Can.Case` and `Can.If` in tail position
-- (recursively).  Other expression shapes at tail position fall
-- through to regular `GoReturn (exprToGo body)`.  Mutual
-- recursion + `Can.Let` rebindings of the function name are out
-- of scope — `isTailRecursive` rules those out before this is
-- called.
tcoBodyStmts
    :: ModuleName.Canonical
    -> String
    -> Int
    -> [String]
    -> [String]   -- param Go types (matching paramNames order)
    -> String     -- function return Go type (for leaf-return coercion)
    -> Can.Expr
    -> [GoIr.GoStmt]
tcoBodyStmts home fnName arity paramNames paramGoTys goRetType = lowerTail
  where
    lowerTail :: Can.Expr -> [GoIr.GoStmt]
    lowerTail (A.At _ e) = case e of
        Can.Case subj branches ->
            -- Coerce the case-subject when the patterns require a
            -- typed shape (ADT `.Tag` access, SkyMaybe / SkyResult
            -- destructure).  For list / primitive patterns
            -- (`detectSubjectType` returns Nothing) the existing
            -- `caseBranchToStmts` path runs on `any` because the
            -- pattern conditions use `len(rt.AsList(__subject))`
            -- which accepts an any-typed source.
            let subjectName = "__tco_subject"
                rawSubjExpr = exprToGo subj
                anyWrap e0 = GoIr.GoCall (GoIr.GoIdent "any") [e0]
                subjExpr = case detectSubjectType branches of
                    Nothing -> rawSubjExpr
                    Just typeName
                        | take (length ("rt.SkyMaybe" :: String)) typeName
                            == "rt.SkyMaybe" ->
                            GoIr.GoCall
                                (GoIr.GoIdent "rt.MaybeCoerce[any]") [rawSubjExpr]
                        | take (length ("rt.SkyResult" :: String)) typeName
                            == "rt.SkyResult" ->
                            GoIr.GoCall
                                (GoIr.GoIdent "rt.ResultCoerce[any, any]")
                                [rawSubjExpr]
                        | otherwise ->
                            GoIr.GoTypeAssert (anyWrap rawSubjExpr) typeName
                subjStmt = GoIr.GoShortDecl subjectName subjExpr
                -- Cycle 4 D2 (TCO path): mirror the non-TCO caseToGo
                -- discard so a tail-position `case` whose only branch
                -- is `_ -> ...` doesn't emit an unused-var Go build
                -- error.
                subjDiscardStmt = GoIr.GoExprStmt
                    (GoIr.GoRaw ("_ = " ++ subjectName))
                branchStmts = concatMap
                    (caseBranchToStmtsWith subjectName tcoLeaf) branches
                fallthroughStmt = GoIr.GoExprStmt
                    (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Unreachable")
                        [GoIr.GoStringLit "tco/case"])
            in subjStmt : subjDiscardStmt : branchStmts ++ [fallthroughStmt]

        Can.If branches elseExpr ->
            ifToTcoStmts branches elseExpr

        -- Top-level body that IS a tail self-call (`f a b = g a b`
        -- style) — handled by tcoLeaf which detects the head.
        _ -> tcoLeaf (A.At A.one e)

    -- Leaf emitter — runs on case-branch bodies + if-arms.
    -- Recursively handles nested case/if; emits the regular
    -- `GoReturn` for anything else.
    tcoLeaf :: Can.Expr -> [GoIr.GoStmt]
    tcoLeaf (A.At _ e) = case e of
        Can.Call (A.At _ (Can.VarTopLevel h n)) args
            | h == home, n == fnName, length args == arity ->
                tcoJump args
        Can.Call (A.At _ (Can.VarLocal n)) args
            | n == fnName, length args == arity ->
                tcoJump args

        Can.If branches elseExpr ->
            ifToTcoStmts branches elseExpr

        Can.Case subj branches ->
            -- Nested case in branch body: lower via the same path.
            lowerTail (A.At A.one (Can.Case subj branches))

        -- Anything else: regular return.  Wrap the value with
        -- `coerceReturnExprT` so a base case like `[] -> []`
        -- coerces `[]any{}` to the function's typed return
        -- (`[]T1`, `rt.SkyMaybe[T1]`, …).
        _ -> [GoIr.GoReturn (coerceReturnExprT goRetType (exprToGo (A.At A.one e)))]

    -- Emit param-reassign + continue for a tail self-call.
    -- Tmps capture the OLD param values before reassignment so a
    -- swap-style `f a b = f b a` works correctly.  Go's `:=` type
    -- inference gives each tmp the static type of the RHS — when
    -- the RHS is an identifier referencing a typed local (e.g. a
    -- case-bound `rest : []T1` or the typed param `pred : func(T1)
    -- bool`), the tmp inherits that type and direct assignment to
    -- the matching param works.  For typed slots whose source is
    -- `any` (e.g. an FFI return), `coerceArg` widens / asserts as
    -- needed.  Function-typed params are excluded from
    -- `coerceArg`'s path because its `eraseTypeParams` rewrites
    -- `func(T1) bool` → `func(any) bool`, and Go rejects the
    -- resulting cross-type assignment.
    tcoJump :: [Can.Expr] -> [GoIr.GoStmt]
    tcoJump args =
        let tmps = ["__tco_t" ++ show i | i <- [0 .. length args - 1]]
            decls = zipWith
                (\t a -> GoIr.GoShortDecl t (exprToGo a))
                tmps args
            -- v0.15.x hardening / Gap A1 — pass the underlying
            -- Can.Expr through to coerceArg so the structural-
            -- fallback arm can recognise parametric-alias cross-
            -- instantiation in tail-recursive jumps.  The Go-side
            -- `__tco_t<i>` identifier hides the source's HM type
            -- from `goExprGoType`, but `inferExprType` on the
            -- original arg recovers it.
            coerceForTco mSrc ge ty
                | take 5 ty == "func(" = ge
                | otherwise = coerceArg mSrc ge ty
            -- Build the (param, tmp, ty, src) tuple list and walk.
            zip4tco (a:as) (b:bs) (c:cs) (d:ds) =
                (a, b, c, d) : zip4tco as bs cs ds
            zip4tco _ _ _ _ = []
            quadrants =
                zip4tco paramNames
                        tmps
                        (paramGoTys ++ repeat "any")
                        (map Just args ++ repeat Nothing)
            assigns =
                [ GoIr.GoAssign p
                    (coerceForTco src (GoIr.GoIdent t) ty)
                | (p, t, ty, src) <- quadrants ]
        in decls ++ assigns ++ [GoIr.GoContinue]

    -- Lower an `Can.If` in tail position as a nested GoIf chain
    -- with TCO-aware leaves.
    ifToTcoStmts
        :: [(Can.Expr, Can.Expr)]
        -> Can.Expr
        -> [GoIr.GoStmt]
    ifToTcoStmts branches elseExpr =
        foldr
            (\(c, b) acc ->
                [GoIr.GoIf (toBoolExpr (exprToGo c)) (tcoLeaf b) acc])
            (tcoLeaf elseExpr)
            branches


-- | Generate a Go condition for pattern matching
patternCondition :: String -> Can.Pattern_ -> Maybe GoIr.GoExpr
patternCondition subject pat = case pat of
    Can.PAnything -> Nothing  -- always matches
    Can.PVar _ -> Nothing     -- always matches

    Can.PInt n ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoIntLit n)

    Can.PStr s ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoStringLit s)

    Can.PBool True ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoBoolLit True)

    Can.PBool False ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoBoolLit False)

    Can.PChr c ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoRuneLit c)

    Can.PCtor home typeName union ctorName ctorIdx args ->
        -- Sky's `Bool` lowers to a raw Go `bool` — its True/False ctor
        -- patterns must compare directly against the value, NOT via
        -- `rt.EnumTagIs` (which expects an SkyADT carrying a .Tag).
        -- A plain `case cond of True -> a; False -> b` is generated
        -- with subject typed Go `bool`; routing through EnumTagIs would
        -- fall through every arm and panic in `rt.Unreachable`.
        if typeName == "Bool" && ctorName `elem` ["True", "False"] then
            Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject)
                       (GoIr.GoBoolLit (ctorName == "True"))
        else case Can._u_opts union of
            Can.Enum ->
                -- Enum: zero-arg ADT. Route through rt.EnumTagIs so
                -- values arriving from rt builders (SkyADT with the
                -- matching Tag) compare equal to the typed-int
                -- constant codegen would otherwise emit. Without
                -- this, `case (error.kind) of Io -> …` lowered by
                -- codegen would never match an rt.ErrIo-built kind
                -- because `Sky_Core_Error_ErrorKind(0) != SkyADT{Tag:0}`
                -- under Go's `any == any` rules — the Sky case would
                -- fall through to rt.Unreachable.
                --
                -- Keeping the named constants live for elsewhere
                -- (debugger strings, direct construction) is free —
                -- they still compile, this branch just doesn't use
                -- `==` on them.
                let _modStr = ModuleName.toString home
                    _qualName = if null _modStr || _modStr == "Main"
                        then typeName ++ "_" ++ ctorName
                        else (map (\c -> if c == '.' then '_' else c) _modStr)
                             ++ "_" ++ typeName ++ "_" ++ ctorName
                in Just $ GoIr.GoCall
                    (GoIr.GoQualified "rt" "EnumTagIs")
                    [ GoIr.GoIdent subject
                    , GoIr.GoIntLit ctorIdx
                    ]
            _ ->
                -- Tagged struct: match outer .Tag AND recurse into every
                -- ctor arg that carries a sub-pattern condition. Without
                -- the recursion, `Ok Nothing` and `Ok (Just x)` both
                -- collapse to `subject.Tag == 0` and the first matching
                -- branch swallows every Ok case — a silent soundness
                -- bug (skyvote sign-up: 'Account created but auto-login
                -- failed' showed even when the user was found).
                let outer = GoIr.GoBinary "=="
                        (GoIr.GoSelector (GoIr.GoIdent subject) "Tag")
                        (GoIr.GoIntLit ctorIdx)
                    inners =
                        [ c
                        | Can.PatternCtorArg idx _ (A.At _ argPat) <- args
                        , Just c <- [argPatternCondition subject ctorName idx argPat]
                        ]
                in Just $ foldl (GoIr.GoBinary "&&") outer inners

    Can.PUnit -> Nothing  -- always matches

    -- Cons: match non-empty list, len(rt.AsList(subject)) >= 1.
    -- `rt.AsList` accepts both `[]any` (legacy runtime shape) and any
    -- typed Go slice (typed codegen: `[]Error`, `[]Endpoint_R`, …) via
    -- reflect, so list patterns fire regardless of how the scrutinee
    -- was typed upstream. Before this, `case errs of [] -> ...` over
    -- a typed `[]Error` panicked with
    -- `interface {} is []Error, not []interface {}`.
    --
    -- Plus: when the head pattern is a constructor or literal, we
    -- ALSO emit a head-discriminator condition so the case arm only
    -- fires when the head matches. Pre-fix, `(AttrDescribe d) :: _`
    -- would fire for ANY non-empty list and then panic inside the
    -- body when it tried to extract field 0 from a head that wasn't
    -- AttrDescribe. The head-discriminator now joins the length
    -- check via `&&`.
    Can.PCons h t ->
        let lenCond = GoIr.GoBinary ">="
                (GoIr.GoCall (GoIr.GoIdent "len")
                    [ GoIr.GoCall (GoIr.GoIdent "rt.AsList") [GoIr.GoIdent subject] ])
                (GoIr.GoIntLit 1)
            (A.At _ hPat) = h
            (A.At _ tPat) = t
            headCond = consHeadCondition subject hPat
            tailCond = consTailCondition subject tPat
            extras = [ c | Just c <- [headCond, tailCond] ]
        in Just $ foldl (GoIr.GoBinary "&&") lenCond extras

    -- Fixed-length list: match exact length; element conditions handled in
    -- bindings below (codegen over-matches conservatively — strict element
    -- matching would need nested if-cascades we don't model in a single cond).
    Can.PList xs ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoIdent "len")
                [ GoIr.GoCall (GoIr.GoIdent "rt.AsList") [GoIr.GoIdent subject] ])
            (GoIr.GoIntLit (length xs))

    -- A tuple's shape is guaranteed by HM, but its components can
    -- carry discriminating sub-patterns (`(Just x, Just y)`) — those
    -- MUST gate the arm. Without this the arm fired unconditionally
    -- and ran its body on a non-matching tuple (issue #56).
    Can.PTuple aPat bPat more ->
        tuplePatternCondition subject (aPat : bPat : more)

    Can.PRecord _    -> Nothing
    Can.PAlias inner _ ->
        let (A.At _ innerPat) = inner
        in patternCondition subject innerPat


-- | Condition for a sub-pattern sitting inside a ctor argument. Uses
-- the same field accessor that bindCtorArg would use (`.OkValue`,
-- `.ErrValue`, `.JustValue`, or `.Fields[idx]`). Returns Nothing for
-- catch-all sub-patterns (PVar / PAnything / PUnit) whose presence
-- doesn't restrict the outer ctor match.
--
-- Only emits a condition for sub-patterns that actually narrow the
-- match — nested ctor, literal, bool, char, list-length, cons.
argPatternCondition :: String -> String -> Int -> Can.Pattern_ -> Maybe GoIr.GoExpr
argPatternCondition subject ctorName idx pat = case pat of
    -- Catch-alls and always-match shapes: no condition.
    Can.PAnything  -> Nothing
    Can.PVar _     -> Nothing
    Can.PUnit      -> Nothing
    Can.PTuple{}   -> Nothing
    Can.PRecord _  -> Nothing

    -- Alias: recurse through to the inner pattern, same accessor.
    Can.PAlias inner _ ->
        let (A.At _ innerPat) = inner
        in argPatternCondition subject ctorName idx innerPat

    _ ->
        -- Build the Go expression for the sub-value using bindCtorArg's
        -- naming convention. Source type may be:
        --   * `any` — needs `.(SkyResult[any,any])` etc. to read .Tag.
        --   * typed `SkyResult[any, X]` / `SkyMaybe[X]` — direct field access.
        --   * a `_tFfi`-suffix variable — always concrete; direct access.
        -- Using `rt.AdtTag(v)` / `rt.ResultTag(v)` / `rt.MaybeTag(v)`
        -- runtime helpers instead of a type-assert avoids the
        -- `SkyMaybe[string] not SkyMaybe[any]` panic class.
        let accessor = case ctorName of
                "Ok"   -> GoIr.GoSelector (GoIr.GoIdent subject) "OkValue"
                "Err"  -> GoIr.GoSelector (GoIr.GoIdent subject) "ErrValue"
                "Just" -> GoIr.GoSelector (GoIr.GoIdent subject) "JustValue"
                _      -> GoIr.GoIndex
                            (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                            (GoIr.GoIntLit idx)
            tagCast ty = GoIr.GoTypeAssert
                            (GoIr.GoCall (GoIr.GoIdent "any") [accessor])
                            ty
            tagHelperFor helper =
                GoIr.GoCall (GoIr.GoQualified "rt" helper)
                    [GoIr.GoCall (GoIr.GoIdent "any") [accessor]]
        in case pat of
            Can.PCtor home innerTypeName innerUnion innerCtor innerIdx _innerArgs ->
                case Can._u_opts innerUnion of
                    Can.Enum
                        -- Bool is special: canonically an enum (True/False)
                        -- but at runtime Sky uses Go `bool`, not an int
                        -- constant. Compare via `any(x).(bool) == true/false`.
                        | innerTypeName == "Bool" ->
                            Just $ GoIr.GoBinary "=="
                                (tagCast "bool")
                                (GoIr.GoBoolLit (innerCtor == "True"))
                        | otherwise ->
                            let modStr = ModuleName.toString home
                                qualName =
                                    if null modStr || modStr == "Main"
                                        then innerTypeName ++ "_" ++ innerCtor
                                        else map (\c -> if c == '.' then '_' else c) modStr
                                             ++ "_" ++ innerTypeName ++ "_" ++ innerCtor
                            in Just $ GoIr.GoBinary "=="
                                    (tagCast "int")
                                    (GoIr.GoIdent qualName)
                    _ ->
                        -- Read .Tag via runtime helper that tolerates
                        -- any generic instantiation of SkyResult/SkyMaybe.
                        -- User ADTs are rt.SkyADT aliases so direct
                        -- `.Tag` works; still route through rt.AdtTag
                        -- for consistency and to accept any-typed
                        -- sources without an extra cast.
                        let innerTag = case innerCtor of
                                "Ok"       -> tagHelperFor "ResultTag"
                                "Err"      -> tagHelperFor "ResultTag"
                                "Just"     -> tagHelperFor "MaybeTag"
                                "Nothing"  -> tagHelperFor "MaybeTag"
                                _          -> tagHelperFor "AdtTag"
                        in Just $ GoIr.GoBinary "=="
                                innerTag
                                (GoIr.GoIntLit innerIdx)

            Can.PInt n   -> Just (GoIr.GoBinary "==" (tagCast "int")    (GoIr.GoIntLit n))
            Can.PStr s   -> Just (GoIr.GoBinary "==" (tagCast "string") (GoIr.GoStringLit s))
            Can.PBool b  -> Just (GoIr.GoBinary "==" (tagCast "bool")   (GoIr.GoBoolLit b))
            Can.PChr c   -> Just (GoIr.GoBinary "==" (tagCast "rune")   (GoIr.GoRuneLit c))

            -- Cons-inside-ctor-arg (e.g. `Just (h :: _)`): the outer
            -- ctor branch must ALSO check that its payload is a
            -- non-empty list. Without this, `Just (r :: _)` over a
            -- `Just []` matches the outer Just, then the binding code
            -- (`rt.AsList(.JustValue)[0]`) panics with
            -- `index out of range`. Pattern surfaced by I18n.regionOf
            -- in a sendcrafts port — `case List.tail parts of Just (r
            -- :: _) -> … | _ -> ""` panicked when parts had length 1
            -- (List.tail returns Just []).
            Can.PCons _ _ ->
                Just $ GoIr.GoBinary ">="
                    (GoIr.GoCall (GoIr.GoIdent "len")
                        [ GoIr.GoCall (GoIr.GoQualified "rt" "AsList")
                            [GoIr.GoCall (GoIr.GoIdent "any") [accessor]] ])
                    (GoIr.GoIntLit 1)

            -- Fixed-length list inside ctor arg (e.g. `Just [a, b]`):
            -- same hazard as PCons — without an exact-length check
            -- the outer ctor branch fires and the destructure panics
            -- on the wrong element count.
            Can.PList xs ->
                Just $ GoIr.GoBinary "=="
                    (GoIr.GoCall (GoIr.GoIdent "len")
                        [ GoIr.GoCall (GoIr.GoQualified "rt" "AsList")
                            [GoIr.GoCall (GoIr.GoIdent "any") [accessor]] ])
                    (GoIr.GoIntLit (length xs))

            _            -> Nothing


-- | Discriminator condition for the head pattern of a `(h :: t)` cons.
-- The cons-pattern itself only checks `len >= 1`; this function adds
-- the head's narrowing condition so that, e.g., `(AttrDescribe d) ::
-- _` only fires when the head's actual constructor is AttrDescribe.
--
-- Without this, the case body's bindings (which assume the head IS
-- the matched constructor) would extract field 0 from a head that
-- might be ANY value of the ADT — a `interface conversion: …` panic
-- at runtime. Returns Nothing for catch-all heads (PVar, PAnything,
-- PUnit) which don't narrow the match.
consHeadCondition :: String -> Can.Pattern_ -> Maybe GoIr.GoExpr
consHeadCondition subject pat =
    let headRaw = "rt.AsList(" ++ subject ++ ")[0]"
    in patternConditionForExpr headRaw pat


-- | Same shape for the tail of a cons. Tail patterns are usually a
-- variable or `_`, but `(_ :: y :: _)` etc would benefit. Returns
-- Nothing for var/anything tails. The tail expression as a Go raw
-- string is `any(rt.AsList(subject)[1:])` (matching the binding
-- code's tail extraction).
consTailCondition :: String -> Can.Pattern_ -> Maybe GoIr.GoExpr
consTailCondition subject pat =
    let tailRaw = "any(rt.AsList(" ++ subject ++ ")[1:])"
    in patternConditionForExpr tailRaw pat


-- | Build a discriminator condition where the subject is an arbitrary
-- Go expression (raw string), not a bound variable. Mirrors the
-- shape of `patternCondition` for the cases where the head/tail of
-- a cons can carry a narrowing pattern. Used by `consHeadCondition`
-- and `consTailCondition`.
--
-- Only handles the patterns that act as discriminators when nested
-- inside a cons pattern: PCtor, PInt, PStr, PBool, PChr, and another
-- PCons. PVar / PAnything / PUnit always match (Nothing). PTuple /
-- PRecord / PList structure is guaranteed by HM (Nothing).
patternConditionForExpr :: String -> Can.Pattern_ -> Maybe GoIr.GoExpr
patternConditionForExpr subjectRaw pat = case pat of
    Can.PAnything -> Nothing
    Can.PVar _    -> Nothing
    Can.PUnit     -> Nothing
    Can.PRecord _ -> Nothing

    -- A nested tuple (a tuple inside a ctor arg, a cons, or another
    -- tuple) recurses the same way — issue #56, "similar patterns".
    Can.PTuple aPat bPat more ->
        tuplePatternCondition subjectRaw (aPat : bPat : more)

    Can.PInt n ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoQualified "rt" "AsInt")
                [GoIr.GoRaw subjectRaw])
            (GoIr.GoIntLit n)

    Can.PStr s ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoQualified "rt" "AsString")
                [GoIr.GoRaw subjectRaw])
            (GoIr.GoStringLit s)

    Can.PBool b ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoQualified "rt" "AsBool")
                [GoIr.GoRaw subjectRaw])
            (GoIr.GoBoolLit b)

    Can.PChr c ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoTypeAssert
                (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoRaw subjectRaw])
                "rune")
            (GoIr.GoRuneLit c)

    Can.PCtor _home typeName union ctorName ctorIdx _args ->
        -- Sky's `Bool` lowers to a raw Go `bool`, so a True/False
        -- ctor pattern must compare the value directly — `rt.EnumTagIs`
        -- expects an SkyADT and is always false on a `bool`. The
        -- top-level `patternCondition` already special-cases this;
        -- the gap here surfaced once tuple components began routing
        -- through `patternConditionForExpr` (issue #56, a Bool inside
        -- a tuple pattern).
        if typeName == "Bool" && (ctorName == "True" || ctorName == "False") then
            Just $ GoIr.GoBinary "=="
                (GoIr.GoCall (GoIr.GoQualified "rt" "AsBool")
                    [GoIr.GoRaw subjectRaw])
                (GoIr.GoBoolLit (ctorName == "True"))
        else case Can._u_opts union of
            Can.Enum ->
                -- Enum (zero-arg ADT): use rt.EnumTagIs which tolerates
                -- both Sky-side typed-int and rt.SkyADT-shaped values.
                Just $ GoIr.GoCall
                    (GoIr.GoQualified "rt" "EnumTagIs")
                    [ GoIr.GoRaw subjectRaw
                    , GoIr.GoIntLit ctorIdx
                    ]
            _ ->
                -- Tagged ADT: read .Tag via the rt.AdtTag helper which
                -- accepts any-typed inputs and routes through
                -- reflection if needed (so this works whether the head
                -- value is Sky-side typed or any-boxed at runtime).
                Just $ GoIr.GoBinary "=="
                    (GoIr.GoCall (GoIr.GoQualified "rt" "AdtTag")
                        [GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoRaw subjectRaw]])
                    (GoIr.GoIntLit ctorIdx)

    Can.PCons _ _ ->
        -- Nested cons (e.g. `(_ :: _) :: _`): the inner needs at
        -- least one element of its own. Only emit the length check —
        -- deeper recursion would need more plumbing and the common
        -- pattern is single-level.
        Just $ GoIr.GoBinary ">="
            (GoIr.GoCall (GoIr.GoIdent "len")
                [ GoIr.GoCall (GoIr.GoQualified "rt" "AsList")
                    [GoIr.GoRaw subjectRaw] ])
            (GoIr.GoIntLit 1)

    Can.PList xs ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoIdent "len")
                [ GoIr.GoCall (GoIr.GoQualified "rt" "AsList")
                    [GoIr.GoRaw subjectRaw] ])
            (GoIr.GoIntLit (length xs))

    Can.PAlias inner _ ->
        let (A.At _ innerPat) = inner
        in patternConditionForExpr subjectRaw innerPat


-- | Conjoin the sub-pattern conditions of a tuple, each tested
-- against its component — `rt.AsTuple2/3(subj).V<i>`, or
-- `SkyTupleN.Vs[i]` for arity ≥ 4 (matching the binding accessors).
-- `subj` is spliced raw, so it works whether the tuple subject is a
-- plain identifier or itself a Go expression (a nested tuple).
-- Nothing when every component is irrefutable (a pure-`PVar` tuple).
tuplePatternCondition :: String -> [Can.Pattern] -> Maybe GoIr.GoExpr
tuplePatternCondition subj pats =
    let arity = length pats
        compRaw i = case arity of
            2 -> "rt.AsTuple2(" ++ subj ++ ").V" ++ show i
            3 -> "rt.AsTuple3(" ++ subj ++ ").V" ++ show i
            _ -> "any(" ++ subj ++ ").(rt.SkyTupleN).Vs[" ++ show i ++ "]"
        conds =
            [ c
            | (i, A.At _ subPat) <- zip [0 :: Int ..] pats
            , Just c <- [patternConditionForExpr (compRaw i) subPat]
            ]
    in case conds of
        [] ->
            Nothing

        (h : t) ->
            Just (foldl (GoIr.GoBinary "&&") h t)


-- | Generate Go variable bindings from a pattern
patternBindings :: String -> Can.Pattern_ -> [GoIr.GoStmt]
patternBindings subject pat = case pat of
    Can.PVar name ->
        if isDiscardName name
            then [ GoIr.GoAssign "_" (GoIr.GoIdent subject) ]
            else letBindStmts name (GoIr.GoIdent subject)

    Can.PAnything -> []
    Can.PUnit -> []
    Can.PInt _ -> []
    Can.PStr _ -> []
    Can.PBool _ -> []
    Can.PChr _ -> []

    Can.PCtor _home typeName _union ctorName _ctorIdx args ->
        -- Bind constructor arguments
        concatMap (bindCtorArg subject ctorName) args

    -- head :: tail  →  h := rt.AsList(subject)[0]; t := rt.AsList(subject)[1:]
    -- `rt.AsList` widens any Go slice (typed or `[]any`) to `[]any`
    -- so list patterns bind correctly whether the scrutinee came
    -- from typed codegen (`[]Endpoint_R`) or the legacy `[]any` path.
    Can.PCons h t ->
        let asSlice = GoIr.GoCall (GoIr.GoIdent "rt.AsList") [GoIr.GoIdent subject]
            (A.At _ hPat) = h
            (A.At _ tPat) = t
            headExpr = GoIr.GoIndex asSlice (GoIr.GoIntLit 0)
            -- Wrap in any() so nested patternBindings can re-slice.
            -- Without this, the recursive case `1 :: 2 :: _` tries
            -- `rt.AsList(__tail)[0]` where __tail is the shape returned
            -- by `rt.AsList(subject)[1:]` — already `[]any`. `rt.AsList`
            -- handles both shapes idempotently so re-wrapping is safe.
            tailExpr = GoIr.GoRaw ("any(rt.AsList(" ++ subject ++ ")[1:])")
            headName = "__sky_h_" ++ subject
            tailName = "__sky_t_" ++ subject
            headStmts = case hPat of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" headExpr ]
                        else [ GoIr.GoShortDecl name headExpr
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" headExpr ]
                _ -> GoIr.GoShortDecl headName headExpr
                    : GoIr.GoAssign "_" (GoIr.GoIdent headName)
                    : patternBindings headName hPat
            tailStmts = case tPat of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" tailExpr ]
                        else [ GoIr.GoShortDecl name tailExpr
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" tailExpr ]
                _ -> GoIr.GoShortDecl tailName tailExpr
                    : GoIr.GoAssign "_" (GoIr.GoIdent tailName)
                    : patternBindings tailName tPat
        in headStmts ++ tailStmts

    -- [a, b, c]  →  bind each element by index
    Can.PList xs ->
        let asSlice suf = GoIr.GoRaw ("rt.AsList(" ++ subject ++ ")[" ++ show suf ++ "]")
            bindEl i (A.At _ p) = case p of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" (asSlice i) ]
                        else [ GoIr.GoShortDecl name (asSlice i)
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" (asSlice i) ]
                _ ->
                    let sub = "__sky_li_" ++ show i ++ "_" ++ subject
                    in GoIr.GoShortDecl sub (asSlice i)
                        : GoIr.GoAssign "_" (GoIr.GoIdent sub)
                        : patternBindings sub p
        in concat (zipWith bindEl [0::Int ..] xs)

    -- (a, b[, c, ...])  →  bind V0/V1/V2 (SkyTuple2/3) or Vs[N] (SkyTupleN)
    Can.PTuple aPat bPat more ->
        let arity = 2 + length more
            allPats = aPat : bPat : more
            (tupleKind, accessor) = case arity of
                2 -> ("SkyTuple2", \i -> GoIr.GoSelector (asTup "AsTuple2") ("V" ++ show i))
                3 -> ("SkyTuple3", \i -> GoIr.GoSelector (asTup "AsTuple3") ("V" ++ show i))
                _ -> ("SkyTupleN", \i -> GoIr.GoIndex
                        (GoIr.GoSelector (asTupAssert "SkyTupleN") "Vs")
                        (GoIr.GoIntLit i))
            -- Route arity-2/3 destructure through `rt.AsTuple2` /
            -- `rt.AsTuple3` helpers (reflect-backed re-box) instead
            -- of a direct `.(rt.SkyTuple2)` assertion.  Sky lambdas
            -- in typed-codegen contexts receive `T2[X, Y]` (typed
            -- generic instantiation), which is NOT the same nominal
            -- type as `SkyTuple2 = T2[any, any]` — the assertion
            -- panics with `is rt.T2[string, R], not rt.T2[any, any]`.
            -- The runtime helper handles both shapes uniformly.
            asTup helper = GoIr.GoCall
                (GoIr.GoIdent ("rt." ++ helper))
                [GoIr.GoIdent subject]
            asTupAssert k = GoIr.GoTypeAssert
                (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject])
                ("rt." ++ k)
            _ = tupleKind  -- silences warning; kept for grep-ability
            bindField i (A.At _ p) = case p of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" (accessor i) ]
                        else [ GoIr.GoShortDecl name (accessor i)
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" (accessor i) ]
                _ ->
                    let sub = "__sky_t_V" ++ show i ++ "_" ++ subject
                    in GoIr.GoShortDecl sub (accessor i)
                       : GoIr.GoAssign "_" (GoIr.GoIdent sub)
                       : patternBindings sub p
        in concat (zipWith bindField [0 :: Int ..] allPats)

    -- { name }  →  name := rt.Field(subject, "Name")
    Can.PRecord fields ->
        concat
        [ [ GoIr.GoShortDecl f
            (GoIr.GoCall (GoIr.GoQualified "rt" "Field")
                [ GoIr.GoIdent subject
                , GoIr.GoStringLit (capitalise_ f)
                ])
          , GoIr.GoAssign "_" (GoIr.GoIdent f)
          ]
        | f <- fields
        ]

    -- `(PCons h t) as whole`  →  bind whole := subject, then recurse into inner
    Can.PAlias inner name ->
        let (A.At _ innerPat) = inner
            aliasStmt = if isDiscardName name
                then [ GoIr.GoAssign "_" (GoIr.GoIdent subject) ]
                else [ GoIr.GoShortDecl name (GoIr.GoIdent subject) ]
        in aliasStmt ++ patternBindings subject innerPat


-- | Bind a constructor argument to a local variable.
-- For Ok/Err/Just (our special generic types) we need a type-assertion on
-- the subject first when the subject is any-typed (comes from an inner
-- destructure temp) — otherwise `.OkValue` / `.JustValue` on `any` fails
-- Go's type check. For user-defined Tag-based ADTs, the outer case already
-- asserted the subject to the struct type so `.Fields[i]` works directly.
bindCtorArg :: String -> String -> Can.PatternCtorArg -> [GoIr.GoStmt]
bindCtorArg subject ctorName (Can.PatternCtorArg idx _ty pat) =
    let (A.At _ innerPat) = pat
        -- P7: a subject name suffixed with "_tFfi" is the typed-FFI
        -- shortcut — the outer caseToGo already guarantees it's a
        -- SkyResult[_, _] or SkyMaybe[_] struct, so we can field-
        -- access directly without a `(any).(SkyResult[any, any])`
        -- assertion. Wrapping the field access in any() preserves
        -- the any-typed binding contract for downstream branch code.
        -- Only the OUTER case's subject carries the typed-FFI shape.
        -- Nested destructure temps (`__sky_cf_N_<parent>`) inherit the
        -- suffix textually but are `any`-typed — reject them so
        -- patternBindings falls through to the any-assertion path.
        isTypedFfiSubject =
            take 5 (reverse subject) == "ifFt_"
            && not ("__sky_cf_" `List.isPrefixOf` subject)
        -- v0.13 typed lowerer: `__subject_tAdt` is the outer case
        -- subject for a custom (non-Result/Maybe) ADT — it was
        -- type-asserted to the ADT struct (`rt.SkyADT` alias) in
        -- `coerceSubject`, so `.Fields[idx]` reads directly without
        -- the `rt.AdtField(any(subject), idx)` reflect helper.
        -- Nested destructure temps (`__sky_cf_*`) are any-typed —
        -- reject them so they fall through to the reflect path.
        isTypedAdtSubject =
            take 5 (reverse subject) == "tdAt_"
            && not ("__sky_cf_" `List.isPrefixOf` subject)
        anyWrap n = GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent n]
        -- Runtime helper unwraps any SkyResult/SkyMaybe instantiation
        -- without a type-assertion panic — used when the subject is
        -- any-typed (nested destructure temps, non-_tFfi subjects
        -- whose runtime type could differ from SkyResult[any,any]).
        helperFor helper = GoIr.GoCall
            (GoIr.GoQualified "rt" helper)
            [anyWrap subject]
        rawField = case ctorName of
            _ | isTypedFfiSubject ->
                case ctorName of
                    "Ok"   -> GoIr.GoSelector (GoIr.GoIdent subject) "OkValue"
                    "Err"  -> GoIr.GoSelector (GoIr.GoIdent subject) "ErrValue"
                    "Just" -> GoIr.GoSelector (GoIr.GoIdent subject) "JustValue"
                    _      -> GoIr.GoIndex
                                (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                                (GoIr.GoIntLit idx)
            "Ok"   -> helperFor "ResultOk"
            "Err"  -> helperFor "ResultErr"
            "Just" -> helperFor "MaybeJust"
            _ | isTypedAdtSubject ->
                -- Custom ADT, statically-typed subject: direct
                -- `.Fields[idx]` — no reflect.
                GoIr.GoIndex
                    (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                    (GoIr.GoIntLit idx)
            _      ->
                -- Custom ADT: use rt.AdtField runtime helper so an
                -- any-typed subject (e.g. bound from
                -- rt.ResultOk/ErrValue above, or a nested destructure
                -- temp) still reads .Fields[idx] without requiring a
                -- type-assertion to the emitted ADT struct.
                GoIr.GoCall
                    (GoIr.GoQualified "rt" "AdtField")
                    [GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject], GoIr.GoIntLit idx]
        fieldAccess =
            if isTypedFfiSubject
                then GoIr.GoCall (GoIr.GoIdent "any") [rawField]
                else rawField
    in case innerPat of
        Can.PVar name ->
            if isDiscardName name
                then [ GoIr.GoAssign "_" fieldAccess ]
                else
                    -- Bind + discard-sink so Go doesn't error on unused when
                    -- the case body doesn't reference the binding.
                    [ GoIr.GoShortDecl name fieldAccess
                    , GoIr.GoAssign "_" (GoIr.GoIdent name)
                    ]
        Can.PAnything -> [ GoIr.GoAssign "_" fieldAccess ]
        _ ->
            let tmp = "__sky_cf_" ++ show idx ++ "_" ++ subject
            in GoIr.GoShortDecl tmp fieldAccess
               : GoIr.GoAssign "_" (GoIr.GoIdent tmp)
               : patternBindings tmp innerPat


-- ═══════════════════════════════════════════════════════════
-- MAIN FUNCTION
-- ═══════════════════════════════════════════════════════════

-- | Generate the main() function (uses solved types for typed codegen)
generateMainFunc :: Can.Module -> Src.Module -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateMainFunc canMod srcMod solvedTypes =
    case findMain canMod of
        Nothing ->
            [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = "main"
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = []
                , GoIr._gf_returnType = ""
                , GoIr._gf_body = [GoIr.GoExprStmt (GoIr.GoCall (GoIr.GoQualified "rt" "Log_println") [GoIr.GoStringLit "No main function"])]
                }
            ]
        Just def ->
            let body = defBody def
                hasTask = any isTaskImport (Src._imports srcMod)
                stmts = exprToMainStmtsTyped solvedTypes body
                wrappedStmts = if hasTask
                    then stmts  -- TODO: wrap in rt.RunMainTask
                    else stmts
            in
            [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = "main"
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = []
                , GoIr._gf_returnType = ""
                , GoIr._gf_body = wrappedStmts
                }
            ]


-- | Find the main definition
findMain :: Can.Module -> Maybe Can.Def
findMain canMod = findMainInDecls (Can._decls canMod)
  where
    findMainInDecls Can.SaveTheEnvironment = Nothing
    findMainInDecls (Can.Declare def rest) =
        if defName def == "main" then Just def else findMainInDecls rest
    findMainInDecls (Can.DeclareRec def defs rest) =
        if defName def == "main" then Just def
        else case filter (\d -> defName d == "main") defs of
            (d:_) -> Just d
            [] -> findMainInDecls rest


-- | Get the name from a definition
defName :: Can.Def -> String
defName (Can.Def (A.At _ n) _ _) = n
defName (Can.TypedDef (A.At _ n) _ _ _ _) = n
defName (Can.DestructDef _ _) = "__destruct__"


-- | Get the body expression from a definition
defBody :: Can.Def -> Can.Expr
defBody (Can.Def _ _ body) = body
defBody (Can.TypedDef _ _ _ body _) = body
defBody (Can.DestructDef _ body) = body


-- | Convert the main body to Go statements, using typed codegen where possible
exprToMainStmtsTyped :: Solve.SolvedTypes -> Can.Expr -> [GoIr.GoStmt]
exprToMainStmtsTyped types (A.At _ expr) = case expr of
    Can.Let def body ->
        -- v0.15.3 — register the let-binding's HM type into the
        -- in-scope lambda-types map BEFORE defToStmts runs so
        -- both the binding's RHS lowering AND every downstream
        -- ref in the body see the typed shape.  Without this,
        -- `wformCfg := Setup_R[Msg]{...}` is emitted correctly
        -- but the next-line call `Widget_Form_view(wformCfg)`
        -- can't pin Setup_R[Msg] as wformCfg's Go-static type —
        -- coerceArg's parametric-record short-circuit doesn't
        -- fire, and the legacy nominal cast panics at runtime.
        registerMainLetBindingType types def `seq`
            (defToStmts def ++ exprToMainStmtsTyped types body)

    Can.LetRec defs body ->
        foldr seq () (map (registerMainLetBindingType types) defs) `seq`
            (concatMap defToStmts defs ++ exprToMainStmtsTyped types body)

    Can.LetDestruct _pat valExpr body ->
        [GoIr.GoExprStmt (exprToGoMain types valExpr)] ++ exprToMainStmtsTyped types body

    -- Calls are valid Go expression statements. Wrap in
    -- `rt.AnyTaskRun` so a Task-returning call (the new normal under
    -- Task-everywhere — `main = println X` returns Task Error ())
    -- has its thunk forced and the side effect actually fires.
    -- AnyTaskRun is defensively shaped: it forces `func() any` thunks
    -- and passes bare values through wrapped in `Ok`, so applying it
    -- to a non-Task call is a no-op modulo the discard. Discard via
    -- blank assignment (Go forbids bare expression statements that
    -- aren't calls; a wrapped AnyTaskRun call is itself a call so
    -- either form is legal, but `_ =` keeps both branches uniform).
    Can.Call _ _ ->
        [GoIr.GoAssign "_"
            (GoIr.GoCall
                (GoIr.GoQualified "rt" "AnyTaskRun")
                [exprToGoMain types (A.At A.one expr)])]

    -- Non-call values (e.g. literals, vars): same AnyTaskRun wrap so
    -- `main = someTask` (a Task-typed value reference) also fires.
    _ ->
        [GoIr.GoAssign "_"
            (GoIr.GoCall
                (GoIr.GoQualified "rt" "AnyTaskRun")
                [exprToGoMain types (A.At A.one expr)])]


-- | Generate Go for main body expressions. Delegates to the standard
-- exprToGo so VarTopLevel/VarCtor call-site coercion kicks in at main
-- call sites just like anywhere else — main used to have a parallel
-- codegen path that skipped coerceCallArgs, causing typed callee args
-- to fail at go build when called from `main`.
exprToGoMain :: Solve.SolvedTypes -> Can.Expr -> GoIr.GoExpr
exprToGoMain _types = exprToGo


-- | Legacy untyped main stmts (kept for reference)
exprToMainStmts :: Can.Expr -> [GoIr.GoStmt]
exprToMainStmts = exprToMainStmtsTyped Solve.emptySolvedTypes


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

-- ═══════════════════════════════════════════════════════════
-- TYPED EXPRESSION CODEGEN
-- ═══════════════════════════════════════════════════════════

-- | Generate Go expression in typed context with known return type.
exprToGoTypedWithRet :: Solve.SolvedTypes -> String -> Can.Expr -> GoIr.GoExpr
exprToGoTypedWithRet types retType expr = exprToGoTyped types retType expr


-- | Generate Go expression in typed context — uses direct Go operators
-- instead of any-typed runtime wrappers.
exprToGoTyped :: Solve.SolvedTypes -> String -> Can.Expr -> GoIr.GoExpr
exprToGoTyped types retType (A.At _ expr) = case expr of
    Can.Int n -> GoIr.GoIntLit n
    Can.Float f -> GoIr.GoFloatLit f
    Can.Str s -> GoIr.GoStringLit s
    Can.Chr c -> GoIr.GoRuneLit c
    Can.Unit -> GoIr.GoRaw "struct{}{}"

    Can.VarLocal name ->
        -- If we have a solved type for this var and it's concrete, use type assertion
        case Solve.lookupSolvedVar name types of
            Just ty | isConcreteType ty -> GoIr.GoTypeAssert (GoIr.GoIdent name) (solvedTypeToGo ty)
            _ -> GoIr.GoIdent name
    Can.VarTopLevel _ name -> GoIr.GoIdent (goSafeName name)
    Can.VarKernel modName funcName -> kernelToGo modName funcName

    Can.Binop op _ _ _ left right -> typedBinop types retType op left right
    Can.If branches elseExpr -> typedIf types retType branches elseExpr

    Can.Call func args ->
        let goFunc = exprToGoTyped types retType func
            goArgs = map (exprToGoTyped types retType) args
            -- Try typed kernel routing first. When func is a kernel
            -- with a typed *T variant AND we can derive concrete
            -- arg types, emit `rt.List_mapT[int, any](...)` instead
            -- of `rt.List_mapAny(...)`. v0.12.x typed-codegen Phase 3.
            typedKernelCall = case func of
                A.At _ (Can.VarKernel m f) ->
                    kernelTypedCall types m f args goArgs
                _ -> Nothing
            callExpr = case typedKernelCall of
                Just expr -> expr
                Nothing -> case func of
                    A.At _ (Can.VarLocal name) ->
                        case Solve.lookupSolvedVar name types of
                            Just (T.TLambda _ _) ->
                                GoIr.GoCall (GoIr.GoRaw (name ++ ".(func(any) any)")) goArgs
                            _ -> GoIr.GoCall goFunc goArgs
                    -- v0.13 D-Lambda-Lowerer: delegate top-level-call
                    -- lowering to the untyped `exprToGo` path, which
                    -- routes through `coerceCallArgsAt` and applies
                    -- typed-lambda emission for func-typed param slots.
                    -- Pre-fix, `exprToGoTyped`'s `Can.Call` lowered each
                    -- arg via `exprToGoTyped types retType` — including
                    -- `Can.Lambda` args, which fell through to the
                    -- untyped `curryLambdaPat` at line ~7840, emitting
                    -- `func(any) any`. After D1 types the HOF param's
                    -- return slot (`func(T) rt.SkyResult[E, V]`), Go
                    -- rejects the un-typed lambda. Delegating routes
                    -- the call through the typed-aware path.
                    A.At _ (Can.VarTopLevel _ _) ->
                        exprToGo (A.At A.one (Can.Call func args))
                    _ -> GoIr.GoCall goFunc goArgs
            -- If the called function has a known return type and we need a primitive,
            -- assert the result. This handles: n * factorial(n-1) where factorial returns any.
            -- BUT: if the callee is itself emitted with a fully-typed Go signature
            -- (concrete params + concrete return, fully applied), the Go call already
            -- yields a concrete value and asserting `.(T)` on it would be a Go error.
            calleeInfo = case func of
                A.At _ (Can.VarTopLevel _ name) ->
                    case Solve.lookupSolvedVar name types of
                        Just ft ->
                            let (argTys, rtTy) = splitFuncType (length args) ft
                                fullyTyped = length argTys == length args
                                           && isConcreteType rtTy
                                           && all isConcreteType argTys
                            in Just (rtTy, fullyTyped)
                        Nothing -> Nothing
                A.At _ (Can.VarLocal name) ->
                    case Solve.lookupSolvedVar name types of
                        Just ft ->
                            let (_, rtTy) = splitFuncType (length args) ft
                            in Just (rtTy, False)  -- VarLocal calls go through any-dispatch
                        Nothing -> Nothing
                _ -> Nothing
        in case calleeInfo of
            Just (_, True) -> callExpr  -- typed-emitted callee returns concrete directly
            Just (rt, False) | isConcreteType rt ->
                -- Audit: parametric Sky containers (Task / Result / Maybe)
                -- need TaskCoerceT / ResultCoerce / MaybeCoerce instead of
                -- a direct .(rt.SkyTask[E,A]) assertion. Direct assertion
                -- panics with `interface {} is func() interface {}, not
                -- rt.SkyTask[Error, A]` when the runtime returned an
                -- untyped thunk (typical of the Db.* / Time.* helpers).
                -- wrapTypedReturn already encapsulates the Coerce-vs-assert
                -- choice for every parametric shape.
                wrapTypedReturn (solvedTypeToGo rt) callExpr
            _ -> callExpr

    Can.Negate inner -> GoIr.GoUnary "-" (exprToGoTyped types retType inner)

    Can.Lambda params body ->
        curryLambdaPat params (exprToGoTyped types retType body)

    _ -> exprToGo (A.At A.one expr)


typedBinop :: Solve.SolvedTypes -> String -> String -> Can.Expr -> Can.Expr -> GoIr.GoExpr
typedBinop types retType op left right = case op of
    "|>" -> pipeApply left right
    "<|" -> pipeApply right left
    -- String concat: use rt.Concat which returns any, then assert to string if needed
    "++" -> let concatExpr = GoIr.GoCall (GoIr.GoQualified "rt" "Concat") [exprToGoTyped types retType left, exprToGoTyped types retType right]
            in if retType == "string"
               then GoIr.GoTypeAssert concatExpr "string"
               else concatExpr
    "/=" -> GoIr.GoCall (GoIr.GoQualified "rt" "NotEq") [exprToGoTyped types retType left, exprToGoTyped types retType right]
    _ -> GoIr.GoBinary op (exprToGoTyped types retType left) (exprToGoTyped types retType right)


typedIf :: Solve.SolvedTypes -> String -> [(Can.Expr, Can.Expr)] -> Can.Expr -> GoIr.GoExpr
typedIf types retType branches elseExpr =
    let
        go [] = "return " ++ GoBuilder.renderExpr (exprToGoTyped types retType elseExpr)
        go ((cond, body):rest) =
            "if " ++ GoBuilder.renderExpr (exprToGoTyped types retType cond)
            ++ " { return " ++ GoBuilder.renderExpr (exprToGoTyped types retType body) ++ " }; "
            ++ go rest
    in
    GoIr.GoRaw $ "func() " ++ retType ++ " { " ++ go branches ++ " }()"


-- | Check if a type is assertable from any (has a known Go representation).
-- Only PRIMITIVE types can be safely asserted — function types can't because
-- the runtime representation is func(any) any, not func(int) int.
isConcreteType :: T.Type -> Bool
isConcreteType ty = case ty of
    T.TVar _ -> False
    T.TType _ name _ -> name `elem` ["Int", "Float", "Bool", "String", "Char"]
    T.TUnit -> True
    _ -> False  -- Functions, containers, etc. stay as any


-- | Infer the Sky Type of an arbitrary Can.Expr from the solver's
-- per-name types map. Returns Nothing when the expression can't be
-- statically typed from the available info (lambda body without
-- enough context, polymorphic constraint, missing entry).
--
-- v0.12.x typed-codegen plumbing — used by the typed kernel routing
-- to derive call-site argument types so e.g. `List.map fn xs` with
-- `xs : List Int` routes to `rt.List_mapT[int, any]` instead of
-- `rt.List_mapAny`. Phase 1 of `docs/v012-typed-codegen-plan.md`.
-- | Collapse TVar names to a shared sentinel for cross-module type
-- equality. Two types that differ only in fresh TVar IDs (which the
-- HM solver assigns per-module) are considered equal — e.g. `List _a3`
-- in module A and `List _b7` in module B both normalise to
-- `List _norm`. Used by the cross-module solvedTypes merge to avoid
-- false-positive conflicts when the same name has the same logical
-- shape in multiple modules with different internal TVar names.
normaliseTypeForMerge :: T.Type -> T.Type
normaliseTypeForMerge = go
  where
    go (T.TVar _) = T.TVar "_norm"
    go (T.TType h n args) = T.TType h n (map go args)
    go (T.TLambda a b) = T.TLambda (go a) (go b)
    go (T.TRecord fs ext) =
        T.TRecord
            (Map.map (\(T.FieldType i t) -> T.FieldType i (go t)) fs)
            ext
    go (T.TTuple a b cs) = T.TTuple (go a) (go b) (map go cs)
    go (T.TAlias h n ps aliasTy) = case aliasTy of
        T.Filled t  -> T.TAlias h n ps (T.Filled (go t))
        T.Hoisted t -> T.TAlias h n ps (T.Hoisted (go t))
    go t = t


-- | Look up a record alias from `_cg_aliases` by its field-set. Returns
-- the alias body (a TRecord) on match. Used as a fallback when a stored
-- record type has unresolved TVars in its field types — the alias body
-- carries the user's declared concrete types.
-- v0.13 A1: superset match for open records (parallels
-- `lookupRecordAlias` in Record.hs). Used by typed-codegen field
-- inference (`Can.Access` arm) to resolve an open-row record
-- against a concrete declared alias. Exact match takes priority;
-- on miss, try strict supersets and pick the smallest one.
-- Ambiguous (multiple at the same size) → Nothing.
matchAliasByFieldSet :: Rec.CodegenEnv -> Set.Set String -> Maybe T.Type
matchAliasByFieldSet env target =
    let aliases = Rec._cg_aliases env
        recordAliases =
            [ (Set.fromList (Map.keys fields), body)
            | (_aname, Can.Alias _ body) <- Map.toList aliases
            , T.TRecord fields _ <- [body]
            ]
        exact = [ body | (fs, body) <- recordAliases, fs == target ]
    in case exact of
        (b:_) -> Just b
        []
          | Set.null target -> Nothing
          | otherwise       ->
              let supersets =
                      [ (Set.size fs, body)
                      | (fs, body) <- recordAliases
                      , target `Set.isSubsetOf` fs
                      , target /= fs
                      ]
              in case List.sortOn fst supersets of
                  []                          -> Nothing
                  [(_, b)]                    -> Just b
                  ((s1, b1) : (s2, _) : _)
                      | s1 < s2   -> Just b1
                      | otherwise -> Nothing


-- | Recursively substitute internal TVars in a type by looking them up
-- in solvedTypes. Sky's HM stores record-field types with unresolved
-- TVars (e.g. `List _elem10`); these vars ARE resolved elsewhere in the
-- solvedTypes map but never back-substituted into the stored record.
-- This pass closes that gap so typed-codegen routing can see concrete
-- element types like `List Job` instead of `List _elem10`.
substTypeVars :: Solve.SolvedTypes -> T.Type -> T.Type
substTypeVars types = go Set.empty
  where
    go seen ty = case ty of
        T.TVar name | not (Set.member name seen) ->
            case Solve.lookupSolvedVar name types of
                Just resolved | resolved /= ty -> go (Set.insert name seen) resolved
                _ -> ty
        T.TType home name args -> T.TType home name (map (go seen) args)
        T.TAlias home name pairs (T.Filled inner) ->
            T.TAlias home name pairs (T.Filled (go seen inner))
        T.TAlias home name pairs (T.Hoisted inner) ->
            T.TAlias home name pairs (T.Hoisted (go seen inner))
        T.TRecord fields ext ->
            T.TRecord
                (Map.map (\(T.FieldType ix ft) ->
                    T.FieldType ix (go seen ft)) fields)
                ext
        T.TLambda a b -> T.TLambda (go seen a) (go seen b)
        T.TTuple a b cs -> T.TTuple (go seen a) (go seen b) (map (go seen) cs)
        _ -> ty


inferExprType :: Solve.SolvedTypes -> Can.Expr -> Maybe T.Type
inferExprType types (A.At r e) = case e of
    Can.Int _    -> Just ConstrainExpr.intType
    Can.Float _  -> Just ConstrainExpr.floatType
    Can.Str _    -> Just ConstrainExpr.stringType
    Can.Chr _    -> Just ConstrainExpr.charType
    Can.Unit     -> Just T.TUnit
    Can.VarLocal name    -> case Solve.lookupSolvedVar name types of
        Just t  -> Just t
        Nothing -> lookupLambdaType name
    Can.VarTopLevel _ n  -> Solve.lookupSolvedVar n types
    -- VarKernel: instantiate the kernel's HM annotation. Strips the
    -- Forall wrapper (kernel sigs are universally quantified) so the
    -- result is the raw type with TVars left in place.
    Can.VarKernel modName funcName ->
        case ConstrainExpr.lookupKernelType modName funcName of
            Just (T.Forall _ ty) -> Just ty
            Nothing -> Nothing
    -- Constructor: build a function type from its arg types to the
    -- result type. Annotations carry it directly.
    Can.VarCtor _ _ _ _ (T.Forall _ ty) -> Just ty
    -- A fully-applied call's result is the callee's return type.
    -- Walk splitFuncType to peel off the consumed arrows.
    --
    -- Special cases: for kernels whose result element type ties to
    -- an INPUT arg's element type (List.take/drop/reverse/filter/
    -- concat, etc.), the polymorphic `a` in the callee's type stays
    -- unresolved through splitFuncType. Substitute from the actual
    -- arg type so downstream callers (List.map, Dict.fromList, etc.)
    -- see the concrete result element type.
    Can.Call func args ->
        case func of
            A.At _ (Can.VarKernel "List" name)
                | name `elem` ["take", "drop", "reverse", "filter", "filterMap",
                               "find", "indexedMap", "concat", "concatMap",
                               "append", "cons", "sort", "sortBy"]
                , let listArgIdx = case name of
                          "take" -> 1
                          "drop" -> 1
                          "filter" -> 1
                          "filterMap" -> 1
                          "find" -> 1
                          "indexedMap" -> 1
                          "sortBy" -> 1
                          "append" -> 0
                          "cons" -> 1
                          _ -> 0
                , listArgIdx < length args ->
                    case inferExprType types (args !! listArgIdx) of
                        Just listTy@(T.TType _ "List" _) -> Just listTy
                        _ -> defaultCallResult
            _ -> defaultCallResult
      where
        defaultCallResult = case inferExprType types func of
            Just ft -> Just (snd (splitFuncType (length args) ft))
            Nothing -> Nothing
    -- A list literal's type is `List <element>`. Use the first
    -- element's type when inferable; otherwise leave as any-list.
    --
    -- Soundness guard: scan all elements for a polymorphic-return
    -- call (the `forall a. T -> a` escape-hatch shape that HM
    -- unifies blindly). HM would have unified those call sites'
    -- return TVar against the first element's concrete type, but
    -- the runtime value carries its actual type — typed codegen
    -- monomorphising the list to that concrete type then panics at
    -- runtime when the polymorphic-call's value lands in a typed
    -- slot. Treat the list as polymorphic (TVar "_lit") so
    -- downstream consumers route through the any-typed helpers.
    Can.List items
        | any (callReturnsFreeTVar types) items
          || any (tupleSecondCallsPolymorphic types) items ->
            Just (mkListType (T.TVar "_lit"))
        | otherwise -> case items of
            (x:_) -> case inferExprType types x of
                Just elemTy -> Just (mkListType elemTy)
                Nothing -> Just (mkListType (T.TVar "_lit"))
            [] -> Just (mkListType (T.TVar "_empty"))
    -- Conditional / case branches: take the type of the first arm
    -- if available. The HM solver already unified all arms, so any
    -- arm's type is representative.
    Can.If [] elseExpr -> inferExprType types elseExpr
    Can.If ((_, b):_) _ -> inferExprType types b
    Can.Case _ ((Can.CaseBranch _ b):_) -> inferExprType types b
    -- Field access: requires knowing the parent record's type. The
    -- TRecord type stores field types directly. For named record
    -- aliases (the common case — `post : State_Post_R`), we also
    -- unfold via _cg_aliases so the field's type is recoverable.
    Can.Access record (A.At _ fieldName) ->
        case inferExprType types record of
            Just (T.TRecord fields _) ->
                case Map.lookup fieldName fields of
                    Just (T.FieldType _ ft) ->
                        -- Sky's HM stores record-field types with
                        -- unresolved internal TVars (`List _elem10`). The
                        -- TVars never make it into solvedTypes as
                        -- top-level keys. Fall back to the user's record
                        -- alias: find an alias whose field-set matches
                        -- and read the field's concrete type from there.
                        let env = getCgEnv
                            fieldSet = Set.fromList (Map.keys fields)
                            aliasMatch = matchAliasByFieldSet env fieldSet
                        in case aliasMatch of
                            Just (T.TRecord aliasFields _) ->
                                case Map.lookup fieldName aliasFields of
                                    Just (T.FieldType _ aft) ->
                                        Just (substTypeVars types aft)
                                    Nothing -> Just (substTypeVars types ft)
                            _ -> Just (substTypeVars types ft)
                    Nothing -> Nothing
            -- TAlias is what HM produces for named record aliases.
            -- The Filled/Hoisted inner is the actual unfolded record.
            -- Recurse into the inner type to find the field.
            Just (T.TAlias _ _ _ aliasInner) ->
                let inner = case aliasInner of
                        T.Filled  i -> i
                        T.Hoisted i -> i
                in case inner of
                    T.TRecord fields _ ->
                        case Map.lookup fieldName fields of
                            Just (T.FieldType _ ft) -> Just ft
                            Nothing -> Nothing
                    _ -> Nothing
            -- TType: a non-aliased named type. Check the codegen env's
            -- alias map in case the alias body wasn't unfolded into
            -- TAlias form (older HM paths).
            Just (T.TType _ aliasName _) ->
                let env = getCgEnv
                    matchAlias = Map.lookup aliasName (Rec._cg_aliases env)
                in case matchAlias of
                    Just (Can.Alias _ (T.TRecord fields _)) ->
                        case Map.lookup fieldName fields of
                            Just (T.FieldType _ ft) -> Just ft
                            Nothing -> Nothing
                    _ -> Nothing
            _ -> Nothing
    -- Record literal: build the TRecord type from field types.
    Can.Record fields ->
        let entries = Map.toList fields
            fieldTypes = mapMaybe (\(n, ex) ->
                case inferExprType types ex of
                    Just t  -> Just (n, T.FieldType 0 t)
                    Nothing -> Nothing) entries
        in if length fieldTypes == length entries
            then Just (T.TRecord (Map.fromList fieldTypes) Nothing)
            else Nothing
    -- Tuple: easy if all components type.
    Can.Tuple a b cs ->
        case (inferExprType types a, inferExprType types b, mapM (inferExprType types) cs) of
            (Just ta, Just tb, Just tcs) -> Just (T.TTuple ta tb tcs)
            _ -> Nothing
    -- Lambda: walk the body to build a TLambda.  Preference order:
    --   (1) `Solve.lookupSolvedRegion r types` on the lambda's
    --       whole-expression region — the solver writes the full
    --       TLambda there when it constrained the lambda.  Cheapest
    --       + most accurate.  v0.15.x P37b: now reads the per-region
    --       map directly from the SolvedTypes value flowing in (no
    --       IORef snapshot via `Compile.lookupRegionType`).
    --   (2) Recurse into the body with each PVar param registered
    --       under a fresh placeholder TVar in `types`, and lift the
    --       body's inferred type into a TLambda chain.  The
    --       placeholder TVars guarantee `solvedTypeToGo` collapses
    --       to `any` if a caller tries to render the lambda's
    --       Go-side shape from this result — sound default.
    --
    -- Audit item #2 closure (v0.15.5 PR 3): pre-PR this returned
    -- Nothing universally, leaving `let cb = \x -> …` un-typed and
    -- forcing typed-let routing to fall back to `func() any`.
    Can.Lambda pats body ->
        case Solve.lookupSolvedRegion r types of
            Just t  -> Just t
            Nothing ->
                let paramNames = [n | A.At _ (Can.PVar n) <- pats]
                    paramTVars =
                        [ T.TVar ("_lambda_arg_" ++ show i)
                        | i <- [0 .. length pats - 1]
                        ]
                    types' = Solve.unionSolvedEnv
                        (Map.fromList (zip paramNames paramTVars))
                        types
                in case inferExprType types' body of
                    Just bodyTy -> Just (foldr T.TLambda bodyTy paramTVars)
                    Nothing     -> Nothing
    -- Negate inherits its operand's type.
    Can.Negate inner -> inferExprType types inner
    -- Binop: most operators have a statically-known result type.
    -- v0.13 typed lowerer: this lets `let diff = to - from` infer
    -- `diff : Int` so a later `let absDiff = if diff < 0 …` types
    -- its IIFE as `func() int` instead of `func() any`.
    Can.Binop op _ _ _ left right -> case op of
        -- Comparison / logical operators → Bool.
        _ | op `elem` ["==", "/=", "<", ">", "<=", ">=", "&&", "||"] ->
            Just ConstrainExpr.boolType
        -- Integer-only / float-only operators.
        "//" -> Just ConstrainExpr.intType
        "/"  -> Just ConstrainExpr.floatType
        -- Arithmetic (`+ - * ^`): the result type matches the
        -- operands.  HM has already unified them, so either operand
        -- is representative — try the left, then the right.
        _ | op `elem` ["+", "-", "*", "^"] ->
            case inferExprType types left of
                Just t  -> Just t
                Nothing -> inferExprType types right
        -- `++`: string concat or list concat — the result type
        -- matches the left operand.
        "++" -> inferExprType types left
        -- `::` cons: result is the right operand's list type, or a
        -- list of the left operand's type.
        "::" -> case inferExprType types right of
            Just lt@(T.TType _ "List" _) -> Just lt
            _ -> case inferExprType types left of
                Just elemTy -> Just (mkListType elemTy)
                Nothing     -> Nothing
        -- Pipe (`|>` `<|`): function-application binops — the result
        -- is the function's return type.  `a |> f` ≡ `f a` so the
        -- function side is `right`; `f <| a` ≡ `f a` so the function
        -- side is `left`.  Peel one arrow off the function's type
        -- via `splitFuncType 1`.
        --
        -- Audit item #2 closure — pre-PR these fell into the
        -- catch-all `_ -> Nothing`.
        "|>" -> case inferExprType types right of
            Just fnTy -> Just (snd (splitFuncType 1 fnTy))
            Nothing   -> Nothing
        "<|" -> case inferExprType types left of
            Just fnTy -> Just (snd (splitFuncType 1 fnTy))
            Nothing   -> Nothing
        -- Composition (`>>` `<<`): the result is a one-arg function
        -- (`a -> c`) whose input matches the first-applied function's
        -- input and whose output matches the second-applied
        -- function's output.  `f >> g` applies `f` then `g`; `f << g`
        -- applies `g` then `f`.  Need at least one TLambda on the
        -- "first" side to know its input.
        ">>" -> composeResult left right
        "<<" -> composeResult right left
        -- Unknown operator — fall back.
        _ -> Nothing
    -- Let: the let-expression's type IS the body's type.  Thread
    -- the binding's inferred type into `types` first so the body
    -- can resolve references to it — e.g. `let s = … in let r = …
    -- in if s == "finished" then r else x` needs `r` registered
    -- for the `if`'s first-arm inference to succeed.
    Can.Let def body ->
        let types' = case def of
                Can.Def (A.At _ n) [] valExpr
                    | n /= "_"
                    , Just t <- inferExprType types valExpr ->
                        Solve.insertSolvedVar n t types
                Can.TypedDef (A.At _ n) _ [] valExpr _
                    | Just t <- inferExprType types valExpr ->
                        Solve.insertSolvedVar n t types
                _ -> types
        in inferExprType types' body
    -- LetRec: same shape as `Can.Let` but with [Def] — register
    -- every simple zero-param binding's inferred type into `types`
    -- before inferring the body.  Forward-references across
    -- recursive defs resolve by a single linear pass; that misses
    -- mutual-recursion type inference but is sound (any unresolved
    -- name falls back to `Map.lookup` returning Nothing).
    --
    -- Audit item #2 closure — pre-PR fell into the catch-all.
    Can.LetRec defs body ->
        let extend acc d = case d of
                Can.Def (A.At _ n) [] valExpr
                    | n /= "_"
                    , Just t <- inferExprType acc valExpr ->
                        Solve.insertSolvedVar n t acc
                Can.TypedDef (A.At _ n) _ [] valExpr _
                    | Just t <- inferExprType acc valExpr ->
                        Solve.insertSolvedVar n t acc
                _ -> acc
            types' = foldl extend types defs
        in inferExprType types' body
    -- Update: a record-update expression inherits the original
    -- record's type — `{ rec | f = v }` has the SAME type as `rec`
    -- (record updates preserve nominal alias identity).  This
    -- closes the let-binding case where `let r' = { r | f = v }`
    -- previously lowered as `any` because Update fell through the
    -- catch-all.
    --
    -- Audit item #2 closure.
    Can.Update _ origExpr _changes ->
        inferExprType types origExpr
    -- Accessor (standalone `.fieldName`): a polymorphic one-arg
    -- function `{ rec | fieldName : a } -> a`.  Without a concrete
    -- record type on hand we return a placeholder TVar so callers
    -- that gate on `solvedTypeToGo == "any"` cleanly route through
    -- the any-typed path (matching today's Nothing behaviour) but
    -- a context-aware caller can still see the result is
    -- *something*.
    --
    -- Audit item #2 closure.
    Can.Accessor _fieldName ->
        Just (T.TVar "_accessor_placeholder")
    -- Anything else (chr/other AST shapes already shadowed by the
    -- explicit arms above) — safe fallback.
    _ -> Nothing
  where
    mkListType elemTy = T.TType ModuleName.list "List" [elemTy]
    -- | `>>` / `<<` composition helper.  `first` is the side that
    -- runs first under `apply`; `second` runs second.  Result is
    -- a function from `first`'s input to `second`'s output.
    composeResult first second =
        case (inferExprType types first, inferExprType types second) of
            (Just (T.TLambda fIn _), Just sTy) ->
                Just (T.TLambda fIn (snd (splitFuncType 1 sTy)))
            _ -> Nothing


-- | Compute the Go-type string for an arbitrary Can.Expr by combining
-- inferExprType + solvedTypeToGo. Returns "any" when the expression
-- can't be typed — keeps the kernel routing safe-by-default.
inferGoType :: Solve.SolvedTypes -> Can.Expr -> String
inferGoType types e = case inferExprType types e of
    Just t  -> solvedTypeToGo t
    Nothing -> "any"


-- ─── v0.15.12 P5 / Gap A6 — Auth typed-boundary gate ───────────────


-- | The security-critical Auth kernels and, for each, the 0-indexed
-- positions of the parameter slots whose Sky type is `String`.
--
-- The kernel signatures live in `Sky.Type.Constrain.Expression`'s
-- `lookupKernelType` (see the `("Auth", ...)` cases). The map below
-- is the AUDIT-SURFACE projection: at every call site the gate
-- checks ONLY these positions, because they are the slots whose
-- runtime values reach `mustStringTyped` / `coerceAuthSecret`.
--
-- Sig recap:
--   * hashPassword     :: String -> Result Error String                          → slot 0
--   * hashPasswordCost :: String -> Int -> Result Error String                   → slot 0
--   * passwordStrength :: String -> Result Error String                          → slot 0
--   * verifyPassword   :: String -> String -> Result Error Bool                  → slots 0, 1
--   * signToken        :: String -> a -> Int -> Result Error String              → slot 0 (secret)
--   * verifyToken      :: String -> String -> Result Error a                     → slots 0, 1
--   * register         :: Db -> String -> String -> Task Error Int               → slots 1, 2
--   * login            :: Db -> String -> String -> Task Error Int               → slots 1, 2
--   * setRole          :: Db -> Int -> String -> Task Error ()                   → slot 2
authSecurityKernels :: Map.Map String [Int]
authSecurityKernels = Map.fromList
    [ ("hashPassword",     [0])
    , ("hashPasswordCost", [0])
    , ("passwordStrength", [0])
    , ("verifyPassword",   [0, 1])
    , ("signToken",        [0])
    , ("verifyToken",      [0, 1])
    , ("register",         [1, 2])
    , ("login",            [1, 2])
    , ("setRole",          [2])
    ]


-- | A Sky type that contains the wildcard `any` at ANY position.
-- The wildcard semantics give each occurrence a fresh UF variable,
-- so HM unifies cleanly at the call site even when the binding
-- carries no concrete contract. The audit's soundness gap (A6) is
-- about exactly this: the user-declared annotation includes `any`
-- somewhere → no typed contract reaches the runtime → bypasses
-- `mustStringTyped` if the FFI return is the wrong shape.
typeContainsAny :: T.Type -> Bool
typeContainsAny ty = case ty of
    T.TVar "any"        -> True
    T.TVar _            -> False
    T.TUnit             -> False
    T.TLambda a b       -> typeContainsAny a || typeContainsAny b
    T.TType _ _ args    -> any typeContainsAny args
    T.TRecord fields _  -> any (typeContainsAny . T._fieldType) (Map.elems fields)
    T.TTuple a b cs     -> any typeContainsAny (a : b : cs)
    T.TAlias _ _ pairs at ->
        any (typeContainsAny . snd) pairs ||
        (case at of
            T.Hoisted t -> typeContainsAny t
            T.Filled  t -> typeContainsAny t)


-- | Is the inferred HM type a typed-String contract? Returns False
-- when the type is `TVar _` (unresolved generic) OR when the
-- underlying structure isn't `String` (in which case the regular
-- HM type-check should already have surfaced a mismatch, but we
-- conservatively reject — defence in depth).
--
-- `String` lives in Sky as `T.TType ModuleName.string "String" []`.
-- We compare on the resolved structure rather than the printed name
-- so we ignore module-prefix variants.
authArgIsTyped :: Maybe T.Type -> Bool
authArgIsTyped mty = case mty of
    Nothing -> False
    Just t  -> case stripAlias t of
        T.TType _ "String" [] -> True
        _                     -> False
  where
    -- Peel a TAlias wrapper so `type alias UserEmail = String` still
    -- counts as String at the boundary. The audit's contract is
    -- "the value is a String at runtime", which aliases respect by
    -- definition.
    stripAlias (T.TAlias _ _ _ at) = case at of
        T.Hoisted inner -> stripAlias inner
        T.Filled  inner -> stripAlias inner
    stripAlias x = x


-- | Pre-computed per-module map of (binding name) → (raw
-- source-level annotation type). Built directly from the
-- canonical `Can.TypedDef` shape so we see the USER'S declared
-- annotation, not the solver's post-unification rendering.
--
-- `Can.TypedDef _ _ pats _ retType` — the function type is rebuilt
-- by folding the pattern types onto the retType:
--   `pats : [(p, ty)]` → `T.TLambda p1 (T.TLambda p2 (... retType))`
-- so we capture the WHOLE shape (e.g. `String -> any` rather than
-- losing the `any` to a solver-side substitution).
collectSourceAnnots :: Can.Module -> Map.Map String T.Type
collectSourceAnnots m =
    foldDecls Map.empty (Can._decls m)
  where
    foldDecls acc d = case d of
        Can.SaveTheEnvironment   -> acc
        Can.Declare def rest     -> foldDecls (addDef acc def) rest
        Can.DeclareRec dd ds rest -> foldDecls (foldl addDef acc (dd:ds)) rest

    addDef acc (Can.TypedDef (A.At _ n) _ pats _ retType) =
        let fullTy = foldr T.TLambda retType (map snd pats)
        in Map.insert n fullTy acc
    addDef acc _ = acc


-- | The arg expression's source-level binding annotation, if it
-- carries one and that annotation contains `any` anywhere. Used by
-- the gate as the SECOND layer of the contract check (the first is
-- HM `inferExprType`): even when HM has unified the per-occurrence
-- UF var with String at the call site, a source-level `any`
-- annotation on the binding means the runtime VALUE flowing into
-- the kernel may be ANY Go type. That's the soundness gap A6
-- describes.
--
-- The annotation map is computed once per module (collectSourceAnnots)
-- and threaded in; we look up the binding name with the home-module
-- alias fallback.
--
-- We inspect:
--   * `Can.VarTopLevel home name` → look up the binding's RAW
--     annotation in the source-annot map; True iff the annotation
--     contains `T.TVar "any"` anywhere.
--   * `Can.Call funcE _` → recurse into the head.
--   * Everything else → False (literals / lambdas / case
--     expressions can't carry an `any` contract that bypasses HM).
argSourceCarriesAny :: Map.Map String T.Type -> Can.Expr -> Bool
argSourceCarriesAny srcAnnots (A.At _ e) = case e of
    Can.VarTopLevel _ name ->
        case Map.lookup name srcAnnots of
            Just t  -> typeContainsAny t
            Nothing -> False
    Can.Call funcE _ -> argSourceCarriesAny srcAnnots funcE
    _ -> False


-- | Walk every Can.Expr in a module, calling `visit` at each Can.Call
-- site whose head is a `Can.VarKernel "Auth" _`. The visitor receives
-- the full call expression so it can re-extract args + region for
-- the diagnostic. Tail-recursive accumulator pattern keeps the walk
-- stack-safe on large modules (Std.Ui / skyshop scale).
walkAuthCalls
    :: (A.Region -> String -> [Can.Expr] -> a -> a)
    -> a
    -> Can.Module
    -> a
walkAuthCalls visit z0 m =
    foldDecls (\acc d -> foldDef visit acc d) z0 (Can._decls m)
  where
    foldDecls f acc decls = case decls of
        Can.SaveTheEnvironment   -> acc
        Can.Declare d rest       -> foldDecls f (f acc d) rest
        Can.DeclareRec d ds rest ->
            let acc' = foldl f acc (d:ds) in foldDecls f acc' rest

    foldDef vis acc d = case d of
        Can.Def _ _ body              -> walkExpr vis acc body
        Can.TypedDef _ _ _ body _     -> walkExpr vis acc body
        Can.DestructDef _ body        -> walkExpr vis acc body

    walkExpr vis acc (A.At r expr) = case expr of
        Can.Call funcE args ->
            -- Apply rewriteAliasHead so we see calls like
            -- `Std.Auth.hashPassword …` (a `Can.VarTopLevel`
            -- pointing at the `Ffi.kernel "Auth_hashPassword"`
            -- alias in Std/Auth.sky) as the kernel call they will
            -- become at lowering time. The kernel-alias registry is
            -- populated during canonicalisation so reading it here
            -- (BEFORE codegen) is safe.
            let A.At _ funcV = rewriteAliasHead funcE
                acc1 = case funcV of
                    Can.VarKernel "Auth" name ->
                        vis r name args acc
                    _ -> acc
                acc2 = walkExpr vis acc1 funcE
            in foldl (walkExpr vis) acc2 args
        Can.Lambda _ body  -> walkExpr vis acc body
        Can.If branches el ->
            let acc1 = foldl (\a (c, t) ->
                                  walkExpr vis (walkExpr vis a c) t) acc branches
            in walkExpr vis acc1 el
        Can.Let d body     -> walkExpr vis (foldDef vis acc d) body
        Can.LetRec ds body -> walkExpr vis (foldl (foldDef vis) acc ds) body
        Can.LetDestruct _ valE body ->
            walkExpr vis (walkExpr vis acc valE) body
        Can.Case subjE arms ->
            let acc1 = walkExpr vis acc subjE
            in foldl (\a (Can.CaseBranch _ armE) -> walkExpr vis a armE)
                     acc1 arms
        Can.Access target _ -> walkExpr vis acc target
        Can.Update _ baseE updates ->
            let acc1 = walkExpr vis acc baseE
            in foldl walkUpdate acc1 (Map.elems updates)
        Can.Record fields ->
            foldl (walkExpr vis) acc (Map.elems fields)
        Can.List elems   -> foldl (walkExpr vis) acc elems
        Can.Negate inner -> walkExpr vis acc inner
        Can.Binop _ _ _ _ l rgt ->
            walkExpr vis (walkExpr vis acc l) rgt
        Can.Tuple a b cs ->
            foldl (walkExpr vis) acc (a : b : cs)
        _ -> acc

    walkUpdate acc (Can.FieldUpdate _ e) = walkExpr (\_ _ _ a -> a) acc e


-- | Run the Auth typed-boundary gate against the entry module's
-- canonical AST + solved types. Returns one diagnostic per offending
-- call site; the empty list means every Auth kernel call site
-- carries a typed-String contract on every String slot.
--
-- Why this gate matters (per Audit Gap A6 + P5 plan): Sky's
-- wildcard-`any` semantics give each occurrence its own fresh UF
-- variable, so `bridge : any` paired with `Auth.hashPassword bridge`
-- unifies cleanly at the call site (bridge's UF var = String) even
-- though the bridge's BODY carries no typed contract that the
-- runtime value is actually a String. The runtime
-- `mustStringTyped` check catches the violation late and (pre-P5)
-- leaked the actual Go type into the user-visible message. This
-- gate stops the program from compiling so the user sees the
-- soundness gap at the source instead of as a runtime error in a
-- production audit log.
authBoundaryDiagnostics
    :: FilePath
    -> Solve.SolvedTypes
    -> Can.Module
    -> [Diag.Diagnostic]
authBoundaryDiagnostics filePath solved canMod =
    reverse $ walkAuthCalls visit [] canMod
  where
    srcAnnots = collectSourceAnnots canMod
    -- An arg slot is "bad" iff EITHER its HM-inferred type doesn't
    -- resolve to String OR its source-level binding annotation
    -- contains `any` anywhere.  The two checks compose: HM catches
    -- structural mismatches at the call site (`Int` vs `String`);
    -- the annotation check catches the wildcard-`any` escape hatch
    -- that HM's per-occurrence freshness lets through.
    visit region name args acc =
        case Map.lookup name authSecurityKernels of
            Nothing -> acc
            Just stringSlots ->
                let badSlots =
                        [ (slot, argRegion arg)
                        | slot <- stringSlots
                        , slot < length args
                        , let arg = args !! slot
                        , let mty = inferExprType solved arg
                              hmTyped = authArgIsTyped mty
                              srcAny  = argSourceCarriesAny srcAnnots arg
                        , not hmTyped || srcAny
                        ]
                in case badSlots of
                    [] -> acc
                    bs -> map (mkDiag region name) bs ++ acc

    argRegion (A.At r _) = r

    mkDiag callRegion kernelName (slot, argRegion0) =
        let msg = authBoundaryMessage kernelName slot
            diag = Diag.mkError filePath argRegion0 Diag.CatCodegen
                     Diag.authE_UntypedBoundary msg
            withCall = Diag.withRelated filePath callRegion
                ("at this `Auth." ++ kernelName ++ "` call site")
                diag
        in Diag.withHint authBoundaryHint withCall


authBoundaryMessage :: String -> Int -> String
authBoundaryMessage kernelName slot =
       "Sky.Auth.UntypedBoundary — argument " ++ show (slot + 1)
    ++ " of `Auth." ++ kernelName ++ "` carries no typed-String\n"
    ++ "contract at the Sky type level.\n\n"
    ++ "Security-critical Auth kernels (Auth.hashPassword,\n"
    ++ "hashPasswordCost, passwordStrength, signToken, verifyToken,\n"
    ++ "register, login, setRole) require every String-typed slot\n"
    ++ "to receive a value whose static Sky type resolves to\n"
    ++ "`String`. Bridging an `any`-typed value (or an unresolved\n"
    ++ "type variable) into the slot would unify at the call site\n"
    ++ "but leave the runtime value's Go type unconstrained — the\n"
    ++ "runtime `mustStringTyped` check would then fire on the\n"
    ++ "request hot path."


authBoundaryHint :: String
authBoundaryHint =
       "Annotate the binding feeding this slot with a concrete\n"
    ++ "`String` type, or thread the value through a String-typed\n"
    ++ "validator (e.g. `Maybe.withDefault \"\" maybeString`,\n"
    ++ "`Result.withDefault \"\" resultString`,\n"
    ++ "`String.fromInt`, …) before passing it to the Auth kernel."


-- | Extract the element type of a list-typed expression, as a Go
-- type string. Returns "any" when the expression isn't a list type
-- or when the element type can't be derived. Used by kernel routing
-- for List.* helpers that need the list element type as a generic.
--
-- Defensive: rejects "Anon_R_..." synthesised record names. Those
-- come from HM's anonymous-record handling and don't have Go type
-- alias counterparts emitted by the codegen — passing them to a
-- typed kernel would generate `undefined: Anon_R_xxx` errors.
-- Falling back to "any" forces the default any-routing path which
-- handles anonymous records correctly via reflect.
inferListElemGoType :: Solve.SolvedTypes -> Can.Expr -> String
inferListElemGoType types e
    -- Same soundness guard as inferListTupleSecondGoType: a list
    -- literal containing a polymorphic-return call (`forall a. T -> a`
    -- escape hatch) cannot be typed-routed safely. HM unifies `a` to
    -- whatever the caller asks, but the runtime value carries its
    -- actual type. Detected by walking the AST element-by-element.
    | literalListElementsPolymorphic types e = "any"
    | otherwise = case inferExprType types e of
        Just (T.TType _ "List" [elemTy]) -> sanitiseTypedElem (solvedTypeToGo elemTy)
        Just (T.TAlias _ _ _ aliasInner) ->
            let inner = case aliasInner of
                    T.Filled  i -> i
                    T.Hoisted i -> i
            in case inner of
                T.TType _ "List" [elemTy] -> sanitiseTypedElem (solvedTypeToGo elemTy)
                _ -> "any"
        _ -> "any"


-- | Sister of `inferListElemGoType` that returns the Sky `T.Type` of
-- the list element instead of the rendered Go type string.  Used by
-- typed-lambda emission to populate `withLambdaTypes` so the lambda
-- body's operations (binops, var lookups) resolve to typed Go-native
-- forms rather than falling back to `rt.*` reflect helpers.
inferListElemSkyType :: Solve.SolvedTypes -> Can.Expr -> Maybe T.Type
inferListElemSkyType types e
    | literalListElementsPolymorphic types e = Nothing
    | otherwise = case inferExprType types e of
        Just (T.TType _ "List" [elemTy]) -> Just elemTy
        Just (T.TAlias _ _ _ aliasInner) ->
            let inner = case aliasInner of
                    T.Filled  i -> i
                    T.Hoisted i -> i
            in case inner of
                T.TType _ "List" [elemTy] -> Just elemTy
                _ -> Nothing
        _ -> Nothing


-- | Build a `Map String T.Type` from a list of simple-var Sky
-- patterns paired with their HM-inferred types.  Skips non-PVar
-- patterns (the caller has already gated typed routing on
-- `isSimpleVarPattern`).  Used to wrap typed-lambda body emission
-- with `withLambdaTypes` so locals resolve correctly.
patVarTypes :: [Can.Pattern] -> [T.Type] -> Map.Map String T.Type
patVarTypes pats tys =
    Map.fromList
        [ (n, t)
        | (A.At _ (Can.PVar n), t) <- zip pats tys
        , not (isWildcardSkyType t)
        ]
  where
    isWildcardSkyType (T.TVar "_unknown") = True
    isWildcardSkyType _                   = False


-- | Best-effort conversion from a Go type string (e.g. "int",
-- "string", "rt.SkyMaybe[string]") back to a Sky `T.Type`.  Used at
-- typed-lambda emission boundaries where the lambda's input types
-- are known as Go strings (from `splitCurriedFuncTypeStr`) but we
-- need the Sky T.Type to register in `withLambdaTypes`.  Returns
-- `T.TVar "_unknown"` for shapes we can't safely reverse-map — those
-- are then filtered out by `patVarTypes` so binop emission falls
-- through to the any-routed runtime helpers.
goTypeStrToSkyType :: String -> T.Type
goTypeStrToSkyType s = case s of
    "int"        -> ConstrainExpr.intType
    "float64"    -> ConstrainExpr.floatType
    "string"     -> ConstrainExpr.stringType
    "bool"       -> ConstrainExpr.boolType
    "rune"       -> ConstrainExpr.charType
    _            -> T.TVar "_unknown"


-- | v0.13 typed lowerer guard: is this expression guaranteed to lower
-- to a Go value whose STATIC type matches its HM-inferred Sky type?
-- True for primitive literals (Int, Float, String, Bool, Char, Unit)
-- and for `Can.VarLocal` references whose name is registered in
-- the lambda-types scope (typed lambda params).  False for everything
-- else — call results, field access, etc. — because runtime kernels
-- (e.g. `rt.Crypto_sha256`) return Go `any` even when HM says the
-- result is String, and forcing a Go-native binop on those would
-- produce `mismatched types string and any`.
operandIsStaticallyTyped :: Can.Expr -> Bool
operandIsStaticallyTyped (A.At _ e) = case e of
    Can.Int _    -> True
    Can.Float _  -> True
    Can.Str _    -> True
    Can.Chr _    -> True
    Can.Unit     -> True
    Can.VarLocal name ->
        -- Gate on Go static type: registered HM type must map to a
        -- concrete Go type (not "any", not bare `T_N`).
        case lookupLambdaType name of
            Just t  ->
                let goTy = solvedTypeToGo t
                in goTy /= "any" && not (isGenericTypeParam goTy)
            Nothing -> False
    Can.Negate inner -> operandIsStaticallyTyped inner
    -- Record field access on a statically-typed target: the field
    -- access emits `target.Field` (typed) AND the field's HM type
    -- is the field's declared type in the record alias.  Both
    -- conditions are gated by the same `operandIsStaticallyTyped`
    -- check recursively + the `isRecordAlias` check in the Can.
    -- Access emit branch.
    Can.Access target _ -> operandIsStaticallyTyped target
    _            -> False


-- | Like literalListHasPolymorphicReturn but checks raw element
-- expressions (not nested tuples). Used by inferListElemGoType for
-- typed-routing of List.map / List.filter / etc.
literalListElementsPolymorphic :: Solve.SolvedTypes -> Can.Expr -> Bool
literalListElementsPolymorphic types (A.At _ (Can.List items)) =
    any (callReturnsFreeTVar types) items
literalListElementsPolymorphic _ _ = False


-- | Reject element types that aren't safe to use in AsListT[T] coercion.
--
-- Anon_R_xxx synthesised record names — no Go type alias is emitted
-- for these, would produce `undefined: Anon_R_xxx`. Falling back to
-- "any" routes through the legacy non-generic helper.
--
-- The earlier `rt.*` rejection was a workaround for cross-module
-- name shadowing in the merged solvedTypes (e.g. `children` resolving
-- to `List rt.VNode` from Std.Html when emitting Std.Ui code, where
-- the right type was `List (Element msg)`). That root cause is now
-- handled at MERGE time: `typesWithDeps` in Compile.hs detects when
-- two modules assign different concrete types to the same binder
-- name and replaces the key with a TVar (resolves to "any" in
-- solvedTypeToGo). With the conflict-detection merge, this filter
-- is no longer load-bearing for the `rt.*` class.
sanitiseTypedElem :: String -> String
sanitiseTypedElem go
    | "Anon_R_" `List.isPrefixOf` go = "any"
    | otherwise = go


-- | v0.13 Phase A5+: recursive variant of `sanitiseTypedElem` that
-- walks a complete Go-type string and replaces every embedded
-- `Anon_R_<hash>` identifier token (anonymous record with no Go
-- type-alias counterpart) with `any`.  Used by the call-site
-- substitution path so a Result/Maybe wrapper around an anonymous
-- record (`Result Error { name : String, … }`) collapses to a
-- usable `rt.SkyResult[Error, any]` instead of emitting
-- `rt.SkyResult[Error, Anon_R_…]` which Go can't resolve.
--
-- Identifier-aware: only replaces tokens that BEGIN with `Anon_R_`
-- and whose preceding char is a non-identifier boundary, so
-- `Anon_R_xxx_in_a_bigger_name` isn't false-matched.  The walker
-- preserves brackets, commas, and other type-string punctuation
-- verbatim so structural shapes (`[]X`, `map[string]X`,
-- `rt.SkyResult[E, A]`) survive intact.
-- | v0.13 E removed the `Anon_R_*` → `any` rewrite that this
-- function used to apply: `synthAnonRecordName` now registers
-- every produced shape into `globalAnonRecords`, and
-- `generateAnonRecordDecls` (wired into `generateGoMulti` after
-- the user decls evaluate) emits one `type Anon_R_<hash> =
-- struct {…}` for each. The token is now a valid Go type
-- identifier, so the pre-E defensive cover-up is no longer
-- needed. Kept as a no-op pass-through so call sites don't
-- have to be touched.
sanitiseTypedDeep :: String -> String
sanitiseTypedDeep s = s


-- | Strip a Go type string of the form `rt.SkyMaybe[INNER]` returning
-- INNER. Returns Nothing for any other shape. Used by wrapAsT to
-- route SkyMaybe targets through MaybeCoerce (lossless across
-- arbitrary source SkyMaybe[X] including Nothing[any]).
stripSkyMaybe :: String -> Maybe String
stripSkyMaybe s = stripWrapper "rt.SkyMaybe[" s


-- | Strip a Go type string of the form `rt.SkyResult[E, A]` returning
-- (E, A). Splits on the first top-level comma (respecting nested
-- brackets) so generic-parameterised E / A round-trip correctly.
stripSkyResult :: String -> Maybe (String, String)
stripSkyResult s = case stripWrapper "rt.SkyResult[" s of
    Just inner -> splitGenericArgs inner
    Nothing    -> Nothing


-- | Strip a Go slice type `[]ELEM` returning ELEM, with two
-- restrictions: ELEM is non-empty and ELEM /= "any" (already-typed
-- AsListT is needed only for concrete element types; `[]any` is
-- fine as-is). Returns Nothing for non-slice shapes.
stripSlice :: String -> Maybe String
stripSlice s = case s of
    '[' : ']' : rest
        | null rest        -> Nothing
        | rest == "any"    -> Nothing
        | otherwise        -> Just rest
    _ -> Nothing


-- | Strip a Go map type `map[string]VAL` returning VAL. Restricted to
-- string-keyed maps because that's the only shape Sky's Dict
-- produces. Returns Nothing for non-string-keyed shapes and for
-- `map[string]any` (already polymorphic, no coercion needed).
-- Note: the value type may itself contain brackets (e.g. nested
-- `map[string]map[string]int`) so we DON'T require the input to
-- end with `]`.
stripStringMap :: String -> Maybe String
stripStringMap s
    | prefix `List.isPrefixOf` s =
        let inner = drop (length prefix) s
        in if inner /= "any" && not (null inner) then Just inner else Nothing
    | otherwise = Nothing
  where prefix = "map[string]"


-- | Strip a `prefix[INNER]` wrapper, ensuring the closing bracket is
-- the very last char. Returns INNER on success.
stripWrapper :: String -> String -> Maybe String
stripWrapper prefix s
    | prefix `List.isPrefixOf` s
    , not (null s)
    , last s == ']'
    = Just (drop (length prefix) (init s))
    | otherwise = Nothing


-- | Split a generic-arg list "E, A" into ("E", "A") at the first
-- top-level comma (depth 0 — respecting nested brackets so that
-- `Foo[X, Y], Bar` still splits on the outer comma).
splitGenericArgs :: String -> Maybe (String, String)
splitGenericArgs = go 0 ""
  where
    go _     _   ""           = Nothing
    go depth acc (c:rest)
        | c == '[' || c == '(' = go (depth + 1) (acc ++ [c]) rest
        | c == ']' || c == ')' = go (depth - 1) (acc ++ [c]) rest
        | c == ',' && depth == 0 = case dropWhile (== ' ') rest of
            r' -> Just (acc, r')
        | otherwise            = go depth (acc ++ [c]) rest


-- | Extract the value type of a Dict-typed expression. Returns "any"
-- on non-Dict / unresolved / anonymous-record element types. Mirror
-- of inferListElemGoType for the Dict family.
inferDictValueGoType :: Solve.SolvedTypes -> Can.Expr -> String
inferDictValueGoType types e = case inferExprType types e of
    Just (T.TType _ "Dict" [_, valTy]) ->
        let go = solvedTypeToGo valTy
        in if "Anon_R_" `List.isPrefixOf` go then "any" else go
    _ -> "any"


-- | Extract the inner type of a Maybe-typed expression. e.g.
-- `Maybe Int` → "int". Returns "any" when the expression isn't a
-- Maybe or when the inner type isn't statically derivable.
inferMaybeInnerGoType :: Solve.SolvedTypes -> Can.Expr -> String
inferMaybeInnerGoType types e = case inferExprType types e of
    Just (T.TType _ "Maybe" [innerTy]) ->
        let go = solvedTypeToGo innerTy
        in if "Anon_R_" `List.isPrefixOf` go then "any" else go
    _ -> "any"


-- | Extract V from a List (String, V) — used by Dict.fromList typed
-- routing. The HM-side rep of `(String, V)` is `T.TTuple String V []`,
-- so we look inside the List's element type. Returns "any" when the
-- list isn't a list of tuples, the tuple isn't (String, V), or V is
-- itself anonymous/synthetic.
inferListTupleSecondGoType :: Solve.SolvedTypes -> Can.Expr -> String
inferListTupleSecondGoType types e =
    -- Soundness guard: if the list expression is a literal whose
    -- elements include a value-position call to a function whose
    -- DECLARED return type is a free TVar (the `forall a. ... -> a`
    -- escape-hatch shape), the typed-codegen monomorphisation would
    -- be unsound — HM unifies the TVar to whatever the caller wants,
    -- but the runtime value's actual type is whatever the function
    -- chose to return. Bail to "any" routing so the runtime helper
    -- keeps the heterogeneous values as-is rather than tripping a
    -- typed-Coerce panic. See `examples/13-skyshop/src/Lib/Db.sky`'s
    -- `boolVal : Bool -> a` for the canonical case.
    if literalListHasPolymorphicReturn types e then "any"
    else case inferExprType types e of
        Just (T.TType _ "List" [elemTy]) -> tupleSnd elemTy
        Just (T.TAlias _ _ _ aliasInner) ->
            let inner = case aliasInner of
                    T.Filled  i -> i
                    T.Hoisted i -> i
            in case inner of
                T.TType _ "List" [elemTy] -> tupleSnd elemTy
                _ -> "any"
        _ -> "any"
  where
    tupleSnd ty = case ty of
        T.TTuple _ b _ ->
            let go = solvedTypeToGo b
            in if "Anon_R_" `List.isPrefixOf` go then "any" else go
        T.TAlias _ _ _ aliasInner ->
            let inner = case aliasInner of
                    T.Filled  i -> i
                    T.Hoisted i -> i
            in tupleSnd inner
        _ -> "any"


-- | Does the expression evaluate to a `[]any` whose element comes
-- from a call to a function whose declared return type is a free
-- type variable (the unsound `forall a. T -> a` escape hatch)?
--
-- Walks the AST for `Can.List items` and inspects each item — if
-- any item's value-position is a `Can.Call` to such a function,
-- returns True. False on non-list-literal expressions (caller
-- handles those by reading the inferred type directly).
literalListHasPolymorphicReturn :: Solve.SolvedTypes -> Can.Expr -> Bool
literalListHasPolymorphicReturn types (A.At _ (Can.List items)) =
    any (tupleSecondCallsPolymorphic types) items
literalListHasPolymorphicReturn _ _ = False


-- | Returns True when the expression is a tuple whose SECOND element
-- (the dict value-position) is a call to a polymorphic-return
-- function. Falls back across simple ADT/Tuple wrappers.
tupleSecondCallsPolymorphic :: Solve.SolvedTypes -> Can.Expr -> Bool
tupleSecondCallsPolymorphic types (A.At _ e) = case e of
    Can.Tuple _ v _ -> callReturnsFreeTVar types v
    _ -> False


-- | Returns True when the expression is a call (full application or
-- partial — we walk through the lambda spine) whose declared return
-- type is a free TVar. Conservatively returns False on shapes we
-- can't introspect — we'd rather miss a soundness check than emit
-- a false-positive any-routing.
callReturnsFreeTVar :: Solve.SolvedTypes -> Can.Expr -> Bool
callReturnsFreeTVar types (A.At _ e) = case e of
    Can.Call callee args ->
        case inferExprType types callee of
            Just calleeTy ->
                let (_, retTy) = splitFuncType (length args) calleeTy
                in isFreeTVar retTy
            Nothing -> False
    _ -> False


-- | True when this is a bare type variable. Doesn't care which
-- letter — `a`, `b`, `msg`, `_e23` all qualify. Concrete types
-- (Int, String, List, etc.) all return False.
isFreeTVar :: T.Type -> Bool
isFreeTVar (T.TVar _) = True
isFreeTVar _ = False


-- | Extract the (E, A) types of a Result-typed expression. Returns
-- (Just (eGo, aGo)) when both types are concrete (and not Anon_R_),
-- Nothing otherwise.
inferResultGoTypes :: Solve.SolvedTypes -> Can.Expr -> Maybe (String, String)
inferResultGoTypes types e = case inferExprType types e of
    Just (T.TType _ "Result" [eTy, aTy]) ->
        let eGo = solvedTypeToGo eTy
            aGo = solvedTypeToGo aTy
            isAnon t = "Anon_R_" `List.isPrefixOf` t
        in if isAnon eGo || isAnon aGo then Nothing
           else Just (eGo, aGo)
    _ -> Nothing


-- | Try to emit a typed kernel call (rt.List_mapT[int, any](...))
-- instead of the default any-routing (rt.List_mapAny(...)). Returns
-- Just (typed-call-expr) when ALL of:
--
--   * The kernel has a typed runtime variant in our routing table.
--   * The relevant call-site arg types are derivable.
--
-- Returns Nothing in every other case → caller falls back to the
-- default kernelToGo path. v0.12.x typed-codegen — Phase 3 of the
-- staged plan in `docs/v012-typed-codegen-plan.md`.
--
-- The decision to route typed vs any-typed is conservative: when in
-- doubt, default to any. The any-routed kernels still work; typed
-- routing is an additive optimisation. This keeps the change
-- regression-safe — every example that currently builds keeps
-- building.
kernelTypedCall
    :: Solve.SolvedTypes
    -> String       -- ^ module name
    -> String       -- ^ function name
    -> [Can.Expr]   -- ^ call-site args
    -> [GoIr.GoExpr] -- ^ pre-lowered Go args
    -> Maybe GoIr.GoExpr
kernelTypedCall types modName funcName args goArgs =
    -- Helper: wrap an arg with rt.AsListT[ElemType] so a runtime
    -- any-typed value (e.g. from rt.Field or List_mapAny) is
    -- converted to the typed Go slice []ElemType the typed kernel
    -- expects. Without this wrapper the Go compiler rejects the
    -- call: "cannot use any value as []T value in argument".
    let wrapAsList :: String -> GoIr.GoExpr -> GoIr.GoExpr
        wrapAsList elemGo e =
            GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elemGo ++ "]")) [e]
        -- v0.12 SAFE element-type inference. Derive the list
        -- element type from the LAMBDA's input type rather than
        -- from the list arg. This is SAFER because HM enforces
        -- the list's element matches the lambda's input — so
        -- typed routing can never produce a wrong type. The
        -- list arg's stored type may be polluted by intra-module
        -- shadowing (same `visible` bound twice with different
        -- types in different functions); the lambda's input is
        -- annotation-driven and immune to that class of bug.
        inferElemFromLambdaInput :: Can.Expr -> Maybe String
        inferElemFromLambdaInput fn = case fn of
            A.At _ (Can.VarTopLevel home name) ->
                lookupFnInputAt home name 0
            -- Partial application: Can.Call (Can.VarTopLevel _ _) [args].
            -- The remaining first param is at index `length args` in the
            -- full param list. E.g. `renderElement renderCtx []` is the
            -- THIRD-param position of renderElement (after 2 args).
            A.At _ (Can.Call (A.At _ (Can.VarTopLevel home name)) appliedArgs) ->
                lookupFnInputAt home name (length appliedArgs)
            _ -> Nothing
        lookupFnInputAt :: ModuleName.Canonical -> String -> Int -> Maybe String
        lookupFnInputAt home name idx =
            let env = getCgEnv
                qualKey = map (\c -> if c == '.' then '_' else c)
                    (ModuleName.toString home) ++ "_" ++ name
            in case Map.lookup qualKey (Rec._cg_funcParamTypes env) of
                Just params | length params > idx ->
                    let s = sanitiseTypedElem (params !! idx)
                    in if s == "any" then Nothing else Just s
                _ -> Nothing
        -- Derive the lambda's RETURN type (B in `a -> b`) from a
        -- top-level function's annotated return type. Used to drive
        -- full `rt.List_mapT[A, B]` instead of `rt.List_mapT[A, any]`.
        inferRetFromTopLevel :: Can.Expr -> Maybe String
        inferRetFromTopLevel fn = case fn of
            A.At _ (Can.VarTopLevel home name) ->
                let env = getCgEnv
                    qualKey = map (\c -> if c == '.' then '_' else c)
                        (ModuleName.toString home) ++ "_" ++ name
                in case Map.lookup qualKey (Rec._cg_funcRetType env) of
                    Just r ->
                        let s = sanitiseTypedElem r
                        in if s == "any" then Nothing else Just s
                    _ -> Nothing
            _ -> Nothing
        -- Prefer the lambda-input-derived element type; fall back
        -- to the list-arg-derived type only if the function isn't
        -- a known top-level binding.
        elemTypeFromFnOrList :: Can.Expr -> Can.Expr -> String
        elemTypeFromFnOrList fnArg listArg =
            case inferElemFromLambdaInput fnArg of
                Just s  -> s
                Nothing ->
                    -- If the fn is a top-level function reference (or
                    -- partial application thereof) and we couldn't get
                    -- its input type, don't trust the list-arg lookup —
                    -- it's vulnerable to intra/cross-module shadowing.
                    -- Return "any" to fall back to safe any-routing.
                    if isTopLevelFnRef fnArg then "any"
                    else inferListElemGoType types listArg
        isTopLevelFnRef :: Can.Expr -> Bool
        isTopLevelFnRef (A.At _ e) = case e of
            Can.VarTopLevel _ _ -> True
            Can.Call (A.At _ (Can.VarTopLevel _ _)) _ -> True
            _ -> False
    in case (modName, funcName, args, goArgs) of
        -- List.map fn xs : (a -> b) -> List a -> List b
        -- v0.12.x Gap 4: if fn is a literal `Can.Lambda`, re-emit it
        -- as a TYPED Go func `func(x A) any` (Gap 4 lambda lowering)
        -- and route to the fully-typed `rt.List_mapT[A, any]` runtime
        -- variant. Otherwise fall back to the TA variant (typed
        -- slice, any-typed function).
        ("List", "map", [_, _], [goFn, goList]) ->
            let elemGo = elemTypeFromFnOrList (args !! 0) (args !! 1)
                elemSkyTy = inferListElemSkyType types (args !! 1)
            in if elemGo == "any" then Nothing
               else case args !! 0 of
                    -- Lambda with simple var pattern: typed-T route is
                    -- safe — patternBindings doesn't need to destructure.
                    A.At _ (Can.Lambda pats body)
                      | all isSimpleVarPattern pats ->
                        -- v0.13 typed lowerer: push the lambda's param
                        -- HM-types into scope so the body's binops /
                        -- var refs resolve to typed Go-native forms
                        -- (e.g. `x + 1` instead of `rt.Add(x, 1)`).
                        let lambdaTypes = case elemSkyTy of
                                Just t  -> patVarTypes pats [t]
                                Nothing -> Map.empty
                            body' = withLambdaTypes lambdaTypes (exprToGo body)
                            typedFn = curryLambdaPatTyped [elemGo] "any" pats body'
                        in Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.List_mapT[" ++ elemGo ++ ", any]"))
                            [typedFn, wrapAsList elemGo goList])
                    -- Lambda with complex pattern (tuple/record/ctor
                    -- destructure): fall back to fully-any routing.
                    -- The body's `.(SkyTuple2)` style assertions assume
                    -- any-typed input; if the slice is typed, elements
                    -- are typed-instantiated (e.g. T2[string,string])
                    -- and the assertion fails. List_map keeps the slice
                    -- and elements as `any` so destructure works.
                    A.At _ (Can.Lambda _ _) ->
                        Just (GoIr.GoCall
                            (GoIr.GoQualified "rt" "List_map")
                            [goFn, goList])
                    -- Non-lambda fn (top-level func ref, partial-app):
                    -- TA variant works fine — fn dispatches via SkyCall
                    -- which handles boxing.
                    _ -> Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.List_mapTA[" ++ elemGo ++ "]"))
                            [goFn, wrapAsList elemGo goList])
        -- List.filter fn xs : (a -> Bool) -> List a -> List a
        ("List", "filter", [_, _], [goFn, goList]) ->
            let elemGo = elemTypeFromFnOrList (args !! 0) (args !! 1)
                elemSkyTy = inferListElemSkyType types (args !! 1)
            in if elemGo == "any" then Nothing
               else case args !! 0 of
                    A.At _ (Can.Lambda pats body)
                      | all isSimpleVarPattern pats ->
                        -- List_filterT[A](fn func(A) bool, xs []A) []A
                        -- v0.13 typed lowerer: push elem type into scope.
                        let lambdaTypes = case elemSkyTy of
                                Just t  -> patVarTypes pats [t]
                                Nothing -> Map.empty
                            body' = withLambdaTypes lambdaTypes (exprToGo body)
                            typedFn = curryLambdaPatTyped [elemGo] "bool" pats body'
                        in Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.List_filterT[" ++ elemGo ++ "]"))
                            [typedFn, wrapAsList elemGo goList])
                    -- Lambda with complex pattern: fall back to
                    -- fully-any routing — same reasoning as List.map.
                    A.At _ (Can.Lambda _ _) ->
                        Just (GoIr.GoCall
                            (GoIr.GoQualified "rt" "List_filter")
                            [goFn, goList])
                    _ -> Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.List_filterTA[" ++ elemGo ++ "]"))
                            [goFn, wrapAsList elemGo goList])
        -- List.foldl fn seed xs : (a -> b -> b) -> b -> List a -> b.
        -- The 2-arg lambda is curried in Sky; with `b = any` we can
        -- route to List_foldlT[A, any] taking func(A, any) any (Go's
        -- 2-arg form). But curryLambdaPatTyped emits a curried fn
        -- shape (func(A) func(B) any), which doesn't match. Keep TA
        -- here; revisit when we add an un-curried typed fold helper.
        ("List", "foldl", [_, _, _], [goFn, goSeed, goList]) ->
            let elemGo = inferListElemGoType types (args !! 2)
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_foldlTA[" ++ elemGo ++ "]"))
                    [goFn, goSeed, wrapAsList elemGo goList])
        -- List.length xs : List a -> Int.
        ("List", "length", [_], [goList]) ->
            let elemGo = inferListElemGoType types (args !! 0)
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_lengthT[" ++ elemGo ++ "]"))
                    [wrapAsList elemGo goList])
        -- List.head xs : List a -> Maybe a.
        ("List", "head", [_], [goList]) ->
            let elemGo = inferListElemGoType types (args !! 0)
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_headT[" ++ elemGo ++ "]"))
                    [wrapAsList elemGo goList])
        -- List.reverse xs : List a -> List a.
        ("List", "reverse", [_], [goList]) ->
            let elemGo = inferListElemGoType types (args !! 0)
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_reverseT[" ++ elemGo ++ "]"))
                    [wrapAsList elemGo goList])
        -- List.take n xs / List.drop n xs : Int -> List a -> List a.
        -- The `n` arg's typed Go param is `int`; the runtime value
        -- might be `any` (came from rt.AdtField, rt.Field record
        -- access, or a function-call result). Coerce via rt.AsInt
        -- to keep Go's typed-generic dispatch happy.
        ("List", "take", [_, _], [goN, goList]) ->
            let elemGo = inferListElemGoType types (args !! 1)
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_takeT[" ++ elemGo ++ "]"))
                    [wrapAsT "int" goN, wrapAsList elemGo goList])
        ("List", "drop", [_, _], [goN, goList]) ->
            let elemGo = inferListElemGoType types (args !! 1)
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_dropT[" ++ elemGo ++ "]"))
                    [wrapAsT "int" goN, wrapAsList elemGo goList])
        -- List.append a b : List x -> List x -> List x.
        ("List", "append", [_, _], [goA, goB]) ->
            let aElem = inferListElemGoType types (args !! 0)
                bElem = inferListElemGoType types (args !! 1)
                pick = if aElem /= "any" then aElem else bElem
            in if pick == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_appendT[" ++ pick ++ "]"))
                    [wrapAsList pick goA, wrapAsList pick goB])
        -- List.member item xs : a -> List a -> Bool. Element type
        -- typically from the list arg, but List.member's signature
        -- `a -> List a -> Bool` means the key and list share `a`.
        -- When the list arg's type is unresolvable (e.g. record
        -- access on a name shadowed across modules in the merged
        -- solvedTypes), fall back to inferring `a` from the KEY arg.
        -- This is sound: HM has already unified the two; the runtime
        -- AsListT[T] reflect coercion handles the actual any-typed
        -- slice at the boundary.
        ("List", "member", [itemArg, listArg], [goItem, goList]) ->
            let elemFromList = inferListElemGoType types listArg
                elemFromItem = inferGoType types itemArg
                elemGo = if elemFromList /= "any" then elemFromList else elemFromItem
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_memberT[" ++ elemGo ++ "]"))
                    [wrapAsT elemGo goItem, wrapAsList elemGo goList])
        -- List.indexedMap fn xs : (Int -> a -> b) -> List a -> List b.
        ("List", "indexedMap", [_, listArg], [goFn, goList]) ->
            let elemGo = inferListElemGoType types listArg
            in if elemGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.List_indexedMapTA[" ++ elemGo ++ "]"))
                    [goFn, wrapAsList elemGo goList])
        -- List.find fn xs : (a -> Bool) -> List a -> Maybe a.
        ("List", "find", [_, _], [goFn, goList]) ->
            let elemGo = elemTypeFromFnOrList (args !! 0) (args !! 1)
                elemSkyTy = inferListElemSkyType types (args !! 1)
            in if elemGo == "any" then Nothing
               else case args !! 0 of
                    A.At _ (Can.Lambda pats body)
                      | all isSimpleVarPattern pats ->
                        -- No fully-typed `List_findT[A]` runtime variant
                        -- yet; the TA shape (typed slice, any fn) is
                        -- the best routing here. Reused for find/member
                        -- so the lambda shape stays the same.
                        let lambdaTypes = case elemSkyTy of
                                Just t  -> patVarTypes pats [t]
                                Nothing -> Map.empty
                            body' = withLambdaTypes lambdaTypes (exprToGo body)
                            typedFn = curryLambdaPatTyped [elemGo] "bool" pats body'
                            -- TA helper expects fn as any; box the
                            -- typed func.
                            anyFn = GoIr.GoCall (GoIr.GoIdent "any") [typedFn]
                        in Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.List_findTA[" ++ elemGo ++ "]"))
                            [anyFn, wrapAsList elemGo goList])
                    _ -> Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.List_findTA[" ++ elemGo ++ "]"))
                            [goFn, wrapAsList elemGo goList])

        -- Dict.* typed routing — Phase 3 batch 2. The same
        -- pattern as List.*: typed value generic for the Dict's
        -- value type; key is always String in Sky's Dict. The
        -- runtime AsDict helper converts any-typed values
        -- (rt.Field on records) to a typed map; no-op on already-
        -- typed maps so this is regression-safe.
        ("Dict", "get", [_, dictArg], [goKey, goDict]) ->
            let valGo = inferDictValueGoType types dictArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_getT[" ++ valGo ++ "]"))
                    [wrapAsString goKey, wrapAsDict valGo goDict])
        ("Dict", "insert", [_, _, dictArg], [goKey, goVal, goDict]) ->
            let valGo = inferDictValueGoType types dictArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_insertT[" ++ valGo ++ "]"))
                    [wrapAsString goKey, wrapAsT valGo goVal, wrapAsDict valGo goDict])
        ("Dict", "remove", [_, dictArg], [goKey, goDict]) ->
            let valGo = inferDictValueGoType types dictArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_removeT[" ++ valGo ++ "]"))
                    [wrapAsString goKey, wrapAsDict valGo goDict])
        ("Dict", "member", [_, dictArg], [goKey, goDict]) ->
            let valGo = inferDictValueGoType types dictArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_memberT[" ++ valGo ++ "]"))
                    [wrapAsString goKey, wrapAsDict valGo goDict])
        ("Dict", "keys", [dictArg], [goDict]) ->
            let valGo = inferDictValueGoType types dictArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_keysT[" ++ valGo ++ "]"))
                    [wrapAsDict valGo goDict])
        ("Dict", "values", [dictArg], [goDict]) ->
            let valGo = inferDictValueGoType types dictArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_valuesT[" ++ valGo ++ "]"))
                    [wrapAsDict valGo goDict])

        -- Dict.fromList list : List (String, V) -> Dict String V
        -- Routes to Dict_fromListT[V] when V is concrete. Reads V from
        -- the inferred type of the list's element tuple (second slot).
        ("Dict", "fromList", [listArg], [goList]) ->
            let valGo = inferListTupleSecondGoType types listArg
            in if valGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_fromListT[" ++ valGo ++ "]"))
                    [wrapAsList "any" goList])

        -- Dict.map fn dict : (String -> V -> W) -> Dict String V -> Dict String W
        -- Sky's Dict.map is 2-arg curried (K -> V -> W). The single-arg
        -- runtime Dict_mapT[V,W] (which discards the key) doesn't match
        -- this shape. Use Dict_map2T[V,W] which calls the curried fn as
        -- fn(k)(v). Both input and OUTPUT types must be concrete since
        -- the result map is `map[string]W`; we infer V from the dict arg
        -- and W from the call's expected type via _cg_funcRetType or
        -- via the lambda's body. For literal lambdas we re-emit with
        -- the typed body's return type. Conservative — only routes when
        -- both V and W are concrete.
        ("Dict", "map", [fnArg, dictArg], [goFn, goDict]) ->
            let valGo = inferDictValueGoType types dictArg
                -- Infer output type from the lambda's innermost body.
                -- For `\_ v -> anyToString v`, the body is the
                -- Can.Call. inferExprType handles Can.Call by walking
                -- the callee's return type via splitFuncType.
                -- peelLambda walks past nested Can.Lambda layers
                -- (Sky-curried form) to reach the expression whose
                -- HM type IS W.
                peelLambda outer@(A.At _ e) = case e of
                    Can.Lambda _ innerBody -> peelLambda innerBody
                    _ -> outer
                outGo = case fnArg of
                    A.At _ (Can.Lambda _ body) ->
                        let innermost = peelLambda body
                        in case inferExprType types innermost of
                            Just bodyTy -> sanitiseTypedElem (solvedTypeToGo bodyTy)
                            Nothing -> "any"
                    _ -> "any"
                -- Route as long as the OUTPUT type is concrete. Even
                -- when V is opaque (e.g. FFI rawMap with Dict String any
                -- values), Dict_map2T[any, W] still wins via typed
                -- output (callers get map[string]W, no further coerce).
            in if outGo == "any" then Nothing
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Dict_map2T[" ++ valGo ++ ", " ++ outGo ++ "]"))
                    [goFn, wrapAsDict valGo goDict])

        -- Maybe.withDefault def m : a -> Maybe a -> a
        -- Routes to Maybe_withDefaultT[A] when A is concrete. The
        -- runtime variant takes a typed `def : A` and a typed
        -- `SkyMaybe[A]`, returning typed A. We coerce both.
        ("Maybe", "withDefault", [_, maybeArg], [goDef, goMaybe]) ->
            let inner = inferMaybeInnerGoType types maybeArg
            in if inner == "any" then
                    Just (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Maybe_withDefaultAnyT")
                        [goDef, goMaybe])
               else Just (GoIr.GoCall
                    (GoIr.GoIdent ("rt.Maybe_withDefaultT[" ++ inner ++ "]"))
                    [wrapAsT inner goDef, wrapMaybe inner goMaybe])

        -- Result.withDefault def r : a -> Result e a -> a
        ("Result", "withDefault", [_, resultArg], [goDef, goResult]) ->
            case inferResultGoTypes types resultArg of
                Just (eGo, aGo) ->
                    Just (GoIr.GoCall
                        (GoIr.GoIdent ("rt.Result_withDefaultT[" ++ eGo ++ ", " ++ aGo ++ "]"))
                        [wrapAsT aGo goDef, wrapResult eGo aGo goResult])
                Nothing ->
                    Just (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Result_withDefaultAnyT")
                        [goDef, goResult])

        -- Maybe.map fn m : (a -> b) -> Maybe a -> Maybe b
        -- v0.12.x Gap 4: typed lambda routing when fn is literal.
        -- Maybe_mapT[A, B](fn func(A) B, m SkyMaybe[A]) SkyMaybe[B].
        -- Without typed body inference we use B = any.
        -- The root-cause fix in Solve.hs (mark shadowed bindings as
        -- TVar "_ambig") ensures inferMaybeInnerGoType returns "any"
        -- when the binder is ambiguous. So the safe path here is
        -- simple: try typed routing; fall back to AnyT on "any".
        ("Maybe", "map", [_, maybeArg], [goFn, goMaybe]) ->
            let inner = inferMaybeInnerGoType types maybeArg
            in if inner == "any" then
                    Just (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Maybe_mapAnyT")
                        [goFn, goMaybe])
               else case args !! 0 of
                    A.At _ (Can.Lambda pats body)
                      | all isSimpleVarPattern pats ->
                        let typedFn = curryLambdaPatTyped [inner] "any" pats (exprToGo body)
                        in Just (GoIr.GoCall
                            (GoIr.GoIdent ("rt.Maybe_mapT[" ++ inner ++ ", any]"))
                            [typedFn, wrapMaybe inner goMaybe])
                    -- Destructure pattern OR non-lambda: keep AnyT
                    -- so body's `.(SkyTuple2)` assertions match.
                    _ ->
                        Just (GoIr.GoCall
                            (GoIr.GoQualified "rt" "Maybe_mapAnyT")
                            [goFn, goMaybe])

        -- Result.map fn r : (a -> b) -> Result e a -> Result e b
        ("Result", "map", [_, resultArg], [goFn, goResult]) ->
            case inferResultGoTypes types resultArg of
                Just (eGo, aGo) ->
                    case args !! 0 of
                        A.At _ (Can.Lambda pats body)
                          | all isSimpleVarPattern pats ->
                            let typedFn = curryLambdaPatTyped [aGo] "any" pats (exprToGo body)
                            in Just (GoIr.GoCall
                                (GoIr.GoIdent ("rt.Result_mapT[" ++ eGo ++ ", " ++ aGo ++ ", any]"))
                                [typedFn, wrapResult eGo aGo goResult])
                        _ ->
                            Just (GoIr.GoCall
                                (GoIr.GoQualified "rt" "Result_mapAnyT")
                                [goFn, goResult])
                Nothing ->
                    Just (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Result_mapAnyT")
                        [goFn, goResult])

        -- Maybe.andThen fn m : (a -> Maybe b) -> Maybe a -> Maybe b
        -- v0.12.x Gap 4: typed-input lambda; return stays any.
        ("Maybe", "andThen", [_, maybeArg], [goFn, goMaybe]) ->
            let inner = inferMaybeInnerGoType types maybeArg
            in if inner == "any" then
                    Just (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Maybe_andThenAnyT")
                        [goFn, goMaybe])
               else case args !! 0 of
                    A.At _ (Can.Lambda pats body) ->
                        let typedFn = curryLambdaPatTyped [inner] "any" pats (exprToGo body)
                            anyFn = GoIr.GoCall (GoIr.GoIdent "any") [typedFn]
                        in Just (GoIr.GoCall
                            (GoIr.GoQualified "rt" "Maybe_andThenAnyT")
                            [anyFn, goMaybe])
                    _ ->
                        Just (GoIr.GoCall
                            (GoIr.GoQualified "rt" "Maybe_andThenAnyT")
                            [goFn, goMaybe])

        -- Result.andThen fn r : (a -> Result e b) -> Result e a -> Result e b
        ("Result", "andThen", [_, resultArg], [goFn, goResult]) ->
            case inferResultGoTypes types resultArg of
                Just (_, aGo) ->
                    case args !! 0 of
                        A.At _ (Can.Lambda pats body) ->
                            let typedFn = curryLambdaPatTyped [aGo] "any" pats (exprToGo body)
                                anyFn = GoIr.GoCall (GoIr.GoIdent "any") [typedFn]
                            in Just (GoIr.GoCall
                                (GoIr.GoQualified "rt" "Result_andThenAnyT")
                                [anyFn, goResult])
                        _ ->
                            Just (GoIr.GoCall
                                (GoIr.GoQualified "rt" "Result_andThenAnyT")
                                [goFn, goResult])
                Nothing ->
                    Just (GoIr.GoCall
                        (GoIr.GoQualified "rt" "Result_andThenAnyT")
                        [goFn, goResult])

        _ -> Nothing
  where
    wrapAsDict :: String -> GoIr.GoExpr -> GoIr.GoExpr
    wrapAsDict valGo e =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valGo ++ "]")) [e]
    -- Sky's Dict only has string keys; the typed kernels enforce
    -- this at the Go signature level. The codegen wraps the key
    -- arg in rt.AsString which converts any-typed inputs (rt.Field
    -- on a record) to string. Already-string inputs round-trip.
    wrapAsString :: GoIr.GoExpr -> GoIr.GoExpr
    wrapAsString e = GoIr.GoCall (GoIr.GoQualified "rt" "AsString") [e]
    -- Coerce a Go expression to a target type. For primitives we
    -- have dedicated helpers (rt.AsInt, rt.AsString, etc.); for
    -- typed Sky container shapes (SkyMaybe / SkyResult / typed slice
    -- / typed map) route through the lossless reconstructor helpers
    -- so a polymorphic source (e.g. rt.Nothing[any]()) converts into
    -- the typed target without tripping the strict rt.Coerce panic.
    -- For other non-primitive targets fall back to rt.Coerce[T].
    -- Bypassed when the target is "any" (no coercion needed).
    wrapAsT :: String -> GoIr.GoExpr -> GoIr.GoExpr
    wrapAsT goTy e = case goTy of
        "any"     -> e
        "string"  -> GoIr.GoCall (GoIr.GoQualified "rt" "AsString") [e]
        "int"     -> GoIr.GoCall (GoIr.GoQualified "rt" "AsInt") [e]
        "bool"    -> GoIr.GoCall (GoIr.GoQualified "rt" "AsBool") [e]
        "float64" -> GoIr.GoCall (GoIr.GoQualified "rt" "AsFloat") [e]
        _ -> case stripSkyMaybe goTy of
            Just inner -> GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [e]
            Nothing    -> case stripSkyResult goTy of
                Just (eGo, aGo) -> GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eGo ++ ", " ++ aGo ++ "]")) [e]
                Nothing -> case stripSlice goTy of
                    Just innerSlice -> GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ innerSlice ++ "]")) [e]
                    Nothing -> case stripStringMap goTy of
                        Just valGo -> GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valGo ++ "]")) [e]
                        Nothing -> GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ goTy ++ "]")) [e]
    -- MaybeCoerce[A](src) → SkyMaybe[A]. Used to convert any-typed
    -- runtime Maybe values to the typed shape the kernel expects.
    wrapMaybe :: String -> GoIr.GoExpr -> GoIr.GoExpr
    wrapMaybe innerGo e =
        GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ innerGo ++ "]")) [e]
    -- ResultCoerce[E, A](src) → SkyResult[E, A].
    wrapResult :: String -> String -> GoIr.GoExpr -> GoIr.GoExpr
    wrapResult eGo aGo e =
        GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eGo ++ ", " ++ aGo ++ "]")) [e]


-- | Convert a solved type to a Go type string.
-- Falls back to "any" for unresolved type variables.
-- | v0.13 Stage 1 (task #189) — variant of `solvedTypeToGo` that
-- preserves Go-side generic type parameter names (T1, T2, …) in
-- the output instead of erasing them to "any". Used by the
-- lambda-types-context lookup in `goExprGoType` so func types
-- registered inside a generic dep function render with the
-- function's actual TVar names (matching the emitted Go sig).
--
-- Distinction from the default `solvedTypeToGo`:
--   * default: TVar _ -> "any"
--   * this:    TVar n where isGenericTypeParam n -> n (kept as-is)
solvedTypeToGoPreserveTVars :: T.Type -> String
solvedTypeToGoPreserveTVars = go
  where
    go ty = case ty of
        T.TVar name
            | isGenericTypeParam name -> name
            | otherwise -> "any"
        T.TLambda from to ->
            "func(" ++ go from ++ ") " ++ go to
        T.TType _ "List" [elem_] ->
            let elemGo = go elem_
            in if elemGo == "any" then "[]any" else "[]" ++ elemGo
        T.TType _ "Maybe" [a] ->
            "rt.SkyMaybe[" ++ go a ++ "]"
        T.TType _ "Result" [e, a] ->
            "rt.SkyResult[" ++ go e ++ ", " ++ go a ++ "]"
        T.TType _ "Task" [e, a] ->
            "rt.SkyTask[" ++ go e ++ ", " ++ go a ++ "]"
        T.TType _ "Dict" [_, v] ->
            "map[string]" ++ go v
        _ -> solvedTypeToGo ty


-- | Render a Sky type to Go, substituting in-scope Sky TVars to
-- their Go TVar names. Used by parametric-alias struct emission
-- where the struct's type parameters appear as `T1`, `T2`, etc. in
-- field types. Sky-TVars NOT in the map fall back to solvedTypeToGo
-- (which renders them as `any` per the legacy widening behaviour
-- for out-of-scope TVars).
substituteTVarsToGo :: Map.Map String String -> T.Type -> String
substituteTVarsToGo tvarMap = go
  where
    go ty = case ty of
        T.TVar name
            | Just goName <- Map.lookup name tvarMap -> goName
            | otherwise -> solvedTypeToGo ty
        T.TLambda from to ->
            "func(" ++ go from ++ ") " ++ go to
        T.TType _ "List" [elem_] ->
            let elemGo = go elem_
            in if elemGo == "any" then "[]any" else "[]" ++ elemGo
        T.TType _ "Maybe" [a] ->
            "rt.SkyMaybe[" ++ go a ++ "]"
        T.TType _ "Result" [e, a] ->
            "rt.SkyResult[" ++ go e ++ ", " ++ go a ++ "]"
        T.TType _ "Task" [e, a] ->
            "rt.SkyTask[" ++ go e ++ ", " ++ go a ++ "]"
        T.TType _ "Dict" [_, v] ->
            "map[string]" ++ go v
        T.TTuple _a _b cs ->
            -- Tuples stay as their concrete runtime types; tvar
            -- substitution doesn't apply to the Tuple struct itself
            -- (rt.SkyTuple2 etc.) — only its inner fields, which the
            -- runtime carries as `any` anyway.
            case length cs of
                0 -> "rt.SkyTuple2"
                1 -> "rt.SkyTuple3"
                _ -> "rt.SkyTupleN"
        -- v0.15 Stage E — recursive-alias self-reference: a TType
        -- referencing the SAME parametric alias being substituted
        -- (e.g. `Tree a` inside `Tree a = { kids : List (Tree a) }`).
        T.TType _ name args
            | not (null args)
            , Just (Can.Alias _ (T.TRecord _ _)) <- lookupAliasDecl name ->
                let argStrs = map go args
                in name ++ "_R[" ++ intercalate_ ", " argStrs ++ "]"
        -- v0.15 Stage E — Surface 1's canonicaliser wraps parametric
        -- alias references as `TAlias name [(v, arg)] (Filled body)`.
        -- The default `solvedTypeToGo` TAlias arm renders the type-
        -- args via its own go (no tvarSubst), losing the outer
        -- substitution.  Re-emit via THIS module's substituting
        -- renderer so nested alias-typed fields stay correctly
        -- instantiated.
        T.TAlias _ name pairs _
            | not (null pairs)
            , Just (Can.Alias _ (T.TRecord _ _)) <- lookupAliasDecl name ->
                let argStrs = map (go . snd) pairs
                in name ++ "_R[" ++ intercalate_ ", " argStrs ++ "]"
        _ -> solvedTypeToGo ty
            -- For other shapes the legacy renderer is correct.


solvedTypeToGo :: T.Type -> String
solvedTypeToGo ty = case ty of
    T.TVar name
        | head name == '_' -> "any"  -- unresolved internal variable
        | otherwise -> "any"         -- unresolved user variable (TODO: Go type param)
    T.TUnit -> "struct{}"
    T.TType _ "Int" [] -> "int"
    T.TType _ "Float" [] -> "float64"
    T.TType _ "Bool" [] -> "bool"
    T.TType _ "String" [] -> "string"
    T.TType _ "Char" [] -> "rune"
    -- Container types: emit concrete Go generic instantiations.
    -- The body codegen must produce matching types (e.g. Nothing[T]()
    -- not Nothing[any]()). Monomorphisation ensures this.
    -- Typed slices: emit `[]T` for known element types. The
    -- runtime-produced `[]any` gets converted at assignment
    -- boundaries via `rt.AsListT[T]` in coerceToFieldType.
    T.TType _ "List" [elem] ->
        let elemGo = solvedTypeToGo elem
        in if elemGo == "any" then "[]any" else "[]" ++ elemGo
    T.TType _ "List" _ -> "[]any"
    T.TType _ "Cmd" _ -> "rt.SkyCmd"
    T.TType _ "Sub" _ -> "rt.SkySub"
    T.TType _ "Maybe" [a] ->
        "rt.SkyMaybe[" ++ solvedTypeToGo a ++ "]"
    T.TType _ "Maybe" _ -> "rt.SkyMaybe[any]"
    T.TType _ "Result" [e, a] ->
        "rt.SkyResult[" ++ solvedTypeToGo e ++ ", " ++ solvedTypeToGo a ++ "]"
    T.TType _ "Result" _ -> "rt.SkyResult[any, any]"
    T.TType _ "Task" [e, a] ->
        "rt.SkyTask[" ++ solvedTypeToGo e ++ ", " ++ solvedTypeToGo a ++ "]"
    T.TType _ "Task" _ -> "rt.SkyTask[any, any]"
    -- Dict values: emit `map[string]V` for known value types;
    -- boundary conversion via rt.AsMapT[V] in coerceToFieldType.
    T.TType _ "Dict" [_, v] ->
        "map[string]" ++ solvedTypeToGo v
    T.TType _ "Dict" _ -> "map[string]any"
    T.TType _ "Set" _ -> "map[any]bool"
    T.TType home name _ ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            env = getCgEnv
            allAliases = Rec._cg_recordAliases env
            -- Mirror safeReturnType's qualified-candidate fallback so a
            -- captured TType with empty home resolves to the correct
            -- module-qualified Go alias (e.g. `Model` → `State_Model_R`).
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList allAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c allAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            -- Sky-defined unions get a `type X = rt.SkyADT` alias
            -- emitted in main.go; FFI-opaque types do not. Without
            -- this gate we'd emit a dangling `Bufio_Scanner` Go type
            -- reference for a field of type Bufio.Scanner.
            isKnownUnion = Set.member base (Rec._cg_unionNames env)
                        || Set.member name (Rec._cg_unionNames env)
            runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                Just goTy -> Just goTy
                Nothing   -> lookup name runtimeTypedMap
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case runtimeTyped of
                Just goTy -> goTy
                Nothing
                    | isRuntimeOnly -> "any"
                    | isKnownUnion  -> base
                    | otherwise     -> "any"
    T.TLambda from to -> "func(" ++ solvedTypeToGo from ++ ") " ++ solvedTypeToGo to
    T.TRecord fields _ ->
        -- v0.15 Stage E — parametric aliases render with explicit
        -- generic args via `aliasGenericArgs` (structural extraction
        -- + synthetic-TVar fallback).
        let env = getCgEnv
            names = Map.keys fields
        in case Rec.lookupRecordAlias (Rec._cg_fieldIndex env) names of
            Just aliasName ->
                case aliasGenericArgs aliasName fields of
                    Just (_, argTys) ->
                        aliasName ++ "_R[" ++
                        intercalate_ ", " (map solvedTypeToGo argTys) ++
                        "]"
                    Nothing -> aliasName ++ "_R"
            Nothing -> synthAnonRecordName fields
    T.TTuple _ _ rest ->
        -- v0.13: render tuples as `rt.SkyTuple2/3/N` — CONSISTENT
        -- with `typeStrWithAliasesReg` / `safeReturnTypeWith` (which
        -- drive function signatures) AND with the actual tuple-
        -- literal emission (`GoStructLit "rt.SkyTuple2"`).  The old
        -- `rt.T2[A,B]` rendering was an inconsistency: a variable
        -- `solvedTypeToGo`-typed `[]rt.T2[int,int]` could never
        -- accept a `[]rt.SkyTuple2` value (`rt.SkyTuple2 =
        -- T2[any,any]`, a DIFFERENT Go type), so the "typed tuple"
        -- claim was never actually honoured by codegen.
        case length rest of
            0 -> "rt.SkyTuple2"
            1 -> "rt.SkyTuple3"
            _ -> "rt.SkyTupleN"
    T.TAlias home name typeArgs aliasTy ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            env = getCgEnv
            allAliases = Rec._cg_recordAliases env
            -- v0.15 Stage E — emit explicit Go type-args.
            typeArgSuffix =
                if null typeArgs
                    then ""
                    else "[" ++ intercalate_ ", "
                              [ solvedTypeToGo argTy
                              | (_, argTy) <- typeArgs ]
                          ++ "]"
            -- Try every registered cross-module alias of the form
            -- "<Mod>_<name>" so a captured TAlias with empty home (the
            -- canonicaliser leaves home unset when the alias is imported
            -- via `exposing (..)`) still resolves to the proper qualified
            -- Go alias. Without this, `Model` (imported from State) emits
            -- as `Model_R` which doesn't exist; the qualified candidate
            -- "State_Model" matches and we emit "State_Model_R".
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList allAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c allAliases ]
            isRecord = case aliasTy of
                T.Hoisted (T.TRecord _ _) -> True
                T.Filled  (T.TRecord _ _) -> True
                _ -> False
            -- Runtime-only types (Attribute, Decoder, Handler, …)
            -- have NO Go alias emitted — using `base` as a Go type
            -- would produce `undefined: Attribute`.  Fall to `any`.
            -- Also check runtimeTypedMap for types with a known
            -- concrete Go counterpart (rt.SkyDecoder etc.).
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            runtimeTyped = case lookup (modStr, name) qualifiedRuntimeTypedMap of
                Just goTy -> Just goTy
                Nothing   -> lookup name runtimeTypedMap
            unionNames = Rec._cg_unionNames env
            isKnownUnion = Set.member base unionNames || Set.member name unionNames
            -- Alias chains: `type alias FileForm = Editor.Form`
            -- stores aliasTy as Hoisted/Filled (TAlias Form …).  If
            -- the outer name (FileForm) isn't in the registry, the
            -- chain's underlying type IS resolvable — recurse on it
            -- rather than widening to `any`.  Without this, a typed
            -- field whose type is an alias-of-alias renders as `any`
            -- and breaks downstream coercions (e.g. the wire
            -- dispatcher's func(Form_R) X assertion).
            unwrappedAlias = case aliasTy of
                T.Hoisted t -> Just t
                T.Filled  t -> Just t
                _           -> Nothing
            recursedFromChain = case unwrappedAlias of
                Just t -> Just (solvedTypeToGo t)
                Nothing -> Nothing
        in case matches of
            (m:_) -> m ++ "_R" ++ typeArgSuffix
            _ -> case runtimeTyped of
                Just goTy -> goTy
                Nothing
                    | isRecord      -> base ++ "_R" ++ typeArgSuffix
                    | isRuntimeOnly -> "any"
                    | isKnownUnion  -> base
                    | Just goTy <- recursedFromChain
                    , goTy /= "any"
                                    -> goTy
                    | otherwise     -> "any"


-- | Generate a curried lambda: \a b -> body → func(a) { return func(b) { return body } }
curryLambda :: [GoIr.GoParam] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambda [] body = body
curryLambda [p] body = GoIr.GoFuncLit [p] "any" [GoIr.GoReturn body]
curryLambda (p:ps) body =
    GoIr.GoFuncLit [p] "any" [GoIr.GoReturn (curryLambda ps body)]


-- | Pattern-aware currying. Each param that is not a simple PVar is bound
-- to `_pN any` and destructured via patternBindings inside the innermost
-- lambda body. This lets `\(a, b) -> a + b` compile correctly.
curryLambdaPat :: [Can.Pattern] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambdaPat [] body = body
curryLambdaPat pats body =
    let go _   []     = [GoIr.GoReturn body]
        go idx (p:ps) =
            let (param, stmts) = oneLambdaParam idx p
                inner          = case ps of
                    [] -> stmts ++ [GoIr.GoReturn body]
                    _  -> stmts ++ [GoIr.GoReturn (wrap (idx + 1) ps)]
            in [GoIr.GoReturn (GoIr.GoFuncLit [param] "any" inner)]
        wrap idx (p:ps) =
            let (param, stmts) = oneLambdaParam idx p
                tail_ = case ps of
                    [] -> stmts ++ [GoIr.GoReturn body]
                    _  -> stmts ++ [GoIr.GoReturn (wrap (idx + 1) ps)]
            in GoIr.GoFuncLit [param] "any" tail_
        wrap _ [] = body
    in case go 0 pats of
        [GoIr.GoReturn e] -> e
        _ -> body
  where
    oneLambdaParam :: Int -> Can.Pattern -> (GoIr.GoParam, [GoIr.GoStmt])
    oneLambdaParam idx (A.At _ pat) = case pat of
        Can.PVar name -> (GoIr.GoParam (goSafeName name) "any", [])
        Can.PAnything -> (GoIr.GoParam "_" "any", [])
        Can.PUnit     -> (GoIr.GoParam "_" "any", [])
        _ ->
            let tmp = "_lp" ++ show idx
            in (GoIr.GoParam tmp "any", patternBindings tmp pat)


-- | Typed variant of curryLambdaPat: emit a Sky lambda with typed
-- Go parameters and a typed Go return type. v0.12.x Gap 4 — typed
-- lambda lowering for passing to typed kernel callbacks.
--
-- For each param, the typed Go signature is `func(_lp_N A) B`. The
-- body still expects the param as `any` (Sky lambdas treat params
-- as any internally), so we re-bind via `name := any(_lp_N)` at the
-- start of each lambda's body before the original body runs. This
-- way the body's existing reflect-based dispatch works unchanged.
--
-- The return is coerced from `any` to the expected `B` using
-- `rt.Coerce[B]` (or `rt.AsX` for primitives). Bypassed when B is
-- "any" — the body already returns any.
--
-- `paramTypes` must have one entry per pattern in `pats`. Use "any"
-- for params whose type isn't statically known.
curryLambdaPatTyped :: [String] -> String -> [Can.Pattern] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambdaPatTyped = curryLambdaPatTypedW False


-- | v0.13 typed lowerer: variant of `curryLambdaPatTyped` for when
-- the `body` GoExpr is ALREADY statically typed to `retType` (e.g.
-- it was lowered via `exprToGoExpectGo retType` so it's a
-- `GoTypedBlock retType …` or a `rt.CoerceX`-coerced leaf).  Skips
-- the innermost `wrapRet` so we don't emit a redundant
-- `rt.AsInt(rt.AsInt(…))`-style double coercion.
curryLambdaPatTypedPre :: [String] -> String -> [Can.Pattern] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambdaPatTypedPre = curryLambdaPatTypedW True


-- | Shared worker.  `bodyPreTyped` = the body GoExpr is already
-- statically `retType`-typed (skip the final `wrapRet`).
curryLambdaPatTypedW :: Bool -> [String] -> String -> [Can.Pattern] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambdaPatTypedW _ [] _ pats body = curryLambdaPat pats body
curryLambdaPatTypedW bodyPreTyped paramTypes retType pats body
    | length paramTypes /= length pats = curryLambdaPat pats body
    | otherwise =
        let -- For each lambda level we know the param's typed Go
            -- type. The OUTER lambda has retType `B` (the kernel
            -- expected return). For curried inner lambdas we
            -- conservatively emit `any` return because intermediate
            -- types aren't tracked here.
            zipped = zip paramTypes pats
            wrapRet retGoTy expr = case retGoTy of
                "any"     -> expr
                "string"  -> GoIr.GoCall (GoIr.GoQualified "rt" "AsString") [expr]
                "int"     -> GoIr.GoCall (GoIr.GoQualified "rt" "AsInt") [expr]
                "bool"    -> GoIr.GoCall (GoIr.GoQualified "rt" "AsBool") [expr]
                "float64" -> GoIr.GoCall (GoIr.GoQualified "rt" "AsFloat") [expr]
                _ -> case stripSkyMaybe retGoTy of
                    Just inner -> GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [expr]
                    Nothing -> case stripSkyResult retGoTy of
                        Just (eGo, aGo) -> GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eGo ++ ", " ++ aGo ++ "]")) [expr]
                        Nothing -> case stripSlice retGoTy of
                            Just elemGo -> GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elemGo ++ "]")) [expr]
                            Nothing -> case stripStringMap retGoTy of
                                Just valGo -> GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valGo ++ "]")) [expr]
                                Nothing -> GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ retGoTy ++ "]")) [expr]
            -- Build nested typed lambdas.  v0.13 Phase A4: the
            -- intermediate return type of each curried level is
            -- `func(<remaining-param-types>) <finalRet>`.  Pre-fix,
            -- inner curried lambdas returned `any` and the Go
            -- compiler rejected the lambda when passed to a typed
            -- HOF param like `fn func(A) func(B) B`.
            paramTypesRest [] = retType
            paramTypesRest ts =
                "func(" ++ head ts ++ ") " ++ paramTypesRest (tail ts)
            buildLambdas [] = body
            buildLambdas [(pTy, pat)] =
                let (param, rebindStmts, rebindAnyStmts) = typedLambdaParam pTy pat
                    rebindAll = rebindStmts ++ rebindAnyStmts
                    finalRetExpr =
                        if bodyPreTyped then body else wrapRet retType body
                in GoIr.GoFuncLit [param] retType
                    (rebindAll ++ [GoIr.GoReturn finalRetExpr])
            buildLambdas ((pTy, pat):rest) =
                let (param, rebindStmts, rebindAnyStmts) = typedLambdaParam pTy pat
                    rebindAll = rebindStmts ++ rebindAnyStmts
                    inner = buildLambdas rest
                    innerRetTy = paramTypesRest (map fst rest)
                in GoIr.GoFuncLit [param] innerRetTy
                    (rebindAll ++ [GoIr.GoReturn inner])
        in buildLambdas zipped
  where
    -- Each lambda param emits two things: a typed Go param + any
    -- statements needed to re-bind the param name as `any` inside
    -- the body (so the existing reflect-based dispatch works).
    typedLambdaParam :: String -> Can.Pattern -> (GoIr.GoParam, [GoIr.GoStmt], [GoIr.GoStmt])
    typedLambdaParam goTy (A.At _ pat) = case pat of
        Can.PVar name ->
            if goTy == "any"
                then (GoIr.GoParam (goSafeName name) "any", [], [])
                else
                    -- v0.13 typed lowerer: preserve the typed param's
                    -- Go static type so the body can use Go-native
                    -- operations (e.g. `a + acc` for Int operands)
                    -- without `+ not defined on any` errors.  Direct
                    -- rebind (`a := _lp_a`) keeps `a`'s static type
                    -- as `goTy`; Go widens to `any` implicitly at any
                    -- function-call boundary so existing helpers that
                    -- take `any` continue to accept it.
                    --
                    -- v0.13 D-Lambda-Lowerer follow-up: emit `_ = a`
                    -- after the rebind to force-use the local. Sky
                    -- lambdas often bind a param the body doesn't
                    -- consume (e.g. `\_ -> ...` or a curry where the
                    -- early arg names get shadowed). Without the
                    -- discard, Go errors "declared and not used: a".
                    -- This is cheap (no runtime cost) and uniform.
                    let tmpName = "_lp_" ++ goSafeName name
                        sn = goSafeName name
                    in ( GoIr.GoParam tmpName goTy
                       , [ GoIr.GoShortDecl sn (GoIr.GoIdent tmpName)
                         , GoIr.GoAssign "_" (GoIr.GoIdent sn)
                         ]
                       , [] )
        Can.PAnything ->
            (GoIr.GoParam "_" (if goTy == "" then "any" else goTy), [], [])
        Can.PUnit ->
            (GoIr.GoParam "_" (if goTy == "" then "any" else goTy), [], [])
        _ ->
            -- Complex pattern destructure (tuple, record, etc.). The
            -- Go param must use the typed Go type to satisfy the
            -- kernel's typed function signature. We then bind to a
            -- local `_lp_destr_any` (cast to any) so patternBindings
            -- can destructure via the standard reflect-based path.
            let tmp = "_lp_destr_typed"
                tmpAny = "_lp_destr"
                paramTy = if goTy == "" then "any" else goTy
                rebind = if paramTy == "any"
                    then []
                    else [GoIr.GoShortDecl tmpAny
                            (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent tmp])]
            in ( GoIr.GoParam tmp paramTy
               , rebind ++ patternBindings tmpAny pat
               , [] )


-- | Convert a pattern to a Go function parameter
patternToParam :: Can.Pattern -> GoIr.GoParam
patternToParam (A.At _ pat) = case pat of
    Can.PVar name -> GoIr.GoParam name "any"
    _ -> GoIr.GoParam "_" "any"


-- | v0.13 Stage 1 — variant of patternToParam that uses the
-- pattern's annotated Sky type to produce a typed Go param.
-- Falls back to "any" when the Sky type can't render to a safely-
-- usable Go type (TVars in non-emittable positions, etc.).
typedPatToParam :: (Can.Pattern, T.Type) -> GoIr.GoParam
typedPatToParam (A.At _ pat, skyTy) =
    let goTy = solvedTypeToGo skyTy
        useTy = if isEmittableGoType goTy && not (isGenericTypeParam goTy)
                  then goTy
                  else "any"
        name = case pat of
            Can.PVar n -> n
            _          -> "_"
    in GoIr.GoParam name useTy


-- | v0.13 Stage 1 — split a Sky TLambda chain into (param types,
-- return type), peeling N TLambda layers. Used by `Can.Def` let-
-- emission to extract typed param sigs from HM-inferred types.
-- Returns (Just-typed param + Just return) when N layers were
-- consumed; partial chains return Nothing for any unconsumed slots.
splitTLambda :: Int -> T.Type -> ([Maybe T.Type], Maybe T.Type)
splitTLambda 0 t = ([], Just t)
splitTLambda n (T.TLambda from to) =
    let (rest, ret) = splitTLambda (n - 1) to
    in (Just from : rest, ret)
splitTLambda n _ = (replicate n Nothing, Nothing)


-- | True if the pattern is a simple variable binding (PVar/PAnything/
-- PUnit) — no destructure required. Used by typed-routing helpers to
-- decide whether the typed-T or typed-slice+any-fn (TA) variant of a
-- kernel is appropriate: destructuring patterns require any-typed
-- input because patternBindings uses `.(SkyTuple2)` style assertions
-- that don't match typed generic instantiations.
isSimpleVarPattern :: Can.Pattern -> Bool
isSimpleVarPattern (A.At _ pat) = case pat of
    Can.PVar _    -> True
    Can.PAnything -> True
    Can.PUnit     -> True
    _             -> False


-- | Extract a single name from a pattern (for destructuring)
patternName :: Can.Pattern -> String
patternName (A.At _ pat) = case pat of
    Can.PVar name -> goSafeName name
    _ -> "_"


-- ═══════════════════════════════════════════════════════════
-- GO RUNTIME SOURCE (embedded)
-- ═══════════════════════════════════════════════════════════

-- | The Go runtime package source — typed with generics
runtimeGoSource :: String
runtimeGoSource = unlines
    [ "package rt"
    , ""
    , "import ("
    , "\t\"fmt\""
    , "\t\"reflect\""
    , "\t\"strconv\""
    , "\t\"strings\""
    , ")"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Result"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyResult[E any, A any] struct {"
    , "\tTag      int"
    , "\tOkValue  A"
    , "\tErrValue E"
    , "}"
    , ""
    , "func Ok[E any, A any](v A) SkyResult[E, A] {"
    , "\treturn SkyResult[E, A]{Tag: 0, OkValue: v}"
    , "}"
    , ""
    , "func Err[E any, A any](e E) SkyResult[E, A] {"
    , "\treturn SkyResult[E, A]{Tag: 1, ErrValue: e}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Maybe"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyMaybe[A any] struct {"
    , "\tTag       int"
    , "\tJustValue A"
    , "}"
    , ""
    , "func Just[A any](v A) SkyMaybe[A] {"
    , "\treturn SkyMaybe[A]{Tag: 0, JustValue: v}"
    , "}"
    , ""
    , "func Nothing[A any]() SkyMaybe[A] {"
    , "\treturn SkyMaybe[A]{Tag: 1}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Task"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyTask[E any, A any] func() SkyResult[E, A]"
    , ""
    , "func Task_succeed[E any, A any](v A) SkyTask[E, A] {"
    , "\treturn func() SkyResult[E, A] { return Ok[E, A](v) }"
    , "}"
    , ""
    , "func Task_fail[E any, A any](e E) SkyTask[E, A] {"
    , "\treturn func() SkyResult[E, A] { return Err[E, A](e) }"
    , "}"
    , ""
    , "func Task_andThen[E any, A any, B any](fn func(A) SkyTask[E, B], task SkyTask[E, A]) SkyTask[E, B] {"
    , "\treturn func() SkyResult[E, B] {"
    , "\t\tr := task()"
    , "\t\tif r.Tag == 0 {"
    , "\t\t\treturn fn(r.OkValue)()"
    , "\t\t}"
    , "\t\treturn Err[E, B](r.ErrValue)"
    , "\t}"
    , "}"
    , ""
    , "func Task_run[E any, A any](task SkyTask[E, A]) SkyResult[E, A] {"
    , "\treturn task()"
    , "}"
    , ""
    , "func RunMainTask[E any, A any](task SkyTask[E, A]) {"
    , "\tr := task()"
    , "\tif r.Tag == 1 {"
    , "\t\tfmt.Println(\"Error:\", r.ErrValue)"
    , "\t}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Composition"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func ComposeL[A any, B any, C any](f func(A) B, g func(B) C) func(A) C {"
    , "\treturn func(a A) C { return g(f(a)) }"
    , "}"
    , ""
    , "func ComposeR[A any, B any, C any](g func(B) C, f func(A) B) func(A) C {"
    , "\treturn func(a A) C { return g(f(a)) }"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Log"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Log_println(args ...any) any {"
    , "\tfmt.Println(args...)"
    , "\treturn struct{}{}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// String"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func String_fromInt(n any) any {"
    , "\treturn strconv.Itoa(AsInt(n))"
    , "}"
    , ""
    , "func String_fromFloat(f any) any {"
    , "\treturn strconv.FormatFloat(AsFloat(f), 'f', -1, 64)"
    , "}"
    , ""
    , "func String_length(s any) any {"
    , "\treturn len(fmt.Sprintf(\"%v\", s))"
    , "}"
    , ""
    , "func String_isEmpty(s any) any {"
    , "\treturn len(fmt.Sprintf(\"%v\", s)) == 0"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Basics"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Basics_identity[A any](a A) A {"
    , "\treturn a"
    , "}"
    , ""
    , "func Basics_always[A any, B any](a A, _ B) A {"
    , "\treturn a"
    , "}"
    , ""
    , "func Basics_not(b bool) bool {"
    , "\treturn !b"
    , "}"
    , ""
    , "func Basics_toString(v any) string {"
    , "\treturn fmt.Sprintf(\"%v\", v)"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Concat (temporary — will use + when types are known)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Concat(a, b any) any {"
    , "\treturn fmt.Sprintf(\"%v%v\", a, b)"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Arithmetic and comparison (any-typed, until type checker)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func AsInt(v any) int { if n, ok := v.(int); ok { return n }; return 0 }"
    , "func AsFloat(v any) float64 { if f, ok := v.(float64); ok { return f }; if n, ok := v.(int); ok { return float64(n) }; return 0 }"
    , "func AsBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }"
    , ""
    , "func Add(a, b any) any { return AsInt(a) + AsInt(b) }"
    , "func Sub(a, b any) any { return AsInt(a) - AsInt(b) }"
    , "func Mul(a, b any) any { return AsInt(a) * AsInt(b) }"
    , "func Div(a, b any) any { if AsInt(b) == 0 { return 0 }; return AsInt(a) / AsInt(b) }"
    , ""
    , "func Eq(a, b any) any { return a == b }"
    , "func Gt(a, b any) any { return AsInt(a) > AsInt(b) }"
    , "func Lt(a, b any) any { return AsInt(a) < AsInt(b) }"
    , "func Gte(a, b any) any { return AsInt(a) >= AsInt(b) }"
    , "func Lte(a, b any) any { return AsInt(a) <= AsInt(b) }"
    , ""
    , "func And(a, b any) any { return AsBool(a) && AsBool(b) }"
    , "func Or(a, b any) any { return AsBool(a) || AsBool(b) }"
    , ""
    , "func Negate(a any) any { return -AsInt(a) }"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// List operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func List_map(fn any, list any) any {"
    , "\tf := fn.(func(any) any)"
    , "\titems := list.([]any)"
    , "\tresult := make([]any, len(items))"
    , "\tfor i, item := range items { result[i] = f(item) }"
    , "\treturn result"
    , "}"
    , ""
    , "func List_filter(fn any, list any) any {"
    , "\tf := fn.(func(any) any)"
    , "\titems := list.([]any)"
    , "\tvar result []any"
    , "\tfor _, item := range items {"
    , "\t\tif AsBool(f(item)) { result = append(result, item) }"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func List_foldl(fn any, acc any, list any) any {"
    , "\tf := fn.(func(any) any)"
    , "\titems := list.([]any)"
    , "\tresult := acc"
    , "\tfor _, item := range items {"
    , "\t\tstep := f(item)"
    , "\t\tresult = step.(func(any) any)(result)"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func List_length(list any) any {"
    , "\titems := list.([]any)"
    , "\treturn len(items)"
    , "}"
    , ""
    , "func List_head(list any) any {"
    , "\titems := list.([]any)"
    , "\tif len(items) == 0 { return Nothing[any]() }"
    , "\treturn Just[any](items[0])"
    , "}"
    , ""
    , "func List_reverse(list any) any {"
    , "\titems := list.([]any)"
    , "\tresult := make([]any, len(items))"
    , "\tfor i, item := range items { result[len(items)-1-i] = item }"
    , "\treturn result"
    , "}"
    , ""
    , "func List_take(n any, list any) any {"
    , "\tcount := AsInt(n)"
    , "\titems := list.([]any)"
    , "\tif count > len(items) { count = len(items) }"
    , "\treturn items[:count]"
    , "}"
    , ""
    , "func List_drop(n any, list any) any {"
    , "\tcount := AsInt(n)"
    , "\titems := list.([]any)"
    , "\tif count > len(items) { count = len(items) }"
    , "\treturn items[count:]"
    , "}"
    , ""
    , "func List_append(a any, b any) any {"
    , "\treturn append(a.([]any), b.([]any)...)"
    , "}"
    , ""
    , "func List_range(lo any, hi any) any {"
    , "\tl, h := AsInt(lo), AsInt(hi)"
    , "\tresult := make([]any, 0, h-l+1)"
    , "\tfor i := l; i <= h; i++ { result = append(result, i) }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// More String operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func String_join(sep any, list any) any {"
    , "\ts := fmt.Sprintf(\"%v\", sep)"
    , "\titems := list.([]any)"
    , "\tparts := make([]string, len(items))"
    , "\tfor i, item := range items { parts[i] = fmt.Sprintf(\"%v\", item) }"
    , "\treturn strings.Join(parts, s)"
    , "}"
    , ""
    , "func String_split(sep any, s any) any {"
    , "\tparts := strings.Split(fmt.Sprintf(\"%v\", s), fmt.Sprintf(\"%v\", sep))"
    , "\tresult := make([]any, len(parts))"
    , "\tfor i, p := range parts { result[i] = p }"
    , "\treturn result"
    , "}"
    , ""
    , "func String_toInt(s any) any {"
    , "\tn, err := strconv.Atoi(fmt.Sprintf(\"%v\", s))"
    , "\tif err != nil { return Nothing[any]() }"
    , "\treturn Just[any](n)"
    , "}"
    , ""
    , "func String_toUpper(s any) any { return strings.ToUpper(fmt.Sprintf(\"%v\", s)) }"
    , "func String_toLower(s any) any { return strings.ToLower(fmt.Sprintf(\"%v\", s)) }"
    , "func String_trim(s any) any { return strings.TrimSpace(fmt.Sprintf(\"%v\", s)) }"
    , "func String_contains(sub any, s any) any { return strings.Contains(fmt.Sprintf(\"%v\", s), fmt.Sprintf(\"%v\", sub)) }"
    , "func String_startsWith(prefix any, s any) any { return strings.HasPrefix(fmt.Sprintf(\"%v\", s), fmt.Sprintf(\"%v\", prefix)) }"
    , "func String_reverse(s any) any { runes := []rune(fmt.Sprintf(\"%v\", s)); for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 { runes[i], runes[j] = runes[j], runes[i] }; return string(runes) }"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Record operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func RecordGet(record any, field string) any {"
    , "\tif m, ok := record.(map[string]any); ok { return m[field] }"
    , "\treturn nil"
    , "}"
    , ""
    , "func RecordUpdate(base any, updates map[string]any) any {"
    , "\toriginal := base.(map[string]any)"
    , "\tresult := make(map[string]any, len(original))"
    , "\tfor k, v := range original { result[k] = v }"
    , "\tfor k, v := range updates { result[k] = v }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Tuple types"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyTuple2 struct { V0, V1 any }"
    , "type SkyTuple3 struct { V0, V1, V2 any }"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Result operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Result_map(fn any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 0 { return Ok[any, any](fn.(func(any) any)(r.OkValue)) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Result_andThen(fn any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 0 { return fn.(func(any) any)(r.OkValue) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Result_withDefault(def any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 0 { return r.OkValue }"
    , "\treturn def"
    , "}"
    , ""
    , "func Result_mapError(fn any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 1 { return Err[any, any](fn.(func(any) any)(r.ErrValue)) }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Maybe operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Maybe_withDefault(def any, maybe any) any {"
    , "\tm := maybe.(SkyMaybe[any])"
    , "\tif m.Tag == 0 { return m.JustValue }"
    , "\treturn def"
    , "}"
    , ""
    , "func Maybe_map(fn any, maybe any) any {"
    , "\tm := maybe.(SkyMaybe[any])"
    , "\tif m.Tag == 0 { return Just[any](fn.(func(any) any)(m.JustValue)) }"
    , "\treturn maybe"
    , "}"
    , ""
    , "func Maybe_andThen(fn any, maybe any) any {"
    , "\tm := maybe.(SkyMaybe[any])"
    , "\tif m.Tag == 0 { return fn.(func(any) any)(m.JustValue) }"
    , "\treturn maybe"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Record field access (reflect-based for any-typed params)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Dict operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Dict_empty() any { return map[string]any{} }"
    , ""
    , "func Dict_insert(key any, val any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tnew := make(map[string]any, len(m)+1)"
    , "\tfor k, v := range m { new[k] = v }"
    , "\tnew[fmt.Sprintf(\"%v\", key)] = val"
    , "\treturn new"
    , "}"
    , ""
    , "func Dict_get(key any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tv, ok := m[fmt.Sprintf(\"%v\", key)]"
    , "\tif ok { return Just[any](v) }"
    , "\treturn Nothing[any]()"
    , "}"
    , ""
    , "func Dict_remove(key any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tnew := make(map[string]any, len(m))"
    , "\tk := fmt.Sprintf(\"%v\", key)"
    , "\tfor kk, v := range m { if kk != k { new[kk] = v } }"
    , "\treturn new"
    , "}"
    , ""
    , "func Dict_member(key any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\t_, ok := m[fmt.Sprintf(\"%v\", key)]"
    , "\treturn ok"
    , "}"
    , ""
    , "func Dict_keys(dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make([]any, 0, len(m))"
    , "\tfor k := range m { result = append(result, k) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_values(dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make([]any, 0, len(m))"
    , "\tfor _, v := range m { result = append(result, v) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_toList(dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make([]any, 0, len(m))"
    , "\tfor k, v := range m { result = append(result, SkyTuple2{V0: k, V1: v}) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_fromList(list any) any {"
    , "\titems := list.([]any)"
    , "\tresult := make(map[string]any, len(items))"
    , "\tfor _, item := range items {"
    , "\t\tt := item.(SkyTuple2)"
    , "\t\tresult[fmt.Sprintf(\"%v\", t.V0)] = t.V1"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_map(fn any, dict any) any {"
    , "\tf := fn.(func(any) any)"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make(map[string]any, len(m))"
    , "\tfor k, v := range m { result[k] = f(v) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_foldl(fn any, acc any, dict any) any {"
    , "\tf := fn.(func(any) any)"
    , "\tm := dict.(map[string]any)"
    , "\tresult := acc"
    , "\tfor k, v := range m {"
    , "\t\tstep := f(k)"
    , "\t\tstep2 := step.(func(any) any)(v)"
    , "\t\tresult = step2.(func(any) any)(result)"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_union(a any, b any) any {"
    , "\tma := a.(map[string]any)"
    , "\tmb := b.(map[string]any)"
    , "\tresult := make(map[string]any, len(ma)+len(mb))"
    , "\tfor k, v := range mb { result[k] = v }"
    , "\tfor k, v := range ma { result[k] = v }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Math operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Math_abs(n any) any { x := AsInt(n); if x < 0 { return -x }; return x }"
    , "func Math_min(a any, b any) any { if AsInt(a) < AsInt(b) { return a }; return b }"
    , "func Math_max(a any, b any) any { if AsInt(a) > AsInt(b) { return a }; return b }"
    , ""
    , "func Field(record any, field string) any {"
    , "\tv := reflect.ValueOf(record)"
    , "\tif v.Kind() == reflect.Ptr { v = v.Elem() }"
    , "\tif v.Kind() == reflect.Struct {"
    , "\t\tf := v.FieldByName(field)"
    , "\t\tif f.IsValid() { return f.Interface() }"
    , "\t}"
    , "\treturn nil"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Any-typed Task wrappers (until type checker provides types)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func AnyTaskSucceed(v any) any {"
    , "\treturn func() any { return Ok[any, any](v) }"
    , "}"
    , ""
    , "func AnyTaskFail(e any) any {"
    , "\treturn func() any { return Err[any, any](e) }"
    , "}"
    , ""
    , "func AnyTaskAndThen(fn any, task any) any {"
    , "\treturn func() any {"
    , "\t\tt := task.(func() any)"
    , "\t\tr := t().(SkyResult[any, any])"
    , "\t\tif r.Tag == 0 {"
    , "\t\t\tnext := fn.(func(any) any)(r.OkValue).(func() any)"
    , "\t\t\treturn next()"
    , "\t\t}"
    , "\t\treturn Err[any, any](r.ErrValue)"
    , "\t}"
    , "}"
    , ""
    , "func AnyTaskRun(task any) any {"
    , "\tt := task.(func() any)"
    , "\treturn t()"
    , "}"
    ]


-- | Capitalise a string (for Go export)
capitalise_ :: String -> String
capitalise_ [] = []
capitalise_ (c:cs) = (if c >= 'a' && c <= 'z' then toEnum (fromEnum c - 32) else c) : cs


-- | String intercalation helper
intercalate_ :: String -> [String] -> String
intercalate_ _ [] = ""
intercalate_ _ [x] = x
intercalate_ sep (x:xs) = x ++ sep ++ intercalate_ sep xs


-- | Combine exhaustiveness diagnostics into a single user-facing string.
-- Each diagnostic reports a missing-pattern set plus a short hint. We
-- emit one line per diagnostic; the caller prefixes "Non-exhaustive
-- patterns:".
renderExhaustDiags :: [Exhaust.Diag] -> String
renderExhaustDiags ds = intercalate_ "\n  " (map render1 ds)
  where
    render1 (Exhaust.Diag region _missing hint) =
        let A.Region (A.Position l c) _ = region
        in "at line " ++ show l ++ ":" ++ show c ++ " — " ++ hint


-- | Synthesise a deterministic Go struct name for an anonymous record.
-- Keyed by the full (fieldName, fieldType) shape so records with the
-- same field names but different field types are distinct Go types
-- (per P4). Format: `Anon_R_<sorted names>__<short hash of types>`.
--
-- The hash is a simple polynomial over the Show-representation of the
-- field types. It isn't cryptographic — we only need it to discriminate
-- between distinct shapes within a single compile unit.
synthAnonRecordName :: Map.Map String T.FieldType -> String
synthAnonRecordName fields =
    let sorted = Map.toAscList fields
        names  = map fst sorted
        typeStr = concatMap (\(_, T.FieldType _ ty) -> show ty) sorted
        nameTag = case names of
            [] -> "Empty"
            _  -> intercalate_ "_" (map sanitiseField names)
        nameStr = "Anon_R_" ++ nameTag ++ "__" ++ shortHash (nameTag ++ typeStr)
    in unsafePerformIO $ do
        -- v0.13 E: register the shape so `generateAnonRecordDecls`
        -- can emit a concrete Go struct decl for this name.
        -- atomicModifyIORef' so racing typed-codegen passes (which
        -- compute renderer strings concurrently for different
        -- modules) accumulate every shape; the latest one wins on
        -- collision because identical shapes hash to the same name.
        atomicModifyIORef' globalAnonRecords
            (\m -> (Map.insertWith (\_ old -> old) nameStr fields m, ()))
        return nameStr
  where
    sanitiseField = map (\c -> if c == '.' || c == '\'' || c == '"' then '_' else c)


-- | Simple polynomial hash, base-32 encoded for short readable names.
shortHash :: String -> String
shortHash s =
    let h = foldl (\acc c -> acc * 131 + fromIntegral (fromEnum c)) (17 :: Integer) s
        absH = abs h
    in take 8 (toBase32 absH)
  where
    toBase32 n
        | n <= 0    = "0"
        | otherwise = reverse (go n)
    go 0 = ""
    go n =
        let (q, r) = n `divMod` 32
            c     = "0123456789abcdefghijklmnopqrstuv" !! fromIntegral r
        in c : go q
