package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Remove only this synthetic alias and publish a fresh immutable epoch, rather
// than restoring an old snapshot pointer (which would manufacture an ABA hit).
func clearNominalEpochFixture(alias string) {
	enumRegistryWriter.Lock()
	defer enumRegistryWriter.Unlock()
	previous := codecEnumRegistry()
	next := &codecEnumRegistrySnapshot{names: previous.names, owners: make(map[string]string, len(previous.owners))}
	for key, value := range previous.owners {
		if key != alias {
			next.owners[key] = value
		}
	}
	enumRegistry.Store(next)
}

func TestAdtOwnerMetadataEpochAndPinnedCanonicalConversion(t *testing.T) {
	const alias = "sky.test.adt.owner.epoch.scalar"
	clearNominalEpochFixture(alias)
	t.Cleanup(func() { clearNominalEpochFixture(alias) })
	type scalar struct {
		V int `sky:"v,sky.test.adt.owner.epoch.scalar"`
	}
	value := scalar{V: 7}
	before := codecEnumRegistry()
	pinned, err := codecAutoEncodeValAt(reflect.ValueOf(value), true, before)
	if err != nil {
		t.Fatal(err)
	}
	RegisterAdtOwner("test.OwnerEpochScalar", alias, skyMoneyOwner)
	after := codecEnumRegistry()
	if before == after || before.owners[alias] != "" || after.owners[alias] != skyMoneyOwner {
		t.Fatal("owner registration did not publish an immutable metadata epoch")
	}
	again, err := codecAutoEncodeValAt(reflect.ValueOf(value), true, before)
	if err != nil {
		t.Fatal("pinned conversion reread live owner registry:", err)
	}
	a, _ := json.Marshal(pinned)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("pinned owner conversion changed")
	}
	if _, err := codecAutoEncodeValAt(reflect.ValueOf(value), true, nil); err == nil {
		t.Fatal("default nil conversion stopped reading live owners")
	}
	RegisterEnum("sky.test.adt.owner.enum.copy", []string{"synthetic"})
	if codecEnumRegistry().owners[alias] != skyMoneyOwner {
		t.Fatal("enum update dropped owner metadata")
	}
}

func TestAdtOwnerEpochInvalidatesClosedCacheAndPinsLazyReconstruction(t *testing.T) {
	const alias = "sky.test.adt.owner.epoch.lazy"
	clearNominalEpochFixture(alias)
	t.Cleanup(func() { clearNominalEpochFixture(alias) })
	type scalar struct {
		V string `sky:"v,sky.test.adt.owner.epoch.lazy"`
	}
	value := scalar{V: strings.Repeat("x", 100000)}
	rv := reflect.ValueOf(value)
	before := codecEnumRegistry()
	options := spaClosedEncodingOptions{NeedRaw: true}
	warmBudget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	warmBudget.BytesLeft = 0
	warm := spaEncodeClosed(rv, reflect.Value{}, nil, "", nil, before, options, warmBudget)
	if !warm.Supported || warm.ConversionErr != nil || warm.MarshalErr != nil || warm.Cache == nil || warm.Cache.children[0].raw != "" {
		t.Fatal("warm fixture did not establish successful uncached-raw closed leaf")
	}
	lazyBudget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
	lazy := spaEncodeClosed(rv, rv, warm.Cache, "", before, before, options, lazyBudget)
	if lazy.ConversionErr != nil || lazy.Work.Conversions != 0 || lazyBudget.Pins.count != 1 {
		t.Fatal("fixture did not defer unchanged closed conversion")
	}
	// This models an unknown effect after Phase A, before Phase B materialises.
	RegisterAdtOwner("test.OwnerEpochLazy", alias, skyMoneyOwner)
	payload, ok := lazy.materialize(spaPersistMaxBytes)
	if !ok || payload != "{\"v\":\""+value.V+"\"}" || lazyBudget.Deferred.Conversions != 1 {
		t.Fatal("lazy reconstruction reread post-effect owner metadata")
	}
	current := codecEnumRegistry()
	next := spaEncodeClosed(rv, rv, warm.Cache, "", before, current, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
	if next.ConversionErr == nil || next.Work.Reuses != 0 {
		t.Fatal("changed owner epoch reused successful scalar certificate")
	}
	if _, err := codecAutoEncodeValAt(rv, true, nil); err == nil {
		t.Fatal("canonical post-effect conversion must now fail")
	}
}

func TestAdtOwnerNominalDispatchPrecedesClosedCompositeAndNothing(t *testing.T) {
	type composite struct {
		N int `sky:"n,int"`
	}
	for _, parent := range []string{skyDecimalOwner, skyMoneyOwner, skyCurrencyOwner} {
		for _, value := range []any{composite{N: 7}, Nothing[int]()} {
			alias := "sky.test.owner.composite." + parent + "." + reflect.TypeOf(value).String()
			clearNominalEpochFixture(alias)
			t.Cleanup(func() { clearNominalEpochFixture(alias) })
			before := codecEnumRegistry()
			rv := reflect.ValueOf(value)
			canonicalBefore, err := codecAutoEncodeTypedAt(rv, alias, true, before)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(canonicalBefore)
			if err != nil {
				t.Fatal(err)
			}
			options := spaClosedEncodingOptions{NeedRaw: true}
			budget := spaNewClosedEncodingBudget(spaPersistMaxBytes)
			budget.BytesLeft = 0
			warm := spaEncodeClosed(rv, reflect.Value{}, nil, alias, nil, before, options, budget)
			if !warm.Supported || warm.ConversionErr != nil || warm.MarshalErr != nil || warm.Cache == nil {
				t.Fatal("pre-registration composite fixture failed")
			}
			phaseA := spaEncodeClosed(rv, rv, warm.Cache, alias, before, before, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			if phaseA.ConversionErr != nil {
				t.Fatal(phaseA.ConversionErr)
			}
			// An unknown effect after A changes the alias to a builtin expected type.
			RegisterAdtOwner("test.OwnerComposite."+alias, alias, parent)
			after := codecEnumRegistry()
			_, canonicalError := codecAutoEncodeTypedAt(rv, alias, true, after)
			if canonicalError == nil {
				t.Fatal("canonical nominal conversion unexpectedly accepted foreign composite")
			}
			for _, prior := range []*spaClosedEncodingNode{nil, warm.Cache} {
				got := spaEncodeClosed(rv, rv, prior, alias, before, after, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
				if !got.Supported || got.ConversionErr == nil || got.ConversionErr.Error() != canonicalError.Error() || got.MarshalErr != nil {
					t.Fatalf("fresh/cached closed dispatch disagreed with canonical %T %s: %v vs %v", value, parent, got.ConversionErr, canonicalError)
				}
			}
			payload, ok := phaseA.materialize(spaPersistMaxBytes)
			if !ok || payload != string(expected) {
				t.Fatal("metadata changed after A rewrote composite/Nothing result")
			}
			// The pinned-before view must also remain valid if reconstruction is
			// initiated after the effect; live metadata must not choose dispatch.
			pinned := spaEncodeClosed(rv, rv, warm.Cache, alias, before, before, options, spaNewClosedEncodingBudget(spaPersistMaxBytes))
			payload, ok = pinned.materialize(spaPersistMaxBytes)
			if pinned.ConversionErr != nil || !ok || payload != string(expected) {
				t.Fatal("pinned composite/Nothing dispatch reread live owner")
			}
			clearNominalEpochFixture(alias)
		}
	}
}
