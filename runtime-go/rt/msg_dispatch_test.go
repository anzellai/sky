package rt

import (
	"sync"
	"testing"
)

// v0.17 Phase 4, Stage 1 — runtime tests for the per-Msg dispatch
// registries.  Verifies register + lookup + concurrency for the
// three Stage 1 registries (RegisterMsgUpdate, RegisterMsgVariant,
// RegisterMsgDecoder) without touching the production code paths
// they'll eventually fast-path (those land in Stage 6).

func TestRegisterMsgUpdate_BasicRegisterAndLookup(t *testing.T) {
	// Stage 1 codegen emits `rt.RegisterMsgUpdate("Main_Msg", nil)`
	// — the table is nil but the registry slot must exist.
	const adt = "TestADT_RegisterMsgUpdate_Basic"
	RegisterMsgUpdate(adt, nil)

	got, ok := LookupMsgUpdate(adt)
	if !ok {
		t.Fatalf("LookupMsgUpdate(%q) ok=false, want true after RegisterMsgUpdate", adt)
	}
	if got != nil {
		t.Errorf("LookupMsgUpdate(%q) = %v, want nil (Stage 1 placeholder)", adt, got)
	}
}

func TestRegisterMsgUpdate_OverwriteIsLastWriteWins(t *testing.T) {
	const adt = "TestADT_RegisterMsgUpdate_Overwrite"

	firstTable := map[int]int{0: 100}
	secondTable := map[int]int{0: 200}

	RegisterMsgUpdate(adt, firstTable)
	RegisterMsgUpdate(adt, secondTable)

	got, ok := LookupMsgUpdate(adt)
	if !ok {
		t.Fatalf("LookupMsgUpdate(%q) ok=false after two registrations", adt)
	}
	m, isMap := got.(map[int]int)
	if !isMap {
		t.Fatalf("LookupMsgUpdate(%q) = %T, want map[int]int", adt, got)
	}
	if m[0] != 200 {
		t.Errorf("LookupMsgUpdate(%q)[0] = %d, want 200 (last-write-wins)", adt, m[0])
	}
}

func TestLookupMsgUpdate_AbsentReturnsFalse(t *testing.T) {
	const adt = "TestADT_LookupMsgUpdate_Absent_DoesNotExist"
	_, ok := LookupMsgUpdate(adt)
	if ok {
		t.Errorf("LookupMsgUpdate(%q) ok=true, want false for unregistered ADT", adt)
	}
}

func TestRegisterMsgVariant_BasicRegisterAndLookup(t *testing.T) {
	const adt = "TestADT_RegisterMsgVariant_Basic"

	// Stage 1 codegen emits one line per (ADT, ctor) pair:
	//   rt.RegisterMsgVariant("Main_Msg", "Increment", 0, 0)
	//   rt.RegisterMsgVariant("Main_Msg", "SetValue", 2, 1)
	RegisterMsgVariant(adt, "Increment", 0, 0)
	RegisterMsgVariant(adt, "SetValue", 2, 1)

	if info, ok := LookupMsgVariant(adt, "Increment"); !ok {
		t.Errorf("LookupMsgVariant(%q, Increment) ok=false", adt)
	} else if info.Tag != 0 || info.Arity != 0 {
		t.Errorf("LookupMsgVariant(%q, Increment) = %+v, want {Tag:0, Arity:0}", adt, info)
	}

	if info, ok := LookupMsgVariant(adt, "SetValue"); !ok {
		t.Errorf("LookupMsgVariant(%q, SetValue) ok=false", adt)
	} else if info.Tag != 2 || info.Arity != 1 {
		t.Errorf("LookupMsgVariant(%q, SetValue) = %+v, want {Tag:2, Arity:1}", adt, info)
	}
}

func TestLookupMsgVariant_AbsentReturnsFalse(t *testing.T) {
	const adt = "TestADT_LookupMsgVariant_Absent"
	if _, ok := LookupMsgVariant(adt, "NeverRegistered"); ok {
		t.Errorf("LookupMsgVariant ok=true for unregistered (adt, ctor)")
	}
}

