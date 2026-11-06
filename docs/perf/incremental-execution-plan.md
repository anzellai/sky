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
| 2 | Internal model descriptors and protected-field projections | Compiler/provenance and focused runtime tests; Chromium/WebKit/Firefox both modes, identical correctness journey | Complete; broad compiler/example/hosted gates passed at 2365815 |
| 3 | Conservative change evidence and safe reuse | Unknown mutation/effects, commands, handlers, context and panic boundaries | Restricted cache and automatic view-region implementation validated locally; final integration pending |
| 4 | Generated encoding and transactional incremental persistence | Differential codec tests, crash/fault injection, reload, migration, identity fencing and readers | Bounded synchronous v2 closed-field path validated in all three browser engines. Mixed and recursive closed record/Maybe paths validated in all three browser engines; pinned lazy reconstruction for raw-budget eviction validated locally. General incremental storage remains pending |
| 5 | Automatic view-region reuse and collection changes | Reference UI/effect traces, bounded cache retention and dependent work counters | Whole-region implementation and native/Wasm/compiler proofs validated; full-app browser gate pending |
| 6 | Cross-target end-to-end gates and independent review | Runtime/compiler suites, full examples, real app, browsers and available native/terminal targets | Six target builds, native execution checks, full TUI gate and all 40 hosted CI jobs pass; private whole-app browser matrix remains pending |

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
of the actual model parameter using those definitions and a literal blank CAF (including resolved pure literal aliases).
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
pass. For twenty typing messages, full-model encoding calls fall from 40 to 20,
while update/view/post/storage-write counts remain identical. Protected-field
projection visits 40 fields at both retained sizes. Reports and measurement
conditions are under runs/step-cost-20261104/cycle2/. These are instrumented
single diagnostic runs, not a cross-device latency claim.

The previous phase's complete native runtime suite passed hosted CI. Its
coverage-accounting job identified a stale ledger for the new browser gate;
the owned-tree regeneration and both accounting checks now pass. The complete
lowering/project suites pass 567 tests with one explicit dependency-harvest
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
result up to the existing 1.5 MB persistence cap, together with its actual model.
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
full encodes fall from 20 to 0 per ten messages, and typing full encodes fall
from 40 to 10 per twenty messages, with all original writes and phase counts
retained. Reports are under runs/step-cost-20261104/cycle3/.

The first broader example run used an unsupported generated-source ancestor
and was rejected by source discovery; its replacement was stopped before
optional cleanup could purge the shared Go cache. Both build and sweep scripts now accept `--keep-go-cache` to preserve
the shared cache without changing any verification. An additional build-script
cleanup path was missed and did purge the cache before that option was added;
subsequent runs must pass the option explicitly. No test is omitted. A complete
sweep now passes all 38 examples with runtime checks; the isolated real-app
`web:app` build also passes. The explicit dependency-harvest test is now running
after the sweep fetched and mirrored the dependencies.

## Broader codec domain: review findings

The isolated FacePlan `web:app` build passes with the cycle 3 compiler. The
generated encoder receives no proof capsule: its blank is an alias to the
application's initial-model binding. Following that alias is admissible only
when resolved definitions prove a terminating literal-data graph. Do not infer
purity from its name, annotation or generated-project flag. Broader runtime model
representations still require evidence even after the encoder shape qualifies.

Two implementation facts constrain the next phase:

- `GoTypeDef::SealedIface` currently emits only public `SkyVariantTag` and
  `SkyVariantName` methods. The Go interface admits foreign implementations.
  A schema must identify exact compiler-generated concrete types and payload
  schemas. Interface implementation, names and method results are insufficient.
- Strict `Codec.auto` construction calls registered factories through
  `codecAutoTypePartialAt` and `adtVariantZeros`. Those callbacks may have
  effects or fail. Pure actual model variants do not prove pure construction.
  Either retain exact construction per reference attempt or introduce a
  compiler-owned immutable metadata path with its own equivalence proof.

A possible intermediate split retains exact construction and caches only
certified value encoding. Generated construction/finish callbacks would need
resolved-HIR validation against the same canonical codec and blank, preserving
the original full callable for fallback. Changing enum metadata must invalidate
encoding certificates, or metadata must be proven immutable.

Value conversion and JSON marshalling are separate phases today: the runtime
first converts every record field, then marshals the resulting ordered object.
Foreign variant methods can run during conversion; foreign JSON marshalers can
run later. Encoding each field completely in a single pass would reorder those
effects. Any field-chunk encoder must preserve the two phases and stop at the
same failure, without rerunning effectful conversion during fallback.

Typed Dict values now have a local implementation of unchanged-root reuse over
their private immutable structure, with integrated browser validation pending
as described below. Changed roots still use full canonical conversion. Erased
`SkyArray` leaves cannot be certified from their Go type alone, and existing
canonical auto-codec support must first be established. Construction/ingress
certificates and persistent-node propagation need separate PIV regressions.
These are next-phase obligations, not implemented features or reasons to widen
the current conservative guard.

## Enum metadata invalidation

A new regression exposed a stale-cache result after `RegisterEnum` changed a
registered constructor name. The original cache fails that regression. Enum
registration now publishes an immutable registry snapshot atomically. Cache
entries retain the snapshot pointer and require the same snapshot on a hit;
metadata changes during full encoding prevent cache population. This replaces
the cache's assumed fixed enum metadata with an executable invalidation rule.

Registration keeps last-registration-wins and present-empty-table semantics.
It now copies the caller's name slice: later caller mutation cannot change a
registered wire representation. That is explicit ownership hardening of the Go
runtime API, not preservation of its previous borrowed-slice behaviour.

All focused runtime tests pass after the fix. The exact production registry
block and unchanged registry tests also pass ten runs under Go's race detector;
that focused extraction is not a claim that the complete local runtime package
ran with race detection. Independent review found no blocker. Publication copies
the registry map, so many distinct startup registrations have cumulative
quadratic map-copy work; broad startup performance remains unmeasured. Reads
require an atomic snapshot load, avoiding a new per-element reader lock.

Fresh compiler installation using `scripts/build.sh --dev --keep-go-cache`
and all six reference/optimised browser journeys pass after metadata
invalidation. The compiler uses the repository's optimised development profile
with assertions and overflow checks enabled; two release compilations were
terminated without a guard diagnostic. Budgets and the guard were not relaxed.
Numeric reports and conditions are in runs/step-cost-20261104/cycle3-metadata/.
The same work counts survive this fix; single instrumented timings are not
claimed as statistically established latency improvements.

The CI encoder-wiring regression was reproduced locally, then corrected to
assert both the generated private setter and its kernel binding. All other
SSR, database-isolation, codec and protected-field assertions remain. The
corrected real build test passes. Configuration-census regeneration changes
only two source-location details, with its summary unchanged. The dependency
harvest and full 84-row narrowing gate also pass after the example sweep.

