package rt

// Cycle 3 P50a / Gap C11 — SSE producer ships structural patches
// (event:patches) when diffTrees produces a small delta, falling
// back to the legacy full-body envelope (event:patch) for first-
// render and full-replace shapes.
//
// Pins three invariants:
//
//   (a) Wire-format — encodePatchesEventFromSnapshot emits
//       {seq, ackInputs?, patches} matching writeEventJSON's HTTP
//       shape (so __skyApplyPatches consumes both routes
//       uniformly).
//   (b) chooseSSEFrame routes by diff result — small non-full-
//       replace → event:patches; nil prev / full-replace → legacy
//       event:patch.
//   (c) runPerformBody end-to-end — a view-changing perform with a
//       tiny delta enqueues an event:patches frame on sess.sseCh,
//       not the legacy full-body event:patch.

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── (a) wire-format equivalence to writeEventJSON ──────────────

func Test_EncodePatchesEvent_WireShapeMatchesHttpReply(t *testing.T) {
	// Construct a snapshot with seq + ackInputs identical to what
	// writeEventJSON produces, then assert the envelope keys.
	snap := frameSnapshot{
		seq:       42,
		body:      "<unused>",
		ackInputs: map[string]int64{"a": 1, "b": 2},
	}
	patches := []Patch{
		{ID: "r/0", Text: stringPtr("hello")},
	}
	got := encodePatchesEventFromSnapshot(snap, patches)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("envelope not valid JSON: %v\n%s", err, got)
	}
	if int64(parsed["seq"].(float64)) != 42 {
		t.Fatalf("seq mismatch: got %v want 42 (%s)", parsed["seq"], got)
	}
	// ackInputs present + correct shape.
	ack, ok := parsed["ackInputs"].(map[string]any)
	if !ok {
		t.Fatalf("ackInputs missing or wrong type: %s", got)
	}
	if int64(ack["a"].(float64)) != 1 || int64(ack["b"].(float64)) != 2 {
		t.Fatalf("ackInputs values mismatch: %v (%s)", ack, got)
	}
	// patches is a non-empty array; first entry shape matches the
	// HTTP reply's Patch struct.
	ps, ok := parsed["patches"].([]any)
	if !ok || len(ps) != 1 {
		t.Fatalf("patches missing or wrong length: %v (%s)", parsed["patches"], got)
	}
	p0 := ps[0].(map[string]any)
	if p0["id"] != "r/0" {
		t.Fatalf("patch id mismatch: %v (%s)", p0, got)
	}
	if p0["text"] != "hello" {
		t.Fatalf("patch text mismatch: %v (%s)", p0, got)
	}
}

func Test_EncodePatchesEvent_EmptyPatchesIsNotOmitted(t *testing.T) {
	snap := frameSnapshot{seq: 7}
	got := encodePatchesEventFromSnapshot(snap, []Patch{})
	if !strings.Contains(got, `"patches":[]`) {
		t.Fatalf("empty patches must serialise as []: %s", got)
	}
	// Also nil-patches treated as empty: producer never ships an
	// envelope with literal `null` for patches.
	got2 := encodePatchesEventFromSnapshot(snap, nil)
	if !strings.Contains(got2, `"patches":[]`) {
		t.Fatalf("nil patches must serialise as []: %s", got2)
	}
}

// ─── (b) chooseSSEFrame routing ────────────────────────────────

func Test_ChooseSSEFrame_PatchesForSmallDelta(t *testing.T) {
	// Non-nil prevTree + small non-full-replace patches → event:patches.
	snap := frameSnapshot{seq: 1, body: "<div>full body</div>"}
	prev := velement("div", nil, []any{vtext("old")})
	patches := []Patch{{ID: "r/0", Text: stringPtr("new")}}
	fr := chooseSSEFrame(snap, &prev, patches)
	if fr.event != "patches" {
		t.Fatalf("small delta must produce event:patches, got %q", fr.event)
	}
	// Data carries the structural envelope, NOT the body.
	if strings.Contains(fr.data, "<div>full body</div>") {
		t.Fatalf("event:patches data must not embed full body: %s", fr.data)
	}
	if !strings.Contains(fr.data, `"id":"r/0"`) {
		t.Fatalf("event:patches data missing structural payload: %s", fr.data)
	}
}

func Test_ChooseSSEFrame_FullBodyWhenPrevNil(t *testing.T) {
	// First render — no prev tree to diff against.
	snap := frameSnapshot{seq: 1, body: "<div>full body</div>"}
	fr := chooseSSEFrame(snap, nil, nil)
	if fr.event != "patch" {
		t.Fatalf("first render must fall back to event:patch, got %q", fr.event)
	}
	// JSON marshalling escapes `<` as `<` for HTML-safety, so
	// substring-match the body via its parsed form rather than the
	// raw HTML.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(fr.data), &parsed); err != nil {
		t.Fatalf("event:patch data is not valid JSON: %v\n%s", err, fr.data)
	}
	if parsed["body"] != "<div>full body</div>" {
		t.Fatalf("event:patch data must carry full body: %v", parsed["body"])
	}
}

