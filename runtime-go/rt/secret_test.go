package rt

import (
	"encoding/json"
	"fmt"
	"testing"
)

// A Secret must redact itself in EVERY print/format/serialise path — this is the
// runtime backstop behind the compile-time opacity of the Sky `Secret` type. A
// regression here (a new %verb hole, a lost Format method) re-opens a real
// secret-leak channel, so every path is pinned.
func TestSecretRedactsEveryPath(t *testing.T) {
	s := Secret_fromString("hunter2").(Secret)

	for _, tc := range []struct {
		what string
		got  string
	}{
		{"%s", fmt.Sprintf("%s", s)},
		{"%v", fmt.Sprintf("%v", s)},
		{"%#v", fmt.Sprintf("%#v", s)},
		{"%q", fmt.Sprintf("%q", s)},
		{"%d (Format catches non-string verbs)", fmt.Sprintf("%d", s)},
		{"String()", s.String()},
		{"GoString()", s.GoString()},
	} {
		if tc.got != "[REDACTED]" {
			t.Errorf("%s leaked the secret: %q (want [REDACTED])", tc.what, tc.got)
		}
	}

	// encoding/json — a bare Secret and a struct/map that contains one.
	if b, err := json.Marshal(s); err != nil || string(b) != `"[REDACTED]"` {
		t.Errorf("json.Marshal(Secret) = %s err=%v (want \"[REDACTED]\")", b, err)
	}
	type creds struct{ Token Secret }
	if b, _ := json.Marshal(creds{Token: s}); string(b) != `{"Token":"[REDACTED]"}` {
		t.Errorf("json of struct-with-Secret leaked: %s", b)
	}
	if b, _ := json.Marshal(map[string]Secret{"k": s}); string(b) != `{"k":"[REDACTED]"}` {
		t.Errorf("json of map-with-Secret leaked: %s", b)
	}

	// reveal (secretReveal) is the ONE way to the raw bytes.
	if got := secretReveal(s); got != "hunter2" {
		t.Errorf("secretReveal = %q (want hunter2)", got)
	}
	// Transitional tolerance: a bare string reveals to itself.
	if got := secretReveal("plain"); got != "plain" {
		t.Errorf("secretReveal(string) = %q (want plain)", got)
	}
}
