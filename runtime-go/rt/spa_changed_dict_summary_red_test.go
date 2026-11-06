package rt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// This is a performance RED with a passing canonical/publication oracle.
// It uses the existing encoded-byte counter, not proposed new instrumentation.
func TestSpaChangedDictSummaryEncodesOnlyReplacedPrimitiveValue(t *testing.T) {
	type model struct {
		Catalog SkyDict[string] `sky:"catalog,rt.SkyDict[string]"`
	}
	const capBytes = 4096
	value := strings.Repeat("x", 128)
	replacement := value + "y"
	dict := SkyDict[string]{}
	for i := 0; i < 256; i++ {
		dict = dict.Insert(fmt.Sprintf("k%04d", i), value)
	}
	old := model{Catalog: dict}
	next := model{Catalog: dict.Insert("k0128", replacement)}
	golden := func(changed bool) string {
		var b strings.Builder
		b.WriteString(`{"catalog":{`)
		for i := 0; i < 256; i++ {
			if i != 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `"k%04d":"`, i)
			b.WriteString(value)
			if changed && i == 128 {
				b.WriteByte('y')
			}
			b.WriteByte('"')
		}
		b.WriteString(`}}`)
		return b.String()
	}
	oldJSON, nextJSON := golden(false), golden(true)
	if len(oldJSON) != 35597 || len(nextJSON) != 35598 || spaClosedCanonical(old) != oldJSON || spaClosedCanonical(next) != nextJSON {
		t.Fatal("fixture does not match exact canonical 35597/35598-byte payloads")
	}
	refTrace, optTrace := []string{}, []string{}
	refEncoder, optEncoder := spaMixedTestEncoder(&refTrace), spaMixedTestEncoder(&optTrace)
	optEncoder.InputType = SpaModelInputType[model]()
	reference := &spaPublicationKV{values: memKV{spaPersistKey: "previous-snapshot"}}
	optimized := &spaPublicationKV{values: memKV{spaPersistKey: "previous-snapshot"}}
	refLife, optLife := spaPersistLife{}, spaPersistLife{}
	controller := spaMixedPersistence{encoder: optEncoder, profiling: true}
	referenceStep := func(previous, current model) bool {
		return spaPersistStep(previous, current, refEncoder.encode, func(blob string) {
			spaPersistWrite(reference, &refLife, blob, nil, capBytes)
		}, nil, nil)
	}
	referenceStep(old, old)
	controller.step(old, old, optEncoder.encode, optimized, &optLife, nil, nil, capBytes)
	if controller.cache == nil {
		t.Fatal("warm canonical Dict did not receive an existing mixed cache")
	}
	warmCache := controller.cache
	refTrace, optTrace = nil, nil
	refClear := referenceStep(old, next)
	optClear, work := controller.step(old, next, optEncoder.encode, optimized, &optLife, nil, nil, capBytes)
	if refClear || optClear || refLife != optLife || !reflect.DeepEqual(reference.values, optimized.values) || len(reference.calls) != 0 || len(optimized.calls) != 0 || optimized.values[spaPersistKey] != "previous-snapshot" {
		t.Fatal("changed oversized Dict did not preserve reference NoWrite and lifetime behavior")
	}
	if !reflect.DeepEqual(refTrace, []string{"prepare"}) || !reflect.DeepEqual(optTrace, []string{"prepare"}) || work.PreparedEncodes != 1 || work.PreparedFallbacks != 0 || work.Encodes != 0 || work.FieldModelSteps != 1 {
		t.Fatal("changed Dict bypassed strict preparation or used an unmeasured reference fallback")
	}
	if work.MaterializedBytes != 0 || work.BridgeBytes != 0 {
		t.Fatal("oversized NoWrite materialized a whole payload or bridge copy")
	}
	// Independently inspect the existing mixed result's exact selected length.
	// This logical invocation has its own mandatory strict preparation.
	optTrace = nil
	result := spaEncodeMixed(optEncoder, warmCache, next, spaMixedEncodeOptions{CapBytes: capBytes})
	if !result.Valid || result.Reference || result.NextCache == nil || result.EncodedBytes != 35598 || result.Payload != "" || result.Work.MaterializedBytes != 0 || !reflect.DeepEqual(optTrace, []string{"prepare"}) {
		t.Fatal("changed Dict summary did not match exact reference size and oversize decision")
	}
	// RED today: both counters report the full 35586-byte Dict encoding.
	// A replacement of this one already-keyed primitive needs only its 131-byte
	// JSON string; unchanged entries and the existing key must not be reencoded.
	const changedValueJSONBytes = 131
	if work.FieldEncodedBytes != changedValueJSONBytes || result.Work.EncodedBytes != changedValueJSONBytes {
		t.Fatalf("changed Dict reencoded retained entries: controller_bytes=%d mixed_bytes=%d want=%d", work.FieldEncodedBytes, result.Work.EncodedBytes, changedValueJSONBytes)
	}
}
