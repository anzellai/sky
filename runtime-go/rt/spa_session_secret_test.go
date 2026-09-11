package rt

import (
	"os"
	"path/filepath"
	"testing"
)

// The STATELESS SIGNED SESSION signing secret (spa_session_secret.go) is the one
// shared piece of config the Sky.Spa auto-split's signed cookie relies on. These
// tests pin its three properties: it honours an operator-supplied env secret,
// it mints + persists a stable per-node secret when the env is unset, and the
// resolved secret round-trips a signToken -> verifyToken through Std.Auth (the
// same token logic Sky.Live reuses).
//
// `resolveSpaSessionSecret` is exercised directly rather than `Spa_sessionSecret`
// because the kernel memoises via sync.Once, so it resolves ONCE per process and
// could not observe two different environments in one test binary.

func TestSpaSessionSecretFromEnv(t *testing.T) {
	// An operator-supplied secret of >= 32 bytes is honoured verbatim (the shared
	// key every replica of a horizontally-scaled deployment must verify with).
	want := "0123456789abcdef0123456789abcdef0123456789" // 42 bytes
	t.Setenv(spaSessionSecretEnv, want)
	got := resolveSpaSessionSecret()
	if got != want {
		t.Fatalf("env secret must be honoured verbatim: got %q, want %q", got, want)
	}
}

func TestSpaSessionSecretMintPersistAndReread(t *testing.T) {
	// No operator secret: a single node mints a random secret and PERSISTS it
	// under the data dir, so it survives a restart with no configuration.
	t.Setenv(spaSessionSecretEnv, "") // treated as unset by resolveSpaSessionSecret
	dir := t.TempDir()
	t.Setenv("SKY_DATA_DIR", dir)

	first := resolveSpaSessionSecret()
	if len(first) < spaSessionSecretMinBytes {
		t.Fatalf("minted secret must be >= %d bytes, got %d", spaSessionSecretMinBytes, len(first))
	}
	path := filepath.Join(dir, "spa-session-secret")
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("minted secret must be persisted at %s: %v", path, err)
	}
	if string(onDisk) != first {
		t.Fatalf("persisted secret must match the returned one: file %q, returned %q", string(onDisk), first)
	}
	// A second resolution re-reads the SAME persisted secret (stable across
	// restarts), never mints a fresh one.
	second := resolveSpaSessionSecret()
	if second != first {
		t.Fatalf("second resolution must re-read the persisted secret: got %q, want %q", second, first)
	}
}

func TestSpaSessionSecretRoundTripsSignVerify(t *testing.T) {
	// The resolved secret must round-trip a token through the Std.Auth kernels the
	// generated backend calls: sign a single JSON claim, verify it back.
	t.Setenv(spaSessionSecretEnv, "") // treated as unset
	dir := t.TempDir()
	t.Setenv("SKY_DATA_DIR", dir)

	secret := Secret{v: resolveSpaSessionSecret()}
	claims := map[string]any{"p0": `{"userId":"u1","role":"admin"}`}

	signed := Auth_signToken(secret, claims, 3600)
	sr, ok := signed.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("signToken must return Ok(token): %#v", signed)
	}
	token, ok := sr.OkValue.(string)
	if !ok || token == "" {
		t.Fatalf("signed token must be a non-empty string: %#v", sr.OkValue)
	}

	verified := Auth_verifyToken(secret, token)
	vr, ok := verified.(SkyResult[any, any])
	if !ok || vr.Tag != 0 {
		t.Fatalf("verifyToken with the SAME secret must return Ok(claims): %#v", verified)
	}
	m, ok := vr.OkValue.(map[string]any)
	if !ok {
		t.Fatalf("verified claims must be a map: %#v", vr.OkValue)
	}
	if got, _ := m["p0"].(string); got != claims["p0"] {
		t.Fatalf("claim p0 must round-trip: got %q, want %q", got, claims["p0"])
	}

	// A token signed with a DIFFERENT secret must NOT verify (the signature is the
	// trust boundary — a forged/foreign cookie is rejected).
	other := Secret{v: "ffffffffffffffffffffffffffffffffffffffff"}
	bad := Auth_verifyToken(other, token)
	if br, ok := bad.(SkyResult[any, any]); !ok || br.Tag != 1 {
		t.Fatalf("verifyToken with a different secret must return Err: %#v", bad)
	}
}
