package rt

import (
	"reflect"
	"testing"
)

type spaInputNamed struct {
	Name string `sky:"name,string"`
}
type spaInputAlias = spaInputNamed
type spaInputOther struct {
	Name string `sky:"name,string"`
}
type spaInputTagOther struct {
	Name string `sky:"other,string"`
}
type spaInputImplemented interface{ InputMarker() }
type spaInputImplementation struct{}

func (spaInputImplementation) InputMarker() {}

func TestSpaModelInputTypeExactlyMatchesSuccessfulAssertionDomain(t *testing.T) {
	named := SpaModelInputType[spaInputNamed]()
	tests := []struct {
		token, value any
		want         bool
	}{
		{named, spaInputNamed{}, true},
		{named, spaInputAlias{}, true},
		{named, spaInputOther{}, false},
		{named, spaInputTagOther{}, false},
		{named, map[string]any{"name": "synthetic"}, false},
		{named, nil, false},
		{nil, spaInputNamed{}, false},
		{"not a type token", spaInputNamed{}, false},
		{SpaModelInputType[any](), spaInputNamed{}, true},
		{SpaModelInputType[any](), "synthetic", true},
		{SpaModelInputType[any](), nil, false},
		{SpaModelInputType[spaInputImplemented](), spaInputImplementation{}, true},
		{SpaModelInputType[spaInputImplemented](), spaInputNamed{}, false},
	}
	for i, tt := range tests {
		if spaEncoderModelMatches(tt.token, tt.value) != tt.want {
			t.Fatalf("input capability case %d", i)
		}
	}
	// Concrete AssignableTo does NOT prove the original v.(T) assertion.
	anonymous := struct {
		Name string `sky:"name,string"`
	}{}
	if !reflect.TypeOf(anonymous).AssignableTo(reflect.TypeOf(spaInputNamed{})) {
		t.Fatal("fixture not assignable")
	}
	if _, exact := any(anonymous).(spaInputNamed); exact {
		t.Fatal("fixture unexpectedly asserts")
	}
	if spaEncoderModelMatches(named, anonymous) {
		t.Fatal("concrete structural assignability granted capability")
	}
}

func TestSpaModelInputCapabilityPropagationAndOverrideRevocation(t *testing.T) {
	full := func(_ any) any { return "reference" }
	token := SpaModelInputType[spaInputNamed]()
	cfg := Spa_withGeneratedModelEncoder(SpaAutoModelEncoder{Full: full, InputType: token}, map[string]any{})
	if fieldOrNil(cfg, "GeneratedModelInputType") != token || fieldOrNil(cfg, "GeneratedAutoModelEncoder") != true {
		t.Fatal("closed setter lost input token")
	}
	cfg = Spa_withPreparedModelEncoder(nil, nil, SpaPreparedEncoderProof{Full: full, InputType: token}, cfg)
	capability, ok := fieldOrNil(cfg, "PreparedAutoModelEncoder").(SpaPreparedModelEncoder)
	if !ok || capability.InputType != token || fieldOrNil(cfg, "GeneratedModelInputType") != token || fieldOrNil(cfg, "GeneratedAutoModelEncoder") != false {
		t.Fatal("prepared setter lost token or conferred independent Closed proof")
	}
	cfg = Spa_withPreparedModelEncoder(nil, nil, SpaPreparedEncoderProof{Full: full, InputType: token, Closed: true}, cfg)
	if fieldOrNil(cfg, "GeneratedModelInputType") != token || fieldOrNil(cfg, "GeneratedAutoModelEncoder") != true {
		t.Fatal("Closed path lost input token")
	}
	cfg = Spa_withModelEncoder(full, cfg)
	if fieldOrNil(cfg, "GeneratedModelInputType") != nil || fieldOrNil(cfg, "PreparedAutoModelEncoder") != nil || fieldOrNil(cfg, "GeneratedAutoModelEncoder") != false {
		t.Fatal("public override retained compiler capabilities")
	}
	cfg = Spa_withGeneratedModelEncoder(SpaAutoModelEncoder{Full: full}, cfg)
	if fieldOrNil(cfg, "GeneratedAutoModelEncoder") != true || spaEncoderModelMatches(fieldOrNil(cfg, "GeneratedModelInputType"), spaInputNamed{}) {
		t.Fatal("legacy Go capsule without token gained fast input eligibility")
	}
}

func TestSpaInputMismatchChoosesOriginalFullBeforePreparation(t *testing.T) {
	calls := []string{}
	prepared := SpaPreparedModelEncoder{
		InputType: SpaModelInputType[spaInputNamed](),
		Full:      func(_ any) any { calls = append(calls, "full coercion"); panic("synthetic coercion failure") },
		Prepare:   func(_ any) any { calls = append(calls, "prepare"); return nil },
		Finish:    func(_, _ any) any { calls = append(calls, "finish"); return "prepared" },
	}
	// The real SPA selector must make this decision BEFORE entering the mixed
	// engine. This fixture pins that explicit integration contract.
	invoke := func(model any) (out string) {
		defer func() {
			if recover() != nil {
				out = ""
			}
		}()
		if !spaEncoderModelMatches(prepared.InputType, model) {
			return AsString(sky_call(prepared.Full, model))
		}
		return prepared.encode(model)
	}
	if invoke(spaInputOther{}) != "" || !reflect.DeepEqual(calls, []string{"full coercion"}) {
		t.Fatal("type mismatch ran preparation before original Full/coercion")
	}
	calls = nil
	if invoke(spaInputNamed{}) != "prepared" || !reflect.DeepEqual(calls, []string{"prepare", "finish"}) {
		t.Fatal("exact input could not use prepared invocation")
	}
}

func TestSpaClosedRouteRequiresWholeRuntimeDomain(t *testing.T) {
	closed := spaInputNamed{}
	if !spaClosedEncoderModelsEligible(true, SpaModelInputType[spaInputNamed](), closed, closed) {
		t.Fatal("closed route lost exact eligible input")
	}
	type mixed struct {
		Page any    `sky:"page"`
		Name string `sky:"name,string"`
	}
	value := mixed{Page: "synthetic"}
	if spaClosedEncoderModelsEligible(true, SpaModelInputType[mixed](), value, value) {
		t.Fatal("literal HIR proof hid mixed runtime domain")
	}
	if spaClosedEncoderModelsEligible(false, SpaModelInputType[spaInputNamed](), closed, closed) {
		t.Fatal("runtime closed type substituted for compiler proof")
	}
}
