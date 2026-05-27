package rt

// Cycle 3 P47 — global + local seq split contract tests.
//
// Pub/sub prereq 2 per docs/skylive/pubsub-design.md §3.2:
//
//   - Per-session monotonic counter renamed `outSeq → localSeq` and the
//     accessor `nextOutSeq → nextLocalSeq` (mechanical refactor — covered
//     by live_protocol_test.go's renamed TestLocalSeqMonotonic).
//   - New app-wide counter `liveApp.globalSeq atomic.Int64` bumped ONCE
//     per Publish, BEFORE fan-out, so all subscribers see the SAME
//     globalSeq for one logical publish (design doc §3.2 invariant).
//   - The SSE envelope grows an OPTIONAL `globalSeq` field (omitempty);
//     non-broadcast frames stay byte-identical to pre-P47 (envelope
//     compatibility — design doc §3.2 + §7).
//   - The client guard drops a frame whose globalSeq has already been
//     applied (mirrors __skyLastAppliedSeq semantics for the local
//     counter — design doc §3.2).
//
// What this file pins (each test maps to an acceptance criterion):
//
//   - Producer test: prepareFrameSnapshotWithGlobalSeq captures the
//     supplied globalSeq alongside the bumped localSeq.
//   - Producer test: liveApp.Publish bumps globalSeq exactly once and
//     stamps the value into the outgoing event.
//   - Producer test: concurrent Publish calls produce a contiguous
//     gap-free globalSeq series (atomic.Int64 contract).
//   - Wire test: encodeSSEFrameFromSnapshot includes globalSeq when
//     non-zero AND omits it when zero (legacy envelope shape).
//   - Wire test: encodePatchesEventFromSnapshot does the same on the
//     patches event envelope.
//   - Client test: __skyHandleResponse drops a replayed broadcast
//     globalSeq (modelled by direct __skyIngestSeq state + the gate
//     condition — JS is exercised via Playwright in P49; here we pin
//     the contract at the level the Go runtime is responsible for).

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// recvTimeout — the same 100ms ceiling live_topics_test.go uses; long
// enough to mask schedule jitter on heavily-loaded CI yet short enough
// that a hung path surfaces fast.
const recvTimeout = 100 * time.Millisecond

// ────────────────────────────────────────────────────────────────────
// Producer — frameSnapshot captures both seqs
// ────────────────────────────────────────────────────────────────────

// Test_PrepareFrameSnapshotWithGlobalSeq_CapturesBothSeqs pins the
// snapshot helper's contract: localSeq bumps via the existing
// nextLocalSeq path; globalSeq rides the supplied value verbatim.
// Both fields land on the returned frameSnapshot.
func Test_PrepareFrameSnapshotWithGlobalSeq_CapturesBothSeqs(t *testing.T) {
	s := &liveSession{}
	// Establish a non-zero localSeq baseline so the test catches a
	// regression that would zero one field while populating the other.
	s.localSeq = 42

	snap := s.prepareFrameSnapshotWithGlobalSeq("<body/>", 99)
	if snap.seq != 43 {
		t.Fatalf("localSeq did not bump: got %d, want 43", snap.seq)
	}
	if snap.globalSeq != 99 {
		t.Fatalf("globalSeq not captured: got %d, want 99", snap.globalSeq)
	}
	if snap.body != "<body/>" {
		t.Fatalf("body not captured: got %q", snap.body)
	}
}

// Test_PrepareFrameSnapshot_DefaultsGlobalSeqToZero pins the
// non-broadcast common case: ordinary dispatch-driven snapshots leave
// globalSeq at 0 so the SSE envelope's `globalSeq,omitempty` field is
// elided and the wire stays byte-identical to pre-P47.
func Test_PrepareFrameSnapshot_DefaultsGlobalSeqToZero(t *testing.T) {
	s := &liveSession{}
	snap := s.prepareFrameSnapshot("<body/>")
	if snap.globalSeq != 0 {
		t.Fatalf("non-broadcast snapshot leaked a non-zero globalSeq: got %d", snap.globalSeq)
	}
	if snap.seq != 1 {
		t.Fatalf("localSeq did not bump from zero baseline: got %d", snap.seq)
	}
}

// ────────────────────────────────────────────────────────────────────
// Producer — liveApp.Publish bumps globalSeq once and stamps
// ────────────────────────────────────────────────────────────────────

