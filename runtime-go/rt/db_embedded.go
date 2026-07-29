package rt

import (
	"fmt"
	"os"
	"strings"
)

// SkyEmbeddedMigrations carries the app's committed `db/migrations/*.json`,
// concatenated into a JSON array of `{id, ops}`. It is baked in at build time by
// `sky build` (which emits a small generated `embedded_migrations.go` beside
// `main.go`) whenever the project has a `db/migrations/` directory. It is the
// empty string for an app that defines no file-based migrations.
var SkyEmbeddedMigrations string

// MaybeApplyEmbeddedMigrationsAndExit is called from that generated main-package
// init(). It lets a DEPLOYED binary self-migrate with NO source tree and NO `sky`
// toolchain on the host:
//
//	SKY_DB_OP=migrate ./app   → apply the embedded migrations, print a summary, exit
//	SKY_DB_OP=status  ./app   → report applied / pending, exit
//	(unset)           ./app   → returns immediately; the app boots and serves
//
// It opens the app's database from the standard config (DATABASE_URL /
// <PREFIX>_DB_PATH), renders each op to dialect-correct SQL, and applies them
// through the checksummed `_sky_migrations` ledger (at most once each) —
// delegating to the same `Db_renderMigrations` + `Db_migrateApply` kernels the
// `sky db migrate` CLI uses. `Db_migrateApply` prints the human summary and exits
// the process itself under `SKY_DB_OP`, so control does not return in that path.
//
// Design: explicit, one-shot, run by a single deploy-time owner — replicas that
// boot without `SKY_DB_OP` just serve. This is the safe shape for horizontal
// scale (no concurrent migrate-on-boot across replicas).
func MaybeApplyEmbeddedMigrationsAndExit() {
	op := strings.ToLower(strings.TrimSpace(os.Getenv("SKY_DB_OP")))
	if op != "migrate" && op != "status" {
		return
	}
	if strings.TrimSpace(SkyEmbeddedMigrations) == "" {
		return
	}

	// Open the DB from the app's standard config (unit arg → DB_PATH/DATABASE_URL).
	connRes := AnyTaskRun(Db_connect(struct{}{}))
	if tag, conn, _ := anyResultView(connRes); tag == 0 {
		// Render dialect-correct SQL, then apply. Db_migrateApply prints a summary
		// and exits (0 / 1) itself under SKY_DB_OP.
		renderRes := AnyTaskRun(Db_renderMigrations(conn, SkyEmbeddedMigrations))
		if rtag, pairs, _ := anyResultView(renderRes); rtag == 0 {
			_ = AnyTaskRun(Db_migrateApply(conn, pairs))
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "db: could not render embedded migrations")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr,
		"db: could not open database for embedded migrations (set DATABASE_URL or "+
			skyEnvName("DB_PATH")+")")
	os.Exit(1)
}
