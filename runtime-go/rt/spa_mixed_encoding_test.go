package rt

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type spaMixedTestFX struct {
	V0     any
	log    *[]string
	action func()
}

func (v spaMixedTestFX) SkyVariantTag() int { *v.log = append(*v.log, "tag"); return 0 }
func (v spaMixedTestFX) SkyVariantName() string {
	*v.log = append(*v.log, "convert")
	if v.action != nil {
		v.action()
	}
	return "Fixture"
}

type spaMixedTestMarshal struct {
	log     *[]string
	name    string
	action  func()
	failure string
}

func (v spaMixedTestMarshal) MarshalJSON() ([]byte, error) {
	*v.log = append(*v.log, v.name)
	if v.action != nil {
		v.action()
	}
	switch v.failure {
	case "error":
		return nil, errors.New("fixture marshaling error")
	case "panic":
		panic("fixture marshaling panic")
	}
	return []byte(`{"synthetic":"<>"}`), nil
}

func spaMixedTestEncoder(log *[]string) SpaPreparedModelEncoder {
	return SpaPreparedModelEncoder{
		Full:    func(_ any) any { panic("mixed encoder restarted Full") },
		Prepare: func(_ any) any { *log = append(*log, "prepare"); return nil },
		Finish:  func(_ any, model any) any { return JsonEnc_encode(0, Codec_autoEnc(true, model)) },
	}
}

func spaMixedExpectedNull(full string, protected []string) string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(full), &fields)
	nulls := map[string]any{}
	for _, name := range protected {
		if raw, ok := fields[name]; ok {
			if spaIsNullRaw(raw) {
				nulls[name] = nil
			} else {
				nulls[name] = true
			}
		}
	}
	b, _ := json.Marshal(nulls)
	return string(b)
}

// Each factory produces fresh foreign state. No console/model logging occurs.
func spaMixedDifferential(t *testing.T, makeModel func(*[]string) any, options spaMixedEncodeOptions) spaMixedEncoded {
	t.Helper()
	refLog := []string{}
	reference := spaMixedTestEncoder(&refLog)
	full := reference.encode(makeModel(&refLog))
	gotLog := []string{}
	got := spaEncodeMixed(spaMixedTestEncoder(&gotLog), nil, makeModel(&gotLog), options)
	if !reflect.DeepEqual(refLog, gotLog) {
		t.Fatalf("effect trace mismatch: reference=%v mixed=%v", refLog, gotLog)
	}
	if got.Valid != (full != "") {
		t.Fatal("guarded encoding validity changed")
	}
	if !got.Valid {
		if got.NextCache != nil {
			t.Fatal("failed invocation created cache")
		}
		return got
	}
	if got.Reference {
		if got.FullJSON != full || got.Work.PreparedFallbacks != 1 {
			t.Fatal("prepared reference fallback changed outcome")
		}
		return got
	}
	if got.NullProjection != spaMixedExpectedNull(full, options.Protected) {
		t.Fatal("exact-key null projection changed")
	}
	if options.PreviousOnly {
		return got
	}
	want := spaPersistStripFields(full, options.Exclude, options.Protected)
	if got.EncodedBytes != uint64(len(want)) {
		t.Fatal("post-exclusion encoded size changed")
	}
	if len(want) <= options.CapBytes && got.Payload != want {
		t.Fatal("stored payload changed")
	}
	if len(want) > options.CapBytes && got.Payload != "" {
		t.Fatal("oversized payload was materialized")
	}
	id, ok := spaIdentityOf(want, options.Protected)
	if !ok || id != got.Identity {
		t.Fatal("protected identity changed")
	}
	return got
}

