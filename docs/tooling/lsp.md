# Language Server

`sky lsp` starts the Sky Language Server over JSON-RPC on stdin/stdout. It's used by the Helix, Zed, and VS Code integrations, and any LSP-aware editor.

## Features

- **Diagnostics** — parse, canonicalisation, type-check, and exhaustiveness errors surfaced as they happen.
- **Hover** — inferred types for any identifier, including qualified cross-module references.
- **Go-to-definition** — jumps across module + FFI boundaries.
- **Completions** — currently qualified-name completion only; unqualified/symbol-based completion is planned.
- **Formatting** — delegates to `Sky.Format` for elm-format-style output.
- **Symbols** — document symbols for the current file; workspace symbols from the project root.

## What it does NOT index

- `.skycache/` — generated FFI wrappers. The LSP reads `.skycache/ffi/*.kernel.json` + `*.skyi` for type signatures, but does not load `.skycache/go/*.go` as source.
- `.skydeps/` — Sky-source deps are indexed as modules, but only when directly imported.
- `sky-out/` — compiled output. Never indexed.
- `dist-newstyle/`, `node_modules/`, `legacy-*/`, `bootstrap/` — hard-coded skips.

## Editor configuration

### Helix

`~/.config/helix/languages.toml`:

```toml
[[language]]
name = "sky"
scope = "source.sky"
file-types = ["sky"]
indent = { tab-width = 4, unit = "    " }
auto-format = true
formatter = { command = "sky", args = ["fmt"] }
language-servers = ["sky-lsp"]

[language-server.sky-lsp]
command = "sky"
args = ["lsp"]
```

### Zed

`.zed/config.json`:

```json
{
  "languages": {
    "Sky": {
      "language_servers": ["sky-lsp"],
      "formatter": { "external": { "command": "sky", "arguments": ["fmt"] } }
    }
  },
  "lsp": {
    "sky-lsp": {
      "binary": { "path": "sky", "arguments": ["lsp"] }
    }
  }
}
```

### VS Code

No official extension yet. The LSP is standards-compliant so any generic LSP client extension (e.g. "LSP Language Client") works.

## Debugging

- Log location: `~/.cache/sky/lsp.log` (or `$XDG_CACHE_HOME/sky/lsp.log`).
- Environment: `SKY_LSP_DEBUG=1 sky lsp` increases verbosity.
- Trace JSON-RPC: `SKY_LSP_TRACE=1 sky lsp` prints every request/response.

## Performance

- Parse + canonicalise are on the critical path for every save.
- Type-check is incremental per module using `.skycache/lowered/` cached state.
- Whole-project cold start on the Sky compiler itself (~15k LoC Haskell): ~600ms.
- Warm hover: < 50ms for any symbol.

## Limitations

- **Single-project workspaces only.** Nested `sky.toml`s are not supported.
- **No refactoring.** Rename, extract, inline, etc. are not implemented.
- **No code lens.** No inline type annotations.
- **No snippet expansion.** Use your editor's built-in snippet system.
