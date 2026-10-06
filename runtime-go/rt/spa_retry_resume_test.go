package rt

import (
	"testing"
	"time"
)

// v0.27.6 regression: a Sky.Spa client resumed after the page was frozen,
// hidden or closed (a phone app switch, a laptop lid, a tab restored from the
// back/forward cache) showed the app's App.withRpcError message for an RPC
// that was in flight when the page stopped. The retry budget (60 s) and the
// fetch timeout (30 s) were measured on the wall clock, which kept running
// while no JavaScript could. On resume the attempt that had hung across the
// gap settled at once (its abort timer was overdue, or the dead connection
// failed), the coordinator found the request already 60 s "old", and it
// delivered the failure as FINAL without one re-send. These tests model the
// gap as a jump of the injected clock with no timer firing during it.

// spaFreezeGap is how long the page is frozen in these tests.
const spaFreezeGap = 2 * time.Minute

// A request in flight when the page froze, whose attempt fails on resume (the
// abort timer is overdue, or the connection died), is re-sent with a fresh
// budget. It never reaches the app as FINAL.
func TestSpaResume_InFlightAcrossFreezeIsResent(t *testing.T) {
	for _, name := range []string{"abort timer overdue", "connection lost"} {
		t.Run(name, func(t *testing.T) {
			clk := newSpaFakeClock()
			c := newSpaTestCoord(t, clk, 0.5)
			attempts := 0
			res := spaRetryLoop(c, "self", func() (string, spaOutcome) {
				attempts++
				if attempts == 1 {
					clk.t = clk.t.Add(spaFreezeGap) // frozen while this attempt was on the wire
					return "Err Timeout", spaNetworkOutcome()
				}
				return "ok", spaOutcome{kind: spaOutcomeOK, status: 200}
			}, func() string { return "refused" })
			if res != "ok" || attempts != 2 {
				t.Fatalf("an attempt that spanned a freeze is re-sent, not delivered FINAL: got %q after %d attempts", res, attempts)
			}
		})
	}
}

// The re-send after a freeze is immediate: the user is back and waiting.
func TestSpaResume_ResendAfterFreezeIsImmediate(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.999)
	attempts := 0
	var resumedAt, resentAt time.Time
	spaRetryLoop(c, "self", func() (int, spaOutcome) {
		attempts++
		if attempts == 1 {
			clk.t = clk.t.Add(spaFreezeGap)
			resumedAt = clk.t
			return 0, spaNetworkOutcome()
		}
		resentAt = clk.t
		return 200, spaOutcome{kind: spaOutcomeOK, status: 200}
	}, func() int { return -1 })
	if !resentAt.Equal(resumedAt) {
		t.Fatalf("the request is re-sent at once on resume, waited %v", resentAt.Sub(resumedAt))
	}
}

// The radio is not up yet on resume: the first re-sends fail too. The request
// still has a whole budget of running time, measured from the resume.
func TestSpaResume_FailuresRightAfterResumeGetAFreshBudget(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.5)
	attempts := 0
	var resumedAt time.Time
	res := spaRetryLoop(c, "self", func() (string, spaOutcome) {
		attempts++
		switch {
		case attempts == 1:
			clk.t = clk.t.Add(spaFreezeGap)
			resumedAt = clk.t
			return "lost", spaNetworkOutcome()
		case clk.t.Sub(resumedAt) < 5*time.Second:
			return "radio still down", spaNetworkOutcome()
		}
		return "ok", spaOutcome{kind: spaOutcomeOK, status: 200}
	}, func() string { return "refused" })
	if res != "ok" {
		t.Fatalf("a 5 s outage right after resume is retried through, got %q after %d attempts", res, attempts)
	}
}

// A request that failed before the freeze waits for its re-send timer. The
// timer fires long after it was due (the page was frozen): that is a resume,
// not the end of the budget, and a failure right after it is not FINAL.
func TestSpaResume_WaitingRequestAcrossFreeze(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.5)
	attempts := 0
	frozen := false
	c.wait = func(r *spaRetryReq) {
		if !frozen {
			frozen = true
			clk.t = clk.t.Add(spaFreezeGap) // the page froze while this request waited
		}
		for len(r.wake) == 0 {
			if !clk.fireNext() {
				t.Fatalf("a waiting request has no timer to wake it")
			}
		}
		<-r.wake
	}
	res := spaRetryLoop(c, "self", func() (string, spaOutcome) {
		attempts++
		if attempts <= 2 { // before the freeze, and the first try on resume
			return "lost", spaNetworkOutcome()
		}
		return "ok", spaOutcome{kind: spaOutcomeOK, status: 200}
	}, func() string { return "refused" })
	if res != "ok" {
		t.Fatalf("a request waiting across a freeze keeps a budget for the resume, got %q after %d attempts", res, attempts)
	}
}

