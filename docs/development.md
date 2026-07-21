# Development

> **Compiler**: Sky's compiler is written in **Rust** (cargo workspace
> at `rust/`, crate `sky-cli` builds the `sky` binary). The retired
> Haskell compiler lives under `legacy-haskell-compiler/` for
> historical reference. Type-directed lowering, Go generics on
> parametric record aliases, Layer-3 stdlib, and whole-program DCE all
> carry over; runtime verification runs across ~50 examples. See
> [`compiler/versions.md`](compiler/versions.md) for the changelog.


Building Sky from source — for contributors, language-tooling work,
or anyone who wants to run the compiler before a release lands.

## Prerequisites

- **Rust toolchain** — installed via [rustup](https://rustup.rs/);
  the exact version is pinned by `rust/rust-toolchain.toml`, so
  `rustup` auto-selects it when you build inside the workspace.
- **Go 1.21+** — required both to build `sky-ffi-inspect` and at
  runtime (Sky compiles to Go and invokes `go build`).

Verify:

```bash
rustc --version           # matches rust/rust-toolchain.toml
cargo --version
go    version             # 1.21+
```

## Local build (one shot)

```bash
./scripts/build.sh --clean
```

This runs `cargo build --release -p sky-cli` and produces:

- `sky-out/sky` — the Sky compiler (Rust). **The only artefact
  end users need.**
- `bin/sky-ffi-inspect` — local dev copy of the Go helper. Optional;
  see "Embedded inspector" below.

Flags:

| Flag | Effect |
|------|--------|
| `--clean` | `rm -rf rust/target/ sky-out/ bin/` first |
| `--self-tests` | Run `sky build` across every fixture in `test-files/` |
| `--sweep` | Clean-build every project under `examples/` |

## Quick rebuild (while hacking)

The full `scripts/build.sh` clean-copies the binary and runs the
hygiene checks — overkill for iterative work. For a fast rebuild of
just the compiler, build the `sky-cli` crate directly:

```bash
( cd rust && cargo build --release -p sky-cli )
cp rust/target/release/sky sky-out/sky
# macOS: re-sign the copy so the kernel's code-signing cache
# doesn't flag the new binary
codesign -s - sky-out/sky
sky-out/sky --version
```

Debug builds (`cargo build -p sky-cli`, no `--release`) compile
faster and land in `rust/target/debug/sky` — handy for `cargo test`
iteration.

## Running tests

Four matrices, all must pass before a push:

```bash
# 1. Cargo workspace suite — lexer, parser, name resolution, type
#    inference, lowering, codegen, LSP protocol, per-crate unit +
#    integration tests. Run from the workspace root.
(cd rust && cargo test --workspace)

# 2. xtask gate suite — end-to-end differential + regression gates.
#    Gates: roundtrip, resolve, infer, reject, fuzz, coerce-floor,
#    repro, build-run (48 build-verified examples), golden.
(cd rust && cargo run -p xtask -- build-run)   # one gate; repeat per gate
#   … or run each of: roundtrip resolve infer reject fuzz \
#     coerce-floor repro build-run golden

# 3. Runtime Go tests — rt helpers, ADT shape, coercion, typed FFI,
#    security (CSRF, rate limit, auth secrets), session round-trip.
(cd runtime-go && go test ./rt/)

# 4. Self-tests — every fixture in test-files/ must build clean.
pass=0; fail=0
for f in test-files/*.sky; do
    rm -rf .skycache
    ./sky-out/sky build "$f" >/dev/null 2>&1 \
        && pass=$((pass+1)) \
        || fail=$((fail+1))
done
echo "self-tests: $pass passed, $fail failed"
```

## Nix

A `flake.nix` at the repo root provides a Rust dev shell (rustc,
cargo, rustfmt, rust-analyzer) plus Go and pkg-config. A separate
`legacy` shell pins GHC 9.4.8 + the system libraries the retired
Haskell compiler links against (gmp, libffi, ncurses, zlib).

### Reproducible shell

```bash
nix develop            # primary — Rust + Go toolchain
# Inside the shell you now have cargo, rustc, go, pkg-config on PATH.
./scripts/build.sh --clean

nix develop .#legacy   # only for building legacy-haskell-compiler/
```

Each shell's `shellHook` sets `SKY_RUNTIME_DIR` to the repo's
`runtime-go/` so in-tree builds resolve the runtime without the
embedded fallback.

### Build the compiler via Nix

```bash
nix build .#sky
./result/bin/sky --version
```

This runs the `cargo build -p sky-cli` pipeline (via
`rustPlatform.buildRustPackage`) inside the Nix sandbox and puts the
result in `./result/bin/sky`. The embedded-runtime and
embedded-inspector splices still bundle the Go source trees into the
binary, so the result is a fully self-contained executable.

### Ad-hoc run

```bash
nix run .#sky -- build src/Main.sky
```

## Artefact layout

A `./scripts/build.sh` run leaves:

```
sky-out/
    sky                       -- the compiler (ship this)
bin/
    sky-ffi-inspect           -- local dev copy (optional)
rust/target/                  -- cargo's intermediate output
                              --   (release/sky, debug/sky, deps)
```

End-user install via `install.sh` or a released tarball only lays
down `sky-out/sky`. There is no separate `sky-ffi-inspect` binary
to install — it's embedded.

## Embedded inspector

`sky add` needs a Go-side helper (`sky-ffi-inspect`, a Go tool at
`tools/sky-ffi-inspect/`) to introspect package APIs. Rather than
shipping a second executable, the Rust compiler embeds the helper's
Go source at build time (alongside the runtime and stdlib embeds)
and materialises + `go build`s it on first use, caching to
`$XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<contentHash>/`.
Resolution order inside the compiler:

1. `$SKY_FFI_INSPECTOR` — explicit override (test harnesses, custom
   builds).
2. `bin/sky-ffi-inspect` walking up from the cwd — **contributor
   workflow** hits this; that's why `scripts/build.sh` still writes
   one into `bin/`.
3. Embedded fallback — extract source, `go build`, cache. Released
   binaries hit this; cold start ~4 seconds, warm calls instant.

Content-hash keying means `sky upgrade` auto-invalidates stale
cached helpers — no manual cleanup required.

If you edit `tools/sky-ffi-inspect/main.go`, rebuild the compiler
(the Rust build re-embeds the modified source) *and* the `bin/`
copy so your dev workflow picks the change up without paying the
one-time go-build on first use.

## Releases

`scripts/build.sh` produces the binary every release pipeline ships.
Before tagging:

1. `./scripts/build.sh --clean`
2. `( cd rust && cargo test --workspace )` + the xtask gate suite
   (`cargo run -p xtask -- <gate>` for each gate)
3. `./sky-out/sky verify` — runs every example end-to-end
   (forbidden-pattern gate, build, run, HTTP probe).
4. Tag + push.

See [`compiler/runtime-verification.md`](compiler/runtime-verification.md)
for the full gate matrix.

## Troubleshooting

**`sky-ffi-inspect: go build failed` on first `sky add`** — `go` is
not on `PATH` inside the environment where `sky` runs, or the Go
module cache is missing network access. Verify `go version` and
`go env GOCACHE`.

**Rust toolchain mismatch** — `cargo build` uses the version pinned
in `rust/rust-toolchain.toml`; `rustup` fetches it automatically the
first time you build inside the workspace. If `rustc --version`
disagrees, run `rustup show` (or enter `nix develop`) to confirm the
active toolchain.

**macOS: `killed: 9` after copying `sky-out/sky`** — the kernel
caches code-signing. Run `codesign -s - sky-out/sky` after any
`cp` of the freshly built `rust/target/release/sky`.

**Slow first build / missing crate deps** — the first `cargo build`
downloads and compiles the dependency graph; subsequent builds are
incremental. If a fetch fails, retry with network access or check
`cargo` proxy settings.
