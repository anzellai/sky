//go:build !js

package rt

// What the runtime decided, said at startup, where an operator will read it.
//
// # The gap this closes
//
// A Sky.Live app's entire startup output was one line — `Sky.Live listening on
// :8000`. Meanwhile the runtime was making two decisions on the user's behalf
// that neither a first-time user nor a deploying one could see:
//
//   - With `ENV`/`SKY_ENV` unset it mounts the embedded console INSIDE the
//     user's process and injects a dev anchor into every page. That default is
//     deliberate and good — it is what makes a first Sky.Live app show its own
//     telemetry with nothing configured — but a user who was never told they
//     have a console cannot use it, and a user who was never told it is on
//     cannot decide to turn it off before deploying.
//   - It now derives `GOMEMLIMIT` and `GOGC` from detected machine memory
//     (`gc_tuning.go`). Detection can be wrong — a host that reports nothing, a
//     container whose limit is not where the runtime looks — and the only way
//     to find that out from outside the process is to be told what it detected.
//
// Neither is a gate. Nothing here changes behaviour; it reports behaviour that
// was already happening silently. The console line ADVERTISES rather than
// warns, and under `ENV=production` it simply disappears rather than being
// replaced by a scolding.
//
// # Why this is additive and not a reformat
//
// Three consumers parse the existing line and would break on a reshape:
// `apps/fieldbook/verify.sh` greps `Sky.Live listening on :$PORT` literally,
// and both `xtask build_run_gate` and `sky run`'s supervisor lift the port from
// the last `:PORT` of any line whose lowercase form contains "listening". So
// the first line is emitted UNCHANGED, and no line added below may contain the
// substring "listening" — `startupReportLines` is checked for exactly that by
// `TestNoAddedStartupLineLooksLikeAListeningLine`.

import (
	"fmt"
	"os"
	"strings"
)

// startupReportLines is the indented block printed under a server's "listening"
// line. Pure, so the wording is testable without starting a server.
//
// `consoleURL` is empty when no console is mounted, which is the honest input:
// the block must not advertise a surface this binary does not serve.
//
// `bindHost` is the actual interface the listener bound (resolveBindHost's
// result: "127.0.0.1" in dev, "" for all-interfaces, or a SKY_HOST value). It
// drives the exposure note, which must reflect the real bind — NOT the display
// URL, because a wide bind (SKY_HOST=0.0.0.0) still renders a localhost URL for
// clickability, yet IS reachable off-box and must warn.
func startupReportLines(consoleURL string, bindHost string, production bool, gc gcTuning, gcQuiet bool) []string {
	dev := consoleURL != "" && !production
	var out []string
	if dev {
		// "open — no login" is the accurate description of what a bare run
		// serves: with `SKY_CONSOLE_AUTH` unset and `ENV` unset the mode is
		// `consoleAuthModeDevOpen` and `evaluateConsoleAuth` returns true
		// outright (console_auth_v2.go). Saying only "console mounted" would
		// leave the user to discover the "unauthenticated" half themselves.
		out = append(out, fmt.Sprintf("  %-11s  %s  (open — no login in dev)", "dev console", consoleURL))
		// The dev listener binds loopback (resolveBindHost), so the localhost
		// URL above is TRUE and the open console is reachable only from this
		// host. The one case where it is NOT is an operator who bound wider than
		// loopback while leaving ENV unset — SKY_HOST=0.0.0.0 or a concrete LAN
		// address: the console is still open AND now reachable off-box. This
		// line makes that exposure explicit so it is not a surprise. Keyed on
		// the real bind host, not the URL, because 0.0.0.0 renders as a
		// localhost URL yet is wide open.
		if !isLoopbackBindHost(bindHost) {
			out = append(out, fmt.Sprintf("  %-11s  console is open AND reachable off this host — set SKY_CONSOLE_AUTH or unset SKY_HOST", "exposed"))
		}
	}
	if !gcQuiet {
		out = append(out, fmt.Sprintf("  %-11s  %s", "GC", gc.reason))
	}
	if dev {
		// A checklist, not a reprimand. Every name here is one the RUNTIME
		// reads — `SKY_CONSOLE_TOKEN` is the console login secret
		// (console_auth_v2.go), NOT the legacy `SKY_CONSOLE_TOKEN_SECRET`,
		// which is only the last fallback of the admin-bearer resolver; and
		// `SKY_ADMIN_TOKEN` is the canonical metrics bearer (console_auth.go),
		// not `SKY_METRICS_TOKEN`, which is its back-compat alias.
		//
		// `SKY_AUTH_TOKEN_SECRET` is deliberately ABSENT. Nothing in the
		// runtime reads it: `Auth.signToken` takes its secret as a Sky-level
		// ARGUMENT, and the env var is a convention in user code that only
		// `sky doctor` knows about. Printing it here would tell every user of
		// every no-auth app to set a variable that changes nothing.
		out = append(out,
			fmt.Sprintf("  %-11s  ENV=production  SKY_CONSOLE_AUTH=token", "to deploy"),
			fmt.Sprintf("  %-11s  SKY_CONSOLE_TOKEN=$(openssl rand -base64 32)  · SKY_ADMIN_TOKEN for /_sky/metrics", ""))
	}
	return out
}

// isLoopbackBindHost reports whether a bind host reaches ONLY this machine.
// An empty host means all-interfaces (":port"), which is NOT loopback; it only
// arises in production, where the exposure note is not printed anyway.
func isLoopbackBindHost(bindHost string) bool {
	switch bindHost {
	case "127.0.0.1", "::1", "[::1]", "localhost":
		return true
	}
	return false
}

// consoleDisplayHost maps a bind host to the host shown in the console URL. A
// loopback or all-interfaces bind renders as "localhost" (the address a human
// on the box types), so the dev URL stays the familiar localhost form and is
// TRUE now that dev binds loopback (resolveBindHost). A concrete off-box host
// (an operator's SKY_HOST=1.2.3.4) is shown verbatim, which is exactly what
// makes the "exposed" note in startupReportLines fire.
func consoleDisplayHost(bindHost string) string {
	switch bindHost {
	case "", "0.0.0.0", "::", "[::]", "127.0.0.1", "localhost":
		return "localhost"
	}
	return bindHost
}

// printStartupReport writes the block after a server's own listening line.
//
// It goes to the same stream as that line (stdout) so the block stays together
// when a user redirects one or the other.
func printStartupReport(port int) {
	bindHost := resolveBindHost()
	consoleURL := ""
	if InlineConsoleHealthy() || LegacyConsoleHealthy() {
		consoleURL = fmt.Sprintf("http://%s:%d/_sky/console", consoleDisplayHost(bindHost), port)
	}
	lines := startupReportLines(consoleURL, bindHost, productionFromEnv(), gcStartupDecision, os.Getenv("SKY_GC_QUIET") != "")
	// The legacy-sky.toml → withX migration LIST (design §8.2), appended AFTER
	// the checklist rather than woven into `startupReportLines`: it depends on
	// the process's seeded-default provenance, not on the pure inputs that
	// function takes, and it must surface in production too (a legacy config
	// deployed unmigrated is worth naming, not just in dev). Self-extinguishing
	// — `legacyMigrationNotices` returns nil once the keys are migrated, so a
	// clean app's startup output is byte-identical to before. None of its lines
	// contains "listening" (the substring the supervisor + verify.sh parse).
	lines = append(lines, legacyMigrationNotices()...)
	if len(lines) == 0 {
		return
	}
	fmt.Println(strings.Join(lines, "\n"))
}