func TestSpaMixedNaNDefersFailureUntilMarshalPosition(t *testing.T) {
	type record struct {
		Before float64 `sky:"before,float64"`
		FX     any     `sky:"fx,any"`
		After  float64 `sky:"after,float64"`
	}
	for _, first := range []bool{false, true} {
		spaMixedDifferential(t, func(log *[]string) any {
			m := record{FX: spaMixedTestFX{log: log, V0: JsonValue{raw: spaMixedTestMarshal{log: log, name: "marshal"}}}}
			if first {
				m.Before = math.NaN()
			} else {
				m.After = math.NaN()
			}
			return m
		}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	}
}

func TestSpaMixedConversionFailureStopsBeforeAllMarshaling(t *testing.T) {
	type before struct {
		NaN float64 `sky:"nan,float64"`
		FX  any     `sky:"fx,any"`
		Bad any     `sky:"bad,any"`
	}
	type after struct {
		Bad any     `sky:"bad,any"`
		FX  any     `sky:"fx,any"`
		NaN float64 `sky:"nan,float64"`
	}
	for _, fxFirst := range []bool{false, true} {
		spaMixedDifferential(t, func(log *[]string) any {
			fx := spaMixedTestFX{log: log, V0: JsonValue{raw: spaMixedTestMarshal{log: log, name: "must-not-marshal"}}}
			if fxFirst {
				return before{NaN: math.NaN(), FX: fx, Bad: func() {}}
			}
			return after{NaN: math.NaN(), FX: fx, Bad: func() {}}
		}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	}
}

func TestSpaMixedNestedMaybeMatchesCurrentJustNothingSentinel(t *testing.T) {
	type record struct {
		Nested SkyMaybe[SkyMaybe[int]] `sky:"nested,rt.SkyMaybe[rt.SkyMaybe[int]]"`
		FX     any                     `sky:"fx,any"`
	}
	got := spaMixedDifferential(t, func(log *[]string) any {
		return record{Nested: Just(Nothing[int]()), FX: spaMixedTestFX{log: log, V0: "synthetic"}}
	}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Protected: []string{"nested"}})
	if !got.Valid || !strings.Contains(got.Payload, `"nested":null`) {
		t.Fatal("Just Nothing wire sentinel changed")
	}
}

func TestSpaMixedUnknownMarshalOrderErrorAndPanic(t *testing.T) {
	type record struct {
		First  JsonValue `sky:"first,rt.JsonValue"`
		Second JsonValue `sky:"second,rt.JsonValue"`
		FX     any       `sky:"fx,any"`
	}
	for _, failure := range []string{"", "error", "panic"} {
		for _, firstFails := range []bool{false, true} {
			spaMixedDifferential(t, func(log *[]string) any {
				a, b := "", failure
				if firstFails {
					a, b = failure, ""
				}
				return record{
					First:  JsonValue{raw: spaMixedTestMarshal{log: log, name: "first", failure: a}},
					Second: JsonValue{raw: spaMixedTestMarshal{log: log, name: "second", failure: b}},
					FX:     spaMixedTestFX{log: log, V0: "synthetic"},
				}
			}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Exclude: []string{"first", "second"}})
		}
	}
}

func TestSpaMixedMetadataMutationKeepsEarlierConversionResults(t *testing.T) {
	const enum = "sky.test.mixed.enum"
	previous, existed := enumVariantsForType(enum)
	defer setEnumRegistration(enum, previous, existed)
	type inA struct {
		Before int `sky:"before,sky.test.mixed.enum"`
		FX     any `sky:"fx,any"`
		After  int `sky:"after,sky.test.mixed.enum"`
	}
	type inB struct {
		Before int       `sky:"before,sky.test.mixed.enum"`
		FX     JsonValue `sky:"fx,rt.JsonValue"`
		After  int       `sky:"after,sky.test.mixed.enum"`
	}
	for _, phase := range []string{"A", "B"} {
		got := spaMixedDifferential(t, func(log *[]string) any {
			RegisterEnum(enum, []string{"old"})
			change := func() { RegisterEnum(enum, []string{"new"}) }
			if phase == "A" {
				return inA{FX: spaMixedTestFX{log: log, action: change, V0: "synthetic"}}
			}
			return inB{FX: JsonValue{raw: spaMixedTestMarshal{log: log, name: "marshal", action: change}}}
		}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
		wantAfter := `"after":"new"`
		if phase == "B" {
			wantAfter = `"after":"old"`
		}
		if !strings.Contains(got.Payload, `"before":"old"`) || !strings.Contains(got.Payload, wantAfter) {
			t.Fatal("metadata change reconverted an earlier result")
		}
		if got.NextCache.fields[0].valid {
			t.Fatal("retained a certificate from earlier metadata generation")
		}
	}
}

func TestSpaMixedExcludedInvalidFieldStillFails(t *testing.T) {
	type record struct {
		Session  SkyMaybe[string] `sky:"session,rt.SkyMaybe[string]"`
		Excluded float64          `sky:"excluded,float64"`
		FX       any              `sky:"fx,any"`
	}
	spaMixedDifferential(t, func(log *[]string) any {
		return record{Session: Just("synthetic"), Excluded: math.NaN(), FX: spaMixedTestFX{log: log, V0: "synthetic"}}
	}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Protected: []string{"session"}, Exclude: []string{"excluded"}})
}

