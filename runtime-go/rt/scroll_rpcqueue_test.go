package rt

import (
	"reflect"
	"testing"
)

func scrollSchedulerFixture() (*spaSched, *spaScrollQueue) {
	s := newSpaSched("scroll")
	q := &spaScrollQueue{}
	s.domEpoch = func() uint64 { return q.epoch }
	s.scrollAuthority = func() uint64 { return q.authority }
	s.awaitDOM = q.afterRender
	return s, q
}

func TestScrollRPCHoldWaitsForActualDOMBeforeNext(t *testing.T) {
	s, q := scrollSchedulerFixture()
	var order []string
	apply := func(c scrollCmd) { order = append(order, "scroll:"+c.Target) }
	run := func(msg any) {
		name := msg.(string)
		order = append(order, name)
		if name == "Applied" {
			q.committed(true, false, apply)
			q.command(scrollCmd{Target: "same-id"}, apply)
		}
	}
	j := s.issueWithDOM(nil, nil, true, true)
	s.dispatch("Next", run)
	if len(order) != 0 {
		t.Fatal("Next overtook server answer")
	}
	s.settle(j, "Applied", run)
	if !s.held() || !reflect.DeepEqual(order, []string{"Applied"}) {
		t.Fatalf("DOM barrier: %v", order)
	}
	q.committed(false, true, apply)
	// Driver drains only AFTER its render guard, not re-entrantly mid-paint.
	if s.held() || !reflect.DeepEqual(order, []string{"Applied", "scroll:same-id"}) {
		t.Fatalf("commit order: %v", order)
	}
	s.drain(run)
	if !reflect.DeepEqual(order, []string{"Applied", "scroll:same-id", "Next"}) {
		t.Fatalf("Next order: %v", order)
	}
}

func TestScrollRPCHoldDisablesHoldFreeExemption(t *testing.T) {
	s, _ := scrollSchedulerFixture()
	s.setHoldFree([]string{"Server Input"})
	j := s.issueWithDOM(nil, nil, true, true)
	j.name = "Server"
	if s.holdFreeNow(SkyADT{SkyName: "Input"}) {
		t.Fatal("scroll network hold allowed input to overtake")
	}
	s.domHold = j
	s.hold = nil
	if s.holdFreeNow(SkyADT{SkyName: "Input"}) {
		t.Fatal("scroll DOM hold allowed input to overtake")
	}
	s.domHold = nil
	legacy := s.issue(nil, nil, true)
	legacy.name = "Server"
	if !s.holdFreeNow(SkyADT{SkyName: "Input"}) {
		t.Fatal("legacy hold-free policy changed")
	}
}

func TestScrollRPCSupersedingRenderCancelsStaleScroll(t *testing.T) {
	s, q := scrollSchedulerFixture()
	var order []string
	apply := func(c scrollCmd) { order = append(order, "scroll:"+c.Target) }
	run := func(msg any) {
		name := msg.(string)
		order = append(order, name)
		if name == "Applied" {
			q.committed(true, false, apply)
			q.command(scrollCmd{Target: "same-id"}, apply)
		}
		if name == "Supersede" {
			q.committed(false, false, apply)
		}
	}
	j := s.issueWithDOM(nil, nil, true, true)
	s.dispatch("Next", run)
	s.settle(j, "Applied", run)
	s.dispatchUrgent("Supersede", run)
	if s.held() || !reflect.DeepEqual(order, []string{"Applied", "Supersede", "Next"}) {
		t.Fatalf("stale scroll or deadlock: %v", order)
	}
	q.committed(false, true, apply)
	if len(order) != 3 {
		t.Fatal("cancelled scroll replayed")
	}
}

func TestScrollRPCFailureAndNoScrollRelease(t *testing.T) {
	for _, mode := range []string{"rpc-error", "decoder-error", "failed-render", "missing-target", "catchup-failure"} {
		t.Run(mode, func(t *testing.T) {
			s, q := scrollSchedulerFixture()
			var order []string
			apply := func(scrollCmd) { order = append(order, "no-op-missing-target") }
			run := func(msg any) {
				name := msg.(string)
				order = append(order, name)
				if name == "Applied" && mode != "failed-render" {
					q.committed(true, false, apply)
					if mode == "missing-target" || mode == "catchup-failure" {
						q.command(scrollCmd{Target: "absent"}, apply)
					}
				}
			}
			j := s.issueWithDOM(nil, nil, true, true)
			s.dispatch("Next", run)
			s.settleWithDOM(j, "Applied", run, mode != "rpc-error")
			if mode == "missing-target" {
				q.committed(false, true, apply)
				s.drain(run)
			}
			if mode == "catchup-failure" {
				q.cancel()
				s.drain(run)
			}
			if s.held() || len(s.queue) != 0 || order[len(order)-1] != "Next" {
				t.Fatalf("failure retained hold: %v", order)
			}
		})
	}
}

