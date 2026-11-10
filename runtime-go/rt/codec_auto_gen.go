package rt

// codec_auto_gen.go: generated typed codecs for Codec.auto (v0.29.9).
//
// The compiler (rust/crates/lower/src/lower/auto_codec.rs) emits, for each
// closed record type it sees as the witness of a canonical `Codec.auto` or
// `Codec.autoCamel` call, three reflection-free functions and one init()
// registration:
//
//   tree(v T, c) (any, error)          the phase-1 tree Codec_autoEnc builds
//   app(b []byte, v T, c) ([]byte, error)  the JSON bytes of that tree
//   dec(raw any, c) (T, error)          the value Codec_autoDecoder builds
//
// Codec_autoEnc, Codec_autoDecoder and Codec_autoJson look the value's
// reflect.Type up here once per call and use the generated codec, else the
// reflective walker. The registry is written only by init() and is read-only
// after it, so lookups take no lock.
//
// Exactness. A generated codec must give the same tree, the same bytes, the
// same value and the same error as the reflective walker, for every input:
//
//   - Registration compares the emitted keys and declared tags of every
//     record type with codecAutoFields (the walker's own plan) and refuses on
//     any difference, a duplicate key, a handle type or a type outside the
//     closed set (Bool, Int, Float, String, registered enums, records, Maybe,
//     List, Dict). A refused type keeps the reflective path.
//   - Every decode check and every error string comes from the AutoDec*
//     helpers below, which the reflective walker calls too, in the same
//     order.
//   - Leaf bytes come from the json_tree_writer.go functions. A value whose
//     bytes the writer would hand to json.Marshal (a NaN or infinite Float)
//     makes app return errAutoFallback, and the caller runs the reflective
//     path for the exact bytes or error.
//   - Enum names come from one codecEnumRegistry() snapshot per call. A
//     declared name that has an ADT owner in that snapshot (the walker would
//     take a nominal branch) sends the whole call to the reflective path.
//
// Reference mode (autoCodecReference, set with the Sky.Spa step profiler's
// reference switch) forces the reflective path for A/B measurement.
//
// Origin: doc 14 §4.2 wire decode (no R-number). Lever: §5.3 typed kernel
// entry points.

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync/atomic"
)

// AutoCodecCtx is the per-call context of a generated codec.
type AutoCodecCtx struct {
	enums *codecEnumRegistrySnapshot
}

// errAutoFallback tells the caller to run the reflective path, which gives
// the exact bytes or error.
var errAutoFallback = errors.New("Codec.auto: generated codec fallback")

// AutoCodecShape is one record type of a generated codec: its emitted keys and
// declared tags, in field order.
type AutoCodecShape struct {
	t        reflect.Type
	keys     []string
	declared []string
}

// AutoShape is the shape of record type T.
func AutoShape[T any](keys, declared []string) AutoCodecShape {
	return AutoCodecShape{t: reflect.TypeOf((*T)(nil)).Elem(), keys: keys, declared: declared}
}

type autoEnumNamer interface{ SkyEnumName() string }

var autoEnumNamerType = reflect.TypeOf((*autoEnumNamer)(nil)).Elem()

type autoCodecKey struct {
	t     reflect.Type
	snake bool
}

type autoCodecEntry struct {
	tree     func(v any, c *AutoCodecCtx) (any, error)
	app      func(b []byte, v any, c *AutoCodecCtx) ([]byte, error)
	dec      func(raw any, c *AutoCodecCtx) (any, error)
	declared []string // every declared tag the codec reads, for the owner check
}

// autoCodecRegistry is written only in init() (RegisterAutoCodec) and only
// read after it.
var autoCodecRegistry = map[autoCodecKey]*autoCodecEntry{}

// autoCodecRefusals records why a registration was refused (tests, sky doctor).
var autoCodecRefusals []string

// autoCodecReference forces the reflective path (A/B reference mode).
var autoCodecReference atomic.Bool

// AutoCodecRefusals is the list of refused registrations, for tests.
func AutoCodecRefusals() []string { return append([]string(nil), autoCodecRefusals...) }

