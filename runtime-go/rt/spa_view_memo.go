package rt

import (
	"math"
	"reflect"
	"sync"
	"unsafe"
)

// Source values only. Every target must still perform its current rendering,
// style, mount identity, handler and input-control passes.
const (
	spaViewMemoMaxEntries    = 128
	spaViewMemoMaxCaptures   = 64
	spaViewMemoMaxTypes      = 4096
	spaViewMemoValueBudget   = 131072
	spaViewMemoStringBudget  = 8 << 20
	spaViewMemoWitnessBudget = 4096
)

func SpaViewMemoType[T any]() any { return reflect.TypeOf((*T)(nil)).Elem() }

// Compiler-issued IotaEnum capability, distinct from raw reflection tokens.
// The lowerer emits it only for actual resolved IotaEnum declarations. Shape
// validation is defensive; neither type spelling nor registry entries confer
// authority, and no enum method is invoked during minting or admission.
type spaViewMemoEnumToken struct{ typ reflect.Type }

type spaViewMemoEnumValue interface{ SkyEnumName() string }

var spaViewMemoEnumValueType = reflect.TypeOf((*spaViewMemoEnumValue)(nil)).Elem()

func spaViewMemoEnumTypeValid(t reflect.Type) bool {
	return t != nil && t.Kind() == reflect.Int && t.Name() != "" && t.PkgPath() != "" &&
		t.NumMethod() == 1 && t.Implements(spaViewMemoEnumValueType)
}

func SpaViewMemoEnumType[T any]() any {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if !spaViewMemoEnumTypeValid(t) {
		return nil
	}
	return spaViewMemoEnumToken{typ: t}
}

// A second nonzero-width field prevents Go's sole-component interface boxing
// optimization from discarding an empty string's actual data owner.
type spaViewMemoCaptureBox[T any] struct {
	Value    T
	preserve byte
}

func (spaViewMemoCaptureBox[T]) spaViewMemoCaptureBox() {}

type spaViewMemoCaptureMarker interface{ spaViewMemoCaptureBox() }

var spaViewMemoCaptureMarkerType = reflect.TypeOf((*spaViewMemoCaptureMarker)(nil)).Elem()

func SpaViewMemoCapture[T any](value T) any { return spaViewMemoCaptureBox[T]{Value: value} }
func spaViewMemoCaptured(value any) (reflect.Value, bool) {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return reflect.Value{}, false
	}
	t := v.Type()
	if t.Kind() != reflect.Struct || t.PkgPath() != spaClosedRuntimePkg || !t.Implements(spaViewMemoCaptureMarkerType) || t.NumField() != 2 {
		return reflect.Value{}, false
	}
	a, b := t.Field(0), t.Field(1)
	if a.Name != "Value" || a.PkgPath != "" || a.Anonymous || b.Name != "preserve" || b.Anonymous || b.PkgPath != spaClosedRuntimePkg || b.Type.Kind() != reflect.Uint8 {
		return reflect.Value{}, false
	}
	return v.Field(0), true
}

type spaViewMemoKey struct{ site, visit int }
type spaViewMemoEntry struct {
	captures              []any
	result                any
	valueCost, stringCost int
}

// One boot/app owns one cache. Published maps and entries are immutable.
// Input owners may retain large backing allocations, as the immutable model
// itself does; these bounds cover entry count and admitted result traversal,
// not the transitive bytes of an arbitrary model capture.
type spaViewMemoEpoch struct{ marker byte }

type spaViewMemoCache struct {
	mu       sync.Mutex
	epoch    *spaViewMemoEpoch
	current  map[spaViewMemoKey]spaViewMemoEntry
	accepted map[reflect.Type]bool
	metadata *codecEnumRegistrySnapshot
}

type spaViewMemoFrame struct {
	mu                      sync.Mutex
	cache                   *spaViewMemoCache
	epoch                   *spaViewMemoEpoch
	previous                map[spaViewMemoKey]spaViewMemoEntry
	next                    map[spaViewMemoKey]spaViewMemoEntry
	visits                  map[int]int
	accepted                map[reflect.Type]bool
	metadata                *codecEnumRegistrySnapshot
	open, aborted, finished bool
	valuesLeft, stringsLeft int
	hits, builds            int
}

