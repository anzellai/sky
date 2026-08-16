package main

// Handler selection, and the per-session setup script.
//
// WHY THIS FILE EXISTS
//
// The generator used to pick its steady-state handler as "the first
// data-sky-hid on the page ending in .click". On `examples/26-ui-showcase`
// that is a counter button and every interaction mutates the model, so the
// choice was invisible. On `examples/19-skyforum` the first .click is the
// site title in the top nav, wired to `Navigate HomePage` -- which, ON the
// home page, produces a model identical to the one it started from, hence an
// identical view, hence ZERO patches.
//
// Three archived runs (docs/perf/runs/attribution-20260815/viewsize/forum-r*)
// therefore recorded 5394, 5401 and 5385 interactions of a user clicking a
// logo that does nothing, and were quoted as "cost tracks view size". The
// generator DID flag them `"valid": false`; the sweep script recorded them
// anyway. Both halves are fixed: this file makes the handler choosable, and
// perfrun.sh now propagates the generator's non-zero exit.
//
// The two knobs are deliberately structural rather than app-specific:
//
//	-hid-suffix   which DOM event   (".click", ".submit", ...)
//	-hid-context  a regex matched against the ~300 bytes FOLLOWING the
//	              hid attribute -- i.e. the element's own attributes and
//	              text. Sky handler ids are structural paths
//	              (`r.1#div.1#div.0#div.1#div.0#button.click`), so they
//	              carry no semantics to match on; the rendered text does.
//	              `-hid-context '▲'` names skyforum's upvote button
//	              without the generator knowing what skyforum is.
//
// -setup runs a fixed sequence of interactions once per session before the
// measurement window opens (skyforum needs a sign-in: every vote handler
// reroutes an anonymous user to the login page instead of mutating). Each
// step re-fetches `/` afterwards so the next step, and the steady-state
// pick, see the handler ids of the page the app is actually on.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
)

// hidContextWindow is how many bytes after the `data-sky-hid="..."` attribute
// -hid-context is matched against. Large enough to reach the element's text
// through a `style="..."` (Std.Ui emits inline styles, ~150-250 bytes),
// small enough not to spill into the next sibling's text.
const hidContextWindow = 300

// setupStep is one scripted interaction performed once per session before
// measurement. `Args` is spliced into the event payload's `args` verbatim,
// which is how a form submit is expressed on the wire: a submit handler
// receives a single object of [name]=value (live.go:8380).
type setupStep struct {
	Name       string `json:"name"`
	HidSuffix  string `json:"hid_suffix"`
	HidContext string `json:"hid_context"`
	Args       []any  `json:"args"`

	ctxRe *regexp.Regexp
}

func loadSetup(path string) ([]setupStep, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var steps []setupStep
	if err := json.Unmarshal(b, &steps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range steps {
		if steps[i].HidSuffix == "" {
			steps[i].HidSuffix = ".click"
		}
		if steps[i].HidContext != "" {
			re, err := regexp.Compile(steps[i].HidContext)
			if err != nil {
				return nil, fmt.Errorf("%s step %q: bad hid_context: %w", path, steps[i].Name, err)
			}
			steps[i].ctxRe = re
		}
		if steps[i].Args == nil {
			steps[i].Args = []any{}
		}
	}
	return steps, nil
}

// pickHandler finds the handler id to POST. It returns an error rather than a
// fallback: silently substituting a different handler is precisely how the
// forum runs came to measure a no-op, and a run that cannot find the handler
// it was told to use has not measured what it claims to.
func pickHandler(body []byte, suffix string, ctxRe *regexp.Regexp) (string, error) {
	ms := hidRe.FindAllSubmatchIndex(body, -1)
	if len(ms) == 0 {
		return "", fmt.Errorf("no data-sky-hid in the served HTML (%d bytes): this "+
			"page has no interactive handlers", len(body))
	}
	var sawSuffix int
	for i, m := range ms {
		hid := string(body[m[2]:m[3]])
		if suffix != "" && !hasSuffix(hid, suffix) {
			continue
		}
		sawSuffix++
		if ctxRe != nil {
			end := m[1] + hidContextWindow
			// Stop at the NEXT handler's attribute whatever the window says.
			// Without this a short element followed by the intended one
			// matches its neighbour's text and the run measures a handler
			// nobody asked for -- the same class of silent substitution that
			// put three empty-exchange runs into the corpus, one layer down.
			if i+1 < len(ms) && ms[i+1][0] < end {
				end = ms[i+1][0]
			}
			if end > len(body) {
				end = len(body)
			}
			if !ctxRe.Match(body[m[1]:end]) {
				continue
			}
		}
		return hid, nil
	}
	if ctxRe != nil {
		return "", fmt.Errorf("no handler matched suffix %q AND context /%s/ "+
			"(%d hids on the page, %d with that suffix); the generator will not "+
			"quietly substitute another handler",
			suffix, ctxRe.String(), len(ms), sawSuffix)
	}
	return "", fmt.Errorf("no handler id ends in %q (%d hids on the page)", suffix, len(ms))
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}

// refetch re-GETs `/` on the session's cookie jar and returns the body. Used
// after every setup step, because a step that navigates changes every
// structural handler id on the page.
func (s *session) refetch(ctx context.Context) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET / returned %d, want 200", resp.StatusCode)
	}
	return body, nil
}

// runSetup performs the scripted steps, then re-picks the steady-state
// handler against the page the session has ended up on.
func (s *session) runSetup(ctx context.Context, steps []setupStep, suffix string, ctxRe *regexp.Regexp) error {
	body := s.lastBody
	for i, st := range steps {
		hid, err := pickHandler(body, st.HidSuffix, st.ctxRe)
		if err != nil {
			return fmt.Errorf("setup step %d (%s): %w", i+1, st.Name, err)
		}
		if _, out, _, _ := s.interactWith(ctx, int64(-len(steps)+i), hid, st.Args); out != outOK && out != outNoPatches {
			return fmt.Errorf("setup step %d (%s) classified %s", i+1, st.Name, out)
		}
		if body, err = s.refetch(ctx); err != nil {
			return fmt.Errorf("setup step %d (%s): re-GET: %w", i+1, st.Name, err)
		}
	}
	hid, err := pickHandler(body, suffix, ctxRe)
	if err != nil {
		return fmt.Errorf("after setup: %w", err)
	}
	s.handler = hid
	s.lastBody = body
	return nil
}