// RegisterAutoCodec registers the generated codec of root record type T for
// one key style (snake = Codec.auto, else Codec.autoCamel). shapes lists T and
// every record type T reaches. It refuses, and leaves the reflective path in
// place, when any shape differs from the walker's plan or a type is outside
// the closed set. Call only from init().
func RegisterAutoCodec[T any](snake bool, shapes []AutoCodecShape,
	tree func(T, *AutoCodecCtx) (any, error),
	app func([]byte, T, *AutoCodecCtx) ([]byte, error),
	dec func(any, *AutoCodecCtx) (T, error)) bool {
	t := reflect.TypeOf((*T)(nil)).Elem()
	declared, problem := autoCodecVerify(t, snake, shapes)
	if problem != "" {
		autoCodecRefusals = append(autoCodecRefusals, fmt.Sprintf("%s (snake=%v): %s", t, snake, problem))
		return false
	}
	autoCodecRegistry[autoCodecKey{t, snake}] = &autoCodecEntry{
		tree: func(v any, c *AutoCodecCtx) (any, error) { return tree(v.(T), c) },
		app:  func(b []byte, v any, c *AutoCodecCtx) ([]byte, error) { return app(b, v.(T), c) },
		dec: func(raw any, c *AutoCodecCtx) (any, error) {
			v, err := dec(raw, c)
			if err != nil {
				return nil, err
			}
			return v, nil
		},
		declared: declared,
	}
	return true
}

// autoCodecVerify checks a registration against the reflective walker's own
// view of the type. It returns every declared tag (for the per-call owner
// check) or the reason to refuse.
func autoCodecVerify(root reflect.Type, snake bool, shapes []AutoCodecShape) ([]string, string) {
	if !isRecordType(root) {
		return nil, "not a record type"
	}
	if dup := codecAutoDuplicateKey(root, snake); dup != "" {
		return nil, "duplicate key: " + dup
	}
	byType := map[reflect.Type]AutoCodecShape{}
	for _, s := range shapes {
		byType[s.t] = s
	}
	var declared []string
	visiting := map[reflect.Type]bool{}
	var walk func(t reflect.Type) string
	walk = func(t reflect.Type) string {
		switch {
		case t.Kind() == reflect.String && t == reflect.TypeOf(""):
			return ""
		case t.Kind() == reflect.Bool && t == reflect.TypeOf(false):
			return ""
		case t.Kind() == reflect.Float64 && t == reflect.TypeOf(float64(0)):
			return ""
		case t.Kind() == reflect.Int:
			// int, or a named enum (its tag is checked with the record).
			// Implements, not MethodByName: a MethodByName call keeps every
			// exported method of every type in the binary (the linker's
			// reflect-method rule), which costs a Wasm client tens of KB.
			if t.NumMethod() > 0 && !t.Implements(autoEnumNamerType) {
				return "int type with methods: " + t.String()
			}
			return ""
		case t.Kind() == reflect.Slice:
			return walk(t.Elem())
		case isSkyMaybeType(t):
			return walk(codecMaybeJustType(t))
		case isSkyDictType(t):
			return walk(reflect.Zero(t).Interface().(skyDictAny).dictValueType())
		case t.Kind() == reflect.Struct:
			if visiting[t] {
				return "recursive type " + t.String()
			}
			if !isRecordType(t) || t == reflect.TypeOf(JsonValue{}) || t == reflect.TypeOf(SkySet{}) ||
				t.Implements(reflect.TypeOf((*SkyVariant)(nil)).Elem()) || t.NumField() == 0 || codecOpaqueStruct(t) {
				return "not a closed record: " + t.String()
			}
			shape, ok := byType[t]
			if !ok {
				return "no shape for " + t.String()
			}
			plan := codecAutoFields(t, snake)
			if len(plan.fields) != t.NumField() || len(shape.keys) != len(plan.fields) || len(shape.declared) != len(plan.fields) {
				return "field count differs for " + t.String()
			}
			visiting[t] = true
			defer delete(visiting, t)
			for i, f := range plan.fields {
				if f.index != i || shape.keys[i] != f.key || shape.declared[i] != f.declared {
					return fmt.Sprintf("field %d of %s differs (emitted %q %q, plan %q %q)", i, t, shape.keys[i], shape.declared[i], f.key, f.declared)
				}
				if codecHandleDeclared(f.declared) {
					return "handle type " + f.declared
				}
				declared = append(declared, f.declared)
				if p := walk(t.Field(i).Type); p != "" {
					return p
				}
			}
			return ""
		}
		return "type outside the closed set: " + t.String()
	}
	if p := walk(root); p != "" {
		return nil, p
	}
	return declared, ""
}

