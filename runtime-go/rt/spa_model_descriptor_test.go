package rt

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

type spaProjectionSession struct {
	ID string `sky:"id,string"`
}

type spaProjectionModel struct {
	Session SkyMaybe[spaProjectionSession] `sky:"session,rt.SkyMaybe[spaProjectionSession]"`
	Data    []string                       `sky:"data,[]string"`
	Text    string                         `sky:"text,string"`
}

func spaProjectionEncode(v any) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	return AsString(Spa_modelToJson(v))
}

func TestSpaAutoProjectionMatchesFullCodecSelectedFields(t *testing.T) {
	for _, session := range []SkyMaybe[spaProjectionSession]{Nothing[spaProjectionSession](), Just(spaProjectionSession{ID: "fixture"})} {
		m := spaProjectionModel{Session: session, Data: []string{"unrelated"}}
		full := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(spaProjectionEncode(m)), &full); err != nil {
			t.Fatal(err)
		}
		projected, ok, visited := spaAutoSessionProjection(m, []string{"session", "missing"})
		if !ok || visited != 1 {
			t.Fatalf("projection failed or visited unrelated fields: valid=%v fields=%d", ok, visited)
		}
		obj := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(projected), &obj); err != nil {
			t.Fatal(err)
		}
		if len(obj) != 1 || string(obj["session"]) != string(full["session"]) {
			t.Fatal("projection changed protected-field codec semantics")
		}
	}
}

func TestSpaAutoProjectionAvoidsUnrelatedPreviousData(t *testing.T) {
	for _, size := range []int{1000, 10000} {
		prev := spaProjectionModel{Session: Nothing[spaProjectionSession](), Data: make([]string, size)}
		for i := range prev.Data {
			prev.Data[i] = strings.Repeat("x", 60)
		}
		next := prev
		next.Text = "changed"
		var metrics spaPersistMetrics
		calls, writes := 0, 0
		cleared := spaPersistStepWithCodec(prev, next, func(v any) string {
			calls++
			return spaProjectionEncode(v)
		}, func(string) { writes++ }, []string{"session"}, true, func(m spaPersistMetrics) { metrics = m })
		if cleared || calls != 1 || writes != 1 || metrics.ProjectionFields != 2 {
			t.Fatalf("unexpected work for %d unrelated entries: clear=%v calls=%d writes=%d fields=%d", size, cleared, calls, writes, metrics.ProjectionFields)
		}
		if metrics.ProjectionBytes != len(`{"session":null}`)*2 {
			t.Fatal("projection bytes grew with unrelated model data")
		}
	}
}

func TestSpaAutoProjectionPreservesFullEncodeFailureDecisions(t *testing.T) {
	type opaqueModel struct {
		Session SkyMaybe[spaProjectionSession] `sky:"session,rt.SkyMaybe[spaProjectionSession]"`
		Opaque  any                            `sky:"opaque,any"`
	}
	signed := opaqueModel{Session: Just(spaProjectionSession{ID: "fixture"}), Opaque: 1}
	anon := signed
	anon.Session = Nothing[spaProjectionSession]()
	badOld := signed
	badOld.Opaque = func() {}
	badNext := anon
	badNext.Opaque = func() {}
	for _, tc := range []struct {
		name string
		prev opaqueModel
		next opaqueModel
	}{
		{"real sign-out", signed, anon},
		{"invalid previous unrelated field", badOld, anon},
		{"invalid next unrelated field", signed, badNext},
		{"invalid previous but no clear", badOld, signed},
		{"unchanged", signed, signed},
		{"sign-in", anon, signed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a, b []string
			ref := spaPersistStep(tc.prev, tc.next, spaProjectionEncode, func(s string) { a = append(a, s) }, []string{"session"}, nil)
			got := spaPersistStepWithCodec(tc.prev, tc.next, spaProjectionEncode, func(s string) { b = append(b, s) }, []string{"session"}, true, nil)
			if ref != got || !reflect.DeepEqual(a, b) {
				t.Fatal("optimisation changed sign-out or persistence on an encoding failure")
			}
		})
	}
}

