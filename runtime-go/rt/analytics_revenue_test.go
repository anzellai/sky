package rt

import (
	"path/filepath"
	"testing"
)

// TestAnalyticsRevenueByCurrency — Money props are summed exactly (no float
// drift), grouped per currency, never across currencies.
func TestAnalyticsRevenueByCurrency(t *testing.T) {
	defer resetAnalyticsStore()
	resetAnalyticsStore()
	path := filepath.Join(t.TempDir(), "rev.db")
	t.Setenv("SKY_ANALYTICS_DB_PATH", path)
	db := analyticsStore()

	// Three USD purchases whose float sum would drift (0.1+0.2), one EUR,
	// and a non-money "plan" prop that must be ignored.
	analyticsStoreInsert(map[string]any{"ts": int64(1), "event": "purchase", "props": map[string]any{"total": "USD 0.10"}})
	analyticsStoreInsert(map[string]any{"ts": int64(2), "event": "purchase", "props": map[string]any{"total": "USD 0.20"}})
	analyticsStoreInsert(map[string]any{"ts": int64(3), "event": "purchase", "props": map[string]any{"total": "USD 19.99"}})
	analyticsStoreInsert(map[string]any{"ts": int64(4), "event": "purchase", "props": map[string]any{"total": "EUR 5.00"}})
	analyticsStoreInsert(map[string]any{"ts": int64(5), "event": "signup", "props": map[string]any{"plan": "pro"}})
	// JPY has 0 minor units — must format WITHOUT decimals.
	analyticsStoreInsert(map[string]any{"ts": int64(6), "event": "purchase", "props": map[string]any{"total": "JPY 1200"}})

	rev := analyticsRevenueByCurrency(db)
	got := map[string]consoleCurrencyTotal{}
	for _, r := range rev {
		got[r.Currency] = r
	}
	if len(rev) != 3 {
		t.Fatalf("want 3 currencies, got %d: %+v", len(rev), rev)
	}
	if jpy := got["JPY"]; jpy.Amount != "1200" {
		t.Errorf("JPY amount = %q, want 1200 (0 minor units, no decimals)", jpy.Amount)
	}
	// Amounts are formatted to each currency's minor units (USD/EUR → 2dp)
	// with banker's rounding — same as Std.Money — so exact sums render
	// consistently: 0.10 + 0.20 + 19.99 = 20.29, and EUR 5 → "5.00".
	if usd := got["USD"]; usd.Amount != "20.29" || usd.Count != 3 {
		t.Errorf("USD = (%q, %d), want (20.29, 3) — exact decimal sum", usd.Amount, usd.Count)
	}
	if eur := got["EUR"]; eur.Amount != "5.00" {
		t.Errorf("EUR amount = %q, want 5.00 (2dp per currency minor units)", eur.Amount)
	}
	// Most transactions first → USD (3) before EUR (1).
	if rev[0].Currency != "USD" {
		t.Errorf("ordering: want USD first (most tx), got %q", rev[0].Currency)
	}
}
