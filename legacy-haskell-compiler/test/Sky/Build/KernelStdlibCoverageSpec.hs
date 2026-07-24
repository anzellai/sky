module Sky.Build.KernelStdlibCoverageSpec (spec) where

-- Cycle 4 D1 regression fence — every Layer-3 stdlib `Ffi.kernel`
-- binding MUST have a matching entry in `Sky.Generate.Go.Kernel`.
--
-- Background: pre-2026-05-27 the Sky-source stdlib declared
-- `String.toList` / `String.fromList` / `String.concat` /
-- `Math.abs` / `Math.min` / `Math.max` / `Math.tan` / `Math.e` /
-- `System.getcwd` via `Ffi.kernel "<Name>"`. The compiler's
-- canonicaliser short-circuits the Sky-source body via
-- `staticKernelModules` (mapping `Sky.Core.String` → kernel
-- pseudo-module `"String"`) and `Stage-4` re-emits the call as
-- `Can.VarKernel "String" "toList"`. `kernelToGo` then walks
-- `Kernel.registry` — and if the entry is absent, the catch-all
-- emits `rt.String_toList` as a Go-symbol direct call. The runtime
-- did not export those names; `go build` rejected the program with
-- `undefined: rt.String_toList`. Validator caught it at codegen
-- but only AFTER an unreachable emission; first user encounter is
-- a confusing E4005 / E5001.
--
-- This spec walks every `Ffi.kernel "Name"` declaration in the
-- embedded stdlib source tree (`sky-stdlib/`), parses out the
-- kernel name, infers the (Mod, fn) split from the "Mod_fn"
-- convention, and asserts each pair appears in the Kernel.hs
-- registry exactly once.  A future Sky-stdlib addition that forgets
-- the Kernel.hs entry fails this spec at unit-test time, BEFORE
-- the user hits the codegen path.

import Test.Hspec
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BS8
import Data.List (isPrefixOf, sort, nub, isInfixOf)
import Data.Maybe (mapMaybe)
import System.Directory (doesDirectoryExist, listDirectory)
import System.FilePath ((</>), takeExtension)
import qualified Sky.Generate.Go.Kernel as Kernel


-- ── Walk the stdlib tree and collect every `Ffi.kernel "Name"` ──

collectSkySources :: FilePath -> IO [FilePath]
collectSkySources root = do
    exists <- doesDirectoryExist root
    if not exists
        then return []
        else go root
  where
    go d = do
        entries <- listDirectory d
        concat <$> mapM (one d) entries
    one d name = do
        let p = d </> name
        isDir <- doesDirectoryExist p
        if isDir
            then go p
            else if takeExtension name == ".sky"
                then return [p]
                else return []


-- Extract every `Ffi.kernel "Name"` from a Sky source file. The
-- formatter normalises to `Ffi.kernel "Name"` on one line; a
-- per-line scanner walks each non-comment line and pulls the
-- double-quoted name after the literal token. Lines starting with
-- `--` (Sky line comments) are skipped so doc examples like
-- ``Ffi.kernel "String_<name>"`` don't leak into the assertion.
extractKernelNames :: FilePath -> IO [String]
extractKernelNames p = do
    body <- BS8.unpack <$> BS.readFile p
    return (concatMap collectLine (lines body))
  where
    needle = "Ffi.kernel \""
    isComment ln = "--" `isPrefixOf` dropWhile (== ' ') ln
    collectLine ln
        | isComment ln = []
        | otherwise    = scan ln
    scan [] = []
    scan s
        | needle `isPrefixOf` s =
            let rest = drop (length needle) s
                (name, _) = span (/= '"') rest
            in name : scan (drop (length name + 1) rest)
        | otherwise = scan (drop 1 s)


-- Split a kernel name "Mod_fn" into (Mod, fn). The convention is
-- the FIRST underscore separates module from function; later
-- underscores belong to the function name (e.g. `String_isEmail`,
-- `Time_addMillis`).
splitKernelName :: String -> Maybe (String, String)
splitKernelName s = case break (== '_') s of
    (m, '_':fn) | not (null m) && not (null fn) -> Just (m, fn)
    _                                            -> Nothing