func TestScrollRPCNetworkReportPreservesRetryHold(t *testing.T) {
	for _, dom := range []bool{false, true} {
		s, _ := scrollSchedulerFixture()
		var order []string
		run := func(msg any) { order = append(order, msg.(string)) }
		j := s.issueWithDOM(nil, nil, true, dom)
		s.dispatch("Next", run)
		s.report(j, "NetworkErr", run)
		if !s.pending() || !s.held() || !reflect.DeepEqual(order, []string{"NetworkErr"}) {
			t.Fatalf("retry hold changed: %v", order)
		}
		s.settle(j, "RetryApplied", run)
		if s.pending() || s.held() || !reflect.DeepEqual(order, []string{"NetworkErr", "RetryApplied", "Next"}) {
			t.Fatalf("retry completion: %v", order)
		}
	}
}

func TestScrollRPCNewNetworkHoldSurvivesOldDOMCommit(t *testing.T) {
	s, q := scrollSchedulerFixture()
	var next *spaRpcJob
	var order []string
	run := func(msg any) {
		name := msg.(string)
		order = append(order, name)
		if name == "Applied" {
			q.committed(true, false, func(scrollCmd) {})
			q.command(scrollCmd{Target: "id"}, func(scrollCmd) {})
			next = s.issue(nil, nil, true)
		}
	}
	j := s.issueWithDOM(nil, nil, true, true)
	s.dispatch("Next", run)
	s.settle(j, "Applied", run)
	q.committed(false, true, func(scrollCmd) {})
	s.drain(run)
	if s.hold != next || !s.held() || !reflect.DeepEqual(order, []string{"Applied"}) {
		t.Fatal("DOM release stole new network hold")
	}
	s.settle(next, "AppliedNext", run)
	if !reflect.DeepEqual(order, []string{"Applied", "AppliedNext", "Next"}) {
		t.Fatalf("new hold order: %v", order)
	}
}

func TestScrollRPCFollowUpsKeepOrderWithoutDeadlock(t *testing.T) {
	s, q := scrollSchedulerFixture()
	var order []string
	var run func(any)
	run = func(msg any) {
		name := msg.(string)
		order = append(order, name)
		if name == "Applied" {
			q.committed(true, false, func(scrollCmd) { order = append(order, "scroll") })
			q.command(scrollCmd{Target: "id"}, func(scrollCmd) { order = append(order, "scroll") })
			s.dispatchFirst([]any{"FollowUp"}, run)
		}
	}
	j := s.issueWithDOM(nil, nil, true, true)
	s.dispatch("Next", run)
	s.settle(j, "Applied", run)
	if !reflect.DeepEqual(order, []string{"Applied"}) {
		t.Fatalf("follow-up overtook DOM: %v", order)
	}
	q.committed(false, true, func(scrollCmd) { order = append(order, "scroll") })
	s.drain(run)
	if !reflect.DeepEqual(order, []string{"Applied", "scroll", "FollowUp", "Next"}) {
		t.Fatalf("follow-up order: %v", order)
	}
}

func TestScrollRPCNavigationRevokesOnlyReturnedSideband(t *testing.T) {
	for _, painted := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued-onNavigate", true: "direct-popstate-render"}[painted], func(t *testing.T) {
			s, q := scrollSchedulerFixture()
			var order []string
			apply := func(c scrollCmd) { order = append(order, "local:"+c.Target) }
			run := func(msg any) {
				name := msg.(string)
				order = append(order, name)
				if name == "Applied" {
					// This represents the unchanged delta merge/render on the new route.
					q.committed(false, false, apply)
					order = append(order, "delta")
					if s.allowReturnedScrolls() {
						order = append(order, "WRONG-old-sideband-same-id")
					}
					// Ordinary local effects and followUps are NOT the returned sideband.
					q.command(scrollCmd{Target: "new-local"}, apply)
					s.dispatchFirst([]any{"FollowUp"}, func(m any) { order = append(order, m.(string)) })
				}
			}
			q.committed(false, false, apply)
			j := s.issueWithDOM(nil, nil, true, true)
			s.dispatch("Next", run)
			// EXACT entry helper used by spaNavigate, even when its onNavigate Msg
			// queues without painting. A direct render advances the lineage again.
			q.supersede()
			if painted {
				q.committed(false, false, apply)
			}
			s.settle(j, "Applied", run)
			want := []string{"Applied", "delta", "local:new-local", "FollowUp", "Next"}
			if !reflect.DeepEqual(order, want) || s.held() || !s.allowReturnedScrolls() {
				t.Fatalf("stale request sideband escaped or semantics lost: %v", order)
			}
		})
	}
}

