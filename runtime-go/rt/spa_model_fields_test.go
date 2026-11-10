package rt

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

type spaFieldsModel struct {
	Z       string           `sky:"z,string"`
	Data    []string         `sky:"data,[]string"`
	Text    string           `sky:"text,string"`
	Session SkyMaybe[string] `sky:"sessionValue,rt.SkyMaybe[string]"`
}

func spaFieldsFixture(size int) spaFieldsModel {
	m := spaFieldsModel{Data: make([]string, size), Session: Nothing[string]()}
	value := strings.Repeat("x", 60)
	for i := range m.Data {
		m.Data[i] = value
	}
	return m
}

func spaFieldsFromOriginal(t *testing.T, model any, retain int) *spaModelFieldSnapshot {
	t.Helper()
	metadata := codecEnumRegistry()
	full := spaProjectionEncode(model)
	snapshot, ok := spaModelFieldsFromFull(model, full, metadata, retain)
	if !ok {
		t.Fatal("canonical fixture did not establish a closed field certificate")
	}
	return snapshot
}

func TestSpaModelFieldsSkipUnrelatedRetainedData(t *testing.T) {
	comparisonVisits := -1
	for _, size := range []int{1000, 10000, 100000} {
		old := spaFieldsFixture(size)
		snapshot := spaFieldsFromOriginal(t, old, spaPersistMaxBytes)
		next := old
		next.Text = "abcd"
		derived, work, ok := snapshot.derive(next)
		if !ok || !derived.valid || work.FieldEncodes != 1 || work.FieldEncodedBytes != 6 || work.RetainedBytes > spaPersistMaxBytes {
			t.Fatalf("size=%d: unexpected field work: %+v", size, work)
		}
		if comparisonVisits == -1 {
			comparisonVisits = work.ComparedNodes
		}
		if work.ComparedNodes != comparisonVisits {
			t.Fatal("identity comparison walked unrelated list elements")
		}
		full := spaProjectionEncode(next)
		if derived.encodedSize(derived.storedFields(nil, nil)) != uint64(len(full)) {
			t.Fatal("encoded size differs from the exact full encoder")
		}
		stored, oversized, materialized, ok := derived.storedJSON(nil, nil, spaPersistMaxBytes)
		if !ok {
			t.Fatal("valid field snapshot could not be planned")
		}
		if size == 100000 {
			if !oversized || stored != "" || materialized.MaterializedBytes != 0 || materialized.FieldEncodes != 0 {
				t.Fatal("oversized snapshot was materialized or re-encoded")
			}
		} else if oversized || stored != full || materialized.MaterializedBytes > spaPersistMaxBytes {
			t.Fatal("bounded materialization changed the original bytes")
		}
	}
}

func TestSpaModelFieldsMirrorExclusionOrderingAndProtectedAliases(t *testing.T) {
	m := spaFieldsFixture(1)
	m.Z = "<>é\n\u2028"
	m.Session = Just("synthetic")
	full := spaProjectionEncode(m)
	// Retain no raw field bytes: reconstruction still only needs selected,
	// bounded stored data and must use the same canonical field encoder.
	snapshot := spaFieldsFromOriginal(t, m, 0)
	for _, exclude := range [][]string{nil, {"absent"}, {"data"}, {"session_value", "data"}} {
		protected := []string{"sessionValue"}
		got, oversized, work, ok := snapshot.storedJSON(exclude, protected, spaPersistMaxBytes)
		want := spaPersistStripFields(full, exclude, protected)
		if !ok || oversized || got != want || work.MaterializedBytes != uint64(len(want)) {
			t.Fatal("field reconstruction differs from actual exclusion/ordering rules")
		}
	}
}

