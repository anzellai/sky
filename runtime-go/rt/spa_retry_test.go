package rt

import (
	"sort"
	"testing"
	"time"
)

// v0.27.3 regression: one transient fetch failure (an installed web app
// switched away on a phone, a laptop after sleep) stopped the Sky.Spa client:
// the red bar showed at once, App.withRpcError got the failure at once, a
// hold RPC froze every later Msg, and only the Retry button moved it on. The
// transport now retries transient failures itself (spa_retry.go). These tests
// drive the coordinator with an injected clock, random source and timer; no
// test sleeps.

type spaFakeTimer struct {
	at   time.Time
	f    func()
	dead bool
}

type spaFakeClock struct {
	t      time.Time
	timers []*spaFakeTimer
}

func newSpaFakeClock() *spaFakeClock {
	return &spaFakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *spaFakeClock) now() time.Time { return c.t }

func (c *spaFakeClock) arm(d time.Duration, f func()) func() {
	tm := &spaFakeTimer{at: c.t.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return func() { tm.dead = true }
}

// fireNext advances the clock to the earliest live timer and runs it.
func (c *spaFakeClock) fireNext() bool {
	live := []*spaFakeTimer{}
	for _, tm := range c.timers {
		if !tm.dead {
			live = append(live, tm)
		}
	}
	if len(live) == 0 {
		return false
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].at.Before(live[j].at) })
	tm := live[0]
	tm.dead = true
	if tm.at.After(c.t) {
		c.t = tm.at
	}
	tm.f()
	return true
}

// newSpaTestCoord builds a coordinator whose wait runs fake timers until the
// request is woken. rnd is fixed so delays are exact.
func newSpaTestCoord(t *testing.T, clk *spaFakeClock, rnd float64) *spaRetryCoord {
	t.Helper()
	c := newSpaRetryCoord(clk.now, func() float64 { return rnd }, clk.arm)
	c.wait = func(r *spaRetryReq) {
		for len(r.wake) == 0 {
			if !clk.fireNext() {
				t.Fatalf("a waiting request has no timer to wake it")
			}
		}
		<-r.wake
	}
	return c
}

func TestSpaRetry_ClassificationTable(t *testing.T) {
	now := time.Now()
	for _, s := range []int{408, 425, 429, 502, 503, 504} {
		if o := spaClassifyResponse(s, "", now); o.kind != spaOutcomeTransient {
			t.Errorf("status %d must be TRANSIENT, got %v", s, o.kind)
		}
	}
	for _, s := range []int{400, 401, 403, 404, 409, 422, 500, 501} {
		if o := spaClassifyResponse(s, "", now); o.kind != spaOutcomeFinal {
			t.Errorf("status %d must be FINAL, got %v", s, o.kind)
		}
	}
	for _, s := range []int{200, 204, 304} {
		if o := spaClassifyResponse(s, "", now); o.kind != spaOutcomeOK {
			t.Errorf("status %d must be OK, got %v", s, o.kind)
		}
	}
	if spaNetworkOutcome().kind != spaOutcomeTransient {
		t.Error("no answer (network / timeout) must be TRANSIENT")
	}
	net, _ := ErrNetwork("x").(SkyADT)
	to, _ := ErrTimeout().(SkyADT)
	dec, _ := ErrDecode("x").(SkyADT)
	if !spaIsTransientErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: net}) ||
		!spaIsTransientErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: to}) {
		t.Error("Network and Timeout Errs are the transient class")
	}
	if spaIsTransientErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: dec}) {
		t.Error("a Decode Err is FINAL")
	}
	if !spaIsNetworkErr(SkyResult[SkyADT, any]{Tag: 1, ErrValue: to}) {
		t.Error("a Timeout (an aborted hang) shows the red bar once exhausted, like Network")
	}
}

func TestSpaRetry_WhichRequestsAreResent(t *testing.T) {
	cases := []struct {
		method, url string
		want        bool
	}{
		{"GET", "/api/items", true},
		{"HEAD", "/api/items", true},
		{"POST", "/_rpc/Save?rid=abc-1", true},
		{"POST", "/_rpc/__spaSignOut", false}, // no request id: not safe
		{"POST", "/api/items", false},
		{"PUT", "/api/items", false},
	}
	for _, c := range cases {
		if got := spaRetryable(c.method, c.url); got != c.want {
			t.Errorf("spaRetryable(%s %s) = %v, want %v", c.method, c.url, got, c.want)
		}
	}
	if spaOriginOf("/_rpc/X?rid=1") != "self" || spaOriginOf("https://API.example.test/a") != "https://api.example.test" {
		t.Error("origin keys: relative URL is self, absolute is scheme://host")
	}
}

