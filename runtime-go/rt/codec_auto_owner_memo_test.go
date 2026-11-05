package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func codecOwnerMemoPublish(s *codecEnumRegistrySnapshot) {
	enumRegistryWriter.Lock()
	enumRegistry.Store(s)
	enumRegistryWriter.Unlock()
}
func codecOwnerMemoSnapshot(base *codecEnumRegistrySnapshot, declared, owner string) *codecEnumRegistrySnapshot {
	next := &codecEnumRegistrySnapshot{names: base.names, owners: make(map[string]string, len(base.owners)+1)}
	for k, v := range base.owners {
		if k != declared {
			next.owners[k] = v
		}
	}
	if owner != "" {
		next.owners[declared] = owner
	}
	return next
}
func TestCodecOwnerMemoObservesEveryLiveLookupEpochAndOnlyRetainsCurrent(t *testing.T) {
	old := codecEnumRegistry()
	t.Cleanup(func() { codecOwnerMemoPublish(old) })
	const declared = "codec.memo.epoch"
	missing := codecOwnerMemoSnapshot(old, declared, "")
	registered := codecOwnerMemoSnapshot(old, declared, "User.A")
	changed := codecOwnerMemoSnapshot(old, declared, "User.B")
	removed := codecOwnerMemoSnapshot(old, declared, "")
	memo := codecDeclaredOwnerMemo{declared: declared}
	states := []*codecEnumRegistrySnapshot{missing, missing, registered, registered, changed, removed, registered, registered}
	hits, misses := 0, 0
	for _, state := range states {
		codecOwnerMemoPublish(state)
		want := codecAutoDeclaredOwnerAt(declared, nil)
		got, hit := codecAutoDeclaredOwnerMemoResultAt(declared, nil, &memo)
		if got != want || memo.epoch != state || memo.owner != want {
			t.Fatal("live lookup reused a stale owner/epoch")
		}
		if hit {
			hits++
		} else {
			misses++
		}
	}
	// Re-publishing the SAME immutable old pointer is safe; it is not a changed
	// object hidden behind an equal pointer. Holding epoch strongly prevents ABA.
	if hits != 3 || misses != 5 {
		t.Fatal("memo branches did not observe current publications")
	}
	t.Logf("live_owner_reads=%d owner_map_misses=%d cache_hits=%d", len(states), misses, hits)
}
func TestCodecOwnerMemoPinAndExactDeclarationFallback(t *testing.T) {
	old := codecEnumRegistry()
	t.Cleanup(func() { codecOwnerMemoPublish(old) })
	const declared = "codec.memo.pin"
	pin := codecOwnerMemoSnapshot(old, declared, "User.Pinned")
	memo := codecDeclaredOwnerMemo{declared: declared}
	for _, owner := range []string{"", "User.Live", "User.Other", ""} {
		codecOwnerMemoPublish(codecOwnerMemoSnapshot(old, declared, owner))
		if got := codecAutoDeclaredOwnerMemoAt(declared, pin, &memo); got != "User.Pinned" || memo.epoch != pin {
			t.Fatal("pinned memo read live metadata")
		}
	}
	other := "codec.memo.different"
	live := codecOwnerMemoSnapshot(codecEnumRegistry(), other, "User.Other")
	codecOwnerMemoPublish(live)
	if got := codecAutoDeclaredOwnerMemoAt(other, nil, &memo); got != "User.Other" || memo.declared != declared || memo.epoch != pin {
		t.Fatal("mismatched declaration polluted fixed list memo")
	}
	for _, declared := range []string{"", "any", "int", "float64", "string", "bool", "rune", "struct{}"} {
		memo := codecDeclaredOwnerMemo{declared: declared}
		codecOwnerMemoPublish(codecOwnerMemoSnapshot(old, declared, skyDecimalOwner))
		if codecAutoDeclaredOwnerAt(declared, nil) != "" || codecAutoDeclaredOwnerMemoAt(declared, nil, &memo) != "" || memo.epoch != nil {
			t.Fatal("original primitive no-owner domain changed")
		}
	}
}

