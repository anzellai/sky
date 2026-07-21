-- | LSP serialiser for `Diagnostic` values.
--
-- Produces JSON matching the LSP `Diagnostic` shape so the language
-- server can publish via `textDocument/publishDiagnostics`.
--
-- Spec reference:
--   https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/#diagnostic
--
-- Output shape (per diagnostic):
--   {
--     "range": { "start": {"line": N, "character": N}, "end": ... },
--     "severity": 1,
--     "code": "E2001",
--     "source": "sky",
--     "message": "Type mismatch in field `update` ...",
--     "relatedInformation": [
--       {
--         "location": { "uri": "file://...", "range": {...} },
--         "message": "`n` is assigned a String here"
--       }
--     ]
--   }
--
-- LSP positions are 0-based; Sky's regions are 1-based. We convert.
module Sky.Reporting.Lsp
    ( renderLspDiagnostic
    , renderLspMany
    ) where

import qualified Data.Aeson as A
import qualified Data.Aeson.Key as Key
import qualified Sky.Reporting.Annotation as An
import Sky.Reporting.Diagnostic


-- | Serialise one Diagnostic to an LSP JSON object.
renderLspDiagnostic :: Diagnostic -> A.Value
renderLspDiagnostic d = A.object
    [ Key.fromString "range"    A..= rangeToJson (_diag_region d)
    , Key.fromString "severity" A..= severityToJson (_diag_severity d)
    , Key.fromString "code"     A..= unDiagCode (_diag_code d)
    , Key.fromString "source"   A..= ("sky" :: String)
    , Key.fromString "message"  A..= _diag_message d
    , Key.fromString "relatedInformation" A..=
        A.toJSON (map (relatedToJson) (_diag_related d))
    ]


-- | Serialise a list of diagnostics. Groups by file so the caller
-- can emit one `publishDiagnostics` notification per file. Returns
-- a JSON object mapping file path → array of diagnostics.
renderLspMany :: [Diagnostic] -> A.Value
renderLspMany diags =
    let grouped = groupByFile diags
    in A.object [ Key.fromString f A..= A.toJSON (map renderLspDiagnostic ds)
                | (f, ds) <- grouped ]


-- ─── encoding ────────────────────────────────────────────────────────

rangeToJson :: An.Region -> A.Value
rangeToJson (An.Region s e) = A.object
    [ Key.fromString "start" A..= positionToJson s
    , Key.fromString "end"   A..= positionToJson e
    ]


positionToJson :: An.Position -> A.Value
positionToJson (An.Position l c) = A.object
    -- LSP is 0-based; Sky is 1-based. Saturate at 0 to avoid
    -- negative coordinates if a region is synthetic.
    [ Key.fromString "line"      A..= max 0 (l - 1)
    , Key.fromString "character" A..= max 0 (c - 1)
    ]


-- | LSP severity: 1 = Error, 2 = Warning, 3 = Info, 4 = Hint.
severityToJson :: Severity -> Int
severityToJson SevError   = 1
severityToJson SevWarning = 2
severityToJson SevInfo    = 3
severityToJson SevHint    = 4


relatedToJson :: RelatedRegion -> A.Value
relatedToJson r = A.object
    [ Key.fromString "location" A..= A.object
        [ Key.fromString "uri"   A..= ("file://" ++ _rel_file r)
        , Key.fromString "range" A..= rangeToJson (_rel_region r)
        ]
    , Key.fromString "message" A..= _rel_message r
    ]


-- ─── helpers ─────────────────────────────────────────────────────────

groupByFile :: [Diagnostic] -> [(FilePath, [Diagnostic])]
groupByFile diags =
    let files = uniqOrdered (map _diag_file diags)
    in [(f, [d | d <- diags, _diag_file d == f]) | f <- files]
  where
    uniqOrdered = foldr (\x acc -> if x `elem` acc then acc else x : acc) []