func TestRegisterMsgVariant_DifferentAdtsDontCollide(t *testing.T) {
	// Two different ADTs both define a "Click" ctor — they must
	// route to distinct registry slots.
	const adtA = "TestADT_RegisterMsgVariant_ADTA"
	const adtB = "TestADT_RegisterMsgVariant_ADTB"

	RegisterMsgVariant(adtA, "Click", 5, 0)
	RegisterMsgVariant(adtB, "Click", 7, 2)

	if infoA, _ := LookupMsgVariant(adtA, "Click"); infoA.Tag != 5 {
		t.Errorf("ADT-A Click tag = %d, want 5", infoA.Tag)
	}
	if infoB, _ := LookupMsgVariant(adtB, "Click"); infoB.Tag != 7 || infoB.Arity != 2 {
		t.Errorf("ADT-B Click = %+v, want {Tag:7, Arity:2}", infoB)
	}
}

func TestRegisterMsgDecoder_BasicRegisterAndLookup(t *testing.T) {
	const ctor = "TestCtor_RegisterMsgDecoder_Basic"

	dec := func(raw []JsonRawMessage) (any, error) {
		return "decoded", nil
	}
	RegisterMsgDecoder(ctor, dec)

	got, ok := LookupMsgDecoder(ctor)
	if !ok {
		t.Fatalf("LookupMsgDecoder(%q) ok=false", ctor)
	}
	out, err := got(nil)
	if err != nil {
		t.Fatalf("decoder returned err=%v", err)
	}
	if out != "decoded" {
		t.Errorf("decoder result = %v, want %q", out, "decoded")
	}
}

func TestLookupMsgDecoder_AbsentReturnsFalse(t *testing.T) {
	if _, ok := LookupMsgDecoder("TestCtor_LookupMsgDecoder_Absent"); ok {
		t.Errorf("LookupMsgDecoder ok=true for unregistered ctor")
	}
}

func TestMsgDispatch_ConcurrentRegisterAndLookupSafe(t *testing.T) {
	// Concurrency floor: the registries are RWMutex-guarded.  We
	// stress with N goroutines all writing + reading per Stage 6's
	// "init() emits N RegisterMsgUpdate calls; sky_call reads
	// concurrently from M HTTP handlers" shape.
	const N = 64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			adt := "Concurrent_" + itoaFast(i)
			RegisterMsgUpdate(adt, nil)
			RegisterMsgVariant(adt, "C", i, 0)
			RegisterMsgDecoder("Dec_"+itoaFast(i), func(raw []JsonRawMessage) (any, error) {
				return nil, nil
			})
			// Each goroutine also reads what it just wrote — the
			// RWMutex semantics must not deadlock.
			_, _ = LookupMsgUpdate(adt)
			_, _ = LookupMsgVariant(adt, "C")
			_, _ = LookupMsgDecoder("Dec_" + itoaFast(i))
		}()
	}
	wg.Wait()

	// Verify N concurrent writes all landed.  Registry sizes
	// monotonically increase, so we just check >= N.  Other tests
	// in this file populate too, so we don't expect exact equality.
	if got := MsgUpdateRegistrySize(); got < N {
		t.Errorf("MsgUpdateRegistrySize after concurrent writes = %d, want >= %d", got, N)
	}
	if got := MsgVariantRegistrySize(); got < N {
		t.Errorf("MsgVariantRegistrySize after concurrent writes = %d, want >= %d", got, N)
	}
	if got := MsgDecoderRegistrySize(); got < N {
		t.Errorf("MsgDecoderRegistrySize after concurrent writes = %d, want >= %d", got, N)
	}
}

// ── Stage 5: fast-path consumer infrastructure tests ────────

func TestLookupAdtByCtor_PopulatedViaRegisterMsgVariant(t *testing.T) {
	const adt = "TestADT_Stage5_LookupByCtor"
	const ctor = "TestCtor_Stage5_AB"
	RegisterMsgVariant(adt, ctor, 0, 0)

	got, ok := LookupAdtByCtor(ctor)
	if !ok {
		t.Fatalf("LookupAdtByCtor(%q) ok=false, want true after RegisterMsgVariant", ctor)
	}
	if got != adt {
		t.Errorf("LookupAdtByCtor(%q) = %q, want %q", ctor, got, adt)
	}
}