func TestSpaMixedUnchangedLargeClosedFieldPreservesUnknownEffects(t *testing.T) {
	type record struct {
		Data []string `sky:"data,[]string"`
		FX   any      `sky:"fx,any"`
		Text string   `sky:"text,string"`
	}
	compared := -1
	var encodedWork uint64
	for _, n := range []int{1000, 10000, 100000} {
		log := []string{}
		m := record{Data: make([]string, n), FX: spaMixedTestFX{log: &log, V0: "synthetic"}}
		for i := range m.Data {
			m.Data[i] = strings.Repeat("x", 60)
		}
		e := spaMixedTestEncoder(&log)
		options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes}
		warm := spaEncodeMixed(e, nil, m, options)
		if !warm.Valid {
			t.Fatal("warm invocation failed")
		}
		log = nil
		m.Text = "changed"
		got := spaEncodeMixed(e, warm.NextCache, m, options)
		if !got.Valid || got.Work.Conversions != 2 || got.Work.Marshals != 2 || got.Work.ClosedReuses != 1 {
			t.Fatal("unchanged closed data was reencoded or unknown effect was skipped")
		}
		if !reflect.DeepEqual(log, []string{"prepare", "tag", "convert"}) {
			t.Fatal("warm unknown effect trace changed")
		}
		if compared == -1 {
			compared = got.Work.ComparedNodes
			encodedWork = got.Work.EncodedBytes
		}
		if encodedWork != got.Work.EncodedBytes {
			t.Fatal("unrelated retained data increased marshaled bytes")
		}
		if got.Work.ComparedNodes != compared || got.Work.RetainedBytes > spaPersistMaxBytes {
			t.Fatal("unrelated data comparison/retention grew")
		}
		if n == 100000 && (got.Payload != "" || got.Work.MaterializedBytes != 0) {
			t.Fatal("oversized snapshot materialized")
		}
		log = nil
		options.PreviousOnly = true
		prev := spaEncodeMixed(e, got.NextCache, m, options)
		if !prev.Valid || prev.Work.Conversions != 1 || prev.Work.Marshals != 1 || prev.Payload != "" {
			t.Fatal("previous-only encoding did not preserve unknown effects and pure reuse")
		}
	}
}

func TestSpaMixedRootFallbackConsumesPreparedValueOnce(t *testing.T) {
	for _, model := range []any{[]string{"synthetic"}, struct{ Plain string }{Plain: "synthetic"}, JsonValue{raw: true}, Just(1)} {
		spaMixedDifferential(t, func(_ *[]string) any { return model }, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	}
}

func TestSpaMixedRejectsOrdinaryRootKeyCollisionsAndAnonymousFields(t *testing.T) {
	type collision struct {
		A string `sky:"duplicate,string"`
		B string `sky:"duplicate,string"`
	}
	type tagged struct {
		A string `sky:"a,string"`
	}
	type embedded struct{ tagged }
	for _, model := range []any{collision{}, embedded{}} {
		if spaMixedRoot(reflect.TypeOf(model)).valid {
			t.Fatal("ambiguous root admitted")
		}
	}
}

func TestSpaMixedMissingRawReconstructsWhenSnapshotBecomesSmall(t *testing.T) {
	type record struct {
		First  string `sky:"first,string"`
		Second string `sky:"second,string"`
	}
	log := []string{}
	e := spaMixedTestEncoder(&log)
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes}
	m := record{First: strings.Repeat("a", 1000000), Second: strings.Repeat("b", 1000000)}
	warm := spaEncodeMixed(e, nil, m, options)
	if !warm.Valid || warm.Payload != "" || warm.NextCache.fields[1].raw != "" {
		t.Fatal("fixture did not establish an unretained selected field")
	}
	m.First = "small"
	got := spaEncodeMixed(e, warm.NextCache, m, options)
	full := e.encode(m)
	if !got.Valid || got.Payload != full || got.Work.Conversions != 2 {
		t.Fatal("now-small selected field was not reconstructed in Phase A")
	}
}

