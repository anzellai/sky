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
