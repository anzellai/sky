package console_app

// The console's own state machine for its controls (v0.27.8), on the
// generated code of sky-bundled/console:
//
//   - a console URL opens on the state it names (the embedded mount's init
//     reads the request's query string);
//   - a tab answer to an older read (a slow answer to the previous range or
//     search) is dropped, so it cannot overwrite the current one;
//   - a range change reads the active tab again at once.
//
// The browser gate scripts/console-controls-e2e.sh drives the same controls.

import (
	"testing"

	rt "sky-app/rt"
)

func TestConsoleInit_ReadsTheURLState(t *testing.T) {
	m := initFromRequest(map[string]any{"query": "tab=errors&range=7d&q=checkout%20failed&service=billing"}).(rt.T2[State_Model_R, any]).V0
	if m.Tab != State_Tab_ErrorsTab || m.Range != State_Range_Last7d || m.GlobalQuery != "checkout failed" || m.SelectedService != "billing" {
		t.Fatalf("init from a console URL: tab=%v range=%v q=%q service=%q", m.Tab, m.Range, m.GlobalQuery, m.SelectedService)
	}
	d := Main_initWith("").V0
	if d.Tab != State_Tab_OverviewTab || d.Range != State_Range_Last24h || d.GlobalQuery != "" {
		t.Fatalf("init with no query: tab=%v range=%v q=%q, want the defaults", d.Tab, d.Range, d.GlobalQuery)
	}
}

func TestConsoleUpdate_DropsAnAnswerToAnOlderRead(t *testing.T) {
	m := Main_initWith("tab=logs").V0
	// A range change starts a new read generation.
	next := Main_update(State_Msg_SelectRange(State_Range_Last1h), m).V0
	if next.Gen != m.Gen+1 || next.Range != State_Range_Last1h {
		t.Fatalf("SelectRange: gen %d → %d, range %v", m.Gen, next.Gen, next.Range)
	}
	rows := func(msg string) rt.SkyResult[Sky_Core_Error_Error, []State_LogEntry_R] {
		return rt.SkyResult[Sky_Core_Error_Error, []State_LogEntry_R]{OkValue: []State_LogEntry_R{{Message: msg}}}
	}
	stale := Main_update(State_Msg_GotLogs(any(m.Gen), any(rows("answer to 24h"))), next).V0
	for _, l := range stale.Logs {
		if l.Message == "answer to 24h" {
			t.Fatalf("an answer read under the previous range replaced the logs: %+v", stale.Logs)
		}
	}
	fresh := Main_update(State_Msg_GotLogs(any(next.Gen), any(rows("answer to 1h"))), next).V0
	if len(fresh.Logs) != 1 || fresh.Logs[0].Message != "answer to 1h" {
		t.Fatalf("the current answer was not taken: %+v", fresh.Logs)
	}
}

func TestConsoleUpdate_AGoodOverviewDoesNotHideAFailedTabRead(t *testing.T) {
	m := Main_initWith("tab=logs").V0
	failedLogs := rt.SkyResult[Sky_Core_Error_Error, []State_LogEntry_R]{Tag: 1, ErrValue: Sky_Core_Error_unavailable("console API /_sky/console/api/logs answered HTTP 500")}
	m = Main_update(State_Msg_GotLogs(any(m.Gen), any(failedLogs)), m).V0
	if m.LastError == "" {
		t.Fatal("a failed logs read shows no error")
	}
	okOverview := rt.SkyResult[Sky_Core_Error_Error, State_Overview_R]{OkValue: State_emptyOverview()}
	m = Main_update(State_Msg_GotOverview(any(okOverview)), m).V0
	if m.LastError == "" {
		t.Fatal("the overview read's answer cleared the logs read's error")
	}
	okLogs := rt.SkyResult[Sky_Core_Error_Error, []State_LogEntry_R]{}
	m = Main_update(State_Msg_GotLogs(any(m.Gen), any(okLogs)), m).V0
	if m.LastError != "" {
		t.Fatalf("a good logs read did not clear its own error: %q", m.LastError)
	}
}
