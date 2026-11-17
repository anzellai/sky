package rt

// admission.go — server admission control (Std.Admission).
//
//	Admission_install : Int -> Int -> Int -> Bool
//	                    -> (Request -> String -> String -> Int -> Int -> Task Error Int)
//	                    -> Task Error ()
//
// A burst of requests used to queue inside the process: every `/_rpc/<Msg>`
// (Sky.Spa) and every `App.api` / `Server.api` route ran its handler at once,
// with no bound. The gate below gives each route class (Rpc, Api) a limit on
// the requests it runs at the same time. A request over the limit is refused
// at once with 503 and a Retry-After, before its body is read and before any
// app code runs (admission_http.go). An optional hook, written in Sky, runs
// next: it reads the request (its session cookie, a header) and the load
// (`inFlight`, `limit`) and answers Admit, Throttle (429) or Shed (503).
//
// The default is no limit and no hook: behaviour unchanged. One policy per
// process: the last `install` wins (an App runs it at boot). The operator's
// SKY_ADMISSION_RPC_LIMIT / SKY_ADMISSION_API_LIMIT win over the code's limit
// (operator env > code > default). Adaptive limiting (a limit that moves with
// latency) is not provided.
//
// This file has no build tag: the policy state is plain atomics, so a wasm
// client that links Std.App still builds. The HTTP half is admission_http.go.

import (
	"strconv"
	"sync"
	"sync/atomic"
)

// admissionClass is a route class. Its name is the Sky `RouteClass`
// constructor in lower case, which the hook adapter reads back.
type admissionClass int

const (
	admissionRpc admissionClass = iota
	admissionApi
	admissionClasses
)

func (c admissionClass) String() string {
	if c == admissionApi {
		return "api"
	}
	return "rpc"
}

// envLimit is the operator variable of a class's limit
// (SKY_ADMISSION_RPC_LIMIT / SKY_ADMISSION_API_LIMIT), and whether it is set.
// Each read names its suffix as a literal, so the config-surface census sees
// it.
func (c admissionClass) envLimit() (string, bool) {
	if c == admissionApi {
		return skyLookupEnv("ADMISSION_API_LIMIT")
	}
	return skyLookupEnv("ADMISSION_RPC_LIMIT")
}

// admissionDefaultRetryAfterMs is the Retry-After of a refusal when the
// policy names none.
const admissionDefaultRetryAfterMs = 1000

type admissionGate struct {
	inFlight [admissionClasses]atomic.Int64
	// codeLimit is the limit the installed policy names (0: none).
	codeLimit [admissionClasses]atomic.Int64
	// limit is the effective limit: the operator's variable, else codeLimit.
	limit        [admissionClasses]atomic.Int64
	retryAfterMs atomic.Int64
	// hook is the Sky hook adapter (nil: no hook), read per request.
	hook atomic.Pointer[admissionHook]
	// envOnce resolves the operator variables for a process that never
	// installs a policy (a plain server with only the variables set).
	envOnce sync.Once
}

type admissionHook struct{ fn any }

var admission = newAdmissionGate()

func newAdmissionGate() *admissionGate {
	g := &admissionGate{}
	g.retryAfterMs.Store(admissionDefaultRetryAfterMs)
	return g
}

// resolveLimits recomputes the effective limits: an operator variable that
// is a whole number of 0 or more wins (0 turns the limit off), anything else
// leaves the code's limit.
func (g *admissionGate) resolveLimits() {
	for c := admissionClass(0); c < admissionClasses; c++ {
		lim := g.codeLimit[c].Load()
		if v, ok := c.envLimit(); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				lim = int64(n)
			}
		}
		g.limit[c].Store(lim)
	}
}

// effectiveLimit is the class's limit, 0 when there is none.
func (g *admissionGate) effectiveLimit(c admissionClass) int64 {
	g.envOnce.Do(g.resolveLimits)
	return g.limit[c].Load()
}

// install sets the policy (the Sky `install`, and the tests).
func (g *admissionGate) install(rpcLimit, apiLimit, retryAfterMs int, hook any) {
	if rpcLimit < 0 {
		rpcLimit = 0
	}
	if apiLimit < 0 {
		apiLimit = 0
	}
	if retryAfterMs < 0 {
		retryAfterMs = 0
	}
	g.codeLimit[admissionRpc].Store(int64(rpcLimit))
	g.codeLimit[admissionApi].Store(int64(apiLimit))
	g.retryAfterMs.Store(int64(retryAfterMs))
	if hook != nil {
		g.hook.Store(&admissionHook{fn: hook})
	} else {
		g.hook.Store(nil)
	}
	g.envOnce.Do(func() {})
	g.resolveLimits()
}

// Admission_install is the kernel behind `Std.Admission.install`.
func Admission_install(rpcLimit, apiLimit, retryAfterMs, hasHook, hook any) any {
	return func() any {
		var h any
		if AsBool(hasHook) {
			h = hook
		}
		admission.install(AsInt(rpcLimit), AsInt(apiLimit), AsInt(retryAfterMs), h)
		return Ok[any, any](struct{}{})
	}
}

// admissionDecision is a decoded hook answer. The Sky adapter encodes a
// Decision as one Int: -1 Admit, 2*ms Throttle ms, 2*ms+1 Shed ms.
type admissionDecision struct {
	status       int // 0 admit, 429, 503
	retryAfterMs int64
}

func decodeAdmissionDecision(code int) admissionDecision {
	if code < 0 {
		return admissionDecision{}
	}
	ms := int64(code / 2)
	if code%2 == 1 {
		return admissionDecision{status: 503, retryAfterMs: ms}
	}
	return admissionDecision{status: 429, retryAfterMs: ms}
}

// retryAfterSeconds renders milliseconds as a Retry-After value: whole
// seconds, rounded up.
func retryAfterSeconds(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	return strconv.FormatInt((ms+999)/1000, 10)
}
