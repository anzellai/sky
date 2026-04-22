# Sky.Live input authority protocol

Design spec for the reliability layer that guarantees **no keystroke is ever lost**, regardless of latency, race conditions, dropped requests, out-of-order responses, page navigation, or server restarts. Supersedes the purely-positional morph algorithm documented in [architecture.md](architecture.md).

Status: **design — not yet implemented**. Approval gate before code lands.

## Motivation

The current runtime has three structural weaknesses:

1. **Positional sky-ids collide across pages.** `assignSkyIDs` in `live.go:836` emits `r.0.1.2` — purely the walk index. Two pages that share an outer wrapper (`<div class="container">`) but diverge internally end up with matching ids at structurally different elements. The diff at `diffNodes` in `live.go:911` recurses into them, tries to patch, and emits wrong HTML.

2. **No patch preservation for focused inputs on the JSON path.** `__skyApplyPatches` in `live.go:2160` does `el.innerHTML = p.html` blindly. If the server sends an innerHTML patch at any ancestor of a focused input, the input is wiped mid-keystroke. Only the `__skyMorph` path (used for full-HTML responses, not the common event path) has any focus protection, and it only covers same-tag matches.

3. **No ordering or replay protection.** Two fetches in flight can return in reversed order; the client applies both in arrival order. The older response clobbers the newer.

Under mild load on sendcrafts, this produces visible input duplication on page navigation and occasional keystroke loss. Under adversarial conditions (slow 3G, tab-switching while typing, concurrent submits) it becomes unusable.

The fix is a coordinated protocol change: **stable structural identities, monotonic sequence numbers, and client-side authority for dirty DOM state.**

## Guarantees

The protocol guarantees, for any Sky.Live app recompiled against the new runtime:

- **G1: Keystroke preservation.** A character typed into an input element will not be lost to any combination of server latency, dropped patches, out-of-order responses, or patch applications — up to the point where the user blurs the input, submits, or navigates.
- **G2: Navigation durability.** Pending debounced input values are flushed to the server before any navigation Msg is dispatched.
- **G3: Monotonic consistency.** Patches are applied in sequence order. Stale patches are silently dropped.
- **G4: Collision-free identity.** No two VNodes in a single render tree share a sky-id. Structurally-different subtrees across renders never collide.
- **G5: Zero app-side changes.** Existing Sky and Sky.Live apps gain all of G1–G4 automatically after rebuild. No new syntax, no required attributes.

Explicitly **not** guaranteed (out of scope for v1):
- Offline buffering (if network is down for minutes, the loader surfaces the error).
- Multi-tab conflict resolution (last writer wins, per current session-mutex behaviour).
- Server-restart resync (next event returns 404; client must reload).

## Wire format

### Sky-id grammar

```
sky-id    = "r"                                        -- root
          | parent-id "." index "#" tag [ ":" key ]

parent-id = sky-id
index     = 1*DIGIT
tag       = 1*ALPHA                                    -- lowercase HTML tag
key       = 1*( ALPHA / DIGIT / "-" / "_" )            -- disambiguator
```

**Key priority** (first match wins):
1. Explicit `Html.keyed "k" node` wrapper → `k`.
2. `name` attribute on `input`, `textarea`, `select`, `form`, `button`, `fieldset` → the attribute value.
3. No key.

Examples:

| VNode | sky-id |
|---|---|
| root `<div id="sky-root">` | `r` |
| first child `<div>` (CSS stylesheet) | `r.0#div` |
| `<header>` at index 1 | `r.1#header` |
| Email input inside a form | `r.2#main.0#form.3#input:email` |
| Keyed list item | `r.4#ul.0#li:todo-42` |

**Properties:**
- Unique within a single render (structural walk produces unique paths).
- Stable across renders when the element is structurally the same (same walk path, same tag, same key).
- Automatically differentiates across renders when structure diverges (different tag → different id).

### Request: `POST /_sky/event`

