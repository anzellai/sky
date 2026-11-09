package rt

// json_tree_writer.go: the single-pass phase-2 JSON writer.
//
// Codec.auto and Json.Encode build a tree of jsonOrderedObject, []any, string,
// int64, float64, bool and nil (phase 1). Phase 2 turns that tree into bytes.
// It used to be json.Marshal(tree): encoding/json called
// jsonOrderedObject.MarshalJSON at every object, MarshalJSON called
// json.Marshal again for every nested value, and encoding/json then compacted
// and HTML-escaped each Marshaler result again. A value at depth d was scanned
// about d times and every level allocated.
//
// jsonAppendTree walks the same tree once into one buffer. The bytes and the
// errors are those json.Marshal(tree) produces, for every tree:
//
//   - The node types above are written here, with encoding/json's exact rules
//     (HTML-safe string escaping, invalid UTF-8 replaced by U+FFFD, U+2028 and
//     U+2029 escaped, float64 in encoding/json's format).
//   - Every other value (a custom Marshaler, json.RawMessage, codecJustNull, a
//     map, a named type, a NaN or infinite float, …) goes to json.Marshal at
//     the same position, so its bytes, its error, its panic and its side
//     effects stay as they were, in the same order.
//   - An error raised inside an object is wrapped in a *json.MarshalerError
//     for jsonOrderedObject once per enclosing object, as encoding/json did
//     when it called MarshalJSON at each level.
//   - Keys and values of an object are read at their position, after the
//     effects of the values before them (as MarshalJSON did).
//
// The proofs are json_tree_writer_test.go: fixtures, a seeded differential
// fuzz of random trees and a native Go fuzz target, all against json.Marshal
// with the previous MarshalJSON.

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"unicode/utf8"
)

var jsonOrderedObjectType = reflect.TypeOf(jsonOrderedObject{})

// jsonSliceCycleDepth matches encoding/json's startDetectingCyclesAfter. A run
// of []any nested deeper than this is handed to json.Marshal, which then keeps
// its own cycle detection for that run (a cyclic []any is an error, as before,
// not an unbounded recursion).
const jsonSliceCycleDepth = 1000

// jsonTreeWriter carries the optional byte limit. limit < 0 means no limit.
// Once the output passes the limit the writer stops writing (over = true) and
// only counts (size), but it keeps walking the rest of the tree in the same
// order, with the same json.Marshal calls for delegated values and the same
// errors, so a limit never changes which failure or which side effect an
// encode produces, nor the encoded length it reports.
type jsonTreeWriter struct {
	limit int
	over  bool
	size  int // the whole encoded length so far, valid once over
}

// jsonMarshalTree is json.Marshal(v) for a phase-1 tree, in one pass.
func jsonMarshalTree(v any) ([]byte, error) {
	w := jsonTreeWriter{limit: -1}
	return w.appendValue(make([]byte, 0, 256), v, 0)
}

// jsonMarshalTreeCapped is jsonMarshalTree with a byte limit. size is always
// the exact length of the encoding. When size > limit it returns no bytes and
// stops building them as soon as the limit is passed; the walk still visits
// every node, so the error (and every delegated Marshaler call) is exactly the
// one jsonMarshalTree would give.
func jsonMarshalTreeCapped(v any, limit int) (out []byte, size int, err error) {
	if limit < 0 {
		limit = 0
	}
	w := jsonTreeWriter{limit: limit}
	out, err = w.appendValue(make([]byte, 0, min(256, limit+1)), v, 0)
	if err != nil {
		return nil, 0, err
	}
	if w.over {
		return nil, w.size, nil
	}
	return out, len(out), nil
}

// fits reports whether n more bytes may be appended. When they may not, the
// writer switches to count-only mode (dropping what it has written) or, in
// that mode already, counts them.
func (w *jsonTreeWriter) fits(b []byte, n int) bool {
	if w.over {
		w.size += n
		return false
	}
	if w.limit >= 0 && len(b)+n > w.limit {
		w.over, w.size = true, len(b)+n
		return false
	}
	return true
}

func (w *jsonTreeWriter) appendByte(b []byte, c byte) []byte {
	if w.fits(b, 1) {
		return append(b, c)
	}
	return b[:0]
}

func (w *jsonTreeWriter) appendBytes(b []byte, s []byte) []byte {
	if w.fits(b, len(s)) {
		return append(b, s...)
	}
	return b[:0]
}

func (w *jsonTreeWriter) appendValue(b []byte, v any, sliceDepth int) ([]byte, error) {
	switch x := v.(type) {
	case jsonOrderedObject:
		out, err := w.appendObject(b, x)
		if err != nil {
			return nil, &json.MarshalerError{Type: jsonOrderedObjectType, Err: err}
		}
		return out, nil
	case []any:
		if x == nil {
			return w.appendBytes(b, jsonNullBytes), nil
		}
		if sliceDepth >= jsonSliceCycleDepth {
			break // json.Marshal below keeps cycle detection for this run
		}
		b = w.appendByte(b, '[')
		for i, e := range x {
			if i > 0 {
				b = w.appendByte(b, ',')
			}
			var err error
			if b, err = w.appendValue(b, e, sliceDepth+1); err != nil {
				return nil, err
			}
		}
		return w.appendByte(b, ']'), nil
	case string:
		return w.appendString(b, x), nil
	case int64:
		var tmp [24]byte
		return w.appendBytes(b, strconv.AppendInt(tmp[:0], x, 10)), nil
	case bool:
		if x {
			return w.appendBytes(b, jsonTrueBytes), nil
		}
		return w.appendBytes(b, jsonFalseBytes), nil
	case nil:
		return w.appendBytes(b, jsonNullBytes), nil
	case float64:
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			var tmp [32]byte
			return w.appendBytes(b, jsonAppendFloat64(tmp[:0], x)), nil
		}
	}
	// Every other value: exactly json.Marshal at this position, as before.
	vb, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return w.appendBytes(b, vb), nil
}

