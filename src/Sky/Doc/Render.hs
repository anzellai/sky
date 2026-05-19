{-# LANGUAGE OverloadedStrings #-}
-- | Sky.Doc.Render — emit a static HTML + JSON site for the doc
-- index. Run by `sky doc --serve` (after which the bundled Sky.
-- Http.Server app serves the dir at the chosen port) or by
-- `sky doc --export <dir>` for offline use.
--
-- Design choices:
--   * Pure-Haskell string assembly (no template engine).  Tag
--     soup, not a markup combinator library.  Page count is
--     small + render-time is negligible (<100ms for a typical
--     project's catalog).
--   * Client-side fuzzy search in the browser.  We emit a single
--     JSON catalog (/api/symbols.json) + a 200-LOC vanilla-JS
--     filter that scores by exact / prefix / substring + module
--     locality.  No build step, no framework.
--   * Stable URLs: `/`, `/m/<Module>` (one HTML file per
--     module), `/api/symbols.json`.  Anchors per symbol.
module Sky.Doc.Render
    ( renderToDir
    ) where

import qualified Data.Aeson as J
import qualified Data.ByteString.Lazy as BL
import qualified Data.List as List
import           System.Directory (createDirectoryIfMissing)
import           System.FilePath ((</>))

import           Sky.Doc.Index


-- | Render the entire site into the given directory. Creates
-- subdirs as needed. Idempotent — safe to re-run.
--
-- CSS + JS are inlined into every HTML page rather than served as
-- separate /doc.css / /doc.js endpoints. This sidesteps a
-- Sky.Http.Server limitation where `Server.text` overrides
-- Content-Type to text/plain — browsers' strict MIME checking
-- then refuses to apply CSS / execute JS. Inline `<style>` +
-- `<script>` blocks bypass the issue entirely. Pages stay small
-- (CSS + JS together are ~6 KB).
renderToDir :: FilePath -> DocIndex -> IO ()
renderToDir outDir idx = do
    createDirectoryIfMissing True outDir
    createDirectoryIfMissing True (outDir </> "m")
    createDirectoryIfMissing True (outDir </> "api")
    -- Index page (with embedded module list).
    writeFile (outDir </> "index.html") (renderIndexPage idx)
    -- Per-module pages.
    mapM_ (renderModulePage outDir) (allModules idx)
    -- JSON catalog for client-side search.
    BL.writeFile (outDir </> "api" </> "symbols.json")
        (J.encode (toSearchCatalog idx))


allModules :: DocIndex -> [DocModule]
allModules idx = diProject idx ++ diDeps idx ++ diStdlib idx


-- ─── Search catalog (flat JSON used by client-side filter) ─────

-- | Flat catalog: every symbol gets one entry. Client-side JS
-- ranks matches by (exact-prefix > camelCase-boundary > substring)
-- × module-locality.
toSearchCatalog :: DocIndex -> J.Value
toSearchCatalog idx =
    J.object
        [ ("entries", J.toJSON
            (concatMap (modEntries Project) (diProject idx)
             ++ concatMap (modEntries Deps)   (diDeps    idx)
             ++ concatMap (modEntries Stdlib) (diStdlib  idx)))
        , ("modules", J.toJSON
            (map dmName (diProject idx)
             ++ map dmName (diDeps    idx)
             ++ map dmName (diStdlib  idx)))
        ]
  where
    modEntries bucket m =
        [ J.object
            [ ("module", J.toJSON (dmName m))
            , ("name",   J.toJSON (dsName s))
            , ("kind",   J.toJSON (kindLabel (dsKind s)))
            , ("sig",    J.toJSON (dsTypeSig s))
            , ("doc",    J.toJSON (dsDoc s))
            , ("bucket", J.toJSON (bucketLabel bucket))
            ]
        | s <- dmSymbols m
        ]
    kindLabel KindFunction = "function" :: String
    kindLabel KindCtor     = "ctor"
    kindLabel KindType     = "type"


data Bucket = Project | Deps | Stdlib

bucketLabel :: Bucket -> String
bucketLabel Project = "project"
bucketLabel Deps    = "deps"
bucketLabel Stdlib  = "stdlib"


-- ─── HTML rendering ────────────────────────────────────────────

renderIndexPage :: DocIndex -> String
renderIndexPage idx = htmlShell "Sky docs" $ concat
    [ "<header>"
    , "  <h1>Sky <span class='dim'>docs</span></h1>"
    , "  <div class='meta'>"
    , "    <span>" ++ esc (diRoot idx) ++ "</span>"
    , "    <span class='dim'>sky " ++ esc (diVersion idx) ++ "</span>"
    , "  </div>"
    , "</header>"
    , "<div class='search'>"
    , "  <input id='q' type='search' placeholder='Search modules + names …' autofocus />"
    , "  <div id='hits'></div>"
    , "</div>"
    , "<main id='index'>"
    , renderBucket "Project"     (diProject idx)
    , renderBucket "Dependencies" (diDeps    idx)
    , renderBucket "Stdlib"      (diStdlib  idx)
    , "</main>"
    ]


renderBucket :: String -> [DocModule] -> String
renderBucket _    []      = ""
renderBucket name modules = concat
    [ "<section class='bucket'>"
    , "  <h2>" ++ esc name ++ " <span class='dim'>("
                 ++ show (length modules) ++ ")</span></h2>"
    , "  <ul class='mod-list'>"
    , concatMap modLink modules
    , "  </ul>"
    , "</section>"
    ]
  where
    modLink m = concat
        [ "<li>"
        , "<a href='/m/" ++ esc (dmName m) ++ "'>"
        , esc (dmName m)
        , "</a>"
        , " <span class='dim sym-count'>"
                 ++ show (length (dmSymbols m)) ++ "</span>"
        , "</li>"
        ]


renderModulePage :: FilePath -> DocModule -> IO ()
renderModulePage outDir m = do
    let path = outDir </> "m" </> (dmName m ++ ".html")
    writeFile path body
  where
    body = htmlShell (dmName m ++ " — Sky docs") $ concat
        [ "<header>"
        , "  <a class='back' href='/'>← all modules</a>"
        , "  <h1>" ++ esc (dmName m) ++ "</h1>"
        , case dmDoc m of
            Just d  -> "  <p class='mod-doc'>" ++ esc d ++ "</p>"
            Nothing -> ""
        , "  <p class='source'>Source: <code>" ++ esc (dmFile m) ++ "</code></p>"
        , "</header>"
        , "<main class='module'>"
        , renderSymGroup "Types" (List.filter (\s -> dsKind s == KindType
                                                  || dsKind s == KindCtor)
                                              (dmSymbols m))
        , renderSymGroup "Functions" (List.filter ((== KindFunction) . dsKind)
                                                  (dmSymbols m))
        , "</main>"
        ]


renderSymGroup :: String -> [DocSymbol] -> String
renderSymGroup _    []   = ""
renderSymGroup hdr  syms = concat
    [ "<section class='group'>"
    , "  <h2>" ++ esc hdr ++ "</h2>"
    , concatMap renderSym syms
    , "</section>"
    ]


renderSym :: DocSymbol -> String
renderSym s = concat
    [ "<article class='sym' id='" ++ esc (dsName s) ++ "'>"
    , "  <h3><a class='anchor' href='#" ++ esc (dsName s) ++ "'>#</a>"
    , esc (dsName s) ++ "</h3>"
    , "  <pre class='sig'>" ++ esc (formatSig s) ++ "</pre>"
    , case dsDoc s of
        Just d  -> "  <p class='doc'>" ++ esc d ++ "</p>"
        Nothing -> ""
    , "</article>"
    ]


-- | Build the `name : Type` line shown for each symbol.  The
-- LSP's symTypeSig sometimes already prefixes the name (top-level
-- functions) and sometimes just gives the type (ctors).
-- Normalise both shapes.
formatSig :: DocSymbol -> String
formatSig s = case dsTypeSig s of
    Just sig
        | (dsName s ++ " :") `List.isPrefixOf` sig -> sig
        | dsName s `List.isPrefixOf` sig           -> sig
        | otherwise                                -> dsName s ++ " : " ++ sig
    Nothing -> dsName s


-- ─── Page shell + escaping ──────────────────────────────────────

htmlShell :: String -> String -> String
htmlShell title body = concat
    [ "<!doctype html>"
    , "<html lang='en'><head>"
    , "<meta charset='utf-8'/>"
    , "<meta name='viewport' content='width=device-width,initial-scale=1'/>"
    , "<title>" ++ esc title ++ "</title>"
    , "<style>" ++ docCSS ++ "</style>"
    , "</head><body>"
    , body
    , "<script>" ++ docJS ++ "</script>"
    , "</body></html>"
    ]


esc :: String -> String
esc = concatMap one
  where
    one '<'  = "&lt;"
    one '>'  = "&gt;"
    one '&'  = "&amp;"
    one '"'  = "&quot;"
    one '\'' = "&#39;"
    one c    = [c]


-- ─── Static assets (CSS + JS) ───────────────────────────────────

docCSS :: String
docCSS = unlines
    [ ":root { --bg:#0e0f12; --fg:#e5e7eb; --dim:#9ca3af; --accent:#60a5fa; --bg-2:#16181d; --border:#2a2d34; }"
    , "* { box-sizing: border-box; margin: 0; padding: 0; }"
    , "body { background: var(--bg); color: var(--fg); font: 14px/1.5 system-ui, -apple-system, sans-serif; padding: 24px; max-width: 1100px; margin: 0 auto; }"
    , "header { padding-bottom: 16px; border-bottom: 1px solid var(--border); margin-bottom: 16px; }"
    , "h1 { font-size: 24px; font-weight: 600; margin-bottom: 6px; }"
    , "h2 { font-size: 16px; font-weight: 600; margin: 16px 0 8px; color: var(--dim); text-transform: uppercase; letter-spacing: .05em; font-size: 11px; }"
    , "h3 { font-size: 14px; font-weight: 600; display: inline-flex; align-items: center; gap: 6px; }"
    , ".dim { color: var(--dim); }"
    , ".meta { color: var(--dim); font-size: 12px; display: flex; gap: 12px; }"
    , ".search { margin-bottom: 24px; position: relative; }"
    , ".search input { width: 100%; padding: 10px 14px; background: var(--bg-2); color: var(--fg); border: 1px solid var(--border); border-radius: 6px; font: inherit; }"
    , ".search input:focus { outline: none; border-color: var(--accent); }"
    , "#hits { position: absolute; left: 0; right: 0; top: 100%; margin-top: 4px; max-height: 400px; overflow-y: auto; background: var(--bg-2); border: 1px solid var(--border); border-radius: 6px; display: none; z-index: 10; }"
    , "#hits.show { display: block; }"
    , "#hits a { display: flex; gap: 12px; padding: 6px 12px; color: inherit; text-decoration: none; }"
    , "#hits a:hover, #hits a.sel { background: rgba(96,165,250,.1); }"
    , "#hits .name { font-weight: 500; }"
    , "#hits .mod { color: var(--dim); font-size: 12px; }"
    , "#hits .sig { color: var(--dim); font-family: ui-monospace, Menlo, monospace; font-size: 12px; margin-left: auto; }"
    , ".bucket { margin: 24px 0; }"
    , ".mod-list { list-style: none; display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 4px 16px; }"
    , ".mod-list a { color: var(--accent); text-decoration: none; }"
    , ".mod-list a:hover { text-decoration: underline; }"
    , ".sym-count { font-size: 11px; }"
    , ".back { color: var(--dim); text-decoration: none; font-size: 12px; }"
    , ".back:hover { color: var(--accent); }"
    , ".mod-doc { color: var(--dim); margin: 8px 0; }"
    , ".source { font-size: 11px; color: var(--dim); margin-top: 4px; }"
    , ".source code { font-family: ui-monospace, Menlo, monospace; }"
    , ".group { margin: 24px 0; }"
    , ".sym { padding: 12px 0; border-top: 1px solid var(--border); }"
    , ".sym:first-of-type { border-top: 0; }"
    , ".anchor { color: var(--dim); text-decoration: none; }"
    , ".sym:hover .anchor { color: var(--accent); }"
    , ".sig { font-family: ui-monospace, Menlo, monospace; font-size: 13px; padding: 8px 12px; background: var(--bg-2); border-radius: 4px; margin: 6px 0; overflow-x: auto; }"
    , ".doc { color: var(--dim); margin-top: 4px; white-space: pre-wrap; }"
    ]


docJS :: String
docJS = unlines
    [ "(async () => {"
    , "  const r = await fetch('/api/symbols.json');"
    , "  const data = await r.json();"
    , "  const q = document.getElementById('q');"
    , "  const hits = document.getElementById('hits');"
    , "  if (!q || !hits) return;"
    , "  let sel = -1;"
    , "  const score = (entry, query) => {"
    , "    const lq = query.toLowerCase();"
    , "    const n = entry.name.toLowerCase();"
    , "    const m = entry.module.toLowerCase();"
    , "    const full = (m + '.' + n);"
    , "    let s = 0;"
    , "    if (n === lq) s += 1000;"
    , "    else if (n.startsWith(lq)) s += 500;"
    , "    else if (m.endsWith('.' + lq) || m === lq) s += 400;"
    , "    else if (n.includes(lq)) s += 200;"
    , "    else if (full.includes(lq)) s += 100;"
    , "    else if (m.includes(lq)) s += 80;"
    , "    else return 0;"
    , "    if (entry.bucket === 'project') s += 50;"
    , "    else if (entry.bucket === 'deps') s += 20;"
    , "    return s;"
    , "  };"
    , "  const render = (results) => {"
    , "    if (results.length === 0) { hits.classList.remove('show'); hits.innerHTML = ''; return; }"
    , "    hits.classList.add('show');"
    , "    hits.innerHTML = results.slice(0, 40).map((e, i) =>"
    , "      `<a class='${i===sel?\"sel\":\"\"}' href='/m/${encodeURIComponent(e.module)}#${encodeURIComponent(e.name)}'>"
    , "         <span class='name'>${e.name}</span>"
    , "         <span class='mod'>${e.module}</span>"
    , "         <span class='sig'>${(e.sig || '').replace(/</g,'&lt;')}</span>"
    , "       </a>`).join('');"
    , "  };"
    , "  q.addEventListener('input', () => {"
    , "    const v = q.value.trim();"
    , "    if (!v) { hits.classList.remove('show'); hits.innerHTML = ''; sel = -1; return; }"
    , "    const ranked = data.entries.map(e => [score(e, v), e]).filter(p => p[0] > 0).sort((a,b) => b[0]-a[0]).map(p => p[1]);"
    , "    sel = -1;"
    , "    render(ranked);"
    , "  });"
    , "  q.addEventListener('keydown', (e) => {"
    , "    const items = hits.querySelectorAll('a');"
    , "    if (e.key === 'ArrowDown') { sel = Math.min(sel + 1, items.length - 1); items.forEach((a,i) => a.classList.toggle('sel', i === sel)); e.preventDefault(); }"
    , "    else if (e.key === 'ArrowUp') { sel = Math.max(sel - 1, 0); items.forEach((a,i) => a.classList.toggle('sel', i === sel)); e.preventDefault(); }"
    , "    else if (e.key === 'Enter' && sel >= 0) { items[sel].click(); }"
    , "    else if (e.key === 'Escape') { hits.classList.remove('show'); q.blur(); }"
    , "  });"
    , "})();"
    ]