```json
{
  "sessionId": "3f2a8b...",
  "seq": 42,
  "msg": "UpdateEmail",
  "args": ["alice@example.com"],
  "handlerId": "r.2#main.0#form.3#input:email.input",
  "inputState": {
    "r.2#main.0#form.3#input:email": {
      "value": "alice@example.com",
      "seq": 42
    },
    "r.2#main.0#form.5#input:password": {
      "value": "hunter2",
      "seq": 40
    }
  }
}
```

New fields:
- **`seq`** (int64, monotonic per session, client-owned): every `POST /_sky/event` increments it. Starts at 1 on first event.
- **`inputState`** (object, optional): snapshot of every input the client considers "dirty". The key is the input's sky-id. Each entry records the *current* DOM value and the seq when the user last typed it. Server uses this to reconcile its idea of the model with reality.

Backward compat: old clients omit `seq` and `inputState`. Server treats missing `seq` as `0` (oldest), missing `inputState` as empty.

### Response: event reply

When the server has usable patches to send back as JSON:

```json
{
  "seq": 42,
  "ackInputs": {
    "r.2#main.0#form.3#input:email": 42,
    "r.2#main.0#form.5#input:password": 40
  },
  "patches": [
    {"id": "r.2#main.0#form.3#input:email", "attrs": {"value": "alice@example.com"}},
    {"id": "r.1#header.0#span:greeting", "text": "Hi, Alice"}
  ]
}
```

New fields:
- **`seq`** (int64): a **single session-wide monotonic** stamp — not an echo of `req.seq`. The server bumps `sess.outSeq` whenever it produces an outgoing frame (event reply OR SSE patch). This makes cross-channel ordering well-defined: a later frame always has a higher seq than every earlier frame, regardless of channel. Client tracks one `__skyLastAppliedSeq` and drops anything `≤` it.
- **`ackInputs`** (object, optional): per input sky-id, the largest `inputState[id].seq` the server has processed. Client uses this to retire "dirty" flags once the server has caught up.
- **`respondingTo`** (int64, optional): the `req.seq` this reply is answering. Informational only; the authoritative ordering signal is `seq`. Omitted on SSE frames (nothing to respond to).

When the server needs to send a full HTML body (first interaction, or patches all target root):

- `Content-Type: text/html`
- `X-Sky-Seq: 99` header (single session-wide seq, same counter as JSON path)
- `X-Sky-Ack-Inputs: {"r.2#...": 42, ...}` header (JSON-encoded compact form)

### Batched events: `POST /_sky/event` with `batch` field

Used by `__skyFlushAllPendingSync` on tab unload (via `navigator.sendBeacon`). Same endpoint as single events — no second route:

```json
{
  "sessionId": "3f2a8b...",
  "batch": [
    {"seq": 42, "msg": "UpdateEmail", "args": ["a@b.com"], "handlerId": "..."},
    {"seq": 43, "msg": "UpdatePhone", "args": ["+44..."],  "handlerId": "..."}
  ]
}
```

Server processes batch entries sequentially under `sess.mu`, dispatching each Msg as if it arrived on its own. Response body is ignored by `sendBeacon`, but the server still stamps out frames (via SSE) so other clients / tabs observe the state changes. When `batch` is set, top-level `msg`/`args`/`handlerId`/`seq` are ignored.

### SSE frames: `GET /_sky/subscribe`

```
event: patch
data: {"seq": 99, "ackInputs": {}, "patches": [...]}
```

Same shape as the JSON event reply. SSE frames and event replies share the **single session-wide `sess.outSeq` counter** — every mutation of session state produces the next seq, regardless of what triggered it. The server already has a single linearisation point (`sess.mu`), so there's one true ordering; exposing it as one counter lets the client resolve cross-channel races correctly. A Cmd goroutine that completes during an in-flight event reply will have its SSE frame stamped *after* the reply if the reply's dispatch ran first, or *before* if the goroutine's dispatch ran first — and the client applies in that exact order.

