//go:build !js

package rt

import "sync"

// processBrokers — the host Sky.Live apps running in this process, in start
// order. Each registers itself in liveAppRun / Live.serve after its broker is
// wired, and a stopped app (Live.stop) removes itself.
//
// Std.PubSub.publish has no update-tuple context, so it cannot name an app:
// it publishes to EVERY registered app. A topic is a process-wide name, so a
// job that publishes "orders" reaches the subscribers of every Live app in
// the process that subscribed to it (v0.27: two apps started with Live.serve;
// before, the first app to start took every publish and a second one never
// saw any).
//
// Sub-apps mounted in-process (the inline console at /_sky/console) never
// register: they have their own private broker for their own pub/sub, and
// must not receive the host's publishes (v0.16.1 PR10-F).
var (
	processBrokersMu sync.Mutex
	processBrokers   []*liveApp
)

// registerProcessBroker adds app to the process's publish targets. A second
// call for the same app is a no-op.
func registerProcessBroker(app *liveApp) {
	if app == nil {
		return
	}
	processBrokersMu.Lock()
	defer processBrokersMu.Unlock()
	for _, a := range processBrokers {
		if a == app {
			return
		}
	}
	processBrokers = append(processBrokers, app)
}

// unregisterProcessBrokerApp removes app (Live.stop). Idempotent.
func unregisterProcessBrokerApp(app *liveApp) {
	processBrokersMu.Lock()
	defer processBrokersMu.Unlock()
	for i, a := range processBrokers {
		if a == app {
			processBrokers = append(processBrokers[:i:i], processBrokers[i+1:]...)
			return
		}
	}
}

// unregisterProcessBroker clears every registration. Test helper.
func unregisterProcessBroker() {
	processBrokersMu.Lock()
	processBrokers = nil
	processBrokersMu.Unlock()
}

// processBrokerApps snapshots the registered apps.
func processBrokerApps() []*liveApp {
	processBrokersMu.Lock()
	defer processBrokersMu.Unlock()
	return append([]*liveApp(nil), processBrokers...)
}

// publishToProcessApps publishes ev on every registered app's broker and
// returns the total delivery count; ok is false when no app is registered.
// Two apps that share one broker object publish once.
func publishToProcessApps(topic string, ev SessionEvent) (int, bool) {
	apps := processBrokerApps()
	if len(apps) == 0 {
		return 0, false
	}
	delivered := 0
	seen := map[Broker]bool{}
	for _, app := range apps {
		if app.topics == nil || seen[app.topics] {
			continue
		}
		seen[app.topics] = true
		delivered += app.Publish(topic, ev)
	}
	return delivered, true
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
		delivered, ok := publishToProcessApps(topic, SessionEvent{
			Payload: payloadArg,
			Origin:  "",
		})
		if !ok {
			return Err[any, any](ErrUnavailable(
				"PubSub.publish: no Sky.Live app registered in this process — Task-shaped publish needs Live.app running",
			))
		}
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
		delivered, ok := publishToProcessApps(topic, SessionEvent{
			Payload:    payloadArg,
			Origin:     "",
			SkipOrigin: true,
		})
		if !ok {
			return Err[any, any](ErrUnavailable(
				"PubSub.publishNoEcho: no Sky.Live app registered in this process — Task-shaped publish needs Live.app running",
			))
		}
		return Ok[any, any](delivered)
	}
}
