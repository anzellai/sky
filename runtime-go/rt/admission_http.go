//go:build !js

package rt

// admission_http.go — the HTTP half of Std.Admission (admission.go).
//
// A gated route runs in this order:
//
//  1. the route's own refusals that need no app code (rpcRequestGuard for a
//     Sky.Spa RPC: method, content type, origin), so a bad origin is still a
//     403, never a 429 / 503;
//  2. admissionEnter: take a slot of the route class. Over the limit, the
//     request is answered 503 with Retry-After at once: its body is not read
//     and the handler never runs;
//  3. the request is built once (body read once) and the policy's hook, if
//     any, decides: Admit, Throttle (429) or Shed (503);
//  4. the handler.
//
// The slot is released when the handler returns, also when it panics.

import (
	"fmt"
	"net/http"
	"strconv"

	"sky-app/rt/telemetry"
)

// admissionTicket is a taken slot: its class, the route it serves, and the
// load the hook sees. A nil ticket means the gate is off for the class.
type admissionTicket struct {
	class    admissionClass
	route    string
	inFlight int64
	limit    int64
	released bool
}

// release gives the slot back. Safe to call twice.
func (t *admissionTicket) release() {
	if t == nil || t.released {
		return
	}
	t.released = true
	admission.inFlight[t.class].Add(-1)
}

// admissionEnter takes a slot of class c for a request to route. ok is false
// when the class is at its limit: the 503 has been written and the caller
// must return. A nil ticket with ok true means the gate is off (no limit and
// no hook): nothing is counted.
func admissionEnter(w http.ResponseWriter, c admissionClass, route string) (*admissionTicket, bool) {
	g := admission
	limit := g.effectiveLimit(c)
	hook := g.hook.Load()
	if limit <= 0 && hook == nil {
		return nil, true
	}
	n := g.inFlight[c].Add(1)
	if limit > 0 && n > limit {
		g.inFlight[c].Add(-1)
		admissionRefuse(w, c, route, http.StatusServiceUnavailable, g.retryAfterMs.Load(),
			fmt.Sprintf("the %s route class is at its limit of %d requests at the same time", c, limit))
		return nil, false
	}
	return &admissionTicket{class: c, route: route, inFlight: n, limit: limit}, true
}

// admissionAdmit runs the policy's hook for a request that holds a slot. It
// returns false when the hook refused the request (the answer is written).
// A hook that fails (an Err, a panic, an answer of the wrong shape) sheds
// the request with the policy's Retry-After: under load, a broken hook must
// not let everything through.
func admissionAdmit(w http.ResponseWriter, t *admissionTicket, req SkyRequest) (admitted bool) {
	if t == nil {
		return true
	}
	h := admission.hook.Load()
	if h == nil || h.fn == nil {
		return true
	}
	defer func() {
		if rec := recover(); rec != nil {
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			logPanicFrame(req.Method, req.Path, rec)
			admissionRefuse(w, t.class, t.route, http.StatusServiceUnavailable, admission.retryAfterMs.Load(),
				"the admission hook failed")
			admitted = false
		}
	}()
	res := anyTaskInvoke(SkyCall(h.fn, req, t.class.String(), t.route, int(t.inFlight), int(t.limit)))
	if res.Tag != 0 {
		msg, _ := renderSkyError(res.ErrValue)
		admissionRefuse(w, t.class, t.route, http.StatusServiceUnavailable, admission.retryAfterMs.Load(),
			"the admission hook failed: "+msg)
		return false
	}
	d := decodeAdmissionDecision(AsInt(res.OkValue))
	if d.status == 0 {
		return true
	}
	reason := "the admission hook throttled this request"
	if d.status == http.StatusServiceUnavailable {
		reason = "the admission hook shed this request"
	}
	admissionRefuse(w, t.class, t.route, d.status, d.retryAfterMs, reason)
	return false
}

// admissionRefuse answers a refused request: 429 or 503, Retry-After in
// whole seconds, and a JSON body in the shape of the RPC guard's refusals
// (rpcRefuse). It counts the refusal as sky_admission_refused_total.
func admissionRefuse(w http.ResponseWriter, c admissionClass, route string, status int, retryAfterMs int64, reason string) {
	code := "overloaded"
	if status == http.StatusTooManyRequests {
		code = "throttled"
	}
	h := w.Header()
	h.Set("Retry-After", retryAfterSeconds(retryAfterMs))
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	hint := "The server admits a bounded number of requests at the same time (Std.Admission). Retry after the Retry-After interval; the Sky.Spa client does so by itself."
	fmt.Fprintf(w, `{"status":%q,"reason":%q,"hint":%q}`, code, reason, hint)
	telemetry.Default().Inc("sky_admission_refused_total", map[string]string{
		"class":  c.String(),
		"route":  route,
		"status": strconv.Itoa(status),
	})
}
