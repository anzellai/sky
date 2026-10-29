# The legacy compilers

Sky had three compilers before the current Rust compiler (`rust/`). All three
were removed from the tree in v0.29.0. They stay in git history.

**Tag `v0.28.1` is the last tag that contains them.**

| Directory | What it was |
|---|---|
| `legacy-ts-compiler/` | The first compiler, written in TypeScript. |
| `legacy-sky-compiler/` | The self-hosted compiler, written in Sky. |
| `legacy-haskell-compiler/` | The Haskell compiler. It carried Sky through v0.17, then served as the byte-for-byte differential oracle for the Rust rewrite. |

None of them was part of the released `sky` binary after the Rust rewrite.

## Read them

```bash
git show v0.28.1:legacy-haskell-compiler/src/Sky/Type/Solve.hs
git ls-tree -r --name-only v0.28.1 -- legacy-haskell-compiler
git archive v0.28.1 legacy-haskell-compiler | tar -x -C /tmp/sky-legacy
```

Source citations of the form `legacy-haskell-compiler/src/...:NNN` in the
live docs and in code comments refer to the tree at `v0.28.1`.

## What changed in the gates

- `xtask build-run --bless` wrote a stdout golden only when the Rust output
  matched the oracle's. It now writes one when two Rust captures agree, the
  capture is not empty and it carries no machine-specific leak. The committed
  `.stdout` diff is reviewed like any other change.
- `xtask welltyped` was a Rust-against-Haskell differential over generated
  well-typed programs. It now asserts that the Rust compiler accepts every
  generated program, which is the property a well-typed generator can check
  without a second compiler.
- `known-divergences.toml` is the frozen record of the Rust-against-Haskell
  differences found while the oracle existed.

## Licence

Some files in `legacy-haskell-compiler/src/Sky/` are derivative works of
elm/compiler (BSD-3-Clause). `NOTICE.md` keeps their list and the licence
text, because the files are still distributed in the git history and in the
source archives of tags up to and including `v0.28.1`.
