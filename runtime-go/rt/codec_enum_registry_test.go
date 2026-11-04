package rt

import (
	"sync"
	"testing"
)

func TestEnumRegistrationOwnsNamesAndPublishesImmutableSnapshots(t *testing.T) {
	const key = "enumSnapshotOwnershipTest"
	defer setEnumRegistration(key, nil, false)
	names := []string{"Before"}
	RegisterEnum(key, names)
	before := codecEnumRegistry()
	names[0] = "CallerMutation"
	if EnumName(key, 0) != "Before" {
		t.Fatal("caller mutation changed registered wire names")
	}
	RegisterEnum(key, []string{"After"})
	after := codecEnumRegistry()
	if before == after || before.names[key][0] != "Before" || after.names[key][0] != "After" {
		t.Fatal("registration mutated or reused a published snapshot")
	}
	RegisterEnum(key, []string{"After"})
	if codecEnumRegistry() == after {
		t.Fatal("identical registration reused an old certificate epoch")
	}
}

func TestEnumRegistrationDistinguishesAbsentAndEmpty(t *testing.T) {
	const key = "enumSnapshotEmptyTest"
	defer setEnumRegistration(key, nil, false)
	for _, names := range [][]string{nil, {}} {
		RegisterEnum(key, names)
		if !isRegisteredEnum(key) {
			t.Fatal("empty registration was treated as absent")
		}
		if _, ok := enumNameForOrdinal(key, 0); ok {
			t.Fatal("empty registration produced a constructor")
		}
	}
	setEnumRegistration(key, nil, false)
	if isRegisteredEnum(key) {
		t.Fatal("removed registration remained present")
	}
}

func TestEnumRegistryConcurrentPublicationAndReads(t *testing.T) {
	const key = "enumSnapshotConcurrentTest"
	defer setEnumRegistration(key, nil, false)
	RegisterEnum(key, []string{"Even"})
	var workers sync.WaitGroup
	workers.Add(5)
	go func() {
		defer workers.Done()
		for i := 0; i < 200; i++ {
			RegisterEnum(key, []string{"Even"})
			RegisterEnum(key, []string{"Odd"})
		}
	}()
	for i := 0; i < 4; i++ {
		go func() {
			defer workers.Done()
			for j := 0; j < 1000; j++ {
				name, ok := enumNameForOrdinal(key, 0)
				if !ok || (name != "Even" && name != "Odd") {
					t.Error("reader observed a partial enum registration")
					return
				}
				isRegisteredEnum(key)
				enumOrdinalForName(key, name)
			}
		}()
	}
	workers.Wait()
}