func (c *spaViewMemoCache) begin(tokens []any) *spaViewMemoFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch = &spaViewMemoEpoch{}
	metadata := codecEnumRegistry()
	if c.metadata != metadata {
		c.current = nil
	}
	c.metadata = metadata
	f := &spaViewMemoFrame{
		cache: c, epoch: c.epoch, previous: c.current, metadata: metadata,
		next: map[spaViewMemoKey]spaViewMemoEntry{}, visits: map[int]int{},
		accepted: map[reflect.Type]bool{}, open: true,
		valuesLeft: spaViewMemoValueBudget, stringsLeft: spaViewMemoStringBudget,
	}
	if len(tokens) > spaViewMemoMaxTypes {
		f.open = false
		return f
	}
	for _, token := range tokens {
		var t reflect.Type
		if enum, ok := token.(spaViewMemoEnumToken); ok {
			t = enum.typ
			if !spaViewMemoEnumTypeValid(t) {
				f.open = false
				return f
			}
		} else {
			var ok bool
			t, ok = token.(reflect.Type)
			if !ok || t == nil || reflect.TypeOf(token) != reflect.TypeOf(reflect.TypeOf(0)) || t.Kind() != reflect.Struct {
				f.open = false
				return f
			}
		}
		f.accepted[t] = true
	}
	matched := len(c.accepted) == len(f.accepted)
	for t := range f.accepted {
		if !c.accepted[t] {
			matched = false
			break
		}
	}
	if !matched {
		c.current = nil
		f.previous = nil
	}
	c.accepted = f.accepted
	return f
}

// Seal after source view returns, before target rendering/paint. A leaked
// context cannot insert a late entry or use a prior frame after this point.
func (f *spaViewMemoFrame) seal() { f.mu.Lock(); f.open = false; f.mu.Unlock() }

// Commit ONLY after paint succeeds. Abort invalidates the old cache too,
// releasing captures from an obsolete model after a failed view/paint.
func (f *spaViewMemoFrame) finish(success bool) {
	f.mu.Lock()
	if f.finished {
		f.mu.Unlock()
		return
	}
	f.finished = true
	f.open = false
	if !success {
		f.aborted = true
	}
	next := f.next
	metadata := f.metadata
	f.metadata = nil
	f.previous = nil
	f.next = nil
	f.visits = nil
	f.mu.Unlock()
	f.cache.mu.Lock()
	defer f.cache.mu.Unlock()
	if f.cache.epoch != f.epoch {
		return
	}
	if f.cache.metadata != metadata || metadata != codecEnumRegistry() {
		f.cache.current = nil
		return
	}
	if success && !f.aborted {
		f.cache.current = next
	} else {
		f.cache.current = nil
	}
}

func (f *spaViewMemoFrame) liveEpoch() bool {
	f.cache.mu.Lock()
	defer f.cache.mu.Unlock()
	return f.cache.epoch == f.epoch && f.cache.metadata == f.metadata && f.metadata == codecEnumRegistry()
}

func (c *spaViewMemoCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch = &spaViewMemoEpoch{}
	c.current = nil
	c.metadata = nil
}

