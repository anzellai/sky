package rt

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// spa_retry.go — transport resilience for the Sky.Spa client (portable, no
// build tag, so every rule here is unit-tested on the host with an injected
// clock and timer; the js adapter is fetchBlocking in http_wasm.go).
//
// A phone that switches apps, a laptop that sleeps, a radio that wakes up
// late, a proxy that answers 503 for a second during a deploy: each of these
// fails ONE request at the network level. Before v0.27.3 that one failure
// stopped the client: the red "Can't reach the server" bar appeared at once,
// a hold RPC kept every later Msg waiting, and only the Retry button moved it
// on. The runtime now treats such a failure as TRANSIENT and retries the
// request itself, so the app and its user never see a blip.
//
//   - Classes. A network failure (fetch rejected), a Timeout (the request did
//     not settle in spaFetchTimeout and was aborted) and the statuses 408,
//     425, 429, 502, 503 and 504 are TRANSIENT. Every other answer is FINAL
//     and goes to the app as before.
//   - Which requests. An auto-split RPC (`POST /_rpc/<Msg>?rid=<id>`) is
//     always safe to re-send: the backend answers a repeated request id from
//     its dedupe cache (spa_rpc_dedupe.go). A client `Http.get` is re-sent; a
//     client `Http.post` is not (it may not be idempotent).
//   - Schedule. Full jitter: the wait before retry n is a uniform value in
//     [0, min(cap, base * 2^(n-1))], base 1 s, cap 30 s. A `Retry-After`
//     header (seconds or an HTTP date) wins when present.
//   - Budget. One retry is in flight per client: failed requests wait in a
//     FIFO queue and are re-sent in the order they failed (a mutation queued
//     during an outage runs once, in order, on recovery). A request gives up
//     after spaRetryMaxAttempts failures or spaRetryBudget of trying, and its
//     last result is then delivered as FINAL (the red bar, App.withRpcError).
//   - Recovery signals (`online`, `visibilitychange` to visible, `pageshow`,
//     `focus`) re-send the head of the queue at once and reset every tracked
//     request's attempt count and budget.
//   - Running time, not wall time (v0.27.6). The budget and the fetch timeout
//     count only time the page could run. While the page is hidden no request
//     is given up (setHidden): it waits, and the return to a visible page gives
//     it a fresh budget and re-sends it at once. A freeze that no event
//     announced (a laptop lid, a frozen tab, the back/forward cache) is found
//     by its effect: a timer that fires spaSuspendGap or more after it was due,
//     or an attempt that outlived its own spaFetchTimeout abort by
//     spaSuspendGap. Either is a resume (resumeLocked): every request the
//     coordinator tracks, on the wire or waiting, starts a fresh budget, and a
//     request whose attempt spanned the gap is re-sent at once. Before this a
//     request in flight when the page froze was judged at resume against the
//     wall clock: its overdue abort fired, the budget read as spent, and the
//     failure went to App.withRpcError without one re-send.
//   - Retry throttle (the gRPC A6 shape): a token bucket per origin, 10
//     tokens, a failure costs 1, a success refunds 0.1. Below 5 tokens no
//     automatic retry starts; a recovery signal still runs one.
//   - Adaptive throttle (the Google SRE shape), for overload answers only:
//     over a 2-minute window of requests and accepts, once the server has
//     answered 429 or 503, a new attempt is refused locally with probability
//     max(0, (requests - 2*accepts) / (requests + 1)) and counts as a
//     transient failure, so a recovering server sees a trickle, not a wall.

