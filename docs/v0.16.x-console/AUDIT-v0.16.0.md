# v0.16.0 — Embedded Console Hardening: Auditor Report

> Date: 2026-06-02. Branch: `feat/v0.16-console` (clean, on top of main).
> Auditor: read-only. No code changes made.

This audit answers: "Is the design in `EMBEDDED.md` actually achievable
with the current code? Where are the surprises? How does it ship in
PR-sized chunks?"

## 1. Current state — files + functions

### Subprocess + reverse-proxy code v0.16.0 deletes

- **`runtime-go/rt/subapp.go`** (639 lines, single biggest doomed file)
  - `SpawnFn` type (L60) — `func(ctx, basePath) (port, *exec.Cmd, error)`. Public; only used internally.
  - `SpawnBinary` (L74-130) — generic external Sky binary spawner. **Unused outside the file.**
  - `SpawnSkyConsole` (L151-229) — what spawns `sky console --port N` on a child process. Direct OOM cause on e2-micro (kicks off a `go build` of the bundled console via the spawned `sky` binary).
  - `MountSubApp` (L245-306) — reverse-proxy via `httputil.NewSingleHostReverseProxy`. **Only caller is `maybeAutoMountConsole` + `MountConsoleAuth`** (both internal to this file). No example app calls it; sky doc doesn't use it.
  - `maybeAutoMountConsole` (L452-497) — the orchestrator. Called from `live.go:3053` + `rt.go:7633`.
  - `MountConsoleAuth` (L546-594) — Pro+ JWT-gated subprocess mount (the SkyDeploy path).
  - `consoleAutoMounted atomic.Bool` (L426) + `ConsoleAutoMounted()` (L431) — flag read by `MountConsoleEndpoints` to skip its own HTML root when the subprocess mount succeeded.
  - `ShutdownSubApps` (L616-639) — signal-driven cleanup. Called from `live.go:3224` + `rt.go:7676`.
  - Helpers: `subAppNamespaceFromPath`, `pickFreeLocalhostPort`, `waitForPort`, `resolveSkyBinary`, `filteredProxyLog`, `registerSubAppChild`.

- **`runtime-go/rt/console.go`** (431 lines) — the **legacy hand-written `/_sky/console`** with a static HTML shell + JSON API. THIS IS THE ESCAPE-VALVE TODAY: when subprocess mount fails, this serves a working (but uglier) dashboard. v0.16.0's plan must keep the spirit of this code — it remains the always-on path, but should be upgraded to the new Std.Ui UI.
  - `MountConsoleEndpoints` (L58-77) — registers root + 5 JSON API routes.
  - `HandleConsole` (L83), `HandleConsoleOverview` (L132), `HandleConsoleMetricsSummary` (L196), `HandleConsoleLogs` (L255), `HandleConsoleTraces` (L290), `HandleConsoleErrors` (L340) — JSON handlers.
  - `consoleAccessAllowed` (L96) — auth gate (serverless 503 + Bearer-token check).
  - The static HTML lives in `console_html.go` (431 lines) — vanilla JS poller; this is what v0.16.0 replaces with the inlined Std.Ui app.

- **`runtime-go/rt/console_auth.go`** (249 lines) — JWT-in-URL handshake.
  - `consoleAuthCookieName` (L53), `consoleAuthSessionTTL` (L62).
  - `consoleTokenAuth` (L76-128) — the middleware. Reads `?token=`, mints session cookie, redirects.
  - `verifyConsoleJwt` (L134), `mintConsoleSession` (L155), `MintConsoleUrlToken` (L224).
  - Used by `MountConsoleAuth` for SkyDeploy's iframe pattern. **EMBEDDED.md says preserve this** for the URL-handshake mode, but harden (one-shot JTI + aud-claim check).

- **`runtime-go/rt/rt.go:7625-7634`** — Sky.Http.Server callsite for `maybeAutoMountConsole + MountObservabilityEndpoints`.
- **`runtime-go/rt/live.go:3053`** — Sky.Live callsite (`maybeAutoMountConsole(mux, app.basePath, parentPortForChildren)`).

### The current console UI source (Sky)

