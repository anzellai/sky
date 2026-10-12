package rt

// ai_failure.go — the ONE place an AI failure becomes a Sky `Error`.
//
// Std.Ai.Endpoint and Std.Ai.Local report hardware and network facts as
// failures a program can handle: a full queue, a timed-out call, a crashed
// engine, a model that does not fit. Every such site calls aiFailf with a
// stable code and adds the code's attributes. aiErrorValue turns it into
// `Error kind { details = Just (Domain (DomainFailure "ai" code …)) }`
// (error_domain.go), and `Std.Ai.Failure.fromError` reads the typed view back.
// The table aiFailSpecs is the single source of the kind, the retryable flag
// and the attribute keys of every code; sky-stdlib/Std/Ai/Failure.sky follows
// it, and ai_failure_test.go fails when the two disagree.

import (
	"fmt"
	"strconv"
)

// aiFailCode is a stable failure code. The strings are the Std.Ai.Failure
// constructor names.
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
	aiBudgetExceeded   aiFailCode = "BudgetExceeded"
	aiToolchainFailed  aiFailCode = "ToolchainFailed"
	// aiGateFailed is raised by Std.Ai.Eval (Sky), never by the runtime; it
	// is in the table so the two sides stay one list.
	aiGateFailed aiFailCode = "GateFailed"
	// NotRegistered, NotPromotable and PromotionConflict are raised by
	// Std.Ai.Registry (Sky). AdapterMismatch is raised here, when a slot's
	// registry adapter was trained on another base than its engine runs.
	aiNotRegistered     aiFailCode = "NotRegistered"
	aiNotPromotable     aiFailCode = "NotPromotable"
	aiPromotionConflict aiFailCode = "PromotionConflict"
	aiAdapterMismatch   aiFailCode = "AdapterMismatch"
	// aiDatasetInvalid is a dataset that cannot be read, parsed or validated
	// (Std.Ai.Dataset). Its reasons name a file and a line, never row text.
	aiDatasetInvalid aiFailCode = "DatasetInvalid"
	// TrainingFailed and TrainingCancelled end a Std.Ai.Train run: the trainer
	// reported an error or died more often than the job allows relaunches, or
	// the run was cancelled. Both name the run and the last step it reached.
	aiTrainingFailed    aiFailCode = "TrainingFailed"
	aiTrainingCancelled aiFailCode = "TrainingCancelled"
	// DeviceUnsupported and StorageDenied are Std.Ai.Device failures: the
	// device cannot run the model on the backend the decision chose (a WebGPU
	// load that registers no device is one, never a silent CPU run), or the
	// device has too little storage for the download.
	aiDeviceUnsupported aiFailCode = "DeviceUnsupported"
	aiStorageDenied     aiFailCode = "StorageDenied"
)

// aiDomain is the DomainFailure domain of every AI failure.
const aiDomain = "ai"

// aiFailSpec is one row of the classification table: the ErrorKind, whether a
// retry of the same call can succeed (the Sky-visible flag, which
// `Error.isRetryable` reads), and the attribute keys with the value used when
// a site has none (strings default to ""; an Int that a site may not know,
// such as a timeout's elapsed time or a signal-killed engine's exit code, is
// left out instead and reads as `Nothing` in the typed view).
type aiFailSpec struct {
	kind      int
	retryable bool
	attrs     [][2]string // key, default
}

// aiFailSpecs is the classification. RemoteRejected's kind and flag depend
// on the HTTP status (aiRemoteKind); its row holds the attributes only.
//
// TimedOut and RemoteRejected 5xx are retryable: both are transient by the
// usual convention. EngineCrashed is not: a crash loop must not be retried
// blindly, and the engine's own supervisor restarts it.
var aiFailSpecs = map[aiFailCode]aiFailSpec{
	aiNotConfigured:     {errKindInvalidInput, false, [][2]string{{"setting", ""}}},
	aiInvalidSetting:    {errKindInvalidInput, false, [][2]string{{"setting", ""}, {"reason", ""}}},
	aiUnreachable:       {errKindUnavailable, true, [][2]string{{"target", ""}}},
	aiOverloaded:        {errKindUnavailable, true, nil},                           // "status" is optional
	aiTimedOut:          {errKindTimeout, true, nil},                               // "afterMs" is optional
	aiEngineCrashed:     {errKindUnavailable, false, [][2]string{{"logTail", ""}}}, // "exitCode" is optional
	aiEngineFailed:      {errKindUnavailable, false, [][2]string{{"reason", ""}}},
	aiModelTooLarge:     {errKindInvalidInput, false, [][2]string{{"needBytes", "0"}, {"availableBytes", "0"}}},
	aiNoAccelerator:     {errKindUnavailable, false, [][2]string{{"wanted", ""}}},
	aiNotProvisioned:    {errKindNotFound, false, [][2]string{{"what", ""}, {"command", ""}}},
	aiChecksumMismatch:  {errKindConflict, false, [][2]string{{"expected", ""}, {"actual", ""}}},
	aiRemoteRejected:    {errKindUnexpected, false, [][2]string{{"status", "0"}}},
	aiBudgetExceeded:    {errKindPermissionDenied, false, [][2]string{{"budget", ""}, {"estimate", ""}}},
	aiToolchainFailed:   {errKindUnavailable, false, [][2]string{{"tool", ""}, {"logTail", ""}}},
	aiGateFailed:        {errKindPermissionDenied, false, [][2]string{{"suite", ""}, {"reasons", ""}}},
	aiNotRegistered:     {errKindNotFound, false, [][2]string{{"ref", ""}}},
	aiNotPromotable:     {errKindPermissionDenied, false, [][2]string{{"ref", ""}, {"reason", ""}}},
	aiPromotionConflict: {errKindConflict, false, [][2]string{{"ref", ""}, {"expected", ""}, {"actual", ""}}},
	aiAdapterMismatch:   {errKindInvalidInput, false, [][2]string{{"adapter", ""}, {"adapterBase", ""}, {"engineBase", ""}}},
	aiDatasetInvalid:    {errKindInvalidInput, false, [][2]string{{"dataset", ""}, {"reasons", ""}}},
	aiTrainingFailed:    {errKindUnavailable, false, [][2]string{{"runId", ""}, {"step", "0"}, {"reason", ""}, {"logTail", ""}}},
	aiTrainingCancelled: {errKindConflict, false, [][2]string{{"runId", ""}, {"step", "0"}}},
	aiDeviceUnsupported: {errKindUnavailable, false, [][2]string{{"backend", ""}, {"reason", ""}}},
	aiStorageDenied:     {errKindPermissionDenied, false, [][2]string{{"needBytes", "0"}, {"availableBytes", "0"}}},
}

