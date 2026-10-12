//go:build !js

package rt

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"sky-app/rt/telemetry"
)

// repoFile reads a file relative to the repository root (runtime-go/rt is two
// levels below it).
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// unionCtors returns the constructor names of `type <name>` in a Sky source,
// in declaration order.
func unionCtors(t *testing.T, src, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)\ntype ` + name + `\s*\n\s*=(.*?)\n\n`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("type %s not found", name)
	}
	var out []string
	for _, arm := range strings.Split(m[1], "\n") {
		arm = strings.TrimSpace(arm)
		arm = strings.TrimPrefix(arm, "| ")
		arm = strings.TrimPrefix(arm, "= ")
		if arm == "" || strings.HasPrefix(arm, "--") {
			continue
		}
		out = append(out, strings.Fields(arm)[0])
	}
	return out
}

// The tags error_domain.go builds must follow Error.sky's constructor order:
// a moved constructor would make the runtime build a value typed code reads
// as another variant.
func TestErrorDomainTagsFollowErrorSky(t *testing.T) {
	src := repoFile(t, "sky-stdlib/Sky/Core/Error.sky")
	details := unionCtors(t, src, "ErrorDetails")
	if len(details) <= errorDetailsDomainTag || details[errorDetailsDomainTag] != errorDetailsDomainCtr {
		t.Fatalf("ErrorDetails constructors %v: want %s at tag %d", details, errorDetailsDomainCtr, errorDetailsDomainTag)
	}
	kinds := unionCtors(t, src, "ErrorKind")
	if kinds[errKindTimeout] != "Timeout" || kinds[errKindUnavailable] != "Unavailable" || kinds[errKindUnexpected] != "Unexpected" ||
		kinds[errKindInvalidInput] != "InvalidInput" || kinds[errKindPermissionDenied] != "PermissionDenied" {
		t.Fatalf("ErrorKind order %v does not match the errKind constants", kinds)
	}
	if !regexp.MustCompile(`= DomainFailure String String Bool \(Maybe Int\) \(List \( String, String \)\)`).MatchString(src) {
		t.Fatal("DomainFailure's fields moved: error_domain.go builds (domain, code, retryable, retryAfterMs, attrs)")
	}
}

func TestErrDomainRoundTripsThroughDomainOf(t *testing.T) {
	f := DomainFailure{Domain: "db", Code: "Deadlock", Retryable: true, RetryAfterMs: 250,
		Attrs: [][2]string{{"table", "orders"}, {"attempt", "2"}}}
	v := ErrDomain(errKindConflict, "deadlock on orders", f)
	got, ok := DomainOf(v)
	if !ok {
		t.Fatal("DomainOf: no domain failure")
	}
	if got.Domain != "db" || got.Code != "Deadlock" || !got.Retryable || got.RetryAfterMs != 250 {
		t.Fatalf("round trip: %+v", got)
	}
	if a, _ := got.Attr("table"); a != "orders" || len(got.Attrs) != 2 || got.Attrs[1][0] != "attempt" {
		t.Fatalf("attributes: %v", got.Attrs)
	}
	// The human rendering is unchanged by the details.
	if s, ok := renderSkyError(v); !ok || s != "Conflict: deadlock on orders" {
		t.Fatalf("renderSkyError: %q", s)
	}
	// No delay named reads back as -1, and an Error without details has no
	// domain failure.
	got, _ = DomainOf(ErrDomain(errKindIo, "x", DomainFailure{Domain: "d", Code: "c", RetryAfterMs: -1}))
	if got.RetryAfterMs != -1 {
		t.Fatalf("no delay: %d", got.RetryAfterMs)
	}
	if _, ok := DomainOf(ErrUnavailable("plain")); ok {
		t.Fatal("a plain error has no domain failure")
	}
	// With keeps a key's first position and replaces its value.
	g := f.With("table", "lines").With("new", "1")
	if g.Attrs[0] != [2]string{"table", "lines"} || g.Attrs[2] != [2]string{"new", "1"} || len(f.Attrs) != 2 {
		t.Fatalf("With: %v (original %v)", g.Attrs, f.Attrs)
	}
}

func TestSkyShowRendersADomainFailure(t *testing.T) {
	v := ErrDomain(errKindUnavailable, "busy", DomainFailure{Domain: "ai", Code: "Overloaded", Retryable: true, RetryAfterMs: 1500,
		Attrs: [][2]string{{"status", "429"}}})
	got := SkyShow(v)
	for _, want := range []string{`Unavailable "busy"`, `Domain`, `"ai"`, `"Overloaded"`, `True`, `Just 1500`, `"status"`, `"429"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("SkyShow = %s; want it to contain %s", got, want)
		}
	}
}

