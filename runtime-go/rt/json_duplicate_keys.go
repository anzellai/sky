package rt

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Duplicate member names (v0.31.0).
//
// JSON text with a repeated key in one object is refused by every Sky decoder:
// `Json.Decode.decodeString`, `Codec.fromJson`, `Codec.auto`, a Sky.Spa RPC
// payload and a `Std.Db.Store` JSON column all parse through
// jsonParseDocument, which calls jsonCheckDuplicateKeys. This is the I-JSON
// rule (RFC 7493 section 2.3) and the default of Go's encoding/json/v2.
//
// Before v0.31.0 the parse kept the LAST value of a repeated key, as
// encoding/json v1 does, and dropped the others with no error. Two readers of
// one document could then see two different values: a proxy or a signature
// check that keeps the first, and Sky that keeps the last.
//
// The check is a second pass over text the parser has already accepted, so it
// can assume the text is valid JSON. It keeps its own stack (no recursion), so
// a deep document cannot grow the Go stack, and it costs one pass over the
// bytes plus one comparison per key of each object.

// jsonDuplicateKeyError names a repeated key and the path of its object.
type jsonDuplicateKeyError struct {
	key  string
	path string
}

func (e *jsonDuplicateKeyError) Error() string {
	where := "the top-level object"
	if e.path != "" {
		where = "the object at " + e.path
	}
	return "duplicate key " + jsonQuoteKey(e.key) + " in " + where +
		" (a JSON object may not repeat a key)"
}

// jsonQuoteKey writes a key as the JSON string `Json.Encode.encode` writes for
// it. Decode errors put keys in their path in this form (`["a b"]`), and
// `Std.Codec` builds the same text in Sky with `Encode.encode 0 (Encode.string
// k)`, so both name a key the same way.
func jsonQuoteKey(k string) string {
	w := jsonTreeWriter{limit: -1}
	return string(w.appendString(nil, k))
}

// jsonPathField is the path segment of an object member: `.name` for a key
// that is an identifier, `["…"]` for any other key.
func jsonPathField(k string) string {
	if jsonIsIdent(k) {
		return "." + k
	}
	return "[" + jsonQuoteKey(k) + "]"
}

// jsonPathKey is the path segment of a key of a JSON object read as a map
// (`keyValuePairs`, `dict`): always `["…"]`, so it never reads as a field.
func jsonPathKey(k string) string {
	return "[" + jsonQuoteKey(k) + "]"
}

func jsonIsIdent(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

type jsonDupFrame struct {
	object bool
	index  int      // array: index of the current element
	key    string   // object: the key of the current member
	keys   []string // object: the keys seen so far (small objects)
	set    map[string]struct{}
}

// jsonDupSetAt is the member count at which a frame moves its keys from a
// slice to a map. Most objects are small, and a slice scan is quicker there.
const jsonDupSetAt = 16

func (f *jsonDupFrame) seen(k string) bool {
	if f.set != nil {
		if _, ok := f.set[k]; ok {
			return true
		}
		f.set[k] = struct{}{}
		return false
	}
	for _, s := range f.keys {
		if s == k {
			return true
		}
	}
	f.keys = append(f.keys, k)
	if len(f.keys) >= jsonDupSetAt {
		f.set = make(map[string]struct{}, len(f.keys)*2)
		for _, s := range f.keys {
			f.set[s] = struct{}{}
		}
		f.keys = nil
	}
	return false
}

// jsonCheckDuplicateKeys reports the first object in the valid JSON text s
// that repeats a key, or nil. Keys compare as the parser stores them: after
// escapes are read, and with invalid UTF-8 replaced as encoding/json replaces
// it, so two spellings of one key (`"a"` and `"a"`) are a repeat.
func jsonCheckDuplicateKeys(s string) error {
	if strings.IndexByte(s, '{') < 0 {
		return nil
	}
	var stack []jsonDupFrame
	expectKey := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '{':
			stack = append(stack, jsonDupFrame{object: true})
			expectKey = true
		case '[':
			stack = append(stack, jsonDupFrame{})
			expectKey = false
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			expectKey = false
		case ',':
			if n := len(stack); n > 0 {
				if stack[n-1].object {
					expectKey = true
				} else {
					stack[n-1].index++
				}
			}
		case '"':
			end := jsonStringEnd(s, i)
			if expectKey && len(stack) > 0 {
				top := &stack[len(stack)-1]
				k := jsonKeyText(s[i : end+1])
				if top.seen(k) {
					return &jsonDuplicateKeyError{key: k, path: jsonDupPath(stack[:len(stack)-1])}
				}
				top.key = k
				expectKey = false
			}
			i = end
		}
	}
	return nil
}

// jsonStringEnd is the index of the quote that closes the string opening at
// s[start].
func jsonStringEnd(s string, start int) int {
	for j := start + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return len(s) - 1
}

// jsonKeyText is the key a quoted JSON string stands for, as the parser stores
// it. A plain ASCII or valid UTF-8 key with no escape is its own text.
func jsonKeyText(quoted string) string {
	inner := quoted[1 : len(quoted)-1]
	if strings.IndexByte(inner, '\\') < 0 && utf8.ValidString(inner) {
		return inner
	}
	var out string
	if err := json.Unmarshal([]byte(quoted), &out); err != nil {
		return inner
	}
	return out
}

// jsonDupPath is the path of the innermost frame's object: each enclosing
// object's current key and each enclosing array's current index.
func jsonDupPath(frames []jsonDupFrame) string {
	var b strings.Builder
	for _, f := range frames {
		if f.object {
			b.WriteString(jsonPathField(f.key))
		} else {
			b.WriteString("[" + strconv.Itoa(f.index) + "]")
		}
	}
	return b.String()
}
