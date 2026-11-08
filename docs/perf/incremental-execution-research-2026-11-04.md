# Correctness-first incremental execution for Sky

Research date: 4 November 2026. Source baseline: released Sky v0.29.2,
`ec8c1c399a34597b396b1cff63255d30a0e308ce`.

Status: research and proposed architecture. No optimisation is implemented by
this document. Source inspection establishes the current mechanisms. It does
not establish their relative costs on a device. No new benchmark was run.

## Recommendation

Keep `App.app` and TEA as the application model. Add a compiler-generated change
representation, conservative dependency analysis, and a shared incremental
execution core. Specialise persistence and rendering through target adapters.
Execute effects through the existing scheduler. Unsupported operations retain
the reference execution path.

The first objective is precise: an update to a small field must not force Sky to
walk, encode, compare, or write unrelated large fields. The stronger objective is
incremental maintenance of derived collections and views where sound rules are
available. Both should require no application annotations for supported cases.

Do not promise constant latency for arbitrary programmes or unlimited model
sizes. Changing a filter can change every result. Producing an N-byte export
requires writing those bytes. First loading an N-byte model requires reading it.
Keeping it resident requires memory. Garbage collection can also depend on the
live heap. No compiler removes these physical and semantic limits.

The correctness commitment should be semantic preservation, including effects,
failures, input handling and persistence. A research paper's theorem does not
automatically prove Sky's Rust compiler, Go runtime, browser integration or
storage adapters correct.

## What the current source establishes

