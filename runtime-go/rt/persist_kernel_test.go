package rt

import "testing"

// Mimics a lowered Sky record: Sky fields id/userId/age → Go fields Id/UserId/Age.
type persistTestRec struct {
	Id     string
	UserId string
	Age    int
	Active bool
	Blob   []string
}

func keyOK(t *testing.T, field string, rec any, want string) {
	t.Helper()
	res := Persist_keyString(field, rec)
	ok, isOk := res.(SkyResult[any, any])
	if !isOk || ok.Tag != 0 {
		t.Fatalf("keyString(%q): expected Ok, got %#v", field, res)
	}
	if got := AsString(ok.OkValue); got != want {
		t.Fatalf("keyString(%q): got %q want %q", field, got, want)
	}
}

func keyErr(t *testing.T, field string, rec any) {
	t.Helper()
	res := Persist_keyString(field, rec)
	r, isOk := res.(SkyResult[any, any])
	if !isOk || r.Tag != 1 {
		t.Fatalf("keyString(%q): expected Err, got %#v", field, res)
	}
}

func TestPersistKeyString(t *testing.T) {
	rec := persistTestRec{Id: "u1", UserId: "acct-42", Age: 30, Active: true, Blob: []string{"x"}}

	// string field via title-case (id → Id)
	keyOK(t, "id", rec, "u1")
	// non-"id" field that the codec would snake-case to user_id — reflection must
	// find the Go field UserId, NOT look up a JSON "user_id" key.
	keyOK(t, "userId", rec, "acct-42")
	// int → string
	keyOK(t, "age", rec, "30")
	// bool → string
	keyOK(t, "active", rec, "true")

	// missing field → Err (never "")
	keyErr(t, "nope", rec)
	// non-scalar field → Err (never "")
	keyErr(t, "blob", rec)
	// empty field name → Err
	keyErr(t, "", rec)
}
