# 10 — LSP & Tooling

The tooling — `sky-lsp`, `sky-cli`, `fmt`, `testrunner`, `sky doc` — is where
law **L2** stops being a slogan and becomes the whole design. Today's LSP is the
single clearest scar in the tree: because the Haskell compiler is a batch
pipeline, the LSP had to grow its own parallel universe — **8 `IORef`s**, a
`forever` stdin loop, a `forkIO` background full-check that *shells out to `sky
check`*, a per-project workspace index that re-typechecks everything, and a
**5-round canonicalise+solve fixpoint** with a 3-second wall-clock timeout escape
hatch. None of that is LSP logic. All of it is the compiler's incrementality,
re-implemented badly outside the compiler.

Over the salsa query core (see [`01`](01-architecture-overview.md)), the LSP
becomes **thin**: it is a `tower-lsp` driver that `set_source_text` on edit and
answers each request by running one query on the same `skydb` the CLI uses.
Incrementality is not the LSP's job anymore — it is salsa's, for free, shared
with `sky build`. This is L2 (`db` is the state, one incremental engine),
L1 (no globals — the 8 IORefs vanish into inputs), and L8 (every query runs on
the lossless CST, so hover/goto/completion work on broken code).

> **Implementation status (as of `rewrite/rust-compiler`).** `sky-lsp` is **built
> and strong**: `rust/crates/sky-lsp/src/lib.rs` implements `hover` (functions,
> fields, type names, kernel calls, ctors, lambda params, case patterns),
> `goto`/definition, `completion` (qualified / field / let-binding), `references`,
> `prepare_rename` + `rename`, `semantic_tokens` (a frozen 12-type legend), and
> document-symbols. It passes the 17/17 Neovim gate (`scripts/lsp-test-nvim.sh`)
> plus a broader in-process + JSON-RPC suite. It owns no IORef pile — the 8-IORef +
> 5-round-fixpoint + background-`sky check` scars are genuinely gone. **Two gaps
> vs the present-tense text below:**
>
> - **It runs over `hir::db::SourceDb`, not salsa.** The `db.resolve_at` /
>   `db.infer_type_of` / `set_source_text` sketches and every "salsa query" label
>   are the target engine ([`01`](01-architecture-overview.md) status); today the
>   handlers drive the same value-threaded resolution db + `Typer` the CLI uses.
>   The scars are gone via that db, not via salsa memoisation yet.
> - **`inlayHint`, `signatureHelp`, and `textDocument/formatting` are not
>   implemented.** No handler for any of the three exists in `crates/sky-lsp`
>   (grep: zero hits). They appear in the request→query map, the `sky_capabilities`
>   block, and the acceptance-gate list below as the **target** capability set
>   (matching the Haskell server's advertised capabilities), but the current server
>   does not advertise or answer them. `sky fmt` itself *does* exist as a CLI verb
>   (`crates/fmt`, opinionated pretty-printer with a lossless safety net) — it is
>   simply not yet wired to the LSP `formatting` endpoint.

---

## The current tooling, and its holes

Studied so the rewrite reproduces the surface and closes the holes.

| Subsystem | Current file | Shape today | Hole the query core closes |
|---|---|---|---|
| LSP server | `src/Sky/Lsp/Server.hs` (3875 L) | `forever` stdin loop (`Server.hs:162`), 8 IORefs in `ServerState` (`Server.hs:90–128`), `forkIO` background `sky check` on save (`Server.hs:889`) | Loop + threads + IORefs → `tower-lsp` async + `db` inputs |
| Per-keystroke diagnostics | `runPipelineSt` (`Server.hs:1387–1445`) | **Re-parses the whole buffer every keystroke** (`Parse.parseModule` at `Server.hs:1388`), re-canon, re-constrain, re-solve | Salsa memoises `parse`/`resolve`/`infer`; edit invalidates only the dirty span's dependents |
| Workspace index | `src/Sky/Lsp/Index.hs` (922 L) | `buildIndex` (`Index.hs:187`) delegates to `Compile.typecheckWorkspace` — a **full-workspace parse+typecheck** cached in an IORef, rebuilt on save | The `db` *is* the index; no separate structure, no manual rebuild |
| Cross-module fixpoint | `typecheckWorkspace` (`Compile.hs:6257–6522`), `maxRounds = 5` (`Compile.hs:6489`) | 5-round canon+solve loop threading `buildCrossModuleExternalsWithMods` (`Compile.hs:6497`); a 3 s timeout falls back to empty externals (`Server.hs:1469–1496`) | The query DAG resolves cross-module deps by demand; no rounds, no timeout, no "externals" side-channel |
| Diagnostic → LSP JSON | `src/Sky/Reporting/Lsp.hs` (`renderLspDiagnostic`, L37) | Already a single shared renderer for CLI + LSP; `Diagnostic` carries severity + stable `_diag_code` + region (`Reporting/Diagnostic.hs:27–40`) | **Keep this shape** — it is already L7-correct; port `Diagnostic` as data |
| Formatter | `src/Sky/Format/{Doc,Format}.hs` (153 + 861 L) | Wadler-Lindig `Doc` algebra (`Doc.hs:33`) + an **AST comment-stream (`CS`) reconstruction** that re-attaches own-line comments by source-line/column (`Format.hs:48–90`); trailing comments are **silently dropped** (`Format.hs:18`) | Over the rowan CST, comments are *trivia in the tree* — no reconstruction, no dropped trailing comments |
| `sky doc` | `src/Sky/Doc/{Index,Render,Terminal,Markdown}.hs` | Doc index is a **"thin re-projection of the LSP index"** (`Doc/Index.hs:5–9,38`); HTML+JSON site (`Render.hs`) + terminal | Becomes queries over `skydb` directly — `module_items` + `infer` give sig/doc/loc |
| `sky test` | `app/Main.hs:1413–1467` | Synthesises `SkyTestEntry__.sky` importing the suite + `Test.runMain Suite.tests`, builds+runs, propagates exit code | Same shape, in a `testrunner` crate over `project` |
| `sky watch` | `src/Sky/Cli/Watch.hs` | Polls an allowlist every 200 ms, debounces 150 ms (`Watch.hs:8–19`), runs the **same compile pipeline as `sky run`** | Watcher just `set_source_text`; salsa recomputes only what changed |
| CLI dispatch | `app/Main.hs:977–1034` (parser), `runCommand` (`app/Main.hs:1280`) | optparse-applicative subparser → `runCommand` case | One-to-one to `sky-cli` verbs over the `project` crate |

The through-line: **every hole is the same hole** — the compiler was batch-only,
so the LSP and the doc server and the watch loop each reinvented incremental
recomputation. The salsa `db` removes the reason they exist.

---

## `sky-lsp` — every request is a query on `skydb`

The `sky-lsp` crate depends on `skydb`, `ty`, and `project` (see
[`02`](02-workspace-and-crates.md)). It owns **no** compiler state. It owns a
`salsa` database handle, a map of open documents (the *only* mutable input it
`set`s), and a `tower-lsp` connection. Everything an editor asks for is a
function of the db.

### The request → query map

Each LSP method lowers to one (or a couple of) salsa queries. The queries are the
**same** ones `sky build` calls — there is no LSP-specific inference path.

| LSP request | Queries consulted | Notes |
|---|---|---|
| `textDocument/hover` | `resolve(module)` → `DefId`; `infer(def)` for the type; `hir`/`module_items` for the doc + kind | Type comes from the real inference table, not a re-solve |
| `textDocument/definition` / `declaration` | `resolve(module)` at span → `DefId` → `def_span(DefId)` | Cross-file for free — `DefId` carries its `FileId` |
| `textDocument/completion` | `resolve(module)` for in-scope names; `module_exports(ModuleId)` for qualified `M.`; `infer` for field completion on a record type | The three completion classes (qualified / field / let-binding) are all scope queries |
| `textDocument/references` | `references(DefId)` — a reverse query over `module_graph` | Workspace-wide by construction; today's is same-file + index-walk (`Server.hs:826–862`) |
| `textDocument/rename` / `prepareRename` | `references(DefId)` → one `WorkspaceEdit`; `prepareRename` validates the target is a renameable `DefId` | Reuses the references query |
| `textDocument/semanticTokens/full` | `parse(file)` (CST) + `resolve` to classify each token | 12 token types (`Server.hs:2042–2056`), preserved verbatim |
| `textDocument/publishDiagnostics` | `parse` + `resolve` + `infer` + `exhaustiveness` diagnostics, unioned | **Push** on input change (below); no `forkIO`, no `sky check` subprocess |
| `textDocument/inlayHint` | `infer(def)` per-region types | **Target — not yet implemented in `crates/sky-lsp`.** |
| `textDocument/formatting` | `fmt` crate over `parse(file)` CST | **Target — `crates/fmt` exists (CLI `sky fmt`) but is not wired to this LSP endpoint yet.** |
| `textDocument/signatureHelp` | `resolve` at call head → `infer` signature | **Target — not yet implemented in `crates/sky-lsp`.** |

### Incremental for free — contrast the 5-round + threads

The old flow, on one keystroke in `Main.sky` of a 500-module workspace:

1. `runPipelineSt` re-parses the **entire buffer** (`Server.hs:1388`).
2. It reads externals from the index; the index was built by
   `typecheckWorkspace`, which ran a **5-round** canon+solve fixpoint over the
   whole workspace (`Compile.hs:6489–6500`).
3. If building externals exceeds **3 seconds** it gives up and uses empty
   externals (`Server.hs:1469–1496`), silently degrading cross-module accuracy.
4. On save, a `forkIO` shells out to `sky check` for the codegen/`go build`
   errors the type-checker alone can't see (`Server.hs:889–918`).

The salsa flow, same keystroke:

1. Driver `set_source_text(file_id, new_text)`. That is the only mutation.
2. Salsa marks `parse(file_id)` and its transitive dependents *maybe-dirty*.
   Queries for **other** modules whose inputs didn't change are **not**
   recomputed — their memoised `infer` results stand.
3. `publishDiagnostics` re-runs `diagnostics(file_id)`; salsa recomputes only the
   sub-queries whose inputs actually changed (often just this file's `parse` →
   `resolve` → `infer`). Cross-module externals are *edges in the DAG*, resolved
   by demand — there is no round count, no externals side-channel, no timeout.
4. The codegen/`go build` layer is a query too (`go_module` →
   `project::go_build`); the LSP can surface it on the same push without a
   subprocess or a second pipeline (see [`08`](08-go-codegen.md)).

The 5-round fixpoint existed because the batch compiler had no way to say "this
module's canonical form depends on that module's exports, recompute on demand."
The query DAG says exactly that. **The fixpoint, the 8 IORefs, the background
thread, and the 3 s timeout all delete.**

### `tower-lsp` scaffolding

```rust
// crates/sky-lsp/src/main.rs
use tower_lsp::{LspService, Server, LanguageServer, Client, jsonrpc::Result};
use tower_lsp::lsp_types::*;

struct SkyLsp {
    client: Client,
    // The ONE piece of state: the salsa db + open-doc inputs live inside it.
    // No IORef pile — `db` is the state (L1). Guarded by a mutex only because
    // tower-lsp is async; salsa queries themselves are pure reads.
    state: tokio::sync::Mutex<Workspace>,
}

/// The workspace is a salsa database plus the driver-set inputs.
struct Workspace {
    db: SkyDatabase,                    // skydb::SkyDatabase — the shared engine
    open: IndexMap<FileId, DocVersion>, // which files the editor has open
    root: Option<FileId>,               // project root (from initialize)
}

#[tower_lsp::async_trait]
impl LanguageServer for SkyLsp {
    async fn initialize(&self, p: InitializeParams) -> Result<InitializeResult> {
        let mut ws = self.state.lock().await;
        ws.set_root_from(p.root_uri);            // replaces ssRootPath IORef
        Ok(InitializeResult {
            capabilities: sky_capabilities(),    // parity table below
            server_info: Some(ServerInfo { name: "sky-lsp".into(), version: Some("1.0.0".into()) }),
        })
    }

    async fn did_change(&self, p: DidChangeTextDocumentParams) {
        let uri = p.text_document.uri;
        let text = p.content_changes.into_iter().next().map(|c| c.text).unwrap_or_default();
        let diags = {
            let mut ws = self.state.lock().await;
            let fid = ws.file_id(&uri);
            ws.db.set_source_text(fid, Arc::new(text)); // the ONLY mutation (L2)
            // Pure read; salsa recomputes only the dirty sub-DAG.
            sky_lsp::diagnostics::for_file(&ws.db, fid)
        };
        self.client.publish_diagnostics(uri, diags, None).await;
    }
    // hover / goto / completion / references / rename / semantic_tokens below…
}
```

`initialize` capabilities — the **target** set, reproducing the Haskell server's
advertised capabilities (`Server.hs:1108–1148`). The current `crates/sky-lsp`
advertises the subset it implements; the three fields marked `// target` below are
not yet answered (see this doc's status callout):

```rust
fn sky_capabilities() -> ServerCapabilities {
    ServerCapabilities {
        text_document_sync: Some(TextDocumentSyncKind::FULL.into()), // openClose+change+save
        hover_provider: Some(true.into()),
        definition_provider: Some(true.into()),
        declaration_provider: Some(true.into()),
        references_provider: Some(true.into()),
        document_symbol_provider: Some(true.into()),
        document_formatting_provider: Some(true.into()),          // target — not yet implemented
        rename_provider: Some(RenameOptions { prepare_provider: Some(true), ..Default::default() }.into()),
        completion_provider: Some(CompletionOptions {
            trigger_characters: Some(vec![".".into()]), ..Default::default() }),
        signature_help_provider: Some(SignatureHelpOptions {      // target — not yet implemented
            trigger_characters: Some(vec!["(".into(), " ".into()]),
            retrigger_characters: Some(vec![",".into()]), ..Default::default() }),
        semantic_tokens_provider: Some(sky_semantic_legend().into()), // 12 types, Server.hs:2042
        code_action_provider: Some(true.into()),
        inlay_hint_provider: Some(OneOf::Left(true)),             // target — not yet implemented
        ..Default::default()
    }
}
```

### Request handlers as db queries — sketches

**Hover** — resolve the ident at the cursor to a `DefId`, read its inferred type.
Today this re-parses (`Server.hs:366–368`) and juggles the `Idx` cache; here it
is three query calls:

```rust
fn hover(db: &SkyDatabase, fid: FileId, pos: Position) -> Option<Hover> {
    let off = line_col_to_offset(db, fid, pos);
    // parse() is memoised — no re-parse per keystroke (contrast Server.hs:368).
    let tok = syntax::token_at(db.parse(fid).syntax(), off)?;
    match db.resolve_at(fid, off)? {            // salsa query -> Resolution
        Resolution::Def(def) => {
            let ty  = db.infer_type_of(def);    // the real inference table (ty crate)
            let doc = db.def_doc(def);          // hir doc comment
            Some(markdown_hover(db, def, ty, doc))
        }
        // Field access `model.count`: resolve the receiver's record type, read the field.
        Resolution::Field { receiver, name } => {
            let rec = db.infer_type_of_expr(fid, receiver)?;
            let fty = db.record_field_type(rec, name)?;   // closes the `.field` hover, Server.hs:377
            Some(markdown_hover_field(name, fty))
        }
        Resolution::Module(m) => Some(module_summary_hover(db, m)),
    }
}
```

**Goto-definition** — the `DefId` already carries its file:

```rust
fn goto_definition(db: &SkyDatabase, fid: FileId, pos: Position) -> Option<Location> {
    let off = line_col_to_offset(db, fid, pos);
    let def = db.resolve_at(fid, off)?.as_def()?;
    let span = db.def_span(def);                // Span { FileId, TextRange } (L3)
    Some(span_to_location(db, span))            // cross-file for free
}
```

**Completion** — the three classes the nvim gate checks are all scope reads:

```rust
fn completion(db: &SkyDatabase, fid: FileId, pos: Position) -> CompletionResponse {
    let off = line_col_to_offset(db, fid, pos);
    let items = match completion_context(db, fid, off) {
        // `M.` -> exports of the aliased/qualified module, with insert_text = bare name
        CompletionCtx::Qualified(module) =>
            db.module_exports(module).iter()
              .map(|sym| qualified_item(sym))     // label "M.foo", insertText "foo" (nvim: completion-qualified-insert-text)
              .collect(),
        // `record.` -> fields of the receiver's inferred record type (nvim: completion-field)
        CompletionCtx::Field(receiver_ty) =>
            db.record_fields(receiver_ty).iter().map(field_item).collect(),
        // bare ident -> everything in lexical scope incl. let-bindings (nvim: completion-let-binding)
        CompletionCtx::Scope(scope) =>
            db.names_in_scope(scope).iter().map(scope_item).collect(),
    };
    CompletionResponse::Array(items)
}
```

**References / rename** — one reverse query, then rename is a `WorkspaceEdit`
over the same set (today references is a same-file walk plus an index scan,
`Server.hs:826–862`):

```rust
fn references(db: &SkyDatabase, fid: FileId, pos: Position) -> Vec<Location> {
    let Some(def) = db.resolve_at(fid, line_col_to_offset(db, fid, pos)).and_then(|r| r.as_def())
        else { return vec![] };
    db.references(def).iter().map(|s| span_to_location(db, *s)).collect() // workspace-wide (L2)
}

fn rename(db: &SkyDatabase, fid: FileId, pos: Position, new: &str) -> Option<WorkspaceEdit> {
    let def = db.resolve_at(fid, line_col_to_offset(db, fid, pos))?.as_def()?;
    let mut edits: IndexMap<Url, Vec<TextEdit>> = IndexMap::new();  // IndexMap: deterministic (L4)
    for span in db.references(def) {
        let (url, range) = span_to_url_range(db, span);
        edits.entry(url).or_default().push(TextEdit { range, new_text: new.into() });
    }
    Some(WorkspaceEdit { changes: Some(edits.into_iter().collect()), ..Default::default() })
}
```

**Diagnostics** — the union of parse + resolve + infer + exhaustiveness
diagnostics, each a query that returns `(_, Vec<Diagnostic>)` (L7). No exception
short-circuits the build; a parse error still yields a tree and downstream
diagnostics (L8):

```rust
fn diagnostics(db: &SkyDatabase, fid: FileId) -> Vec<lsp_types::Diagnostic> {
    let mut ds = Vec::new();
    ds.extend(db.parse(fid).diagnostics());          // recovery: partial tree + errors
    ds.extend(db.resolve(db.module_of(fid)).diagnostics());
    for def in db.defs_in(fid) {
        ds.extend(db.infer(def).diagnostics());       // memoised across keystrokes
        ds.extend(db.exhaustiveness(def).diagnostics());
    }
    // ONE shared renderer, same as the CLI (port of Sky.Reporting.Lsp.renderLspDiagnostic).
    ds.into_iter().map(diagnostics::to_lsp).collect()
}
```

**Semantic tokens** — walk the CST, classify each token by its resolution, emit
the 12-type legend delta-encoded (`Server.hs:2042–2065`). Because the legend is
frozen, this is a pure compat surface.

---

## The 17-test compat gate (acceptance criteria)

`scripts/lsp-test-nvim.sh` drives the LSP through a **real Neovim LSP client** —
so it catches editor-level bugs (label-vs-insertText, filterText, scope) that
synthetic JSON-RPC tests miss (`lsp-test-nvim.sh:1–10`). The 17 tests
(`lsp-test-nvim.sh:26–46`) are the **hard acceptance gate**: `sky-lsp` ships only
when all 17 pass against the same harness, unmodified.

| # | Test | LSP feature | Query it exercises |
|---|---|---|---|
| 1 | `hover-task-run` | hover on a `Task.run` call | `resolve_at` → kernel `DefId` → `infer_type_of` |
| 2 | `hover-field` | hover on `record.field` | `record_field_type` (the `Resolution::Field` arm) |
| 3 | `hover-type-name` | hover on a type name | `resolve_at` → type `DefId` |
| 4 | `completion-qualified-insert-text` | completion after `M.` (label ≠ insertText) | `module_exports` |
| 5 | `completion-field` | completion after `record.` | `record_fields` |
| 6 | `completion-let-binding` | completion of a `let`-bound name | `names_in_scope` |
| 7 | `goto-def-type-name` | goto on a type name | `def_span` of a type `DefId` |
| 8 | `hover-function-use` | hover on a used function | `infer_type_of` at a use-site |
| 9 | `goto-def-function` | goto on a function use | `def_span` (function) |
| 10 | `hover-ctor-use` | hover on a constructor use | `resolve_at` → ctor `DefId` |
| 11 | `hover-lambda-param` | hover on a lambda parameter | `resolve_at` → local binder |
| 12 | `hover-case-pattern` | hover on a `case` pattern binder | `resolve_at` → pattern binder |
| 13 | `hover-kernel-call` | hover on a kernel/stdlib call | kernel `DefId` type surface |
| 14 | `goto-def-ctor` | goto on a constructor | `def_span` (ctor) |
| 15 | `goto-def-let-binding` | goto on a `let`-bound name | `def_span` (local) |
| 16 | `goto-def-lambda-param` | goto on a lambda param | `def_span` (binder) |
| 17 | `goto-def-field` | goto on a record field | field-decl span via record type |

These 17 partition into three resolution shapes — **hover** (7 symbol classes:
kernel calls, fields, type names, functions, constructors, lambda params, case
patterns), **completion** (3: qualified insert-text, field, let-binding), and
**goto-def** (7: type names, functions, constructors, let bindings, lambda
params, fields). Every one is `resolve_at` returning a `Resolution`, then either
`infer_type_of` (hover), `def_span` (goto), or a scope/exports enumeration
(completion). The gate is met when the same `.lua` runner reports `PASS` for all
17 (`lsp-test-nvim.sh:49–67`). `xtask` runs it in CI; see
[`11`](11-testing-and-verification.md).

> Beyond the 17, the semantic-tokens, references, rename, signature-help,
> inlay-hint, and diagnostics surfaces carry their own snapshot tests (`insta`),
> but the 17-nvim suite is the *non-negotiable editor-parity floor*.

---

## `sky fmt` — exact formatter over the CST

Today's formatter is two layers: a Wadler-Lindig `Doc` algebra
(`Format/Doc.hs:33–46`, `maxWidth = 80`) and — because the Haskell AST **discards
trivia** — an elaborate comment-reconstruction pass. `Format.hs` threads a
comment-stream `CS` and, at *every* semantic boundary (top-decl, value body,
let-def, case-arm, record field, list/tuple element, if-else), drains own-line
comments by matching source line and column (`Format.hs:48–90`). It is ~500 LOC
of heuristic re-attachment, and it still **silently drops trailing comments**
(`x = y  -- inline` loses the comment — `Format.hs:18`).

Over the rowan CST (see [`04`](04-syntax-lexer-parser.md)), **comments and
whitespace are trivia nodes in the tree**. The formatter never reconstructs
placement because placement was never lost:

- `fmt` (its own crate, depends only on `syntax`) walks the CST, re-emitting each
  node with normalised spacing and attaching its **leading/trailing trivia**
  verbatim. Trailing comments round-trip — the `Format.hs:18` data-loss hole
  **closes**.
- The Wadler-Lindig `group`/`line` layout logic ports directly (the algebra is
  language-agnostic); `maxWidth = 80` is preserved.
- **Idempotence is an invariant, tested**: `fmt(fmt(x)) == fmt(x)` byte-for-byte,
  as an `insta` + property test in the `fmt` crate. The current guarantee ("two
  passes are byte-identical") becomes a CI gate, not a hope.

```rust
// crates/fmt/src/lib.rs
pub fn format_source(text: &str) -> String {
    let parse = syntax::parse(text);           // lossless: trivia + errors in the tree
    let doc = fmt::to_doc(parse.syntax_node()); // Wadler-Lindig Doc, trivia carried on nodes
    doc.render(80)                              // maxWidth 80, byte-exact
}
// Invariant (tested): format_source(&format_source(x)) == format_source(x)
```

`sky fmt` (CLI) and `textDocument/formatting` (LSP) call the **same**
`format_source` — the CLI reads the file, the LSP reads the open-doc input; both
hit one function. Formatting a syntactically broken file still works (L8): the
CST has error nodes; `fmt` re-emits their text verbatim rather than throwing.

---

## `sky doc` — queries over `skydb`

Today `sky doc`'s catalogue is explicitly a "thin re-projection of the LSP index"
(`Doc/Index.hs:5–9`) — it imports `Sky.Lsp.Index` (`Doc/Index.hs:38`) to get
`module → [symbol{name, sig, doc, location}]`, filters to public symbols, and
renders three targets: a **terminal** view (`Doc/Terminal.hs`), an **HTML+JSON
static site** for `--serve` / `--export` (`Doc/Render.hs:1–20`, with a client-
side fuzzy-search JSON catalog), and a **TUI** browser (`--tui`, a Sky.Tui app).

In the rewrite this dependency inverts cleanly: since the "LSP index" *is* the
`skydb`, `sky doc` queries the db directly — no re-projection layer:

```rust
fn doc_index(db: &SkyDatabase, project: &Project) -> DocIndex {
    let mut modules = IndexMap::new();               // deterministic order (L4)
    for m in db.project_modules(project) {           // src + deps + stdlib, topo order
        let syms = db.module_exports(m).iter().map(|def| DocSymbol {
            name: db.def_name(*def),
            sig:  db.infer_signature(*def),          // real inference, same as hover
            doc:  db.def_doc(*def),                  // doc comment from hir
            span: db.def_span(*def),
            kind: db.def_kind(*def),
        }).collect();
        modules.insert(db.module_name(m), DocModule { syms, bucket: db.module_bucket(m) });
    }
    DocIndex { modules }                             // buckets: project / deps / stdlib
}
```

Render targets are pure functions of `DocIndex` and stay identical in behaviour:
`sky doc Module` (terminal), `sky doc --serve [--port N]` (HTTP, reusing the Go
runtime's `Sky.Http.Server` to serve the emitted static dir), `sky doc --tui`
(Sky.Tui), `sky doc --list`, `sky doc --export <dir>`. Because signatures come
from `infer_signature` — the same query hover uses — doc output and hover output
can never disagree.

---

## `sky test` — the `testrunner` crate

`sky test tests/FooTest.sky` today synthesises a temporary
`SkyTestEntry__.sky` that imports the suite and calls `Test.runMain Suite.tests`,
builds+runs it through the normal pipeline, and propagates the exit code so CI
sees failures (`app/Main.hs:1413–1467`). It cleans up the synthesised entry
regardless of outcome.

The `testrunner` crate (depends on `project`) reproduces this exactly:

1. Derive the suite's module name from its path under `src/` or `tests/`
   (`app/Main.hs:1428`).
2. Build an **in-memory** entry module (`module SkyTestEntry__ exposing (main);
   main = Test.runMain Suite.tests`) — set as a synthetic `source_text` input on
   the db, so no temp file is written to the user's `src/` (closing the "entry
   left behind on a build exception" footgun the Haskell path guards against at
   `app/Main.hs:1463–1467`).
3. `project::build` the entry → `go build` → run; propagate the process exit code.

```rust
// crates/testrunner/src/lib.rs
pub fn run_test(project: &Project, suite_path: &Path) -> Result<ExitStatus> {
    let suite = project.module_name_under_roots(suite_path, &["src", "tests"])?;
    let entry = format!(
        "module SkyTestEntry__ exposing (main)\n\n\
         import Sky.Test as Test\nimport {suite} as Suite\n\nmain =\n    Test.runMain Suite.tests\n");
    let fid = project.db_mut().add_virtual_source("SkyTestEntry__.sky", &entry); // input, not a file
    let bin = project.build_entry(fid)?;   // same build driver as `sky build`
    Ok(run_binary(&bin)?)                  // exit code propagated for CI
}
```

`Sky.Test` itself is stdlib Sky source (`sky-stdlib/Sky/Test.sky`, unchanged,
embedded per [`09`](09-runtime-and-ffi.md)) — assertions (`equal`, `ok`, `err`,
`expectErrorKind`, …), the `Passed | Failed String` result type, `summarise`
(prints `ok`/`FAIL` lines + an `N passed, M failed` tally), and `runMain`
(runs, summarises, then `System.exit 0`/`1`). The `testrunner` crate is only the
entry-synthesis + exit-code glue.

---

## `sky watch` — salsa-driven incremental rebuild + restart

`sky watch` today runs on two cache layers that salsa unifies into one. The
**watch loop** (`Watch.hs:373`) polls a strict allowlist every 200 ms
(`woPollMs`, `Watch.hs:77`), debounces 150 ms (`Watch.hs:78`) to coalesce a save
burst, detects change by an **in-memory mtime+size fingerprint** (`hashFiles`,
`Watch.hs:171` — it deliberately does *not* hash contents), then runs the **same
compile pipeline as `sky run`** and respawns the binary
(SIGTERM→SIGKILL after 3 s, `Watch.hs:188`), keeping the old binary alive across
a *failing* rebuild (`Watch.hs:21–25,398–402`). The watched scope is an
allowlist — sky.toml + entry dir + `tests/`, with `sky-out`/`.skycache`/
`.skydeps`/etc. excluded (`collectWatchedPaths`, `Watch.hs:113`; `skipDirs`,
`Watch.hs:153`). Underneath, `Compile.compile` keeps its **own** incremental
cache — `.skycache/source.hash` (`Compile.hs:5429`), `.skycache/lowered/`
(per-module IR, `Compile.hs:3158`), and `.skycache/ffi/*.kernel.json` folded into
the hash (`Compile.hs:3197–3208`).

Salsa collapses both layers into its memoisation. The compiler's
`source.hash`/`lowered/` short-circuit is **exactly what salsa's invalidation
does internally** — an unchanged input is a no-op `set_input`, and only the dirty
sub-DAG recomputes; the watch loop's mtime fingerprint becomes the fs-event feed
that decides *which* input to `set`. The loop shrinks to:

```rust
fn run_watch(project: &Project, entry: FileId, opts: WatchOpts) -> ! {
    let mut running: Option<Child> = None;
    let watcher = notify::recommended_watcher(...);   // OS fs events; allowlist = entry dir + sky.toml + tests/
    loop {
        let changed: Vec<PathBuf> = watcher.debounced(opts.debounce_ms); // coalesce a save burst
        for path in &changed {
            let fid = project.file_id(path);
            project.db_mut().set_source_text(fid, read_to_string(path).into()); // salsa invalidates the sub-DAG
        }
        // Only the queries depending on `changed` recompute (the source.hash + lowered/ caches, for free).
        match project.build_entry(entry) {
            Ok(bin) => { running = respawn(running, &bin, opts.kill_timeout_ms); } // SIGTERM→SIGKILL
            Err(diags) => { report(&diags); /* keep old binary alive — Watch.hs:21 policy */ }
        }
    }
}
```

The FFI `.skyi` cache stays a pinned, committed input (never regenerated
mid-build — L4, see [`09`](09-runtime-and-ffi.md)); everything else the watch loop
used to hand-cache is now the db's job. The build-error policy (old binary keeps
running through a failed rebuild) is preserved as loop control, not a cache
concern.

---

## The full CLI verb surface → `project` crate

`sky-cli` is a thin `clap` front-end (replacing the optparse-applicative
subparser at `app/Main.hs:977–1034`) over the `project` crate. Every verb the
Haskell `runCommand` dispatches (`app/Main.hs:1280+`) maps one-to-one:

| Verb | Current dispatch | `project`/crate mapping |
|---|---|---|
| `sky build <file>` | `Compile.compile` → `go build` (`Main.hs:1285`) | `project::build` (with the repo-root guard, `Main.hs:1293`) |
| `sky run <file>` | build + exec (`Main.hs:1333`) | `project::build` + spawn |
| `sky watch <file>` | `Sky.Cli.Watch.runWatch` | `project::watch` (above) |
| `sky check <file>` | type-check + `go build` (`Main.hs:986`) | `project::check` — runs the query DAG through `go_module` |
| `sky fmt <target>` | `Format.formatModule` (`Main.hs:988`) | `fmt::format_source` (`--stdin` / `-` supported) |
| `sky test <file>` | synth entry + build+run (`Main.hs:1413`) | `testrunner::run_test` |
| `sky init [name]` | scaffold project | `project::init` (emits `sky.toml` + `src/Main.sky` + `CLAUDE.md`) |
| `sky add <pkg>` / `remove` | FFI binding gen/removal (`Main.hs:999–1004`) | `ffi` crate — deterministic inspect → pinned `.skyi` |
| `sky install` / `update` | dep sync (`Main.hs:1005–1008`) | `project` + `ffi` |
| `sky db status` / `migrate` | `dbParser` (`Main.hs:1030`) | `project::db` (drives `Std.Db` migrations via the built binary) |
| `sky doc …` | `Doc.*` (`Main.hs:1024`) | `sky doc` queries (above) — terminal / `--serve` / `--tui` / `--list` |
| `sky doctor [--fix]` | `Sky.Cli.Doctor` (`Main.hs:1027`) | `project::doctor` (stale cache, port-in-use, missing FFI) |
| `sky console [--tui]` | bundled console app (`Main.hs:1018`) | `project` spawns the embedded console (Go runtime asset) |
| `sky console-serve` | hub daemon (`Main.hs:1021`) | `project` spawns the hub (unchanged runtime, L10) |
| `sky clean` | rm `sky-out/` + `.skycache/` (`Main.hs:1009`) | `project::clean` |
| `sky lsp` | `runLsp` (`Main.hs:1011`) | `sky-lsp::serve` (`tower-lsp`, above) |
| `sky upgrade` / `upgrade-claude` | self-update / template refresh | `sky-cli` (binary self-management) |
| `sky --version` / `version` | print version (`Main.hs:1281`) | `sky-cli` |
| `sky verify [example]` | build+run+panic-check corpus (`Main.hs:993`) | `xtask` / `project::verify` — the conformance driver ([`11`](11-testing-and-verification.md)) |

`sky check ≡ sky build` (both run `go build` on the emitted Go) remains a hard
invariant — in the rewrite both call `project::go_build` on the `go_module`
query output, so they *cannot* diverge (the LSP-vs-CLI asymmetry that forced the
`forkIO sky check` at `Server.hs:889` disappears).

---

## Acceptance gates (this doc)

1. **17/17 nvim LSP tests pass** against `scripts/lsp-test-nvim.sh` unmodified —
   the editor-parity floor.
2. **LSP capability parity** with `Server.hs:1108–1148` (hover, goto, declaration,
   completion, references, rename+prepare, document-symbol, formatting,
   signature-help, code-action, 12-type semantic tokens, inlay hints). *Status:
   hover / goto / declaration / completion / references / rename+prepare /
   document-symbol / semantic-tokens are implemented; **formatting,
   signature-help, inlay-hint, and code-action are still target** (see this doc's
   status callout).*
3. **No LSP-private compiler state** — `sky-lsp` owns a `skydb` handle + open-doc
   inputs and nothing else. No fixpoint loop, no background full-check thread, no
   externals timeout (L1, L2). CI greps the crate for `IORef`-equivalents (extra
   `Mutex`/`RwLock` over compiler state) and fails.
4. **`sky fmt` is byte-exact idempotent** — `fmt(fmt(x)) == fmt(x)`; trailing
   comments round-trip (closes `Format.hs:18`); formats broken code (L8).
5. **`sky doc` signatures == hover signatures** — both from `infer_signature`.
6. **`sky test` propagates exit codes**; the synthesised entry never touches the
   user's `src/` on disk.
7. **`sky watch` recomputes only the dirty sub-DAG** — no manual `.skycache`
   short-circuit logic; correctness verified by editing one module in a
   multi-module project and asserting sibling `infer` results are not recomputed.
8. **Full CLI verb parity** with `app/Main.hs:977–1034`; `sky check ≡ sky build`.
