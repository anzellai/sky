//go:build !js

package rt

// ai_eval_history.go — the eval runs the console's AI tab reads from the
// database, so a run made by another process (`sky ai eval` in CI or a
// terminal) appears in the running app's console.
//
// The in-process ring (ai_console.go) only holds the runs this process made.
// Every Std.Ai.Eval run is also a row of `_sky_ai_eval_runs` in the database
// the run used, which for `sky ai eval` is the app's own (the pipeline's
// `withDb`, by default `Db.connect ()`). A database whose AI tables this
// process created or checked (Ai_migrate with the eval or the registry
// migrations, which an app that serves a registry adapter runs when its slot
// first resolves) is remembered here, and the console reads its newest runs
// when the tab asks: on demand, bounded, never on a timer.

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// aiEvalDbMax bounds the databases remembered for the console.
const aiEvalDbMax = 4

var (
	aiEvalDbMu sync.Mutex
	aiEvalDbs  []*SkyDb
)

// aiRememberEvalDb remembers a pool handle whose AI migrations ran, so the
// console can read its eval runs. A transaction handle is never kept.
func aiRememberEvalDb(d *SkyDb, files []migFile) {
	if d == nil || d.tx != nil || d.conn == nil {
		return
	}
	ai := false
	for _, f := range files {
		if strings.HasPrefix(f.Id, "_sky_ai_eval_") || strings.HasPrefix(f.Id, "_sky_ai_registry_") {
			ai = true
		}
	}
	if !ai {
		return
	}
	aiEvalDbMu.Lock()
	defer aiEvalDbMu.Unlock()
	for _, k := range aiEvalDbs {
		if k == d {
			return
		}
	}
	aiEvalDbs = append(aiEvalDbs, d)
	if over := len(aiEvalDbs) - aiEvalDbMax; over > 0 {
		aiEvalDbs = append([]*SkyDb(nil), aiEvalDbs[over:]...)
	}
}

// aiForgetEvalDbs forgets every remembered database (tests).
func aiForgetEvalDbs() {
	aiEvalDbMu.Lock()
	aiEvalDbs = nil
	aiEvalDbMu.Unlock()
}

// aiStoredEvals reads the newest eval runs of every remembered database. A
// database without the table (an app that only serves a registry adapter
// and never ran an eval) or one that does not answer within the bound adds
// nothing.
func aiStoredEvals(limit int) []aiEvalRecord {
	aiEvalDbMu.Lock()
	dbs := append([]*SkyDb(nil), aiEvalDbs...)
	aiEvalDbMu.Unlock()
	var out []aiEvalRecord
	for _, d := range dbs {
		out = append(out, aiReadEvalRuns(d.conn, limit)...)
	}
	return out
}

func aiReadEvalRuns(conn *sql.DB, limit int) []aiEvalRecord {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx,
		"SELECT digest, suite, candidate, passed, failed, errors, pass_rate, p95_latency_ms, created_at, console "+
			"FROM _sky_ai_eval_runs ORDER BY created_at DESC LIMIT "+strconv.Itoa(limit))
	if err != nil {
		// An older table without the console column still lists its runs.
		rows, err = conn.QueryContext(ctx,
			"SELECT digest, suite, candidate, passed, failed, errors, pass_rate, p95_latency_ms, created_at, '' "+
				"FROM _sky_ai_eval_runs ORDER BY created_at DESC LIMIT "+strconv.Itoa(limit))
		if err != nil {
			return nil
		}
	}
	defer rows.Close()
	var out []aiEvalRecord
	for rows.Next() {
		var digest, suite, candidate, passRate string
		var passed, failed, errs, p95, created int64
		var console sql.NullString
		if rows.Scan(&digest, &suite, &candidate, &passed, &failed, &errs, &passRate, &p95, &created, &console) != nil {
			continue
		}
		var r aiEvalRecord
		if console.Valid && console.String != "" && json.Unmarshal([]byte(console.String), &r) == nil {
			r.TS = time.UnixMilli(created).UTC()
		} else {
			rate, _ := strconv.ParseFloat(passRate, 64)
			r = aiEvalRecord{ID: digest, TS: time.UnixMilli(created).UTC(), Suite: suite, Model: candidate,
				Cases: int(passed + failed + errs), Passed: int(passed), Failed: int(failed), Errors: int(errs),
				PassRate: rate, P95LatencyMs: p95, Scores: map[string]float64{}}
		}
		r.Source = "database"
		out = append(out, r)
	}
	return out
}

// aiMergeEvals joins the ring (this process's runs) with the stored runs,
// one row per run digest (the ring's copy wins), newest first, at most max.
func aiMergeEvals(ring, stored []aiEvalRecord, max int) []aiEvalRecord {
	seen := map[string]bool{}
	var out []aiEvalRecord
	for _, r := range ring {
		if r.Source == "" {
			r.Source = "process"
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	for _, r := range stored {
		if r.ID != "" && seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS.After(out[j].TS) })
	if len(out) > max {
		out = out[:max]
	}
	return out
}
