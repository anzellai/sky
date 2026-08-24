//go:build !js

package rt

// Native_geolocation on a NON-client build (native CLI / server). Geolocation is
// a client capability — it needs the webview/browser's platform location
// service — so there is nothing to call here; return Err rather than pretend.
// The real implementation is the wasm build (native_wasm.go).
func Native_geolocation(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.geolocation is a client-only capability (no location service in this runtime)"))
	}
}

// The rest of Std.Native is likewise client-only: clipboard, vibration, and the
// share sheet all live in the browser/webview. On a native (server/CLI) build
// there is nothing to call, so each returns Err rather than pretend. The real
// implementations are the wasm build (native_wasm.go).

func Native_clipboardWrite(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.clipboardWrite is a client-only capability (no clipboard in this runtime)"))
	}
}

func Native_clipboardRead(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.clipboardRead is a client-only capability (no clipboard in this runtime)"))
	}
}

func Native_vibrate(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.vibrate is a client-only capability (no vibration hardware in this runtime)"))
	}
}

func Native_share(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.share is a client-only capability (no share sheet in this runtime)"))
	}
}

func Native_storageSet(_ any, _ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.storageSet is a client-only capability (no local storage in this runtime)"))
	}
}

func Native_storageGet(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.storageGet is a client-only capability (no local storage in this runtime)"))
	}
}

func Native_storageRemove(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.storageRemove is a client-only capability (no local storage in this runtime)"))
	}
}

func Native_isOnline(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.isOnline is a client-only capability (no navigator in this runtime)"))
	}
}

func Native_language(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.language is a client-only capability (no navigator in this runtime)"))
	}
}

func Native_setTitle(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.setTitle is a client-only capability (no document in this runtime)"))
	}
}

func Native_prefersDarkMode(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.prefersDarkMode is a client-only capability (no media queries in this runtime)"))
	}
}

func Native_openUrl(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.openUrl is a client-only capability (no window in this runtime)"))
	}
}

func Native_notify(_ any, _ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.notify is a client-only capability (no notifications in this runtime)"))
	}
}

func Native_batteryStatus(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.batteryStatus is a client-only capability (no battery API in this runtime)"))
	}
}

func Native_pickFile(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.pickFile is a client-only capability (no file picker in this runtime)"))
	}
}

func Native_pickImage(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.pickImage is a client-only capability (no file picker in this runtime)"))
	}
}

func Native_capturePhoto(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.capturePhoto is a client-only capability (no camera in this runtime)"))
	}
}

func Native_bridge(_ any, _ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.bridge is a client-only capability (no native bridge in this runtime)"))
	}
}
