package rt

import (
	"encoding/json"
	"reflect"
	"sync"
)

// SpaAutoModelEncoder is emitted only after the lowerer's resolved-HIR proof
// gate. It retains the EXACT full encoder: strict Codec.auto construction and
// its failure behaviour are not replaced by a different runtime codec.
// No Sky kernel constructs a capsule from an arbitrary callable.
type SpaAutoModelEncoder struct {
	Full      any
	InputType any
}

// Spa_withGeneratedModelEncoder is compiler plumbing, not a public Sky API.
// A forged generated-project flag plus an ordinary callable gains nothing.
func Spa_withGeneratedModelEncoder(encoder, cfg any) any {
	if proven, ok := encoder.(SpaAutoModelEncoder); ok {
		cfg = Spa_withModelEncoder(proven.Full, cfg)
		cfg = spaCfgSet(cfg, "GeneratedModelInputType", proven.InputType)
		return spaCfgSet(cfg, "GeneratedAutoModelEncoder", true)
	}
	return Spa_withModelEncoder(encoder, cfg)
}

type spaAutoModelField struct {
	index    int
	declared string
}

type spaAutoModelDescriptor struct {
	fields map[string]spaAutoModelField
	valid  bool
}

// Only immutable TYPE metadata is shared. No model/session values are cached.
var spaAutoModelDescriptors sync.Map // reflect.Type -> spaAutoModelDescriptor
var spaProjectionVariantType = reflect.TypeOf((*SkyVariant)(nil)).Elem()

// Skipping the previous FULL encoder also skips unrelated fields. Prove its
// entire type domain has no foreign marshaler/variant dispatch without walking
// any instance data. Back-edges are rejected, not assumed safe: foreign code can
// construct a cyclic slice graph even for Node{children: []Node}.
func spaProjectionTypeClosed(t reflect.Type, states map[reflect.Type]uint8, depth int) (ok bool) {
	// Named Go containers are opaque FFI references, not immutable Sky data.
	if t.Name() != "" && t.PkgPath() != "" &&
		(t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
		return false
	}
	if depth > 64 || t.Implements(spaProjectionVariantType) {
		return false
	}
	if state := states[t]; state != 0 {
		return state == 2 // 1: visiting/cycle; 2: validated; 3: rejected
	}
	states[t] = 1
	defer func() {
		if ok {
			states[t] = 2
		} else {
			states[t] = 3
		}
	}()
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice, reflect.Array:
		return spaProjectionTypeClosed(t.Elem(), states, depth+1)
	case reflect.Struct:
		if isSkyAdtType(t) {
			return false
		}
		if isSkyMaybeType(t) {
			tag, _ := t.FieldByName("Tag")
			just, _ := t.FieldByName("JustValue")
			return tag.Type.Kind() == reflect.Int && spaProjectionTypeClosed(just.Type, states, depth+1)
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" || f.Tag.Get("sky") == "" || !spaProjectionTypeClosed(f.Type, states, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Projection adds encodes, so even a canonical codec is not sufficient when a
// foreign Json.Value can invoke user-supplied MarshalJSON/variant methods.
// Initially admit only closed scalar/list/Maybe/tagged-record data. Unknown
// representations, excessive depth and large session graphs retain reference
// execution. This inspects SELECTED fields only, never unrelated model fields.
func spaProjectionClosed(v reflect.Value, depth int, budget *int) bool {
	if v.IsValid() && v.Type().Name() != "" && v.Type().PkgPath() != "" &&
		(v.Kind() == reflect.Slice || v.Kind() == reflect.Array || v.Kind() == reflect.Map) {
		return false
	}
	if !v.IsValid() || depth > 64 || *budget <= 0 {
		return false
	}
	*budget--
	if v.Type().Implements(spaProjectionVariantType) {
		return false // do not call foreign variant methods even to reject them
	}
	switch v.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	case reflect.Interface:
		return v.IsNil() || spaProjectionClosed(v.Elem(), depth+1, budget)
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !spaProjectionClosed(v.Index(i), depth+1, budget) {
				return false
			}
		}
		return true
	case reflect.Struct:
		t := v.Type()
		if isSkyAdtType(t) {
			return false
		}
		if isSkyMaybeType(t) {
			tag := v.FieldByName("Tag")
			if tag.Kind() != reflect.Int {
				return false
			}
			return tag.Int() != 0 || spaProjectionClosed(v.FieldByName("JustValue"), depth+1, budget)
		}
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.PkgPath != "" || f.Tag.Get("sky") == "" || !spaProjectionClosed(v.Field(i), depth+1, budget) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func spaAutoDescriptor(t reflect.Type) spaAutoModelDescriptor {
	if cached, ok := spaAutoModelDescriptors.Load(t); ok {
		return cached.(spaAutoModelDescriptor)
	}
	d := spaAutoModelDescriptor{fields: map[string]spaAutoModelField{}}
	if t.Kind() == reflect.Struct && isRecordType(t) && spaProjectionTypeClosed(t, map[reflect.Type]uint8{}, 0) {
		d.valid = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// Compiler-emitted record fields have explicit Sky tags. Reject
			// opaque/special runtime wrappers and unrecognised representations.
			if f.PkgPath != "" || f.Tag.Get("sky") == "" {
				d.valid = false
				break
			}
			key := skyColKey(f, true)
			if _, duplicate := d.fields[key]; duplicate {
				d.valid = false
				break
			}
			d.fields[key] = spaAutoModelField{index: i, declared: skyTagType(f)}
		}
	}
	actual, _ := spaAutoModelDescriptors.LoadOrStore(t, d)
	return actual.(spaAutoModelDescriptor)
}

func spaAutoSessionProjection(model any, protected []string) (out string, ok bool, visited int) {
	defer func() {
		if recover() != nil {
			out, ok = "", false
		}
	}()
	rv := reflect.ValueOf(model)
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return "", false, 0
	}
	if rv.Type().Implements(spaProjectionVariantType) {
		return "", false, 0
	}
	d := spaAutoDescriptor(rv.Type())
	if !d.valid {
		return "", false, 0
	}
	fields := make(map[string]any, len(protected))
	budget := 4096
	for _, key := range protected {
		f, exists := d.fields[key]
		if !exists {
			continue // absent in the full JSON object too
		}
		visited++
		if !spaProjectionClosed(rv.Field(f.index), 0, &budget) {
			return "", false, visited
		}
		value, err := codecAutoEncodeTyped(rv.Field(f.index), f.declared, true)
		if err != nil {
			return "", false, visited
		}
		fields[key] = value
	}
	b, err := json.Marshal(fields)
	return string(b), err == nil, visited
}

// A negative projection decision proves the FULL JSON cannot clear a session.
// A positive/unknown decision MUST retain the previous full encode, because an
// unrelated invalid old field can make that encode fail (then no sign-out).
func spaAutoSessionCannotClear(prev, next any, protected []string) (bool, int, int) {
	a, ok, visitedA := spaAutoSessionProjection(prev, protected)
	if !ok {
		return false, visitedA, len(a)
	}
	b, ok, visitedB := spaAutoSessionProjection(next, protected)
	return ok && !spaSessionClearedByStep(a, b, protected), visitedA + visitedB, len(a) + len(b)
}
