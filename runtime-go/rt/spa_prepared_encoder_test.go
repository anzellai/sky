package rt

import (
	"reflect"
	"testing"
)

func TestSpaPreparedEncoderCallsPreparationEveryInvocation(t *testing.T) {
	calls := []string{}
	prepare := func(_ any) any { calls = append(calls, "prepare"); return len(calls) }
	finish := func(codec, model any) any { calls = append(calls, "finish"); return AsString(model) }
	e := SpaPreparedModelEncoder{Prepare: prepare, Finish: finish}
	if e.encode("next") != "next" || e.encode("prev") != "prev" {
		t.Fatal("prepared encoder changed result")
	}
	if !reflect.DeepEqual(calls, []string{"prepare", "finish", "prepare", "finish"}) {
		t.Fatalf("invocation order: %v", calls)
	}
}

func TestSpaPreparedEncoderFailureDoesNotSuppressPreviousInvocation(t *testing.T) {
	prepares, finishes, writes := 0, 0, 0
	e := SpaPreparedModelEncoder{
		Prepare: func(_ any) any {
			prepares++
			if prepares == 1 {
				panic("fixture preparation failure")
			}
			return nil
		},
		Finish: func(codec, model any) any { finishes++; return `{"session":true}` },
	}
	cleared := spaPersistStep(nil, nil, e.encode, func(string) { writes++ }, []string{"session"}, nil)
	if cleared || prepares != 2 || finishes != 1 || writes != 0 {
		t.Fatalf("failure parity: prepares=%d finishes=%d writes=%d clear=%v", prepares, finishes, writes, cleared)
	}
}

func TestSpaPreparedEncoderKeepsNextWritePreviousOrder(t *testing.T) {
	calls := []string{}
	e := SpaPreparedModelEncoder{
		Prepare: func(_ any) any { calls = append(calls, "prepare"); return nil },
		Finish:  func(codec, model any) any { calls = append(calls, AsString(model)); return `{"session":true}` },
	}
	spaPersistStep("prev", "next", e.encode, func(string) { calls = append(calls, "write") }, []string{"session"}, nil)
	if !reflect.DeepEqual(calls, []string{"prepare", "next", "write", "prepare", "prev"}) {
		t.Fatalf("publication order: %v", calls)
	}
}

func TestSpaPreparedEncoderCannotGrantClosedProofOrTrustFakeHelpers(t *testing.T) {
	full := func(_ any) any { return "reference" }
	base := map[string]any{}
	cfg := Spa_withPreparedModelEncoder(nil, nil, full, base)
	if fieldOrNil(cfg, "GeneratedAutoModelEncoder") != false || fieldOrNil(cfg, "PreparedAutoModelEncoder") != nil {
		t.Fatal("unproven callable gained capability")
	}
	cfg = Spa_withPreparedModelEncoder(func(_ any) any { return nil }, func(_, _ any) any { return "prepared" }, SpaPreparedEncoderProof{Full: full}, base)
	if fieldOrNil(cfg, "GeneratedAutoModelEncoder") != false {
		t.Fatal("prepared proof implied closed proof")
	}
	if AsString(sky_call(fieldOrNil(cfg, "ModelEncoder"), nil)) != "reference" {
		t.Fatal("stage1 must retain exact Full")
	}
	cfg = Spa_withModelEncoder(full, cfg)
	if fieldOrNil(cfg, "PreparedAutoModelEncoder") != nil {
		t.Fatal("custom encoder retained stale prepared capability")
	}
	cfg = Spa_withPreparedModelEncoder(nil, nil, SpaPreparedEncoderProof{Full: full, Closed: true}, base)
	if fieldOrNil(cfg, "GeneratedAutoModelEncoder") != true || AsString(sky_call(fieldOrNil(cfg, "ModelEncoder"), nil)) != "reference" {
		t.Fatal("independent closed proof did not preserve Full path")
	}
}

func TestSpaPreparedEncoderPreservesMutableFactoryCalls(t *testing.T) {
	const adt = "sky.test.prepared.factory"
	key := AdtCtorKey{Adt: adt, Ctor: "Fixture"}
	adtVariantRegistryMu.Lock()
	previous, existed := adtVariantRegistry[key]
	ctors, hadCtors := adtVariantCtors[adt]
	delete(adtVariantRegistry, key)
	delete(adtVariantCtors, adt)
	adtVariantRegistryMu.Unlock()
	defer func() {
		adtVariantRegistryMu.Lock()
		defer adtVariantRegistryMu.Unlock()
		if existed {
			adtVariantRegistry[key] = previous
		} else {
			delete(adtVariantRegistry, key)
		}
		if hadCtors {
			adtVariantCtors[adt] = ctors
		} else {
			delete(adtVariantCtors, adt)
		}
	}()
	calls := 0
	RegisterAdtVariant(adt, "Fixture", func(_ []JsonRawMessage) any { calls++; return calls })
	prepared := SpaPreparedModelEncoder{
		Prepare: func(_ any) any { return adtVariantZeros(adt)[0] },
		Finish:  func(codec, model any) any { return AsString(model) },
	}
	prepared.encode("next")
	prepared.encode("prev")
	if calls != 2 {
		t.Fatalf("factory calls = %d, want 2", calls)
	}
	// The prepared fallback consumes the existing preparation once; it never
	// invokes Full and repeats strict construction within that invocation.
	prepared.Full = func(_ any) any { t.Fatal("prepared fallback restarted Full"); return nil }
	if prepared.encode("fallback") != "fallback" || calls != 3 {
		t.Fatal("prepared fallback duplicated construction")
	}
}

func TestSpaPreparedEncoderFinishFailureRemainsPerInvocation(t *testing.T) {
	prepares, finishes := 0, 0
	e := SpaPreparedModelEncoder{
		Prepare: func(_ any) any { prepares++; return nil },
		Finish: func(_, _ any) any {
			finishes++
			if finishes == 1 {
				panic("fixture finish failure")
			}
			return "previous"
		},
	}
	if e.encode("next") != "" || e.encode("prev") != "previous" || prepares != 2 || finishes != 2 {
		t.Fatal("finish failure suppressed or duplicated a logical invocation")
	}
}