func TestSpaRetry_FullJitterBounds(t *testing.T) {
	for attempt := 1; attempt <= 12; attempt++ {
		ceil := spaRetryBase << uint(attempt-1)
		if ceil > spaRetryCap {
			ceil = spaRetryCap
		}
		if d := spaFullJitter(attempt, 0); d != 0 {
			t.Errorf("attempt %d: rnd 0 must wait 0, got %v", attempt, d)
		}
		if d := spaFullJitter(attempt, 0.999999); d > ceil || d < ceil*99/100 {
			t.Errorf("attempt %d: rnd ~1 must wait just under %v, got %v", attempt, ceil, d)
		}
		if d := spaFullJitter(attempt, 0.5); d != ceil/2 {
			t.Errorf("attempt %d: rnd 0.5 must wait %v, got %v", attempt, ceil/2, d)
		}
	}
	if spaFullJitter(40, 1) > spaRetryCap {
		t.Error("the cap holds for a large attempt count")
	}
}

func TestSpaRetry_RetryAfterWins(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if d, ok := spaParseRetryAfter("5", now); !ok || d != 5*time.Second {
		t.Errorf("Retry-After seconds: got %v %v", d, ok)
	}
	if d, ok := spaParseRetryAfter("Sun, 04 Oct 2026 12:00:07 GMT", now); !ok || d != 7*time.Second {
		t.Errorf("Retry-After HTTP date: got %v %v", d, ok)
	}
	if _, ok := spaParseRetryAfter("soon", now); ok {
		t.Error("an unreadable Retry-After is ignored")
	}

	// The coordinator waits exactly the server's delay, not the jitter.
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.5)
	var sentAt []time.Time
	attempts := 0
	res := spaRetryLoop(c, "self", func() (int, spaOutcome) {
		attempts++
		sentAt = append(sentAt, clk.now())
		if attempts == 1 {
			return 503, spaClassifyResponse(503, "5", clk.now())
		}
		return 200, spaClassifyResponse(200, "", clk.now())
	}, func() int { return -1 })
	if res != 200 || attempts != 2 {
		t.Fatalf("a 503 is retried and the retry's answer delivered: res=%d attempts=%d", res, attempts)
	}
	if gap := sentAt[1].Sub(sentAt[0]); gap != 5*time.Second {
		t.Fatalf("Retry-After: 5 must set the delay exactly, waited %v", gap)
	}
}

// Report only after exhaustion: the caller sees nothing until the budget is
// spent, then the LAST transient result, once.
func TestSpaRetry_GivesUpAfter8Attempts(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.01) // short waits: the attempt cap binds first
	attempts := 0
	res := spaRetryLoop(c, "self", func() (int, spaOutcome) {
		attempts++
		// Keep the retry bucket full (another request's successes refund it), so
		// the attempt cap is what ends this request.
		c.mu.Lock()
		c.buckets["self"] = spaBucketMax
		c.mu.Unlock()
		return attempts, spaNetworkOutcome()
	}, func() int { return -1 })
	if attempts != spaRetryMaxAttempts {
		t.Fatalf("a request gives up after %d attempts, made %d", spaRetryMaxAttempts, attempts)
	}
	if res != spaRetryMaxAttempts {
		t.Fatalf("the last attempt's result is delivered, got %d", res)
	}
	if c.queued() != 0 {
		t.Fatal("a request that gave up leaves the queue")
	}
}

func TestSpaRetry_GivesUpAfter60Seconds(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.999) // long waits: the time budget binds first
	start := clk.now()
	attempts := 0
	spaRetryLoop(c, "self", func() (int, spaOutcome) {
		attempts++
		return 0, spaNetworkOutcome()
	}, func() int { return -1 })
	if attempts >= spaRetryMaxAttempts {
		t.Fatalf("the 60 s budget must end the request before 8 attempts, made %d", attempts)
	}
	if el := clk.now().Sub(start); el > spaRetryBudget {
		t.Fatalf("gave up after %v, over the %v budget", el, spaRetryBudget)
	}
}

