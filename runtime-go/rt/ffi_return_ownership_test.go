package rt

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// A normal Go producer may reuse its PRIVATE buffer on its next invocation;
// it never mutates a slice argument supplied by Sky.
type ffiReuseProducer struct {
	values []int
	nested map[string][][]int
}

func (p *ffiReuseProducer) read(n int) []int                    { p.values[0] = n; return p.values }
func (p *ffiReuseProducer) readNested(n int) map[string][][]int { p.values[0] = n; return p.nested }

// This is the actual typed generated-wrapper return protocol: []int is a
// non-native slot, converted by FfiRet inside a SkyResult[any,any].
func ffiReuseTypedRead(p *ffiReuseProducer, n int) (out SkyResult[any, any]) {
	defer SkyFfiGuardT(&out)()
	out = Ok[any, any](FfiRet(p.read(n)))
	return
}

func TestFfiRetDetachesReusedProducerBufferThroughTypedResult(t *testing.T) {
	p := &ffiReuseProducer{values: []int{0}}
	first := ffiReuseTypedRead(p, 1)
	if first.Tag != 0 {
		t.Fatal("typed FFI result failed")
	}
	xs := AsListT[int](first.OkValue)
	second := ffiReuseTypedRead(p, 2)
	if second.Tag != 0 || xs[0] != 1 || AsListT[int](second.OkValue)[0] != 2 {
		t.Fatal("second Go call changed retained Sky List Int")
	}
	if &xs[0] == &p.values[0] {
		t.Fatal("return retained Go producer backing array")
	}
	xs[0] = 9
	if p.values[0] != 2 {
		t.Fatal("detached return still writes into producer")
	}
}

func TestFfiRetDetachesNestedSameRepMapListTrees(t *testing.T) {
	p := &ffiReuseProducer{values: []int{0}}
	p.nested = map[string][][]int{"items": {p.values}, "nil": nil, "empty": {}}
	first := FfiRet(p.readNested(1)).(map[string][][]int)
	_ = p.readNested(2)
	p.nested["new"] = [][]int{{3}}
	if first["items"][0][0] != 1 {
		t.Fatal("nested list still aliases Go producer buffer")
	}
	if _, ok := first["new"]; ok {
		t.Fatal("returned map still aliases producer")
	}
	if first["nil"] != nil || first["empty"] == nil {
		t.Fatal("nested nil/empty distinction changed")
	}
	first["items"][0][0] = 8
	if p.values[0] != 2 {
		t.Fatal("nested snapshot writes reached producer")
	}
}

func TestFfiRetDetachesConvertedOuterContainersAndStructGetters(t *testing.T) {
	shared := []int{1}
	// The outer non-same-representation map already allocates/converts keys;
	// its inner same-representation list must detach too.
	converted := FfiRet(map[int][]int{7: shared}).(map[string]any)
	got := converted[encodeDictKey(7)].([]int)
	// A nonempty Go struct is intentionally opaque. Its generated field getter
	// is the data ownership boundary, not a reflective clone of private fields.
	holder := &struct {
		Values  [][]int
		private chan int
	}{Values: [][]int{shared}, private: make(chan int)}
	opaque := FfiRet(holder)
	if opaque != holder {
		t.Fatal("opaque struct handle identity changed")
	}
	field := SkyFfiFieldGet3(opaque, "Values").(SkyResult[any, any])
	if field.Tag != 0 {
		t.Fatal("field getter failed")
	}
	extracted := field.OkValue.([][]int)
	shared[0] = 2
	if got[0] != 1 || extracted[0][0] != 1 {
		t.Fatal("converted map / opaque struct getter exposed shared buffer")
	}
	array := FfiRet([1][]int{shared}).([]any)
	shared[0] = 3
	if array[0].([]int)[0] != 2 {
		t.Fatal("converted array retained nested mutable data")
	}
}

