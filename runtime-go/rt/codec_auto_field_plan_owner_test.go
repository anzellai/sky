package rt

// Stage this test WITH the nominal-ADT owner patch. It verifies that the new
// schema-only plan never freezes dynamic owner reads or reconstructs fields.
import (
	"encoding/json"
	"reflect"
	"testing"
)

func codecFieldPlanClearOwner(alias string) {
	enumRegistryWriter.Lock()
	defer enumRegistryWriter.Unlock()
	previous := codecEnumRegistry()
	owners := make(map[string]string, len(previous.owners))
	for key, value := range previous.owners {
		if key != alias {
			owners[key] = value
		}
	}
	enumRegistry.Store(&codecEnumRegistrySnapshot{names: previous.names, owners: owners})
}
func TestCodecFieldPlanOwnerReadsRemainLiveOrPinned(t *testing.T) {
	const alias = "codec.fieldplan.owner"
	t.Cleanup(func() { codecFieldPlanEffect = nil; codecFieldPlanClearOwner(alias) })
	type row struct {
		First codecFieldPlanVariant `sky:"first"`
		Later int                   `sky:"later,codec.fieldplan.owner"`
	}
	for _, pinned := range []bool{false, true} {
		for _, planned := range []bool{false, true, true} {
			codecFieldPlanClearOwner(alias)
			snapshot := codecEnumRegistry()
			if !pinned {
				snapshot = nil
			}
			codecFieldPlanEffect = func() { RegisterAdtOwner("test.codec.fieldplan.owner", alias, skyMoneyOwner) }
			raw, err := codecFieldPlanEncode(reflect.ValueOf(row{}), nil, true, snapshot, false, planned)
			if !pinned {
				if err == nil {
					t.Fatal("live owner registration no longer changes later typed conversion")
				}
			} else {
				if err != nil {
					t.Fatal("pinned typed conversion reread live owners")
				}
				if _, err = json.Marshal(raw); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
