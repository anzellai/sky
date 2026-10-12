//go:build !js

package rt

// ai_gcp.go — the runtime half of Std.Ai.Cloud.Google: the emulator seam.
//
// Std.Ai.Cloud.Google is Sky over Sky.Core.Http, so this file holds no
// Google client. It answers one question, where the calls go:
//
//	Ai_gcpEndpoint : () -> Task Error String
//
// `<PREFIX>_AI_GCP_ENDPOINT` sends every Google call (OAuth, metadata,
// Vertex AI, Cloud Run, GCS, Artifact Registry, Cloud Quotas, Logging) to one
// server, under fixed path prefixes. Under test mode the host
// `sky-gcp.invalid` is answered by the built-in emulator (ai_gcpfake.go), so
// `SKY_AI_GCP_ENDPOINT=http://sky-gcp.invalid` in a project's `.env.test`
// runs a whole Vertex training run offline. Outside test mode the variable
// names a real server (the `rt/testdata/fakegcp` binary in the cargo flows).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// aiGcpFakeHost is the host the test-mode transport answers with the
// built-in emulator.
const aiGcpFakeHost = "sky-gcp.invalid"

// aiGcpEndpoint is the emulator's base URL, or "" for Google itself.
func aiGcpEndpoint() string {
	for _, v := range configLayers("AI_GCP_ENDPOINT", "") {
		if v = strings.TrimSpace(v); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return ""
}

// Ai_gcpEndpoint : () -> Task Error String
func Ai_gcpEndpoint(_ any) any {
	return func() any { return Ok[any, any](aiGcpEndpoint()) }
}

var (
	aiGcpFakeOnce   sync.Once
	aiGcpFakeShared *aiFakeGCP
)

// aiGcpFakeInProcess is the test-mode emulator, one per process.
func aiGcpFakeInProcess() *aiFakeGCP {
	aiGcpFakeOnce.Do(func() { aiGcpFakeShared = newAiFakeGCP() })
	return aiGcpFakeShared
}

// aiGcpFakeRoundTrip answers a test-mode request to sky-gcp.invalid with the
// in-process emulator. body is the request body the transport already read.
func aiGcpFakeRoundTrip(req *http.Request, body string) *http.Response {
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(strings.NewReader(body))
	r.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	aiGcpFakeInProcess().ServeHTTP(rec, r)
	res := rec.Result()
	data, _ := io.ReadAll(res.Body)
	res.Body = io.NopCloser(bytes.NewReader(data))
	res.Request = req
	return res
}