- `sky-bundled/console/sky.toml` — declares `name = "sky-console"`, `entry = "src/Main.sky"`, `[live].port = 8025`.
- `sky-bundled/console/src/Main.sky` (390 lines), `src/State.sky` (204), `src/View.sky` (913), `src/MainTui.sky` (82). ~1.6k lines of Sky source. This is a real Sky.Live app.
- TH-embedding mechanism: **`src/Sky/Build/EmbeddedConsole.hs`** uses `embedDirRecursive "sky-bundled/console"` (workaround for #58 `embedDir` recursion bug) → produces `embeddedConsoleApp :: [(FilePath, ByteString)]`. THE BYTES ARE Sky SOURCE.
- Materialisation in `app/Main.hs:2029` (runConsole / `sky console` command) writes the files into `XdgCache/sky/console-<version>/`, then shells out `sky build src/Main.sky` to produce a binary. **This is the e2-micro OOM cause** — the recursive `sky build` invokes `go build` against 30+ runtime-go files.

**Confirmed: option (a) is reality** — sky-bundled is TH-embedded Sky SOURCE, runtime-`go build` on first launch, OOMs e2-micro.

### The current build pipeline

- `sky-compiler.cabal:163-202` — declares `Sky.Build.EmbedDirTH`, `Sky.Build.EmbeddedRuntime`, `Sky.Build.EmbeddedConsole`, `Sky.Build.EmbeddedDocServer`. These run at cabal-install / cabal-build time via TH.
- The cabal pipeline currently CANNOT invoke `sky` against `sky-bundled/console/` because `sky` itself is the artefact being built. **Chicken-and-egg.**

The EMBEDDED.md plan ("cabal build invokes the local sky binary") IS feasible but needs a two-stage build: (a) cabal builds `sky` without the console, (b) a custom Setup.hs or external Makefile-like step invokes the just-built `sky` against `sky-bundled/console/`, copies the generated `sky-out/main.go` into `runtime-go/rt/console_app/`, (c) cabal-build is re-run to embed the now-present `runtime-go/rt/console_app/` into `embeddedRuntime`. **See §2 for the proposed alternative.**

### The current `Live.app` cfg shape

- HM signature lives at `src/Sky/Type/Constrain/Expression.hs:2068-2089`. The shape is `Forall ["model", "msg", "page", "e", "req", "appExt"]` with `TRecord (… required fields …) (Just "appExt")` — the row variable `appExt` absorbs any extra fields. **Adding `consoleAuth` requires NO HM signature change** — exactly the same pattern as v0.15.58's `head` field (commit `2a780989`).
- Runtime `liveAppCfg` struct: `runtime-go/rt/live.go:2572-2603`, with the new `head any` field at L2594. `Live_app` reads it at L2952 with `Field(cfg, "Head")`. New `consoleAuth` field slots in identically.

### Storage layer

- `runtime-go/rt/telemetry/persist.go` — the SQLite write-through. `persistEnvVar = "SKY_CONSOLE_DB_PATH"` (L91). Schema embedded at L48-87 (`telemetry_log`, `telemetry_metric`, `telemetry_span` + indexes + per-table retention via the pruner goroutine at L173).
- `EnablePersistence(path)` (L137-175) — opens SQLite WAL, applies schema, spawns flusher + pruner goroutines.
- `EnablePersistenceFromEnv()` (L180-186) — reads env var, forwards.
- Boot wire-in at `runtime-go/rt/observability.go:67` (inside `MountObservabilityEndpoints`).

**Key finding**: the schema is already shared with SkyDeploy's `control-plane/static/console.db.schema.sql`. v0.16.0 keeps this schema; v0.16.5 adds hot/warm split per OPS.md.

### Tests touching console / telemetry

- `runtime-go/rt/console_test.go` (362 lines) — covers HTML shell, JSON API, auth gate, serverless 503, filter params, error grouping. **Must be rewritten** for the Std.Ui-rendered console (HTML body changes; JSON API stays the same).
- `runtime-go/rt/console_auth_test.go` (217 lines) — JWT-in-URL + cookie flow. Most stays valid; the one-shot JTI check is new.
- `runtime-go/rt/subapp_abort_test.go` — **becomes dead** when subapp.go disappears.
- `runtime-go/rt/telemetry/persist_test.go` — persistence layer. Stays.
- `test/Sky/Build/SkyLiveHeadSpec.hs` (194 lines) — the v0.15.58 template. The new `consoleAuth` field needs an equivalent regression spec.

### Examples that use Sky.Live

`examples/09-live-counter` and `examples/10-live-component` use `Live.app`. Neither sets `head` today, so the `consoleAuth` row-poly addition is byte-identical for them. **No example needs updating.**

## 2. Design vs. reality — surprises and risks

### S1: cabal can't invoke `sky` mid-build (chicken-and-egg)

EMBEDDED.md L25-27 reads:
> "cabal build invokes the local sky binary to compile this source. Output Go code written to runtime-go/rt/console_app/*.go. runtime-go is built with these files included."

**Reality**: at cabal-build time, the `sky` binary doesn't yet exist (it's the artefact being built). And `runtime-go` is TH-embedded INTO `sky` via `EmbeddedRuntime.hs` — so once `sky` exists, you'd need a SECOND cabal pass to re-embed.

