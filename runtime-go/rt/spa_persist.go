package rt

// spa_persist.go — the PORTABLE, host-testable core of Sky.Spa client
// scratch-state persistence (P2). The wasm client writes the WHOLE model to
// browser localStorage after each `update`, and on boot restores it OVER the SSR
// seed — so a reload (web) or an app relaunch (desktop/mobile webview) brings
// back the client scratch state (cart, a dismissed cookie banner, form inputs).
// This mirrors how Sky.Live holds the whole model server-side; here the store is
// the browser, and the mechanism is TRANSPARENT (no app edit).
//
// SECURITY (the one precedence rule). The session field(s) are the server's, not
// the client's. P3 signs the session into an httpOnly `sky_sid` cookie and the
// SSR seed already reflects the cookie-verified session. A stale localStorage
// session must NEVER override it. So on boot we start from the stored model but
// OVERRIDE every protected field with the seed's value. This file does NOT touch
// the P3 sign/verify logic — it only decides which bytes win per field.
//
// The DECISION is build-tag-free and lives here so a host `go test` exercises it
// without a browser. The js wiring (localStorage reads/writes, the sign-out
// fetch) lives in the //go:build js driver.

import "encoding/json"

// spaPersistKey is the localStorage key holding the serialised model.
const spaPersistKey = "sky:spa:model"

// spaPersistMaxBytes caps both the stored blob we will accept on boot and the
// blob we write per step. A model larger than this is not persisted (localStorage
// quotas are small and vary by browser); the app still works, it just does not
// restore scratch state. Chosen well under the ~5 MB per-origin quota so a large
// model degrades gracefully rather than throwing a QuotaExceededError mid-write.
const spaPersistMaxBytes = 1_500_000

// spaMergeStoredOverSeed decides the model JSON the client boots from. It starts
// from the localStorage-stored model and overrides each protected (session)
// field with the SSR seed's value, so the server-verified session always wins.
//
// Rules (all fail SAFE — a bad input falls back to the seed, never a wrong prime,
// and it NEVER panics):
//   - stored empty, or larger than maxBytes            -> ("", false): use seed.
//   - stored not a JSON object                         -> ("", false): use seed.
//   - seed empty (no SSR, e.g. a pure CDN mount)       -> (stored, true): nothing
//     to protect, restore the stored model as-is.
//   - otherwise: merged = stored, then for each protectedField PRESENT in the
//     seed, merged[field] = seed[field]; return (marshal(merged), true).
//
// A protected field absent from the seed is left as stored (the seed did not
// carry a session value, so there is nothing to override with). When
// protectedFields is empty there is no session to protect and the whole stored
// model is restored.
func spaMergeStoredOverSeed(storedJSON, seedJSON string, protectedFields []string, maxBytes int) (mergedJSON string, useIt bool) {
	if storedJSON == "" || (maxBytes > 0 && len(storedJSON) > maxBytes) {
		return "", false
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal([]byte(storedJSON), &stored); err != nil || stored == nil {
		// Not a valid JSON object (an array, a scalar, or malformed) — fall back.
		return "", false
	}
	if seedJSON == "" {
		// No SSR seed: nothing to protect, restore the stored model verbatim.
		return storedJSON, true
	}
	var seed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(seedJSON), &seed); err != nil || seed == nil {
		// A present-but-unparseable seed cannot be trusted to protect the
		// session, so do NOT let the stored session slip through: fall back to
		// the seed path (the caller then keeps the typed seed/init model).
		return "", false
	}
	for _, f := range protectedFields {
		if v, ok := seed[f]; ok {
			stored[f] = v
		}
	}
	out, err := json.Marshal(stored)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// spaSessionClearedByStep reports whether a step signed the user OUT — a
// protected (session) field went from a non-null value in prev to null/absent in
// next. That is the signal to clear the httpOnly `sky_sid` cookie the client JS
// cannot clear itself (via a POST to the P3 sign-out endpoint). It returns false
// on any parse failure (fail safe: never fire a spurious sign-out) and never
// panics.
//
// "non-null in prev" means the field is present with a value other than JSON
// `null`; "null/absent in next" means the field is missing or explicitly `null`.
// A sign-IN (null -> value) or an unchanged field returns false.
func spaSessionClearedByStep(prevJSON, nextJSON string, sessionFields []string) bool {
	if prevJSON == "" || nextJSON == "" {
		return false
	}
	var prev, next map[string]json.RawMessage
	if err := json.Unmarshal([]byte(prevJSON), &prev); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(nextJSON), &next); err != nil {
		return false
	}
	for _, f := range sessionFields {
		wasSet := prev != nil && !spaIsNullRaw(prev[f])
		nowClear := next == nil || spaIsNullRaw(next[f])
		if wasSet && nowClear {
			return true
		}
	}
	return false
}

// spaStringList reads a Sky `List String` config value into a []string. A nil /
// non-list value yields nil (persistence then protects no field). Portable so the
// conversion is host-testable and the native build shares it.
func spaStringList(v any) []string {
	if v == nil {
		return nil
	}
	items := asList(v)
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, e := range items {
		out = append(out, AsString(e))
	}
	return out
}

// spaIsNullRaw reports whether a decoded field slot is absent or JSON `null`. An
// absent key yields a nil RawMessage; an explicit `null` yields the 4 bytes.
func spaIsNullRaw(r json.RawMessage) bool {
	if len(r) == 0 {
		return true
	}
	switch string(r) {
	case "null":
		return true
	}
	return false
}
