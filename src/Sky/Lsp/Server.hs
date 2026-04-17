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

import Control.Exception (SomeException, try)
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

import System.IO

import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Type.Constrain.Module as Constrain
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as Ty
import qualified Sky.Type.Exhaustiveness as Exhaust
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Format.Format as Fmt
import qualified Sky.Lsp.Index as Idx
import qualified System.Directory as Dir
import System.FilePath (takeDirectory, (</>))


-- ─── State ─────────────────────────────────────────────────────────────

-- | Open documents keyed by URI → (version, full text).
type Docs = Map.Map T.Text (Int, T.Text)


-- | Mutable LSP state: open docs + lazily built workspace index per
-- project root. The index is keyed by absolute project root path so
-- editors with multi-root workspaces work too.
data ServerState = ServerState
    { ssDocs  :: !(IORef.IORef Docs)
    , ssIndex :: !(IORef.IORef (Map.Map FilePath Idx.Index))
    }


-- ─── Main loop ─────────────────────────────────────────────────────────

runLsp :: IO ()
runLsp = do
    hSetBuffering stdout NoBuffering
    hSetBuffering stdin NoBuffering
    hSetBinaryMode stdout True
    hSetBinaryMode stdin True
    docs <- IORef.newIORef (Map.empty :: Docs)
    idx  <- IORef.newIORef (Map.empty :: Map.Map FilePath Idx.Index)
    let st = ServerState { ssDocs = docs, ssIndex = idx }
    forever $ do
        r <- try (handleOne st) :: IO (Either SomeException ())
        case r of
            Left _  -> return ()  -- keep serving; never die on a single bad request
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
        "shutdown"                    -> sendReply reqId A.Null
        "exit"                        -> return ()
        "textDocument/didOpen"        -> handleDidOpen docs req
        "textDocument/didChange"      -> handleDidChange docs req
        "textDocument/didSave"        -> handleDidSaveSt st req
        "textDocument/didClose"       -> handleDidClose docs req
        "textDocument/hover"          -> handleHoverIdx st req reqId
        "textDocument/completion"     -> handleCompletion docs req reqId
        "textDocument/definition"     -> handleDefinitionIdx st req reqId
        "textDocument/declaration"    -> handleDefinitionIdx st req reqId
        "textDocument/documentSymbol" -> handleDocumentSymbol docs req reqId
        "textDocument/formatting"     -> handleFormatting docs req reqId
        "textDocument/references"     -> handleReferencesIdx st req reqId
        "textDocument/rename"         -> handleRename docs req reqId
        "textDocument/prepareRename"  -> handlePrepareRename docs req reqId
        "textDocument/signatureHelp"  -> handleSignatureHelp docs req reqId
        "textDocument/codeAction"          -> handleCodeAction docs req reqId
        "textDocument/semanticTokens/full" -> handleSemanticTokens docs req reqId
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
            IORef.modifyIORef (ssIndex st) (Map.insert root idx)
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
                _              -> sendReply reqId A.Null


