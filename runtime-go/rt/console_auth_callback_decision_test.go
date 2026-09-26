package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// consoleTestIdentity has the Go shape the typed emitter gives a Sky
// `{ subject : String, email : String, claims : Dict String String }` record.
type consoleTestIdentity struct {
	Subject string
	Email   string
	Claims  map[string]string
}

// anyConsoleCallback builds a `Request -> Task Error (Maybe Identity)` whose
// task is any-typed, yielding the given Result.
func anyConsoleCallback(result SkyResult[any, any]) any {
	return func(_ any) any {
		return func() any { return result }
	}
}

// typedConsoleCallback returns the concretely typed task the typed emitter
// produces: a named SkyTask[E, A].
func typedConsoleCallback[E any, A any](result SkyResult[E, A]) any {
	return func(_ any) any {
		return SkyTask[E, A](func() SkyResult[E, A] { return result })
	}
}

func appModeRequest(t *testing.T, cb any) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	t.Setenv("SKY_CONSOLE_AUTH", "app")
	ResetConsoleAuthStateForTesting()
	withServerlessEnv(t, nil)
	SetConsoleAuthCallback(cb)
	t.Cleanup(func() { SetConsoleAuthCallback(nil) })
	r := httptest.NewRequest("GET", "/_sky/console/", nil)
	w := httptest.NewRecorder()
	return w, evaluateConsoleAuth(w, r)
}

// The app-mode console must deny every callback result except
// `Ok (Just identity)` with a non-empty subject.
//
// Regression: the decision compared the typed int tags against the strings
// "Err" / "Nothing" / "Just". None matched, so Nothing and Err were read as
// allow with an empty identity, and every request to an app-mode console was
// let in (a real Sky.Live app with a callback returning Nothing for a request
// without a session cookie answered 200 and set a console cookie).
func TestConsoleAppMode_OnlyOkJustWithSubjectIsAllowed(t *testing.T) {
	admin := consoleTestIdentity{Subject: "u1", Email: "a@example.test", Claims: map[string]string{}}
	type maybeID = SkyMaybe[consoleTestIdentity]
	cases := []struct {
		name  string
		cb    any
		allow bool
	}{
		{"Ok Nothing (any task)", anyConsoleCallback(Ok[any, any](Nothing[any]())), false},
		{"Ok Nothing (typed task)", typedConsoleCallback(Ok[error, maybeID](Nothing[consoleTestIdentity]())), false},
		{"Err (any task)", anyConsoleCallback(Err[any, any]("denied")), false},
		{"Err (typed task)", typedConsoleCallback(Err[error, maybeID](nil)), false},
		{"Ok Just empty subject", anyConsoleCallback(Ok[any, any](Just[any](consoleTestIdentity{Email: "x@example.test"}))), false},
		{"Ok Just whitespace subject", typedConsoleCallback(Ok[error, maybeID](Just(consoleTestIdentity{Subject: "  "}))), false},
		{"task is not a Result", func(_ any) any { return func() any { return "surprise" } }, false},
		{"Ok Just admin (any task)", anyConsoleCallback(Ok[any, any](Just[any](admin))), true},
		{"Ok Just admin (typed task)", typedConsoleCallback(Ok[error, maybeID](Just(admin))), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, ok := appModeRequest(t, tc.cb)
			if ok != tc.allow {
				t.Fatalf("allowed=%v, want %v", ok, tc.allow)
			}
			setCookie := w.Result().Header.Get("Set-Cookie")
			issued := strings.Contains(setCookie, consoleAuthCookieV2Name+"=") && !strings.Contains(setCookie, "Max-Age=0")
			if tc.allow {
				if !issued {
					t.Fatalf("allowed request got no console session cookie (Set-Cookie=%q)", setCookie)
				}
				return
			}
			if issued {
				t.Fatalf("denied request was issued a console session cookie: %q", setCookie)
			}
			if w.Result().StatusCode != http.StatusForbidden {
				t.Fatalf("status %d, want 403", w.Result().StatusCode)
			}
		})
	}
}

func TestConsoleAppMode_PanickingCallbackIsDenied(t *testing.T) {
	cb := func(_ any) any { panic("callback exploded") }
	w, ok := appModeRequest(t, cb)
	if ok {
		t.Fatal("a panicking callback must deny")
	}
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Result().StatusCode)
	}
}

// slidingUnwrapMaybe read the same string tags, so a typed `Nothing` and a
// typed `Just f` both came back as the Maybe struct itself. Calling that as
// the revocation check panicked, which reads as "revoked", so the sliding
// token was never re-issued.
func TestSlidingUnwrapMaybe_ReadsTypedMaybe(t *testing.T) {
	check := func(sub string) any { return nil }
	if got := slidingUnwrapMaybe(Nothing[any]()); got != nil {
		t.Fatalf("Nothing: got %T, want nil", got)
	}
	if got := slidingUnwrapMaybe(Nothing[func(string) any]()); got != nil {
		t.Fatalf("typed Nothing: got %T, want nil", got)
	}
	if _, ok := slidingUnwrapMaybe(Just[any](check)).(func(string) any); !ok {
		t.Fatal("Just f: want the function back")
	}
	if _, ok := slidingUnwrapMaybe(Just(check)).(func(string) any); !ok {
		t.Fatal("typed Just f: want the function back")
	}
	if slidingUnwrapMaybe(check) == nil {
		t.Fatal("a bare function must pass through")
	}
}
