package rt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"
)

// This engine is reachable only through a compiler-proven prepared capsule.
// It intentionally does not widen the interleaved whole-closed field engine.
type spaMixedEncodeOptions struct {
	Exclude, Protected []string
	CapBytes           int
	PreviousOnly       bool
	Profile            bool
}

type spaMixedEncodingWork struct {
	Prepares, PreparedFallbacks           int
	Conversions, Marshals                 int
	ClosedReuses, ComparedNodes           int
	EncodedBytes, MaterializedBytes       uint64
	RetainedBytes                         int
	EncodeNanos, BuildNanos               int64
	DeferredConversions, DeferredMarshals int
	DeferredEncodedBytes                  uint64
	DeferredNanos                         int64
}

type spaMixedEncoded struct {
	Valid bool
	// Reference means the caller must consume FullJSON by its original path.
	Reference      bool
	FullJSON       string
	Payload        string
	EncodedBytes   uint64
	Identity       string
	NullProjection string
	NextCache      *spaMixedModelCache
	Work           spaMixedEncodingWork
}

type spaMixedFieldCertificate struct {
	valid bool
	size  uint64
	null  bool
	raw   string
}

type spaMixedModelCache struct {
	model    any // strong CURRENT owner; never an address without its value
	enums    *codecEnumRegistrySnapshot
	fields   []spaMixedFieldCertificate
	payload  string
	selected []int
	closed   []*spaClosedEncodingNode
}

type spaMixedFieldDescriptor struct {
	index                  int
	key, keyJSON, declared string
	closed                 bool
}

type spaMixedRootDescriptor struct {
	valid  bool
	fields []spaMixedFieldDescriptor
}

var spaMixedRootDescriptors sync.Map // reflect.Type -> immutable descriptor

func spaMixedRoot(t reflect.Type) spaMixedRootDescriptor {
	if t == nil {
		return spaMixedRootDescriptor{}
	}
	if d, ok := spaMixedRootDescriptors.Load(t); ok {
		return d.(spaMixedRootDescriptor)
	}
	d := spaMixedRootDescriptor{}
	// Match codecAutoEncodeVal's special dispatch BEFORE ordinary records.
	// Do not invoke foreign SkyVariant methods just to decide eligibility.
	if t.Kind() == reflect.Struct && !t.Implements(skyVariantIfaceType) &&
		!isSkyAdtType(t) && !isSkyMaybeType(t) && !isSkyDictType(t) &&
		t != reflect.TypeOf(SkySet{}) && t != reflect.TypeOf(JsonValue{}) && !codecOpaqueStruct(t) {
		d.valid = true
		keys := map[string]bool{}
		states := map[reflect.Type]uint8{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			key := skyColKey(f, true)
			if f.PkgPath != "" || f.Anonymous || f.Tag.Get("sky") == "" || !utf8.ValidString(key) || keys[key] {
				d.valid = false
				break
			}
			keys[key] = true
			keyJSON, _ := json.Marshal(key)
			d.fields = append(d.fields, spaMixedFieldDescriptor{
				index: i, key: key, keyJSON: string(keyJSON), declared: skyTagType(f),
				closed: spaProjectionTypeClosed(f.Type, states, 0),
			})
		}
	}
	spaMixedRootDescriptors.Store(t, d)
	return d
}

type spaMixedInvocationField struct {
	raw        any // actual Phase-A result for unknown fields, frozen through Phase B
	encoded    string
	marshalErr error // pure early marshal failure, observed ONLY at B position
	size       uint64
	null       bool
	reused     bool
	generation int
	closed     *spaClosedEncodingResult
}