// aiFailure is a classified AI failure before it becomes a Sky value. Go code
// passes it around as an `error` so the engine and queue code stay ordinary
// Go; aiErrorValue turns it into the Sky `Error` at the kernel boundary.
type aiFailure struct {
	code aiFailCode
	msg  string
	// retryable is the endpoint's OWN retry policy (aiRemotePost): only an
	// overloaded server and a connection that never reached the server are
	// retried in place. It is narrower than the Sky-visible flag in
	// aiFailSpecs, because a timed-out call may have done the work.
	retryable    bool
	retryAfterMs int // 0 when the server named no delay
	httpStatus   int // for aiRemoteRejected and aiOverloaded
	attrs        [][2]string
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

// with sets an attribute of the failure (a later value for a key wins).
// Attributes go to logs and across the Sky.Spa wire: never a secret, and a URL
// only through aiRedactURL.
func (f *aiFailure) with(key, value string) *aiFailure {
	for i := range f.attrs {
		if f.attrs[i][0] == key {
			f.attrs[i][1] = value
			return f
		}
	}
	f.attrs = append(f.attrs, [2]string{key, value})
	return f
}

// withInt is with for an integer attribute.
func (f *aiFailure) withInt(key string, n int) *aiFailure {
	return f.with(key, strconv.Itoa(n))
}

// aiRemoteKind classifies a remote server's refusal by HTTP status.
func aiRemoteKind(status int) (int, bool) {
	switch {
	case status == 400 || status == 422:
		return errKindInvalidInput, false
	case status == 401 || status == 403:
		return errKindPermissionDenied, false
	case status == 404:
		return errKindNotFound, false
	case status >= 500:
		return errKindUnavailable, true
	}
	return errKindUnexpected, false
}

// aiDomainFailure is the DomainFailure an aiFailure becomes: the table's
// attributes in the table's order (a missing one gets its default, so the
// typed view always decodes), then any extra attribute a site added.
func aiDomainFailure(f *aiFailure) (int, DomainFailure) {
	spec, known := aiFailSpecs[f.code]
	kind, retryable := spec.kind, spec.retryable
	if !known {
		kind, retryable = errKindUnexpected, false
	}
	if f.code == aiRemoteRejected {
		if _, ok := f.attrValue("status"); !ok && f.httpStatus != 0 {
			f.withInt("status", f.httpStatus)
		}
		kind, retryable = aiRemoteKind(f.httpStatus)
	}
	if f.code == aiOverloaded && f.httpStatus != 0 {
		if _, ok := f.attrValue("status"); !ok {
			f.withInt("status", f.httpStatus)
		}
	}
	df := DomainFailure{Domain: aiDomain, Code: string(f.code), Retryable: retryable, RetryAfterMs: -1}
	if f.retryAfterMs > 0 {
		df.RetryAfterMs = f.retryAfterMs
	}
	for _, kv := range spec.attrs {
		v, ok := f.attrValue(kv[0])
		switch {
		case ok:
		case kv[0] == "reason":
			v = f.msg // a site's message is its reason
		default:
			v = kv[1]
		}
		df.Attrs = append(df.Attrs, [2]string{kv[0], v})
	}
	for _, kv := range f.attrs {
		if _, listed := df.Attr(kv[0]); !listed {
			df.Attrs = append(df.Attrs, kv)
		}
	}
	return kind, df
}

func (f *aiFailure) attrValue(key string) (string, bool) {
	for _, kv := range f.attrs {
		if kv[0] == key {
			return kv[1], true
		}
	}
	return "", false
}

// aiErrorValue is the classification: an aiFailure becomes a Sky `Error`
// whose kind comes from aiFailSpecs and whose details are the typed
// `Domain` failure. The message starts with the code, so a log line names
// the failure class.
func aiErrorValue(err error) any {
	if se, ok := err.(*aiSkyError); ok {
		return se.v
	}
	f, ok := err.(*aiFailure)
	if !ok {
		return ErrUnexpected("Std.Ai: " + err.Error())
	}
	kind, df := aiDomainFailure(f)
	return ErrDomain(kind, "Std.Ai "+string(f.code)+": "+f.msg, df)
}

// aiTaskErr is the Task-shaped failure: `Err e` for a kernel's thunk.
func aiTaskErr(err error) any { return Err[any, any](aiErrorValue(err)) }
