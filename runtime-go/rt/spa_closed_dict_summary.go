package rt

import (
	"encoding/json"
	"math/bits"
	"reflect"
)

// Retained certificates contain ONLY scalar shape/count/size metadata and
// child certificates. They never hold model nodes, values, interfaces or epoch
// history. Typed old/current Dict pointers are compared invocation-locally,
// under spaEncodeClosed's existing prior-certificate/old-owner pairing contract.
// units includes both node headers and every retained children slot.
type spaClosedDictSummary struct {
	bitmap                           uint32
	flat                             bool
	leaf                             bool
	count                            int
	keyBytes, valueBytes, entryBytes uint64
	units, nodes                     int
	children                         []*spaClosedDictSummary
	height                           int
}

func (s *spaClosedDictSummary) jsonSize() uint64 {
	size := spaSizeAdd(2, s.entryBytes)
	if s.count > 0 {
		size = spaSizeAdd(size, uint64(s.count-1))
	}
	return size
}

// Validate only retained scalar shape at this level. Descendant validity was
// established when the successful old-owner certificate was constructed.
// This bounded width check does not walk a physically unchanged old subtree.
func spaDictSummaryLeafValid(s *spaClosedDictSummary) bool {
	return s != nil && s.leaf && !s.flat && s.bitmap == 0 && s.count == 1 && s.units == 1 && s.nodes == 1 && s.height == 0 && len(s.children) == 0 && s.keyBytes >= 2 && s.valueBytes > 0 && s.entryBytes == spaSizeAdd(spaSizeAdd(s.keyBytes, 1), s.valueBytes)
}
func spaDictSummaryBranchValid(s *spaClosedDictSummary, bitmap uint32, flat bool, width int) bool {
	if s == nil || s.leaf || s.bitmap != bitmap || s.flat != flat || len(s.children) != width || width > dictFlatMax || s.keyBytes != 0 || s.valueBytes != 0 || s.units < 1 || s.units > spaClosedEncodingNodeLimit || s.nodes < 1 || s.nodes > spaClosedEncodingNodeLimit || s.count < 0 || s.count > spaClosedEncodingNodeLimit {
		return false
	}
	if flat {
		if bitmap != 0 {
			return false
		}
	} else if bits.OnesCount32(bitmap) != width || bitmap&^uint32((1<<(1<<dictBits))-1) != 0 {
		return false
	}
	units, nodes, count, height := 1+width, 1, 0, 0
	var entryBytes uint64
	for _, child := range s.children {
		if child == nil || child.units < 1 || child.units > spaClosedEncodingNodeLimit || child.nodes < 1 || child.nodes > spaClosedEncodingNodeLimit || child.count < 1 || child.count > spaClosedEncodingNodeLimit || child.height < 0 || child.height > spaClosedEncodingDepthLimit {
			return false
		}
		if child.leaf {
			if !spaDictSummaryLeafValid(child) {
				return false
			}
		} else if flat {
			return false
		}
		units += child.units
		nodes += child.nodes
		count += child.count
		if child.height+1 > height {
			height = child.height + 1
		}
		entryBytes = spaSizeAdd(entryBytes, child.entryBytes)
	}
	return s.units == units && s.nodes == nodes && s.count == count && s.height == height && s.entryBytes == entryBytes
}

type spaClosedDictSummaryMarker interface {
	spaClosedDictSummaryBuild(*spaClosedEncodeContext, any, *spaClosedDictSummary, int) *spaClosedDictSummary
}

