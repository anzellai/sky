package rt

import "testing"

// Typing while a hold RPC is in flight (v0.29.0). A hold holds every later Msg,
// the person's keystrokes among them, so for the length of the hold the model
// is behind what the focused box shows. The render that follows the hold's
// answer, and the renders of the held keystrokes as they run one by one, used
// to write the model's older text into the box and clamp the caret: typing
// "harbourside" against a slow search lost and swapped letters, and the caret
// jumped back into the word.
//
// The harness is the real scheduler (spa_rpcqueue.go), the real planner
// (spaPlanRender, diffTrees with the focused-input alignment) and the real
// filter (spaKeepTyped). The box is a fake DOM control that applies a value
// patch the way applyAttrs does in the browser: it writes the value and
// clamps the saved caret to the new length.

type tyModel struct {
	Query string
	Hits  string
}

type tySetQuery struct{ Q string }
type tyRunSearch struct{}
type tyApplied struct{ Hits string }

func (tySetQuery) SkyVariantName() string  { return "SetQuery" }
func (tySetQuery) SkyVariantTag() int      { return 0 }
func (tyRunSearch) SkyVariantName() string { return "RunSearch" }
func (tyRunSearch) SkyVariantTag() int     { return 1 }
func (tyApplied) SkyVariantName() string   { return "AppliedRunSearch" }
func (tyApplied) SkyVariantTag() int       { return 2 }

// tyBox is the focused <input>: its value and caret, as the browser holds them.
type tyBox struct {
	value string
	caret int
}

// typeKey inserts one character at the caret, as a key press does.
func (b *tyBox) typeKey(ch string) {
	b.value = b.value[:b.caret] + ch + b.value[b.caret:]
	b.caret += len(ch)
}

const tyBoxID = "r.0"

type tyApp struct {
	t      *testing.T
	sch    *spaSched
	model  tyModel
	box    tyBox
	prev   *VNode
	job    *spaRpcJob
	writes int // value writes that reached the box
}

func (a *tyApp) view() *VNode {
	vn := VNode{Kind: "element", Tag: "div", SkyID: "r", Children: []VNode{
		{Kind: "element", Tag: "input", SkyID: tyBoxID, Attrs: map[string]string{"value": a.model.Query}},
		{Kind: "element", Tag: "span", SkyID: "r.1", Children: []VNode{vtext(a.model.Hits)}},
	}}
	return &vn
}

// render is renderCurrent without the browser: plan, drop the writes to a
// control whose input Msgs wait, apply.
func (a *tyApp) render() {
	next := a.view()
	focused := map[string]string{tyBoxID: a.box.value}
	plan := spaPlanRender(a.prev, next, focused, spaSelectState{})
	typing := map[string]bool{}
	for _, src := range a.sch.typingSources() {
		typing[src.(string)] = true
	}
	for _, p := range spaKeepTyped(plan.Apply, typing) {
		if p.ID != tyBoxID {
			continue
		}
		if v, ok := p.Attrs["value"]; ok && v != a.box.value {
			a.box.value = v
			a.box.caret = min(a.box.caret, len(v))
			a.writes++
		}
	}
	a.prev = plan.DOM
}

func (a *tyApp) step(msg any) {
	switch m := msg.(type) {
	case tySetQuery:
		a.model.Query = m.Q
	case tyRunSearch:
		// A hold: the write (the hits) comes back in the answer.
		a.job = a.sch.issue(nil, nil, true)
	case tyApplied:
		a.model.Hits = m.Hits
	}
	a.render()
}

func (a *tyApp) key(ch string) {
	a.box.typeKey(ch)
	a.sch.dispatchFrom(tySetQuery{Q: a.box.value}, tyBoxID, a.step)
}

func newTyApp(t *testing.T) *tyApp {
	a := &tyApp{t: t, sch: newSpaSched("t")}
	a.prev = a.view()
	return a
}

func TestSpaTyping_HeldKeystrokesNeverRewindTheFocusedBox(t *testing.T) {
	a := newTyApp(t)
	a.key("c")
	a.sch.dispatch(tyRunSearch{}, a.step)
	if !a.sch.held() {
		t.Fatal("RunSearch did not hold the queue")
	}
	// Three keys while the search is in flight: the box moves ahead, the
	// SetQuery Msgs wait.
	a.key("l")
	a.key("i")
	a.key("n")
	if a.model.Query != "c" {
		t.Fatalf("a held keystroke ran during the hold: model %q", a.model.Query)
	}
	a.sch.settle(a.job, tyApplied{Hits: "3 hits"}, a.step)
	if a.box.value != "clin" || a.box.caret != 4 {
		t.Fatalf("the box was rewound: value %q caret %d (want \"clin\" caret 4)", a.box.value, a.box.caret)
	}
	if a.writes != 0 {
		t.Fatalf("%d value writes reached the box; the model was only catching up with it", a.writes)
	}
	if a.model.Query != a.box.value || a.model.Hits != "3 hits" {
		t.Fatalf("model %+v, box %q: the model must end equal to the box", a.model, a.box.value)
	}
	// The next key lands at the end, where the person's caret is.
	a.key("i")
	if a.box.value != "clini" || a.box.caret != 5 || a.model.Query != "clini" {
		t.Fatalf("next key: box %q caret %d model %q", a.box.value, a.box.caret, a.model.Query)
	}
}