**Concrete adjustment**: introduce a checked-in pre-built artefact. Add `runtime-go/rt/console_app/` as a **committed directory of generated Go files** produced by a `scripts/regenerate-console.sh` step. The script invokes the current `sky-out/sky` against `sky-bundled/console/`, copies `sky-out/main.go` → `runtime-go/rt/console_app/main.go` plus dep modules (Sky.Live runtime-go files are already in the same runtime-go tree, so the embedded copy is the entry-point + module-prefixed top-levels only — ~50KB). A CI check verifies the committed copy is up-to-date by re-running the regen and diffing. Developers run the script after editing `sky-bundled/console/src/*.sky`.

This avoids the multi-stage cabal mess AND keeps a clean reviewable diff on PR.

### S2: MountSubApp is console-only — safe to delete

I greppped runtime-go, examples, and the sky-bundled apps for `MountSubApp` / `SpawnBinary` callers. The ONLY callers are `maybeAutoMountConsole` and `MountConsoleAuth` (both inside `subapp.go`). `sky doc --serve` and `sky console --tui` use a DIFFERENT pattern (materialise + go-build a standalone binary, run it as the foreground process — NOT a sub-app of a parent). **Deleting `MountSubApp` is safe.**

### S3: JWT-in-URL handshake STAYS (SkyDeploy uses it)

EMBEDDED.md §"URL handshake" preserves the JWT-in-URL pattern for iframe embedding. SkyDeploy's control-plane mints these tokens via `MintConsoleUrlToken` (exported). v0.16.0 keeps `consoleAuth_*` files but:
- moves the gate from "wraps a reverse-proxy" → "wraps the inline handler"
- adds one-shot JTI enforcement
- adds aud-claim check against build ID
- gates behind `SKY_CONSOLE_EMBED_ORIGIN=<exact-origin>` opt-in

**Backwards compat**: SkyDeploy keeps minting URL tokens the same way; only the runtime gate moves.

### S4: Project name needs to flow from sky.toml → build-time ld-flag → runtime

The `<projectName>.console.db` path needs `<projectName>` at runtime. Currently `BuildInfo` (observability.go:113) has Commit / BuiltAt / SkyVersion / GoVersion only. **New piece**: compiler must inject `-X sky-app/rt.projectName=<name-from-sky.toml>` at codegen time (it already injects 3 other ld-flags). Trivial; one-line addition in `Sky.Build.Compile`'s go-build invocation + a new `var projectName = "app"` in `observability.go`.

### S5: `consoleAutoMounted atomic.Bool` should disappear

Today, `MountConsoleEndpoints` (`console.go:69`) reads `ConsoleAutoMounted()` to decide whether to skip mounting its own HTML root (it would conflict with the subprocess proxy). With subprocess gone, `MountConsoleEndpoints` becomes the sole console mount. The flag and its `atomic.Bool` global are deletable. Reduces global state.

### S6: Storage path collision

EMBEDDED.md §"Storage" says: "Multiple apps on the same host with the same (dataDir, projectName) pair → framework emits `console.storage.collision` warn log at boot." Implementing this means a **boot-time fcntl lock** (or simpler: `os.OpenFile` with `O_EXCL` + a sidecar `.lock` file holding the PID). On graceful exit, remove. On crash, the next boot detects stale lock via `unix.Kill(pid, 0)` and overwrites. ~30 lines of Go in `telemetry/persist.go`.

## 3. PR decomposition

The cycle needs 4 PRs, each landing safely on `feat/v0.16-console`. The branch starts clean on top of main; the final PR is the merge to main.

### PR 1 — `feat(console): inline Std.Ui-rendered HTML/CSS shell`
**Scope**:
- Bring `sky-bundled/console/src/*.sky` into `feat/v0.16-console`. Add `scripts/regenerate-console.sh` that runs the **just-built local** `sky-out/sky` against it, drops the emitted Go into `runtime-go/rt/console_app/`.
- Commit the generated `runtime-go/rt/console_app/` (subpackage of `sky-app/rt`).
- Replace `console_html.go`'s static HTML with an embed of the generated Std.Ui-rendered HTML. JSON API handlers stay byte-identical for now (keeps the dashboard polling logic working through the transition).
- Add CI step: `scripts/regenerate-console.sh && git diff --exit-code runtime-go/rt/console_app/` — drift detection.
**Verification**: `console_test.go` HTML body assertions updated to match the new Std.Ui output; `cabal test`; example sweep; visual smoke `sky run examples/09-live-counter && curl /_sky/console`.
**Blocked by**: nothing.
**Risk**: medium — generation pipeline is new code; manual testing of the regen script needed.

