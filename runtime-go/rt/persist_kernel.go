// persist_kernel.go — Std.Persist runtime support.
//
// Persist_keyString reflects a record's key field to a string (the KV-store key
// for the unified data layer), WITHOUT a JSON round-trip — the codec renames
// fields to snake_case, so a round-trip would look up the wrong key and silently
// yield "". Reflecting the Go struct field directly is O(1) and snake-safe.
package rt

import (
	"strconv"
	"strings"
)

// Persist_keyString : String -> a -> Result Error String
// `field` is a Sky record field name (e.g. "id" / "userId"); it lowers to a
// title-cased Go struct field ("Id" / "UserId"). Fails (Err) on a missing/nil or
// non-scalar field rather than yielding an empty key.
func Persist_keyString(fieldArg, recordArg any) any {
	field := AsString(fieldArg)
	if field == "" {
		return Err[any, any](ErrInvalidInput("Persist: empty key field"))
	}
	goName := strings.ToUpper(field[:1]) + field[1:]
	val := Field(recordArg, goName)
	if val == nil {
		val = Field(recordArg, field) // already-capitalized fallback
	}
	if val == nil {
		return Err[any, any](ErrInvalidInput("Persist: key field \"" + field + "\" not found or nil"))
	}
	s, ok := persistScalarKey(val)
	if !ok {
		return Err[any, any](ErrInvalidInput("Persist: key field \"" + field + "\" is not a scalar (string/int/bool)"))
	}
	return Ok[any, any](s)
}

func persistScalarKey(v any) (string, bool) {
	switch x := unwrapAny(v).(type) {
	case string:
		return x, true
	case int:
		return strconv.FormatInt(int64(x), 10), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}