Configuration census, denominators and coverage-ledger checks pass after these
fixes. Both prior CI failures are addressed; the new head still needs its full
hosted workflow result.


## Cycle 4 bounded synchronous field path

The first storage implementation preserves the synchronous v2 localStorage
format and 1.5 MB post-exclusion limit. A successful original full encode plus
closed-type proof establishes per-field encoding validity, exact byte lengths
and bounded retained field bytes. Later steps compare bounded sufficient field
witnesses and encode changed fields, including excluded fields whose failures
must still invalidate the full result. Unknown types, metadata epochs and
projection limits retain the original encoder. Pure literal blank CAF aliases
are resolved with cycle detection and a 4096-expression budget.

An invalid full next model never changes storage identity. An invalid previous
model still prevents sign-out. Identity uses the existing alias precedence and
number-preserving canonicalisation; sign-out keeps its distinct exact-key
contract. Previous validity is reusable only for the actual previous immutable
model and the metadata epoch still current AFTER publication. A storage callback
that changes enum metadata therefore forces the original previous encoder.

Valid oversized snapshots perform the original identity/removal/lifetime rules
without building a whole JSON buffer. Bounded snapshots reconstruct exactly the
original bytes and retain one payload. Unchanged persisted fields reuse those
bytes. One browser string can also be retained with its actual immutable Go
owner; boot, identity removal and sign-out reset it. EVERY original storage write
still calls setItem, including retries and republication after another tab or
removal. There is no asynchronous durability window or new storage protocol.

This is a partial scaling improvement. Changed snapshots below the cap still
join and write all persisted bytes. Changed large fields still require their
full encoding. Large protected identity projections use reference fallback.
Models containing ADTs, erased/foreign values or unsupported containers still
use reference execution. FacePlan's broader model is not yet covered, and view
execution is unchanged. General incremental storage, broader compiler-owned
metadata and automatic view/collection work remain required.

Numeric counters distinguish original full calls, certified field steps,
individual field encodes/bytes, materialized payload bytes and wasm-to-JS string
bytes. Identity reconstruction counts its field encodes too. encodeNanos covers
full/field derivation and cold ingestion; writeNanos includes identity,
reconstruction and the storage adapter; sessionNanos covers null projections and
sign-out decisions. These are phase timers, not exclusive attribution of every
field encode to encodeNanos. Profiling remains opt-in.

Focused differential tests cover reference-equivalent storage calls, bytes,
lifetime state and sign-out through identity A/B changes, invalid floats,
exclusions, oversized values, repairs and removed snapshots at 1K/10K/100K
retained entries. The expanded browser gate also exercises a 6 MB model and
200 visible rows. Passing these gates is evidence for this restricted path,
not a universal correctness or cross-device latency claim.

### Nominal container correctness prerequisite

Ordinary Sky records may legally contain fields named tag, justValue, okValue,
errValue, skyName and fields. Runtime classification previously mistook some
such records for Maybe/Result/ADT containers, potentially dropping data during
encoding or rejecting their decode. Classification now recognises actual runtime
containers, retains true Go aliases and fences structural conversions. The
Sky-source regression suite checks preserved record fields and genuine
Maybe/Result controls. If an application previously persisted one of the affected
record shapes, data already discarded by that bug cannot be reconstructed by
this fix; application-specific recovery would require another source. No private
application is asserted to have had that shape.

## Mixed-model PIV extension (in progress)

The compiler now separately proves an exact Prepare/Finish factorization of
canonical `Codec.toJson (Codec.auto blank) model`, while retaining Full unchanged.
The blank may be nonliteral only for this prepared capability: every logical
invocation still executes strict codec construction, including registry factories.
The independent closed proof is not widened. The proof checks the actual emitted
Go callback ABI and transports Full's parameter type; inputs requiring coercion
use Full before any preparation effects.

A portable mixed controller converts unknown fields on every invocation in the
original declaration order, then marshals their captured raw results in the
original order. Closed fields alone receive bounded immutable certificates.
Conversion failures stop before marshaling; pure early marshal failures remain
deferred to their original marshal position. Global enum metadata changes discard
stale certificates without replaying unknown effects. Excluded and oversized
fields still validate. The previous invocation runs after publication even when
nullness appears unchanged, because its unknown effects remain observable.

Payload reuse occurs only after all unknown effects, and requires exact selected
encoded output evidence. Retention is bounded; the cache refreshes its current
model owner. This retains synchronous v2 storage and every required write.

Compiler lower units, two real project proof groups, and 18 focused runtime test
files against all production runtime sources passed before browser wiring.
The actual generated wasm callback probe also confirmed factory construction
count/failure parity. Browser integration and the public payload-ADT fixture passed all six
engine/mode runs (Chromium, WebKit, Firefox; reference and mixed). The first
mixed run exposed a routing error: HIR literal provenance alone selected the
whole-closed controller even when its runtime domain rejected Page. The selector
now requires both proofs; the focused regression and all six browser runs pass.
Saved [reports](runs/step-cost-20261104/cycle5-mixed/conditions.json) include the
source/compiler digests. Typing converts 1,315 field bytes at each of 1K/10K/100K
retained entries, with zero Full invocations; every required update/view/post and
storage write remains. This is one instrumented diagnostic repeat, not a latency
claim. Ordinary under-cap changed snapshots still join/copy the selected payload.
A synthetic probe of the previously generated private-app model is described below; no fresh-build application or latency claim is made. Automatic view/collection work and final cross-target validation
remain pending.


The next concrete granularity gap is a changed nested form inside a large closed
Editor record. Root-only summaries reencode that entire Editor, including
unchanged sibling product lists. The next PIV decomposes closed records and
nominal Maybe while keeping lists atomic and unknown root effects ordered.
Source inspection of the isolated FacePlan copy suggests Page and one tuple
counts list are its nonclosed root fields; generated runtime evidence is still
required before asserting application activation.


A private synthetic wasm probe passed against the existing generated FacePlan Go
model and current production runtime. Its exact canonical Full body was checked
before a test-only Prepare/Finish factorization was supplied. The Full parameter
and model representations match. At both 1K and 10K synthetic patient rows, 274
of 276 root fields reused closed certificates; the two unknown fields still
converted and marshaled, totaling 20 warm field bytes. Full byte/length parity
passed. Probe source and hashes stay private. This establishes runtime shape and
unchanged-field behavior; fresh root compiler/build integration remains pending
because the unchanged local memory guard stopped that build. It does not cover
nested form changes or real-user/application latency.


