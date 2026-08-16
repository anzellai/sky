# Stage 1 — runtime constant factors on the interaction hot path

What the six proposed changes were worth once each was measured, including
the two that turned out not to be worth landing.

The attribution run (`../attribution-20260815/`) said where the interaction
cost went. This one says what removing it actually bought.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — arm64 |
| Base | `50c8dcee` on `feat/embedded-postgres` |
| Go | 1.26.1 |
| Fixture | `live_alloc_gate_test.go`'s `buildHtmlPage(96)` — 389 elements, 290 texts, 96 event bindings, renders to **37,538 bytes** |
| Load | shared with two other agents throughout; load average moved between 2.2 and 12.3 |

**Allocation counts are the signal; wall-clock is corroboration.** The
counts reproduced EXACTLY across runs and across a loaded and an idle host
— 692 allocations was 692 every time. Wall-clock on the same binary moved
70% between a quiet and a busy minute, so every wall-clock figure below is
the median of five **alternating** base/new reps (`ab.sh`, the pattern from
`../hof-dispatch-20260815/`), with the spread quoted.

## The fixture excludes what the Sky `view` costs

`buildHtmlPage` is hoisted OUT of every measured loop. Building the `Html`
ADT is what the compiled Sky `view` does, above the boundary these changes
live at, and it is 4,560 allocations — more than the entire runtime path.
An early version of these numbers left it inside the loop and every figure
was diluted by roughly half.

## Per-interaction, below the Sky boundary

Lower the view ADT → assign sky-ids → style passes → render + collect
handlers → diff.

| | allocations | bytes |
|---|---|---|
| Base `50c8dcee` | 4,499 | 667,212 |
| after all five landed changes | **2,061** | **335,458** |
| | **-54.2%** | **-49.7%** |

### Where it went

| component | allocs before | after | bytes before | after |
|---|---|---|---|---|
| `HtmlToVNode` | 1,368 | 1,368 | 266,784 | 266,784 |
| `assignSkyIDs` | 388 | 388 | 8,504 | 8,504 |
| four style passes | 6 | 6 | 848 | 848 |
| `renderVNode` | 2,632 | **193** | 380,440 | **48,640** |
| `diffTrees` | 96 | 96 | 1,536 | 1,536 |

## What landed

1. **One builder for the whole render** (not in the original six). Every
   element had its own `strings.Builder`, produced a string, and the parent
   copied those bytes into its own — so a leaf's bytes were copied once per
   level above it. 2,632 → 692 allocations, 380 kB → 176 kB.

2. **Item 6** — the per-event-binding `fmt.Sprintf`, the `reflect` +
   `FieldByName` in `msgDisplayName` for legacy `SkyADT`, the four
   throwaway handler maps in `diffNodes`. 692 → 212 allocations;
   `msgDisplayName` 46.4 ns → 4.2 ns.

3. **Item 3** — one marker scan decides which of the four style passes have
   work. 66.2 µs → 37.3 µs with one marker present; 65.2 µs → 18.3 µs with
   none, which is the common case for at least three of the four passes.

4. **Sized render buffer** (not in the original six). The builder grows once
   to the length of the body this session rendered last time. 162 kB →
   48.6 kB, a 4.32× bytes-to-output ratio down to 1.30×.

5. **Item 5** — `ackInputsForPrevTree` built a ~390-entry set of every
   sky-id in the tree to look up the one or two ids the user has dirty. It
   now searches for them and stops when it has them. 20.2 µs → 0.68 µs when
   the field is near the top, 20.0 µs → 6.4 µs at the end; 15 → 2
   allocations, 27,112 B → 256 B.

## What did NOT pay — measured, not assumed

### Item 1's residual: skipping the render on the patch path

The premise was right at the time it was written: `renderVNode` was 2,632
allocations, 58.5% of the interaction, and the patch path threw the string
away. But changes 1 and 4 above removed **92.7% of its allocations** and
**87.2% of its bytes** without touching handler registration, the
suppression contract or the wire protocol.

So the split was measured against what was left. `markView` — walk the tree
through the same renderer, writing into a `maphash` instead of a buffer, so
the handler table is collected and the body is never built:

| | wall | allocs | bytes |
|---|---|---|---|
| `renderBody` (builds the 37.5 kB string) | 84 µs | 194 | 48,672 |
| `markView` (builds nothing) | **87 µs** | **192** | 7,700 |

**No wall-clock saving and two allocations.** The render's remaining cost is
not the string — it is the traversal, the escaping, and 192 allocations
that are the per-element `evKeys` slice and the `skyID + "." + ev` handler
key. Building the buffer, once it is sized, is nearly free.

Against that: replacing `lastComputedBody`/`lastShippedBody` with a digest
puts a 2^-64 chance of a wrongly-suppressed frame into a contract with a
documented history of freezing keypress dispatch. Dropped.

### Item 1's other half: dropping the two retained body strings

