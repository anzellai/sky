//go:build !js

// Package rt — Sky.Cli typed-builder config kernels (v0.19 Path A).
//
// Mirror of live_config.go / tui_config.go for the one-shot CLI backend.
// `Cli.config { init, update, view, subscriptions }` produces an opaque
// AppConfig; the line handler is attached with `Cli.withOnLine`. Built as a
// map[string]any keyed with the exact PascalCase names Cli_program reads via
// rt.Field(cfg,"…") (cli.go), so Cli_program is UNCHANGED. Same four
// invariants as live_config.go.
package rt

// Cli_config builds the opaque AppConfig from the four required fields.
func Cli_config(req any) any {
	return map[string]any{
		"Init":          Field(req, "Init"),
		"Update":        Field(req, "Update"),
		"View":          Field(req, "View"),
		"Subscriptions": Field(req, "Subscriptions"),
	}
}

// cliCfgSet: shallow-clone + set (invariants 1–3), identical to liveCfgSet.
func cliCfgSet(cfg any, key string, val any) any {
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

// Cli_withOnLine — `onLine : String -> msg` stdin-line handler.
func Cli_withOnLine(fn, cfg any) any { return cliCfgSet(cfg, "OnLine", fn) }

// Cli_withDurable — the durable wiring record (restore/persist/setup/runId);
// Cli_program restores the model at start and snapshots it after each update.
func Cli_withDurable(d, cfg any) any { return cliCfgSet(cfg, "Durable", d) }

// Cli_withGuard attaches a `msg -> model -> Result Error ()` guard. It runs
// before update on every Msg, exactly as on Sky.Tui and Sky.Live.
func Cli_withGuard(fn, cfg any) any { return cliCfgSet(cfg, "Guard", fn) }
