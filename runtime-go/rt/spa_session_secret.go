package rt

import (
	crand "crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

// Transparent, STATELESS session signing for the Sky.Spa auto-split.
//
// A Sky.Live app moved to `--target web:app` has its `update` become POST
// /_rpc/<Msg> handlers built from the CLIENT-supplied model. A branch that
// gates on `model.session` for a trust decision (`requireAdmin`) would trust a
// forgeable wire value. Sky.Live cannot be forged because it holds the model
// server-side per `sky_sid`; the SPA cannot hold server state without losing
// the horizontal-scale property that is the whole reason to pick the SPA.
//
// The stateless answer, reusing Std.Auth's own signed-token logic: the backend
// signs the session projection into an httpOnly cookie on login and VERIFIES
// that cookie on every RPC, taking identity from the verified cookie, never
// from the wire. No server session store, so the backend stays stateless and
// scales. The one shared piece of config is this signing secret.

// The env var an operator sets to pin ONE shared session-signing secret across
// replicas. A horizontally-scaled deployment MUST set it (every replica must
// verify with the same key). When unset, a single node mints and persists a
// secret, so a lone VM works with no configuration.
const spaSessionSecretEnv = "SKY_SPA_SESSION_SECRET"

// The minimum key length Std.Auth enforces (db_auth.go coerceAuthSecret). A
// shorter operator-supplied secret is refused rather than silently accepted.
const spaSessionSecretMinBytes = 32

var (
	spaSessionSecretOnce sync.Once
	spaSessionSecretVal  string
)

// Spa_sessionSecret is the internal kernel the auto-split's generated backend
// calls (`Ffi.kernel "Spa_sessionSecret"`) to obtain the session-signing secret
// as an opaque Secret. It resolves ONCE per process:
//
//  1. $SKY_SPA_SESSION_SECRET, if set (must be >= 32 bytes) — the shared key an
//     operator supplies for a multi-replica deployment.
//  2. else a 32-byte random secret minted once and persisted 0600 under the
//     data dir (SKY_DATA_DIR, else <cwd>/.skydata), so a single node keeps the
//     same key across restarts with no configuration.
//
// The value is wrapped in Secret so it redacts in every log / JSON path; the
// generated backend hands it straight to Auth.signToken / verifyToken and never
// reveals it.
func Spa_sessionSecret(_ any) any {
	spaSessionSecretOnce.Do(func() { spaSessionSecretVal = resolveSpaSessionSecret() })
	return Secret{v: spaSessionSecretVal}
}

func resolveSpaSessionSecret() string {
	if v := os.Getenv(spaSessionSecretEnv); v != "" {
		if len(v) < spaSessionSecretMinBytes {
			// A short shared secret is a real misconfiguration, not something
			// to silently accept. Fail loud (classified as a startup error by
			// the top-level recover) rather than sign with a weak key.
			panic(spaSessionSecretEnv + " is set but shorter than 32 bytes; supply a key of at least 32 bytes")
		}
		return v
	}
	// No operator secret: mint + persist under the data dir for a single node.
	dir := spaSecretDataDir()
	path := filepath.Join(dir, "spa-session-secret")
	if b, err := os.ReadFile(path); err == nil && len(b) >= spaSessionSecretMinBytes {
		return string(b)
	}
	raw := make([]byte, 32)
	if _, err := crand.Read(raw); err != nil {
		panic("spa session secret: crypto/rand failed: " + err.Error())
	}
	secret := hex.EncodeToString(raw) // 64 hex chars, >= 32 bytes
	// Best-effort persist. A read-only FS (an ephemeral serverless deploy) keeps
	// the in-memory secret for this process — it just will not survive a
	// restart, which such a deploy already implies. Perms 0600: run user only.
	if err := os.MkdirAll(dir, 0o700); err == nil {
		_ = os.WriteFile(path, []byte(secret), 0o600)
	}
	return secret
}

// spaSecretDataDir mirrors the embedded-Postgres data-root convention
// (pg_embed.go dataRootFrom): SKY_DATA_DIR, else <cwd>/.skydata.
func spaSecretDataDir() string {
	if v := os.Getenv("SKY_DATA_DIR"); v != "" {
		return v
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return filepath.Join(cwd, ".skydata")
}