// autoCodecFor is the generated codec for a value of type t, or nil. It also
// returns the context to call it with: one enum registry snapshot. A declared
// tag that has an ADT owner in that snapshot means the walker would take a
// nominal branch, so the call goes to the walker (nil).
func autoCodecFor(t reflect.Type, snake bool) (*autoCodecEntry, *AutoCodecCtx) {
	if t == nil || len(autoCodecRegistry) == 0 || autoCodecReference.Load() {
		return nil, nil
	}
	e := autoCodecRegistry[autoCodecKey{t, snake}]
	if e == nil {
		return nil, nil
	}
	snap := codecEnumRegistry()
	if len(snap.owners) > 0 {
		for _, d := range e.declared {
			if codecAutoDeclaredOwnerAt(d, snap) != "" {
				return nil, nil
			}
		}
	}
	return e, &AutoCodecCtx{enums: snap}
}

// ── Encode helpers (tree) ────────────────────────────────────────────────────

// AutoObj is the walker's object node: keys in field order. An empty object
// (an empty Dict) has nil slices, as the walker's appends leave them.
func AutoObj(keys []string, vals []any) any {
	if len(keys) == 0 {
		return jsonOrderedObject{}
	}
	return jsonOrderedObject{keys: keys, vals: vals}
}

// AutoJust is the walker's Just node (codecAutoJust).
func AutoJust(v any, err error) (any, error) { return codecAutoJust(v, err) }

// Enum is a declared enum value as the walker writes it: its name, or the
// ordinal as an Int when the name is unknown.
func (c *AutoCodecCtx) Enum(declared string, ord int64) any {
	if codecAutoEnumRegisteredAt(declared, c.enums) {
		if name, ok := codecAutoEnumNameAt(declared, int(ord), c.enums); ok {
			return name
		}
	}
	return ord
}

// AutoDictSorted is a Dict's entries in the walker's order: stored keys sorted
// byte-wise.
func AutoDictSorted[V any](d SkyDict[V]) ([]string, []V) {
	type kv struct {
		k string
		v V
	}
	pairs := make([]kv, 0, d.dictLen())
	d.dictRangeAny(func(k string, v any) bool {
		tv, _ := v.(V)
		pairs = append(pairs, kv{k, tv})
		return true
	})
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	keys := make([]string, len(pairs))
	vals := make([]V, len(pairs))
	for i, p := range pairs {
		keys[i], vals[i] = p.k, p.v
	}
	return keys, vals
}

// ── Encode helpers (bytes) ───────────────────────────────────────────────────

var autoUnlimitedWriter = jsonTreeWriter{limit: -1}

// AutoAppendString writes s as the tree writer writes a string node.
func AutoAppendString(b []byte, s string) []byte {
	w := autoUnlimitedWriter
	return w.appendString(b, s)
}

// AutoAppendInt writes an Int node.
func AutoAppendInt(b []byte, n int64) []byte { return strconv.AppendInt(b, n, 10) }

// AutoAppendBool writes a Bool node.
func AutoAppendBool(b []byte, x bool) []byte {
	if x {
		return append(b, jsonTrueBytes...)
	}
	return append(b, jsonFalseBytes...)
}

// AutoAppendFloat writes a Float node. NaN and infinities are delegated to
// json.Marshal by the tree writer (an error), so they fall back.
func AutoAppendFloat(b []byte, f float64) ([]byte, error) {
	if f != f || f > maxFloat64 || f < -maxFloat64 {
		return nil, errAutoFallback
	}
	return jsonAppendFloat64(b, f), nil
}

const maxFloat64 = 1.7976931348623157e308

// AppendEnum writes an enum value as Enum gives it.
func (c *AutoCodecCtx) AppendEnum(b []byte, declared string, ord int64) []byte {
	switch x := c.Enum(declared, ord).(type) {
	case string:
		return AutoAppendString(b, x)
	default:
		return AutoAppendInt(b, ord)
	}
}

// ── Decode helpers (shared with the reflective walker) ───────────────────────

// AutoDecString reads a String.
func AutoDecString(raw any) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("Codec.auto: expected String, got %s", jsonValueKind(raw))
	}
	return s, nil
}

// AutoDecBool reads a Bool.
func AutoDecBool(raw any) (bool, error) {
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("Codec.auto: expected Bool, got %s", jsonValueKind(raw))
	}
	return b, nil
}

// AutoDecInt reads an Int (codecStrictInt).
func AutoDecInt(raw any) (int64, error) { return codecStrictInt(raw) }

// AutoDecFloat reads a Float.
func AutoDecFloat(raw any) (float64, error) {
	f, ok := jsonDecodeFloat(raw)
	if !ok {
		return 0, fmt.Errorf("Codec.auto: expected Float, got %s", jsonValueKind(raw))
	}
	return f, nil
}

