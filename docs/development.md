# Development

> **Compiler**: Sky's compiler is written in **Rust** (cargo workspace
> at `rust/`, crate `sky` builds the `sky` binary). The retired
> Haskell compiler lives under `legacy-haskell-compiler/` for
> historical reference. Type-directed lowering, Go generics on
> parametric record aliases, Layer-3 stdlib, and whole-program DCE all
> carry over; runtime verification runs across ~50 examples. See
> [`history/compiler/versions.md`](history/compiler/versions.md) for the changelog.


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

This runs `cargo build --release -p sky` and produces:

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
just the compiler, build the `sky` crate directly:

```bash
( cd rust && cargo build --release -p sky )
cp rust/target/release/sky sky-out/sky
# macOS: re-sign the copy so the kernel's code-signing cache
# doesn't flag the new binary
codesign -s - sky-out/sky
sky-out/sky --version
```

Debug builds (`cargo build -p sky`, no `--release`) compile
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

Two files at the repo root hold the Nix support, and stable Nix needs
no flakes:

- `default.nix` holds every piece: `package` (a `callPackage`-able
  function that builds Sky), `devShell` (a `callPackage`-able function
  for the dev shell) and `overlay` (`final: prev: { sky =
  final.callPackage package { }; }`). Called with no arguments it builds
  against the nixpkgs pins in `flake.lock`, so there is one pin source.
- `flake.nix` is a thin wrapper that exports the same pieces:
  `overlays.default`, `packages.<system>.{sky,default}`,
  `devShells.<system>.default` and `apps`.

### Build the compiler

```bash
nix-build                  # stable Nix
nix build .#sky            # flakes
./result/bin/sky --version
./result/bin/sky-ffi-inspect strings   # the FFI inspector, beside sky
```

This runs `cargo build -p sky` (through `rustPlatform.buildRustPackage`)
in the Nix sandbox. The binary embeds the stdlib, the Go runtime, the
templates, the bundled apps and the inspector source, so it is
self-contained. `sky` builds programs with `go`: a Go on your `PATH`
wins, and the package puts its own Go 1.26 after it as a fallback.

The version is `[workspace.package] version` in `rust/Cargo.toml`,
the one version source, and `sky --version` prints it. A flake build
adds the commit (`sky v0.27.5 (1a2b3c4)`); `nix-build` has no git
information and prints the plain version.

### Reproducible shell

```bash
nix-shell -A shell     # stable Nix
nix develop            # flakes
# Either gives cargo, rustc, rustfmt, clippy, rust-analyzer, go 1.26,
# pkg-config, make, curl, jq and git.
./scripts/build.sh --clean
```

The shell sets `SKY_RUNTIME_DIR` to the repo's `runtime-go/` so
in-tree builds resolve the runtime without the embedded fallback.

### Use the overlay

```nix
let
  sky = import (fetchTarball "https://github.com/anzellai/sky/archive/main.tar.gz") { };
  pkgs = import <nixpkgs> { overlays = [ sky.overlay ]; };
in
pkgs.sky
```

`package` takes a `go_1_26` argument. A nixpkgs without `go_1_26`
must supply one: `pkgs.callPackage sky.package { go_1_26 = <a Go 1.26>; }`.

### Ad-hoc run

```bash
nix run .#sky -- build src/Main.sky
```

CI builds both entry points nightly (`nightly-sweep.yml`, job `nix`)
and in the release gate (`release.yml`, job `gate-nix`), through
`scripts/ci/nix-build-check.sh`.

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
Resolution inside the compiler is **one step, not three**.
`ffi::ensure_inspector` (`rust/crates/ffi/src/inspect.rs:329-351`) is the only
resolver — three call sites, all in `project/src/ffi_ops.rs`. It goes straight
to `<repo_root>/tools/sky-ffi-inspect`, content-hashes the sources, and
`go build`s into `$XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<hash>/`, returning
the cached binary if it is already there.

Content-hash keying means `sky upgrade` auto-invalidates stale
cached helpers — no manual cleanup required.

> **Two probes documented here never existed in the Rust compiler.** This
> passage used to list a three-step order: `$SKY_FFI_INSPECTOR` override, then
> `bin/sky-ffi-inspect` walking up from the cwd ("**contributor workflow** hits
> this"), then the embedded fallback. Only the third is real —
> `grep -rn 'SKY_FFI_INSPECTOR\|bin/sky-ffi-inspect' rust/crates --include='*.rs'`
> returns nothing.
>
> The practical consequence is the one that wasted time: `scripts/build.sh:88`
> still writes `bin/sky-ffi-inspect`, and **nothing consumes it**. The old
> instruction to "rebuild the `bin/` copy so your dev workflow picks the change
> up" had no effect. If you edit `tools/sky-ffi-inspect/`, the content hash
> changes and the next FFI operation rebuilds the cached helper on its own —
> that is the whole workflow.

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
