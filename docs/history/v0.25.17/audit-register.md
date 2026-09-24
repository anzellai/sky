# v0.25.17 app-surface soundness audit — register

The goal: audit every app surface (Std.App, Sky.Live, Sky.Spa `web:app`,
Sky.Tui / Sky.Cli) against "if it compiles, it works", and fix everything found
in one patch release. Six independent audits reproduced each finding with a real
run (Go test, headless Chromium, or a real pty) before it was listed.

This file is the durable copy of the register. The working copy lived in a
temporary directory that a machine restart cleared; this one is rebuilt from the
audit reports and tracked so the release can be checked against it.

Finding IDs: `F` shared diff and appliers, `L` Sky.Live server and client, `SPA`
Sky.Spa, `UF` Std.Ui controls and forms, `T` terminal, `SA` Std.App, `K` the
lead's first audit, `D` the later UI/UX example sweep. Several audits found the
same defect; duplicates are listed on one row (`=`).

Status: **fixed** means a regression test or e2e check covers it and was seen to
fail before the fix. **open** means not closed in this release, with the reason.

## A. Sky.Live / webview handler addressing

| ID | Defect | Status | Test |
|---|---|---|---|
| F1 = UF-2 = L1 | An element with 2+ handlers dispatched the first handler for every event (one `data-sky-hid`) | fixed | `TestRender_OneHandlerIDAttributePerElement`; live-client e2e "F1 composer", "F1 hover", "webview F1" |
| F7 | Gaining an event whose handler has no display name was read as a removal | fixed | `TestDiff_GainingUnnamedHandlerIsNotARemoval` |
| UF-3 | `Ui.onFile` / `Ui.onImage` never dispatched on Live | fixed | live-client e2e "UF-3" |
| F12 | Live / webview bound a fixed list of event names | fixed | live-client e2e "F12", "webview F12" |
| L2 | A click resolved against the CURRENT render, so a slow tap could delete another row (data loss) | fixed | `TestEvent_ResolvesAgainstTheRenderTheUserClicked`, `TestEvent_UnknownViewIsDesyncNotAnotherMsg`, `TestBeaconBatch_ResolvesAgainstEntryView`; live-client e2e "L2" |
| L3 | Two tabs on different routes: an SSE reconnect re-routed the shared page | fixed | live-client e2e "L3" |
| L6 | Enter / a key Msg overtook the pending debounced input | fixed | live-client e2e "F1/L6 composer" |

## B. DOM property sync and input authority

| ID | Defect | Status | Test |
|---|---|---|---|
| F2 = F3 = L5 = UF-6 | Removing `value` / `checked` did not reset the DOM property (stale text; several radios checked) | fixed | live-client e2e "F2", "F3", "webview F2/F3" |
| UF-6 (arrow keys) | Std.Ui radios had no shared `name`, so the browser did not group them | fixed | `TestRadioGroupsAreNamedPerGroup`, `TestRadioGroupKeepsAnAppName` |
| F9 = UF-4 | A programmatic value on the focused input never applied (focus treated as dirty) | fixed | live-client e2e "F9/UF-4" |
| UF-5 | A rejected / normalised edit left the DOM showing what the user did | fixed | `TestEvent_RejectedEditConvergesToModel`; spa-vdom-identity e2e "UF-5"; live-client e2e "UF-5" |
| F4 | A select lost its value when its options re-rendered | fixed | `TestSelectKeepsValueWhenOptionsChange`, `TestEvent_SelectKeepsValueAcrossOptionsRerender`; spa-vdom-identity e2e "F4" |
| F5 | Spa select first paint showed option 0 | fixed | spa-vdom-identity e2e "F5" |
| UF-8 | A cleared number input sent `"0"` on Live | fixed | live-client e2e "UF-8" |
| UF-11 | IME pre-edit strings were dispatched | fixed (Live, Spa); webview: see open | spa-vdom-identity e2e "UF-11"; live-client e2e "UF-11" |
| UF-13 | Checkbox / radio labels were not clickable | fixed | spa-vdom-identity e2e "UF-13" |

## C. Node identity in the diff

