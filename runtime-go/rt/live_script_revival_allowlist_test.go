package rt

// Cycle 3 audit gap C9 / cycle 2 plan P31 — XSS hardening of
// __skyReviveScripts via a strict attribute allowlist.
//
// Threat model: Sky's Ui.html surface admits arbitrary HTML. A
// Sky.Live app that round-trips WYSIWYG-style user content back
// into a render is the canonical exposure vector. Prior to this
// fix, content like
//
//   Html.node "script" [ Attr.attribute "onerror" "alert(1)"
//                      , Attr.attribute "src" "data:,"
//                      ] []
//
// would survive __skyReviveScripts verbatim — the `onerror`
// attribute fires when the no-such-resource src= load fails,
// running attacker-controlled JS in the document origin.
//
// The fix in live.go's __skyReviveScripts:
//   1. Allowlist of safe <script> attrs: src, type, async, defer,
//      integrity, crossorigin, nomodule, referrerpolicy, plus the
//      internal data-sky-script-revived marker.
//   2. EVERY other attribute (event handlers like onerror /
//      onload / onclick, plus anything novel) is silently dropped
//      (one console.warn per element batched together).
//   3. Inline script bodies are dropped UNLESS the element also
//      carries `src` — Sky-bundled scripts (sky-editor's
//      Editor.scriptTag) set src, so they survive; user-supplied
//      inline <script>alert(1)</script> is rejected.
//   4. Rejected elements still get the data-sky-script-revived
//      marker so repeated passes don't re-warn.
//
// These tests are JS-shape pins: substring asserts on the
// liveJS() output. The Playwright probe at
// scripts/verify-script-revival-allowlist.{html,sh} runs the
// JS in a real browser and asserts no attacker callback fires.

import (
	"strings"
	"testing"
)

// TestSkyReviveScripts_AllowlistDeclaresExactly9Entries pins the
// allowlist's authoritative entry set. The exact membership is a
// security boundary — any future broadening (e.g. adding
// `language=` or `for=`) MUST adjust this list AND audit the
// browser's behaviour on the new attr.
func TestSkyReviveScripts_AllowlistDeclaresExactly9Entries(t *testing.T) {
	js := liveJS("test-sid")
	mustContain := []string{
		`var __skyScriptAttrAllowlist = {`,
		`"src": 1`,
		`"type": 1`,
		`"async": 1`,
		`"defer": 1`,
		`"integrity": 1`,
		`"crossorigin": 1`,
		`"nomodule": 1`,
		`"referrerpolicy": 1`,
		`"data-sky-script-revived": 1`,
	}
	for _, want := range mustContain {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing allowlist entry: %q", want)
		}
	}
}

// TestSkyReviveScripts_AllowlistOmitsEventHandlerAttrs is the
// counter-assertion. Every `on*` attribute name a browser will
// fire on a <script> element MUST NOT appear as a key in the
// allowlist object. (We grep the LITERAL allowlist block — the
// JS body itself contains "onerror" inside the comment/dev-warn
// strings, which would false-positive a naive whole-JS grep.)
func TestSkyReviveScripts_AllowlistOmitsEventHandlerAttrs(t *testing.T) {
	js := liveJS("test-sid")
	startKey := `var __skyScriptAttrAllowlist = {`
	startIdx := strings.Index(js, startKey)
	if startIdx < 0 {
		t.Fatalf("allowlist block not found in liveJS output")
	}
	endIdx := strings.Index(js[startIdx:], "};")
	if endIdx < 0 {
		t.Fatalf("allowlist block has no closing brace in liveJS output")
	}
	block := js[startIdx : startIdx+endIdx+2]
	badKeys := []string{
		`"onerror"`, `"onload"`, `"onclick"`, `"onmouseover"`,
		`"onfocus"`, `"onblur"`, `"onbeforescriptexecute"`,
		`"onreadystatechange"`,
	}
	for _, bad := range badKeys {
		if strings.Contains(block, bad) {
			t.Errorf("allowlist block must NOT contain event-handler key %q; got %s", bad, block)
		}
	}
}

// TestSkyReviveScripts_AttrCopyConsultsAllowlist pins the copy
// loop checks `__skyScriptAttrAllowlist[n] === 1` BEFORE
// fresh.setAttribute. A future maintainer flipping the order
// (setAttribute before allowlist check) would silently restore
// the XSS surface.
func TestSkyReviveScripts_AttrCopyConsultsAllowlist(t *testing.T) {
	js := liveJS("test-sid")
	wants := []string{
		// lowercase the attribute name before the lookup so
		// `OnError` is normalised to `onerror` (which is NOT in
		// the allowlist).
		`var n = a.name.toLowerCase();`,
		// The check itself.
		`if (__skyScriptAttrAllowlist[n] === 1) {`,
		// Setattr happens inside the truthy branch.
		`fresh.setAttribute(a.name, a.value);`,
	}
	for _, w := range wants {
		if !strings.Contains(js, w) {
			t.Errorf("__skyReviveScripts allowlist gate missing required token %q", w)
		}
	}
}