func TestLogFieldsOfADomainError(t *testing.T) {
	v := aiErrorValue(aiFailf(aiOverloaded, "slot %q is full", "chat").with("status", "429"))
	logEmit(logLevelError, "error", "ai call failed (domain-log-test)", map[string]any{"error": v})
	var fields map[string]string
	for _, e := range telemetry.Default().RecentLogs(0) {
		if e.Message == "ai call failed (domain-log-test)" {
			fields = e.Fields
		}
	}
	if fields == nil {
		t.Fatal("the log line did not reach the telemetry ring")
	}
	want := map[string]string{
		"error":             `Unavailable: Std.Ai Overloaded: slot "chat" is full`,
		"error.domain":      "ai",
		"error.code":        "Overloaded",
		"error.retryable":   "true",
		"error.attr.status": "429",
	}
	for k, w := range want {
		if fields[k] != w {
			t.Fatalf("field %s = %q; want %q (all: %v)", k, fields[k], w, fields)
		}
	}
}

// ─── Std.Ai failures ─────────────────────────────────────────────────────

var aiAllCodes = []aiFailCode{
	aiNotConfigured, aiInvalidSetting, aiUnreachable, aiOverloaded, aiTimedOut,
	aiEngineCrashed, aiEngineFailed, aiModelTooLarge, aiNoAccelerator,
	aiNotProvisioned, aiChecksumMismatch, aiRemoteRejected, aiBudgetExceeded,
	aiToolchainFailed, aiGateFailed, aiDatasetInvalid,
}

func TestAiErrorValueIsATypedDomainFailureForEveryCode(t *testing.T) {
	if len(aiFailSpecs) != len(aiAllCodes) {
		t.Fatalf("aiFailSpecs has %d codes, the test lists %d", len(aiFailSpecs), len(aiAllCodes))
	}
	wantKind := map[aiFailCode]int{
		aiNotConfigured: errKindInvalidInput, aiInvalidSetting: errKindInvalidInput, aiModelTooLarge: errKindInvalidInput,
		aiUnreachable: errKindUnavailable, aiOverloaded: errKindUnavailable, aiEngineCrashed: errKindUnavailable,
		aiEngineFailed: errKindUnavailable, aiNoAccelerator: errKindUnavailable, aiToolchainFailed: errKindUnavailable,
		aiTimedOut: errKindTimeout, aiNotProvisioned: errKindNotFound, aiChecksumMismatch: errKindConflict,
		aiBudgetExceeded: errKindPermissionDenied, aiRemoteRejected: errKindUnexpected, aiGateFailed: errKindPermissionDenied,
		aiDatasetInvalid: errKindInvalidInput,
	}
	wantRetry := map[aiFailCode]bool{aiUnreachable: true, aiOverloaded: true, aiTimedOut: true}
	for _, code := range aiAllCodes {
		v := aiErrorValue(aiFailf(code, "detail %d", 7))
		adt := v.(skyErrorAdt)
		if adt.Fields[0] != errorKindAdt(wantKind[code], "") {
			t.Errorf("%s: kind %v, want %d", code, adt.Fields[0], wantKind[code])
		}
		if msg := adt.Fields[1].(skyErrorInfo).Message; msg != "Std.Ai "+string(code)+": detail 7" {
			t.Errorf("%s: message %q", code, msg)
		}
		df, ok := DomainOf(v)
		if !ok || df.Domain != "ai" || df.Code != string(code) {
			t.Fatalf("%s: domain failure %+v (ok %v)", code, df, ok)
		}
		if df.Retryable != wantRetry[code] {
			t.Errorf("%s: retryable %v", code, df.Retryable)
		}
		for _, kv := range aiFailSpecs[code].attrs {
			if _, ok := df.Attr(kv[0]); !ok {
				t.Errorf("%s: attribute %s missing (the typed view would not decode)", code, kv[0])
			}
		}
	}
	// A remote refusal is classified by its status; only 5xx is retryable.
	for status, want := range map[int][2]int{400: {errKindInvalidInput, 0}, 422: {errKindInvalidInput, 0},
		401: {errKindPermissionDenied, 0}, 403: {errKindPermissionDenied, 0}, 404: {errKindNotFound, 0},
		500: {errKindUnavailable, 1}, 502: {errKindUnavailable, 1}, 418: {errKindUnexpected, 0}} {
		f := aiFailf(aiRemoteRejected, "no")
		f.httpStatus = status
		v := aiErrorValue(f)
		df, _ := DomainOf(v)
		if v.(skyErrorAdt).Fields[0] != errorKindAdt(want[0], "") || df.Retryable != (want[1] == 1) {
			t.Errorf("HTTP %d: kind %v retryable %v", status, v.(skyErrorAdt).Fields[0], df.Retryable)
		}
		if s, _ := df.Attr("status"); s != strconv.Itoa(status) {
			t.Errorf("HTTP %d: status attribute %q", status, s)
		}
	}
	// An overloaded server's delay becomes the failure's retry delay.
	f := aiFailf(aiOverloaded, "busy")
	f.retryAfterMs, f.httpStatus = 1500, 429
	df, _ := DomainOf(aiErrorValue(f))
	if df.RetryAfterMs != 1500 {
		t.Fatalf("retry delay %d", df.RetryAfterMs)
	}
	if s, _ := df.Attr("status"); s != "429" {
		t.Fatalf("status %q", s)
	}
	// The endpoint's own retry policy is unchanged: only Overloaded and an
	// unreachable server are retried in place, never a timed-out call.
	for _, code := range aiAllCodes {
		if got, want := aiFailf(code, "x").retryable, code == aiOverloaded || code == aiUnreachable; got != want {
			t.Errorf("%s: in-place retry %v, want %v", code, got, want)
		}
	}
}