// The compiler authenticates a pure, canonical map call and freezes every
// operand once before this invocation. site also identifies its specialization.
// The dependency bag is fresh scaffolding; its own slice pointer is NOT a key.
// No callable pointer/environment guesses are used. Never retain build.
func SpaViewMemoRegion(ctx any, site int, captures any, build func() any) any {
	f, ok := ctx.(*spaViewMemoFrame)
	deps, depsOK := captures.([]any)
	if !ok || f == nil || !depsOK || len(deps) > spaViewMemoMaxCaptures || site < 0 {
		return build()
	}
	values := make([]reflect.Value, len(deps))
	for i, dep := range deps {
		var valid bool
		values[i], valid = spaViewMemoCaptured(dep)
		if !valid {
			return build()
		}
	}
	f.mu.Lock()
	if !f.open || f.finished || f.aborted {
		f.mu.Unlock()
		return build()
	}
	if !f.liveEpoch() {
		f.open = false
		f.aborted = true
		f.mu.Unlock()
		return build()
	}
	if _, exists := f.visits[site]; !exists && len(f.visits) >= spaViewMemoMaxEntries {
		f.mu.Unlock()
		return build()
	}
	visit := f.visits[site]
	f.visits[site] = visit + 1
	key := spaViewMemoKey{site, visit}
	if len(f.next) >= spaViewMemoMaxEntries {
		f.mu.Unlock()
		return build()
	}
	old, found := f.previous[key]
	if found && len(old.captures) == len(deps) {
		budget := spaViewMemoWitnessBudget
		same := true
		for i := range deps {
			oldValue, valid := spaViewMemoCaptured(old.captures[i])
			if !valid || !spaViewMemoPhysicalSame(oldValue, values[i], &budget) {
				same = false
				break
			}
		}
		if same {
			if !f.liveEpoch() {
				f.open = false
				f.aborted = true
				f.mu.Unlock()
				return build()
			}
			f.hits++
			if old.valueCost <= f.valuesLeft && old.stringCost <= f.stringsLeft {
				f.valuesLeft -= old.valueCost
				f.stringsLeft -= old.stringCost
				f.next[key] = spaViewMemoEntry{captures: append([]any(nil), deps...), result: old.result, valueCost: old.valueCost, stringCost: old.stringCost}
			}
			f.mu.Unlock()
			return old.result
		}
	}
	// Admission never invokes methods. Metadata-only closed types avoid walking
	// large immutable input lists; open/dynamic values use a bounded visitor.
	valuesBefore, stringsBefore := f.valuesLeft, f.stringsLeft
	inputOK := true
	for _, v := range values {
		if !v.IsValid() {
			continue
		}
		if !spaViewMemoClosedType(v.Type(), f.accepted, map[reflect.Type]uint8{}, 0) && !f.dataOnly(v, 0) {
			inputOK = false
			break
		}
	}
	f.builds++
	f.mu.Unlock()
	// A panic propagates exactly once. The renderer's deferred finish(false)
	// discards all staged entries; there is no effect replay or Full restart.
	result := build()
	f.mu.Lock()
	defer f.mu.Unlock()
	if inputOK && f.open && !f.finished && !f.aborted && f.liveEpoch() && len(f.next) < spaViewMemoMaxEntries && f.dataOnly(reflect.ValueOf(result), 0) {
		f.next[key] = spaViewMemoEntry{captures: append([]any(nil), deps...), result: result, valueCost: valuesBefore - f.valuesLeft, stringCost: stringsBefore - f.stringsLeft}
	}
	return result
}

