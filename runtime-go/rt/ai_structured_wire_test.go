//go:build !js

package rt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// The wire of typed tools and structured answers. Std.Ai.Provider writes the
// request (each tool's `parameters` JSON Schema and the `response_format`);
// these tests prove the runtime's transports carry both to the server exactly
// as written, fill only the model, and hand the server's canned answer back
// unchanged for the Sky side to decode with the codec.

const aiTypedRequest = `{"model":"","messages":[{"role":"user","content":"Oslo?"}],` +
	`"tools":[{"type":"function","function":{"name":"forecast","description":"The forecast for a city",` +
	`"parameters":{"type":"object","properties":{"city":{"type":"string"},"days":{"type":"integer"}},"required":["city","days"],"additionalProperties":false}}}],` +
	`"response_format":{"type":"json_schema","json_schema":{"name":"reply","schema":{"type":"object","properties":{"summary":{"type":"string"},"days":{"type":"integer"},"note":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["summary","days","note"],"additionalProperties":false},"strict":true}}}`

const aiCannedToolCall = `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"forecast","arguments":"{\"city\":\"Oslo\",\"days\":2}"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func jsonSame(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("not JSON: %v: %s", err, a)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("not JSON: %v: %s", err, b)
	}
	return reflect.DeepEqual(x, y)
}

// A remote slot sends the tools' schemas and the response_format as written,
// with only the model filled in, and returns the server's answer verbatim.
func TestAiRemoteSlotCarriesToolSchemasAndResponseFormat(t *testing.T) {
	aiTestEnv(t, nil)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Write([]byte(aiCannedToolCall))
	}))
	defer srv.Close()

	b := endpointBuilder("chat")
	b.url, b.model = srv.URL+"/v1", "base-8b"
	s, err := aiSlotFor(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.post("/chat/completions", aiTypedRequest)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	json.Unmarshal([]byte(aiTypedRequest), &want)
	want["model"] = "base-8b"
	wantJSON, _ := json.Marshal(want)
	if !jsonSame(t, got, string(wantJSON)) {
		t.Fatalf("the server received\n%s\nwant\n%s", got, wantJSON)
	}
	if out != aiCannedToolCall {
		t.Fatalf("the answer changed on the way back:\n%s", out)
	}
}

// A local engine with named adapters adds its `lora` list and keeps the tool
// schemas and the response_format.
func TestAiLocalAdapterRoutingKeepsToolSchemasAndResponseFormat(t *testing.T) {
	spec := aiLocalSpec{adapters: []aiNamedAdapter{{name: "support", scale: 0.5}}}
	routed, err := aiRouteAdapter(spec, aiTypedRequest)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	json.Unmarshal([]byte(aiTypedRequest), &want)
	want["lora"] = []any{map[string]any{"id": float64(0), "scale": float64(0)}}
	wantJSON, _ := json.Marshal(want)
	if !jsonSame(t, routed, string(wantJSON)) {
		t.Fatalf("routed\n%s\nwant\n%s", routed, wantJSON)
	}
}
