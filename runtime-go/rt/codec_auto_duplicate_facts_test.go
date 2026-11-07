package rt

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The fixture and its canonical type are constructed outside the measured
// calls. This tests the real public-helper path, not a duplicate scanner or a
// timing threshold. Preparing the same immutable keys should allocate once.
func TestCodecAutoRecordDuplicateKeyWarmAllocations(t *testing.T) {
	fields := make([]reflect.StructField, 128)
	for i := range fields {
		fields[i] = reflect.StructField{
			Name: fmt.Sprintf("Field%d", i), Type: reflect.TypeOf(0),
			Tag: reflect.StructTag(fmt.Sprintf(`sky:"fieldValue%d,Int"`, i)),
		}
	}
	typ := reflect.StructOf(fields)
	if got := codecAutoRecordDuplicateKey(typ, true); got != "" {
		t.Fatalf("valid distinct keys refused: %s", got)
	}
	var got string
	allocs := testing.AllocsPerRun(20, func() {
		got = codecAutoRecordDuplicateKey(typ, true)
	})
	if got != "" {
		t.Fatal("warming changed the duplicate-key result")
	}
	if allocs > 0 {
		t.Fatalf("warm immutable duplicate-key fact allocated %.0f times per call; want 0", allocs)
	}
}

func codecDuplicateTestType(tag string) reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "First", Type: reflect.TypeOf(0), Tag: reflect.StructTag(fmt.Sprintf("sky:%q", tag+",Int"))},
		{Name: "Second", Type: reflect.TypeOf(0), Tag: reflect.StructTag(fmt.Sprintf("sky:%q", tag+",Int"))},
	})
}

func TestCodecDuplicateFactsLegacyParity(t *testing.T) {
	cache := codecDuplicateFacts{maxEntries: 32, maxBytes: 4096}
	types := []reflect.Type{
		reflect.TypeOf(struct{}{}),
		reflect.TypeOf(struct{ FieldName int }{}),
		reflect.TypeOf(struct {
			First  int `sky:"aB,Int"`
			Second int `sky:"a_b,Int"`
			Third  int `sky:"aB,Int"`
		}{}),
		reflect.TypeOf(struct {
			hidden  int `sky:"same,Int"`
			Visible int `sky:"same,Int"`
		}{}),
		codecDuplicateTestType("quoted\"|key"),
	}
	for _, typ := range types {
		for _, snake := range []bool{false, true} {
			want := codecDuplicateLegacy(typ, snake)
			for call := 0; call < 3; call++ {
				if got := cache.problem(typ, snake); got != want {
					t.Fatalf("call %d snake %t: got %q, want %q", call, snake, got, want)
				}
			}
		}
	}
	if len(cache.facts) != 2*len(types) {
		t.Fatal("empty successes or naming modes were not retained independently")
	}
	typ := types[2]
	if got := cache.problem(typ, true); !strings.Contains(got, `"aB" and "a_b"`) {
		t.Fatal("snake first-collision order changed")
	}
	if got := cache.problem(typ, false); !strings.Contains(got, `"aB" and "aB"`) {
		t.Fatal("camel first-collision order changed")
	}
}

type codecDuplicateForeignType struct {
	reflect.Type
	trace     *[]string
	duplicate *bool
	panicAt   *int
}

func (f codecDuplicateForeignType) NumField() int {
	*f.trace = append(*f.trace, "NumField")
	return 2
}

func (f codecDuplicateForeignType) Field(i int) reflect.StructField {
	*f.trace = append(*f.trace, fmt.Sprintf("Field%d", i))
	if i == *f.panicAt {
		panic("foreign field panic")
	}
	name := fmt.Sprintf("value%d", i)
	if *f.duplicate {
		name = "same"
	}
	return reflect.StructField{Name: fmt.Sprintf("Field%d", i), Type: reflect.TypeOf(0), Tag: reflect.StructTag(fmt.Sprintf("sky:%q", name+",Int"))}
}

type codecDuplicateNoncomparableType struct {
	codecDuplicateForeignType
	data []byte
}

func codecDuplicateOutcome(fn func() string) (value string, failure any) {
	defer func() { failure = recover() }()
	value = fn()
	return
}

