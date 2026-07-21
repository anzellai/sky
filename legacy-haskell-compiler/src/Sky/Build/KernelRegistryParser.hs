-- | Source-text → [(module, name)] for the kernel-registry
-- scrape. Lives in its own module so the TH splice in
-- `Sky.Build.KernelRegistryEntries` can call it (GHC stage
-- restriction).
module Sky.Build.KernelRegistryParser
    ( parseEntries
    ) where

import qualified Data.List as List
import qualified Data.Maybe as Maybe


-- | Walk the source line-by-line; emit `(M, n)` whenever the
-- line looks like a kernel-registry arm header.
--
-- The grammar we recognise (with leading horizontal whitespace
-- of any width):
--
--     ("Sky.Core.String", "length") ->
--
-- Robust to varying indentation and comment lines in between.
parseEntries :: String -> [(String, String)]
parseEntries =
    Maybe.mapMaybe parseLine . lines


parseLine :: String -> Maybe (String, String)
parseLine ln0 =
    let ln = dropWhile (\c -> c == ' ' || c == '\t') ln0
    in case ln of
        '(' : '"' : rest ->
            case break (== '"') rest of
                (modName, '"' : ',' : afterMod) ->
                    let afterModTrim = dropWhile (\c -> c == ' ' || c == '\t') afterMod
                    in case afterModTrim of
                        '"' : rest2 ->
                            case break (== '"') rest2 of
                                (funcName, '"' : afterFunc) ->
                                    let trimmed = dropWhile (\c -> c == ' ' || c == '\t') afterFunc
                                    in if isCloseArrow trimmed
                                        then Just (modName, funcName)
                                        else Nothing
                                _ -> Nothing
                        _ -> Nothing
                _ -> Nothing
        _ -> Nothing


-- | Recognise the `) ->` (with optional intervening whitespace)
-- that closes a `("M", "f") -> ...` tuple pattern.
isCloseArrow :: String -> Bool
isCloseArrow s =
    case s of
        ')' : rest ->
            let trimmed = dropWhile (\c -> c == ' ' || c == '\t') rest
            in "->" `List.isPrefixOf` trimmed
        _ -> False
