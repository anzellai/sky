// server_response_bridge_test.go — v0.15.44 fix-up regression specs.
//
// Layer-3 stdlib's typed `Sky.Http.Server.Response` record alias
// (Sky_Http_Server_Response_R) now flows into every kernel boundary
// where the runtime used to do `.(SkyResponse)` directly. The
// asSkyResponse / asSkyRequest helpers bridge both shapes through a
// single reflect-extract path; this spec pins:
//
//   1. The fast-path: an SkyResponse value goes through unchanged
//      (including StreamHandler).
//   2. The reflect path: any struct with matching field names
//      (Status/Body/Headers/ContentType) decodes correctly.
//   3. StreamHandler preservation across the typed-record boundary
//      via the pendingStreamHandlers sentinel (ServerStream_stream
//      stamps the token into Body; asSkyResponse restores the
//      closure + clears the sentinel).
//   4. Symmetric asSkyRequest behaviour for the inbound side.

package rt

import (
	"testing"
)

// userResponse mimics what typed codegen emits for the user-side
// `type alias Response = { status, body, headers, contentType }`.
// Same field names + types as SkyResponse minus StreamHandler.
type userResponse struct {
	Status      int
	Body        string
	Headers     map[string]string
	ContentType string
}

// userRequest mirrors codegen output for `type alias Request = …`.
// Headers / Params / Query carry concrete map[string]string here,
// whereas rt.SkyRequest's matching fields are map[string]any.
type userRequest struct {
	Method     string
	Path       string
	Body       string
	Headers    map[string]string
	Params     map[string]string
	Query      map[string]string
	Cookies    map[string]string
	RemoteAddr string
}

func TestAsSkyResponse_FastPath_PassesThroughIncludingStreamHandler(t *testing.T) {
	dummy := func() {}
	src := SkyResponse{
		Status:        200,
		Body:          "hello",
		ContentType:   "text/plain",
		Headers:       map[string]string{"X-Foo": "1"},
		StreamHandler: dummy,
	}
	out, ok := asSkyResponse(src)
	if !ok {
		t.Fatal("asSkyResponse should return ok=true for direct SkyResponse")
	}
	if out.Status != 200 || out.Body != "hello" || out.ContentType != "text/plain" {
		t.Errorf("fast-path field mismatch: %+v", out)
	}
	if out.Headers["X-Foo"] != "1" {
		t.Errorf("Headers not preserved: %v", out.Headers)
	}
	if out.StreamHandler == nil {
		t.Error("StreamHandler dropped on fast path")
	}
}

func TestAsSkyResponse_TypedRecordBridge(t *testing.T) {
	// Codegen-shape struct — exact field names match.
	src := userResponse{
		Status:      404,
		Body:        "missing",
		Headers:     map[string]string{"Content-Length": "7"},
		ContentType: "text/plain",
	}
	out, ok := asSkyResponse(src)
	if !ok {
		t.Fatal("asSkyResponse should bridge typed user record")
	}
	if out.Status != 404 {
		t.Errorf("Status: got %d, want 404", out.Status)
	}
	if out.Body != "missing" {
		t.Errorf("Body: got %q, want missing", out.Body)
	}
	if out.ContentType != "text/plain" {
		t.Errorf("ContentType: got %q, want text/plain", out.ContentType)
	}
	if out.Headers["Content-Length"] != "7" {
		t.Errorf("Headers not bridged: %v", out.Headers)
	}
}

func TestAsSkyResponse_NilPathReturnsNotOk(t *testing.T) {
	if _, ok := asSkyResponse(nil); ok {
		t.Error("asSkyResponse(nil) should return ok=false")
	}
	// Bare int has no matching fields.
	if _, ok := asSkyResponse(42); ok {
		t.Error("asSkyResponse(42) should return ok=false")
	}
}

func TestAsSkyResponse_StreamHandlerSurvivesTypedBoundary(t *testing.T) {
	// Simulate the v0.15.44 path: ServerStream_stream registers a
	// handler + stamps the sentinel token in Body. The user's
	// handler return flows through TaskCoerceT → narrowStructToStruct
	// which drops StreamHandler (typed record has no slot). The
	// listener then calls asSkyResponse with the typed record;
	// StreamHandler must be restored from pendingStreamHandlers.
	called := false
	handler := func() { called = true }
	token := registerPendingStreamHandler(handler)

	typedRecord := userResponse{
		Status:      200,
		ContentType: "text/event-stream",
		Body:        pendingStreamSentinelPrefix + token,
	}
	out, ok := asSkyResponse(typedRecord)
	if !ok {
		t.Fatal("asSkyResponse should bridge typed record")
	}
	if out.StreamHandler == nil {
		t.Fatal("StreamHandler not restored from pendingStreamHandlers")
	}
	if out.Body != "" {
		t.Errorf("sentinel not stripped from Body: %q", out.Body)
	}
	// The closure should still be callable.
	out.StreamHandler.(func())()
	if !called {
		t.Error("restored StreamHandler not invokable")
	}
	// Token should be one-shot (drained from the registry).
	if _, found := takePendingStreamHandler(token); found {
		t.Error("token should be drained after first lookup")
	}
}

func TestAsSkyRequest_FastPath(t *testing.T) {
	src := SkyRequest{
		Method:     "POST",
		Path:       "/api/x",
		Body:       "{}",
		Headers:    map[string]any{"Content-Type": "application/json"},
		RemoteAddr: "127.0.0.1:1234",
	}
	out, ok := asSkyRequest(src)
	if !ok {
		t.Fatal("asSkyRequest should ok=true on fast path")
	}
	if out.Method != "POST" || out.Path != "/api/x" || out.Body != "{}" {
		t.Errorf("fields mismatch: %+v", out)
	}
}

func TestAsSkyRequest_TypedRecordBridge(t *testing.T) {
	src := userRequest{
		Method:     "GET",
		Path:       "/users/42",
		Body:       "",
		Headers:    map[string]string{"User-Agent": "curl/8"},
		Params:     map[string]string{"id": "42"},
		Query:      map[string]string{"q": "search"},
		Cookies:    map[string]string{"sid": "abc"},
		RemoteAddr: "127.0.0.1:5050",
	}
	out, ok := asSkyRequest(src)
	if !ok {
		t.Fatal("asSkyRequest should bridge typed record")
	}
	if out.Method != "GET" {
		t.Errorf("Method: got %q want GET", out.Method)
	}
	if out.Path != "/users/42" {
		t.Errorf("Path: got %q want /users/42", out.Path)
	}
	if out.RemoteAddr != "127.0.0.1:5050" {
		t.Errorf("RemoteAddr: got %q", out.RemoteAddr)
	}
	// Headers must widen string→any.
	if v, _ := out.Headers["User-Agent"]; v != "curl/8" {
		t.Errorf("Headers bridge dropped value: got %v", out.Headers)
	}
	if v, _ := out.Params["id"]; v != "42" {
		t.Errorf("Params bridge: got %v", out.Params)
	}
	if v, _ := out.Query["q"]; v != "search" {
		t.Errorf("Query bridge: got %v", out.Query)
	}
	if out.Cookies["sid"] != "abc" {
		t.Errorf("Cookies bridge: got %v", out.Cookies)
	}
}

func TestAsSkyRequest_NilPath(t *testing.T) {
	if _, ok := asSkyRequest(nil); ok {
		t.Error("asSkyRequest(nil) should ok=false")
	}
	if _, ok := asSkyRequest("not a struct"); ok {
		t.Error("asSkyRequest of non-struct should ok=false")
	}
}
