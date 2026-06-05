# Cycle 6 PC — Synchronous-panic site audit (v0.15.43)

Sky's compiler audit §3.5 + §9 flagged the synchronous-panic class
as a credibility-bar bug: every `main = Task.run …` shape that's
NOT a server (Sky.Cli, Sky.Tui, batch jobs, scheduled tasks) lacks
a defer/recover layer, so any Go panic that escapes the user's
code crashes the process with a Go stack dump.

v0.15.43 closes the class via two tiers:

1. **Tier 1** — `defer rt.LogPanicAndExit()` injected at the start
   of every emitted `func main()`. `panic_recover.go` centralises
   the recover→classify→log→exit(1) pipeline.
2. **Tier 2** — this audit. Every `panic(...)` site in
   `runtime-go/rt/` is classified and annotated in-source with
   either `REACHABLE-FROM-SKY:` or `COMPILER-BUG-CONTRACT:` so
   future audits can find them at a glance.

## Classifier

```
panic message contains                                    → kind                  bucket
─────────────────────────────────────────────────────────────────────────────────
rt.IntDiv: integer division by zero                       → DivisionByZero        reachable
rt.Rem: modulo by zero                                    → DivisionByZero        reachable
rt.Div: division by zero                                  → DivisionByZero        reachable
rt.AsInt: expected numeric value                          → TypeMismatch          reachable
rt.AsFloat: expected numeric value                        → TypeMismatch          reachable
rt.AsBool: expected bool                                  → TypeMismatch          reachable
rt.skyCallDirect: argument N type mismatch                → TypeMismatch          reachable
rt.cmp: type mismatch                                     → ComparisonMismatch    reachable
rt.Coerce: expected …                                     → CoerceFailure         reachable
rt.Coerce: slice element […]                              → CoerceFailure         reachable
rt.Coerce: map value […]                                  → CoerceFailure         reachable
rt.coerceInner: type mismatch                             → CoerceFailure         compiler-bug
runtime error: index out of range                         → IndexOutOfRange       reachable (Go runtime)
runtime error: invalid memory address / nil pointer       → NilDereference        reachable (Go runtime)
sky.Unreachable(...)                                      → CompilerBug           compiler-bug
Ffi.kernel "..." reached the runtime                      → CompilerBug           compiler-bug
(anything else)                                           → Unexpected            -
```

## Site-by-site

| File:line                          | Kind                  | Reachable?       | Disposition |
|------------------------------------|-----------------------|------------------|-------------|
| `rt.go:544`  rt.coerceInner        | CoerceFailure         | compiler-bug     | Annotated `COMPILER-BUG-CONTRACT`. Routing emitted `Coerce[T]` over a value HM never narrowed. |
| `rt.go:2049` rt.AsInt              | TypeMismatch          | reachable        | Heterogeneous slice / untyped FFI return. Top-level recover surfaces structured Err. |
| `rt.go:2100` rt.AsFloat            | TypeMismatch          | reachable        | Same shape as AsInt. |
| `rt.go:2137` rt.AsBool             | TypeMismatch          | reachable        | Same shape as AsInt. |
| `rt.go:2176` rt.Div                | DivisionByZero        | reachable        | `x / 0.0` from valid Sky. Sky exposes no NaN/Inf shape — safe behaviour is fail-fast. |
| `rt.go:2186` rt.IntDiv             | DivisionByZero        | reachable        | `n // 0`. |
| `rt.go:2194` rt.Rem                | DivisionByZero        | reachable        | `n % 0`. |
| `rt.go:2418` cmp                   | ComparisonMismatch    | reachable        | Cross-type ordering via untyped boundary. HM rejects in pure Sky; FFI any-bridge can leak. |
| `rt.go:3338` Ffi_kernel            | CompilerBug           | compiler-bug     | Stage-4 rewrite missed a wrapper shape. |
| `rt.go:4641` rt.Coerce slice elem  | CoerceFailure         | reachable        | Heterogeneous slice through typed narrowing. |
| `rt.go:4677` rt.Coerce map value   | CoerceFailure         | reachable        | Heterogeneous map through typed narrowing. |
| `rt.go:4730` rt.Coerce final       | CoerceFailure         | reachable + cb   | Mixed disposition — FFI-contract violation OR codegen routing bug. Both surface as `CoerceFailure`. |
| `rt.go:4858` Unreachable           | CompilerBug           | compiler-bug     | Exhaustiveness checker said impossible; codegen emitted over-broad case match. |
| `rt.go:6873` http.ErrAbortHandler  | (re-panic)            | by-design        | Go's net/http sentinel; the request-handler recover re-panics to let net/http abort cleanly. NOT a panic CREATION site — a re-panic. |
| `rt.go:8273` rt.skyCallDirect      | TypeMismatch          | reachable        | FFI boundary — Sky's any-typed value didn't match the typed Go FFI signature. |
| `live.go:3034` Sky.Live recover    | (re-panic)            | by-design        | Same shape as the http.ErrAbortHandler case — re-panic to let outer handler abort. |

