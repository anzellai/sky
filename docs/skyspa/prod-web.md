# Sky.Spa — production web (exploration + measured evidence)

> **Status:** exploration (v2). Sky.Spa v1 ships **desktop/mobile-embed** weight
> (Go→wasm, ~2.4 MB gzip for the Todos app). This document is the evidence-based
> plan for a **web-viable** bundle. Every size here is measured on this machine.

## The goal

A public-web-viable client bundle. Reference points: Elm ~30 KB gzip, a React
app's runtime ~40–130 KB gzip. Sky.Spa v1's Todos client is **~2.4 MB gzip** —
fine for logged-in/internal tools + installable PWAs (load-once, cached), too
heavy for cold public web.

## The blocker (confirmed against the code)

The wasm **client core is reflection-native**, in three places, all compiled into
the client (none `//go:build !js`):

- **Dispatch** — `runtime-go/rt/live_core.go:2425,2436,2516`: `sky_call`/
  `sky_call2` invoke the app's `update`/`view` via `reflect.Value.Call`.
- **Codec** — `runtime-go/rt/codec_auto.go`: **85 reflect sites** (`Codec.auto`,
  used to decode the client's `data`).
- **ADT** — `runtime-go/rt/adt_shape.go`, `rt.go` kernels.

**TinyGo — the only lever that shrinks wasm ~10–20× — implements neither
`reflect.Value.Call` nor `reflect.MakeFunc`.** So it cannot compile this core.
The half-built escape hatch does not help: `perMsgTypedDispatch`
(`msg_dispatch.go`) is **Stage-5 scaffolding only** — it always falls through to
reflect (`LookupMsgUpdate` is empty in every real binary; codegen emits no
`RegisterMsgUpdate`; the Stage-6 "thread the ADT name from the call site" step
never landed).

## Measured evidence (this machine, 2026-08-22)

The Phase-1 spike (`docs/skyspa/spike/main.go`) is a **reflection-free** Go→wasm
TEA+DOM counter — a faithful stand-in for what a *de-reflected* Sky.Spa client
would be. Compiled both ways:

| Toolchain | raw | gzip |
|---|---|---|
| standard Go (`GOOS=js`) | 1,996,836 B (1.90 MB) | 592,397 B (**579 KB**) |
| **TinyGo 0.41.1** (`-target wasm`) | 195,970 B (191 KB) | 66,999 B (**65 KB**) |

**~9× smaller, 65 KB gzip — web-viable.** And the current v1 real-app figure for
contrast: the Todos client (reflect-heavy, standard Go) is **~2.4 MB gzip**.

So the two facts that decide the plan: (1) TinyGo turns a reflection-free client
into a web-viable bundle; (2) the current client is reflection-heavy, so TinyGo
is blocked until it is de-reflected.

## The three paths

### Path A — de-reflect the client core, compile with TinyGo (recommended)

Make the client core reflection-free, then TinyGo it. Work:

1. **Dispatch** — finish the `perMsgTypedDispatch` Stage 6: a typed dispatch
   table keyed on the Msg ADT (thread the ADT name from the call site), so
   `update`/`view` are invoked without `reflect.Value.Call`. Scaffolding exists.
2. **Codec** — the client decodes `data` with `Codec.auto` (reflection). Replace
   with the **typed/generated codecs** the app already declares (the Todos app
   hand-writes `todoCodec` etc. in `Shared.sky` — those are reflection-free
   already; the reflection is in `Codec.auto`'s auto-derivation). A codegen or
   a reflection-free `Codec.auto` for the client target.
3. **ADT shape** — a reflection-free ADT representation for the client.
4. **Isolate** — do all of this in a **separate `rt_spa` client runtime** (or a
   `//go:build js && spa` variant), so **Sky.Live's server reflect path is
   untouched** and unregressed.
5. **Verify TinyGo covers the rest** — TinyGo's stdlib is narrower than Go's
   (not only reflect). The whole reflection-free client core must be checked
   against TinyGo's supported packages, not just the spike.

- **Payoff (measured surrogate):** ~65 KB gzip for a counter. A real app (typed
  codecs + routing + a larger view) will be bigger — estimate **~150–400 KB
  gzip**, still web-viable — but that must be **measured after the rewrite**, not
  promised (this programme has a history of projections off by several-fold).
- **Cost:** a bounded runtime rewrite (dispatch + codec + ADT), isolatable to a
  client runtime. Multi-week. Reuses the entire Go toolchain + the existing IR.
- **Risk:** TinyGo stdlib gaps beyond reflect; the codec de-reflection is the
  largest single piece.

### Path B — a Sky→JS backend (long-horizon ideal)

A new codegen target emitting JavaScript from the existing typed IR (like
Elm→JS). Smallest bundles (~30 KB), best web perf, and JS's dynamism dissolves
the reflect problem entirely.

- **Payoff:** the best possible web story.
- **Cost:** enormous — a whole new emit backend (codegen currently emits Go).
  Multi-quarter. Reuses `hir`/`ty` but not the Go runtime.

### Path C — ship Go→wasm for web now, mitigated (interim)

Keep standard Go→wasm; mitigate the ~2.4 MB: **brotli** (smaller than gzip),
streaming compilation, lazy-load/split, aggressive caching.

- **Payoff:** zero rewrite; usable **today** for logged-in apps, internal tools,
  and installable PWAs (the bundle is fetched once and cached).
- **Cost:** none. **Not** suitable for cold public-web first paint.

## Recommendation

- **Now:** Path C is already what v1 is — document it as the supported web story
  for logged-in/PWA use, add brotli to the static serving.
- **The web bet:** **Path A.** The measured 9× (579 KB → 65 KB) confirms
  de-reflection + TinyGo reaches web-viable, it reuses the whole Go toolchain,
  and it can be isolated to a client runtime so Sky.Live is untouched. The
  scope is a bounded, ordered rewrite (dispatch → codec → ADT), each piece
  independently verifiable, with a real-app bundle measured at the end.
- **Long-horizon:** Path B (Sky→JS) if/when a from-scratch web-native backend is
  worth a multi-quarter investment; Path A does not preclude it and buys the web
  story far sooner.

## Honest caveats

- 65 KB is a **counter**; the real-app figure is unmeasured until the rewrite —
  do not quote 65 KB as "Sky.Spa's web bundle."
- TinyGo compatibility of the **full** reflection-free core (beyond the spike) is
  unverified — a required Path-A spike is: de-reflect the *smallest* real client
  (spa-counter) and TinyGo it end-to-end.
- The de-reflection touches the runtime; isolating it to a client-only runtime is
  a hard requirement so Sky.Live's (correct, reflection-based, server-side) path
  does not regress.
