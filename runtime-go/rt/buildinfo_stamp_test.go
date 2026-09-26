//go:build !js

package rt

import (
	"encoding/json"
	"net/http"
	"testing"
)

// withStamp installs a generated-source stamp and restores the previous state.
func withStamp(t *testing.T, version, commit, builtAt, source string) {
	t.Helper()
	prev := embeddedStamp
	prevC, prevA, prevV := buildCommit, buildAt, skyVersion
	t.Cleanup(func() {
		embeddedStamp = prev
		buildCommit, buildAt, skyVersion = prevC, prevA, prevV
	})
	SetBuildStamp(version, commit, builtAt, source)
}

func readBuildInfo(t *testing.T) BuildInfo {
	t.Helper()
	resp := serveOnce(HandleBuildInfo, http.MethodGet, "/_sky/buildinfo")
	var bi BuildInfo
	if err := json.Unmarshal(resp.Body.Bytes(), &bi); err != nil {
		t.Fatalf("buildinfo body not valid JSON: %v\n%s", err, resp.Body.String())
	}
	return bi
}

// The stamp lives in the generated Go source (the skybuildinfo package calls
// SetBuildStamp from an init), so ANY `go build` of sky-out reports it — not
// only the `go build` that `sky build` runs itself. v0.25.20 stamped only via
// -X linker flags, and a plain cross-compile of sky-out reported dev/unknown.
func TestBuildInfo_ReportsTheGeneratedSourceStamp(t *testing.T) {
	withStamp(t, "v0.25.21", "0123456789ab", "2026-09-21T14:13:20Z", "ci:GITHUB_SHA")
	bi := readBuildInfo(t)
	if bi.Commit != "0123456789ab" || bi.BuiltAt != "2026-09-21T14:13:20Z" ||
		bi.SkyVersion != "v0.25.21" || bi.Source != "ci:GITHUB_SHA" {
		t.Fatalf("buildinfo must carry the generated stamp, got %+v", bi)
	}
	// The console overview reads the same snapshot.
	if got := ConsoleCurrentBuildInfo(); got.Commit != "0123456789ab" {
		t.Fatalf("console build info must match /_sky/buildinfo, got %+v", got)
	}
}

// A user's own `-ldflags "-X sky-app/rt.buildCommit=..."` still wins over the
// generated stamp, field by field, and the source says so.
func TestBuildInfo_LdflagsOverrideWinsOverTheStamp(t *testing.T) {
	withStamp(t, "v0.25.21", "0123456789ab", "2026-09-21T14:13:20Z", "git")
	buildCommit = "cafef00d"
	bi := readBuildInfo(t)
	if bi.Commit != "cafef00d" || bi.Source != "ldflags" {
		t.Fatalf("an -X buildCommit must win and report source=ldflags, got %+v", bi)
	}
	if bi.BuiltAt != "2026-09-21T14:13:20Z" || bi.SkyVersion != "v0.25.21" {
		t.Fatalf("fields the -X did not set keep the stamp, got %+v", bi)
	}
}

// A runtime compiled with no generated stamp (the runtime's own tests, a
// hand-assembled tree) keeps the dev defaults and says it is unstamped.
func TestBuildInfo_UnstampedDefaults(t *testing.T) {
	prev := embeddedStamp
	t.Cleanup(func() { embeddedStamp = prev })
	embeddedStamp = buildStamp{}
	bi := readBuildInfo(t)
	if bi.Commit != "dev" || bi.BuiltAt != "unknown" || bi.SkyVersion != "dev" || bi.Source != "unstamped" {
		t.Fatalf("unstamped build must report the dev defaults, got %+v", bi)
	}
}
