package rt

import (
	"errors"
	"strings"
	"testing"
)

// The v0.29.9 persistence cap and quota rules (spa_persist.go).
//
// The cap is 3,000,000 bytes. The smallest per-origin localStorage limit
// measured in WebKit, Firefox and Chromium (Playwright, 2026-11-10) is
// 5,241,856 UTF-16 code units, and a UTF-8 byte count is never smaller than
// the UTF-16 unit count of the same text, so a capped blob leaves at least
// 42% of the smallest quota free.
//
// A storage write can still fail (another key on the origin, a browser with a
// smaller quota, a full disk). spaKV.Set then returns the error, and the
// write rule keeps the previous stored value, warns once per page, counts the
// failure, and does not try again at or above the failed size until a smaller
// write succeeds.

// quotaKV is a memKV with a quota: a value longer than limit is refused with a
// QuotaExceededError, as window.localStorage.setItem does.
type quotaKV struct {
	values memKV
	limit  int
	sets   []int
	fail   error // when set, every Set fails with this error instead
}

func (q *quotaKV) Get(k string) (string, bool) { return q.values.Get(k) }
func (q *quotaKV) Remove(k string)             { q.values.Remove(k) }
func (q *quotaKV) Set(k, v string) error {
	q.sets = append(q.sets, len(v))
	if q.fail != nil {
		return q.fail
	}
	if len(v) > q.limit {
		return spaKVQuotaError{Name: "QuotaExceededError"}
	}
	return q.values.Set(k, v)
}

func quotaModel(n int) string {
	return `{"session":null,"data":"` + strings.Repeat("x", n) + `"}`
}

func captureQuotaWarnings(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := spaPersistWarn
	spaPersistWarn = func(msg string) { got = append(got, msg) }
	t.Cleanup(func() { spaPersistWarn = old })
	return &got
}

func TestSpaPersistCapIsThreeMillionBytes(t *testing.T) {
	if spaPersistMaxBytes != 3_000_000 {
		t.Fatalf("spaPersistMaxBytes = %d, want 3,000,000", spaPersistMaxBytes)
	}
}

func TestSpaPersistStoresATwoMegabyteModel(t *testing.T) {
	st := memKV{}
	life := spaPersistLife{}
	model := quotaModel(2_000_000)
	spaPersistWrite(st, &life, model, []string{"session"}, spaPersistMaxBytes)
	if got, ok := st.Get(spaPersistKey); !ok || got != model {
		t.Fatalf("a 2 MB model was not stored (stored %d bytes, present %v)", len(got), ok)
	}
	merged, useIt, _ := spaRestoreStored(st, "", []string{"session"}, []string{"session"}, spaPersistMaxBytes)
	if !useIt || merged != model {
		t.Fatalf("a stored 2 MB model did not restore (useIt %v, %d bytes)", useIt, len(merged))
	}

	// The reference step and the generated field step write it too.
	ref := &spaReferencePersist{st: memKV{}, life: &spaPersistLife{}, capBytes: spaPersistMaxBytes}
	spaPersistStepReference(nil, struct{}{}, func(any) string { return model }, ref, nil)
	if got, _ := ref.st.Get(spaPersistKey); got != model {
		t.Fatalf("the reference step did not store a 2 MB model (%d bytes)", len(got))
	}
	big := spaFieldsFixture(1)
	big.Text = strings.Repeat("y", 2_000_000)
	kv := &spaPublicationKV{values: memKV{}}
	fields := &spaFieldPersistence{}
	fields.step(big, big, spaProjectionEncode, kv, &spaPersistLife{}, nil, nil, spaPersistMaxBytes, true)
	if got, _ := kv.values.Get(spaPersistKey); len(got) < 2_000_000 || got != spaProjectionEncode(big) {
		t.Fatalf("the field step did not store a 2 MB model (%d bytes)", len(got))
	}
}