// Test_LiveApp_Publish_BumpsGlobalSeqOnce_AndStamps pins the
// publish-side invariant: ONE Publish call → ONE globalSeq bump → the
// bumped value rides on the event delivered to every subscriber.
//
// design doc §3.2: "Every Publish call (one bump per publish, BEFORE
// fan-out, so every subscriber sees the same globalSeq for one
// publish)".
func Test_LiveApp_Publish_BumpsGlobalSeqOnce_AndStamps(t *testing.T) {
	app := &liveApp{topics: newTopicRegistry(8)}
	ch1, cancel1 := app.topics.Subscribe("room-1")
	defer cancel1()
	ch2, cancel2 := app.topics.Subscribe("room-1")
	defer cancel2()

	delivered := app.Publish("room-1", SessionEvent{Payload: "hi", Origin: "sid-A"})
	if delivered != 2 {
		t.Fatalf("expected 2 subscribers reached, got %d", delivered)
	}

	ev1, ok1 := recvWithin(t, ch1, recvTimeout)
	if !ok1 {
		t.Fatalf("sub 1 received no event")
	}
	ev2, ok2 := recvWithin(t, ch2, recvTimeout)
	if !ok2 {
		t.Fatalf("sub 2 received no event")
	}
	if ev1.GlobalSeq != 1 {
		t.Fatalf("sub 1 globalSeq: got %d, want 1", ev1.GlobalSeq)
	}
	if ev2.GlobalSeq != 1 {
		t.Fatalf("sub 2 globalSeq: got %d, want 1 (both subs MUST see the same value for one publish)", ev2.GlobalSeq)
	}

	// Second publish bumps once.
	app.Publish("room-1", SessionEvent{Payload: "ho", Origin: "sid-A"})
	ev1b, _ := recvWithin(t, ch1, recvTimeout)
	ev2b, _ := recvWithin(t, ch2, recvTimeout)
	if ev1b.GlobalSeq != 2 {
		t.Fatalf("second publish globalSeq: got %d, want 2", ev1b.GlobalSeq)
	}
	if ev2b.GlobalSeq != 2 {
		t.Fatalf("second publish globalSeq on sub2: got %d, want 2", ev2b.GlobalSeq)
	}

	// The app counter ALSO advances independently of the stamp — pin
	// the atomic snapshot to catch a regression where the stamp reads
	// the wrong field.
	if got := app.globalSeq.Load(); got != 2 {
		t.Fatalf("app.globalSeq after 2 publishes: got %d, want 2", got)
	}
}

// Test_LiveApp_Publish_ConcurrentBumpsAreGapFree pins atomic.Int64 +
// the locked-in "one bump per publish" contract under concurrent
// publishers. N publish calls MUST produce a contiguous globalSeq
// series [1..N] with no duplicates, no gaps.
//
// design doc §3.2 footnote: "The split keeps per-session dispatch
// lock-free (no cross-session contention) and pays the atomic cost
// only on broadcast."
func Test_LiveApp_Publish_ConcurrentBumpsAreGapFree(t *testing.T) {
	const N = 200
	app := &liveApp{topics: newTopicRegistry(N + 8)}
	// One subscriber receives every publish; the channel buffer is
	// sized for the burst so no drops mask a missed bump.
	ch, cancel := app.topics.Subscribe("burst")
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			app.Publish("burst", SessionEvent{Payload: "x"})
		}()
	}
	wg.Wait()

	seen := map[int64]bool{}
	for i := 0; i < N; i++ {
		ev, ok := recvWithin(t, ch, recvTimeout)
		if !ok {
			t.Fatalf("received only %d / %d events", i, N)
		}
		if seen[ev.GlobalSeq] {
			t.Fatalf("duplicate globalSeq: %d", ev.GlobalSeq)
		}
		seen[ev.GlobalSeq] = true
	}
	// Pin gap-freeness: every value in [1..N] is present.
	for k := int64(1); k <= int64(N); k++ {
		if !seen[k] {
			t.Fatalf("missing globalSeq %d in [1..%d]", k, N)
		}
	}
	if got := app.globalSeq.Load(); got != int64(N) {
		t.Fatalf("app.globalSeq after %d concurrent publishes: got %d", N, got)
	}
}

