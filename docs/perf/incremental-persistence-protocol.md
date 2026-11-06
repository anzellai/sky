# Incremental persistence: protocol obligations

Status: design constraints, not an implemented or validated storage backend.
This accompanies [the execution plan](incremental-execution-plan.md). The
current reference remains synchronous whole-model localStorage persistence.

## Observable reference boundary

An update executes once. `spaTransition` keeps the previous model if update or
render fails before render commits. A post/Cmd failure after that commit keeps
the next model. Persistence then attempts to write that **kept** model, and
session-clear handling follows the existing encoder/write order. Persistence is
best effort when the browser denies storage; this does not justify introducing
additional loss windows while storage works.

Every step republishes its model, including an ignored key. In two tabs, A can
overwrite B's last snapshot even when A's own model is unchanged. Avoiding an
encode must therefore preserve publication; suppressing all no-op writes would
change the existing last-writer semantics. Metadata, identity fencing and
session-clear decisions remain separate obligations.

## Why an asynchronous replacement is insufficient

Publishing an IndexedDB transaction after rendering introduces a visible edit
that immediate reload can lose. Publishing before rendering can instead restore
a model whose render failed, whereas reference execution retains the previous
model. There is no transaction spanning DOM commit and IndexedDB commit.

Immutable localStorage chunks plus one manifest avoid neither cross-tab garbage
collection races nor quota limits by themselves. An unpublished writer and a
reader restoring an older root must be protected from collection. Wall-clock
leases and arbitrarily long retention are not correctness proofs.

## Candidate hybrid boundary

Prepare immutable chunks in IndexedDB while holding the existing message
scheduler before model publication. Wait for transaction completion. Render and
post using the existing panic/commit rules, then synchronously publish a small
localStorage root for the model actually kept. The root is the publication
boundary, not the preparatory database transaction. A no-op still republishes
its root. Failed preparation must use the full synchronous reference path
where that path remains available.

This candidate requires a coordinator shared by **all** readers, preparers,
publishers and collectors in the same storage partition. It must protect roots
until publication and pin boot readers until their read transaction finishes.
An old tab must validate or recreate its retained root before republishing it
after another tab has collected unreachable chunks. Identity changes cannot
make another identity's data eligible for restore. Reference-format migration,
mixed old/new tabs and unsupported browser capabilities need explicit rules.

### Pre-protocol tab compatibility

An older **protocol-aware** tab can participate in coordination and revalidate
its retained root. A pre-protocol tab cannot: it still reads and publishes full
JSON at the existing model key and does not acquire the new locks.

A separate root key cannot preserve publication order with those tabs. Suppose
the existing key contains model A, a new tab publishes root B without updating
that key, and a suspended pre-protocol tab resumes and republishes A. The
existing key still contains A; the root still names B. Republishing an identical
value produces no storage event, so this state cannot reveal whether A was
published before or after B. The [HTML `setItem` algorithm](https://html.spec.whatwg.org/multipage/webstorage.html#dom-storage-setitem)
returns before broadcasting when the stored value already equals the supplied
value. Replacing the existing key with a root instead breaks pre-protocol
restore.

The conservative compatibility policy is therefore to keep full JSON at the
existing key as the sole publication authority while pre-protocol contexts may
exist. New tabs must continue synchronous full-JSON publication, including
unchanged steps, with the existing identity, sign-out and size-cap rules. An
optional chunk sidecar may only cache the current authoritative value after
content, schema and identity validation; it must not resurrect state after that
value changes or disappears, or introduce additional storage-failure windows.
This policy does not remove the cost of whole-model publication.

Root-only publication requires a storage scope known never to have hosted
pre-protocol code, or an explicit compatibility cutover that accepts changed
old-tab restore and last-writer semantics. Timeouts, heartbeats and a staged
deployment do not prove that suspended pre-protocol writers are gone. These are
design constraints and a candidate policy, not an implemented migration.

Web Locks associate coordination with a storage bucket and release locks on
agent termination; they do not turn the two storage APIs into one transaction.
Unsupported coordination must select reference execution rather than an unsafe
chunk backend. [Web Locks specification](https://www.w3.org/TR/web-locks/).

IndexedDB's `complete` event and transaction durability option have distinct
meaning. The `strict` hint requests completion after persistent-medium writes;
browser eviction and unavailable storage still require handling. Neither the
hint nor a successful prepare publishes the application snapshot.
[IndexedDB specification](https://www.w3.org/TR/IndexedDB/).

## Required falsification matrix before enabling

- Terminate/reload between each prepare, render, post, root publication and GC
  boundary; assert the reference-kept model and identity decision.
- Inject update/view/paint/post failures separately, including post failures
  after successful render.
- Run concurrent tabs with conflicting roots, ignored keys, boot reads,
  suspended preparation, identity changes, sign-out and collection.
- Deny locks, IndexedDB, localStorage or quota independently; require the
  documented reference fallback and continued message ordering.
- Restore legacy snapshots and reject malformed, missing, wrong-identity or
  incompatible chunk graphs without partially restoring a model.
- Prove bounded retained storage without deleting any published, pending or
  pinned root; record work against changed data and independent retained size.

The hybrid remains a candidate until these obligations have executable proofs
and browser evidence. No change in durability or multi-tab semantics is assumed
to be authorised by the performance goal.
