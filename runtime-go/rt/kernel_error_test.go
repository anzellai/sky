package rt

import "testing"

// Regression (2026-05-20): a kernel whose Sky type is `Task Error _`
// must put an Error ADT in the Err channel, never a bare string.
//
// A string error survives until the result flows into a concrete
// TaskCoerceT[Error, _] — then ResultCoerce's Err branch runs
// coerceInner[Error](string) and panics ("source string cannot be
// cast to target rt.SkyADT"). This is exactly how the `skydeploy`
// CLI crashed at the `gcloud builds submit` step: Process_run
// returned Err[any,any](string).
func TestKernelErrorsAreErrorAdt(t *testing.T) {
	cases := []struct {
		name string
		run  func() any
	}{
		{
			"Process_run missing command",
			func() any {
				return Process_run("sky-no-such-command-xyzzy", []any{}).(func() any)()
			},
		},
		{
			"File_readFileLimit over-limit",
			func() any {
				// /etc/hosts is always present and > 1 byte.
				return File_readFileLimit("/etc/hosts", 1).(func() any)()
			},
		},
	}
	for _, tc := range cases {
		res := tc.run()
		r, ok := res.(SkyResult[any, any])
		if !ok {
			t.Fatalf("%s: result is %T, want SkyResult[any,any]", tc.name, res)
		}
		if r.Tag == 0 {
			t.Fatalf("%s: expected an Err result, got Ok", tc.name)
		}
		if _, isStr := r.ErrValue.(string); isStr {
			t.Fatalf("%s: Err carries a bare string — a `Task Error _` "+
				"surface must carry an Error ADT", tc.name)
		}
		if _, isErr := r.ErrValue.(skyErrorAdt); !isErr {
			t.Fatalf("%s: Err value is %T, want skyErrorAdt", tc.name, r.ErrValue)
		}
	}
}