func TestCodecDuplicateFactsForeignFallback(t *testing.T) {
	cache := codecDuplicateFacts{maxEntries: 8, maxBytes: 4096}
	var trace []string
	duplicate, panicAt := false, -1
	foreign := codecDuplicateForeignType{Type: reflect.TypeOf(struct{}{}), trace: &trace, duplicate: &duplicate, panicAt: &panicAt}
	for _, typ := range []reflect.Type{foreign, codecDuplicateNoncomparableType{codecDuplicateForeignType: foreign, data: []byte{1}}, nil, reflect.TypeOf(0)} {
		for _, collision := range []bool{false, true} {
			for _, panicIndex := range []int{-1, 1} {
				duplicate, panicAt = collision, panicIndex
				trace = nil
				want, wantPanic := codecDuplicateOutcome(func() string { return codecDuplicateLegacy(typ, true) })
				wantTrace := append([]string(nil), trace...)
				trace = nil
				got, gotPanic := codecDuplicateOutcome(func() string { return cache.problem(typ, true) })
				if got != want || fmt.Sprint(gotPanic) != fmt.Sprint(wantPanic) || !reflect.DeepEqual(trace, wantTrace) {
					t.Fatalf("foreign fallback changed: result %q/%q panic %v/%v trace %v/%v", got, want, gotPanic, wantPanic, trace, wantTrace)
				}
			}
		}
	}
	if len(cache.facts) != 0 || cache.bytes != 0 {
		t.Fatal("foreign or invalid type entered the cache")
	}
}

func TestCodecDuplicateFactsBoundedAdmission(t *testing.T) {
	one, two := codecDuplicateTestType("first"), codecDuplicateTestType("second")
	first := codecDuplicateLegacy(one, true)
	for _, cache := range []*codecDuplicateFacts{
		{maxEntries: 1, maxBytes: 4096},
		{maxEntries: 8, maxBytes: len(first)},
		{maxEntries: 0, maxBytes: 4096},
		{maxEntries: 8, maxBytes: 0},
	} {
		for _, typ := range []reflect.Type{one, two, one, two} {
			if got, want := cache.problem(typ, true), codecDuplicateLegacy(typ, true); got != want {
				t.Fatal("a retention limit changed the original error")
			}
		}
		if len(cache.facts) > cache.maxEntries || cache.bytes > cache.maxBytes {
			t.Fatal("retention budget exceeded")
		}
	}
	cache := codecDuplicateFacts{maxEntries: 8, maxBytes: 64 << 10}
	long := codecDuplicateTestType(strings.Repeat("x", codecDuplicateFactMaxResultBytes))
	if got, want := cache.problem(long, true), codecDuplicateLegacy(long, true); got != want || got == "" {
		t.Fatal("oversized result changed")
	}
	if len(cache.facts) != 0 || cache.bytes != 0 {
		t.Fatal("oversized result was retained")
	}
	// Empty results consume entries even when their retained string size is zero.
	cache = codecDuplicateFacts{maxEntries: 1, maxBytes: 0}
	cache.problem(reflect.TypeOf(struct{}{}), true)
	cache.problem(reflect.TypeOf(struct{ X int }{}), true)
	if len(cache.facts) != 1 || cache.bytes != 0 {
		t.Fatal("empty successes bypassed the entry limit")
	}
}

func TestCodecDuplicateFactsConcurrentBudgets(t *testing.T) {
	cache := codecDuplicateFacts{maxEntries: 7, maxBytes: 1024}
	types := make([]reflect.Type, 24)
	wants := make([]string, len(types))
	for i := range types {
		types[i] = codecDuplicateTestType(fmt.Sprintf("entry%d", i))
		wants[i] = codecDuplicateLegacy(types[i], true)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for call := 0; call < 32; call++ {
				for i, typ := range types {
					if cache.problem(typ, true) != wants[i] {
						t.Error("concurrent lookup changed the original result")
					}
				}
			}
		}()
	}
	workers.Wait()
	bytes := 0
	for _, value := range cache.facts {
		bytes += len(value)
	}
	if len(cache.facts) > cache.maxEntries || bytes != cache.bytes || bytes > cache.maxBytes {
		t.Fatal("concurrent publication exceeded the exact retention budget")
	}
}

// Frozen pre-cache scanner: retain its complete first-error behavior as an oracle.
func codecDuplicateLegacy(t reflect.Type, snake bool) string {
	seen := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		key := skyColKey(f, snake)
		if first, ok := seen[key]; ok {
			return "Codec.auto: the fields \"" + first + "\" and \"" + skyTagName(f) +
				"\" both become the key \"" + key + "\", so one would overwrite the other; rename one, or use Codec.autoCamel"
		}
		seen[key] = skyTagName(f)
	}
	return ""
}