computeHoverIdx :: ServerState -> FilePath -> T.Text -> Int -> Int -> IO (Maybe A.Value)
computeHoverIdx st file text line col =
    case Parse.parseModule text of
        Left _ -> return Nothing
        Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
            Nothing   -> return Nothing
            Just name -> do
                idx <- getIndex st file
                let mSym = Idx.lookupAtCursor idx file (line + 1) (col + 1) name
                case mSym of
                    Just s | hasType s -> return (Just (mkHover (renderSym s)))
                    _ -> do
                        -- Fallback: run the single-file solve pipeline so
                        -- identifiers not in the index (stdlib kernels,
                        -- prelude builtins) or indexed without a type
                        -- (inferred functions) still get a type on hover.
                        solvedType <- solveForName srcMod name
                        case solvedType of
                            Just t  ->
                                let sig = name ++ " : " ++ Solve.showType t
                                    modLine = case mSym of
                                        Just s | Idx.symModule s /= "" ->
                                            "\n-- defined in " ++ Idx.symModule s
                                        _ -> ""
                                in return (Just (mkHover (sig ++ modLine)))
                            Nothing ->
                                case kernelTypeSig name of
                                    Just sig -> return (Just (mkHover (name ++ " : " ++ sig)))
                                    Nothing  -> case mSym of
                                        Just s  -> return (Just (mkHover (renderSym s)))
                                        Nothing -> return (Just (mkHover name))


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
            Right srcMod -> case identAtPosition srcMod (line + 1) (col + 1) of
                Nothing -> sendReply reqId A.Null
                Just name -> do
                    idx <- getIndex st path
                    case Idx.lookupAtCursor idx path (line + 1) (col + 1) name of
                        Just s -> sendReply reqId $ A.object
                            [ "uri"   A..= pathToUri (Idx.symFile s)
                            , "range" A..= regionToLspRange (Idx.symRegion s)
                            ]
                        Nothing -> sendReply reqId A.Null


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
    docs <- IORef.readIORef (ssDocs st)
    case Map.lookup uri docs of
        Just (_, text) -> publishDiagnostics uri text
        Nothing -> return ()
    -- Rebuild the workspace index so cross-file lookups see the change.
    -- Best effort — failures don't break the server.
    _ <- try (refreshIndex st path) :: IO (Either SomeException Idx.Index)
    return ()


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
publishDiagnostics uri text = do
    diags <- computeDiagnostics text
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
  where
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
        Src.Binops pairs x  -> concatMap (\(e',_) -> exprIdents e') pairs ++ exprIdents x
        Src.Lambda ps body  -> concatMap patIdents ps ++ exprIdents body
        Src.If arms e'      -> concatMap (\(c,b) -> exprIdents c ++ exprIdents b) arms ++ exprIdents e'
        Src.Let defs body   -> concatMap defIdents defs ++ exprIdents body
        Src.Case s arms     -> exprIdents s ++ concatMap (\(p, b) -> patIdents p ++ exprIdents b) arms
        Src.Access t _      -> exprIdents t
        Src.Update _ fs     -> concatMap (exprIdents . snd) fs
        Src.Record fs       -> concatMap (exprIdents . snd) fs
        Src.Tuple a b cs    -> exprIdents a ++ exprIdents b ++ concatMap exprIdents cs
        Src.List xs         -> concatMap exprIdents xs
        Src.Negate inner    -> exprIdents inner
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

handleCompletion :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleCompletion docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
        line = jsonIntAt ["params", "position", "line"] req
        col  = jsonIntAt ["params", "position", "character"] req
    m <- IORef.readIORef docs
    let (items, isIncomplete) = case Map.lookup uri m of
            Nothing -> (stdlibCompletions, False)
            Just (_, text) ->
                let ctx    = prefixAt text line col
                    module_ = either (const Nothing) Just (Parse.parseModule text)
                    locals = maybe [] localCompletions module_
                    all_   = locals ++ stdlibCompletions
                in (filterCompletions ctx all_, False)
    sendReply reqId (A.object
        [ "isIncomplete" A..= isIncomplete
        , "items"        A..= items
        ])


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
        -- Qualified prefix (`String.`) → strict prefix matching only.
        let p = T.unpack prefix
        in [ v | v <- items, p `isPrefixOf` T.unpack (jsonStr "label" v) ]
    | otherwise =
        let p  = T.unpack prefix
            ms = mapMaybe (scored p) items
        in map snd (sortBy (comparing fst) ms)
  where
    scored :: String -> A.Value -> Maybe (Int, A.Value)
    scored p v =
        let lbl = T.unpack (jsonStr "label" v)
        in if p `isPrefixOf` lbl
               then Just (0, v)
               else if T.toLower (T.pack p) `T.isInfixOf` T.toLower (T.pack lbl)
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
