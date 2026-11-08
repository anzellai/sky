package rt

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSpaFieldPersistenceDifferentialJourney(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Text    string           `sky:"text,string"`
		Data    []string         `sky:"data,[]string"`
		Number  float64          `sky:"number,float64"`
	}
	for _, size := range []int{1000, 10000, 100000} {
		for _, exclude := range [][]string{nil, {"data"}, {"number"}} {
			for _, protected := range [][]string{nil, {"session"}, {"absent"}} {
				initial := model{Session: Nothing[string](), Data: []string{strings.Repeat("x", size*64)}}
				reference, optimized := &spaPublicationKV{values: memKV{}}, &spaPublicationKV{values: memKV{}}
				lifeA, lifeB := spaPersistLife{}, spaPersistLife{}
				controller := &spaFieldPersistence{}
				current := initial
				for step := 0; step < 13; step++ {
					next := current
					switch step {
					case 1, 2:
						next.Text += "x"
					case 3:
						next.Session = Just("synthetic-A")
					case 4:
						next.Session = Just("synthetic-B")
					case 5:
						next.Number = math.NaN()
					case 6:
						next.Session = Nothing[string]() // invalid full cannot sign out
					case 7:
						next.Number = 0
					case 8:
						next.Session = Just("synthetic-A")
					case 9:
						next.Session = Nothing[string]()
					case 10:
						next.Data = []string{"small"}
					case 11:
						delete(reference.values, spaPersistKey)
						delete(optimized.values, spaPersistKey)
					case 12:
						next.Data = []string{strings.Repeat("x", size*64)}
					}
					a := spaPersistStep(current, next, spaProjectionEncode, func(blob string) {
						spaPersistWrite(reference, &lifeA, spaPersistStripFields(blob, exclude, protected), protected, spaPersistMaxBytes)
					}, protected, nil)
					b, _ := controller.step(current, next, spaProjectionEncode, optimized, &lifeB, exclude, protected, spaPersistMaxBytes, true)
					if a != b || lifeA != lifeB || !reflect.DeepEqual(reference.calls, optimized.calls) || !reflect.DeepEqual(reference.values, optimized.values) {
						t.Fatalf("differential persistence mismatch at size=%d step=%d", size, step)
					}
					current = next
				}
			}
		}
	}
}

func TestSpaFieldPersistenceBoundedWorkAndReassertion(t *testing.T) {
	for _, size := range []int{1000, 10000, 100000} {
		current := spaFieldsFixture(size)
		controller := &spaFieldPersistence{}
		kv := &spaPublicationKV{values: memKV{}}
		life := spaPersistLife{}
		controller.step(current, current, spaProjectionEncode, kv, &life, nil, nil, spaPersistMaxBytes, true)
		next := current
		next.Text = "typed"
		_, work := controller.step(current, next, spaProjectionEncode, kv, &life, nil, nil, spaPersistMaxBytes, true)
		if work.FullEncodes != 0 || work.FieldWork.FieldEncodes != 1 || work.FieldWork.FieldEncodedBytes != 7 {
			t.Fatalf("unrelated data encoded: %+v", work)
		}
		before := len(kv.calls)
		delete(kv.values, spaPersistKey)
		_, noop := controller.step(next, next, spaProjectionEncode, kv, &life, nil, nil, spaPersistMaxBytes, true)
		if noop.FullEncodes != 0 || noop.FieldWork.FieldEncodes != 0 || noop.FieldWork.MaterializedBytes != 0 {
			t.Fatal("unchanged snapshot was encoded/materialized")
		}
		if size == 100000 {
			if len(kv.calls) != before || work.FieldWork.MaterializedBytes != 0 {
				t.Fatal("oversized snapshot was materialized/published")
			}
		} else if len(kv.calls) != before+1 || kv.values[spaPersistKey] != spaProjectionEncode(next) {
			t.Fatal("unchanged step failed to reassert removed snapshot")
		}
	}
}

func TestSpaFieldPersistenceCustomCodecUsesExactReference(t *testing.T) {
	controller := &spaFieldPersistence{}
	kv := &spaPublicationKV{values: memKV{}}
	life := spaPersistLife{}
	calls := []int{}
	encode := func(v any) string {
		n := v.(int)
		calls = append(calls, n)
		if n == 1 {
			return `{"session":1}`
		}
		return `{"session":null}`
	}
	clear, work := controller.step(1, 2, encode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes, false)
	if !clear || work.FullEncodes != 2 || !reflect.DeepEqual(calls, []int{2, 1}) || controller.snapshot != nil {
		t.Fatal("custom encoder did not keep exact ordered reference calls")
	}
}

type spaMetadataMutationKV struct {
	*spaPublicationKV
	change func()
}

func (s *spaMetadataMutationKV) Set(key, value string) {
	s.spaPublicationKV.Set(key, value)
	if s.change != nil {
		s.change()
	}
}

func TestSpaFieldPersistenceRechecksPreviousValidityAfterPublication(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Kind    int              `sky:"kind,spaPublicationKind"`
	}
	defer setEnumRegistration("spaPublicationKind", nil, false)
	RegisterEnum("spaPublicationKind", []string{"Allowed"})
	prev := model{Session: Just("synthetic")}
	next := model{Session: Nothing[string]()}
	controller := &spaFieldPersistence{}
	base := &spaPublicationKV{values: memKV{}}
	kv := &spaMetadataMutationKV{spaPublicationKV: base}
	// No protected fields during this certificate-establishing step. Keeping the
	// next step unprotected for persistence still allows the sign-out decision.
	life := spaPersistLife{}
	controller.step(prev, prev, spaProjectionEncode, kv, &life, nil, nil, spaPersistMaxBytes, true)
	// A sign-out normally suppresses Set after a held identity. Start unknown so
	// publication runs, then invalidate the old Full certificate through the registry.
	life = spaPersistLife{}
	kv.change = func() { RegisterEnum("spaPublicationKind", []string{}) }
	clear, work := controller.step(prev, next, spaProjectionEncode, kv, &life, nil, []string{"session"}, spaPersistMaxBytes, true)
	want := spaSessionClearedByStep(spaProjectionEncode(prev), spaProjectionEncode(next), []string{"session"})
	if clear != want || work.FullEncodes != 1 {
		t.Fatalf("post-publication reference mismatch: clear=%t want=%t full_calls=%d", clear, want, work.FullEncodes)
	}
}