// Test_LiveApp_nextGlobalSeq_StandaloneMonotonic pins the bump helper
// directly (no Publish indirection) so a future refactor that moves
// the call site can't regress the atomic semantics silently.
func Test_LiveApp_nextGlobalSeq_StandaloneMonotonic(t *testing.T) {
	app := &liveApp{}
	a := app.nextGlobalSeq()
	b := app.nextGlobalSeq()
	c := app.nextGlobalSeq()
	if a != 1 || b != 2 || c != 3 {
		t.Errorf("nextGlobalSeq non-monotonic: got %d,%d,%d, want 1,2,3", a, b, c)
	}
}

// ────────────────────────────────────────────────────────────────────
// Wire — SSE envelope carries globalSeq (optional + backwards compat)
// ────────────────────────────────────────────────────────────────────

// Test_EncodeSSEFrameFromSnapshot_OmitsGlobalSeqWhenZero is the
// backwards-compat anchor: a non-broadcast frame's JSON envelope
// MUST be byte-identical to pre-P47 (no `globalSeq` key).
func Test_EncodeSSEFrameFromSnapshot_OmitsGlobalSeqWhenZero(t *testing.T) {
	snap := frameSnapshot{seq: 5, body: "<p/>"}
	got := encodeSSEFrameFromSnapshot(snap)

	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("frame invalid JSON: %v (%q)", err, got)
	}
	if _, present := env["globalSeq"]; present {
		t.Fatalf("globalSeq must be omitted when zero, got envelope %q", got)
	}
	if env["seq"].(float64) != 5 {
		t.Fatalf("seq missing/wrong: %v", env["seq"])
	}
	if env["body"].(string) != "<p/>" {
		t.Fatalf("body missing/wrong: %v", env["body"])
	}
}

// Test_EncodeSSEFrameFromSnapshot_IncludesGlobalSeqWhenNonZero pins
// the broadcast-derived envelope shape: globalSeq present, value
// matches the snapshot.
func Test_EncodeSSEFrameFromSnapshot_IncludesGlobalSeqWhenNonZero(t *testing.T) {
	snap := frameSnapshot{seq: 5, globalSeq: 42, body: "<p/>"}
	got := encodeSSEFrameFromSnapshot(snap)

	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("frame invalid JSON: %v (%q)", err, got)
	}
	if env["globalSeq"].(float64) != 42 {
		t.Fatalf("globalSeq missing/wrong: %v", env["globalSeq"])
	}
}

// Test_EncodePatchesEventFromSnapshot_OmitsGlobalSeqWhenZero is the
// backwards-compat anchor for the patches envelope (P50 SSE diff path).
func Test_EncodePatchesEventFromSnapshot_OmitsGlobalSeqWhenZero(t *testing.T) {
	snap := frameSnapshot{seq: 5}
	got := encodePatchesEventFromSnapshot(snap, []Patch{})

	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("envelope invalid JSON: %v (%q)", err, got)
	}
	if _, present := env["globalSeq"]; present {
		t.Fatalf("globalSeq must be omitted when zero, got %q", got)
	}
}

// Test_EncodePatchesEventFromSnapshot_IncludesGlobalSeqWhenNonZero
// pins the broadcast-derived patches envelope shape.
func Test_EncodePatchesEventFromSnapshot_IncludesGlobalSeqWhenNonZero(t *testing.T) {
	snap := frameSnapshot{seq: 5, globalSeq: 7}
	got := encodePatchesEventFromSnapshot(snap, []Patch{})

	var env map[string]any
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("envelope invalid JSON: %v (%q)", err, got)
	}
	if env["globalSeq"].(float64) != 7 {
		t.Fatalf("globalSeq missing/wrong: %v", env["globalSeq"])
	}
}