| Mechanism | Evidence at the baseline | Consequence |
|---|---|---|
| Per-step persistence | [`spaPersistAfterStep`](../../runtime-go/rt/spa_persist_wasm.go#L190) encodes the next model and, with protected fields, the previous model. | Session detection can revisit a large unchanged model. |
| Further JSON processing | [`spaIdentityOf`](../../runtime-go/rt/spa_persist.go#L265) and persistence helpers parse encoded data. | Merely accelerating the encoder leaves other whole-model passes. |
| Persistence writes | [`spaPersistWrite`](../../runtime-go/rt/spa_persist.go#L361) has no compiler-provided unchanged-model witness. | No-op steps can still pay storage costs. |
| View pipeline | [`renderCurrent`](../../runtime-go/rt/live_wasm.go#L769) runs the view, VNode conversion, path assignment, style injection and render planning. | A small DOM patch does not establish cheap view construction. |
| Lazy keys | [`lazyKey`](../../runtime-go/rt/lazy.go#L148) uses structural [`identityKey`](../../runtime-go/rt/identity_key.go#L107). | A cache hit can still traverse large arguments. |
| Existing RPC projection | [`spa_split.rs`](../../rust/crates/project/src/spa_split.rs#L1) emits branch read/write field sets with conservative fallbacks. | Extend existing analysis. Do not invent a replacement protocol without need. |
| Record representation | [`lower_update`](../../rust/crates/lower/src/lower.rs#L9048) copies typed Go records. | Source immutability is not uniform pointer identity. Copy cost also depends on record width. |
| Existing persistent collections | [`array_trie.go`](../../runtime-go/rt/array_trie.go#L1), [`dict_persistent.go`](../../runtime-go/rt/dict_persistent.go#L1). | Reuse existing path-copying structures. A wholesale collection rewrite is not the first step. |
| Server session storage | [`encodeSession`](../../runtime-go/rt/live_store.go#L1771) uses gob and persists session metadata as well as the model. | Browser JSON optimisation does not automatically optimise Sky.Live storage. |
| Terminal rendering | [`tuiAppState.render`](../../runtime-go/rt/tui_app_loop.go#L223) uses terminal size, focus, input and scroll state. | Model equality alone cannot suppress a terminal redraw. |

The historical [Sky.Live measurements](skylive-interaction-cost.md) also
separate view construction from diffing. Their commits, applications, view
sizes and machines differ from the present browser problem. Their throughput
figures must not be transferred to this workload.

We have evidence of avoidable work, not a measured allocation of every
millisecond. An aggregate browser typing metric includes several events,
asynchronous completions, browser layout and GC. It cannot isolate one phase.

## Research findings and their limits

### Self-adjusting computation

[Adaptive Functional Programming, Acar, Blelloch and Harper, POPL 2002](https://www.cs.cmu.edu/~rwh/papers/afp/popl02.pdf)
records dependencies and repairs a computation after input changes. Its
correctness result relates repaired output to full reevaluation. Its cost
analysis does not claim all changes have small effects.

[A consistent semantics of self-adjusting computation, Acar, Blume and Donham,
JFP 2013](https://www.cambridge.org/core/journals/journal-of-functional-programming/article/consistent-semantics-of-selfadjusting-computation/441A28C813BDA23B57F1ED2BB1A7E36E)
formalises memoising change propagation and machine-checks its metatheory in
Twelf. This is a model for the standard Sky should demand, rather than evidence
that a newly written implementation inherits the proof.

**Application to Sky:** establish equivalence to ordinary TEA before choosing
caching heuristics. Track data and control dependencies. Consider the memory
and bookkeeping cost of a dynamic graph. An entire runtime graph for every
primitive operation is not justified by the current measurements.

### Typed changes and static differentiation

[A Theory of Changes for Higher-Order Languages, Cai et al., 2013 preprint](https://arxiv.org/pdf/1312.0658)
gives a static transformation with a correctness proof in Agda for its typed
calculus. Crucially, primitive change operations have proof obligations, and
their performance depends on their implementation.

**Application to Sky:** define internal changes such as `Unchanged`, record-field
changes and full replacement. Derive changes compositionally through known
operations. Full replacement is always a conservative option. Efficient rules
for collections, closures and primitives are separate work, not an automatic
consequence of having a delta type.

### Stable names and incremental cache identity

[Incremental Computation with Names, Hammer et al., OOPSLA 2015](https://arxiv.org/pdf/1503.07792)
shows how naming helps reuse survive allocation changes and proves
from-scratch consistency for its calculus. The extended paper also identifies
subtle unsoundness in simplistic weak-reference graph cleanup.

**Application to Sky:** distinguish allocation identity, computation-instance
identity and business entity identity. Give caches bounded lifetimes. Eviction
must lead to correct recomputation. Do not derive entity identity from arbitrary
record contents or assume a closure's code address identifies its captures.

### Incremental collection queries

[DBSP, Budiu et al., PVLDB 2023](https://www.vldb.org/pvldb/vol16/p1601-budiu.pdf)
provides a compositional algebra for incremental query maintenance and a
Lean-checked mathematical development. [Differential Dataflow, McSherry et al.,
CIDR 2013](https://www.microsoft.com/en-us/research/publication/differential-dataflow/)
addresses incremental computation with nested iteration.

**Application to Sky:** use these as foundations for a later, restricted query
IR over collections and reactive data. They are not drop-in semantics for every
Sky list, arbitrary function, UI event or effect. Order and duplicates matter.
An IEEE floating-point fold cannot be reassociated or replaced by algebraic
subtraction merely because a theorem assumes an exact mathematical group.

Indexes and maintained intermediate results can reduce repeated work, but
occupy memory. A new predicate or join key can still invalidate a large result.
Do not claim that a one-field input delta implies a one-field output delta.

### Production incremental engines

[Jane Street Incremental's interface and algorithm](https://github.com/janestreet/incremental/blob/master/src/incremental_intf.ml)
describe an observed dependency DAG and topological stabilisation. Functions
that are not needed are not evaluated for side effects. Configurable cutoffs
are a deliberate application policy.

[Salsa's red-green algorithm](https://salsa-rs.github.io/salsa/reference/algorithm.html)
tracks revisions and dependencies to validate cached results. Dependencies that
may have changed cause reevaluation; change propagation can stop when results
remain equal.

**Application to Sky:** stabilise a coherent generation before publishing it.
Use revisions and conservative invalidation. Never adopt approximate numeric
cutoffs as an invisible language optimisation. Equality checks themselves need
cost accounting. Revalidating an enormous dependency graph on every key press
would recreate the scaling problem in another form.

### Production UI compilers

[React Compiler](https://react.dev/learn/react-compiler/introduction) demonstrates
automatic memoisation without widespread source annotations, under React's
rules. It does not promise to memoise every ordinary function or share every
cache across components.

[Compose strong skipping](https://developer.android.com/develop/ui/compose/performance/stability/strongskipping)
distinguishes instance and value equality and accounts for lambda captures.
Its documentation warns that deep comparison of large immutable collections
can cost more than recomposition.

[Solid's fine-grained reactivity](https://docs.solidjs.com/advanced-concepts/fine-grained-reactivity)
tracks reads through reactive primitives. Its [batch operation](https://docs.solidjs.com/reference/reactive-utilities/batch)
coordinates downstream work. [Elm's lazy rendering](https://guide.elm-lang.org/optimization/lazy)
uses function and argument identity to reuse a subtree.

**Application to Sky:** compiler automation is a credible direction. Sky should
infer safe reuse internally rather than require application authors to manage
dependency arrays. These systems do not establish an end-to-end Sky correctness
theorem, and their equality and effect assumptions must not be copied blindly.

### Storage and recovery

The [HTML Web Storage standard](https://html.spec.whatwg.org/multipage/webstorage.html)
explicitly warns against assuming locking across agent clusters. Several
`localStorage` field writes are not one transactional snapshot.

The [IndexedDB specification](https://www.w3.org/TR/IndexedDB-3/)
defines atomic transaction writes and durability hints. The cited version is a
working draft, not a blanket guarantee of identical implementation support.
Browser version, platform and storage policy require testing.

[SQLite atomic commit](https://www.sqlite.org/atomiccommit.html) and
[SQLite's failure testing](https://www.sqlite.org/testing.html) provide useful
models for transactional publication and systematic crash injection.
[Chrome's lifecycle documentation](https://developer.chrome.com/docs/web-platform/page-lifecycle-api)
explains why termination callbacks are unreliable on mobile.

**Application to Sky:** prefer transactional incremental storage to a sequence
of independent key writes. Preserve an old complete revision until a new one is
committed. Browser storage remains subject to eviction, user deletion and
platform failures. No background flush can make an uncommitted update durable
after an arbitrary process kill.

## Alternatives and why they are insufficient alone

| Approach | Value | Limit or correctness condition |
|---|---|---|
| Faster generated full codecs | Removes reflection and allocations | Full encoding and writing still scales with total state. Useful first component. |
| Deep equality or full fingerprints | Can detect equal results | Detection traverses state. Hash equality alone is not proof of value equality. |
| Persistent data alone | Makes local updates and sharing cheaper | Downstream render, codec and storage can still traverse the entire structure. |
| Debounce and event dropping | Reduces work frequency | Can alter recovery, command counts or input semantics. Not a transparent correctness-first fix. |
| Worker threads | Can move eligible CPU work off the UI thread | Serialisation, transfer, duplicated memory, platform support and main-thread DOM work remain. Effects and event order need preservation. |
| Smaller DOM patches | Reduces mutations | Does not remove whole-view construction, model encoding or layout dependencies. |
| General dynamic dependency engine | Can support complex changing dependencies | Graph maintenance, sound purity boundaries, reclamation and memory overhead need proof and measurement. |
| Compiler change evidence plus conservative reuse | Eliminates known irrelevant work without app annotations | Requires validated analysis and safe fallback. This is the recommended starting architecture. |

Background work remains an optional implementation technique inside that
architecture. It must consume a coherent immutable snapshot, respect the
scheduler contract and publish against the correct generation. Moving the same
full-model computation elsewhere does not establish small-change scaling.

## The guarantee Sky should specify

There are three different claims:

1. **Optimisation correctness:** the incremental programme preserves the
   observable semantics of the reference programme.
2. **Application correctness:** the programme satisfies its business invariants.
   This requires those invariants and the application or library logic to be
   specified and verified. A faster compiler cannot infer that a payment rule is
   the intended rule.
3. **Failure guarantees:** recovery and durability hold under stated assumptions
   about transactions, acknowledgements, storage and external systems.

Sky can make a strong first commitment and provide mechanisms for the others.
It cannot honestly guarantee all application logic or all hardware behaviour
without specifications and assumptions.

[CompCert's semantic preservation specification](https://compcert.org/man/manual001.html)
is a useful model for stating observable behaviour and a trusted boundary. Its
particular treatment of undefined C behaviour is not Sky's contract. Sky's
classified failures and recovery boundaries are observable and must be retained.

For a deterministic pure computation, the proposed law is:

```text
applyOutput(f(x), incremental_f(x, delta)) == f(applyInput(x, delta))
```

For an application, equality of final models is insufficient. Under the same
external input events and allowed completion schedule, require equivalence of:

```text
reference trace == incremental trace
```

The trace includes model values, command requests and arguments, ordering
constraints, handler payloads, navigation, classified failures, committed
restorable state and authentication transitions. Renderer equivalence includes
focus, selection, IME composition and accessibility behaviour where applicable.
Different targets have their own renderer observations. Their pixels need not
be identical to each other; each must preserve its own reference semantics.

Execution speed changes real event timing. Therefore a universal wall-clock
trace equality claim is inappropriate. Inject clocks, random results, external
responses and scheduler choices in the semantic model and differential tests.
Keep the same allowed concurrency relations. Do not turn concurrent Cmd batches
into an invented total ordering or promise exactly-once external delivery.

## Proposed architecture

### 1. Internal descriptors and sound change evidence

Have the compiler emit a private descriptor for the model and reusable pure
regions. Include field layout, codecs, dependencies, capability summaries and
code/schema versions. This is generated metadata, not a user-maintained list.

Emit conservative change evidence with updates:

| Source operation | Safe initial evidence |
|---|---|
| Return the existing model in an eligible region | No model writes only after capability analysis excludes hidden mutation. Still execute update and its Cmd. |
| Update one named record field | That field may have changed. Other forwarded fields remain unchanged. |
| Known helper | Compose its validated summary. |
| Unknown helper, reconstruction or opaque value | Treat affected output as replaced. Recompute normally. |

Eligibility also requires accounting for opaque mutation and runtime-context
invalidation. Aliasing the previous value is not by itself proof that nothing
changed. This is a runtime-path-aware witness, not just a branch's static may-write set.
A static overapproximation is safe but less precise. A same-value assignment can
remain dirty initially. Avoid requiring deep equality to discover that it is a
semantic no-op. False positives lose speed. False negatives lose correctness.

Stable immutable allocation tokens may later improve precision. They must not
depend on mutable backing objects, address reuse, hash collision probabilities
or wrapping revision counters. Invalidate on generation rollover. Include
closure environments. Keep identity tokens internal and do not change Sky's
value equality or serialised application schema. Array root identity alone is
insufficient: a tail update can retain the root. Include the immutable wrapper
and its complete logical state. Dict header/type metadata matters too.

Reuse Sky's persistent `Array` trie and `Dict` HAMT where appropriate. Sky also
already has [block-based List cons](../../runtime-go/rt/list_block.go#L1), rather
than a simple copy of the full list for every cons. Preserve its ownership and
copying invariants, ordered Dict enumeration, List semantics and FFI conversions. Audit
paths that clone or erase types. A wide Go struct still has copy cost; achieving
small-change scaling for wide records may need a later representation change.

### 2. Effect and dependency analysis before automatic reuse

A function's return type is not enough. [Sky's FFI boundary](../ffi/boundary-philosophy.md#why-result-not-task)
explicitly permits synchronous calls returning `Result`. `Task.run` can force an
effect in an expression. Trusted stdlib kernels also need audited summaries.

Track transitive capabilities: deterministic computation, executed effects,
runtime-context reads, opaque calls and failure behaviour. A higher-order call
is reusable only when the invoked function and captured environment satisfy
the contract. Unknown foreign or effectful calls are barriers. Keep executing
them in reference order. Do not reject previously accepted source merely to
simplify optimisation.

Track control dependencies as well as field reads. A conditional's predicate
and its changing selected branch matter. A dependency set from a previous run
must be repaired when control flow changes. Runtime-derived dependencies are
published only with the result of their completed evaluation.

### 3. Reuse pure view regions, including behaviour

Use conservative compiler regions first. Add dynamic dependency machinery only
where its measured benefit exceeds its cost. Invalidate through reverse edges
or precise change masks rather than scanning all cached regions on each event.
Stabilise affected regions before publishing a coherent view generation.

A region's result includes event handlers and their captured payloads. In
[`live_wasm.go`](../../runtime-go/rt/live_wasm.go#L809), a visually unchanged
element still receives a fresh handler payload. Caching only text and attributes
would break this behaviour. Preserve the event-start view used for bubbling in
[`dom_event_view.go`](../../runtime-go/rt/dom_event_view.go#L1).

Separate immutable reusable descriptions from per-instance mutable VNodes,
mount paths, handler slots and browser state. Never share a positioned mutable
VNode across siblings, sessions or targets. Cache instance identity must remain
sound through list insertions, reorders and duplicate items. Reuse by position
may be conservative. Inventing business keys is unsafe.

Runtime context is an input: URL, dimensions, focus, composition, held RPC input,
selection and relevant resources. An unchanged model does not prove the context
is unchanged. Unknown context dependencies trigger reference rendering.

Do not silently virtualise arbitrary HTML or omit off-screen nodes. Browser
search, accessibility, selection, layout and automation can observe them.
Virtual collections need a defined widget contract; they are not a universally
transparent optimisation of any existing view.

### 4. Generated codecs and transactional incremental persistence

First remove previous-model encoding from session detection using generated
protected-field projections. Then encode changed fields through generated
codecs. Preserve ADTs, Maybe, field naming, numeric behaviour, Secret handling,
unknown-field policy and existing custom-codec semantics. An opaque custom
codec may require a whole-value fallback.

A field cache alone does not solve persistence if Sky concatenates or writes
the full cached JSON after every edit. Use versioned independently stored
chunks and a transactional root revision. Collection chunks should eventually
follow persistent structure boundaries rather than treating a huge List as one
field-sized blob.

For browser targets, assess IndexedDB transactions for changed chunks, root
revision and metadata. Keep chunks immutable until publication and retain a
complete recoverable revision. Reclamation must also protect roots and chunks
needed by live readers through snapshot transactions, reader tracking or a
validated retry protocol. Treat read, validation and root update as one
transaction when coordinating writers. A manifest written last in localStorage
is not sufficient proof of multi-tab correctness or safe garbage collection.

For native targets, use an existing transactional store such as SQLite where
the target contract calls for local persistence. Sky.Live needs a model-chunk
store plus required session metadata commits under its existing locking and
rotation rules. Keep LastSeen, expiry, revocation and output sequence updates
even when the model is unchanged. Existing gob snapshots need a versioned
migration path. This is a storage format change, not a codec-only optimisation.

The root protocol should validate schema/code compatibility, base revision,
snapshot identity and an identity generation. Never apply a patch to a different
base silently. Reject, reload a coherent snapshot or resynchronise according to
the existing consistency contract. Do not automatically merge concurrent tab
edits as if they were commutative business operations.

On sign-out or identity rotation, invalidate pending old-generation writes
before they can publish. Preserve server authority over sessions. Preserve existing exclusion and protected-field rules. Server-verified seed
values retain authority over protected session fields. Cached descriptors and
data and mutable instance state must not cross identity or tenant boundaries.
Immutable code/type descriptors may be shared when they contain no instance or
identity data.

No transparent debounce in the initial design. An acknowledged durable commit
and a rendered-but-pending update are different states. Async storage may
improve responsiveness, but it must not silently extend the data-loss window.
If a future version changes the durability contract, specify it separately.
Surface failures through established error/observability mechanisms. Never
claim a state is saved when the required commit failed.

### 5. Keep RPC semantics and incrementality separate

Current branch field projections already avoid sending the entire model in
supported cases. Preserve that stateless mechanism first. Skip unused fields
and reuse encodings where safe without adding protocol state.

A patch within a large field or a difference since the previous request needs
a baseline identifier, revisions, code/schema checks and recovery rules. Add it
only after measuring the remaining wire cost. Preserve hold-RPC ordering,
dispatch-once semantics, retry deduplication and stale response handling. Never
replay effects merely to validate a cache.

### 6. Incremental collections as a later compiler capability

Add certified rules for selected operations after the basic change protocol is
stable. A persistent collection's node identity can skip unchanged subtrees.
An item replacement can update an indexed map or suitable aggregate. A changed
predicate may still touch every item. Do not disguise that work with a cache
lookup that itself hashes or compares the entire collection.

Preserve List order, duplicates, exact arithmetic behaviour, comparator calls
where observable, and classified failures. Keep ordinary evaluation as the
fallback for unsupported functions and algebraic laws. Reassociating Float
arithmetic is not an acceptable correctness trade for a faster incremental
aggregate.

## Complexity and cross-target contract

Use an accounting model rather than an unsupported universal Big-O claim:

```text
step work = required update work
          + invalidation and scheduling work
          + affected derived/view computation
          + changed encoding and publication
          + required effects and platform work
```

For supported local changes, require no traversal or encoding of unrelated
resident data. Invalidation should scale with changed paths and affected
consumers, not all cached nodes. Initial computation, global invalidation,
checkpointing, compaction and GC have separate costs. Bound their retention,
measure their pauses and avoid putting avoidable full checkpoints on the
typing path. Compaction still requires a crash-safe publication protocol.

This gives an achievable small-change objective such as path-depth work plus
affected computation and output bytes. It does not guarantee that affected
computation is small. A one-item change can legitimately affect a million
consumers. Total resident state still consumes memory on a phone.

| Target | Shared contract | Target-specific obligations |
|---|---|---|
| Sky.Live web/tablet/embedded desktop | Change evidence, pure dependencies, effects and failures | Session locking, TTL/rotation, SSE sequencing, replay and durable-store failure |
| Sky.Spa web:app | Same pure and scheduler semantics | Wasm/main-thread costs, DOM/IME/focus, transactional browser persistence, reload and tabs |
| Sky.Spa mobile/native desktop shells | Same supported incremental programme | Webview lifecycle, native capabilities, OS termination and actual storage adapter |
| Terminal TUI | Same pure dependencies | Resize, focus, scrolling, layout, cell painting and terminal event ordering |
| Terminal CLI/jobs | Same safe pure reuse where useful | Task execution and output; no invented browser persistence mechanism |

One source and shared contracts do not imply one storage implementation, one
UI context, or equal latency. Hardware, memory and browser engines differ.
Large-data paging belongs at a data boundary with defined access semantics.
Silently turning a resident pure value into hidden I/O changes the language.

## Proof obligations and verification strategy

Define a small formal core for changes, pure evaluation, region reuse and
publication. A proof assistant is appropriate for its laws. The choice among
Lean, Agda and another suitable system is secondary to keeping the trusted core
small and connecting it to executable code.

| Obligation | Required statement |
|---|---|
| Change evidence | An omitted path cannot differ in any observable dependency. |
| Pure region reuse | Same validated inputs/context/code imply the same value or classified outcome. |
| Change composition | Applying composed changes equals applying them in reference order. |
| Generated codecs | Incremental restore agrees with the established full codec under its compatibility rules. |
| Publication | Recovery selects a complete committed revision, never a mixture. |
| Session fencing | An older identity generation cannot publish after invalidation. |
| Effects | Reuse does not duplicate, drop or reorder effects outside the source's permitted ordering. |
| Cache lifecycle | Eviction and reclamation cannot change results or create cross-instance reuse. |
| Target lowering | Each adapter preserves its reference observations and error boundaries. |

The proof must distinguish mathematical values from machine representations.
Foreign mutation, unknown kernels and unverified transforms cannot be waved
away. Keep their reference path or prove an additional contract. If translations
emit certificates, the certificate checker also needs a soundness argument.
An unchecked sidecar declaring a function pure is not a proof.

Retain an unoptimised reference interpreter/path. Differential tests compare
reference and optimised execution under generated messages and controlled
external inputs. Run effects once through a test scheduler with recorded
results, not twice against live services. Differential testing and fuzzing find
counterexamples; they do not replace a universal proof.

Required cases include:

- Same model plus non-empty Cmd. Unknown higher-order functions, forced Tasks
  and synchronous FFI. Classified failure before and after render commit.
- Identical visual button with changed payload. Captured closures. Conditional
  dependencies. Nested synchronous events and bubbling. Duplicate list items,
  insertion, deletion, reorder and cache eviction.
- IME, caret, focus, selection, held-RPC typing, out-of-order completions,
  navigation and resize. Equal models in two sessions must stay isolated.
- Interrupted storage after every protocol operation. Quota denial, corrupt
  chunks, missing roots, code/schema mismatch, old-format migration and repair.
- Sign-out during a pending write, identity change in another tab, late RPC
  response, conflicting publications and GC of a still-referenced revision.
- Unchanged model with TTL renewal, session rotation, revocation and SSE
  sequence changes. Store failure must preserve the established failure rule.
- Wide records, large collections, pathological dependencies and bounded cache
  retention. Fuzzed hash collisions must never produce false equivalence.

## Measurement and release plan

Use public synthetic applications, synthetic data and locked toolchains. Do not
run private application credentials or production purchases in a benchmark.
Do not dump environment values. Capture only safe toolchain, commit, device and
scenario metadata.

Measure update, view evaluation, VNode conversion, ID/style work, diff planning,
DOM application, encode, decode, storage and GC separately. Count invocations,
nodes visited, encoded bytes, storage calls, allocations and retained cache
bytes. Counters prove absence of work; end-to-end latency proves user benefit.

Vary unrelated data independently from visible view size, record width, changed
bytes and affected consumers. Include 60 KB, 600 KB and several MB where the
device can support them. Test unchanged keys, normal input, async dispatch and
completion, one-item collection changes and intentional global changes.

Use Chromium, WebKit and Firefox; actual iPhone/iPad and Android hardware;
desktop ARM64/x86 where supported; native shells and TUI. CPU throttling is a
diagnostic, not a substitute for actual mobile CPU, memory and lifecycle tests.
Warm/cold caches and sustained runs matter. Report p50/p95/p99 and long tasks.

Proposed gates, to be calibrated against a measured baseline:

1. Proven unchanged eligible model/context: zero model codec calls and zero
   model writes. Commands and required metadata still run.
2. A small field edit: zero visits/encodes of a retained unrelated large field.
   The claim is verified by counters, not inferred from a fast laptop result.
3. Supported collection edit: work follows changed paths and affected results.
   Expected global changes are reported separately.
4. Every correctness trace and crash-recovery test passes with optimisation
   enabled, disabled and with forced cache eviction.
5. Small applications avoid material regressions; large applications have
   bounded incremental metadata and no monotonic cache growth in a soak.
6. A device-specific interaction budget is set before release. No latency
   improvement or flat-scaling result is claimed until measured.

Delivery order:

1. Instrumentation and reference conformance fixtures.
2. Session projections, internal change descriptors and safe no-op handling.
3. Generated encoders plus a versioned transactional persistence adapter.
4. Conservative automatic view-region reuse with handler/context coverage.
5. Incremental collection rules and any necessary representation changes.
6. Versioned large-field RPC deltas only if the remaining measurements justify
   their additional protocol and recovery complexity.

Each stage has its own proof obligations, failure tests and cross-target gates.
Supported conservative optimisations can ship before an ambitious general
self-adjusting engine. Until the relevant proofs and implementation connection
exist, describe the system as tested for semantic preservation, not formally
verified end to end.
