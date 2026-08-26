# Autonomous goal — Std.App finish + Secret migration

Captured verbatim 2026-08-26 (branch `feat/unified-app-builder`).

## User's verbatim mandate

> let's do the parked 1 & 2 now. 3 can wait.
>
> after parked 1 & 2 completed, move on to Secret migration e2e + tested + verified.
>
> after all these examples need to be migrated to use new Secret mechanism too.
>
> and then we will test everything together with like --open/install etc.

Where "parked 1 / 2 / 3" refer to the assistant's preceding status message:

1. **Parked 1** — Example migration + SPA-example rename to Std.App
   ("rename the examples for SPA to app-x … refactor all examples sky.live
   sky.tui, sky.webview, sky.cli … to Std.App").
2. **Parked 2** — `--open` auto install/launch flag (+ `sky run` auto-installs
   deps instead of requiring a manual `sky install` first).
3. **Parked 3** — `App.withRequest` — **DEFERRED by the user** ("3 can wait").

## Ordered execution

1. Parked 1 (examples → Std.App + SPA rename to `app-*`).
2. Parked 2 (`--open` + `sky run` auto-install).
3. Secret migration — **e2e + tested + verified** (design in
   `docs/design/std-app-config-architecture.md` §§7,7b,7c,7d).
4. Migrate all examples to the new `Secret` mechanism.
5. Joint final integration test with `--open`/install (with the user).

## Standing constraints (INVIOLABLE)

- **No push / tag / release without an explicit user ask.** Get the branch
  ready only. Local commits are checkpoints.
- **darraghstudio HARD HOLD** — never touch/deploy/upgrade it until the user
  personally verifies no regression.
- Root-cause fixes only; every fix gets a regression test; only an independent
  adversarial Judge declares a phase "done".
- Secrets are typed — the migration formalises this into the `Secret` type.
- `--open` LIVE launch verification (real browser/simulator) is step 5, the
  joint test WITH the user — implement + unit-test the wiring now, don't claim
  live-launch verified solo.

## Scope decision — example → Std.App migration (stated up front)

- **Migrate app-shaped `examples/`** (TEA entries: `Live.app` / `Spa.app` /
  `Tui.app` / `Cli.program` with init/update/view/subscriptions) to `Std.App`
  (`App.app` + `App.run`). Examples are the *documentation* layer → they model
  the recommended unified way.
- **One-shot jobs / pure-lib examples** (`main = Task.run work`, no TEA loop;
  stdlib smoke like `00-standard-libs`) **stay** on direct `Task.run` — they are
  not TEA apps and do not fit `App.app`.
- **Rename the SPA examples to `app-*`.**
- **`apps/` (Layer 2 real-world) stays on the direct front doors** — preserves
  direct regression coverage of `Live.app`/`Tui.app`/etc. and the only Postgres
  coverage. Std.App composes those modules; they are not deleted.
- Re-bless `coerce-floor` + stdout goldens where emission legitimately changes;
  verify byte-equivalence where the Direct/synthesised path should preserve it.
