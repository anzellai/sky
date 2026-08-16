package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

// Two DISTINCT ADTs that share a constructor name.
//
// This is not a contrived shape — it is what every shipped binary
// already contains. `runtime-go/rt/console_app/main.go` registers 55
// `Std_Ui_*` constructor names (`Text`, `Node`, `Empty`, `Raw`, `Min`,
// `Max`, `Fill`, …) that any user `Msg` is free to reuse, and
// `AlignLeft` exists in BOTH `Std.Ui.HAlign` and `Std.Css.TextAlign`
// (documented at rust/crates/lower/src/lower.rs:412-416).
//
// Codegen emits the IDENTICAL method set for every sealed ADT
// (`SkyVariantTag`/`SkyVariantName`, no unexported sealing method —
// rust/crates/codegen/src/lib.rs:186-212), so Go's type system cannot
// reject a cross-ADT substitution. See
// TestSealedIfacesAreStructurallyInterchangeable below.

// ADT keys are PACKAGE-QUALIFIED — Go's own `reflect.Type.String()` form.
// The bare Go type name is not unique across a process: `rt/console_app`
// is a second package in every binary, compiled from
// `sky-bundled/console/src/State.sky`, so its Msg is `State_Msg` — the
// same Go type name a user app whose own module is `State.sky` gets.
// Examples 12-skyvote, 13-skyshop and 16-skychess all do exactly that.
// See TestSamelyNamedAdtsInDifferentPackagesDoNotCollide.
const (
	nsAppMsgAdt = "main.nsTest_AppMsg"
	nsWidgetAdt = "main.nsTest_Widget"
)

type nsTest_AppMsg interface {
	SkyVariantTag() int
	SkyVariantName() string
}

type nsTest_AppMsg_Submit_V struct{ V0 string }

func (nsTest_AppMsg_Submit_V) SkyVariantTag() int     { return 0 }
func (nsTest_AppMsg_Submit_V) SkyVariantName() string { return "Submit" }

type nsTest_Widget interface {
	SkyVariantTag() int
	SkyVariantName() string
}

type nsTest_Widget_Submit_V struct{ V0 int }

func (nsTest_Widget_Submit_V) SkyVariantTag() int     { return 0 }
func (nsTest_Widget_Submit_V) SkyVariantName() string { return "Submit" }

type nsTest_Widget_Repaint_V struct{}

func (nsTest_Widget_Repaint_V) SkyVariantTag() int     { return 1 }
func (nsTest_Widget_Repaint_V) SkyVariantName() string { return "Repaint" }

func appMsgSubmitFactory(raw []json.RawMessage) any {
	var v0 string
	if len(raw) >= 1 {
		_ = json.Unmarshal(raw[0], &v0)
	}
	return nsTest_AppMsg_Submit_V{V0: v0}
}

func widgetSubmitFactory(raw []json.RawMessage) any {
	var v0 int
	if len(raw) >= 1 {
		_ = json.Unmarshal(raw[0], &v0)
	}
	return nsTest_Widget_Submit_V{V0: v0}
}

// Mirrors what codegen's init() blocks now emit, in the order a linker
// happens to choose. Registering the FOREIGN ADT first is deliberate:
// under the old bare-name registries that made the foreign ctor win.
func registerNsFixtures() {
	RegisterAdtVariant(nsWidgetAdt, "Submit", widgetSubmitFactory)
	RegisterAdtTag(nsWidgetAdt, "Submit", 0)
	RegisterAdtVariant(nsWidgetAdt, "Repaint", func(raw []json.RawMessage) any {
		return nsTest_Widget_Repaint_V{}
	})
	RegisterAdtTag(nsWidgetAdt, "Repaint", 1)

	RegisterAdtVariant(nsAppMsgAdt, "Submit", appMsgSubmitFactory)
	RegisterAdtTag(nsAppMsgAdt, "Submit", 0)
}