func TestSpaPersistQuotaKeepsPreviousValue(t *testing.T) {
	warnings := captureQuotaWarnings(t)
	before := spaPersistQuotaFailures()
	st := &quotaKV{values: memKV{}, limit: 1000}
	life := spaPersistLife{}
	small := quotaModel(100)
	spaPersistWrite(st, &life, small, []string{"session"}, spaPersistMaxBytes)
	if got, _ := st.Get(spaPersistKey); got != small {
		t.Fatalf("small model not stored")
	}

	// Over the quota: the old value stays byte for byte, one warning, one count.
	spaPersistWrite(st, &life, quotaModel(2000), []string{"session"}, spaPersistMaxBytes)
	if got, _ := st.Get(spaPersistKey); got != small {
		t.Fatalf("a failed write changed the stored value (%d bytes, want %d)", len(got), len(small))
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "quota") {
		t.Fatalf("warnings = %q, want one quota warning", *warnings)
	}
	if spaPersistQuotaFailures()-before != 1 {
		t.Fatalf("quota failure counter moved by %d, want 1", spaPersistQuotaFailures()-before)
	}

	// At or above the failed size the write is not tried again.
	tries := len(st.sets)
	spaPersistWrite(st, &life, quotaModel(2000), []string{"session"}, spaPersistMaxBytes)
	spaPersistWrite(st, &life, quotaModel(5000), []string{"session"}, spaPersistMaxBytes)
	if len(st.sets) != tries {
		t.Fatalf("writes at or above the failed size were tried (%d new Set calls)", len(st.sets)-tries)
	}

	// A smaller one is tried. It fails again: still one warning per page.
	spaPersistWrite(st, &life, quotaModel(1500), []string{"session"}, spaPersistMaxBytes)
	if len(st.sets) != tries+1 || len(*warnings) != 1 {
		t.Fatalf("smaller write: %d Set calls, %d warnings, want 1 and 1", len(st.sets)-tries, len(*warnings))
	}
	if got, _ := st.Get(spaPersistKey); got != small {
		t.Fatalf("a failed write changed the stored value")
	}

	// A write that fits succeeds and clears the size fence.
	fits := quotaModel(500)
	spaPersistWrite(st, &life, fits, []string{"session"}, spaPersistMaxBytes)
	if got, _ := st.Get(spaPersistKey); got != fits {
		t.Fatalf("a write under the quota was not stored")
	}
	st.limit = 1 << 20
	larger := quotaModel(3000)
	spaPersistWrite(st, &life, larger, []string{"session"}, spaPersistMaxBytes)
	if got, _ := st.Get(spaPersistKey); got != larger {
		t.Fatalf("after a success, a larger write was still fenced")
	}
}

func TestSpaPersistOtherStorageErrorIsNotASizeFence(t *testing.T) {
	warnings := captureQuotaWarnings(t)
	st := &quotaKV{values: memKV{}, limit: 1 << 20, fail: errors.New("SecurityError")}
	life := spaPersistLife{}
	spaPersistWrite(st, &life, quotaModel(10), nil, spaPersistMaxBytes)
	spaPersistWrite(st, &life, quotaModel(10), nil, spaPersistMaxBytes)
	if len(st.sets) != 2 {
		t.Fatalf("a non-quota error fenced later writes (%d Set calls, want 2)", len(st.sets))
	}
	if len(*warnings) != 1 || strings.Contains((*warnings)[0], "quota") {
		t.Fatalf("warnings = %q, want one non-quota warning", *warnings)
	}
}

// The step controllers keep running after a refused write: update continues,
// and the sign-out decision is unchanged.
func TestSpaPersistQuotaStepContinues(t *testing.T) {
	captureQuotaWarnings(t)
	st := &quotaKV{values: memKV{}, limit: 200}
	life := spaPersistLife{}
	signedIn := `{"session":"A","data":"` + strings.Repeat("x", 500) + `"}`
	signedOut := `{"session":null,"data":"` + strings.Repeat("x", 500) + `"}`
	ref := &spaReferencePersist{st: st, life: &life, protected: []string{"session"}, capBytes: spaPersistMaxBytes}
	encode := func(m any) string { return m.(string) }
	if spaPersistStepReference(signedIn, signedIn, encode, ref, nil) {
		t.Fatalf("no sign-out expected")
	}
	if !spaPersistStepReference(signedIn, signedOut, encode, ref, nil) {
		t.Fatalf("a refused write hid the sign-out")
	}
	if _, ok := st.Get(spaPersistKey); ok {
		t.Fatalf("nothing fitted the quota, so nothing must be stored")
	}
}
