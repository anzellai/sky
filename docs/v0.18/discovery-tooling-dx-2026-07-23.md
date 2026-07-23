# Discovery sweep — tooling / DX (2026-07-23, batch 2)

A 5-prober + adversarial-grill workflow probed LSP / diagnostics / formatter /
exhaustiveness / parser (surfaces the soundness sweep didn't cover deeply). 9
grill-confirmed findings; 5 closed, 1 at-parity, 3 carried to the next batch.

## Closed

| # | Finding | Class | Fix | Regression |
|---|---------|-------|-----|-----------|
| 1 | Rename/goto of a param/lambda/case binder **corrupted source** — the binder span used the pattern NODE range (leading whitespace), so `pick maybeVal`→`pick mv` produced `pickmv` | **lsp (source corruption)** | `cst::first_lower_tok` — record the ident TOKEN range | `param_rename_span_starts_at_the_ident_not_the_space` + nvim 17/17 |
| 5 | E1002 duplicate-definition had no source location / caret | diagnostic | label the redefinition span | verified (build repro) |
| 6 | E1010 import-cycle + E2007 arity fell through to a generic `-- ERROR --` header | diagnostic | `code_title` → `IMPORT CYCLE` / `ARITY MISMATCH` | verified |
| 8 | E1006 float-pattern caret pointed at the `case` scrutinee (span included leading trivia) | diagnostic | `cst::sig_range` trims to the first significant token; both float arms | verified |
| 9 | **Unterminated string literal silently accepted** (lexer auto-closes at newline; type-checks + go-builds) | **parser (check≢build)** | `is_terminated_string` check in the atom + pattern parsers → E0001 | reject corpus `unterminated_string.sky` |

## At parity (not a finding)

- **7 E1001 undefined-name** — the finding claimed a missing "actionable help line", but
  Rust and the oracle both show the location + `Undefined name: <n>` with a caret;
  they are equivalent. No change.

## Closed (batch 2b)

| # | Finding | Class | Fix | Regression |
|---|---------|-------|-----|-----------|
| 4 | Arity / over-application gave a generic E2001 clash + cascade; E2007 never constructed | diagnostic | arity gate in `Expr::Call` — a named callee over its resolved (alias-unfolded) arrow count emits `[E2007] <name> declared N-arg, called M`, recovering to suppress the cascade | reject corpus `arity_over_application.sky`; infer 49/49 |

## Carried to next batch (M effort)

- **2 Hover expands type aliases** to their structural record form (loses the
  user-written `User` name). `ty` expands aliases eagerly (unify.rs), so the alias
  identity is gone by the `value_sig` layer. Fix = render the ANNOTATED def's sig
  syntactically from the AST type node (which still says `User`). Scoped to sky-lsp
  + a name-preserving printer.
- **3 Hover field ordering inconsistent** — the signature path shows declaration
  order, the parameter/inferred path shows alphabetical (`Ty::Record` is sorted for
  row-poly unify). Rendering the alias name (fix #2) closes this simultaneously.
_(4 closed above.)_
