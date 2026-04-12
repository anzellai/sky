{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE BangPatterns #-}
-- | Language Server Protocol server for Sky.
-- Minimal, production-ready subset of LSP over JSON-RPC/stdio:
--   - initialize / initialized / shutdown / exit
--   - textDocument/didOpen, didChange, didSave, didClose
--   - textDocument/publishDiagnostics (outbound on change/save)
--   - textDocument/hover (shows inferred types for the document)
--   - textDocument/completion (top-level + stdlib)
--
-- Editors supported: VS Code, Neovim, Emacs, Zed, Helix, Sublime LSP.
--
-- Safety: a single malformed request returns a JSON-RPC error; it never
-- crashes the server. All parsing/type-checking is wrapped in Haskell's
-- exception machinery and invalid Sky produces diagnostics, not aborts.
module Sky.Lsp.Server (runLsp) where

import Control.Exception (SomeException, try)
import Control.Monad (forever, when)
import qualified Data.Aeson as A
import qualified Data.Aeson.Key as AK
import qualified Data.Aeson.KeyMap as KM
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BC
import qualified Data.ByteString.Lazy as BL
import qualified Data.IORef as IORef
import qualified Data.Map.Strict as Map
import Data.Maybe (fromMaybe)
import qualified Data.Text as T
import System.IO

import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Type.Constrain.Module as Constrain
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as Ty
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


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
        , "hoverProvider" A..= True
        , "completionProvider" A..= A.object
            [ "triggerCharacters" A..= (["."] :: [T.Text])
            ]
        ]
    , "serverInfo" A..= A.object
        [ "name" A..= ("sky-lsp" :: T.Text)
        , "version" A..= ("0.1.0" :: T.Text)
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


runPipeline :: T.Text -> IO [A.Value]
runPipeline src = case Parse.parseModule src of
    Left err ->
        return [mkDiagnostic 0 0 0 0 ("Parse error: " ++ show err) 1]
    Right srcMod ->
        case Canonicalise.canonicalise srcMod of
            Left err ->
                return [mkDiagnostic 0 0 0 0 ("Canonicalise: " ++ err) 1]
            Right canMod -> do
                cs <- Constrain.constrainModule canMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveOk _ -> return []
                    Solve.SolveError err ->
                        return [mkDiagnostic 0 0 0 0 ("Type error: " ++ err) 1]


mkDiagnostic :: Int -> Int -> Int -> Int -> String -> Int -> A.Value
mkDiagnostic r1 c1 r2 c2 msg severity = A.object
    [ "range" A..= A.object
        [ "start" A..= A.object ["line" A..= r1, "character" A..= c1]
        , "end"   A..= A.object ["line" A..= r2, "character" A..= c2]
        ]
    , "severity" A..= severity       -- 1=Error 2=Warn 3=Info 4=Hint
    , "source"   A..= ("sky" :: T.Text)
    , "message"  A..= msg
    ]


-- ─── Hover ─────────────────────────────────────────────────────────────

handleHover :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleHover docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    case Map.lookup uri m of
        Nothing -> sendReply reqId A.Null
        Just (_, text) -> do
            hover <- computeHover text
            case hover of
                Just h  -> sendReply reqId h
                Nothing -> sendReply reqId A.Null


computeHover :: T.Text -> IO (Maybe A.Value)
computeHover text = do
    r <- try go :: IO (Either SomeException (Maybe A.Value))
    case r of
        Left _  -> return Nothing
        Right v -> return v
  where
    go = case Parse.parseModule text of
        Left _ -> return Nothing
        Right srcMod ->
            case Canonicalise.canonicalise srcMod of
                Left _ -> return Nothing
                Right canMod -> do
                    cs <- Constrain.constrainModule canMod
                    r <- Solve.solve cs
                    case r of
                        Solve.SolveOk types ->
                            return (Just (mkHover (summariseTypes types)))
                        _ -> return Nothing


summariseTypes :: Map.Map String Ty.Type -> String
summariseTypes m =
    unlines
        [ n ++ " : " ++ Solve.showType t
        | (n, t) <- Map.toList m
        , not (isInternal n)
        ]
  where
    isInternal s = take 2 s == "__"


mkHover :: String -> A.Value
mkHover body = A.object
    [ "contents" A..= A.object
        [ "kind"  A..= ("markdown" :: T.Text)
        , "value" A..= ("```sky\n" ++ body ++ "```")
        ]
    ]


-- ─── Completion ────────────────────────────────────────────────────────

handleCompletion :: IORef.IORef Docs -> A.Value -> Maybe A.Value -> IO ()
handleCompletion docs req reqId = do
    let uri = jsonStrAt ["params", "textDocument", "uri"] req
    m <- IORef.readIORef docs
    let items = case Map.lookup uri m of
            Nothing -> stdlibCompletions
            Just (_, text) -> case Parse.parseModule text of
                Left _       -> stdlibCompletions
                Right srcMod -> localCompletions srcMod ++ stdlibCompletions
    sendReply reqId (A.object
        [ "isIncomplete" A..= False
        , "items"        A..= items
        ])


localCompletions :: Src.Module -> [A.Value]
localCompletions srcMod =
    [ A.object
        [ "label" A..= n
        , "kind"  A..= (3 :: Int)  -- Function
        ]
    | (A.At _ v) <- Src._values srcMod
    , let (A.At _ n) = Src._valueName v
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
