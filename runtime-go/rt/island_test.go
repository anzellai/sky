//go:build !js

package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

// Widget islands (island_core.go): a third-party JS widget owns the DOM under
// an island element. The shared diff (Sky.Live and Sky.Spa) must never descend
// into it, must patch only its attributes (the props) while its identity holds,
// and must replace it when its identity (name + id) changes.

func islandVNode(name, id, props string, children ...VNode) VNode {
	return VNode{
		Kind: "element",
		Tag:  "div",
		Attrs: map[string]string{
			islandNameAttr:  name,
			islandIDAttr:    id,
			islandPropsAttr: props,
			"sky-key":       id,
		},
		Children: children,
	}
}

func islandTree(island VNode, extra ...VNode) VNode {
	root := VNode{Kind: "element", Tag: "div", Children: append([]VNode{
		{Kind: "element", Tag: "p", Children: []VNode{vtext("counter")}},
		island,
	}, extra...)}
	assignSkyIDs(&root, "r")
	return root
}

func TestIslandDiff_SameIdNeverPatchesInside(t *testing.T) {
	// The children differ between the two renders on purpose: whatever the
	// server tree says is under the island, the diff must not touch it.
	old := islandTree(islandVNode("editor", "ed1", `{"a":1}`,
		VNode{Kind: "element", Tag: "span", Children: []VNode{vtext("old")}}))
	new_ := islandTree(islandVNode("editor", "ed1", `{"a":1}`,
		VNode{Kind: "element", Tag: "em", Children: []VNode{vtext("new")}},
		VNode{Kind: "element", Tag: "b"}))
	islandID := old.Children[1].SkyID
	for _, p := range diffTrees(&old, &new_, nil) {
		if p.ID == islandID || strings.HasPrefix(p.ID, islandID+".") {
			t.Fatalf("diff patched inside or on an unchanged island: %+v", p)
		}
	}
}

func TestIslandDiff_PropsChangeIsOneAttrPatch(t *testing.T) {
	old := islandTree(islandVNode("editor", "ed1", `{"text":"a"}`))
	new_ := islandTree(islandVNode("editor", "ed1", `{"text":"ab"}`))
	patches := diffTrees(&old, &new_, nil)
	if len(patches) != 1 {
		t.Fatalf("want exactly one patch, got %d: %+v", len(patches), patches)
	}
	p := patches[0]
	if p.ID != old.Children[1].SkyID || p.Replace != nil || p.HTML != nil || p.Kids != nil || p.Text != nil {
		t.Fatalf("want an attribute patch on the island, got %+v", p)
	}
	if len(p.Attrs) != 1 || p.Attrs[islandPropsAttr] != `{"text":"ab"}` {
		t.Fatalf("want only %s changed, got %+v", islandPropsAttr, p.Attrs)
	}
}