// appendObject writes one object. Errors are returned unwrapped; the caller
// adds the MarshalerError layer encoding/json added around MarshalJSON.
func (w *jsonTreeWriter) appendObject(b []byte, o jsonOrderedObject) ([]byte, error) {
	b = w.appendByte(b, '{')
	for i, k := range o.keys {
		if i > 0 {
			b = w.appendByte(b, ',')
		}
		// Read this key ONLY at its position, after all preceding value
		// effects. Dynamic misses never populate the schema-key cache.
		if encoded, ok := codecAutoSchemaKeys.Load(k); ok {
			s := encoded.(string)
			if w.fits(b, len(s)) {
				b = append(b, s...)
			} else {
				b = b[:0]
			}
		} else {
			b = w.appendString(b, k)
		}
		b = w.appendByte(b, ':')
		var err error
		if b, err = w.appendValue(b, o.vals[i], 0); err != nil {
			return nil, err
		}
	}
	return w.appendByte(b, '}'), nil
}

var (
	jsonNullBytes  = []byte("null")
	jsonTrueBytes  = []byte("true")
	jsonFalseBytes = []byte("false")
)

// appendString writes s as encoding/json does with HTML escaping on. The
// common bytes are handled here; a string with any other byte that needs work
// (a control character other than \n \r \t, invalid UTF-8, U+2028, U+2029) is
// given to json.Marshal whole, so those rules stay encoding/json's own.
func (w *jsonTreeWriter) appendString(b []byte, s string) []byte {
	// The encoding is never shorter than the string plus its two quotes, so a
	// string that cannot fit is counted, never copied.
	if !w.over && w.limit >= 0 && len(b)+len(s)+2 > w.limit {
		w.over, w.size = true, len(b)
		b = b[:0]
	}
	if w.over {
		w.size += jsonStringLen(s)
		return b
	}
	mark := len(b)
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if jsonStringSafe[c] {
				i++
				continue
			}
			var esc string
			switch c {
			case '"':
				esc = `\"`
			case '\\':
				esc = `\\`
			case '\n':
				esc = `\n`
			case '\r':
				esc = `\r`
			case '\t':
				esc = `\t`
			case '<':
				esc = `\u003c`
			case '>':
				esc = `\u003e`
			case '&':
				esc = `\u0026`
			default:
				return w.appendStringCanonical(b[:mark], s)
			}
			b = append(b, s[start:i]...)
			b = append(b, esc...)
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || r == '\u2028' || r == '\u2029' {
			return w.appendStringCanonical(b[:mark], s)
		}
		i += size
	}
	b = append(b, s[start:]...)
	b = append(b, '"')
	if w.limit >= 0 && len(b) > w.limit {
		w.over, w.size = true, len(b)
		return b[:0]
	}
	return b
}

// jsonStringLen is len(appendString(s)) without building it.
func jsonStringLen(s string) int {
	n := 2
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case jsonStringSafe[c]:
				n++
			case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
				n += 2
			case c == '<' || c == '>' || c == '&':
				n += 6
			default:
				q, _ := json.Marshal(s)
				return len(q)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || r == '\u2028' || r == '\u2029' {
			q, _ := json.Marshal(s)
			return len(q)
		}
		n += size
		i += size
	}
	return n
}

func (w *jsonTreeWriter) appendStringCanonical(b []byte, s string) []byte {
	q, _ := json.Marshal(s) // a string never fails to marshal
	return w.appendBytes(b, q)
}

// jsonStringSafe marks the ASCII bytes encoding/json copies unchanged inside a
// string when HTML escaping is on (its htmlSafeSet): printable ASCII and DEL,
// except '"', '\\', '<', '>' and '&'.
var jsonStringSafe = func() (t [utf8.RuneSelf]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		t[c] = true
	}
	for _, c := range []byte{'"', '\\', '<', '>', '&'} {
		t[c] = false
	}
	return
}()

// jsonAppendFloat64 is encoding/json's float64 encoder (floatEncoder, 64 bits)
// for a finite value.
func jsonAppendFloat64(b []byte, f float64) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		// clean up e-09 to e-9
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}

// jsonEncodeTree is the phase-2 entry for Json.Encode.encode: compact when
// indent <= 0, otherwise json.MarshalIndent's exact layout (Marshal, then
// Indent with an empty prefix).
func jsonEncodeTree(v any, indent int) ([]byte, error) {
	b, err := jsonMarshalTree(v)
	if err != nil || indent <= 0 {
		return b, err
	}
	var out bytes.Buffer
	out.Grow(2 * len(b))
	if err := json.Indent(&out, b, "", string(bytes.Repeat([]byte{' '}, indent))); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