## Counts

- **17 panic sites** in `runtime-go/rt/` (rt.go + live.go).
- **2 re-panic sites** (`http.ErrAbortHandler`-style) — not bugs; intentional pass-through to the outer net/http handler.
- **3 compiler-bug-contract** sites: `coerceInner`, `Ffi_kernel`, `Unreachable`. Top-level recover catches all three as `CompilerBug` and prompts the user to file an issue.
- **12 reachable-from-valid-Sky** sites: `AsInt`, `AsFloat`, `AsBool`, `Div`, `IntDiv`, `Rem`, `cmp`, `Coerce` (3 variants), `skyCallDirect`, and the two Go-runtime errors (`index out of range`, `nil deref`).

Every reachable site is now caught by `LogPanicAndExit` in the
synchronous main path, surfacing a structured `Sky panic: <Kind>
(ref <errId>) — <hint>` log line and exit 1. The Sky.Http.Server
handler defer (`rt.go:6863`) and the goroutine-context `safeGo`
helper handle the same panics on the server / Cmd.perform paths.

## Open architectural decisions

1. **`1 // 0` semantics — Err vs panic-with-recover?** Kept as
   panic. Rationale: Sky's `//` is a typed kernel binop with
   signature `Int -> Int -> Int`; returning `Err` would require
   widening the surface to `Int -> Int -> Result Error Int`,
   breaking every existing expression that uses `//`. The
   recover-based gate is the lower-friction path and matches the
   feel of every other typed language (Haskell `div`, OCaml `/`,
   Rust `/` all crash on zero). Users wanting fallible division
   write `Int.divChecked` (a new helper to add in v0.16) or
   guard with `if d == 0 then Err … else Ok (n // d)`.

2. **Should `rt.AsInt` widen to `Result Error Int`?** Same
   answer: no, and for the same reason. `AsInt` is the typed
   narrowing primitive, not a user-facing decoder. Users who want
   a `Maybe Int` from a string get it via `String.toInt`; the
   `AsX` family is the runtime-only floor and stays panic-typed
   under the gate.

## Verification anchors

- `runtime-go/rt/panic_recover_test.go` — 13 unit tests covering
  every classification bucket + the errId / stack-tail shape.
- `test/Sky/Build/MainPanicRecoverSpec.hs` — cabal spec pinning
  the `defer rt.LogPanicAndExit()` codegen anchor and an
  end-to-end div-by-zero exit-1 + structured-log check.

## Sample output

A user's CLI doing `n // 0` at top level today prints:

```
2026-05-31T14:54:28.065604Z ERROR Sky panic: DivisionByZero (ref 49a4211c) — Integer or float division by zero. Guard the divisor with `if d == 0 then Err … else Ok (n // d)` before the operation. errId=49a4211c panicKind=DivisionByZero panicMsg=rt.IntDiv: integer division by zero hint=Integer or float division by zero. Guard the divisor with `if d == 0 then Err … else Ok (n // d)` before the operation. stackFrame=/tmp/sky-panic-test/sky-out/rt/panic_recover.go:51 +0x2c | panic({0x105bbc480?, 0x105d31ee0?}) | /nix/store/.../runtime/panic.go:860 +0x12c | sky-app/rt.IntDiv(...) | /tmp/sky-panic-test/sky-out/rt/rt.go:2186 +0x80 | main.main() | /tmp/sky-panic-test/sky-out/main.go:108 +0x54
```

— exit code 1, no Go-runtime stack header, actionable hint, errId
for log correlation. Same shape regardless of which reachable
panic site fires. `SKY_LOG_FORMAT=json` produces the JSON variant.
