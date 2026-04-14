# Language Server

`sky lsp` starts the Sky Language Server over JSON-RPC on stdin/stdout. It's used by the Helix, Zed, and VS Code integrations, and any LSP-aware editor.

## Capabilities declared

From `serverCapabilities` in `src/Sky/Lsp/Server.hs`:

| Capability | Provided | Notes |
|------------|----------|-------|
| `textDocument/hover` | yes | Renders type + doc comment |
| `textDocument/definition` | yes | Jumps across module + FFI boundaries |
| `textDocument/declaration` | yes | Alias of definition |
| `textDocument/documentSymbol` | yes | Module-level + nested symbols |
| `textDocument/formatting` | yes | Delegates to `Sky.Format` |
| `textDocument/references` | yes | Finds use-sites across the project |
| `textDocument/rename` + `prepareRename` | yes | WorkspaceEdit with per-file TextEdits |
| `textDocument/signatureHelp` | yes | Parameter info while typing a call |
| `textDocument/codeAction` | yes | `quickfix` + `source.organizeImports` kinds |
| `textDocument/semanticTokens/full` | yes | Syntactic highlighting |
| `textDocument/completion` | yes | Triggered on `.` (qualified-name) |
| `workspace/symbol` | no | Use `documentSymbol` per-file |

## What gets indexed

The LSP discovers symbols from:

- Project `src/` tree (recursive `.sky`).
- Embedded Sky stdlib (`Sky.Core.*`, `Std.*`, `Sky.Live`, `Sky.Http.*`).
- `.skycache/ffi/*.kernel.json` + `.skycache/ffi/*.skyi` for FFI signatures.
- `.skydeps/<pkg>/src/` for Sky source dependencies.

The LSP **does NOT** index:

- `.skycache/go/*.go` — generated Go FFI wrappers.
- `.skycache/lowered/` — incremental cache.
- `sky-out/` — compiled output.
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

## Feature completeness matrix

| Feature | Top-level funcs | Local bindings | Imported names | ADT ctors | Record fields | FFI imports | Kernel funcs |
|---------|-----------------|----------------|----------------|-----------|---------------|-------------|--------------|
| Hover type | yes | yes | yes | yes | yes | yes | yes |
| Goto definition | yes | yes | yes | yes | partial (record field hops to type decl) | yes (to generated `.skyi`) | yes (to kernel decl in stdlib or `.skyi`) |
| References | yes | yes | yes | yes | partial | no — generated bindings are excluded from index | yes |
| Rename | yes | yes | yes, but only inside the current project (doesn't rewrite dependency code) | yes | partial | no — FFI names are generated | no — kernel names are structural |
| Completion | qualified-name after `.` | not surfaced | yes after module alias `.` | yes inside pattern | yes after `record.` | yes after FFI module alias `.` | yes after `String.`/`List.` etc. |
| Signature help | yes | yes | yes | yes | n/a | yes | yes |

## Known limitations

- **Unqualified completion is not surfaced.** Typing a bare identifier does not propose suggestions; only `.`-triggered qualified completion fires.
- **Single-project workspaces only.** Nested `sky.toml` projects under the workspace root are not recognised.
- **Rename does not touch dependencies.** Renaming a symbol exported by a Sky source dep does not rewrite `.skydeps/` (those are cloned read-only).
- **No code lens / inlay hints.** No per-line type annotations in the editor.

## Debugging

- Log location: `~/.cache/sky/lsp.log` (or `$XDG_CACHE_HOME/sky/lsp.log`).
- Environment: `SKY_LSP_DEBUG=1 sky lsp` increases verbosity.
- Trace JSON-RPC: `SKY_LSP_TRACE=1 sky lsp` prints every request/response.

## Performance

- Parse + canonicalise are on the critical path for every save.
- Type-check is incremental per module using `.skycache/lowered/` cached state.
- Whole-project cold start on the Sky compiler itself (~15k LoC Haskell): ~600 ms.
- Warm hover: < 50 ms for any symbol.
