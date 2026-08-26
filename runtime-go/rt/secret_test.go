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

// A DSN's password is the one credential a sql.Open / Ping error can echo into
// a log or an Err. redactSecretsInDSN must mask it in both DSN spellings while
// leaving host / user / database visible.
func TestRedactSecretsInDSN(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			"url form",
			"db connect: failed dialing postgres://admin:s3cr3t@db.internal:5432/app",
			"db connect: failed dialing postgres://admin:[REDACTED]@db.internal:5432/app",
		},
		{
			"keyword form",
			"host=db.internal user=admin password=s3cr3t dbname=app sslmode=require",
			"host=db.internal user=admin password=[REDACTED] dbname=app sslmode=require",
		},
		{
			"quoted keyword password",
			"user=admin password='s3 cr3t' dbname=app",
			"user=admin password=[REDACTED] dbname=app",
		},
		{
			"no password — untouched",
			"host=/var/run/postgresql user=admin dbname=app",
			"host=/var/run/postgresql user=admin dbname=app",
		},
	} {
		if got := redactSecretsInDSN(tc.in); got != tc.want {
			t.Errorf("%s:\n  got  %q\n  want %q", tc.name, got, tc.want)
		}
		if got := redactSecretsInDSN(tc.in); got != tc.want && (got == tc.in) {
			t.Errorf("%s: the password survived redaction", tc.name)
		}
	}
	// The raw password must never appear in the output.
	leak := redactSecretsInDSN("postgres://u:TOPSECRET@h/d password=TOPSECRET")
	if containsSubstr(leak, "TOPSECRET") {
		t.Errorf("redaction leaked the password: %q", leak)
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
