package rt

import (
	"math"
	"reflect"
)

const spaModelWitnessBudget = 256

// spaModelEncoding belongs to one app/encoder instance. It retains one actual
// immutable model and one successful full JSON result, never raw addresses
// without their owner. JSON retention is capped by the existing snapshot cap.
// This caches ONLY next-model encoding, not previous-model failure decisions.
type spaModelEncoding struct {
	full      func(any) string
	generated bool
	model     any
	json      string
}

func (e *spaModelEncoding) reset() { e.model, e.json = nil, "" }

func spaModelEncodingEligible(model any) (ok bool) {
	defer func() { _ = recover() }()
	t := reflect.TypeOf(model)
	return t != nil && t.Kind() == reflect.Struct && spaAutoDescriptor(t).valid
}

func (e *spaModelEncoding) encodeNext(model any) (out string, reused bool, compared int) {
	eligible := e.generated && spaModelEncodingEligible(model)
	if eligible && e.json != "" {
		var same bool
		same, compared = spaModelSame(e.model, model)
		if same {
			return e.json, true, compared
		}
	}
	// A failed new encode must not retain an unrelated older user's large
	// snapshot. In particular, do not leave the entry live until a success.
	e.reset()
	out = e.full(model)
	if eligible && out != "" && len(out) <= spaPersistMaxBytes {
		e.model, e.json = model, out
	}
	return out, false, compared
}

// spaModelSame is a bounded SUFFICIENT witness, never approximate/deep equality.
// A miss means encode normally. Its caller must establish the entire closed
// acyclic type domain first. Pointer checks apply only to immutable String/List
// representations, under the existing FFI read-only-slice contract. A List
// witness includes its visible length: sharing a backing block alone is not
// identity. No closures, maps, foreign handles or general pointers are admitted.
func spaModelSame(a, b any) (same bool, compared int) {
	budget := spaModelWitnessBudget
	defer func() {
		compared = spaModelWitnessBudget - budget
		if recover() != nil {
			same = false
		}
	}()
	same = spaModelValueSame(reflect.ValueOf(a), reflect.ValueOf(b), &budget)
	return
}

func spaModelValueSame(a, b reflect.Value, budget *int) bool {
	if *budget <= 0 || !a.IsValid() || !b.IsValid() || a.Type() != b.Type() {
		return false
	}
	*budget--
	switch a.Kind() {
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return a.Uint() == b.Uint()
	case reflect.Float32, reflect.Float64:
		return math.Float64bits(a.Float()) == math.Float64bits(b.Float())
	case reflect.String:
		if a.Len() != b.Len() {
			return false
		}
		if a.Pointer() == b.Pointer() {
			return true
		}
		return a.Len() <= 256 && a.String() == b.String()
	case reflect.Slice:
		return a.IsNil() == b.IsNil() && a.Len() == b.Len() && a.Pointer() == b.Pointer()
	case reflect.Array:
		for i := 0; i < a.Len(); i++ {
			if !spaModelValueSame(a.Index(i), b.Index(i), budget) {
				return false
			}
		}
		return true
	case reflect.Struct:
		if isSkyMaybeType(a.Type()) {
			tagA, tagB := a.FieldByName("Tag"), b.FieldByName("Tag")
			if tagA.Int() != tagB.Int() {
				return false
			}
			return tagA.Int() != 0 || spaModelValueSame(a.FieldByName("JustValue"), b.FieldByName("JustValue"), budget)
		}
		for i := 0; i < a.NumField(); i++ {
			if !spaModelValueSame(a.Field(i), b.Field(i), budget) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