func TestIslandDiff_IdChangeRemounts(t *testing.T) {
	// remounted: the new render's element at the island slot arrives as new
	// markup (a Replace of the node, or a Kids slot carrying HTML), never as
	// the old node kept and patched in place.
	remounted := func(patches []Patch, oldIslandID, marker string) bool {
		ok := false
		for _, p := range patches {
			if p.ID == oldIslandID && p.Replace == nil {
				return false // the old island was patched in place
			}
			if p.Replace != nil && strings.Contains(*p.Replace, marker) {
				ok = true
			}
			for _, k := range p.Kids {
				if k.Keep == oldIslandID {
					return false // the old island was kept
				}
				if k.HTML != nil && strings.Contains(*k.HTML, marker) {
					ok = true
				}
			}
		}
		return ok
	}
	for _, tc := range []struct {
		name   string
		new_   VNode
		marker string
	}{
		{"id", islandVNode("editor", "ed2", `{}`), `data-sky-island-id="ed2"`},
		{"name", islandVNode("painter", "ed1", `{}`), `data-sky-island="painter"`},
		{"plain div", VNode{Kind: "element", Tag: "div", Attrs: map[string]string{"sky-key": "ed1", "class": "plain"}}, `class="plain"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := islandTree(islandVNode("editor", "ed1", `{}`))
			new_ := islandTree(tc.new_)
			patches := diffTrees(&old, &new_, nil)
			if !remounted(patches, old.Children[1].SkyID, tc.marker) {
				t.Fatalf("an island whose %s changed was not remounted: %+v", tc.name, patches)
			}
		})
	}
	t.Run("div becomes island", func(t *testing.T) {
		old := islandTree(VNode{Kind: "element", Tag: "div", Attrs: map[string]string{"sky-key": "ed1"}})
		new_ := islandTree(islandVNode("editor", "ed1", `{}`))
		patches := diffTrees(&old, &new_, nil)
		if !remounted(patches, old.Children[1].SkyID, islandNameAttr) {
			t.Fatalf("a div that became an island was patched in place: %+v", patches)
		}
	})
}

func TestIslandDiff_RendersNoChildren(t *testing.T) {
	root := islandTree(islandVNode("editor", "ed1", `{"x":"<b>"}`, vtext("server text")))
	html := renderVNode(root, map[string]any{})
	if strings.Contains(html, "server text") {
		t.Fatalf("an island must render no server children: %s", html)
	}
	if !strings.Contains(html, `data-sky-props="{&#34;x&#34;:&#34;&lt;b&gt;&#34;}"`) {
		t.Fatalf("the props attribute is not escaped as expected: %s", html)
	}
}

// ── Typed widget events ─────────────────────────────────────────

func okResult(v any) any  { return SkyResult[any, any]{Tag: 0, OkValue: v} }
func errResult(e any) any { return SkyResult[any, any]{Tag: 1, ErrValue: e} }

func TestIslandEvent_HtmlToVNodeWrapsTheHandler(t *testing.T) {
	handler := func(s any) any { return okResult("Got " + AsString(s)) }
	h := SkyADT{SkyName: "HElement", Fields: []any{"div", []any{
		SkyADT{SkyName: "EventAttr", Fields: []any{SkyADT{SkyName: "OnRaw", Fields: []any{islandEventPrefix + "change", handler}}}},
		SkyADT{SkyName: "EventAttr", Fields: []any{SkyADT{SkyName: "OnMsg", Fields: []any{"click", "Clicked"}}}},
	}, []any{}}}
	vn := HtmlToVNode(h)
	if _, ok := vn.Events[islandEventPrefix+"change"].(islandEventHandler); !ok {
		t.Fatalf("an island event handler must be wrapped, got %T", vn.Events[islandEventPrefix+"change"])
	}
	if _, ok := vn.Events["click"].(islandEventHandler); ok {
		t.Fatal("an ordinary event must not be wrapped")
	}
}

func TestIslandEvent_DecodesTheDetailIntoTheMsg(t *testing.T) {
	var got any
	h := islandEventHandler{fn: func(s any) any {
		got = s
		return okResult("Changed")
	}}
	raw, _ := json.Marshal(`{"text":"hi"}`)
	msg := applyMsgArgs(h, []json.RawMessage{raw}, "")
	if msg != "Changed" {
		t.Fatalf("want the decoded Msg, got %#v", msg)
	}
	if got != `{"text":"hi"}` {
		t.Fatalf("the decoder must receive the detail JSON text, got %#v", got)
	}
}

func TestIslandEvent_DecodeFailureIsDroppedNotACrash(t *testing.T) {
	for name, fn := range map[string]any{
		"decoder Err": func(s any) any { return errResult(SkyADT{SkyName: "Error"}) },
		"panic":       func(s any) any { panic("boom") },
	} {
		h := islandEventHandler{fn: fn}
		raw, _ := json.Marshal(`{}`)
		if _, bad := applyMsgArgs(h, []json.RawMessage{raw}, "").(msgDecodeError); !bad {
			t.Fatalf("%s: a failed island decode must drop the event", name)
		}
	}
	// No args, or a non-string arg (an old client, a forged request): dropped.
	h := islandEventHandler{fn: func(s any) any { return okResult("X") }}
	if _, bad := applyMsgArgs(h, nil, "").(msgDecodeError); !bad {
		t.Fatal("an island event without a payload must be dropped")
	}
	if _, bad := applyMsgArgs(h, []json.RawMessage{json.RawMessage(`{"a":1}`)}, "").(msgDecodeError); !bad {
		t.Fatal("an island event whose payload is not JSON text must be dropped")
	}
}

// ── Cmd.toIsland ────────────────────────────────────────────────

func TestIslandCmd_LivePushesAnIslandFrame(t *testing.T) {
	app := &liveApp{}
	sess := &liveSession{sid: "s1", sseCh: make(chan sseFrame, 4)}
	payload := JsonValue{raw: map[string]any{"line": 3}}
	app.runCmd(sess, Cmd_batch([]any{Cmd_none(), Cmd_toIsland("ed1", "goto", payload)}))
	select {
	case fr := <-sess.sseCh:
		if fr.event != "island" {
			t.Fatalf("want an island frame, got %q", fr.event)
		}
		var d struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(fr.data), &d); err != nil {
			t.Fatalf("frame is not JSON: %v (%s)", err, fr.data)
		}
		if d.ID != "ed1" || d.Name != "goto" || string(d.Payload) != `{"line":3}` {
			t.Fatalf("unexpected frame %s", fr.data)
		}
	default:
		t.Fatal("Cmd.toIsland pushed no frame")
	}
}

func TestIslandCmd_TeaLoopIgnoresIt(t *testing.T) {
	// A terminal target has no widget; the command is a no-op, not a crash:
	// it starts no effect and sends no Msg (G-11: this test asserted
	// nothing, so it failed only on a panic).
	msgCh := make(chan any, 4)
	l := newTeaLoop(msgCh, nil, nil, nil)
	l.runCmd(Cmd_toIsland("x", "y", JsonValue{raw: nil}))
	if n := l.inflight.Load(); n != 0 {
		t.Fatalf("Cmd.toIsland started %d effect(s) in a terminal loop", n)
	}
	if len(msgCh) != 0 {
		t.Fatalf("Cmd.toIsland sent %d Msg(s) in a terminal loop", len(msgCh))
	}
}

// ── Sky.Spa hydration keeps the widget's DOM ────────────────────

func TestIslandHydration_IgnoresWidgetChildren(t *testing.T) {
	v := islandVNode("editor", "ed1", `{}`)
	v.SkyID = "r"
	dom := &fakeHydrateNode{tag: "div", attrs: map[string]string{
		"sky-id": "r", islandNameAttr: "editor", islandIDAttr: "ed1", islandPropsAttr: `{}`, "sky-key": "ed1",
	}, kids: []*fakeHydrateNode{{tag: "canvas", attrs: map[string]string{}}}}
	if ok, why := spaCanHydrate(dom, &v); !ok {
		t.Fatalf("a mounted widget's children must not force a rebuild: %s", why)
	}
}

// fakeHydrateNode is a minimal spaDOM for the hydration decision.
type fakeHydrateNode struct {
	tag    string
	attrs  map[string]string
	kids   []*fakeHydrateNode
	parent *fakeHydrateNode
	idx    int
}

func (n *fakeHydrateNode) NodeType() int { return 1 }
func (n *fakeHydrateNode) Tag() string   { return n.tag }
func (n *fakeHydrateNode) Attr(k string) (string, bool) {
	v, ok := n.attrs[k]
	return v, ok
}
func (n *fakeHydrateNode) FirstChild() spaDOM {
	if len(n.kids) == 0 {
		return nil
	}
	c := n.kids[0]
	c.parent, c.idx = n, 0
	return c
}
func (n *fakeHydrateNode) NextSibling() spaDOM {
	if n.parent == nil || n.idx+1 >= len(n.parent.kids) {
		return nil
	}
	c := n.parent.kids[n.idx+1]
	c.parent, c.idx = n.parent, n.idx+1
	return c
}
func (n *fakeHydrateNode) Data() string         { return "" }
func (n *fakeHydrateNode) SplitText(int) spaDOM { return nil }

func TestIslandCmd_SpaServerBranchSaysItCannotDeliver(t *testing.T) {
	var got []islandCmd
	prev := spaLogIslandFromServer
	spaLogIslandFromServer = func(ic islandCmd) { got = append(got, ic) }
	defer func() { spaLogIslandFromServer = prev }()
	Spa_collectFollowUps(Cmd_batch([]any{Cmd_toIsland("ed1", "focus", JsonValue{raw: nil})}))
	if len(got) != 1 || got[0].ID != "ed1" || got[0].Name != "focus" {
		t.Fatalf("a server branch's Cmd.toIsland must be reported, got %+v", got)
	}
}
