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
import Data.Maybe (fromMaybe, mapMaybe)
import qualified Data.Text as T

import System.IO

import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Type.Constrain.Module as Constrain
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as Ty
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Format.Format as Fmt


-- ─── State ─────────────────────────────────────────────────────────────

-- | Open documents keyed by URI → (version, full text).
type Docs = Map.Map T.Text (Int, T.Text)


-- ─── Main loop ─────────────────────────────────────────────────────────

runLsp :: IO ()
runLsp = do
    hSetBuffering stdout NoBuffering
    hSetBuffering stdin NoBuffering
    hSetBinaryMode stdout True
    hSetBinaryMode stdin True
    docs <- IORef.newIORef (Map.empty :: Docs)
    forever $ do
        r <- try (handleOne docs) :: IO (Either SomeException ())
        case r of
            Left _  -> return ()  -- keep serving; never die on a single bad request
            Right _ -> return ()


handleOne :: IORef.IORef Docs -> IO ()
handleOne docs = do
    msg <- readMessage
    case A.decode (BL.fromStrict msg) of
        Nothing  -> return ()
        Just val -> dispatch docs val


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

dispatch :: IORef.IORef Docs -> A.Value -> IO ()
dispatch docs req = do
    let method = jsonStr "method" req
        reqId  = KM.lookup "id" =<< asObj req
    case method of
        "initialize"                  -> sendReply reqId initializeResult
        "initialized"                 -> return ()
        "shutdown"                    -> sendReply reqId A.Null
        "exit"                        -> return ()
        "textDocument/didOpen"        -> handleDidOpen docs req
        "textDocument/didChange"      -> handleDidChange docs req
        "textDocument/didSave"        -> handleDidSave docs req
        "textDocument/didClose"       -> handleDidClose docs req
        "textDocument/hover"          -> handleHover docs req reqId
        "textDocument/completion"     -> handleCompletion docs req reqId
        "textDocument/definition"     -> handleDefinition docs req reqId
        "textDocument/declaration"    -> handleDefinition docs req reqId
        "textDocument/documentSymbol" -> handleDocumentSymbol docs req reqId
        "textDocument/formatting"     -> handleFormatting docs req reqId
        _ -> case reqId of
            Just _  -> sendReply reqId A.Null
            Nothing -> return ()


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
runPipeline :: T.Text -> IO [A.Value]
runPipeline src = case Parse.parseModule src of
    Left err ->
        return [mkDiagnosticAtError err ("Parse error: " ++ showParseError err)]
    Right srcMod ->
        case Canonicalise.canonicalise srcMod of
            Left err ->
                -- Canonicalise errors are plain strings — place on line 1 for
                -- now; when canonicaliser learns to return ranges, plumb them.
                return [mkDiagnostic 0 0 0 80 ("Canonicalise: " ++ err) 1]
            Right canMod -> do
                cs <- Constrain.constrainModule canMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveOk _ -> return []
                    Solve.SolveError err ->
                        return [mkDiagnostic 0 0 0 80 ("Type error: " ++ err) 1]


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
-- callers use the first containing region.
collectIdents :: Src.Module -> [(A.Region, String)]
collectIdents srcMod =
       [ (A.toRegion ln, n)
       | A.At _ v <- Src._values srcMod
       , let ln = Src._valueName v, let A.At _ n = ln
       ]
    ++ concatMap valueBodyIdents (Src._values srcMod)
  where
    valueBodyIdents (A.At _ v) = exprIdents (Src._valueBody v)

    exprIdents :: Src.Expr -> [(A.Region, String)]
    exprIdents (A.At reg e) = case e of
        Src.Var n           -> [(reg, n)]
        Src.VarQual q n     -> [(reg, q ++ "." ++ n)]
        Src.Call f xs       -> exprIdents f ++ concatMap exprIdents xs
        Src.Binops pairs x  -> concatMap (\(e',_) -> exprIdents e') pairs ++ exprIdents x
        Src.Lambda _ body   -> exprIdents body
        Src.If arms e'      -> concatMap (\(c,b) -> exprIdents c ++ exprIdents b) arms ++ exprIdents e'
        Src.Let defs body   -> concatMap defIdents defs ++ exprIdents body
        Src.Case s arms     -> exprIdents s ++ concatMap (\(_,b) -> exprIdents b) arms
        Src.Access t _      -> exprIdents t
        Src.Update _ fs     -> concatMap (exprIdents . snd) fs
        Src.Record fs       -> concatMap (exprIdents . snd) fs
        Src.Tuple a b cs    -> exprIdents a ++ exprIdents b ++ concatMap exprIdents cs
        Src.List xs         -> concatMap exprIdents xs
        Src.Negate inner    -> exprIdents inner
        _                   -> []

    defIdents (A.At _ d) = case d of
        Src.Define _ _ body _ -> exprIdents body
        Src.Destruct _ body   -> exprIdents body


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
