package rt

// v0.16.1 PR 2 — console mount-precedence + boot-time invariant
// regression specs. Covers:
//
//   - Inline mount sets inlineConsoleHealthy and the subsequent
//     MountConsoleEndpoints SKIPS its /_sky/console HTML-shell
//     registration (no safeMount duplicate-pattern race, no
//     legacy-healthy flip).
//   - When inline is unavailable, MountConsoleEndpoints DOES register
//     the legacy HTML shell and flips legacyConsoleHealthy.
//   - shouldHaveConsole reflects the env-var triple gate.
//   - AssertConsoleInvariantOrExit is a no-op when SKY_CONSOLE_AUTH is
//     off / unset and when at least one healthy flag is set.
//   - The os.Exit(1) path is verified via a subprocess (os/exec
//     re-invoking the test binary), driven by an env flag the helper
//     test reads to call AssertConsoleInvariantOrExit directly.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

// ─── inlineConsoleHealthy flips when MountInlineConsole succeeds ──

// TestMountConsoleEndpoints_SkipsHTMLShellWhenInlineHealthy asserts
// the explicit precedence: setting inlineConsoleHealthy short-circuits
// MountConsoleEndpoints's safeMount call for `/_sky/console`, so the
// legacy HandleConsole no-CSS HTML shell is NOT installed.
func TestMountConsoleEndpoints_SkipsHTMLShellWhenInlineHealthy(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	ResetConsoleHealthFlagsForTesting()
	t.Cleanup(ResetConsoleHealthFlagsForTesting)

	// Pretend the inline mount succeeded earlier.
	inlineConsoleHealthy.Store(true)

	mux := http.NewServeMux()
	MountConsoleEndpoints(mux)

	if LegacyConsoleHealthy() {
		t.Fatalf("legacyConsoleHealthy must remain false when inline owns /_sky/console")
	}
	// The HTML shell handler should NOT have been registered, so a
	// GET to /_sky/console returns 404 (the JSON API endpoints under
	// /_sky/console/api/* still resolve — they're more-specific
	// patterns).
	req := httptest.NewRequest(http.MethodGet, "/_sky/console", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("/_sky/console: expected 404 (inline owns it, legacy skipped), got %d", w.Code)
	}
	// The JSON API endpoints DO mount unconditionally.
	for _, path := range []string{
		"/_sky/console/api/overview",
		"/_sky/console/api/logs",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Errorf("%s: expected mounted regardless of inline health, got 404", path)
		}
	}
}

// TestMountConsoleEndpoints_RegistersHTMLShellWhenInlineMissing
// covers the converse: when the inline path didn't run (or failed
// before flipping the flag), the legacy path takes over and sets
// legacyConsoleHealthy.
func TestMountConsoleEndpoints_RegistersHTMLShellWhenInlineMissing(t *testing.T) {
	resetReadiness(t)
	withServerlessEnv(t, nil)
	ResetConsoleHealthFlagsForTesting()
	t.Cleanup(ResetConsoleHealthFlagsForTesting)

	mux := http.NewServeMux()
	MountConsoleEndpoints(mux)

	if !LegacyConsoleHealthy() {
		t.Fatalf("legacyConsoleHealthy must flip when inline is unavailable")
	}
	// HTML shell now serves /_sky/console.
	req := httptest.NewRequest(http.MethodGet, "/_sky/console", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("/_sky/console: expected 200 from legacy HandleConsole, got %d", w.Code)
	}
}

// ─── shouldHaveConsole reflects the env-var triple gate ─────────

func TestShouldHaveConsole_OffWhenAuthUnset(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	if shouldHaveConsole() {
		t.Errorf("SKY_CONSOLE_AUTH unset → should NOT require console")
	}
}

func TestShouldHaveConsole_OffWhenAuthExplicitlyOff(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "off")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	if shouldHaveConsole() {
		t.Errorf("SKY_CONSOLE_AUTH=off → should NOT require console")
	}
}

func TestShouldHaveConsole_OffWhenSubApp(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_LIVE_BASE_PATH", "/billing")
	if shouldHaveConsole() {
		t.Errorf("sub-app context → should NOT require console (parent owns it)")
	}
}

func TestShouldHaveConsole_OffWhenEmbedDisabled(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_EMBED", "off")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	if shouldHaveConsole() {
		t.Errorf("SKY_CONSOLE_EMBED=off → should NOT require console")
	}
}

func TestShouldHaveConsole_OnWhenTokenMode(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	if !shouldHaveConsole() {
		t.Errorf("SKY_CONSOLE_AUTH=token (no other gates) → SHOULD require console")
	}
}

func TestShouldHaveConsole_OnWhenAppMode(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "app")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	if !shouldHaveConsole() {
		t.Errorf("SKY_CONSOLE_AUTH=app (no other gates) → SHOULD require console")
	}
}

// ─── AssertConsoleInvariantOrExit no-op cases ───────────────────

