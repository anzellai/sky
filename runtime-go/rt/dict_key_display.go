package rt

import "fmt"

// Rendering an encoded Dict key for a HUMAN.
//
// `encodeDictKey` writes a kind tag into the runtime map key so that a
// key-polymorphic helper can decode it back (see the typed-key section of
// rt.go). That tag is an INTERNAL representation and never reaches a person:
// `Debug.toString` renders a Dict through `dictEntries` (sky_show.go), and the
// session-store inspector names a key through `detagDisplayKey`.

// detagDisplayKey turns one encoded map key back into what the user wrote.
// An untagged key is its own display form.
func detagDisplayKey(raw string) string {
	if k, _, ok := decodeTaggedDictKey(raw); ok {
		if s, isStr := k.(string); isStr {
			return s
		}
		return fmt.Sprintf("%v", k)
	}
	return raw
}

// dictJSONKey is the JSON object key Codec.auto writes for one stored Dict key.
//
// A non-String key is written as its plain text: the decimal of an Int
// (`"10"`), the `%v` text of a Float or Bool, the code point of a Char. Before
// v0.29.9 the stored form (`"\u0001i10"`) reached the JSON, which a Sky
// decoder read back but every other JSON reader saw as a wrong key. The plain
// text is the bare form the runtime already reads for a Dict that came from
// outside (a foreign Dict probes it, see dictProbe), so a decoder that does not
// know the key type still finds every entry.
//
// A String key is written as stored. That keeps the escape of a String key that
// starts with the tag byte (`"\u0001s…"`), so it never reads back as an Int key.
func dictJSONKey(stored string) string {
	if len(stored) >= 2 && stored[0] == dictKeyTagByte {
		switch stored[1] {
		case 'i', 'f', 'c', 'b':
			return stored[2:]
		}
	}
	return stored
}
