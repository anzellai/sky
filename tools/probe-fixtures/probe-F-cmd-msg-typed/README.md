# probe-F-cmd-msg-typed

Exercises **Cause F** — Cmd/Sub non-generic runtime.

This fixture probes the typed-Msg surface; `update : Msg -> Int -> Int`
emits today as `func update(msg Msg, count int) int`.  The actual
F cause manifests in Sky.Live apps where the return type is
`(Model, Cmd Msg)` — that lowers to `(Model, rt.SkyCmd)` because
SkyCmd is non-generic.

**Current state: GREEN** for the Msg-in-arg case.
**Tighten when C15 lands** — add a Sky.Live mini-app fixture with
`update : Msg -> Model -> (Model, Cmd Msg)` and assert
`MUST_CONTAIN "rt.SkyCmd[Msg]"`.