// A request whose wait was the whole remaining budget (the retry bucket was
// low) must not be given up when that timer fires late because the page was
// frozen.
func TestSpaResume_BudgetTimerFiredLateIsNotGiveUp(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0)
	c.buckets["self"] = spaBucketMin - 1 // retries suspended: the head waits for the budget's end
	r := c.begin("self")
	c.admit(r)
	if !c.done(r, spaNetworkOutcome()) {
		t.Fatal("a first transient failure is retried")
	}
	// The grace probe re-sends it at 3 s; that fails too, and the head now
	// waits for the end of its budget.
	for len(r.wake) == 0 {
		if !clk.fireNext() {
			t.Fatal("no grace timer")
		}
	}
	<-r.wake
	if !c.proceed(r) {
		t.Fatal("the grace probe proceeds")
	}
	c.admit(r)
	if !c.done(r, spaNetworkOutcome()) {
		t.Fatal("the probe's failure is retried")
	}
	clk.t = clk.t.Add(spaFreezeGap)
	for len(r.wake) == 0 {
		if !clk.fireNext() {
			t.Fatal("no timer")
		}
	}
	<-r.wake
	if !c.proceed(r) {
		t.Fatal("a budget timer that fired late (the page was frozen) is a resume, not the end of the budget")
	}
}

// A request in flight while the page is visible and the network is merely
// slow is unaffected: an attempt that settles within the fetch timeout keeps
// its budget, and the 60 s budget still ends a real outage.
func TestSpaResume_RealOutageStillEndsAfterTheBudget(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.999)
	start := clk.now()
	spaRetryLoop(c, "self", func() (int, spaOutcome) {
		clk.t = clk.t.Add(spaFetchTimeout) // every attempt hangs and is aborted on time
		return 0, spaNetworkOutcome()
	}, func() int { return -1 })
	if el := clk.now().Sub(start); el > spaRetryBudget+spaFetchTimeout {
		t.Fatalf("a real outage on a visible page still ends after the budget, ran %v", el)
	}
}

// While the page is hidden no request is given up, however long the outage
// runs: the user cannot see the result. The return to a visible page gives it
// a fresh budget and re-sends it at once.
func TestSpaResume_HiddenPageNeverGivesUp(t *testing.T) {
	clk := newSpaFakeClock()
	c := newSpaTestCoord(t, clk, 0.999)
	c.setHidden(true)
	var shownAt time.Time
	c.wait = func(r *spaRetryReq) {
		for len(r.wake) == 0 {
			if !clk.fireNext() {
				// Nothing armed: the request waits for the user's return.
				clk.t = clk.t.Add(5 * time.Minute)
				shownAt = clk.t
				if !c.setHidden(false) {
					t.Fatal("the return to a visible page finds the waiting request")
				}
			}
		}
		<-r.wake
	}
	start := clk.now()
	var resentAt time.Time
	res := spaRetryLoop(c, "self", func() (string, spaOutcome) {
		if shownAt.IsZero() {
			clk.t = clk.t.Add(10 * time.Second) // each hidden attempt hangs 10 s, then fails
			return "lost", spaNetworkOutcome()
		}
		resentAt = clk.t
		return "ok", spaOutcome{kind: spaOutcomeOK, status: 200}
	}, func() string { return "refused" })
	if res != "ok" {
		t.Fatalf("a request is never given up while the page is hidden, got %q", res)
	}
	if shownAt.IsZero() || shownAt.Sub(start) < spaRetryBudget {
		t.Fatalf("the test must hide the page for longer than the budget (hidden %v)", shownAt.Sub(start))
	}
	if !resentAt.Equal(shownAt) {
		t.Fatalf("the return to a visible page re-sends at once, waited %v", resentAt.Sub(shownAt))
	}
}

// The fetch abort timer: on time it aborts a hang; fired after a suspension it
// says so, and a request the runtime does not re-send gets a fresh timeout.
func TestSpaResume_FetchTimerSuspended(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	if spaFetchTimerSuspended(t0, t0.Add(spaFetchTimeout)) ||
		spaFetchTimerSuspended(t0, t0.Add(spaFetchTimeout+time.Second)) {
		t.Fatal("a timer that fires on time (or a little late) is a real hang")
	}
	if !spaFetchTimerSuspended(t0, t0.Add(spaFetchTimeout+spaFreezeGap)) {
		t.Fatal("a timer that fires minutes late was held by a suspension")
	}
}
