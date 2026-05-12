// runtime-go/rt/gui.go — Sky.Gui backend (gio-based native window)
//
// Status: SPIKE (v0.13 milestone, branch exp/sky-gui-gio)
//
// Sibling backend to Sky.Live (HTML/SSE) and Sky.Tui (ANSI cells).
// Same TEA shape: init / update / view / subscriptions; same Std.Ui
// source code. The Std.Ui → gio interpreter is the bulk of the work
// for v0.13; this file is currently the spike entry point that opens
// a window, renders a placeholder, and exits cleanly on window close.
//
// Why gio: see docs/design/sky-gui-gio-plan.md. TL;DR — only viable
// pure-Go cross-platform (Mac/Win/Linux + iOS/Android) UI library
// that fits TEA's pure-view shape cleanly. The cgo footprint is
// bounded to OS window/event interop (no cgo for the rendering
// pipeline itself), and end users don't need any runtime deps —
// gio links against system frameworks only.

package rt

import (
	"image/color"
	"os"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// Gui_app — entry point matching Tui_app / Live_app shape. Takes the
// same {init, update, view, subscriptions, …} cfg record. Returns a
// Task (deferred until the user binds it to `main`).
//
// SPIKE BEHAVIOUR: ignores cfg's view function entirely and renders a
// placeholder "Sky.Gui spike" label. Stage 2 (per the plan doc)
// wires up the actual Std.Ui → gio interpreter.
func Gui_app(cfg any) any {
	return func() any {
		return guiAppRun(cfg)
	}
}

// guiAppRun: synchronous entry — opens the gio window, blocks on
// the event loop, returns when the user closes the window.
//
// gio's main loop must run on the OS main thread on most platforms
// (Cocoa requirement on macOS, etc.). We spawn the app logic on a
// goroutine and call app.Main() from the calling thread, which
// pumps OS events.
func guiAppRun(cfg any) any {
	title := "Sky.Gui"
	if cfgTitle, ok := guiCfgString(cfg, "title"); ok && cfgTitle != "" {
		title = cfgTitle
	}
	width := 800
	if w, ok := guiCfgInt(cfg, "width"); ok && w > 0 {
		width = w
	}
	height := 600
	if h, ok := guiCfgInt(cfg, "height"); ok && h > 0 {
		height = h
	}

	go guiRunLoop(title, width, height)
	app.Main()
	return SkyResult[any, any]{Tag: 0, OkValue: struct{}{}}
}

// guiRunLoop: the actual render/event loop. Stage 1 spike renders a
// fixed placeholder. Stage 2 will receive the user's view function
// + model + Msg channel and call view(model) per frame, dispatching
// events back through Sky's update.
func guiRunLoop(title string, width, height int) {
	w := new(app.Window)
	w.Option(
		app.Title(title),
		app.Size(unit.Dp(float32(width)), unit.Dp(float32(height))),
	)

	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

	var ops op.Ops
	for {
		switch ev := w.Event().(type) {
		case app.DestroyEvent:
			// Clean exit on window close.
			if ev.Err != nil {
				os.Exit(1)
			}
			os.Exit(0)

		case app.FrameEvent:
			gtx := app.NewContext(&ops, ev)
			guiRenderPlaceholder(gtx, th)
			ev.Frame(gtx.Ops)
		}
	}
}

// guiRenderPlaceholder: the Stage 1 spike payload. Renders centered
// text. Stage 2 replaces this with the Std.Ui → gio interpreter
// driven by the user's `view` function.
func guiRenderPlaceholder(gtx layout.Context, th *material.Theme) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.H4(th, "Sky.Gui — spike")
				label.Color = color.NRGBA{R: 0x1a, G: 0x20, B: 0x2c, A: 0xff}
				return label.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				sub := material.Body1(th, "Std.Ui → gio interpreter pending (Stage 2)")
				sub.Color = color.NRGBA{R: 0x4a, G: 0x55, B: 0x68, A: 0xff}
				return sub.Layout(gtx)
			}),
		)
	})
}

// guiCfgString / guiCfgInt — best-effort reads on the cfg record.
// Sky lowers TEA configs to map[string]any (via RecordUpdate-style
// emission). For the spike we accept that shape and ignore strict
// typing; the Stage 2 interpreter will use the typed-codegen
// signature directly.
func guiCfgString(cfg any, key string) (string, bool) {
	if m, ok := cfg.(map[string]any); ok {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

func guiCfgInt(cfg any, key string) (int, bool) {
	if m, ok := cfg.(map[string]any); ok {
		if v, ok := m[key]; ok {
			switch n := v.(type) {
			case int:
				return n, true
			case int64:
				return int(n), true
			case float64:
				return int(n), true
			}
		}
	}
	return 0, false
}