func TestSpaAutoProjectionPossibleClearKeepsInvalidPreviousFullEncode(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Reading float64          `sky:"reading,float64"`
	}
	old := model{Session: Just("synthetic"), Reading: math.NaN()}
	next := model{Session: Nothing[string](), Reading: 0}
	if !spaAutoDescriptor(reflect.TypeOf(old)).valid {
		t.Fatal("fixture must reach the eligible projection path")
	}
	calls := 0
	cleared := spaPersistStepWithCodec(old, next, func(v any) string { calls++; return spaProjectionEncode(v) }, func(string) {}, []string{"session"}, true, nil)
	if cleared || calls != 2 {
		t.Fatal("possible clear bypassed the invalid previous full encoder")
	}
}

func TestSpaGeneratedEncoderSetterCannotGrantArbitraryCallable(t *testing.T) {
	fn := func(any) any { return "{}" }
	base := Spa_config(map[string]any{})
	forged := Spa_withGeneratedModelEncoder(fn, base)
	if fieldOrNil(forged, "GeneratedAutoModelEncoder") != false {
		t.Fatal("an ordinary callable received generated-codec eligibility")
	}
	proven := Spa_withGeneratedModelEncoder(SpaAutoModelEncoder{Full: fn}, base)
	if fieldOrNil(proven, "GeneratedAutoModelEncoder") != true {
		t.Fatal("lowerer capsule was not retained")
	}
	custom := Spa_withModelEncoder(fn, proven)
	if fieldOrNil(custom, "GeneratedAutoModelEncoder") != false {
		t.Fatal("custom encoder did not revoke prior eligibility")
	}
	if fieldOrNil(proven, "GeneratedAutoModelEncoder") != true {
		t.Fatal("builder mutated an earlier configuration")
	}
}

func TestSpaAutoProjectionRejectsUnknownAndDuplicateRepresentations(t *testing.T) {
	duplicate := struct {
		First  string `sky:"aB,string"`
		Second string `sky:"a_b,string"`
	}{}
	for _, v := range []any{nil, map[string]any{"session": nil}, duplicate, JsonValue{raw: map[string]any{"session": nil}}} {
		if _, ok, _ := spaAutoSessionProjection(v, []string{"session"}); ok {
			t.Fatal("unknown or duplicate-key representation received a projection")
		}
	}
}

type spaForeignProjectionJSON struct{ calls *int }

func (v spaForeignProjectionJSON) MarshalJSON() ([]byte, error) {
	*v.calls++
	return []byte(`"synthetic"`), nil
}

func TestSpaAutoProjectionDoesNotAddForeignMarshalEffects(t *testing.T) {
	type session struct {
		Payload JsonValue `sky:"payload,rt.JsonValue"`
	}
	type model struct {
		Session session `sky:"session,session"`
	}
	for _, generated := range []bool{false, true} {
		calls := 0
		m := model{Session: session{Payload: JsonValue{raw: spaForeignProjectionJSON{calls: &calls}}}}
		if spaPersistStepWithCodec(m, m, spaProjectionEncode, func(string) {}, []string{"session"}, generated, nil) {
			t.Fatal("unchanged foreign session cleared")
		}
		if calls != 2 {
			t.Fatalf("projection changed observable foreign marshal count: generated=%v calls=%d", generated, calls)
		}
	}
}

func TestSpaAutoProjectionDoesNotDropUnrelatedForeignMarshalEffects(t *testing.T) {
	type model struct {
		Session   SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Unrelated JsonValue        `sky:"unrelated,rt.JsonValue"`
	}
	for _, generated := range []bool{false, true} {
		calls := 0
		m := model{Session: Nothing[string](), Unrelated: JsonValue{raw: spaForeignProjectionJSON{calls: &calls}}}
		spaPersistStepWithCodec(m, m, spaProjectionEncode, func(string) {}, []string{"session"}, generated, nil)
		if calls != 2 {
			t.Fatalf("unrelated foreign effects dropped: generated=%v calls=%d", generated, calls)
		}
	}
}

