{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The Sky Go runtime (runtime-go/) and Sky-source stdlib (sky-stdlib/)
-- bundled into the sky binary at build time via Template Haskell.
-- Released binaries are fully standalone — no on-disk runtime-go/ or
-- sky-stdlib/ required.
--
-- Issue #58: empirical bug found 2026-05-18 — `embedDir` from
-- `file-embed-0.0.16.0` does NOT recurse into subdirectories when
-- TH-spliced into a cabal-installed binary (works correctly in
-- `runghc`, so the bug is in optimised-compile path / TH state
-- interaction). The compiled binary's `embeddedRuntime` had 97
-- entries (only top-level + go.mod/go.sum) instead of the 114 on
-- disk — `rt/jobs/*.go` and `rt/telemetry/*.go` were silently
-- missing. Any user app would `go build` fail with `package
-- sky-app/rt/jobs is not in std`.
--
-- Workaround: use a custom TH splice that explicitly walks the
-- tree at TH compile time and emits the full file list (rather
-- than trusting `embedDir`'s recursive walk).
--
-- Audit P3-3: cabal must re-embed when runtime / stdlib files
-- change. `embedDirRecursive` calls `qAddDependentFile` on every
-- file, but cabal does NOT track non-`.hs` files for its own
-- up-to-date check — so editing a `.sky` / `.go` file alone (even
-- with a newer mtime) does NOT make `cabal build` recompile this
-- module, and the embedded copy goes stale. To force a re-embed,
-- make a real content change to THIS `.hs` file — bump the marker
-- below — so cabal recompiles it and the splice re-walks the tree.
--
-- re-embed marker: 2026-05-27 — Cycle 4 HS (rev7): JS comment </script> literal escaped + stream-loop session stamp
-- re-embed marker: 2026-05-28 — Cycle 4 PT: Std.PubSub.publish (Task-shaped) + Std/PubSub.sky stdlib + live_pubsub_task.go runtime
-- re-embed marker: 2026-05-28b — Sky.Http.Server withHeader Content-Type override fix (spike-discovered)
-- re-embed marker: 2026-05-28c — fix(http-server): Server.static implementation via http.FileServer (was a stub returning literal "static:dir")
-- re-embed marker: 2026-05-28e — Cycle 4 #353: sky fmt next-anchor fallback so body comments above a reflowed expression round-trip losslessly
-- re-embed marker: 2026-05-28f — revert(canonicalise): roll back #350 alias-name fix (regression in row-poly access on duplicate-named modules — #361)
-- re-embed marker: 2026-05-28g — fix(canonicalise): cross-module alias-name collision v2 — (home, name) primary lookup + unique-body bare-name fallback (#350 + #361)
-- re-embed marker: 2026-05-29 — Cycle 4 NE (#359): Cmd.publishNoEcho + PubSub.publishNoEcho — opt-out echo via SessionEvent.SkipOrigin + Broker.SubscribeWithOwner
-- re-embed marker: 2026-05-29b — Cycle 4 HS-Server (#362): Sky.Http.Server.Stream — server-side streaming HTTP response primitive via SkyResponse.StreamHandler sentinel
-- re-embed marker: 2026-05-29c — Sky.Webview backend v0.1 MVP (#356): rt.Webview_app via webview_go bridge + Std.Webview.sky stdlib + Element-tree reuse from Sky.Live
-- re-embed marker: 2026-05-29d — sky build: force cgo-on first attempt when emitted main.go calls rt.Webview_app (the static-first build picked up webview_stub.go and shipped a silent-exit binary)
-- re-embed marker: 2026-05-29e — reservedGoNames: extend with predeclared types + constants + Go 1.21+ builtins (defense-in-depth; audit of examples/*/sky-out/main.go shows zero existing collisions — module prefix was already insulating top-level bindings, this hardens locals + parameters)
-- re-embed marker: 2026-05-29f — Sky.Core.Math: full Go math.* parity (#366) — inverse trig (asin/acos/atan/atan2), hyperbolic (sinh/cosh/tanh + inverse), exp/log family (exp/exp2/log2/log10), roots + utilities (cbrt/hypot/trunc/mod/remainder), constants (phi/sqrt2/inf/nan); +23 functions across stdlib + Kernel.hs registry + rt.go kernels
-- re-embed marker: 2026-05-30c-approach-C — fix(#365): cross-module local lambda collision — Approach C LowerCtx cascade scoped to the #365 reader path. SolvedTypes gains _stPerModuleEnv + _stCurrentModule (+ _stPerModuleRegions for safety); generateDeclsForDepScoped wraps each dep's emission in a per-module scope (eager render + IORef force-restore); lookupSolvedVarScoped consults the per-module env first under that hint.  The defToStmts let-bound-name reader path now reads via an explicit unsafePerformIO IORef read (works around the CAF leak in getCgEnv that was sharing the first-evaluated solvedTypes across all dep emissions).
-- re-embed marker: 2026-05-30d — fix(#355): align TestTime_CalendarAdd/StartOfDay/EndOfMonth/FromParts assertions with the canonical Int kernel contract (boxed Go int, not int64). Tests were written against the pre-f1e9d0d behaviour where time.UnixMilli() leaked through as int64; current contract — pinned by TestTimeKernelsBoxInt + coerceInner numeric-width bridge — narrows to int at every Int-returning Time kernel boundary. Tests now assert .(int), matching kernel behaviour and ending the "pre-existing flake" status that violated CLAUDE.md §2.4 no-deferral.
-- re-embed marker: 2026-05-30e — fix(#339): setupSubscriptions cancelSub race — guard close+reassign with dedicated cancelSubMu (test fixtures + future non-dispatch callers can no longer double-close the channel); test fix lands paired so the tick-drop regression no longer flakes the race detector.
-- re-embed marker: 2026-05-30f — fix(#342): rt.Field on user-defined ADT model field — betterTypeStr was preferring "any" over a bare TVar "T1" when the merger picked between HM-inferred dep param types (TVar-preserved: ["T1","T1"]) and the early collector's TVar-erased view (["any","any"]). Bare TVar carries strictly MORE info than `any` (preserves the polymorphism for call-site σ-recovery); the previous ordering picked `any`, collapsing polymorphic dep functions like `Sky_Test_equal[T1](T1, T1)` to `(any, any)`. coerceCallArgsAt's fallback then never emitted the needed `rt.Coerce[T1]` wrap around `rt.Field(record, "Page")`, and Go's call-site inference rejected the call with `type any of rt.Field(...) does not match inferred type Live_CounterTest_Page for T1`. Fix: betterTypeStr now treats bare TVar > "any" (concrete > both > T1 > any).
-- re-embed marker: 2026-05-30g — fix(#369): telemetry.LoadTracerConfigFromEnv VM-mode default — was returning 100% (dev/serverless arm fired against the production-gate check, which mis-attributed VM-dev as "100% is fine"). Restored to the canonical policy already encoded in rt.ServerlessTraceSampleRate(): VM=1%, serverless=100%, env-override (OTEL_TRACES_SAMPLER_ARG) wins. Production-gate (ENV/SKY_ENV) no longer participates — the always-on VM rate is independent of dev-vs-prod label, and 100% local debugging is one env var away.
-- re-embed marker: 2026-05-30h — fix(#370): Sky.Webview relative-path asset loading — when sky.toml `[live].static` is set, runtime now spawns a 127.0.0.1 loopback http.Server (free port via net.Listen) that serves `/static/*` from the configured dir + `/` returns the current rendered body wrapped in webviewPageWrap; webview Navigate()s to http://127.0.0.1:<port>/ instead of SetHtml() so the embedded browser has an origin to resolve relative URLs against. No regression: unset static → original SetHtml path. webviewState.currentBody (atomic.Value) publishes the latest render so manual reload picks up TEA-tick state. Loopback bound to 127.0.0.1 only — never 0.0.0.0 (desktop-app LAN-safety gate).
-- re-embed marker: 2026-05-31a — fix(#372): user-defined Decoder pipeline (Decode.andThen + Decode.map over curried ctor) panicked with `rt.Coerce: expected func(interface {}) interface {}, got Spec_R` at the final stage. Root cause: adaptFuncValue's Coerce[func(any) any](multiArgGoFunc) branch zero-padded the remaining slot (Spec("x", "") instead of returning a curried closure) when target's return was `any` instead of another func. Now mirrors curryRemainingArgs: when nin > len(args) AND target's return is interface{}, box a curryRemainingArgs closure as the `any` return — matches skyCallOne's existing currying contract and aligns with Sky's all-functions-curried invariant.
-- re-embed marker: 2026-05-31b — fix(#371): sky doctor port-8000 check produced false-positive findings on pristine scaffolded projects whenever the host happened to bind 8000 (unrelated dev server, parallel cabal test, browser live-reload). Doctor's `clean project` cabal spec then flaked under the full sweep with "1 warnings" instead of "no issues found". Two-tier narrowing: (1) `checkPortInUse` skipped when `sky-out/` doesn't exist (an un-built project can't have leaked a prior `sky run` listener — the suggested-fix hint is nonsensical there); (2) `SKY_DOCTOR_SKIP_PORT_CHECK=1` escape hatch for built projects where 8000 is intentionally owned by something else. DoctorSpec sets the env var on every invocation for determinism + adds a Bug #371 regression test that hits the no-sky-out gate directly without env-var help.
-- re-embed marker: 2026-05-31d — feat(#376): Std.Ui media-query primitive — Ui.breakpoint + Ui.mediaQuery. Sky-side emits a wrapper Node carrying base layout attrs + marker `data-sky-mq-q` + `data-sky-mq-rules` data attrs; runtime's new injectMediaQueryStyles pass (paired with assignSkyIDs at every render site) walks the tree, generates a sky-id-scoped `<style>` child with the per-element `@media <q> { [sky-id="<sid>"] { <rules> } }` rule, then strips the markers from the wire output. Typed Breakpoint ADT (Mobile/Tablet/Desktop, SmAndUp/MdAndUp/LgAndUp/XlAndUp, DarkMode/LightMode/ReducedMotion/TouchDevice/Portrait/Landscape, Custom { minPx, maxPx }) covers 95% of cases; mediaQuery is the escape hatch for any raw CSS media-query string. Composes via nesting (two breakpoints → two scoped style blocks → independent CSS rules). Sky.Tui silently ignores the injected <style>; Sky.Webview honours it identically to Sky.Live (shared runtime VNode pipeline). Reuses collectStyle for the per-attribute CSS-emission helper — same lowering as base inline-style attrs, so Background.color / Border.width / Font.size etc. work identically inside breakpoints.
-- re-embed marker: 2026-05-31c — fix(#63): Std.Ui flex-chain propagation — root-cause fix to the 5-attempt v0.14.7→v0.14.16 regression cycle. renderNodeAs in sky-stdlib/Std/Ui.sky now inspects the container's immediate children for a Fill attribute on its own main axis; when found AND the container itself left that axis open, a synthetic AttrHeight (Fill 1) (column-shaped: AsEl/AsColumn/AsTextColumn) or AttrWidth (Fill 1) (AsRow) is injected before computing styleStr. The synthetic flows through the same widthCssIn/heightCssIn pipeline so the propagated CSS (flex-grow:1+min-{height,width}:0 in flex parent / align-self:stretch+100% in cross-axis case) matches what the grandparent expects. Cleanly fixes Input.multiline-in-Ui.el-in-Ui.layout: the wrapWithLabel Ui.el wrapper was empty-attr'd, so the textarea's flex-grow:1 collapsed against content height; now the wrapper picks up synthetic Fill and propagates the height chain. Surgical: gated on child-has-fill, so `Ui.row [] [b,b,b]` (no fill children) and `Ui.column [Ui.height (Ui.px N)] [fill-children]` (explicit parent height) are unchanged. Paragraph (AsParagraph, display:block) skipped — its height comes from line-box rules, not flex-grow.
module Sky.Build.EmbeddedRuntime
    ( embeddedRuntime
    , embeddedSkyStdlib
    ) where

import Data.ByteString (ByteString)
import Sky.Build.EmbedDirTH (embedDirRecursive)


embeddedRuntime :: [(FilePath, ByteString)]
embeddedRuntime = $(embedDirRecursive "runtime-go")


embeddedSkyStdlib :: [(FilePath, ByteString)]
embeddedSkyStdlib = $(embedDirRecursive "sky-stdlib")