func TestFfiRetPreservesSameRepTypesNilnessAndArgumentPolicy(t *testing.T) {
	var nilList []int
	var nilMap map[string][]int
	if got := FfiRet(nilList); reflect.TypeOf(got) != reflect.TypeOf(nilList) || got.([]int) != nil {
		t.Fatal("nil list type/shape changed")
	}
	if got := FfiRet(nilMap); reflect.TypeOf(got) != reflect.TypeOf(nilMap) || got.(map[string][]int) != nil {
		t.Fatal("nil map type/shape changed")
	}
	if got := FfiRet([]int{}).([]int); got == nil || len(got) != 0 {
		t.Fatal("empty list changed to nil")
	}
	if got := FfiRet(map[string][]int{}).(map[string][]int); got == nil || len(got) != 0 {
		t.Fatal("empty map changed to nil")
	}
	original := []string{"a"}
	copied := FfiRet(original).([]string)
	if &copied[0] == &original[0] {
		t.Fatal("typed []string shape was retained without ownership")
	}
	// Existing Sky-to-Go slice arguments remain borrowed/read-only. Do not
	// conflate argument conversion policy with the new return snapshot.
	arg := FfiArg[[]string](original)
	if &arg[0] != &original[0] {
		t.Fatal("return fix changed argument-side borrowing")
	}
}

type ffiOpaqueNamedList []int
type ffiOpaqueNamedMap map[string][]int
type ffiOpaqueNamedArray [1][]int

func TestFfiOpaqueNamedContainersRemainHandlesAndCannotGetClosedWitness(t *testing.T) {
	list := ffiOpaqueNamedList{1}
	retained := FfiRet(list).(ffiOpaqueNamedList)
	list[0] = 2
	if retained[0] != 2 || &retained[0] != &list[0] {
		t.Fatal("opaque named slice identity changed")
	}
	for _, value := range []any{list, ffiOpaqueNamedMap{"x": {1}}, ffiOpaqueNamedArray{[]int{1}}} {
		typ := reflect.TypeOf(value)
		budget := 1024
		if spaClosedTypeEligible(typ) || spaProjectionTypeClosed(typ, map[reflect.Type]uint8{}, 0) || spaProjectionClosed(reflect.ValueOf(value), 0, &budget) {
			t.Fatalf("opaque %v received immutable container witness", typ)
		}
		result := spaEncodeClosed(reflect.ValueOf(value), reflect.ValueOf(value), nil, "", nil, codecEnumRegistry(), spaClosedEncodingOptions{NeedRaw: true}, spaNewClosedEncodingBudget(spaPersistMaxBytes))
		if result.Supported {
			t.Fatalf("opaque %v entered closed cache path", typ)
		}
	}
	type field struct {
		Value ffiOpaqueNamedList `sky:"value,Foreign_List"`
	}
	if spaClosedTypeEligible(reflect.TypeOf(field{})) || spaProjectionTypeClosed(reflect.TypeOf(field{}), map[reflect.Type]uint8{}, 0) {
		t.Fatal("nested opaque named slice bypassed gate")
	}
	// A Go alias to an unnamed slice is ordinary convertible data, and is
	// copied by FfiRet; actual generated Sky List slots remain eligible.
	type alias = []int
	if !spaClosedTypeEligible(reflect.TypeOf(alias{})) {
		t.Fatal("ordinary Sky list alias lost closed eligibility")
	}
}

func TestFfiReflectAndCallbackIngressDetachProducerData(t *testing.T) {
	shared := []int{1}
	reflected := SkyFfiReflectCall3(reflect.ValueOf(func() []int { return shared }), false, nil).(SkyResult[any, any])
	if reflected.Tag != 0 {
		t.Fatal("reflective return failed")
	}
	saved := reflected.OkValue.([]int)
	var captured [][]int
	callback := FfiArg[func([]int)](func(value []int) struct{} {
		captured = append(captured, value)
		return struct{}{}
	})
	callback(shared)
	shared[0] = 2
	callback(shared)
	if saved[0] != 1 || captured[0][0] != 1 || captured[1][0] != 2 {
		t.Fatal("reflective return/callback data retained producer ownership")
	}
}