// TestAssertConsoleInvariant_NoopWhenAuthUnset asserts the
// invariant never trips when the user didn't ask for a console.
func TestAssertConsoleInvariant_NoopWhenAuthUnset(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "")
	ResetConsoleHealthFlagsForTesting()
	t.Cleanup(ResetConsoleHealthFlagsForTesting)
	// If this returns, the test passes. If it os.Exit(1)s, the test
	// process dies and the runner reports failure.
	AssertConsoleInvariantOrExit()
}

func TestAssertConsoleInvariant_NoopWhenAuthOff(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "off")
	ResetConsoleHealthFlagsForTesting()
	t.Cleanup(ResetConsoleHealthFlagsForTesting)
	AssertConsoleInvariantOrExit()
}

// TestAssertConsoleInvariant_NoopWhenInlineHealthy: SKY_CONSOLE_AUTH
// is set AND inline mounted successfully → invariant clears.
func TestAssertConsoleInvariant_NoopWhenInlineHealthy(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	ResetConsoleHealthFlagsForTesting()
	t.Cleanup(ResetConsoleHealthFlagsForTesting)
	inlineConsoleHealthy.Store(true)
	AssertConsoleInvariantOrExit()
}

// TestAssertConsoleInvariant_NoopWhenLegacyHealthy: the legacy mount
// also satisfies the invariant (the JSON HTML shell IS a console
// surface, just not the inline Std.Ui one).
func TestAssertConsoleInvariant_NoopWhenLegacyHealthy(t *testing.T) {
	t.Setenv("SKY_CONSOLE_AUTH", "token")
	t.Setenv("SKY_LIVE_BASE_PATH", "")
	t.Setenv("SKY_CONSOLE_EMBED", "")
	ResetConsoleHealthFlagsForTesting()
	t.Cleanup(ResetConsoleHealthFlagsForTesting)
	legacyConsoleHealthy.Store(true)
	AssertConsoleInvariantOrExit()
}

// ─── AssertConsoleInvariantOrExit fatal path (os.Exit subprocess) ─

// invariantHelperRan is set when the in-process "helper test"
// detects the SKY_CONSOLE_BOOT_HELPER env flag and calls
// AssertConsoleInvariantOrExit directly. If we observe this counter
// rise outside the subprocess case, our subprocess driver isn't
// actually re-invoking the binary, so the assertion below would be
// trivially green — guard against that false-positive.
var invariantHelperRan atomic.Int32

// TestHelper_RunInvariant is the subprocess entry. Skipped in normal
// runs; when the parent test sets SKY_CONSOLE_BOOT_HELPER=1, it
// resets the flags, sets the auth env, and calls the assertion. We
// expect os.Exit(1); the parent observes the non-zero exit code AND
// the stderr message.
func TestHelper_RunInvariant(t *testing.T) {
	if os.Getenv("SKY_CONSOLE_BOOT_HELPER") != "1" {
		t.Skip("subprocess helper; parent test invokes this via os/exec")
	}
	invariantHelperRan.Add(1)
	// Caller has already set SKY_CONSOLE_AUTH=token via env. The
	// flags start at false (this is a fresh process). Call the
	// assertion — should print stderr + os.Exit(1).
	ResetConsoleHealthFlagsForTesting()
	AssertConsoleInvariantOrExit()
	// If we reach here, the assertion did NOT fire. Print a marker
	// the parent can spot so the failure is diagnosable.
	t.Fatal("AssertConsoleInvariantOrExit returned without exiting (invariant did not fire)")
}

// TestAssertConsoleInvariant_FatalWhenNeitherHealthyAndAuthSet
// drives the os.Exit(1) path via a subprocess. The parent re-invokes
// the test binary with `-run TestHelper_RunInvariant` and inspects
// the exit code + stderr.
func TestAssertConsoleInvariant_FatalWhenNeitherHealthyAndAuthSet(t *testing.T) {
	if os.Getenv("SKY_CONSOLE_BOOT_HELPER") == "1" {
		t.Skip("inside subprocess — handled by TestHelper_RunInvariant")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run", "^TestHelper_RunInvariant$", "-test.v")
	cmd.Env = append(os.Environ(),
		"SKY_CONSOLE_BOOT_HELPER=1",
		"SKY_CONSOLE_AUTH=token",
		"SKY_CONSOLE_EMBED=",
		"SKY_LIVE_BASE_PATH=",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("subprocess exited 0, expected non-zero. output:\n%s", string(out))
	}
	body := string(out)
	if !strings.Contains(body, "FATAL: SKY_CONSOLE_AUTH=token") {
		t.Errorf("expected FATAL stderr line referencing SKY_CONSOLE_AUTH=token; got:\n%s", body)
	}
	if !strings.Contains(body, "/_sky/console") {
		t.Errorf("expected FATAL line to reference /_sky/console; got:\n%s", body)
	}
	if !strings.Contains(body, "console_app blank import") {
		t.Errorf("expected FATAL line to hint at the blank-import fix; got:\n%s", body)
	}
}