// This domain deliberately excludes aliases, floats, named foreign scalars,
// enums, custom methods and registered declared owners. Exact primitive values
// have total callback-free conversion/marshalling; hash traversal therefore
// cannot reorder observable conversion failures or unknown effects.
func (c *spaClosedEncodeContext) primitiveDict(value reflect.Value, declared string, cacheable bool) spaClosedDictSummaryMarker {
	if !cacheable || c.options.DiscardRawRetention || !c.options.AllowOversizeWithoutRaw ||
		c.epoch == nil || c.epoch != codecEnumRegistry() || !spaClosedNominalDict(value.Type()) || !value.CanInterface() ||
		codecAutoDeclaredOwnerAt(declared, c.epoch) != "" {
		return nil
	}
	element := value.Interface().(spaClosedDictMarker).spaClosedDictElement()
	name := ""
	switch element {
	case reflect.TypeOf(""):
		name = "string"
	case reflect.TypeOf(int(0)):
		name = "int"
	case reflect.TypeOf(false):
		name = "bool"
	default:
		return nil
	}
	if declared != "rt.SkyDict["+name+"]" || codecAutoEnumRegisteredAt(name, c.epoch) {
		return nil
	}
	// Only these three wrappers instantiate the generic walker; do not add a
	// method to SkyDict[V] that expands metadata/code for every unrelated V.
	switch d := value.Interface().(type) {
	case SkyDict[string]:
		return spaPrimitiveDictSummary[string]{d}
	case SkyDict[int]:
		return spaPrimitiveDictSummary[int]{d}
	case SkyDict[bool]:
		return spaPrimitiveDictSummary[bool]{d}
	}
	return nil
}

// Charge the whole reused reachable tree, not merely its newly allocated root.
// Thus sharing cannot smuggle an unbounded retained graph through one unit.
func (c *spaClosedEncodeContext) keepDictSummary(node, prior *spaClosedEncodingNode, depth int) {
	if prior == nil || prior.dict == nil || c.options.DiscardRawRetention {
		return
	}
	if !spaDictSummaryBranchValid(prior.dict, prior.dict.bitmap, prior.dict.flat, len(prior.dict.children)) {
		return
	}
	if prior.dict.units > c.budget.NodesLeft || depth+1+prior.dict.height > c.budget.DepthLimit {
		return
	}
	c.budget.NodesLeft -= prior.dict.units
	c.work.Nodes += prior.dict.nodes
	node.dict = prior.dict
}

func spaDictPrimitiveSame(a, b any) bool {
	switch a.(type) {
	case string, int, bool:
		// Preserve the existing bounded string witness (physical identity, or at
		// most256 bytes), rather than introducing unbounded deep value equality.
		budget := 1
		return spaModelValueSame(reflect.ValueOf(a), reflect.ValueOf(b), &budget)
	}
	return false
}

type spaDictSummaryBuilder[V any] struct{ ctx *spaClosedEncodeContext }

func (b spaDictSummaryBuilder[V]) charge(units, nodes, depth int) bool {
	if depth > b.ctx.budget.DepthLimit || units > b.ctx.budget.NodesLeft {
		return false
	}
	b.ctx.budget.NodesLeft -= units
	b.ctx.work.Nodes += nodes
	return true
}

type spaPrimitiveDictSummary[V any] struct{ dict SkyDict[V] }

func (d spaPrimitiveDictSummary[V]) spaClosedDictSummaryBuild(c *spaClosedEncodeContext, previous any, prior *spaClosedDictSummary, depth int) *spaClosedDictSummary {
	return spaBuildPrimitiveDictSummary(d.dict, c, previous, prior, depth)
}
func spaBuildPrimitiveDictSummary[V any](d SkyDict[V], c *spaClosedEncodeContext, previous any, prior *spaClosedDictSummary, depth int) *spaClosedDictSummary {
	old, ok := previous.(SkyDict[V])
	if prior != nil && (!ok || prior.leaf || prior.flat == old.trie || prior.bitmap != old.bm || prior.count != old.n || old.foreign != d.foreign) {
		// No retained pointer is needed to authenticate this association: the
		// enclosing cache pairs prior with its exact immutable old model. Reject
		// scalar/layout contradictions instead of borrowing an unrelated shape.
		return nil
	}
	if prior == nil {
		old = SkyDict[V]{}
	}
	builder := spaDictSummaryBuilder[V]{ctx: c}
	var current, previousKids []dslot[V]
	if d.trie {
		root := d.root()
		current = root.children()
	} else {
		current = d.flat()
	}
	if prior != nil {
		if old.trie {
			root := old.root()
			previousKids = root.children()
		} else {
			previousKids = old.flat()
		}
	}
	summary := builder.branch(d.kids, d.bm, !d.trie, current, old.kids, old.bm, !old.trie, previousKids, prior, depth)
	if summary == nil || summary.count != d.n {
		return nil
	}
	return summary
}