func TestSpaModelFieldsMatchCanonicalFloatEncoding(t *testing.T) {
	type model struct {
		Small  float32 `sky:"small,float32"`
		Signed float64 `sky:"signed,float64"`
	}
	old := model{}
	snapshot := spaFieldsFromOriginal(t, old, spaPersistMaxBytes)
	next := model{Small: 1.2, Signed: math.Copysign(0, -1)}
	derived, work, ok := snapshot.derive(next)
	if !ok || !derived.valid || work.FieldEncodes != 2 {
		t.Fatal("float changes were not encoded")
	}
	got, oversized, _, ok := derived.storedJSON(nil, nil, spaPersistMaxBytes)
	if !ok || oversized || got != spaProjectionEncode(next) {
		t.Fatal("float32 widening or signed zero diverged from the canonical encoder")
	}
}

func TestSpaModelFieldsChangedExcludedFailureInvalidatesWholeResult(t *testing.T) {
	type model struct {
		Data    []string `sky:"data,[]string"`
		Reading float64  `sky:"reading,float64"`
	}
	old := model{Data: spaFieldsFixture(100000).Data}
	snapshot := spaFieldsFromOriginal(t, old, spaPersistMaxBytes)
	next := old
	next.Reading = math.NaN()
	derived, _, ok := snapshot.derive(next)
	if !ok || derived.valid || spaProjectionEncode(next) != "" {
		t.Fatal("excluded invalid value did not invalidate the full encoding")
	}
	_, oversized, work, ok := derived.storedJSON([]string{"reading"}, nil, spaPersistMaxBytes)
	if ok || oversized || work.MaterializedBytes != 0 {
		t.Fatal("size/exclusion planning hid the full encoding failure")
	}
}

func TestSpaModelFieldsRejectChangedMetadataAndUnknownEncoders(t *testing.T) {
	m := spaFieldsFixture(1)
	snapshot := spaFieldsFromOriginal(t, m, spaPersistMaxBytes)
	RegisterEnum("spaFieldMetadataTest", []string{"Synthetic"})
	defer setEnumRegistration("spaFieldMetadataTest", nil, false)
	if _, _, ok := snapshot.derive(m); ok {
		t.Fatal("metadata changed without invalidating field certificates")
	}
	if _, _, _, ok := snapshot.storedJSON(nil, nil, spaPersistMaxBytes); ok {
		t.Fatal("stale metadata snapshot was materialized")
	}
	open := struct {
		Value any `sky:"value,any"`
	}{Value: 1}
	if _, ok := spaModelFieldsFromFull(open, spaProjectionEncode(open), codecEnumRegistry(), spaPersistMaxBytes); ok {
		t.Fatal("successful JSON alone granted a certificate to an open type")
	}
	if _, ok := spaModelFieldsFromFull(m, `{"data":[],"z":"","text":"","session_value":null}`, codecEnumRegistry(), spaPersistMaxBytes); ok {
		t.Fatal("noncanonical root order was accepted")
	}
}

func TestSpaModelFieldsLengthAccountingSaturates(t *testing.T) {
	snapshot := spaFieldsFromOriginal(t, spaFieldsFixture(0), spaPersistMaxBytes)
	snapshot.fields[0].size = math.MaxUint64
	if spaSizeAdd(math.MaxUint64, 1) != math.MaxUint64 || snapshot.encodedSize(snapshot.storedFields(nil, nil)) != math.MaxUint64 {
		t.Fatal("encoded size wrapped into a small snapshot")
	}
	got, oversized, work, ok := snapshot.storedJSON(nil, nil, spaPersistMaxBytes)
	if !ok || !oversized || got != "" || work.MaterializedBytes != 0 {
		t.Fatal("overflowed length triggered materialization")
	}
}

