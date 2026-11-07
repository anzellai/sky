package rt

import (
	"reflect"
	"testing"
)

var spaViewEnumMethodCalls int

type spaViewGeneratedEnum int

func (spaViewGeneratedEnum) SkyEnumName() string {
	spaViewEnumMethodCalls++
	panic("admission must not invoke enum methods")
}

type spaViewForeignEnum int

func (spaViewForeignEnum) SkyEnumName() string {
	spaViewEnumMethodCalls++
	panic("foreign method must not run")
}

type spaViewStringerInt int

func (spaViewStringerInt) String() string { spaViewEnumMethodCalls++; panic("String must not run") }

type spaViewExtraMethodEnum int

func (spaViewExtraMethodEnum) SkyEnumName() string { panic("must not run") }
func (spaViewExtraMethodEnum) Extra()              {}

type spaViewWrongWidthEnum int64

func (spaViewWrongWidthEnum) SkyEnumName() string { panic("must not run") }

type spaViewNamedEnumString string
type spaViewWrongReturnEnum int

func (spaViewWrongReturnEnum) SkyEnumName() spaViewNamedEnumString { panic("must not run") }

func TestSpaViewMemoEnumCapabilityAdmitsOnlyExactGrantedTypeWithoutCalls(t *testing.T) {
	spaViewEnumMethodCalls = 0
	token := SpaViewMemoEnumType[spaViewGeneratedEnum]()
	if token == nil {
		t.Fatal("actual emitted enum shape needs capability")
	}
	cache := &spaViewMemoCache{}
	frame := cache.begin([]any{token})
	if !frame.open {
		t.Fatal("opaque enum capability rejected")
	}
	for _, value := range []any{spaViewGeneratedEnum(0), []spaViewGeneratedEnum{1, 2}, Nothing[spaViewGeneratedEnum]()} {
		if !frame.dataOnly(reflect.ValueOf(value), 0) {
			t.Fatal("nested granted enum rejected")
		}
	}
	if frame.dataOnly(reflect.ValueOf(spaViewForeignEnum(0)), 0) || frame.dataOnly(reflect.ValueOf(spaViewStringerInt(0)), 0) {
		t.Fatal("ungranted named methods admitted")
	}
	if spaViewEnumMethodCalls != 0 {
		t.Fatal("admission invoked methods")
	}
	frame.finish(false)
}

func TestSpaViewMemoEnumRawReflectionAndMalformedCapabilitiesStayRefused(t *testing.T) {
	if (&spaViewMemoCache{}).begin([]any{SpaViewMemoType[spaViewGeneratedEnum]()}).open {
		t.Fatal("legacy raw reflection named-int token widened")
	}
	for _, token := range []any{SpaViewMemoEnumType[int](), SpaViewMemoEnumType[spaViewStringerInt](), SpaViewMemoEnumType[spaViewExtraMethodEnum](), SpaViewMemoEnumType[spaViewWrongWidthEnum](), SpaViewMemoEnumType[spaViewWrongReturnEnum](), SpaViewMemoEnumType[*spaViewGeneratedEnum]()} {
		if token != nil {
			t.Fatal("factory granted non-emitted shape")
		}
	}
	forged := struct{ typ reflect.Type }{reflect.TypeOf(spaViewGeneratedEnum(0))}
	if (&spaViewMemoCache{}).begin([]any{forged}).open {
		t.Fatal("lookalike opaque capability admitted")
	}
}

func TestSpaViewMemoEnumHitCatalogResetAndCombinedBudget(t *testing.T) {
	spaViewEnumMethodCalls = 0
	cache := &spaViewMemoCache{}
	tokens := []any{SpaViewMemoType[struct{}](), SpaViewMemoEnumType[spaViewGeneratedEnum]()}
	builds := 0
	for i := 0; i < 2; i++ {
		frame := cache.begin(tokens)
		result := SpaViewMemoRegion(frame, 0, []any{SpaViewMemoCapture(1)}, func() any { builds++; return []spaViewGeneratedEnum{1} })
		if !reflect.DeepEqual(result, []spaViewGeneratedEnum{1}) {
			t.Fatal("wrong cached enum result")
		}
		frame.seal()
		frame.finish(true)
	}
	if builds != 1 || spaViewEnumMethodCalls != 0 {
		t.Fatal("enum hit missing or method invoked")
	}
	frame := cache.begin([]any{SpaViewMemoType[struct{}]()})
	SpaViewMemoRegion(frame, 0, []any{SpaViewMemoCapture(1)}, func() any { builds++; return "fresh" })
	frame.seal()
	frame.finish(true)
	if builds != 2 {
		t.Fatal("changed capability set reused stale entry")
	}
	oversized := make([]any, spaViewMemoMaxTypes+1)
	for i := range oversized {
		oversized[i] = tokens[i%len(tokens)]
	}
	if cache.begin(oversized).open {
		t.Fatal("combined enum/struct token budget widened")
	}
	cache.reset()
	if cache.current != nil {
		t.Fatal("reset retained enum cache")
	}
}