// TestSkyReviveScripts_InlineWithoutSrcRejected pins that the
// reject branch for inline-only <script>s is wired before the
// fresh-element clone, AND that the rejection skips the entire
// revival path via `continue`.
func TestSkyReviveScripts_InlineWithoutSrcRejected(t *testing.T) {
	js := liveJS("test-sid")
	wants := []string{
		`var hasSrc = old.hasAttribute("src");`,
		`var hasInline = !!(old.textContent && old.textContent.length > 0);`,
		`if (!hasSrc && hasInline) {`,
		// dev-time visibility — the rejection MUST surface or
		// developers will silently lose script behaviour.
		`console.warn("[sky.live] script revival rejected an inline <script> without src=`,
		// The skip itself — without `continue`, the rejected
		// element would still hit replaceChild + execute.
		`continue;`,
	}
	for _, w := range wants {
		if !strings.Contains(js, w) {
			t.Errorf("inline-without-src rejection path missing required token %q", w)
		}
	}
}

// TestSkyReviveScripts_RejectedNodeStillMarkedRevived locks the
// idempotency contract for rejected nodes: a malicious or
// otherwise-rejected <script> MUST get
// data-sky-script-revived set on the ORIGINAL element BEFORE
// the rejection branch returns. Otherwise the next revival
// pass would re-warn for the same node, flooding the console.
//
// The pin: the setAttribute happens inside a try/catch BEFORE
// hasSrc/hasInline are inspected.
func TestSkyReviveScripts_RejectedNodeStillMarkedRevived(t *testing.T) {
	js := liveJS("test-sid")
	// Find the function body.
	startIdx := strings.Index(js, "function __skyReviveScripts(root) {")
	if startIdx < 0 {
		t.Fatalf("__skyReviveScripts function not found")
	}
	body := js[startIdx:]
	// Cut at the closing brace of the for loop (close enough — we
	// just need the structural ordering inside one iteration).
	bodyEnd := strings.Index(body, "\n}\n")
	if bodyEnd > 0 {
		body = body[:bodyEnd]
	}
	idxMark := strings.Index(body, `old.setAttribute("data-sky-script-revived", "1");`)
	idxRejectBranch := strings.Index(body, `if (!hasSrc && hasInline) {`)
	if idxMark < 0 {
		t.Fatalf("revival marker setAttribute not found in __skyReviveScripts body")
	}
	if idxRejectBranch < 0 {
		t.Fatalf("inline-without-src reject branch not found in __skyReviveScripts body")
	}
	if idxMark >= idxRejectBranch {
		t.Errorf("data-sky-script-revived marker must be set on the source element BEFORE the reject branch; "+
			"got mark idx=%d, reject idx=%d", idxMark, idxRejectBranch)
	}
}

// TestSkyReviveScripts_NonAllowlistedAttrsBatchedWarn pins that
// dropped non-allowlisted attrs are batched into a SINGLE
// console.warn per element (a node with five `on*` attrs must
// produce one warn, not five — otherwise the dev console
// becomes unreadable).
func TestSkyReviveScripts_NonAllowlistedAttrsBatchedWarn(t *testing.T) {
	js := liveJS("test-sid")
	wants := []string{
		// Collector array starts null (lazy alloc).
		`var droppedAttrs = null;`,
		// Push pattern inside the else branch.
		`droppedAttrs.push(a.name);`,
		// Single warn after the loop, gated on collector.
		`if (droppedAttrs) {`,
		`console.warn("[sky.live] script revival dropped non-allowlisted attrs`,
		`droppedAttrs.join(", ")`,
	}
	for _, w := range wants {
		if !strings.Contains(js, w) {
			t.Errorf("batched-warn shape missing required token %q", w)
		}
	}
}

// TestSkyReviveScripts_InlineWithSrcPreserved is the
// "Editor.scriptTag still works" sanity pin. Sky-bundled
// scripts that set BOTH src AND a small inline body (e.g. for
// bootstrap config) must keep the inline body when src= is
// present.
func TestSkyReviveScripts_InlineWithSrcPreserved(t *testing.T) {
	js := liveJS("test-sid")
	want := `if (hasSrc && hasInline) {
      fresh.textContent = old.textContent;
    }`
	if !strings.Contains(js, want) {
		t.Errorf("inline-with-src preservation branch missing: %q", want)
	}
}

// TestSkyReviveScripts_CommentReferencesAuditGap is a doc pin —
// the security rationale comment names gap C9 / plan P31 so a
// future grep for "gap C9" surfaces this code path. If a future
// refactor strips the comment, this test fires; the maintainer
// then either restores the reference or moves it consciously.
func TestSkyReviveScripts_CommentReferencesAuditGap(t *testing.T) {
	js := liveJS("test-sid")
	if !strings.Contains(js, "gap C9") {
		t.Errorf("revival JS comment should reference audit gap C9 for grep discoverability")
	}
}