const (
	spaRetryBase        = time.Second
	spaRetryCap         = 30 * time.Second
	spaRetryMaxAttempts = 8
	spaRetryBudget      = 60 * time.Second
	// spaFetchTimeout aborts a request that has not settled: a radio that
	// sleeps mid-request otherwise leaves the fetch (and a hold) pending.
	spaFetchTimeout   = 30 * time.Second
	spaBucketMax      = 10.0
	spaBucketMin      = 5.0
	spaBucketRefund   = 0.1
	spaThrottleWindow = 2 * time.Minute
	spaThrottleK      = 2.0
	// spaThrottleMaxEvents bounds the throttle's window memory.
	spaThrottleMaxEvents = 4096
	// spaReconnectGrace is how long a transient outage stays invisible.
	spaReconnectGrace = 3 * time.Second
	// spaSuspendGap: a timer that fires this much later than it was due, or an
	// attempt that outlives its spaFetchTimeout abort by this much, means the
	// page could not run in between (frozen, suspended, asleep).
	spaSuspendGap = 5 * time.Second
)

// spaFetchTimerSuspended reports whether an attempt's abort timer, armed at
// `armed` for spaFetchTimeout, fired at `now` late enough to show that the page
// was suspended while it waited. The attempt has then not had its running time:
// a request the runtime does not re-send gets a fresh spaFetchTimeout instead of
// a Timeout it did not earn (a re-sent one is aborted and re-sent at once,
// spaRetryCoord.done).
func spaFetchTimerSuspended(armed, now time.Time) bool {
	return now.Sub(armed) >= spaFetchTimeout+spaSuspendGap
}

// spaOutcomeKind classifies one attempt of a request.
type spaOutcomeKind int

const (
	spaOutcomeOK spaOutcomeKind = iota
	spaOutcomeTransient
	spaOutcomeFinal
)

// spaOutcome is the transport-level result of one attempt.
type spaOutcome struct {
	kind          spaOutcomeKind
	status        int // the HTTP status; 0 when no response arrived
	retryAfter    time.Duration
	hasRetryAfter bool
	local         bool // refused locally by the adaptive throttle
}

// spaTransientStatus reports whether an HTTP status asks the client to try
// again later: 408 Request Timeout, 425 Too Early, 429 Too Many Requests,
// 502 Bad Gateway, 503 Service Unavailable, 504 Gateway Timeout.
func spaTransientStatus(status int) bool {
	switch status {
	case 408, 425, 429, 502, 503, 504:
		return true
	}
	return false
}

// spaOverloadStatus reports an overload answer, the only statuses that feed
// the adaptive throttle.
func spaOverloadStatus(status int) bool {
	return status == 429 || status == 503
}

// spaClassifyResponse classifies an attempt that got an HTTP answer.
func spaClassifyResponse(status int, retryAfter string, now time.Time) spaOutcome {
	if !spaTransientStatus(status) {
		if status >= 200 && status < 400 {
			return spaOutcome{kind: spaOutcomeOK, status: status}
		}
		return spaOutcome{kind: spaOutcomeFinal, status: status}
	}
	o := spaOutcome{kind: spaOutcomeTransient, status: status}
	if d, ok := spaParseRetryAfter(retryAfter, now); ok {
		o.retryAfter, o.hasRetryAfter = d, true
	}
	return o
}

// spaNetworkOutcome is an attempt that got no answer (a rejected fetch or a
// timeout).
func spaNetworkOutcome() spaOutcome {
	return spaOutcome{kind: spaOutcomeTransient}
}

// spaParseRetryAfter reads a Retry-After header: delta seconds or an HTTP
// date. A date in the past is a zero wait.
func spaParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	for _, layout := range []string{time.RFC1123, "Monday, 02-Jan-06 15:04:05 MST", time.ANSIC} {
		if t, err := time.Parse(layout, v); err == nil {
			d := t.Sub(now)
			if d < 0 {
				d = 0
			}
			return d, true
		}
	}
	return 0, false
}

// spaFullJitter is the wait before retry number `attempt` (1 for the first
// retry): rnd (in [0,1)) of min(cap, base * 2^(attempt-1)).
func spaFullJitter(attempt int, rnd float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	ceil := spaRetryCap
	if attempt-1 < 30 {
		if c := spaRetryBase << uint(attempt-1); c < ceil {
			ceil = c
		}
	}
	if rnd < 0 {
		rnd = 0
	}
	if rnd >= 1 {
		rnd = math.Nextafter(1, 0)
	}
	return time.Duration(float64(ceil) * rnd)
}

