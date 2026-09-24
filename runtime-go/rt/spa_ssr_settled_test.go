//go:build !js

package rt

import (
	"strings"
	"testing"
)

// Seeded-boot navigation (register M, SPA-10). The SSR handler settles the
// route's onNavigate command into the `#sky-model` seed. The client must boot
// from that seed and must NOT run onNavigate again after the mount — but ONLY
// when the server really finished the command: every leaf ran, no follow-up
// was left un-chased, and no destructive effect was suppressed. In every other
// case the client keeps firing it, so an effect the server could not run still
// runs once on the client.

func ssrSettledUpdate(follow func(msg any) any) func(msg, m any) any {
	return func(msg any, m any) any {
		res, _ := msg.(SkyResult[any, any])
		next := RecordUpdate(m, map[string]any{"items": res.OkValue, "loading": false})
		var c any = Cmd_none()
		if follow != nil {
			c = follow(msg)
		}
		return SkyTuple2{V0: next, V1: c}
	}
}

func ssrRead(val string) cmdT {
	return cmdT{
		kind:  "perform",
		task:  func() any { return Ok[any, any](val) },
		toMsg: func(r any) any { return r },
	}
}

func settleFull(t *testing.T, model, cmd, update any) (any, bool) {
	t.Helper()
	pair, ok := Spa_ssrSettleFull(model, cmd, update).(T2[any, any])
	if !ok {
		t.Fatalf("Spa_ssrSettleFull must return a T2[any, any]; got %T", Spa_ssrSettleFull(model, cmd, update))
	}
	done, isBool := pair.V1.(bool)
	if !isBool {
		t.Fatalf("Spa_ssrSettleFull's second field must be a Bool; got %T", pair.V1)
	}
	return pair.V0, done
}

func TestSpaSSRSettleFull_AReadWithNoFollowUpIsSettled(t *testing.T) {
	model := map[string]any{"items": "", "loading": true}
	m, done := settleFull(t, model, ssrRead("[a,b]"), ssrSettledUpdate(nil))
	if Field(m, "items") != "[a,b]" {
		t.Fatalf("the read must fold into the model; items=%v", Field(m, "items"))
	}
	if !done {
		t.Fatal("a read whose update returns Cmd.none is fully settled; the page must be marked")
	}
}

func TestSpaSSRSettleFull_NoneAndBatchOfReadsAreSettled(t *testing.T) {
	model := map[string]any{"items": "", "loading": false}
	if _, done := settleFull(t, model, Cmd_none(), ssrSettledUpdate(nil)); !done {
		t.Fatal("Cmd.none has nothing left to run; it is settled")
	}
	batch := cmdT{kind: "batch", batch: []any{ssrRead("x"), Cmd_none()}}
	if _, done := settleFull(t, model, batch, ssrSettledUpdate(nil)); !done {
		t.Fatal("a batch of settled leaves is settled")
	}
}

func TestSpaSSRSettleFull_AnUnchasedFollowUpIsNotSettled(t *testing.T) {
	model := map[string]any{"items": "", "loading": true}
	follow := func(any) any { return ssrRead("second") }
	_, done := settleFull(t, model, ssrRead("index"), ssrSettledUpdate(follow))
	if done {
		t.Fatal("the settle runs one round; a follow-up it did not run must leave the page unmarked, so the client runs onNavigate")
	}
}

func TestSpaSSRSettleFull_ASuppressedWriteIsNotSettled(t *testing.T) {
	model := map[string]any{"items": "", "loading": true}
	write := cmdT{
		kind: "perform",
		task: func() any {
			if r := ssrSuppressedWrite("file.write"); r != nil {
				return r
			}
			return Ok[any, any]("written")
		},
		toMsg: func(r any) any { return r },
	}
	_, done := settleFull(t, model, write, ssrSettledUpdate(nil))
	if done {
		t.Fatal("a destructive effect the settle suppressed did not run; the page must stay unmarked so the client runs it")
	}
	// The suppression record is per settle: a later clean settle on the same
	// goroutine is settled again.
	if _, done := settleFull(t, model, ssrRead("ok"), ssrSettledUpdate(nil)); !done {
		t.Fatal("a suppression in an earlier settle must not leak into the next one")
	}
	if InSsrSettle() {
		t.Fatal("the settle mark must be cleared after Spa_ssrSettleFull returns")
	}
}

func TestSpaSSRSettleFull_ALeafTheSettleDoesNotRunIsNotSettled(t *testing.T) {
	model := map[string]any{"items": "", "loading": true}
	pub := cmdT{kind: "publish", topic: "t", payload: "p"}
	if _, done := settleFull(t, model, pub, ssrSettledUpdate(nil)); done {
		t.Fatal("a publish leaf is not run by the SSR settle; the page must stay unmarked")
	}
	if _, done := settleFull(t, model, "not a command", ssrSettledUpdate(nil)); done {
		t.Fatal("an unrecognised command value is not settled")
	}
}

func TestSpaSSRCmdIsNone(t *testing.T) {
	if !AsBool(Spa_ssrCmdIsNone(Cmd_none())) {
		t.Fatal("Cmd.none is none")
	}
	if !AsBool(Spa_ssrCmdIsNone(cmdT{kind: "batch", batch: []any{Cmd_none()}})) {
		t.Fatal("a batch of none is none")
	}
	if AsBool(Spa_ssrCmdIsNone(ssrRead("x"))) {
		t.Fatal("a perform is not none")
	}
}

func TestSpaSSRPageSettled_MarksOnlyWhatTheServerSettled(t *testing.T) {
	both := Spa_ssrPageSettled("", "<p>x</p>", "main.wasm", "{}", true, true)
	if !strings.Contains(AsString(both), `data-sky-settled="init nav"`) {
		t.Fatalf("a fully settled page must carry data-sky-settled=\"init nav\":\n%s", both)
	}
	navOnly := Spa_ssrPageSettled("", "<p>x</p>", "main.wasm", "{}", false, true)
	if !strings.Contains(AsString(navOnly), `data-sky-settled="nav"`) {
		t.Fatalf("expected data-sky-settled=\"nav\":\n%s", navOnly)
	}
	none := Spa_ssrPageSettled("", "<p>x</p>", "main.wasm", "{}", false, false)
	if !strings.Contains(AsString(none), `data-sky-settled=""`) {
		t.Fatalf("an unsettled page must carry an empty data-sky-settled:\n%s", none)
	}
}

// R2: the page names the fields the server settled for it
// (`data-sky-seed-fields`), deduplicated; a name that is not an identifier
// never reaches the attribute.
func TestSpaSSRPageSeeded_NamesTheSettledFields(t *testing.T) {
	page := SpaSSRPageSeeded("", "<p>x</p>", "main.wasm", "{}", "init nav",
		[]string{"siteConfig", "notice", "siteConfig", `bad"><script>`})
	if !strings.Contains(page, `data-sky-seed-fields="siteConfig notice"`) {
		t.Fatalf("seed-field marker missing or wrong: %s", page)
	}
	if strings.Contains(page, "<script>\"") || strings.Contains(page, `bad"`) {
		t.Fatalf("a non-identifier field name reached the page: %s", page)
	}
	if !strings.Contains(SpaSSRPageSettled("", "", "m.wasm", "{}", ""), `data-sky-seed-fields=""`) {
		t.Fatalf("a page with no settled fields must carry an empty marker")
	}
}