The mixed-model checkpoint additionally passed the full lower/project suite and
focused actual Sky CLI build regression (571 passing tests combined, one harvest
test ignored in the ordinary suite). The ignored all-example harvest was then
run explicitly: one test passed. Denominators, regenerated tracked-file coverage
ledger, configuration surface and migration gates pass. The focused native
runtime staging used 18 unchanged test files against every production runtime
source. Independent review found no further scoped semantic blocker after the
runtime closed-domain routing fix. Hosted CI for this checkpoint is still needed.


## Recursive closed summaries: validated checkpoint

Closed records and nominal Maybe values now decompose into bounded summaries;
lists remain atomic. An edit to `Editor.productForm.brand` can reuse unchanged
`Editor.products`. Invocation-local byte plans append captured bytes directly
into the final selected payload and protected identity, without intermediate
whole-Editor JSON joins. The mixed root alone owns the current model. Recursive
cache metadata reserves node headers and child slots under a shared 4096-unit
budget (in addition to the fixed root field table), with depth 64 and one shared
raw-byte cap. Exhausted subtrees retain canonical atomic execution.

Proven prepared encoders use this path for both mixed and fully closed models.
A previous invocation may be skipped only with the original literal/canonical
proof, matching input types, both whole-model closed runtime descriptors, a
valid supported next encode, and the existing negative projection after
publication. Prepared-only/nonliteral or unknown-field models retain every
previous preparation and unknown effect. Successful supported next invocations
count as `fieldModelSteps`; conversions/reuses now include recursive atomic
leaves. Strict next codec construction remains observable.

Validation: all production runtime sources plus 20 focused native test files
passed, including controller differential ordering, enum changes during A/B and
publication, nested NaN/true conversion failure, exact escaped bytes, input
proof guards and retained cache-slot budgets. A fresh canonical compiler,
repository Sky formatting gate and all twelve closed/nested engine-mode browser
journeys pass. [Reports and exact source hashes](runs/step-cost-20261104/cycle6-recursive/conditions.json)
record one instrumented repeat per Chromium/WebKit/Firefox and mode. Nested
fixture typing encodes 1,390 field bytes at each of 1K/10K/100K retained products;
no Full model encoder runs. Reload, both form/input state, caret, commands,
handler captures and sign-out remain checked. Ordinary changed under-cap v2
snapshots still require the selected payload join/copy.

A separate private wasm probe uses the real app's previously generated model
and actual compiled `SetBrandTyped`/`BrandKey` update arms with synthetic product
records. At 1K and 10K products, typing converts five fields/leaves totaling 44
bytes; ignored brand keys encode 20 bytes. Both form locations and exact
canonical Full size/bytes pass. This is model/update/persistence evidence, not a
fresh root build, private-app browser journey, deployment or latency guarantee.
The fresh root build remains constrained by the unchanged local memory guard.

The initially expanded browser fixture serialized its list at two positions;
Firefox reference execution exceeded the unchanged deadline. The final fixture
retains the full 100K-entry list once inside Editor, matching the intended
retained-data dimension; all twelve final journeys pass with unchanged budgets.

Small flat-model diagnostics show the cost of retaining strict next preparation:
one instrumented Chromium repeat measured about 0.27 ms extra encoding per
ignored key. This is a tradeoff observation, not a latency benchmark. No shortcut
assumes codec construction total from convenience alone.

The preceding mixed checkpoint, including its canonical-format follow-up at
`76a7b7512`, passed all 39 hosted CI jobs (run 41920407390). Hosted validation for
the recursive checkpoint passed all 40 hosted CI jobs (run 41926388460).

## Pinned reconstruction: validated checkpoint

An unchanged, previously successful closed leaf whose raw bytes were evicted
can now defer reconstruction until a selected payload actually needs those
bytes. Invocation-local plans pin the original enum metadata, reserve bounded
raw capacity and fill only a new cache node. Published caches retain neither
reflection values nor deferred plans. Changed or uncertified leaves retain
canonical eager conversion and error order. Ordinary codecs still perform
their original global metadata reads; only certified deferred work uses a
pinned context. Unknown conversion/marshal effects and strict preparation
remain observable on every required invocation.

The public mixed journey now fills the shared raw budget with unrelated lists,
types while the model exceeds the persistence cap, removes those lists, and
reloads. Across Chromium, WebKit and Firefox, two typing messages convert 150
field bytes with zero deferred conversions or writes. Shrinking performs
exactly one deferred product-list conversion and one write; the recovered
ignored key performs no deferred conversion, payload join or bridge copy.
[Twelve passing reports and exact source hashes](runs/step-cost-20261104/cycle7-pinned/conditions.json)
cover both closed and mixed fixtures with unchanged browser deadlines. These
are single instrumented repeats, not production latency measurements.

Local validation also passes 33 focused runtime/codec test source files against
all production runtime sources (with the unchanged shared helper), a canonical
compiler build, and three Sky codec suites totaling 154 assertions. Private
wasm probes using previously generated real model/update code pass normal
typing and ignored-key checks at 1K/10K products, plus natural budget eviction
at 500/1K products. Evicted leaves of 239,501/479,001 bytes require no deferred
conversion during oversized typing, exactly one on shrink, then none on an
ignored key. Typing/ignored field work remains 44/20 bytes. This does not replace
a fresh private application build or application browser E2E.