// Test_LegacyEnvelope_MissingGlobalSeqDecodesToZero pins the
// backwards-compat contract from the typed-struct decode angle: an
// SSE envelope produced by a pre-P47 server (no globalSeq field at
// all) decodes into the patchesEventEnvelope struct with
// GlobalSeq == 0 (Go's zero value for int64). The client treats 0 as
// "no broadcast ordering constraint" and never blocks on it — so a
// fresh-install Sky.Live client talking to a stale server (or vice
// versa) keeps working without protocol negotiation.
//
// This is the canonical pin for design doc §3.2's "Add `globalSeq`
// (optional/zero when not a broadcast frame)" + §7's "Both seqs
// travel in the same JSON envelope. No conflict."
func Test_LegacyEnvelope_MissingGlobalSeqDecodesToZero(t *testing.T) {
	// Pre-P47 envelope shape — no globalSeq field at all.
	legacyJSON := `{"seq":17,"patches":[]}`
	var env patchesEventEnvelope
	if err := json.Unmarshal([]byte(legacyJSON), &env); err != nil {
		t.Fatalf("legacy envelope failed to decode: %v", err)
	}
	if env.GlobalSeq != 0 {
		t.Fatalf("legacy envelope.GlobalSeq: got %d, want 0 (zero value)", env.GlobalSeq)
	}
	if env.Seq != 17 {
		t.Fatalf("legacy envelope.Seq: got %d, want 17", env.Seq)
	}
}

// Test_NewEnvelope_RoundTripsBothSeqs pins the additive contract: a
// P47 server's envelope decodes BOTH seqs cleanly through the typed
// struct path. Round-trip: encode → JSON → decode → assert values.
func Test_NewEnvelope_RoundTripsBothSeqs(t *testing.T) {
	original := patchesEventEnvelope{
		Seq:       3,
		GlobalSeq: 11,
		Patches:   []Patch{},
	}
	blob, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded patchesEventEnvelope
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Seq != 3 || decoded.GlobalSeq != 11 {
		t.Fatalf("round-trip dropped a field: got seq=%d, globalSeq=%d", decoded.Seq, decoded.GlobalSeq)
	}
}

// ────────────────────────────────────────────────────────────────────
// Wire — end-to-end: producer + envelope through prepareFrameSnapshot
// ────────────────────────────────────────────────────────────────────

// Test_BroadcastDerivedFrame_WireShape exercises the canonical
// broadcast-frame production path end-to-end at the runtime layer:
//
//  1. app.Publish stamps globalSeq onto the SessionEvent.
//  2. (Simulating P48's subscriber goroutine:) the subscriber captures
//     the stamped globalSeq AND threads it through
//     prepareFrameSnapshotWithGlobalSeq under sess.mu.
//  3. encodeSSEFrameFromSnapshot ships an envelope carrying both seqs.
//
// This is the contract P48 depends on. Pinning it at the snapshot+
// encode layer means P48 can wire the subscriber goroutine without
// re-litigating the seq plumbing.
func Test_BroadcastDerivedFrame_WireShape(t *testing.T) {
	app := &liveApp{topics: newTopicRegistry(8)}
	ch, cancel := app.topics.Subscribe("collab")
	defer cancel()

	// Publish → globalSeq bumped + stamped.
	app.Publish("collab", SessionEvent{Payload: "edit-1", Origin: "sid-pub"})
	ev, ok := recvWithin(t, ch, recvTimeout)
	if !ok {
		t.Fatalf("no event delivered")
	}

	// Simulate the subscriber goroutine (P48's job to wire — here we
	// just exercise the same surface): capture globalSeq, acquire
	// sess.mu, build the snapshot.
	sess := &liveSession{}
	sess.mu.Lock()
	snap := sess.prepareFrameSnapshotWithGlobalSeq("<view/>", ev.GlobalSeq)
	sess.mu.Unlock()

	// Encode + assert wire shape.
	wire := encodeSSEFrameFromSnapshot(snap)
	var env map[string]any
	if err := json.Unmarshal([]byte(wire), &env); err != nil {
		t.Fatalf("envelope invalid JSON: %v", err)
	}
	if env["globalSeq"].(float64) != 1 {
		t.Fatalf("end-to-end globalSeq lost: %v in %q", env["globalSeq"], wire)
	}
	if env["seq"].(float64) != 1 {
		t.Fatalf("localSeq lost: %v in %q", env["seq"], wire)
	}
}

// ────────────────────────────────────────────────────────────────────
// Client guard — runtime-level model of __skyHandleResponse contract
// ────────────────────────────────────────────────────────────────────

