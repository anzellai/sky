//go:build !js

package rt

import (
	"strings"
	"testing"

	"sky-app/rt/telemetry"
)

// storeProblemLogLines counts the ERROR lines in the telemetry ring (the Sky
// Console's log view) that carry this problem.
func storeProblemLogLines(table, msg string) int {
	n := 0
	for _, e := range telemetry.Default().RecentLogs(0) {
		if e.Level == "error" && e.Fields["table"] == table && strings.Contains(e.Message, msg) {
			n++
		}
	}
	return n
}

func storeProblemCount(table string) float64 {
	for _, m := range telemetry.Default().Snapshot() {
		if m.Name == storeProblemMetric && m.Labels["table"] == table {
			return m.Value
		}
	}
	return 0
}

// A Store builder problem makes every verb on the store fail. An app that
// turns that Err into an empty list showed an empty page with no cause
// (v0.29.0). Db_storeProblem makes it loud: one ERROR line per table and
// problem in the log and the Console, and a counter bumped per refusal.
func TestStoreProblemLogsOncePerTableAndProblem(t *testing.T) {
	table := "sp_test_enquiries"
	msg := `Store "sp_test_enquiries": Store.unique: unknown column "emial" — columns are id, email`
	for i := 0; i < 3; i++ {
		if got := AsString(Db_storeProblem(table, msg)); got != msg {
			t.Fatalf("Db_storeProblem returned %q, want the message unchanged", got)
		}
	}
	if n := storeProblemLogLines(table, msg); n != 1 {
		t.Fatalf("ERROR lines for one problem after 3 refusals = %d, want 1", n)
	}
	if c := storeProblemCount(table); c != 3 {
		t.Fatalf("%s{table=%q} = %v, want 3 (one per refusal)", storeProblemMetric, table, c)
	}

	// A second problem on the same table is a new line, and so is the same
	// problem on another table.
	other := `Store "sp_test_enquiries": Store.index: unknown column "x" — columns are id, email`
	Db_storeProblem(table, other)
	Db_storeProblem("sp_test_other", msg)
	if n := storeProblemLogLines(table, other); n != 1 {
		t.Fatalf("a second problem on one table logged %d lines, want 1", n)
	}
	if n := storeProblemLogLines("sp_test_other", msg); n != 1 {
		t.Fatalf("the same problem on another table logged %d lines, want 1", n)
	}
	if c := storeProblemCount(table); c != 4 {
		t.Fatalf("%s{table=%q} = %v, want 4", storeProblemMetric, table, c)
	}
}
