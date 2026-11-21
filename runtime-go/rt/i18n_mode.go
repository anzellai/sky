//go:build sky_i18n && !js

package rt

// i18nProduction reports whether a missing message key shows as the key
// (production) or as ⟦key⟧ (development) on the server.
func i18nProduction() bool { return isProductionMode() }
