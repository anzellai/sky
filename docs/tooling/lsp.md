# Language Server

`sky lsp` starts the Sky Language Server over JSON-RPC on stdin/stdout.
It's used by the Helix, Zed, and VS Code integrations, and any
LSP-aware editor.

The server runs **inline** — `sky lsp` is served by the single `sky`
binary, so there is no separate `sky-lsp` process to install or locate.
Point your editor at `command = "sky", args = ["lsp"]` and make sure
`sky` is on the editor's `PATH` (GUI editors often don't inherit your
shell `PATH` — use an absolute path or launch the editor from a shell
if hover/completion don't appear). The stdlib is resolved from the
compiler's embedded copy, so hover, completion, and go-to work in any
project — not only inside the compiler repo.

**LSP contract**: every USED symbol class has hover + goto-definition
coverage. The gate asserts an exact case count — `LSP_EXPECTED: u64 = 49`
(`rust/crates/xtask/src/harness/bodies.rs:2758`), enforced at `:2902` so a
case that silently stops running fails the build. `docs/rust-rewrite/11`
decomposes that as 17 symbol-class + 32 corpus.

> **This heading said "(100 % coverage)" and listed a total of 20 (17 + 3),
> which disagrees with the 49 the gate enforces.** The "100 %" figure has no
> derivation behind it — there is no denominator anywhere — so it is dropped
> rather than restated. The 17 below are the symbol-class half; take the
> total from `LSP_EXPECTED`, not from this page.

The 17 symbol-class cases run via the headless Neovim gate driver
(`scripts/lsp-test-nvim.{lua,sh}`):

- hover-task-run, hover-field, hover-type-name
- hover-function-use, hover-ctor-use, hover-lambda-param,
  hover-case-pattern, hover-kernel-call
- goto-def-type-name, goto-def-function, goto-def-ctor,
  goto-def-let-binding, goto-def-lambda-param, goto-def-field
- completion-qualified-insert-text, completion-field,
  completion-let-binding

Plus 3 huge-FFI tests against `examples/13-skyshop` (Stripe SDK +
Firebase) via `scripts/lsp-test-skyshop.lua`. The driver runs via the
`scripts/lsp-test-nvim.sh` gate (alongside `cargo test`); skipped if
`nvim` isn't on PATH (so CI environments without headless Neovim
setup stay green).

## Capabilities declared

From `serverCapabilities` in the Rust LSP crate (`rust/crates/sky-lsp`):

| Capability | Provided | Notes |
|------------|----------|-------|
| `textDocument/hover` | yes | Renders type + doc comment |
| `textDocument/definition` | yes | Jumps across module + FFI boundaries |
| `textDocument/declaration` | yes | Alias of definition |
| `textDocument/documentSymbol` | yes | Module-level + nested symbols |
| `textDocument/formatting` | yes | Delegates to `Sky.Format` |
| `textDocument/references` | yes | Finds use-sites across the project |
| `textDocument/rename` + `prepareRename` | yes | WorkspaceEdit with per-file TextEdits |
| `textDocument/signatureHelp` | yes | Parameter info while typing a call (v0.15.48+: per-parameter `[startOffset, endOffset]` ranges so editors highlight the active argument in-place) |
| `textDocument/codeAction` | yes | `quickfix` + `source.organizeImports` kinds |
| `textDocument/semanticTokens/full` | yes | Syntactic highlighting |
| `textDocument/completion` | yes | Triggered on `.` (qualified-name) |
| `workspace/symbol` | **yes** | Project-wide symbol search. This row said "no — use `documentSymbol` per-file"; the server advertises `workspace_symbol_provider: Some(OneOf::Left(true))` (`rust/crates/sky-lsp/src/server.rs:86`), handles it at `:243`, and has a dedicated test (`sky-lsp/tests/workspace_symbol.rs`) |

## What gets indexed

The LSP discovers symbols from:

- Project `src/` tree (recursive `.sky`).
- Embedded Sky stdlib (`Sky.Core.*`, `Std.*`, `Sky.Live`, `Sky.Http.*`).
- `.skycache/ffi/*.kernel.json` + `.skycache/ffi/*.skyi` for FFI signatures.
- `.skydeps/<pkg>/src/` for Sky source dependencies.

The LSP **does NOT** index:

