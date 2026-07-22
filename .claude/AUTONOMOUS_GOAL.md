# AUTONOMOUS MANDATE — v1-blocker closure before merging the Rust compiler

**Set:** 2026-07-22. **Branch:** `rewrite/rust-compiler`. **Mode:** fully
autonomous. Directive: **"don't stop midway."**

> Supersedes the 2026-07-18 M0→M4 rewrite mandate (that work shipped — the Rust
> compiler builds+runs the corpus at oracle parity: 265/265 tests, 40/40 oracle
> byte-match). PR #154 is open (merge-with-caveats). This mandate closes v1
> blockers before that merge lands.

## Verbatim user goal (the authority on "done")

> ok use agents + grilling + autonomous mode please. don't stop midway, until
> getting whole repo tested + verified + ready for merge, get as close to v1 as
> possible with options we picked.

## The picked options (AskUserQuestion, 2026-07-22)

- **Scope**: CLOSE 5 + auto-TCO + multiline-interp. Merge (PR #154) HELD until done.
- **Import-cycle posture**: Elm-like REJECT (verified 0 cycles across 86 example +
  77 stdlib modules — breaks nothing).
- server-shape CI verification + known-divergences enforcement gate = post-merge
  follow-up PRs (NOT in this mandate).

## Definition of done — all on `rewrite/rust-compiler`, all verified green

- **A1 fmt comment-safety** — already shipped (verified `is_safe` 4-part gate). ✅
- **A2 §8 forbidden-pattern gate** — `xtask s8` + CI. ✅ (f7a6f615)
- **A3 char-literal strictness** — reject `''`/`'ab'`/`'\x41'`, oracle parity. ✅ (impl done, test+commit pending)
- **A4 import-cycle rejection** — Elm-like E-code at name resolution (SCC>1, first-party).
- **A5 nvim 17/17 LSP gate** — real Neovim client wired to xtask + CI.
- **B1 auto-TCO** — user tail-recursion → `for{}`/continue; no stack overflow on
  well-typed Sky. Port `legacy-haskell-compiler/src/Sky/Build/TailCallOpt.hs`.
  Re-bless `coerce-floor` (with written justification).
- **B2 multiline `{{expr}}` interpolation** — route bodies through the real
  expression parser; scope = parser-routing only (auto-toString OUT); roundtrip-clean.

## Final gate (whole repo, before "done")

`cargo test --workspace` · `xtask` {roundtrip, resolve, infer, reject, build-run,
coerce-floor, repro, s8} · golden gate · `build-run --all --run` oracle parity.
Then an INDEPENDENT Judge agent verifies against this file before I declare done.

## Honest ceiling (state, never hide)

Even at done this is **merge-primary, oracle-retained — NOT a v1 tag**. Remaining
post-merge: server wire-decode floor (any-typed ADT ctor params, TEA Msg
decode/dispatch), dead-click coverage, oracle removal.

## Resume protocol (if compacted / new session)

1. Read THIS file + `docs/v0.18/v1-blocker-closure-pr154.md` (the tracker).
2. `git log --oneline -20` on `rewrite/rust-compiler` — last committed item = resume point.
3. Continue the next unchecked item A3→A4→A5→B1→B2, then the final gate + Judge.
   Do NOT narrow the scope. The oracle + corpus + xtask gates are acceptance truth.