func TestFfiExactAbiCallbacksDetachListAndNestedMapInputs(t *testing.T) {
	var lists [][]int
	listCallback := FfiArg[func([]int) int](func(value []int) int {
		lists = append(lists, value)
		return len(value)
	})
	shared := []int{1}
	if listCallback(shared) != 1 {
		t.Fatal("exact list callback failed")
	}
	shared[0] = 2
	listCallback(shared)
	if lists[0][0] != 1 || lists[1][0] != 2 {
		t.Fatal("exact func([]int) int bypassed callback ownership")
	}
	var maps []map[string][]int
	mapCallback := FfiArg[func(map[string][]int) int](func(value map[string][]int) int {
		maps = append(maps, value)
		return len(value)
	})
	source := map[string][]int{"items": shared}
	mapCallback(source)
	shared[0] = 3
	source["later"] = []int{4}
	mapCallback(source)
	if maps[0]["items"][0] != 2 || len(maps[0]) != 1 || maps[1]["items"][0] != 3 || len(maps[1]) != 2 {
		t.Fatal("exact map callback retained producer containers")
	}
	// Cover the shared conversion entry used by reflective calls too; disabling
	// FfiArg alone would still leave ffiOpaqueToGo's AssignableTo shortcut.
	var reflected []int
	sky := func(value []int) int { reflected = value; return 0 }
	viaReflect := ffiToGo(sky, reflect.TypeOf(sky)).Interface().(func([]int) int)
	viaReflect(shared)
	shared[0] = 4
	if reflected[0] != 3 {
		t.Fatal("reflective exact callback conversion bypassed snapshot")
	}
}

func TestFfiOpaqueReturnedFunctionKeepsGoIdentityAndSemantics(t *testing.T) {
	calls := 0
	original := func(values []int) int { calls++; values[0]++; return calls }
	opaque := FfiRet(original)
	restored := FfiArg[func([]int) int](opaque)
	viaAny := FfiArg[any](opaque).(func([]int) int)
	if reflect.ValueOf(restored).Pointer() != reflect.ValueOf(original).Pointer() || reflect.ValueOf(viaAny).Pointer() != reflect.ValueOf(original).Pointer() {
		t.Fatal("opaque Go function identity changed")
	}
	values := []int{0}
	if restored(values) != 1 || viaAny(values) != 2 || values[0] != 2 {
		t.Fatal("opaque Go function was misclassified as a Sky callback")
	}
	// Interface-typed Go results also retain their actual function provenance.
	var boxed any = original
	fromInterface := FfiRet(boxed)
	if FfiArg[func([]int) int](fromInterface)(values) != 3 || values[0] != 3 {
		t.Fatal("interface-wrapped opaque function lost provenance")
	}
	if deepEq(opaque, opaque) != deepEq(original, original) || identityKey(opaque) != identityKey(original) || SkyShow(opaque) != SkyShow(original) {
		t.Fatal("private function provenance changed logical operations")
	}
	nested := FfiRet([]any{original}).([]any)
	if FfiArg[[]any](nested)[0].(func([]int) int)(values) != 4 || values[0] != 4 {
		t.Fatal("nested opaque Go function failed interface round-trip")
	}
	var nilCallback func([]int) int
	if FfiArg[func([]int) int](nilCallback) != nil || FfiRet(nilCallback) != nil {
		t.Fatal("nil function behavior changed")
	}
}

func TestFfiOpaqueFunctionProvenanceDoesNotExposePrivateFields(t *testing.T) {
	holder := &struct{ private func([]int) int }{private: func(xs []int) int { return len(xs) }}
	result := SkyFfiFieldGet3(holder, "private").(SkyResult[any, any])
	if result.Tag == 0 {
		t.Fatal("private function field became an opaque successful return")
	}
}

