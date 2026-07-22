# v1-Blocker Closure — PR #154 (`rewrite/rust-compiler`)

Multi-session tracker for closing the v1 blockers before merging the Rust
compiler into `main`. Scope decided 2026-07-22: **CLOSE 5 + auto-TCO +
multiline-interp**, merge held; server-ci + known-divergences gate are
post-merge follow-ups. Import-cycle posture: **Elm-like reject** (verified 0
cycles across 86 example + 77 stdlib modules — breaks nothing).

Source analysis: 21-agent readiness audit + 19-agent feasibility workflow
(2026-07-22). See memory `rust_v1_merge_readiness_2026_07_22`.

## Phase A — CLOSE bucket (parity-safe, no emitted-Go change)

| # | Item | Crate(s) | Status |
|---|------|----------|--------|
| A1 | **fmt comment-safety** — verify the multiset gate already ships | fmt | ✅ already shipped (`fmt/src/lib.rs:61` `is_safe` 4-part gate; `format_source` falls back to lossless reprint; audit claim was wrong) |
| A2 | **s8 forbidden-pattern gate** — Result String / Task String / Std.IoError / RemoteData, as an xtask gate + wired into CI | xtask, CI | ✅ `xtask s8` (`s8_gate.rs`) + rust-ci.yml step; PASS 244 files/0 |
| A3 | **char-literal strictness** — reject `''` / `'ab'` / `'\x41'` at parse (raw inner-structure check) | syntax | ✅ grammar.rs `valid_char_literal`; parse-test + roundtrip green |
| A4 | **import-cycle rejection** — Elm-like E1010 at check time (SCC>1, first-party). Posture: reject (verified 0 cycles) | ty | ✅ check.rs + sig.rs `app_import_cycle_groups`; 2 tests; infer 49/49 + reject 59/59 clean |
| A5 | **nvim 17/17 LSP gate** — real Neovim client via `xtask lsp` + CI (rhysd/action-setup-vim) | xtask, CI | ✅ `lsp_gate.rs`; 17/17 PASS vs Rust `sky lsp` |

## Phase B — STRETCH (parity-sensitive; each its own commit + re-verify)

| # | Item | Crate(s) | Status |
|---|------|----------|--------|
| B1 | **auto-TCO** — GoStmt::Loop/Continue/Assign + tail-detection walk + clobber-safe reassignment | lower/ir, codegen | 🔄 impl 8fd93e8b (deep-recursion no overflow, build-run 40/40, coerce-floor neutral); grilling |
| B2 | **multiline {{expr}} interpolation** — route bodies through the real expr parser (sub-parser + green-subtree remap) | syntax, hir | ✅ 61c297ce; roundtrip 167/167; `{{String.fromInt n}}`→42 (was nil); build-run green |

## Parity guardrail (end-of-batch re-verify)

`cargo test --workspace` · `xtask build-run --all --run` · `xtask roundtrip` ·
`xtask coerce-floor` (re-bless after B1 with justification) · golden gate.

## Deferred (post-merge follow-up PRs)

- **server-shape CI verification** — bless-time HTML↔oracle parity unproven; only
  GET-render (not dead-click). Prove parity first or reduce scope.
- **known-divergences.toml enforcement gate** — L; honest c3 classification needs
  server-runtime evidence (§8 wire-decode floor). Land ledger doc + DoD re-scope
  only.
- **Not-v1-even-after-this**: any-typed ADT constructor params + missing TEA Msg
  decode/dispatch (server wire-decode floor); dead-click coverage; oracle removal.
