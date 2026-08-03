// console_data.go — the admin DATA endpoint: read (and gated write) of the app's
// live BlueDB app-stores, for `sky bluedb --url` remote mode and the console
// "Data" tab. This is a network data surface, so it is hardened per the security
// review, NOT gated like the other console read APIs:
//
//   - NO loopback-IP bypass. `consoleAccessAllowed` trusts any loopback peer,
//     which behind a reverse proxy is every request — unacceptable for a data
//     endpoint. Here auth is an `Authorization: Bearer <token>` check against
//     SKY_ADMIN_TOKEN (constant-time), loopback included (grill F1).
//   - OFF BY DEFAULT. Reads mount only in dev, or when SKY_CONSOLE_DATA is set
//     (readonly|readwrite|on). Writes require SKY_CONSOLE_DATA=readwrite in EVERY
//     env AND a configured admin token (grill F3/F6).
//   - Writes: Bearer only (never an ambient cookie), an `X-Sky-Console: 1` custom
//     header + JSON content-type (blocks a cross-site form POST — grill F2), a
//     bounded value size, and an audit log line with a before-value (grill F10).
//   - APP stores only (the BlueDB_open registry). Session stores are excluded —
//     a raw write would corrupt the gob frame + desync the live pointer (grill F7).
package rt

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
)

// dataMode reads SKY_CONSOLE_DATA: "" | "off" | "on"/"readonly" | "readwrite".
func dataMode() string {
	switch skyGetenv("CONSOLE_DATA") {
	case "readwrite", "rw":
		return "readwrite"
	case "on", "readonly", "ro", "1", "true":
		return "readonly"
	case "off", "0", "false":
		return "off"
	default:
		return ""
	}
}

// readsEnabled — the data read endpoint mounts in dev automatically, or in any
// env when SKY_CONSOLE_DATA opts in; never when explicitly "off".
func dataReadsEnabled() bool {
	m := dataMode()
	if m == "off" {
		return false
	}
	return m == "readonly" || m == "readwrite" || !productionFromEnv()
}

// writesEnabled — mutations require an EXPLICIT readwrite opt-in in every env.
func dataWritesEnabled() bool {
	return dataMode() == "readwrite"
}

// dataAuthOK enforces the Bearer-token gate. No loopback bypass. For writes the
// token is REQUIRED (a missing admin token means writes are refused). For reads
// in dev with no token configured, allow (dev-open, matching the console).
func dataAuthOK(w http.ResponseWriter, r *http.Request, write bool) bool {
	admin := skyGetenv("ADMIN_TOKEN")
	supplied := bearerToken(r)
	if admin != "" && supplied != "" &&
		subtle.ConstantTimeCompare([]byte(supplied), []byte(admin)) == 1 {
		return true
	}
	if !write && !productionFromEnv() && admin == "" {
		return true // dev-open reads only
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized: admin bearer token required", http.StatusUnauthorized)
	return false
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

// listBluedbStores / findBluedbStore enumerate the APP BlueDB stores (the
// BlueDB_open registry) — never the session store (excluded by construction).
func listBluedbStores() []string {
	bluedbRegMu.Lock()
	defer bluedbRegMu.Unlock()
	paths := make([]string, 0, len(bluedbRegistry))
	for _, e := range bluedbRegistry {
		paths = append(paths, e.path)
	}
	sort.Strings(paths)
	return paths
}

func findBluedbStore(path string) *bluedbEntry {
	bluedbRegMu.Lock()
	defer bluedbRegMu.Unlock()
	if id, ok := bluedbByPath[path]; ok {
		return bluedbRegistry[id]
	}
	return nil
}

// HandleConsoleData — GET. No `store` → list open app stores. With `store` →
// prefix-scan it: ?store=&prefix=&after=&limit=.
func HandleConsoleData(w http.ResponseWriter, r *http.Request) {
	if !dataReadsEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only (writes go to /mutate)", http.StatusMethodNotAllowed)
		return
	}
	if !dataAuthOK(w, r, false) {
		return
	}
	q := r.URL.Query()
	store := q.Get("store")
	if store == "" {
		writeJSON(w, map[string]any{"stores": listBluedbStores(), "writable": dataWritesEnabled()})
		return
	}
	entry := findBluedbStore(store)
	if entry == nil {
		http.Error(w, "unknown store", http.StatusNotFound)
		return
	}
	limit := 200
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	rows := []map[string]string{}
	entry.db.Scan([]byte(q.Get("prefix")), []byte(q.Get("after")), limit, func(k, v []byte) bool {
		rows = append(rows, map[string]string{"key": string(k), "value": string(v)})
		return true
	})
	writeJSON(w, map[string]any{"store": store, "rows": rows, "writable": dataWritesEnabled()})
}

// HandleConsoleDataMutate — POST {store, op:"put"|"delete", key, value}. Gated
// hard: readwrite opt-in + Bearer + custom header + JSON + bounded + audited.
func HandleConsoleDataMutate(w http.ResponseWriter, r *http.Request) {
	if !dataWritesEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if r.Header.Get("X-Sky-Console") != "1" { // not sendable as a cross-site simple request
		http.Error(w, "missing X-Sky-Console header", http.StatusBadRequest)
		return
	}
	if !dataAuthOK(w, r, true) {
		return
	}
	var req struct {
		Store string `json:"store"`
		Op    string `json:"op"`
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	entry := findBluedbStore(req.Store)
	if entry == nil {
		http.Error(w, "unknown store", http.StatusNotFound)
		return
	}
	before, had := entry.db.Get([]byte(req.Key))
	beforeStr := ""
	if had {
		beforeStr = string(before)
	}
	var err error
	switch req.Op {
	case "put":
		if len(req.Value) > bluedbMaxValueBytes {
			http.Error(w, "value exceeds max size", http.StatusRequestEntityTooLarge)
			return
		}
		err = entry.db.Put([]byte(req.Key), []byte(req.Value))
	case "delete":
		err = entry.db.Delete([]byte(req.Key))
	default:
		http.Error(w, "op must be put|delete", http.StatusBadRequest)
		return
	}
	// Audit BEFORE responding — identity, source, store, key, op, before-value.
	logStructured("warn", "console.data.mutate",
		"op", req.Op, "store", req.Store, "key", req.Key,
		"remote", r.RemoteAddr, "forwarded", r.Header.Get("X-Forwarded-For"),
		"had_before", strconv.FormatBool(had), "before_bytes", strconv.Itoa(len(beforeStr)),
		"ok", strconv.FormatBool(err == nil))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "op": req.Op, "key": req.Key})
}
