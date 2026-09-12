//go:build !js

package rt

// Mock-by-default outbound HTTP for TEST MODE (auto-testing phase 3c).
//
// When SKY_TEST_MODE is on, the shared outbound client's transport intercepts
// EVERY outbound request — no test ever touches the real network. Behaviour:
//
//   - a request matching a declarative fixture (a JSON file under the mocks dir,
//     default `tests/mocks/`, or `SKY_TEST_MOCKS_DIR`) returns that fixture's
//     canned status + body;
//   - an UNMATCHED request FAILS CLOSED with a transport error — which surfaces
//     to the app as an `Http` error, so the app's failure path runs automatically
//     (the "fuzz the failure modes" property, for free, with zero per-test code).
//
// So a project writes NO mock plumbing: the default is deterministic failure-mode
// coverage; a specific happy-path response is DATA (a fixture file), never code.
// A fixture is matched by an optional method + a URL substring, so one fixture
// covers a whole host or path prefix.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// httpMockMatch selects which requests a fixture answers.
type httpMockMatch struct {
	Method      string `json:"method"`
	URLContains string `json:"urlContains"`
}

// httpMockFixture is one declarative mock, loaded from a JSON file.
type httpMockFixture struct {
	Match  httpMockMatch `json:"match"`
	Status int           `json:"status"`
	Body   string        `json:"body"`
}

var (
	httpMocksOnce sync.Once
	httpMocks     []httpMockFixture
)

// mocksDir is the directory scanned for fixtures, cwd-relative by default (the
// `sky test` runner runs from the project root).
func mocksDir() string {
	if d := os.Getenv("SKY_TEST_MOCKS_DIR"); d != "" {
		return d
	}
	return "tests/mocks"
}

func loadHttpMocks() {
	httpMocksOnce.Do(func() {
		entries, err := os.ReadDir(mocksDir())
		if err != nil {
			return // no mocks dir → every outbound request fails closed
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(mocksDir(), e.Name()))
			if err != nil {
				continue
			}
			var f httpMockFixture
			if json.Unmarshal(data, &f) == nil {
				httpMocks = append(httpMocks, f)
			}
		}
	})
}

// testHttpTransport wraps the real transport, intercepting outbound requests in
// test mode. Outside test mode it is a transparent passthrough.
type testHttpTransport struct {
	base http.RoundTripper
}

func (t *testHttpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if !testModeActive() {
		return base.RoundTrip(req)
	}
	loadHttpMocks()
	url := req.URL.String()
	for _, f := range httpMocks {
		if f.Match.Method != "" && !strings.EqualFold(f.Match.Method, req.Method) {
			continue
		}
		if f.Match.URLContains == "" || strings.Contains(url, f.Match.URLContains) {
			status := f.Status
			if status == 0 {
				status = 200
			}
			return &http.Response{
				StatusCode: status,
				Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				Body:       io.NopCloser(strings.NewReader(f.Body)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}
	}
	// Fail closed — the default in test mode is deterministic error-mode coverage.
	return nil, fmt.Errorf(
		"sky test mode: outbound HTTP %s %s is not mocked — add a fixture under %s/ (a JSON {match:{method,urlContains},status,body}), or set SKY_TEST_MOCKS_DIR; no real network in tests",
		req.Method, url, mocksDir(),
	)
}