// A blip: one failure, then success. The caller only ever sees the success.
func TestSpaRetry_BlipIsInvisible(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.3)
	changes := 0
	c.change = func() { changes++ }
	attempts := 0
	res := spaRetryLoop(c, "self", func() (string, spaOutcome) {
		attempts++
		if attempts == 1 {
			return "network error", spaNetworkOutcome()
		}
		return "ok", spaOutcome{kind: spaOutcomeOK, status: 200}
	}, func() string { return "refused" })
	if res != "ok" {
		t.Fatalf("a blip must deliver the retry's success, got %q", res)
	}
	if clk.now().Sub(newSpaFakeClock().now()) >= spaReconnectGrace {
		t.Fatal("a first retry lands inside the indicator's grace period")
	}
}

func TestSpaRetry_TokenBucket(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0)
	r := c.begin("self")
	for i := 0; i < 5; i++ {
		c.done(r, spaNetworkOutcome())
	}
	if got := c.tokens("self"); got != 5 {
		t.Fatalf("five failures cost five tokens: got %v", got)
	}
	// At 5 tokens a retry still starts (suspended only BELOW 5): a timer is armed
	// for the jitter, not the end of the budget.
	clk.timers = nil
	c.done(r, spaNetworkOutcome()) // tokens 4: suspended
	if got := c.tokens("self"); got != 4 {
		t.Fatalf("got %v tokens", got)
	}
	if len(clk.timers) != 1 || clk.timers[0].at.Sub(clk.now()) != spaRetryBudget-clk.now().Sub(r.start) {
		t.Fatal("below 5 tokens no automatic retry starts: the head waits for a recovery signal or the budget's end")
	}
	// A recovery signal still runs one try.
	if !c.recover() || len(r.wake) != 1 {
		t.Fatal("a recovery signal wakes the suspended head at once")
	}
	// A success refunds 0.1.
	c.done(r, spaOutcome{kind: spaOutcomeOK, status: 200})
	if got := c.tokens("self"); got < 4.09 || got > 4.11 {
		t.Fatalf("a success refunds 0.1: got %v", got)
	}
	// Buckets are per origin.
	if c.tokens("https://other.example.test") != spaBucketMax {
		t.Fatal("another origin has its own full bucket")
	}
}

func TestSpaRetry_AdaptiveThrottle(t *testing.T) {
	if spaThrottleP(10, 10) != 0 || spaThrottleP(10, 5) != 0 {
		t.Error("no refusal while accepts keep up with requests (K = 2)")
	}
	if got, want := spaThrottleP(100, 10), (100.0-20.0)/101.0; got != want {
		t.Errorf("p(100, 10) = %v, want %v", got, want)
	}
	if spaThrottleP(1, 0) != 0.5 {
		t.Error("p(1, 0) = 0.5")
	}

	// Without an overload answer in the window nothing is refused, however many
	// network failures there were.
	clk := newSpaFakeClock()
	c := newSpaRetryCoord(clk.now, func() float64 { return 0 }, clk.arm)
	r := c.begin("self")
	for i := 0; i < 20; i++ {
		c.admit(r)
		c.done(r, spaNetworkOutcome())
	}
	if !c.admit(c.begin("self")) {
		t.Fatal("network failures alone never trigger the overload throttle")
	}
	// Overload answers: requests outrun 2 x accepts, so a draw below p refuses.
	c2 := newSpaRetryCoord(clk.now, func() float64 { return 0 }, clk.arm)
	r2 := c2.begin("self")
	for i := 0; i < 10; i++ {
		c2.done(r2, spaClassifyResponse(503, "", clk.now()))
	}
	if c2.admit(r2) {
		t.Fatal("after ten 503s a request is refused locally")
	}
	// The window is two minutes: later the throttle forgets.
	clk.t = clk.t.Add(spaThrottleWindow + time.Second)
	if !c2.admit(c2.begin("self")) {
		t.Fatal("overload answers older than the window no longer throttle")
	}
}

