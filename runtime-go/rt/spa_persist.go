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
		// The model codec (`Codec.auto`) writes snake_case keys, so a camelCase
		// field `userName` is `user_name` in both blobs; accept either spelling.
		for _, k := range spaFieldKeys(f) {
			if v, ok := seed[k]; ok {
				stored[k] = v
			}
		}
	}
	out, err := json.Marshal(stored)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// spaFieldKeys returns the JSON keys a model field may be stored under: its Sky
// name and the snake_case key `Codec.auto` derives from it.
func spaFieldKeys(f string) []string {
	s := camelToSnake(f)
	if s == f {
		return []string{f}
	}
	return []string{f, s}
}

// spaSeedWinsFields is the set of fields a full load takes from the SSR seed
// rather than localStorage (R2): the protected (session) fields, the fields the
// `withRequest` hook writes (it runs on every page request, as Sky.Live re-runs
// it over a restored model), and the fields the server SETTLED for THIS page —
// the write-sets of the init and onNavigate command chains it finished (the
// page's `data-sky-seed-fields`). Every other field keeps its stored value,
// even one only server branches write: the seed holds `init`'s default for a
// field this page did not load, and a default must never paint over data.
func spaSeedWinsFields(protected, request, settled []string) []string {
	out := make([]string, 0, len(protected)+len(request)+len(settled))
	out = append(out, protected...)
	out = append(out, request...)
	return append(out, settled...)
}

// spaFirstPaintPlan decides how an SSR first paint proceeds after a localStorage
// restore replaced the model. When the restored model's JSON differs from the SSR
// seed's JSON, the client must paint in TWO steps: first hydrate/adopt the SEED
// render (which matches the server DOM by construction, since the server rendered
// from the seed), then diff-patch to the restored model through the normal path.
// A single hydrate of the restored tree would bind handlers to the server's (seed)
// markup but never patch its text/attrs/children, leaving stale content on screen.
//
// It returns twoStep=false — a single render, exactly today's behaviour — when the
// restored model is byte-equal to the seed (nothing to patch), or when either blob
// is empty (no seed means no server markup to diverge from; no restored model means
// no restore happened). The comparison normalises through a JSON round-trip so key
// order or whitespace differences do not force a needless second render. Pure and
// host-testable; never panics.
func spaFirstPaintPlan(seedJSON, restoredJSON string) (twoStep bool) {
	if seedJSON == "" || restoredJSON == "" {
		return false
	}
	return !spaJSONEqual(seedJSON, restoredJSON)
}

// spaJSONEqual reports whether two JSON blobs are semantically equal, ignoring
// object key order and insignificant whitespace. A blob that fails to parse is
// treated as not-equal (fail toward the two-step paint, which is always correct —
// it just costs one extra diff). Never panics.
func spaJSONEqual(a, b string) bool {
	if a == b {
		return true
	}
	var av, bv any
	if json.Unmarshal([]byte(a), &av) != nil {
		return false
	}
	if json.Unmarshal([]byte(b), &bv) != nil {
		return false
	}
	na, err := json.Marshal(av)
	if err != nil {
		return false
	}
	nb, err := json.Marshal(bv)
	if err != nil {
		return false
	}
	return string(na) == string(nb)
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
