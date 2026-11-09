package rt

import (
	"encoding/json"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
)

// These summaries are private compiler-certified codec machinery. Successful
// JSON alone does NOT prove an arbitrary encoder pure. The caller must already
// hold the canonical-encoder proof; the entire runtime type must also be closed.
// A constructor certificate is established by an actual successful Full call.
type spaModelFieldSnapshot struct {
	model     any
	enums     *codecEnumRegistrySnapshot
	fields    []spaEncodedModelField
	valid     bool
	retainCap int
	rawBytes  int
	plan      *spaFieldPersistencePlan
	payload   *spaStoredFieldPayload
	identity  *spaStoredFieldIdentity
}

type spaFieldPersistencePlan struct {
	exclude   []string
	protected []string
	indices   []int
	selected  []bool
	capBytes  int
}

type spaStoredFieldIdentity struct {
	protected []string
	indices   []int
	id        string
}

type spaStoredFieldPayload struct {
	plan      *spaFieldPersistencePlan
	json      string
	oversized bool
}

func spaFieldNamesSame(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type spaEncodedModelField struct {
	index    int
	key      string
	keyJSON  string
	declared string
	size     uint64
	raw      string // owned bytes; empty means not retained, not encoding failure
	valid    bool
	null     bool
}

type spaFieldEncodingWork struct {
	ComparedNodes     int
	FieldEncodes      int
	FieldEncodedBytes uint64
	RetainedBytes     int
	PayloadBytes      int
	MaterializedBytes uint64
}

func spaSizeAdd(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}

// Ingest the exact original full result, checking its root shape and field
// order. Parsing here is cold work, not a repeated walk of unchanged data.
func spaModelFieldsFromFull(model any, full string, metadata *codecEnumRegistrySnapshot, retainCap int) (*spaModelFieldSnapshot, bool) {
	if !spaModelEncodingEligible(model) || full == "" || retainCap < 0 || retainCap > spaPersistMaxBytes || metadata != codecEnumRegistry() {
		return nil, false
	}
	rv := reflect.ValueOf(model)
	t := rv.Type()
	dec := json.NewDecoder(strings.NewReader(full))
	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return nil, false
	}
	s := &spaModelFieldSnapshot{model: model, enums: metadata, valid: true, retainCap: retainCap}
	remaining := retainCap
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := skyColKey(f, true)
		got, err := dec.Token()
		if err != nil || got != key {
			return nil, false
		}
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return nil, false
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return nil, false
		}
		field := spaEncodedModelField{index: i, key: key, keyJSON: string(keyJSON), declared: skyTagType(f), size: uint64(len(raw)), valid: true, null: spaIsNullRaw(raw)}
		if len(raw) <= remaining {
			// RawMessage owns its bytes. Never retain a short substring of a
			// potentially huge full JSON buffer (including old session data).
			field.raw = string(raw)
			remaining -= len(raw)
		}
		s.fields = append(s.fields, field)
	}
	close, err := dec.Token()
	if err != nil || close != json.Delim('}') {
		return nil, false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || metadata != codecEnumRegistry() {
		return nil, false
	}
	s.rawBytes = retainCap - remaining
	return s, true
}

func spaEncodeModelField(value reflect.Value, declared string) ([]byte, bool) {
	raw, err := codecAutoEncodeTyped(value, declared, true)
	if err != nil {
		return nil, false
	}
	encoded, err := jsonMarshalTree(raw)
	return encoded, err == nil
}