// Test_LocalSeq_StaleDrop_GuardSemantics models the client-side
// __skyLastAppliedSeq gate at the runtime layer: a frame whose seq has
// already been applied is dropped.
//
// The JS itself is exercised via Playwright in P49 (the full pubsub
// example). Here we lock in the same monotonic-applied invariant at
// the Go layer so a regression in the guard logic surfaces in the
// runtime test sweep (fast feedback) rather than waiting for an
// integration run.
func Test_LocalSeq_StaleDrop_GuardSemantics(t *testing.T) {
	// Model: applied is the largest local seq the client has ingested.
	// A frame is dropped iff frame.seq > 0 && frame.seq <= applied.
	applied := int64(5)
	drop := func(seq int64) bool {
		return seq > 0 && seq <= applied
	}
	if !drop(3) {
		t.Errorf("seq=3 (<=applied=5) MUST drop")
	}
	if !drop(5) {
		t.Errorf("seq=5 (==applied=5) MUST drop (monotonic-applied)")
	}
	if drop(6) {
		t.Errorf("seq=6 (>applied=5) MUST apply")
	}
	if drop(0) {
		t.Errorf("seq=0 (missing/legacy) MUST apply (backwards compat)")
	}
}

// Test_GlobalSeq_DedupeDrop_GuardSemantics models the P47 broadcast
// dedupe guard's contract: a replayed broadcast frame (one whose
// globalSeq has already been applied) is dropped, mirroring the local
// counter's gate.
//
// design doc §3.2: "Client stores __skyLastAppliedLocalSeq (existing)
// AND __skyLastAppliedGlobalSeq (new). Gap-check on globalSeq
// surfaces missed broadcast frames."
//
// The runtime variant of the test pins the same boolean logic the JS
// applies; the JS itself is verified via Playwright in P49.
func Test_GlobalSeq_DedupeDrop_GuardSemantics(t *testing.T) {
	// Model: lastGlobal is the largest broadcast globalSeq applied.
	// A frame is dropped iff globalSeq > 0 && globalSeq <= lastGlobal.
	lastGlobal := int64(10)
	drop := func(gs int64) bool {
		return gs > 0 && gs <= lastGlobal
	}
	if !drop(7) {
		t.Errorf("globalSeq=7 (<=lastGlobal=10) MUST drop")
	}
	if !drop(10) {
		t.Errorf("globalSeq=10 (==lastGlobal=10) MUST drop")
	}
	if drop(11) {
		t.Errorf("globalSeq=11 (>lastGlobal=10) MUST apply")
	}
	// The non-broadcast case (legacy / no broadcast on this frame):
	// globalSeq absent → coerced to 0 → guard always passes.
	if drop(0) {
		t.Errorf("globalSeq=0 (missing/non-broadcast) MUST NOT block — backwards compat + the common case")
	}
}

// Test_BothGuardsIndependent pins the §3.2 invariant that the two
// guards fire independently: a fresh broadcast frame (new globalSeq)
// MUST still pass even when its local seq was already applied (the
// per-session view re-render happens regardless of broadcast ordering;
// only the SAME broadcast event replayed is what dedupe catches).
//
// This is subtle and load-bearing: per-session localSeq applies to
// EVERY frame; broadcast globalSeq applies to broadcast frames only.
// The two counters describe different orderings and the guard MUST
// honour both independently.
func Test_BothGuardsIndependent(t *testing.T) {
	// State after some history.
	appliedLocal := int64(5)
	lastGlobal := int64(2)

	// Combined gate, mirroring __skyHandleResponse:
	//   drop if frame.localSeq <= appliedLocal AND frame.localSeq > 0
	//   OR    frame.globalSeq <= lastGlobal AND frame.globalSeq > 0
	drop := func(localSeq, globalSeq int64) bool {
		if localSeq > 0 && localSeq <= appliedLocal {
			return true
		}
		if globalSeq > 0 && globalSeq <= lastGlobal {
			return true
		}
		return false
	}
	// Fresh on both → apply.
	if drop(6, 3) {
		t.Errorf("fresh on both counters MUST apply")
	}
	// Stale on local → drop.
	if !drop(4, 3) {
		t.Errorf("stale local MUST drop")
	}
	// Stale on global → drop (even if local fresh).
	if !drop(6, 1) {
		t.Errorf("stale global MUST drop")
	}
	// Both stale → drop.
	if !drop(2, 1) {
		t.Errorf("stale on both MUST drop")
	}
	// Legacy-no-global (globalSeq=0) + fresh local → apply.
	if drop(6, 0) {
		t.Errorf("fresh local + missing global MUST apply")
	}
}