// spaRetryable reports whether a client request may be re-sent by the
// runtime: an auto-split RPC (its request id makes a re-send safe) or a GET
// or HEAD. Any other client request is not retried.
func spaRetryable(method, url string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD":
		return true
	case "POST":
		return strings.HasPrefix(url, "/_rpc/") && strings.Contains(url, "rid=")
	}
	return false
}

// spaOriginOf is the token-bucket key of a URL: its scheme and host, or
// "self" for a relative URL (the app's own origin).
func spaOriginOf(url string) string {
	i := strings.Index(url, "://")
	if i < 0 {
		return "self"
	}
	rest := url[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return strings.ToLower(url[:i] + "://" + rest)
}

// spaThrottleP is the SRE client-side throttling probability.
func spaThrottleP(requests, accepts int) float64 {
	p := (float64(requests) - spaThrottleK*float64(accepts)) / (float64(requests) + 1)
	if p < 0 {
		return 0
	}
	return p
}

// spaIsTransientErr reports whether a delivered Result is an Err of kind
// Network or Timeout: the request could not reach the server. Such an Err only
// reaches the app once the retry budget is spent (or for a client request the
// runtime does not re-send).
func spaIsTransientErr(result SkyResult[SkyADT, any]) bool {
	if result.Tag != 1 {
		return false
	}
	kind := AdtField(result.ErrValue, 0)
	return EnumTagIs(kind, 1) || EnumTagIs(kind, 4) // 1 = Network, 4 = Timeout
}

// spaThrottleEvent is one request in the adaptive throttle's window.
type spaThrottleEvent struct {
	at       time.Time
	accepted bool
	answered bool // a response arrived (accepted or overload)
	overload bool
}

// spaRetryReq is one request the coordinator tracks while it is retried.
type spaRetryReq struct {
	origin  string
	start   time.Time
	attempt int // failed attempts so far
	queued  bool
	forced  bool // a recovery signal: the next try ignores the bucket
	sent    bool // an attempt has been made: only a NEW request is throttled
	giveUp  bool // the budget ran out while it waited
	paced   bool // its current wait is the server's Retry-After
	// attemptAt is when the current attempt started (admit).
	attemptAt time.Time
	// resumed: the page resumed while this request's attempt was on the wire.
	// If that attempt fails, the request is re-sent at once.
	resumed bool
	wake    chan struct{}
}

// spaRetryCoord is the per-client retry coordinator.
type spaRetryCoord struct {
	mu     sync.Mutex
	now    func() time.Time
	rnd    func() float64
	arm    func(time.Duration, func()) func() // a one-shot timer; returns cancel
	wait   func(*spaRetryReq)                 // blocks until the request is woken
	change func()                             // called (unlocked) when the queue changes

	queue []*spaRetryReq
	// outageStart / lastFail: the first and the latest transient failure of the
	// current outage (zero while the queue is empty).
	outageStart time.Time
	lastFail    time.Time
	// graceCancel stops the grace timer of the current outage (nil when none
	// is armed). At spaReconnectGrace into an outage the head is re-sent at
	// once (a probe): its failure proves the outage and shows the indicator,
	// whatever the jittered schedule or the retry bucket would have done.
	graceCancel func()
	// graceProven: at the grace point the head was waiting for the server's
	// Retry-After. The server's "come back later" proves the outage, so the
	// probe does not re-send early and outage() counts up from outageStart.
	graceProven bool
	cancel      func()
	buckets     map[string]float64
	events      []spaThrottleEvent
	// live is every request between begin and end: on the wire or queued. A
	// resume gives each a fresh budget.
	live map[*spaRetryReq]struct{}
	// hidden: the page is hidden (setHidden). No request is given up while it
	// is: the budget counts only time the user could see the page.
	hidden bool
}

func newSpaRetryCoord(now func() time.Time, rnd func() float64, arm func(time.Duration, func()) func()) *spaRetryCoord {
	return &spaRetryCoord{
		now:     now,
		rnd:     rnd,
		arm:     arm,
		wait:    func(r *spaRetryReq) { <-r.wake },
		change:  func() {},
		buckets: map[string]float64{},
		live:    map[*spaRetryReq]struct{}{},
	}
}

// queued is the number of requests waiting to be re-sent.
func (c *spaRetryCoord) queued() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// outage is how long the current outage has been observed: from its first
// transient failure to its latest one (0 when nothing is being re-sent).
func (c *spaRetryCoord) outage() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 || c.outageStart.IsZero() {
		return 0
	}
	if c.graceProven {
		return c.now().Sub(c.outageStart)
	}
	return c.lastFail.Sub(c.outageStart)
}

