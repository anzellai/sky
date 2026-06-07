# Parametric-Cfg unannotated `view` repro scratch

Filed as task #521. Real-world panic site:
`skydeploy/control-plane/src/Editor.sky` pre-2026-06-06.

## Symptom

Every wire-event dispatch panics:

```
[sky.live] dispatch panic recovered, dropping event:
  interface conversion: interface {} is
  main.Editor_Cfg_R[sky-app/rt.SkyADT],
  not main.Editor_Cfg_R[interface {}]
```

## Trigger conditions (observed)

1. A parametric record alias `Cfg msg` with callback-shaped fields
   (`msg`, `a -> msg`).
2. A `view` (or similar) top-level binding in the module that defines
   `Cfg` — DELIBERATELY UNANNOTATED.
3. The body routes those callbacks through `Std.Ui.onSubmit` /
   `Ui.onClick` / similar typed event HOFs.
4. Called from a different module with a concrete `Msg` ADT.

## Workaround that closes the runtime panic

Add the head annotation:

```elm
view : Cfg msg -> Element msg
view cfg = ...
```

The Sky compiler then monomorphises the callee body consistently —
the emitted Go has `Cfg_R[any]` (or the concrete specialisation)
end-to-end.

## What I tried in this scratch (didn't reproduce)

- `Widget.sky` mirrors the Editor `Cfg` shape (7 fields, mixed
  callback shapes).
- `Main.sky` consumes from a separate module via `Std.Live.app` with
  the same `view = Widget.view {...}` pattern that triggers it in
  skydeploy.
- Build succeeds, but runtime click-event dispatch (via curl POST to
  `/_sky/event`) returned `session not found` — I couldn't get the
  POST body shape right outside the browser. Worth trying with a
  real browser + Playwright next session.

## What's likely missing from this repro vs the real case

- Editor uses additional helpers (`editorBody`, `toolbar`,
  `busyBanner`, `diagnostics`) that ALL receive `cfg` directly. The
  bug may need this multi-call-site cfg-flowing pattern.
- Editor sits in a 3-module dependency chain (Editor → AppDetail →
  Main); the repro is 2 modules.
- Editor's `View.AppDetail` call site is itself inside a nested
  closure (tab-switch handler), one more level of indirection than
  the repro.

## Next session for the compiler fix

1. Either extend this repro to trigger the panic in a 2-3 module
   form, OR
2. Use the pre-fix skydeploy commit as the test fixture (run
   the panic case under Playwright, then revert Editor.view's
   annotation and watch it fail).
3. Trace through `Sky.Build.Compile` + `Sky.Type.Monomorphise` —
   sibling family of fixes already shipped:
   - #261 (dedupe structurally-equal aliases)
   - #262 (record-stored partial-applied constructors)
   - #263 (Surface 2 parametric record alias callbacks)
   - #461 (cross-module Set a return)
   - #465 (2-arg partial application miscompile)
4. Add an Hspec regression spec at
   `test/Sky/Build/UnannotatedParametricCfgViewSpec.hs`.
5. Close the limitation in CLAUDE.md.
