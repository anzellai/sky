package rt

import (
	"strings"
	"testing"
)

// TestLiveJS_SubmitFilterByActualSubmitter guards the submitter-aware
// form-data extraction inside __skyExtractArgs's "submit" branch.
//
// Bug fenced here: when a <form> carries multiple submit buttons that
// share a name (e.g. three <button type="submit" name="action"
// value="save|format|check">), the naive "iterate every form element"
// loop lets later submit buttons overwrite earlier ones — so the
// payload always carries the LAST button's value regardless of which
// the user actually clicked. Editor-style toolbars are the obvious
// failure case (clicking Format runs Check; clicking Save runs
// Check), but any multi-button form with shared names is affected.
//
// Spec: HTML5 form submission only includes the SUBMITTER's
// name/value in the form data, NOT the values of peer submit
// buttons. ev.submitter is the spec-required carrier (modern
// browsers; document.activeElement is the legacy fallback for old
// Safari).
//
// These markers fence the fix:
//
//  1. Look up the submitter (ev.submitter || activeElement-in-form).
//  2. For submit/button/image/reset-typed elements, only the
//     submitter contributes its name/value.
//  3. Skip disabled fields entirely (spec requires this; otherwise
//     a disabled-but-named field leaks a stale value).
func TestLiveJS_SubmitFilterByActualSubmitter(t *testing.T) {
	js := liveJS("test-sid")
	required := []string{
		// Submitter resolution — modern browsers provide ev.submitter;
		// fall back to document.activeElement scoped to the form.
		`var submitter = ev.submitter ||`,
		`document.activeElement && t && t.contains(document.activeElement)`,
		// Only the submitter button contributes its name/value.
		`if (el.type === "submit" || el.type === "button" ||`,
		`if (el === submitter) data[el.name] = el.value;`,
		// Disabled-field skip is part of the spec-correct path.
		`if (!el.name || el.disabled) continue;`,
	}
	for _, want := range required {
		if !strings.Contains(js, want) {
			t.Errorf("liveJS missing submitter-filter marker: %q", want)
		}
	}

	// Equally important: ensure we never re-introduce the old
	// "include every form element" loop that would clobber the
	// payload. The old code had no submit/button skip and no
	// submitter check. If a future refactor drops these, the
	// editor's three-button toolbar regresses silently.
	forbidden := []string{
		// The old loop with no submitter filter — if this exact
		// shape re-appears, the bug is back.
		`if (!el.name) continue;
          if (el.type === "checkbox"`,
	}
	for _, no := range forbidden {
		if strings.Contains(js, no) {
			t.Errorf("liveJS regressed: pre-fix submit loop reintroduced (%q)", no)
		}
	}
}
