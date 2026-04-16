# Development

Building Sky from source — for contributors, language-tooling work,
or anyone who wants to run the compiler before a release lands.

## Prerequisites

- **GHC 9.4.8** — pinned; other `9.4.x` versions should work but CI
  runs 9.4.8.
- **Cabal 3.10+** — via [ghcup](https://www.haskell.org/ghcup/) or
  your distro.
- **Go 1.21+** — required both to build `sky-ffi-inspect` and at
  runtime (Sky compiles to Go and invokes `go build`).

Verify:

```bash
ghc   --numeric-version   # 9.4.8
cabal --numeric-version   # 3.10+
go    version             # 1.21+
```

## Local build (one shot)

```bash
./scripts/build.sh --clean
```

This produces:

- `sky-out/sky` — the Sky compiler (Haskell). **The only artefact
  end users need.**
- `bin/sky-ffi-inspect` — local dev copy of the Go helper. Optional;
  see "Embedded inspector" below.

Flags:

| Flag | Effect |
|------|--------|
| `--clean` | `rm -rf dist-newstyle/ sky-out/ bin/` first |
| `--self-tests` | Run `sky build` across every fixture in `test-files/` |
| `--sweep` | Clean-build every project under `examples/` |

## Quick rebuild (while hacking)

The full `scripts/build.sh` runs `cabal update` and clean-copies the
binary — overkill for iterative work. For a fast rebuild of just the
compiler:

```bash
cabal install --overwrite-policy=always --install-method=copy \
              --installdir=./sky-out exe:sky
# macOS: re-sign the copy so the kernel's code-signing cache
# doesn't flag the new binary
codesign -s - sky-out/sky
sky-out/sky --version
```

## Running tests

Three matrices, all must pass before a push:

```bash
# 1. Cabal suite — parsing, type-checking, codegen, LSP protocol,
#    audit-remediation specs, example sweep. ~25 minutes.
cabal test

# 2. Runtime Go tests — rt helpers, ADT shape, coercion, typed FFI,
#    security (CSRF, rate limit, auth secrets), session round-trip.
(cd runtime-go && go test ./rt/)

# 3. Self-tests — every fixture in test-files/ must build clean.
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

A `flake.nix` at the repo root pins GHC 9.4.8 + Go + every system
library the cabal transitive deps link against (gmp, libffi,
ncurses, zlib).

### Reproducible shell

```bash
nix develop
# Inside the shell you now have ghc, cabal, go, pkg-config on PATH.
./scripts/build.sh --clean
```

The shell's `shellHook` sets `SKY_RUNTIME_DIR` to the repo's
`runtime-go/` so in-tree builds resolve the runtime without the
embedded fallback.

### Build the compiler via Nix

```bash
nix build .#sky
./result/bin/sky --version
```

This runs the same `cabal install exe:sky` pipeline inside the Nix
sandbox and puts the result in `./result/bin/sky`. The
embedded-runtime and embedded-inspector splices still bundle the
Go source trees into the binary, so the result is a fully self-
contained executable.

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
dist-newstyle/                -- cabal's intermediate output
```

End-user install via `install.sh` or a released tarball only lays
down `sky-out/sky`. There is no separate `sky-ffi-inspect` binary
to install — it's embedded.

## Embedded inspector

`sky add` needs a Go-side helper (`sky-ffi-inspect`) to introspect
package APIs. Rather than shipping a second executable, Sky embeds
the helper's Go source via Template Haskell (alongside the runtime
and stdlib embeds) and materialises it to
`$XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<contentHash>/` on first
use. Resolution order inside the compiler:

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
(TH re-embeds the modified source) *and* the `bin/` copy so your
dev workflow picks the change up without paying the one-time
go-build on first use.

## Releases

`scripts/build.sh` produces the binary every release pipeline ships.
Before tagging:

1. `./scripts/build.sh --clean`
2. `cabal test`
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

**GHC version mismatch** — `cabal install exe:sky` fails if your GHC
is not 9.4.x. Use `ghcup install ghc 9.4.8 && ghcup set ghc 9.4.8`,
or enter `nix develop` for the pinned toolchain.

**macOS: `killed: 9` after copying `sky-out/sky`** — the kernel
caches code-signing. Run `codesign -s - sky-out/sky` after any
`cp`, or rebuild via `cabal install` which writes in place.

**Cabal can't find Aeson / hspec during test build** — you're on a
fresh checkout. Run `cabal update` once, then retry.
