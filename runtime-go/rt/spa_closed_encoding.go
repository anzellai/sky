package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
)

// This prototype decomposes only the existing closed codec domain. Lists and
// special declared-type dispatch remain canonical atomic leaves. Primitive Dict
// summaries derive oversize NoWrite decisions; under-cap output stays canonical.
// The enclosing mixed cache must retain the CURRENT root owner.
const spaClosedEncodingNodeLimit = 4096
const spaClosedEncodingDepthLimit = 64

type spaClosedRawKind uint8

const (
	spaClosedRawOther spaClosedRawKind = iota
	spaClosedRawNil
	spaClosedRawJustNull
)

type spaClosedNodeKind uint8

const (
	spaClosedAtomic spaClosedNodeKind = iota
	spaClosedRecord
	spaClosedMaybe
	spaClosedUnsupported
)

type spaClosedEncodingNode struct {
	type_    reflect.Type
	declared string
	kind     spaClosedNodeKind
	rawKind  spaClosedRawKind
	size     uint64
	null     bool
	raw      string // atomic raw ONLY, charged against the single global byte ledger
	children []*spaClosedEncodingNode
	dict     *spaClosedDictSummary
}

type spaClosedEncodingBudget struct {
	NodesLeft, BytesLeft, DepthLimit int
	CapBytes                         int
	Profile                          bool
	Pins                             spaClosedLazyPins
	Deferred                         spaClosedDeferredWork
}

func spaNewClosedEncodingBudget(capBytes int) *spaClosedEncodingBudget {
	return &spaClosedEncodingBudget{NodesLeft: spaClosedEncodingNodeLimit, BytesLeft: spaPersistMaxBytes, DepthLimit: spaClosedEncodingDepthLimit, CapBytes: capBytes}
}

type spaClosedEncodingOptions struct {
	NeedRaw bool
	// Missing bytes imply a whole selected snapshot is oversized. Protected
	// identity must instead set this false, even for huge closed values.
	AllowOversizeWithoutRaw bool
	// Excluded/previous-only roots should reserve raw retention for stored data.
	DiscardRawRetention bool
}

type spaClosedEncodingWork struct {
	Conversions, Marshals, Reuses, ComparedNodes int
	Nodes, RetainedBytes, BudgetFallbacks        int
	EncodedBytes                                 uint64
}

// Plans are invocation-local. Lazy leaves may capture CURRENT values/pinned
// metadata, but cache nodes NEVER point to plans or snapshots. Dict summaries
// may retain typed private subtrees only when reachable from the CURRENT owner.
type spaClosedBytePlan struct {
	raw      string
	keys     []string
	children []*spaClosedBytePlan
	missing  bool
	lazy     *spaClosedLazyLeaf
}

func (p *spaClosedBytePlan) appendTo(b *strings.Builder) bool {
	if p == nil || p.missing {
		return false
	}
	if p.lazy != nil {
		if !p.lazy.resolve() {
			return false
		}
		p.raw = p.lazy.raw
		p.lazy = nil // resolved plans release captured values/snapshots early
	}
	if p.raw != "" {
		b.WriteString(p.raw)
		return true
	}
	b.WriteByte('{')
	for i, child := range p.children {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(p.keys[i])
		b.WriteByte(':')
		if !child.appendTo(b) {
			return false
		}
	}
	b.WriteByte('}')
	return true
}

type spaClosedEncodingResult struct {
	Supported     bool
	ConversionErr error // observe immediately in Phase A
	MarshalErr    error // observe only at enclosing root field's Phase-B position
	Size          uint64
	Null          bool
	RawKind       spaClosedRawKind
	SameJSON      bool
	Cache         *spaClosedEncodingNode
	Plan          *spaClosedBytePlan
	Work          spaClosedEncodingWork
}

// Materialization may resolve certified lazy CLOSED leaves through pinned
// canonical encoding. It never invokes unknown callbacks or global enum reads.
func (r spaClosedEncodingResult) materialize(limit uint64) (string, bool) {
	if !r.Supported || r.ConversionErr != nil || r.MarshalErr != nil || r.Size > limit || r.Size > uint64(^uint(0)>>1) {
		return "", false
	}
	var b strings.Builder
	b.Grow(int(r.Size))
	if !r.Plan.appendTo(&b) || uint64(b.Len()) != r.Size {
		return "", false
	}
	return b.String(), true
}

