package rt

// Serverless / edge-runtime mode detection. Sky's observability and
// dispatch defaults flip when running on request-billed serverless
// (Cloud Run, AWS Lambda, Vercel, Azure Functions, Netlify) so the
// runtime doesn't burn CPU on background goroutines that can't
// survive container eviction.
//
// Full design + cost analysis: docs/v1-rfc/1-observability.md
// §"Serverless / edge runtime mode".
//
// Decision boundary:
//
//   - VM / long-lived host: background goroutines, in-memory ring
//     buffers, scheduled flushes, Prometheus pull, SSE — all viable.
//   - Serverless: container evicts seconds after the last request;
//     background work is wasted; use synchronous push at request end.

import (
	"os"
	"sync"
	"sync/atomic"
)

// IsServerless returns true when the runtime detects a request-billed
// serverless or function-as-a-service environment. Cached after first
// call (the underlying env vars don't change at runtime; re-reading
// them per-call is wasteful).
//
// Explicit override:
//
//	SKY_RUNTIME_MODE=serverless   → force true
//	SKY_RUNTIME_MODE=vm           → force false
//	SKY_RUNTIME_MODE=longlived    → force false (alias)
//
// Otherwise platform fingerprints (env vars each platform sets
// automatically):
//
//	K_SERVICE / K_REVISION       → Google Cloud Run
//	FUNCTION_TARGET              → Google Cloud Functions
//	AWS_LAMBDA_FUNCTION_NAME     → AWS Lambda
//	FUNCTIONS_WORKER_RUNTIME     → Azure Functions
//	VERCEL                       → Vercel
//	NETLIFY                      → Netlify Functions
//	FLY_APP_NAME                 → fly.io (NOT serverless per se but
//	                               billed similarly when scale-to-zero
//	                               is enabled; we err on the safe
//	                               side and treat as serverless)
//
// Test helper `ResetServerlessCache()` clears the memo between
// test cases that mutate the env.
func IsServerless() bool {
	return serverlessDetect()
}

var (
	serverlessOnce  sync.Once
	serverlessCache atomic.Bool
)

func serverlessDetect() bool {
	serverlessOnce.Do(func() {
		serverlessCache.Store(detectServerlessFromEnv())
	})
	return serverlessCache.Load()
}

func detectServerlessFromEnv() bool {
	// Explicit override always wins.
	switch os.Getenv("SKY_RUNTIME_MODE") {
	case "serverless":
		return true
	case "vm", "longlived":
		return false
	}
	// Platform fingerprint match — any of these env vars present
	// → serverless.
	for _, env := range serverlessEnvFingerprints {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return false
}

// serverlessEnvFingerprints — read by IsServerless. Exported as a
// var (not const list) so tests / users can append custom platforms
// without recompiling. Default list covers the five major
// request-billed runtimes + fly.io scale-to-zero.
var serverlessEnvFingerprints = []string{
	"K_SERVICE",                // Cloud Run (Knative-style)
	"K_REVISION",               // Cloud Run revision
	"FUNCTION_TARGET",          // Cloud Functions
	"AWS_LAMBDA_FUNCTION_NAME", // Lambda
	"FUNCTIONS_WORKER_RUNTIME", // Azure Functions
	"VERCEL",                   // Vercel
	"NETLIFY",                  // Netlify Functions
	"FLY_APP_NAME",             // fly.io (scale-to-zero risk)
}

// ResetServerlessCache forces re-detection on the next IsServerless
// call. TEST-ONLY — production code should never call this (the
// env vars don't change at runtime).
func ResetServerlessCache() {
	serverlessOnce = sync.Once{}
	serverlessCache.Store(false)
}

// ServerlessShutdownGrace returns the SIGTERM drain budget appropriate
// for the current mode. Serverless platforms kill containers quickly
// (Lambda: <1s, Cloud Run: 10s default but can be smaller); blocking
// for the VM-mode 30s steals billing window.
func ServerlessShutdownGrace() (graceMs int) {
	if IsServerless() {
		return 1000 // 1 s — leaves headroom for in-flight requests
	}
	return 30000 // 30 s — standard VM grace
}

// ServerlessTraceSampleRate returns the default OTel head-based
// sampling rate for the current mode. Serverless workloads are
// bounded by per-request billing → sampling is less critical, so
// default to 100% (caller can override via OTEL_TRACES_SAMPLER_ARG).
// VM mode defaults to 1% to keep collector load proportional to
// always-on traffic.
func ServerlessTraceSampleRate() float64 {
	if IsServerless() {
		return 1.0
	}
	return 0.01
}

// ServerlessExporterMode returns "sync" on serverless (flush at end
// of request, before container can be evicted) or "batched" on VMs
// (background goroutine batches and flushes every 30s). Caller
// (Step 7 OTel exporter) uses this to pick the exporter
// implementation at startup.
func ServerlessExporterMode() string {
	if IsServerless() {
		return "sync"
	}
	return "batched"
}