// tokens is the retry-bucket level of an origin.
func (c *spaRetryCoord) tokens(origin string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokensLocked(origin)
}

func (c *spaRetryCoord) tokensLocked(origin string) float64 {
	t, ok := c.buckets[origin]
	if !ok {
		return spaBucketMax
	}
	return t
}

func (c *spaRetryCoord) begin(origin string) *spaRetryReq {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := &spaRetryReq{origin: origin, start: c.now(), wake: make(chan struct{}, 1)}
	c.live[r] = struct{}{}
	return r
}

// end forgets a request whose result has been delivered.
func (c *spaRetryCoord) end(r *spaRetryReq) {
	c.mu.Lock()
	delete(c.live, r)
	c.mu.Unlock()
}

// spentLocked reports whether r has used its budget. Never while the page is
// hidden: a request waits for the user's return instead of failing unseen.
func (c *spaRetryCoord) spentLocked(r *spaRetryReq, now time.Time) bool {
	if c.hidden {
		return false
	}
	return r.attempt >= spaRetryMaxAttempts || now.Sub(r.start) >= spaRetryBudget
}

// resumeLocked is a resume: the page runs again after a time it could not (a
// return to a visible page, or a gap found by a late timer). Every tracked
// request starts a fresh budget and attempt count; one whose attempt is on the
// wire is re-sent at once if that attempt fails. The current outage is
// re-timed from now, so the indicator's grace starts again: the time the page
// was away is not outage the user watched.
func (c *spaRetryCoord) resumeLocked(now time.Time) {
	for q := range c.live {
		q.attempt = 0
		q.start = now
		q.giveUp = false
		if !q.queued {
			q.resumed = true
		}
	}
	if !c.outageStart.IsZero() {
		c.outageStart, c.lastFail = now, now
		c.graceProven = false
		c.armGraceLocked(now)
	}
}

// setHidden records whether the page is hidden (visibilitychange, freeze,
// pagehide). The return to a visible page is a resume: every tracked request
// starts a fresh budget and the head of the queue is re-sent now. It reports
// whether anything was waiting.
func (c *spaRetryCoord) setHidden(hidden bool) bool {
	c.mu.Lock()
	was := c.hidden
	c.hidden = hidden
	if hidden || !was {
		c.mu.Unlock()
		return false
	}
	c.resumeLocked(c.now())
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return false
	}
	h := c.queue[0]
	h.forced = true
	c.scheduleLocked(h, spaOutcome{kind: spaOutcomeTransient})
	c.mu.Unlock()
	c.change()
	return true
}

// throttleP is the current local-refusal probability: 0 unless an overload
// answer is in the window.
func (c *spaRetryCoord) throttlePLocked(now time.Time) float64 {
	cut := now.Add(-spaThrottleWindow)
	i := 0
	for i < len(c.events) && c.events[i].at.Before(cut) {
		i++
	}
	c.events = c.events[i:]
	req, acc, overloaded := 0, 0, false
	for _, e := range c.events {
		req++
		if e.accepted {
			acc++
		}
		if e.overload {
			overloaded = true
		}
	}
	if !overloaded {
		return 0
	}
	return spaThrottleP(req, acc)
}

