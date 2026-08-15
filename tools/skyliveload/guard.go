package main

// Target guards.
//
// This generator was written against localhost, where the worst outcome of
// a mistake is a busy laptop. Teaching it a base URL makes a much worse
// outcome reachable: sky-lang.org is a live site on a single e2-micro with
// 2 shared-core vCPUs and 993 MB of RAM, and the local runs established
// that 500 concurrent sessions drive a 1-CPU target into 4-second p50
// latency. Pointing this tool at it would not "add some load" -- it would
// take the site down for as long as the run lasted.
//
// So the guards are structural rather than advisory, and they live HERE,
// in the binary, rather than only in the wrapper script. A script guard is
// bypassed the first time someone runs the binary directly, which is
// exactly what a person does while debugging a run that failed.
//
// Three gates, in order:
//
//  1. Anything that is not loopback needs -remote-load. Absent it, the
//     tool refuses. This is what makes passive the default: you cannot
//     apply load off-box by forgetting a flag, only by adding one.
//  2. Known production hosts are refused even WITH -remote-load, and are
//     released only by a flag whose name is deliberately unpleasant to
//     type and impossible to add absent-mindedly.
//  3. The resolved target is printed and confirmed before the first
//     request, so a wrong -url is caught by a human rather than by the
//     site's users.

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// productionHosts are refused by gate 2. Both the public domain and the
// GCE instance name appear here: the instance name is what someone types
// after copying a deploy.sh invocation, and it resolves over IAP or an
// internal DNS zone just as well as the domain does.
//
// Extend for a deployment this repo does not know about with
// SKYLIVE_BENCH_DENY_HOSTS (comma-separated).
var productionHosts = []string{
	"sky-lang.org",
	"sky-lang-org",
}

// benchHosts are the throwaway targets this harness is FOR. Listed so the
// confirmation prompt can say plainly that the target is a known-disposable
// one; they get no exemption from gates 1 or 3.
var benchHosts = []string{
	"sky-lang-bench",
}

// checkTarget applies all three gates. It returns an error to refuse the
// run; it prints to stderr and may read stdin for the confirmation.
//
// It must be called before ANY request is issued -- including -self-check,
// which is still a real session against a real server.
func checkTarget(rawURL string, remoteLoad, prodOverride, assumeYes bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("cannot parse -url %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("-url %q has no host", rawURL)
	}

	local := isLoopback(host)

	// --- Gate 1: off-box needs an explicit flag ------------------------
	if !local && !remoteLoad {
		return fmt.Errorf(
			"refusing to load a non-loopback target without -remote-load.\n"+
				"  target: %s (host %q)\n"+
				"  This tool defaults to passive for off-box targets. If you meant to\n"+
				"  observe an instance rather than load it, use\n"+
				"  scripts/skylive-observe-remote.sh, which is read-only.\n"+
				"  If you really mean to generate load against %s, pass -remote-load.",
			rawURL, host, host)
	}

	// --- Gate 2: production is refused even with -remote-load ----------
	if why := matchesProduction(host); why != "" {
		if !prodOverride {
			return fmt.Errorf(
				"REFUSING TO LOAD A PRODUCTION TARGET.\n"+
					"  target: %s\n"+
					"  matched: %s\n"+
					"  sky-lang.org runs on ONE e2-micro (2 shared vCPU, 993 MB). The\n"+
					"  constrained runs in docs/perf/skylive-interaction-cost.md put a\n"+
					"  1-CPU target at 4.2s p50 under 500 sessions. Loading this host\n"+
					"  takes the site down for the duration of the run.\n"+
					"  Stand up a throwaway instead:\n"+
					"    sky-lang.org/deploy/deploy.sh --instance sky-lang-bench \\\n"+
					"        --project <id> --zone us-central1-a\n"+
					"  and point -url at that.",
				rawURL, why)
		}
		fmt.Fprintf(os.Stderr,
			"\n*** PRODUCTION OVERRIDE ACTIVE -- target %s matched %s ***\n"+
				"*** You have asserted this is intentional. It will degrade or  ***\n"+
				"*** take down the live site for the duration of the run.       ***\n\n",
			host, why)
	}

	// --- Gate 3: print the resolved target, confirm before request 1 ---
	if local {
		return nil
	}

	kind := "UNKNOWN (not a host this harness recognises)"
	if w := matchesProduction(host); w != "" {
		kind = "PRODUCTION (" + w + ")"
	} else if matchesAny(host, benchHosts) {
		kind = "throwaway bench instance"
	}

	fmt.Fprintf(os.Stderr, "─── resolved load target ──────────────────────────────\n")
	fmt.Fprintf(os.Stderr, "  url        %s\n", rawURL)
	fmt.Fprintf(os.Stderr, "  host       %s\n", host)
	fmt.Fprintf(os.Stderr, "  classified %s\n", kind)
	if addrs, err := net.LookupHost(host); err == nil && len(addrs) > 0 {
		fmt.Fprintf(os.Stderr, "  resolves   %s\n", strings.Join(addrs, ", "))
	}
	fmt.Fprintf(os.Stderr, "───────────────────────────────────────────────────────\n")

	if assumeYes {
		fmt.Fprintf(os.Stderr, "-assume-yes given; proceeding without confirmation.\n\n")
		return nil
	}

	// Require the hostname to be typed back. A bare y/n is too easy to
	// answer on autopilot, and the failure being guarded against is
	// precisely inattention about WHICH host is targeted.
	fmt.Fprintf(os.Stderr, "Type the hostname to proceed (or anything else to abort): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("could not read confirmation (not a terminal? pass -assume-yes "+
			"only when the target is known-disposable): %w", err)
	}
	if strings.TrimSpace(line) != host {
		return fmt.Errorf("aborted: confirmation did not match %q", host)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// matchesProduction reports the rule a host matched, or "" for no match.
// Matching is exact or subdomain-suffix, so `staging.sky-lang.org` is
// caught but `sky-lang.org.example.com` is not treated as a match by
// accident of substring containment.
func matchesProduction(host string) string {
	deny := productionHosts
	if extra := os.Getenv("SKYLIVE_BENCH_DENY_HOSTS"); extra != "" {
		for _, h := range strings.Split(extra, ",") {
			if h = strings.TrimSpace(h); h != "" {
				deny = append(deny, h)
			}
		}
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range deny {
		d = strings.ToLower(d)
		if h == d {
			return d
		}
		if strings.HasSuffix(h, "."+d) {
			return d + " (subdomain " + h + ")"
		}
	}
	return ""
}

func matchesAny(host string, list []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range list {
		d = strings.ToLower(d)
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}