func Test_ChooseSSEFrame_FullBodyWhenPatchesNil(t *testing.T) {
	// prev present but diff wasn't run (patches==nil signals "no
	// diff available", distinct from len(patches)==0).
	snap := frameSnapshot{seq: 1, body: "<div>full body</div>"}
	prev := velement("div", nil, nil)
	fr := chooseSSEFrame(snap, &prev, nil)
	if fr.event != "patch" {
		t.Fatalf("nil patches must fall back to event:patch, got %q", fr.event)
	}
}

func Test_ChooseSSEFrame_FullBodyWhenDegenerateFullReplace(t *testing.T) {
	// Diff degenerated to a single root-level HTML replace —
	// patchesAreFullReplace returns true; chooseSSEFrame routes to
	// the legacy event:patch transport.
	html := "<div>full body</div>"
	snap := frameSnapshot{seq: 1, body: html}
	prev := velement("div", nil, nil)
	fullReplacePatches := []Patch{{ID: "r", HTML: &html}}
	if !patchesAreFullReplace(fullReplacePatches) {
		t.Fatalf("test fixture bug: patchesAreFullReplace must accept root-id HTML patch")
	}
	fr := chooseSSEFrame(snap, &prev, fullReplacePatches)
	if fr.event != "patch" {
		t.Fatalf("full-replace patches must fall back to event:patch, got %q", fr.event)
	}
}

// ─── (c) runPerformBody end-to-end — small delta ships event:patches

func Test_RunPerformBody_SmallDelta_ShipsEventPatches(t *testing.T) {
	// View toggles between two structurally-tiny shapes — a single
	// text-node change. dispatch's first call (bootstrap) renders
	// "first"; runPerformBody's call advances to "second". The diff
	// between the two trees is a single text patch on one sky-id, NOT
	// a full root replace.
	callCount := 0
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			callCount++
			if callCount == 1 {
				return velement("div", nil, []any{vtext("first")})
			}
			return velement("div", nil, []any{vtext("second")})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 16),
		model:     "initial",
	}
	// Bootstrap: dispatch once so prevTree + lastShippedBody are
	// populated — without this the diff would see prev==nil and fall
	// back to event:patch.
	body := app.dispatch(sess, "bootstrap")
	sess.lastShippedBody = body

	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)

	select {
	case fr := <-sess.sseCh:
		if fr.event != "patches" {
			t.Fatalf("small-delta render must ship event:patches, got %q (data=%s)", fr.event, fr.data)
		}
		// Sanity-check the envelope shape — must contain a non-
		// empty patches array and NOT the full body.
		if !strings.Contains(fr.data, `"patches":[`) {
			t.Fatalf("event:patches data must contain patches array: %s", fr.data)
		}
		if strings.Contains(fr.data, `"patches":[]`) {
			t.Fatalf("view-changing render produced empty patches: %s", fr.data)
		}
	default:
		t.Fatalf("runPerformBody did not enqueue an SSE frame")
	}
}

// ─── (c.2) runPerformBody with NO prev tree falls back to event:patch

func Test_RunPerformBody_NoPrevTree_FallsBackToFullBody(t *testing.T) {
	// Session never had a baseline dispatch — sess.prevTree is nil
	// when runPerformBody captures it. Producer must fall back to
	// event:patch (legacy full body).
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("only-render")})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 16),
		model:     "initial",
		// NO bootstrap dispatch → prevTree stays nil at the moment
		// runPerformBody captures it.
	}

	task := func() any { return 0 }
	toMsg := func(r any) any { return r }
	app.runPerformBody(sess, task, toMsg)

	select {
	case fr := <-sess.sseCh:
		if fr.event != "patch" {
			t.Fatalf("first-render perform must fall back to event:patch, got %q", fr.event)
		}
		// data must carry the body envelope (legacy shape).
		if !strings.Contains(fr.data, "only-render") {
			t.Fatalf("event:patch data must carry the rendered body: %s", fr.data)
		}
	default:
		t.Fatalf("runPerformBody did not enqueue an SSE frame")
	}
}

// stringPtr is a tiny helper for constructing test Patches with
// optional fields (Text / HTML are *string). Lives here rather than
// shared because the production code never needs a pointer-to-string
// constructor (Patch shipping sites build literals).
func stringPtr(s string) *string { return &s }
