package rt

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// B-1 runtime backstop (codec_auto_backstop.go). Before it, a Secret or key
// field encoded as `{}` (silent loss), a function field panicked with no
// class, and a runtime handle decoded from client JSON.

type bkSecretRec struct {
	Name   string `sky:"name,string"`
	Secret Secret `sky:"secret,rt.Secret"`
}

type bkKeyRec struct {
	Key SkyMaybe[any] `sky:"key,rt.SkyMaybe[any]"`
}

type bkFuncRec struct {
	F func(any) any `sky:"f,func(any) any"`
}

type bkHandleRec struct {
	P SkyADT `sky:"p,Sky_Core_Process_Process"`
}

type bkJSONRec struct {
	V JsonValue `sky:"v,rt.JsonValue"`
}

// encodePanic runs Codec_autoEnc and returns the panic message ("" if none).
func encodePanic(v any) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprintf("%v", r)
		}
	}()
	_ = Codec_autoEnc(false, v)
	return ""
}

func TestCodecAutoRefusesToEncodeWhatHasNoJSONForm(t *testing.T) {
	cases := map[string]any{
		"Secret field":        bkSecretRec{Name: "ada", Secret: Secret{v: "k"}},
		"Maybe Kx.SecretKey":  bkKeyRec{Key: Just[any](KxSecretKey{k: "x"})},
		"Noise transport":     NoiseTransport{},
		"function field":      bkFuncRec{F: func(a any) any { return a }},
		"Process handle":      bkHandleRec{P: SkyADT{SkyName: "Process", Fields: []any{7}}},
		"opaque Cache handle": SkyADT{SkyName: "Cache__Internal", Fields: []any{3}},
		"Ed25519 signing key": SignSecretKey{k: "s"},
	}
	for name, v := range cases {
		msg := encodePanic(v)
		if msg == "" {
			t.Fatalf("%s: Codec.auto encoded it (silently, as {} or a handle id)", name)
		}
		if kind, _ := classifyPanic(msg); kind != "JsonEncodeFailure" {
			t.Fatalf("%s: panic %q is classified %s, want JsonEncodeFailure", name, msg, kind)
		}
	}
}

func decodeInto(t *testing.T, witness any, text string) SkyResult[any, any] {
	t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatal(err)
	}
	return Codec_autoDecoder(false, witness).(JsonDecoder).run(raw).(SkyResult[any, any])
}

func TestCodecAutoRefusesToDecodeSecretsAndHandles(t *testing.T) {
	if r := decodeInto(t, bkSecretRec{}, `{"name":"ada","secret":{}}`); r.Tag == 0 {
		t.Fatalf("a Secret was built from client JSON: %#v", r.OkValue)
	}
	r := decodeInto(t, bkHandleRec{}, `{"p":{"tag":"Process","v0":1}}`)
	if r.Tag == 0 {
		t.Fatalf("a Process handle was decoded from client JSON: %#v", r.OkValue)
	}
	if !strings.Contains(errorMessage(r.ErrValue), "handle") {
		t.Fatalf("handle refusal does not say why: %s", errorMessage(r.ErrValue))
	}
}

func TestCodecAutoCarriesAJsonValueField(t *testing.T) {
	in := bkJSONRec{V: JsonValue{raw: map[string]any{"a": 1.0}}}
	b, err := json.Marshal(Codec_autoEnc(false, in).(JsonValue).raw)
	if err != nil || !strings.Contains(string(b), `"a":1`) {
		t.Fatalf("Json.Value field encoded as %s (%v)", b, err)
	}
	r := decodeInto(t, bkJSONRec{}, string(b))
	if r.Tag != 0 {
		t.Fatalf("Json.Value field did not decode: %s", errorMessage(r.ErrValue))
	}
}

// A `Json.Value` record field lowers to `any` with the declared type `any`
// (goty erases the kernel `Value` handle), not to `JsonValue`. Codec.auto
// decodes it as JSON when the blank holds a JSON value there, and refuses an
// `any` slot whose blank holds anything else (a key type erased to `any` is
// never built from JSON).
type bkAnyJSONRec struct {
	Id   string        `sky:"id,string"`
	Data any           `sky:"data,any"`
	More SkyMaybe[any] `sky:"more,rt.SkyMaybe[any]"`
	In   bkAnyJSONIn   `sky:"in,bkAnyJSONIn"`
}

type bkAnyJSONIn struct {
	V any `sky:"v,any"`
}

func TestCodecAutoDecodesAnErasedJsonValueField(t *testing.T) {
	blank := bkAnyJSONRec{
		Data: JsonValue{raw: nil},
		More: Just[any](JsonValue{raw: nil}),
		In:   bkAnyJSONIn{V: JsonValue{raw: nil}},
	}
	text := `{"id":"h1","data":{"data":null,"tag":"X","errors":[]},"more":[1],"in":{"v":"s"}}`
	r := decodeInto(t, blank, text)
	if r.Tag != 0 {
		t.Fatalf("an erased Json.Value field did not decode: %s", errorMessage(r.ErrValue))
	}
	back := r.OkValue.(bkAnyJSONRec)
	b, err := json.Marshal(Codec_autoEnc(false, back).(JsonValue).raw)
	if err != nil {
		t.Fatal(err)
	}
	// The decoded object is a Go map, so the keys come back sorted.
	if string(b) != `{"id":"h1","data":{"data":null,"errors":[],"tag":"X"},"more":[1],"in":{"v":"s"}}` {
		t.Fatalf("round trip = %s", b)
	}
	if r := decodeInto(t, blank, `{"id":"h2","data":null,"more":null,"in":{"v":null}}`); r.Tag != 0 {
		t.Fatalf("JSON null in a Json.Value field did not decode: %s", errorMessage(r.ErrValue))
	}
	// An `any` slot whose blank is not a JSON value stays refused.
	if r := decodeInto(t, bkKeyRec{Key: Just[any](KxSecretKey{k: "x"})}, `{"key":{"k":"y"}}`); r.Tag == 0 {
		t.Fatalf("a key slot erased to any was built from JSON: %#v", r.OkValue)
	}
	if r := decodeInto(t, bkAnyJSONIn{V: 0}, `{"v":{"a":1}}`); r.Tag == 0 {
		t.Fatalf("an any slot whose blank is not JSON was built from JSON: %#v", r.OkValue)
	}
}
