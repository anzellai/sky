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

### Wall-clock, five alternating base/new reps on a quiet host

`HtmlToVNode` is the CONTROL: untouched by any of these changes. It moves
0.6%, which is what says the other three numbers are the code and not the
machine.

| bench | base (median) | new (median) | Δ |
|---|---|---|---|
| whole interaction | 492.2 µs | 396.8 µs | **-19.4%** |
| `renderVNode` | 149.7 µs | 85.6 µs | **-42.8%** |
| `diffTrees` | 54.3 µs | 47.3 µs | -12.8% |
| `HtmlToVNode` *(control)* | 200.4 µs | 199.3 µs | -0.6% |

Per-rep spreads (µs), base then new:

```
Interaction   493.9 492.2 508.2 491.8 491.6  |  397.4 394.5 398.8 396.8 395.9
renderVNode   149.5 149.7 152.5 149.4 150.5  |   85.6  86.7  97.3  85.2  85.3
HtmlToVNode   199.0 200.4 200.4 200.4 204.2  |  200.4 201.0 199.3 197.8 198.8
diffTrees      54.1  54.6  54.7  54.3  54.2  |   47.2  47.3  51.4  47.8  47.0
```

The interaction figure UNDERSTATES the total: the fixture carries one style
marker, so it does not show the marker-free style saving, and it does not
include `ackInputsForPrevTree` at all (that runs only once an input has been
typed).

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

## Verification

Sequential, bounded through `scripts/lib/with-timeout.sh`, on the branch as
landed. Every leg's `rc` was captured to a ledger BEFORE any pipe, which is
how three of them were caught reporting success wrongly (see below).

| leg | result |
|---|---|
| `cargo test --release --workspace` | 988 passed, 98 suites, 0 failed, **0 ignored** |
| `CGO_ENABLED=1 go test -race ./rt/...` | 5 packages ok, **0 data races** |
| `xtask coerce-floor` | PASS — adapter exact at 24 — **see the coverage note below** |
| `xtask repro` | PASS — **byte-stable 50/50** building, 50/50 emitting |
| `xtask infer` | PASS |
| `xtask roundtrip` | PASS |
| `xtask build-run --golden` | PASS — **8/8 matched committed goldens** |
| `scripts/build.sh` | rc=0 |
| `scripts/example-sweep.sh` | **29 passed, 0 failed** |
| `scripts/doc-examples.sh` | PASS — 14/14 doc examples compile |

`repro` and `golden` are the two that had to stay green and did. Nothing was
re-baselined.

> **Coverage note, added 2026-08-16 — two of these PASSes covered less than
> they read.** Neither gate stated its denominator at the time, and both
> defaults were narrower than the table implies:
>
> * `xtask coerce-floor` locks a floor **per project** across a 61-row golden.
>   A project whose generated FFI surface is absent could not be measured, and
>   the gate filed it under "did not emit (not gated)" and passed on the rest.
>   On a clean checkout that is **56 of 61 rows** — `03-tea-external`,
>   `05-mux-server`, `08-notes-app`, `11-fyne-stopwatch` and `13-skyshop` need
>   a `sky install` (`sky-ffi/` and `.skydeps/` are `.gitignore`d).
> * `xtask build-run --golden` **selects a subset without `--all`**: 8 of 24
>   committed goldens, which is why the row above reads 8/8. Stage 4 found and
>   documented this one.
>
> **What the retrospective shows: the window hid nothing.** All 61 rows were
> measured in one run on 2026-08-16 against the then-current golden, and the
> five that this stage could not see came back `ok` ×3 (`03-tea-external`,
> `05-mux-server`, `11-fyne-stopwatch`) and `tightened` ×2 (`08-notes-app`
> −21 narrow, `13-skyshop` −34). **None widened and none raised `adapter`**,
> so this stage's "nothing rose" holds on the full corpus — it was narrower
> than it read, not wrong. That is a verified result, not an assumption.
>
> Both holes are now closed at the gate rather than left to a reader: an
> unmeasurable `coerce-floor` row fails and names the `sky install` that fixes
> it, and both its verdict lines carry the denominator.

### Real-app end to end (`examples/19-skyforum`)

The corpus gates are necessary and not sufficient here: these changes move
where the HTML bytes are produced and what buffer they land in, and the
thing that would break is a live session, not a compile. Driven over the
real wire protocol:

- **15/15** dispatches of a FRESHLY RENDERED handler id resolved, **0
  desync**; 11 of them returned non-empty patches.
- Msg names render correctly on real sealed-variant ADTs
  (`sky-click="UpvotePost"`, `"DownvotePost"`, `"Navigate"`) — the check
  that matters for the `msgDisplayName` change.
- **Form submit works**: navigated four steps to the sign-in form,
  dispatched `sky-submit`, got real HTML patches back
  (`{"id":"r.1#div.1#form.0#div","html":"<h2 …>Sign in</h2>…"}`), no
  desync. Subtree-replace patches are the path that exercises
  `renderChildrenHTML`'s nil handler table.
- Two fresh renders of the same state are **byte-identical (109,411
  bytes)** once the per-session `__skySid` / csrf token are normalised —
  the runtime counterpart of what `repro` pins at build time.
- **0 panics** for browser-shaped requests.

Two probe artefacts were chased down rather than accepted, and both are
worth recording because either could have been mistaken for a regression:

1. **20/23 "desync" on the first pass** was correct behaviour. Handler ids
   are position-derived, so the first dispatch that changes the view
   invalidates every id captured from the previous render. Re-rendering
   between dispatches, as a browser does, gives 15/15 clean.
2. **A recovered `rt.Coerce` panic** came from the probe POSTing a curried
   Msg handler with no `value`, so a bare string reached a slot expecting a
   record. Sending a value, as a browser does, gives zero panics.

### Three legs reported success while failing

Recorded because the pattern is the one `AGENTS.md` documents for bare
`timeout`, and it recurred twice more here in new forms:

- `example-sweep.sh` returned **rc=2** (it needs `sky-out/sky`, which the
  `xtask` gates do not build) while the surrounding pipeline exited 0,
  because a trailing `tail` took the status.
- The retry returned **rc=1** having run nothing: `noclobber` is set in this
  shell, so `> existing.log` failed — and the stale log from the first
  attempt read exactly like a fresh result.
- A `perl -0pi` mutation intended to prove a gate could fail matched
  nothing and reported the gate PASSING. Every mutation in this session was
  subsequently confirmed present in the file with `grep` before its red was
  believed.

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
