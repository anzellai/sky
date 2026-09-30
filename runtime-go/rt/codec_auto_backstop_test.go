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
