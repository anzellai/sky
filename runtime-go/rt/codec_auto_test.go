package rt

import (
	"encoding/json"
	"testing"
)

// mirrors what a Sky record lowers to, with the S3 `sky:` field tags.
type acAddress struct {
	City string `sky:"city,string"`
	Zip  string `sky:"zip,string"`
}

type acUser struct {
	Id      string           `sky:"id,string"`
	Age     int              `sky:"age,int"`
	Active  bool             `sky:"active,bool"`
	Nick    SkyMaybe[string] `sky:"nick,rt.SkyMaybe[string]"`
	Address acAddress        `sky:"address,acAddress"`
	Tags    []string         `sky:"tags,[]string"`
}

func TestCodecAutoRoundTrip(t *testing.T) {
	u := acUser{
		Id: "u1", Age: 30, Active: true,
		Nick:    Just("ace"),
		Address: acAddress{City: "London", Zip: "E1"},
		Tags:    []string{"a", "b"},
	}

	// encode → JSON string
	encoded := Codec_autoEnc(true, u)
	jv, ok := encoded.(JsonValue)
	if !ok {
		t.Fatalf("autoEnc did not return a JsonValue: %T", encoded)
	}
	b, err := json.Marshal(jv.raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// decode via the reflection decoder
	dec := Codec_autoDecoder(true, acUser{}).(JsonDecoder)
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := dec.run(raw).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("decode failed: %+v", res)
	}
	back := res.OkValue.(acUser)

	if back.Id != "u1" || back.Age != 30 || !back.Active {
		t.Errorf("scalars wrong: %+v", back)
	}
	if back.Nick.Tag != 0 || back.Nick.JustValue != "ace" {
		t.Errorf("Maybe wrong: %+v", back.Nick)
	}
	if back.Address.City != "London" || back.Address.Zip != "E1" {
		t.Errorf("nested record wrong: %+v", back.Address)
	}
	if len(back.Tags) != 2 || back.Tags[0] != "a" || back.Tags[1] != "b" {
		t.Errorf("list wrong: %+v", back.Tags)
	}
}

func TestCodecAutoNothingAndEmpty(t *testing.T) {
	u := acUser{Id: "u2", Nick: Nothing[string](), Tags: []string{}}
	encoded := Codec_autoEnc(true, u).(JsonValue)
	b, _ := json.Marshal(encoded.raw)

	dec := Codec_autoDecoder(true, acUser{}).(JsonDecoder)
	var raw any
	json.Unmarshal(b, &raw)
	back := dec.run(raw).(SkyResult[any, any]).OkValue.(acUser)

	if back.Nick.Tag != 1 {
		t.Errorf("Nothing did not round-trip: %+v", back.Nick)
	}
	if len(back.Tags) != 0 {
		t.Errorf("empty list wrong: %+v", back.Tags)
	}
}

func TestCodecAutoCols(t *testing.T) {
	cols := AsList(Codec_autoCols(true, acUser{}))
	want := map[string]string{
		"id": "text", "age": "int", "active": "bool",
		"nick": "text?", "address": "blob", "tags": "blob", // nick is Maybe → nullable
	}
	if len(cols) != len(want) {
		t.Fatalf("cols count = %d, want %d", len(cols), len(want))
	}
	for _, c := range cols {
		tup := AsTuple2(c)
		name := AsString(tup.V0)
		kind := AsString(tup.V1)
		if want[name] != kind {
			t.Errorf("col %q kind = %q, want %q", name, kind, want[name])
		}
	}
}

func TestCodecAutoSnakeVsCamel(t *testing.T) {
	type multiWord struct {
		ItemName   string `sky:"itemName,string"`
		PriceMinor int    `sky:"priceMinor,int"`
	}
	snakeCols := AsList(Codec_autoCols(true, multiWord{}))
	camelCols := AsList(Codec_autoCols(false, multiWord{}))
	if AsString(AsTuple2(snakeCols[0]).V0) != "item_name" || AsString(AsTuple2(snakeCols[1]).V0) != "price_minor" {
		t.Errorf("auto(snake) cols = %v, want item_name/price_minor", snakeCols)
	}
	if AsString(AsTuple2(camelCols[0]).V0) != "itemName" || AsString(AsTuple2(camelCols[1]).V0) != "priceMinor" {
		t.Errorf("autoCamel cols = %v, want itemName/priceMinor", camelCols)
	}
}
