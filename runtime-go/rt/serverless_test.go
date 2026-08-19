package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// IsServerless() detection. Behaviour must be deterministic per
// env-var set; we ResetServerlessCache between tests to defeat the
// sync.Once memoisation.

func TestIsServerless_NoEnv_FalseOnBareMachine(t *testing.T) {
	withServerlessEnv(t, nil)
	if IsServerless() {
		t.Errorf("bare machine (no fingerprint env vars) should not be serverless")
	}
}

func TestIsServerless_ExplicitOverride_Serverless(t *testing.T) {
	withServerlessEnv(t, map[string]string{
		"SKY_RUNTIME_MODE": "serverless",
	})
	if !IsServerless() {
		t.Errorf("SKY_RUNTIME_MODE=serverless should force true")
	}
}

func TestIsServerless_ExplicitOverride_VM(t *testing.T) {
	// VM override beats platform fingerprint — useful for users
	// running long-lived workloads on Cloud Run's 2nd-gen (instance-
	// based billing) where K_SERVICE still gets set but the VM
	// model applies.
	withServerlessEnv(t, map[string]string{
		"SKY_RUNTIME_MODE": "vm",
		"K_SERVICE":        "my-svc",
	})
	if IsServerless() {
		t.Errorf("SKY_RUNTIME_MODE=vm should override K_SERVICE fingerprint")
	}
}

func TestIsServerless_ExplicitOverride_Longlived(t *testing.T) {
	withServerlessEnv(t, map[string]string{
		"SKY_RUNTIME_MODE":         "longlived",
		"AWS_LAMBDA_FUNCTION_NAME": "fn",
	})
	if IsServerless() {
		t.Errorf("SKY_RUNTIME_MODE=longlived should override Lambda fingerprint")
	}
}

func TestIsServerless_CloudRun_K_SERVICE(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_SERVICE": "my-svc"})
	if !IsServerless() {
		t.Errorf("K_SERVICE should be detected as Cloud Run / serverless")
	}
}

func TestIsServerless_CloudRun_K_REVISION(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_REVISION": "rev-1"})
	if !IsServerless() {
		t.Errorf("K_REVISION should be detected as Cloud Run / serverless")
	}
}

func TestIsServerless_Lambda(t *testing.T) {
	withServerlessEnv(t, map[string]string{"AWS_LAMBDA_FUNCTION_NAME": "fn"})
	if !IsServerless() {
		t.Errorf("AWS_LAMBDA_FUNCTION_NAME should be detected as Lambda")
	}
}

func TestIsServerless_Azure(t *testing.T) {
	withServerlessEnv(t, map[string]string{"FUNCTIONS_WORKER_RUNTIME": "go"})
	if !IsServerless() {
		t.Errorf("FUNCTIONS_WORKER_RUNTIME should be detected as Azure Functions")
	}
}

func TestIsServerless_Vercel(t *testing.T) {
	withServerlessEnv(t, map[string]string{"VERCEL": "1"})
	if !IsServerless() {
		t.Errorf("VERCEL should be detected")
	}
}

func TestIsServerless_Netlify(t *testing.T) {
	withServerlessEnv(t, map[string]string{"NETLIFY": "true"})
	if !IsServerless() {
		t.Errorf("NETLIFY should be detected")
	}
}

func TestIsServerless_FlyScaleToZero(t *testing.T) {
	// fly.io is technically VM but with scale-to-zero the billing
	// shape matches serverless. Err on safe side.
	withServerlessEnv(t, map[string]string{"FLY_APP_NAME": "my-app"})
	if !IsServerless() {
		t.Errorf("FLY_APP_NAME should be detected (scale-to-zero risk)")
	}
}

// ─── Mode-aware defaults ──────────────────────────────────────

func TestServerlessShutdownGrace(t *testing.T) {
	withServerlessEnv(t, nil)
	if g := ServerlessShutdownGrace(); g != 30000 {
		t.Errorf("VM mode grace should be 30s, got %dms", g)
	}

	withServerlessEnv(t, map[string]string{"K_SERVICE": "x"})
	if g := ServerlessShutdownGrace(); g != 1000 {
		t.Errorf("serverless grace should be 1s (Cloud Run kills fast), got %dms", g)
	}
}

func TestServerlessTraceSampleRate(t *testing.T) {
	withServerlessEnv(t, nil)
	if r := ServerlessTraceSampleRate(); r != 0.01 {
		t.Errorf("VM trace sample default should be 1%%, got %v", r)
	}

	withServerlessEnv(t, map[string]string{"VERCEL": "1"})
	if r := ServerlessTraceSampleRate(); r != 1.0 {
		t.Errorf("serverless trace sample default should be 100%%, got %v", r)
	}
}

func TestServerlessExporterMode(t *testing.T) {
	withServerlessEnv(t, nil)
	if m := ServerlessExporterMode(); m != "batched" {
		t.Errorf("VM exporter default should be batched, got %q", m)
	}

	withServerlessEnv(t, map[string]string{"AWS_LAMBDA_FUNCTION_NAME": "x"})
	if m := ServerlessExporterMode(); m != "sync" {
		t.Errorf("serverless exporter must be sync (container evicts before background flush), got %q", m)
	}
}

// ─── /_sky/metrics behaviour in serverless mode ───────────────

func TestHandleMetrics_ServerlessReturns503(t *testing.T) {
	withServerlessEnv(t, map[string]string{"K_SERVICE": "x"})
	resetReadiness(t)
	// Even with valid prod token, serverless should refuse —
	// the pull model is structurally wrong.
	t.Setenv("SKY_METRICS_TOKEN", "tok")
	SetProductionMode(true)

	req := httptest.NewRequest(http.MethodGet, "/_sky/metrics", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp := httptest.NewRecorder()
	HandleMetrics(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("serverless /_sky/metrics should be 503, got %d body=%s",
			resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "OTLP") {
		t.Errorf("503 body should hint at OTLP push, got: %s", resp.Body.String())
	}
}

func TestHandleMetrics_VMOpenInDev(t *testing.T) {
	// Sanity: ensure the serverless gate doesn't bleed into VM mode.
	withServerlessEnv(t, nil)
	resetReadiness(t)
	SetProductionMode(false)

	resp := serveOnce(HandleMetrics, http.MethodGet, "/_sky/metrics")
	if resp.Code != http.StatusOK {
		t.Errorf("VM + dev mode metrics should be 200, got %d", resp.Code)
	}
}

// ─── helper ───────────────────────────────────────────────────

// withServerlessEnv sets the given env vars for the duration of the
// test, clears the IsServerless cache, and registers cleanup that
// re-clears the cache for following tests. All fingerprint env vars
// are reset to empty FIRST so a previous test setting K_SERVICE
// doesn't bleed into the current one.
func withServerlessEnv(t *testing.T, set map[string]string) {
	t.Helper()
	// Clear every fingerprint AND the override.
	for _, env := range append(serverlessEnvFingerprints, "SKY_RUNTIME_MODE") {
		t.Setenv(env, "")
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
	ResetServerlessCache()
	t.Cleanup(func() {
		ResetServerlessCache()
	})
}
