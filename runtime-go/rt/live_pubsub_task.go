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
// app.topics has been wired from app.store.Broker(). Subsequent
// callers overwrite the previous registration (last writer wins).
//
// Tests use unregisterProcessBroker() to keep package state clean
// between cases.
func registerProcessBroker(app *liveApp) {
	processBroker.Store(app)
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