## Client state

```js
// Top-level session state.
var __skyClientSeq = 0;             // monotonic; client-owned, tagged on every outgoing event
var __skyLastAppliedSeq = 0;        // monotonic; server-owned, drop any frame with seq ≤ this
var __skyInputs = {};               // per sky-id → InputEntry
```

Two counters, different directions: `__skyClientSeq` is what the client stamps on outgoing events (so the server can match an inputState snapshot to its producing event). `__skyLastAppliedSeq` tracks the server's session-wide `outSeq` — a single counter covering both event replies and SSE frames, because the server has a single mutation order.

```ts
interface InputEntry {
  liveValue: string;       // most recent DOM value the user typed
  lastSentSeq: number;     // seq when this value was bundled into inputState
  lastAckedSeq: number;    // largest seq the server has acked for this id
  pendingDebounceId: number | null;  // window.setTimeout handle
  pendingSend: {msgName: string; args: any[]; hid: string} | null;
}
```

### State transitions (per input `I`)

| Event | Update |
|---|---|
| `input` fires on `I` | `I.liveValue := el.value` |
| Debounce schedules a send | `I.pendingSend := {...}`, `I.pendingDebounceId := setTimeout(…)` |
| `__skySend` flushes `I` | `I.lastSentSeq := ++__skySeq`, include `I` in `req.inputState`, clear pending |
| Response arrives with `ackInputs[I.id] = N` | `I.lastAckedSeq := max(I.lastAckedSeq, N)` |
| Patch arrives with `attrs.value` on `I` | See "Authority rule" below |
| `I` becomes unmounted (not in new DOM) | `delete __skyInputs[I.id]` |

### Authority rule (client-side patch filter)

When `__skyApplyPatches` processes a patch `p` targeting an element that is an input/textarea/select with `attrs.value`, `attrs.checked`, or `attrs.selected`:

```
entry = __skyInputs[p.id]
el    = querySelector([sky-id=p.id])

if entry exists AND (el is focused OR entry.pendingDebounceId != null):
    DROP the value/checked/selected keys from p.attrs
    // apply the rest of the patch (class, style, aria-*, etc.)

else if entry exists AND p.attrs.value == entry.liveValue:
    DROP (server is echoing our own value — no-op)

else if response.seq <= entry.lastAckedSeq:
    DROP the whole patch (stale)

else:
    APPLY
    entry.liveValue = p.attrs.value   // realign
```

The same filter runs for innerHTML patches targeting ancestors of a focused/dirty input: **an innerHTML patch that would wipe a dirty input is rewritten to a scoped patch that preserves the input's subtree.** Implementation: before applying `innerHTML`, scan the new HTML for sky-ids, diff against existing dirty inputs, and if a dirty input is inside the target, fall back to per-element patching for that subtree (morph-style).

This preserves G1 even when the server sends a "wipe your whole form and rebuild" patch.

## Server state

```go
type liveSession struct {
    // ... existing fields ...

    // Single monotonic counter for every outgoing frame (event reply OR
    // SSE patch). Bumped under sess.mu, so reflects the session's actual
    // linearisation order. Client uses this as the authoritative sequence.
    outSeq int64

    // Per-input-id → largest client seq the server has observed.
    // Used to populate ackInputs on every response. Evicted when the
    // corresponding input is no longer in the rendered tree.
    inputSeqs map[string]int64
}
```

**Why one counter not two:** every state mutation (dispatching a user event, completing a Cmd goroutine, applying a subscription tick) passes through `dispatch()` which holds `sess.mu`. There's exactly one true order. A single counter surfaces that order to the client; two counters would let the client accidentally reorder unrelated mutations that the server had already serialised.

### Server-side input reconciliation — fused into diff

