//go:build !js

package rt

import (
	"reflect"
	"testing"
)

func TestDatabaseDoesNotTreatOrdinaryRecordsAsMaybe(t *testing.T) {
	value := nominalMaybeRecord{Tag: 0, JustValue: "payload", Extra: []string{"keep"}}
	if !reflect.DeepEqual(dbBindArg(value), value) {
		t.Fatal("database binding unwrapped an ordinary record")
	}
	if _, nullable := dbSchemaKindForField(nil, "ordinary", reflect.TypeOf(value)); nullable {
		t.Fatal("container-like record fields made a column nullable")
	}
	if _, nullable := dbSchemaKindForField(nil, "optional", reflect.TypeOf(SkyMaybe[string]{})); !nullable {
		t.Fatal("actual Maybe lost SQL nullability")
	}
	if dbBindArg(Nothing[string]()) != nil || dbBindArg(Just("kept")) != "kept" {
		t.Fatal("actual Maybe database binding changed")
	}
}
