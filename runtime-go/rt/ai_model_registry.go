//go:build !js

package rt

// ai_model_registry.go — the runtime half of Std.Ai.Registry, the model
// registry (not to be confused with ai_registry.go, the machine's list of
// running engines).
//
// The registry's source of truth is the app's own database (Std.Db: SQLite in
// development, PostgreSQL in QA and production). Its schema is a list of
// dialect-neutral migrations in the format of `db/migrations/*.json`, owned by
// sky-stdlib/Std/Ai/Registry.sky. This file applies them through the same
// checksummed `_sky_migrations` ledger `Db.migrate` uses:
//
//	Ai_migrate : Db -> String -> Task Error (List String)
//	Ai_recordRegistry  : String -> Task Error ()
//
// The difference from `Db.migrate` is the DB-ops mode: under
// `SKY_DB_OP=migrate` the app's own `Db.migrate` call applies its list and
// exits. A stdlib module must neither exit the process nor stop the app's
// own list from running, so the registry's migrations go through
// dbMigrateCore with no ops mode.

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// aiMigrateMu serialises the registry's migrations inside one
// process; two replicas racing on one database are handled by a retry (a
// lost race finds the rows already applied).
var aiMigrateMu sync.Mutex

// Ai_migrate : Db -> String -> Task Error (List String)
func Ai_migrate(dbArg, jsonArg any) any {
	raw := AsString(jsonArg)
	return func() any {
		d, ok := dbArg.(*SkyDb)
		if !ok {
			return Err[any, any](ErrInvalidInput("Std.Ai.Registry: not a Db handle"))
		}
		var files []migFile
		if err := json.Unmarshal([]byte(raw), &files); err != nil {
			return Err[any, any](ErrInvalidInput("Std.Ai.Registry: the migrations do not parse: " + err.Error()))
		}
		pairs := make([]any, 0, len(files))
		for _, f := range files {
			stmts := make([]string, 0, len(f.Ops))
			for _, op := range f.Ops {
				sql, err := renderMigOp(d.driver, op)
				if err != nil {
					return Err[any, any](ErrInvalidInput("Std.Ai.Registry: " + err.Error()))
				}
				stmts = append(stmts, sql)
			}
			pairs = append(pairs, T2[any, any]{V0: f.Id, V1: strings.Join(stmts, ";\n")})
		}
		aiMigrateMu.Lock()
		defer aiMigrateMu.Unlock()
		var last any
		for attempt := 0; attempt < 3; attempt++ {
			r := dbMigrateCore(d, pairs, "", func(_ string, e any) any { return e })
			if tag, _, _ := anyResultView(r); tag == 0 {
				return r
			}
			last = r
			time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
		}
		return last
	}
}

// aiRegistryModel is one model of the registry as the console's AI panel
// shows it: its aliases, its newest versions and its newest alias events.
type aiRegistryModel struct {
	Name     string           `json:"name"`
	Store    string           `json:"store"`
	Aliases  []map[string]any `json:"aliases"`
	Versions []map[string]any `json:"versions"`
	Events   []map[string]any `json:"events"`
	Reports  []map[string]any `json:"reports"`
	TS       time.Time        `json:"ts"`
}

var (
	aiRegistryConsoleMu sync.Mutex
	aiRegistryConsole   = map[string]aiRegistryModel{}
)

// aiRegistryConsoleMax bounds the models the panel keeps.
const aiRegistryConsoleMax = 50

// Ai_recordRegistry : String -> Task Error () — Std.Ai.Registry hands the
// panel a model's current state after it reads or changes it. The state holds
// names, numbers, digests and the alias log, never an artefact or a secret.
func Ai_recordRegistry(jsonArg any) any {
	raw := AsString(jsonArg)
	return func() any {
		var m aiRegistryModel
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return Err[any, any](ErrInvalidInput("Std.Ai.Registry: the console state does not parse: " + err.Error()))
		}
		if m.Name == "" {
			return Err[any, any](ErrInvalidInput("Std.Ai.Registry: the console state has no model name"))
		}
		m.TS = time.Now().UTC()
		aiRegistryConsoleMu.Lock()
		defer aiRegistryConsoleMu.Unlock()
		aiRegistryConsole[m.Name] = m
		if len(aiRegistryConsole) > aiRegistryConsoleMax {
			oldest := ""
			for k, v := range aiRegistryConsole {
				if oldest == "" || v.TS.Before(aiRegistryConsole[oldest].TS) {
					oldest = k
				}
			}
			delete(aiRegistryConsole, oldest)
		}
		return Ok[any, any](struct{}{})
	}
}

// aiRegistrySnapshot is the panel's registry section, sorted by model name.
func aiRegistrySnapshot() []aiRegistryModel {
	aiRegistryConsoleMu.Lock()
	defer aiRegistryConsoleMu.Unlock()
	out := make([]aiRegistryModel, 0, len(aiRegistryConsole))
	for _, m := range aiRegistryConsole {
		out = append(out, m)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// aiResetRegistryConsole empties the registry section (tests).
func aiResetRegistryConsole() {
	aiRegistryConsoleMu.Lock()
	aiRegistryConsole = map[string]aiRegistryModel{}
	aiRegistryConsoleMu.Unlock()
}