- `.skycache/go/*.go` — generated Go FFI wrappers.
- `.skycache/lowered/` — named in the watcher's exclude list. **Note it is not a
  real cache**: `grep -rn 'lowered' rust/crates --include='*.rs'` finds the
  string exactly once, in a `is_watched_change_excludes_generated_dirs` test
  fixture (`rust/crates/sky/src/main.rs:5028`). Nothing reads or writes that
  directory.
- `sky-out/` — compiled output.
- `target/`, `node_modules/`, `legacy-*/`, `bootstrap/` — hard-coded skips.

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
formatter = { command = "sky", args = ["fmt", "--stdin" ] }
comment-tokens = "--"
# Ideally block-comment-tokens should be '{ start = "{-\n", end = "\n-}"}',
# but `sky fmt` deletes the code when newlines are included.
# TODO: fix `sky fmt` command?
block-comment-tokens = { start = "{-", end = "-}"} 
language-servers = ["sky-lsp"]

[language-server.sky-lsp]
command = "sky"
args = ["lsp"]

[[grammar]]
name = "sky"
source = { git = "https://github.com/anzellai/tree-sitter-sky", rev = "main" }
```

Then fetch and build:

```bash
hx --grammar fetch
hx --grammar build
```

Finally copy some needed files:

```bash
curl --create-dirs --output-dir ~/.config/helix/runtime/queries/sky \
  -O https://raw.githubusercontent.com/anzellai/tree-sitter-sky/refs/heads/main/queries/highlights.scm \
  -O https://raw.githubusercontent.com/anzellai/tree-sitter-sky/refs/heads/main/queries/locals.scm \
  -O https://raw.githubusercontent.com/anzellai/tree-sitter-sky/refs/heads/main/queries/tags.scm

```

### Zed

Zed can't register a brand-new language from project settings alone — it needs
an **extension**. Use the community Sky extension, which wires the
`tree-sitter-sky` grammar for highlighting and runs `sky lsp` for hover /
completion / goto / rename / format:

**[github.com/TheGB0077/sky-zed](https://github.com/TheGB0077/sky-zed)**

It isn't in the Zed extension registry, so install it as a dev extension:

```bash
git clone https://github.com/TheGB0077/sky-zed
```

Then in Zed: **Extensions** (`cmd-shift-x` / `ctrl-shift-x`) →
**Install Dev Extension** → select the cloned `sky-zed` folder. Zed builds the
extension, fetches the grammar, and registers `.sky` files.

Two things need to be on Zed's `PATH`:

- **`sky`** — the extension locates the binary with `which sky` and runs
  `sky lsp`.
- **Rust / `cargo`** — Zed compiles the `tree-sitter-sky` grammar and the WASM
  extension itself when you install the dev extension, so a Rust toolchain
  (`rustup` / `cargo`) must be available. Install from
  [rustup.rs](https://rustup.rs) if you don't have it.

GUI editors often don't inherit your shell `PATH`, so launch Zed from a shell,
or put both on the system `PATH`.

> The older `.zed/config.json` snippet these docs used to show never worked on
> modern Zed — settings can only configure languages Zed already knows, not
> define a new one. The extension above is the supported path.

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

> **None of the three debugging affordances documented here exist.** This
> section listed a log at `~/.cache/sky/lsp.log`, `SKY_LSP_DEBUG=1` for
> verbosity and `SKY_LSP_TRACE=1` for JSON-RPC tracing.
> `grep -rn 'SKY_LSP_DEBUG\|SKY_LSP_TRACE\|lsp.log' rust runtime-go` returns
> nothing. There is no log file and neither variable is read. Use your
> editor's own LSP trace (`"sky.trace.server": "verbose"` in VS Code,
> `vim.lsp.set_log_level` in Neovim) until a server-side one is added.

## Performance

- Parse + canonicalise are on the critical path for every save.

> **The two figures that were here are not measurements of this server.** They
> read: "Whole-project cold start on the Sky compiler itself (~15k LoC
> Haskell): ~600 ms" and "Warm hover: < 50 ms for any symbol", alongside
> "Type-check is incremental per module using `.skycache/lowered/` cached
> state". The benchmark subject named — the Haskell compiler tree — is no
> longer the compiler, there is no `.skycache/lowered/` state (see "What gets
> indexed"), and no run artefact backs either number. They are removed rather
> than restated; `rust/crates/sky-lsp` has not been benchmarked.