// This is a PHYSICAL capture witness, not the JSON witness: include slice cap,
// all inactive Maybe/ADT fields, string owners even when empty, and float bits.
func spaViewMemoPhysicalSame(a, b reflect.Value, budget *int) bool {
	if *budget <= 0 || a.IsValid() != b.IsValid() {
		return false
	}
	*budget--
	if !a.IsValid() {
		return true
	}
	if a.Type() != b.Type() {
		return false
	}
	t := a.Type()
	if spaClosedNominalDict(t) {
		return a.CanInterface() && b.CanInterface() && a.Interface().(spaClosedDictMarker).spaClosedDictSame(b.Interface())
	}
	switch a.Kind() {
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return spaViewMemoPhysicalSame(a.Elem(), b.Elem(), budget)
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() == b.Uint()
	case reflect.Float32:
		return spaViewMemoFloat32Bits(a) == spaViewMemoFloat32Bits(b)
	case reflect.Float64:
		return math.Float64bits(a.Float()) == math.Float64bits(b.Float())
	case reflect.String:
		x, y := a.String(), b.String()
		return len(x) == len(y) && unsafe.StringData(x) == unsafe.StringData(y)
	case reflect.Slice:
		return a.IsNil() == b.IsNil() && a.Len() == b.Len() && a.Cap() == b.Cap() && a.Pointer() == b.Pointer()
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !spaViewMemoPhysicalSame(a.Field(i), b.Field(i), budget) {
				return false
			}
		}
		return true
	case reflect.Array:
		for i := 0; i < a.Len(); i++ {
			if !spaViewMemoPhysicalSame(a.Index(i), b.Index(i), budget) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func spaViewMemoTypeMethodsAllowed(t reflect.Type, accepted map[reflect.Type]bool) bool {
	return t.NumMethod() == 0 || accepted[t] || isSkyMaybeType(t) || spaClosedNominalTuple(t) || spaClosedNominalDict(t) || isSkyAdtType(t)
}

func spaViewMemoClosedType(t reflect.Type, accepted map[reflect.Type]bool, seen map[reflect.Type]uint8, depth int) (ok bool) {
	if ffiDefined(t) && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
		return false
	}
	if depth > 64 || !spaViewMemoTypeMethodsAllowed(t, accepted) {
		return false
	}
	if state := seen[t]; state != 0 {
		return state == 2
	}
	seen[t] = 1
	defer func() {
		if ok {
			seen[t] = 2
		} else {
			seen[t] = 3
		}
	}()
	if spaClosedNominalDict(t) {
		return spaViewMemoClosedType(reflect.Zero(t).Interface().(spaClosedDictMarker).spaClosedDictElement(), accepted, seen, depth+1)
	}
	if codecOpaqueStruct(t) {
		return false
	}
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice, reflect.Array:
		return spaViewMemoClosedType(t.Elem(), accepted, seen, depth+1)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || field.Anonymous || !spaViewMemoClosedType(field.Type, accepted, seen, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (f *spaViewMemoFrame) dataOnly(v reflect.Value, depth int) bool {
	if depth > 64 || f.valuesLeft <= 0 {
		return false
	}
	f.valuesLeft--
	if !v.IsValid() {
		return true
	}
	t := v.Type()
	if ffiDefined(t) && (t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
		return false
	}
	if !spaViewMemoTypeMethodsAllowed(t, f.accepted) {
		return false
	}
	if spaClosedNominalDict(t) {
		if !spaViewMemoClosedType(t, f.accepted, map[reflect.Type]uint8{}, 0) || !v.CanInterface() {
			return false
		}
		// Inputs have a metadata shortcut above. RESULTS must account for
		// retained dictionary entries and strings under the shared budget.
		d := v.Interface().(skyDictAny)
		if d.dictLen() < 0 || d.dictLen() > f.valuesLeft/2 {
			return false
		}
		valid := true
		d.dictRangeAny(func(k string, value any) bool {
			valid = f.dataOnly(reflect.ValueOf(k), depth+1) && f.dataOnly(reflect.ValueOf(value), depth+1)
			return valid
		})
		return valid
	}
	if codecOpaqueStruct(t) {
		return false
	}
	switch v.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	case reflect.String:
		if v.Len() > f.stringsLeft {
			return false
		}
		f.stringsLeft -= v.Len()
		return true
	case reflect.Interface:
		return v.IsNil() || f.dataOnly(v.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		if v.Len() > f.valuesLeft {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if !f.dataOnly(v.Index(i), depth+1) {
				return false
			}
		}
		return true
	case reflect.Struct:
		if v.NumField() > f.valuesLeft {
			return false
		}
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || field.Anonymous || !f.dataOnly(v.Field(i), depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Reflect.Float and float-to-float Convert can quiet signaling NaNs. Exact
// typed Set copies the storage; reading its bits performs no FP conversion.
func spaViewMemoFloat32Bits(v reflect.Value) uint32 {
	storage := reflect.New(v.Type()).Elem()
	storage.Set(v)
	return *(*uint32)(storage.Addr().UnsafePointer())
}

// Per-app driver ownership. No render context is ambient: only the private
// ViewMemo adapter and its typed clones receive this frame explicitly.
type spaViewMemoApp struct {
	cache spaViewMemoCache
	view  func(any) any
	memo  func(any, any) any
	types []any
}

type spaViewMemoMetrics struct{ Frames, Hits, Builds, Resets int }

func newSpaViewMemoApp(fns SpaFns) *spaViewMemoApp {
	return &spaViewMemoApp{view: fns.View, memo: fns.ViewMemo, types: append([]any(nil), fns.ViewMemoTypes...)}
}
func (a *spaViewMemoApp) begin() *spaViewMemoFrame     { return a.cache.begin(a.types) }
func (a *spaViewMemoApp) seal(frame *spaViewMemoFrame) { frame.seal() }
func (a *spaViewMemoApp) finish(frame *spaViewMemoFrame, success bool) {
	frame.finish(success)
	if m := spaActiveStepMetrics; m != nil {
		frame.mu.Lock()
		m.ViewMemo.Frames++
		m.ViewMemo.Hits += frame.hits
		m.ViewMemo.Builds += frame.builds
		frame.mu.Unlock()
	}
}
func (a *spaViewMemoApp) reset() {
	a.cache.reset()
	if m := spaActiveStepMetrics; m != nil {
		m.ViewMemo.Resets++
	}
}
func (a *spaViewMemoApp) render(model any, reference bool, paint func(any)) {
	if reference || a.memo == nil {
		paint(a.view(model))
		return
	}
	frame := a.begin()
	success := false
	defer func() { a.finish(frame, success) }()
	html := a.memo(model, frame)
	a.seal(frame)
	paint(html)
	success = true
}