### PR 2 — `refactor(rt): delete subapp.go + sub-process reverse-proxy`
**Scope**:
- Delete `runtime-go/rt/subapp.go` entirely (639 lines).
- Delete `runtime-go/rt/subapp_abort_test.go`.
- Remove callsites: `live.go:3053` (`maybeAutoMountConsole`), `live.go:3224` (`ShutdownSubApps`), `rt.go:7633`/`7676`. Replace `maybeAutoMountConsole` callsite with `MountEmbeddedConsole(mux)` — new entry point in `console.go`.
- Delete `consoleAutoMounted` flag + `ConsoleAutoMounted()` from observability/console wiring (single mount path now).
- Delete `app/Main.hs` runConsole's `sky build src/Main.sky` shell-out (the materialise + go-build chunk at L2021-2064). `sky console` becomes a thin wrapper that prints a deprecation hint + spawns the user's app with `SKY_CONSOLE_EMBED=on` set — OR delete `sky console` CLI command entirely (decision needed). **Recommend**: keep the `sky console` CLI surface but make it route to the embedded path.
- Delete `Sky.Build.EmbeddedConsole` cabal module + `sky-bundled/console/` (no longer needed once PR 1 has generated the inline copy).
**Verification**: cabal test (`SkyLiveHeadSpec`, `EmbeddedRuntimeSpec`); example sweep + `verify-all-web.sh`; e2-micro VM build (the canary).
**Blocked by**: PR 1.
**Risk**: medium — large deletion, but all callers traced; sky doc unaffected (uses a separate flow).

### PR 3 — `feat(skylive): row-poly consoleAuth + production gate`
**Scope**:
- Add `consoleAuth any` field on `liveAppCfg` struct (`live.go:2603`), mirroring `head any`. Read via `Field(cfg, "ConsoleAuth")` in `Live_app`.
- New `SKY_CONSOLE_AUTH=token|app|off` env var. Production gate: ENV != dev/development/local AND `SKY_CONSOLE_AUTH` unset → console doesn't mount + `console.disabled reason=auth-unset` warn log.
- New token-mode auth: `__Host-sky_console` cookie + HKDF-derived signing key from `SKY_CONSOLE_TOKEN`. Login page POST (NOT GET — avoid Referer leaks).
- App-mode auth: framework calls `consoleAuth Request -> Task Error (Maybe Identity)` before mount. `Nothing` → 403 + audit log.
- Harden the URL handshake (existing `console_auth.go`): one-shot JTI via `sync.Map`, aud-claim against build ID, opt-in via `SKY_CONSOLE_EMBED_ORIGIN`.
- New stdlib export: `Std.Live.Console.Identity` type alias (`{ subject, email, claims }`) + helpers if any. Minimal — mostly the framework consumes the callback.
- New Haskell module `Sky.Build.KernelRegistryEntries` already exists; no new kernel routing needed (callback is plain Sky).
**Verification**:
- New cabal spec `test/Sky/Build/SkyLiveConsoleAuthSpec.hs` (template from `SkyLiveHeadSpec.hs`): app WITH/WITHOUT `consoleAuth`, token-mode 401, app-mode 403 path, app-mode 200 path, production-mode "auth-unset" decline.
- New runtime test `runtime-go/rt/console_auth_v2_test.go`: __Host- cookie + HKDF + one-shot JTI.
**Blocked by**: PR 2.
**Risk**: medium — auth surface is sensitive; one-shot JTI map needs careful concurrency review.

### PR 4 — `feat(stdui): Chart primitives (line/area/bar/sparkline/heatmap)`
**Scope**:
- New module `sky-stdlib/Std/Ui/Chart.sky` — public API: `line cfg series`, `area cfg series`, `bar cfg series`, `sparkline cfg values`, `heatmap cfg grid`. Each takes a typed `Cfg` record with `defaultCfg + with*` builders per v0.15.46 convention.
- Server-side rendering to inline SVG via existing Std.Ui Node primitives (no runtime helper needed; SVG-as-Sky-text).
- Wire the regenerated `runtime-go/rt/console_app/` (PR 1) to use these primitives — Overview tab gets real sparklines instead of mock divs. Run `scripts/regenerate-console.sh` to refresh the embed.
**Verification**: 
- New cabal spec `test/Sky/Build/StdUiChartSpec.hs` — each helper compiles to expected SVG markup.
- Update `examples/26-ui-showcase` with chart demo cells + Playwright visual gate.
- Console smoke: load `/_sky/console`, confirm sparklines render against live telemetry.
**Blocked by**: PR 3 (so the new console UI ships with auth+charts together for the v0.16.0 tag).
**Risk**: low — pure Std.Ui surface, no runtime/codegen changes.