// Every logical invocation owns its own recovery boundary. Failure produces no
// cache; the caller can still invoke the previous encoder after publication.
func spaEncodeMixed(e SpaPreparedModelEncoder, previous *spaMixedModelCache, model any, options spaMixedEncodeOptions) (out spaMixedEncoded) {
	var encodeStart, buildStart time.Time
	var closedBudget *spaClosedEncodingBudget
	if options.Profile {
		encodeStart = time.Now()
	}
	defer func() {
		// Merge ONCE, including a resolution panic. BuildNanos already
		// includes DeferredNanos; the latter is a diagnostic subrange only.
		if closedBudget != nil {
			d := closedBudget.Deferred
			out.Work.Conversions += d.Conversions
			out.Work.Marshals += d.Marshals
			out.Work.EncodedBytes = spaSizeAdd(out.Work.EncodedBytes, d.EncodedBytes)
			out.Work.DeferredConversions = d.Conversions
			out.Work.DeferredMarshals = d.Marshals
			out.Work.DeferredEncodedBytes = d.EncodedBytes
			out.Work.DeferredNanos = d.Nanos
		}
		if !encodeStart.IsZero() {
			if buildStart.IsZero() {
				out.Work.EncodeNanos = time.Since(encodeStart).Nanoseconds()
			} else {
				out.Work.BuildNanos = time.Since(buildStart).Nanoseconds()
			}
		}
		if recover() != nil {
			out.Valid, out.NextCache = false, nil
			out.FullJSON, out.Payload, out.Identity, out.NullProjection = "", "", "", ""
		}
	}()
	out.Work.Prepares++
	prepared := sky_call(e.Prepare, struct{}{})
	d := spaMixedRoot(reflect.TypeOf(model))
	if !d.valid || options.CapBytes <= 0 || options.CapBytes > spaPersistMaxBytes {
		out.Reference = true
		out.Work.PreparedFallbacks++
		// NEVER call Full: strict codec construction has already occurred.
		out.FullJSON = AsString(sky_call2(e.Finish, prepared, model))
		out.Valid = out.FullJSON != ""
		return
	}
	value := reflect.ValueOf(model)
	selected := make([]bool, len(d.fields))
	protected := make([]bool, len(d.fields))
	indices := make([]int, 0, len(d.fields))
	removed := false
	for i, f := range d.fields {
		protected[i] = spaPersistProtectedWireKey(f.key, options.Protected)
		drop := false
		for _, name := range options.Exclude {
			for _, key := range spaFieldKeys(name) {
				if f.key == key && !protected[i] {
					drop = true
				}
			}
		}
		selected[i] = !drop
		if drop {
			removed = true
		} else {
			indices = append(indices, i)
		}
	}
	if removed {
		sort.Slice(indices, func(i, j int) bool { return d.fields[indices[i]].key < d.fields[indices[j]].key })
	}
	metadata := codecEnumRegistry() // AFTER original strict preparation
	generation := 0
	observeMetadata := func() {
		if now := codecEnumRegistry(); now != metadata {
			metadata = now
			generation++
		}
	}
	closedBudget = spaNewClosedEncodingBudget(options.CapBytes)
	closedBudget.Profile = options.Profile
	fields := make([]spaMixedInvocationField, len(d.fields))
	canCompare := previous != nil && reflect.TypeOf(previous.model) == value.Type() && len(previous.fields) == len(fields)
	var old reflect.Value
	if canCompare {
		old = reflect.ValueOf(previous.model)
	}
	// PHASE A: all conversions in declaration order. Unknown fields are never
	// pre-marshaled: later conversions can still fail or mutate their raw data.
	for i, f := range d.fields {
		observeMetadata()
		field := &fields[i]
		field.generation = generation

		if f.closed {
			var prior *spaClosedEncodingNode
			var oldField reflect.Value
			var previousEpoch *codecEnumRegistrySnapshot
			if canCompare {
				oldField = old.Field(f.index)
				previousEpoch = previous.enums
				if len(previous.closed) == len(fields) {
					prior = previous.closed[i]
				}
			}
			summary := spaEncodeClosed(value.Field(f.index), oldField, prior, f.declared, previousEpoch, metadata,
				spaClosedEncodingOptions{
					NeedRaw:                 !options.PreviousOnly && (selected[i] || protected[i]),
					AllowOversizeWithoutRaw: !protected[i],
					DiscardRawRetention:     options.PreviousOnly || !selected[i],
				}, closedBudget)
			if summary.Supported {
				field.closed = &summary
				out.Work.Conversions += summary.Work.Conversions
				out.Work.Marshals += summary.Work.Marshals
				out.Work.ClosedReuses += summary.Work.Reuses
				out.Work.ComparedNodes += summary.Work.ComparedNodes
				out.Work.EncodedBytes = spaSizeAdd(out.Work.EncodedBytes, summary.Work.EncodedBytes)
				if summary.ConversionErr != nil {
					panic("Codec.auto: cannot encode this value — " + summary.ConversionErr.Error())
				}
				field.size, field.null, field.marshalErr = summary.Size, summary.Null, summary.MarshalErr
				// Observe enum changes without replaying any prior work.
				observeMetadata()
				continue
			}
		}
		out.Work.Conversions++
		raw, err := codecAutoEncodeTyped(value.Field(f.index), f.declared, true)
		if err != nil {
			panic("Codec.auto: cannot encode this value — " + err.Error())
		}
		field.raw = raw
		observeMetadata()

	}
	// PHASE B: marshal/error/panic order matches jsonOrderedObject.MarshalJSON.
	// Unknown values are the captured A results, not freshly read model fields.
	for i := range d.fields {
		observeMetadata()
		field := &fields[i]
		if field.closed == nil {
			out.Work.Marshals++
			encoded, err := json.Marshal(field.raw)
			field.encoded, field.marshalErr = string(encoded), err
			field.size, field.null = uint64(len(encoded)), spaIsNullRaw(encoded)
			out.Work.EncodedBytes = spaSizeAdd(out.Work.EncodedBytes, uint64(len(encoded)))
		}
		if field.marshalErr != nil {
			panic("rt.JsonEnc_encode: cannot encode the value as JSON: " + field.marshalErr.Error())
		}
		observeMetadata()
	}
	if options.Profile {
		out.Work.EncodeNanos = time.Since(encodeStart).Nanoseconds()
		buildStart = time.Now()
	}
	// All unknown conversion/marshal FX have run. Byte-plan materialization
	// below consumes ONLY captured pure bytes, never mutable enum metadata.
	size := uint64(2)
	for pos, index := range indices {
		if pos > 0 {
			size = spaSizeAdd(size, 1)
		}
		size = spaSizeAdd(size, uint64(len(d.fields[index].keyJSON))+1)
		size = spaSizeAdd(size, fields[index].size)
	}
	reusePayload := canCompare && spaMixedRecursivePayloadSame(previous, fields, indices)
	// Build the new certificate only after the ENTIRE invocation succeeded.
	// One retained epoch; stamps discard certificates computed before changes.
	next := &spaMixedModelCache{model: model, enums: metadata, fields: make([]spaMixedFieldCertificate, len(fields)), closed: make([]*spaClosedEncodingNode, len(fields))}
	remaining := closedBudget.BytesLeft
	for i := range d.fields {
		field := fields[i]
		if field.closed != nil && field.generation == generation {
			next.fields[i] = spaMixedFieldCertificate{valid: true, size: field.size, null: field.null}
			if field.closed != nil {
				next.closed[i] = field.closed.Cache
				if field.closed.Cache != nil {
					next.fields[i].raw = field.closed.Cache.raw
				}
			}
		}
	}
	// Give selected data first claim on bounded raw retention; excluded values
	// need only success/nullness certificates unless configuration changes.
	for pass := 0; pass < 2; pass++ {
		for i, field := range fields {
			if field.closed != nil || selected[i] != (pass == 0) || (!next.fields[i].valid && len(field.encoded) > 256) || len(field.encoded) > remaining {
				continue
			}
			next.fields[i].raw = field.encoded
			remaining -= len(field.encoded)
		}
	}
	out.Work.RetainedBytes = spaPersistMaxBytes - remaining
	out.NextCache = next
	// Sign-out uses exact keys/nullness; identity uses aliases and UseNumber.
	nulls := map[string]any{}
	for _, name := range options.Protected {
		for i, f := range d.fields {
			if f.key == name {
				if fields[i].null {
					nulls[name] = nil
				} else {
					nulls[name] = true
				}
				break
			}
		}
	}
	nullJSON, _ := json.Marshal(nulls)
	out.NullProjection = string(nullJSON)
	if options.PreviousOnly {
		out.Valid = true
		return
	}
	var identity strings.Builder
	identity.WriteByte('{')
	wrote := false
	for i, f := range d.fields {
		if !protected[i] {
			continue
		}
		if fields[i].closed == nil && fields[i].encoded == "" {
			panic("mixed codec: missing protected raw")
		}
		if wrote {
			identity.WriteByte(',')
		}
		wrote = true
		identity.WriteString(f.keyJSON)
		identity.WriteByte(':')
		if fields[i].closed != nil {
			if !fields[i].closed.Plan.appendTo(&identity) {
				panic("recursive codec: incomplete captured identity plan")
			}
		} else {
			identity.WriteString(fields[i].encoded)
		}
	}
	identity.WriteByte('}')
	var identityOK bool
	out.Identity, identityOK = spaIdentityOf(identity.String(), options.Protected)
	if !identityOK {
		panic("mixed codec: invalid protected identity")
	}
	out.EncodedBytes = size
	if size <= uint64(options.CapBytes) {
		// All preparation, conversions and unknown marshaling have already run.
		// Reuse only byte-proven output, never the identity of a foreign value.
		samePayload := reusePayload
		if samePayload {
			out.Payload = previous.payload
		} else {
			var b strings.Builder
			b.Grow(int(size))
			b.WriteByte('{')
			for pos, index := range indices {
				if fields[index].closed == nil && fields[index].encoded == "" {
					panic(fmt.Sprintf("mixed codec: missing stored raw at field %d", index))
				}
				if pos > 0 {
					b.WriteByte(',')
				}
				b.WriteString(d.fields[index].keyJSON)
				b.WriteByte(':')
				if fields[index].closed != nil {
					if !fields[index].closed.Plan.appendTo(&b) {
						panic("recursive codec: incomplete captured payload plan")
					}
				} else {
					b.WriteString(fields[index].encoded)
				}
			}
			b.WriteByte('}')
			out.Payload = b.String()
			if uint64(len(out.Payload)) != size {
				panic("mixed codec: encoded size mismatch")
			}
			out.Work.MaterializedBytes = size
		}
		next.payload = out.Payload
		next.selected = append([]int(nil), indices...)
	}
	out.Valid = true
	return
}

// This proof is evaluated only AFTER unknown A/B FX. Recursive SameJSON is
// tied to the prior owner's matching node/epoch and its exact canonical schema.
func spaMixedRecursivePayloadSame(previous *spaMixedModelCache, fields []spaMixedInvocationField, indices []int) bool {
	if previous == nil || previous.payload == "" || len(previous.fields) != len(fields) || len(previous.selected) != len(indices) {
		return false
	}
	for pos, index := range indices {
		if previous.selected[pos] != index {
			return false
		}
		if fields[index].closed != nil && fields[index].closed.SameJSON {
			continue
		}
		a, b := fields[index].encoded, previous.fields[index].raw
		if len(a) != len(b) || a == "" || !(unsafe.StringData(a) == unsafe.StringData(b) || (len(a) <= 256 && a == b)) {
			return false
		}
	}
	return true
}