// THE SECURITY PROPERTY: a client-supplied wire string resolves a
// constructor ONLY within the ADT the dispatch site expects. It never
// reaches a constructor belonging to some other ADT that happens to
// share the name.
//
// `req.Msg` (runtime-go/rt/live.go) is attacker-controlled and flows
// into BuildAdtFromWire at the two __sky_send dispatch sites.
func TestWireCannotSelectCtorFromUnaskedAdt(t *testing.T) {
	registerNsFixtures()

	// A wire event arrives for a session whose app Msg type is
	// nsTest_AppMsg. The handler asked for nsTest_AppMsg — nothing else.
	argJSON, _ := json.Marshal("hello")
	got, ok := BuildAdtFromWire(nsAppMsgAdt, "Submit", []json.RawMessage{argJSON}, -1)
	if !ok {
		t.Fatal("expected the app's own Submit ctor to resolve")
	}
	v, isApp := got.(nsTest_AppMsg_Submit_V)
	if !isApp {
		t.Fatalf(
			"wire string selected a constructor from an ADT the handler did not ask for:\n"+
				"  got  %T\n"+
				"  want rt.nsTest_AppMsg_Submit_V",
			got)
	}
	if v.V0 != "hello" {
		t.Fatalf("payload decoded against the wrong variant: %+v", v)
	}

	// And the same name, asked for by the OTHER ADT's dispatch site,
	// resolves to that ADT's ctor — with its own payload type.
	numJSON, _ := json.Marshal(7)
	gotW, ok := BuildAdtFromWire(nsWidgetAdt, "Submit", []json.RawMessage{numJSON}, -1)
	if !ok {
		t.Fatal("expected the widget's Submit ctor to resolve")
	}
	if w, isW := gotW.(nsTest_Widget_Submit_V); !isW || w.V0 != 7 {
		t.Fatalf("got %T (%+v), want rt.nsTest_Widget_Submit_V{V0:7}", gotW, gotW)
	}
}

// Registration order must not decide which constructor a wire string
// resolves to. The fixtures register the foreign ADT first; re-running
// them in the opposite order must not move either answer.
func TestRegistrationOrderCannotChangeWireResolution(t *testing.T) {
	registerNsFixtures()
	// Re-register in the reverse order. Under bare-name last-write-wins
	// this flipped the answer; keyed by (ADT, ctor) it cannot.
	RegisterAdtVariant(nsAppMsgAdt, "Submit", appMsgSubmitFactory)
	RegisterAdtVariant(nsWidgetAdt, "Submit", widgetSubmitFactory)

	argJSON, _ := json.Marshal("hello")
	got, ok := BuildAdtFromWire(nsAppMsgAdt, "Submit", []json.RawMessage{argJSON}, -1)
	if !ok {
		t.Fatal("expected Submit to resolve")
	}
	if _, isApp := got.(nsTest_AppMsg_Submit_V); !isApp {
		t.Fatalf(
			"registration order changed which ADT a wire string resolves to:\n"+
				"  got  %T\n"+
				"  want rt.nsTest_AppMsg_Submit_V",
			got)
	}
}

// A constructor that exists ONLY on some other ADT must be unreachable
// from a dispatch site expecting a different ADT. `Repaint` belongs to
// nsTest_Widget alone; an app whose Msg is nsTest_AppMsg must never be
// handed one.
func TestWireCannotReachAForeignOnlyCtor(t *testing.T) {
	registerNsFixtures()

	got, ok := BuildAdtFromWire(nsAppMsgAdt, "Repaint", nil, -1)
	if ok {
		t.Fatalf(
			"a wire string reached a constructor belonging to an ADT the handler "+
				"never asked for: got %T for Msg %q", got, "Repaint")
	}
}

// When the dispatch site cannot name its expected ADT, NEITHER global
// registry may be consulted — an unknown scope must not degrade to a
// program-wide search, because a program-wide search is the defect.
// Only the per-app localTag (populated exclusively from messages this
// app already dispatched) remains.
func TestUnknownExpectedAdtConsultsNoGlobalRegistry(t *testing.T) {
	registerNsFixtures()

	if got, ok := BuildAdtFromWire("", "Submit", nil, -1); ok {
		t.Fatalf("empty expected-ADT still resolved a global ctor: %T", got)
	}
	if got, ok := BuildAdtFromWire("", "Repaint", nil, -1); ok {
		t.Fatalf("empty expected-ADT still resolved a global ctor: %T", got)
	}
	// localTag still works — it cannot name a foreign ADT's ctor.
	got, ok := BuildAdtFromWire("", "Submit", nil, 4)
	if !ok {
		t.Fatal("expected the per-app localTag fallback to still resolve")
	}
	if adt, isAdt := got.(SkyADT); !isAdt || adt.Tag != 4 {
		t.Fatalf("got %T (%+v), want SkyADT{Tag:4}", got, got)
	}
}

// The real collision the example sweep surfaced: the bundled console and
// a user app BOTH have a Go type `State_Msg`, in different Go packages,
// both with a ctor `Tick` — at different tags (console 1, 12-skyvote 19).
// Keyed on the unqualified type name those share one slot and init()
// order decides the winner; package-qualified they are simply different
// ADTs and each resolves to its own tag.
func TestSamelyNamedAdtsInDifferentPackagesDoNotCollide(t *testing.T) {
	const userAdt = "main.State_Msg"
	const consoleAdt = "console_app.State_Msg"

	RegisterAdtTag(consoleAdt, "Tick", 1)
	RegisterAdtTag(userAdt, "Tick", 19)

	if got, ok := LookupAdtTag(userAdt, "Tick"); !ok || got != 19 {
		t.Fatalf("user app's Tick = (%d, %v), want (19, true)", got, ok)
	}
	if got, ok := LookupAdtTag(consoleAdt, "Tick"); !ok || got != 1 {
		t.Fatalf("console's Tick = (%d, %v), want (1, true)", got, ok)
	}

	// And the wire cannot cross between them.
	got, ok := BuildAdtFromWire(userAdt, "Tick", nil, -1)
	if !ok {
		t.Fatal("expected the user app's own Tick to resolve")
	}
	if adt, isAdt := got.(SkyADT); !isAdt || adt.Tag != 19 {
		t.Fatalf("wire resolved the console's Tick for a user-app dispatch: %+v", got)
	}
}