func TestLookupAdtByCtor_AbsentReturnsFalse(t *testing.T) {
	const ctor = "TestCtor_Stage5_DoesNotExist_Absent"
	if _, ok := LookupAdtByCtor(ctor); ok {
		t.Errorf("LookupAdtByCtor(%q) ok=true, want false for unregistered ctor", ctor)
	}
}

func TestFastPathProbe_NonSkyADTFallsThrough(t *testing.T) {
	FastPathProbeReset()
	// Non-SkyADT argument: e.g. an int — must fall through.
	_, ok := tryFastPathMsgUpdate(42)
	if ok {
		t.Errorf("tryFastPathMsgUpdate(int) ok=true, want false")
	}
	eligible, fall := FastPathProbeStats()
	if eligible != 0 || fall != 1 {
		t.Errorf("probe stats = (e=%d, f=%d), want (0, 1)", eligible, fall)
	}
}

func TestFastPathProbe_SkyADTWithNoRegistryFallsThrough(t *testing.T) {
	FastPathProbeReset()
	a := SkyADT{Tag: 0, SkyName: "TestCtor_FastPath_Unknown_NotInRegistry", Fields: nil}
	_, ok := tryFastPathMsgUpdate(a)
	if ok {
		t.Errorf("tryFastPathMsgUpdate(SkyADT) with no registry ok=true, want false")
	}
	eligible, fall := FastPathProbeStats()
	if eligible != 0 || fall != 1 {
		t.Errorf("probe stats = (e=%d, f=%d), want (0, 1)", eligible, fall)
	}
}

func TestFastPathProbe_RegisteredADTReportsEligible(t *testing.T) {
	const adt = "TestADT_FastPath_Eligible"
	const ctor = "TestCtor_FastPath_Eligible_Inc"
	// Register both the variant (populates reverse ctor->adt map)
	// and the update dispatch table (non-nil so probe reports eligible).
	RegisterMsgVariant(adt, ctor, 0, 0)
	RegisterMsgUpdate(adt, map[int]any{0: "sentinel"})

	FastPathProbeReset()
	table, ok := tryFastPathMsgUpdate(SkyADT{Tag: 0, SkyName: ctor, Fields: nil})
	if !ok {
		t.Fatalf("tryFastPathMsgUpdate(eligible SkyADT) ok=false, want true")
	}
	m, isMap := table.(map[int]any)
	if !isMap || m[0] != "sentinel" {
		t.Errorf("returned table = %v, want map with sentinel", table)
	}
	eligible, fall := FastPathProbeStats()
	if eligible != 1 || fall != 0 {
		t.Errorf("probe stats = (e=%d, f=%d), want (1, 0)", eligible, fall)
	}
}

func TestFastPathProbe_RegisteredVariantButNilUpdateTableFallsThrough(t *testing.T) {
	// Stage 1 contract: RegisterMsgUpdate("X", nil) is valid
	// (placeholder).  Probe MUST treat nil as "no table" and fall
	// through — Stage 6 fast-path can't consume a nil table.
	const adt = "TestADT_FastPath_NilTable"
	const ctor = "TestCtor_FastPath_NilTable_X"
	RegisterMsgVariant(adt, ctor, 0, 0)
	RegisterMsgUpdate(adt, nil)

	FastPathProbeReset()
	_, ok := tryFastPathMsgUpdate(SkyADT{Tag: 0, SkyName: ctor, Fields: nil})
	if ok {
		t.Errorf("tryFastPathMsgUpdate(SkyADT, nil table) ok=true, want false")
	}
	eligible, fall := FastPathProbeStats()
	if eligible != 0 || fall != 1 {
		t.Errorf("probe stats = (e=%d, f=%d), want (0, 1)", eligible, fall)
	}
}

// itoaFast — local int → string without pulling strconv into the
// dispatch test surface.  Stays portable across the Go versions
// the runtime supports.
func itoaFast(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