func (c *spaRetryCoord) recordLocked(e spaThrottleEvent) {
	c.events = append(c.events, e)
	if len(c.events) > spaThrottleMaxEvents {
		c.events = c.events[len(c.events)-spaThrottleMaxEvents:]
	}
}

// admit decides whether the next attempt goes to the network. false means the
// adaptive throttle refused it locally (the attempt counts as transient).
func (c *spaRetryCoord) admit(r *spaRetryReq) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	r.attemptAt = now
	// The adaptive throttle refuses NEW requests (SRE): a re-send is already
	// bounded by the retry bucket and by the server's Retry-After.
	first := !r.sent
	r.sent = true
	if !first {
		return true
	}
	p := c.throttlePLocked(now)
	if p > 0 && c.rnd() < p {
		c.recordLocked(spaThrottleEvent{at: now})
		return false
	}
	return true
}

// done records an attempt's outcome. It returns true when the request must be
// retried: the caller then waits (c.wait) and asks c.proceed before the next
// attempt. false means deliver this attempt's result now.
func (c *spaRetryCoord) done(r *spaRetryReq, o spaOutcome) bool {
	c.mu.Lock()
	now := c.now()
	// A wake that arrived while this attempt was on the wire (a recovery
	// signal) is spent: the attempt itself was the "try now".
	select {
	case <-r.wake:
	default:
	}
	// An attempt cannot outlive its own abort timer while the page runs: if
	// it did, the page was suspended during it (frozen, asleep, a hidden tab
	// whose timers were held back). That is a resume, found by its effect.
	// (While the page is hidden its timers are throttled, so lateness proves
	// nothing then; the return to a visible page is the resume.)
	if !c.hidden && !r.attemptAt.IsZero() && now.Sub(r.attemptAt) >= spaFetchTimeout+spaSuspendGap {
		c.resumeLocked(now)
	}
	resumed := r.resumed
	r.resumed = false
	if !o.local {
		if o.status > 0 {
			c.recordLocked(spaThrottleEvent{at: now, answered: true,
				accepted: !spaOverloadStatus(o.status), overload: spaOverloadStatus(o.status)})
		}
	}
	retry := false
	switch o.kind {
	case spaOutcomeOK, spaOutcomeFinal:
		// The server answered: the bucket gets its refund.
		t := c.tokensLocked(r.origin) + spaBucketRefund
		if t > spaBucketMax {
			t = spaBucketMax
		}
		c.buckets[r.origin] = t
		c.removeLocked(r, true)
	default:
		if !o.local {
			t := c.tokensLocked(r.origin) - 1
			if t < 0 {
				t = 0
			}
			c.buckets[r.origin] = t
		}
		r.attempt++
		if !o.local {
			if c.outageStart.IsZero() {
				c.outageStart = now
				c.armGraceLocked(now)
			}
			c.lastFail = now
		}
		if c.spentLocked(r, now) {
			c.removeLocked(r, false)
		} else {
			if !r.queued {
				r.queued = true
				c.queue = append(c.queue, r)
			}
			if c.queue[0] == r {
				// The page came back while this attempt was on the wire:
				// the user is waiting, so the re-send goes now.
				r.forced = r.forced || resumed
				c.scheduleLocked(r, o)
			}
			retry = true
		}
	}
	c.mu.Unlock()
	c.change()
	return retry
}

// proceed is called after a woken request's wait: false means the budget ran
// out while it waited, and its last result is delivered as FINAL.
func (c *spaRetryCoord) proceed(r *spaRetryReq) bool {
	c.mu.Lock()
	if !r.giveUp {
		c.mu.Unlock()
		return true
	}
	c.removeLocked(r, false)
	c.mu.Unlock()
	c.change()
	return false
}