| ID | Defect | Status | Test |
|---|---|---|---|
| K2 | A child whose key changed kept its old sky-id (stale message; later patches lost) | fixed | `TestKeyedKeyChangeLeavesNoStaleID`, `TestKeyedReorderMovesNodes`; spa-vdom-identity e2e "K2" (Spa and Live) |
| F8 | A sibling inserted before a focused input changed its id (keys lost on Spa) | fixed | `TestKeyedChildIDIgnoresSiblingInsert`, `TestUnkeyedSiblingInsertKeepsInputNode`; e2e "F8" |
| K3 | A root tag change was applied as a children rebuild | fixed | `TestRootTagChangeReplacesRoot` |
| F6 | Injected `<style>` nodes had no id, so CSS changes were never patched | fixed | `TestInjectedStyleFollowsModel`; e2e "F6" |
| (harness) | Random tree transitions vs simulated Live and Spa appliers | fixed | `vdom_converge_test.go` (20,000 transitions, 0 mismatches against Go models of both appliers) |
| T2 | TUI: queued keys used the previous frame's handlers | fixed | `TestTuiLoop_QueuedKeysUseCurrentFrame`; tui e2e |
| T6 | TUI: focus followed a list index | fixed | `TestTuiLoop_FocusFollowsElementIdentity`; tui e2e |

## D. Sky.Spa client event payloads

| ID | Defect | Status | Test |
|---|---|---|---|
| F10 = UF-10 | Key events carried `""` | fixed | spa-vdom-identity e2e "F10" |
| F11 = UF-9 | `onCheck` panicked (string passed as Bool) | fixed | spa-vdom-identity e2e "F11" |
| UF-12 | File-too-large message said "Max 0MB" | fixed (message) | `TestSpaFileTooLargeMessageNamesTheRealLimit` |
| (lead) | A re-rendered button dispatched its first render's payload | fixed | `spa_handlers_test.go`; spa-stale-handler e2e |

## E. Type holes (compiles, fails at run time)

| ID | Defect | Status | Test |
|---|---|---|---|
| UF-1 | `Ui.onKeyDown` crashed every render; the stdlib was not checked against its annotations | fixed | `stdlib_annotation_gate::every_stdlib_body_checks_against_its_annotation`; ui-forms e2e |
| L4 = UF-7 | `onSubmit` untyped; fields zero-filled | fixed ([E2010] + strict decode) | `form_submit_and_topic_check.rs`; `TestFormSubmit_*`, `TestCoerce_FormFieldsIntoRecordIsStrict`; ui-forms e2e |
| L11 | Pub/sub payload `any` | fixed ([E2011] + classified decode) | `disagreeing_topic_across_modules_is_rejected_naming_both_sites`; `TestTopicDecode_*` |
| D1 | `!=` is not a Sky operator but compiled to `rt.Add` | fixed ([E1014]) | reject corpus `unknown_operator_bang_equals.sky` |
| T16 | Two terminal apps compiled but exited 1 at start | fixed (sensible defaults) | tui e2e |
| SA-13 | Bare `sky check` checked a different target than bare `sky build` | fixed | `std_app_flow::bare_check_verifies_the_target_a_bare_build_builds` |

## F. Subscriptions

| ID | Defect | Status | Test |
|---|---|---|---|
| SA-4 = T10 = K4 | `Sub.every` restarted on every update; Live honoured one only | fixed | `TestSubManager_SlowTimerSurvivesFrequentUpdates`, `TestSubEvery_AllTimersRunAcrossDispatches` |
| L8 | Subscriptions not re-established after a restart; own echo missed | fixed | `TestSSEConnect_ReestablishesSubscriptionsOfRestoredSession`, `TestDispatch_PublishReachesSubscriptionOpenedByTheSameUpdate` |
| T11 = SA-7 | Publish / subscribeTopic dropped in terminal loops | fixed | `TestCli_PublishAndGuard`; tui e2e |

## G. Sky.Live persistence and ordering