// Failed requests are re-sent one at a time, in the order they failed, each
// once, and a recovery signal re-sends the head at once with a fresh budget.
func TestSpaRetry_ReplayOrderOnRecovery(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.9)
	a, b, d := c.begin("self"), c.begin("self"), c.begin("self")
	for _, r := range []*spaRetryReq{a, b, d} {
		if !c.done(r, spaNetworkOutcome()) {
			t.Fatal("a first transient failure is retried")
		}
	}
	if c.queued() != 3 {
		t.Fatalf("three requests wait, got %d", c.queued())
	}
	if len(b.wake) != 0 || len(d.wake) != 0 {
		t.Fatal("only the head may be retried: one retry in flight")
	}
	a.attempt = 5
	if !c.recover() {
		t.Fatal("a recovery signal finds the queue")
	}
	if len(a.wake) != 1 || a.attempt != 0 {
		t.Fatal("recovery re-sends the head now and resets its attempt count")
	}
	<-a.wake
	var order []*spaRetryReq
	for _, r := range []*spaRetryReq{a, b, d} {
		if r != a {
			if len(r.wake) != 1 {
				t.Fatalf("after the head succeeds the next is re-sent at once")
			}
			<-r.wake
		}
		if !c.proceed(r) {
			t.Fatal("a woken request within budget proceeds")
		}
		order = append(order, r)
		if c.done(r, spaOutcome{kind: spaOutcomeOK, status: 200}) {
			t.Fatal("a success is delivered, not retried")
		}
	}
	if order[0] != a || order[1] != b || order[2] != d || c.queued() != 0 {
		t.Fatal("the queue drains in failure order, each request once")
	}
}

func TestSpaRetry_ConnectionStateAndIndicator(t *testing.T) {
	if code, _ := spaConnState(0, 0); code != spaConnOnline {
		t.Error("nothing pending is Online")
	}
	if code, _ := spaConnState(2, 0); code != spaConnReconnecting {
		t.Error("requests being re-sent is Reconnecting")
	}
	if code, p := spaConnState(1, 2); code != spaConnOffline || p != 3 {
		t.Errorf("a spent budget is Offline with every pending request, got %d %d", code, p)
	}

	shown, hidden := 0, 0
	in := &spaIndicator{show: func() { shown++ }, hide: func() { hidden++ }}
	// A blip: failures within the grace period show nothing, even if the
	// jittered re-send that succeeds comes later than 3 s.
	in.update(spaConnReconnecting, 0)
	in.update(spaConnReconnecting, 2*time.Second)
	in.update(spaConnOnline, 0)
	if shown != 0 {
		t.Fatal("an outage not proven longer than 3 s shows nothing")
	}
	// A failure observed 3 s or more into the outage shows the indicator.
	in.update(spaConnReconnecting, 0)
	in.update(spaConnReconnecting, spaReconnectGrace)
	if shown != 1 {
		t.Fatalf("a proven 3 s outage shows the indicator (shown=%d)", shown)
	}
	in.update(spaConnOnline, 0)
	if hidden != 1 {
		t.Fatal("recovery clears the indicator")
	}
	// Offline (budget spent) replaces the pill with the red bar.
	in.update(spaConnReconnecting, 5*time.Second)
	in.update(spaConnOffline, 0)
	if hidden != 2 {
		t.Fatal("the red bar replaces the indicator")
	}

	// The coordinator measures the outage from its first to its latest failure.
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.5)
	r := c.begin("self")
	c.done(r, spaNetworkOutcome())
	clk.t = clk.t.Add(3500 * time.Millisecond)
	if c.outage() != 0 {
		t.Fatal("time alone proves nothing: the blip may already be over")
	}
	c.done(r, spaNetworkOutcome())
	if c.outage() != 3500*time.Millisecond {
		t.Fatalf("outage = %v", c.outage())
	}
	c.done(r, spaOutcome{kind: spaOutcomeOK, status: 200})
	if c.outage() != 0 {
		t.Fatal("a drained queue ends the outage")
	}
}

// The adaptive throttle refuses NEW requests only: a re-send the server paced
// with Retry-After is not refused locally.
func TestSpaRetry_ThrottleSparesResends(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0) // every draw is below p
	attempts := 0
	res := spaRetryLoop(c, "self", func() (int, spaOutcome) {
		attempts++
		if attempts == 1 {
			return 503, spaClassifyResponse(503, "2", clk.now())
		}
		return 200, spaClassifyResponse(200, "", clk.now())
	}, func() int { return -1 })
	if res != 200 || attempts != 2 {
		t.Fatalf("the paced re-send goes out: res=%d attempts=%d", res, attempts)
	}
	r := c.begin("self")
	c.done(r, spaClassifyResponse(503, "", clk.now()))
	if c.admit(c.begin("self")) {
		t.Fatal("a new request is refused while 503s outnumber accepts")
	}
}