Rather than pre-walk `prev` to rewrite values (O(n) extra pass, requires an index to be efficient at scale), the client's `inputState` is passed **directly into `diffNodes`** as a lookup table. The diff already walks the tree — we just consult the lookup at input nodes:

```go
func diffNodes(old, new_ *VNode, clientState map[string]string, out *[]Patch) {
    // ... existing tag/kind check, attr diff ...

    // When diffing an input-tag value/checked/selected attr, treat the
    // client-reported value as the effective old value. This prevents the
    // diff from emitting a patch that reverts the user's in-progress typing.
    if isInputTag(new_.Tag) {
        if cv, ok := clientState[old.SkyID]; ok {
            if new_.Attrs["value"] == cv {
                delete(attrChanges, "value")  // server matches DOM — no patch
            }
            // else: server genuinely wants to overwrite (form reset, etc.)
            //       emit the patch; Step 3 client filter decides whether to apply
        }
    }

    // ... recurse into children with same clientState ...
}
```

On every `POST /_sky/event`:

```
1. Read req.seq, req.inputState.
2. For each (id, {value, seq}) in req.inputState:
     if sess.inputSeqs[id] < seq:
         sess.inputSeqs[id] = seq
3. Build clientState map[sky-id]string from req.inputState.
4. Dispatch Msg as usual.
5. Call diffNodes(prev, new_, clientState, &patches).
6. Reply with {seq: ++sess.outSeq, respondingTo: req.seq,
               ackInputs: subsetOfInputSeqsInPrevTree, patches}.
```

**Why fused, not pre-pass:** the diff already walks both trees. Adding a pre-pass walks the tree twice for no benefit. Adding an index on `prev` means maintaining invalidation on every mutation — another rule to get wrong. The fused approach costs O(hash-lookup) per input node, zero extra allocations, and no state to invalidate.

**What "client authority" means on the server:** the diff is a hint, not a veto. If `update` decides `model.email = "Bob"` (form reset, admin edit, whatever), `new_.Attrs["value"]` becomes `"Bob"`, which doesn't match the client's reported value, so the diff emits `value="Bob"`. The Step 3 client filter then decides — if the user is *currently* typing into that field, the filter drops the `value` attr and the user keeps typing; otherwise the patch applies. Server model stays authoritative; client input stays unclobbered. Both invariants hold simultaneously.

## Invariants enforced

| Invariant | Mechanism | Location |
|---|---|---|
| I1 — Client authority | Patch filter drops `value`/`checked`/`selected` when input is focused/dirty | `__skyApplyPatches`, `__skyMorph` |
| I2 — Monotonic application | `response.seq ≤ lastApplied` → drop | `__skyApplyPatches`, SSE handler |
| I3 — Navigation flush | Intercept `<a>` click inside sky-root → flush debounces synchronously → send nav Msg | `__skyBindEvents`, `beforeunload` |
| I4 — Identity collision-free | Structural sky-ids with tag+key | `assignSkyIDs` in `live.go` |
| I5 — Server diff uses real DOM | `alignPrevTreeValue` merges `req.inputState` into `prev` before diffing | `/_sky/event` handler |

## Failure-mode matrix

| Scenario | Before | After | Invariant |
|---|---|---|---|
| Type fast, server responds slow with old `value` | Input reverts to old value mid-type | Patch dropped; user's value kept | I1 + I5 |
| Two sends in flight, responses arrive reversed | Older response clobbers newer | Older patch dropped via seq check | I2 |
| Click `<a>` while typing | Final keystrokes lost | Debounce flushed before nav leaves | I3 |
| Navigate to page with same outer wrapper | Inputs duplicate / wrong attrs | Different sky-ids; clean replace | I4 |
| SSE delivers duplicate frame | Frame applied twice | Second frame dropped via seq | I2 |
| Form reset via `update` sets model.email = "" | Works today, still works after | `value=""` patch applied; empty input takes effect | (no regression) |
| User types, tab-switches, comes back | Works if debounce already flushed via blur | Same: `focusout` handler flushes | I3 (existing) |
| Server sends innerHTML at parent of focused input | Input wiped, value lost | Scoped morph preserves dirty input subtree | I1 |
| Slow-3G + rapid typing | Characters skip or double | Smooth; buffered locally, flushed in seq order | I1 + I2 |

