package rt

// spa_persist_index.go: one parse of a Model's JSON per persistence step.
//
// The reference persistence path gets the Model's JSON from the app's encoder
// (a Sky function the runtime cannot look inside). It then parsed that whole
// document again, every Msg, up to four times: spaPersistStripFields
// (json.Unmarshal into a map, json.Marshal back), spaIdentityOf on the
// stripped result, and spaSessionClearedByStep on the previous and the next
// JSON. Only the top-level members matter to every one of them.
//
// spaTopObject indexes the top-level members once: a validity scan
// (json.Valid) and one walk that records each member's key and the exact text
// of its value, without decoding the values. The identity, the exclusions,
// the stored bytes and the sign-out decision are then derived from the index,
// with the same results as the functions above (proved by the differential
// tests in spa_persist_index_test.go). A step whose previous Model encodes to
// the JSON the step before stored reuses that step's index (one string compare
// instead of one parse).

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// spaTopObject is the top level of a JSON document that
// json.Unmarshal(doc, &map[string]json.RawMessage) accepts: an object or null.
type spaTopObject struct {
	src  string
	null bool     // the document is JSON null (Unmarshal leaves the map nil)
	keys []string // decoded keys, in document order, duplicates kept
	vals []string // the exact source text of each value
}

// spaIndexTop indexes s. ok is false exactly when json.Unmarshal into a
// map[string]json.RawMessage fails.
func spaIndexTop(s string) (x spaTopObject, ok bool) {
	if !json.Valid([]byte(s)) {
		return x, false
	}
	x.src = s
	i := spaSkipSpace(s, 0)
	switch s[i] {
	case 'n':
		x.null = true
		return x, true
	case '{':
	default:
		return x, false // a valid array, string, number or bool
	}
	i = spaSkipSpace(s, i+1)
	if s[i] == '}' {
		return x, true
	}
	for {
		end := spaStringEnd(s, i)
		key, keyOK := spaDecodeKey(s[i:end])
		if !keyOK {
			return spaTopObject{}, false
		}
		i = spaSkipSpace(s, end)
		i = spaSkipSpace(s, i+1) // ':'
		vEnd := spaValueEnd(s, i)
		x.keys = append(x.keys, key)
		x.vals = append(x.vals, s[i:vEnd])
		i = spaSkipSpace(s, vEnd)
		if s[i] == '}' {
			return x, true
		}
		i = spaSkipSpace(s, i+1) // ','
	}
}

func spaSkipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// spaStringEnd returns the index after the closing quote of the string that
// starts at s[i]. s is valid JSON.
func spaStringEnd(s string, i int) int {
	for j := i + 1; ; {
		switch s[j] {
		case '"':
			return j + 1
		case '\\':
			j += 2
		default:
			j++
		}
	}
}

// spaValueEnd returns the index after the value that starts at s[i].
func spaValueEnd(s string, i int) int {
	switch s[i] {
	case '"':
		return spaStringEnd(s, i)
	case '{', '[':
		depth := 0
		for j := i; ; {
			switch s[j] {
			case '"':
				j = spaStringEnd(s, j)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
			j++
		}
	default:
		j := i
		for j < len(s) {
			switch s[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return j
			}
			j++
		}
		return j
	}
}

// spaDecodeKey decodes a key token as json.Unmarshal does for a map key
// (escapes resolved, invalid UTF-8 replaced by U+FFFD).
func spaDecodeKey(tok string) (string, bool) {
	inner := tok[1 : len(tok)-1]
	if !bytesContainsBackslash(inner) && utf8.ValidString(inner) {
		return inner, true
	}
	var k string
	if err := json.Unmarshal([]byte(tok), &k); err != nil {
		return "", false
	}
	return k, true
}

func bytesContainsBackslash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			return true
		}
	}
	return false
}

// get returns the value text of key k, as a map lookup would: the last
// member with that key wins.
func (x *spaTopObject) get(k string) (string, bool) {
	for i := len(x.keys) - 1; i >= 0; i-- {
		if x.keys[i] == k {
			return x.vals[i], true
		}
	}
	return "", false
}