| ID | Defect | Status | Test |
|---|---|---|---|
| L7 | Only some dispatch paths persisted the session | fixed | `TestPerformCompletion_PersistsTheSession`, `TestTimeEveryTick_PersistsTheSession`, `TestDecodeSession_StaleOutSeqLiftedToFloor` |
| L7 (epoch) | Client seq guard across a process restart | see open | — |
| L9 | A delta frame overtaken by an HTTP reply was dropped | fixed | live-client e2e "L9" |
| L10 | `withNotFound` only on the first request | fixed | `TestInitial_NotFoundPageOnEveryRequest` |
| L12 | A classified update panic gave no feedback | fixed | `TestEvent_UpdatePanicSurfacesToTheUser`; live-client e2e "L12" |

## H. Sky.Spa RPC consistency

| ID | Defect | Status | Test |
|---|---|---|---|
| SPA-1 | Concurrent RPCs lost updates / overwrote in-flight edits | fixed | `spa_rpcqueue_test.go`; spa-rpc-consistency e2e |
| SPA-2 | Out-of-order responses applied stale values | fixed | `TestSpaRpcQueue_ResponsesApplyInDispatchOrder`; e2e "order" |
| SPA-3 | A server branch's follow-up Cmd ran nowhere | fixed | `TestSpaCollectFollowUps_RunsEveryPerformInOrder`; e2e "follow-up" |
| SPA-4 = SA-6 (web:app) | Guard saw a partial model; client never ran it | fixed | `TestSpaGuardedUpdate_RejectsLikeLive`; e2e |
| SPA-5 | Hydration checked structure only; route params decoded differently | fixed | `TestSpaRouteParamDecodesLikeTheServer`; e2e "hydration verifies text parity" |
| SPA-6 | Retry re-ran a non-idempotent RPC | fixed | `spa_rpc_dedupe_test.go` |
| SPA-7 | Retry kept only the last failed perform | fixed | `TestSpaRetryQueue_KeepsEveryFailureInOrder` |
| SPA-8 | Persistence wired only for GET-safe-init apps | fixed | spa-rpc-consistency e2e "reload" |
| K5 | Reload painted saved server-only fields over the SSR seed | fixed | `spa_persist_test.go` (server-only fields from the seed) |
| (lead) | Prelude `Cmd` read as opaque | fixed | `spa_prelude_cmd_followup::prelude_cmd_is_analysed_like_imported_std_cmd` |
| (lead) | A Msg the client `init` dispatches was pruned from the client | fixed | `spa_prelude_cmd_followup::a_msg_the_client_init_dispatches_is_never_server_internal` |
| (lead) | A wildcard guard parameter read the whole model | fixed | `spa_split_flow::spa_guard_is_enforced_server_side_on_rpc` |
| (lead) | The UF-5 reconcile overwrote an edit an RPC had not answered | fixed | spa-rpc-consistency e2e "order" |
| D2 | A record-alias field naming an imported type resolved to a same-named stdlib type | fixed | `samename_stdlib_type::imported_type_in_a_record_alias_field_is_not_hijacked_by_a_stdlib_type`; spa-examples e2e |
| D3 | Notes example saved into no row | fixed (example) | spa-examples e2e |

## I. Std.App extraction (compiler)

| ID | Defect | Status | Test |
|---|---|---|---|
| SA-1 | `withGuard` via a helper dropped on web:app (security) | fixed (fail closed) | `std_app_guard_attached_via_local_helper_is_carried`, `std_app_unknown_builder_or_opaque_step_fails_closed`; `std_app_flow::a_guard_attached_via_a_local_helper_is_enforced_on_web_app` |
| SA-2 | A second app value replaced fields | fixed | `std_app_second_app_value_does_not_replace_fields` |
| SA-3 | `import Std.App as A` not detected | fixed | `std_app_aliased_and_exposed_run_is_detected_and_rewritten` |
| SA-12 | Inline `App.run (App.app …)`; update without `case` | fixed | `std_app_inline_run_argument_is_read`; `spa_update_no_case.rs` |
| T12 = SA-6 (terminal) | `withGuard` ignored on `terminal:cli` and `App.tui` (security) | fixed; App.tui test: see open | `TestCli_PublishAndGuard`; tui e2e (cli) |

## J. Durable

