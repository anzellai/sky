package rt

import (
	"strings"
	"testing"
)

// Regression fence for v0.17 G2 (Sky.Tui silent-drop diagnostic).
//
// Before this fix:
//   - Pseudo-class style blocks (`Background.hoverColor`,
//     `focusColor`, `activeColor`, `Font.hoverSize`,
//     `Border.hoverWidth`, the generic `Ui.onPseudo` escape) emit
//     a `data-sky-pc-rules` AttrAttribute payload. The TUI's
//     `walkAttrs` tag-12 branch hit the default arm and logged a
//     generic "raw HTML attribute data-sky-pc-rules" warning that
//     gave no hint as to which Std.Ui feature was being silently
//     dropped.
//   - `Ui.mediaQuery` / `Ui.breakpoint` emit
//     `data-sky-mq-q` + `data-sky-mq-rules`. Same generic
//     "raw HTML attribute" noise.
//   - `Std.Ui.Transition.attribute` emits `data-sky-tr-rules` /
//     `data-sky-tr-respect`. Same.
//   - `Std.Ui.Animation.attribute` emits `data-sky-anim-rules` /
//     `data-sky-anim-respect`. Same.
//   - `Ui.aspectRatio` / `aspectRatioWH` emit `AttrStyle
//     "aspect-ratio" "16 / 9"`. The AttrStyle default branch
//     warned generically as "raw CSS attribute aspect-ratio".
//   - `Std.Ui.Grid.tracks` emits the `__gridTracks` internal
//     marker which the AttrStyle switch never recognised; the
//     default arm short-circuited on `isInternalMarker(k)` and
//     emitted NO warning at all.
//
// CLAUDE.md's Sky.Tui section claims unsupported attrs emit a
// "deduped tuiWarn"; G2 from
// `docs/architecture/sky-stdlib-correctness.md` flagged that the
// claim was inaccurate for these specific markers — users saw
// degraded rendering with no diagnostic.
//
// Fix: route each marker to a category-specific tuiWarn with a
// detail string that names the Std.Ui feature, before the
// generic fallback. Each call dedupes through the existing
// tuiWarn ledger so the summary at exit shows one line per
// dropped feature.

func TestTui_UnsupportedMarkers_PseudoClass(t *testing.T) {
	tuiResetWarnings()
	attr := SkyADT{
		Tag:     12, // AttrAttribute "k" "v"
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-pc-rules", "h| background-color: rgba(0, 92, 215, 1);"},
	}
	_ = walkAttrs([]any{attr}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	w := tuiWarnSeen["pseudo-class::hover / :focus / :active / :disabled (terminal has no CSS pseudo-class engine — base style still renders)"]
	if w == nil {
		t.Fatalf("pseudo-class warning not recorded; saw keys: %v", keysOf(tuiWarnSeen))
	}
	if _, leaked := tuiWarnSeen["attribute:raw HTML attribute data-sky-pc-rules"]; leaked {
		t.Errorf("generic 'raw HTML attribute' warning leaked for data-sky-pc-rules")
	}
}

func TestTui_UnsupportedMarkers_MediaQuery(t *testing.T) {
	tuiResetWarnings()
	attrQ := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-mq-q", "(max-width: 767px)"},
	}
	attrR := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-mq-rules", "padding: 8px; flex-direction: column;"},
	}
	_ = walkAttrs([]any{attrQ, attrR}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if _, ok := tuiWarnSeen["media-query:@media rule (terminal viewport is fixed — base style still renders)"]; !ok {
		t.Fatalf("media-query warning not recorded; saw: %v", keysOf(tuiWarnSeen))
	}
	for k := range tuiWarnSeen {
		if strings.Contains(k, "raw HTML attribute data-sky-mq") {
			t.Errorf("generic 'raw HTML attribute' leaked for %q", k)
		}
	}
}

func TestTui_UnsupportedMarkers_Transition(t *testing.T) {
	tuiResetWarnings()
	attrR := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-tr-rules", "background-color 200ms ease-out"},
	}
	attrA := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-tr-respect", "1"},
	}
	_ = walkAttrs([]any{attrR, attrA}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if _, ok := tuiWarnSeen["transition:CSS transition (terminal can't interpolate between frames)"]; !ok {
		t.Fatalf("transition warning not recorded; saw: %v", keysOf(tuiWarnSeen))
	}
	for k := range tuiWarnSeen {
		if strings.Contains(k, "raw HTML attribute data-sky-tr") {
			t.Errorf("generic 'raw HTML attribute' leaked for %q", k)
		}
	}
}

func TestTui_UnsupportedMarkers_Animation(t *testing.T) {
	tuiResetWarnings()
	attrR := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-anim-rules", "name=fadeIn duration=400ms iters=1"},
	}
	attrA := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-anim-respect", "1"},
	}
	_ = walkAttrs([]any{attrR, attrA}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if _, ok := tuiWarnSeen["animation:@keyframes animation (terminal renders final keyframe only — no per-frame loop)"]; !ok {
		t.Fatalf("animation warning not recorded; saw: %v", keysOf(tuiWarnSeen))
	}
	for k := range tuiWarnSeen {
		if strings.Contains(k, "raw HTML attribute data-sky-anim") {
			t.Errorf("generic 'raw HTML attribute' leaked for %q", k)
		}
	}
}

