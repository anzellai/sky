package rt

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/shopspring/decimal"
)

const skyDecimalOwner = "builtin:Std.Decimal.Decimal"
const skyMoneyOwner = "builtin:Std.Money.Money"
const skyCurrencyOwner = "builtin:Std.Money.Currency"

// codecAutoEncodeNominalADT recognises builtin formatting only from resolved
// parent provenance. An exact declared field/container type can recover old
// Gob values, whose owner field is absent. A context-free reserved constructor
// with missing provenance is ambiguous and must never silently become zero or
// lose fields. No shape/name test authorises builtin identity.
func codecAutoEncodeNominalADT(rv reflect.Value, declared string) (any, bool, error) {
	return codecAutoEncodeNominalADTAt(rv, declared, nil)
}

func codecAutoEncodeNominalADTAt(rv reflect.Value, declared string, snapshot *codecEnumRegistrySnapshot) (any, bool, error) {
	expected := codecAutoDeclaredOwnerAt(declared, snapshot)
	if err := codecAutoCheckDeclaredOwner(rv, expected); err != nil {
		return nil, true, err
	}
	if expected != skyDecimalOwner && expected != skyMoneyOwner {
		expected = ""
	}
	return codecAutoEncodeNominalADTExpected(rv, expected)
}

func codecAutoEncodeNominalADTExpected(rv reflect.Value, expected string) (any, bool, error) {
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return nil, false, nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || !rv.CanInterface() {
		return nil, false, nil
	}
	adt, ok := rv.Interface().(SkyADT)
	if !ok {
		return nil, false, nil
	}
	owner := adt.SkyOwner
	if expected != "" {
		if owner != "" && owner != expected {
			return nil, true, fmt.Errorf("Codec.auto: ADT provenance %q conflicts with declared %s", owner, expected)
		}
		owner = expected // trusted compiler-emitted declared context only
	}
	if owner == "" && (adt.SkyName == "Decimal__Internal" || adt.SkyName == "Money") {
		return nil, true, fmt.Errorf("Codec.auto: legacy ADT provenance missing for reserved constructor %q; restore through its declared type or discard the old snapshot", adt.SkyName)
	}
	switch owner {
	case skyDecimalOwner:
		if adt.SkyName != "Decimal__Internal" || len(adt.Fields) != 1 {
			return nil, true, fmt.Errorf("Codec.auto: invalid nominal Decimal payload")
		}
		// Actual Decimal producers and Gob use decimal.Decimal. Do not invoke the
		// tolerant arithmetic unboxer here: it returns zero on a malformed box.
		d, ok := adt.Fields[0].(decimal.Decimal)
		if !ok {
			return nil, true, fmt.Errorf("Codec.auto: invalid nominal Decimal storage %T", adt.Fields[0])
		}
		return d.String(), true, nil
	case skyMoneyOwner:
		if adt.SkyName != "Money" || len(adt.Fields) != 2 {
			return nil, true, fmt.Errorf("Codec.auto: invalid nominal Money payload")
		}
		// A recovered old Money supplies a trusted declared type for its amount.
		amount, handled, err := codecAutoEncodeNominalADTExpected(reflect.ValueOf(adt.Fields[0]), skyDecimalOwner)
		if err != nil {
			return nil, true, err
		}
		if !handled {
			return nil, true, fmt.Errorf("Codec.auto: invalid nominal Money amount")
		}
		// Its actual Money parent also supplies a trusted Currency context.
		currency, err := codecAutoCurrencyCodeExpected(reflect.ValueOf(adt.Fields[1]))
		if err != nil {
			return nil, true, err
		}
		return jsonOrderedObject{keys: []string{"amount", "currency"}, vals: []any{amount, currency}}, true, nil
	default:
		return nil, false, nil
	}
}

// Called only for a source-granted declared Currency slot, or an actual
// builtin Money's Currency field. That expected type can recover legacy
// ownerless boxes, but never override explicit conflicting provenance or
// accept a sealed user variant with the same constructor spelling.
func codecAutoCurrencyCodeExpected(rv reflect.Value) (string, error) {
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return "", fmt.Errorf("Codec.auto: expected nominal Currency")
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || !rv.CanInterface() {
		return "", fmt.Errorf("Codec.auto: expected nominal Currency")
	}
	currency, ok := rv.Interface().(SkyADT)
	if !ok {
		return "", fmt.Errorf("Codec.auto: expected nominal Currency, got %s", rv.Type())
	}
	if currency.SkyOwner != "" && currency.SkyOwner != skyCurrencyOwner {
		return "", fmt.Errorf("Codec.auto: Currency provenance %q conflicts with expected %s", currency.SkyOwner, skyCurrencyOwner)
	}
	if currency.SkyName == "CurrencyRaw" {
		if len(currency.Fields) != 1 {
			return "", fmt.Errorf("Codec.auto: invalid nominal CurrencyRaw payload")
		}
		code, ok := currency.Fields[0].(string)
		if !ok {
			return "", fmt.Errorf("Codec.auto: invalid nominal CurrencyRaw storage %T", currency.Fields[0])
		}
		// Rendering is deliberately as permissive as the existing writer. The
		// strict reader and derived write check remain responsible for grammar;
		// empty/short/non-ASCII raw codes must not acquire a new encode failure.
		return strings.ToUpper(code), nil
	}
	if _, ok := currencyTags[currency.SkyName]; !ok || len(currency.Fields) != 0 {
		return "", fmt.Errorf("Codec.auto: invalid nominal Currency constructor %q", currency.SkyName)
	}
	return currency.SkyName, nil
}

// Registered declared owners constrain explicit value provenance symmetrically.
// In particular a user parent must not silently accept/format a builtin box.
// Empty legacy owners are not guessed or rewritten under a user declaration.
func codecAutoCheckDeclaredOwner(rv reflect.Value, expected string) error {
	if expected == "" {
		return nil
	}
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || !rv.CanInterface() {
		return nil
	}
	if adt, ok := rv.Interface().(SkyADT); ok && adt.SkyOwner != "" && adt.SkyOwner != expected {
		return fmt.Errorf("Codec.auto: ADT provenance %q conflicts with declared %s", adt.SkyOwner, expected)
	}
	return nil
}
