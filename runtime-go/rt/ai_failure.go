//go:build !js

package rt

// ai_failure.go — the ONE place an AI failure becomes a Sky `Error`.
//
// Std.Ai.Endpoint and Std.Ai.Local report hardware and network facts as
// failures a program can handle: a full queue, a timed-out call, a crashed
// engine, a model that does not fit. Every such site calls aiFail with a
// stable code. The code decides the ErrorKind, so handlers that match on
// ErrorKind work today, and the design's typed representation
// (docs/design/std-ai-lifecycle.md §8.3, the `Domain DomainFailure` variant)
// replaces only this function: no call site changes.

import "fmt"

// aiFailCode is a stable failure code. The strings are the design's
// Std.Ai.Failure constructor names, so the later typed payload keeps them.
type aiFailCode string

const (
	aiNotConfigured    aiFailCode = "NotConfigured"
	aiUnreachable      aiFailCode = "Unreachable"
	aiOverloaded       aiFailCode = "Overloaded"
	aiTimedOut         aiFailCode = "TimedOut"
	aiEngineCrashed    aiFailCode = "EngineCrashed"
	aiModelTooLarge    aiFailCode = "ModelTooLarge"
	aiNoAccelerator    aiFailCode = "NoAccelerator"
	aiNotProvisioned   aiFailCode = "NotProvisioned"
	aiChecksumMismatch aiFailCode = "ChecksumMismatch"
	aiInvalidSetting   aiFailCode = "InvalidSetting"
	aiRemoteRejected   aiFailCode = "RemoteRejected"
	aiEngineFailed     aiFailCode = "EngineFailed"
)

// aiFailure is a classified AI failure before it becomes a Sky value. Go code
// passes it around as an `error` so the engine and queue code stay ordinary
// Go; aiErrorValue turns it into the Sky `Error` at the kernel boundary.
type aiFailure struct {
	code         aiFailCode
	msg          string
	retryable    bool
	retryAfterMs int // 0 when the server named no delay
	httpStatus   int // for aiRemoteRejected
}

func (f *aiFailure) Error() string { return string(f.code) + ": " + f.msg }

func aiFailf(code aiFailCode, format string, args ...any) *aiFailure {
	f := &aiFailure{code: code, msg: fmt.Sprintf(format, args...)}
	switch code {
	case aiOverloaded, aiUnreachable:
		f.retryable = true
	}
	return f
}

// aiErrorValue is the classification: code -> ErrorKind. It is the only
// function that decides how an AI failure looks to Sky code.
//
//	Overloaded, Unreachable, EngineCrashed, NoAccelerator, EngineFailed -> Unavailable
//	TimedOut                                               -> Timeout
//	ModelTooLarge, NotConfigured, InvalidSetting           -> InvalidInput
//	ChecksumMismatch                                       -> Conflict
//	NotProvisioned                                         -> NotFound
//	RemoteRejected                                         -> by HTTP status
//
// The message starts with the code, so a log line names the failure class
// before the typed payload exists.
func aiErrorValue(err error) any {
	f, ok := err.(*aiFailure)
	if !ok {
		return ErrUnexpected("Std.Ai: " + err.Error())
	}
	msg := "Std.Ai " + string(f.code) + ": " + f.msg
	switch f.code {
	case aiOverloaded, aiUnreachable, aiEngineCrashed, aiNoAccelerator, aiEngineFailed:
		return ErrUnavailable(msg)
	case aiTimedOut:
		return makeError(4, "Timeout", msg)
	case aiModelTooLarge, aiNotConfigured, aiInvalidSetting:
		return ErrInvalidInput(msg)
	case aiChecksumMismatch:
		return ErrConflict(msg)
	case aiNotProvisioned:
		return makeError(5, "NotFound", msg)
	case aiRemoteRejected:
		switch {
		case f.httpStatus == 400 || f.httpStatus == 422:
			return ErrInvalidInput(msg)
		case f.httpStatus == 401 || f.httpStatus == 403:
			return ErrPermissionDenied(msg)
		case f.httpStatus == 404:
			return makeError(5, "NotFound", msg)
		case f.httpStatus >= 500:
			return ErrUnavailable(msg)
		}
		return ErrUnexpected(msg)
	}
	return ErrUnexpected(msg)
}

// aiTaskErr is the Task-shaped failure: `Err e` for a kernel's thunk.
func aiTaskErr(err error) any { return Err[any, any](aiErrorValue(err)) }
