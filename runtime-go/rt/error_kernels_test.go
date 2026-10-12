package rt

import "testing"

// The typed kernels behind Error.isRetryable, Error.domain and
// Error.retryAfter (error_kernels.go). They read an `Error` in every shape it
// reaches the runtime in and never panic.

// typedKind stands for the named-int enum codegen emits for `ErrorKind`.
type typedKind int

// typedError builds an Error the way typed codegen does: the kind as a named
// int, the info as SkyErrorInfo, the details as a typed SkyMaybe.
func typedError(kind int, details SkyMaybe[any]) any {
	return SkyADT{
		Tag:     0,
		SkyName: "Error",
		Fields:  []any{typedKind(kind), SkyErrorInfo{Message: "m", Details: details}},
	}
}

func TestErrorIsRetryableByKind(t *testing.T) {
	retryable := map[int]bool{
		errKindTimeout:     true,
		errKindNetwork:     true,
		errKindUnavailable: true,
	}
	for tag := errKindIo; tag <= errKindUnexpected; tag++ {
		want := retryable[tag]
		if got := Error_isRetryableT(makeError(tag, errorKindCtors[tag], "m")); got != want {
			t.Errorf("runtime-built kind %d: got %v, want %v", tag, got, want)
		}
		if got := Error_isRetryableT(typedError(tag, Nothing[any]())); got != want {
			t.Errorf("typed kind %d: got %v, want %v", tag, got, want)
		}
		adtKind := SkyADT{Tag: 0, SkyName: "Error", Fields: []any{
			SkyADT{Tag: tag, SkyName: errorKindCtors[tag]},
			SkyErrorInfo{Message: "m", Details: Nothing[any]()},
		}}
		if got := Error_isRetryableT(adtKind); got != want {
			t.Errorf("ADT kind %d: got %v, want %v", tag, got, want)
		}
		if got := Error_isRetryable(makeError(tag, errorKindCtors[tag], "m")); got != any(want) {
			t.Errorf("erased form, kind %d: got %v, want %v", tag, got, want)
		}
	}
}

func TestErrorDomainFailureDecidesRetry(t *testing.T) {
	// An Unavailable error whose domain failure says "do not retry" (a crashed
	// engine) is not retryable, and a domain failure that says "retry" wins
	// over a non-transient kind.
	crash := ErrDomain(errKindUnavailable, "crash", DomainFailure{Domain: "ai", Code: "EngineCrashed", RetryAfterMs: -1})
	if Error_isRetryableT(crash) {
		t.Fatal("a domain failure that is not retryable must decide, not the kind")
	}
	busy := ErrDomain(errKindInvalidInput, "busy", DomainFailure{Domain: "ai", Code: "Overloaded", Retryable: true, RetryAfterMs: 250})
	if !Error_isRetryableT(busy) {
		t.Fatal("a retryable domain failure must be retryable")
	}
	if got := Error_domainT(busy); got.Tag != 0 || got.JustValue != "ai" {
		t.Fatalf("domain: got %#v", got)
	}
	if got := Error_retryAfterT(busy); got.Tag != 0 || got.JustValue != 250 {
		t.Fatalf("retryAfter: got %#v", got)
	}
	if got := Error_retryAfterT(crash); got.Tag != 1 {
		t.Fatalf("no delay named: got %#v", got)
	}
	if got := Error_domain(busy); got != any(Just[string]("ai")) {
		t.Fatalf("erased domain: got %#v", got)
	}
	if got := Error_retryAfter(busy); got != any(Just[int](250)) {
		t.Fatalf("erased retryAfter: got %#v", got)
	}
}

func TestErrorDomainWithBadAttributesStillReads(t *testing.T) {
	bad := SkyADT{Tag: 0, SkyName: "DomainFailure", Fields: []any{
		"ai", "Overloaded", true, Just[any](7),
		[]any{SkyTuple2{V0: 1, V1: "x"}, "not a tuple", nil, SkyTuple2{V0: "k", V1: "v"}},
	}}
	err := typedError(errKindIo, Just[any](SkyADT{Tag: errorDetailsDomainTag, SkyName: errorDetailsDomainCtr, Fields: []any{bad}}))
	if !Error_isRetryableT(err) {
		t.Fatal("bad attributes must not hide the retryable flag")
	}
	if got := Error_retryAfterT(err); got.Tag != 0 || got.JustValue != 7 {
		t.Fatalf("retryAfter: got %#v", got)
	}
}

func TestErrorKernelsAreTotalOnMalformedValues(t *testing.T) {
	shapes := []any{
		nil,
		42,
		"Error",
		SkyADT{},
		SkyADT{SkyName: "Error"},
		SkyADT{SkyName: "Error", Fields: []any{nil, nil}},
		SkyADT{SkyName: "Error", Fields: []any{"Timeout", "info"}},
		SkyADT{SkyName: "Error", Fields: []any{typedKind(99), SkyErrorInfo{}}},
		// a Domain whose failure has the wrong shape: falls back to the kind
		typedError(errKindTimeout, Just[any](SkyADT{Tag: errorDetailsDomainTag, SkyName: errorDetailsDomainCtr, Fields: []any{
			SkyADT{SkyName: "DomainFailure", Fields: []any{1, 2, "x", nil, nil}},
		}})),
		typedError(errKindTimeout, Just[any](SkyADT{Tag: errorDetailsDomainTag, SkyName: errorDetailsDomainCtr})),
		typedError(errKindTimeout, Just[any](nil)),
	}
	for i, v := range shapes {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("shape %d panicked: %v", i, r)
				}
			}()
			_ = Error_isRetryableT(v)
			_ = Error_domainT(v)
			_ = Error_retryAfterT(v)
			_ = Error_isRetryable(v)
			_ = Error_domain(v)
			_ = Error_retryAfter(v)
		}()
	}
	if Error_isRetryableT(nil) {
		t.Fatal("nil is not retryable")
	}
	if !Error_isRetryableT(shapes[8]) {
		t.Fatal("a malformed domain failure falls back to the kind (Timeout)")
	}
	if Error_domainT(shapes[8]).Tag != 1 {
		t.Fatal("a malformed domain failure has no domain")
	}
}
