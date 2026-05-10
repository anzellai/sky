{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE BangPatterns #-}
-- | Language Server Protocol server for Sky.
--
-- Supported LSP methods:
--   * initialize / initialized / shutdown / exit
--   * textDocument/didOpen, didChange, didSave, didClose
--   * textDocument/publishDiagnostics    (outbound)
--   * textDocument/hover                 (type of identifier at cursor)
--   * textDocument/definition            (jump to a local decl)
--   * textDocument/declaration           (alias of definition)
--   * textDocument/documentSymbol        (outline: values, unions, aliases)
--   * textDocument/completion            (prefix- and context-aware)
--   * textDocument/formatting            (run sky fmt, return TextEdits)
--   * textDocument/references            (all use-sites of a local name)
--   * textDocument/rename                (prepareRename + full WorkspaceEdit)
--   * textDocument/prepareRename         (validate rename target)
--   * textDocument/signatureHelp         (parameter info while typing a call)
--   * textDocument/codeAction            (quick-fixes: unused imports, add annot)
--   * textDocument/semanticTokens/full   (type-aware syntax highlighting)
--
-- Editors supported: VS Code, Neovim, Emacs, Zed, Helix, Sublime LSP.
--
-- Safety: a single malformed request returns a JSON-RPC error; it never
-- crashes the server. All parsing/type-checking is wrapped in Haskell's
-- exception machinery and invalid Sky produces diagnostics, not aborts.
module Sky.Lsp.Server (runLsp) where

import Control.Exception (SomeException, fromException, throwIO, try)
import Control.Monad (forever, when)
import Data.List (isPrefixOf, sortBy)
import Data.Ord (comparing)
import qualified Data.Aeson as A
import qualified Data.Aeson.Key as AK
import qualified Data.Aeson.KeyMap as KM
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BC
import qualified Data.ByteString.Lazy as BL
import qualified Data.IORef as IORef
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import Data.Maybe (fromMaybe, mapMaybe)
import qualified Data.Text as T
import qualified Data.Time.Clock as Clock

import System.IO
import System.Exit (exitSuccess, exitWith, ExitCode(..))

import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Type.Constrain.Module as Constrain
import qualified Sky.Type.Constrain.Expression as ConstrainExpr
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as Ty
import qualified Sky.Type.Exhaustiveness as Exhaust
import qualified Sky.AST.Canonical as Can
import qualified Sky.AST.Source as Src
import qualified Sky.Sky.ModuleName as ModuleName
import System.Timeout (timeout)
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Format.Format as Fmt
import qualified Sky.Lsp.Index as Idx
import qualified Sky.Lsp.Diag as Diag
import qualified System.Directory as Dir
import System.FilePath (takeDirectory, (</>))


-- ─── State ─────────────────────────────────────────────────────────────

-- | Open documents keyed by URI → (version, full text).
type Docs = Map.Map T.Text (Int, T.Text)


-- | Mutable LSP state: open docs + lazily built workspace index per
-- project root. The index is keyed by absolute project root path so
-- editors with multi-root workspaces work too.
--
-- `ssShutdown` tracks whether the client sent a `shutdown` request
-- before the `exit` notification. LSP spec: exit-after-shutdown
-- terminates with code 0; exit-without-shutdown terminates with
-- code 1 so editors can detect protocol misuse.
data ServerState = ServerState
    { ssDocs     :: !(IORef.IORef Docs)
    , ssIndex    :: !(IORef.IORef (Map.Map FilePath Idx.Index))
    , ssShutdown :: !(IORef.IORef Bool)
    , ssTimedOutFiles :: !(IORef.IORef (Set.Set FilePath))
      -- Files whose externals computation has tripped the LSP
      -- timeout. Used to ensure we only emit ONE
      -- `window/showMessage` per file per session — without this
      -- the editor would get a popup on every keystroke after the
      -- first timeout, which would be obnoxious.
    }


-- ─── Main loop ─────────────────────────────────────────────────────────

runLsp :: IO ()
runLsp = do
    hSetBuffering stdout NoBuffering
    hSetBuffering stdin NoBuffering
    hSetBinaryMode stdout True
    hSetBinaryMode stdin True
    docs     <- IORef.newIORef (Map.empty :: Docs)
    idx      <- IORef.newIORef (Map.empty :: Map.Map FilePath Idx.Index)
    shutdown <- IORef.newIORef False
    timedOut <- IORef.newIORef (Set.empty :: Set.Set FilePath)
    let st = ServerState
            { ssDocs = docs
            , ssIndex = idx
            , ssShutdown = shutdown
            , ssTimedOutFiles = timedOut
            }
    forever $ do
        r <- try (handleOne st) :: IO (Either SomeException ())
        case r of
            Left e -> case fromException e :: Maybe ExitCode of
                -- `exitSuccess`/`exitWith` throws ExitCode as an exception.
                -- Propagate so the LSP `exit` notification actually
                -- terminates the process. All other exceptions are
                -- swallowed — LSP servers must survive malformed
                -- per-request input.
                Just code -> throwIO code
                Nothing   -> return ()
            Right _ -> return ()


handleOne :: ServerState -> IO ()
handleOne st = do
    msg <- readMessage
    case A.decode (BL.fromStrict msg) of
        Nothing  -> return ()
        Just val -> dispatch st val


-- ─── Framing ───────────────────────────────────────────────────────────

readMessage :: IO BS.ByteString
readMessage = do
    n <- readHeaders
    BS.hGet stdin n


readHeaders :: IO Int
readHeaders = go 0
  where
    go !len = do
        line <- readLine
        if BS.null line
            then return len
            else
                let key  = BC.takeWhile (/= ':') line
                    val  = BS.drop 1 (BC.dropWhile (/= ':') line)
                    valS = BC.unpack (BC.dropWhile (== ' ') val)
                in if BC.map toLowerAscii key == "content-length"
                    then go (safeRead valS)
                    else go len

    toLowerAscii c
        | c >= 'A' && c <= 'Z' = toEnum (fromEnum c + 32)
        | otherwise            = c
    safeRead s = case reads (trim s) of
        [(n, _)] -> n
        _ -> 0
    trim = reverse . dropWhile (`elem` (" \r\n\t" :: String)) . reverse
                   . dropWhile (`elem` (" \r\n\t" :: String))


readLine :: IO BS.ByteString
readLine = go BS.empty
  where
    go acc = do
        c <- BS.hGet stdin 1
        if BS.null c
            then return acc
            else if c == BC.pack "\n"
                then return (stripCR acc)
                else go (acc `BS.append` c)
    stripCR bs
        | BS.null bs = bs
        | BS.last bs == 13 = BS.init bs
        | otherwise = bs


sendMessage :: A.Value -> IO ()
sendMessage v = do
    let body = A.encode v
        hdr  = "Content-Length: " ++ show (BL.length body) ++ "\r\n\r\n"
    BS.hPut stdout (BC.pack hdr)
    BL.hPut stdout body
    hFlush stdout


-- ─── Dispatch ──────────────────────────────────────────────────────────

dispatch :: ServerState -> A.Value -> IO ()
dispatch st req = do
    let docs = ssDocs st
        method = jsonStr "method" req
        reqId  = KM.lookup "id" =<< asObj req
    case method of
        "initialize"                  -> sendReply reqId initializeResult
        "initialized"                 -> return ()
        "shutdown"                    -> do
            IORef.writeIORef (ssShutdown st) True
            sendReply reqId A.Null
        "exit"                        -> do
            -- LSP spec: exit terminates the server process. Code 0
            -- iff shutdown was received first; otherwise 1.
            wasShutdown <- IORef.readIORef (ssShutdown st)
            if wasShutdown
                then exitSuccess
                else exitWith (ExitFailure 1)
        "textDocument/didOpen"        -> handleDidOpenSt st req
        "textDocument/didChange"      -> handleDidChangeSt st req
        "textDocument/didSave"        -> handleDidSaveSt st req
        "textDocument/didClose"       -> handleDidClose docs req
        "textDocument/hover"          -> handleHoverIdx st req reqId
        "textDocument/completion"     -> handleCompletionSt st req reqId
        "textDocument/definition"     -> handleDefinitionIdx st req reqId
        "textDocument/declaration"    -> handleDefinitionIdx st req reqId
        "textDocument/documentSymbol" -> handleDocumentSymbol docs req reqId
        "textDocument/formatting"     -> handleFormatting docs req reqId
        "textDocument/references"     -> handleReferencesIdx st req reqId
        "textDocument/rename"         -> handleRenameSt st req reqId
        "textDocument/prepareRename"  -> handlePrepareRename docs req reqId
        "textDocument/signatureHelp"  -> handleSignatureHelp docs req reqId
        "textDocument/codeAction"          -> handleCodeAction docs req reqId
        "textDocument/semanticTokens/full" -> handleSemanticTokens docs req reqId
        "textDocument/inlayHint"           -> handleInlayHint st req reqId
        _ -> case reqId of
            Just _  -> sendReply reqId A.Null
            Nothing -> return ()


-- ─── Workspace index (lazy) ───────────────────────────────────────────

-- | Convert a `file://` URI to an absolute filesystem path.
uriToPath :: T.Text -> FilePath
uriToPath uri =
    let s = T.unpack uri
    in case stripPrefix' "file://" s of
        Just rest -> rest
        Nothing   -> s
  where
    stripPrefix' p xs
        | take (length p) xs == p = Just (drop (length p) xs)
        | otherwise               = Nothing

pathToUri :: FilePath -> T.Text
pathToUri p = T.pack ("file://" ++ p)

-- | Walk up from a file looking for sky.toml. The directory containing
-- sky.toml is the project root for index purposes. Falls back to the
-- file's directory if nothing is found.
findProjectRoot :: FilePath -> IO FilePath
findProjectRoot startFile = go (takeDirectory startFile)
  where
    go dir = do
        let toml = dir </> "sky.toml"
        ok <- Dir.doesFileExist toml
        if ok then return dir
        else
            let parent = takeDirectory dir
            in if parent == dir then return (takeDirectory startFile) else go parent

-- | Look up the cached index for the project containing `file`,
-- building it on demand if not present.
getIndex :: ServerState -> FilePath -> IO Idx.Index
getIndex st file = do
    root <- findProjectRoot file
    cache <- IORef.readIORef (ssIndex st)
    case Map.lookup root cache of
        Just idx -> return idx
        Nothing  -> do
            idx <- Idx.buildIndex root
            -- atomicModifyIORef' (the prime variant) forces the
            -- new map value strictly so subsequent readers can never
            -- observe a half-evaluated thunk.
            IORef.atomicModifyIORef' (ssIndex st) $ \m ->
                (Map.insert root idx m, ())
            return idx

-- | Force a fresh index for `file`'s project.
refreshIndex :: ServerState -> FilePath -> IO Idx.Index
refreshIndex st file = do
    root <- findProjectRoot file
    idx <- Idx.buildIndex root
    IORef.modifyIORef (ssIndex st) (Map.insert root idx)
    return idx


-- ─── Index-aware Hover (Stage 3) ──────────────────────────────────────

handleHoverIdx :: ServerState -> A.Value -> Maybe A.Value -> IO ()
handleHoverIdx st req reqId = do
    let uri  = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
        path = uriToPath uri
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> do
            r <- try (computeHoverIdx st path text line col)
                    :: IO (Either SomeException (Maybe A.Value))
            case r of
                Right (Just h) -> sendReply reqId h
                _ -> sendReply reqId A.Null


computeHoverIdx :: ServerState -> FilePath -> T.Text -> Int -> Int -> IO (Maybe A.Value)
computeHoverIdx st file text line col =
    case Parse.parseModule text of
        Left _ -> return Nothing
        Right srcMod ->
            case identAtPositionWithText srcMod text (line + 1) (col + 1) of
                Nothing -> return Nothing
                -- Field access (`model.count`) — the ident extracted from
                -- exprIdents has a leading `.`. We resolve it by finding
                -- the parent record's solved type and reading the field.
                -- Handles record literals + accessors too.
                Just ('.':fieldName) -> do
                    fieldType <- resolveFieldType st file text srcMod line col fieldName
                    case fieldType of
                        Just sig -> return (Just (mkHover ("." ++ fieldName ++ " : " ++ sig)))
                        Nothing  -> return (Just (mkHover ("." ++ fieldName)))
                Just name -> do
                    idx <- getIndex st file
                    -- Module-name hover: if the name matches an indexed
                    -- module, show a one-line summary (symbol count +
                    -- exposed-via if there's an alias). Detected by an
                    -- exact match in idxModules.
                    case Map.lookup name (Idx.idxModules idx) of
                        Just modPath ->
                            let symsCount = length (fromMaybe [] (Map.lookup modPath (Idx.idxByFile idx)))
                                descr = "module " ++ name
                                      ++ " — " ++ show symsCount ++ " symbol(s)"
                            in return (Just (mkHover descr))
                        Nothing -> do
                            let mSym = Idx.lookupAtCursor idx file (line + 1) (col + 1) name
                            case mSym of
                                Just s | hasType s ->
                                    return (Just (mkHover (renderSym s)))
                                _ -> do
                                    -- Fallback: single-file solve for stdlib /
                                    -- prelude / inferred names not in the
                                    -- index. Bounded by a 2s timeout so a
                                    -- pathological file can't pin the LSP.
                                    solvedType <- fromMaybe Nothing <$>
                                        timeout (2 * 1000 * 1000)
                                            (solveForName srcMod name)
                                    case solvedType of
                                        Just t  ->
                                            let sig = name ++ " : " ++ Solve.showType t
                                                modLine = case mSym of
                                                    Just s | Idx.symModule s /= "" ->
                                                        "\n-- defined in " ++ Idx.symModule s
                                                    _ -> ""
                                            in return (Just (mkHover (sig ++ modLine)))
                                        Nothing ->
                                            -- Kernel-only symbols: Task.run,
                                            -- Cmd.perform, etc. These have
                                            -- no source .sky file, so they
                                            -- don't appear in the workspace
                                            -- index. Their HM types live in
                                            -- lookupKernelType — same source
                                            -- the type-checker uses. Hovering
                                            -- now shows e.g. `Task.run :
                                            -- Task e a -> Result e a`.
                                            case kernelLookupForHover name of
                                                Just sig -> return (Just (mkHover (name ++ " : " ++ sig)))
                                                Nothing ->
                                                    case kernelTypeSig name of
                                                        Just sig -> return (Just (mkHover (name ++ " : " ++ sig)))
                                                        Nothing  -> case mSym of
                                                            Just s  -> return (Just (mkHover (renderSym s)))
                                                            Nothing -> return (Just (mkHover name))


-- | Look up a qualified or bare name in the runtime kernel registry
-- (Sky.Type.Constrain.Expression.lookupKernelType). Used by hover so
-- kernel-only symbols (Task.run, Cmd.perform, JsonDec.string, etc.)
-- show their real type signatures instead of just the name.
kernelLookupForHover :: String -> Maybe String
kernelLookupForHover name =
    -- Split `Mod.fn` (and `Mod.Sub.fn` chains — Sky qualified names
    -- can have multiple segments, but kernel registry uses last-
    -- segment-as-name shape).
    let parts = splitOnDots name
    in case parts of
        [m, f] -> renderAnnotation <$> ConstrainExpr.lookupKernelType m f
        _ -> Nothing
  where
    splitOnDots s = case break (== '.') s of
        (before, '.':after) -> before : splitOnDots after
        (before, _)         -> [before]

    renderAnnotation :: Ty.Annotation -> String
    renderAnnotation (Ty.Forall _ ty) = Solve.showType ty


-- | Resolve a field name's type by finding the enclosing record
-- expression and looking up the field in its solved type. Returns
-- a rendered type string (e.g. "Int") or Nothing if the parent's
-- type isn't known or doesn't have the field.
--
-- Strategy:
--   1. Find the parent expression at the cursor that's an Access
--      or Record literal.
--   2. Look up the parent's expression type via the workspace's
--      solver-tracked types.
--   3. If the parent is a record type, read the field's type.
--
-- Falls back to scanning user record-type aliases declared in the
-- module — when `model : Model` and Model is `{count : Int}`, hover
-- on `.count` shows `Int` even when the solver hasn't typed the
-- access expression directly.
resolveFieldType :: ServerState -> FilePath -> T.Text -> Src.Module -> Int -> Int -> String -> IO (Maybe String)
resolveFieldType st path _text srcMod line col fieldName = do
    let l = line + 1
        c = col + 1
    case findRecordContextAtPos srcMod l c fieldName of
        Nothing -> return Nothing
        Just targetName ->
            -- Try same-file first (cheap, no index lookup).
            case findFieldOnAlias srcMod l c targetName fieldName of
                Just sig -> return (Just sig)
                Nothing  ->
                    -- Cross-file: walk to find target's type, look
                    -- up the alias body in workspace index.
                    case findTargetType srcMod l c targetName of
                        Nothing -> return Nothing
                        Just typeExpr ->
                            case extractTypeName typeExpr of
                                Nothing -> return Nothing
                                Just typeName -> do
                                    -- Search the workspace index for an
                                    -- alias declaration with this name.
                                    eidx <- try (getIndex st path)
                                        :: IO (Either SomeException Idx.Index)
                                    case eidx of
                                        Left _    -> return Nothing
                                        Right idx ->
                                            return (lookupAliasFieldInIndex
                                                        idx typeName fieldName)


-- | Search the workspace index for a type alias named `typeName` and
-- return the rendered type of `fieldName` from its body. Walks
-- `idxFileSrc` re-parsing each file to find the alias declaration —
-- the index doesn't currently store alias bodies directly. Cost is
-- one re-parse per project file in the worst case; the alias name
-- match short-circuits.
lookupAliasFieldInIndex :: Idx.Index -> String -> String -> Maybe String
lookupAliasFieldInIndex idx typeName fieldName =
    let files = Map.toList (Idx.idxFileSrc idx)
        candidates = concatMap go files
    in case candidates of
        (s:_) -> Just s
        []    -> Nothing
  where
    go (_, src) = case Parse.parseModule src of
        Left _ -> []
        Right m ->
            [ resolved
            | A.At _ a <- Src._aliases m
            , let A.At _ n = Src._aliasName a
            , n == typeName
            , let A.At _ body = Src._aliasType a
            , Just resolved <- [findFieldInTypeExpr fieldName body]
            ]


-- | Walk the expression tree to find the access target whose
-- expression contains the cursor at the field name. Returns the
-- target identifier name (e.g. "model" in `model.count`).
findRecordContextAtPos :: Src.Module -> Int -> Int -> String -> Maybe String
findRecordContextAtPos srcMod line col _fieldName =
    let allValues = [v | A.At _ v <- Src._values srcMod]
        candidates = concatMap walkValue allValues
        hits = [n | (reg, n) <- candidates, regionContains reg line col]
    in case hits of
        (n:_) -> Just n
        []    -> Nothing
  where
    walkValue (Src.Value _ _ body _) = walkExpr body

    walkExpr :: Src.Expr -> [(A.Region, String)]
    walkExpr (A.At _ e) = case e of
        -- Cursor on the field name → record (fieldRegion, targetName)
        -- only if target is a simple Var. (Chained access like
        -- `model.user.name` would need recursion; keep simple for v1.)
        Src.Access (A.At _ (Src.Var n)) (A.At fr _) -> [(fr, n)]
        Src.Access target _ -> walkExpr target
        Src.Call f xs        -> walkExpr f ++ concatMap walkExpr xs
        Src.Binops pairs x   -> concatMap (walkExpr . fst) pairs ++ walkExpr x
        Src.Lambda _ body    -> walkExpr body
        Src.If arms e'       -> concatMap (\(c,b) -> walkExpr c ++ walkExpr b) arms ++ walkExpr e'
        Src.Let _ body       -> walkExpr body
        Src.Case s arms      -> walkExpr s ++ concatMap (walkExpr . snd) arms
        Src.Tuple a b cs     -> walkExpr a ++ walkExpr b ++ concatMap walkExpr cs
        Src.List xs          -> concatMap walkExpr xs
        Src.Negate inner     -> walkExpr inner
        Src.Paren inner      -> walkExpr inner
        Src.Record fs        -> concatMap (walkExpr . snd) fs
        Src.Update _ fs      -> concatMap (walkExpr . snd) fs
        _ -> []


-- | Find the rendered type of `fieldName` on `target` by:
--   1. Finding `target`'s type from any of these sources:
--      - top-level value annotation `target : SomeType`
--      - enclosing function's parameter annotation: if the cursor
--        is inside `f model = ...` and `f : Model -> Int`, then
--        target=model has type Model.
--   2. Resolving SomeType via type aliases (`Model = { count : Int }`).
--   3. Looking up the field in the resolved record body.
findFieldOnAlias :: Src.Module -> Int -> Int -> String -> String -> Maybe String
findFieldOnAlias srcMod line col target fieldName =
    let mTargetType = findTargetType srcMod line col target
    in case mTargetType of
        Just typeExpr -> resolveFieldThroughAlias srcMod typeExpr fieldName
        Nothing       -> Nothing


-- | Resolve a type expression through alias chains, then look up
-- `fieldName` in the resulting record. e.g. `Model` → `{count :
-- Int}` → `Int`.
resolveFieldThroughAlias :: Src.Module -> Src.TypeAnnotation -> String -> Maybe String
resolveFieldThroughAlias srcMod typeExpr fieldName =
    case extractTypeName typeExpr of
        Just typeName ->
            let aliasBodies =
                    [ body
                    | A.At _ a <- Src._aliases srcMod
                    , let A.At _ n = Src._aliasName a
                    , n == typeName
                    , let A.At _ body = Src._aliasType a
                    ]
            in case aliasBodies of
                (body:_) -> findFieldInTypeExpr fieldName body
                []       -> findFieldInTypeExpr fieldName typeExpr
        Nothing -> findFieldInTypeExpr fieldName typeExpr


-- | Find the type expression for `target` by checking:
--   1. Top-level value annotations (target = some annotated value)
--   2. The enclosing function's parameter list — if `target` is the
--      Nth parameter of function `f : T1 -> T2 -> ... -> R`, return
--      Tn.
findTargetType :: Src.Module -> Int -> Int -> String -> Maybe Src.TypeAnnotation
findTargetType srcMod line col target =
    -- Try top-level annotation first.
    let topLevel = [ ann | A.At _ v <- Src._values srcMod
                         , let A.At _ n = Src._valueName v
                         , n == target
                         , Just (A.At _ ann) <- [Src._valueType v]
                         ]
    in case topLevel of
        (a:_) -> Just a
        []    -> findParamType srcMod line col target


-- | Look at the enclosing top-level function's annotation and
-- parameter list. If `target` is a PVar in position N of the param
-- list, return the Nth argument type from the annotation.
--
-- The Value's outer region is just the name's region (parser quirk),
-- so we check the BODY's region instead — `_valueBody`'s `A.At` is
-- the full body including any sub-expressions, which is what we
-- need to detect "cursor inside this function".
findParamType :: Src.Module -> Int -> Int -> String -> Maybe Src.TypeAnnotation
findParamType srcMod line col target =
    let candidates = [ v
                     | A.At _ v <- Src._values srcMod
                     , let A.At bodyReg _ = Src._valueBody v
                     , regionContains bodyReg line col
                     ]
    in case candidates of
        (v:_) -> case Src._valueType v of
            Just (A.At _ typeExpr) ->
                let pats = Src._valuePatterns v
                    paramIdx = findParamIndex target pats
                in case paramIdx of
                    Just i  -> nthArgType i typeExpr
                    Nothing -> Nothing
            Nothing -> Nothing
        [] -> Nothing


-- | Find the index of the parameter whose pattern binds `name`.
findParamIndex :: String -> [Src.Pattern] -> Maybe Int
findParamIndex name pats = go 0 pats
  where
    go _ [] = Nothing
    go i (p:rest)
        | patHasName p name = Just i
        | otherwise         = go (i + 1) rest

    patHasName (A.At _ p) n = case p of
        Src.PVar v          -> v == n
        Src.PAlias inner (A.At _ v) -> v == n || patHasName inner n
        _                   -> False


-- | Walk an annotated function type and return the i-th argument
-- type. e.g. `Model -> String -> Int` index 0 = Model, index 1 = String.
-- Returns Nothing if i is past the arrow chain length.
nthArgType :: Int -> Src.TypeAnnotation -> Maybe Src.TypeAnnotation
nthArgType 0 (Src.TLambda from _) = Just from
nthArgType i (Src.TLambda _ rest) = nthArgType (i - 1) rest
nthArgType _ _ = Nothing


-- | Extract the head type name from a type expression like
-- `TypeName a b` or just `TypeName`.
-- Src.TType has shape `TType module [nameSegs] args` so we read
-- the LAST segment of the second slot.
extractTypeName :: Src.TypeAnnotation -> Maybe String
extractTypeName (Src.TType _ segs _) = case reverse segs of
    (n:_) -> Just n
    []    -> Nothing
extractTypeName (Src.TTypeQual _ n _) = Just n
extractTypeName _ = Nothing


-- | Search a record-type expression for a field and render its
-- type. Returns Nothing if the type expression isn't a record or
-- the field is missing.
findFieldInTypeExpr :: String -> Src.TypeAnnotation -> Maybe String
findFieldInTypeExpr fieldName (Src.TRecord fields _) =
    case [ ft | (A.At _ fn, ft) <- fields, fn == fieldName ] of
        (ft:_) -> Just (renderTypeAnnotation ft)
        []     -> Nothing
findFieldInTypeExpr _ _ = Nothing


-- | Lossy renderer for a Src.TypeAnnotation. Used for hover only —
-- the canonical type machinery would be more precise but requires
-- the canonicalised module.
renderTypeAnnotation :: Src.TypeAnnotation -> String
renderTypeAnnotation t = case t of
    Src.TLambda a b      ->
        renderTypeAnnotationParen a ++ " -> " ++ renderTypeAnnotation b
    Src.TVar n           -> n
    Src.TType _ segs args ->
        let name = case reverse segs of (n:_) -> n; [] -> "?"
        in name ++ if null args then ""
             else " " ++ unwords (map renderTypeAnnotationAtom args)
    Src.TTypeQual _ n args ->
        n ++ if null args then ""
             else " " ++ unwords (map renderTypeAnnotationAtom args)
    Src.TUnit            -> "()"
    Src.TTuple a b cs    ->
        "( " ++ renderTypeAnnotation a ++ ", " ++ renderTypeAnnotation b
            ++ concatMap ((", " ++) . renderTypeAnnotation) cs ++ " )"
    Src.TRecord fs _     ->
        "{ " ++ commaSep (map (\(A.At _ fn, ft) -> fn ++ " : " ++ renderTypeAnnotation ft) fs)
             ++ " }"
  where
    commaSep []     = ""
    commaSep [x]    = x
    commaSep (x:xs) = x ++ ", " ++ commaSep xs

renderTypeAnnotationAtom :: Src.TypeAnnotation -> String
renderTypeAnnotationAtom inner = case inner of
    Src.TLambda{} -> "(" ++ renderTypeAnnotation inner ++ ")"
    Src.TType _ _ (_:_) -> "(" ++ renderTypeAnnotation inner ++ ")"
    _ -> renderTypeAnnotation inner

renderTypeAnnotationParen :: Src.TypeAnnotation -> String
renderTypeAnnotationParen inner = case inner of
    Src.TLambda{} -> "(" ++ renderTypeAnnotation inner ++ ")"
    _ -> renderTypeAnnotation inner


-- | Does this Sym carry a real type signature?
hasType :: Idx.Sym -> Bool
hasType s = case Idx.symTypeSig s of
    Just _  -> True
    Nothing -> False


-- | Format a Sym for hover Markdown: type signature first, then a blank
-- line, then the doc comment block (if present). We surface the source
-- module so users see where the symbol came from for cross-file/stdlib
-- references.
renderSym :: Idx.Sym -> String
renderSym s =
    let header = case Idx.symTypeSig s of
            Just sig -> Idx.symLocalName s ++ " : " ++ sig
            Nothing  -> Idx.symLocalName s
        moduleLine = case Idx.symModule s of
            "" -> ""
            m  -> "\n-- defined in " ++ m
        docPart = case Idx.symDoc s of
            Just d  -> "\n\n" ++ d
            Nothing -> ""
    in header ++ moduleLine ++ docPart


-- ─── Index-aware Definition (Stage 4) ─────────────────────────────────

handleDefinitionIdx :: ServerState -> A.Value -> Maybe A.Value -> IO ()
handleDefinitionIdx st req reqId = do
    let uri  = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
        path = uriToPath uri
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> case Parse.parseModule text of
            Left _ -> sendReply reqId A.Null
            Right srcMod -> case identAtPositionWithText srcMod text (line + 1) (col + 1) of
                Nothing -> sendReply reqId A.Null
                -- Field access (`.field`) → jump to the field's
                -- declaration in the parent record type. Resolves
                -- via the same path as field hover (find target,
                -- look up its annotation, find the alias body, find
                -- the field's declared region).
                Just ('.':fieldName) -> do
                    case findFieldDefRegion srcMod (line + 1) (col + 1) fieldName of
                        Just (_, reg) -> sendReply reqId $ A.object
                            -- Same-file lookup for now; future work
                            -- may chase imports for cross-file aliases.
                            [ "uri"   A..= uri
                            , "range" A..= regionToLspRange reg
                            ]
                        Nothing -> sendReply reqId A.Null
                Just name -> do
                    idx <- getIndex st path
                    case Idx.lookupAtCursor idx path (line + 1) (col + 1) name of
                        Just s -> sendReply reqId $ A.object
                            [ "uri"   A..= pathToUri (Idx.symFile s)
                            , "range" A..= regionToLspRange (Idx.symRegion s)
                            ]
                        Nothing -> sendReply reqId A.Null


-- | Find the source region where a field is declared in its
-- parent record type alias. Returns (filePath, region) so the LSP
-- can produce a Location.
--
-- Only handles same-file aliases for now — cross-file alias chains
-- would need to walk the workspace index too.
findFieldDefRegion :: Src.Module -> Int -> Int -> String -> Maybe (FilePath, A.Region)
findFieldDefRegion srcMod line col fieldName = do
    targetName <- findRecordContextAtPos srcMod line col fieldName
    typeExpr   <- findTargetType srcMod line col targetName
    typeName   <- extractTypeName typeExpr
    let aliases =
            [ (a, body)
            | A.At _ a <- Src._aliases srcMod
            , let A.At _ n = Src._aliasName a
            , n == typeName
            , let A.At _ body = Src._aliasType a
            ]
    case aliases of
        ((_, Src.TRecord fields _):_) ->
            case [ fr | (A.At fr fn, _) <- fields, fn == fieldName ] of
                (fr:_) -> Just ("", fr)  -- same-file; uri set by caller
                []     -> Nothing
        _ -> Nothing


-- ─── didSave with index invalidation (Stage 5) ────────────────────────

-- ─── Workspace-wide references (Stage 5) ──────────────────────────────

handleReferencesIdx :: ServerState -> A.Value -> Maybe A.Value -> IO ()
handleReferencesIdx st req reqId = do
    let uri  = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
        path = uriToPath uri
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
        Just (_, text) -> case Parse.parseModule text of
            Left _ -> sendReply reqId (A.toJSON ([] :: [A.Value]))
            Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
                Just name -> do
                    idx <- getIndex st path
                    -- Walk every parsed module in the index to find use-sites.
                    let target = simpleName name
                        modList = Map.toList (Idx.idxFileSrc idx)
                        locs = concatMap (siteLocations target) modList
                        sameFileLocs = collectReferences srcMod target
                        sameFileResults =
                            [ A.object [ "uri" A..= uri
                                       , "range" A..= regionToLspRange r ]
                            | r <- sameFileLocs ]
                    sendReply reqId (A.toJSON (sameFileResults ++ locs))
  where
    siteLocations target (filePath, src)
        | filePath == uriToPath (jsonStrAt ["params", "textDocument", "uri"] req) = []
        | otherwise = case Parse.parseModule src of
            Left _ -> []
            Right m ->
                [ A.object
                    [ "uri" A..= pathToUri filePath
                    , "range" A..= regionToLspRange r
                    ]
                | r <- collectReferences m target
                ]


handleDidSaveSt :: ServerState -> A.Value -> IO ()
handleDidSaveSt st req = do
    let uri  = jsonStrAt ["params", "textDocument", "uri"] req
        path = uriToPath uri
    -- IMPORTANT: refresh the workspace index BEFORE running diagnostics.
    -- The new file content is now on disk; the externals map needs to
    -- reflect it so any cross-file impact (a new top-level binding,
    -- a renamed export, a fixed type error in another file) is
    -- visible to the diagnostics pass that follows. Best effort —
    -- failures don't break the server.
    _ <- try (refreshIndex st path) :: IO (Either SomeException Idx.Index)
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Just (_, text) -> publishDiagnosticsSt (Just st) uri text
        Nothing -> return ()
    return ()


-- | handleDidOpenSt: same as handleDidOpen but routes diagnostics
-- through the workspace-aware pipeline so cross-module type errors
-- (issue #52) surface immediately on file open.
handleDidOpenSt :: ServerState -> A.Value -> IO ()
handleDidOpenSt st req = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        text = jsonStrAt ["params", "textDocument", "text"] req
        version = jsonIntAt ["params", "textDocument", "version"] req
    IORef.modifyIORef (ssDocs st) (Map.insert uri (version, text))
    publishDiagnosticsSt (Just st) uri text


-- | handleDidChangeSt: same as handleDidChange but workspace-aware.
-- Note we do NOT refresh the index on every keystroke — that would
-- be prohibitively expensive on large projects. The index reflects
-- the on-disk state; the open file's diagnostics use the in-memory
-- text + the on-disk-derived externals. This is the right trade-off
-- for editor latency: the user's current file is precise; cross-file
-- effects are visible after save (which DOES rebuild the index).
handleDidChangeSt :: ServerState -> A.Value -> IO ()
handleDidChangeSt st req = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        version = jsonIntAt ["params", "textDocument", "version"] req
        changes = fromMaybe [] (jsonArrAt ["params", "contentChanges"] req)
    case changes of
        (c:_) ->
            let text = jsonStrAt ["text"] c
            in do
                IORef.modifyIORef (ssDocs st) (Map.insert uri (version, text))
                publishDiagnosticsSt (Just st) uri text
        [] -> return ()


-- ─── Initialize ────────────────────────────────────────────────────────

initializeResult :: A.Value
initializeResult = A.object
    [ "capabilities" A..= A.object
        [ "textDocumentSync" A..= A.object
            [ "openClose" A..= True
            , "change"    A..= (1 :: Int)
            , "save"      A..= True
            ]
        , "hoverProvider"            A..= True
        , "definitionProvider"       A..= True
        , "declarationProvider"      A..= True
        , "documentSymbolProvider"   A..= True
        , "documentFormattingProvider" A..= True
        , "referencesProvider"       A..= True
        , "renameProvider" A..= A.object [ "prepareProvider" A..= True ]
        , "signatureHelpProvider" A..= A.object
            [ "triggerCharacters"   A..= (["(", " "] :: [T.Text])
            , "retriggerCharacters" A..= ([","]      :: [T.Text])
            ]
        , "codeActionProvider" A..= A.object
            [ "codeActionKinds" A..= (["quickfix", "source.organizeImports"] :: [T.Text])
            ]
        , "semanticTokensProvider" A..= A.object
            [ "legend" A..= A.object
                [ "tokenTypes"     A..= semanticTokenTypes
                , "tokenModifiers" A..= ([] :: [T.Text])
                ]
            , "full" A..= True
            ]
        , "completionProvider" A..= A.object
            [ "triggerCharacters" A..= (["."] :: [T.Text])
            ]
        , "inlayHintProvider" A..= A.object
            [ "resolveProvider" A..= False
            ]
        ]
    , "serverInfo" A..= A.object
        [ "name"    A..= ("sky-lsp" :: T.Text)
        , "version" A..= ("0.2.0"   :: T.Text)
        ]
    ]


-- ─── Document lifecycle ───────────────────────────────────────────────

handleDidOpen :: IORef.IORef Docs -> A.Value -> IO ()
handleDidOpen docs req = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        text = jsonStrAt ["params", "textDocument", "text"] req
        version = jsonIntAt ["params", "textDocument", "version"] req
    IORef.modifyIORef docs (Map.insert uri (version, text))
    publishDiagnostics uri text


handleDidChange :: IORef.IORef Docs -> A.Value -> IO ()
handleDidChange docs req = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        version = jsonIntAt ["params", "textDocument", "version"] req
        changes = fromMaybe [] (jsonArrAt ["params", "contentChanges"] req)
    case changes of
        (c:_) ->
            let text = jsonStrAt ["text"] c
            in do
                IORef.modifyIORef docs (Map.insert uri (version, text))
                publishDiagnostics uri text
        [] -> return ()


handleDidSave :: IORef.IORef Docs -> A.Value -> IO ()
handleDidSave docs req = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Just (_, text) -> publishDiagnostics uri text
        Nothing -> return ()


handleDidClose :: IORef.IORef Docs -> A.Value -> IO ()
handleDidClose docs req = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    IORef.modifyIORef docs (Map.delete uri)
    sendNotification "textDocument/publishDiagnostics" $ A.object
        [ "uri" A..= uri
        , "diagnostics" A..= ([] :: [A.Value])
        ]


-- ─── Diagnostics ───────────────────────────────────────────────────────

publishDiagnostics :: T.Text -> T.Text -> IO ()
publishDiagnostics uri text = publishDiagnosticsSt Nothing uri text


-- | publishDiagnosticsSt: when given a ServerState, use the workspace
-- index to populate cross-module externals so type errors involving
-- stdlib / dep modules (issue #52 — `Ui.layout codeSection` where
-- codeSection is partially-applied) actually surface as red squiggles.
-- Without externals, HM treats imported names as fresh polymorphic
-- variables and rejects nothing.
--
-- The Nothing case (legacy `publishDiagnostics`) falls back to the
-- old in-isolation behaviour for callers that pre-date ServerState
-- threading. Tests use this; production paths now go through the
-- ServerState variant.
publishDiagnosticsSt :: Maybe ServerState -> T.Text -> T.Text -> IO ()
publishDiagnosticsSt mst uri text = do
    diags <- case mst of
        Just st -> computeDiagnosticsSt st (uriToPath uri) text
        Nothing -> computeDiagnostics text
    sendNotification "textDocument/publishDiagnostics" $ A.object
        [ "uri"         A..= uri
        , "diagnostics" A..= diags
        ]


computeDiagnostics :: T.Text -> IO [A.Value]
computeDiagnostics src = do
    r <- try (runPipeline src) :: IO (Either SomeException [A.Value])
    case r of
        Left _   -> return []
        Right ds -> return ds


-- | Like computeDiagnostics but uses the workspace index to populate
-- cross-module externals before solving. This is the path that closes
-- issue #52: type errors involving imported / stdlib functions surface
-- as proper diagnostics instead of being silently accepted.
computeDiagnosticsSt :: ServerState -> FilePath -> T.Text -> IO [A.Value]
computeDiagnosticsSt st path src = do
    r <- try (runPipelineSt st path src) :: IO (Either SomeException [A.Value])
    case r of
        Left _   -> return []
        Right ds -> return ds


-- | Run the compile pipeline on a string and translate every failure into
-- an LSP diagnostic with the best source position we can extract.
--
-- Parse errors: position comes from `ModuleError`'s (Row, Col).
-- Canonicalise + solver errors: the downstream phases emit messages with
-- a leading `LINE:COL: ` prefix when they know the location; stripMsgPos
-- extracts it and we fall back to (0,0) otherwise.
--
-- Exhaustiveness: after a successful solve, we run the exhaustiveness
-- pass so users see "case does not cover: Blue" in their editor —
-- same signal `sky build` emits, no more asymmetry between the two.
-- Each `Exhaust.Diag` carries a real `A.Region`, so we can produce a
-- precise range without parsing a LINE:COL prefix.
runPipeline :: T.Text -> IO [A.Value]
runPipeline src = case Parse.parseModule src of
    Left err ->
        return [mkDiagnosticAtError err ("Parse error: " ++ showParseError err)]
    Right srcMod ->
        case Canonicalise.canonicalise srcMod of
            Left err ->
                return [diagnosticFromMessage ("Canonicalise: " ++ err)]
            Right canMod -> do
                cs <- Constrain.constrainModule canMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveError err ->
                        return [diagnosticFromMessage ("Type error: " ++ err)]
                    Solve.SolveOk _ ->
                        return (map exhaustDiagnostic (Exhaust.checkModule canMod))


-- | runPipelineSt: cross-module-aware diagnostics. Builds externals
-- from the workspace index so the open file is type-checked against
-- the actual signatures of imported stdlib / dep functions.
--
-- This is what closes issue #52 in the editor: typing
--   view : Model -> any
--   view model = Ui.layout [] codeSection   -- partial app
-- now produces the same red-squiggle "Type mismatch: (Model) ->
-- Element a vs Element a" that `sky check` produces, instead of
-- silently passing.
--
-- Falls back to runPipeline (no externals) when the workspace index
-- can't be built (no sky.toml, no project root, transient build error).
-- That keeps the editor responsive on standalone files outside a
-- project at the cost of cross-module checks for those — which is
-- the right trade-off (the user can't have cross-module errors in a
-- module they aren't importing anything in).
runPipelineSt :: ServerState -> FilePath -> T.Text -> IO [A.Value]
runPipelineSt st path src = case Parse.parseModule src of
    Left err ->
        return [mkDiagnosticAtError err ("Parse error: " ++ showParseError err)]
    Right srcMod ->
        case Canonicalise.canonicalise srcMod of
            Left err ->
                return [diagnosticFromMessage ("Canonicalise: " ++ err)]
            Right canMod -> do
                externals <- getExternalsForFile st path srcMod canMod
                cs <- Constrain.constrainModuleWithExternals externals canMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveError err ->
                        -- No suppression: with closed-record kernel
                        -- sigs for Live.app / Tui.app / Cli.program
                        -- AND field-name-aware error rendering, the
                        -- LSP's diagnostic now matches `sky check`'s
                        -- exactly. Limitation #19 closed.
                        return [diagnosticFromMessage ("Type error: " ++ err)]
                    Solve.SolveOk _ ->
                        return (map exhaustDiagnostic (Exhaust.checkModule canMod))


-- | Build the cross-module externals map for a given file's canonical
-- module by consulting the workspace index. The index already holds
-- the solved types of every other module in the project (including
-- stdlib, materialised under .skycache/stdlib). We pull each imported
-- module's solved types and run them through the same externals
-- builder the production `compile` pipeline uses.
--
-- Empty externals on no-index path (e.g. file outside a project).
getExternalsForFile
    :: ServerState
    -> FilePath
    -> Src.Module
    -> Can.Module
    -> IO (Map.Map (String, String) Ty.Annotation)
getExternalsForFile st path srcMod canMod = do
    eidx <- try (getIndex st path) :: IO (Either SomeException Idx.Index)
    case eidx of
        Left _    -> return Map.empty
        Right idx -> buildScopedExternals st path idx srcMod canMod


-- | Compute the scoped externals for an open file: walk the file's
-- imports, fetch ONLY the imported modules' types from the index,
-- run them through `Idx.externalsForFile` to generate the externals
-- map. Bounded by a 3-second wall-clock timeout — if the
-- computation takes longer (a pathological dep generates an
-- unbounded type), we fall back to empty externals so the editor
-- stays responsive. Cross-module diagnostics on that file are
-- temporarily lost but hover / completion / goto-def still work.
--
-- Forces the result strictly (`!ext, !sz`) so the cost is paid here,
-- inside the timeout, instead of later when the constraint solver
-- traverses the lazy thunk.
buildScopedExternals
    :: ServerState
    -> FilePath
    -> Idx.Index
    -> Src.Module
    -> Can.Module
    -> IO (Map.Map (String, String) Ty.Annotation)
buildScopedExternals st path idx srcMod canMod = do
    -- Build externals scoped to (imports ∪ references). Without
    -- this scope the LSP attempts to feed the entire workspace's
    -- externals (~1800 entries on skyshop) into every per-file
    -- solve, hanging on pathological FFI types (Stripe SDK,
    -- Firebase). The 3s timeout is a safety net for projects that
    -- still trigger a runaway generaliseToAnnotation despite the
    -- scoping; the editor stays responsive at the cost of cross-
    -- module diagnostics on that one file.
    --
    -- We union explicit `import M` statements (from the source AST)
    -- with `Can.VarTopLevel`-derived references (from the
    -- canonicalised expressions). Importing without referencing
    -- would otherwise give the LSP an empty scope until the user
    -- actually wrote a usage.
    let compute = do
            let !imports = collectImportNames srcMod canMod
                !ext = Idx.externalsForFile imports idx
                _ = Map.size ext   -- force spine
            return ext
    r <- timeout (3 * 1000 * 1000) compute
    case r of
        Just ext -> return ext
        Nothing -> do
            -- Surface the timeout to the editor as a one-shot
            -- info notification. Without this the user just sees
            -- red squiggles silently disappear from one file
            -- while every other file in the workspace works fine
            -- — confusing. Dedup per file: the timeout is sticky
            -- for the LSP-server lifetime so a single keystroke
            -- doesn't generate a cascade of popups.
            already <- IORef.atomicModifyIORef' (ssTimedOutFiles st) $
                \s -> if Set.member path s
                    then (s, True)
                    else (Set.insert path s, False)
            when (not already) $
                sendNotification "window/showMessage" $ A.object
                    [ "type"    A..= (3 :: Int) -- 3 = Info
                    , "message" A..=
                        ("Sky LSP: cross-module type checks degraded "
                         ++ "for this file — externals computation "
                         ++ "exceeded the 3 s budget. Hover, "
                         ++ "completion and goto-def still work; "
                         ++ "`sky check` is unaffected.")
                    ]
            Diag.logRaw_uri (T.pack ("file://" ++ path))
                "LSP: scoped-externals computation exceeded 3s budget"
            return Map.empty


-- | Compute the externals scope for an open file: union of
-- (a) every `import M` statement in the source AST and
-- (b) every cross-module reference observed in the canonicalised
-- expressions (`Can.VarTopLevel`, `Can.VarCtor`, etc.).
--
-- Why both? An `import` alone tells us the user opted in to that
-- module's surface — even if they haven't written a reference yet,
-- the LSP should treat its symbols as candidates so that the moment
-- they DO write a reference, type-check fires correctly.
-- References (without imports) catch implicit re-exports / Prelude
-- and any other path we'd otherwise miss.
collectImportNames :: Src.Module -> Can.Module -> [String]
collectImportNames srcMod canMod =
    let homeName = ModuleName.toString (Can._name canMod)
        srcImports = [ joinDots segs
                     | imp <- Src._imports srcMod
                     , let A.At _ segs = Src._importName imp
                     ]
        usage = collectDeclModNames (Can._decls canMod)
        union = Set.fromList (srcImports ++ usage)
    in Set.toList (Set.delete homeName union)
  where
    joinDots = foldr (\a b -> if null b then a else a ++ "." ++ b) ""

collectDeclModNames :: Can.Decls -> [String]
collectDeclModNames decls = case decls of
    Can.Declare def rest -> defModNames def ++ collectDeclModNames rest
    Can.DeclareRec d ds rest ->
        defModNames d ++ concatMap defModNames ds ++ collectDeclModNames rest
    Can.SaveTheEnvironment -> []

defModNames :: Can.Def -> [String]
defModNames d = case d of
    Can.Def _ _ body -> exprModNames body
    Can.TypedDef _ _ _ body _ -> exprModNames body
    Can.DestructDef _ body -> exprModNames body

exprModNames :: Can.Expr -> [String]
exprModNames (A.At _ e) = case e of
    Can.VarTopLevel home _ -> [ModuleName.toString home]
    Can.VarCtor _ home _ _ _ -> [ModuleName.toString home]
    Can.Call f xs -> exprModNames f ++ concatMap exprModNames xs
    Can.Lambda _ body -> exprModNames body
    Can.Let def body -> defModNames def ++ exprModNames body
    Can.LetDestruct _ rhs body -> exprModNames rhs ++ exprModNames body
    Can.LetRec defs body ->
        concatMap defModNames defs ++ exprModNames body
    Can.If arms el ->
        concatMap (\(c, b) -> exprModNames c ++ exprModNames b) arms
        ++ exprModNames el
    Can.Case sub arms ->
        exprModNames sub ++ concatMap caseArmModNames arms
    Can.Access r _ -> exprModNames r
    Can.Update _ _ fields ->
        concatMap (\(_, Can.FieldUpdate _ ex) -> exprModNames ex)
                  (Map.toList fields)
    Can.Record fields ->
        concatMap (exprModNames . snd) (Map.toList fields)
    Can.Tuple a b cs ->
        exprModNames a ++ exprModNames b ++ concatMap exprModNames cs
    Can.List xs -> concatMap exprModNames xs
    Can.Negate inner -> exprModNames inner
    Can.Binop _ home _ _ a b ->
        ModuleName.toString home : exprModNames a ++ exprModNames b
    _ -> []
  where
    caseArmModNames (Can.CaseBranch _ body) = exprModNames body


-- | True when the solver error matches the shape the no-externals
-- LSP path produces for cross-module record kernel sigs (notably
-- `Live.app`). Identified by the truncated `{ ... }` rendering on
-- both sides — the renderer truncates records past a certain
-- complexity, which only happens for kernel-shape records here.
--
-- This is a HEURISTIC interim fix until the LSP loads dep
-- externals properly. Real-world impact: the LSP no longer false-
-- positives on TEA apps using `Live.app`. Trade-off: a genuine
-- record-vs-record mismatch involving large records would also be
-- silently dropped — but those are rare in user code (records
-- that big almost never appear in user-defined types).
isLikelyExternalsFalsePositive :: String -> Bool
isLikelyExternalsFalsePositive err =
    let hasTruncatedRecord = "{ ... }" `isInfixOfStr` err
        isTypeMismatch     = "Type mismatch:" `isInfixOfStr` err
    in hasTruncatedRecord && isTypeMismatch
  where
    isInfixOfStr needle hay = any (needle `isPrefixOfStr`) (tails hay)
    isPrefixOfStr [] _ = True
    isPrefixOfStr _ [] = False
    isPrefixOfStr (n:ns) (h:hs) = n == h && isPrefixOfStr ns hs
    tails [] = [[]]
    tails xs@(_:rest) = xs : tails rest


-- | Convert an exhaustiveness diagnostic into an LSP diagnostic. The
-- region is the case-expression region carried by the `Diag`.
exhaustDiagnostic :: Exhaust.Diag -> A.Value
exhaustDiagnostic (Exhaust.Diag region missing hint) =
    let A.Region (A.Position r1 c1) (A.Position r2 c2) = region
        line1 = max 0 (r1 - 1)
        col1  = max 0 (c1 - 1)
        line2 = max 0 (r2 - 1)
        col2  = max 0 (c2 - 1)
        msg = case missing of
            [] -> hint
            _  -> "Non-exhaustive patterns: " ++ hint
                ++ " (missing: " ++ listWithCommas missing ++ ")"
    in mkDiagnostic line1 col1 line2 col2 msg 1
  where
    listWithCommas [] = ""
    listWithCommas [x] = x
    listWithCommas (x:xs) = x ++ ", " ++ listWithCommas xs


-- | Extract `LINE:COL:` prefix if present; otherwise return no position.
stripMsgPos :: String -> (Maybe (Int, Int), String)
stripMsgPos s =
    case reads s :: [(Int, String)] of
        [(r, ':':rest1)] -> case reads rest1 :: [(Int, String)] of
            [(c, ':':' ':rest2)] -> (Just (r, c), rest2)
            [(c, ':':rest2)]     -> (Just (r, c), dropWhile (== ' ') rest2)
            _                    -> (Nothing, s)
        _ -> (Nothing, s)


-- | Turn a plain-text error (possibly prefixed with `LINE:COL:`) into a
-- diagnostic that points at the right place when the prefix is present.
diagnosticFromMessage :: String -> A.Value
diagnosticFromMessage fullMsg =
    -- The prefix may sit after a leading "Canonicalise: " or "Type error: "
    -- label we added in runPipeline. Strip the label first, then the pos.
    let (label, rest) = span (/= ':') fullMsg
        msg = case rest of
            ':':' ':after -> case stripMsgPos after of
                (Just (r, c), clean) -> Just (r, c, label ++ ": " ++ clean)
                _ -> Nothing
            _ -> Nothing
        (pos, displayMsg) = case msg of
            Just (r, c, m) -> (Just (r, c), m)
            Nothing        -> (Nothing, fullMsg)
    in case pos of
        Just (r, c) ->
            let line = max 0 (r - 1)
                col  = max 0 (c - 1)
            in mkDiagnostic line col line (col + 1) displayMsg 1
        Nothing -> mkDiagnostic 0 0 0 80 displayMsg 1


-- | Parse errors carry (Row, Col); LSP positions are 0-based.
mkDiagnosticAtError :: Parse.ModuleError -> String -> A.Value
mkDiagnosticAtError err msg =
    let (r, c) = errorPos err
        line = max 0 (r - 1)
        col  = max 0 (c - 1)
    in mkDiagnostic line col line (col + 1) msg 1


errorPos :: Parse.ModuleError -> (Int, Int)
errorPos e = case e of
    Parse.ModuleExpected     r c -> (r, c)
    Parse.ModuleNameExpected r c -> (r, c)
    Parse.ImportExpected     r c -> (r, c)
    Parse.DeclarationError   r c -> (r, c)


showParseError :: Parse.ModuleError -> String
showParseError e = case e of
    Parse.ModuleExpected     _ _ -> "expected `module` declaration"
    Parse.ModuleNameExpected _ _ -> "expected module name"
    Parse.ImportExpected     _ _ -> "expected `import` declaration"
    Parse.DeclarationError   _ _ -> "expected top-level declaration"


mkDiagnostic :: Int -> Int -> Int -> Int -> String -> Int -> A.Value
mkDiagnostic r1 c1 r2 c2 msg severity = A.object
    [ "range" A..= lspRange r1 c1 r2 c2
    , "severity" A..= severity       -- 1=Error 2=Warn 3=Info 4=Hint
    , "source"   A..= ("sky" :: T.Text)
    , "message"  A..= msg
    ]


lspRange :: Int -> Int -> Int -> Int -> A.Value
lspRange r1 c1 r2 c2 = A.object
    [ "start" A..= A.object ["line" A..= r1, "character" A..= c1]
    , "end"   A..= A.object ["line" A..= r2, "character" A..= c2]
    ]


regionToLspRange :: A.Region -> A.Value
regionToLspRange (A.Region s e) =
    lspRange (A._line s - 1) (A._col s - 1) (A._line e - 1) (A._col e - 1)


-- ─── Hover ─────────────────────────────────────────────────────────────

handleHover :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleHover docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> do
            r <- try (computeHover text line col) :: IO (Either SomeException (Maybe A.Value))
            case r of
                Right (Just h) -> sendReply reqId h
                _              -> sendReply reqId A.Null


-- | Run the solve pipeline on a parsed module and return the inferred
-- type for a specific name. Checks both the top-level solver env AND
-- the locals accumulator (which captures every CLet-bound name, i.e.
-- all declarations including top-level — the solver's env-restore
-- removes top-level names from the final env, but _locals retains them).
solveForName :: Src.Module -> String -> IO (Maybe Ty.Type)
solveForName srcMod name =
    case Canonicalise.canonicalise srcMod of
        Left _       -> return Nothing
        Right canMod -> do
            cs <- Constrain.constrainModule canMod
            (r, localTys) <- Solve.solveWithLocals cs
            case r of
                Solve.SolveOk types ->
                    case Map.lookup name types of
                        Just t  -> return (Just t)
                        Nothing -> case Map.lookup name localTys of
                            Just (t:_) -> return (Just t)
                            _          -> return Nothing
                _ -> return Nothing


-- | Hard-coded type signatures for stdlib kernel functions. These are
-- the functions available without any import (Prelude) or via the
-- standard `Sky.Core.*` / `Std.*` imports. The index may miss them
-- because kernel modules don't have .sky source files to index.
kernelTypeSig :: String -> Maybe String
kernelTypeSig name = Map.lookup name kernelSigs
  where
    kernelSigs = Map.fromList
        -- Prelude / Basics
        [ ("println",      "a -> Task Error ()")
        , ("identity",     "a -> a")
        , ("always",       "a -> b -> a")
        , ("not",          "Bool -> Bool")
        , ("toString",     "a -> String")
        , ("modBy",        "Int -> Int -> Int")
        , ("clamp",        "comparable -> comparable -> comparable -> comparable")
        , ("fst",          "( a, b ) -> a")
        , ("snd",          "( a, b ) -> b")
        , ("errorToString","Error -> String")
        -- Operators — hover on `|>`, `++`, etc. now shows the
        -- operator's signature instead of returning empty.
        , ("|>",           "a -> (a -> b) -> b")          -- forward pipe
        , ("<|",           "(a -> b) -> a -> b")          -- backward pipe
        , (">>",           "(a -> b) -> (b -> c) -> a -> c")  -- function composition
        , ("<<",           "(b -> c) -> (a -> b) -> a -> c")
        , ("++",           "appendable -> appendable -> appendable")
        , ("::",           "a -> List a -> List a")
        , ("==",           "a -> a -> Bool")
        , ("/=",           "a -> a -> Bool")
        , ("<",            "comparable -> comparable -> Bool")
        , (">",            "comparable -> comparable -> Bool")
        , ("<=",           "comparable -> comparable -> Bool")
        , (">=",           "comparable -> comparable -> Bool")
        , ("&&",           "Bool -> Bool -> Bool")
        , ("||",           "Bool -> Bool -> Bool")
        , ("+",            "number -> number -> number")
        , ("-",            "number -> number -> number")
        , ("*",            "number -> number -> number")
        , ("/",            "Float -> Float -> Float")
        , ("//",           "Int -> Int -> Int")
        , ("^",            "number -> number -> number")
        ]


-- | Find the identifier at (line, col) (LSP 0-based) and return its type
-- formatted as a markdown code block.
computeHover :: T.Text -> Int -> Int -> IO (Maybe A.Value)
computeHover text line col = case Parse.parseModule text of
    Left _       -> return Nothing
    Right srcMod ->
        case identAtPosition srcMod (line + 1) (col + 1) of
            Nothing   -> return Nothing
            Just name ->
                case Canonicalise.canonicalise srcMod of
                    Left _       -> return Nothing
                    Right canMod -> do
                        cs <- Constrain.constrainModule canMod
                        r  <- Solve.solve cs
                        case r of
                            Solve.SolveOk types -> case Map.lookup name types of
                                Just t  -> return (Just (mkHover (name ++ " : " ++ Solve.showType t)))
                                Nothing -> return (Just (mkHover name))
                            _ -> return (Just (mkHover name))


mkHover :: String -> A.Value
mkHover body = A.object
    [ "contents" A..= A.object
        [ "kind"  A..= ("markdown" :: T.Text)
        , "value" A..= ("```sky\n" ++ body ++ "\n```")
        ]
    ]


-- | Walk the source tree and return the name at a (1-based) position, if
-- any. When several regions contain the position, prefer the smallest
-- (innermost) one so we pick a `Var` inside an enclosing `Call` rather
-- than the whole expression.
identAtPosition :: Src.Module -> Int -> Int -> Maybe String
identAtPosition srcMod line col =
    let matches = [ (reg, n) | (reg, n) <- collectIdents srcMod
                             , regionContains reg line col ]
    in case sortBy (comparing (regionWidth . fst)) matches of
        ((_, n):_) -> Just n
        []         -> Nothing


-- | Like identAtPosition but falls back to a line-text scan when the
-- AST has no ident at the cursor. Used by hover + go-to-def so users
-- can hover on type names inside annotations (`view : Model -> ...`),
-- which the AST doesn't track per-name regions for.
identAtPositionWithText :: Src.Module -> T.Text -> Int -> Int -> Maybe String
identAtPositionWithText srcMod text line col =
    case identAtPosition srcMod line col of
        Just n  -> Just n
        Nothing -> identInLineText text (line - 1) (col - 1)


-- | Extract the identifier (alphanumeric + dot) word at a 0-based
-- (line, col) cursor position from raw text. Used as the fallback
-- for identAtPositionWithText when the AST walker misses it.
identInLineText :: T.Text -> Int -> Int -> Maybe String
identInLineText text line col =
    let ls = T.lines text
    in if line < 0 || line >= length ls
        then Nothing
        else
            let cur = ls !! line
                upto = T.take col cur
                rest = T.drop col cur
                lhs = T.reverse (T.takeWhile isIdentChar (T.reverse upto))
                rhs = T.takeWhile isIdentChar rest
                word = T.unpack (lhs `T.append` rhs)
            in if null word then Nothing else Just word
  where
    isIdentChar c = c == '.' || c == '_'
                  || (c >= 'a' && c <= 'z')
                  || (c >= 'A' && c <= 'Z')
                  || (c >= '0' && c <= '9')


regionWidth :: A.Region -> Int
regionWidth (A.Region s e) =
    let lineSpan = A._line e - A._line s
        colSpan  = A._col  e - A._col  s
    -- lines count 1000× more than columns so a multi-line region always
    -- loses to a single-line one.
    in lineSpan * 1000 + colSpan


-- | Every (region, name) pair in the module. Ordered as encountered —
-- callers use the smallest containing region.
collectIdents :: Src.Module -> [(A.Region, String)]
collectIdents srcMod =
       [ (A.toRegion ln, n)
       | A.At _ v <- Src._values srcMod
       , let ln = Src._valueName v, let A.At _ n = ln
       ]
    ++ concatMap valueIdents (Src._values srcMod)
    -- Module path in `import X` lines — emit the full path as a
    -- hoverable ident so cursor anywhere in `import Std.Ui as Ui`'s
    -- module name resolves to the workspace module info. The path's
    -- region covers all the dot-separated segments.
    ++ [ (A.toRegion ln, joinDotsList segs)
       | imp <- Src._imports srcMod
       , let ln = Src._importName imp
       , let A.At _ segs = ln
       ]
  where
    joinDotsList :: [String] -> String
    joinDotsList = foldr (\a b -> if null b then a else a ++ "." ++ b) ""

    valueIdents (A.At _ v) =
        let pats = Src._valuePatterns v
            body = Src._valueBody v
        in concatMap patIdents pats ++ exprIdents body

    -- Binding sites from a pattern (so the user can hover / jump on them).
    patIdents :: Src.Pattern -> [(A.Region, String)]
    patIdents (A.At reg p) = case p of
        Src.PVar n           -> [(reg, n)]
        Src.PAlias inner (A.At nr n) -> (nr, n) : patIdents inner
        Src.PCtor _ _ xs     -> concatMap patIdents xs
        Src.PCtorQual _ _ xs -> concatMap patIdents xs
        Src.PCons h t        -> patIdents h ++ patIdents t
        Src.PList xs         -> concatMap patIdents xs
        Src.PTuple a b cs    -> patIdents a ++ patIdents b ++ concatMap patIdents cs
        Src.PRecord fields   -> [ (fr, n) | A.At fr n <- fields ]
        _                    -> []

    exprIdents :: Src.Expr -> [(A.Region, String)]
    exprIdents (A.At reg e) = case e of
        Src.Var n           -> [(reg, n)]
        Src.VarQual q n     -> [(reg, q ++ "." ++ n)]
        Src.Call f xs       -> exprIdents f ++ concatMap exprIdents xs
        -- Binops: walk both expressions AND emit each operator as a
        -- hoverable ident (so the user can hover `|>` and see its
        -- type signature). The operator's region is the located
        -- string's region.
        Src.Binops pairs x  ->
            concatMap (\(e', A.At opR op) -> exprIdents e' ++ [(opR, op)]) pairs
                ++ exprIdents x
        Src.Lambda ps body  -> concatMap patIdents ps ++ exprIdents body
        Src.If arms e'      -> concatMap (\(c,b) -> exprIdents c ++ exprIdents b) arms ++ exprIdents e'
        Src.Let defs body   -> concatMap defIdents defs ++ exprIdents body
        Src.Case s arms     -> exprIdents s ++ concatMap (\(p, b) -> patIdents p ++ exprIdents b) arms
        -- Access: walk the target AND emit the field name as a
        -- hoverable ident. The ident name is `.field` (with leading
        -- dot) so the hover lookup can distinguish field access
        -- from a regular variable named "field".
        Src.Access t (A.At fr fn) -> exprIdents t ++ [(fr, "." ++ fn)]
        -- Standalone accessor `.field` (point-free record getter):
        -- expose the field name as a hoverable ident too.
        Src.Accessor fn     -> [(reg, "." ++ fn)]
        -- Record update `{ base | a = ..., b = ... }`: walk the
        -- record-base name AND each field-update value. The field
        -- names ARE syntactic locations the user would hover; emit
        -- them as `.field` idents.
        Src.Update (A.At br bn) fs ->
            (br, bn) : concatMap (\(A.At fr fn, v) -> (fr, "." ++ fn) : exprIdents v) fs
        -- Record literal: emit each field name as `.field` ident.
        Src.Record fs ->
            concatMap (\(A.At fr fn, v) -> (fr, "." ++ fn) : exprIdents v) fs
        Src.Tuple a b cs    -> exprIdents a ++ exprIdents b ++ concatMap exprIdents cs
        Src.List xs         -> concatMap exprIdents xs
        Src.Negate inner    -> exprIdents inner
        Src.Paren inner     -> exprIdents inner
        _                   -> []

    defIdents (A.At _ d) = case d of
        Src.Define (A.At nr n) ps body _ -> (nr, n) : concatMap patIdents ps ++ exprIdents body
        Src.Destruct pat body            -> patIdents pat ++ exprIdents body


regionContains :: A.Region -> Int -> Int -> Bool
regionContains (A.Region s e) line col =
    let afterStart = (A._line s < line) || (A._line s == line && A._col s <= col)
        beforeEnd  = (A._line e > line) || (A._line e == line && A._col e >= col)
    in afterStart && beforeEnd


-- ─── Definition / Declaration ─────────────────────────────────────────

handleDefinition :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleDefinition docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId A.Null
            Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId A.Null
                Just name ->
                    case findDefinition srcMod (baseName name) of
                        Just reg -> sendReply reqId $ A.object
                            [ "uri"   A..= uri
                            , "range" A..= regionToLspRange reg
                            ]
                        Nothing -> sendReply reqId A.Null
  where
    -- `String.length` → `length`; we only look up local decls by short name.
    baseName n = case break (== '.') n of
        (_, '.':rest) -> rest
        _             -> n


findDefinition :: Src.Module -> String -> Maybe A.Region
findDefinition srcMod name = firstJust
    [ fromValue v | A.At _ v <- Src._values srcMod ]
  where
    fromValue v =
        let A.At reg n = Src._valueName v
        in if n == name then Just reg else Nothing

    firstJust = foldr (\m acc -> case m of Just r -> Just r; Nothing -> acc) Nothing


-- ─── Semantic Tokens ──────────────────────────────────────────────────
--
-- LSP encodes semantic tokens as a flat [Int] with 5 integers per token:
--   [deltaLine, deltaStartChar, length, tokenType, tokenModifiers]
-- deltaLine and deltaStartChar are relative to the previous token (or 0
-- if first). Editors use the legend to map integer types to names.

-- | Order here defines the numeric tokenType index sent on the wire.
semanticTokenTypes :: [T.Text]
semanticTokenTypes =
    [ "namespace"   -- 0
    , "type"        -- 1
    , "class"       -- 2
    , "enum"        -- 3
    , "enumMember"  -- 4
    , "function"    -- 5
    , "variable"    -- 6
    , "parameter"   -- 7
    , "property"    -- 8
    , "string"      -- 9
    , "number"      -- 10
    , "keyword"     -- 11
    ]


-- | A single semantic token before delta-encoding.
data SemToken = SemToken
    { _st_line :: !Int     -- 0-based
    , _st_col  :: !Int     -- 0-based
    , _st_len  :: !Int
    , _st_type :: !Int     -- index into semanticTokenTypes
    }


handleSemanticTokens :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleSemanticTokens docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId $ A.object ["data" A..= ([] :: [Int])]
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId $ A.object ["data" A..= ([] :: [Int])]
            Right srcMod ->
                let tokens  = sortBy compareTokenPos (collectSemTokens srcMod)
                    encoded = deltaEncode tokens
                in sendReply reqId $ A.object ["data" A..= encoded]
  where
    compareTokenPos a b =
        compare (_st_line a, _st_col a) (_st_line b, _st_col b)


-- | Flatten to [deltaLine, deltaStartChar, length, tokenType, 0] tuples.
deltaEncode :: [SemToken] -> [Int]
deltaEncode = go 0 0
  where
    go _ _ [] = []
    go prevLine prevCol (t:ts) =
        let dLine = _st_line t - prevLine
            dCol  = if dLine == 0 then _st_col t - prevCol else _st_col t
        in [dLine, dCol, _st_len t, _st_type t, 0]
           ++ go (_st_line t) (_st_col t) ts


-- | Walk the source tree, emitting a typed token for every identifier
-- we can classify.
collectSemTokens :: Src.Module -> [SemToken]
collectSemTokens srcMod =
       -- Imports — each segment is a namespace.
       concatMap importTokens (Src._imports srcMod)
    ++ -- Type declarations (unions + aliases): name is a type; ctors are enumMembers.
       concatMap unionTokens  (Src._unions srcMod)
    ++ concatMap aliasTokens  (Src._aliases srcMod)
       -- Value declarations and bodies.
    ++ concatMap (valueTokens srcMod) (Src._values srcMod)
  where
    mkTok reg ty =
        let A.Region (A.Position l c) (A.Position l2 c2) = reg
            len = if l == l2 then max 1 (c2 - c) else 1
        in SemToken (l - 1) (c - 1) len ty

    importTokens imp =
        let A.At reg segs = Src._importName imp
            A.Region (A.Position l c) _ = reg
            -- Length = sum of segments + dots between them.
            totalLen = case segs of
                []     -> 0
                (x:xs) -> length x + sum [length s + 1 | s <- xs]
        in [SemToken (l - 1) (c - 1) totalLen 0]  -- namespace

    unionTokens (A.At _ u) =
        let A.At nr _ = Src._unionName u
        in [mkTok nr 3]  -- enum (type)
    aliasTokens (A.At _ a) =
        let A.At nr _ = Src._aliasName a
        in [mkTok nr 2]  -- class (type alias)

    valueTokens _ (A.At _ v) =
        let A.At nr _ = Src._valueName v
            pats      = Src._valuePatterns v
            body      = Src._valueBody v
            paramToks = concatMap patternTokens pats
            paramNames = Set.fromList (concatMap patternNames pats)
            bodyToks  = exprTokens paramNames body
            headTokKind = if null pats then 6 else 5  -- variable / function
        in mkTok nr headTokKind : paramToks ++ bodyToks

    -- Pattern positions for parameter highlighting.
    patternTokens (A.At reg p) = case p of
        Src.PVar _       -> [mkTok reg 7]  -- parameter
        Src.PAlias i (A.At nr _) -> mkTok nr 7 : patternTokens i
        Src.PTuple a b cs -> concatMap patternTokens (a : b : cs)
        Src.PList xs     -> concatMap patternTokens xs
        Src.PCons h t    -> patternTokens h ++ patternTokens t
        Src.PCtor _ _ xs -> concatMap patternTokens xs
        Src.PCtorQual _ _ xs -> concatMap patternTokens xs
        Src.PRecord fields -> [ mkTok fr 7 | A.At fr _ <- fields ]
        _ -> []

    -- Classify references inside an expression. `locals` tracks names bound
    -- by surrounding params / lets so we can mark them `variable` vs the
    -- default `function` for unknown names.
    exprTokens :: Set.Set String -> Src.Expr -> [SemToken]
    exprTokens locals (A.At reg e) = case e of
        Src.Var n
            | isUpper (headChar n) -> [mkTok reg 4]  -- enumMember (constructor)
            | Set.member n locals  -> [mkTok reg 6]  -- variable (local)
            | otherwise            -> [mkTok reg 5]  -- function (top-level or import)
        Src.VarQual _ n
            | isUpper (headChar n) -> [mkTok reg 4]
            | otherwise            -> [mkTok reg 5]
        Src.Int _    -> [mkTok reg 10]
        Src.Float _  -> [mkTok reg 10]
        Src.Str _    -> [mkTok reg 9]
        Src.Chr _    -> [mkTok reg 9]
        Src.MultilineStr _ -> [mkTok reg 9]
        Src.Call f xs -> exprTokens locals f ++ concatMap (exprTokens locals) xs
        Src.Binops pairs final ->
            concat [exprTokens locals e' | (e', _) <- pairs] ++ exprTokens locals final
        Src.Lambda pats body ->
            let inner = Set.union locals (Set.fromList (concatMap patternNames pats))
            in concatMap patternTokens pats ++ exprTokens inner body
        Src.If branches elseE ->
            concatMap (\(a, b) -> exprTokens locals a ++ exprTokens locals b) branches
            ++ exprTokens locals elseE
        Src.Let defs body ->
            let letNames = Set.fromList (concatMap letDefNamesSafe defs)
                inner    = Set.union locals letNames
            in concatMap (letDefTokens inner) defs ++ exprTokens inner body
        Src.Case scrut arms ->
            exprTokens locals scrut
            ++ concatMap (\(p, rhs) ->
                let inner = Set.union locals (Set.fromList (patternNames p))
                in patternTokens p ++ exprTokens inner rhs) arms
        Src.Access t (A.At fr _) -> exprTokens locals t ++ [mkTok fr 8]  -- property
        Src.Update (A.At nr _) fields ->
            mkTok nr 6 : concat [mkTok fr 8 : exprTokens locals v | (A.At fr _, v) <- fields]
        Src.Record fields ->
            concat [mkTok fr 8 : exprTokens locals v | (A.At fr _, v) <- fields]
        Src.Tuple a b cs ->
            exprTokens locals a ++ exprTokens locals b ++ concatMap (exprTokens locals) cs
        Src.List xs -> concatMap (exprTokens locals) xs
        Src.Negate i -> exprTokens locals i
        -- `Src.Paren (Expr)` wraps a grouped sub-expression introduced
        -- to survive Binops precedence-climbing (added in 85ef8d1).
        -- Recurse transparently — the parens don't emit tokens themselves
        -- but the inner expression does. Missing match here would make
        -- the case non-exhaustive → pattern-match exception → swallowed
        -- by runLsp's `try`, so `handleSemanticTokens` would never send
        -- its reply and every LSP client that requested semantic tokens
        -- would hang waiting. Was the cause of LSP.CapabilitiesSpec's
        -- 7th test blocking cabal test indefinitely.
        Src.Paren e'   -> exprTokens locals e'
        Src.Accessor _ -> []
        Src.Op _       -> []
        Src.Unit       -> []

    letDefNamesSafe (A.At _ d) = case d of
        Src.Define (A.At _ n) _ _ _ -> [n]
        Src.Destruct pat _          -> patternNames pat

    letDefTokens locals (A.At _ d) = case d of
        Src.Define (A.At nr _) pats body _ ->
            let inner = Set.union locals (Set.fromList (concatMap patternNames pats))
            in mkTok nr 6 : concatMap patternTokens pats ++ exprTokens inner body
        Src.Destruct pat body -> patternTokens pat ++ exprTokens locals body

    headChar [] = ' '
    headChar (c:_) = c

    isUpper c = c >= 'A' && c <= 'Z'


-- ─── Inlay Hints ──────────────────────────────────────────────────────

-- | textDocument/inlayHint — show inferred types next to let-bindings
-- and function parameters that lack explicit annotations. Editors
-- render these as faded inline labels (`x: Int = 42` style).
--
-- For now we emit:
--   * Inferred types after `let x =` when the binder has no
--     annotation. Reads from the workspace index's idxLocalTypes
--     map populated by Solve.solveWithLocals.
--   * Inferred types after a top-level `name args =` when the
--     value has no annotation — gives users free type confirmation
--     while writing.
handleInlayHint :: ServerState -> A.Value -> Maybe A.Value -> IO ()
handleInlayHint st req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        path = uriToPath uri
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
        Just (_, text) -> do
            r <- try (computeInlayHints st path text) :: IO (Either SomeException [A.Value])
            case r of
                Right hs -> sendReply reqId (A.toJSON hs)
                Left _   -> sendReply reqId (A.toJSON ([] :: [A.Value]))


computeInlayHints :: ServerState -> FilePath -> T.Text -> IO [A.Value]
computeInlayHints st path text = case Parse.parseModule text of
    Left _ -> return []
    Right srcMod -> do
        eidx <- try (getIndex st path) :: IO (Either SomeException Idx.Index)
        case eidx of
            Left _    -> return []
            Right idx -> do
                let rename = fromMaybe Map.empty
                                (Map.lookup path (Idx.idxRenaming idx))
                    localTypes = fromMaybe Map.empty
                                    (Map.lookup path (Idx.idxLocalTypes idx))
                    -- Top-level values without explicit annotation.
                    topHints =
                        [ topLevelHint name reg t rename
                        | A.At _ v <- Src._values srcMod
                        , let A.At reg name = Src._valueName v
                        , Nothing <- [Src._valueType v]
                        , Just (t:_) <- [Map.lookup name localTypes]
                        ]
                    -- Local let-bindings: walk every value's body
                    -- collecting Defines without annotations.
                    localHints = concatMap (letHints rename localTypes) (Src._values srcMod)
                return (topHints ++ localHints)


-- | Hint after a top-level binder name `foo` → ` : Int -> String`.
-- Position is the END of the binder name; LSP renders the label
-- inline at that column.
topLevelHint :: String -> A.Region -> Ty.Type -> Map.Map String String -> A.Value
topLevelHint _ (A.Region _ end) ty rename =
    let label = " : " ++ Solve.showTypeWith rename ty
    in A.object
        [ "position" A..= A.object
            [ "line"      A..= (max 0 (A._line end - 1) :: Int)
            , "character" A..= (max 0 (A._col end - 1)  :: Int)
            ]
        , "label"  A..= label
        , "kind"   A..= (1 :: Int)        -- LSP InlayHintKind.Type
        , "paddingLeft" A..= False
        ]


-- | Walk a top-level value's body for let-Defines without
-- annotations and emit a type hint after each binder name.
letHints :: Map.Map String String -> Map.Map String [Ty.Type] -> A.Located Src.Value -> [A.Value]
letHints rename localTypes (A.At _ v) =
    let body = Src._valueBody v
    in walkExpr body
  where
    walkExpr :: Src.Expr -> [A.Value]
    walkExpr (A.At _ e) = case e of
        Src.Let defs body -> concatMap goDef defs ++ walkExpr body
        Src.Lambda _ inner -> walkExpr inner
        Src.Call f xs -> walkExpr f ++ concatMap walkExpr xs
        Src.Binops pairs end ->
            concatMap (walkExpr . fst) pairs ++ walkExpr end
        Src.If arms els ->
            concatMap (\(c, b) -> walkExpr c ++ walkExpr b) arms ++ walkExpr els
        Src.Case s arms -> walkExpr s ++ concatMap (walkExpr . snd) arms
        Src.Tuple a b cs -> walkExpr a ++ walkExpr b ++ concatMap walkExpr cs
        Src.List xs -> concatMap walkExpr xs
        Src.Negate inner -> walkExpr inner
        Src.Paren inner -> walkExpr inner
        Src.Access t _ -> walkExpr t
        Src.Update _ fs -> concatMap (walkExpr . snd) fs
        Src.Record fs -> concatMap (walkExpr . snd) fs
        _ -> []

    goDef (A.At _ d) = case d of
        -- Skip Defines that already have an annotation.
        Src.Define (A.At reg n) _ body (Just _) -> walkExpr body
        Src.Define (A.At reg n) _ body Nothing ->
            case Map.lookup n localTypes of
                Just (t:_) ->
                    [ A.object
                        [ "position" A..= A.object
                            [ "line"      A..= (max 0 (A._line (regEnd reg) - 1) :: Int)
                            , "character" A..= (max 0 (A._col (regEnd reg) - 1)  :: Int)
                            ]
                        , "label"  A..= (" : " ++ Solve.showTypeWith rename t)
                        , "kind"   A..= (1 :: Int)
                        , "paddingLeft" A..= False
                        ]
                    ] ++ walkExpr body
                _ -> walkExpr body
        Src.Destruct _ body -> walkExpr body

    regEnd (A.Region _ e) = e


-- ─── Code Actions ─────────────────────────────────────────────────────

handleCodeAction :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleCodeAction docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId (A.toJSON ([] :: [A.Value]))
            Right srcMod -> do
                annotActions <- addAnnotationActions uri text srcMod
                let actions = unusedImportActions uri text srcMod
                           ++ organizeImportsActions uri text srcMod
                           ++ annotActions
                sendReply reqId (A.toJSON actions)


-- | Detect imports whose exposed (or aliased) names are never referenced
-- in the module. Offer a one-line removal.
--
-- Rules (intentionally conservative to avoid false positives):
--   * imports ending in `Prelude` are never flagged — they're re-export
--     surfaces whose operators (e.g. `++`) bypass our AST-level detector;
--   * we walk value bodies AND type annotations (union ctors + alias bodies
--     + value-type signatures);
--   * we ALSO text-scan the raw source for the import's alias as a
--     word boundary — the parser currently drops value-level type
--     signatures onto the floor, so AST-only detection is unsafe.
unusedImportActions :: T.Text -> T.Text -> Src.Module -> [A.Value]
unusedImportActions uri rawText srcMod =
    let astRefs = collectAllRefs srcMod
        isUsed imp = importIsUsed imp astRefs
                  || importAliasAppearsInSource imp rawText
        dead = [ imp | imp <- Src._imports srcMod
                     , not (isPrelude imp)
                     , not (isUsed imp) ]
    in map (removeImportAction uri) dead
  where

    -- Crude "text mention" scan — looks for the alias or last-segment
    -- surrounded by non-identifier chars anywhere past the imports block.
    importAliasAppearsInSource imp text =
        let alias = case Src._importAlias imp of
                Just a  -> a
                Nothing -> case Src._importName imp of
                    A.At _ segs -> last segs
            pattern = T.pack alias
            -- Skip the import line itself by starting past the last import.
            body = pastImports (Src._imports srcMod) text
        in hasWordMatch pattern body

    pastImports [] t = t
    pastImports imps t =
        let lastImportLine = maximum
                [ l | imp <- imps
                , let A.At (A.Region _ (A.Position l _)) _ = Src._importName imp
                ]
            ls = T.lines t
        in T.unlines (drop lastImportLine ls)

    -- "word match" = pattern surrounded by non-identifier chars.
    hasWordMatch pattern haystack = go (T.unpack haystack) (T.unpack pattern)
      where
        go src pat =
            case break (`elem` ['\n', ' ', '\t', '(', ')', ',', '.']) src of
                (tok, rest)
                    | tok == pat -> True
                    | null rest  -> False
                    | otherwise  -> go (tail rest) pat
    isPrelude imp = case Src._importName imp of
        A.At _ segs -> last segs == "Prelude"

    collectAllRefs :: Src.Module -> Set.Set String
    collectAllRefs m = Set.fromList $
        concatMap valueRefs   (Src._values m)
        ++ concatMap unionRefs  (Src._unions m)
        ++ concatMap aliasRefs  (Src._aliases m)

    valueRefs (A.At _ v) =
        exprAllRefs (Src._valueBody v)
        ++ case Src._valueType v of
            Just (A.At _ ta) -> typeAnnotNames ta
            Nothing          -> []

    unionRefs (A.At _ u) = concatMap ctorArgNames (Src._unionCtors u)
    ctorArgNames (A.At _ (_, args)) = concatMap typeAnnotNames args

    aliasRefs (A.At _ al) =
        let A.At _ ta = Src._aliasType al
        in typeAnnotNames ta

    -- Every type-level identifier we can see. Qualified names (e.g.
    -- `String.Char`) contribute both the qualifier and the full dotted form.
    typeAnnotNames :: Src.TypeAnnotation -> [String]
    typeAnnotNames t = case t of
        Src.TVar _             -> []
        Src.TLambda a b        -> typeAnnotNames a ++ typeAnnotNames b
        Src.TType _mod segs args -> segs ++ concatMap typeAnnotNames args
        Src.TTypeQual modPath n args -> [modPath, n] ++ concatMap typeAnnotNames args
        Src.TRecord fs _       -> concatMap (\(_, ft) -> typeAnnotNames ft) fs
        Src.TUnit              -> []
        Src.TTuple a b cs      -> typeAnnotNames a ++ typeAnnotNames b
                                ++ concatMap typeAnnotNames cs

    exprAllRefs (A.At _ e) = case e of
        Src.Var n -> [n]
        Src.VarQual q n -> [q, q ++ "." ++ n]
        Src.Call f xs -> exprAllRefs f ++ concatMap exprAllRefs xs
        Src.Binops pairs final ->
            concat [exprAllRefs e' | (e', _) <- pairs] ++ exprAllRefs final
        Src.Lambda _ body -> exprAllRefs body
        Src.If branches elseE ->
            concat [exprAllRefs a ++ exprAllRefs b | (a, b) <- branches]
            ++ exprAllRefs elseE
        Src.Let defs body ->
            concatMap (\(A.At _ d) -> case d of
                Src.Define _ _ b _ -> exprAllRefs b
                Src.Destruct _ b   -> exprAllRefs b) defs
            ++ exprAllRefs body
        Src.Case scrut arms ->
            exprAllRefs scrut ++ concatMap (\(_, b) -> exprAllRefs b) arms
        Src.Access t _ -> exprAllRefs t
        Src.Update _ fields -> concat [exprAllRefs v | (_, v) <- fields]
        Src.Record fields   -> concat [exprAllRefs v | (_, v) <- fields]
        Src.Tuple a b cs -> exprAllRefs a ++ exprAllRefs b ++ concatMap exprAllRefs cs
        Src.List xs -> concatMap exprAllRefs xs
        Src.Negate i -> exprAllRefs i
        Src.Paren e' -> exprAllRefs e'
        _ -> []

    importIsUsed imp refs =
        let qualifier = case Src._importAlias imp of
                Just a  -> a
                Nothing -> case Src._importName imp of
                    A.At _ segs -> last segs
            exposedNames = case Src._importExposing imp of
                A.At _ (Src.ExposingList xs) -> concatMap exposedName xs
                _                            -> []
        in Set.member qualifier refs
           || any (`Set.member` refs) exposedNames

    exposedName (A.At _ e) = case e of
        Src.ExposedValue n    -> [n]
        Src.ExposedType n _   -> [n]
        Src.ExposedOperator _ -> []


removeImportAction :: T.Text -> Src.Import -> A.Value
removeImportAction uri imp =
    let A.At reg _ = Src._importName imp
        -- Remove the full line the import lives on.
        A.Region (A.Position l _) _ = reg
        range = lspRange (l - 1) 0 l 0
    in A.object
        [ "title"    A..= T.pack "Remove unused import"
        , "kind"     A..= T.pack "quickfix"
        , "isPreferred" A..= True
        , "edit"     A..= A.object
            [ "changes" A..= A.object
                [ AK.fromText uri A..=
                    [ A.object
                        [ "range"   A..= range
                        , "newText" A..= T.pack ""
                        ]
                    ]
                ]
            ]
        ]


-- | Offer to sort every import alphabetically. Always available; LSP
-- clients filter it by kind `source.organizeImports`.
organizeImportsActions :: T.Text -> T.Text -> Src.Module -> [A.Value]
organizeImportsActions uri _text srcMod =
    case Src._imports srcMod of
        []  -> []
        [_] -> []
        imps ->
            let sorted = sortBy (comparing importPath) imps
                sortedPaths = map importPath sorted
                origPaths   = map importPath imps
            in if sortedPaths == origPaths
                then []
                else [organizeAction uri sorted imps]
  where
    importPath imp = case Src._importName imp of
        A.At _ segs -> segs


organizeAction :: T.Text -> [Src.Import] -> [Src.Import] -> A.Value
organizeAction uri sorted original =
    let firstReg = case original of
            (imp:_) -> let A.At r _ = Src._importName imp in r
            []      -> A.one
        lastReg  = case reverse original of
            (imp:_) -> let A.At r _ = Src._importName imp in r
            []      -> A.one
        A.Region (A.Position l0 _) _ = firstReg
        A.Region _ (A.Position l1 _) = lastReg
        range = lspRange (l0 - 1) 0 l1 9999
        sortedText = T.intercalate (T.pack "\n") (map renderImport sorted)
    in A.object
        [ "title" A..= T.pack "Organize imports"
        , "kind"  A..= T.pack "source.organizeImports"
        , "edit"  A..= A.object
            [ "changes" A..= A.object
                [ AK.fromText uri A..=
                    [ A.object
                        [ "range"   A..= range
                        , "newText" A..= sortedText
                        ]
                    ]
                ]
            ]
        ]


renderImport :: Src.Import -> T.Text
renderImport imp =
    let A.At _ segs = Src._importName imp
        base = T.pack ("import " ++ foldr1 (\a b -> a ++ "." ++ b) segs)
        aliasPart = case Src._importAlias imp of
            Just a  -> T.pack (" as " ++ a)
            Nothing -> T.empty
        exposingPart = case Src._importExposing imp of
            A.At _ Src.ExposingAll            -> T.pack " exposing (..)"
            A.At _ (Src.ExposingList [])      -> T.empty
            A.At _ (Src.ExposingList xs)      ->
                T.pack (" exposing (" ++ foldr1 (\a b -> a ++ ", " ++ b)
                                                (concatMap exposedShow xs) ++ ")")
    in base `T.append` aliasPart `T.append` exposingPart
  where
    exposedShow (A.At _ e) = case e of
        Src.ExposedValue n    -> [n]
        Src.ExposedType n Src.Public -> [n ++ "(..)"]
        Src.ExposedType n _   -> [n]
        Src.ExposedOperator o -> ["(" ++ o ++ ")"]


-- | Offer to add a type annotation to any value that lacks one. The
-- inferred type comes from the solver.
addAnnotationActions :: T.Text -> T.Text -> Src.Module -> IO [A.Value]
addAnnotationActions uri _text srcMod = do
    r <- try (runInfer srcMod) :: IO (Either SomeException (Map.Map String Ty.Type))
    case r of
        Left _      -> return []
        Right types -> return (mapMaybe (annotAction types) (Src._values srcMod))
  where
    hasAnnotation v = case Src._valueType v of
        Just _  -> True
        Nothing -> False

    runInfer m = case Canonicalise.canonicalise m of
        Left _       -> return Map.empty
        Right canMod -> do
            cs <- Constrain.constrainModule canMod
            r  <- Solve.solve cs
            case r of
                Solve.SolveOk types -> return types
                _                   -> return Map.empty

    annotAction types (A.At _ v)
        | hasAnnotation v = Nothing
        | otherwise =
            let A.At nr n = Src._valueName v
            in case Map.lookup n types of
                Nothing -> Nothing
                Just t  ->
                    let typeStr = Solve.showType t
                        A.Region (A.Position l _) _ = nr
                        lineIdx = l - 1  -- 0-based
                        -- Insert `name : type` on a new line just above the decl.
                        annotLine = T.pack (n ++ " : " ++ typeStr ++ "\n")
                        insertRange = lspRange lineIdx 0 lineIdx 0
                    in Just $ A.object
                        [ "title" A..= T.pack ("Add type annotation: " ++ n ++ " : " ++ typeStr)
                        , "kind"  A..= T.pack "quickfix"
                        , "edit"  A..= A.object
                            [ "changes" A..= A.object
                                [ AK.fromText uri A..=
                                    [ A.object
                                        [ "range"   A..= insertRange
                                        , "newText" A..= annotLine
                                        ]
                                    ]
                                ]
                            ]
                        ]


-- ─── References / Rename ─────────────────────────────────────────────

handleReferences :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleReferences docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId (A.toJSON ([] :: [A.Value]))
            Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
                Just name ->
                    let regions = collectReferences srcMod (simpleName name)
                        locations = [ A.object
                                        [ "uri"   A..= uri
                                        , "range" A..= regionToLspRange r
                                        ]
                                    | r <- regions
                                    ]
                    in sendReply reqId (A.toJSON locations)


handlePrepareRename :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handlePrepareRename docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId A.Null
            Right srcMod -> case identAtRegion srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId A.Null
                Just (n, reg) -> sendReply reqId $ A.object
                    [ "range"       A..= regionToLspRange reg
                    , "placeholder" A..= n
                    ]


handleRename :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleRename docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
        newName = jsonStrAt ["params", "newName"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId A.Null
            Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId A.Null
                Just name -> do
                    let short  = simpleName name
                        nameLen = length short
                        refs   = collectReferences srcMod short
                        edits  =
                            [ A.object
                                [ "range"   A..= clampRangeWidth r nameLen
                                , "newText" A..= newName
                                ]
                            | r <- refs
                            ]
                    sendReply reqId $ A.object
                        [ "changes" A..= A.object [ AK.fromText uri A..= edits ] ]


-- | Workspace-wide rename. Walks every file in the index for
-- references to `name` and produces a WorkspaceEdit.
--
-- Strategy:
--   1. Identify the symbol being renamed via the workspace index.
--   2. If it's a LOCAL binding, rename only in the current file
--      (locals can't escape the file they're declared in).
--   3. If it's a TOP-LEVEL symbol (function, type, ctor), walk
--      every file's parsed module for references — both unqualified
--      uses (after `exposing`) and module-qualified uses
--      (`Mod.name`, accounting for the file's import alias).
handleRenameSt :: ServerState -> A.Value -> Maybe A.Value -> IO ()
handleRenameSt st req reqId = do
    let uri  = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
        newName = jsonStrAt ["params", "newName"] req
        path = uriToPath uri
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId A.Null
            Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId A.Null
                Just name -> do
                    let short = simpleName name
                    eidx <- try (getIndex st path) :: IO (Either SomeException Idx.Index)
                    case eidx of
                        -- Index unavailable → fall back to file-only.
                        Left _    -> sendFileOnlyRename uri text short newName reqId
                        Right idx -> do
                            -- Is this a top-level symbol the workspace
                            -- knows about? lookupAtCursor returns Just
                            -- when it is.
                            let mSym = Idx.lookupAtCursor idx path (line + 1) (col + 1) name
                                isTopLevel = case mSym of
                                    Just s -> Idx.symKind s `elem`
                                                [Idx.SymFunction, Idx.SymCtor, Idx.SymType]
                                    Nothing -> False
                            if isTopLevel
                                then sendWorkspaceRename idx short newName reqId
                                else sendFileOnlyRename uri text short newName reqId


-- File-only fallback for locals + cases where the workspace index
-- can't be built.
sendFileOnlyRename :: T.Text -> T.Text -> String -> T.Text -> Maybe A.Value -> IO ()
sendFileOnlyRename uri text short newName reqId = case Parse.parseModule text of
    Left _ -> sendReply reqId A.Null
    Right srcMod -> do
        let nameLen = length short
            refs   = collectReferences srcMod short
            edits  = [ A.object
                          [ "range"   A..= clampRangeWidth r nameLen
                          , "newText" A..= newName
                          ]
                     | r <- refs
                     ]
        sendReply reqId $ A.object
            [ "changes" A..= A.object [ AK.fromText uri A..= edits ] ]


-- Walk every file in the workspace for references and build a
-- WorkspaceEdit covering all of them.
sendWorkspaceRename :: Idx.Index -> String -> T.Text -> Maybe A.Value -> IO ()
sendWorkspaceRename idx short newName reqId = do
    let nameLen = length short
        files = Map.toList (Idx.idxFileSrc idx)
        perFileEdits =
            [ (filePath, edits)
            | (filePath, src) <- files
            , Right srcMod <- [Parse.parseModule src]
            , let refs = collectReferences srcMod short
            , not (null refs)
            , let edits = [ A.object
                              [ "range"   A..= clampRangeWidth r nameLen
                              , "newText" A..= newName
                              ]
                          | r <- refs ]
            ]
        changes = A.object
            [ AK.fromText (T.pack ("file://" ++ p)) A..= eds
            | (p, eds) <- perFileEdits
            ]
    sendReply reqId $ A.object [ "changes" A..= changes ]


-- | Guarantee a rename edit's end column equals `startCol + nameLength`.
-- Parser regions are sometimes one char too wide (trailing non-identifier
-- consumed during lookahead); trimming keeps surrounding whitespace intact.
clampRangeWidth :: A.Region -> Int -> A.Value
clampRangeWidth (A.Region s e) nameLen =
    let startLine = A._line s - 1
        startCol  = A._col  s - 1
        endLine   = A._line e - 1
        fullEndCol = A._col  e - 1
        -- Only clamp single-line regions; multi-line stays as-is.
        endCol = if A._line s == A._line e
                    then min fullEndCol (startCol + nameLen)
                    else fullEndCol
    in lspRange startLine startCol endLine endCol


-- | Every occurrence of `name` (unqualified) anywhere in the module —
-- top-level declarations, pattern bindings (lambda params, let bindings,
-- case arms), and call sites. Shadowing is respected: once an inner
-- scope shadows the name we stop recording inner uses.
collectReferences :: Src.Module -> String -> [A.Region]
collectReferences srcMod name =
    let declRefs =
            [ reg
            | A.At _ v <- Src._values srcMod
            , let A.At reg n = Src._valueName v, n == name
            ]
        bodyRefs = concatMap
            (\(A.At _ v) ->
                let pats = Src._valuePatterns v
                    body = Src._valueBody v
                    paramHits = patternRefs name pats
                in paramHits ++ refsInExpr name Set.empty body)
            (Src._values srcMod)
    in declRefs ++ bodyRefs


-- | Scan patterns for occurrences of the target name (PVar / PAlias).
patternRefs :: String -> [Src.Pattern] -> [A.Region]
patternRefs target = concatMap (patternRefsOne target)

patternRefsOne :: String -> Src.Pattern -> [A.Region]
patternRefsOne target (A.At reg p) = case p of
    Src.PVar n
        | n == target -> [reg]
        | otherwise   -> []
    Src.PAlias inner (A.At nr n) ->
        (if n == target then [nr] else []) ++ patternRefsOne target inner
    Src.PCtor _ _ xs     -> concatMap (patternRefsOne target) xs
    Src.PCtorQual _ _ xs -> concatMap (patternRefsOne target) xs
    Src.PCons h t        -> patternRefsOne target h ++ patternRefsOne target t
    Src.PList xs         -> concatMap (patternRefsOne target) xs
    Src.PTuple a b cs    -> patternRefsOne target a ++ patternRefsOne target b
                          ++ concatMap (patternRefsOne target) cs
    Src.PRecord fields   -> [ fr | A.At fr n <- fields, n == target ]
    _                    -> []


refsInExpr :: String -> Set.Set String -> Src.Expr -> [A.Region]
refsInExpr target shadowed (A.At reg e) = case e of
    Src.Var n
        | n == target && not (Set.member n shadowed) -> [reg]
        | otherwise -> []
    Src.VarQual _ n
        | n == target -> [reg]
        | otherwise   -> []
    Src.Call f xs -> refsInExpr target shadowed f ++ concatMap (refsInExpr target shadowed) xs
    Src.Binops pairs final ->
        concat [refsInExpr target shadowed e' | (e', _) <- pairs]
        ++ refsInExpr target shadowed final
    Src.Lambda pats body ->
        -- Include the pattern's binding region(s) — renaming the lambda
        -- parameter means both the binder and every use inside the body
        -- must update together. Keep the target OUT of `shadowed` so its
        -- body uses remain reachable.
        let bound   = Set.fromList (concatMap patternNames pats)
            others  = Set.delete target bound
            shadowed' = Set.union shadowed others
            paramPositions = patternRefs target pats
        in paramPositions ++ refsInExpr target shadowed' body
    Src.If branches elseE ->
        concat [refsInExpr target shadowed a ++ refsInExpr target shadowed b | (a, b) <- branches]
        ++ refsInExpr target shadowed elseE
    Src.Let defs body ->
        -- Each def's bound name IS a rename target: references to it
        -- MUST stay visible inside the let body. We therefore only add
        -- OTHER let-bound names to shadows — not the target itself.
        let defNames    = Set.fromList (concatMap letDefNames defs)
            otherDefs   = Set.delete target defNames
            shadowed'   = Set.union shadowed otherDefs
        in concatMap (letDefRefs target shadowed') defs
        ++ refsInExpr target shadowed' body
    Src.Case scrut arms ->
        refsInExpr target shadowed scrut
        ++ concatMap (\(p, rhs) ->
            let bound  = Set.fromList (patternNames p)
                others = Set.delete target bound
                shadowed' = Set.union shadowed others
            in patternRefsOne target p ++ refsInExpr target shadowed' rhs) arms
    Src.Access target' _ -> refsInExpr target shadowed target'
    Src.Update _ fields  -> concat [refsInExpr target shadowed v | (_, v) <- fields]
    Src.Record fields    -> concat [refsInExpr target shadowed v | (_, v) <- fields]
    Src.Tuple a b cs ->
        refsInExpr target shadowed a ++ refsInExpr target shadowed b
        ++ concatMap (refsInExpr target shadowed) cs
    Src.List xs       -> concatMap (refsInExpr target shadowed) xs
    Src.Negate inner  -> refsInExpr target shadowed inner
    -- Recurse transparently into grouped sub-expressions. Without
    -- this, `println (greet "world")` hid the `greet` reference
    -- behind the `Paren` node and textDocument/definition/rename/
    -- references all failed silently — fixture-defined by the
    -- skychess parser-paren-preservation commit (85ef8d1).
    Src.Paren inner   -> refsInExpr target shadowed inner
    _ -> []
  where
    letDefNames (A.At _ d) = case d of
        Src.Define (A.At _ n) _ _ _ -> [n]
        Src.Destruct pat _          -> patternNames pat

    -- A let-bound value's name is a rename target; its own params are a
    -- fresh inner shadow scope.
    letDefRefs t sh (A.At _ d) = case d of
        Src.Define (A.At nr n) pats body _ ->
            let bindingHit = if n == t then [nr] else []
                bound   = Set.fromList (concatMap patternNames pats)
                others  = Set.delete t bound
                sh'     = Set.union sh others
                paramHits = patternRefs t pats
            in bindingHit ++ paramHits ++ refsInExpr t sh' body
        Src.Destruct pat body ->
            patternRefsOne t pat ++ refsInExpr t sh body


-- | The local names bound by a pattern.
patternNames :: Src.Pattern -> [String]
patternNames (A.At _ p) = case p of
    Src.PVar n        -> [n]
    Src.PCtor _ _ xs  -> concatMap patternNames xs
    Src.PCtorQual _ _ xs -> concatMap patternNames xs
    Src.PCons h t     -> patternNames h ++ patternNames t
    Src.PList xs      -> concatMap patternNames xs
    Src.PTuple a b cs -> patternNames a ++ patternNames b ++ concatMap patternNames cs
    Src.PRecord ns    -> map (\(A.At _ n) -> n) ns
    Src.PAlias inner (A.At _ n) -> n : patternNames inner
    _                 -> []


-- | Like identAtPosition but also returns the exact Region of the word.
identAtRegion :: Src.Module -> Int -> Int -> Maybe (String, A.Region)
identAtRegion srcMod line col =
    let matches = [ (reg, n) | (reg, n) <- collectIdents srcMod
                             , regionContains reg line col ]
    in case sortBy (comparing (regionWidth . fst)) matches of
        ((reg, n):_) -> Just (n, reg)
        []           -> Nothing


-- | Strip a qualifier: `String.length` → `length`; `foo` → `foo`.
simpleName :: String -> String
simpleName n = case break (== '.') n of
    (_, '.':rest) -> rest
    _             -> n


-- ─── Signature Help ───────────────────────────────────────────────────

handleSignatureHelp :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleSignatureHelp docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> do
            r <- try (computeSignatureHelp text line col)
                :: IO (Either SomeException (Maybe A.Value))
            case r of
                Right (Just v) -> sendReply reqId v
                _              -> sendReply reqId A.Null


-- | Find the innermost `Call` expression whose region contains the cursor
-- and whose function-head region ends before it. Emit the function's type
-- and the 0-based index of the argument the cursor is currently in.
--
-- This supports Sky's paren-less call style (`greet "World"`) as well as
-- parenthesised calls (`greet ("World")`).
computeSignatureHelp :: T.Text -> Int -> Int -> IO (Maybe A.Value)
computeSignatureHelp text line col = case Parse.parseModule text of
    Left _       -> return Nothing
    Right srcMod -> case enclosingCall srcMod (line + 1) (col + 1) of
        Nothing                    -> return Nothing
        Just (funcName, paramIdx) ->
            case Canonicalise.canonicalise srcMod of
                Left _ -> return (Just (mkSignature funcName "" paramIdx))
                Right canMod -> do
                    cs <- Constrain.constrainModule canMod
                    r  <- Solve.solve cs
                    case r of
                        Solve.SolveOk types ->
                            case Map.lookup (simpleName funcName) types of
                                Just t  -> return (Just (mkSignature funcName (Solve.showType t) paramIdx))
                                Nothing -> return (Just (mkSignature funcName "" paramIdx))
                        _ -> return (Just (mkSignature funcName "" paramIdx))


-- | Walk every value body looking for a `Call` whose region contains the
-- cursor but whose function-head region does NOT (so we're past the head
-- in argument territory). Pick the innermost such call.
enclosingCall :: Src.Module -> Int -> Int -> Maybe (String, Int)
enclosingCall srcMod line col =
    let calls =
            [ (reg, funcName, argIdx)
            | A.At _ v <- Src._values srcMod
            , (reg, funcName, argIdx) <- findCalls line col (Src._valueBody v)
            ]
    in case sortBy (comparing (regionWidth . fstOf3)) calls of
        ((_, f, i):_) -> Just (f, i)
        []            -> Nothing
  where
    fstOf3 (a, _, _) = a


-- | Recurse into an expression collecting every Call whose outer region
-- contains (line, col) and whose function-head region does not — plus the
-- argument index the cursor falls into.
findCalls :: Int -> Int -> Src.Expr -> [(A.Region, String, Int)]
findCalls line col (A.At reg e) = here ++ recurse
  where
    here = case e of
        Src.Call f args
          | regionContains reg line col
          , not (regionContains (A.toRegion f) line col)
          , Just funcName <- exprHeadName f ->
                let idx = argIndexAtPos line col args
                in [(reg, funcName, idx)]
        _ -> []

    recurse = case e of
        Src.Call f args           -> findCalls line col f ++ concatMap (findCalls line col) args
        Src.Binops pairs final    -> concat [findCalls line col e' | (e', _) <- pairs] ++ findCalls line col final
        Src.Lambda _ body         -> findCalls line col body
        Src.If branches elseE     -> concat [findCalls line col a ++ findCalls line col b | (a, b) <- branches] ++ findCalls line col elseE
        Src.Let defs body         -> concatMap letInner defs ++ findCalls line col body
        Src.Case scrut arms       -> findCalls line col scrut ++ concatMap (\(_, b) -> findCalls line col b) arms
        Src.Access t _            -> findCalls line col t
        Src.Update _ fields       -> concat [findCalls line col v | (_, v) <- fields]
        Src.Record fields         -> concat [findCalls line col v | (_, v) <- fields]
        Src.Tuple a b cs          -> findCalls line col a ++ findCalls line col b ++ concatMap (findCalls line col) cs
        Src.List xs               -> concatMap (findCalls line col) xs
        Src.Negate inner          -> findCalls line col inner
        _                         -> []

    letInner (A.At _ d) = case d of
        Src.Define _ _ body _ -> findCalls line col body
        Src.Destruct _ body   -> findCalls line col body


-- | The function head of `Var f`, `VarQual m f`, or a parenthesised call.
exprHeadName :: Src.Expr -> Maybe String
exprHeadName (A.At _ e) = case e of
    Src.Var n       -> Just n
    Src.VarQual q n -> Just (q ++ "." ++ n)
    _               -> Nothing


-- | Index of the first argument whose region starts past the cursor
-- (i.e. the one we're currently typing). When cursor is past all args
-- we return `length args` so signatureHelp highlights the next param.
argIndexAtPos :: Int -> Int -> [Src.Expr] -> Int
argIndexAtPos line col = go 0
  where
    go !i []     = i
    go !i (a:as) =
        let A.Region s _ = A.toRegion a
            startLine = A._line s
            startCol  = A._col  s
            pastArg   = (startLine < line) || (startLine == line && startCol <= col)
        in if regionContains (A.toRegion a) line col || pastArg
               then go (i + 1) as
               else i


mkSignature :: String -> String -> Int -> A.Value
mkSignature funcName typeStr paramIdx =
    let label = funcName ++ (if null typeStr then "" else " : " ++ typeStr)
    in A.object
        [ "signatures" A..= A.toJSON
            [ A.object
                [ "label"       A..= label
                , "documentation" A..= ("" :: T.Text)
                ]
            ]
        , "activeSignature" A..= (0 :: Int)
        , "activeParameter" A..= paramIdx
        ]


-- ─── Document Symbols ─────────────────────────────────────────────────

handleDocumentSymbol :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleDocumentSymbol docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId (A.toJSON ([] :: [A.Value]))
            Right srcMod -> sendReply reqId (A.toJSON (documentSymbols srcMod))


-- | SymbolKind constants (LSP spec):
--   Function = 12, Constant = 14, Class = 5, Enum = 10, TypeParameter = 26.
documentSymbols :: Src.Module -> [A.Value]
documentSymbols srcMod =
       [ symbol n reg (if null pats then 14 else 12)  -- Constant : Function
       | A.At _ v <- Src._values srcMod
       , let A.At reg n = Src._valueName v
       , let pats = Src._valuePatterns v
       ]
    ++ [ symbol n reg 10                              -- Enum
       | A.At _ u <- Src._unions srcMod
       , let A.At reg n = Src._unionName u
       ]
    ++ [ symbol n reg 5                               -- Class (type alias)
       | A.At _ al <- Src._aliases srcMod
       , let A.At reg n = Src._aliasName al
       ]
  where
    symbol n reg kind = A.object
        [ "name"           A..= n
        , "kind"           A..= (kind :: Int)
        , "range"          A..= regionToLspRange reg
        , "selectionRange" A..= regionToLspRange reg
        ]


-- ─── Formatting ───────────────────────────────────────────────────────

handleFormatting :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleFormatting docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId (A.toJSON ([] :: [A.Value]))
        Just (_, text) -> case Parse.parseModule text of
            Left _       -> sendReply reqId (A.toJSON ([] :: [A.Value]))
            Right srcMod -> do
                let formatted = Fmt.formatModule srcMod
                    totalLines = max 1 (length (T.lines text))
                    edit = A.object
                        [ "range" A..= lspRange 0 0 (totalLines + 1) 0
                        , "newText" A..= T.pack formatted
                        ]
                sendReply reqId (A.toJSON [edit])


-- ─── Completion ────────────────────────────────────────────────────────

handleCompletionSt :: ServerState -> A.Value -> Maybe A.Value -> IO ()
handleCompletionSt st req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
        path = uriToPath uri
    m <- IORef.readIORef (ssDocs st)
    items <- case Map.lookup uri m of
        Nothing -> return stdlibCompletions
        Just (_, text) -> do
            let ctx    = prefixAt text line col
                -- Parse recovery: the user typing `model.<cursor>`
                -- produces a trailing-dot expression that's a parse
                -- error. We fall back to a recovered text where the
                -- trailing `.` is filled with a placeholder field
                -- name so the parser succeeds and we can walk the
                -- AST. The recovered text is ONLY used for AST-based
                -- completion paths; the original text is what the
                -- user sees in their editor.
                module_ = case Parse.parseModule text of
                    Right m'  -> Just m'
                    Left _    -> tryParseWithRecovery text line col
                locals = maybe [] localCompletions module_
            -- Import-statement completion: when the current LINE starts
            -- with `import `, suggest module names from the workspace
            -- index. Cheap to detect (line text only, no AST), and
            -- doesn't conflict with regular expression completion
            -- because import lines never contain expression syntax.
            inImport <- detectImportLine text line col
            importMatches <- if inImport
                then resolveImportCompletion st path ctx
                else return []
            -- Pattern position in `case ... of` arms: suggest
            -- constructors of the scrutinee's type.
            patternMatches <- case module_ of
                Just sm -> resolvePatternCompletion st path sm line col ctx
                Nothing -> return []
            -- Qualified prefix paths. Two flavours:
            --   * `<lowercase>.partial` → record field access on a
            --     value (model.count). Look up the value's type,
            --     enumerate its alias's fields.
            --   * `<UpperCase>.partial` → module-qualified call
            --     (Ui.layout, String.toUpp).
            qualMatches <- if '.' `T.elem` ctx && not inImport
                then resolveDotCompletion st path module_ line col ctx
                else return []
            -- For unqualified prefixes, also include workspace index
            -- symbols (top-level + exposed names from the file's
            -- explicit imports) AND let-bound names whose scope
            -- contains the cursor.
            unqualIdx <- if not ('.' `T.elem` ctx) && not inImport
                then resolveUnqualifiedCompletionAt (line + 1) (col + 1)
                            st path module_ ctx
                else return []
            let all_ = if inImport
                    then importMatches  -- only show modules in import lines
                    else patternMatches ++ locals ++ qualMatches
                            ++ unqualIdx ++ stdlibCompletions
            return (filterCompletions ctx all_)
    sendReply reqId (A.object
        [ "isIncomplete" A..= False
        , "items"        A..= items
        ])


-- | Replace the cursor's line with a parse-recoverable variant so
-- common completion contexts that fail full-text parse (`model.`,
-- `Ui.`, `case x of`) still produce a usable AST.
--
-- Recovery rules (applied in order):
--   1. Trailing `<ident>.` → append `__sky_lsp_placeholder` so the
--      Access expression parses cleanly. The walker then sees a
--      legitimate Access node we can use for field completion.
--   2. Bare `import <prefix>` line stays as-is (already parses or
--      doesn't matter since import-line completion uses line text).
--   3. Other contexts: replace the line with a stub `_ = 0` so the
--      rest of the file parses. We lose the cursor's local AST but
--      preserve workspace context.
tryParseWithRecovery :: T.Text -> Int -> Int -> Maybe Src.Module
tryParseWithRecovery text line col =
    let recovered = recoverText text line col
    in case Parse.parseModule recovered of
        Right m -> Just m
        Left _  ->
            -- Last-resort: replace the offending line entirely.
            let stubbed = stubLine text line
            in case Parse.parseModule stubbed of
                Right m -> Just m
                Left _  -> Nothing


-- | Recover a Sky source whose cursor line ends in a dot or
-- otherwise breaks the parser. Returns text with a placeholder
-- field name appended after a trailing dot.
recoverText :: T.Text -> Int -> Int -> T.Text
recoverText text line _col =
    let ls = T.lines text
    in if line < 0 || line >= length ls
        then text
        else
            let cur = ls !! line
                cur' = if T.isSuffixOf "." (T.stripEnd cur)
                       then cur `T.append` "__sky_lsp_placeholder"
                       else cur
                ls' = take line ls ++ [cur'] ++ drop (line + 1) ls
            in T.unlines ls'


-- | Replace the cursor line with a stub binding `__sky_lsp_stub__ = 0`.
-- Used when finer-grained recovery doesn't restore parseability.
stubLine :: T.Text -> Int -> T.Text
stubLine text line =
    let ls = T.lines text
    in if line < 0 || line >= length ls
        then text
        else
            let stub = T.pack "__sky_lsp_stub__ = 0"
                ls' = take line ls ++ [stub] ++ drop (line + 1) ls
            in T.unlines ls'


-- | Decide whether the cursor is inside an `import ` statement based
-- only on the text of the current line. Cheap (no AST walk). True
-- iff the line's first non-whitespace word is `import` and the
-- cursor is past it.
detectImportLine :: T.Text -> Int -> Int -> IO Bool
detectImportLine text line col = do
    let ls = T.lines text
    if line < 0 || line >= length ls
        then return False
        else do
            let cur = ls !! line
                stripped = T.stripStart cur
            return (T.isPrefixOf "import " stripped
                    && col >= T.length (T.takeWhile (== ' ') cur) + 7)


-- | Disambiguate dot-prefix completion based on case of the LHS:
-- lowercase first letter → record field access on a value; uppercase
-- → module-qualified call.
resolveDotCompletion :: ServerState -> FilePath -> Maybe Src.Module -> Int -> Int -> T.Text -> IO [A.Value]
resolveDotCompletion st path mModule line col ctx =
    case mModule of
        Nothing -> return []
        Just srcMod -> do
            let parts = T.splitOn "." ctx
            case parts of
                xs@(_:_:_) ->
                    let lhs = T.intercalate "." (init xs)
                        partial = last xs
                    in case T.uncons lhs of
                        Just (h, _)
                            | h >= 'a' && h <= 'z' ->
                                resolveRecordFieldCompletion srcMod (line + 1) (col + 1)
                                    (T.unpack lhs) (T.unpack partial)
                            | otherwise ->
                                resolveQualifiedCompletion st path mModule ctx
                        Nothing -> return []
                _ -> return []


-- | Enumerate the fields of a record-typed value for completion at
-- `model.<Tab>` style. Uses the same alias-chain resolution as
-- field hover: target type → alias body → field list.
resolveRecordFieldCompletion :: Src.Module -> Int -> Int -> String -> String -> IO [A.Value]
resolveRecordFieldCompletion srcMod line col target partial = do
    case findTargetType srcMod line col target of
        Nothing -> return []
        Just typeExpr -> do
            -- Walk through alias chain (same-file). Cross-file aliases
            -- are resolved separately (task #65).
            case typeExpr of
                Src.TRecord fields _ ->
                    return (fieldsToCompletions target partial fields)
                _ ->
                    case extractTypeName typeExpr of
                        Just typeName ->
                            let aliasBodies =
                                    [ body
                                    | A.At _ a <- Src._aliases srcMod
                                    , let A.At _ n = Src._aliasName a
                                    , n == typeName
                                    , let A.At _ body = Src._aliasType a
                                    ]
                            in case aliasBodies of
                                (Src.TRecord fields _ : _) ->
                                    return (fieldsToCompletions target partial fields)
                                _ -> return []
                        Nothing -> return []
  where
    fieldsToCompletions baseName lp fields =
        -- Three fields, each doing one job:
        --   * label     = bare field name (clean dropdown)
        --   * insertText = bare field name (cursor is after the dot;
        --                  inserting "count" yields "m.count")
        --   * filterText = qualified form (so the server's '.'-aware
        --                  filterCompletions still keeps the item, and
        --                  editor-side fuzzy matchers see the prefix the
        --                  user actually typed).
        [ A.object
            [ "label"      A..= fname
            , "insertText" A..= fname
            , "filterText" A..= (baseName ++ "." ++ fname)
            , "kind"       A..= (5 :: Int)  -- LSP CompletionItemKind.Field
            , "detail"     A..= renderTypeAnnotation ftype
            ]
        | (A.At _ fname, ftype) <- fields
        , null lp || lp `isPrefixOf` fname
        ]


-- | Constructor completion in pattern position. When the cursor is
-- inside a `case ... of` arm's pattern slot, look up the scrutinee's
-- type — if it's a known ADT, suggest its constructors.
--
-- v1: handles top-level case scrutinees that are simple Vars whose
-- type can be resolved via findTargetType (top-level annotation OR
-- function parameter). Nested cases / complex scrutinees fall
-- through with no false-positive completions.
resolvePatternCompletion :: ServerState -> FilePath -> Src.Module -> Int -> Int -> T.Text -> IO [A.Value]
resolvePatternCompletion st path srcMod line col partial = do
    let l = line + 1
        c = col + 1
    case findCasePatternContext srcMod l c of
        Nothing -> return []
        Just scrutVar -> do
            -- Find scrutinee's type; expect it to name an ADT.
            case findTargetType srcMod l c scrutVar of
                Nothing -> return []
                Just typeExpr -> case extractTypeName typeExpr of
                    Just adtName -> do
                        eidx <- try (getIndex st path)
                                :: IO (Either SomeException Idx.Index)
                        case eidx of
                            Left _    -> return []
                            Right idx -> return (ctorCompletionsFor idx adtName partial)
                    Nothing -> return []


-- | Walk the source for the smallest enclosing `Case` whose pattern
-- region contains the cursor — that's the position where we'd want
-- ctor suggestions. Returns the scrutinee's variable name (only
-- handles `case x of ...` v1; `case (foo y) of ...` not yet).
findCasePatternContext :: Src.Module -> Int -> Int -> Maybe String
findCasePatternContext srcMod line col =
    let hits = concatMap (walkValue . extractValueExpr) (Src._values srcMod)
    in case hits of
        (n:_) -> Just n
        []    -> Nothing
  where
    extractValueExpr (A.At _ v) = Src._valueBody v

    walkExpr :: Src.Expr -> [String]
    walkExpr (A.At _ e) = case e of
        Src.Case (A.At _ (Src.Var scrut)) arms ->
            -- Cursor in any pattern of an arm? Use pat region.
            let inPattern = any (\(A.At pr _, _) -> regionContains pr line col) arms
            in if inPattern then [scrut] else concatMap (walkExpr . snd) arms
        Src.Case scrut arms ->
            walkExpr scrut ++ concatMap (walkExpr . snd) arms
        Src.Lambda _ body  -> walkExpr body
        Src.Let _ body     -> walkExpr body
        Src.Call f xs      -> walkExpr f ++ concatMap walkExpr xs
        Src.Binops pairs x -> concatMap (walkExpr . fst) pairs ++ walkExpr x
        Src.If arms els    -> concatMap (\(c', b) -> walkExpr c' ++ walkExpr b) arms ++ walkExpr els
        Src.Tuple a b cs   -> walkExpr a ++ walkExpr b ++ concatMap walkExpr cs
        Src.List xs        -> concatMap walkExpr xs
        Src.Negate inner   -> walkExpr inner
        Src.Paren inner    -> walkExpr inner
        Src.Access t _     -> walkExpr t
        Src.Update _ fs    -> concatMap (walkExpr . snd) fs
        Src.Record fs      -> concatMap (walkExpr . snd) fs
        _ -> []

    walkValue :: Src.Expr -> [String]
    walkValue = walkExpr


-- | Enumerate constructors of an ADT from the workspace index.
ctorCompletionsFor :: Idx.Index -> String -> T.Text -> [A.Value]
ctorCompletionsFor idx adtName partial =
    let lp = T.unpack partial
        allCtors = [ s | s <- concat (Map.elems (Idx.idxByLocal idx))
                       , Idx.symKind s == Idx.SymCtor ]
        -- Filter ctors by membership in the named ADT — heuristic:
        -- ctor sigs like "Red : Colour" or "Just : a -> Maybe a".
        belongsTo s =
            case Idx.symTypeSig s of
                Just sig ->
                    -- Match ": Adt" or "-> Adt" anywhere
                    (": " ++ adtName) `isInfixOf` sig
                    || ("-> " ++ adtName) `isInfixOf` sig
                Nothing -> False
        matching = [ s | s <- allCtors
                       , belongsTo s
                       , null lp || lp `isPrefixOf` Idx.symLocalName s ]
    in [ A.object
            [ "label"  A..= Idx.symLocalName s
            , "kind"   A..= (21 :: Int)  -- EnumMember
            , "detail" A..= maybe "" id (Idx.symTypeSig s)
            ]
       | s <- matching ]
  where
    isInfixOf needle hay = any (needle `isPrefixOf`) (tails hay)
    tails [] = [[]]
    tails xs@(_:rest) = xs : tails rest


-- | Module-name completion in `import <prefix>`. Pulls from
-- idxModules — every module the workspace has discovered.
resolveImportCompletion :: ServerState -> FilePath -> T.Text -> IO [A.Value]
resolveImportCompletion st path prefix = do
    eidx <- try (getIndex st path) :: IO (Either SomeException Idx.Index)
    case eidx of
        Left _    -> return []
        Right idx -> do
            let lp = T.unpack prefix
                modules = Map.keys (Idx.idxModules idx)
            return [ A.object
                        [ "label"  A..= modName
                        , "kind"   A..= (9 :: Int)  -- Module
                        , "detail" A..= ("module " ++ modName)
                        ]
                   | modName <- modules
                   , null lp || lp `isPrefixOf` modName ]


-- | Build completion items by resolving a qualified prefix against
-- the workspace index. e.g. `Ui.lay` → look up `Ui` in the file's
-- imports → "Std.Ui" → enumerate every Sym whose qualName starts
-- with "Std.Ui." and whose local name starts with "lay".
resolveQualifiedCompletion :: ServerState -> FilePath -> Maybe Src.Module -> T.Text -> IO [A.Value]
resolveQualifiedCompletion st path mModule ctx = do
    case mModule of
        Nothing -> return []
        Just srcMod -> do
            eidx <- try (getIndex st path) :: IO (Either SomeException Idx.Index)
            case eidx of
                Left _    -> return []
                Right idx -> do
                    let parts = T.splitOn "." ctx
                    case parts of
                        [aliasOrName, localPrefix] -> do
                            let alias = T.unpack aliasOrName
                                lp    = T.unpack localPrefix
                                realModule = resolveImportAlias srcMod alias
                            -- Use the user's alias as the label
                            -- prefix — matching what they actually
                            -- typed. If the file has `import Std.Ui
                            -- as Ui`, completion shows `Ui.layout`
                            -- not `Std.Ui.layout`. Cleaner DX +
                            -- editors apply the completion as-is.
                            return (matchingExports idx realModule lp alias)
                        _ -> return []
  where
    matchingExports idx modName lp displayPrefix =
        let qualPrefix = modName ++ "."
            symMatches =
                [ s | (q, s) <- Map.toList (Idx.idxByQual idx)
                    , qualPrefix `isPrefixOf` q
                    , lp `isPrefixOf` Idx.symLocalName s ]
        in [ A.object
                -- `label` is what the user sees in the dropdown
                -- (`Ui.layout`). `insertText` is what the editor
                -- actually inserts when the item is accepted —
                -- ONLY the local name (`layout`), since the user
                -- has already typed `Ui.`. Without `insertText`
                -- the editor inserts the label, producing
                -- `Ui.Ui.layout`.
                [ "label"      A..= (displayPrefix ++ "." ++ Idx.symLocalName s)
                , "insertText" A..= Idx.symLocalName s
                , "kind"       A..= symKindToLsp (Idx.symKind s)
                , "detail"     A..= maybe "" id (Idx.symTypeSig s)
                ]
           | s <- symMatches ]


-- | Resolve a module alias used in the user's file to its true name.
-- `import Sky.Core.String as String` → alias "String" → "Sky.Core.String".
-- `import Std.Ui as Ui` → "Ui" → "Std.Ui".
-- If the alias matches no import, return it as-is (might be a real
-- module name).
resolveImportAlias :: Src.Module -> String -> String
resolveImportAlias srcMod alias =
    let imports = Src._imports srcMod
        match = listToMaybeFirst
            [ joinDots origName
            | imp <- imports
            , let A.At _ origName = Src._importName imp
            , let aliasName = case Src._importAlias imp of
                    Just a -> a
                    Nothing -> joinDots origName
            , aliasName == alias
            ]
    in case match of
        Just real -> real
        Nothing   -> alias
  where
    joinDots :: [String] -> String
    joinDots = foldr (\a b -> if null b then a else a ++ "." ++ b) ""
    listToMaybeFirst []    = Nothing
    listToMaybeFirst (x:_) = Just x


-- | Build unqualified completions from the index — top-level names
-- in the current file's project, AND let-bound names whose scope
-- contains the cursor. The cursor parameters are 1-based.
resolveUnqualifiedCompletion :: ServerState -> FilePath -> Maybe Src.Module -> T.Text -> IO [A.Value]
resolveUnqualifiedCompletion = resolveUnqualifiedCompletionAt 0 0


resolveUnqualifiedCompletionAt :: Int -> Int -> ServerState -> FilePath -> Maybe Src.Module -> T.Text -> IO [A.Value]
resolveUnqualifiedCompletionAt line col st path _mModule prefix = do
    eidx <- try (getIndex st path) :: IO (Either SomeException Idx.Index)
    case eidx of
        Left _    -> return []
        Right idx -> do
            let lp = T.unpack prefix
                -- Top-level + ctor + alias names in the same file.
                fileSyms = fromMaybe [] (Map.lookup path (Idx.idxByFile idx))
                topLevelItems =
                    [ A.object
                        [ "label"  A..= Idx.symLocalName s
                        , "kind"   A..= symKindToLsp (Idx.symKind s)
                        , "detail" A..= maybe "" id (Idx.symTypeSig s)
                        ]
                    | s <- fileSyms
                    , null lp || lp `isPrefixOf` Idx.symLocalName s ]
                -- Let-bound + lambda-param + case-binder names whose
                -- scope contains the cursor — the user typing
                -- `let abc = 123 in ab|` should get `abc` suggested.
                localItems
                    | line <= 0 || col <= 0 = []
                    | otherwise = localBindingsAtCursor idx path line col lp
            return (localItems ++ topLevelItems)


-- | All let-/lambda-/case-bound names whose scope contains the
-- 1-based (line, col) cursor. Pulled from idxLocals; type rendered
-- via idxLocalTypes when the solver populated them.
localBindingsAtCursor :: Idx.Index -> FilePath -> Int -> Int -> String -> [A.Value]
localBindingsAtCursor idx path line col lp =
    let bs = fromMaybe [] (Map.lookup path (Idx.idxLocals idx))
        localTypes = fromMaybe Map.empty (Map.lookup path (Idx.idxLocalTypes idx))
        rename = fromMaybe Map.empty (Map.lookup path (Idx.idxRenaming idx))
        inScope =
            [ b | b <- bs
                , scopeContains (Idx.lbScope b) line col
                , null lp || lp `isPrefixOf` Idx.lbName b ]
        seen = foldr (\b acc -> Idx.lbName b : acc) [] inScope
        deduped = nubOrdered seen
    in [ A.object
            [ "label" A..= name
            , "kind"  A..= (6 :: Int)  -- Variable
            , "detail" A..= renderLocalType name
            ]
       | name <- deduped ]
  where
    scopeContains (A.Region s e) ln cl =
        let afterStart = (A._line s < ln) || (A._line s == ln && A._col s <= cl)
            beforeEnd  = (A._line e > ln) || (A._line e == ln && A._col e >= cl)
        in afterStart && beforeEnd
    nubOrdered = foldr (\x acc -> if x `elem` acc then acc else x : acc) []
    renderLocalType :: String -> String
    renderLocalType _name = ""  -- TODO: lookup in idxLocalTypes; need rename merge


symKindToLsp :: Idx.SymKind -> Int
symKindToLsp k = case k of
    Idx.SymFunction -> 3   -- LSP CompletionItemKind.Function
    Idx.SymCtor     -> 21  -- LSP CompletionItemKind.EnumMember
    Idx.SymType     -> 7   -- LSP CompletionItemKind.Class
    Idx.SymLocal    -> 6   -- LSP CompletionItemKind.Variable
    Idx.SymParam    -> 6   -- Variable


-- | The word immediately left of the cursor. Supports `String.foo`.
prefixAt :: T.Text -> Int -> Int -> T.Text
prefixAt text line col =
    let ls = T.lines text
    in if line < 0 || line >= length ls
           then T.empty
           else
               let current = ls !! line
                   upto    = T.take col current
               in T.reverse (T.takeWhile isIdent (T.reverse upto))
  where
    isIdent c = c == '.' || c == '_' || (c >= 'a' && c <= 'z')
                || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')


-- | Filter an item list by prefix. If the prefix contains `.`, only
-- exact-prefix matches are kept — no fuzzy fallback. Without a `.` we
-- permit case-insensitive infix matches ranked below prefix matches.
filterCompletions :: T.Text -> [A.Value] -> [A.Value]
filterCompletions prefix items
    | T.null prefix   = items
    | '.' `T.elem` prefix =
        -- Qualified prefix (`String.`) → strict prefix matching against
        -- whichever string the editor will use to match the user's
        -- input. LSP says: editor matches against `filterText` if
        -- present, else `label`. We follow the same rule on the server.
        let p = T.unpack prefix
        in [ v | v <- items, p `isPrefixOf` matchKey v ]
    | otherwise =
        let p  = T.unpack prefix
            ms = mapMaybe (scored p) items
        in map snd (sortBy (comparing fst) ms)
  where
    matchKey :: A.Value -> String
    matchKey v =
        let ft = T.unpack (jsonStr "filterText" v)
            lb = T.unpack (jsonStr "label" v)
        in if null ft then lb else ft

    scored :: String -> A.Value -> Maybe (Int, A.Value)
    scored p v =
        let key = matchKey v
        in if p `isPrefixOf` key
               then Just (0, v)
               else if T.toLower (T.pack p) `T.isInfixOf` T.toLower (T.pack key)
                   then Just (1, v)
                   else Nothing


localCompletions :: Src.Module -> [A.Value]
localCompletions srcMod =
       [ item n 12 | A.At _ v <- Src._values srcMod
                   , let A.At _ n = Src._valueName v ]
    ++ [ item n 10 | A.At _ u <- Src._unions srcMod
                   , let A.At _ n = Src._unionName u ]
    ++ [ item n  5 | A.At _ al <- Src._aliases srcMod
                   , let A.At _ n = Src._aliasName al ]
  where
    item n kind = A.object
        [ "label" A..= n
        , "kind"  A..= (kind :: Int)
        ]


stdlibCompletions :: [A.Value]
stdlibCompletions =
    [ item n | n <-
        -- Prelude
        [ "println", "identity", "always", "not", "toString"
        , "fst", "snd", "clamp", "modBy"
        -- String
        , "String.length", "String.toUpper", "String.toLower", "String.trim"
        , "String.split", "String.join", "String.contains", "String.fromInt"
        , "String.isEmail", "String.isUrl", "String.slugify"
        , "String.htmlEscape", "String.truncate", "String.ellipsize"
        , "String.normalize", "String.graphemes", "String.equalFold"
        -- List
        , "List.map", "List.filter", "List.foldl", "List.length", "List.head"
        -- Dict
        , "Dict.empty", "Dict.get", "Dict.insert", "Dict.keys"
        -- Task / Result / Maybe
        , "Task.succeed", "Task.perform", "Task.andThen", "Task.map"
        , "Result.withDefault", "Result.map", "Maybe.withDefault"
        -- Crypto
        , "Crypto.sha256", "Crypto.hmacSha256", "Crypto.randomToken"
        , "Crypto.constantTimeEqual"
        -- Uuid
        , "Uuid.v4", "Uuid.v7"
        -- Path
        , "Path.join", "Path.safeJoin"
        -- Http / Server
        , "Http.get", "Http.post", "Server.listen", "Server.get", "Server.html"
        -- Sky.Live
        , "app", "route", "div", "button", "text", "onClick"
        -- Logging / Env
        , "Log.info", "Log.warn", "Log.error", "Env.get", "Env.require"
        -- FFI
        , "Ffi.callPure", "Ffi.callTask"
        ]
    ]
  where
    item n = A.object ["label" A..= (T.pack n), "kind" A..= (3 :: Int)]


-- ─── JSON-RPC helpers ──────────────────────────────────────────────────

sendReply :: Maybe A.Value -> A.Value -> IO ()
sendReply reqId result =
    when (not (isNull reqId)) $ sendMessage $ A.object
        [ "jsonrpc" A..= ("2.0" :: T.Text)
        , "id"      A..= fromMaybe A.Null reqId
        , "result"  A..= result
        ]
  where
    isNull Nothing = True
    isNull (Just A.Null) = True
    isNull _ = False


sendNotification :: T.Text -> A.Value -> IO ()
sendNotification method params = sendMessage $ A.object
    [ "jsonrpc" A..= ("2.0" :: T.Text)
    , "method"  A..= method
    , "params"  A..= params
    ]


-- ─── JSON accessors ────────────────────────────────────────────────────

asObj :: A.Value -> Maybe A.Object
asObj (A.Object o) = Just o
asObj _ = Nothing


jsonStr :: T.Text -> A.Value -> T.Text
jsonStr k v = case asObj v of
    Just o -> case KM.lookup (AK.fromText k) o of
        Just (A.String s) -> s
        _ -> ""
    _ -> ""


jsonStrAt :: [T.Text] -> A.Value -> T.Text
jsonStrAt path v = case descend path v of
    A.String s -> s
    _ -> ""


jsonIntAt :: [T.Text] -> A.Value -> Int
jsonIntAt path v = case descend path v of
    A.Number n -> truncate n
    _ -> 0


jsonArrAt :: [T.Text] -> A.Value -> Maybe [A.Value]
jsonArrAt path v = case descend path v of
    A.Array xs -> Just (foldr (:) [] xs)
    _ -> Nothing


descend :: [T.Text] -> A.Value -> A.Value
descend [] v = v
descend (p:ps) (A.Object o) = case KM.lookup (AK.fromText p) o of
    Just v -> descend ps v
    Nothing -> A.Null
descend _ _ = A.Null