// Caller must pass a prior node ONLY with its original owner's matching field
// value and the enclosing cache's matching enum epoch. One shared budget must
// span ALL root fields; never allocate a fresh retention budget per child.
func spaEncodeClosed(value, old reflect.Value, prior *spaClosedEncodingNode, declared string, previousEpoch, epoch *codecEnumRegistrySnapshot, options spaClosedEncodingOptions, budget *spaClosedEncodingBudget) (out spaClosedEncodingResult) {
	if !value.IsValid() || budget == nil || budget.CapBytes <= 0 || budget.CapBytes > spaPersistMaxBytes ||
		!spaClosedTypeEligible(value.Type()) {
		return out // caller executes the ordinary unknown path, with no work done
	}
	if previousEpoch == nil || previousEpoch != epoch {
		prior = nil
	}
	ctx := spaClosedEncodeContext{epoch: epoch, options: options, budget: budget}
	out = ctx.encode(value, old, prior, declared, 0)
	out.Work = ctx.work
	// Concurrent/global metadata changes never cause a restart. Keep captured
	// results but refuse to save certificates spanning more than one epoch.
	if epoch == nil || epoch != codecEnumRegistry() {
		out.Cache = nil
	}
	return out
}

type spaClosedEncodeContext struct {
	epoch   *codecEnumRegistrySnapshot
	options spaClosedEncodingOptions
	budget  *spaClosedEncodingBudget
	work    spaClosedEncodingWork
}

func (c *spaClosedEncodeContext) retain(n *spaClosedEncodingNode, raw string) {
	if n != nil && !c.options.DiscardRawRetention && len(raw) <= c.budget.BytesLeft {
		n.raw = raw
		c.budget.BytesLeft -= len(raw)
		c.work.RetainedBytes += len(raw)
	}
}

var spaClosedEligibleTypes sync.Map // immutable reflect.Type -> bool, no values/owners

func spaClosedTypeEligible(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if cached, ok := spaClosedEligibleTypes.Load(t); ok {
		return cached.(bool)
	}
	eligible := spaClosedOwnDomain(t, map[reflect.Type]uint8{}, 0)
	spaClosedEligibleTypes.Store(t, eligible)
	return eligible
}

// Keep a PRIVATE fixed domain instead of inheriting future widening of the
// shared closed gate. Every canonical special struct dispatch is excluded.
func spaClosedSpecialStruct(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && (t.Implements(skyVariantIfaceType) || isSkyAdtType(t) ||
		isSkyDictType(t) || t == reflect.TypeOf(SkySet{}) || t == reflect.TypeOf(JsonValue{}) || codecOpaqueStruct(t))
}

