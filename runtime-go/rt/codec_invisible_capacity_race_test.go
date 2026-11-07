package rt

import (
	"encoding/json"
	"testing"
)

type codecInvisibleCapacitySync struct{ handoff <-chan []any }

func (v codecInvisibleCapacitySync) MarshalJSON() ([]byte, error) {
	// The reference encoder synchronizes this producer before reading any
	// subsequent visible value. It NEVER reads the invisible capacity slot.
	grown := <-v.handoff
	if len(grown) != 2 || grown[1] != "ordinary unused capacity" {
		panic("bad race fixture handoff")
	}
	return []byte(`"value"`), nil
}

// Run with -race. RED for the spare-capacity hint candidate, GREEN for both
// the unchanged reference and the schema-key-only replacement. Appending to
// the disjoint, invisible slot is normal Go and does not race with reference
// reads of the visible slice elements; the value-level handoff is explicit.
func TestCodecOrderedObjectNeverReadsInvisibleCallerCapacity(t *testing.T) {
	values := make([]any, 1, 2)
	handoff := make(chan []any)
	values[0] = codecInvisibleCapacitySync{handoff: handoff}
	object := jsonOrderedObject{keys: []string{"dynamic"}, vals: values}
	go func() { grown := append(values, "ordinary unused capacity"); handoff <- grown }()
	encoded, err := json.Marshal(object)
	if err != nil || string(encoded) != `{"dynamic":"value"}` {
		t.Fatal("canonical object outcome changed")
	}
}