// removeLocked takes r out of the queue. When r was the head the next request
// becomes the head: at once after a success (the server is back, so the queue
// drains in order), on its own schedule otherwise.
func (c *spaRetryCoord) removeLocked(r *spaRetryReq, succeeded bool) {
	if !r.queued {
		return
	}
	r.queued = false
	defer func() {
		if len(c.queue) == 0 {
			c.endOutageLocked()
		}
	}()
	wasHead := len(c.queue) > 0 && c.queue[0] == r
	for i, q := range c.queue {
		if q == r {
			c.queue = append(c.queue[:i], c.queue[i+1:]...)
			break
		}
	}
	if !wasHead {
		return
	}
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if len(c.queue) == 0 {
		return
	}
	next := c.queue[0]
	if succeeded {
		next.forced = true
	}
	c.scheduleLocked(next, spaOutcome{kind: spaOutcomeTransient})
}

// armGraceLocked arms the grace probe of the outage that started at start.
//
// Before v0.27.4 the indicator showed only when a re-send that the schedule
// happened to place 3 s or more into the outage failed. Full jitter can place
// the first re-sends close together (waits drawn from [0, 1 s], [0, 2 s], ...),
// and each failure costs a retry token: once the bucket fell below
// spaBucketMin no automatic re-send started until the 60 s budget ran out. A
// real outage of 10 s then never showed "Reconnecting…". The probe ties the
// indicator to the outage's age instead: at spaReconnectGrace the head is
// re-sent now. If it fails the outage is proven and the indicator shows; if it
// succeeds the blip is over, nothing shows, and the queue drains at once.
func (c *spaRetryCoord) armGraceLocked(start time.Time) {
	if c.graceCancel != nil {
		c.graceCancel()
	}
	due := c.now().Add(spaReconnectGrace)
	c.graceCancel = c.arm(spaReconnectGrace, func() {
		c.mu.Lock()
		if !c.outageStart.Equal(start) || len(c.queue) == 0 {
			c.mu.Unlock()
			return // that outage is over
		}
		c.graceCancel = nil
		if now := c.now(); !c.hidden && now.Sub(due) >= spaSuspendGap {
			// The page was suspended past the grace point: a resume. The
			// outage is re-timed (and its grace re-armed) and the head is
			// re-sent now.
			c.resumeLocked(now)
			h := c.queue[0]
			h.forced = true
			c.scheduleLocked(h, spaOutcome{kind: spaOutcomeTransient})
			c.mu.Unlock()
			c.change()
			return
		}
		h := c.queue[0]
		switch {
		case h.giveUp:
			c.mu.Unlock()
			return
		case h.paced:
			// The server asked for this wait: obey it, and show the indicator.
			c.graceProven = true
			c.mu.Unlock()
			c.change()
			return
		}
		h.forced = true
		c.scheduleLocked(h, spaOutcome{kind: spaOutcomeTransient})
		c.mu.Unlock()
	})
}

// endOutageLocked forgets the current outage (the queue drained).
func (c *spaRetryCoord) endOutageLocked() {
	c.outageStart, c.lastFail = time.Time{}, time.Time{}
	c.graceProven = false
	if c.graceCancel != nil {
		c.graceCancel()
		c.graceCancel = nil
	}
}

// scheduleLocked arms the head's next try.
func (c *spaRetryCoord) scheduleLocked(h *spaRetryReq, last spaOutcome) {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	now := c.now()
	h.paced = false
	if h.forced {
		h.forced = false
		c.wakeLocked(h)
		return
	}
	remaining := h.start.Add(spaRetryBudget).Sub(now)
	if remaining <= 0 && !c.hidden {
		h.giveUp = true
		c.wakeLocked(h)
		return
	}
	var delay time.Duration
	switch {
	case c.tokensLocked(h.origin) < spaBucketMin:
		// Retries are suspended: only a recovery signal (or the end of the
		// budget) moves this request on.
		if c.hidden {
			return // no budget runs out while hidden: the return re-sends it
		}
		delay = remaining
	case last.hasRetryAfter:
		delay = last.retryAfter
		h.paced = true
	default:
		delay = spaFullJitter(h.attempt, c.rnd())
	}
	giveUp := false
	if delay >= remaining && !c.hidden {
		delay, giveUp = remaining, true
	}
	due := now.Add(delay)
	c.cancel = c.arm(delay, func() {
		c.mu.Lock()
		if len(c.queue) > 0 && c.queue[0] == h {
			if now := c.now(); !c.hidden && now.Sub(due) >= spaSuspendGap {
				// The timer fired long after it was due: the page was
				// suspended. A resume, not the end of the budget. (A hidden
				// page's timers are throttled; its resume is setHidden.)
				c.resumeLocked(now)
				giveUp = false
			}
			h.giveUp = h.giveUp || (giveUp && !c.hidden)
			h.paced = false
			c.cancel = nil
			c.wakeLocked(h)
		}
		c.mu.Unlock()
	})
}

