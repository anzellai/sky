package rt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

type nominalUserMoney struct {
	V0 string
	V1 int
}

func (nominalUserMoney) SkyVariantTag() int     { return 0 }
func (nominalUserMoney) SkyVariantName() string { return "Money" }

type nominalUserDecimal struct{ V0 string }

func (nominalUserDecimal) SkyVariantTag() int     { return 0 }
func (nominalUserDecimal) SkyVariantName() string { return "Decimal__Internal" }

func nominalJSON(t *testing.T, v any, declared string) string {
	t.Helper()
	var raw any
	var err error
	if declared == "" {
		raw, err = codecAutoEncodeVal(reflect.ValueOf(v), true)
	} else {
		raw, err = codecAutoEncodeTyped(reflect.ValueOf(v), declared, true)
	}
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCodecNominalConstructorCollisions(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{nominalUserMoney{"kept", 7}, `{"tag":"Money","v0":"kept","v1":7}`},
		{nominalUserDecimal{"kept"}, `{"tag":"Decimal__Internal","v0":"kept"}`},
		{SkyADT{SkyOwner: "User.Price", SkyName: "Money", Fields: []any{"kept", 7}}, `{"tag":"Money","v0":"kept","v1":7}`},
		{SkyADT{SkyOwner: "User.Exact", SkyName: "Decimal__Internal", Fields: []any{"kept"}}, `{"tag":"Decimal__Internal","v0":"kept"}`},
	}
	for _, c := range cases {
		if got := nominalJSON(t, c.v, ""); got != c.want {
			t.Errorf("%T: got %s want %s", c.v, got, c.want)
		}
		if got := nominalJSON(t, []any{c.v}, ""); got != "["+c.want+"]" {
			t.Errorf("nested %T: %s", c.v, got)
		}
	}
	for _, v := range []any{nominalUserMoney{}, nominalUserDecimal{}} {
		s := codecSchemaType(reflect.TypeOf(v), true, 0)
		if s.exact || s.json != "{}" {
			t.Errorf("%T acquired builtin schema: %+v", v, s)
		}
	}
}