func TestTui_UnsupportedMarkers_AspectRatio(t *testing.T) {
	tuiResetWarnings()
	attr := SkyADT{
		Tag:     8, // AttrStyle "k" "v"
		SkyName: "AttrStyle",
		Fields:  []any{"aspect-ratio", "16 / 9"},
	}
	_ = walkAttrs([]any{attr}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if _, ok := tuiWarnSeen["layout:aspect-ratio (terminal cells don't honour CSS aspect-ratio)"]; !ok {
		t.Fatalf("aspect-ratio warning not recorded; saw: %v", keysOf(tuiWarnSeen))
	}
	if _, leaked := tuiWarnSeen["style:raw CSS attribute aspect-ratio"]; leaked {
		t.Errorf("generic 'raw CSS attribute aspect-ratio' leaked (should be category-specific)")
	}
}

func TestTui_UnsupportedMarkers_GridTracks(t *testing.T) {
	tuiResetWarnings()
	attr := SkyADT{
		Tag:     8, // AttrStyle "k" "v"
		SkyName: "AttrStyle",
		Fields:  []any{"__gridTracks", "1fr 200px 1fr|auto"},
	}
	_ = walkAttrs([]any{attr}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	if _, ok := tuiWarnSeen["layout:explicit grid tracks (terminal can't render fr/minmax/auto)"]; !ok {
		t.Fatalf("__gridTracks warning not recorded; saw: %v", keysOf(tuiWarnSeen))
	}
}

func TestTui_UnsupportedMarkers_DataSkyPathSilent(t *testing.T) {
	// data-sky-path is the Sky.Live URL-sync sentinel. It has no
	// TUI analogue but it is NOT a "missing feature" — apps can
	// emit it unconditionally even for TUI builds. Verify we
	// silently skip it (no warning at all) rather than spam the
	// summary with a useless line.
	tuiResetWarnings()
	attr := SkyADT{
		Tag:     12,
		SkyName: "AttrAttribute",
		Fields:  []any{"data-sky-path", "/dashboard"},
	}
	_ = walkAttrs([]any{attr}, tuiLayoutCtx{cols: 80, rows: 24})

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	for k := range tuiWarnSeen {
		if strings.Contains(k, "data-sky-path") {
			t.Errorf("data-sky-path should be silent skip; got warning %q", k)
		}
	}
}

func TestTui_UnsupportedMarkers_AllDedupe(t *testing.T) {
	// One element with the full suite of unsupported markers
	// produces one warning PER category — even across multiple
	// render passes (tuiWarn dedupes by key).
	tuiResetWarnings()
	attrs := []any{
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"data-sky-pc-rules", "h| color: red;"}},
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"data-sky-mq-q", "(prefers-color-scheme: dark)"}},
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"data-sky-mq-rules", "color: white;"}},
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"data-sky-tr-rules", "color 100ms"}},
		SkyADT{Tag: 12, SkyName: "AttrAttribute", Fields: []any{"data-sky-anim-rules", "name=spin duration=1s"}},
		SkyADT{Tag: 8, SkyName: "AttrStyle", Fields: []any{"aspect-ratio", "1 / 1"}},
		SkyADT{Tag: 8, SkyName: "AttrStyle", Fields: []any{"__gridTracks", "1fr 1fr|auto"}},
	}
	// Render 5 times.  Each marker fires once; subsequent calls
	// only increment the count.
	for i := 0; i < 5; i++ {
		_ = walkAttrs(attrs, tuiLayoutCtx{cols: 80, rows: 24})
	}

	tuiWarnMu.Lock()
	defer tuiWarnMu.Unlock()
	wantKeys := []string{
		"pseudo-class::hover / :focus / :active / :disabled (terminal has no CSS pseudo-class engine — base style still renders)",
		"media-query:@media rule (terminal viewport is fixed — base style still renders)",
		"transition:CSS transition (terminal can't interpolate between frames)",
		"animation:@keyframes animation (terminal renders final keyframe only — no per-frame loop)",
		"layout:aspect-ratio (terminal cells don't honour CSS aspect-ratio)",
		"layout:explicit grid tracks (terminal can't render fr/minmax/auto)",
	}
	for _, k := range wantKeys {
		w := tuiWarnSeen[k]
		if w == nil {
			t.Errorf("missing warning %q; saw %v", k, keysOf(tuiWarnSeen))
			continue
		}
		if w.count < 5 {
			t.Errorf("warning %q count=%d, want >=5 (dedupe should still increment)", k, w.count)
		}
	}
}

func keysOf(m map[string]*tuiWarning) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
