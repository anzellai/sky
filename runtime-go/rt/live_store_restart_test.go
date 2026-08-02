// live_store_restart_test.go — L10a "cross-restart any-field session loss".
//
// The single-process specs (register_sky_gob_types_test.go,
// gob_register_test.go) prove RegisterSkyGobTypes lets an `any`-typed Model
// field gob-round-trip WITHIN ONE PROCESS. They CANNOT reproduce the real
// defect, which is process-local: gob's name→type registry lives in the
// process; encodeSession's gobRegisterAll(s.model) populates the WRITER's
// registry by walking the live value, but that never reaches a freshly
// restarted DECODER process. After a restart the new process must have
// INDEPENDENTLY registered any concrete type that only ever lived in an
// `any` field — that is exactly what the codegen-emitted
// `func init(){ rt.RegisterSkyGobTypes([]any{…}) }` (rust/crates/codegen/
// src/lib.rs emit_program) provides at boot.
//
// A same-process encode→decode can't exercise that. This file drives the
// genuine CROSS-process path (approach (a)): the test binary re-exec's a
// fresh copy of itself (the standard os/exec TestHelperProcess pattern) so
// each phase runs in its OWN process with its OWN gob registry.
//
// Faithful defect shape: `restartModel` is reachable from the init value, so
// a boot walk of the init model registers it. `restartAnyPayload` only ever
// appears inside `restartModel.Payload any`, which is nil at init — so a boot
// walk of the init VALUE never sees it, and only the whole-binary
// RegisterSkyGobTypes list (which the codegen emits) registers it. This is
// the precise gap the fix closes.

package rt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// restartModel stands for a Sky record-alias Model. Reachable from the init
// value → registered by any boot walk of the init model.
type restartModel struct {
	Page    string
	Payload any // nil at init; later holds a concrete restartAnyPayload
}

// restartAnyPayload stands for a Sky record/ADT that only ever lives inside an
// `any` field. Invisible to a boot walk of the (Payload==nil) init value; only
// the exhaustive RegisterSkyGobTypes list registers it.
type restartAnyPayload struct {
	Label string
	N     int
}

const (
	restartRoleEnv = "SKY_LIVE_RESTART_ROLE"
	restartFileEnv = "SKY_LIVE_RESTART_FILE"
)

// TestRestartHelperProcess is the re-exec target. In the parent test run its
// role env is empty and it returns immediately (a no-op). When the parent
// re-exec's this binary with SKY_LIVE_RESTART_ROLE set, it performs one phase
// in a fresh process and os.Exit's with a role-appropriate code + stdout
// marker so the parent can assert cross-process behaviour.
func TestRestartHelperProcess(t *testing.T) {
	role := os.Getenv(restartRoleEnv)
	if role == "" {
		return // ordinary in-process run — not a re-exec'd helper.
	}
	path := os.Getenv(restartFileEnv)

	switch role {
	case "writer":
		// Fresh process encodes a session whose Model carries a concrete value
		// behind an `any` field, via the REAL serialization path (encodeSession
		// → gob). encodeSession's gobRegisterAll registers the type in THIS
		// (writer) process only.
		s := &liveSession{
			model: restartModel{
				Page:    "home",
				Payload: restartAnyPayload{Label: "deep-any-value", N: 42},
			},
		}
		s.setLastSeenTime(time.Now())
		blob, err := encodeSession(s)
		if err != nil {
			fmt.Printf("WRITER_ENCODE_ERR: %v\n", err)
			os.Exit(3)
		}
		if err := os.WriteFile(path, blob, 0o600); err != nil {
			fmt.Printf("WRITER_WRITE_ERR: %v\n", err)
			os.Exit(3)
		}
		fmt.Printf("WRITER_OK bytes=%d\n", len(blob))
		os.Exit(0)

	case "reader-none":
		// Fresh process, NO boot registration at all — empty gob registry for our
		// types. Reproduces a restart where the codegen registration is missing.
		restartReadAndReport(path)

	case "reader-initwalk":
		// Simulates a boot that only walked the INIT model value (Payload==nil):
		// registers restartModel but NOT the any-only restartAnyPayload. This is
		// the exact insufficiency the whole-binary list exists to close.
		RegisterSkyGobTypes([]any{restartModel{}})
		restartReadAndReport(path)

	case "reader-boot":
		// Simulates the codegen-emitted whole-binary RegisterSkyGobTypes list —
		// EVERY non-generic record/ADT struct, incl. the any-only payload.
		RegisterSkyGobTypes([]any{restartModel{}, restartAnyPayload{}})
		restartReadAndReport(path)

	default:
		fmt.Printf("UNKNOWN_ROLE: %q\n", role)
		os.Exit(4)
	}
}

