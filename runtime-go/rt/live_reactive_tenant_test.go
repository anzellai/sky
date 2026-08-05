package rt

import (
	"encoding/json"
	"testing"
	"time"
)

// Tenant re-key (subscribe side): a reactive session whose VERIFIED identity
// carries Claims["tenant"]="acme" subscribes on the PER-TENANT topic
// reactive:acme:<coll> — the security boundary — and NOT on the shared collection
// topic. A session only ever receives its own tenant's nudges, removing the
// cross-tenant activity oracle the shared collection topic exposed.
func TestStartReactiveTenantTopicSubscription(t *testing.T) {
	reg := newTopicRegistry(16)
	// run returns nil → reactiveFoldFromResult yields ok=false → empty fold →
	// reactiveRefreshOnce early-returns (no view/render needed).
	runFn := func(_ any) any { return nil }
	binding := map[string]any{"Coll": "todos", "Run": runFn}
	app := &liveApp{
		reactive: func(_ any) any { return []any{binding} },
		topics:   reg,
	}
	sess := &liveSession{
		sseCh:         make(chan sseFrame, 4),
		cancelSub:     make(chan struct{}),
		done:          make(chan struct{}),
		model:         map[string]any{"todos": []any{}},
		identity:      ConsoleIdentity{Subject: "u1", Claims: map[string]string{"tenant": "acme"}},
		identityValid: true,
	}

	app.startReactive(sess)

	if got := reg.SubscriberCount("reactive:acme:todos"); got != 1 {
		t.Fatalf("tenant topic SubscriberCount=%d want 1 (session must subscribe on the tenant topic)", got)
	}
	if got := reg.SubscriberCount(bluedbCollTopic("todos")); got != 0 {
		t.Fatalf("collection topic SubscriberCount=%d want 0 (a tenant session must NOT subscribe on the shared collection topic)", got)
	}

	sess.teardownReactive()
	if got := reg.SubscriberCount("reactive:acme:todos"); got != 0 {
		t.Fatalf("teardown left %d subscriptions on the tenant topic", got)
	}
}

// Fallback (subscribe side): no verified identity → subscribe on the collection
// topic (byte-identical to the pre-tenant behaviour for unauth / dev /
// single-tenant), never an empty-tenant "reactive::todos" topic.
func TestStartReactiveNoIdentityFallsBackToCollectionTopic(t *testing.T) {
	reg := newTopicRegistry(16)
	runFn := func(_ any) any { return nil }
	binding := map[string]any{"Coll": "todos", "Run": runFn}
	app := &liveApp{
		reactive: func(_ any) any { return []any{binding} },
		topics:   reg,
	}
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		model:     map[string]any{"todos": []any{}},
		// no identity — identityValid stays false → fallback path
	}

	app.startReactive(sess)

	if got := reg.SubscriberCount(bluedbCollTopic("todos")); got != 1 {
		t.Fatalf("collection topic SubscriberCount=%d want 1 (unauth session subscribes the fallback)", got)
	}
	if got := reg.SubscriberCount("reactive::todos"); got != 0 {
		t.Fatalf("no-tenant session must not create an empty-tenant topic; got %d", got)
	}

	sess.teardownReactive()
}

// Tenant re-key (write/publish side): with the current goroutine stamped with a
// verified tenant identity, reactivePublishScoped derives the topic from the
// WRITER's own identity and delivers the nudge on the tenant topic — never the
// collection topic. This is what makes it forgery-safe (topic from verified
// identity, not from record data).
func TestReactivePublishScopedToTenantTopic(t *testing.T) {
	unregisterProcessBroker() // clear any leftover from a prior test
	reg := newTopicRegistry(16)
	app := &liveApp{topics: reg}
	registerProcessBroker(app)
	defer unregisterProcessBroker()

	sess := &liveSession{
		identity:      ConsoleIdentity{Subject: "u1", Claims: map[string]string{"tenant": "acme"}},
		identityValid: true,
	}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	tenantCh, cancelT := reg.Subscribe("reactive:acme:todos")
	defer cancelT()
	collCh, cancelC := reg.Subscribe(bluedbCollTopic("todos"))
	defer cancelC()

	reactivePublishScoped("todos", "put", "7")

	select {
	case ev := <-tenantCh:
		var p bluedbChangePayload
		if err := json.Unmarshal([]byte(ev.Payload.(string)), &p); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
		// NUDGE only — record body never broadcast (cross-tenant safety).
		if p.Op != "put" || p.Coll != "todos" || p.Pk != "7" || p.Record != "" {
			t.Fatalf("tenant payload = %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no nudge delivered to the tenant topic")
	}

	// The collection topic must NOT receive the write-layer scoped publish.
	select {
	case ev := <-collCh:
		t.Fatalf("collection topic unexpectedly received a scoped publish: %v", ev.Payload)
	default:
	}
}

// Fallback (write/publish side): with NO verified identity stamped on the
// goroutine, reactivePublishScoped falls back to the collection topic —
// byte-identical to the unauth path reactivePublish uses.
func TestReactivePublishScopedFallsBackToCollectionTopic(t *testing.T) {
	unregisterProcessBroker()
	reg := newTopicRegistry(16)
	app := &liveApp{topics: reg}
	registerProcessBroker(app)
	defer unregisterProcessBroker()

	// Ensure no session is stamped on this goroutine.
	clearGoroutineLiveSession()

	ch, cancel := reg.Subscribe(bluedbCollTopic("todos"))
	defer cancel()

	reactivePublishScoped("todos", "put", "7")

	select {
	case ev := <-ch:
		var p bluedbChangePayload
		if err := json.Unmarshal([]byte(ev.Payload.(string)), &p); err != nil {
			t.Fatalf("payload not JSON: %v", err)
		}
		if p.Op != "put" || p.Coll != "todos" || p.Pk != "7" || p.Record != "" {
			t.Fatalf("fallback payload = %+v", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no nudge delivered to the collection topic (unauth fallback)")
	}
}
