// This executable imports the production runtime only. In particular, never
// import console_app or a test package: those can incidentally register SkyADT
// through their static type graphs and hide a missing kernel boot registration.
package main

import (
	"encoding/gob"
	"fmt"
	"os"

	"sky-app/rt"
)

type blankModel struct{ Value any }

func check(path string) error {
	// Simulate a generated blank model. Its static any field cannot reveal
	// the concrete carrier in the saved payload. No manual SkyADT registration.
	rt.RegisterSkyGobTypes([]any{blankModel{}})
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open fixture: %w", err)
	}
	defer file.Close()
	var model blankModel
	if err := gob.NewDecoder(file).Decode(&model); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	adt, ok := model.Value.(rt.SkyADT)
	if !ok || adt.Tag != 37 || adt.SkyName != "UserCarrier" || len(adt.Fields) != 5 {
		return fmt.Errorf("root carrier type, tag, name or field count changed")
	}
	if text, ok := adt.Fields[0].(string); !ok || text != "keep-original-text" {
		return fmt.Errorf("string field lost its type or value")
	}
	if number, ok := adt.Fields[1].(int64); !ok || number != 7 {
		return fmt.Errorf("integer field lost its exact int64 type or value")
	}
	if flag, ok := adt.Fields[2].(bool); !ok || !flag {
		return fmt.Errorf("boolean field lost its type or value")
	}
	list, ok := adt.Fields[3].([]any)
	if !ok || len(list) != 2 {
		return fmt.Errorf("list carrier changed")
	}
	if text, ok := list[0].(string); !ok || text != "tail" {
		return fmt.Errorf("list text changed")
	}
	if number, ok := list[1].(int); !ok || number != -3 {
		return fmt.Errorf("list exact int changed")
	}
	nested, ok := adt.Fields[4].(rt.SkyADT)
	if !ok || nested.Tag != 9 || nested.SkyName != "NestedUser" || len(nested.Fields) != 1 {
		return fmt.Errorf("nested carrier changed")
	}
	if text, ok := nested.Fields[0].(string); !ok || text != "inner" {
		return fmt.Errorf("nested field changed")
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "ADT_GOB_PROBE_ERR: expected one fixture path")
		os.Exit(1)
	}
	if err := check(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "ADT_GOB_PROBE_ERR:", err)
		os.Exit(1)
	}
	fmt.Println("ADT_GOB_PROBE_OK")
}