// v0.27.4 regression (CI, gate-web: the offline e2e saw no "Reconnecting…"
// in 10 s offline): the indicator showed only when a re-send that the jittered
// schedule placed 3 s or more into the outage failed. Re-sends drawn close
// together spend the retry bucket (six quick failures take it from 10 to 4),
// and below spaBucketMin no automatic re-send
// starts until the 60 s budget ends. The outage was real, the user saw
// nothing, then the red bar. The grace probe re-sends the head at
// spaReconnectGrace, so the indicator shows at 3 s whatever the schedule.
func TestSpaRetry_IndicatorShowsAtTheGraceWhateverTheSchedule(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0) // every jittered wait is 0: the re-sends bunch at the start
	t0 := clk.now()
	var shownAt time.Duration = -1
	in := &spaIndicator{show: func() { shownAt = clk.now().Sub(t0) }, hide: func() {}}
	c.change = func() {
		code, _ := spaConnState(c.queued(), 0)
		in.update(code, c.outage())
	}
	attempts := 0
	spaRetryLoop(c, "self", func() (int, spaOutcome) {
		attempts++
		if shownAt >= 0 && clk.now().Sub(t0) > spaReconnectGrace {
			return 200, spaOutcome{kind: spaOutcomeOK, status: 200}
		}
		return 0, spaNetworkOutcome()
	}, func() int { return -1 })
	if shownAt != spaReconnectGrace {
		t.Fatalf("a real outage shows the indicator at the %v grace, shown at %v (-1: never) after %d attempts",
			spaReconnectGrace, shownAt, attempts)
	}
}

// The probe does not turn a blip into an indicator: an outage that is over
// before the grace point shows nothing, and the probe's success drains the
// queue at once instead of after a long jittered wait.
func TestSpaRetry_GraceProbeShowsNothingForABlip(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.9) // re-sends at 0.9 s, 2.7 s, then 6.3 s
	t0 := clk.now()
	shown := false
	in := &spaIndicator{show: func() { shown = true }, hide: func() {}}
	c.change = func() {
		code, _ := spaConnState(c.queued(), 0)
		in.update(code, c.outage())
	}
	var okAt time.Duration
	spaRetryLoop(c, "self", func() (int, spaOutcome) {
		if clk.now().Sub(t0) < 2800*time.Millisecond {
			return 0, spaNetworkOutcome()
		}
		okAt = clk.now().Sub(t0)
		return 200, spaOutcome{kind: spaOutcomeOK, status: 200}
	}, func() int { return -1 })
	if shown {
		t.Fatal("a 2.8 s blip shows nothing")
	}
	if okAt != spaReconnectGrace {
		t.Fatalf("the grace probe re-sends at %v (the schedule said 6.3 s), got %v", spaReconnectGrace, okAt)
	}
	if c.queued() != 0 || c.outage() != 0 {
		t.Fatal("the queue drained and the outage ended")
	}
}

// A server that paced the client with Retry-After is obeyed: the grace probe
// does not re-send early. Its "come back later" already proves the outage,
// so the indicator shows at the grace point all the same.
func TestSpaRetry_GraceObeysRetryAfterAndStillShows(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.5)
	t0 := clk.now()
	var shownAt time.Duration = -1
	in := &spaIndicator{show: func() { shownAt = clk.now().Sub(t0) }, hide: func() {}}
	c.change = func() {
		code, _ := spaConnState(c.queued(), 0)
		in.update(code, c.outage())
	}
	var sentAt []time.Duration
	spaRetryLoop(c, "self", func() (int, spaOutcome) {
		sentAt = append(sentAt, clk.now().Sub(t0))
		if len(sentAt) == 1 {
			return 503, spaClassifyResponse(503, "10", clk.now())
		}
		return 200, spaClassifyResponse(200, "", clk.now())
	}, func() int { return -1 })
	if len(sentAt) != 2 || sentAt[1] != 10*time.Second {
		t.Fatalf("the re-send waits for the server's Retry-After: sent at %v", sentAt)
	}
	if shownAt != spaReconnectGrace {
		t.Fatalf("a paced outage shows the indicator at the grace, shown at %v", shownAt)
	}
}
