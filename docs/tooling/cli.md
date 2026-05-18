# CLI reference

> **v0.13 state**: typed Go output end-to-end. Whole-program Sky DCE
> prunes unused FFI bindings (Stripe-SDK scale: −82 % source). LSP 100 %
> coverage; runtime verification across all 26 examples. See
> [`../compiler/journey.md`](../compiler/journey.md) for the changelog.


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

### `sky upgrade-claude`

Refreshes the cwd's `CLAUDE.md` from the template embedded in the
running `sky` binary at build time. Useful after `sky upgrade` —
the binary's embedded template moves with new releases (new stdlib
APIs, deprecation notes, current limitations) but a project's
`CLAUDE.md` is a snapshot taken at `sky init` time and won't auto-
update.

Behaviour:

- Always overwrites `./CLAUDE.md` (the file is AI-context, not
  hand-edited project source).
- Backs the prior file up to `./CLAUDE.md.bak` so an accidental run
  on a project that customised the file is recoverable.
- Prints a one-line summary including the byte-count delta and the
  `sky` version that produced the new template, so you can see at a
  glance whether the template actually changed.

```bash
$ sky upgrade-claude
Refreshed CLAUDE.md (118432 → 132422 bytes, from sky v0.11.1)
  previous version saved as CLAUDE.md.bak
```

## Dev console

### `sky console [--port N] [--tui]`

Runs the bundled Sky Console mini-app standalone. The console is
also auto-mounted at `/_sky/console` inside every Sky.Live and
Sky.Http.Server app in dev mode — the standalone form is for
ad-hoc inspection when you don't have (or don't want to start)
a host app.

```bash
sky console                # Sky.Live in the browser on :8025
sky console --port 8030    # different port
sky console --tui          # same UI rendered through Sky.Tui in your terminal
```

The source lives in `sky-bundled/console/` (embedded into the sky
binary via Template Haskell). First invocation builds into
`$XDG_CACHE_HOME/sky/console-<version>/` (~3–10 s); subsequent
runs are instant. Cache keys include the version string so
`sky upgrade` auto-invalidates.

Live and TUI variants share the same `State.sky` + `View.sky` —
only the entry-point module switches between `Live.app` and
`Tui.app`. Cached binaries are kept side-by-side (`app-live` /
`app-tui`) so switching backends doesn't trigger a rebuild.

**Env flags** (full reference in CLAUDE.md):

- `SKY_CONSOLE_EMBED=off` — opt-out of the auto-mount inside user
  apps (the standalone CLI still works).
- `SKY_DEV_BANNER=off` — opt-out of the floating "🔍 Console"
  banner without disabling the mount.
- `SKY_CONSOLE_URL=https://...` — override the banner's href
  (e.g. to point at a remote shared dashboard).
- `ENV=production` (or `SKY_ENV=…` outside `{dev, development,
  local}`) — production-mode gate; suppresses console + banner
  entirely and gates `/_sky/metrics` behind auth.

## Formatting

### `sky fmt <file>`

Opinionated, deterministic, no configuration (output is Elm-compatible):

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
