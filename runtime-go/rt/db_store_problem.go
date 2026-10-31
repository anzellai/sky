//go:build !js

package rt

import "sync"

// storeProblemSeen holds each ( table, problem ) Db_storeProblem has logged,
// so a store whose builder problem fails every verb logs it once, not once
// per request.
var storeProblemSeen sync.Map

// storeProblemMetric counts every verb a Store problem refused, by table.
const storeProblemMetric = "sky_store_problems_total"

// Db_storeProblem : String -> String -> String
//
// Std.Db.Store calls it with the table and the message of every store
// problem (a builder that named an unknown column, an invalid table name, a
// primary key that is not a column) before the verb returns its Err. The
// Err alone is quiet: an app that logs it at a low level, or turns it into
// an empty list, shows an empty page with no cause. So the problem is made
// loud here, whatever the app does with the Err:
//
//   - an ERROR log line, once per table and problem, which also reaches the
//     Sky Console's log view (logEmit writes the telemetry ring);
//   - the counter sky_store_problems_total{table}, bumped on every refusal.
//
// It returns the message unchanged, so the Sky side builds its Err from the
// value and the call is never dropped as dead code.
func Db_storeProblem(tableArg, msgArg any) any {
	table := AsString(tableArg)
	msg := AsString(msgArg)
	RecordCounter(storeProblemMetric, map[string]string{"table": table}, 1)
	if _, seen := storeProblemSeen.LoadOrStore(table+"\x00"+msg, true); !seen {
		logStructured("error", "Std.Db.Store: "+msg+" (every verb on this store fails until the builder is fixed; `sky verify` reports it before a deploy)",
			"table", table)
	}
	return msg
}
