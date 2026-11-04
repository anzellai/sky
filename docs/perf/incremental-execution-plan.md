# Incremental execution: implementation and validation

This is the execution ledger for the architecture in
[the research report](incremental-execution-research-2026-11-04.md).
User authorisation: autonomous end-to-end Plan, Implement, Validate cycles.
No release tag or application deployment is part of this compiler work.

## Invariants

Every message still executes its update and commands exactly once under the
existing scheduler. Unknown effects and dependencies use reference execution.
Optimisations preserve classified failures, identity/session precedence,
handler captures, focus/caret/composition and each target's context inputs.
No debounce, dropped events, approximate equality or silent persistence loss.
Existing applications require no new annotations or builder calls.

The baseline is released v0.29.2. Read the compiler architecture, typed Go IR,
runtime narrowing taxonomy and verification matrix before changing lowering.
Do not build from or modify the owner's detached root checkout. Use the locked
Nix toolchain, the unchanged memory guard, and one heavy local job at a time.
Never print dotenv contents or environment values. Use synthetic fixtures.

## PIV cycles

| Cycle | Deliverable | Validation | State |
|---|---|---|---|
| 0 | Architecture consultation and executable invariants | Source review against released baseline | Complete |
| 1 | Public large-model fixture, phase counters and reference path | Reference runs pass Chromium/WebKit/Firefox at 1K/10K retained entries, fixed small view; other size/context dimensions remain for later gates | Complete |
| 2 | Internal model descriptors and protected-field projections | Custom encoder fallback, encoding failure and session/sign-out equivalence | Pending |
| 3 | Conservative change evidence and safe reuse | Unknown mutation/effects, commands, handlers, context and panic boundaries | Pending |
| 4 | Generated encoding and transactional incremental persistence | Differential codec tests, crash/fault injection, reload, migration, identity fencing and readers | Pending |
| 5 | Automatic view-region reuse and collection changes | Reference UI/effect traces, bounded cache retention and dependent work counters | Pending |
| 6 | Cross-target end-to-end gates and independent review | Runtime/compiler suites, full examples, real app, browsers and available native/terminal targets | Pending |

Cycle boundaries record the exact implementation, evidence, failures and
remaining limitations. A completed cycle is not a claim that all model sizes
or targets have passed. Counter evidence must precede latency claims.

## Mechanism

The existing compiler's typed `GoExpr`/`GoTy` representation and private
`rt.SpaFns` dispatch table allow internal descriptors without adding an
application-facing API. Auto-split already resolves model fields, session
projections and codecs. Descriptor eligibility must be established by semantics,
not generated names or a return type. Arbitrary custom encoders keep the full
reference path. Mutable foreign values cannot receive unchanged witnesses.

Persistent Array/Dict/List internals are retained. New identity logic must cover
Array tails, Dict metadata and closure environments. VNode descriptions and
per-mount mutable state must remain separate. Session metadata writes are not
suppressed by model reuse.

## Required evidence

Record source commit, fixture, safe toolchain versions, target, browser/device,
phase counts and elapsed measurements. No environment dumps. Test effects through
a deterministic synthetic scheduler, never by duplicating live service calls.
Keep reference and optimised modes comparable under the same external inputs.

No formal end-to-end guarantee is claimed until proof obligations are connected
to the implementation and trusted target adapters. Differential/property tests
are evidence and counterexample searches, not a universal proof.

## Cycle 1 evidence

Fresh instrumented compiler build PASS under locked Nix and the unchanged
memory guard. Portable reference persistence tests PASS. Real wasm reference
fixture PASS on Chromium, WebKit and Firefox. Ten letters execute20 messages,
40 full model encodes and20 storage writes at both data sizes. JSON reports
under runs/step-cost-20261104 record numeric diagnostics and conditions.
An initial WebKit harness assertion used layout-dependent innerText; corrected
to exact DOM textContent, then all three engines reran cleanly. No application
assertion was weakened. This cycle adds instrumentation, not a speedup.

Local host-test resource note: compiling the entire runtime test package hit
the unchanged swap guard three times, even with reduced compiler heap and
parallelism. This is not a passing full-suite result. A staging copy containing
ALL production runtime source plus five byte-identical selected persistence
and transition test files passes. Their SHA256 manifest is retained in
.skycache/piv/runtime-focused/focused-test-sources.json. Full native runtime
coverage remains mandatory on a runner that can compile the complete package.
Browser fixture builds and actual wasm tests pass on the local machine.