Automatic view/collection reuse, the separately discovered nominal codec
constructor collision, broader storage and final cross-target/application
validation remain in the active PIV. Changed under-cap v2 payloads still need
their selected payload join/copy. The complete hosted CI run
[41954316141](https://github.com/anzellai/sky/actions/runs/41954316141) passes
for checkpoint `5b489e811`, including the pinned work and subsequent fixes.

Hosted testing uncovered a real AI adapter loading race: requests could reach a
resident before its engine finished loading. The fix waits with a request lease
and handles load failure/cancellation explicitly. Deterministic regression tests
fail the old implementation and pass the fix. All functional jobs then passed;
the aggregate duration gate exposed an uneven runtime race-test partition.
Refreshing recorded durations and moving the existing smoke step between
equivalent shards preserves the work, tests, deadlines and budgets. The complete
run above passes after that rebalance. No release or main merge is implied.


## Owner provenance: locally validated correctness prerequisite

A constructor spelling cannot identify its parent type. A user constructor
named `Money` previously reached the builtin writer and silently lost its
payload. Constructors now carry their resolved parent identity; builtin grants
come only from the original standard-library source modules. Project tests
exercise actual source loading, transparent aliases, generated projects, added
namespaces and rejected standard-library replacements. Runtime checks preserve
live and pinned metadata reads at their original conversion positions.

The internal owner field is hidden from JSON. Typed legacy builtin slots can
recover their parent through registered metadata. An ambiguous legacy root
fails with classified `JsonEncodeFailure` instead of guessing. Cross-binary
experiments use the released v0.29.2 runtime and the new runtime: five static
payload combinations and thirteen reports cover upgrade, rollback and erased
slots. They detect owner loss during rollback. Their synthetic authority
bootstrap is separate from the real compiler source-grant tests.

Local evidence includes 292 focused native tests, a real PostgreSQL
cross-instance session test in an owned temporary cluster, the compiler proof
suites, and all four Sky codec suites. The bundled console was regenerated and
the canonical compiler rebuilt afterward. A separate production-only restart
probe exposed missing Gob registration that the console's initialization had
masked; that fix is committed at `e490b26b3` and passes all forty hosted CI jobs.
The owner changes themselves still require the final integrated gates.

Canonical field metadata and escaped schema keys are cached without retaining
model values. Tuple type arguments are parsed once per list invocation, while
each element keeps its original conversion and metadata reads. Ordered-object
output buffers start at 32 bytes. Only exact builtin `int64`, boolean, null and
safe ASCII strings of at most 64 bytes use direct primitive appends at their
original marshal position. Named values, custom marshalers, floats, escaped,
non-ASCII and longer strings retain `encoding/json.Marshal`. Differential
byte/error/effect tests and race checks accompany these changes.

Two rejected proposals remain excluded: discovering hints in invisible slice
capacity introduced a demonstrated race, and sharing key/value allocation would
retain replaced values through the existing plain-null key-slice path. The
passing implementation preserves the ordered-object layout and ownership.

## Collection integration: local browser validation complete

Mixed-model reuse now admits exact runtime T2/T3 tuples and SkyDict values when
their children qualify. It reuses unchanged roots. A changed root still takes
the complete canonical conversion; this is not a changed-key delta algorithm.
The original Full/projection gate remains in place.

The new public Dict/tuple fixture uses one-byte string values and the unchanged
1.5 MiB persistence cap. The complete current-owner gate now exits successfully:
twelve reports cover Chromium, WebKit and Firefox, and all six reference /
incremental pairs match exactly at eight stored checkpoints. The
[cycle8 evidence bundle](runs/step-cost-20261104/cycle8-collections/conditions.json)
binds the actual compiler, production runtime, generated code, served Wasm and
fixture sources. Every original message, assertion and 300-second browser
deadline remains in place. Earlier attempts timed out in Firefox tuple
reference; the invocation-local metadata optimizations below resolve that
measured gate failure. This is a single instrumented correctness/work-count
repeat, not a production latency or general scaling claim.

An invocation-local owner memo preserves each original live atomic registry
read and supplied pin, while reusing the owner map result only for the same
fixed list-element declaration and immutable metadata epoch. It retains one
epoch and no model values. Its candidate passes 298 selected native tests and
23 targeted race tests with no skips. An invocation-local tuple field-plan
optimization also passes 302 native tests and 35 targeted race tests without
skips. Its exact actual type and naming mode select an immutable layout only
at the original tuple-conversion boundary. Mutable argument/value reads,
canonical guards, metadata observations and independent output buffers remain
unchanged. Both the fresh Firefox pair and the subsequent full twelve-report
matrix pass with those changes.


## Automatic view regions: implementation checkpoint, integrated gates pending

The candidate now lowers proven-pure `indexedMap` regions through an explicit
per-application frame. Reuse requires the same physical list and capture
witnesses and an admissible data-only result. Source-tree reuse leaves VNode
conversion, event refresh, styles, paths and DOM planning on their normal path.
Entries publish only after successful paint; reset, authentication changes,
reboot, persistence reset and failed frames invalidate them. Frame bounds are
128 entries/sites, 131072 accounted cells and 8 MiB of accounted strings. These
are admission/work bounds, not a strict byte bound on retained backing arrays.
The implementation reuses a whole mapped region, not changed rows individually.

Local validation passes twelve native lifecycle/admission tests and twelve
actual Go/Wasm runtime tests. Thirty-three compiler proofs cover purity,
ownership, callable capture, source-authorized domains, model encoding and the
actual private normal-row callback. Private source probes were removed before
the shipping compiler rebuild. An expanded regression also exposed and fixes
statically Task-typed root discards that previously failed to execute their
effect; tuple-contained Task data remains latent. Actual generated-IR and native
counter execution validate both sides of that distinction.

The full private application and final cross-target gates remain pending. A
fresh application build exceeded the unchanged memory guard. After borrowing
HIR bodies, avoiding full annotated type readback for Task-head queries and
consuming alternate GoIR scratch, a second measured build still exceeded it.
Fixed phase markers put the checker high-water at 1048313856 bytes before
lowering; the guard observed 1210 MiB current RSS during lowering. No browser,
latency or memory improvement is claimed from this failed build. Compiler
retention is being investigated before another changed-source attempt.

General changed-key storage deltas, arbitrary-model scaling, real-device IME
behavior and successful native GUI execution remain outside the evidence so
far. The existing collection browser results above do not establish those
properties. Final hosted CI is also required for this checkpoint.

Follow-up lifetime changes release reporting type trees before lowering and
solved inference scratch before checker scans. Lowering also retains immutable
resolved-module owners instead of cloning reached HIR bodies. The latter passes
all 45 lowerer unit tests and six project integration proofs. The checker
regressions pass all 48 tests; the two release-only Task/WebSocket and
parallel-panic programs pass when explicitly selected with `--ignored`. The
unchanged coercion floor passes without widening or baseline changes.

The latest full-app measurement still fails the guard during checking. The
separate `check-world` marker ends at a process high-water of 1030995968 bytes,
before per-module checking finishes. This localizes most peak allocation to
world assembly; it does not establish that all those bytes remain live. The
ownership changes have not yet demonstrated a successful complete application
build. Actual application browser comparisons remain unrun.

The local checkpoint preflight passes all 21 static integration binaries and the
first five original configuration steps, including the effective-value matrix.
The final combined harness passes CLI verbs, roundtrip, shared-world and format
checks. Four Go runtime gates fail during building, before any assertions run;
the memory guard separately records three Go compiler terminations. The gate
helper omits compiler stderr, so the captured harness diagnostics do not prove
that every failure has the same cause. Improving that diagnostic and validating
the remaining gates are required before a complete-green claim or merge.


## World ownership follow-up: compiler and local contract checks pass

Checkpoint `0f4b8346` passes all forty hosted CI jobs (run `42027583420`).
The subsequent world-assembly patch retains immutable resolved-module owners
instead of copying target HIR in wildcard/bounds passes, and releases solved
inference scratch before call-site record harvesting. All 195 selected type
checker tests pass; one existing diagnostic census remains ignored. A new
subprocess regression first fails for omitted compiler stderr, then passes with
bounded diagnostics retaining both streams without counting stderr assertions.

The final eight local contract gates also all pass after explicit local Go
compiler controls reach the gates that intentionally clear `GOFLAGS`. The
original toolchain, inner deadlines, assertions, memory guard and caches remain
in place. This is a local execution shim, not an application/compiler feature.

A fresh complete private application build now succeeds: 142 seconds rebuilding
the canonical compiler and 211 seconds building the application in this run.
The world-query high-water marker is 873693184 bytes; the complete frontend
process reaches 2872901632 bytes. The guard is unchanged and responds to machine
pressure, so this is not evidence of a strict 1 GiB peak bound or a complete
before/after peak-memory comparison.

The follow-up checkpoint `dadf2a0d` also passes all forty hosted Rust CI jobs
(run `42039950149`). All six programs in the unchanged performance gate pass.
The build-growth mutation verifier passes its deliberate regression and canary;
the subsequent strict harness run passes with matching proof inputs and the
original 600-second gate budget.

Private browser validation remains in progress. Exact fresh generated-schema
binding and the diagnostic Wasm rebuild both pass, with compiler controls
explicitly preserved in the isolated build context. The browser harness then
stopped before PostgreSQL initialization because its Unix socket directory was
too long. A short, exclusively owned socket directory and a resume path that
verifies and reuses the compiled artifacts have passed source review and helper
regressions. Success receipts publish only after cleanup succeeds. The resumed run initialized PostgreSQL and entered the first browser account
setup, then failed before any comparison completed. Its cleanup verdict is
unverified; it is not a passing browser result. The failure is being diagnosed.
Actual application comparisons and final cross-target checks remain pending;
the successful build alone does not establish application performance.


## Final gate continuation: growth proof and core target builds

The strict `build-growth-linear` gate now passes after its deliberate compiler
mutation and the harness canary both behave as declared. The original 600-second
budget and assertion count remain unchanged; proof evidence is regenerated by
the harness rather than edited by hand.

The same public `std-app-dispatch` fixture builds successfully for `web`,
`web:app`, `terminal:cli` and `terminal:tui`. The CLI also executes with EOF and
produces its expected greeting. The scoped receipt records compiler, wrapper and
artifact hashes in `runs/incremental-final-validation-20261105/core-target-builds.json`.
These builds do not establish browser interaction, TUI screen behavior, or view
cache activation: that small fixture does not contain the mapped row region.

The first private application browser retry exposed a deterministic test-adapter
signup-loop scoping defect. An extracted executable regression fails with the
original loop and passes with braces preserving all three ordered fills and
action counts. The original browser exception was discarded, so the precise
observed exception is not retrospectively known. No application comparisons have
passed yet; the next retry will retain private failure diagnostics.

Both Mac desktop forms (`desktop` and `desktop:mac`) also build successfully
with the matched locked Nix toolchain. Six exact `Std.App` tests pass, including
native HTTP startup/configuration and the desktop failure-to-listen exit path.
Receipts are alongside the core build receipt. Successful desktop GUI execution
and actual terminal PTY screen behavior are not established by these checks.
The earlier isolated desktop/test attempts failed at linking after the harness
removed SDK configuration; preserving the clean locked Nix context resolves
those failures without changing compiler source, assertions or budgets.

The private application's profiling-readiness timeout has a source-confirmed
harness launch mismatch: it started the backend from the executable directory,
while the generated asset path and canonical Sky CLI require the backend project
directory. The retry will correct that directory and verify asset bindings;
readiness requirements remain unchanged. Real application browser comparisons
still have no passing result.


The unchanged full `scripts/tui-e2e.sh` gate now passes, including its actual PTY
screen assertions and CLI effect, guard and exit checks. The test dependency is
provided by a Python closure from the existing locked Nix input, without a pip
fallback or repository lock change. The successful run preserves the original
clean Nix context and prefixes only that Python executable; non-Python tool
identity hashes remain equal. Earlier nested-launch attempts received SIGTERM
before assertions, whose sender remains unclassified. The subsequent passing
run does not establish who sent those signals. Original 600-second fixture
build, 300-second PTY and 30-second CLI limits remain unchanged.

Checkpoint `0391bb063` passes all forty hosted Rust CI jobs
(run `42124193018`), as do the three preceding checkpoints. The private whole-application browser gate remains pending.


Source review found a mismatch in the private stress fixture: the original
application report varies the number of distinct source identifiers in its seed,
whereas the attempted fixture seeded many product rows under one identifier.
The retained model shape must be established from the original loading flow;
the seed dimension alone does not identify a model field.
A candidate editor DTO collection also has
an application-imposed 500-row query limit and cannot establish the required
larger retained-model case. Those attempts are not equivalent to the original
benchmark. The original benchmark source has been recovered read-only and is
being used to establish the correct fixture and observation binding. The
unsuccessful snapshot-reset experiment is not an application fix or passing
performance evidence; requested model-size assertions will not be silently
reduced to fit the mistaken collection.


## Next proposed PIV slice: changed primitive Dict summaries

This is a scoped plan, not implemented or validated behavior. Keep synchronous
v2 publication and summarize exact nominal string/int/bool Dict leaves along
their persistent HAMT structure. Shared subtries may reuse successful summaries;
changed paths must derive exact canonical JSON size and validity. Retain the
five-field Dict layout and existing mutation APIs. Charge all reachable summary
nodes and slots to the existing global 4096-unit ledger, retain only the current
model owner, and use canonical fallback on admission failure. No global pointer
cache or chain of previous models is permitted.

The initial regression warms 256 keys with 128-character ASCII values, then
replaces one value with 129 characters under a 4096-byte persistence cap. The
single-field selected payload grows from 35597 to 35598 bytes; both outcomes
must preserve NoWrite, zero storage calls and one strict preparation. The
candidate work assertion requires one encoded entry and 131 value JSON bytes,
without whole-Dict conversion or payload/bridge materialization. The current
path instead converts the complete 35586-byte Dict leaf. The temporary overlay test now reaches this exact RED: both existing encoded-byte
counters report 35586 rather than 131, after all preceding correctness checks
pass. It ran under locked Nix and the unchanged guard in 128 seconds, with the
test itself taking 0.036 seconds. No runtime source was changed. Validate exact
reference bytes and work independently when implementing the optimization.

Cover insertion, removal, cap crossings, no-op replacement, flat/HAMT
transitions, collisions, metadata changes and budget exhaustion. Float, ADTs,
aliases and custom methods remain canonical until separately proven. Preserve
construction callbacks, unknown-field phase ordering, pinned metadata and
protected identity materialization. Under-cap publication still emits the
complete payload; this slice does not implement general chunk storage or prove
arbitrary-model scaling.


The source-faithful private discovery experiment now completes all 24 checks in
105 seconds with cleanup reported successful. Exact input counts are verified
at 0, 1000 and 10000 buckets (0, 27448 and 276023 rows). Nevertheless the saved
model after typing is 19896 bytes at every scale, with the largest observed
array containing six entries. All eight keydown and eight input messages arrive
at each scale; the previously selected mapped-view site receives no calls.
This establishes that the experiment ran, but does **not** reproduce the reported
large-model workload or pass its acceptance gate. Current loader behavior and the
original setup are under investigation. The numeric discovery receipt is
`runs/incremental-final-validation-20261105/source-workload-discovery.json`;
private values and schema names are excluded.


## Primitive Dict prototype: initial hosted validation checkpoint

The reviewed prototype adds bounded summaries of immutable Dict subtrees for
exact string/int/bool values. It can derive an exact oversized NoWrite decision
after a changed primitive without rebuilding unchanged entries. Writable
snapshots, protected fields, metadata-sensitive declarations and unsupported or
unaffordable summaries retain canonical conversion. Leaves and branches retain
only scalar metadata and child certificates. Private Dict pointers are compared
only during an invocation, under the existing old-owner/certificate pairing;
none is stored in a certificate. The 4096-unit ledger bounds each retained cache graph, not all concurrent graphs or
process memory. Cold summary construction and cap-crossing fallback add real
work, which remains counted.

The unchanged RED and seven further tests accompanied this checkpoint. Local
prototype tests had **not executed**: the unchanged pressure-sensitive memory
guard stopped compilation with two workers, one worker, tighter compiler heap
controls, and an independently reviewed focused source closure. That closure
retains all production files and the complete selected tests, helper and init
behavior; it is not a full-suite substitute. No local performance or correctness
PASS was claimed for that local attempt. The draft feature-branch checkpoint was
submitted to the existing hosted full runtime and race jobs for validation.
Main, release tags and deployed applications remain untouched.

[Hosted run 42136087088](https://github.com/anzellai/sky/actions/runs/42136087088) reached the existing cache-reachability
invariant: runtime-rt, macos-determinism and integration-postgres-2 all reported
the same failure. The first prototype retained private Dict branch pointers,
which violated that invariant even when the branches belonged to the current
model. The source correction removes them, keeps scalar shape validation and
extends the exact-type reachability guard without admitting model, value, plan,
interface or snapshot pointers. It adds a contradictory-certificate regression;
the original 131-byte RED assertion remains unchanged. At this checkpoint the
correction had not been compiled locally; later hosted and browser results are
recorded below.

Current-app browser work is also still pending. The original typing fixture
requires twelve products: the UI hides its search below that threshold. Restoring
the original count allowed typing/caret/replacement/composition assertions to
complete. A strict diagnostic then proved that the old semantic projection
attempted to dereference an inactive optional record. Its correction must follow
the actual handler's unconditional root write and rendered collection, including
an assertion equating persisted edited text with the controlled input. No missing
path is being normalized to null and no completed browser comparison is claimed.
The isolated historical benchmark build separately stopped during lowering under
the same pressure guard, so its large-model reproduction remains unvalidated.

## Complete inference-result ownership

The tracked inference query already shared expression/local tables, but its
bridge cloned the owned result and signature types into a fresh handle, and
lowering cloned them again. The memo, bridge and lowerer now share the complete
immutable result through `Arc`. Tooling retains its owned-copy API. Inference,
query dependency edges, eager-backend computations and emitted-type consumers
are unchanged.

An executed regression first failed the owner-identity assertion. After the
change, the complete lower and skydb suites pass 118 tests with one existing
ignored test. The regression covers memo identity, owned tooling-copy isolation,
retained owners across revisions and equality with fresh inference, including
signatures. Memory savings remain unmeasured. The ignored unifier-growth test
was then explicitly executed: it fails with near-quadratic work and is being
investigated separately. It is not counted as passing or silently skipped.
See [the receipt](runs/incremental-final-validation-20261105/inference-owner-sharing.json).

## Oversized primitive Dict browser validation

The corrected Dict certificate checkpoint passes all 40 jobs in hosted run
[42197690937](https://github.com/anzellai/sky/actions/runs/42197690937).
The existing public Dict browser gate now also exercises a changed entry in a
64-entry Dict with 63 unchanged 32KiB values. Its load warms the certificate;
replacing the remaining 128-byte value keeps the model above the unchanged
1.5MB persistence cap.

All six reference/candidate runs pass in Chromium, WebKit and Firefox, including
the original eight canonical checkpoints and three additional oversized
publication/reload checkpoints. For the measured change, reference execution
encodes 4,130,961 bytes across its logical invocations; candidate execution
encodes 71 bytes in total. Both retain the previous stored snapshot, make zero
storage Set/Remove calls, copy zero bytes across the storage bridge, and restore
the same previous snapshot on reload. Strict codec preparation and update/view/
post counts remain asserted. Cold summary work is separately measured, not
removed from the report.

The complete gate passes in 661 seconds under the unchanged memory guard and
300-second per-browser-run deadlines. This is exact oversized NoWrite evidence;
under-cap whole-model publication and general changed-key storage remain open.
No cross-device latency claim follows from these diagnostic runs. See the
[cycle 9 evidence](runs/step-cost-20261104/cycle9-dict-summary/conditions.json).

## Sparse storage for unreachable type-variable reservations

A prior inference fast path reserved unused variables to preserve later logical
IDs. Those reservations still allocated physical UF slots and occurs-check
marks. The new store preserves the exact logical ID sequence while translating
only live variables to dense storage; reserved holes cannot alias live slots.

The executed RED observed 33,024 slots for 128 live variables. The corrected
physical-storage regression passes, along with dense-oracle, clone/error,
epoch-wrap and ID-exhaustion tests. The complete type library passes 86 tests
with one existing ignored diagnostic; complete lower/skydb suites pass 118
with one existing ignored growth test. That growth test was already explicitly
executed and remains unresolved. CPU/RSS savings on the private app are not
yet measured. See [the receipt](runs/incremental-final-validation-20261105/sparse-uf-reservations.json).

## Shared declaration-core validation

Declaration and checker queries previously built separate copies of the same
declaration maps. They now share 15 immutable declaration containers through one
`Arc` core. Eight body-derived channels remain independent, including the
bound-check map that has both static seeds and later body-pass writes.
Declaration-building forks detach with copy-on-write. The checker still runs
the same body passes; declaration equality and body-edit backdating remain
structural.

An executed original-source RED failed the actual Scheme-address sharing
assertion. The complete Rust validation passes 534 tests with two unchanged
ignored diagnostics. Tests cover whole-core sharing, body-channel isolation,
declaration mutation, revision retention, unrelated inference memo reuse and
equality with eager construction. Subsequent formatting and a targeted type
check pass, and a fresh canonical compiler builds in 206 seconds. This does not
establish application peak-memory savings or a successful historical workload
build. See [the receipt](runs/incremental-final-validation-20261105/declaration-core-sharing.json).

## Latest hosted failure and readiness correction

[Hosted run 42204965045](https://github.com/anzellai/sky/actions/runs/42204965045)
completed with 38 successful jobs and a macOS runtime test failure; the aggregate
gate consequently failed too. The failed `TestPushExporter_BatchedDelivery`
waited for the fake server's request-start counter, which advances before its
body is decoded and published. It could then inspect an empty payload.

The test now waits for captured-payload readiness with the same two-second
deadline and unchanged payload assertions. Exporter production code is unchanged.
A standard-library-only exact-source helper proof executed deterministic RED
then GREEN: both GREEN tests pass, including an AST check tying the real helper,
barrier and original test predicate together. This proves helper synchronization.
Subsequent hosted run 42215098140 passed both full runtime and macOS jobs;
the overall run remains red on two other functional jobs and its aggregate.
See [the receipt](runs/incremental-final-validation-20261105/ingest-readiness-synchronization.json).

Full current-application browser validation remains incomplete. The earlier cached
reference-only result and candidate admission failure are preserved. The later
scoped derived-artifact core comparison below passes both modes; it is not a full
current-head application rebuild or historical
large-model performance result is claimed.


## Hosted follow-up: static naming and edit compression

[Run 42215098140](https://github.com/anzellai/sky/actions/runs/42215098140) completed
with 37 successful functional jobs. The static skip-helper policy matched the new
private logical-ID reservation method name. Its method and sole call have been
renamed to describe reserved unreachable IDs; the policy remains unchanged.
All ten tests in the unchanged static policy suite now pass.

The other failure is the existing app50 web:app edit build budget: median 16.82s
against a 9.57s reference. Its ratio 1.75758 exceeds 1.72225; the combined
unchanged ratio and absolute-slack conditions give an effective 16.4819325s
wall bound. Three uncached Brotli(max) calls take 13.71/13.93/13.71s, dominating
parent precompression. Cold builds reuse the already compressed pristine bytes;
the benchmark deliberately gives each edit unique content. The complete-content
cache miss is expected. These timings do not demonstrate a declaration-world
regression. See the [classification receipt](runs/incremental-final-validation-20261105/ci-42215098140-classification.json).

A source-only measurement candidate compares deterministic 6 MiB and 3 MiB Brotli
partitions using the same pinned encoder, quality 11, window 24 and maximum 8 parts.
It requires a fresh source-bound public benchmark asset, exact decoding through
independent decoders, the unchanged compression-size oracle and memory accounting.
No candidate encoding measurements or performance improvement are claimed. No
budget, timeout, quality or memory guard is relaxed. Historical application
validation and the current application's browser proof remain incomplete.


## Open-record Access: model work is linear in the measured growth check

Repeated access to an expanding open record previously copied its accumulated
fields and built a complement for a fresh row that never escaped. The new narrow
path consumes only that unpublished singleton constraint. Existing row aliases,
held content copies, ranks, late constraints and errors match the general unifier.
Updates and unsupported row shapes retain reference unification.

The original regression observed 24,768 copied fields instead of zero. All six
focused tests and 540 broad compiler tests pass, with two unchanged ignores.
The explicitly executed growth diagnostic now reports 2.00 doubling ratios for
all four model-app unifier counters. Codec-chain ratios remain 3.87–3.92, so the
unchanged overall growth test still fails. No bound or ignore is widened. See
[the receipt](runs/incremental-final-validation-20261105/open-access-consumption.json).

The private application's diagnostic also identified an exact missing capability:
compiler-emitted integer enums such as canonical `Std.Ui.VAlign` have a generated
method but receive no view token. A provenance-backed enum token and metadata
epoch checks are being prepared. Observational diagnostic compilation disabled
app-package inlining under unchanged resource bounds; five public fixtures have
identical numeric rejection/accounting results with and without that option.
These observations do not establish hit-rate, latency or current-head validation.


## Enum capability, metadata privacy and scoped validation checkpoint

Code checkpoint `913c97184` grants view-memo enum capabilities only for actual
compiler-emitted enum declarations. Runtime validation uses an exact typed
interface without extracting or invoking reflected methods. Metadata snapshots
invalidate cached results when the enum/owner registry changes. The split now
explicitly drops reporting type storage before partition analysis. Two internal
Go settings queries are replaced with public version and dry-run installation
metadata, using the actual output module directory.

Executed local evidence is: 18 selected native runtime tests, the same 18 in
actual Wasm, 47 lower tests, and the complete project suite with 519 passed and
2 ignored. The observer AST proof preserves metadata checks and rejects all
8 negative mutations. Fake-only legacy metadata RED fails its 2 intended
assertions with 1 negative pass; final metadata tests pass 6, with 4 memo
regressions. Split disposal has actual boundary RED followed by 2 GREEN tests.
No real legacy settings query was used for the RED proof.

A strict saved-plan core comparison passes in 92 seconds with 2 comparisons
and confirmed cleanup. Both modes use the same derived Wasm. This diagnostic
uses cached application inputs and a no-inlining intervention; it is not a full
application rebuild, current-head runtime validation, full browser journey,
physical-identity or latency proof. Earlier admission failures remain preserved.

A fresh canonical compiler builds in 192 seconds. Its bytes match the checkpoint's
code content, while its embedded stamp retains the precommit `a703e3cb0` base.
The original 8-file public app50 automatic `web:app` smoke then passes in
46 seconds, verifying Wasm headers and both artifact/SDK-loader copies. That
smoke does not establish build-performance medians or a performance improvement.
Formatting and source checks pass in 16 seconds.

The [scoped receipt](runs/incremental-final-validation-20261105/enum-privacy-scoped-validation.json)
binds source and evidence hashes. The later original public step-cost browser gate passes in 291 seconds across
all 6 reference/incremental reports for Chromium, WebKit and Firefox, preserving
8 scenarios per report. This is the public fixture proof, not a full private
application or general latency claim. Checkpoint CI required the separately
recorded source-line census correction. Historical large-model build
and performance validation remain incomplete; guards and budgets are unchanged.

The checkpoint CI config gate exposed stale source-line census witnesses after
the settings-query removal. Updating two line witnesses leaves all summary counts
and the ratchet unchanged; the original direct check then exits successfully.
This is a metadata refresh, with no product-source or budget change.


## Hosted enum/privacy checkpoint

[Run 42239339686](https://github.com/anzellai/sky/actions/runs/42239339686)
completed successfully at `2bcee4b5f`: all 40 jobs pass, including the config
census and unchanged build-performance gates. This validates the enum/privacy
checkpoint, not the subsequent isolated accessor-row experiment. The separate
current-source public browser matrix passes all six runs and eight scenarios per
run. The full private-application rebuild, historical workload and remaining
codec constructor-arrow growth work remain open.


## Singleton accessor rows: codec record-field growth is linear

A private factory returns only the accessor function root and grants authority
for its unexported row. Owned Record copies, normalizer results, generic
publication and whole-UF snapshots revoke that authority before it can escape.
The narrow merge moves the growing record map and elides only the unreachable
accessor-row complement. The meaningful old-row complement still uses ordinary
unification in original allocation/error order. Other shapes fall back.

Three executed aliasing regressions reject the earlier unadopted design. A
source-faithful 240-field Codec chain establishes the actual work RED: 29,399 copied
fields in both baseline paths. The closed-only repair passes correctness checks
but fails the same work assertion. The open-row repair passes all 17 focused
checks, including the unchanged requirement to copy less than half that count.
All 559 broad type/lower/database tests pass, with two existing ignores.

The original ignored growth diagnostic was explicitly executed: both codec
record-field counters and all four model counters now have 2.00 doubling ratios.
Both codec constructor-arrow occurs counters remain 3.91, so the overall test
still fails its unchanged 2.3 bound. Fresh compiler/application and hosted
validation for this source remain pending. See the [scoped receipt](runs/incremental-final-validation-20261105/accessor-row-consumption.json).

The unchanged coerce-floor gate also passes in 202 seconds: all 84 golden rows
are measured, adapters remain exactly zero, and no dispatch/narrow count rises.
No golden is changed.


The d16 accessor checkpoint builds a fresh canonical CLI and passes the full
519-test project suite (two existing ignores) and automatic public app50 build.
Its private-app build is still stopped by the original swap guard during
lowering, with no checker diagnostic. Hosted run 42246342362 passes 38 jobs,
including both unchanged build-performance gates. Its memory measurement test
fails a fixed 100 MiB small-child assumption after observing 104 MiB; the aggregate
gate also fails. That test oracle is under correction; no full hosted or private
application success is claimed for this checkpoint.


## Per-child RSS oracle correction

The hosted small-child peak was 104 MiB, already below the preceding child
whose assertion required at least 300 MiB. The test incorrectly also required
that the small child stay below a fixed 100 MiB. The corrected existing test
uses a fresh exact one-test launcher and compares small children before/after
the unchanged 300 MiB workload. It retains status and baseline-discrimination
assertions. Production wait4 and all memory guards are unchanged.

A real cumulative-RUSAGE_CHILDREN fault fails the intended independence
assertion. Restoring the per-child reporter passes, as do all six memory-module
tests, all ten unchanged static skip-policy tests and the config census. New
Linux hosted validation remains pending. See the [receipt](runs/incremental-final-validation-20261105/child-rss-oracle.json).


## Query-world aggregate harvest removal

The query world now runs passes 5–7 and leaves aggregate pass 8 to the unchanged
eager/shared-world oracle. Query-world consumers do not read those aggregate
param-record/conflict maps; lowering already requests per-definition summaries.
Pass 7 remains available for caller inference of CAF record updates.

The accepted-fixture regression fails the old aggregate-work assertion, then
passes with complete channel, harvest, body-table, numeric and checker parity.
A further guard covers caller Bool-field edits, CAF String-field edits and a
revert against fresh/eager output. The full compiler validation passes 561 tests
with two existing ignores. No counter, golden, timeout or memory guard changes.

Local broad and coerce compiler attempts were separately interrupted by the
external pressure watchdog before running tests/the coerce gate. The full broad
suite passed after measured headroom recovery; existing Linux and macOS hosted
coerce gates remain required. Fresh compiler/private-app memory evidence is
pending. See the [receipt](runs/incremental-final-validation-20261105/query-world-harvest.json).


## Query-world checkpoint validation and remaining resource blocker

Hosted [run 42260996734](https://github.com/anzellai/sky/actions/runs/42260996734)
passes all 40 jobs at `885219bda`, including the unchanged Linux and macOS
coerce gates. The preceding RSS-oracle checkpoint also passes hosted CI.
A fresh canonical compiler passes in 189 seconds, with all 2,415 tracked
framework inputs verified unchanged before and after the build. The automatic
public app50 smoke passes in 19 seconds, verifying Wasm headers and matching
SDK loaders. No build-performance improvement is inferred from this smoke.

The fresh private application build still stops at the original swap guard
during lowering, after 27 seconds, with no checker error codes and successful
cleanup. Both the 671-file donor and destination snapshots remain unchanged.
The sampled process RSS is 1,140 MiB. This is a resource stop, not a successful
application build or a measured memory improvement. A production change is
required before another equivalent attempt.

A separate, unadopted immutable constructor representation passes its genuine
equal-work baseline RED, all 14 focused checks, all 17 unchanged accessor checks
and 575 broad compiler tests (two existing ignores). The original growth test
still fails: the two Codec occurs ratios are 3.43 and 3.45 against the unchanged
2.3 bound; the other six counters remain 2.00. Temporary classification identifies
ordinary record-field traversal as the remaining route. The temporary counters
were removed, and the representation is not part of the tracked implementation.
Full private-app, historical workload and persistence goals remain incomplete.

The current query-world compiler also passes the unchanged public step-cost
browser gate in 271 seconds: six reference/incremental runs across Chromium,
WebKit and Firefox, each with all eight scenarios. The build cache is explicitly
disabled so its settings/environment cache-key probes do not execute. This is
public fixture evidence, not the blocked private-application matrix.


## Build allocation ownership checkpoint

Build checking now omits only the final reporting-type collector, rather than
cloning declared type trees into a vector that the build later discards. The
reporting APIs, inference and authoritative scans remain unchanged. Two genuine
report-allocation regressions fail on the previous implementation and pass with
the build mode, including a valid fixture with at least 1,920 reported fields.

Numeric elaboration now shares the tracked immutable owner through `Arc` rather
than deep-cloning it at the lowering adapter. The strengthened accepted-program
regression fails on the previous adapter. All 14 incremental cases then pass,
including body-only, caller-only, imported-signature-only and revert stages with
retained-owner, eager/fresh and tooling-clone checks. All 32 existing arithmetic,
cross-module numeric and rigid-number tests pass. The fixture correction retains
the Int/Float refusal and separates edits instead of weakening the type checker.

The [scoped receipt](runs/incremental-final-validation-20261105/build-allocation-ownership.json)
binds source and executed evidence. Full compiler/project and hosted checks are
still pending, and the prior compiler is stale for these sources. No measured
memory saving or successful private-application rebuild is claimed. The discovered
raw-ID authority regressions and remaining Codec occurs growth are separate fixes
in progress. No guard, counter limit, golden or release policy changes.