// AutoDecEnumName reads a registered enum's name as its ordinal.
func AutoDecEnumName(declared string, raw any) (int64, error) {
	name := codecRawStr(raw)
	if ord, ok := enumOrdinalForName(declared, name); ok {
		return int64(ord), nil
	}
	// Unknown enum value: error rather than silently defaulting to ordinal 0
	// (the first variant), which was silent corruption (C2).
	return 0, fmt.Errorf("Codec.auto: unknown %s value %q", declared, name)
}

// AutoDecEnum reads a declared enum: by name when it is registered, else as
// the Int the walker falls back to.
func AutoDecEnum(declared string, raw any) (int64, error) {
	if isRegisteredEnum(declared) {
		return AutoDecEnumName(declared, raw)
	}
	return AutoDecInt(raw)
}

// AutoDecList reads an Array.
func AutoDecList(raw any) ([]any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("Codec.auto: expected Array, got %s", jsonValueKind(raw))
	}
	return items, nil
}

// AutoDecObject reads the object of record type typeName.
func AutoDecObject(raw any, typeName string) (map[string]any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Codec.auto: expected object for %s", typeName)
	}
	return m, nil
}

// AutoDecField reads one field. A required (non-Maybe) field absent from the
// object is a decode error (C2). A Maybe field may be omitted: its value is
// nil, which reads as Nothing.
func AutoDecField(m map[string]any, key string, optional bool) (any, error) {
	val, present := m[key]
	if !present && !optional {
		return nil, fmt.Errorf("Codec.auto: missing required field %q", key)
	}
	return val, nil
}

// AutoDecMaybe reports whether raw is a Just (false: Nothing).
func AutoDecMaybe(raw any) (bool, error) {
	if raw == nil {
		return false, nil
	}
	if _, ok := raw.(codecJustNull); ok {
		return false, fmt.Errorf("Codec.auto: %s", codecJustNullMsg)
	}
	return true, nil
}

// AutoDecDict reads a Dict's object: keys sorted byte-wise, so the first
// failing entry (and its error) does not depend on map order.
func AutoDecDict(raw any) ([]string, []any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("Codec.auto: expected object for Dict, got %s", jsonValueKind(raw))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]any, len(keys))
	for i, k := range keys {
		vals[i] = m[k]
	}
	return keys, vals, nil
}

// AutoDictBuild builds the Dict a decode read (dictFromEntries, foreign).
func AutoDictBuild[V any](keys []string, vals []V) SkyDict[V] {
	return dictBuild(keys, vals, true)
}

// AutoDecIntT reads an Int as the Go int a record field holds.
func AutoDecIntT(raw any) (int, error) {
	n, err := AutoDecInt(raw)
	return int(n), err
}

// AutoDecEnumT reads a declared enum as its Go type.
func AutoDecEnumT[E ~int](declared string, raw any) (E, error) {
	n, err := AutoDecEnum(declared, raw)
	return E(n), err
}

// Codec_autoJson : Bool -> a -> Maybe String. The JSON text a generated codec
// writes for record, in one pass with no intermediate tree, or Nothing when
// the record's type has no generated codec or a value needs the walker (a NaN
// or infinite Float). `Codec.toJson` tries it first and otherwise encodes the
// walker's tree, so the text, or the error, is always the walker's.
func Codec_autoJson(snakeArg, record any) any { return Codec_autoJsonT(AsBool(snakeArg), record) }

// Codec_autoJsonT is Codec_autoJson with a proven Bool (the typed twin).
func Codec_autoJsonT(snake bool, record any) SkyMaybe[string] {
	gen, ctx := autoCodecFor(reflect.TypeOf(record), snake)
	if gen == nil {
		return Nothing[string]()
	}
	b, err := gen.app(make([]byte, 0, 256), record, ctx)
	if err != nil {
		return Nothing[string]()
	}
	return Just(string(b))
}

// AutoCodecUseReference turns the reflective reference path on or off for
// every Codec.auto call (A/B tests and benchmarks of a compiled program).
func AutoCodecUseReference(on bool) { autoCodecReference.Store(on) }

// AutoCodecRegistered reports whether value's type has a generated codec for
// this key style (tests of a compiled program).
func AutoCodecRegistered(value any, snake bool) bool {
	return autoCodecRegistry[autoCodecKey{reflect.TypeOf(value), snake}] != nil
}
