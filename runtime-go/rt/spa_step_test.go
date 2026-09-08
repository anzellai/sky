package rt

import "testing"

// Fix 3 regression — a panic in a click or keystroke must NOT white-screen the
// wasm app. Before the fix the primary event path (step -> renderCurrent, and
// dispatchEvent) had no recover, so a classified panic (DivisionByZero, an
// rt.Coerce panic) from the user's update or view killed the whole Go/wasm
// instance. spaTransition is the portable guard: a panicking update keeps the
// last good model, reports the panic, and leaves the instance usable for the
// next event.
//
// RED before the fix: spaTransition did not exist / had no recover, so the
// panic escaped and crashed the test process.
func TestSpaTransitionUpdatePanicKeepsModelAndSurvives(t *testing.T) {
	const good = "GOOD-MODEL"

	rendered := 0
	posted := 0
	panics := []string{}
	render := func(model any) { rendered++ }
	post := func(cmd any) { posted++ }
	onPanic := func(stage string, r any) { panics = append(panics, stage) }

	panicUpdate := func(msg, model any) SkyTuple2 { panic("10.0 / 0.0: DivisionByZero") }

	// A panicking update must not crash the process, must keep the last good
	// model, must not paint from a half-updated model, and must be reported.
	kept := spaTransition("click", good, panicUpdate, render, post, onPanic)
	if kept != good {
		t.Fatalf("model was mutated by a panicking update: got %v, want %q", kept, good)
	}
	if rendered != 0 {
		t.Fatalf("render ran despite a panicking update (repainted a half-updated model): %d", rendered)
	}
	if posted != 0 {
		t.Fatalf("post (Cmd/subs) ran despite a panicking update: %d", posted)
	}
	if len(panics) != 1 || panics[0] != "update" {
		t.Fatalf("update panic not reported as stage=update: %v", panics)
	}

	// Still usable: a following non-panicking msg applies to the kept model.
	goodUpdate := func(msg, model any) SkyTuple2 {
		return SkyTuple2{V0: "NEXT-MODEL", V1: nil}
	}
	kept2 := spaTransition("next", kept, goodUpdate, render, post, onPanic)
	if kept2 != "NEXT-MODEL" {
		t.Fatalf("instance not usable after a panic: following msg did not apply, got %v", kept2)
	}
	if rendered != 1 || posted != 1 {
		t.Fatalf("following non-panicking msg did not render+post: rendered=%d posted=%d", rendered, posted)
	}
}

// A panic in view (after a successful update) must also keep the model/screen
// consistent: the paint never committed, so the model rolls back to the last
// good value rather than diverging from what is on screen.
func TestSpaTransitionViewPanicRollsBackModel(t *testing.T) {
	const good = "GOOD-MODEL"
	panics := []string{}
	onPanic := func(stage string, r any) { panics = append(panics, stage) }

	goodUpdate := func(msg, model any) SkyTuple2 { return SkyTuple2{V0: "NEW-MODEL", V1: nil} }
	panicRender := func(model any) { panic("rt.Coerce: cannot narrow") }
	post := func(cmd any) { t.Fatalf("post ran after a view panic") }

	kept := spaTransition("click", good, goodUpdate, panicRender, post, onPanic)
	if kept != good {
		t.Fatalf("view panic left a half-updated model: got %v, want %q", kept, good)
	}
	if len(panics) != 1 || panics[0] != "view" {
		t.Fatalf("view panic not reported as stage=view: %v", panics)
	}
}

// A panic in post (Cmd interpretation / subscription reconciliation) happens
// AFTER the paint has committed, so the new model is kept (the screen already
// shows it) and the panic is still reported.
func TestSpaTransitionPostPanicKeepsCommittedModel(t *testing.T) {
	panics := []string{}
	onPanic := func(stage string, r any) { panics = append(panics, stage) }

	goodUpdate := func(msg, model any) SkyTuple2 { return SkyTuple2{V0: "NEW-MODEL", V1: nil} }
	render := func(model any) {}
	panicPost := func(cmd any) { panic("cmd interpretation blew up") }

	kept := spaTransition("click", "OLD", goodUpdate, render, panicPost, onPanic)
	if kept != "NEW-MODEL" {
		t.Fatalf("committed model rolled back after a post panic: got %v", kept)
	}
	if len(panics) != 1 || panics[0] != "cmd" {
		t.Fatalf("post panic not reported as stage=cmd: %v", panics)
	}
}
