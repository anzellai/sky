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

import (
	"bytes"
	"encoding/json"
	"strconv"
	"sync/atomic"

	"sky-app/rt/telemetry"
)

// spaPersistKey is the localStorage key holding the serialised model. It holds
// ONE model, the last one written on this origin, whatever identity wrote it;
// spaRestoreStored restores it only for the identity it was written under.
//
// v2: before v0.25.18 the model sat under spaPersistLegacyKey with no identity
// rule, and an app with a session wrote a signed-out user's leftover data into
// it. That blob cannot prove whose data it holds, so it is deleted on boot.
const (
	spaPersistKey       = "sky:spa:model:v2"
	spaPersistLegacyKey = "sky:spa:model"
)

// spaPersistMaxBytes caps both the stored blob we will accept on boot and the
// blob we write per step. A model larger than this is not persisted; the app
// still works, it just does not restore scratch state.
//
// 3,000,000 bytes (v0.29.9, was 1,500,000). The smallest per-origin
// localStorage limit measured in headless WebKit, Firefox and Chromium
// (Playwright 1.59, 2026-11-10) is 5,241,856 UTF-16 code units for keys plus
// values. The cap counts UTF-8 bytes, which are never fewer than the UTF-16
// units of the same text, so a capped blob leaves at least 42% of that quota
// for the origin's other keys. A write the browser still refuses is handled
// by spaPersistWriteKnownIdentity (the old value stays).
const spaPersistMaxBytes = 3_000_000

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

// spaKV is the storage the persistence rule reads and writes: window.localStorage
// in the wasm client (spa_persist_wasm.go), a map in host tests. Get reports
// whether the key is present. Set reports a refused write as an error (a
// spaKVQuotaError for a full quota). Get and Remove swallow a storage failure.
type spaKV interface {
	Get(key string) (string, bool)
	Set(key, value string) error
	Remove(key string)
}

// spaPersistLife is what one page lifetime knows about the identity it persists
// under. known is false until the boot or the first write settles it. id is the
// canonical identity (spaIdentityOf) of the model the page holds; "" is signed
// out. heldSignedIn is true once the page held a signed-in identity: from then
// on the page never writes a signed-out model, because that model still carries
// the signed-in user's data (an update that signs out usually clears only the
// session field).
type spaPersistLife struct {
	known        bool
	id           string
	heldSignedIn bool
	// quotaFenceBytes is the size of the last write the storage refused for
	// its quota, or 0. A write of this size or larger is not tried again
	// until a smaller one succeeds. warned is true once this page has warned
	// about a refused write (one warning per page).
	quotaFenceBytes uint64
	warned          bool
}

func spaLifeFor(id string) spaPersistLife {
	return spaPersistLife{known: true, id: id, heldSignedIn: id != ""}
}