func spaClosedOwnDomain(t reflect.Type, seen map[reflect.Type]uint8, depth int) (ok bool) {
	// Named Go containers remain opaque references at the FFI boundary.
	// Actual Sky lists/array aliases are unnamed; trusted runtime Dict/tuple
	// representations pass their separate nominal checks below.
	if t.Name() != "" && t.PkgPath() != "" &&
		(t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map) {
		return false
	}
	if depth > spaClosedEncodingDepthLimit || t.Implements(spaProjectionVariantType) {
		return false
	}
	if spaClosedNominalDict(t) {
		return spaClosedOwnDomain(reflect.Zero(t).Interface().(spaClosedDictMarker).spaClosedDictElement(), seen, depth+1)
	}
	if spaClosedNominalTuple(t) {
		for i := 0; i < t.NumField(); i++ {
			if !spaClosedOwnDomain(t.Field(i).Type, seen, depth+1) {
				return false
			}
		}
		return true
	}
	if spaClosedSpecialStruct(t) {
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
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice, reflect.Array:
		return spaClosedOwnDomain(t.Elem(), seen, depth+1)
	case reflect.Struct:
		if isSkyMaybeType(t) {
			return spaClosedOwnDomain(t.Field(1).Type, seen, depth+1)
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" || f.Anonymous || f.Tag.Get("sky") == "" || !spaClosedOwnDomain(f.Type, seen, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func spaClosedDispatch(value reflect.Value, declared string) (spaClosedNodeKind, string) {
	t := value.Type()
	if (spaClosedNominalTuple(t) || spaClosedNominalDict(t)) && spaClosedTypeEligible(t) {
		return spaClosedAtomic, ""
	}
	if spaClosedSpecialStruct(t) {
		return spaClosedUnsupported, ""
	}
	// These branches precede structural record/Maybe dispatch in the codec.
	if isRegisteredEnum(declared) && value.Kind() >= reflect.Int && value.Kind() <= reflect.Int64 {
		return spaClosedAtomic, ""
	}
	if inner, ok := strings.CutPrefix(declared, "rt.SkyMaybe["); ok && isSkyMaybeType(t) {
		return spaClosedMaybe, strings.TrimSuffix(inner, "]")
	}
	if args := codecDeclaredTupleArgs(declared); args != nil && t.Kind() == reflect.Struct && t.NumField() == len(args) {
		return spaClosedAtomic, "" // preserve declared tuple element metadata
	}
	if t.Kind() == reflect.Struct {
		if isSkyMaybeType(t) {
			return spaClosedMaybe, ""
		} // value-based fallback
		return spaClosedRecord, ""
	}
	return spaClosedAtomic, "" // lists/arrays/scalars stay canonical atomic leaves
}

func (c *spaClosedEncodeContext) encode(value, old reflect.Value, prior *spaClosedEncodingNode, declared string, depth int) (out spaClosedEncodingResult) {
	out.Supported = true
	// Nominal declared-type dispatch precedes record/Maybe/null dispatch in
	// canonical conversion, even for foreign tagged closed composite values.
	// Use the invocation's pinned owner metadata, never a live registry read.
	kind, inner := spaClosedAtomic, ""
	owner := codecAutoDeclaredOwnerAt(declared, c.epoch)
	if owner != skyDecimalOwner && owner != skyMoneyOwner && owner != skyCurrencyOwner {
		kind, inner = spaClosedDispatch(value, declared)
	}
	if kind == spaClosedUnsupported {
		return spaClosedEncodingResult{}
	}
	// The unit budget includes BOTH node headers and retained child slots.
	// Reserve a parent's entire direct width before visiting its children;
	// otherwise an exhausted budget could still retain millions of nil slots.
	slots := 0
	if kind == spaClosedRecord {
		slots = value.NumField()
	}
	if kind == spaClosedMaybe && value.FieldByName("Tag").Int() == 0 {
		slots = 1
	}
	cacheable := c.budget.NodesLeft > 0 && slots < c.budget.NodesLeft && depth <= c.budget.DepthLimit
	if !cacheable {
		// Choose atomic fallback BEFORE traversing this subtree. No partially
		// converted subtree is restarted and no new public proof is granted.
		c.work.BudgetFallbacks++
		return c.atomicWithOld(value, reflect.Value{}, nil, declared, false, depth)
	}
	c.budget.NodesLeft -= 1 + slots
	c.work.Nodes++
	node := &spaClosedEncodingNode{type_: value.Type(), declared: declared, kind: kind}
	if slots != 0 {
		node.children = make([]*spaClosedEncodingNode, 0, slots)
	}
	usable := prior != nil && prior.type_ == value.Type() && prior.declared == declared && prior.kind == kind &&
		old.IsValid() && old.Type() == value.Type() && c.epoch != nil && c.epoch == codecEnumRegistry()
	if !usable {
		prior = nil
	}
	if kind == spaClosedAtomic {
		return c.atomicWithOld(value, old, prior, declared, true, depth)
	}
	if kind == spaClosedMaybe {
		tag := value.FieldByName("Tag").Int()
		if tag != 0 {
			out.Size, out.Null, out.RawKind = 4, true, spaClosedRawNil
			out.Plan = &spaClosedBytePlan{raw: "null"}
			out.SameJSON = prior != nil && prior.rawKind == spaClosedRawNil
		} else {
			var previousChild *spaClosedEncodingNode
			var oldChild reflect.Value
			if prior != nil && len(prior.children) == 1 {
				previousChild = prior.children[0]
				oldChild = old.FieldByName("JustValue")
			}
			child := c.encode(value.FieldByName("JustValue"), oldChild, previousChild, inner, depth+1)
			if !child.Supported || child.ConversionErr != nil {
				return child
			}
			out.Size, out.Null, out.RawKind, out.Plan, out.MarshalErr = child.Size, child.Null, child.RawKind, child.Plan, child.MarshalErr
			if child.RawKind == spaClosedRawNil {
				out.RawKind = spaClosedRawJustNull
			}
			out.SameJSON = prior != nil && len(prior.children) == 1 && child.SameJSON
			node.children = []*spaClosedEncodingNode{child.Cache}
		}
	} else {
		out.RawKind = spaClosedRawOther
		out.Size = 2
		out.Plan = &spaClosedBytePlan{}
		out.SameJSON = prior != nil && len(prior.children) == value.NumField()
		for i := 0; i < value.NumField(); i++ {
			f := value.Type().Field(i)
			var previousChild *spaClosedEncodingNode
			var oldChild reflect.Value
			if prior != nil && len(prior.children) == value.NumField() {
				previousChild = prior.children[i]
				oldChild = old.Field(i)
			}
			child := c.encode(value.Field(i), oldChild, previousChild, skyTagType(f), depth+1)
			if !child.Supported || child.ConversionErr != nil {
				return child
			}
			// Keep converting all later children despite an earlier pure marshal
			// error. Reference conversion completes before marshaling begins.
			if out.MarshalErr == nil {
				out.MarshalErr = child.MarshalErr
			}
			key, _ := json.Marshal(skyColKey(f, true))
			if i > 0 {
				out.Size = spaSizeAdd(out.Size, 1)
			}
			out.Size = spaSizeAdd(out.Size, uint64(len(key))+1)
			out.Size = spaSizeAdd(out.Size, child.Size)
			out.Plan.keys = append(out.Plan.keys, string(key))
			out.Plan.children = append(out.Plan.children, child.Plan)
			node.children = append(node.children, child.Cache)
			out.SameJSON = out.SameJSON && child.SameJSON
		}
	}
	node.size, node.null, node.rawKind = out.Size, out.Null, out.RawKind
	if out.MarshalErr == nil {
		out.Cache = node
	}
	return out
}

func (c *spaClosedEncodeContext) atomicWithOld(value, old reflect.Value, prior *spaClosedEncodingNode, declared string, cacheable bool, depth int) (out spaClosedEncodingResult) {
	out.Supported = true
	node := &spaClosedEncodingNode{type_: value.Type(), declared: declared, kind: spaClosedAtomic}
	if prior != nil && old.IsValid() {
		budget := spaModelWitnessBudget
		same := spaClosedMixedValueSame(old, value, &budget)
		c.work.ComparedNodes += spaModelWitnessBudget - budget
		missingAllowed := !c.options.NeedRaw || (c.options.AllowOversizeWithoutRaw && prior.size > uint64(c.budget.CapBytes))
		if same {
			c.keepDictSummary(node, prior, depth)
		}
		if same && prior.raw == "" && !missingAllowed && c.budget.Pins.claim(c.epoch) {
			// Prior overall success and the current immutable witness certify
			// validity/size. Only raw bytes are missing, not validation.
			c.work.Reuses++
			out.Size, out.Null, out.RawKind, out.SameJSON = prior.size, prior.null, prior.rawKind, true
			node.size, node.null, node.rawKind = prior.size, prior.null, prior.rawKind
			lazy := &spaClosedLazyLeaf{
				value: value, declared: declared, snapshot: c.epoch, budget: c.budget,
				size: prior.size, null: prior.null, rawKind: prior.rawKind,
			}
			if !c.options.DiscardRawRetention && prior.size <= uint64(c.budget.BytesLeft) {
				c.budget.BytesLeft -= int(prior.size)
				c.work.RetainedBytes += int(prior.size) // reserved capacity
				lazy.target = node                      // NEVER the prior node
			}
			out.Plan = &spaClosedBytePlan{lazy: lazy}
			out.Cache = node
			return out
		}
		if same && (prior.raw != "" || missingAllowed) {
			c.work.Reuses++
			out.Size, out.Null, out.RawKind, out.SameJSON = prior.size, prior.null, prior.rawKind, true
			out.Plan = &spaClosedBytePlan{raw: prior.raw, missing: prior.raw == ""}
			node.size, node.null, node.rawKind = out.Size, out.Null, out.RawKind
			c.retain(node, prior.raw)
			out.Cache = node
			return out
		}
	}
	marker := c.primitiveDict(value, declared, cacheable)
	var summary *spaClosedDictSummary
	if marker != nil && prior != nil && prior.dict != nil && old.IsValid() && old.CanInterface() {
		summary = marker.spaClosedDictSummaryBuild(c, old.Interface(), prior.dict, depth+1)
		if summary == nil {
			c.work.BudgetFallbacks++
		}
		if summary != nil && summary.jsonSize() > uint64(c.budget.CapBytes) && c.epoch == codecEnumRegistry() {
			out.Size, out.RawKind = summary.jsonSize(), spaClosedRawOther
			out.Plan = &spaClosedBytePlan{missing: true}
			node.size, node.rawKind, node.dict = out.Size, out.RawKind, summary
			out.Cache = node
			return out
		}
	}
	c.work.Conversions++
	raw, err := codecAutoEncodeTyped(value, declared, true)
	if err != nil {
		out.ConversionErr = err
		return out
	}
	out.RawKind = spaClosedRawOther
	if raw == nil {
		out.RawKind = spaClosedRawNil
	} else if _, ok := raw.(codecJustNull); ok {
		out.RawKind = spaClosedRawJustNull
	}
	c.work.Marshals++
	encoded, err := json.Marshal(raw)
	out.MarshalErr, out.Size, out.Null = err, uint64(len(encoded)), spaIsNullRaw(encoded)
	c.work.EncodedBytes = spaSizeAdd(c.work.EncodedBytes, uint64(len(encoded)))
	out.Plan = &spaClosedBytePlan{raw: string(encoded)}
	if err == nil && cacheable {
		if marker != nil && summary == nil && node.dict == nil && out.Size > uint64(c.budget.CapBytes) {
			summary = marker.spaClosedDictSummaryBuild(c, nil, nil, depth+1)
			if summary == nil {
				c.work.BudgetFallbacks++
			}
		}
		if summary != nil && summary.jsonSize() == out.Size {
			node.dict = summary
		}
		node.size, node.null, node.rawKind = out.Size, out.Null, out.RawKind
		c.retain(node, out.Plan.raw)
		out.Cache = node
	}
	return out
}
