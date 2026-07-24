# Known divergences — the Rust-vs-oracle ledger

The Rust compiler (`rust/`, the primary toolchain) is validated against the
**Haskell oracle** (`legacy-haskell-compiler/`, built as `sky-out/sky`) by
differential testing: same Sky input, compare the emitted Go, the accept/reject
decision, and the diagnostics. The working assumption is **Rust output ==
oracle output**.

That assumption is not 100% true on purpose. A handful of places differ
deliberately — usually because the Rust compiler is stricter and fixes an
oracle shortcut. `known-divergences.toml` (repo root) is the **authoritative
ledger** of every one of those intentional differences.

## The contract

> The Rust compiler matches the oracle **except** for the entries in
> `known-divergences.toml`. Any **unlisted** divergence is a **bug**.

This is the M8 definition-of-done paper trail. It matters most at v1: once the
oracle is retired we can no longer diff against it, so this ledger is the
permanent record of exactly how the Rust compiler departed from the thing we
deleted — nobody later has to wonder "was this difference intended?".

## Directions

A divergence is one of:

| `direction` | meaning | where it's caught otherwise |
|---|---|---|
| `rust-stricter` | oracle ACCEPTS, Rust REJECTS | **nowhere else** — falls between accept-parity (Rust emits no Go to byte-match) and reject-parity (the oracle doesn't reject it). This is exactly what the ledger + `xtask divergences` exist for. |
| `rust-lenient` | oracle REJECTS, Rust ACCEPTS | `xtask reject` — tag the fixture `-- gate: known-leniency` in `crates/ty/tests/reject/corpus/`. Currently none active. |
| `equivalent-output` | both accept, emitted Go differs but is value-equivalent | `golden` / `build-run` compare by value where needed (e.g. float literals are compared numerically because the oracle re-renders `0.05` as `5.0e-2`). |

## How the ledger is enforced

- **`xtask divergences`** (CI gate) — for every `rust-stricter` entry, re-runs
  the Rust checker on the entry's fixture, in-process against the real stdlib,
  and asserts it still REJECTS with the ledgered diagnostic code. It also
  cross-checks that every fixture under
  `rust/crates/xtask/divergence-fixtures/` is documented in the ledger, so the
  two can't drift. If Rust ever stops enforcing the divergence (a regression),
  this gate fails instead of the change sliding through silently.
- **`xtask reject`** — the `rust-lenient` direction (reject-parity).
- **`xtask infer` / `golden` / `build-run`** — accept-parity + emitted-Go
  byte-match (40/40 deterministic examples).

## Authoring an entry (do NOT skip the probe)

Never ledger a divergence you have not observed. Verify empirically with a
differential probe against the **absolute** oracle path:

```sh
RUST=$(command -v sky)                 # or the built rust binary
ORACLE=/abs/path/to/sky/sky-out/sky    # the Haskell oracle — ABSOLUTE path
# build the same project with each; record ACCEPT (exit 0) / REJECT + code.
( cd fixture && "$ORACLE" build src/Main.sky; echo "oracle=$?" )
( cd fixture && "$RUST"   build src/Main.sky; echo "rust=$?" )
```

Sanity gate: the program must be one a reasonable user would write and the
outcome must be genuinely different between the two compilers — otherwise it is
a fabricated gap, not a divergence. Then:

1. add a fixture under `rust/crates/xtask/divergence-fixtures/<name>.sky` with a
   machine-readable header:
   `-- divergence: <id> code=<CODE> rust=<REJECT|ACCEPT>`
2. add the matching `[[divergence]]` block to `known-divergences.toml` with the
   rationale + the verification date;
3. run `cargo run -p xtask -- divergences` — it must PASS.

## Current entries

As of 2026-07-24, Rust and the oracle are in strong parity — there is exactly
one active behavioural divergence:

- **D001 — export enforcement on stdlib.** `import Sky.Core.List exposing
  (appendHelp)` (a module-private stdlib helper) is ACCEPTED by the oracle
  (kernel-module exemption) and REJECTED `[E1011]` by Rust, which enforces the
  `exposing` boundary uniformly. Intentional hardening.