func TestSpaMixedProtectedAliasesExclusionEscapingAndOrdering(t *testing.T) {
	type record struct {
		Z       string           `sky:"z,string"`
		Session SkyMaybe[string] `sky:"sessionValue,rt.SkyMaybe[string]"`
		A       string           `sky:"a,string"`
		FX      any              `sky:"fx,any"`
	}
	for _, name := range []string{"sessionValue", "session_value"} {
		spaMixedDifferential(t, func(log *[]string) any {
			return record{Z: "excluded", Session: Just("synthetic<>\u2028"), A: "<>é\n\u2028", FX: spaMixedTestFX{log: log, V0: "synthetic"}}
		}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes, Exclude: []string{"z", "sessionValue"}, Protected: []string{name}})
	}
}

func TestSpaMixedWarmMetadataMutationDoesNotReplayOrReconvertEarlierField(t *testing.T) {
	const enum = "sky.test.mixed.warm.enum"
	previous, existed := enumVariantsForType(enum)
	defer setEnumRegistration(enum, previous, existed)
	type record struct {
		Before int `sky:"before,sky.test.mixed.warm.enum"`
		FX     any `sky:"fx,any"`
		After  int `sky:"after,sky.test.mixed.warm.enum"`
	}
	RegisterEnum(enum, []string{"old"})
	log := []string{}
	e := spaMixedTestEncoder(&log)
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes}
	m := record{FX: spaMixedTestFX{log: &log, V0: "synthetic"}}
	warm := spaEncodeMixed(e, nil, m, options)
	if !warm.Valid {
		t.Fatal("warm encode failed")
	}
	log = nil
	m.FX = spaMixedTestFX{log: &log, V0: "synthetic", action: func() { RegisterEnum(enum, []string{"new"}) }}
	got := spaEncodeMixed(e, warm.NextCache, m, options)
	if !got.Valid || got.Work.ClosedReuses != 1 || !strings.Contains(got.Payload, `"before":"old"`) || !strings.Contains(got.Payload, `"after":"new"`) {
		t.Fatal("warm metadata transition did not preserve per-position conversion")
	}
	if !reflect.DeepEqual(log, []string{"prepare", "tag", "convert"}) {
		t.Fatal("unknown effect replayed during warm fallback")
	}
}

func TestSpaMixedInvalidUTF8TagMatchesCanonicalNormalization(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{{
		Name: "Value", Type: reflect.TypeOf(""),
		// Get unquotes ASCII escape into a bad byte; camelToSnake normalizes it.
		Tag: reflect.StructTag(`sky:"bad\xff,string"`),
	}})
	value := reflect.New(typ).Elem()
	value.Field(0).SetString("synthetic")
	got := spaMixedDifferential(t, func(_ *[]string) any { return value.Interface() }, spaMixedEncodeOptions{
		CapBytes: spaPersistMaxBytes, Exclude: []string{"bad" + string([]byte{0xff})},
	})
	if !got.Valid || got.Reference || got.Payload != "{}" {
		t.Fatal("snake-case normalization/exclusion differs from reference")
	}
}

func TestSpaMixedNormalizedDuplicateKeysUsePreparedFallback(t *testing.T) {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "First", Type: reflect.TypeOf(""), Tag: reflect.StructTag(`sky:"bad\xff,string"`)},
		{Name: "Second", Type: reflect.TypeOf(""), Tag: reflect.StructTag(`sky:"bad\ufffd,string"`)},
	})
	value := reflect.New(typ).Elem()
	value.Field(0).SetString("first")
	value.Field(1).SetString("second")
	if spaMixedRoot(typ).valid {
		t.Fatal("normalized duplicate wire keys admitted")
	}
	got := spaMixedDifferential(t, func(_ *[]string) any { return value.Interface() }, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	if !got.Reference || got.Work.Prepares != 1 || got.Work.PreparedFallbacks != 1 {
		t.Fatal("normalized duplicate fallback replayed preparation or entered mixed phases")
	}
}

type spaMixedMutableJSON struct {
	state *string
	log   *[]string
}