func TestFfiOpaqueFunctionCannotBecomeEmptyJSONObject(t *testing.T) {
	function := func(xs []int) int { return len(xs) }
	for _, value := range []any{function, FfiRet(function)} {
		encoded, err := json.Marshal(value)
		var unsupported *json.UnsupportedTypeError
		if err == nil || !errors.As(err, &unsupported) || unsupported.Type.Kind() != reflect.Func || len(encoded) != 0 {
			t.Fatalf("opaque function JSON lost original unsupported-function refusal: %q, %v", encoded, err)
		}
		msg := func() (msg string) {
			defer func() {
				if r := recover(); r != nil {
					msg = fmt.Sprint(r)
				}
			}()
			_ = Codec_autoEncOverrides(true, []any{}, value)
			return ""
		}()
		if kind, _ := classifyPanic(msg); kind != "JsonEncodeFailure" {
			t.Fatalf("autoWith [] function was not a classified refusal: %q (%s)", msg, kind)
		}
	}
	// Ordinary generated records still use the per-column override path.
	record := struct {
		Count int `sky:"count,Int"`
	}{Count: 3}
	overrides := []any{T2[any, any]{V0: "count", V1: func(any) any { return JsonValue{raw: "overridden"} }}}
	encoded := Codec_autoEncOverrides(true, overrides, record).(JsonValue)
	bytes, err := json.Marshal(encoded.raw)
	if err != nil || string(bytes) != `{"count":"overridden"}` {
		t.Fatalf("ordinary record override behavior changed: %s, %v", bytes, err)
	}
}

func TestFfiExactAbiCallbackResultsDetachSkyData(t *testing.T) {
	retained := []int{1}
	callback := FfiArg[func() []int](func() []int { return retained })
	result := callback()
	result[0] = 2
	if retained[0] != 1 {
		t.Fatal("exact callback result exposed retained Sky list")
	}
	nested := map[string][]int{"items": retained}
	mapCallback := FfiArg[func() map[string][]int](func() map[string][]int { return nested })
	mapResult := mapCallback()
	mapResult["items"][0] = 3
	mapResult["new"] = []int{4}
	if retained[0] != 1 || len(nested) != 1 {
		t.Fatal("callback result exposed retained Sky nested containers")
	}
	// Result and Maybe callback signatures also use owned result conversion.
	withError := FfiArg[func() ([]int, error)](func() SkyResult[any, any] { return Ok[any, any](retained) })
	errorResult, err := withError()
	if err != nil {
		t.Fatal(err)
	}
	errorResult[0] = 5
	withBool := FfiArg[func() ([]int, bool)](func() SkyMaybe[any] { return Just[any](retained) })
	boolResult, ok := withBool()
	if !ok {
		t.Fatal("Just callback lost value")
	}
	boolResult[0] = 6
	if retained[0] != 1 {
		t.Fatal("Result/Maybe callback output retained Sky storage")
	}
	// Intentional opaque Go output funcs still round-trip without adaptation.
	original := func() []int { return retained }
	restored := FfiArg[func() []int](FfiRet(original))
	restored()[0] = 7
	if retained[0] != 7 {
		t.Fatal("opaque returned Go function lost intentional reference semantics")
	}
}

// Match generated sealed-variant representation, including the reserved user
// constructor that originally collided with Std.Money's builtin writer.
type ffiOverrideUserMoney struct {
	V0 string
	V1 int
}

func (ffiOverrideUserMoney) SkyVariantTag() int     { return 0 }
func (ffiOverrideUserMoney) SkyVariantName() string { return "Money" }

type ffiOverrideUserChoice struct{ V0 int }

func (ffiOverrideUserChoice) SkyVariantTag() int     { return 0 }
func (ffiOverrideUserChoice) SkyVariantName() string { return "Chosen" }