func (c *spaRetryCoord) wakeLocked(r *spaRetryReq) {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// recover is a recovery signal (online, visible, pageshow, focus): every
// tracked request (queued, or on the wire) starts a fresh budget and the head
// is re-sent now, whatever the bucket holds. It reports whether anything was waiting.
func (c *spaRetryCoord) recover() bool {
	c.mu.Lock()
	if len(c.queue) == 0 {
		c.mu.Unlock()
		return false
	}
	now := c.now()
	for q := range c.live {
		q.attempt = 0
		q.start = now
		q.giveUp = false
	}
	h := c.queue[0]
	h.forced = true
	c.scheduleLocked(h, spaOutcome{kind: spaOutcomeTransient})
	c.mu.Unlock()
	c.change()
	return true
}

// spaRetryLoop runs one request through the coordinator: attempt, classify,
// wait, re-send, until it succeeds, fails FINAL, or spends its budget. It
// returns the result of the last attempt. `refused` is the result delivered
// for an attempt the adaptive throttle refused locally.
func spaRetryLoop[R any](c *spaRetryCoord, origin string, attempt func() (R, spaOutcome), refused func() R) R {
	r := c.begin(origin)
	defer c.end(r)
	for {
		var res R
		var o spaOutcome
		if c.admit(r) {
			res, o = attempt()
		} else {
			res, o = refused(), spaOutcome{kind: spaOutcomeTransient, local: true}
		}
		if !c.done(r, o) {
			return res
		}
		c.wait(r)
		if !c.proceed(r) {
			return res
		}
	}
}

// Connection states, as Sub.connection reports them (sub_connection.go).
const (
	spaConnOnline       = 0
	spaConnReconnecting = 1
	spaConnOffline      = 2
)

// spaConnState is the client's connection state from the retry queue (requests
// the runtime is still re-sending) and the red bar's queue (requests whose
// budget is spent, waiting for Retry or a recovery signal).
func spaConnState(retrying, exhausted int) (code, pending int) {
	switch {
	case exhausted > 0:
		return spaConnOffline, exhausted + retrying
	case retrying > 0:
		return spaConnReconnecting, 0
	}
	return spaConnOnline, 0
}

// spaIndicator is the quiet "Reconnecting…" indicator. It shows only once an
// outage is PROVEN longer than spaReconnectGrace: a request failed, and a
// re-send at least spaReconnectGrace later failed too. The coordinator makes
// that re-send at spaReconnectGrace (armGraceLocked), so a real outage shows
// at once at the grace point, and a blip that is over by then shows nothing.
// It hides as soon as the client is not Reconnecting (Online, or Offline,
// where the red bar replaces it). It does not block taps.
type spaIndicator struct {
	show  func()
	hide  func()
	shown bool
}

// update takes the connection state and how long the current outage has been
// observed (its first failure to its latest failure).
func (in *spaIndicator) update(code int, outage time.Duration) {
	if code == spaConnReconnecting {
		if !in.shown && outage >= spaReconnectGrace {
			in.shown = true
			in.show()
		}
		return
	}
	if in.shown {
		in.shown = false
		in.hide()
	}
}
