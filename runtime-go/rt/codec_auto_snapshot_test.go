package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type codecSnapshotFX struct {
	V0     any
	action func()
}

func (codecSnapshotFX) SkyVariantTag() int { return 0 }
func (v codecSnapshotFX) SkyVariantName() string {
	if v.action != nil {
		v.action()
	}
	return "Fixture"
}

type codecSnapshotMarshal struct {
	action func()
	calls  *int
}

func (v codecSnapshotMarshal) MarshalJSON() ([]byte, error) {
	*v.calls++
	if v.action != nil {
		v.action()
	}
	return []byte("true"), nil
}

func TestCodecAutoNilSnapshotPreservesUnknownMutationPerRead(t *testing.T) {
	const enum = "sky.test.codec.snapshot.nil"
	previous, existed := enumVariantsForType(enum)
	defer setEnumRegistration(enum, previous, existed)
	type record struct {
		Before int `sky:"before,sky.test.codec.snapshot.nil"`
		FX     any `sky:"fx,any"`
		After  int `sky:"after,sky.test.codec.snapshot.nil"`
	}
	for _, phase := range []string{"A", "B"} {
		run := func(explicitNil bool) (string, int) {
			RegisterEnum(enum, []string{"old"})
			calls := 0
			change := func() { RegisterEnum(enum, []string{"new-longer"}) }
			fx := any(codecSnapshotFX{V0: "synthetic", action: change})
			if phase == "B" {
				fx = JsonValue{raw: codecSnapshotMarshal{action: change, calls: &calls}}
			}
			value := reflect.ValueOf(record{FX: fx})
			var raw any
			var err error
			if explicitNil {
				raw, err = codecAutoEncodeValAt(value, true, nil)
			} else {
				raw, err = codecAutoEncodeVal(value, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			return string(encoded), calls
		}
		a, ac := run(false)
		b, bc := run(true)
		if a != b || ac != bc {
			t.Fatal("default-nil wrapper changed effects or canonical bytes")
		}
		wantAfter := `"after":"new-longer"`
		if phase == "B" {
			wantAfter = `"after":"old"`
		}
		if !strings.Contains(a, `"before":"old"`) || !strings.Contains(a, wantAfter) {
			t.Fatal("nil context captured metadata instead of reading at original positions")
		}
	}
}

func TestCodecAutoPinnedSnapshotThreadsEveryRecursiveEncodeEdge(t *testing.T) {
	const enum = "sky.test.codec.snapshot.recursion"
	previous, existed := enumVariantsForType(enum)
	defer setEnumRegistration(enum, previous, existed)
	type inner struct {
		Ordinal int `sky:"ordinal,sky.test.codec.snapshot.recursion"`
	}
	type outer struct {
		List  []inner         `sky:"list,[]inner"`
		Maybe SkyMaybe[inner] `sky:"maybe,rt.SkyMaybe[inner]"`
		Tuple T2[int, string] `sky:"tuple,rt.T2[sky.test.codec.snapshot.recursion,string]"`
		Dict  SkyDict[inner]  `sky:"dict,rt.SkyDict[inner]"`
		Map   map[int]inner   `sky:"map,map[sky.test.codec.snapshot.recursion]inner"`
	}
	RegisterEnum(enum, []string{"old"})
	snapshot := codecEnumRegistry()
	m := outer{List: []inner{{}}, Maybe: Just(inner{}), Tuple: T2[int, string]{V1: "synthetic"}, Dict: SkyDict[inner]{}.Insert("key", inner{}), Map: map[int]inner{0: {}}}
	RegisterEnum(enum, []string{"new-longer"})
	oldRaw, err := codecAutoEncodeValAt(reflect.ValueOf(m), true, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	newRaw, err := codecAutoEncodeVal(reflect.ValueOf(m), true)
	if err != nil {
		t.Fatal(err)
	}
	oldJSON, err := json.Marshal(oldRaw)
	if err != nil {
		t.Fatal(err)
	}
	newJSON, err := json.Marshal(newRaw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(oldJSON), `"old"`) != 6 || strings.Contains(string(oldJSON), "new-longer") {
		t.Fatal("pinned context missed a List/Maybe/Tuple/Dict/Map recursive edge")
	}
	if strings.Count(string(newJSON), `"new-longer"`) != 6 {
		t.Fatal("default wrappers stopped reading current metadata")
	}
}

func TestCodecAutoPinnedMaybeKeepsRawNilAndJustNullSentinel(t *testing.T) {
	snapshot := codecEnumRegistry()
	nothing, err := codecAutoEncodeTypedAt(reflect.ValueOf(Nothing[int]()), "rt.SkyMaybe[int]", true, snapshot)
	if err != nil || nothing != nil {
		t.Fatal("pinned Nothing lost raw nil")
	}
	justNothing, err := codecAutoEncodeTypedAt(reflect.ValueOf(Just(Nothing[int]())), "rt.SkyMaybe[rt.SkyMaybe[int]]", true, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := justNothing.(codecJustNull); !ok {
		t.Fatal("pinned Just Nothing lost raw sentinel")
	}
	for _, raw := range []any{nothing, justNothing} {
		encoded, err := json.Marshal(raw)
		if err != nil || string(encoded) != "null" {
			t.Fatal("pinned Maybe wire null changed")
		}
	}
}