| ID | Defect | Status | Test |
|---|---|---|---|
| T1 = SA-5 | `withDurable` no-op on the TUI | fixed | tui e2e "withDurable restores the model on restart" |
| SA-8 | An undecodable snapshot was reset and overwritten | fixed | `TestDurableBoot_RestoreFailureKeepsSnapshot` |
| (user decision) | `SnapshotEvent` swallowed restore and write failures | fixed (`RestoreFailed`, `PersistFailed`) | durable-tea-app fixture (exhaustive match) |
| SA-9 | Desktop window probed the wrong port | fixed; e2e: see open | `TestStdAppLivePort_FollowsEnvOverride` |
| SA-10 | `terminal:tui` ignored `withInput` | fixed | tui e2e "line prompt" |

## K. Terminal loop

| ID | Defect | Status | Test |
|---|---|---|---|
| T3 | Model changed but screen not repainted | fixed | `TestTuiLoop_RepaintsAfterDrainedMsgs` |
| T4 | `Ui.width` on an input made the view 50,000 rows | fixed | `TestTuiLayout_FillInsideContentSizedParent`; tui e2e |
| T5 | Focus markers overwrote the label | fixed | `TestPaintBox_FocusMarkersKeepLabel` |
| T7 | `Input.multiline` invisible in the TUI | fixed | tui e2e |
| T8 | TUI forms / `onEnter` never fired | fixed | `TestTuiForm_*`, `TestTuiInput_OnEnterFires`; tui e2e |
| T9 | Slider ignored value / min / max | fixed | tui e2e "vol=12" |
| T13 | String views printed a staircase | fixed | tui e2e |
| T14 | Input split across reads froze or lost characters | fixed | `TestKeyDecoder_*`; tui e2e |
| T15 | Ctrl-C ignored with `withOnKey` | fixed | tui e2e |
| T17 = SA-11 | CLI EOF dropped in-flight performs | fixed | `TestCli_EOFWaitsForInFlightPerform` |
| T18 | Alt+key not decoded | fixed | tui e2e "bumps=1" |

## L. UI/UX example sweep

| ID | Defect | Status | Test |
|---|---|---|---|
| D4 badge | The dev badge sat between the page and its script, so `sky-nav` nested the document | fixed | `TestNavEnvelopeStripsDevBadgePage`, `TestDevBannerStaysOffTheBottomBar` |
| D4 overflow | Mobile overflow in 12, 26, 37 | fixed (examples) | — (visual; checked in the sweep) |
| D4 checkbox | Two boxes in 37 / 38 | fixed (example icons); a stdlib overlay was tried and REVERTED because an empty icon made the checkbox unclickable | live-client e2e (native checkbox clickable) |

## Open in this release (and why)

| Item | Status |
|---|---|
| App.tui guard | Fixed in code; the automated test covers only `terminal:cli`. Test to be added before the tag. |
| L7 process epoch | Fixed in code; no test. Test to be added before the tag. |
| UF-11 webview IME, SA-9 desktop port | Fixed in code; no end-to-end test. Tests to be added before the tag, or the claim narrowed. |
| Auditors' "unconfirmed" items | Never reproduced by the audits; each is being reproduced before the tag (list below). |

Unconfirmed items (from the audit reports): Live popstate did not fire `onNavigate`
(Spa does); Live server clear of a focused input then typing gave `secondfirst`;
Live retry-queue replay of stale ids (covered by L2's versioning, not driven end to
end); Live POSTs not serialised (0/20 reorders seen); Live sqlite `idleEvict` could
turn L7 into loss without a restart; Spa `onImage` does no resize although the docs
say it does; `Input.currentPassword` needs a per-keystroke `onChange`; `App.tui`
had no SIGWINCH repaint; TUI focus / blur Msgs could be dropped when the channel is
full; a Raw node renders as `[raw]` in the TUI; the CLI flatten lacked a trailing
newline; Live durable restore may overwrite `withRequest` fields; a `runLiveWindow`
start failure may be lost; the window may open before an `--embed` server is ready;
a second app value's `withNotFound` / `withRoutes` may be picked up; publish /
subscribe payload types unchecked across a topic (closed by [E2011]).
