package rt

import (
	"fmt"
	"testing"
	"time"
)

// Silent-degrade regression. An EXPLICITLY-configured but unrecognised session
// store kind — a typo ("postgress"/"psql") or the documented-but-unimplemented
// "firestore" (listed as a store option in the docs + sky.toml, but with no
// branch in chooseStore) — must NOT silently fall back to memory in production.
// Silent memory loses every session on restart and never shares across replicas
// (the darraghstudio-class production failure). The pre-fix `default` arm
// returned memory for ANY unknown kind; the v0.19.4 fail-loud work only covered
// KNOWN stores that fail to CONNECT, not UNKNOWN store names. It must fail loud
// (storeFatalf) in prod, and warn + fall back to memory in dev.
func TestChooseStore_UnknownKind_FailsLoudInProd(t *testing.T) {
	t.Setenv("ENV", "production")

	oldFatal := storeFatalf
	var fatalMsg string
	storeFatalf = func(format string, args ...any) { fatalMsg = fmt.Sprintf(format, args...) }
	defer func() { storeFatalf = oldFatal }()

	for _, kind := range []string{"firestore", "postgress", "psql"} {
		fatalMsg = ""
		_ = chooseStore(kind, "", time.Minute)
		if fatalMsg == "" {
			t.Fatalf("chooseStore(%q) in production must FAIL LOUD, but storeFatalf did not fire "+
				"(silent in-memory fallback would lose sessions on restart)", kind)
		}
	}
}

// In dev the same unknown kind warns and falls back to memory (never fatal), so
// local iteration isn't blocked — the hard failure is production-only.
func TestChooseStore_UnknownKind_WarnsAndMemoryInDev(t *testing.T) {
	t.Setenv("ENV", "development")

	oldFatal := storeFatalf
	fataled := false
	storeFatalf = func(string, ...any) { fataled = true }
	defer func() { storeFatalf = oldFatal }()

	s := chooseStore("firestore", "", time.Minute)
	if fataled {
		t.Fatal("chooseStore(firestore) in DEV must NOT fatal — warn + memory fallback")
	}
	if _, ok := s.(*memoryStore); !ok {
		t.Fatalf("dev fallback for an unknown store kind should be *memoryStore, got %T", s)
	}
}

// The intended memory paths (unset + explicit "memory") must NEVER fatal, even
// in production — SKY_LIVE_STORE=memory is the deliberate in-memory opt-in.
func TestChooseStore_MemoryAndEmpty_NeverFatal(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("SKY_LIVE_STORE", "") // isolate: empty kind must not read a leaked env store

	oldFatal := storeFatalf
	fataled := false
	storeFatalf = func(string, ...any) { fataled = true }
	defer func() { storeFatalf = oldFatal }()

	for _, kind := range []string{"", "memory"} {
		s := chooseStore(kind, "", time.Minute)
		if fataled {
			t.Fatalf("chooseStore(%q) must never fatal (intended memory path)", kind)
		}
		if _, ok := s.(*memoryStore); !ok {
			t.Fatalf("chooseStore(%q) should be *memoryStore, got %T", kind, s)
		}
	}
}