// Derive only inside the closed, effect-free encoding domain. No decoder,
// round-trip check or business validation is added. Changed excluded fields
// are encoded too: their failure makes the FULL result invalid.
func (s *spaModelFieldSnapshot) derive(model any) (*spaModelFieldSnapshot, spaFieldEncodingWork, bool) {
	work := spaFieldEncodingWork{}
	if s == nil || reflect.TypeOf(model) != reflect.TypeOf(s.model) || s.enums != codecEnumRegistry() {
		return nil, work, false
	}
	old, next := reflect.ValueOf(s.model), reflect.ValueOf(model)
	out := &spaModelFieldSnapshot{model: model, enums: s.enums, valid: true, retainCap: s.retainCap, plan: s.plan}
	remaining := s.retainCap
	sameFields := make([]bool, len(s.fields))
	for i, previous := range s.fields {
		field := previous
		budget := spaModelWitnessBudget
		same := spaModelValueSame(old.Field(field.index), next.Field(field.index), &budget)
		sameFields[i] = same
		work.ComparedNodes += spaModelWitnessBudget - budget
		if !same {
			work.FieldEncodes++
			encoded, ok := spaEncodeModelField(next.Field(field.index), field.declared)
			field.valid, field.size, field.raw, field.null = ok, uint64(len(encoded)), "", ok && string(encoded) == "null"
			work.FieldEncodedBytes = spaSizeAdd(work.FieldEncodedBytes, uint64(len(encoded)))
			if ok && len(encoded) <= remaining && (s.plan == nil || s.plan.selected[i]) {
				field.raw = string(encoded)
			}
		}
		if len(field.raw) > remaining || (s.plan != nil && !s.plan.selected[i]) {
			field.raw = ""
		}
		remaining -= len(field.raw)
		out.valid = out.valid && field.valid
		out.fields = append(out.fields, field)
	}
	work.RetainedBytes = s.retainCap - remaining
	out.rawBytes = work.RetainedBytes
	if s.enums != codecEnumRegistry() {
		return nil, work, false // caller retains the exact reference path
	}
	if s.identity != nil {
		unchanged := true
		for _, index := range s.identity.indices {
			unchanged = unchanged && sameFields[index]
		}
		if unchanged {
			out.identity = s.identity
		}
	}
	if s.payload != nil {
		unchanged := true
		for _, index := range s.payload.plan.indices {
			unchanged = unchanged && sameFields[index]
		}
		if unchanged {
			// The payload owns only bounded JSON/configuration, never its old
			// model owner. Invalid full encodings still cannot publish it.
			out.payload = s.payload
			work.PayloadBytes = len(s.payload.json)
		}
	}
	return out, work, true
}

func (s *spaModelFieldSnapshot) persistencePlan(exclude, protected []string, capBytes int) *spaFieldPersistencePlan {
	if p := s.plan; p != nil && p.capBytes == capBytes && spaFieldNamesSame(p.exclude, exclude) && spaFieldNamesSame(p.protected, protected) {
		return p
	}
	indices := s.storedFields(exclude, protected)
	p := &spaFieldPersistencePlan{exclude: append([]string(nil), exclude...), protected: append([]string(nil), protected...), indices: indices, selected: make([]bool, len(s.fields)), capBytes: capBytes}
	for _, index := range indices {
		p.selected[index] = true
	}
	// Reserve raw retention for data that can actually be published. An
	// excluded large field must not evict an unchanged stored field.
	for i := range s.fields {
		if !p.selected[i] {
			s.rawBytes -= len(s.fields[i].raw)
			s.fields[i].raw = ""
		}
	}
	s.plan = p
	return p
}

func (s *spaModelFieldSnapshot) storedFields(exclude, protected []string) []int {
	indices := make([]int, 0, len(s.fields))
	removed := false
	for i, field := range s.fields {
		drop := false
		for _, name := range exclude {
			for _, key := range spaFieldKeys(name) {
				if field.key == key && !spaPersistProtectedWireKey(key, protected) {
					drop = true
				}
			}
		}
		if drop {
			removed = true
		} else {
			indices = append(indices, i)
		}
	}
	if removed {
		// spaPersistStripFields marshals a map only when it removed a key.
		sort.Slice(indices, func(i, j int) bool { return s.fields[indices[i]].key < s.fields[indices[j]].key })
	}
	return indices
}

func (s *spaModelFieldSnapshot) encodedSize(indices []int) uint64 {
	total := uint64(2) // braces
	for i, index := range indices {
		field := s.fields[index]
		if i > 0 {
			total = spaSizeAdd(total, 1)
		}
		total = spaSizeAdd(total, uint64(len(field.keyJSON))+1)
		total = spaSizeAdd(total, field.size)
	}
	return total
}

