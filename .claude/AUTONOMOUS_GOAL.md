# Autonomous mandate — kernel-metadata unification + builder cfg (v0.19.x)

**Set:** 2026-07-28. **Branch:** `feat/std-analytics`. **Mode:** fully
autonomous + grilling. (Supersedes the completed-and-held 2026-07-27
Std.Analytics mandate — that work is DONE on this branch and ships together
in the same v0.19.x release; see the prior mandate archived at
`docs/v0.19/kernel-metadata-unification.md` §Context.)

## Verbatim user goal (the authority on "done")

> [smart unification, agreed] we don't want to maintain 2 logics of LSP + sky
> docs, they shouldn't be competing... ALL kernel functions/vars in module we
> have a dedicated sky kernel files? so essentially lsp + docs reference to sky
> source files directly? and doc' description/example will just be from their
> comment... as kernel functions are used via FFI anyway, so sky's source can
> call that directly, and that with sky source code LSP + docs will render the
> same thing.
>
> [row-open holdouts] A. use builder cfg i like this, the migration path is
> clear, we just need to document it + show on readme with breakage, the
> release will be v0.19.x. use analytics branch for this? as v0.19.x we can
> ship the whole changes.
>
> remember to take deep consideration on the whole arch e2e with soundness,
> grill the design + implementation, test & verify. ok now fully autonomous
> mode on.

## Goal

Make the **`.sky` source file the single source of truth** for every
kernel/runtime function's Sky type signature + doc + example, so the
type-checker, LSP hover, and `sky doc` all render the SAME thing from ONE
place — eliminating the competing `kernel_api.rs` (project) vs `kernel_sigs`
(ty) registries and the drift they cause. Ship on `feat/std-analytics` as
v0.19.x.

### The four outcomes that define "done"

1. **Every kernel-only binding lives in a `.sky` Layer-3 interface file**
   (`name : <sig>` + `-- |` doc [+ example] + `name = Ffi.kernel "Sym"`),
   exactly like the already-migrated `Sky.Http.Server` (Server.sky proves the
   pattern). Targets: `Std.Jobs` (define/enqueue/enqueueIn/cancel),
   `Std.Live` (route/api/lifecycle), plus the row-open holdouts below.
2. **Row-open cfgs (`Live.app`, `Tui.app`, and `Webview.app` for parity) move
   to Path A typed-builder cfg** — a closed `AppConfig` record built via
   `Live.config { ...required... } |> Live.withHead ... |> Live.withConsoleAuth
   ...`, fully expressible in Sky, no open-row syntax needed. Runtime kernels
   (`Live_config`/`Live_withHead`/… + `Live_app` reading the built config)
   updated to match. EVERY app in `examples/` + the bundled console migrated to
   the builder form. Backward-compat is intentionally BROKEN (v0.19.x major-ish
   bump) and DOCUMENTED.
3. **`kernel_api.rs` is deleted** (or reduced to nothing the gate needs), and
   the coverage gate flips to: *every registered kernel-only function has a
   `.sky` declaration, or CI fails.* Drift becomes a build error, not a
   CLAUDE.md hope. LSP hover shows the `.sky` sig for every kernel fn — no `?`,
   no "not found"; generics render as real type vars (`a -> a`).
4. **Docs + migration**: CLAUDE.md + templates/CLAUDE.md + docs/skylive +
   docs/skytui + README updated; a **migration guide with the breakage note**
   (record-literal `Live.app {...}` → builder `Live.app (Live.config {...} |>
   ...)`) on the README + a `docs/v0.19/` migration doc.

## Hard rules

1. **Grill the design against Sky architecture BEFORE implementing** — cite the
   concrete mechanism (Res::Def kernel_alias lowering, Ffi.kernel dispatch,
   runtime reflect-read of cfg, LSP `hover_ref` sig source, the doc gate).
   Especially grill Path A: the runtime must reflect-read the built `AppConfig`
   soundly (no raw `.(T)` on a Sky value — same class as the Db.withTransaction
   / config-decoder defects already fixed this session).
2. **Soundness is INVIOLABLE.** "If it compiles it works" — the builder cfg must
   type-check precisely AND run. No kernel may raw-assert a Sky callback to a Go
   func type; route via `sky_call`/reflect. Every optional-field-omitted app
   must produce byte-identical runtime behaviour to supplying the default.
3. **No two logics.** After this, there is ONE source (the `.sky` file). If any
   kernel genuinely can't be expressed, it stays in `.sky` with the closest
   expressible sig + a `-- |` note, and the gate records the exception
   explicitly — never a silent second registry.
4. **Verify per phase**: narrowest gate per change; at phase close rebuild +
   representative `cargo test` + `scripts/example-sweep.sh` (FULL) +
   `sky doc`/LSP-hover smoke + a real app build+run. Commit per phase locally;
   push only at milestone boundaries. HOLD ship/tag until the user asks.
5. **Stop only on a genuine blocker** — a Sky-architecture decision needing the
   user. Describe it concretely, continue with their direction. No deferral
   framings ("out of scope for this iter", "session boundary").

## Verify-at-close (independent adversarial Judge)

Done only when: all kernel-only fns are `.sky`-sourced; `kernel_api.rs` gone +
gate enforces `.sky` coverage; `Live.app`/`Tui.app`/`Webview.app` are builder
cfgs with runtime + ALL apps migrated; LSP hover + `sky doc` render identically
from `.sky` for every kernel fn (no `?`); full example sweep + rt suite green;
docs + README breakage migration shipped. A fresh-context Judge confirms with
NO "but/except/however/mostly/for-the-scope-of".
