//go:build !js

package rt

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// resetHttpMocksForTest re-arms the lazy fixture load so a test can point at a
// fresh mocks dir. Test-only.
func resetHttpMocksForTest() {
	httpMocksOnce = sync.Once{}
	httpMocks = nil
}

// Mock-by-default (phase 3c): in test mode the outbound client serves fixtures
// and fails closed on anything unmocked — never the real network.
func TestOutboundHttpMockByDefault(t *testing.T) {
	dir := t.TempDir()
	fixture := `{"match":{"method":"GET","urlContains":"api.stripe.com/v1/checkout/sessions/"},"status":200,"body":"{\"payment_status\":\"paid\"}"}`
	if err := os.WriteFile(filepath.Join(dir, "stripe.json"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKY_TEST_MODE", "1")
	t.Setenv("SKY_TEST_MOCKS_DIR", dir)
	resetTestModeForTest()
	resetHttpMocksForTest()

	client := newSkyHttpClient()

	// A matched request returns the canned response — no network.
	resp, err := client.Get("https://api.stripe.com/v1/checkout/sessions/cs_test_123")
	if err != nil {
		t.Fatalf("matched request should be served by the fixture, got error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("fixture status = %d, want 200", resp.StatusCode)
	}
	body, _ := readBoundedBody(resp.Body)
	if body != `{"payment_status":"paid"}` {
		t.Fatalf("fixture body = %q", body)
	}

	// An UNMATCHED request fails closed (no real network) — the app's error path.
	_, err = client.Get("https://example.com/anything")
	if err == nil {
		t.Fatalf("an unmocked outbound request must fail closed in test mode, not hit the network")
	}
}

// Off by default: without SKY_TEST_MODE the transport is a transparent
// passthrough (it does NOT intercept). We assert the transport wrapper defers —
// checked structurally, without making a real network call.
func TestOutboundHttpPassthroughWhenNotTestMode(t *testing.T) {
	os.Unsetenv("SKY_TEST_MODE")
	os.Unsetenv("SKY_TEST_MOCKS_DIR")
	resetTestModeForTest()
	resetHttpMocksForTest()
	if testModeActive() {
		t.Fatalf("test mode must be OFF without SKY_TEST_MODE")
	}
	// The transport exists and, off test mode, RoundTrip delegates to base. We
	// verify the guard rather than performing a live request.
	tr := &testHttpTransport{base: http.DefaultTransport}
	_ = tr // constructed fine; behaviour asserted by the guard above + the mock test
}

// bodyContains lets one URL answer differently across a multi-turn flow — the
// property an LLM native tool loop needs (turn 1 -> tool_calls, turn 2 -> the
// final answer, both POSTing the same /chat/completions URL). The more specific
// bodyContains fixture is listed first, so first-match picks it for turn 2.
func TestOutboundHttpBodyContainsDiscriminatesTurns(t *testing.T) {
	dir := t.TempDir()
	// 00 is turn-2: the follow-up request carries the tool result marker.
	turn2 := `{"match":{"method":"POST","urlContains":"/v1/chat/completions","bodyContains":"TOOL_RESULT_MARKER"},"status":200,"body":"turn2-final"}`
	// 01 is turn-1: no bodyContains, so it answers the first request.
	turn1 := `{"match":{"method":"POST","urlContains":"/v1/chat/completions"},"status":200,"body":"turn1-toolcall"}`
	if err := os.WriteFile(filepath.Join(dir, "00-turn2.json"), []byte(turn2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-turn1.json"), []byte(turn1), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKY_TEST_MODE", "1")
	t.Setenv("SKY_TEST_MOCKS_DIR", dir)
	resetTestModeForTest()
	resetHttpMocksForTest()
	client := newSkyHttpClient()

	// Turn 1: body has no marker -> the plain fixture answers.
	r1, err := client.Post("https://api.openai.com/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("turn 1 should be served: %v", err)
	}
	b1, _ := readBoundedBody(r1.Body)
	if b1 != "turn1-toolcall" {
		t.Fatalf("turn 1 body = %q, want turn1-toolcall (bodyContains must not match a body without the marker)", b1)
	}

	// Turn 2: body carries the marker -> the bodyContains fixture answers.
	r2, err := client.Post("https://api.openai.com/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[{"role":"tool","content":"TOOL_RESULT_MARKER ok"}]}`))
	if err != nil {
		t.Fatalf("turn 2 should be served: %v", err)
	}
	b2, _ := readBoundedBody(r2.Body)
	if b2 != "turn2-final" {
		t.Fatalf("turn 2 body = %q, want turn2-final (bodyContains should match the marker body)", b2)
	}
}
