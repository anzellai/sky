//go:build !js

package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── Std.Nav: the command and its URL rule (nav.go) ──────────────

func TestNav_OnlyASameOriginReferenceIsFollowed(t *testing.T) {
	ok := []string{"/", "/orders/7", "/a?b=1#c", "?page=2", "#top", "#"}
	for _, u := range ok {
		if why := navTargetError(u); why != "" {
			t.Errorf("%q must be followed, refused: %s", u, why)
		}
	}
	bad := []string{
		"", "https://evil.test/", "http://x/", "//evil.test/a", "/\\evil.test",
		"javascript:alert(1)", "orders/7", "mailto:a@b", "/a\nb", "/a\x7f",
	}
	for _, u := range bad {
		if navTargetError(u) == "" {
			t.Errorf("%q must be refused", u)
		}
	}
}

func TestNav_CommandsCarryTheURLAndTheHistoryMode(t *testing.T) {
	push, ok := navCmdOf(Nav_pushUrl("/a"))
	if !ok || push.URL != "/a" || push.Replace {
		t.Fatalf("pushUrl: %+v %v", push, ok)
	}
	rep, ok := navCmdOf(Nav_replaceUrl("#"))
	if !ok || rep.URL != "#" || !rep.Replace {
		t.Fatalf("replaceUrl: %+v %v", rep, ok)
	}
	// The frame is data, never markup.
	d := navFrameData(navCmd{URL: "/a?x=</script>", Replace: true})
	if strings.Contains(d, "</script>") {
		t.Fatalf("frame must escape markup: %s", d)
	}
	var back navCmd
	if err := json.Unmarshal([]byte(d), &back); err != nil || back.URL != "/a?x=</script>" || !back.Replace {
		t.Fatalf("frame round-trip: %v %+v", err, back)
	}
}

// ── Sky.Live: one tab moves (live_nav_delivery.go) ──────────────

func navSession() *liveSession {
	return &liveSession{sid: "s1", sseCh: make(chan sseFrame, 4)}
}

func navFrame(t *testing.T, ch chan sseFrame) (navCmd, bool) {
	t.Helper()
	select {
	case fr := <-ch:
		if fr.event != "nav" {
			t.Fatalf("want a nav frame, got %q", fr.event)
		}
		var nc navCmd
		if err := json.Unmarshal([]byte(fr.data), &nc); err != nil {
			t.Fatalf("frame is not JSON: %v", err)
		}
		return nc, true
	default:
		return navCmd{}, false
	}
}

func TestNavLive_TheOriginTabMovesAndNoOtherTab(t *testing.T) {
	app := &liveApp{}
	sess := navSession()
	_, chA, _ := sess.registerSSEConn("tabA")
	_, chB, _ := sess.registerSSEConn("tabB")
	withLiveOriginTab("tabB", func() {
		app.runCmd(sess, Cmd_batch([]any{Cmd_none(), Nav_pushUrl("/orders/7")}))
	})
	if nc, ok := navFrame(t, chB); !ok || nc.URL != "/orders/7" || nc.Replace {
		t.Fatalf("the origin tab must receive the navigation: %+v %v", nc, ok)
	}
	if _, ok := navFrame(t, chA); ok {
		t.Fatal("another tab must not move")
	}
	if len(sess.sseCh) != 0 {
		t.Fatal("a navigation is not fanned out through the relay")
	}
}

func TestNavLive_WorkNoTabStartedMovesTheOldestTab(t *testing.T) {
	app := &liveApp{}
	sess := navSession()
	_, chA, _ := sess.registerSSEConn("tabA")
	_, chB, _ := sess.registerSSEConn("tabB")
	app.runCmd(sess, Nav_replaceUrl("#"))
	if nc, ok := navFrame(t, chA); !ok || nc.URL != "#" || !nc.Replace {
		t.Fatalf("the oldest tab must receive it: %+v %v", nc, ok)
	}
	if _, ok := navFrame(t, chB); ok {
		t.Fatal("only one tab moves")
	}
}

// An `init` command runs before the page's SSE connection is up: the tab
// gets its navigation when it connects, and a different tab is never moved
// in its place.
func TestNavLive_ATabThatIsNotConnectedYetGetsItWhenItConnects(t *testing.T) {
	app := &liveApp{}
	sess := navSession()
	_, chOld, _ := sess.registerSSEConn("old")
	withLiveOriginTab("fresh", func() {
		app.runCmd(sess, Nav_replaceUrl("/welcome"))
	})
	if _, ok := navFrame(t, chOld); ok {
		t.Fatal("a connected tab must not move for a tab that has not connected")
	}
	_, chFresh, _ := sess.registerSSEConn("fresh")
	if nc, ok := navFrame(t, chFresh); !ok || nc.URL != "/welcome" || !nc.Replace {
		t.Fatalf("the tab must get its navigation on connect: %+v %v", nc, ok)
	}
	// Delivered once.
	_, chAgain, _ := sess.registerSSEConn("fresh")
	if _, ok := navFrame(t, chAgain); ok {
		t.Fatal("a pending navigation is delivered once")
	}
}

func TestNavLive_AURLOffTheSiteIsRefusedAndLogged(t *testing.T) {
	var got []string
	prev := logNavRejected
	logNavRejected = func(nc navCmd, why string) { got = append(got, nc.URL+" | "+navRejectedMessage(nc, why)) }
	defer func() { logNavRejected = prev }()
	app := &liveApp{}
	sess := navSession()
	_, ch, _ := sess.registerSSEConn("tabA")
	withLiveOriginTab("tabA", func() {
		app.runCmd(sess, Nav_pushUrl("https://evil.test/x"))
	})
	if _, ok := navFrame(t, ch); ok {
		t.Fatal("a URL off the site must never be sent")
	}
	if len(got) != 1 || !strings.Contains(got[0], "Std.Nav.pushUrl refused") {
		t.Fatalf("the refusal must be logged: %v", got)
	}
}

func TestNav_TerminalTargetAndSpaServerBranchIgnoreIt(t *testing.T) {
	// A terminal has no address bar: a no-op, not a crash.
	l := &teaLoop{}
	l.runCmd(Nav_pushUrl("/a"))
	// A Sky.Spa server branch: the client already ran it (the split's
	// residual); the backend produces no follow-up Msg for it.
	out := Spa_collectFollowUps(Cmd_batch([]any{Nav_pushUrl("/a")}))
	if l, ok := out.([]any); !ok || len(l) != 0 {
		t.Fatalf("no follow-up for a navigation: %#v", out)
	}
}