var codecOwnerMemoEffect func()

type codecOwnerMemoVariant struct{ V0 string }

func (codecOwnerMemoVariant) SkyVariantTag() int { return 0 }
func (codecOwnerMemoVariant) SkyVariantName() string {
	if codecOwnerMemoEffect != nil {
		codecOwnerMemoEffect()
	}
	return "OwnerMemoFX"
}
func codecOwnerMemoReference(rows any, elem string, pin *codecEnumRegistrySnapshot) (any, error) {
	rv := reflect.ValueOf(rows)
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		raw, err := codecAutoEncodeTypedAt(rv.Index(i), elem, true, pin)
		if err != nil {
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}
func codecOwnerMemoEncode(rows any, elem string, pin *codecEnumRegistrySnapshot, planned bool) (any, error) {
	if !planned {
		return codecOwnerMemoReference(rows, elem, pin)
	}
	return codecAutoEncodeTypedAt(reflect.ValueOf(rows), "[]"+elem, true, pin)
}
func TestCodecOwnerMemoMidARegistrationPreservesFactoryEffectsAndErrorPosition(t *testing.T) {
	old := codecEnumRegistry()
	const declared = "rt.T2[any,int]"
	const adt = "codec.memo.midA.public"
	adtOwnerRegistryMu.RLock()
	oldOwner, hadOwner := adtOwnerRegistry[adt]
	adtOwnerRegistryMu.RUnlock()
	t.Cleanup(func() {
		codecOwnerMemoEffect = nil
		codecOwnerMemoPublish(old)
		adtOwnerRegistryMu.Lock()
		if hadOwner {
			adtOwnerRegistry[adt] = oldOwner
		} else {
			delete(adtOwnerRegistry, adt)
		}
		adtOwnerRegistryMu.Unlock()
	})
	for _, pinned := range []bool{false, true} {
		var referenceError, referenceJSON string
		var referenceCalls int
		for _, planned := range []bool{false, true, true} {
			before := codecOwnerMemoSnapshot(old, declared, "")
			codecOwnerMemoPublish(before)
			var pin *codecEnumRegistrySnapshot
			if pinned {
				pin = before
			}
			calls := 0
			codecOwnerMemoEffect = func() { calls++; RegisterAdtOwner(adt, declared, skyDecimalOwner) }
			rows := []T2[any, int]{{V0: codecOwnerMemoVariant{}, V1: 1}, {V0: codecOwnerMemoVariant{}, V1: 2}}
			raw, err := codecOwnerMemoEncode(rows, declared, pin, planned)
			errorText, jsonText := "", ""
			if err != nil {
				errorText = err.Error()
			} else {
				b, e := json.Marshal(raw)
				if e != nil {
					t.Fatal(e)
				}
				jsonText = string(b)
			}
			if !planned {
				referenceError = errorText
				referenceJSON = jsonText
				referenceCalls = calls
			} else if errorText != referenceError || jsonText != referenceJSON || calls != referenceCalls {
				t.Fatal("mid-A owner registration changed factory FX or error order")
			}
			if pinned {
				if errorText != "" || calls != 2 {
					t.Fatal("pin observed later owner registration")
				}
			} else if !strings.Contains(errorText, "expected nominal") || calls != 1 {
				t.Fatal("later tuple did not reject newly declared builtin before its FX")
			}
		}
	}
}
func TestCodecOwnerMemoCompositeDeclaredPrecedenceAndLateReads(t *testing.T) {
	old := codecEnumRegistry()
	t.Cleanup(func() { codecOwnerMemoEffect = nil; codecOwnerMemoPublish(old) })
	const declared = "rt.T2[any,int]"
	for _, preexisting := range []bool{false, true} {
		var referenceError, referenceJSON string
		var referenceCalls int
		for _, planned := range []bool{false, true} {
			owner := ""
			if preexisting {
				owner = skyDecimalOwner
			}
			codecOwnerMemoPublish(codecOwnerMemoSnapshot(old, declared, owner))
			rows := []T2[any, int]{{V0: codecOwnerMemoVariant{}}, {V0: codecOwnerMemoVariant{}}}
			calls := 0
			codecOwnerMemoEffect = func() { calls++; rows[0].V1 = 11; rows[1].V1 = 22 }
			raw, err := codecOwnerMemoEncode(rows, declared, nil, planned)
			e, s := "", ""
			if err != nil {
				e = err.Error()
			} else {
				b, jerr := json.Marshal(raw)
				if jerr != nil {
					t.Fatal(jerr)
				}
				s = string(b)
			}
			if !planned {
				referenceError = e
				referenceJSON = s
				referenceCalls = calls
			} else if e != referenceError || s != referenceJSON || calls != referenceCalls {
				t.Fatal("memo changed composite owner precedence or current physical reads")
			}
			if preexisting {
				if calls != 0 || !strings.Contains(e, "expected nominal") {
					t.Fatal("tuple structure bypassed earlier builtin owner")
				}
			} else if calls != 2 || !strings.Contains(s, `"v1":11`) || !strings.Contains(s, `"v1":22`) {
				t.Fatal("memo froze value reads before conversion FX")
			}
		}
	}
}
func TestCodecOwnerMemoHasBoundedMetadataOnlyShapeAndNoRawEscape(t *testing.T) {
	typ := reflect.TypeOf(codecDeclaredOwnerMemo{})
	if typ.NumField() != 3 {
		t.Fatal("memo gained retained state")
	}
	pointers := 0
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type == reflect.TypeOf((*codecEnumRegistrySnapshot)(nil)) {
			pointers++
			continue
		}
		if field.Type.Kind() != reflect.String {
			t.Fatal("memo retains non-schema data/callback/history")
		}
	}
	if pointers != 1 {
		t.Fatal("memo retains multiple epochs")
	}
	rows := []T2[int, string]{{V0: 1, V1: "x"}, {V0: 2, V1: "y"}}
	raw, err := codecAutoEncodeTypedAt(reflect.ValueOf(rows), "[]rt.T2[int,string]", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range raw.([]any) {
		o, ok := row.(jsonOrderedObject)
		if !ok {
			t.Fatal("memo escaped as raw output")
		}
		for _, v := range o.vals {
			switch v.(type) {
			case int64, string:
			default:
				t.Fatal("raw output retains invocation metadata")
			}
		}
	}
}
func TestCodecOwnerMemoOneListEntryDoesNotAllocatePerLookup(t *testing.T) {
	const declared = "rt.T2[int,string]"
	memo := codecDeclaredOwnerMemo{declared: declared}
	codecAutoDeclaredOwnerMemoAt(declared, nil, &memo)
	var retained string
	allocations := testing.AllocsPerRun(100, func() {
		for i := 0; i < 1000; i++ {
			retained = codecAutoDeclaredOwnerMemoAt(declared, nil, &memo)
		}
	})
	if retained != codecAutoDeclaredOwnerAt(declared, nil) || allocations != 0 {
		t.Fatal("memo allocates history or changes owner lookup")
	}
	hits, misses := 0, 0
	for i := 0; i < 3000; i++ {
		_, hit := codecAutoDeclaredOwnerMemoResultAt(declared, nil, &memo)
		if hit {
			hits++
		} else {
			misses++
		}
	}
	if hits != 3000 || misses != 0 {
		t.Fatal("unchanged epoch repeated owner map work")
	}
	t.Logf("live_owner_reads=%d owner_map_misses=%d cache_hits=%d allocations_per_1000_reads=%.0f", hits+misses, misses, hits, allocations)
}
