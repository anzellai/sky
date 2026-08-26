package rt

import (
	"fmt"
	"io"
	"os"
)

// Secret is the Go representation of a Sky `Sky.Core.Secret.Secret` — an opaque,
// self-redacting secret handle.
//
// Every path by which a value can be printed, formatted, or serialised is
// overridden to emit "[REDACTED]" instead of the wrapped bytes:
//
//   - String()      → fmt %s, %v, and any Stringer-aware logger
//   - GoString()    → fmt %#v
//   - Format()      → the fmt.Formatter fast-path for every verb
//   - MarshalJSON() → encoding/json (a Secret in a struct/map, a JSON body)
//
// So a Secret that leaks into a log line, a `fmt.Sprintf("%v", …)`, or a JSON
// response never exposes the value. The raw bytes come out ONLY through
// secretReveal (the Secret_reveal kernel) — the single, greppable escape hatch.
//
// The field is unexported so no other package can read it without going through
// secretReveal, and the struct is passed by value (a small string header) so a
// copy carries the same redaction.
type Secret struct{ v string }

// String redacts the secret for %s / %v and any Stringer-aware logger.
func (s Secret) String() string { return "[REDACTED]" }

// GoString redacts the secret for %#v.
func (s Secret) GoString() string { return "[REDACTED]" }

// Format redacts the secret for EVERY fmt verb (fmt.Formatter takes precedence
// over Stringer), closing the %d/%x/%q/… holes String() alone would leave.
func (s Secret) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, "[REDACTED]")
}

// MarshalJSON redacts the secret in any encoding/json path (a Secret field on a
// struct, a value in a map, a JSON response body).
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

// Secret_fromEnv reads the named environment variable into a Secret. A missing
// variable yields an empty Secret (the boundary check at the consumer — e.g.
// coerceAuthSecret's minimum-length gate — is what rejects an unset secret, with
// an actionable message).
func Secret_fromEnv(name any) any { return Secret{v: os.Getenv(AsString(name))} }

// Secret_fromString promotes a runtime string into a Secret (backs both
// Secret.fromString and Secret.unsafeFromString).
func Secret_fromString(s any) any { return Secret{v: AsString(s)} }

// Secret_reveal returns the raw string — the one escape hatch (Secret.reveal).
func Secret_reveal(v any) any { return secretReveal(v) }

// secretReveal extracts the raw value from a Secret. It also accepts a bare
// string so that during the migration a caller still passing a plain String
// where a Secret is now expected keeps working (the typed Sky surface will have
// been updated to Secret; this is the runtime's tolerance for a not-yet-migrated
// FFI or dynamic path). Anything else is coerced to its string form.
func secretReveal(v any) string {
	switch s := v.(type) {
	case Secret:
		return s.v
	case string:
		return s
	default:
		return AsString(v)
	}
}