func (v spaMixedMutableJSON) MarshalJSON() ([]byte, error) {
	*v.log = append(*v.log, "read-state")
	return json.Marshal(*v.state)
}

func TestSpaMixedUnknownRawIsCapturedBeforeLaterConversionEffects(t *testing.T) {
	type record struct {
		First JsonValue `sky:"first,rt.JsonValue"`
		FX    any       `sky:"fx,any"`
	}
	got := spaMixedDifferential(t, func(log *[]string) any {
		state := "before"
		return record{First: JsonValue{raw: spaMixedMutableJSON{state: &state, log: log}}, FX: spaMixedTestFX{log: log, V0: "synthetic", action: func() { state = "after" }}}
	}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	if !got.Valid || !strings.Contains(got.Payload, `"first":"after"`) {
		t.Fatal("unknown raw was marshaled before all conversions completed")
	}
}

func TestSpaMixedUnknownRawSeesEarlierMarshalEffects(t *testing.T) {
	type record struct {
		First  JsonValue `sky:"first,rt.JsonValue"`
		Second JsonValue `sky:"second,rt.JsonValue"`
	}
	got := spaMixedDifferential(t, func(log *[]string) any {
		state := "before"
		return record{First: JsonValue{raw: spaMixedTestMarshal{log: log, name: "change-state", action: func() { state = "after" }}}, Second: JsonValue{raw: spaMixedMutableJSON{state: &state, log: log}}}
	}, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	if !got.Valid || !strings.Contains(got.Payload, `"second":"after"`) {
		t.Fatal("unknown marshaling order changed")
	}
}

func TestSpaMixedUnchangedModelDoesNotFreezeForeignRawState(t *testing.T) {
	type record struct {
		Data  []string  `sky:"data,[]string"`
		Value JsonValue `sky:"value,rt.JsonValue"`
	}
	state := "before"
	log := []string{}
	encoder := spaMixedTestEncoder(&log)
	model := record{Data: make([]string, 10000), Value: JsonValue{raw: spaMixedMutableJSON{state: &state, log: &log}}}
	first := spaEncodeMixed(encoder, nil, model, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	if !first.Valid {
		t.Fatal("foreign fixture failed")
	}
	state = "after"
	log = nil
	next := spaEncodeMixed(encoder, first.NextCache, model, spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes})
	if !next.Valid || !strings.Contains(next.Payload, `"value":"after"`) || next.Work.ClosedReuses != 1 || !reflect.DeepEqual(log, []string{"prepare", "read-state"}) {
		t.Fatal("foreign raw state was reused or its operation skipped")
	}
	if next.Payload != encoder.encode(model) {
		t.Fatal("foreign raw output differs from current original codec")
	}
}

func TestSpaMixedPayloadReuseStillRunsUnknownEffects(t *testing.T) {
	type model struct {
		Data []string `sky:"data"`
		FX   any      `sky:"fx"`
		Text string   `sky:"text"`
	}
	log := []string{}
	encoder := spaMixedTestEncoder(&log)
	value := model{Data: []string{strings.Repeat("x", 16000)}, FX: JsonValue{raw: spaMixedTestMarshal{log: &log, name: "marshal"}}, Text: "a"}
	options := spaMixedEncodeOptions{CapBytes: spaPersistMaxBytes}
	first := spaEncodeMixed(encoder, nil, value, options)
	log = nil
	second := spaEncodeMixed(encoder, first.NextCache, value, options)
	if !second.Valid || second.Payload != first.Payload || second.Work.MaterializedBytes != 0 || !reflect.DeepEqual(log, []string{"prepare", "marshal"}) {
		t.Fatal("unchanged payload reuse suppressed effects or rebuilt data")
	}
	value.Text = "b"
	third := spaEncodeMixed(encoder, second.NextCache, value, options)
	if !third.Valid || third.Payload == second.Payload || third.Work.MaterializedBytes == 0 {
		t.Fatal("changed selected output reused stale payload")
	}
	options.Exclude = []string{"text"}
	fourth := spaEncodeMixed(encoder, third.NextCache, value, options)
	if !fourth.Valid || fourth.Payload == third.Payload || fourth.Work.MaterializedBytes == 0 {
		t.Fatal("changed selection reused stale payload")
	}
}