func TestSpaModelFieldsReusePayloadAndRefreshOwner(t *testing.T) {
	old := spaFieldsFixture(10000)
	largeOwner := strings.Repeat("x", 8*1024*1024)
	old.Text = largeOwner[:1]
	snapshot := spaFieldsFromOriginal(t, old, spaPersistMaxBytes)
	first, _, _, ok := snapshot.storedJSON(nil, nil, spaPersistMaxBytes)
	if !ok {
		t.Fatal("initial payload was not materialized")
	}
	next := old
	next.Text = strings.Clone(old.Text)
	derived, work, ok := snapshot.derive(next)
	if !ok || work.FieldEncodes != 0 || reflect.ValueOf(derived.model).FieldByName("Text").Pointer() != reflect.ValueOf(next.Text).Pointer() {
		t.Fatal("equal model did not retain an encoding certificate")
	}
	second, _, materialized, ok := derived.storedJSON(nil, nil, spaPersistMaxBytes)
	if !ok || first != second || materialized.MaterializedBytes != 0 || materialized.FieldEncodes != 0 {
		t.Fatal("unchanged stored fields repeated materialization")
	}
	// A context/configuration change must recompute the projection instead of
	// reusing a payload produced under different exclusion rules.
	projected, _, _, ok := derived.storedJSON([]string{"data"}, nil, spaPersistMaxBytes)
	if !ok || projected != spaPersistStripFields(spaProjectionEncode(next), []string{"data"}, nil) {
		t.Fatal("payload was reused across different persistence configuration")
	}
}

func TestSpaModelFieldsExcludedDataCannotEvictStoredRawBytes(t *testing.T) {
	type model struct {
		Ignored []string `sky:"ignored,[]string"`
		Stored  []string `sky:"stored,[]string"`
		Text    string   `sky:"text,string"`
	}
	old := model{Ignored: spaFieldsFixture(22000).Data, Stored: spaFieldsFixture(8000).Data}
	snapshot := spaFieldsFromOriginal(t, old, spaPersistMaxBytes)
	_, oversized, _, ok := snapshot.storedJSON([]string{"ignored"}, nil, spaPersistMaxBytes)
	if !ok || oversized {
		t.Fatal("stored projection must fit despite large excluded data")
	}
	next := old
	next.Text = "edit"
	derived, work, ok := snapshot.derive(next)
	if !ok || work.FieldEncodes != 1 || work.FieldEncodedBytes != 6 || derived.plan != snapshot.plan {
		t.Fatal("typing re-encoded unrelated data or rebuilt the immutable layout")
	}
	got, _, materialized, ok := derived.storedJSON([]string{"ignored"}, nil, spaPersistMaxBytes)
	if !ok || materialized.FieldEncodes != 0 || got != spaPersistStripFields(spaProjectionEncode(next), []string{"ignored"}, nil) {
		t.Fatal("greedy raw retention repeated an unchanged stored field encode")
	}
	if materialized.RetainedBytes > spaPersistMaxBytes || materialized.PayloadBytes > spaPersistMaxBytes {
		t.Fatal("raw or materialized payload retention exceeded its separate bound")
	}
}

func TestSpaModelFieldsShrinkingSnapshotRetainsReconstructedFields(t *testing.T) {
	type model struct {
		First  []string `sky:"first,[]string"`
		Second []string `sky:"second,[]string"`
		Text   string   `sky:"text,string"`
	}
	old := model{First: spaFieldsFixture(spaPersistMaxBytes / 68).Data, Second: spaFieldsFixture(spaPersistMaxBytes / 188).Data}
	snapshot := spaFieldsFromOriginal(t, old, spaPersistMaxBytes)
	if _, oversized, _, ok := snapshot.storedJSON(nil, nil, spaPersistMaxBytes); !ok || !oversized {
		t.Fatal("initial fixture must exceed the storage cap")
	}
	small := old
	small.First = old.First[:spaPersistMaxBytes/107]
	derived, _, ok := snapshot.derive(small)
	if !ok {
		t.Fatal("shrinking the first field lost the certificate")
	}
	if _, oversized, _, ok := derived.storedJSON(nil, nil, spaPersistMaxBytes); !ok || oversized {
		t.Fatal("the smaller fixture must fit the cap")
	}
	small.Text = "edit"
	again, work, ok := derived.derive(small)
	if !ok || work.FieldEncodes != 1 {
		t.Fatal("typing did not change exactly one field")
	}
	got, oversized, materialized, ok := again.storedJSON(nil, nil, spaPersistMaxBytes)
	if !ok || oversized || materialized.FieldEncodes != 0 || got != spaProjectionEncode(small) {
		t.Fatal("a once-missing raw field was re-encoded again on typing")
	}
}