func TestCodecAutoWithNonRecordWritersRetainCanonicalSemantics(t *testing.T) {
	dict := Dict_fromListT[int]([]any{T2[any, any]{V0: "one", V1: 1}})
	values := []struct {
		name  string
		value any
	}{
		{"sealed-user-Money", ffiOverrideUserMoney{V0: "user", V1: 7}},
		{"sealed-user-choice", ffiOverrideUserChoice{V0: 7}},
		{"legacy-user-choice", SkyADT{SkyName: "Chosen", Fields: []any{7}}},
		{"Just", Just[int](7)}, {"Nothing", Nothing[int]()},
		{"Ok", Ok[string, int](7)}, {"Err", Err[string, int]("failure")},
		{"typed-Dict", dict}, {"Set", Set_fromList([]any{1, 2})},
		{"JsonValue", JsonValue{raw: map[string]any{"one": 1}}},
		{"tuple", T2[int, string]{V0: 7, V1: "seven"}},
		{"list", []int{1, 2}}, {"scalar", 7},
	}
	for _, test := range values {
		t.Run(test.name, func(t *testing.T) {
			want := Codec_autoEnc(true, test.value).(JsonValue)
			got := Codec_autoEncOverrides(true, []any{}, test.value).(JsonValue)
			wantBytes, wantErr := json.Marshal(want.raw)
			gotBytes, gotErr := json.Marshal(got.raw)
			if wantErr != nil || gotErr != nil || string(wantBytes) != string(gotBytes) {
				t.Fatalf("autoWith [] changed canonical data: %s -> %s (%v/%v)", wantBytes, gotBytes, wantErr, gotErr)
			}
			rv := reflect.ValueOf(test.value)
			if test.name != "tuple" && rv.Kind() == reflect.Struct && codecAutoOverrideRecord(rv) {
				t.Fatal("nonrecord wrapper entered the record override path")
			}
			// Recognized wrappers retain their canonical writer even if the
			// low-level override list is nonempty; it cannot rewrite payloads.
			if !codecAutoOverrideRecord(rv) {
				overrides := []any{T2[any, any]{V0: "v0", V1: func(any) any { t.Fatal("nonrecord payload override ran"); return JsonValue{} }}}
				actual := Codec_autoEncOverrides(true, overrides, test.value).(JsonValue)
				actualBytes, err := json.Marshal(actual.raw)
				if err != nil || string(actualBytes) != string(wantBytes) {
					t.Fatalf("nonrecord override lost semantics: %s (%v)", actualBytes, err)
				}
			}
		})
	}
}

func TestFfiOpaqueNilFunctionReturnRepresentations(t *testing.T) {
	var function func([]int) int
	if direct := FfiRet(function); direct != nil {
		t.Fatal("direct nil Go function did not retain nil return policy")
	}
	var boxed any = function
	retained := FfiRet[any](boxed)
	if _, ok := retained.(ffiGoFuncHandle); ok {
		t.Fatal("interface containing a nil Go func became a nonnil carrier")
	}
	rv := reflect.ValueOf(retained)
	if !rv.IsValid() || rv.Kind() != reflect.Func || !rv.IsNil() {
		t.Fatal("interface nil function did not retain its original flattened typed-nil representation")
	}
	if FfiArg[func([]int) int](retained) != nil {
		t.Fatal("interface nil function did not round-trip into a nil func slot")
	}
	restored := FfiArg[any](retained)
	if v := reflect.ValueOf(restored); v.Kind() != reflect.Func || !v.IsNil() {
		t.Fatal("Any round-trip hid typed nil function")
	}
	// The same protocol used by a generated typed Result-returning wrapper.
	outcome := Ok[any, any](FfiRet[any](boxed))
	if outcome.Tag != 0 {
		t.Fatal("nil return unexpectedly changed Result outcome")
	}
	if v := reflect.ValueOf(outcome.OkValue); v.Kind() != reflect.Func || !v.IsNil() {
		t.Fatal("typed wrapper outcome hid nil function")
	}
	// Reflective invocation uses the static interface return type as well.
	result := SkyFfiReflectCall3(reflect.ValueOf(func() any { return function }), false, nil).(SkyResult[any, any])
	if result.Tag != 0 {
		t.Fatal("reflective nil function return failed")
	}
	if v := reflect.ValueOf(result.OkValue); v.Kind() != reflect.Func || !v.IsNil() {
		t.Fatal("reflective return hid typed nil function")
	}
}