// A real programmatic change still applies once the held Msgs have run: the
// arm that runs last decides, as without a hold.
func TestSpaTyping_AProgrammaticValueStillAppliesAfterTheHeldMsgs(t *testing.T) {
	a := newTyApp(t)
	a.sch.dispatch(tyRunSearch{}, a.step)
	a.key("x")
	a.key("y")
	clear := func(msg any) {
		a.step(msg)
		if m, ok := msg.(tySetQuery); ok && m.Q == "xy" {
			// The arm trims the query: the model now differs from the box.
			a.model.Query = "x"
			a.render()
		}
	}
	a.sch.settle(a.job, tyApplied{Hits: "h"}, clear)
	if a.box.value != "x" || a.box.caret != 1 {
		t.Fatalf("the arm's own value did not apply: box %q caret %d", a.box.value, a.box.caret)
	}
}

// spaKeepTyped follows a rename: a kept subtree whose sky-ids move keeps the
// typed control's value alone under its new id too.
func TestSpaKeepTyped_FollowsARenamedSubtree(t *testing.T) {
	in := []Patch{
		{ID: "r", Kids: []KidOp{{Keep: "r.1", ID: "r.2"}}},
		{ID: "r.2.0", Attrs: map[string]string{"value": "old", "class": "x"}},
		{ID: "r.3", Attrs: map[string]string{"value": "other"}},
		{ID: "r.4", Attrs: map[string]string{"checked": ""}},
	}
	out := spaKeepTyped(in, map[string]bool{"r.1.0": true, "r.4": true})
	if len(out) != 3 {
		t.Fatalf("want 3 patches (the checked-only one dropped), got %d: %+v", len(out), out)
	}
	if _, ok := out[1].Attrs["value"]; ok || out[1].Attrs["class"] != "x" {
		t.Fatalf("renamed control: value must go, class must stay: %+v", out[1].Attrs)
	}
	if out[2].Attrs["value"] != "other" {
		t.Fatalf("another control's value must stay: %+v", out[2])
	}
	if in[1].Attrs["value"] != "old" {
		t.Fatal("spaKeepTyped changed its input")
	}
}

// A hold-free input Msg (Spa.withHoldFree) runs at once during the hold it is
// listed for, and only while nothing ordinary waits ahead of it.
func TestSpaSched_HoldFreeInputRunsDuringItsHold(t *testing.T) {
	a := newTyApp(t)
	a.sch.setHoldFree([]string{"RunSearch SetQuery", "malformed"})
	a.sch.dispatch(tyRunSearch{}, a.step)
	if a.job == nil || a.job.name != "RunSearch" {
		t.Fatalf("the hold must carry its branch name, got %+v", a.job)
	}
	a.key("a")
	if a.model.Query != "a" {
		t.Fatalf("a hold-free keystroke must run during the hold: model %q", a.model.Query)
	}
	// An ordinary Msg now waits; a later keystroke must not overtake it.
	a.sch.dispatch(tyRunSearch{}, a.step)
	a.key("b")
	if a.model.Query != "a" {
		t.Fatalf("a keystroke overtook a waiting Msg: model %q", a.model.Query)
	}
	first := a.job
	a.sch.settle(first, tyApplied{Hits: "h"}, a.step)
	if a.job == first || a.model.Query != "a" {
		t.Fatalf("the waiting RunSearch must hold again before the keystroke: model %q", a.model.Query)
	}
	a.sch.settle(a.job, tyApplied{Hits: "h"}, a.step)
	if a.model.Query != "ab" || a.box.value != "ab" || a.box.caret != 2 {
		t.Fatalf("after the hold: model %+v box %+v", a.model, a.box)
	}
	// Not listed for this hold: waits.
	b := newTyApp(t)
	b.sch.setHoldFree([]string{"OtherHold SetQuery"})
	b.sch.dispatch(tyRunSearch{}, b.step)
	b.key("z")
	if b.model.Query != "" {
		t.Fatalf("a Msg the table does not list for this hold ran during it: %q", b.model.Query)
	}
}

func TestSpaMsgCtor_ReadsEveryShape(t *testing.T) {
	if got := spaMsgCtor(tySetQuery{}); got != "SetQuery" {
		t.Fatalf("sealed variant: %q", got)
	}
	if got := spaMsgCtor(SkyADT{SkyName: "Go"}); got != "Go" {
		t.Fatalf("SkyADT: %q", got)
	}
	if got := spaMsgCtor(42); got != "" {
		t.Fatalf("an int has no constructor: %q", got)
	}
}