func TestSpaModelFieldIdentityAndClearMatchReference(t *testing.T) {
	type model struct {
		Session SkyMaybe[string] `sky:"sessionValue,rt.SkyMaybe[string]"`
		Other   SkyMaybe[string] `sky:"other,rt.SkyMaybe[string]"`
		Large   []string         `sky:"large,[]string"`
		Number  float64          `sky:"number,float64"`
	}
	values := []SkyMaybe[string]{Nothing[string](), Just("synthetic-A"), Just("synthetic-B")}
	for _, protected := range [][]string{nil, {"sessionValue"}, {"session_value"}, {"session_value", "other"}, {"absent"}} {
		for _, from := range values {
			for _, to := range values {
				prev := model{Session: from, Other: Just("other"), Large: make([]string, 10000)}
				next := prev
				next.Session = to
				a := spaFieldsFromOriginal(t, prev, 0)
				b, _, ok := a.derive(next)
				if !ok {
					t.Fatal("closed derivation rejected")
				}
				got, ok := b.protectedIdentity(protected)
				want, referenceOK := spaIdentityOf(spaProjectionEncode(next), protected)
				if !ok || !referenceOK || got != want {
					t.Fatal("protected identity differs from full reference")
				}
				clear, ok := spaModelFieldsSessionCleared(a, b, protected)
				if !ok || clear != spaSessionClearedByStep(spaProjectionEncode(prev), spaProjectionEncode(next), protected) {
					t.Fatal("exact-key sign-out differs from full reference")
				}
				changed := next
				changed.Number = math.NaN()
				invalid, _, ok := b.derive(changed)
				if !ok || invalid.valid {
					t.Fatal("excluded invalid value must invalidate full certificate")
				}
				if _, ok := invalid.protectedIdentity(protected); ok {
					t.Fatal("invalid full model yielded identity")
				}
				if clear, ok := spaModelFieldsSessionCleared(invalid, b, protected); !ok || clear {
					t.Fatal("invalid previous full model triggered sign-out")
				}
			}
		}
	}
}

func TestSpaModelFieldIdentityCacheTracksOnlyProtectedOwners(t *testing.T) {
	m := spaFieldsFixture(10000)
	m.Session = Just("synthetic")
	a := spaFieldsFromOriginal(t, m, 0)
	protected := []string{"sessionValue"}
	id, ok := a.protectedIdentity(protected)
	if !ok || id == "" {
		t.Fatal("signed-in identity unavailable")
	}
	protected[0] = "mutated"
	next := m
	next.Text = "changed"
	b, _, ok := a.derive(next)
	if !ok || b.identity != a.identity || b.model.(spaFieldsModel).Text != "changed" {
		t.Fatal("unrelated change lost identity or retained obsolete owner")
	}
	if got, ok := b.protectedIdentity([]string{"sessionValue"}); !ok || got != id {
		t.Fatal("caller mutation altered cached configuration")
	}
	next.Session = Nothing[string]()
	c, _, _ := b.derive(next)
	if c.identity != nil {
		t.Fatal("changed session retained old identity")
	}
	if got, ok := c.protectedIdentity([]string{"sessionValue"}); !ok || got != "" {
		t.Fatal("sign-out retained old identity")
	}
	large := m
	large.Session = Just(strings.Repeat("x", 70000))
	d := spaFieldsFromOriginal(t, large, 0)
	if _, ok := d.protectedIdentity([]string{"sessionValue"}); ok {
		t.Fatal("unbounded protected projection must use full fallback")
	}
}