func TestFfiNilCallbacksWithOwnershipSignaturesNeverBecomeCallable(t *testing.T) {
	var input func([]int) int
	var output func() []int
	inputType := reflect.TypeOf(input)
	outputType := reflect.TypeOf(output)
	if !ffiCallbackNeedsOwnership(inputType) || !ffiCallbackNeedsOwnership(outputType) {
		t.Fatal("nil regression must exercise both mutable-input and mutable-output adapter paths")
	}
	// These direct FfiArg calls must pass the ownership-aware exact-type
	// checks without manufacturing a nonnil reflect.MakeFunc wrapper.
	if FfiArg[func([]int) int](input) != nil || FfiArg[func() []int](output) != nil {
		t.Fatal("direct ownership-signature nil func became a callable adapter")
	}
	if FfiArg[func([]int) int](FfiRet(input)) != nil || FfiArg[func() []int](FfiRet(output)) != nil {
		t.Fatal("direct nil function return did not round-trip into a nil func slot")
	}
	var inputAny any = input
	var outputAny any = output
	inputRet := FfiRet[any](inputAny)
	outputRet := FfiRet[any](outputAny)
	if FfiArg[func([]int) int](inputRet) != nil || FfiArg[func() []int](outputRet) != nil {
		t.Fatal("boxed ownership-signature nil function became a callable adapter")
	}
	for _, test := range []struct {
		value any
		typ   reflect.Type
	}{{inputRet, inputType}, {outputRet, outputType}} {
		if _, ok := test.value.(ffiGoFuncHandle); ok {
			t.Fatal("boxed typed nil acquired nonnull provenance carrier")
		}
		if rv := reflect.ValueOf(test.value); rv.Kind() != reflect.Func || !rv.IsNil() {
			t.Fatal("boxed typed nil representation changed")
		}
		if rv := ffiToGo(test.value, test.typ); !rv.IsNil() {
			t.Fatal("reflective exact nil conversion manufactured callable wrapper")
		}
		if rv := ffiCallback(reflect.ValueOf(test.value), test.typ); !rv.IsNil() {
			t.Fatal("exact callback nil guard manufactured callable wrapper")
		}
	}
}

func TestFfiNilInterfaceArgumentsPreserveConvertedZero(t *testing.T) {
	if FfiArg[any](nil) != nil {
		t.Fatal("nil Any argument changed")
	}
	if FfiArg[interface{ String() string }](nil) != nil {
		t.Fatal("nil nonempty interface argument changed")
	}
	// Actual generated typed-wrapper protocol: an opaque nil function output
	// is Ok(nil), and forwarding it into a Go Any slot must not panic inside
	// the wrapper guard and turn a supported conversion into Err.
	forward := func(value any) (out SkyResult[any, bool]) {
		defer SkyFfiGuardT(&out)()
		out = Ok[any, bool](FfiArg[any](value) == nil)
		return
	}
	var nilFunction func([]int) int
	result := forward(FfiRet(nilFunction))
	if result.Tag != 0 || !result.OkValue {
		t.Fatal("forwarding direct nil function result to Any became Err")
	}
	// Boxed typed nil retains its concrete Go function type and nilness.
	var boxed any = nilFunction
	forwarded := FfiArg[any](FfiRet[any](boxed))
	if value := reflect.ValueOf(forwarded); value.Kind() != reflect.Func || !value.IsNil() {
		t.Fatal("boxed typed-nil func identity changed")
	}
}
