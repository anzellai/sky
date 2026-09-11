//go:build js

package rt

// spa_persist_wasm.go — the js/wasm wiring for Sky.Spa client scratch-state
// persistence (P2). The DECISION functions are portable and host-tested in
// spa_persist.go; this file supplies the browser side: localStorage reads/writes
// (guarded so a private-mode throw is swallowed), reading the SSR `#sky-model`
// seed blob, and the sign-out POST the client JS cannot do itself (clearing the
// httpOnly `sky_sid` cookie). None of this is referenced by the native backend
// (build tag js), and every js access degrades gracefully — a missing/throwing
// localStorage falls back to the SSR seed and never breaks the app.

import "syscall/js"

// spaModelEncoder is the wired `model -> String` encoder (Spa_withModelEncoder),
// or nil when the app has no encoder (persistence then disabled). spaPersistProt
// is the protected (session) field-name list (Spa_withPersistProtectedFields):
// on boot these fields are kept from the SSR seed, never from localStorage.
var (
	spaModelEncoder any
	spaPersistProt  []string
)

// spaEncodeModel applies the wired encoder to a model, returning the JSON string.
// Returns "" when no encoder is wired or the encode throws/panics (guarded so a
// codec panic never kills the instance).
func spaEncodeModel(model any) (out string) {
	if spaModelEncoder == nil || model == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	return AsString(sky_call(spaModelEncoder, model))
}

// spaLocalStorage returns the window.localStorage handle, or a null js.Value when
// it is unavailable or accessing it throws (some sandboxed / privacy contexts
// throw on the property access itself).
func spaLocalStorage() (ls js.Value) {
	defer func() {
		if recover() != nil {
			ls = js.Null()
		}
	}()
	g := js.Global()
	if !g.Truthy() {
		return js.Null()
	}
	v := g.Get("localStorage")
	if !v.Truthy() {
		return js.Null()
	}
	return v
}

// spaReadStoredModel reads the persisted model JSON from localStorage. Returns
// ("", false) when storage is unavailable, the key is absent, or the read throws.
func spaReadStoredModel() (blob string, ok bool) {
	defer func() {
		if recover() != nil {
			blob, ok = "", false
		}
	}()
	ls := spaLocalStorage()
	if !ls.Truthy() {
		return "", false
	}
	v := ls.Call("getItem", spaPersistKey)
	if v.Type() != js.TypeString {
		return "", false
	}
	return v.String(), true
}

// spaWriteStoredModel writes the model JSON to localStorage, skipping a blob over
// the size cap and swallowing any throw (a QuotaExceededError, or a private-mode
// setItem throw). Best effort: a failed write never breaks the app.
func spaWriteStoredModel(blob string) {
	if blob == "" || len(blob) > spaPersistMaxBytes {
		return
	}
	defer func() { _ = recover() }()
	ls := spaLocalStorage()
	if !ls.Truthy() {
		return
	}
	ls.Call("setItem", spaPersistKey, blob)
}

// spaReadSeedBlob reads the SSR-embedded `#sky-model` JSON text, or "" when no
// blob is present (a pure CDN mount with no SSR). js reads guarded.
func spaReadSeedBlob(doc js.Value) (blob string) {
	defer func() {
		if recover() != nil {
			blob = ""
		}
	}()
	if !doc.Truthy() {
		return ""
	}
	el := doc.Call("getElementById", "sky-model")
	if !el.Truthy() {
		return ""
	}
	txt := el.Get("textContent")
	if txt.Type() != js.TypeString {
		return ""
	}
	return txt.String()
}

// spaRestoreFromStorage is the boot restore (called AFTER the SSR seed decision,
// BEFORE the first render). It reads the stored model, merges it over the SSR
// seed (session fields kept from the seed — spaMergeStoredOverSeed), decodes the
// merged JSON with the app's model decoder, and sets spaModel from it. Returns
// true when it restored (so the caller can null init's cmd0). Any failure — no
// encoder, no storage, no stored blob, a merge/decode failure — leaves spaModel
// as the caller set it (the SSR seed or init) and returns false.
func spaRestoreFromStorage(cfg any, doc js.Value) bool {
	if spaModelEncoder == nil {
		return false
	}
	decoder := Field(cfg, "ModelDecoder")
	if decoder == nil {
		return false
	}
	stored, ok := spaReadStoredModel()
	if !ok {
		return false
	}
	seed := spaReadSeedBlob(doc)
	merged, useIt := spaMergeStoredOverSeed(stored, seed, spaPersistProt, spaPersistMaxBytes)
	if !useIt {
		return false
	}
	model, ok := spaDecodeModelBlob(merged, decoder)
	if !ok {
		return false
	}
	spaModel = model
	return true
}

// spaPersistAfterStep runs after each TEA step (live_wasm.go step). It encodes
// the new model and writes it to localStorage, and — when a protected (session)
// field went from set to cleared this step — POSTs the P3 sign-out endpoint to
// clear the httpOnly `sky_sid` cookie. All js access is guarded; a codec panic or
// a storage throw never kills the instance.
func spaPersistAfterStep(prevModel, nextModel any) {
	if spaModelEncoder == nil {
		return
	}
	defer func() { _ = recover() }()
	nextJSON := spaEncodeModel(nextModel)
	spaWriteStoredModel(nextJSON)
	if len(spaPersistProt) == 0 {
		return // no session to protect → nothing to sign out
	}
	prevJSON := spaEncodeModel(prevModel)
	if spaSessionClearedByStep(prevJSON, nextJSON, spaPersistProt) {
		spaPostSignOut()
	}
}

// spaPostSignOut fires POST /_rpc/__spaSignOut (the framework sign-out endpoint
// emitted by the auto-split when a session projection is present) to clear the
// httpOnly cookie. Fire-and-forget in a goroutine because fetchBlocking must run
// off the main event loop (it blocks on the Promise); the result is ignored — a
// failed clear is harmless (the cookie's own Max-Age still bounds it and the next
// verified request settles identity). Same-origin, so the cookie is sent.
func spaPostSignOut() {
	go func() {
		defer func() { _ = recover() }()
		fetchBlocking("POST", "/_rpc/__spaSignOut", "{}")
	}()
}