func TestScrollRPCCatchupDoesNotRevokeCurrentRequest(t *testing.T) {
	s, q := scrollSchedulerFixture()
	q.committed(true, false, func(scrollCmd) {})
	j := s.issueWithDOM(nil, nil, true, true)
	q.committed(false, true, func(scrollCmd) {})
	allowed := false
	s.settle(j, "Applied", func(any) { q.committed(false, false, func(scrollCmd) {}); allowed = s.allowReturnedScrolls() })
	if !allowed || s.held() {
		t.Fatal("same-render catch-up revoked current request")
	}
}

func TestScrollRPCRetryErrorRenderKeepsSamePageScroll(t *testing.T) {
	s, q := scrollSchedulerFixture()
	var order []string
	apply := func(c scrollCmd) { order = append(order, "scroll:"+c.Target) }
	run := func(msg any) {
		name := msg.(string)
		order = append(order, name)
		if name == "NetworkErr" {
			q.committed(false, false, apply)
		}
		if name == "RetryApplied" {
			q.committed(true, false, apply)
			if s.allowReturnedScrolls() {
				q.command(scrollCmd{Target: "same-id"}, apply)
			}
		}
	}
	j := s.issueWithDOM(nil, nil, true, true)
	s.dispatch("Next", run)
	s.report(j, "NetworkErr", run)
	if !s.held() || !reflect.DeepEqual(order, []string{"NetworkErr"}) {
		t.Fatal("report lost retry hold")
	}
	s.settle(j, "RetryApplied", run)
	if !s.held() || !reflect.DeepEqual(order, []string{"NetworkErr", "RetryApplied"}) {
		t.Fatal("same-page retry lost scroll authority")
	}
	q.committed(false, true, apply)
	s.drain(run)
	want := []string{"NetworkErr", "RetryApplied", "scroll:same-id", "Next"}
	if s.held() || !reflect.DeepEqual(order, want) {
		t.Fatalf("retry scroll/Next order: %v", order)
	}
}

func TestScrollRPCMixedResidualScopeUsesOriginalAuthority(t *testing.T) {
	s, q := scrollSchedulerFixture()
	var j *spaRpcJob
	// The actual private interpreter case scopes BEFORE the URL residual.
	s.withScrollAuthority(func() {
		q.supersede()
		s.withScrollAuthority(func() { j = s.issueWithDOM(nil, nil, true, true) })
	})
	allowed := true
	s.settle(j, "Applied", func(any) { q.committed(false, false, func(scrollCmd) {}); allowed = s.allowReturnedScrolls() })
	if allowed || s.scopeAuthorityKnown || s.held() {
		t.Fatal("nested mixed residual refreshed old authority")
	}
	// Scope exit must not poison a genuinely new request on the new route.
	fresh := s.issueWithDOM(nil, nil, true, true)
	s.settle(fresh, "FreshApplied", func(any) { q.committed(false, false, func(scrollCmd) {}); allowed = s.allowReturnedScrolls() })
	if !allowed {
		t.Fatal("fresh route request inherited stale scope")
	}
}

func TestScrollRPCScopeRestoresAfterPanic(t *testing.T) {
	s, q := scrollSchedulerFixture()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected scope fixture panic")
			}
		}()
		s.withScrollAuthority(func() { q.supersede(); panic("public scope fixture") })
	}()
	if s.scopeAuthorityKnown {
		t.Fatal("panic leaked command authority")
	}
	j := s.issueWithDOM(nil, nil, true, true)
	allowed := false
	s.settle(j, "Applied", func(any) { q.committed(false, false, func(scrollCmd) {}); allowed = s.allowReturnedScrolls() })
	if !allowed || s.held() {
		t.Fatal("scope panic poisoned next request")
	}
}
