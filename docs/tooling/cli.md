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

Fully validate the program. `sky check` is a strict superset of `sky build`:
it runs parsing, canonicalisation, HM inference, Go codegen, *and* invokes
`go build` on the emitted output — without producing a runnable binary. If
`sky build` would fail, `sky check` fails with the same error. This is the
v0.9 soundness gate (audit P0-1) — editor integrations should use it
directly.

### `sky verify [example]`

CI canonical runtime check. Iterates every directory under `examples/`
(or the named one), builds, runs, and asserts runtime behaviour:

- HTTP examples: hits `/` (and any routes declared in `examples/<n>/verify.json`)
  and checks status codes + body substrings.
- GUI examples (Fyne): skipped on headless CI via `SKY_SKIP_GUI=1`.

Output lines: `runtime ok: <name>`, `FAIL scenario: ...`, `FAIL build: ...`,
`[skip] <name>: ...`. Exit code is non-zero if any example fails.

Scenario file format:

```json
{
    "requests": [
        { "method": "GET", "path": "/",           "expectStatus": 200, "expectBody": ["Hello"] },
        { "method": "GET", "path": "/api/status", "expectStatus": 200, "expectBody": ["status"] }
    ]
}
```

### `sky test <file>`

Run a Sky test module. See [`testing.md`](testing.md).

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

The FFI inspector (`sky-ffi-inspect`) is embedded in the `sky`
binary and self-provisions into `$XDG_CACHE_HOME/sky/tools/` on
first use — no separate install required. Cold start costs one
`go build` (~4s); subsequent calls are instant. Content-hashed
cache means `sky upgrade` invalidates the helper automatically.

Overrides, in probe order:

1. `$SKY_FFI_INSPECTOR` — absolute path to a pre-built helper.
2. `bin/sky-ffi-inspect` in the cwd or any ancestor (dev workflow).
3. Embedded fallback (default for installed binaries).

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
