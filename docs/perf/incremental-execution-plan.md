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
| 2 | Internal model descriptors and protected-field projections | Compiler/provenance and focused runtime tests; Chromium/WebKit/Firefox both modes, identical correctness journey | Complete; broader compiler gates pending |
| 3 | Conservative change evidence and safe reuse | Unknown mutation/effects, commands, handlers, context and panic boundaries | Restricted next-encoding cache validating; view reuse remains pending |
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

## Cycle 2 proof boundary

The driver grants resolved canonical codec IDs only while original stdlib source
identities and namespaces remain intact. Lowering accepts only a direct encoder
of the actual model parameter using those definitions and a literal blank CAF.
It emits an internal capsule retaining the exact original full encoder. An
editable generated-project marker or arbitrary callable does not establish
eligibility. A later public encoder builder revokes eligibility.

Skipping the previous full encode also skips unrelated fields, so the descriptor
must prove the ENTIRE model type has closed, acyclic serialization. Interfaces,
maps, pointers, opaque/foreign values, variants and recursive back-edges retain
reference execution initially. This is a metadata check, not a walk through large
instance data. It establishes no foreign method dispatch under canonical codec,
valid immutable representations and fixed codec metadata. It is not a general
application purity or total-encoding theorem.

When protected projections prove that no field can clear, the old full encode
has no sign-out result to contribute. If a clear is possible, projection fails,
or next encoding fails, the original previous encoder and decision still run.
This preserves the case where an unrelated invalid old Float prevents encoding.
Rendering, commands, subscription reconciliation, next full encoding and storage
writes remain unchanged in this cycle. Incremental storage/view work is pending.

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

## Cycle 2 evidence

The restricted lower proof, 35 split-generator unit cases, five real-project
proof scenarios and focused native runtime tests pass. A fresh compiler build
and all six real-browser journeys (reference/optimised × Chromium/WebKit/Firefox)
pass. For twenty typing messages, full-model encoding calls fall from40 to20,
while update/view/post/storage-write counts remain identical. Protected-field
projection visits40 fields at both retained sizes. Reports and measurement
conditions are under runs/step-cost-20261104/cycle2/. These are instrumented
single diagnostic runs, not a cross-device latency claim.

The previous phase's complete native runtime suite passed hosted CI. Its
coverage-accounting job identified a stale ledger for the new browser gate;
the owned-tree regeneration and both accounting checks now pass. The complete
lowering/project suites pass567 tests with one explicit dependency-harvest
ignore (run after the sweep fetches all example dependencies). Complete
compiler/example/real-app gates remain mandatory before
claiming the overall implementation verified. Cycle2 new runtime code still
requires the hosted complete runtime suite. No release is authorised here.

Storage design constraints and independent review findings are recorded in
[incremental-persistence-protocol.md](incremental-persistence-protocol.md). In
particular, unchanged local state still republishes under current multi-tab
semantics, and asynchronous storage cannot silently add a reload loss window.

## Cycle 3 restricted encoding witness

The compiler-proven canonical encoder may retain one successful next-model JSON
result up to the existing1.5MB persistence cap, together with its actual model.
A bounded sufficient identity check covers closed tagged records, Maybe,
primitive bits, immutable strings and the existing immutable List slice
representation. A list requires the same data pointer, visible length and nil
representation; a string requires shared bytes or bounded short equality.
Unknown values, different allocations, excessive array/record work and changed
values call the original encoder. No deep equality or approximate hash is used.

The cache binds to one app/encoder at boot, clears before a miss is encoded
(including failures), and resets after sign-out. Its JSON bytes and entry count
are bounded; its retained model can still reference a larger backing allocation
also reachable from that model. This relies on Sky's documented immutable-value
contract, including the FFI rule forbidding mutation of slice arguments. It does
not make foreign mutation safe.

Only next encoding is reused. The possible-clear previous full encode retains
its original failure semantics. Every write, identity decision, update, render,
Cmd and subscription phase still runs. Targeted tests cover bounded work at
1K/10K/100K empty entries, signed zero, slice allocation/length/nil differences,
Maybe changes, custom/open-model fallback, cache cap/reset/failure, denied-write
retry, snapshot removal, cross-tab republication and the previous-failure
sign-out case. Fresh compiler and all six browser/mode journeys now pass. Ignored-key
full encodes fall from20 to0 per ten messages, and typing full encodes fall
from40 to10 per twenty messages, with all original writes and phase counts
retained. Reports are under runs/step-cost-20261104/cycle3/.

The first broader example run used an unsupported generated-source ancestor
and was rejected by source discovery; its replacement was stopped before
optional cleanup could purge the shared Go cache. Both build and sweep scripts now accept `--keep-go-cache` to preserve
the shared cache without changing any verification. An additional build-script
cleanup path was missed and did purge the cache before that option was added;
subsequent runs must pass the option explicitly. No test is omitted. A complete sweep and isolated real-app build remain required.
