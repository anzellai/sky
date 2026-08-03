// console_internal_token.go — closes F1 (the loopback-auth bypass).
//
// The in-process console sub-app calls the parent's /_sky/console/api/* over
// loopback. The OLD gate trusted any loopback IP — but behind a reverse proxy
// (app on 127.0.0.1, proxy terminates TLS) EVERY request's RemoteAddr is
// loopback, so the console APIs (incl. telemetry that leaks PII/secrets) were
// unauthenticated in production, and an app-side SSRF could reach them too.
//
// Fix: the parent mints a per-boot random token and injects it into the sub-app
// via SKY_CONSOLE_INTERNAL_TOKEN; the sub-app sends it as a Bearer on its own
// fetches; the gate accepts that token (or the operator's SKY_ADMIN_TOKEN) and
// NEVER trusts a loopback IP. Modeled on the observability ingest token.
//
// Honest scope: this authenticates the INTERNAL caller (the console sub-app). It
// is NOT a secret from in-process app code — same-process Sky code can read the
// env — but that code is already fully trusted (it can reach bluedbRegistry /
// dbRegistry directly). It is an internal-caller authenticator, not a capability
// boundary against the app itself.
package rt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

var consoleInternalTokenVal atomic.Value // string

// ConsoleInternalTokenInit mints (once) the per-boot token and publishes it to
// the process env so the in-process console sub-app reads it at its init. Idempotent.
func ConsoleInternalTokenInit() string {
	if existing, ok := consoleInternalTokenVal.Load().(string); ok && existing != "" {
		return existing
	}
	// Honour an externally-provided token (operator override / test), else mint one.
	if v := strings.TrimSpace(os.Getenv("SKY_CONSOLE_INTERNAL_TOKEN")); v != "" {
		consoleInternalTokenVal.Store(v)
		return v
	}
	buf := make([]byte, 32)
	var tok string
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure shouldn't stop the app booting; degrade to a
		// process-unique fallback (predictable, but the endpoint still functions).
		tok = fmt.Sprintf("fallback-%d-%d", os.Getpid(), time.Now().UnixNano())
	} else {
		tok = hex.EncodeToString(buf)
	}
	consoleInternalTokenVal.Store(tok)
	_ = os.Setenv("SKY_CONSOLE_INTERNAL_TOKEN", tok)
	return tok
}

func currentConsoleInternalToken() string {
	v, _ := consoleInternalTokenVal.Load().(string)
	return v
}
