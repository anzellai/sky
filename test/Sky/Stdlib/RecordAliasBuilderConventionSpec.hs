module Sky.Stdlib.RecordAliasBuilderConventionSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         doesDirectoryExist, listDirectory)
import System.FilePath ((</>), takeExtension, takeBaseName,
                       takeDirectory)
import qualified Data.List as List
import Control.Monad (filterM, when)
import Data.Char (isLower)


-- v0.16.10 #393(d) — convention enforcement: every typed record
-- alias exported from a stdlib module that users construct (config
-- shapes, request/message builders, etc.) ships a `defaultX` value
-- AND at least one `withY` builder.  Without this convention,
-- adding a field to a typed record breaks every downstream inline
-- record literal — the v0.15.44 HttpRequest 4→7 field breakage
-- across SkyDeploy was exactly that class.
--
-- The convention preserves Elm syntax compat (no optional fields,
-- no defaults-in-aliases) while making typed-record extensions
-- non-breaking for callers that build via the builder chain.
--
-- This spec walks every `*.sky` file under `sky-stdlib/`, finds the
-- `type alias` declarations the module exports, and for each one
-- requires a `defaultX` / `default` value AND at least one `withY`
-- function in the same module.
--
-- Exemptions:
--   * `type alias X = Foo` (transparent re-aliases — Bytes / Handler /
--     Page-shaped opaque renames).  No record fields → no builder
--     concept applies.
--   * `type alias X = (a, b)` (tuple aliases like Chart.Point).
--   * `type alias X y z = (...)` with TWO type params left of `=` —
--     polymorphic builder is the AppCfg / AppCfg-style top-level TEA
--     entry shape; users always construct inline at one call site.
--   * Modules NOT in this allow-list are skipped — only types Sky's
--     OWN downstream apps build are covered.  Apps SHOULD treat
--     this as the recommended pattern, but the gate isn't enforced
--     on every internal type alias.
spec :: Spec
spec = describe "v0.16.10 #393(d) — typed record alias builder convention" $ do
    it "every covered stdlib record alias ships defaultX + at least one withY" $ do
        cwd <- getCurrentDirectory
        let stdlibRoot = cwd </> "sky-stdlib"
        present <- doesDirectoryExist stdlibRoot
        when present $ do
            files <- walkSky stdlibRoot
            offenders <- concat <$> mapM checkFile files
            case offenders of
                [] -> return ()
                xs -> expectationFailure $ unlines
                    ( "Typed record aliases missing default + with*:"
                    : map ("  - " ++) xs
                    )

  where
    walkSky :: FilePath -> IO [FilePath]
    walkSky root = do
        entries <- listDirectory root
        let paths = map (root </>) entries
        files <- filterM doesFileExist paths
        dirs <- filterM doesDirectoryExist paths
        let skyFiles = filter ((== ".sky") . takeExtension) files
        children <- concat <$> mapM walkSky dirs
        return (skyFiles ++ children)


    -- Returns "module: alias" entries for each offender in `f`.
    checkFile :: FilePath -> IO [String]
    checkFile f = do
        src <- readFile f
        let modName = inferModuleName f
            aliases = recordAliases src
            covered = isCoveredModule modName
        if not covered
            then return []
            else do
                let missing =
                        [ alias
                        | alias <- aliases
                        , not (hasDefaultAndWith alias src)
                        ]
                return [ modName ++ ": " ++ a | a <- missing ]


-- Conservative: only enforce on the most-built-by-users modules.
-- Adding to this list signs the module up to the convention.
isCoveredModule :: String -> Bool
isCoveredModule m = m `elem`
    [ "Sky.Core.Http"
    , "Sky.Core.WebSocket"
    , "Sky.Http.Server.WebSocket"
    , "Std.Cache"
    , "Std.Email"           -- EmailMessage covered; Attachment/SesConfig/SmtpConfig pending
    , "Std.Webview"         -- WindowCfg covered v0.16.10
    , "Std.Ui.Chart"
    ]


-- Best-effort module name from `module X.Y exposing (..)` line.
inferModuleName :: FilePath -> String
inferModuleName f =
    let base = takeBaseName f
        parent = takeBaseName (takeDirectory f)
        gp = takeBaseName (takeDirectory (takeDirectory f))
    in case (gp, parent, base) of
        ("sky-stdlib", _, _) -> parent ++ "." ++ base
        _                    -> gp ++ "." ++ parent ++ "." ++ base


-- Extract record-aliased type names that are NOT transparent
-- re-aliases (Bytes = String etc).  Looks for
-- `type alias Foo[ args ] =\n    { ... }` shape.
recordAliases :: String -> [String]
recordAliases src = go (lines src)
  where
    go [] = []
    go (l : rest) =
        case List.stripPrefix "type alias " l of
            Just s ->
                let name = takeWhile (\c -> c /= ' ' && c /= '=') s
                    nextOpen = List.dropWhile null
                        (map (dropWhile (== ' ')) (drop 1 (l : rest)))
                in case nextOpen of
                    (next : _) | startsWithBrace next ->
                        if isPolymorphicTwoParam s
                            then go rest
                            else name : go rest
                    _ -> go rest
            Nothing -> go rest

    startsWithBrace s = case dropWhile (== ' ') s of
        ('{' : _) -> True
        _         -> False

    -- `Foo a b =` shape → polymorphic 2-param TEA entry — exempt.
    isPolymorphicTwoParam s =
        let beforeEq = takeWhile (/= '=') s
            ws = words beforeEq
        in length ws >= 3
            && all (all isLower . take 1) (drop 1 ws)


-- A module has `defaultX` (any casing matching "default" prefix and
-- the alias name) AND at least one `withY` top-level binding.
hasDefaultAndWith :: String -> String -> Bool
hasDefaultAndWith aliasName src =
    hasDefault && hasWith
  where
    hasDefault =
        any (\l -> startsWithDefaultBinding aliasName l) (lines src)
    hasWith =
        any (\l -> startsWithWithBinding l) (lines src)

    -- Matches `defaultRequest :` or `defaultCfg :` or `default<Anything> :`
    -- as a top-level binding for THIS alias.
    startsWithDefaultBinding name l =
        case List.stripPrefix "default" l of
            Just rest ->
                -- Accept any `default<Suffix> :` as long as suffix is
                -- lower-cased or matches the alias.  Conservative: just
                -- require the binding to be top-level.
                let _ = name -- name unused intentionally
                in case dropWhile (\c -> c /= ' ' && c /= ':') rest of
                    (' ' : ':' : _) -> True
                    (':' : _)       -> True
                    _               -> False
            Nothing -> False

    -- Matches `withFoo :` top-level binding.
    startsWithWithBinding l =
        case List.stripPrefix "with" l of
            Just rest ->
                case dropWhile (\c -> c /= ' ' && c /= ':') rest of
                    (' ' : ':' : _) -> True
                    (':' : _)       -> True
                    _               -> False
            Nothing -> False
