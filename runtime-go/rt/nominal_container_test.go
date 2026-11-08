package rt

import (
	"encoding/json"
	"reflect"
	"testing"
)

// These are ordinary Sky record shapes, including legal field names which
// collide with the runtime's container layout. They must remain records.
type nominalMaybeRecord struct {
	Tag       int      `sky:"tag,int"`
	JustValue string   `sky:"justValue,string"`
	Extra     []string `sky:"extra,[]string"`
}

type nominalResultRecord struct {
	Tag      int      `sky:"tag,int"`
	OkValue  string   `sky:"okValue,string"`
	ErrValue string   `sky:"errValue,string"`
	Extra    []string `sky:"extra,[]string"`
}

type nominalADTRecord struct {
	SkyName string   `sky:"skyName,string"`
	Fields  []string `sky:"fields,[]string"`
	Extra   bool     `sky:"extra,bool"`
}

func TestOrdinaryContainerNamedRecordsKeepCodecFields(t *testing.T) {
	for _, value := range []any{
		nominalMaybeRecord{Tag: 0, JustValue: "payload", Extra: []string{"keep"}},
		nominalMaybeRecord{Tag: 1, JustValue: "payload", Extra: []string{"keep"}},
		nominalResultRecord{Tag: 0, OkValue: "ok", ErrValue: "err", Extra: []string{"keep"}},
		nominalResultRecord{Tag: 1, OkValue: "ok", ErrValue: "err", Extra: []string{"keep"}},
		nominalADTRecord{SkyName: "ordinary", Fields: []string{"keep"}, Extra: true},
	} {
		t.Run(reflect.TypeOf(value).Name(), func(t *testing.T) {
			encoded := JsonEnc_encode(0, Codec_autoEnc(true, value)).(string)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(encoded), &fields); err != nil || len(fields) != reflect.TypeOf(value).NumField() {
				t.Fatal("ordinary record lost fields or became a container payload")
			}
			if _, present := fields["extra"]; !present {
				t.Fatal("ordinary record's extra field disappeared")
			}
			result := JsonDec_decodeString(Codec_autoDecoder(true, value), encoded).(SkyResult[any, any])
			if result.Tag != 0 || !reflect.DeepEqual(result.OkValue, value) {
				t.Fatal("ordinary record did not round-trip through its own codec")
			}
			if len(AsList(Codec_autoCols(true, value))) != reflect.TypeOf(value).NumField() {
				t.Fatal("ordinary record's schema lost columns")
			}
		})
	}
}

func TestOrdinaryContainerNamedRecordsAreNotUnwrapped(t *testing.T) {
	maybe := nominalMaybeRecord{Tag: 0, JustValue: "payload", Extra: []string{"keep"}}
	result := nominalResultRecord{Tag: 0, OkValue: "ok", ErrValue: "err", Extra: []string{"keep"}}
	for _, value := range []any{maybe, result} {
		if isSkyContainer(value) || !reflect.DeepEqual(unwrapAny(value), value) {
			t.Fatal("ordinary record was implicitly unwrapped")
		}
		if !reflect.DeepEqual(Field(value, "Extra"), []string{"keep"}) {
			t.Fatal("ordinary record field access discarded its container-like record")
		}
	}
	updated := RecordUpdate(maybe, map[string]any{"Extra": []string{"updated"}})
	if !reflect.DeepEqual(updated, nominalMaybeRecord{Tag: 0, JustValue: "payload", Extra: []string{"updated"}}) {
		t.Fatal("polymorphic record update unwrapped a record")
	}
	if _, _, found := reflectMaybeFields(maybe); found {
		t.Fatal("ordinary record acquired Maybe coercion")
	}
	if _, _, _, found := reflectResultFields(result); found {
		t.Fatal("ordinary record acquired Result coercion")
	}
	if _, _, found := ffiMaybeParts(reflect.ValueOf(maybe)); found {
		t.Fatal("ordinary record acquired FFI Maybe conversion")
	}
	if _, _, _, found := ffiResultParts(reflect.ValueOf(result)); found {
		t.Fatal("ordinary record acquired FFI Result conversion")
	}
	if MaybeTag(maybe) != -1 || ResultTag(result) != -1 || MaybeJust(maybe) != nil || ResultOk(result) != nil || ResultErr(result) != nil {
		t.Fatal("ordinary records were accepted by container accessors")
	}
	if tag, _ := anyMaybeView(maybe); tag != -1 {
		t.Fatal("ordinary record acquired Maybe combinator semantics")
	}
	if tag, _, _ := anyResultView(result); tag != -1 {
		t.Fatal("ordinary record acquired Result combinator semantics")
	}
}

