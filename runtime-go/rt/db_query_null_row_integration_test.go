//go:build integration
// +build integration

package rt

// Real-PostgreSQL arm of db_query_null_row_test.go: a NULL column reads
// as "" in a `Dict String String` row on pgx too. Runs in release.yml
// gate-race ("Postgres session-store integration tests", -run Postgres).

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresDbQuery_NullColumnReadsEmpty(t *testing.T) {
	dsn := requirePostgresDSN(t)
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer conn.Close()
	if err := conn.Ping(); err != nil {
		t.Fatalf("postgres unreachable at SKY_TEST_POSTGRES_DSN: %v", err)
	}
	assertNullRowReadsEmpty(t, &SkyDb{conn: conn, driver: "pgx"},
		"SELECT NULL::text AS t, 'x' AS s, 7 AS n")
}
