package rt

// Std.Money — production-grade currency arithmetic. Uses Std.Decimal
// for exact math; pluggable FX-rate provider; ISO 4217 minor-unit
// awareness so JPY (0 dp), USD (2 dp), BHD (3 dp) round correctly
// at the boundary.
//
// Sky-side surface lives in sky-stdlib/Std/Money.sky. This file
// is the FFI boundary — Money_* primitives via RegisterPure.
//
// Money values are pure Sky ADT { amount : Decimal, currency : Currency }
// — no FFI boxing needed at the value level. The FFI primitives here
// handle: currency-property lookup (ISO 4217 minor units, symbols),
// fair-split allocation, formatting, and the FX rate registry.

import (
	"fmt"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

// ── ISO 4217 currency metadata ───────────────────────────────────
//
// Top ~30 currencies covering >95% of global commerce by trade
// volume, plus the IMF special-drawing-rights basket. The Sky
// `Currency` ADT is a sum type of these codes plus `CurrencyRaw
// String` for the long tail (any user-defined or rare code).
//
// Each entry records:
//   * minor: number of decimal places (currency precision)
//   * symbol: short display character (e.g. "$", "€", "¥")
//   * name: human-readable name for labels/dropdowns

type currencyInfo struct {
	Minor  int
	Symbol string
	Name   string
}

var currencyTable = map[string]currencyInfo{
	"USD":  {2, "$", "US Dollar"},
	"EUR":  {2, "€", "Euro"},
	"GBP":  {2, "£", "British Pound"},
	"JPY":  {0, "¥", "Japanese Yen"},
	"CNY":  {2, "¥", "Chinese Yuan"},
	"AUD":  {2, "A$", "Australian Dollar"},
	"CAD":  {2, "C$", "Canadian Dollar"},
	"CHF":  {2, "Fr.", "Swiss Franc"},
	"HKD":  {2, "HK$", "Hong Kong Dollar"},
	"SGD":  {2, "S$", "Singapore Dollar"},
	"NZD":  {2, "NZ$", "New Zealand Dollar"},
	"SEK":  {2, "kr", "Swedish Krona"},
	"NOK":  {2, "kr", "Norwegian Krone"},
	"DKK":  {2, "kr", "Danish Krone"},
	"PLN":  {2, "zł", "Polish Złoty"},
	"CZK":  {2, "Kč", "Czech Koruna"},
	"HUF":  {2, "Ft", "Hungarian Forint"},
	"RON":  {2, "lei", "Romanian Leu"},
	"BGN":  {2, "лв", "Bulgarian Lev"},
	"TRY":  {2, "₺", "Turkish Lira"},
	"ZAR":  {2, "R", "South African Rand"},
	"BRL":  {2, "R$", "Brazilian Real"},
	"MXN":  {2, "$", "Mexican Peso"},
	"ARS":  {2, "$", "Argentine Peso"},
	"CLP":  {0, "$", "Chilean Peso"},
	"INR":  {2, "₹", "Indian Rupee"},
	"PKR":  {2, "₨", "Pakistani Rupee"},
	"BDT":  {2, "৳", "Bangladeshi Taka"},
	"LKR":  {2, "₨", "Sri Lankan Rupee"},
	"NPR":  {2, "₨", "Nepalese Rupee"},
	"KRW":  {0, "₩", "South Korean Won"},
	"TWD":  {2, "NT$", "Taiwan Dollar"},
	"THB":  {2, "฿", "Thai Baht"},
	"VND":  {0, "₫", "Vietnamese Đồng"},
	"PHP":  {2, "₱", "Philippine Peso"},
	"IDR":  {2, "Rp", "Indonesian Rupiah"},
	"MYR":  {2, "RM", "Malaysian Ringgit"},
	"AED":  {2, "د.إ", "UAE Dirham"},
	"SAR":  {2, "﷼", "Saudi Riyal"},
	"QAR":  {2, "﷼", "Qatari Riyal"},
	"KWD":  {3, "د.ك", "Kuwaiti Dinar"},
	"BHD":  {3, "ب.د", "Bahraini Dinar"},
	"OMR":  {3, "﷼", "Omani Rial"},
	"JOD":  {3, "د.أ", "Jordanian Dinar"},
	"ILS":  {2, "₪", "Israeli Shekel"},
	"EGP":  {2, "ج.م", "Egyptian Pound"},
	"NGN":  {2, "₦", "Nigerian Naira"},
	"KES":  {2, "Sh", "Kenyan Shilling"},
	"GHS":  {2, "₵", "Ghanaian Cedi"},
	"MAD":  {2, "د.م.", "Moroccan Dirham"},
	"TND":  {3, "د.ت", "Tunisian Dinar"},
	"DZD":  {2, "د.ج", "Algerian Dinar"},
	"RUB":  {2, "₽", "Russian Ruble"},
	"UAH":  {2, "₴", "Ukrainian Hryvnia"},
	"BTC":  {8, "₿", "Bitcoin"},
	"ETH":  {18, "Ξ", "Ether"},
	"USDT": {6, "₮", "Tether"},
	"USDC": {6, "$", "USD Coin"},
}

// lookupCurrency falls back to (2, code, code) for unknown codes so
// the FFI surface degrades gracefully. The Sky-side type system
// keeps the typed enum honest at compile time; this is the runtime
// fallback for CurrencyRaw String.
func lookupCurrency(code string) currencyInfo {
	c := strings.ToUpper(strings.TrimSpace(code))
	if info, ok := currencyTable[c]; ok {
		return info
	}
	return currencyInfo{Minor: 2, Symbol: c, Name: c}
}

// ── FX rate registry ─────────────────────────────────────────────
//
// Pluggable: users register rates at startup (Money.setRate),
// either statically (compiled in / config) or after fetching from
// an FX provider (e.g. on app boot, refreshed on a schedule via
// Sub.every). The registry is process-local — multi-instance
// deployments need a shared source (DB, cache) for consistency.
//
// Rates are stored as decimal.Decimal so they compose with Money
// arithmetic without precision loss.

type ratePair struct {
	From string
	To   string
}

var (
	fxRatesMu sync.RWMutex
	fxRates   = map[ratePair]decimal.Decimal{}
)

func setRate(from, to string, rate decimal.Decimal) {
	from = strings.ToUpper(from)
	to = strings.ToUpper(to)
	fxRatesMu.Lock()
	fxRates[ratePair{from, to}] = rate
	// Auto-register inverse so users don't have to set both
	// directions for the common case. Skipped if rate is zero
	// (avoid divide-by-zero); users supplying their own inverse
	// later override this.
	if !rate.IsZero() {
		fxRates[ratePair{to, from}] = decimal.NewFromInt(1).Div(rate)
	}
	fxRatesMu.Unlock()
}

func getRate(from, to string) (decimal.Decimal, bool) {
	from = strings.ToUpper(from)
	to = strings.ToUpper(to)
	if from == to {
		return decimal.NewFromInt(1), true
	}
	fxRatesMu.RLock()
	defer fxRatesMu.RUnlock()
	if r, ok := fxRates[ratePair{from, to}]; ok {
		return r, true
	}
	return decimal.Zero, false
}

func init() {
	// ── Currency property lookups ───────────────────────────────

	// minorUnits : String -> Int
	// e.g. "USD" -> 2, "JPY" -> 0, "BHD" -> 3
	RegisterPure("Money_minorUnits", func(args []any) any {
		if len(args) < 1 {
			return 2
		}
		return lookupCurrency(fmt.Sprintf("%v", args[0])).Minor
	})

	// symbol : String -> String
	RegisterPure("Money_symbol", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		return lookupCurrency(fmt.Sprintf("%v", args[0])).Symbol
	})

	// currencyName : String -> String
	RegisterPure("Money_currencyName", func(args []any) any {
		if len(args) < 1 {
			return ""
		}
		return lookupCurrency(fmt.Sprintf("%v", args[0])).Name
	})

	// isKnownCurrency : String -> Bool
	RegisterPure("Money_isKnownCurrency", func(args []any) any {
		if len(args) < 1 {
			return false
		}
		code := strings.ToUpper(strings.TrimSpace(fmt.Sprintf("%v", args[0])))
		_, ok := currencyTable[code]
		return ok
	})

	// ── Fair-split allocation ───────────────────────────────────

	// allocate : Int -> Int -> Decimal -> List Decimal
	// Distributes the amount across N portions, ROUNDED to
	// minor-units precision, with any remainder spread one-cent-
	// each to the front of the list. Classic "$1.00 split 3 ways
	// = [0.34, 0.33, 0.33]" pattern.
	//
	// Arg order: places parts amount  (target precision first so
	// the most common shape `Money.allocate cents N total` reads
	// naturally).
	RegisterPure("Money_allocate", func(args []any) any {
		if len(args) < 3 {
			return []any{}
		}
		places := int32(AsInt(args[0]))
		parts := AsInt(args[1])
		amount := decimalUnbox(args[2])
		if parts <= 0 {
			return []any{}
		}
		// Work in minor units (integer) to avoid rounding drift.
		totalMinor := amount.Shift(places).Truncate(0)
		partsDec := decimal.NewFromInt(int64(parts))
		// base = trunc(totalMinor / parts) (toward zero), so the residue
		// carries the SAME SIGN as the total and |residue| < parts.
		base := totalMinor.Div(partsDec).Truncate(0)
		remainder := totalMinor.Sub(base.Mul(partsDec))
		remInt := int(remainder.IntPart())
		// Distribute the residue one minor-unit at a time to the front of the
		// list, respecting sign: a positive total spreads +1 cents, a NEGATIVE
		// total (refund / chargeback / negative-balance split) spreads -1 cents.
		// The old code compared `i < remInt` with a negative `remInt`, which is
		// never true, so negative allocations dropped the residue cent and the
		// parts summed to (total + sign) — violating the "parts sum to the input
		// exactly" contract. Split into sign + magnitude so both directions fill
		// exactly |residue| slots.
		sign := int64(1)
		nResidue := remInt
		if remInt < 0 {
			sign = -1
			nResidue = -remInt
		}
		out := make([]any, parts)
		for i := 0; i < parts; i++ {
			share := base
			if i < nResidue {
				share = base.Add(decimal.NewFromInt(sign))
			}
			// Shift back to "major" units.
			out[i] = decimalBox(share.Shift(-places))
		}
		return out
	})

	// ── Formatting ──────────────────────────────────────────────

	// format : String -> Decimal -> String
	// Default format: `<symbol><amount>` with currency-correct
	// precision (USD: "$12.34", JPY: "¥1234", BHD: "ب.د 1.234").
	// Negative amounts: "-$12.34".
	RegisterPure("Money_format", func(args []any) any {
		if len(args) < 2 {
			return ""
		}
		code := fmt.Sprintf("%v", args[0])
		info := lookupCurrency(code)
		d := decimalUnbox(args[1])
		neg := d.IsNegative()
		fixed := d.Abs().StringFixed(int32(info.Minor))
		if neg {
			return "-" + info.Symbol + fixed
		}
		return info.Symbol + fixed
	})

	// formatWithCode : String -> Decimal -> String
	// "12.34 USD" — preferred for B2B / accounting output where
	// the ISO code is more useful than the symbol.
	RegisterPure("Money_formatWithCode", func(args []any) any {
		if len(args) < 2 {
			return ""
		}
		code := strings.ToUpper(fmt.Sprintf("%v", args[0]))
		info := lookupCurrency(code)
		d := decimalUnbox(args[1])
		return d.StringFixed(int32(info.Minor)) + " " + code
	})

	// ── FX rate registry ────────────────────────────────────────

	// setRate : String -> String -> Decimal -> Result Error ()
	// Register a rate from→to. Inverse auto-registered.
	RegisterPure("Money_setRate", func(args []any) any {
		if len(args) < 3 {
			return Err[any, any](ErrInvalidInput("Money.setRate: missing args"))
		}
		from := fmt.Sprintf("%v", args[0])
		to := fmt.Sprintf("%v", args[1])
		rate := decimalUnbox(args[2])
		if rate.IsNegative() || rate.IsZero() {
			return Err[any, any](ErrInvalidInput("Money.setRate: rate must be positive"))
		}
		setRate(from, to, rate)
		return Ok[any, any](nil)
	})

	// getRate : String -> String -> Result Error Decimal
	RegisterPure("Money_getRate", func(args []any) any {
		if len(args) < 2 {
			return Err[any, any](ErrInvalidInput("Money.getRate: missing args"))
		}
		from := fmt.Sprintf("%v", args[0])
		to := fmt.Sprintf("%v", args[1])
		rate, ok := getRate(from, to)
		if !ok {
			return Err[any, any](ErrInvalidInput(
				"Money.getRate: no rate registered for " + from + "→" + to))
		}
		return Ok[any, any](decimalBox(rate))
	})

	// hasRate : String -> String -> Bool
	RegisterPure("Money_hasRate", func(args []any) any {
		if len(args) < 2 {
			return false
		}
		from := fmt.Sprintf("%v", args[0])
		to := fmt.Sprintf("%v", args[1])
		_, ok := getRate(from, to)
		return ok
	})

	// clearRates : () -> Result Error ()  — test/admin only
	RegisterPure("Money_clearRates", func(args []any) any {
		fxRatesMu.Lock()
		fxRates = map[ratePair]decimal.Decimal{}
		fxRatesMu.Unlock()
		return Ok[any, any](nil)
	})
}
