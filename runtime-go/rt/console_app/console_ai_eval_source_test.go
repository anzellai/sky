package console_app

// The AI tab says where each eval run came from (v0.28.0): the console API
// answers `source` ("database" for a run read from the app's database, such
// as a `sky ai eval` run in CI, "process" for one this process made), the
// console decodes it, and each eval row is labelled "from database" or
// "this process".

import (
	"strings"
	"testing"

	rt "sky-app/rt"
)

func TestConsoleAiTab_LabelsWhereAnEvalRunCameFrom(t *testing.T) {
	body := `{"evals":[
		{"id":"run-db","suite":"support","model":"chat","verdict":"passed","passRate":1,"source":"database"},
		{"id":"run-here","suite":"support","model":"chat","verdict":"failed","passRate":0.5,"source":"process"}]}`
	res := rt.ResultCoerce[Sky_Core_Error_Error, any](rt.JsonDec_decodeString(Main_aiDecoder(), any(body)))
	if res.Tag != 0 {
		t.Fatalf("the AI answer did not decode: %v", res.ErrValue)
	}
	data := rt.Coerce[State_AiData_R](res.OkValue)
	if len(data.Evals) != 2 || data.Evals[0].Source != "database" || data.Evals[1].Source != "process" {
		t.Fatalf("decoded eval sources: %+v", data.Evals)
	}
	row := func(e State_AiEval_R) string {
		return rt.HtmlRender(Std_Ui_layout([]Std_Ui_Attribute{}, AiTab_evalRow(State_AiFilter_R{}, e)))
	}
	fromDb := row(data.Evals[0])
	if !strings.Contains(fromDb, "from database") || !strings.Contains(fromDb, `data-ai-eval-source="database"`) {
		t.Fatalf("a run read from the database is not labelled:\n%s", fromDb)
	}
	here := row(data.Evals[1])
	if strings.Contains(here, "from database") || !strings.Contains(here, "this process") {
		t.Fatalf("a run of this process is labelled wrongly:\n%s", here)
	}
}