After PR 4 lands, v0.16.0 is tagged. The branch merges to main; CI runs the release-gate sweep (§5 below).

## 4. Test plan

**New cabal specs**:
- `test/Sky/Build/SkyLiveConsoleAuthSpec.hs` — row-poly `consoleAuth` field across with/without/token/app/prod-unset.
- `test/Sky/Build/StdUiChartSpec.hs` — chart primitive compile + SVG output.

**New runtime-go tests**:
- `runtime-go/rt/console_auth_v2_test.go` — __Host- cookie path, HKDF derivation, one-shot JTI, aud-claim mismatch.
- `runtime-go/rt/console_storage_test.go` — `<dataDir>/<projectName>.console.db` path resolution, collision warning, project name from build-time ld-flag.

**Existing tests that change**:
- `console_test.go` — HTML body assertions change (new Std.Ui-rendered output); JSON handlers should remain backwards-compatible.
- `console_auth_test.go` — most stays; remove the reverse-proxy-mount assumptions.

**Deleted**:
- `subapp_abort_test.go` — covers code that's gone.

**Examples**: none need updates. `09-live-counter` + `10-live-component` build byte-identically (don't set `consoleAuth`).

## 5. Release-gate sweep for v0.16.0

Adapted from CLAUDE.md's "Release checklist (non-negotiable)":

1. `cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky` — clean rebuild.
2. `sky-out/sky --version` — smoke (must print v0.16.0, not start a server).
3. `scripts/regenerate-console.sh && git diff --exit-code runtime-go/rt/console_app/` — drift check (artefact stays synced).
4. `timeout 3600 cabal test` — full suite zero-fail; pending count matches main.
5. Clean-build sweep: `for d in examples/*/; do (cd "$d" && rm -rf sky-out .skycache .skydeps && sky build src/Main.sky); done`.
6. `scripts/verify-all-web.sh` — Sky.Live + Sky.Http.Server Playwright canaries; specifically check `/_sky/console` renders the Std.Ui shell and `/_sky/console/api/overview` returns JSON.
7. `scripts/verify-cli.sh` — CLI / Tui / Cli sweep.
8. `cd examples/12-skyvote && sky check` — large-example HM/build smoke.
9. From-scratch `sky init mytest && sky build && sky run`, verify `/_sky/console` mounts in dev with no env config; verify `SKY_CONSOLE_AUTH=token SKY_CONSOLE_TOKEN=… ENV=production sky run` mounts gated, and `ENV=production sky run` (no auth env) declines + logs the "auth-unset" warn.
10. **The e2-micro canary**: deploy a `sky run`-style binary onto sky-lang.org's e2-micro VM, hit `/_sky/console`, confirm Std.Ui shell renders and RSS stays < 200 MB. This is the load-bearing test — the whole reason v0.16.0 exists.

If step 10 fails: fix root cause, return to step 1. Never tag v0.16.0 with a known e2-micro regression — that would unship the whole point of the patch.

## Summary of design adjustments

| EMBEDDED.md claim | Reality | Adjustment |
|---|---|---|
| "cabal build invokes the local sky binary" | Chicken-and-egg: sky doesn't exist mid-cabal-build | **Commit generated Go in `runtime-go/rt/console_app/`** + CI drift check |
| "delete MountSubApp" | MountSubApp is exclusively console-related | Safe; sky doc uses a different pattern |
| "preserve JWT-in-URL handshake" | console_auth.go is wrapped around a reverse-proxy today | Re-wire the same middleware around the inline handler |
| `<dataDir>/<projectName>.console.db` | No projectName in BuildInfo today | Add `-X sky-app/rt.projectName=<from sky.toml>` ld-flag at codegen |
| Auth callback row-poly | `Live.app` is already row-open via `appExt` | Exact same pattern as v0.15.58's `head` field; no HM change |
| Chart primitives | No Std.Ui.Chart module today | New `sky-stdlib/Std/Ui/Chart.sky` — SVG via existing Std.Ui Node |

Total cycle scope: 4 PRs, ~6 days of work as the design predicted.
End artefact: a v0.16.0 binary that mounts the inline Std.Ui console on
the same listener, holds RSS < 200 MB on e2-micro, and exposes the
new `consoleAuth` callback row-poly field for SSO/app-pluggable
deployments.