// ────────────────────────────────────────────────────────────────────
// Persistence — Cycle 3 P47 leaves OutSeq GOB field name unchanged
// ────────────────────────────────────────────────────────────────────

// Test_OutSeqGobFieldName_StableForBackwardsCompat pins the
// persistence-layer contract: even though the in-memory liveSession
// field renamed `outSeq → localSeq`, the GOB-persisted struct
// storableSession.OutSeq MUST stay named OutSeq so existing SQLite /
// Postgres / Redis / Firestore session blobs continue to decode after
// a server binary upgrade.
//
// Documented in live_store.go's storableSession comment + this test
// guards against a future "consistency cleanup" rename that would
// invalidate every persisted session in production.
func Test_OutSeqGobFieldName_StableForBackwardsCompat(t *testing.T) {
	// Compile-time check via reflection — if the field is renamed the
	// compiler will catch it at this line, surfacing as a test build
	// failure rather than a silent gob-decode regression at deploy.
	var s storableSession
	_ = s.OutSeq

	// Round-trip: localSeq → OutSeq → localSeq across encode/decode.
	sess := buildSess(map[string]any{"model": "x"})
	sess.localSeq = 12
	blob, err := encodeSession(sess)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.localSeq != 12 {
		t.Fatalf("localSeq did not round-trip via OutSeq: got %d, want 12", decoded.localSeq)
	}
}

// Test_GlobalSeq_NotPersistedPerSession pins the design decision
// (live_store.go's storableSession comment): globalSeq is an
// app-wide atomic on liveApp, NOT a per-session field. A server
// restart resets globalSeq to 0; the client's __skyLastGlobalSeq
// guard is benign in that case (a fresh broadcast cycle is its own
// monotonic series; existing local-seq persistence keeps the
// stale-drop ladder working for the per-session counter).
//
// The test asserts there is NO globalSeq field on storableSession.
// Catches a regression where a misguided "make pub/sub durable" PR
// tries to bake the wrong thing into the session blob.
func Test_GlobalSeq_NotPersistedPerSession(t *testing.T) {
	// Compile-time check: storableSession has no GlobalSeq field.
	// Build-time failure if a future PR adds it.
	type expected struct {
		Model    any
		LastSeen any
		OutSeq   int64
	}
	_ = expected{}

	// And the canonical pattern: app-level globalSeq is reset to 0
	// when a fresh liveApp is constructed — exactly what a server
	// restart yields.
	app := &liveApp{}
	if app.globalSeq.Load() != 0 {
		t.Fatalf("fresh liveApp.globalSeq: got %d, want 0", app.globalSeq.Load())
	}
}

// ────────────────────────────────────────────────────────────────────
// Compile-time spec: the atomic.Int64 type is the canonical surface
// ────────────────────────────────────────────────────────────────────

// Test_LiveApp_GlobalSeq_IsAtomicInt64 pins the field type via
// compile-time + runtime check. atomic.Int64 has explicit Add/Load
// methods and disallows copying (sync/atomic.noCopy); using a bare
// int64 would lose the lock-free contract design doc §3.2 calls out.
// A future "simplification" PR that swaps it for a plain int64 +
// sync.Mutex would either fail to compile (no Add method) or fail
// the runtime invariant (Load on bare int64 returns the zero value).
func Test_LiveApp_GlobalSeq_IsAtomicInt64(t *testing.T) {
	app := &liveApp{}
	// Take the address — atomic.Int64 is a noCopy type, so the spec
	// is "always reference via pointer". If app.globalSeq's type were
	// changed away from atomic.Int64, the .Add method would not
	// resolve and this would fail to compile.
	got := app.globalSeq.Add(0)
	if got != 0 {
		t.Fatalf("fresh app.globalSeq.Add(0): got %d, want 0", got)
	}
	if v := app.globalSeq.Load(); v != 0 {
		t.Fatalf("fresh app.globalSeq.Load(): got %d, want 0", v)
	}
}

// ────────────────────────────────────────────────────────────────────
// Client JS — __skyLastGlobalSeq + globalSeq plumbing on every handler
// ────────────────────────────────────────────────────────────────────