## Implementation plan

Ordered; each step is independently landable and reviewable.

### Step 1 — Structural sky-ids (Fix B)

**File:** `runtime-go/rt/live.go`

Replace `assignSkyIDs` (line 836) with:

```go
func assignSkyIDs(n *VNode, path string) {
    if n.Kind != "element" {
        return
    }
    seg := path
    if path == "r" {
        n.SkyID = "r"
    } else {
        n.SkyID = path
    }
    for i := range n.Children {
        child := &n.Children[i]
        childSeg := seg + "." + itoa(i) + "#" + child.Tag
        if k := skyIDKey(child); k != "" {
            childSeg += ":" + k
        }
        assignSkyIDs(child, childSeg)
    }
}

func skyIDKey(n *VNode) string {
    if k, ok := n.Attrs["sky-key"]; ok && k != "" {
        return sanitiseKey(k)
    }
    switch n.Tag {
    case "input", "textarea", "select", "form", "button", "fieldset":
        if k, ok := n.Attrs["name"]; ok && k != "" {
            return sanitiseKey(k)
        }
    }
    return ""
}

func sanitiseKey(s string) string {
    // Replace anything that's not [A-Za-z0-9_-] with _.
    // Prevents sky-id parse ambiguity and HTML injection.
    var b strings.Builder
    for _, r := range s {
        switch {
        case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
            b.WriteRune(r)
        default:
            b.WriteByte('_')
        }
    }
    return b.String()
}
```

**Also:** `Html.keyed` helper added to `sky-stdlib/Sky/Html.sky` (opt-in; sets `sky-key` attribute, consumed by `skyIDKey` above):

```elm
keyed : String -> VNode -> VNode
keyed k node = Html.attr "sky-key" k node
```

**Verification:** `scripts/example-sweep.sh` must pass. Every example renders; HTML diff against main shows only sky-id format changes (longer ids, no layout change).

### Step 2 — Wire format bump

**Files:** `runtime-go/rt/live.go` (Go), embedded JS at line 2030+.

Add `seq`, `inputState`, `ackInputs` fields to request/response structs. Populate but don't yet enforce — behaviour identical to today. This is the "stub" step that makes the protocol upgradable.

Go struct additions:
```go
type eventRequest struct {
    SessionID  string                    `json:"sessionId"`
    Seq        int64                     `json:"seq,omitempty"`
    Msg        string                    `json:"msg"`
    Args       []any                     `json:"args,omitempty"`
    HandlerID  string                    `json:"handlerId,omitempty"`
    InputState map[string]inputStateEntry `json:"inputState,omitempty"`
}
type inputStateEntry struct {
    Value string `json:"value"`
    Seq   int64  `json:"seq"`
}
type eventResponse struct {
    Seq        int64            `json:"seq,omitempty"`
    AckInputs  map[string]int64 `json:"ackInputs,omitempty"`
    Patches    []Patch          `json:"patches"`
}
```

Client JS:
- On `__skySend`: bump `__skySeq`, include it in body.
- On response: capture `resp.seq` into `__skyLastAppliedEventSeq` (not enforced yet).
- On `input` event: capture `el.value` into `__skyInputs[sky-id].liveValue` (not yet used in filters).

### Step 3 — I1 client authority

**Files:** embedded JS at line 2030+ in `live.go`.

Wire the patch filter. `__skyApplyPatches` consults `__skyInputs` for every patch targeting an input/textarea/select:

```js
function __skyApplyPatches(resp) {
  var patches = resp.patches || [];
  // Ingest ackInputs first so the filter can see the latest acks.
  if (resp.ackInputs) {
    var ids = Object.keys(resp.ackInputs);
    for (var k = 0; k < ids.length; k++) {
      var e = __skyInputs[ids[k]];
      if (e) e.lastAckedSeq = Math.max(e.lastAckedSeq, resp.ackInputs[ids[k]]);
    }
  }
  for (var i = 0; i < patches.length; i++) {
    var p = patches[i];
    var el = document.querySelector('[sky-id="' + p.id.replace(/"/g, '\\"') + '"]');
    if (!el) continue;
    __skyFilterPatch(p, el);    // mutates p in place per authority rule
    __skyApplyOne(p, el);
  }
  __skyBindEvents(document);
}
```

`__skyFilterPatch` implements the authority rule from the client state section. `__skyApplyOne` holds the existing `innerHTML` / `textContent` / attr application logic.

For innerHTML patches that contain dirty inputs: parse the new HTML with a DOMParser, check whether any dirty input id is present, and if so, convert the innerHTML patch into a morph (walk new vs. old, skip dirty inputs, apply rest).

### Step 4 — I2 stale drop

**File:** same JS block.

Activate the seq check:

```js
function __skyApplyPatches(resp) {
  if (resp.seq !== undefined && resp.seq <= __skyLastAppliedEventSeq) {
    return;  // stale — a newer response already landed
  }
  __skyLastAppliedEventSeq = Math.max(__skyLastAppliedEventSeq, resp.seq || 0);
  // ... rest as in Step 3
}
```

SSE handler uses `__skyLastAppliedSubSeq` (separate counter) for subscription frames.

### Step 5 — I3 flush on unmount

**File:** same JS block.

Intercept `<a href>` clicks inside sky-root:

```js
document.addEventListener('click', function(ev) {
  var a = ev.target.closest && ev.target.closest('a[href]');
  if (!a) return;
  if (!document.getElementById('sky-root').contains(a)) return;
  // External links: let the browser handle, but flush first.
  if (/^https?:/.test(a.href) && a.host !== location.host) {
    __skyFlushAllPending();
    return;
  }
  // Internal nav: let Sky.Live's client-side router handle it, but flush first.
  __skyFlushAllPending();
  // Don't preventDefault — the existing router takes over.
}, true);

window.addEventListener('beforeunload', function() {
  __skyFlushAllPendingSync();  // uses navigator.sendBeacon
});

function __skyFlushAllPending() {
  var ids = Object.keys(__skyInputs);
  for (var i = 0; i < ids.length; i++) {
    var e = __skyInputs[ids[i]];
    if (e.pendingDebounceId !== null) {
      clearTimeout(e.pendingDebounceId);
      e.pendingDebounceId = null;
      if (e.pendingSend) {
        __skySend(e.pendingSend.msgName, e.pendingSend.args, e.pendingSend.hid,
                  {noLoader: true});
        e.pendingSend = null;
      }
    }
  }
}

function __skyFlushAllPendingSync() {
  var ids = Object.keys(__skyInputs);
  var batch = [];
  for (var i = 0; i < ids.length; i++) {
    var e = __skyInputs[ids[i]];
    if (e.pendingSend) batch.push(e.pendingSend);
  }
  if (batch.length === 0) return;
  navigator.sendBeacon('/_sky/event-batch', JSON.stringify({
    sessionId: __skySid, batch: batch
  }));
}
```

`/_sky/event-batch` is a new server endpoint that processes a batch of events in order before the tab closes. Best-effort — browsers guarantee `sendBeacon` delivery but not ordering across multiple beacons; a single batched beacon is reliable.

### Step 6 — Adversarial test matrix

**Go tests** in `runtime-go/rt/live_test.go`:

- `TestSkyIDStructural` — two VNode trees with same positional layout but different tags; `assignSkyIDs` gives different ids.
- `TestSkyIDStableAcrossRenders` — same tree rendered twice produces same ids.
- `TestSkyIDKeyedList` — keyed list reorder: ids follow items, not positions.
- `TestEventSeqMonotonic` — `eventSeq` increments per event, survives marshalling round-trip.
- `TestInputStateReconciliation` — `alignPrevTreeValue` merges client inputState into prev tree.
- `TestAckInputsEmitted` — response echoes server's view of every dirty input.

**Browser tests** via Playwright (new dep) or an example-driven manual matrix (`examples/19-reliability`):

1. Type "hello" with 500ms server latency — all 5 characters appear.
2. Type + click `<a href>` mid-keystroke — server logs show final value.
3. Type on page A, navigate to page B — no orphan value, no state bleed.
4. Fire two submits in rapid succession — server serializes, client shows consistent final state.
5. SSE send duplicate frame — DOM not double-mutated.
6. innerHTML patch at `<form>` while `<input>` focused — input survives with user's value.
7. Kill server mid-fetch — client retries, no silent data loss.
8. Throttle to slow-3G, type continuously — no character loss.

### Step 7 — Verify on sendcrafts

Rebuild sendcrafts' compiler binary (`cabal install` in sky repo), rebuild sendcrafts (`sky build src/Main.sky`). Manual check:

- signIn → signUp transition: no duplicated inputs.
- Type email, navigate away, come back: server remembers last typed value (via `inputState` flush).
- Open two tabs, edit session from both: one wins per session mutex (existing behaviour, documented).

## Backward compatibility

- **Runtime bump:** all Sky.Live apps must recompile to get the new runtime. No opt-out flag; the runtime is embedded via Template Haskell and shipped with the compiler binary.
- **Wire format:** additive. New fields are optional; omitting them produces the pre-upgrade behaviour.
- **Sky-id format:** changes visible in DOM (`sky-id="r.0#div.1#form..."` instead of `sky-id="r.0.1..."`). Tests that assert on sky-id strings must update. Dev tools scripts that query by `[sky-id^="r."]` continue to work.
- **No CLAUDE.md changes needed.** The public API (`Live.app`, `Html.*`, `Server.*`) is unchanged. `Html.keyed` is a new opt-in helper, documented but not required.

## Open questions — resolved

1. **Batch endpoint:** single endpoint. `POST /_sky/event` accepts an optional `batch` field; when present, top-level `msg`/`args`/`handlerId`/`seq` are ignored. One route, one mental model.

2. **Single session-wide seq counter.** The server's `sess.mu` is the single linearisation point for all mutations; exposing it as one monotonic counter lets the client resolve cross-channel races correctly. Two counters would leak implementation detail (separate request/subscription tracking) and make cross-channel ordering ambiguous. Client tracks one `__skyLastAppliedSeq`.

3. **Fused client-value alignment.** `clientState map[string]string` parameter passed to `diffNodes`. No pre-pass, no index to maintain, no invalidation rule. Consulted O(1) per input node during the walk the diff already performs.

4. **No patch reordering.** Current DFS emission order is correct. Ancestor `innerHTML` patches are self-healing — they carry the full rendered subtree, so clobbered descendant patches don't corrupt DOM state (just waste client work). Step 3's filter with innerHTML-preserving-dirty-input morph handles the edge cases categorically. Emission-ordering optimisations (suppressing descendant patches when an ancestor `innerHTML` will be emitted) are a future perf tweak, not a correctness fix.

## Approval gate

This doc is the contract. Before any of Steps 1–7 lands, the following must be confirmed:

- [ ] Sky-id grammar accepted.
- [ ] Wire format (request, response, SSE, headers) accepted.
- [ ] Invariants I1–I5 cover the guarantees G1–G5.
- [ ] Failure-mode matrix is complete (flag any missing scenario).
- [ ] Implementation sequence + verification strategy accepted.

Once approved, implementation proceeds in the step order above, one commit per step. Each commit ships its own regression test. No step is marked complete until `scripts/example-sweep.sh` passes clean-slate.
