//go:build js

package rt

// Std_App_desktopWindowMode — the wasm client never serves a desktop window;
// the kernel exists so a Std.App module that references it still links.
func Std_App_desktopWindowMode(_ any) any {
	return func() any { return Ok[any, any](struct{}{}) }
}
