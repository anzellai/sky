package main

import (
	"strings"
	"testing"
)

// The guards in guard.go are the only thing standing between a mistyped
// -url and sky-lang.org going down. That makes them exactly the class of
// code that must be proven to REFUSE, not merely proven to compile: a
// guard that has quietly stopped matching looks identical, from the
// outside, to one that was never exercised.
//
// Every case below therefore asserts the direction of the decision, and
// the production cases assert on the reason string too, so a refusal that
// starts happening for the wrong reason (e.g. gate 1 shadowing gate 2)
// still fails the test.

func TestLoopbackNeedsNoFlags(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1:8000",
		"http://localhost:8477",
		"http://[::1]:8000",
	} {
		if err := checkTarget(u, false, false, false); err != nil {
			t.Errorf("checkTarget(%q) = %v, want nil (loopback must stay frictionless)", u, err)
		}
	}
}

func TestNonLoopbackRefusedWithoutRemoteLoad(t *testing.T) {
	// Gate 1. This is what makes passive the default: applying load off-box
	// requires ADDING a flag, never forgetting one.
	for _, u := range []string{
		"http://sky-lang-bench:8000",
		"http://10.128.0.7:8000",
		"https://example.invalid",
	} {
		err := checkTarget(u, false, false, true)
		if err == nil {
			t.Fatalf("checkTarget(%q, remoteLoad=false) = nil, want refusal", u)
		}
		if !strings.Contains(err.Error(), "-remote-load") {
			t.Errorf("checkTarget(%q) refused for the wrong reason: %v", u, err)
		}
	}
}

func TestProductionRefusedEvenWithRemoteLoad(t *testing.T) {
	// Gate 2. -remote-load is necessary but never sufficient for prod.
	cases := []struct{ url, wantMatch string }{
		{"https://sky-lang.org", "sky-lang.org"},
		{"https://www.sky-lang.org", "sky-lang.org"},
		{"https://staging.sky-lang.org", "sky-lang.org"},
		{"http://sky-lang-org:8000", "sky-lang-org"},
		// Trailing-dot FQDN form must not slip past the matcher.
		{"https://sky-lang.org.", "sky-lang.org"},
		// Case must not slip past it either.
		{"https://SKY-LANG.ORG", "sky-lang.org"},
	}
	for _, c := range cases {
		err := checkTarget(c.url, true, false, true)
		if err == nil {
			t.Fatalf("checkTarget(%q, remoteLoad=true) = nil, want production refusal", c.url)
		}
		if !strings.Contains(err.Error(), "PRODUCTION") {
			t.Errorf("checkTarget(%q): want production refusal, got: %v", c.url, err)
		}
		if !strings.Contains(err.Error(), c.wantMatch) {
			t.Errorf("checkTarget(%q): refusal should name %q, got: %v", c.url, c.wantMatch, err)
		}
	}
}

func TestProductionOverrideReleasesTheRefusal(t *testing.T) {
	// The override must actually work -- a guard that cannot be released
	// gets deleted by the next person in a hurry. It is gated on a flag
	// name that cannot be typed absent-mindedly, which is the real control.
	if err := checkTarget("https://sky-lang.org", true, true, true); err != nil {
		t.Fatalf("with the override set, want nil, got %v", err)
	}
}

func TestProductionOverrideAloneIsNotEnough(t *testing.T) {
	// Gate 1 still applies: the embarrassing flag does not imply
	// -remote-load. Both are required, so neither alone is a footgun.
	err := checkTarget("https://sky-lang.org", false, true, true)
	if err == nil {
		t.Fatal("override without -remote-load = nil, want gate-1 refusal")
	}
	if !strings.Contains(err.Error(), "-remote-load") {
		t.Errorf("want gate-1 refusal, got: %v", err)
	}
}

func TestNoFalsePositiveOnSubstringLookalikes(t *testing.T) {
	// A host that merely CONTAINS the production name is not production.
	// Refusing it would be harmless here, but the same sloppy matching in
	// the other direction is what lets a real prod host through, so the
	// matcher is pinned in both directions.
	for _, h := range []string{
		"sky-lang.org.evil.example",
		"notsky-lang.org",
		"sky-lang-org-bench",
		"sky-lang-bench",
	} {
		if why := matchesProduction(h); why != "" {
			t.Errorf("matchesProduction(%q) = %q, want no match", h, why)
		}
	}
}

func TestDenyListIsExtensibleByEnv(t *testing.T) {
	// A deployment this repo does not know about must be protectable
	// without a code change.
	t.Setenv("SKYLIVE_BENCH_DENY_HOSTS", "my-prod.example, other.example")
	if why := matchesProduction("my-prod.example"); why == "" {
		t.Error("SKYLIVE_BENCH_DENY_HOSTS entry was not honoured")
	}
	if why := matchesProduction("api.other.example"); why == "" {
		t.Error("SKYLIVE_BENCH_DENY_HOSTS subdomain was not honoured")
	}
	if why := matchesProduction("unrelated.example"); why != "" {
		t.Errorf("unrelated host matched %q", why)
	}
}

func TestBenchTargetProceedsUnderAssumeYes(t *testing.T) {
	// The intended path: a throwaway, with both the flag and an explicit
	// -assume-yes for non-interactive scripting.
	if err := checkTarget("http://sky-lang-bench:8000", true, false, true); err != nil {
		t.Fatalf("bench target with -remote-load -assume-yes: want nil, got %v", err)
	}
}

func TestMalformedURLIsRefused(t *testing.T) {
	for _, u := range []string{"", "://nope", "not a url at all"} {
		if err := checkTarget(u, true, true, true); err == nil {
			t.Errorf("checkTarget(%q) = nil, want a parse/host refusal", u)
		}
	}
}