// restartReadAndReport decodes the persisted blob via the REAL decodeSession
// path and reports the outcome via stdout marker + exit code. Runs in a fresh
// process, so success depends solely on THIS process's gob registration.
func restartReadAndReport(path string) {
	blob, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("READ_FILE_ERR: %v\n", err)
		os.Exit(3)
	}
	sess, err := decodeSession(blob)
	if err != nil {
		fmt.Printf("DECODE_ERR: %v\n", err)
		os.Exit(1)
	}
	m, ok := sess.model.(restartModel)
	if !ok {
		fmt.Printf("MODEL_TYPE_ERR: got %T\n", sess.model)
		os.Exit(1)
	}
	p, ok := m.Payload.(restartAnyPayload)
	if !ok {
		fmt.Printf("PAYLOAD_TYPE_ERR: got %T\n", m.Payload)
		os.Exit(1)
	}
	if p.Label != "deep-any-value" || p.N != 42 {
		fmt.Printf("PAYLOAD_VALUE_ERR: %#v\n", p)
		os.Exit(1)
	}
	fmt.Printf("DECODE_OK label=%s n=%d\n", p.Label, p.N)
	os.Exit(0)
}

// runRestartRole re-exec's this test binary running ONLY the helper test, in a
// fresh process, with the given role. Returns the combined output + the
// process error (nil ⇔ exit 0).
func runRestartRole(t *testing.T, role, blobPath string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestRestartHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(),
		restartRoleEnv+"="+role,
		restartFileEnv+"="+blobPath,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCrossProcessSessionRestart is the orchestrator. It proves the restart
// path is SOUND (a fresh process with the codegen boot registration decodes an
// any-field session) AND that the registration is load-bearing (a fresh
// process WITHOUT it — or with only an init-value walk — cannot decode, so a
// regression that drops RegisterSkyGobTypes would turn this test RED).
func TestCrossProcessSessionRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-process restart test re-exec's the binary; skipped under -short")
	}

	dir := t.TempDir() // auto-cleaned; under the OS temp dir.
	blobPath := filepath.Join(dir, "session.gob")

	// Phase 1 — WRITER process persists the session.
	out, err := runRestartRole(t, "writer", blobPath)
	if err != nil {
		t.Fatalf("writer subprocess failed: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "WRITER_OK") {
		t.Fatalf("writer did not confirm encode; output:\n%s", out)
	}
	if fi, e := os.Stat(blobPath); e != nil || fi.Size() == 0 {
		t.Fatalf("session blob was not written to %s: %v", blobPath, e)
	}

	// Phase 2 — READER process WITH the codegen boot registration. This is the
	// fix; it MUST decode the any-field session in a fresh process. Failure here
	// is a REAL BUG (boot registration missing/incomplete → cross-restart loss).
	outBoot, errBoot := runRestartRole(t, "reader-boot", blobPath)
	if errBoot != nil {
		t.Fatalf("REAL BUG (L10a cross-restart any-field loss): a fresh process WITH the "+
			"codegen-emitted RegisterSkyGobTypes boot list could NOT decode an any-field "+
			"session: %v\noutput:\n%s", errBoot, outBoot)
	}
	if !strings.Contains(outBoot, "DECODE_OK label=deep-any-value n=42") {
		t.Fatalf("reader-boot decoded but the any-field value was wrong; output:\n%s", outBoot)
	}

	// Phase 3 — the registration is load-bearing. A fresh process that either
	// registered nothing, or walked only the init value (Payload==nil), MUST
	// fail to decode. If either SUCCEEDS, the cross-process registry gap isn't
	// actually being exercised and Phase 2 proves nothing — flag it loudly.
	for _, role := range []string{"reader-none", "reader-initwalk"} {
		o, e := runRestartRole(t, role, blobPath)
		if e == nil {
			t.Fatalf("%s unexpectedly SUCCEEDED — the process-local gob-registry gap is "+
				"not being reproduced, so this test would not catch a dropped "+
				"RegisterSkyGobTypes. output:\n%s", role, o)
		}
		if !strings.Contains(o, "DECODE_ERR") {
			// Still a failure (non-zero exit), but not via the decode path we
			// expect — surface it for diagnosis without failing the phase.
			t.Logf("%s failed (as required) but without a DECODE_ERR marker; output:\n%s", role, o)
		} else {
			t.Logf("%s correctly failed cross-process decode: %s", role,
				firstLine(o, "DECODE_ERR"))
		}
	}
}

// firstLine returns the first line of s containing needle (for concise logs).
func firstLine(s, needle string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, needle) {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}