spec :: Spec
spec = describe "Cycle 4 D1 — every Ffi.kernel name has a Kernel.hs entry" $ do
    it "walks sky-stdlib/ for Ffi.kernel usages" $ do
        files <- collectSkySources "sky-stdlib"
        -- Sanity: the stdlib tree must exist relative to the cabal
        -- test's cwd (the repo root). If it doesn't, the test would
        -- silently pass with zero coverage — explicit assertion.
        (length files >= 30) `shouldBe` True

    it "every Ffi.kernel name resolves via Kernel.lookup" $ do
        files <- collectSkySources "sky-stdlib"
        rawNames <- concat <$> mapM extractKernelNames files
        let allNames = sort (nub rawNames)
            split = mapMaybe (\n -> fmap (\p -> (n, p)) (splitKernelName n)) allNames
            missing =
                [ (n, m, f)
                | (n, (m, f)) <- split
                , Nothing <- [Kernel.lookup m f]
                ]
        -- Useful diagnostic if it fails: show which entries are
        -- absent. hspec prints the actual value on mismatch.
        missing `shouldBe` []

    it "covers the Cycle 4 D1 high-impact entries explicitly" $ do
        -- Belt-and-braces — the per-file walk above is the load-
        -- bearing assertion, but these specific entries are the
        -- user-reported D1 regressions. A future refactor that
        -- accidentally drops one of these would still fail the
        -- broader walk, but having the names by-hand here makes
        -- the failure self-documenting.
        let d1Pairs =
                [ ("String", "toList")
                , ("String", "fromList")
                , ("String", "concat")
                , ("Math",   "abs")
                , ("Math",   "min")
                , ("Math",   "max")
                , ("Math",   "tan")
                , ("Math",   "e")
                , ("System", "getcwd")
                ]
        mapM_ (\(m, f) ->
            case Kernel.lookup m f of
                Just _  -> return ()
                Nothing -> expectationFailure
                    ("Kernel.lookup missing entry for "
                     ++ show (m, f)
                     ++ " — Cycle 4 D1 regression"))
            d1Pairs

    it "Std.Time + Sky.Core.Time functions route through known channels" $ do
        -- Std.Time uses `Ffi.callPure "Name"` (NOT `Ffi.kernel`)
        -- which routes through `rt.Ffi_callPure(...)` runtime
        -- dispatch — so missing entries surface as runtime panics
        -- via RegisterPure, not as Kernel.hs gaps. This sub-spec
        -- documents the asymmetry so a future audit doesn't add
        -- spurious Kernel.hs entries for Std.Time names.
        files <- collectSkySources "sky-stdlib"
        names <- concat <$> mapM extractKernelNames files
        -- Sky.Core.Time exposes `now / unixMillis / sleep /
        -- timeString / format / formatHTTP / formatISO8601 /
        -- formatRFC3339 / addMillis / diffMillis / every` via
        -- Ffi.kernel; each MUST have a Kernel.hs entry.
        let coreTime =
                [ "Time_now", "Time_unixMillis", "Time_sleep"
                , "Time_timeString", "Time_format", "Time_formatHTTP"
                , "Time_formatISO8601", "Time_formatRFC3339"
                , "Time_addMillis", "Time_diffMillis", "Time_every"
                ]
        mapM_ (\n ->
            (n `elem` names) `shouldBe` True) coreTime
        -- And every one resolves in Kernel.registry.
        mapM_ (\n ->
            case splitKernelName n of
                Just (m, f) ->
                    case Kernel.lookup m f of
                        Just _  -> return ()
                        Nothing -> expectationFailure
                            ("Sky.Core.Time entry " ++ show n
                             ++ " is declared via Ffi.kernel but Kernel.lookup returns Nothing")
                Nothing -> expectationFailure
                    ("Malformed kernel name: " ++ show n))
            coreTime

    it "Sky.Core.String public surface fully resolves" $ do
        -- The 33 entries advertised in CLAUDE.md stdlib reference
        -- + new D1 additions. Every one must lookup.
        let stringNames =
                [ "length", "reverse", "append", "split", "join"
                , "contains", "startsWith", "endsWith", "toInt", "fromInt"
                , "toFloat", "fromFloat", "toUpper", "toLower"
                , "trim", "trimStart", "trimEnd", "replace", "slice"
                , "isEmpty", "fromChar", "toList", "fromList", "repeat"
                , "padLeft", "padRight", "casefold", "equalFold"
                , "isEmail", "isUrl", "words", "lines", "concat"
                ]
        let missing = [ n | n <- stringNames, Nothing <- [Kernel.lookup "String" n] ]
            -- silence the "import isInfixOf" warning by lightly
            -- exercising it on the diagnostic path.
            _ = "warn" `isInfixOf` "no warn"
        missing `shouldBe` []