`lastComputedBody` and `lastShippedBody` hold 37.5 kB each, for the life of
the session, and an audit of all 19 sites found they are only ever compared
— never shipped, never persisted (`storableSession` has never carried
them), and `lastComputedBody` is never even compared, only saved and
restored. A `maphash` mark is 8 bytes and costs 3.5 µs per body (10.8 GB/s).

That would cut per-session retention by 75 kB — 22% of the 336 kB a session
holds. It was still dropped, because **`../skylive-remote-validation.md`
measured CPU binding ~12× before memory** on real GCE instances: an
e2-micro holds ~450 sessions and is unusable past ~50. The change spends
the binding resource to save the abundant one. A smaller live heap also
raises GC frequency at a constant allocation rate, so the second-order
saving is not clearly positive either.

Worth revisiting if a future stage makes an instance memory-bound.

### Item 2: caching `unwrapADTShape`'s field indices

`unwrapADTShape` does `fmt.Sprintf("V%d", i)` + `FieldByName` +
`FieldByIndex().Interface()` per field — but only on its **sealed-interface
path**, and that path is unreachable from the two call sites named
(`HtmlToVNode` at live.go:114 and `applyHtmlAttr` at live.go:185).

Both receive `Std.Html.Html` / `Std.Html.Attribute`, and
`rust/crates/lower/src/lower.rs:1774` `should_seal_prefix` excludes every
`Std_`, `Sky_Core_` and `Sky_Http_` union from sealing — deliberately,
because the runtime constructs those values as `rt.SkyADT` itself. So both
sites take the legacy fast path at `adt_shape.go:68` and return before
reaching any reflection.

The reflect path IS live for app-module `Msg` ADTs, which are sealed — but
that is one call per dispatch (live.go:5098), not one per node and one per
attribute. Nothing to cache. No change made.

## Still outstanding

**Item 4 — ordered slices instead of `map` + `sort.Strings` for
`VNode.Attrs`/`Events` — is NOT done, and it is now the largest single item
left.**

| what | allocations |
|---|---|
| `setAttr` — the per-element `Attrs` map | 574 |
| `setEvent` — the per-element `Events` map | 198 |
| `attrKeys` / `evKeys` in the render | 192 |
| **total** | **964 of 2,061 — 47%** |

Two maps of 2 allocations each (hmap + bucket) per element is irreducible
for a Go map; only dropping the map removes it.

The constraint that shapes the fix: the slices must be kept **sorted by
key**, not in insertion order. Emission order today is `sort.Strings` over
the keys, and `build-run --golden` pins whole-program stdout. Insertion
order would be deterministic — so `xtask repro` would stay green — while
still changing the emitted bytes, which is the failure `--golden` exists to
catch. Sorted-insert into a 2-4 element slice keeps the bytes identical.

Surface: ~40 production sites (`live.go` concentrated in `setAttr`,
`applyHtmlAttr`, `renderVNodeInto`, the style-injection passes, `diffNodes`
and the view fingerprint at live.go:5497) plus ~87 sites across 14 test
files that build `Attrs` as map literals.

## Gates added

- `TestRenderBodyByteBudget` — bytes allocated per byte of HTML emitted,
  budget 2.0×, passing at 1.30×, failing at 4.32×. The **first bytes gate**
  in `live_alloc_gate_test.go`, whose own header lists bytes as blind spot
  2 after a change lowered the allocation count 6.7% while raising bytes 8%.
  An allocation gate would have scored change 4 above as a 9% win and
  missed the 70%.
- `TestStyleInjectionGuardRunsEveryPassItsMarkerNeeds` and
  `TestStyleMarkerScanIsDerivedFromTheSpecs` — the marker scan cannot skip
  a pass that has work. Driven off `styleMarkerPasses`, so a pass or marker
  added later is covered without anyone remembering.
- `TestAckInputsFindsEveryDirtyIdWhereverItSits` — the early exit in the
  ack walk must not return its first match.

Every one was proven able to fail by mutation, and **the mutation was
confirmed present in the file before the red was believed**: one `perl`
substitution in this session silently matched nothing and reported a
passing test as proof.

## What these gates would NOT catch

- **The four style walks coming back.** The two new style gates check
  correctness — that no pass is wrongly skipped. Nothing gates the *number*
  of traversals, which is what change 3 bought. Reverting
  `applyStyleInjections` to four unconditional calls leaves every assertion
  in the repository green. A wall-clock gate on this shared host would be a
  coin toss, so this is deliberate, and it is a real hole.
- **`HtmlToVNode` and `assignSkyIDs`**, 1,756 of the remaining 2,061
  allocations, have no per-component budget at all — only the loose
  `interactionAllocBudget`, which is diluted because it measures the
  fixture builder alongside the runtime.
- **Retention.** Everything here measures the churn of one interaction. What
  a session HOLDS is `memrun.sh`'s question and no assertion here sees it.
- **Anything above the Sky boundary** — the user's `update` and `view`, 84%
  of the handler per the attribution run.
