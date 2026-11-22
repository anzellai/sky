//go:build sky_i18n && js

package rt

// i18nProduction in a Sky.Spa client: the client has no ENV, so a missing
// message key shows as the key, as in production. A catalogue key cannot be
// missing (the build checks it), so this only affects a key made at run time.
func i18nProduction() bool { return true }
