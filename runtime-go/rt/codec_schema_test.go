package rt

import (
	"encoding/json"
	"testing"
)

type csEnumHolder struct {
	Kind  int     `sky:"kind,csKind"`
	Score float64 `sky:"score,float64"`
}

// The schema of an auto codec follows the auto encoder: snake_case keys, a
// closed object with every field required, Maybe as a nullable value, a list
// as an array, a nested record as a nested object, and a registered enum as
// its names.
func TestCodecAutoSchemaFollowsTheEncoder(t *testing.T) {
	got := Codec_autoSchema(true, acUser{}).(string)
	want := `{"fields":[` +
		`{"name":"id","schema":{"type":"string"},"exact":true},` +
		`{"name":"age","schema":{"type":"integer"},"exact":true},` +
		`{"name":"active","schema":{"type":"boolean"},"exact":true},` +
		`{"name":"nick","schema":{"anyOf":[{"type":"string"},{"type":"null"}]},"exact":true},` +
		`{"name":"address","schema":{"type":"object","properties":{"city":{"type":"string"},"zip":{"type":"string"}},"required":["city","zip"],"additionalProperties":false},"exact":true},` +
		`{"name":"tags","schema":{"type":"array","items":{"type":"string"}},"exact":true}]}`
	if got != want {
		t.Fatalf("schema\n got %s\nwant %s", got, want)
	}
	var v any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("not JSON: %v", err)
	}

	RegisterEnum("csKind", []string{"Bug", "Feature"})
	defer delete(enumRegistry, "csKind")
	got = Codec_autoSchema(false, csEnumHolder{}).(string)
	want = `{"fields":[` +
		`{"name":"kind","schema":{"type":"string","enum":["Bug","Feature"]},"exact":true},` +
		`{"name":"score","schema":{"type":"number"},"exact":true}]}`
	if got != want {
		t.Fatalf("enum schema\n got %s\nwant %s", got, want)
	}

	// A value that is not a record has no field list.
	if got := Codec_autoSchema(true, "x").(string); got != `{"fields":null}` {
		t.Fatalf("a string witness: %s", got)
	}
	// A Json.Value field is open, so the schema is not exact.
	type withValue struct {
		Raw JsonValue `sky:"raw,rt.JsonValue"`
	}
	if got := Codec_autoSchema(true, withValue{}).(string); got != `{"fields":[{"name":"raw","schema":{},"exact":false}]}` {
		t.Fatalf("an open field: %s", got)
	}
}