// identity is spaIdentityOf(x.src, protected), and also the identity of the
// document spaPersistStripFields makes from it (a protected field is never
// stripped, and stripping does not change what a value decodes to).
func (x *spaTopObject) identity(protected []string) (string, bool) {
	if x.null {
		return "", false
	}
	out := map[string]any{}
	for _, f := range protected {
		for _, k := range spaFieldKeys(f) {
			raw, present := x.get(k)
			if !present {
				continue
			}
			if spaIsNullRaw(json.RawMessage(raw)) {
				break
			}
			dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
			dec.UseNumber()
			var v any
			if err := dec.Decode(&v); err != nil {
				return "", false
			}
			out[f] = v
			break
		}
	}
	if len(out) == 0 {
		return "", true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// stored is spaPersistStripFields(x.src, exclude, protected) and its length.
// When capBytes > 0 and the result is longer than capBytes, the bytes are not
// built: payload is "" and size is still the exact length.
func (x *spaTopObject) stored(exclude, protected []string, capBytes int) (payload string, size uint64) {
	if len(exclude) == 0 || x.null {
		return x.src, uint64(len(x.src))
	}
	drop := map[string]bool{}
	for _, f := range exclude {
		for _, k := range spaFieldKeys(f) {
			if spaPersistProtectedWireKey(k, protected) {
				continue
			}
			if _, ok := x.get(k); ok {
				drop[k] = true
			}
		}
	}
	if len(drop) == 0 {
		return x.src, uint64(len(x.src))
	}
	// json.Marshal(map[string]json.RawMessage): unique keys (the last value
	// wins), sorted, each value compacted and HTML-escaped.
	last := map[string]int{}
	for i, k := range x.keys {
		if !drop[k] {
			last[k] = i
		}
	}
	keys := make([]string, 0, len(last))
	for k := range last {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w := jsonTreeWriter{limit: -1}
	encKeys := make([][]byte, len(keys))
	vals := make([]string, len(keys))
	size = 2
	for i, k := range keys {
		encKeys[i] = w.appendString(nil, k)
		vals[i] = spaCompactHTML(x.vals[last[k]])
		if i > 0 {
			size++
		}
		size += uint64(len(encKeys[i])) + 1 + uint64(len(vals[i]))
	}
	if capBytes > 0 && size > uint64(capBytes) {
		return "", size
	}
	var b strings.Builder
	b.Grow(int(size))
	b.WriteByte('{')
	for i := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(encKeys[i])
		b.WriteByte(':')
		b.WriteString(vals[i])
	}
	b.WriteByte('}')
	return b.String(), size
}

// spaCompactHTML is the text encoding/json writes for a json.RawMessage
// value: compacted and HTML-escaped. Text that is already in that form (the
// usual case: the encoder's own output) is returned as it is.
func spaCompactHTML(raw string) string {
	in := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '<' || c == '>' || c == '&' || c == 0xE2:
			return spaCompactHTMLSlow(raw)
		case in && c == '\\':
			i++
		case c == '"':
			in = !in
		case !in && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			return spaCompactHTMLSlow(raw)
		}
	}
	return raw
}

func spaCompactHTMLSlow(raw string) string {
	b, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		return raw // unreachable: raw is a value of a valid document
	}
	return string(b)
}

// sessionCleared is spaSessionClearedByStep(prev.src, next.src, fields) for
// two indexed documents.
func spaSessionClearedIndexed(prev, next *spaTopObject, fields []string) bool {
	for _, f := range fields {
		var wasSet, nowClear bool
		if !prev.null {
			raw, _ := prev.get(f)
			wasSet = !spaIsNullRaw(json.RawMessage(raw))
		}
		if next.null {
			nowClear = true
		} else {
			raw, _ := next.get(f)
			nowClear = spaIsNullRaw(json.RawMessage(raw))
		}
		if wasSet && nowClear {
			return true
		}
	}
	return false
}

// spaPersistWriteJSON is spaPersistWrite(st, life,
// spaPersistStripFields(modelJSON, exclude, protected), protected, capBytes)
// from one parse. It returns the index it made (nil when the JSON is not an
// object) so the step's sign-out check can use it too.
func spaPersistWriteJSON(st spaKV, life *spaPersistLife, modelJSON string, exclude, protected []string, capBytes int) *spaTopObject {
	x, ok := spaIndexTop(modelJSON)
	if !ok || x.null {
		return nil // the identity of a non-object is unknown: nothing is written
	}
	id, ok := x.identity(protected)
	if !ok {
		return nil
	}
	payload, size := x.stored(exclude, protected, capBytes)
	spaPersistWriteKnownIdentity(st, life, payload, id, size, capBytes)
	return &x
}

// spaReferencePersist runs the reference path's write and sign-out check for
// one step with one parse of the next JSON, and keeps that index (when the
// JSON is within the snapshot cap) for the next step's previous Model.
type spaReferencePersist struct {
	st        spaKV
	life      *spaPersistLife
	exclude   []string
	protected []string
	capBytes  int
	memo      *spaTopObject // the last step's next JSON, indexed
	next      *spaTopObject
	nextJSON  string
}

func (r *spaReferencePersist) write(nextJSON string) {
	r.nextJSON = nextJSON
	r.next = spaPersistWriteJSON(r.st, r.life, nextJSON, r.exclude, r.protected, r.capBytes)
}

// cleared is spaSessionClearedByStep(prevJSON, nextJSON, protected).
func (r *spaReferencePersist) cleared(prevJSON, nextJSON string, protected []string) bool {
	if prevJSON == "" || nextJSON == "" {
		return false
	}
	next := r.index(nextJSON, r.next)
	prev := r.index(prevJSON, r.memo)
	if next == nil || prev == nil {
		return false
	}
	return spaSessionClearedIndexed(prev, next, protected)
}

func (r *spaReferencePersist) index(s string, known *spaTopObject) *spaTopObject {
	if known != nil && known.src == s {
		return known
	}
	x, ok := spaIndexTop(s)
	if !ok {
		return nil
	}
	return &x
}

// finish keeps this step's next index for the next step. Only a JSON within
// the snapshot cap is kept, the bound the encoding cache already holds.
func (r *spaReferencePersist) finish() {
	r.memo = nil
	if r.next != nil && len(r.next.src) <= spaPersistMaxBytes && len(r.protected) != 0 {
		r.memo = r.next
	}
	r.next, r.nextJSON = nil, ""
}
