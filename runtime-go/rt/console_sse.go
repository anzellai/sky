package rt

// console_sse.go — back-compat stub surface (v0.16.1 PR10-G).
//
// The PR3 isolated SSE channel + PR8 console update loop were the
// pre-canonical-Sky.Live mechanism for driving the inline console.
// PR10-F replaced them with the canonical MountLiveSubAppInProcess
// machinery — the bundled console is now a same-process Sky.Live
// sub-app at /_sky/console with its own handleEvent / handleSSE /
// handleInitial bound to the parent mux via the per-app cookieName +
// skyIDPrefix scaffolding PR10-A landed.
//
// This file retains the public symbols that external callers (smoke
// tests, admin tooling) might reach for, returning safe defaults
// (false / nil). The internal state was deleted alongside
// console_loop.go in PR10-G.
//
// Why keep stubs vs delete: the symbols are part of the
// runtime-go public surface (anything `rt.X` that's been exported
// via Go's capitalisation convention). Removing them in a patch
// release would silently break downstream binaries that linked
// against earlier v0.16.1 PRs. v1.0.0 will drop them; v0.16.x keeps
// the no-op surface to preserve linkability.

// ConsoleEvent is the wire shape of an inline-console POST event.
//
// Pre-PR10-F: the PR3 isolated SSE channel surfaced ConsoleEvent
// values to a private rt-side consumer (console_loop.go).
// Post-PR10-F: events flow through the canonical Sky.Live
// handleEvent path, not this channel; the type stays as a public
// alias purely for callers that already declare variables of this
// shape.
type ConsoleEvent struct {
	SessionID  string
	Hid        string
	Payload    map[string]any
	Headers    map[string]string
	ReceivedAt int64 // unix nanos; was time.Time pre-PR10
}

// ConsoleSSEHealthy reports whether the legacy PR3 isolated SSE
// channel is mounted. Post-PR10-F this is ALWAYS false — the
// canonical Sky.Live machinery handles console SSE under
// `/_sky/console/_sky/sse`, not `/_sky/console/_sse`.
//
// Kept as a back-compat surface; callers that need to test "is the
// inline console serving" should use rt.InlineConsoleHealthy().
func ConsoleSSEHealthy() bool {
	return false
}

// ConsoleEventChannel returns the legacy PR3 inbound-event channel.
// Post-PR10-F this is ALWAYS nil — no producer attaches, no consumer
// needs to drain. Callers reaching for this in the v0.16.x window
// should migrate to the canonical Sky.Live event-handling path
// (Sky-source `update` function on the cfg returned by
// rt.InlineConsoleCfg).
func ConsoleEventChannel() <-chan ConsoleEvent {
	return nil
}
