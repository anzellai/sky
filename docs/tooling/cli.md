# CLI reference

Every `sky` subcommand. Run `sky --help` for the authoritative list.

## Build & run

### `sky build [path]`

Compile a Sky source file to a Go binary under `sky-out/`.

```bash
sky build src/Main.sky
```

Pipeline:

1. Parse `sky.toml` for `[go.dependencies]` and `[dependencies]`.
2. Auto-regenerate any missing FFI bindings in `.skycache/`.
3. Resolve modules, type-check, lower to Go under `sky-out/`.
4. Invoke `go build` → `sky-out/app` (or the `bin` name set in `sky.toml`).

### `sky run [path]`

`sky build` + execute the resulting binary.

### `sky check [path]`

Type-check only. No codegen, no `go build`. Useful in editor integrations.

## Cache & cleanup

### `sky clean`

Removes:

- `sky-out/` — compiled binary + Go source
- `.skycache/` — generated FFI bindings, lowered-module cache, incremental state
- `.skydeps/` — Sky source dependencies (if any)
- `dist/` — release archives

Rebuild from scratch with `sky build` after `sky clean`.

## Dependencies

### `sky add <pkg>`

Fetches a Go module, runs the FFI inspector, generates `.skycache/ffi/<slug>.{skyi,kernel.json}` + `.skycache/go/<slug>_bindings.go`. Records the dependency in `sky.toml` under `[go.dependencies]`.

```bash
sky add github.com/google/uuid
sky add github.com/stripe/stripe-go/v84
```

### `sky remove <pkg>`

Drops the dependency from `sky.toml` and prunes the Go module cache.

### `sky install`

Re-fetches every declared dependency. Idempotent — skips packages whose bindings are already present.

### `sky update`

Bumps all `[go.dependencies]` to their latest versions.

### `sky upgrade`

Self-upgrades the `sky` binary from the latest GitHub release.

## Formatting

### `sky fmt <file>`

Opinionated elm-format style:

- 4-space indent, no tabs.
- Leading commas for multi-line lists/records.
- Pipelines broken onto new lines.
- Refuses to overwrite if the formatter would lose more than one-third of the source lines (guards against partial-parse deletions).

## Editor integration

### `sky lsp`

Starts the Language Server over JSON-RPC / stdio. Used by the Helix and Zed integrations and any LSP-aware editor.

See [`lsp.md`](lsp.md) for configuration snippets.

## Layout

Sky writes generated artefacts to predictable locations — everything under `.skycache/` and `sky-out/` is regenerable. Nothing generated lives alongside your source.

```
project/
    src/                  -- your Sky source
    sky.toml              -- manifest
    .skycache/
        ffi/              -- .skyi signatures + kernel.json registries
        go/               -- generated Go FFI wrappers
        lowered/          -- incremental lowered-module cache
    .skydeps/             -- Sky source deps (if any)
    sky-out/              -- compiled binary + lowered main.go + rt/
```