func TestCodecNominalBuiltinsAndDeclaredRecovery(t *testing.T) {
	d := decimalBox(decimal.RequireFromString("12.50"))
	m, err := moneyFromJSONRaw(map[string]any{"amount": "12.50", "currency": "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	if d.SkyOwner != skyDecimalOwner || m.SkyOwner != skyMoneyOwner {
		t.Fatal("runtime producers lost provenance")
	}
	if got := nominalJSON(t, d, ""); got != `"12.5"` {
		t.Fatal(got)
	}
	if got := nominalJSON(t, m, ""); got != `{"amount":"12.5","currency":"GBP"}` {
		t.Fatal(got)
	}
	d.SkyOwner = ""
	m.SkyOwner = ""
	m.Fields = []any{d, m.Fields[1]}
	if got := nominalJSON(t, d, "Std_Decimal_Decimal"); got != `"12.5"` {
		t.Fatal(got)
	}
	if got := nominalJSON(t, m, "Std_Money_Money"); got != `{"amount":"12.5","currency":"GBP"}` {
		t.Fatal(got)
	}
	// Recovery must survive the recursive declared container/record path.
	type oldRow struct {
		Prices []SkyADT `sky:"prices,[]Std_Money_Money"`
	}
	if got := nominalJSON(t, oldRow{[]SkyADT{m}}, ""); got != `{"prices":[{"amount":"12.5","currency":"GBP"}]}` {
		t.Fatal(got)
	}
	if d.SkyOwner != "" || m.SkyOwner != "" {
		t.Fatal("encoding mutated legacy values")
	}
	for _, v := range []any{d, m, []any{m}} {
		_, err := codecAutoEncodeVal(reflect.ValueOf(v), true)
		if err == nil || !strings.Contains(err.Error(), "legacy ADT provenance missing") {
			t.Fatalf("ambiguous %T: %v", v, err)
		}
	}
	conflict := SkyADT{SkyOwner: "User.Price", SkyName: "Money", Fields: []any{"keep", 7}}
	if _, err := codecAutoEncodeTyped(reflect.ValueOf(conflict), "Std_Money_Money", true); err == nil {
		t.Fatal("trusted context overwrote conflicting owner")
	}
	for _, v := range []SkyADT{
		{SkyOwner: skyDecimalOwner, SkyName: "Decimal__Internal", Fields: []any{"not decimal storage"}},
		{SkyOwner: skyDecimalOwner, SkyName: "Other"},
		{SkyOwner: skyMoneyOwner, SkyName: "Money", Fields: []any{}},
	} {
		if _, err := codecAutoEncodeVal(reflect.ValueOf(v), true); err == nil {
			t.Fatalf("malformed builtin silently encoded: %+v", v)
		}
	}
}

func TestCodecNominalLegacyFailureClassified(t *testing.T) {
	defer func() {
		p := recover()
		if p == nil {
			t.Fatal("legacy ambiguous value did not fail")
		}
		msg := fmt.Sprint(p)
		kind, _ := loudPanicClass(msg)
		if kind != "JsonEncodeFailure" || !strings.Contains(msg, "legacy ADT provenance missing") {
			t.Fatalf("%s: %s", kind, msg)
		}
	}()
	Codec_autoEnc(true, SkyADT{SkyName: "Money", Fields: []any{"not builtin", 7}})
}

func TestAdtWireResolvedOwner(t *testing.T) {
	const key = "test.NominalBag"
	RegisterAdtOwner(key, "NominalBag", "sky:Collision.User")
	RegisterAdtOwner(key, "NominalBag", "sky:Collision.User") // repeated generated package registration
	RegisterAdtTag(key, "Money", 2)
	v, ok := BuildAdtFromWire(key, "Money", []json.RawMessage{json.RawMessage(`"keep"`), json.RawMessage(`7`)}, -1)
	if !ok || v.(SkyADT).SkyOwner != "sky:Collision.User" {
		t.Fatalf("wire lost resolved owner: %+v", v)
	}
	if got := nominalJSON(t, v, ""); got != `{"tag":"Money","v0":"keep","v1":7}` {
		t.Fatal(got)
	}
	v, ok = BuildAdtFromWire("test.Unregistered", "Money", nil, 2)
	if !ok || v.(SkyADT).SkyOwner != "" {
		t.Fatal("localTag fabricated nominal provenance")
	}
	calls := 0
	RegisterAdtOwner("test.NominalFactory", "NominalFactory", "sky:User.Factory")
	RegisterAdtVariant("test.NominalFactory", "Money", func([]JsonRawMessage) any { calls++; return nominalUserMoney{"once", 1} })
	v, ok = BuildAdtFromWire("test.NominalFactory", "Money", nil, -1)
	if !ok || calls != 1 {
		t.Fatalf("factory count %d", calls)
	}
	if _, ok := v.(nominalUserMoney); !ok {
		t.Fatalf("factory result replaced: %T", v)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("conflicting owner accepted")
		}
	}()
	RegisterAdtOwner(key, "NominalBag", "sky:Other.User")
}