func (b spaDictSummaryBuilder[V]) slot(current dslot[V], old *dslot[V], prior *spaClosedDictSummary, depth int) *spaClosedDictSummary {
	b.ctx.work.ComparedNodes++
	// Large/full-hash collision buckets are intentionally canonical fallback in
	// this first slice; no quadratic alignment scan is added to the cache path.
	if current.coll || old != nil && old.coll {
		return nil
	}
	if current.kids != nil {
		var previousKids []dslot[V]
		var previousPtr *dslot[V]
		var bitmap uint32
		if old != nil && old.kids != nil {
			previousKids = old.children()
			previousPtr = old.kids
			bitmap = old.bm
		} else {
			prior = nil
		}
		return b.branch(current.kids, current.bm, false, current.children(), previousPtr, bitmap, false, previousKids, prior, depth)
	}
	if !b.charge(1, 1, depth) {
		return nil
	}
	sameKey := old != nil && old.kids == nil && old.key == current.key && spaDictSummaryLeafValid(prior)
	if sameKey && spaDictPrimitiveSame(any(current.val), any(old.val)) {
		return prior
	}
	s := &spaClosedDictSummary{leaf: true, count: 1, units: 1, nodes: 1}
	if sameKey {
		s.keyBytes = prior.keyBytes
	} else {
		// The key as Codec.auto writes it (dictJSONKey), so the size
		// matches the encoding.
		raw, err := json.Marshal(dictJSONKey(current.key))
		if err != nil {
			return nil
		}
		b.ctx.work.Marshals++
		b.ctx.work.EncodedBytes = spaSizeAdd(b.ctx.work.EncodedBytes, uint64(len(raw)))
		s.keyBytes = uint64(len(raw))
	}
	// V has been checked against the exact built-in primitive reflect.Type.
	raw, err := json.Marshal(current.val)
	if err != nil {
		return nil
	}
	b.ctx.work.Conversions++
	b.ctx.work.Marshals++
	b.ctx.work.EncodedBytes = spaSizeAdd(b.ctx.work.EncodedBytes, uint64(len(raw)))
	s.valueBytes = uint64(len(raw))
	s.entryBytes = spaSizeAdd(spaSizeAdd(s.keyBytes, 1), s.valueBytes)
	return s
}

func (b spaDictSummaryBuilder[V]) branch(ptr *dslot[V], bitmap uint32, flat bool, current []dslot[V], oldPtr *dslot[V], oldBitmap uint32, oldFlat bool, old []dslot[V], prior *spaClosedDictSummary, depth int) *spaClosedDictSummary {
	validPrior := spaDictSummaryBranchValid(prior, oldBitmap, oldFlat, len(old))
	if prior != nil && !validPrior {
		return nil
	}
	if prior != nil && ptr == oldPtr && bitmap == oldBitmap && flat == oldFlat && len(current) == len(old) {
		if !b.charge(prior.units, prior.nodes, depth+prior.height) {
			return nil
		}
		return prior
	}
	if !b.charge(1+len(current), 1, depth) {
		return nil
	}
	s := &spaClosedDictSummary{bitmap: bitmap, flat: flat, units: 1 + len(current), nodes: 1, children: make([]*spaClosedDictSummary, 0, len(current))}
	bit := uint32(1)
	for _, slot := range current {
		index := -1
		if prior != nil && flat == oldFlat {
			if flat {
				// Runtime flat roots are limited to16 entries, so key alignment is bounded.
				for j := range old {
					if old[j].key == slot.key {
						index = j
						break
					}
				}
			} else {
				for bit != 0 && bitmap&bit == 0 {
					bit <<= 1
				}
				if oldBitmap&bit != 0 {
					index = bits.OnesCount32(oldBitmap & (bit - 1))
				}
			}
		}
		var oldSlot *dslot[V]
		var previous *spaClosedDictSummary
		if index >= 0 && index < len(old) {
			oldSlot = &old[index]
			previous = prior.children[index]
		}
		child := b.slot(slot, oldSlot, previous, depth+1)
		if child == nil {
			return nil
		}
		s.children = append(s.children, child)
		if child.height+1 > s.height {
			s.height = child.height + 1
		}
		s.count += child.count
		s.entryBytes = spaSizeAdd(s.entryBytes, child.entryBytes)
		s.units += child.units
		s.nodes += child.nodes
		if !flat {
			bit <<= 1
		}
	}
	return s
}