// spaIdentityOf returns the identity a model blob belongs to: the values of its
// protected (session) fields, canonically serialised (object keys sorted, numbers
// kept as written). A field that is absent or JSON null does not count, so a
// model with no session value is signed out and returns "". ok is false when the
// blob is not a JSON object. With no protected fields every model is "": an app
// without a session has one identity. Never panics.
func spaIdentityOf(modelJSON string, protected []string) (id string, ok bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(modelJSON), &m); err != nil || m == nil {
		return "", false
	}
	out := map[string]any{}
	for _, f := range protected {
		for _, k := range spaFieldKeys(f) {
			raw, present := m[k]
			if !present {
				continue
			}
			if spaIsNullRaw(raw) {
				break
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			var v any
			if err := dec.Decode(&v); err != nil {
				return "", false
			}
			out[f] = v
			break
		}
	}
	if len(out) == 0 {
		return "", true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// spaRestoreStored is the boot decision: which stored model, if any, the client
// restores over the SSR seed. It returns the merged model (spaMergeStoredOverSeed
// with winFields) and the page lifetime's starting identity.
//
// The rule (identity = spaIdentityOf over the protected fields):
//   - The stored model restores ONLY when its identity equals the seed's. Any
//     change of identity (signed out -> signed in, signed in -> signed out, one
//     user -> another) restores nothing, REMOVES the stored copy and boots from
//     the seed. Two tabs with different identities on one origin share the one
//     key; each tab's full load then boots from its own seed and never shows
//     the other identity's data.
//   - No SSR seed (a static deploy, a native shell): there is no server
//     identity to compare, so the stored model restores as-is, as before.
//   - A seed that is present but not a JSON object: nothing restores.
//   - The legacy key is always deleted. Its blob restores only for an app with
//     no protected fields (one identity for everyone); an app with a session
//     cannot tell whose data it holds.
//
// Never panics; every failure boots from the seed.
func spaRestoreStored(st spaKV, seedJSON string, protected, winFields []string, maxBytes int) (merged string, useIt bool, life spaPersistLife) {
	legacy, hadLegacy := st.Get(spaPersistLegacyKey)
	if hadLegacy {
		st.Remove(spaPersistLegacyKey)
	}
	stored, ok := st.Get(spaPersistKey)
	if !ok && hadLegacy && len(protected) == 0 {
		stored, ok = legacy, true
	}
	seedID, seedOK := "", false
	if seedJSON != "" {
		if seedID, seedOK = spaIdentityOf(seedJSON, protected); seedOK {
			life = spaLifeFor(seedID)
		}
	}
	if !ok {
		return "", false, life
	}
	storedID, storedOK := spaIdentityOf(stored, protected)
	if seedJSON == "" {
		if storedOK {
			life = spaLifeFor(storedID)
		}
		merged, useIt = spaMergeStoredOverSeed(stored, "", winFields, maxBytes)
		return merged, useIt, life
	}
	if !seedOK {
		return "", false, life
	}
	if !storedOK || storedID != seedID {
		st.Remove(spaPersistKey)
		return "", false, life
	}
	merged, useIt = spaMergeStoredOverSeed(stored, seedJSON, winFields, maxBytes)
	return merged, useIt, life
}

// spaPersistWrite stores the model a step produced. When the model's identity
// differs from the one the page held, the stored copy (the old identity's) is
// removed first. A signed-out model is never written by a page that held a
// signed-in identity (see spaPersistLife). An oversized model is not written.
// Never panics.
func spaPersistWrite(st spaKV, life *spaPersistLife, modelJSON string, protected []string, maxBytes int) {
	id, ok := spaIdentityOf(modelJSON, protected)
	if !ok {
		return
	}
	spaPersistWriteKnownIdentity(st, life, modelJSON, id, uint64(len(modelJSON)), maxBytes)
}

// The generated field path can establish identity and encoded length without
// materializing an oversized whole model. Call only after FULL encoding
// validity is established. modelJSON may be empty only for an oversized value;
// identity/removal/lifetime transitions still precede the size decision.
func spaPersistWriteKnownIdentity(st spaKV, life *spaPersistLife, modelJSON, id string, encodedBytes uint64, maxBytes int) {
	if life.known && life.id != id {
		st.Remove(spaPersistKey)
	}
	life.known, life.id = true, id
	if id != "" {
		life.heldSignedIn = true
	} else if life.heldSignedIn {
		return
	}
	if maxBytes > 0 && encodedBytes > uint64(maxBytes) {
		return
	}
	if life.quotaFenceBytes > 0 && encodedBytes >= life.quotaFenceBytes {
		return
	}
	if err := st.Set(spaPersistKey, modelJSON); err != nil {
		spaPersistSetFailed(life, err, encodedBytes)
		return
	}
	life.quotaFenceBytes = 0
}

// spaKVQuotaError is a storage write refused because the origin's quota is
// full (the browser's QuotaExceededError).
type spaKVQuotaError struct{ Name string }

func (e spaKVQuotaError) Error() string { return "storage quota exceeded (" + e.Name + ")" }

// spaPersistWarn reports a refused write to the developer: console.warn in
// the wasm client, a log line natively. Tests replace it.
var spaPersistWarn = func(msg string) { spaPersistWarnDefault(msg) }

var spaPersistQuotaFailureCount atomic.Int64

// spaPersistQuotaFailures is the number of refused persistence writes in this
// process, also counted as sky_spa_persist_write_failures_total.
func spaPersistQuotaFailures() int64 { return spaPersistQuotaFailureCount.Load() }

// spaPersistSetFailed handles a refused write. Nothing is removed, so the
// stored value is the last one written. A quota failure fences writes of this
// size or larger. The page warns once, and every failure is counted. It never
// panics and never blocks the update.
func spaPersistSetFailed(life *spaPersistLife, err error, encodedBytes uint64) {
	// A type assertion and strconv, not errors.As and fmt: they cost a Wasm
	// client about 10 KB gzip for one warning (measured on 60-spa-todos).
	reason := "other"
	if _, ok := err.(spaKVQuotaError); ok {
		reason = "quota"
		life.quotaFenceBytes = encodedBytes
	}
	spaPersistQuotaFailureCount.Add(1)
	telemetry.Default().Inc("sky_spa_persist_write_failures_total", map[string]string{"reason": reason})
	if life.warned {
		return
	}
	life.warned = true
	if reason == "quota" {
		spaPersistWarn("[sky.spa] the saved model (" + strconv.FormatUint(encodedBytes, 10) + " bytes) does not fit the browser storage quota. The previous saved copy is kept, and a reload restores it.")
		return
	}
	spaPersistWarn("[sky.spa] the browser refused to save the model (" + err.Error() + "). The previous saved copy is kept.")
}

// spaPersistStripFields removes the excluded fields (App.withPersistExclude /
// Spa.withPersistExcludeFields, v0.29.0) from a model's JSON before it is
// stored: a field the server owns (site settings, a flag, the server clock) is
// never written to localStorage, so a stale copy cannot paint a later page.
// Both the field name and the snake_case key Codec.auto derives are removed. A
// protected (session) field is never removed, because the identity rule reads
// it. No exclusion, or JSON that does not parse, returns the input unchanged.
func spaPersistStripFields(modelJSON string, exclude, protected []string) string {
	if len(exclude) == 0 || modelJSON == "" {
		return modelJSON
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(modelJSON), &m); err != nil || m == nil {
		return modelJSON
	}
	removed := false
	for _, f := range exclude {
		for _, k := range spaFieldKeys(f) {
			if spaPersistProtectedWireKey(k, protected) {
				continue
			}
			if _, ok := m[k]; ok {
				delete(m, k)
				removed = true
			}
		}
	}
	if !removed {
		return modelJSON
	}
	out, err := json.Marshal(m)
	if err != nil {
		return modelJSON
	}
	return string(out)
}

// Test actual wire keys after alias expansion. Checking only the exclusion's
// source spelling can otherwise remove a protected camelCase/snake_case alias.
func spaPersistProtectedWireKey(key string, protected []string) bool {
	for _, field := range protected {
		for _, alias := range spaFieldKeys(field) {
			if key == alias {
				return true
			}
		}
	}
	return false
}

// spaPersistWinsWithExclude adds the excluded fields to the set a reload takes
// from the SSR seed: an excluded field always comes from the seed, never from
// a stored copy (an older build may have stored it).
func spaPersistWinsWithExclude(wins, exclude []string) []string {
	if len(exclude) == 0 {
		return wins
	}
	out := make([]string, 0, len(wins)+len(exclude))
	out = append(out, wins...)
	return append(out, exclude...)
}