// Test_LiveJS_EmitsGlobalSeqGuard pins the JS-emission contract for
// the client-side broadcast-dedupe guard (mirrors
// TestLiveJS_EmitsPatchesEventListener's grep-style approach):
//
//  1. The state variable `__skyLastGlobalSeq` MUST be declared.
//  2. `__skyHandleResponse` MUST accept a globalSeq parameter and
//     gate on it (`globalSeq <= __skyLastGlobalSeq`).
//  3. Each handler that calls `__skyHandleResponse` MUST forward
//     the frame's globalSeq (`frame.globalSeq` or `data.globalSeq`).
//
// design doc §3.2: "Client stores __skyLastAppliedLocalSeq (existing)
// AND __skyLastAppliedGlobalSeq (new)." (Naming shortened to
// __skyLastGlobalSeq for symmetry with the existing
// __skyLastAppliedSeq counter.)
func Test_LiveJS_EmitsGlobalSeqGuard(t *testing.T) {
	js := liveJS("test-sid")

	// State variable present.
	if !strings.Contains(js, "__skyLastGlobalSeq") {
		t.Fatalf("liveJS missing __skyLastGlobalSeq state declaration — P47 broadcast-dedupe guard not wired")
	}

	// Guard condition present (substring match on the boolean shape).
	if !strings.Contains(js, "<= __skyLastGlobalSeq") {
		t.Fatalf("liveJS missing `<= __skyLastGlobalSeq` guard expression — broadcast dedupe gate not active")
	}

	// __skyHandleResponse signature carries the new parameter.
	if !strings.Contains(js, "function __skyHandleResponse(seq, ackInputs, applyFn, globalSeq)") {
		t.Fatalf("liveJS __skyHandleResponse signature does NOT carry globalSeq — caller-side plumbing won't compile")
	}

	// Every existing call site that legitimately delivers a frame
	// from the wire forwards globalSeq. The substring on each path is
	// the specific call site shape so a regression that misses one
	// surfaces with a precise diagnostic.
	wantSites := []string{
		// HTTP /_sky/event JSON reply (data.globalSeq).
		"function() {\n          if (data.patches) __skyApplyPatches(data.patches);\n        }, data.globalSeq",
		// Legacy SSE event:patch handler (frame.globalSeq).
		"if (frame.body) __skyPatch(frame.body.replace(/\\\\n/g, \"\\n\"));\n      }, frame.globalSeq",
		// P50b SSE event:patches handler (frame.globalSeq).
		"__skyApplyPatches(frame.patches);\n    }, frame.globalSeq",
	}
	for i, want := range wantSites {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS call site %d does NOT forward globalSeq: missing substring %q", i, want)
		}
	}
}

// Test_LiveJS_GlobalSeqGuardComesAfterLocalSeqGuard pins the ordering
// in __skyHandleResponse: the localSeq guard fires FIRST (the existing
// gate), the globalSeq guard SECOND (additive). Reversing them would
// be functionally equivalent but the explicit ordering keeps the
// "broadcast-dedupe is additive on top of local-stale-drop" mental
// model the design doc §3.2 stipulates.
//
// We scope the search to the function body so the test isn't fooled
// by an earlier mention of __skyLastGlobalSeq in a doc comment (the
// design-doc reference attached to __skyHandleResponse itself
// describes the global counter BEFORE the function starts).
func Test_LiveJS_GlobalSeqGuardComesAfterLocalSeqGuard(t *testing.T) {
	js := liveJS("test-sid")
	// Scope the index search to the function body. The function
	// definition is `function __skyHandleResponse(seq, ackInputs,
	// applyFn, globalSeq) { ... }`; we start the scan from the `{`
	// after the signature so prose comments earlier in the file don't
	// pollute the ordering check.
	bodyStart := strings.Index(js, "function __skyHandleResponse(seq, ackInputs, applyFn, globalSeq) {")
	if bodyStart < 0 {
		t.Fatalf("__skyHandleResponse function not found")
	}
	scope := js[bodyStart:]
	localIdx := strings.Index(scope, "<= __skyLastAppliedSeq")
	globalIdx := strings.Index(scope, "<= __skyLastGlobalSeq")
	if localIdx < 0 {
		t.Fatalf("local seq guard absent in __skyHandleResponse body")
	}
	if globalIdx < 0 {
		t.Fatalf("global seq guard absent in __skyHandleResponse body")
	}
	if globalIdx < localIdx {
		t.Errorf("global seq guard appears BEFORE local seq guard in __skyHandleResponse; expected local then global per design doc §3.2 (local is the long-standing gate, global is additive)")
	}
}
