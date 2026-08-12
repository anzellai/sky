package rt

import "sync/atomic"

// processBroker — the process's active *liveApp, set by Live_app when
// the broker is wired (see liveAppRun in live.go). Reads happen on the
// hot path of PubSub_publish so the atomic.Pointer is preferable to a
// sync.Mutex.
//
// Single-app process: works trivially.
// Multi-app process (rare — each Live.app binds its own port, but a
// program could orchestrate several): the most-recently-registered app
// wins. Documented limitation; consistent with the existing port-
// binding model.
var processBroker atomic.Pointer[liveApp]

// registerProcessBroker — called once per Live.app startup, after
// app.topics has been wired from app.store.Broker().
//
// FIRST-WRITER-WINS semantics (v0.16.1 PR10-F):
//
//   The HOST application's broker is the canonical process broker for
//   Std.PubSub.publish — that's the broker user code expects when it
//   reaches for a Task-shaped publish from raw `api` handlers /
//   post-init goroutines / scheduled jobs. Sub-apps mounted via
//   MountLiveSubAppInProcess (e.g. the v0.16.1 PR10 inline console at
//   /_sky/console) have their OWN private broker for their OWN
//   internal pub/sub; they MUST NOT clobber the host's registration.
//
//   Pre-v0.16.1 PR10 the semantics were last-writer-wins: the most
//   recent registration won. With the canonical Sky.Live mount path
//   driving the inline console, MountEmbeddedConsole's sub-app
//   registration would have shadowed the host app's broker right after
//   AssertConsoleInvariantOrExit's predecessor (liveAppRun's
//   registerProcessBroker call). The host's Std.PubSub.publish would
//   then route to the console's empty broker — silent breakage.
//
// Tests use unregisterProcessBroker() to keep package state clean
// between cases.
func registerProcessBroker(app *liveApp) {
	// CompareAndSwap from nil to app: succeeds only when no prior
	// broker is registered (host app's first call wins). Subsequent
	// callers (sub-apps) silently no-op.
	processBroker.CompareAndSwap(nil, app)
}

// unregisterProcessBroker — test helper; in production the process
// exits when the Live.app's http.ListenAndServe returns, so manual
// teardown is unnecessary.
func unregisterProcessBroker() {
	processBroker.Store(nil)
}

// PubSub_publish — Task-shaped publish callable from ANY goroutine.
//
// Sky surface:
//
//	Std.PubSub.publish : String -> any -> Task Error Int
//
// Returns the count of subscribers that received the broadcast.
// Returns Err(Unavailable) when no Live.app has been registered in
// this process (CLI tools, isolated unit tests, agent-service-only
// processes without a Live.app).
//
// Unlike Std.Cmd.publish — which requires an update-return tuple
// and therefore only fires from Sky.Live `update` — PubSub_publish
// works from raw Sky.Http.Server `api` handlers, post-init
// goroutines, scheduled jobs, and any other "I need to broadcast
// state without an update Cmd" context.
//
// Origin is the empty string: server-side publishes are not tied to
// any originating session and therefore have no echo-suppression
// target. (Subscribers' Origin checks against their own sid will
// naturally not match "".)
func PubSub_publish(topicArg, payloadArg any) any {
	topic := AsString(topicArg)
	return func() any {
		app := processBroker.Load()
		if app == nil {
			return Err[any, any](ErrUnavailable(
				"PubSub.publish: no Sky.Live app registered in this process — Task-shaped publish needs Live.app running",
			))
		}
		if app.topics == nil {
			return Ok[any, any](0)
		}
		delivered := app.Publish(topic, SessionEvent{
			Payload: payloadArg,
			Origin:  "",
		})
		return Ok[any, any](delivered)
	}
}

// PubSub_publishNoEcho — Task-shaped no-echo publish. Sky surface:
//
//	Std.PubSub.publishNoEcho : String -> any -> Task Error Int
//
// Cycle 4 NE / issue #359 — broker sets SkipOrigin = true on the
// outgoing event. For the server-side path the Origin is always ""
// (this function is called outside any Live session's update loop),
// so SkipOrigin is effectively a no-op for the common case — no
// subscriber's ownerSid will match an empty Origin.
//
// The Sky-side surface is still useful for forward-compat with v0.16+
// cross-process broker tiers: a Redis/NATS/Cloud Pub/Sub backend may
// need to advertise the "no-echo" bit on its own protocol level (so
// the receiving node's broker can self-suppress without re-checking
// the local registry). Shipping the surface now means user code that
// migrates from PubSub.publish to PubSub.publishNoEcho doesn't need
// a re-deploy at the v0.15 → v0.16 transition.
func PubSub_publishNoEcho(topicArg, payloadArg any) any {
	topic := AsString(topicArg)
	return func() any {
		app := processBroker.Load()
		if app == nil {
			return Err[any, any](ErrUnavailable(
				"PubSub.publishNoEcho: no Sky.Live app registered in this process — Task-shaped publish needs Live.app running",
			))
		}
		if app.topics == nil {
			return Ok[any, any](0)
		}
		delivered := app.Publish(topic, SessionEvent{
			Payload:    payloadArg,
			Origin:     "",
			SkipOrigin: true,
		})
		return Ok[any, any](delivered)
	}
}
