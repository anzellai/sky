-- | Sky.Doc.Terminal — pretty-print a module's exposed surface to
-- stdout. Like `go doc fmt`. The user runs `sky doc Std.Money`
-- from a project root and gets a Quick view of every public
-- name + signature + doc comment.
module Sky.Doc.Terminal
    ( printIndexSummary
    , printModule
    , printAllModules
    ) where

import qualified Data.List as List
import           System.IO (hPutStrLn, stderr)

import           Sky.Doc.Index


-- | One-line summary printed when the user runs `sky doc` with
-- no arguments. Lists the module groupings + counts.
printIndexSummary :: DocIndex -> IO ()
printIndexSummary idx = do
    let nProject = length (diProject idx)
        nDeps    = length (diDeps idx)
        nStdlib  = length (diStdlib idx)
    putStrLn $ "sky doc — index for " ++ diRoot idx
    putStrLn ""
    putStrLn $ "  project: " ++ show nProject ++ " modules"
    putStrLn $ "  deps:    " ++ show nDeps    ++ " modules"
    putStrLn $ "  stdlib:  " ++ show nStdlib  ++ " modules"
    putStrLn ""
    putStrLn "Usage:"
    putStrLn "  sky doc <Module>            print one module's surface"
    putStrLn "  sky doc --list              list every module name"
    putStrLn "  sky doc --serve [--port N]  open the browsable doc server"


-- | List every indexed module name, grouped by bucket. Useful
-- when the user doesn't remember the exact name to pass to
-- `sky doc <Module>`.
printAllModules :: DocIndex -> IO ()
printAllModules idx = do
    section "project" (diProject idx)
    section "deps"    (diDeps idx)
    section "stdlib"  (diStdlib idx)
  where
    section label modules
        | null modules = return ()
        | otherwise = do
            putStrLn $ "── " ++ label ++ " ──"
            mapM_ (\m -> putStrLn ("  " ++ dmName m)) modules
            putStrLn ""


-- | Print one module by name. Falls back to a "did you mean"
-- list on no-match.
printModule :: DocIndex -> String -> IO ()
printModule idx target = do
    let allMods = diProject idx ++ diDeps idx ++ diStdlib idx
    case List.find (\m -> dmName m == target) allMods of
        Just m -> renderModule m
        Nothing -> do
            hPutStrLn stderr $ "sky doc: no module named '" ++ target ++ "'"
            let suggestions = take 5 $ filter (caseInsensitiveContains target)
                                              (map dmName allMods)
            case suggestions of
                [] -> return ()
                xs -> do
                    hPutStrLn stderr "Did you mean:"
                    mapM_ (\n -> hPutStrLn stderr ("  " ++ n)) xs


caseInsensitiveContains :: String -> String -> Bool
caseInsensitiveContains needle haystack =
    map toLowerC needle `List.isInfixOf` map toLowerC haystack
  where
    toLowerC c
        | c >= 'A' && c <= 'Z' = toEnum (fromEnum c + 32)
        | otherwise = c


renderModule :: DocModule -> IO ()
renderModule m = do
    putStrLn $ "module " ++ dmName m
    case dmDoc m of
        Just d  -> putStrLn $ indent 2 d
        Nothing -> return ()
    putStrLn ""
    let (types, fns) = List.partition (\s -> dsKind s == KindType
                                          || dsKind s == KindCtor)
                                      (dmSymbols m)
    renderGroup "Types"     types
    renderGroup "Functions" fns
    putStrLn ""
    putStrLn $ "Source: " ++ dmFile m


renderGroup :: String -> [DocSymbol] -> IO ()
renderGroup _    []   = return ()
renderGroup hdr  syms = do
    putStrLn $ "── " ++ hdr ++ " ──"
    mapM_ renderSymbol syms
    putStrLn ""


renderSymbol :: DocSymbol -> IO ()
renderSymbol s = do
    -- Prefix the signature with the symbol name (LSP's symTypeSig
    -- is the result-type-only form for ctors like `USD : Currency`,
    -- and the full `name : Type` form for functions). Normalise
    -- so both appear as `name : sig`.
    let line = case dsTypeSig s of
            Just sig
                | (dsName s ++ " :") `List.isPrefixOf` sig
                    -> sig                          -- already prefixed
                | dsName s `List.isPrefixOf` sig
                    -> sig                          -- bare "Name args = ..."
                | otherwise
                    -> dsName s ++ " : " ++ sig     -- prefix it
            Nothing -> dsName s
    putStrLn $ "  " ++ line
    case dsDoc s of
        Just d  -> putStrLn $ indent 6 d
        Nothing -> return ()
    putStrLn ""


-- | Indent every line in a string by N spaces.
indent :: Int -> String -> String
indent n s =
    let pad = replicate n ' '
    in List.intercalate "\n" (map (pad ++) (lines s))