// Two distinct Sky types lowering to one Go name would make init() order
// decide how the wire decodes a constructor. That must be a loud,
// deterministic failure, never a silent winner.
func TestConflictingTagRegistrationPanics(t *testing.T) {
	const adt = "main.nsTest_ConflictAdt"
	RegisterAdtTag(adt, "Dup", 3)
	RegisterAdtTag(adt, "Dup", 3) // same tag: idempotent, must not panic

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("re-registering a ctor with a DIFFERENT tag did not panic; " +
				"init() order would silently decide the winner")
		}
		if !strings.Contains(r.(string), "conflicting ADT tag registration") {
			t.Fatalf("unexpected panic message: %v", r)
		}
	}()
	RegisterAdtTag(adt, "Dup", 9)
}

// Severity documentation, and it is checked rather than asserted in a
// comment: a variant struct from ANY sealed ADT satisfies EVERY sealed
// ADT's interface, because codegen gives them all the same method set
// and no unexported sealing method. Go therefore cannot reject the
// substitution the tests above are guarding, and neither can `go vet`.
//
// The same structural hazard was found and fixed on the COMPILE-TIME
// side (rust/crates/ty/src/nominal.rs:1-20, pinned at
// corpus/repro/cross-module-union-conflation/). The wire path has no
// type checker in front of it — the string comes from the client.
func TestSealedIfacesAreStructurallyInterchangeable(t *testing.T) {
	// Compiles, and must: this assignment is the whole problem.
	var asAppMsg nsTest_AppMsg = nsTest_Widget_Submit_V{V0: 7}
	if asAppMsg.SkyVariantName() != "Submit" {
		t.Fatalf("unexpected name %q", asAppMsg.SkyVariantName())
	}
	// And the tag a user's `case` arm compares against (rt.go, EnumTagIs
	// → sv.SkyVariantTag()) is a bare integer, so a wrong-ADT variant
	// whose tag collides selects a wrong arm SILENTLY rather than
	// panicking. That is why the scoping above is a security property
	// and not a robustness nicety.
	if !EnumTagIs(nsTest_Widget_Submit_V{V0: 7}, 0) {
		t.Fatal("expected tag-0 match")
	}
	if !EnumTagIs(nsTest_AppMsg_Submit_V{V0: "x"}, 0) {
		t.Fatal("expected tag-0 match")
	}
}

// The expected ADT is read off the app's own `update` signature, which
// codegen emits strongly typed. These are the shapes that must map to
// "unknown" rather than to a wrong or program-wide scope.
func TestMsgAdtFromUpdateSignature(t *testing.T) {
	type Main_Model_R struct{ N int }

	// Typed sealed-iface Msg param — the normal emitted shape.
	typed := func(m nsTest_AppMsg, model Main_Model_R) Main_Model_R { return model }
	if got := msgAdtFromUpdate(typed); got != "rt.nsTest_AppMsg" {
		t.Fatalf("msgAdtFromUpdate(typed) = %q, want %q", got, "rt.nsTest_AppMsg")
	}

	// `type Main_Msg = rt.SkyADT` is a Go ALIAS; reflect erases it and
	// would report "SkyADT", which names the runtime's untyped bag and
	// would collide across every app. Must be "unknown".
	aliased := func(m SkyADT, model Main_Model_R) Main_Model_R { return model }
	if got := msgAdtFromUpdate(aliased); got != "" {
		t.Fatalf("msgAdtFromUpdate(SkyADT-aliased) = %q, want \"\"", got)
	}

	// An `any` first param (reflect.MakeFunc-adapted update) is unknown.
	anyParam := func(m any, model Main_Model_R) Main_Model_R { return model }
	if got := msgAdtFromUpdate(anyParam); got != "" {
		t.Fatalf("msgAdtFromUpdate(any-param) = %q, want \"\"", got)
	}

	if got := msgAdtFromUpdate(nil); got != "" {
		t.Fatalf("msgAdtFromUpdate(nil) = %q, want \"\"", got)
	}
	if got := msgAdtFromUpdate("not a function"); got != "" {
		t.Fatalf("msgAdtFromUpdate(non-func) = %q, want \"\"", got)
	}
}
