# probe-H3 — canonical TEA `init = (Model, Cmd.none)` shape

The shape every Sky.Live + Sky.Tui + Sky.Webview app uses. The
init function returns `(Model, Cmd Msg)` and every consumer
destructures it.

This probe is the one that proves Ship Point B actually closes
the user-visible win: once H3 emits typed, every TEA app's init
emits typed, and the runtime panic class around Msg-payload
destructure (#532 + family) closes for the right reason.

**Closes by:** PR-17 + PR-18 (Cmd kernel typed migration).
