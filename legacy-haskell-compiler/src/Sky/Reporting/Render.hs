-- | CLI renderer for `Diagnostic` values.
--
-- Produces Elm-style error output with source-context snippet, caret,
-- code, severity badge, and related regions / hints. The format is
-- DESIGNED to be readable in a terminal AND machine-parseable
-- (severity tag + code on the header line lets CI/grep scripts find
-- specific error classes).
--
-- Output shape:
--
--   -- ERROR ────────────────────────── src/Main.sky:47:9 [E2001]
--
--      45 | main =
--      46 |     Live.app
--      47 |         { init = init
--         |         ^
--      48 |         , update = update
--
--   Type mismatch in field `update` → return → param 1 → field `n`:
--      expected: Int
--      actual:   String
--
--   Related at src/Main.sky:42:38:
--      42 |     ( { model | n = String.fromInt ... }, Cmd.none )
--         |                                     ^
--      → `n` is assigned a String here
--
--   Hint: remove the `String.fromInt` if `n` should stay an Int.
--
-- See `docs/v013-diagnostics-architecture.md` for the design rationale.
module Sky.Reporting.Render
    ( renderCli
    , renderCliMany
    ) where

import qualified Sky.Reporting.Annotation as A
import Sky.Reporting.Diagnostic
import System.Directory (doesFileExist)


-- | Render a single diagnostic as a multi-line string suitable for
-- stdout/stderr printing. Reads the source file to inject context.
renderCli :: Diagnostic -> IO String
renderCli diag = do
    header <- pure (renderHeader diag)
    snippet <- renderSnippet (_diag_file diag) (_diag_region diag)
    let body = renderBody diag
    related <- mapM renderRelated (_diag_related diag)
    let hints = renderHints (_diag_hints diag)
    return $ unlines $
        [ header
        , ""
        ] ++ snippet ++
        [ ""
        , body
        ] ++ (if null related then [] else "" : concat related)
          ++ (if null hints then [] else "" : hints)


-- | Render a list of diagnostics. Stable order (by file, region,
-- severity). Each separated by a blank line.
renderCliMany :: [Diagnostic] -> IO String
renderCliMany [] = return ""
renderCliMany diags = do
    let sorted = sortDiagnostics diags
    rendered <- mapM renderCli sorted
    return $ unlines (interleave "" rendered)
  where
    interleave _ [] = []
    interleave _ [x] = [x]
    interleave sep (x:xs) = x : sep : interleave sep xs


-- ─── pieces ─────────────────────────────────────────────────────────

renderHeader :: Diagnostic -> String
renderHeader d =
    let sev = severityBadge (_diag_severity d) (_diag_category d)
        loc = _diag_file d ++ ":" ++ showRegionStart (_diag_region d)
        code = "[" ++ unDiagCode (_diag_code d) ++ "]"
        sep = replicate (max 1 (74 - length sev - length loc - length code - 4)) '─'
    in "-- " ++ sev ++ " " ++ sep ++ " " ++ loc ++ " " ++ code


-- | Severity badge with category prefix.  Matches the historical
-- Sky/Elm convention of "TYPE ERROR", "PARSE ERROR", "NAMING ERROR"
-- etc. so CI / grep tools that key off the existing wording keep
-- matching.  Non-error severities use bare "WARNING" / "INFO" /
-- "HINT" — category prefix would be noise there.
severityBadge :: Severity -> Category -> String
severityBadge SevError   cat = categoryPrefix cat ++ "ERROR"
severityBadge SevWarning _   = "WARNING"
severityBadge SevInfo    _   = "INFO"
severityBadge SevHint    _   = "HINT"


categoryPrefix :: Category -> String
categoryPrefix CatParse          = "PARSE "
categoryPrefix CatCanonical      = "NAMING "
categoryPrefix CatType           = "TYPE "
categoryPrefix CatExhaustiveness = "EXHAUSTIVENESS "
categoryPrefix CatCodegen        = "CODEGEN "
categoryPrefix CatGoBuild        = "GO BUILD "
categoryPrefix CatRuntime        = "RUNTIME "


showRegionStart :: A.Region -> String
showRegionStart (A.Region (A.Position l c) _) = show l ++ ":" ++ show c


-- | Read source file + emit numbered lines around the region, with
-- a caret on the offending column. 2 lines before + the line + 1 line
-- after. Silent (returns empty list) if file unreadable.
renderSnippet :: FilePath -> A.Region -> IO [String]
renderSnippet path region = do
    exists <- doesFileExist path
    if not exists then return [] else do
        src <- readFile path
        let allLines = lines src
            lineN = A._line (A._start region)
            colN  = A._col  (A._start region)
            totalLines = length allLines
        if lineN < 1 || lineN > totalLines then return [] else do
            let startLine   = max 1 (lineN - 2)
                endLine     = min totalLines (lineN + 1)
                contextLines = take (endLine - startLine + 1)
                                    (drop (startLine - 1) allLines)
                gutterW     = length (show endLine)
                padNum n    = replicate (gutterW - length (show n)) ' ' ++ show n
                caretLine   = "   " ++ replicate gutterW ' ' ++ " | "
                                    ++ replicate (colN - 1) ' ' ++ "^"
                renderOne (n, l)
                    | n == lineN = [ "   " ++ padNum n ++ " | " ++ l
                                   , caretLine ]
                    | otherwise  = [ "   " ++ padNum n ++ " | " ++ l ]
            return (concatMap renderOne (zip [startLine..] contextLines))


renderBody :: Diagnostic -> String
renderBody = _diag_message


renderRelated :: RelatedRegion -> IO [String]
renderRelated rel = do
    let header = "Related at " ++ _rel_file rel ++ ":"
              ++ showRegionStart (_rel_region rel) ++ ":"
    snippet <- renderSnippet (_rel_file rel) (_rel_region rel)
    let arrow = "   → " ++ _rel_message rel
    return $ [header, ""] ++ snippet ++ ["", arrow]


renderHints :: [Hint] -> [String]
renderHints = concatMap renderHint
  where
    renderHint (Hint msg Nothing) = ["Hint: " ++ msg]
    renderHint (Hint msg (Just fix)) =
        [ "Hint: " ++ msg
        , "   Try: " ++ _fix_description fix
        ]
