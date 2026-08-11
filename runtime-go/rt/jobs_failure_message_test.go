package rt

import (
	"strings"
	"testing"

	"sky-app/rt/jobs"
)

// generatedErrorInfo stands in for `Sky_Core_Error_ErrorInfo_R` — the struct
// the CODE GENERATOR emits for `Sky.Core.Error.ErrorInfo` in every compiled
// app. It is a distinct, app-package type: the runtime can only read it
// structurally, never by type assertion. That is the whole point of this
// fixture.
type generatedErrorInfo struct {
	Message string
	Details any
}

// A failing job's recorded error must be the Sky error's MESSAGE, not a Go
// struct dump of the Error ADT.
//
// `Jobs_define`'s wrapper turned a failed handler into
// `fmt.Errorf("%v", extractErrResultValue(result))`. `extractErrResultValue`
// returns the Sky `Error` ADT *value*, and `%v` on it renders the struct, so
// the only durable record of a job failure — `_sky_jobs.last_error`, and
// `_sky_jobs_dead.final_error` after the retries are exhausted — read:
//
//	{0 Error [7 {dispatch: deliberate job failure for payload 42 {1 {0  []}}}]}
//
// instead of the message. That is observed output, taken from the `_sky_jobs`
// ledger of a real app running a real failing job. The operator inspecting why
// a job dead-lettered gets an ADT dump; the message is in there, but wrapped in
// the internal representation, and the kind/details are unreadable.
//
// The package already has `extractErrMsg` for exactly this, used by the other
// Err-unwrapping call sites. The jobs path simply never used it — nothing
// imported Std.Jobs, so no test had ever looked at a recorded failure.
func TestJobsHandler_FailureRecordsSkyErrorMessage(t *testing.T) {
	jobs.ResetHandlersForTest()

	const name = "test-failing-job"
	const msg = "deliberate job failure for payload 42"

	// A Sky handler: `a -> Task Error ()`. In the emitted representation a
	// Task is a zero-arg thunk returning a SkyResult, so the handler is a
	// func(any) any returning func() any.
	//
	// The error MUST be built in the shape CODEGEN emits, not with rt's own
	// `ErrInvalidInput`. A compiled app's `Error.invalidInput` lowers to a
	// GENERATED info struct (`Sky_Core_Error_ErrorInfo_R`, with a `Message`
	// field), not to `rt.SkyErrorInfo` — so any unwrapper that type-asserts on
	// rt's own types matches in a unit test and misses in every real app. An
	// earlier version of this test used `ErrInvalidInput` and went green
	// against an unwrapper that still produced an ADT dump in production.
	handler := func(_ any) any {
		return func() any {
			return Err[any, any](SkyADT{
				Tag:     0,
				SkyName: "Error",
				Fields: []any{
					7, // the ErrorKind tag
					generatedErrorInfo{Message: msg, Details: nil},
				},
			})
		}
	}

	if ref := Jobs_define(name, handler); ref != name {
		t.Fatalf("Jobs_define should return the job name as its opaque ref, got %v", ref)
	}

	h, ok := jobs.LookupHandler(name)
	if !ok {
		t.Fatal("Jobs_define did not register the handler")
	}

	payload, err := jobs.EncodePayload(7)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}

	runErr := h(payload)
	if runErr == nil {
		t.Fatal("a handler whose Task returns Err MUST surface a non-nil error — " +
			"a swallowed failure would be recorded as success and never retried")
	}

	got := runErr.Error()

	if got != msg {
		t.Errorf("recorded job error must be the Sky error MESSAGE.\n want: %q\n got:  %q", msg, got)
	}

	// The specific regression: the ADT's Go struct rendering. `{0 Error [`
	// and the trailing `[]}` bracket soup are the signature of `%v` over
	// skyErrorAdt, and must never reach an operator-facing field.
	for _, leak := range []string{"{0 Error", "skyErrorAdt", "[]}"} {
		if strings.Contains(got, leak) {
			t.Errorf("recorded job error leaks the internal Error ADT representation "+
				"(found %q) — `_sky_jobs.last_error` is the operator's only record "+
				"of why a job failed.\n got: %q", leak, got)
		}
	}
}

// A handler whose Task SUCCEEDS must report no error, so the success path is
// not accidentally made failing by the unwrapping change above.
func TestJobsHandler_SuccessReportsNoError(t *testing.T) {
	jobs.ResetHandlersForTest()

	const name = "test-succeeding-job"
	handler := func(_ any) any {
		return func() any {
			return Ok[any, any](struct{}{})
		}
	}
	Jobs_define(name, handler)

	h, ok := jobs.LookupHandler(name)
	if !ok {
		t.Fatal("Jobs_define did not register the handler")
	}
	payload, err := jobs.EncodePayload(1)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	if runErr := h(payload); runErr != nil {
		t.Fatalf("a succeeding handler must report nil, got %v", runErr)
	}
}