func TestSpaAutoProjectionRejectsOpenAndRecursiveTypeDomains(t *testing.T) {
	type node struct {
		Children []node `sky:"children,[]node"`
	}
	open := struct {
		Session any `sky:"session,any"`
	}{0}
	recursive := struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Tree    node             `sky:"tree,node"`
	}{Session: Nothing[string]()}
	for _, model := range []any{open, recursive} {
		if spaAutoDescriptor(reflect.TypeOf(model)).valid {
			t.Fatal("an open or cyclic type graph was considered pure")
		}
	}
	// Shared types in an acyclic graph are not recursive back-edges.
	closed := struct {
		First  spaProjectionSession `sky:"first,spaProjectionSession"`
		Second spaProjectionSession `sky:"second,spaProjectionSession"`
	}{}
	if !spaAutoDescriptor(reflect.TypeOf(closed)).valid {
		t.Fatal("shared acyclic child type was rejected")
	}
}

type spaForeignProjectionVariant struct{ calls *int }

func (v spaForeignProjectionVariant) SkyVariantTag() int     { *v.calls++; return 0 }
func (v spaForeignProjectionVariant) SkyVariantName() string { *v.calls++; return "Synthetic" }

func TestSpaAutoProjectionRejectsForeignVariantWithoutCallingIt(t *testing.T) {
	calls := 0
	m := struct {
		Session any `sky:"session,any"`
	}{spaForeignProjectionVariant{calls: &calls}}
	if _, ok, _ := spaAutoSessionProjection(m, []string{"session"}); ok || calls != 0 {
		t.Fatal("projection invoked a foreign method while deciding eligibility")
	}
}

func TestSpaAutoProjectionDepthAndBudgetFallBack(t *testing.T) {
	var deep any = "leaf"
	for i := 0; i < 80; i++ {
		deep = []any{deep}
	}
	budget := 4096
	if spaProjectionClosed(reflect.ValueOf(deep), 0, &budget) {
		t.Fatal("deep graph bypassed the value guard")
	}
	large := struct {
		Session []string `sky:"session,[]string"`
	}{make([]string, 5000)}
	if _, ok, _ := spaAutoSessionProjection(large, []string{"session"}); ok {
		t.Fatal("large session bypassed the node budget")
	}
}

func TestSpaAutoProjectionUsesFinalExactKeysAndDeclaredEnums(t *testing.T) {
	RegisterEnum("SkyProjectionRole", []string{"Reader", "Writer"})
	type model struct {
		Session     SkyMaybe[int]    `sky:"session,rt.SkyMaybe[SkyProjectionRole]"`
		UserSession SkyMaybe[string] `sky:"userSession,rt.SkyMaybe[string]"`
	}
	before := model{Session: Just(0), UserSession: Just("synthetic")}
	after := model{Session: Nothing[int](), UserSession: Nothing[string]()}
	for _, fields := range [][]string{{"session"}, {"userSession"}, {"user_session"}, {"missing"}, {"session", "user_session"}} {
		want := spaSessionClearedByStep(spaProjectionEncode(before), spaProjectionEncode(after), fields)
		got := spaPersistStepWithCodec(before, after, spaProjectionEncode, func(string) {}, fields, true, nil)
		if got != want {
			t.Fatal("final protected-field list or exact JSON-key semantics changed")
		}
	}
	projected, ok, _ := spaAutoSessionProjection(before, []string{"session"})
	if !ok || projected != `{"session":"Reader"}` {
		t.Fatal("projection lost the declared enum codec")
	}
}
