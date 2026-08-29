package rt

// Std_App_htmlDocOrDefault routes a `Std.App` `ViewUi` value at render time.
//
// The `App.app` config field is `view : model -> Element msg`, and the
// web/desktop/mobile runners render it with `Ui.layout [] (v model)` (wrap the
// Element in a page-tall document). A user, however, can annotate their view
// `model -> any` and root it at `Ui.layout` — returning a full `Html` DOCUMENT
// where an `Element` is expected. The `any` annotation plus a polymorphic `msg`
// lets `Html a` unify with `Element msg`, so `sky check` (and `go build`) pass.
//
// `Std_Ui_Element` and `Std_Html_Html` are BOTH the type alias `rt.SkyADT`, so
// the mismatch is invisible to the type-erased record boundary: the
// `rt.Coerce[Std_Ui_Element](htmlValue)` emitted at the config join is a no-op
// (`v.(T)` succeeds, same underlying type) and the raw `Html` document reaches
// the runner UNCHANGED. Wrapping it in another `Ui.layout` is then wrong two
// ways: `Std_Ui_renderElement` dispatches on the ADT Tag, and Html's `HElement`
// constructor and Element's `Empty` constructor BOTH carry Tag 0 — so the whole
// document is read as an empty Element and the page renders SILENTLY BLANK.
//
// The fix routes on the one discriminator that survives erasure: the
// constructor NAME. Html-document constructors (`HElement` / `HText` / `HRaw`)
// are disjoint from Element constructors (`Empty` / `Text` / `Node` /
// `TaggedNode` / `Raw`), even though their Tags collide. So:
//
//   - `el` is already an Html document (SkyName in the Html set) -> return it
//     unchanged; the user rooted at `Ui.layout` and the escape is harmless.
//   - otherwise `el` is a genuine Element -> return `deflt`, which the caller
//     computed as `Ui.layout [] el` (the unchanged, correct behaviour).
//
// Only a value whose runtime constructor is literally an Html node triggers the
// pass-through, so every ordinary `Element` view is completely unaffected.
func Std_App_htmlDocOrDefault(el any, deflt any) any {
	if adt, ok := el.(SkyADT); ok {
		switch adt.SkyName {
		case "HElement", "HText", "HRaw":
			// Already a Std.Html document — the user wrapped in Ui.layout
			// themselves (escaping via `-> any`). Render it directly.
			return el
		}
	}
	// A genuine Std.Ui Element (or any non-Html value): use the caller's
	// `Ui.layout [] el` wrapping — the behaviour that has always been correct.
	return deflt
}

// Std_App_asElement reinterprets a value at the App view boundary as an
// `Element`. `Std_Ui_Element` and `Std_Html_Html` are BOTH the alias `rt.SkyADT`
// (identical Go rep), so this is a runtime IDENTITY. It exists only so the
// `App.web` render path (whose view slot is `Html msg`) can reuse
// `renderUiRoot_` / `htmlDocOrDefault` — both typed `Element msg -> …` — on the
// SYMMETRIC escape: a user annotating an `App.web` view `model -> any` and
// returning a Std.Ui `Element`. Without this the runner renders that Element as
// Html, the Element constructors dispatch to nothing, and the page is silently
// blank (the App.app hole's twin). After the cast, `htmlDocOrDefault`'s SkyName
// check routes it: a genuine Html doc (`HElement`/`HText`/`HRaw`) passes
// through unchanged; a crossed-in Element (`Empty`/`Text`/`Node`/…) is wrapped
// in `Ui.layout []`.
func Std_App_asElement(v any) any { return v }
