//go:build integration
// +build integration

package rt

import "testing"

// The PostgreSQL arm of db_store_legacy_nested_maybe_test.go: the v0.28 TEXT
// columns of nested Maybe scalars take the int64, float64 and bool v0.29
// binds, through pgx, and read back.
func TestLegacyNestedMaybeTextColumnsPostgres(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	assertLegacyNestedMaybeTextColumns(t, d, "legacy_nested_maybe_pg")
}
