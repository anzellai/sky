//go:build !js

package rt

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

// A structural fixture with the exact pre-provenance exported fields. Gob's
// field matching decodes this old schema into today's SkyADT; it does not
// invent the new field. Test both directions explicitly.
type nominalOldADT struct {
	Tag     int
	SkyName string
	Fields  []any
}

func TestCodecNominalGobMigration(t *testing.T) {
	d := decimalBox(decimal.RequireFromString("12.5"))
	m, err := moneyFromJSONRaw(map[string]any{"amount": "12.5", "currency": "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	values := []SkyADT{d, m, {SkyOwner: "User.Price", SkyName: "Money", Fields: []any{"keep", 7}}}
	for _, v := range values {
		gobRegisterAll(v)
		var b bytes.Buffer
		if err := gob.NewEncoder(&b).Encode(v); err != nil {
			t.Fatal(err)
		}
		var restored SkyADT
		if err := gob.NewDecoder(&b).Decode(&restored); err != nil {
			t.Fatal(err)
		}
		if restored.SkyOwner != v.SkyOwner || nominalJSON(t, restored, "") != nominalJSON(t, v, "") {
			t.Fatalf("new Gob changed provenance/value: %+v", restored)
		}
		b.Reset()
		if err := gob.NewEncoder(&b).Encode(v); err != nil {
			t.Fatal(err)
		}
		var old nominalOldADT
		if err := gob.NewDecoder(&b).Decode(&old); err != nil {
			t.Fatal(err)
		}
		if old.SkyName != v.SkyName || len(old.Fields) != len(v.Fields) {
			t.Fatal("old Gob reader lost constructor fields")
		}
	}
	oldDecimal := nominalOldADT{SkyName: "Decimal__Internal", Fields: []any{decimal.RequireFromString("12.5")}}
	gobRegisterAll(oldDecimal)
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(oldDecimal); err != nil {
		t.Fatal(err)
	}
	var restored SkyADT
	if err := gob.NewDecoder(&b).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.SkyOwner != "" {
		t.Fatal("old Gob invented owner")
	}
	if _, err := codecAutoEncodeVal(reflect.ValueOf(restored), true); err == nil {
		t.Fatal("ambiguous old Gob silently encoded")
	}
	if got := nominalJSON(t, restored, "Std_Decimal_Decimal"); got != `"12.5"` {
		t.Fatal(got)
	}
	// Old nested Money boxes recover only under the trusted Money context.
	legacyMoney := nominalOldADT{SkyName: "Money", Fields: []any{restored, sqlCodeToCurrency("GBP")}}
	gobRegisterAll(legacyMoney)
	b.Reset()
	if err := gob.NewEncoder(&b).Encode(legacyMoney); err != nil {
		t.Fatal(err)
	}
	var oldMoney SkyADT
	if err := gob.NewDecoder(&b).Decode(&oldMoney); err != nil {
		t.Fatal(err)
	}
	if got := nominalJSON(t, oldMoney, "Std_Money_Money"); got != `{"amount":"12.5","currency":"GBP"}` {
		t.Fatal(got)
	}
}