func TestAdtOwnerIsNotJSONInput(t *testing.T) {
	var value SkyADT
	if err := json.Unmarshal([]byte(`{"SkyOwner":"Std.Money.Money","SkyName":"Money","Tag":0,"Fields":["keep",7]}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.SkyOwner != "" {
		t.Fatal("JSON supplied trusted ADT owner")
	}
	d := decimalBox(decimal.RequireFromString("1"))
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SkyOwner") || strings.Contains(string(b), skyDecimalOwner) {
		t.Fatal("owner changed public JSON wire shape")
	}
}

func TestAdtOwnerDoesNotChangeUnionOperations(t *testing.T) {
	legacy := SkyADT{Tag: 1, SkyName: "Keep", Fields: []any{"payload", 7}}
	current := legacy
	current.SkyOwner = "User.Value"
	if !AsBool(Eq(legacy, current)) || !deepEq([]any{legacy}, []any{current}) {
		t.Fatal("provenance changed logical equality")
	}
	if cmp(legacy, current) != 0 || cmp(current, legacy) != 0 {
		t.Fatal("provenance changed union ordering")
	}
	if identityKey(legacy) != identityKey(current) {
		t.Fatal("provenance changed Set/cache identity")
	}
	if SkyShow(legacy) != SkyShow(current) || strings.Contains(SkyShow(current), current.SkyOwner) {
		t.Fatal("provenance leaked into show")
	}
	name, tag, fields, ok := unwrapADTShape(current)
	if !ok || name != "Keep" || tag != 1 || len(fields) != 2 {
		t.Fatal("provenance changed variant shape")
	}
	// Genuine values restored from old Gob must also compare equal to current
	// branded boxes; neither the outer nor nested provenance is logical data.
	d := decimalBox(decimal.RequireFromString("12.5"))
	oldD := d
	oldD.SkyOwner = ""
	m := SkyADT{SkyOwner: skyMoneyOwner, SkyName: "Money", Fields: []any{d, sqlCodeToCurrency("GBP")}}
	oldM := SkyADT{SkyName: "Money", Fields: []any{oldD, sqlCodeToCurrency("GBP")}}
	if !AsBool(Eq(m, oldM)) {
		t.Fatal("provenance changed restored Money equality")
	}
}

func init() {
	RegisterAdtOwner("test.CanonicalDecimal", "Std_Decimal_Decimal", skyDecimalOwner)
	RegisterAdtOwner("test.CanonicalMoney", "Std_Money_Money", skyMoneyOwner)
	RegisterAdtOwner("test.CanonicalCurrency", "Std_Money_Currency", skyCurrencyOwner)
}

func TestCodecDeclaredBuiltinRequiresRegisteredGrant(t *testing.T) {
	const shadow = "SyntheticShadow_Money"
	RegisterAdtOwner("test.Shadow", shadow, "sky:Std.Money.Money")
	value := SkyADT{SkyOwner: "sky:Std.Money.Money", SkyName: "Money", Fields: []any{"keep", 7}}
	if codecAutoDeclaredReadable(shadow) {
		t.Fatal("module/type spelling granted builtin reader")
	}
	if got := nominalJSON(t, value, shadow); got != `{"tag":"Money","v0":"keep","v1":7}` {
		t.Fatal(got)
	}
	if s := codecSchemaTyped(reflect.TypeOf(value), shadow, true, 0); s.exact || s.json != "{}" {
		t.Fatalf("shadow acquired builtin schema: %+v", s)
	}
	if _, err := codecAutoDecodeTyped(reflect.TypeOf(value), shadow, map[string]any{"amount": "1", "currency": "GBP"}, true); err == nil {
		t.Fatal("shadow acquired builtin decoder")
	}
	ownerless := value
	ownerless.SkyOwner = ""
	if _, err := codecAutoEncodeTyped(reflect.ValueOf(ownerless), shadow, true); err == nil {
		t.Fatal("untrusted declared alias recovered ambiguous legacy value")
	}
}

func TestAdtOwnerCannotBecomeTupleOrOverridePayload(t *testing.T) {
	value := decimalBox(decimal.RequireFromString("12.5"))
	const tuple = "rt.T4[int,string,[]any,string]"
	encoded, err := codecAutoEncodeTyped(reflect.ValueOf(value), tuple, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"12.5"` {
		t.Fatalf("forged tuple tag reinterpreted nominal fields: %s", b)
	}
	args := codecDeclaredTupleArgs(tuple)
	if _, err := codecAutoEncodeTuple(reflect.ValueOf(value), args, true); err == nil {
		t.Fatal("tuple helper exposed nominal fields")
	}
	raw := map[string]any{"tag": 0, "sky_name": "Money", "fields": []any{}, "sky_owner": skyMoneyOwner}
	if _, err := codecAutoDecodeTyped(reflect.TypeOf(value), tuple, raw, true); err == nil {
		t.Fatal("tuple metadata assigned owner from JSON")
	}
	if _, err := codecAutoDecodeTuple(reflect.TypeOf(value), args, raw, true, reflect.Value{}); err == nil {
		t.Fatal("tuple helper assigned owner from JSON")
	}
	// autoWith also accepts polymorphic root witnesses. Nominal roots retain
	// canonical encoding; their unsupported reflective record decoder refuses.
	viaOverride := Codec_autoEncOverrides(true, []any{}, value).(JsonValue)
	b, err = json.Marshal(viaOverride.raw)
	if err != nil || string(b) != `"12.5"` {
		t.Fatalf("autoWith leaked nominal fields: %s %v", b, err)
	}
	decoded := Codec_autoDecoderOverrides(true, []any{}, value).(JsonDecoder).run(raw).(SkyResult[any, any])
	if decoded.Tag == 0 {
		t.Fatal("autoWith record decoder assigned owner from JSON")
	}
}

type nominalUserCurrencyRaw struct{ V0 string }

func (nominalUserCurrencyRaw) SkyVariantTag() int     { return 0 }
func (nominalUserCurrencyRaw) SkyVariantName() string { return "CurrencyRaw" }

type nominalUserUSD struct{}

func (nominalUserUSD) SkyVariantTag() int     { return 0 }
func (nominalUserUSD) SkyVariantName() string { return "USD" }

func TestCodecCurrencyRejectsExplicitConflictsAndSealedLookalikes(t *testing.T) {
	good, err := moneyFromJSONRaw(map[string]any{"amount": "12.5", "currency": "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	badCurrencies := []any{
		SkyADT{SkyOwner: "sky:User.Currency", SkyName: "CurrencyRaw", Fields: []any{"GBP"}},
		SkyADT{SkyOwner: "sky:Std.Money.Currency", SkyName: "GBP"},
		SkyADT{SkyOwner: skyMoneyOwner, SkyName: "GBP"},
		nominalUserCurrencyRaw{V0: "GBP"}, nominalUserUSD{},
	}
	for _, currency := range badCurrencies {
		if _, err := codecAutoEncodeTyped(reflect.ValueOf(currency), "Std_Money_Currency", true); err == nil {
			t.Fatalf("declared Currency accepted %T with foreign provenance", currency)
		}
		bad := good
		bad.Fields = []any{good.Fields[0], currency}
		if _, err := codecAutoEncodeVal(reflect.ValueOf(bad), true); err == nil {
			t.Fatalf("Money silently consumed foreign Currency %T", currency)
		}
	}
	for _, currency := range []SkyADT{
		{SkyOwner: skyCurrencyOwner, SkyName: "CurrencyRaw"},
		{SkyOwner: skyCurrencyOwner, SkyName: "CurrencyRaw", Fields: []any{7}},
		{SkyOwner: skyCurrencyOwner, SkyName: "GBP", Fields: []any{"extra"}},
		{SkyOwner: skyCurrencyOwner, SkyName: "UnknownConstructor"},
	} {
		if _, err := codecAutoEncodeTyped(reflect.ValueOf(currency), "Std_Money_Currency", true); err == nil {
			t.Fatalf("malformed nominal Currency encoded: %+v", currency)
		}
	}
	// Context-free user variants keep their general tagged representation.
	if got := nominalJSON(t, nominalUserCurrencyRaw{V0: "GBP"}, ""); got != `{"tag":"CurrencyRaw","v0":"GBP"}` {
		t.Fatal(got)
	}
}

func TestCodecCurrencyNominalProducersLegacyAndRoundTrips(t *testing.T) {
	for _, code := range []string{"GBP", "USDT", "XYZ"} {
		currency := sqlCodeToCurrency(code)
		if currency.SkyOwner != skyCurrencyOwner {
			t.Fatal("runtime Currency producer lost provenance")
		}
		if got := nominalJSON(t, currency, "Std_Money_Currency"); got != `"`+code+`"` {
			t.Fatal(got)
		}
		decoded, err := codecAutoDecodeTyped(reflect.TypeOf(currency), "Std_Money_Currency", code, true)
		if err != nil {
			t.Fatal(err)
		}
		restored := decoded.Interface().(SkyADT)
		if restored.SkyOwner != skyCurrencyOwner || nominalJSON(t, restored, "Std_Money_Currency") != `"`+code+`"` {
			t.Fatal("Currency round-trip lost builtin identity/code")
		}
		// Trusted declared contexts recover old Currency boxes nonmutatingly.
		legacy := currency
		legacy.SkyOwner = ""
		if got := nominalJSON(t, legacy, "Std_Money_Currency"); got != `"`+code+`"` || legacy.SkyOwner != "" {
			t.Fatal("legacy Currency recovery changed")
		}
		money := SkyADT{SkyOwner: skyMoneyOwner, SkyName: "Money", Fields: []any{decimalBox(decimal.RequireFromString("12.5")), legacy}}
		if got := nominalJSON(t, money, ""); got != `{"amount":"12.5","currency":"`+code+`"}` {
			t.Fatal(got)
		}
	}
	// Preserve CurrencyRaw's previous writer semantics, including values its
	// strict reader intentionally refuses. Do not replace this with a grammar
	// check or canonicalise a raw constructor to a named constructor on encode.
	for _, code := range []string{"usd", "xYz", "zz", "", "💰", "bad-code"} {
		currency := SkyADT{SkyOwner: skyCurrencyOwner, Tag: len(currencyOrder), SkyName: "CurrencyRaw", Fields: []any{code}}
		raw, err := codecAutoEncodeTyped(reflect.ValueOf(currency), "Std_Money_Currency", true)
		if err != nil || raw != strings.ToUpper(code) {
			t.Fatalf("CurrencyRaw %q writer semantics changed: %v %v", code, raw, err)
		}
	}
	// Adding provenance does not expand context-free Currency formatting.
	if got := nominalJSON(t, sqlCodeToCurrency("GBP"), ""); got != `{"tag":"GBP"}` {
		t.Fatal(got)
	}
	if got := nominalJSON(t, sqlCodeToCurrency("usd"), ""); got != `{"tag":"CurrencyRaw","v0":"usd"}` {
		t.Fatal(got)
	}
	type row struct {
		Price SkyADT `sky:"price,Std_Money_Money"`
	}
	original, err := moneyFromJSONRaw(map[string]any{"amount": "12.5", "currency": "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := nominalJSON(t, row{original}, "")
	var raw any
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		t.Fatal(err)
	}
	decoded := Codec_autoDecoder(true, row{}).(JsonDecoder).run(raw).(SkyResult[any, any])
	if decoded.Tag != 0 {
		t.Fatalf("Money round-trip failed: %v", decoded.ErrValue)
	}
	restored := decoded.OkValue.(row).Price
	if restored.SkyOwner != skyMoneyOwner || restored.Fields[0].(SkyADT).SkyOwner != skyDecimalOwner || restored.Fields[1].(SkyADT).SkyOwner != skyCurrencyOwner || nominalJSON(t, row{restored}, "") != encoded {
		t.Fatal("Money round-trip lost nested nominal provenance")
	}
}

func TestCodecRegisteredAliasRemainsPartialThroughContainers(t *testing.T) {
	const alias = "App_CurrencyAlias"
	RegisterAdtOwner("test.CurrencyAlias", alias, skyCurrencyOwner)
	for _, declared := range []string{alias, "[]" + alias, "rt.SkyMaybe[" + alias + "]", "rt.SkyDict[" + alias + "]", "map[string]" + alias, "rt.T2[int, " + alias + "]", "rt.SkyMaybe[rt.T3[int, []" + alias + ", bool]]", "App_GenericBox[" + alias + "]", "[4]" + alias} {
		if !codecAutoDeclaresPartial(declared) {
			t.Fatalf("registered alias lost derived partial check in %s", declared)
		}
	}
	if codecAutoDeclaresPartial("Unregistered_Std_Money_Money_suffix") {
		t.Fatal("a substring acquired builtin partiality")
	}
	type row struct {
		Currency SkyADT `sky:"currency,App_CurrencyAlias"`
	}
	columns := AsList(Codec_autoCols(true, row{}))
	if len(columns) != 1 || !strings.Contains(columns[0].(T2[any, any]).V1.(string), "|check") {
		t.Fatal("registered alias column lost write check")
	}
	// Its permissive writer still emits ZZ; the strict reader must refuse it,
	// which is the reason alias columns require the same write check.
	invalid := sqlCodeToCurrency("zz")
	encoded := Codec_autoEnc(true, row{invalid}).(JsonValue)
	b, err := json.Marshal(encoded.raw)
	if err != nil {
		t.Fatal(err)
	}
	var wire any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	decoded := Codec_autoDecoder(true, row{}).(JsonDecoder).run(wire).(SkyResult[any, any])
	if decoded.Tag == 0 {
		t.Fatal("registered Currency alias accepted invalid raw code")
	}
	valid, err := json.Marshal(Codec_autoEnc(true, row{sqlCodeToCurrency("GBP")}).(JsonValue).raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(valid, &wire); err != nil {
		t.Fatal(err)
	}
	accepted := Codec_autoDecoder(true, row{}).(JsonDecoder).run(wire).(SkyResult[any, any])
	if accepted.Tag != 0 || accepted.OkValue.(row).Currency.SkyOwner != skyCurrencyOwner {
		t.Fatal("registered alias reader must accept genuine Currency")
	}
}

func TestCodecDeclaredUserOwnerRejectsBuiltinValuesSymmetrically(t *testing.T) {
	const alias = "User_Price"
	const owner = "sky:User.Price"
	RegisterAdtOwner("test.DeclaredUserPrice", alias, owner)
	money, err := moneyFromJSONRaw(map[string]any{"amount": "1", "currency": "GBP"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{decimalBox(decimal.RequireFromString("1")), money, sqlCodeToCurrency("GBP")} {
		if _, err := codecAutoEncodeTyped(reflect.ValueOf(value), alias, true); err == nil {
			t.Fatalf("user declaration silently consumed builtin %T", value)
		}
	}
	user := SkyADT{SkyOwner: owner, SkyName: "Money", Fields: []any{"keep", 7}}
	if got := nominalJSON(t, user, alias); got != `{"tag":"Money","v0":"keep","v1":7}` {
		t.Fatal(got)
	}
	legacy := user
	legacy.SkyOwner = ""
	if _, err := codecAutoEncodeTyped(reflect.ValueOf(legacy), alias, true); err == nil || legacy.SkyOwner != "" {
		t.Fatal("missing legacy user owner was guessed")
	}
}
