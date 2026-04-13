{-# LANGUAGE OverloadedStrings #-}
-- | Workspace symbol index for the Sky LSP.
--
-- Built by parsing + canonicalising + type-checking every .sky file in
-- the project (including embedded stdlib Std.* materialised under
-- <projectRoot>/sky-out/.sky-stdlib/) and indexing every binding by
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
    ) where

import qualified Data.Map.Strict as Map
import Data.Map.Strict (Map)
import qualified Data.Set as Set
import Data.Maybe (mapMaybe, fromMaybe, listToMaybe)
import Data.List (sortOn, isPrefixOf, isSuffixOf)
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


-- ─── Types ─────────────────────────────────────────────────────────────

data SymKind
    = SymFunction       -- top-level value/function
    | SymCtor           -- ADT constructor
    | SymType           -- type alias or union
    | SymLocal          -- let binding
    | SymParam          -- function parameter / lambda param / case binder
    deriving (Show, Eq)


data Sym = Sym
    { symQualName  :: !String        -- "Lib.Db.exec" or "Std.IoError.DbError"
    , symLocalName :: !String        -- "exec" or "DbError"
    , symModule    :: !String        -- "Lib.Db"
    , symFile      :: !FilePath      -- absolute path
    , symRegion    :: !A.Region      -- declaration region (1-based)
    , symKind      :: !SymKind
    , symTypeSig   :: !(Maybe String)  -- "exec : String -> List Value -> Result IoError ()"
    , symDoc       :: !(Maybe String)  -- preceding `--` comment block
    } deriving (Show)


data Import = Import
    { impModule    :: !String          -- "Std.IoError"
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
    , idxFileSrc   :: !(Map FilePath T.Text)
    , idxRoot      :: !(Maybe FilePath)
    } deriving (Show)


emptyIndex :: Index
emptyIndex = Index
    { idxByQual = Map.empty
    , idxByLocal = Map.empty
    , idxByFile = Map.empty
    , idxModules = Map.empty
    , idxImports = Map.empty
    , idxLocals = Map.empty
    , idxFileSrc = Map.empty
    , idxRoot = Nothing
    }


-- ─── Builder ───────────────────────────────────────────────────────────

-- | Build the workspace symbol index from a project root. Looks for
-- sky.toml in projectRoot to find the entry path, then runs the full
-- typecheck pipeline (which materialises stdlib + dep roots), and
-- transforms the per-module canonical+typed result into a flat lookup.
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
                    Left _   -> return emptyIndex { idxRoot = Just projectRoot }
                    Right wt -> return (fromTypecheck (Just projectRoot) wt)
    -- Merge in FFI catalogue symbols so hover/definition work for
    -- auto-generated bindings as well.
    ffiSyms <- loadFfiSymbols projectRoot
    return (mergeFfi ffiSyms baseIdx)


-- | Convert a WorkspaceTypecheck into an Index. Pure — testable.
fromTypecheck :: Maybe FilePath -> Compile.WorkspaceTypecheck -> Index
fromTypecheck root wt =
    let modList = Map.toList (Compile._wt_modules wt)
        (allTops, allLocals, allImports, allFileSrc, modPaths) =
            foldr step ([], [], [], [], []) modList
        byQual = Map.fromList [ (symQualName s, s) | s <- allTops ]
        byLocal = Map.fromListWith (++)
            [ (symLocalName s, [s]) | s <- allTops ]
        byFile = Map.fromListWith (++)
            [ (symFile s, [s]) | s <- allTops ]
    in Index
        { idxByQual   = byQual
        , idxByLocal  = byLocal
        , idxByFile   = byFile
        , idxModules  = Map.fromList modPaths
        , idxImports  = Map.fromList allImports
        , idxLocals   = Map.fromList allLocals
        , idxFileSrc  = Map.fromList allFileSrc
        , idxRoot     = root
        }
  where
    step (modName, wmod) (tops, locals, imps, srcs, mods) =
        let path = Compile._wm_path wmod
            srcMod = Compile._wm_src wmod
            types = Compile._wm_types wmod
            srcText = Compile._wm_source wmod
            tops' = symFromTopLevel modName path srcText types srcMod
            locals' = (path, collectLocalBindings srcMod)
            imps' = (path, fromImports (Src._imports srcMod))
            srcs' = (path, srcText)
            mods' = (modName, path)
        in (tops' ++ tops, locals' : locals, imps' : imps,
            srcs' : srcs, mods' : mods)


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


-- | Synthesize a ctor's type signature: "DbError : String -> IoError"
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
    valueLocals (A.At valReg v) =
        let body = Src._valueBody v
            paramBinders = concatMap (patBinders valReg) (Src._valuePatterns v)
        in paramBinders ++ exprLocals valReg body

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

-- | Look up a fully-qualified symbol like "Std.IoError.DbError".
lookupQualified :: Index -> String -> Maybe Sym
lookupQualified idx q = Map.lookup q (idxByQual idx)


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
    let ffiDir = projectRoot </> "ffi"
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
        let skyiLines = zip [1..] (T.lines skyiText)
        case Aeson.eitherDecode jbs of
            Left _ -> return []
            Right reg ->
                return [ mkFfiSym skyiPath skyiLines (ffiModule reg) fn
                       | fn <- ffiFunctions reg
                       ]

    mkFfiSym skyiPath skyiLines modName fn =
        let nm = funcName fn
            (sigLine, sigText) = findSkyiLine skyiLines nm
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

    -- Find the .skyi line whose PascalCase name matches our camelCase
    -- binding. Catalogue uses `-- [effect] FnName : Type` so we match
    -- `" " ++ capitalised n ++ " :"` against a trimmed line.
    findSkyiLine ls nm =
        let cap = capitalise nm
            needle = " " ++ cap ++ " : "
            matches = [ (i, l) | (i, l) <- ls, T.isInfixOf (T.pack needle) l ]
        in case matches of
            ((i, l):_) -> (i, l)
            []         -> (0, T.empty)

    -- Parse "-- [effect] FnName : Type Signature   -- runtime wrap: Task String"
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
    let newByQual  = foldr (\s m -> Map.insertWith (\_ old -> old) (symQualName s) s m)
                           (idxByQual idx) syms
        newByLocal = foldr (\s m -> Map.insertWith (++) (symLocalName s) [s] m)
                           (idxByLocal idx) syms
        newByFile  = foldr (\s m -> Map.insertWith (++) (symFile s) [s] m)
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
    in fmap (\b -> Sym
        { symQualName = "(local) " ++ lbName b
        , symLocalName = lbName b
        , symModule = ""
        , symFile = file
        , symRegion = lbRegion b
        , symKind = SymLocal
        , symTypeSig = Nothing
        , symDoc = Nothing
        }) best
  where
    regionContains (A.Region rs re) ln cl =
        let afterStart = (A._line rs < ln) || (A._line rs == ln && A._col rs <= cl)
            beforeEnd  = (A._line re > ln) || (A._line re == ln && A._col re >= cl)
        in afterStart && beforeEnd
    regionWidth (A.Region rs re) =
        (A._line re - A._line rs) * 1000 + (A._col re - A._col rs)