func TestNominalContainersDoNotReinterpretOrdinaryConvertibleStructs(t *testing.T) {
	type ordinaryMaybe struct {
		Tag       int
		JustValue string
	}
	type ordinaryResult struct {
		Tag      int
		OkValue  string
		ErrValue int
	}
	type ordinaryADT struct {
		Tag     int
		SkyName string
		Fields  []any
	}
	for _, tc := range []struct {
		value  any
		target reflect.Type
	}{
		{ordinaryMaybe{}, reflect.TypeOf(SkyMaybe[string]{})},
		{ordinaryResult{}, reflect.TypeOf(SkyResult[int, string]{})},
		{ordinaryADT{}, reflect.TypeOf(SkyADT{})},
	} {
		if narrowed := narrowReflectValue(reflect.ValueOf(tc.value), tc.target); narrowed.IsValid() {
			t.Fatal("struct conversion bypassed nominal container identity")
		}
	}
}

func TestNominalContainerAliasesRemainCompatible(t *testing.T) {
	type maybeAlias = SkyMaybe[string]
	type resultAlias = SkyResult[string, int]
	type adtAlias = SkyADT
	if !isSkyMaybeType(reflect.TypeOf(maybeAlias{})) || !isSkyAdtType(reflect.TypeOf(adtAlias{})) {
		t.Fatal("a true runtime type alias lost container identity")
	}
	if got := MaybeCoerce[any](maybeAlias{Tag: 0, JustValue: "kept"}); got.Tag != 0 || got.JustValue != "kept" {
		t.Fatal("Maybe cross-instantiation coercion changed")
	}
	if got := ResultCoerce[any, any](resultAlias{Tag: 0, OkValue: 7}); got.Tag != 0 || got.OkValue != 7 {
		t.Fatal("Result cross-instantiation coercion changed")
	}
	// Marker methods may be promoted through embedding. Such a wrapper is
	// still a record, never the embedded runtime container itself.
	type embeddedMaybe struct {
		SkyMaybe[int]
		Extra string
	}
	if isSkyMaybeType(reflect.TypeOf(embeddedMaybe{})) || isSkyContainer(embeddedMaybe{}) {
		t.Fatal("embedding forged the exact runtime container identity")
	}
	type embeddedResult struct {
		SkyResult[string, int]
		Extra string
	}
	if isSkyResultType(reflect.TypeOf(embeddedResult{})) || isSkyContainer(embeddedResult{}) {
		t.Fatal("embedding forged Result identity")
	}
	type definedMaybe SkyMaybe[int]
	if isSkyMaybeType(reflect.TypeOf(definedMaybe{})) {
		t.Fatal("a new Go record type acquired container identity from its fields")
	}
	legacy := adtAlias{Tag: 7, SkyName: "Legacy", Fields: []any{"kept"}}
	if AdtTag(legacy) != 7 || AdtField(legacy, 0) != "kept" {
		t.Fatal("explicit legacy SkyADT access changed")
	}
}

func TestOrdinaryTaggedRecordsStillNarrowAsRecords(t *testing.T) {
	type source struct {
		Tag       int64
		JustValue string
		Extra     []string
	}
	value := source{Tag: 3, JustValue: "payload", Extra: []string{"keep"}}
	got := Coerce[nominalMaybeRecord](value)
	if got.Tag != 3 || got.JustValue != "payload" || !reflect.DeepEqual(got.Extra, value.Extra) {
		t.Fatal("ordinary record-to-record narrowing lost a tagged field")
	}
}

func TestNominalResultExtractorsRejectOrdinaryRecord(t *testing.T) {
	ordinary := struct {
		Tag      int
		OkValue  string
		ErrValue string
	}{OkValue: "ordinary"}
	if _, ok := spaResultOk(ordinary); ok {
		t.Fatal("SPA decoder accepted ordinary record as Result")
	}
	if _, _, _, ok := islandResultParts(ordinary); ok {
		t.Fatal("island handler accepted ordinary record as Result")
	}
	result := Ok[string, int](42)
	if v, ok := spaResultOk(result); !ok || v != 42 {
		t.Fatal("SPA decoder rejected actual Result instantiation")
	}
	if tag, v, _, ok := islandResultParts(result); !ok || tag != 0 || v != 42 {
		t.Fatal("island handler rejected actual Result instantiation")
	}
}