// No whole buffer is built when the valid post-exclusion value is oversized.
// Under the existing positive cap, remaining reconstruction is byte-bounded;
// it is still proportional to the stored bytes and is not flat step latency.
func (s *spaModelFieldSnapshot) storedJSON(exclude, protected []string, capBytes int) (string, bool, spaFieldEncodingWork, bool) {
	work := spaFieldEncodingWork{}
	if s == nil || !s.valid || capBytes <= 0 || capBytes > spaPersistMaxBytes || s.enums != codecEnumRegistry() {
		return "", false, work, false
	}
	plan := s.persistencePlan(exclude, protected, capBytes)
	work.RetainedBytes = s.rawBytes
	if p := s.payload; p != nil && p.plan == plan {
		work.PayloadBytes = len(p.json)
		return p.json, p.oversized, work, true
	}
	indices := plan.indices
	size := s.encodedSize(indices)
	payload := &spaStoredFieldPayload{plan: plan}
	if size > uint64(capBytes) {
		payload.oversized = true
		s.payload = payload
		return "", true, work, true
	}
	var b strings.Builder
	b.Grow(int(size))
	b.WriteByte('{')
	for i, index := range indices {
		field := s.fields[index]
		raw := field.raw
		if raw == "" {
			// A now-small snapshot may need an unchanged field whose raw
			// bytes were not retained. Only its bounded stored bytes are rebuilt.
			encoded, ok := spaEncodeModelField(reflect.ValueOf(s.model).Field(field.index), field.declared)
			work.FieldEncodes++
			work.FieldEncodedBytes = spaSizeAdd(work.FieldEncodedBytes, uint64(len(encoded)))
			if !ok || uint64(len(encoded)) != field.size {
				return "", false, work, false
			}
			raw = string(encoded)
			if len(raw) <= s.retainCap-s.rawBytes {
				s.fields[index].raw = raw
				s.rawBytes += len(raw)
			}
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(field.keyJSON)
		b.WriteByte(':')
		b.WriteString(raw)
	}
	b.WriteByte('}')
	work.MaterializedBytes = uint64(b.Len())
	if work.MaterializedBytes != size || s.enums != codecEnumRegistry() {
		return "", false, work, false
	}
	payload.json = b.String()
	s.payload = payload
	work.RetainedBytes, work.PayloadBytes = s.rawBytes, len(payload.json)
	return payload.json, false, work, true
}

// Identity uses alias precedence and UseNumber, whereas sign-out intentionally
// uses exact keys. Keep those contracts separate. A bounded projection miss
// retains the original full path, including large protected session values.
func (s *spaModelFieldSnapshot) protectedIdentity(protected []string) (string, bool) {
	return s.protectedIdentityWithWork(protected, nil)
}

func (s *spaModelFieldSnapshot) protectedIdentityWithWork(protected []string, work *spaFieldEncodingWork) (string, bool) {
	if s == nil || !s.valid || s.enums != codecEnumRegistry() {
		return "", false
	}
	if cached := s.identity; cached != nil && spaFieldNamesSame(cached.protected, protected) {
		return cached.id, true
	}
	const projectionBytes = 65536
	selected := make([]int, 0)
	for i, field := range s.fields {
		found := false
		for _, name := range protected {
			for _, key := range spaFieldKeys(name) {
				found = found || field.key == key
			}
		}
		if found {
			selected = append(selected, i)
		}
	}
	if s.encodedSize(selected) > projectionBytes {
		return "", false
	}
	budget := 4096
	var b strings.Builder
	b.WriteByte('{')
	for i, index := range selected {
		field := s.fields[index]
		raw := field.raw
		if raw == "" {
			value := reflect.ValueOf(s.model).Field(field.index)
			if !spaProjectionClosed(value, 0, &budget) {
				return "", false
			}
			encoded, ok := spaEncodeModelField(value, field.declared)
			if work != nil {
				work.FieldEncodes++
				work.FieldEncodedBytes = spaSizeAdd(work.FieldEncodedBytes, uint64(len(encoded)))
			}
			if !ok || uint64(len(encoded)) != field.size {
				return "", false
			}
			raw = string(encoded)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(field.keyJSON)
		b.WriteByte(':')
		b.WriteString(raw)
	}
	b.WriteByte('}')
	id, ok := spaIdentityOf(b.String(), protected)
	if !ok || s.enums != codecEnumRegistry() {
		return "", false
	}
	s.identity = &spaStoredFieldIdentity{protected: append([]string(nil), protected...), indices: selected, id: id}
	return id, true
}

// These certificates cover FULL encoding validity, including excluded fields.
// A previous invalid Float therefore still prevents a false sign-out.
func spaModelFieldsSessionCleared(prev, next *spaModelFieldSnapshot, protected []string) (bool, bool) {
	if prev == nil || next == nil || prev.enums != codecEnumRegistry() || next.enums != prev.enums {
		return false, false
	}
	if !prev.valid || !next.valid {
		return false, true
	}
	for _, name := range protected {
		wasSet, nowClear := false, true
		for _, field := range prev.fields {
			if field.key == name {
				wasSet = !field.null
				break
			}
		}
		for _, field := range next.fields {
			if field.key == name {
				nowClear = field.null
				break
			}
		}
		if wasSet && nowClear {
			return true, true
		}
	}
	return false, true
}

// Only nullness/presence is consumed by the exact-key sign-out decision.
func spaFieldsNullProjection(s *spaModelFieldSnapshot, protected []string) (string, bool) {
	if s == nil || !s.valid || s.enums != codecEnumRegistry() {
		return "", false
	}
	fields := map[string]any{}
	for _, name := range protected {
		for _, field := range s.fields {
			if field.key == name {
				if field.null {
					fields[name] = nil
				} else {
					fields[name] = true
				}
				break
			}
		}
	}
	raw, err := json.Marshal(fields)
	return string(raw), err == nil
}
