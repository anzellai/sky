# [E1011] NOT EXPOSED

The code uses a name that its module does not expose to it.

[E1011] covers three cases:

| You wrote | Why it is refused |
|---|---|
| `import Helper exposing (privateFn)` | `Helper` does not list `privateFn` in its `exposing (…)` |
| `Ffi.kernel "…"`, `Ffi.call …` (or the same member through another module, such as `Webview.kernel`) | `Sky.Ffi` is stdlib-only: it binds a runtime kernel or a Go binding with a type the checker cannot know. Call the typed stdlib function, or `sky add` a Go module and use its binding (see `docs/migration/v0.27.md`) |
| `Spa.withHoldFree [ … ]` | It is generated-only (v0.29.0). See below |

## `Spa.withHoldFree` is generated-only

```text
-- NOT EXPOSED ------------------------------------------ Main:50:16 [E1011]

`Spa.withHoldFree` is not exposed to application code. Only the Sky.Spa
split writes it: it lets an input Msg run while a hold RPC is in flight,
which is safe only for the pairs the split proves commute. A pair listed by
hand can run a Msg out of order.
```

A hold RPC (a server branch that writes server data into the model) holds
every later Msg until its answer has run, as Sky.Live does. `Spa.withHoldFree`
lists `"<Hold> <Input>"` pairs whose input Msg may run at once instead. The
auto-split lists a pair only when it proves that running the input early gives
the same model (`docs/skyspa/auto-split.md`, "hold"). A list written by hand
has no such proof, so the checker refuses any reference to it in a project the
split did not generate. The split also refuses a `main` that already calls it,
so a hand-written list is never kept or merged with the generated one.

Fix: remove the call. `sky build --target web:app` derives the pairs from
`update` and adds them to the generated client.