// Failure.sky and aiFailSpecs are one table in two languages: the same codes,
// every Go attribute a field of the Sky record, the same kinds and retryable
// flags.
func TestAiFailSpecsMatchFailureSky(t *testing.T) {
	src := repoFile(t, "sky-stdlib/Std/Ai/Failure.sky")
	// The constructors of `type Failure`, each `Ctor { field : T, … }`; the
	// formatter may break a record over several lines.
	start := strings.Index(src, "\ntype Failure\n")
	if start < 0 {
		t.Fatal("type Failure not found")
	}
	block := src[start:]
	if end := strings.Index(block[1:], "\n\n"); end > 0 {
		block = block[:end+1]
	}
	re := regexp.MustCompile(`(?s)[=|] ([A-Z][A-Za-z]*) \{([^}]*)\}`)
	sky := map[string][]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		var fields []string
		for _, f := range strings.Split(m[2], ",") {
			fields = append(fields, strings.TrimSpace(strings.SplitN(f, ":", 2)[0]))
		}
		sky[m[1]] = fields
	}
	var goCodes, skyCodes []string
	for c := range aiFailSpecs {
		goCodes = append(goCodes, string(c))
	}
	for c := range sky {
		skyCodes = append(skyCodes, c)
	}
	sort.Strings(goCodes)
	sort.Strings(skyCodes)
	if strings.Join(goCodes, ",") != strings.Join(skyCodes, ",") {
		t.Fatalf("codes differ:\n  Go:  %v\n  Sky: %v", goCodes, skyCodes)
	}
	for code, spec := range aiFailSpecs {
		for _, kv := range spec.attrs {
			found := false
			for _, f := range sky[string(code)] {
				found = found || f == kv[0]
			}
			if !found {
				t.Errorf("%s: Go attribute %s is not a field of the Sky constructor %v", code, kv[0], sky[string(code)])
			}
		}
	}
	kinds := armTargets(t, src, "kind")
	for code, spec := range aiFailSpecs {
		if code == aiRemoteRejected {
			continue
		}
		if kinds[string(code)] != errorKindCtors[spec.kind] {
			t.Errorf("%s: Sky kind %q, Go kind %q", code, kinds[string(code)], errorKindCtors[spec.kind])
		}
	}
	retry := armTargets(t, src, "retryable")
	for code, spec := range aiFailSpecs {
		if code == aiRemoteRejected {
			continue
		}
		got := retry[string(code)] == "True"
		if got != spec.retryable {
			t.Errorf("%s: Sky retryable %v, Go %v", code, got, spec.retryable)
		}
	}
}

// armTargets reads `name f = case f of Ctor _ -> Target …` arms.
func armTargets(t *testing.T, src, name string) map[string]string {
	t.Helper()
	start := strings.Index(src, "\n"+name+" f =\n")
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	body := src[start+1:]
	if end := strings.Index(body, "\n\n\n"); end > 0 {
		body = body[:end]
	}
	re := regexp.MustCompile(`(?m)^        ([A-Z][A-Za-z]*) [_a-z]+ ->\n\s+([A-Za-z]+)`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// An AI failure's attributes reach logs and the Sky.Spa wire: a URL in them is
// scheme, host and port only, never a path, query or user info.
func TestAiFailureAttributesCarryNoURLSecrets(t *testing.T) {
	url := "http://user:pass@127.0.0.1:1/v1/chat/completions?token=s3cret"
	_, _, err := aiPostOnce(t.Context(), url, "k", true, "{}")
	f, ok := err.(*aiFailure)
	if !ok || f.code != aiUnreachable {
		t.Fatalf("want Unreachable, got %v", err)
	}
	df, _ := DomainOf(aiErrorValue(err))
	target, _ := df.Attr("target")
	if target != "http://127.0.0.1:1" {
		t.Fatalf("target attribute %q", target)
	}
	for _, kv := range df.Attrs {
		for _, bad := range []string{"s3cret", "pass", "token"} {
			if strings.Contains(kv[1], bad) {
				t.Fatalf("attribute %s carries %q: %q", kv[0], bad, kv[1])
			}
		}
	}
}
