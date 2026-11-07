//go:build !js

package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthClaimsDoNotExposeAdtOwner(t *testing.T) {
	money, err := moneyFromJSONRaw(map[string]any{"amount": "12.5", "currency": "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	// signToken's Sky signature is polymorphic, so even an ADT root reaches the
	// reflected claims adapter. Exporting provenance must not add a JWT claim.
	claims := authClaimsToMap(money)
	if _, ok := claims["skyOwner"]; ok {
		t.Fatal("ADT owner became an Auth claim")
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "builtin:") || strings.Contains(string(encoded), "SkyOwner") {
		t.Fatal("nested provenance escaped through claims JSON")
	}
	// Ordinary user fields named skyOwner remain ordinary data; the exclusion
	// follows json:"-", not a user-visible field spelling.
	type ordinary struct{ SkyOwner string }
	if got := authClaimsToMap(ordinary{SkyOwner: "user data"})["skyOwner"]; got != "user data" {
		t.Fatal("ordinary claims field was suppressed")
	}
}

func TestAdtProvenanceIsNotRecordTableDataOrModelChange(t *testing.T) {
	value := SkyADT{SkyOwner: "sky:User.Value", SkyName: "Keep", Fields: []any{"payload", 7}}
	legacy := value
	legacy.SkyOwner = ""
	if hashAny(value) != hashAny(legacy) {
		t.Fatal("provenance changed logical model hash")
	}
	if _, ok := dbStructType(value); ok {
		t.Fatal("ADT metadata treated as table columns")
	}
	table := map[string]any{"Sample": value, "Name": "nominal", "Pk": "tag"}
	if _, ok := dbTableToSchemaMap(table); ok {
		t.Fatal("ADT provenance became DDL column")
	}
	if _, _, err := dbEncodeRecord(table, value); err == nil {
		t.Fatal("ADT provenance became stored row data")
	}
	if _, err := dbRowsToStructs(table, nil); err == nil {
		t.Fatal("row decoder accepted nominal metadata target")
	}
	// An ordinary record field with this spelling remains user data.
	type record struct{ SkyOwner string }
	a, b := record{SkyOwner: "a"}, record{SkyOwner: "b"}
	if hashAny(a) == hashAny(b) {
		t.Fatal("ordinary user skyOwner field stopped being logical state")
	}
	if _, ok := dbStructType(a); !ok {
		t.Fatal("ordinary record table rejected")
	}
}
