package rt

// spa_server_settings.go — the server half of an `App.withConfig` /
// `App.withBase` for the Sky.Spa split backend.
//
//	Spa_applyServerSettings : List ( String, String ) -> Task Error ()
//
// A Sky.Live build applies `App.withConfig (WebConfig { store, ttl, … })` as
// `Live.withX` builder values. The split backend has no Live app, so the
// App→Spa synthesis carries the configuration into a boot task
// (`App.applyServerConfig`) that calls this kernel with each set field as an
// `( env suffix, value )` pair. Each value lands in the BUILDER layer, the
// layer `Sky.Config.withX` uses (applyConfigValue): an operator's variable
// still wins, and a seeded `sky.toml` default is overridden. One precedence,
// shared with the Live build: operator env > code > default.

import "sync"

var (
	serverSettingsMu sync.Mutex
	// serverSettingsBy names who set an applied variable, for the boot line
	// that says where a setting came from (resolveStoreKindSource).
	serverSettingsBy = map[string]string{}
)

// serverSettingSource names the code that set the applied variable `name`,
// or "" when this kernel did not set it.
func serverSettingSource(name string) string {
	serverSettingsMu.Lock()
	defer serverSettingsMu.Unlock()
	return serverSettingsBy[name]
}

// Spa_applyServerSettings applies `( suffix, value )` pairs at the builder
// layer. An empty value is skipped (an unset `Maybe` field).
func Spa_applyServerSettings(pairsArg any) any {
	items := AsList(pairsArg)
	return func() any {
		changed := false
		for _, it := range items {
			t := AsTuple2(it)
			suffix, value := AsString(t.V0), AsString(t.V1)
			if suffix == "" || value == "" {
				continue
			}
			name := skyEnvName(suffix)
			if applyConfigValue(name, value) {
				changed = true
				serverSettingsMu.Lock()
				serverSettingsBy[name] = "App.withConfig"
				serverSettingsMu.Unlock()
			}
		}
		if changed {
			for _, fn := range envPrefixHooks {
				fn()
			}
		}
		return Ok[any, any](struct{}{})
	}
}
