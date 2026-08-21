//go:build !js

// Package rt — Sky.Tui typed-builder config kernels (v0.19 Path A).
//
// Mirror of live_config.go for the terminal backend. `Tui.config { init,
// update, view, subscriptions }` produces an opaque AppConfig; optional
// fields (onKey / guard / canvasWidth / canvasHeight) are attached with the
// `withX` builders. The built object is a map[string]any keyed with the
// exact PascalCase names Tui_app reads via rt.Field(cfg,"…") (tui_ui.go),
// so Tui_app is UNCHANGED. The same four invariants as live_config.go hold
// (exact keys / values `any` / unset optional ABSENT; store callbacks
// verbatim; shallow-clone before set; sub-records Field-readable).
//
// NOTE: Tui_app ignores Routes/NotFound (they were vestigial in the old
// row-open record), so `Tui.config` takes only the four genuinely-read
// required fields.
package rt

// Tui_config builds the opaque AppConfig from the four required fields.
func Tui_config(req any) any {
	return map[string]any{
		"Init":          Field(req, "Init"),
		"Update":        Field(req, "Update"),
		"View":          Field(req, "View"),
		"Subscriptions": Field(req, "Subscriptions"),
	}
}

// tuiCfgSet returns a shallow clone of the config map with key=val set
// (invariants 1–3), identical discipline to liveCfgSet.
func tuiCfgSet(cfg any, key string, val any) any {
	src, ok := cfg.(map[string]any)
	if !ok {
		src = map[string]any{}
	}
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	out[key] = val
	return out
}

// Tui_withOnKey — `onKey : KeyEvent -> msg` raw key-event handler.
func Tui_withOnKey(fn, cfg any) any { return tuiCfgSet(cfg, "OnKey", fn) }

// Tui_withGuard — `guard : msg -> model -> Result Error ()` per-Msg gate.
func Tui_withGuard(fn, cfg any) any { return tuiCfgSet(cfg, "Guard", fn) }

// Tui_withCanvasWidth — `canvasWidth : Int` logical design width (default 1280).
func Tui_withCanvasWidth(w, cfg any) any { return tuiCfgSet(cfg, "CanvasWidth", w) }

// Tui_withCanvasHeight — `canvasHeight : Int` logical design height (default 720).
func Tui_withCanvasHeight(h, cfg any) any { return tuiCfgSet(cfg, "CanvasHeight", h) }
