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
func startupReportLines(consoleURL string, production bool, gc gcTuning, gcQuiet bool) []string {
	dev := consoleURL != "" && !production
	var out []string
	if dev {
		// "open — no login" is the accurate description of what a bare run
		// serves: with `SKY_CONSOLE_AUTH` unset and `ENV` unset the mode is
		// `consoleAuthModeDevOpen` and `evaluateConsoleAuth` returns true
		// outright (console_auth_v2.go). Saying only "console mounted" would
		// leave the user to discover the "unauthenticated" half themselves.
		out = append(out, fmt.Sprintf("  %-11s  %s  (open — no login in dev)", "dev console", consoleURL))
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

// printStartupReport writes the block after a server's own listening line.
//
// It goes to the same stream as that line (stdout) so the block stays together
// when a user redirects one or the other.
func printStartupReport(port int) {
	consoleURL := ""
	if InlineConsoleHealthy() || LegacyConsoleHealthy() {
		consoleURL = fmt.Sprintf("http://localhost:%d/_sky/console", port)
	}
	lines := startupReportLines(consoleURL, productionFromEnv(), gcStartupDecision, os.Getenv("SKY_GC_QUIET") != "")
	if len(lines) == 0 {
		return
	}
	fmt.Println(strings.Join(lines, "\n"))
}
