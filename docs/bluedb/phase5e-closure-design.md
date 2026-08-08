# Phase 5e closure design — built-in Sky Console admin access to records

> **Status:** design, not implemented. Authority chain: `.claude/AUTONOMOUS_GOAL.md`
> (goal #5) → `docs/bluedb/phase5-grill-findings.md` **B2** (overrides the v1 design)
> → this document. `docs/bluedb/phase5-dx-collapse-design.md` §3 supplies the
> *structure*; its *mechanisms* (fail-open tenant reuse, `SKY_ADMIN_TOKEN`-only gate,
> HTTP data endpoint) are superseded here.
>
> Branch `feat/bluedb` @ `0242154e`. Every file:line in this document was verified
> against that SHA. **Path correction to the audit brief:** the engine package is
> `runtime-go/bluedb/`, **not** `runtime-go/rt/bluedb/`.

---

## 0. Executive summary — the five decisions a griller will attack

| # | Decision | Why (mechanism, not optimism) |
|---|---|---|
| **D1** | The Data tab reads via **in-process kernels** (`Ffi.kernel "Data_*"` → `rt.Data_*`), **not** a new HTTP endpoint. | The console sub-app's `Http.get` runs *server-side* carrying only the per-boot internal token, which authenticates the *sub-app*, not the human → a confused deputy that structurally cannot be tenant-scoped. `runPerform` wraps every `Cmd.perform` Task in `runWithLiveSession(sess, …)` (`runtime-go/rt/live.go:5306-5324`), so a kernel invoked from a Task resolves `currentLiveSession()` → the console's own session → `SessionIdentity(sess)`. This is the **identical mechanism already in production** for `tenantPrefixForSession()` (`runtime-go/rt/hub_bridge.go:539-549`) and `currentSessionTenant()` (`runtime-go/rt/bluedb_reactive.go:50-60`). The tenant never touches the wire, so it cannot be forged. |
| **D2** | The three authorization inputs get producers by making **`ConsoleGate` stamp a `ConsoleIdentity` into `r.Context()`** under `rt.IdentityContextKey`, exactly as the hub does at `runtime-go/rt/hub/app_auth.go:129`. `live.go:4117-4119` then sets `sess.identity`/`sess.identityValid` at session mint — with zero changes to `live.go`. | `IdentityContextKey` + `IdentityFromContext` (`runtime-go/rt/session_identity.go:44,53`) is a *generic* bridge documented as "not hub-specific"; the embedded console is the second consumer. `evaluateAppMode` (`console_auth_v2.go:503`) already **has** the identity and throws it away at `:510` — we stop throwing it away. |
| **D3** | The decision object moves to a **separate Go package** `runtime-go/rt/consoledata/` with **unexported fields and exactly one constructor**. | Grill **B3**'s own conclusion, applied: "Go has no intra-package access control → a lowercase funnel stays callable everywhere → make the emit surface real encapsulation — move it into a SEPARATE Go package whose only exported entry is the funnel (so a bypass won't compile)". A `consoledata.Decision` cannot be fabricated by any code in `rt`: its fields are unexported and `Decide` is the only constructor. A scoped decision therefore **cannot compile** into an unscoped read. |
| **D4** | The tenant column is **declared** on the Sky `Std.Persist.Collection` (`tenantCol`), threaded through the existing `schemaJson` string → `parseEmbeddedSchema` → a new `bluedb.CollSchema.TenantCol`. | `Persist.sky:613-656` fixes the kernel arity at 3 args (`store, schemaJson, key/row/plan`); riding `schemaJson` needs **no FFI arity change**. `docs/bluedb/phase4-reactivity-design.md:360` forbids deriving tenant *from* a row — we don't: the column names *where to filter*, the *value* comes from `currentSessionTenant()`. |
| **D5** | Rows are **records with uniquely-prefixed field names** (`dataKey`/`dataValue`, `sqlCell…`), not `{key,value}` and not tuples. | `goty.rs:275-282` — `select_record_candidate` returns `candidates.first()` when `candidates.len() <= 1`. The collision class requires **≥2 aliases sharing a sorted field-NAME set**. A name-set no other in-scope module declares has exactly one candidate → the resolver is unambiguous *by construction*, and we add a mechanical guard test. This is strictly safer than exp's 3-tuple workaround, which forced `-- Sky has no 3-tuple fst/snd` let-destructuring gymnastics in `exp/bluedb:DataTab.sky`. |

---

## 1. Scope statement

### 1.1 The verbatim gate

> Original goal **#5: "Built-in Sky Console admin access to records."**
> Phase 5e's gate: *auto-render a read-only CRUD LIST/detail for every declared
> collection in the Sky Console*, tenant-scoped **fail-closed**, gated behind
> `SKY_CONSOLE_AUTH`.

### 1.2 What is IN scope (each clause mapped)

| Goal clause | What closes it |
|---|---|
| "auto-render … for **every declared collection**" | `adminEmbeddedCollections()` enumerates every registered `bluedb` collection across every open `*EmbeddedBackend` (`embeddedByID`, `runtime-go/rt/embedded_kernel.go:37-42`); every `Std.Persist.Collection` self-registers on first verb (`ensureRegistered`, `bluedb/embedded.go:71`). SQL-backed declared collections are enumerated by an allowlist populated from `Db_createCols` **and** `Db_autoMigrate`. No app code, no config: **auto**. |
| "read-only" | No write path ships. No `/mutate` route, no mutate kernel, no `dataWritesEnabled`. §1.4 confirms this against the grill. |
| "**CRUD LIST/detail**" | LIST = the collection's rows (key + stored codec JSON, capped). DETAIL = the selected row's stored JSON, pretty-rendered field-by-field. C/U/D are the deferred half of "CRUD" — see §1.3. |
| "in the Sky Console" | A 7th tab in `sky-bundled/console/src/`, rendered by the same `Std.Ui` `Element` pipeline as the other six, regenerated into `runtime-go/rt/console_app/main.go`. |
| "tenant-scoped **fail-closed**" | `consoledata.Decide` (the B2 matrix, unchanged semantics) + a declared-`tenantCol` row filter + **the structural refusal in §3.5**: a `Scoped` decision on a collection with no declared `tenantCol` is **hidden and unreadable**, never a full read. |
| "gated behind `SKY_CONSOLE_AUTH`" | Structural: `SKY_CONSOLE_AUTH=off` → `MountEmbeddedConsole` returns before mounting (`console.go:293-296`); production + unset → `consoleAuthModeUnsetProd` declines (`console.go:297-300`). The tab cannot exist without the gate. On top of that, the *data plane specifically* requires a verified identity in production (§2). |

### 1.3 What is explicitly NOT in scope — with goal-text justification

1. **Create / Update / Delete (the write half of "CRUD").** The gate sentence says
   *"a **read-only** CRUD LIST/detail"* — "read-only" is the user-side qualifier on
   the noun phrase. Shipping writes would exceed, not close, the gate. The grill
   (`phase5-grill-findings.md` B2 tail) independently gates the edit form on the
   `goty.rs` collision. **I confirm read-only** — see §1.4 for the challenge.
2. **Hub-mode (aggregated) data browsing.** `SKY_CONSOLE_HUB_DB` runs the console
   in the hub daemon's process; BlueDB engines and `dbRegistry` are per-host-app and
   are simply *absent* there. Enumeration returns empty by physics, so
   `HubStore.sky` gets an explicit `Task.succeed []` arm (the precedent is
   `readAnalytics`, `HubStore.sky:57-61`, for exactly the same reason).
3. **A public HTTP data endpoint.** See D1. Not shipping one is a *reduction* of
   attack surface, not a reduction of the gate — the gate says "in the Sky Console".
4. **Secondary-index seeks for the tenant filter.** `classifyIndexable`
   (`bluedb/cond.go:243-285`) only recognises a single leaf or an AND of two bounds
   **on one column**; `AND(tenantEq, userWhere)` therefore falls to the collection
   witness. Sound (over-approximate read-set), slower. The admin browse is capped at
   200 rows so this is not a latency risk. Filed, not fixed.
5. **`Cond` filter builder in the tab UI.** The v1 design (`§3.2`) listed a `Cond`
   filter. The gate says "LIST/detail". A key-prefix box ships (cheap, and it is what
   `exp/bluedb:DataTab.sky` shipped); an arbitrary `Cond` builder is UI surface with
   no gate clause behind it.

### 1.4 Challenging the read-only decision (asked for explicitly)

The brief invites a challenge. **The challenge fails, and here is why concretely.**

The stated blocker is the `goty.rs` record-fieldset collision, and §D5 shows that
blocker is *avoidable* with unique field names. So "the compiler bug blocks the edit
form" is **not**, on its own, a sufficient reason.

The reasons that *do* hold:

- **The gate text says read-only.** Per CLAUDE.md §0 rule 3, shipping beyond the
  goal is as much a drift as shipping under it, and it costs Judge cycles.
- **A write path needs a threat model this design has not built.** A generic
  row-writer over `bluedb` must: reject session-store collections (a raw write
  corrupts the gob frame — `live_store.go` `storableSession`), preserve the
  `Generated` column contract (`CollSchema.Generated`, `bluedb/backend.go:153`),
  re-run `fillGenerated` (`embedded.go:637`), and go through `PutTenant` with the
  *verified* tenant so a scoped admin cannot write a row into another tenant. Each
  is a separate fail-closed proof. That is a Phase 5e′, not a line item.
- **Read-only makes the fail-closed proof total.** With no write path, "the worst a
  compromised console session can do" is bounded by the read decision alone — which
  is the single thing B2 asks us to get right.

**Verdict: read-only confirmed.** Filed as 5e′ with the two prerequisites named
(unique-field-name form rows per D5; the four write-path invariants above).

---

## 2. The authorization design (the crux)

### 2.1 The gap, restated precisely

`consoleDataAccess(prod, verified, superAdmin, tenant)` (`console_data.go:32`) is a
correct decision function with **no producers**:

- `prod` — ✅ `productionFromEnv()` (`observability.go:314-330`).
- `verified` — ❌ `SessionIdentity(sess)` (`session_identity.go:76`) returns `ok=false`
  until `sess.identityValid` is set, and the **only** writer is `live.go:4117-4119`,
  fed by `IdentityFromContext(r.Context())`. The **only** producer of that context
  value in the repo is `runtime-go/rt/hub/app_auth.go:129` (the hub).
  `evaluateAppMode` obtains a `ConsoleIdentity` at `console_auth_v2.go:503` and
  **discards** it, minting a subject-only cookie at `:510`.
- `tenant` — ❌ same chain (`Claims["tenant"]`).
- `superAdmin` — ❌ **no producer anywhere** (verified: the only hits repo-wide are
  `console_data.go:32,42` and the test).

⇒ wired as-is: **DENY-always in production**, unscoped in dev. Dead on arrival.
Closing this is the crux and is IN scope.

### 2.2 The producer chain (new code)

```
browser request → ConsoleGate (console_auth_v2.go:962)
                     │
                     ├─ resolveConsolePrincipal(w, r, st) ──► (consolePrincipal, ok)
                     │        token mode  → cookie/login  → subject "token-auth" + ENV claims
                     │        app mode    → cookie        → claims decoded from the v3 cookie
                     │                    → callback      → claims from the Sky Identity
                     │        dev-open    → (no identity; prod-legacy admin-token → ENV claims)
                     │        off / unset-prod → not mounted
                     │
                     └─ if ok && p.Verified:
                            *r = *r.WithContext(context.WithValue(r.Context(),
                                     IdentityContextKey, p.ToConsoleIdentity()))
                                          │
Sky.Live session mint (live.go:4117-4119) ┘  ← UNCHANGED, already reads this
        sess.identity = id ; sess.identityValid = true
                     │
Cmd.perform Task  → runPerform (live.go:5306) → runWithLiveSession(sess, …)
                     │
        rt.Data_* kernel → currentLiveSession() → SessionIdentity(sess)
                         → consoledata.Decide(prod, verified, superAdmin, tenant)
```

Every arrow except the two new ones already exists and is exercised in production by
the hub console.

### 2.3 New types and functions (exact signatures)

**New file `runtime-go/rt/console_principal.go`:**

```go
// consolePrincipal is the resolved, per-request console caller. Verified is true
// only when an identity was CRYPTOGRAPHICALLY established for THIS request
// (signed cookie, or an app callback that returned `Just identity`). It is NEVER
// true for the per-boot internal token — that authenticates the console sub-app,
// not a human (see console_internal_token.go's honest-scope note).
type consolePrincipal struct {
	Verified   bool
	Subject    string
	Email      string
	Tenant     string
	SuperAdmin bool
	Claims     map[string]string
	Source     string // "cookie" | "app-callback" | "token-login" | "admin-token" | "dev-open"
}

// Claim keys — reuse the de-facto convention (hub_bridge.go:548,
// bluedb_reactive.go:59); do NOT invent a new tenant key.
const (
	consoleClaimTenant     = "tenant"
	consoleClaimSuperAdmin = "superAdmin"
)

// Operator declarations for the modes that carry no claims channel.
const (
	envConsoleDataTenant = "SKY_CONSOLE_DATA_TENANT" // token-mode / admin-token operator's tenant
	envConsoleSuperAdmin = "SKY_CONSOLE_SUPER_ADMIN" // "1"|"true"|"yes" (case-insensitive)
)

func consoleTruthy(s string) bool           // "1","true","yes","on" (case-insensitive, trimmed)
func operatorClaims() (tenant string, super bool) // reads the two envs via os.Getenv

func (p consolePrincipal) ToConsoleIdentity() ConsoleIdentity
func consoleIdentityToPrincipal(id ConsoleIdentity, source string) consolePrincipal

// resolveConsolePrincipal is evaluateConsoleAuth's identity-returning form. It
// performs EXACTLY the same checks and writes exactly the same failure responses;
// evaluateConsoleAuth becomes a thin wrapper so no existing caller changes.
func resolveConsolePrincipal(w http.ResponseWriter, r *http.Request) (consolePrincipal, bool)
```

**`ToConsoleIdentity` must round-trip the derived flags back into `Claims`** (that is
the only channel `SessionIdentity` preserves):

```go
func (p consolePrincipal) ToConsoleIdentity() ConsoleIdentity {
	cl := map[string]string{}
	for k, v := range p.Claims {
		cl[k] = v
	}
	if p.Tenant != "" {
		cl[consoleClaimTenant] = p.Tenant
	}
	if p.SuperAdmin {
		cl[consoleClaimSuperAdmin] = "true"
	}
	return ConsoleIdentity{Subject: p.Subject, Email: p.Email, Claims: cl}
}
```

### 2.4 Edits to `console_auth_v2.go`

| Site | Current | Change |
|---|---|---|
| `:392` `evaluateConsoleAuth` | returns `bool` | rename body → `resolveConsolePrincipal(w,r) (consolePrincipal,bool)`; keep `func evaluateConsoleAuth(w,r) bool { _, ok := resolveConsolePrincipal(w,r); return ok }`. **Zero behaviour change for the 6 existing handlers.** |
| `:462` `evaluateTokenMode` | `bool` | → `(consolePrincipal, bool)`. Cookie hit → `verifyConsolePrincipalCookie`. Login POST path unchanged (`handleConsoleLogin` sets the cookie and redirects; subject is the fixed `"token-auth"`, `console_auth_v2.go:707`). On a cookie hit whose payload carries no claims (legacy or token-mode), fill `Tenant`/`SuperAdmin` from `operatorClaims()`. `Verified=true`, `Source="cookie"`/`"token-login"`. |
| `:487` `evaluateAppMode` | discards identity at `:510` | Cookie hit → `verifyConsolePrincipalCookie` (claims ride the cookie). Callback hit → `consoleIdentityToPrincipal(identity, "app-callback")`; **`setConsoleV2Cookie` is replaced by `setConsolePrincipalCookie(w, st.signKey, p)`** so the claims survive the cookie fast-path. App mode does **not** consult `operatorClaims()` (see §2.6 rationale). |
| `:432` dev-open arm | `return true` | → `(consolePrincipal{Verified:false, Source:"dev-open"}, true)`. In the legacy `isProductionMode() && hasAdminAuth(r)` sub-branch, return `Verified:true, Source:"admin-token"` with `operatorClaims()`. |
| `:286` `signCookieValue` | `b64(subject).exp.sig` | add `signConsolePrincipalCookie(key []byte, p consolePrincipal, ttl time.Duration) string` — identical 3-part format, part 1 is `b64(compact JSON)`. |
| `:298` `verifyCookieValue` | `(subject, bool)` | keep verbatim (other callers). Add `verifyConsolePrincipalCookie(key []byte, value string) (consolePrincipal, bool)` which verifies via the same HMAC path, then b64-decodes part 1: **if it begins with `{` → JSON principal; otherwise → a legacy bare subject** (`Verified:true`, no claims). |
| `:962` `ConsoleGate` | `return evaluateConsoleAuth(w,r)` | resolve the principal; on `ok && p.Verified` stamp `*r = *r.WithContext(context.WithValue(r.Context(), IdentityContextKey, p.ToConsoleIdentity()))`. **Signature unchanged** (public API stable since v0.16.0); mutating `*r` in place is the documented idiom (`hub/app_auth.go:120-127`). |
| `:556` `invokeConsoleAuthCallback` | falls through on `idAny == nil` | **PORT the `exp/bluedb` fail-closed guard verbatim** (see §2.7). Security fix, ships as its own commit. |

Cookie JSON payload (kept small — cookies are capped at 4 KB):

```json
{"s":"alice@corp","e":"alice@corp","t":"acme","a":false,"c":{"role":"admin"}}
```
`c` is omitted when empty; `a` is omitted when false.

### 2.5 The decision table AFTER the change

`consoledata.Decide(prod, verified, superAdmin, tenant)` semantics are **unchanged**
(the B2 matrix at `console_data.go:32-49` is correct). What changes is that the
inputs are now real. `prod = productionFromEnv()`.

| `SKY_CONSOLE_AUTH` | `prod` | Principal source | `verified` | `tenant` | `superAdmin` | Data decision | Operator must configure |
|---|---|---|---|---|---|---|---|
| `off` | any | — | — | — | — | **console not mounted** (`console.go:293`) | — |
| unset | **true** | — | — | — | — | **console not mounted** (`consoleAuthModeUnsetProd`, `console.go:297`) | — |
| unset | false | dev-open | `false` | — | — | **ALLOW UNSCOPED** (`!prod` arm) | nothing |
| `token` | false | cookie | `true` | env | env | **ALLOW UNSCOPED** (`!prod` arm wins first) | `SKY_CONSOLE_TOKEN` (else dev auto-token) |
| `token` | **true** | cookie (subject `token-auth`) | `true` | `SKY_CONSOLE_DATA_TENANT` | `SKY_CONSOLE_SUPER_ADMIN` | tenant set → **SCOPED**; else super → **UNSCOPED**; else **DENY** | `SKY_CONSOLE_TOKEN` **and** one of the two data envs |
| `app` | false | callback / cookie | `true` | `Claims["tenant"]` | `Claims["superAdmin"]` | **ALLOW UNSCOPED** (`!prod`) | `consoleAuth` callback on `Live.app cfg` |
| `app` | **true** | callback / cookie | `true` | `Claims["tenant"]` | `Claims["superAdmin"]` | tenant → **SCOPED**; else super → **UNSCOPED**; else **DENY** | callback must return `tenant` or `superAdmin` in `Claims` |
| any | true | **internal token** (`SKY_CONSOLE_INTERNAL_TOKEN`) | **`false`** | — | — | **DENY** | — (by design; see below) |
| any | true | `SKY_ADMIN_TOKEN` bearer | `true` | env | env | per matrix | `SKY_ADMIN_TOKEN` + a data env |
| unset | true (legacy `SetProductionMode(true)` + `SKY_METRICS_TOKEN`) | admin-token | `true` | env | env | per matrix | `SKY_METRICS_TOKEN` + a data env |

**Why the internal token is `Verified:false` (and thus DENY in prod).** It
authenticates the console *sub-app*, not a human — `console_internal_token.go:14-19`
says so explicitly ("an internal-caller authenticator, not a capability boundary").
Treating it as a data principal is the confused-deputy that D1 eliminates. Under D1
the data plane never uses HTTP at all, so this row is a *defense-in-depth
statement*, and the tripwire test in §6 pins it.

**Dev-mode behaviour and why it is safe.** `!prod` ⇒ `Allowed, Scoped:false` — the
single developer on their own machine sees everything, zero configuration. This is
safe because: (a) `productionFromEnv()` returns false **only** when `ENV`/`SKY_ENV`
is unset or is one of `dev`/`development`/`local` (`observability.go:314-330`) — it
biases *to gate* whenever `ENV` is set to anything else, including `staging`; (b) the
data lives in the developer's own process, on their own disk, which they can already
read with `cat`; (c) any deployment that has bothered to set `ENV` at all lands in
the production column. There is no configuration by which a real deployment silently
gets the dev arm.

**Why `SKY_CONSOLE_DATA_TENANT` / `SKY_CONSOLE_SUPER_ADMIN` are token/admin-token
only.** In app mode the callback's claims are the sole authority. If the envs also
applied in app mode, an operator setting `SKY_CONSOLE_SUPER_ADMIN=1` would silently
promote **every** callback-approved user to platform-wide read — a privilege
escalation across the app's own users. Token mode has exactly one shared operator
(subject `token-auth`, `console_auth_v2.go:707`), so a deploy-time declaration *is*
that operator's identity. In app mode, granting the tenant is one line in the app's
own callback (`Claims = Dict.fromList [ ( "tenant", t ) ]`).

**Kill switch.** `SKY_CONSOLE_DATA=off` disables the data plane in every mode
(`consoledata.ReadsEnabled()`); unset/`readonly` = enabled subject to the matrix.
`readwrite` is parsed and **rejected with a warn** in 5e (no write path exists) so
the value is not silently accepted as meaning something it does not.

### 2.6 Revocation window (pre-empting a griller)

Claims are frozen twice: into the signed cookie (`consoleAuthCookieV2MaxAge`) and
onto the session at mint (`live.go:4117`). Revoking a user's tenant therefore takes
effect at cookie expiry / session end, not instantly. This is **not new** — it is the
existing property of the hub console and of every `Live.withIdentify` app. It is
documented in `docs/v0.16.x-console/EMBEDDED.md` as part of this change, with the
mitigation: shorten `consoleAuthCookieV2MaxAge` or set `SKY_CONSOLE_AUTH=off` for
immediate lockout. Re-invoking the callback per data read was considered and
rejected: the tab polls every 3 s, so it would multiply callback load ~20×/min per
open console with no security gain (the *session* is already minted).

### 2.7 The ported security fix (its own commit)

`invokeConsoleAuthCallback` on HEAD (`console_auth_v2.go:~556`) does:

```go
idAny := consoleUnwrapMaybeJust(maybeAny)
return extractConsoleIdentity(idAny), true
```

An `Err` result is an int-tagged `SkyResult{Tag:1}` that the string-tagged
`consoleIsResultErr` does not catch; it unwraps to a non-nil non-Maybe struct, so
today an app callback that **errors** yields an empty-but-VALID `ConsoleIdentity`.
Before this design that was inert (the identity was discarded). **After D2 it becomes
`identityValid=true, tenant=""`** — a verified principal with no tenant. Under the B2
matrix that is DENY (fail-closed), so it is not a leak — but it is one guard away
from being one, and `exp/bluedb` already fixed it. Port verbatim:

```go
idAny := consoleUnwrapMaybeJust(maybeAny)
if idAny == nil {
	// Fail-closed (SECURITY). Positive-allow, default-deny: only a
	// recognised `Just identity` grants an identity. An Err / malformed /
	// undecodable payload lands here and DENIES.
	return ConsoleIdentity{}, false
}
return extractConsoleIdentity(idAny), true
```

---

## 3. The tenant-scoped row filter

### 3.1 Where `tenantCol` is declared

On the Sky `Collection`, alongside `key` and `index` — the builder chain users
already write (`examples/59-persist-live/src/Main.sky:52-54`):

```elm
todos : P.Collection Todo
todos =
    P.collection "todos" todoCodec
        |> P.key "id"
        |> P.tenantCol "tenant"
```

**Rejected alternatives:**
- *`[data] tenantCol` in `sky.toml`* — one global column name for every collection;
  `build.rs:900`'s `_ => {}` swallows typos silently; and it is a *deployment* knob
  for a *schema* fact. Rejected.
- *Auto-detect a column literally named `tenant`* — magic that silently changes the
  security posture when someone adds an unrelated `tenant` column. Rejected; the
  whole point of B2 is that the scope must be *declared*, not inferred.

### 3.2 The thread (exact edits)

| # | File:line | Edit |
|---|---|---|
| 1 | `sky-stdlib/Std/Persist.sky:114-121` | add `, tenantCol : String` to the `Collection` record (match the file's leading-comma-at-column-1 layout) |
| 2 | `Persist.sky:125-126` | `collection` defaults `tenantCol = ""` |
| 3 | `Persist.sky:~142` (next to `index`) | `tenantCol : String -> Collection a -> Collection a` builder + `tenantColOf : Collection a -> String` accessor |
| 4 | `Persist.sky:34` | export `tenantCol` in the `exposing (...)` list (next to `index`) |
| 5 | `Persist.sky:961-968` | `schemaJson` gains `, ( "tenantCol", E.string (tenantColOf coll) )` |
| 6 | `runtime-go/rt/embedded_kernel.go:122-137` | `embeddedSchemaJSON` gains `TenantCol string \`json:"tenantCol"\`` |
| 7 | `runtime-go/rt/embedded_kernel.go:179-186` | `parseEmbeddedSchema` sets `TenantCol: d.TenantCol` **and validates it** (§3.3) |
| 8 | `runtime-go/bluedb/backend.go:147-154` | `CollSchema` gains `TenantCol string` |

No FFI arity changes (`Persist.sky:613-656` keeps its 3-arg kernels).

### 3.3 Mandatory validation at parse time

`decodeColumns` (`bluedb/indexer.go:49-67`) only emits columns present in `cs.Cols`.
A `TenantCol` naming a column that is not in `Cols` makes `CondEq` return `false` for
**every** row (`cond.go:98-104`) — silently zero rows, and worse, silently *unenforced*
if anyone later inverts the check. So `parseEmbeddedSchema` must fail loudly:

```go
if d.TenantCol != "" {
	found := false
	for _, c := range cs.Cols {
		if c.Name == d.TenantCol {
			found = true
			break
		}
	}
	if !found {
		return bluedb.CollSchema{}, fmt.Errorf(
			"collection %q declares tenantCol %q which is not a column of its codec "+
				"(columns: %v)", d.Name, d.TenantCol, colNames(cs.Cols))
	}
	cs.TenantCol = d.TenantCol
}
```

All 7 production callers of `parseEmbeddedSchema` already surface the error as
`Err(ErrInvalidInput(...))` (`embedded_kernel.go:380,403,438,465,490,509`;
`bluedb_reactive.go:188` degrades to no-watch). Fail-fast, not fail-silent.

### 3.4 The exported schema accessor (fixes the PROVEN bug + registry poisoning)

**The bug, confirmed at source.** `Query` (`bluedb/embedded.go:325-334`) scans with
**the caller's** `&coll`, not the registry copy. `scanFilter` → `decodeColumns`
iterates `cs.Cols` only. `adminReadRows` (`console_data.go:80-86`) passes
`bluedb.CollSchema{Name: collName}` — **`Cols` empty** ⇒ empty column map ⇒ any
`CondEq` is `false` for every row. Today it "works" only because
`QueryPlan{Limit: limit}` leaves `Where` as the zero `CondNode`, i.e. `CondTrue`,
which short-circuits at `cond.go:50-51`. **Adding a Where returns zero rows for every
tenant.**

**The poisoning, confirmed at source.** `Query` calls `ensureRegistered(coll)`
(`embedded.go:326`), which is set-if-absent (`embedded.go:71-78`). If the admin's
bare schema registers **first**, that collection carries `Cols: nil` **for the life of
the process** — breaking the app's own indexed reads (`buildIndexer` returns `nil`
coords, `indexer.go:23-25`) and its reactive baselines (`WatchTenant` reads the
registry copy at `embedded.go:557`). This is a live data-correctness hazard created
by an admin *read*.

**Fix — new exported accessor on `*EmbeddedBackend` (`runtime-go/bluedb/embedded.go`,
next to `CollectionNames` at `:80`):**

```go
// SchemaOf returns a COPY of the registry-owned schema for `name`, and false when
// the collection is not registered. The copy is deliberate: a caller mutating the
// returned value cannot corrupt the registry, and a caller passing it back into
// Query/Count gets the FULL Cols/Indexes set (Query scans with the CALLER's schema
// — embedded.go:331 — so a bare {Name} schema decodes zero columns and every
// non-CondTrue predicate evaluates false). Read-only; takes the RLock.
func (b *EmbeddedBackend) SchemaOf(name string) (CollSchema, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cs, ok := b.byName[name]
	if !ok {
		return CollSchema{}, false
	}
	return *cs, true
}
```

`schemaByName` (`embedded.go:94`) stays as the internal pointer form.

**`adminReadRows` must never register a schema-less collection.** Its replacement
(§3.5) obtains the schema via `SchemaOf` and returns an error when `!ok` — it never
constructs a `CollSchema` literal, so no code path can poison the registry. A
regression test pins this (§6.1 `TestAdminReadRows_NeverPoisonsRegistry`).

### 3.5 Structural refusal — the `consoledata` package

The auditor suggests `adminReadRows` take the decision and refuse. **That is
convention, not structure** — any code in `rt` can build the decision struct. Grill
B3 established the fix for exactly this shape of problem: *move the surface into a
separate Go package whose only exported entry is the funnel, so a bypass won't
compile.* Apply it.

**New package `runtime-go/rt/consoledata/`** (imports `sky-app/bluedb` + stdlib only;
`rt` imports it — no cycle, and it does **not** import `rt`, preserving the layering
rule).

`runtime-go/rt/consoledata/decision.go`:

```go
// Package consoledata owns the Sky Console's read-only admin data-access decision
// (goal #5, grill B2). Decision's fields are UNEXPORTED and Decide is its ONLY
// constructor, so no code — in rt or anywhere else — can fabricate an "allowed,
// unscoped" decision. A scoped decision therefore cannot COMPILE into an unscoped
// read: ReadRows is the sole row-read entry and it applies the tenant predicate
// itself. This is the grill-B3 remedy ("a separate package whose only exported
// entry is the funnel") applied to the admin surface.
package consoledata

// Decision is the outcome of the admin data-access gate. The zero value is DENY.
type Decision struct {
	allowed bool
	scoped  bool
	tenant  string
	reason  string
}

func (d Decision) Allowed() bool { return d.allowed }
func (d Decision) Scoped() bool  { return d.scoped }
func (d Decision) Tenant() string { return d.tenant }
func (d Decision) Reason() string { return d.reason }

// SQLBrowseAllowed reports whether the raw-SQL browse plane may be offered. A
// SCOPED decision is refused: a raw Std.Db.Store table carries no declared tenant
// column, so a scoped admin would read every tenant's rows. Persist-declared SQL
// collections WITH a declared tenantCol are handled by ScopedSQLTables instead.
func (d Decision) SQLBrowseAllowed() bool { return d.allowed && !d.scoped }

// Decide is the FAIL-CLOSED gate (grill B2) — semantics identical to the
// consoleDataAccess matrix it replaces. [full doc comment carried over verbatim
// from console_data.go:19-31]
func Decide(prod, verified, superAdmin bool, tenant string) Decision
```

`runtime-go/rt/consoledata/read.go`:

```go
import "sky-app/bluedb"

// ErrNoTenantCol is returned when a SCOPED decision reaches a collection that
// declares no tenantCol. The row read is REFUSED — never downgraded to a full
// read. Callers hide such collections from the listing.
var ErrNoTenantCol = errors.New("consoledata: collection declares no tenantCol; refusing a scoped read")

var ErrNotRegistered = errors.New("consoledata: collection is not registered on this backend")
var ErrDenied = errors.New("consoledata: access denied")

// MaxRows caps any admin read.
const MaxRows = 200

// Collections returns the names this decision may LIST for one backend. Under a
// Scoped decision, a collection with no declared tenantCol is omitted entirely —
// its existence is not even disclosed.
func Collections(be *bluedb.EmbeddedBackend, d Decision) []string

// ReadRows is the ONLY row-read entry. It resolves the REGISTERED schema (never a
// bare {Name} literal — see EmbeddedBackend.SchemaOf), and for a Scoped decision
// ANDs a tenant equality onto the plan using d.tenant, which came from the
// verified session claim and can never come from the request.
func ReadRows(be *bluedb.EmbeddedBackend, d Decision, coll string, limit int) ([][]byte, error)
```

`ReadRows` body (the exact `QueryPlan` construction):

```go
func ReadRows(be *bluedb.EmbeddedBackend, d Decision, coll string, limit int) ([][]byte, error) {
	if !d.allowed {
		return nil, ErrDenied
	}
	if limit <= 0 || limit > MaxRows {
		limit = MaxRows
	}
	cs, ok := be.SchemaOf(coll) // registry copy — full Cols/Indexes
	if !ok {
		return nil, ErrNotRegistered
	}
	plan := bluedb.QueryPlan{Limit: limit}
	if d.scoped {
		if cs.TenantCol == "" {
			return nil, ErrNoTenantCol // NEVER a full read
		}
		plan.Where = bluedb.CondNode{
			Op:   bluedb.CondEq,
			Col:  cs.TenantCol,
			Type: bluedb.ColText,          // enforced at parse time — see the note below
			Val:  bluedb.TextVal(d.tenant),
		}
	}
	return be.Query(cs, plan)
}
```

**The `ColType` question, resolved by a stricter declaration rule.** `valuesEqual`
compares normalized `Bytes` (`cond.go:107`), and `ColValue.withType`
(`indexer.go:126-128`, unexported) exists precisely to carry a possibly-descending
`ColType`. Rather than export it and thread the declared type, **`parseEmbeddedSchema`
requires a `tenantCol` whose mapped engine type is exactly `bluedb.ColText`** — add to
the §3.3 validation:

```go
if embeddedColType(typeOfCol(d.Cols, d.TenantCol)) != bluedb.ColText {
	return bluedb.CollSchema{}, fmt.Errorf(
		"collection %q declares tenantCol %q whose codec type is not text; "+
			"a tenant column must be a plain String", d.Name, d.TenantCol)
}
```

A tenant identifier that is not a string is meaningless, so this rejects nothing
legitimate, it fails loudly on a nonsensical declaration, and it lets `ReadRows` use
`Type: bluedb.ColText` + bare `TextVal` with no ambiguity and no new exported API on
`bluedb`.

**Behaviour when a collection has no declared `tenantCol` under a Scoped decision:
DENY and HIDE.** `Collections` omits it (existence not disclosed); `ReadRows`
returns `ErrNoTenantCol` if someone names it directly. Never a full read. The tab
renders an explanatory empty state ("this collection declares no `tenantCol`; a
tenant-scoped admin cannot browse it") only when the *unscoped* listing would have
shown it — i.e. never leaking cross-tenant names.

### 3.6 What replaces `runtime-go/rt/console_data.go`

`console_data.go` shrinks to the *rt-side adapters* — the parts that need `rt` types:

```go
// consoleDataDecide resolves the gate from the CURRENT live session (the console
// sub-app's own session, stamped by ConsoleGate → live.go:4117). Mirrors
// tenantPrefixForSession (hub_bridge.go:539) — the proven pattern.
func consoleDataDecide() consoledata.Decision {
	prod := productionFromEnv()
	sess := currentLiveSession()
	if sess == nil {
		return consoledata.Decide(prod, false, false, "")
	}
	id, ok := SessionIdentity(sess)
	if !ok {
		return consoledata.Decide(prod, false, false, "")
	}
	return consoledata.Decide(prod, true,
		consoleTruthy(id.Claims[consoleClaimSuperAdmin]),
		id.Claims[consoleClaimTenant])
}

// adminEmbeddedCollections — UNCHANGED (console_data.go:54-70), still the raw
// enumeration; consoledata.Collections applies the decision filter on top.
```

`consoleDataAccess` and `adminReadRows` are **deleted** from `rt` (moved into
`consoledata` as `Decide`/`ReadRows`). The existing tests move with them.

---

## 4. SQL-backend enumeration + browse

### 4.1 The port (pure port from `exp/bluedb`)

`git show exp/bluedb:runtime-go/rt/console_data_sql.go` → new
`runtime-go/rt/console_data_sql.go`, **minus** `handleSqlBrowse` (HTTP-only; D1 drops
the endpoint) and **minus** the `bearerToken` redeclaration (already on HEAD at
`console_internal_token.go:66` — a duplicate is a compile error).

Ported verbatim: `sqlSourceHandle` (sha256, `"src-"+hex[:12]`), `sqlSourceLabel`
(strips postgres userinfo; sqlite basename only), `browsableTables` +
`registerBrowsableTable` + `browsableTablesFor` + `isBrowsableTable`,
`sqlSourceInfo` + `listSqlSources`, `findSqlSource`, `sensitiveTokens` +
`sensitiveSubstrings` + `isSensitiveCol`, `openBrowseConn`, `sqlBrowseMaxLimit=200`,
`sqlBrowseMaxOffset=100000`, `sqlBrowseSem` (cap 4), `sqlBrowseResult`,
`browseSqlTable`, `cellToString`. Test file `console_data_sql_test.go` ports with the
end-to-end HTTP test replaced by a kernel-level test (§6.2).

**Three corrections to the port (do not port the defects):**

1. **`sqlBrowseSem` release must be `defer`red.** exp acquires and releases
   non-deferred around `browseSqlTable`; a panic leaks a slot permanently (4 panics =
   the SQL plane wedges forever). Use `sqlBrowseSem <- struct{}{}; defer func(){ <-sqlBrowseSem }()`.
2. **`findSqlSource` takes `dbRegistryMu` and then `browsableTablesMu`;
   `listSqlSources` takes them in the opposite order** (snapshot, release, then
   per-source). Make `findSqlSource` follow `listSqlSources`' discipline (snapshot
   under `dbRegistryMu`, release, then hash) so no lock-order inversion exists at all.
3. **Hook `Db_autoMigrate`, not just `Db_createCols`.** exp hooks only
   `Db_createCols` (exp `db_codec.go:147`), so a table reached solely through
   `Store.migrate` is invisible. On HEAD:
   - `runtime-go/rt/db_codec.go:133` `Db_createCols` — insert
     `registerBrowsableTable(d.name, AsString(tableArg))` at **line 145**, after the
     `schemaRenderTable` loop, immediately before `return Ok[any, any](struct{}{})`.
   - `runtime-go/rt/db_codec.go:201` `Db_autoMigrate` — insert the same call right
     after the `codecValidIdent(table)` guard (`d` is a `*SkyDb`, `table` is
     validated).
   Do **not** hook `Db_execObject*` / `Db_updateByPk` / `Db_upsertObject`: the
   allowlist must mean "declared schema", not "any table someone wrote to".

### 4.2 Persist-declared SQL collections get a tenantCol too

A `Std.Persist.Collection` on a SQL conn is a **declared collection** — the goal's
words. It routes through `ensureSqlTable` → `Store.create` → `Db_createCols`
(`Persist.sky:690-692`), so the allowlist hook already covers it; only the tenantCol
is missing. One contained addition:

```elm
-- Persist.sky:690-692
ensureSqlTable : Db -> Collection a -> Task Error ()
ensureSqlTable db coll =
    Store.create db (sqlStoreOf coll)
        |> Task.andThen
               (\_ -> declareSqlTenantCol db (collNameOf coll) (tenantColOf coll))


declareSqlTenantCol : Db -> String -> String -> Task Error ()
declareSqlTenantCol =
    Ffi.kernel "Persist_declareSqlTenantCol"
```

Go side (`runtime-go/rt/console_data_sql.go`):

```go
// browsableTenantCol: (dbName, table) → the declared tenant column, "" when none.
var browsableTenantCol = map[[2]string]string{} // guarded by browsableTablesMu

func registerBrowsableTenantCol(dbName, table, col string)

// Persist_declareSqlTenantCol : Db -> String -> String -> Task Error ()
func Persist_declareSqlTenantCol(connArg, tableArg, colArg any) any
```

Effect on the browse gate:

- **Unscoped decision** → every allow-listed table is browsable (exp behaviour).
- **Scoped decision** → only tables with a registered tenant column are listed, and
  `browseSqlTable` appends `WHERE <quoteIdent(tenantCol)> = ?` with the verified
  tenant bound as a **parameter** (never interpolated). A table with no registered
  tenant column is **omitted from `listSqlSources().Tables`** — same DENY/HIDE rule
  as §3.5.

`browseSqlTable` gains `(d Decision)` as its first parameter and starts with
`if !d.Allowed() { return nil, consoledata.ErrDenied }`, then branches on
`d.Scoped()`. The constructed SQL becomes:

```go
q := "SELECT " + strings.Join(selCols, ", ") + " FROM " + quoteIdent(table)
args := []any{}
if d.Scoped() {
	q += " WHERE " + quoteIdent(tcol) + " = ?"
	args = append(args, d.Tenant())
}
q += " ORDER BY " + quoteIdent(cols[0]) + " LIMIT ? OFFSET ?"
args = append(args, limit+1, offset)
rows, err := tx.QueryContext(ctx, d0.rebind(q), args...)
```

Everything else is unchanged from exp: `isSafeIdent` + `quoteIdent` on every
identifier, `'***' AS col` server-side redaction (the secret bytes never leave the
database), read-only conn (`PRAGMA query_only` / `SET default_transaction_read_only`)
**plus** `BeginTx(ReadOnly:true)` **plus** a constructed column-only SELECT — three
independent read-only guarantees, `limit+1` truncation detection, 5 s statement
timeout, 3 s setup timeout, `SetConnMaxLifetime(30s)`, `MaxOpenConns(1)`.

### 4.3 DSN opacity

`sqlSourceInfo.Name` is `"src-" + sha256(dsn)[:12]`; `Label` is credential-free
(`postgres://host/db` with userinfo stripped, or the sqlite basename). The raw DSN —
which carries the Postgres password — **never leaves the process**. `findSqlSource`
reverses the handle by re-hashing `dbRegistry` entries (`db_auth.go:227-230`), so an
unknown handle 404s. A test asserts the DSN string appears nowhere in the payload
(§6.1).

### 4.4 Resolving the design collision (asked for explicitly)

`exp/bluedb`'s gate is **env-opt-in (`SKY_CONSOLE_DATA`) + a Bearer token**;
`feat/bluedb`'s is **identity/tenant-based** (newer, grilled, B2). They compose as
**layers, not alternatives** — and the layer order matters:

| Layer | Mechanism | Answers |
|---|---|---|
| L0 (outermost) | `SKY_CONSOLE_AUTH` — the console mounts at all | "does this deployment have a console?" |
| L1 | `SKY_CONSOLE_DATA=off` | "does this deployment expose data browsing?" (operator kill switch) |
| L2 | `consoledata.Decide` — the B2 matrix over the **verified session identity** | "**who** is asking, and **what may they see**?" |
| L3 | Per-plane row/table filter — `tenantCol` predicate (KV + Persist-SQL); default-deny allowlist + redaction + caps (SQL) | "**which rows/columns**?" |

exp's `dataAuthOK` (Bearer-only) is **dropped**, not merged: it accepted the internal
token as a data principal (the confused deputy, §2.5) and it has no notion of a
tenant, so it cannot express B2 at all. Its *hardening* (allowlist, redaction, caps,
read-only conn, audit) is retained wholesale at L3, where it belongs. exp's
`SKY_CONSOLE_DATA` is retained at L1 as a kill switch only — it is **not** the
authorization gate, so an operator cannot accidentally open the data plane by setting
one env var.

### 4.5 Audit logging

Kept from exp, with the decision recorded. Three event names, `logStructured`
(`console_auth_v2.go:669`):

```
console.data.denied   warn  reason=<Decision.Reason()> subject=<p.Subject> coll=<name>
console.data.list     info  subject=… scoped=<bool> tenant=<t> kv=<n> sql=<n>
console.data.read     info  subject=… scoped=<bool> tenant=<t> coll=<name> rows=<n>
console.data.sql.read info  subject=… handle=<opaque> table=<t> rows=<n> redacted=<cols>
```

Never log the DSN, never log row contents, never log the tenant of a *denied*
request beyond the reason string.

---

## 5. The Data tab UI

### 5.1 Transport: kernels (D1), not fetches

Four kernels, exposed from a new `sky-bundled/console/src/DataStore.sky` that mirrors
`HubStore.sky`'s point-free `Ffi.kernel` idiom. `Ffi.kernel "X"` lowers directly to
`rt.X` (`rust/crates/lower/src/lower.rs:1122-1133`) — **no `kernel.rs` table entry is
required** for a raw `Ffi.kernel` alias (the `("Hub", …)` rows at `kernel.rs:621-633`
serve a module-qualified surface the console does not use). The executor confirms
this by building; `abi_guard.rs` is the backstop if a symbol is missing.

Return convention: **`[]any` of `map[string]any`**, exactly as
`Hub_readLogs`/`decodeRowsJSON` (`hub_bridge.go:257-280`, `:~500`) — the proven path
for `Task Error (List <Record>)`.

```elm
module DataStore exposing (dataCollections, dataRows, dataSqlSources, dataSqlRows)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Ffi as Ffi
import State exposing (DataCollection, DataRow, SqlSource, SqlRow)

dataCollections : () -> Task Error (List DataCollection)
dataCollections =
    Ffi.kernel "Data_collections"


dataRows : String -> String -> Task Error (List DataRow)
dataRows =
    Ffi.kernel "Data_rows"          -- (collName, keyPrefix)


dataSqlSources : () -> Task Error (List SqlSource)
dataSqlSources =
    Ffi.kernel "Data_sqlSources"


dataSqlRows : String -> String -> Task Error (List SqlRow)
dataSqlRows =
    Ffi.kernel "Data_sqlRows"       -- (opaque handle, table)
```

Go side, new `runtime-go/rt/data_kernel.go`:

```go
// Data_collections : () -> Task Error (List DataCollection)
// Every kernel begins with the SAME two lines — the gate is not optional and is
// not passed in from Sky (which the app could forge); it is resolved here from the
// goroutine's live session (ConsoleGate → live.go:4117 → SessionIdentity).
func Data_collections(_ any) any {
	return func() any {
		if !consoledata.ReadsEnabled() {
			return Ok[any, any]([]any{})
		}
		d := consoleDataDecide()
		if !d.Allowed() {
			logStructured("warn", "console.data.denied", "reason", d.Reason())
			return Ok[any, any]([]any{}) // empty, never an error the UI must special-case
		}
		out := []any{}
		for id, be := range adminEmbeddedBackends() {
			for _, name := range consoledata.Collections(be, d) {
				out = append(out, map[string]any{
					"dataConn": id, "dataName": name, "dataKind": "kv",
				})
			}
		}
		return Ok[any, any](out)
	}
}

func Data_rows(collArg, prefixArg any) any     // → []map[string]any{"dataKey","dataValue"}
func Data_sqlSources(_ any) any                // → []map[string]any{"sqlHandle","sqlLabel","sqlDriver","sqlTables"}
func Data_sqlRows(handleArg, tableArg any) any // → []map[string]any{"sqlCells","sqlIsHeader","sqlRedactedCols"}
```

`adminEmbeddedBackends() map[int64]*bluedb.EmbeddedBackend` is a small refactor of
`adminEmbeddedCollections` (`console_data.go:54-70`) that returns the backends
instead of just their names, preserving the existing lock discipline (release
`embeddedRegistryMu` before touching a backend).

**Why kernels return `Ok([])` on deny rather than `Err`.** The tab must not reveal
*why* it is empty via an error string that could differ between "no tenant claim" and
"no such collection". The reason goes to the audit log; the UI gets an empty state.
One exception: `SKY_CONSOLE_DATA=off` and "denied" both render the same neutral copy.

### 5.2 Row shapes (D5) — uniquely-prefixed record field names

`sky-bundled/console/src/State.sky`, inserted after `ErrorRow` (`:105-108`):

```elm
-- Field names are deliberately PREFIXED and unique across every module in the
-- console's compile unit. `goty.rs` keys record aliases by their sorted field-NAME
-- set (goty.rs:69) and `select_record_candidate` (goty.rs:275-282) returns the
-- single candidate unambiguously when there is exactly one. A generic
-- `{ key, value }` WOULD collide with Std.Analytics.EventProp (pulled in
-- transitively by Std.Live) and miscompile to a runtime CoerceFailure — see
-- `TestConsoleAppNoRecordFieldsetCollision`. Do NOT rename these to bare
-- `key`/`value`/`name`/`label`.
type alias DataCollection =
    { dataConn : Int
    , dataName : String
    , dataKind : String
    }


type alias DataRow =
    { dataKey : String
    , dataValue : String
    }


type alias SqlSource =
    { sqlHandle : String
    , sqlLabel : String
    , sqlDriver : String
    , sqlTables : List String
    }


type alias SqlRow =
    { sqlCells : List String
    , sqlIsHeader : Bool
    , sqlRedactedCols : List String
    }
```

`SqlRow` carries the header as a row (`sqlIsHeader = True`) so the payload is one
homogeneous list — no `(columns, rows, redacted)` 3-tuple and therefore none of exp's
`-- Sky has no 3-tuple fst/snd` destructuring.

### 5.3 The exact edit list

Verified line numbers on `feat/bluedb` @ `0242154e`. **Compiler-enforced** = omitting
it fails the build (good); **silent** = it compiles and misbehaves (dangerous).

| # | File:line | Edit | Enforced? |
|---|---|---|---|
| 1 | `State.sky:24` | `\| DataTab` after `AnalyticsTab`; fix the stale "5 tabs" comment at `:11` | — |
| 2 | `State.sky:47` | `DataTab -> "Data"` arm (blank line between arms, 8/12-space indent) | **yes** — `tabLabel` is exhaustive, no `_` |
| 3 | `State.sky:~109` | the four row types from §5.2 | — |
| 4 | `State.sky:350` | `Store` gains `, readDataCollections : () -> Task Error (List DataCollection)`, `, readDataRows : String -> String -> Task Error (List DataRow)`, `, readSqlSources : () -> Task Error (List SqlSource)`, `, readSqlRows : String -> String -> Task Error (List SqlRow)` | **yes** ×3 impls |
| 5 | `State.sky:381` | `Model` gains `, dataCollections : List DataCollection`, `, dataSelected : String`, `, dataPrefix : String`, `, dataRows : List DataRow`, `, dataDetail : String`, `, sqlSources : List SqlSource`, `, sqlSelectedHandle : String`, `, sqlSelectedTable : String`, `, sqlRows : List SqlRow` — insert **before** line 382's `}` so the detached comment block at `:383-398` stays intact | **yes** — every Model literal |
| 6 | `State.sky:428` | `Msg` gains `\| GotDataCollections (Result Error (List DataCollection))`, `\| GotDataRows (Result Error (List DataRow))`, `\| GotSqlSources (Result Error (List SqlSource))`, `\| GotSqlRows (Result Error (List SqlRow))`, `\| SelectDataCollection String`, `\| DataPrefixQuery String`, `\| SelectDataRow String`, `\| SelectSqlSource String`, `\| SelectSqlTable String` — insert after `:428` to keep the `Got*` block contiguous | — |
| 7 | `State.sky:~509` | `mockDataCollections`/`mockDataRows`/`mockSqlSources`/`mockSqlRows` (all `[]`) for `tuiStore` | — |
| 8 | **NEW** `src/DataStore.sky` | §5.1 | — |
| 9 | **NEW** `src/DataTab.sky` | `module DataTab exposing (viewDataTab)`; local palette block copied from `LogsTab.sky:31-59` (there is no shared palette module); `viewDataTab : Model -> List (Element Msg)` | — |
| 10 | `Main.sky:36` | `import DataStore exposing (dataCollections, dataRows, dataSqlSources, dataSqlRows)` | — |
| 11 | `Main.sky:208` | `httpStore` gains the four fields, each `\_ -> dataCollections ()` etc. — **the kernel, not a fetch** | **yes** |
| 12 | `Main.sky:301` | `startModel` gains the nine new fields (`[]`/`""`) | **yes** |
| 13 | `Main.sky:390` | nine new `update` arms (`(Ok x)`/`(Err e)` pairs for the four `Got*`; the five selection Msgs re-issue the dependent fetch, mirroring `SelectService`) | **yes** — `Main.update` has **no** `_ ->` catch-all (last arm `GlobalQuery` at `:514`) |
| 14 | `Main.sky:611` **and** `Main.sky:639` | `DataTab ->` arm in **BOTH** `case tab of` blocks inside `tabFetches` (`:569-639`); both are exhaustive with no `_ ->`. Hub-mode arm: `[ Cmd.perform (model.store.readDataCollections ()) GotDataCollections, Cmd.perform (model.store.readSqlSources ()) GotSqlSources ]` — identical in both, since `hubStore`'s impls are the no-ops | **yes** ×2 |
| 15 | `Main.sky:758` | add `DataTab -> 5000` **before** the `_ -> 3000` fallback at `:758-759` | **NO — silent** (inherits 3 s) |
| 16 | `HubStore.sky:27` | add `DataCollection`, `DataRow`, `SqlSource`, `SqlRow` to the **explicit** `State exposing (...)` list | **yes** |
| 17 | `HubStore.sky:61` | four `Task.succeed []` no-op arms — copy the `readAnalytics` precedent's comment shape (`:57-61`): BlueDB + `dbRegistry` are per-host-app, not hub-aggregated | **yes** |
| 18 | `View.sky:19` | `import DataTab exposing (viewDataTab)` | — |
| 19 | `View.sky:196` | add `DataTab` to the hardcoded list; the line is already 88 chars — **reflow to multi-line** | **NO — silent**: the tab routes but no button renders |
| 20 | `View.sky:606` | `DataTab -> viewDataTab model` arm; **move the fused `)` off line 606** onto the new last arm | **yes** |
| 21 | `MainTui.sky:42` | `tuiStore` gains the four fields (`Task.succeed []`) | **yes** |
| 22 | `MainTui.sky:67` | init Model literal gains the nine fields **and the missing `logoutUrl`** — see §5.5 | **yes** |

`tabButton` (`View.sky:463-492`) and the `SelectTab` update arm (`Main.sky:338-346`)
are fully generic over `Tab` — **no edit**. `urlSync`/`encodeFilters`
(`View.sky:374-407`) does not encode the active tab, so the Data tab is not
deep-linkable; that matches the other six.

**`View.sky:495-510` prohibition honored:** the `DataTab` arm passes the plain
`model` and nothing else. No `{ model | … }` synthetic Model, no per-branch `Coerce`.
That regression dropped Sky.Live sessions in production (v0.16.20 hotfix).

### 5.4 The `authGet` migration — a separate, independently-shippable commit

Not needed by the Data tab (D1), but **required for the console to work at all in
production**, and discovered by this work:

`Main.sky:644,651,696,703,710,717` issue bare `Http.get` with **no** `Authorization`
header. `consoleAccessAllowed` (`console.go:395`) accepts a Bearer (internal or
admin) or falls through to `evaluateConsoleAuth`, which in `token`/`app` mode
requires the `__Host-sky_console` cookie. The console sub-app's `Http.get` runs
server-side with neither ⇒ **all six telemetry tabs 401 in production today**. Nobody
noticed because dev-open returns `true` unconditionally (`console_auth_v2.go:432-452`).

Per AGENTS.md's no-deferral rule this enters the pipeline now. Port from
`exp/bluedb:sky-bundled/console/src/Main.sky` verbatim:

```elm
consoleAuthToken : String
consoleAuthToken =
    System.getenvOr "SKY_CONSOLE_INTERNAL_TOKEN" ""


{-| Every console→parent fetch goes through this so the per-boot internal token
(F1) is carried on ALL of them — the parent gate authenticates by token, not by a
proxy-spoofable loopback IP. A single missed site would 401 a tab in prod. -}
authGet : String -> Task Error Http.HttpResponse
authGet url =
    Http.request
        (Http.defaultRequest url
            |> Http.withHeader "Authorization" ("Bearer " ++ consoleAuthToken))
```

Then `Http.get` → `authGet` at all six sites. The memoised-CAF ordering is **safe and
precedented**: `ConsoleInternalTokenInit()` runs at `console.go:320` with the comment
"mint + publish the per-boot internal token BEFORE the sub-app inits", and
`SKY_CONSOLE_LOGOUT_URL` (`console.go:315`) is read the same way at `Main.sky:222`.

### 5.5 `MainTui.sky` is already broken — fix it in the same commit

`MainTui.sky`'s init Model literal (`:48-68`) is **missing `logoutUrl`**, which
`State.Model` has required since `State.sky:381`. It went unnoticed because
`sky-bundled/console/sky.toml` sets `entry = "src/Main.sky"` and
`regenerate-console.sh:132-138` moves `MainTui.sky` aside for the Live build. But
`crates/sky/src/bundled.rs::materialise` builds the Tui entry for `sky console`, so
this is a real broken build path. **Do not assume it compiles**; fix `logoutUrl`
while adding the nine Data fields, and verify with an explicit Tui build.

Note the correction to the brief: `MainTui.sky` has **zero** `case tab of` sites — its
`update` (`:73-95`) ends in a `_ ->` catch-all at `:94`, so the new `Got*` Msgs are
absorbed silently. Only `tuiStore` and the Model literal force edits.

### 5.6 Regeneration + rebuild

```bash
cd /Users/anzel/works/playground/sky
./scripts/regenerate-console.sh            # sky fmt-clean sources first
git diff --stat runtime-go/rt/console_app/ # expect main.go to change
cd rust && cargo build --release --locked -p sky
command cp -f "${CARGO_TARGET_DIR:-rust/target}/release/sky" ../sky-out/sky
```

Environment gotchas (from `docs/bluedb/RESUME.md`, they cost hours): `CARGO_TARGET_DIR`
may be `/Users/anzel/.cargo/bin` so the binary lands at
`/Users/anzel/.cargo/bin/release/sky`; `cp` is aliased interactive → always
`command cp -f`; zsh `noclobber` → use `>|`. The script needs `SKY_RUNTIME_DIR`
pointing at the worktree `runtime-go` (it sets this itself at `:146-160`) or edits to
`runtime-go/rt/*.go` silently fall through to the embedded snapshot.

**Correction to the brief:** there is **no CI gate** on `console_app` drift.
`regenerate-console.sh:386` only *prints* `drift check: 'git diff --exit-code
runtime-go/rt/console_app/' should be clean` — grep of `.github/workflows/` and
`rust/crates/xtask/` finds nothing. So the regeneration is a **manual step the
executor must not skip**, and §8-R1 adds the gate.

---

## 6. Test plan — three legs

Every test below is stated with *the regression it catches*. A test that would pass
against today's code without the fix is vacuous and does not count.

### 6.1 Leg 1 — unit, `go test -race`

**`runtime-go/rt/consoledata/decision_test.go`** (the moved + extended B2 matrix)

| Test | Asserts | Fails today because |
|---|---|---|
| `TestDecide_FailClosedMatrix` | the 6-row matrix from `console_data_test.go:16-31`, now over the unexported-field `Decision` | (moved; guards the port) |
| `TestDecide_NoTenantNeverAllTenants` | verified + no tenant + no super ⇒ `!Allowed()` | (moved; the non-vacuous B2 pin) |
| `TestDecision_ZeroValueIsDeny` | `var d Decision; !d.Allowed() && !d.SQLBrowseAllowed()` | **new** — pins that a forgotten `Decide` call denies rather than allows |
| `TestDecision_ScopedRefusesSQLBrowse` | `Decide(true,true,false,"acme").SQLBrowseAllowed() == false` | **new** — pins §4.2's cross-tenant refusal for raw Store tables |

**`runtime-go/rt/consoledata/read_test.go`**

| Test | Asserts | Fails today because |
|---|---|---|
| `TestReadRows_TenantFilterActuallyFilters` | seed `notes` with a FULL `CollSchema` (`Cols` = id,text,tenant) and rows for `acme`+`globex`; `ReadRows(be, Decide(true,true,false,"acme"), "notes", 100)` returns **exactly the acme rows** | **THE proven bug.** Today `adminReadRows` passes `CollSchema{Name}` → `Cols` nil → `decodeColumns` yields `{}` → `CondEq` false (`cond.go:98-104`) → **0 rows**. Empirically measured: FULL schema + `CondEq(tenant=acme)` → 1 row; BARE schema + same cond → 0. |
| `TestReadRows_ScopedNoTenantColRefuses` | a collection registered with `TenantCol: ""` under a Scoped decision ⇒ `ErrNoTenantCol`, and `len(rows)==0` | **new** — the structural DENY-never-downgrade rule |
| `TestReadRows_UnregisteredCollectionRefuses` | `ReadRows(be, d, "never-registered", 10)` ⇒ `ErrNotRegistered` **and** `be.CollectionNames()` is unchanged afterwards | **new** — the registry-poisoning regression: today `adminReadRows` would `Query` a bare schema and `ensureRegistered` would install `Cols: nil` permanently |
| `TestReadRows_NeverPoisonsRegistry` | register `notes` with 3 columns; call `ReadRows` 5× with every decision shape; assert `be.SchemaOf("notes")` still reports 3 columns and 1 index | **new** — pins the `SchemaOf`-not-literal contract |
| `TestReadRows_DeniedDecisionReturnsErrDenied` | `ReadRows(be, Decision{}, "notes", 10)` ⇒ `ErrDenied` | **new** |
| `TestReadRows_CapsAtMaxRows` | `limit = 10_000` ⇒ ≤ `MaxRows` | **new** |
| `TestCollections_ScopedHidesTenantColless` | two collections, one with `TenantCol`; Scoped ⇒ only the tenanted name is listed; Unscoped ⇒ both | **new** — existence non-disclosure |

**`runtime-go/rt/console_principal_test.go`** (the new producers)

| Test | Asserts | Fails today because |
|---|---|---|
| `TestResolveConsolePrincipal_AppModeCarriesClaims` | with a stub `consoleAuth` callback returning `Just {subject, claims:{tenant:"acme"}}`, the principal has `Verified && Tenant=="acme"` | **new** — `evaluateAppMode:510` discards the identity today |
| `TestConsoleGate_StampsIdentityOnRequest` | after `ConsoleGate(w,r)`, `IdentityFromContext(r.Context())` returns `ok` with `Claims["tenant"]=="acme"` | **new** — nothing writes `IdentityContextKey` for the console today |
| `TestConsolePrincipalCookie_RoundTripsClaims` | `verifyConsolePrincipalCookie(sign(p)) == p` for tenant + superAdmin + email | **new** |
| `TestConsolePrincipalCookie_LegacyBareSubject` | a cookie minted by the old `signCookieValue` verifies to `{Verified:true, Subject:"alice", Tenant:""}` | **new** — back-compat; and it lands on DENY in prod (fail-closed on upgrade) |
| `TestConsolePrincipalCookie_TamperedClaimsRejected` | flip one byte of the tenant inside the payload ⇒ `ok=false` | **new** — pins that claims are inside the HMAC |
| `TestTokenMode_UsesOperatorEnvClaims` | `SKY_CONSOLE_DATA_TENANT=acme` ⇒ token-mode principal has `Tenant=="acme"`; unset ⇒ `""` ⇒ prod DENY | **new** |
| `TestAppMode_IgnoresOperatorEnvClaims` | `SKY_CONSOLE_SUPER_ADMIN=1` + an app callback with no claims ⇒ decision is **DENY**, not unscoped | **new** — pins §2.5's privilege-escalation guard |
| `TestInvokeConsoleAuthCallback_ErrDenies` | a callback returning `Err …` ⇒ `(ConsoleIdentity{}, false)` | **FAILS TODAY** — the `idAny == nil` guard is absent on HEAD |
| `TestConsoleDataDecide_NoSessionIsDeny` | `clearGoroutineLiveSession()`; in prod ⇒ `!Allowed()` | **new** — mirrors `hub_currentidentity_test.go:13` |

**`runtime-go/rt/data_kernel_test.go`**

| Test | Asserts | Fails today because |
|---|---|---|
| `TestDataKernels_NeverAcceptTenantFromArgs` | *tripwire*: parse `data_kernel.go` and assert no kernel parameter is named/used as a tenant, and that every `Data_*` body's first statements are `ReadsEnabled()` + `consoleDataDecide()` | **new** — the B3-style structural tripwire; a new kernel that forgets the gate trips it |
| `TestDataKernels_InternalTokenIsNotAPrincipal` | a request bearing `SKY_CONSOLE_INTERNAL_TOKEN` yields `Verified=false` | **new** — pins the confused-deputy refusal |
| `TestDataKernels_ScopedReturnsOnlyOwnTenant` | stamp a session with `tenant=acme`; `Data_rows("notes","")` returns only acme rows | **new** |
| `TestDataKernels_DisabledReturnsEmpty` | `SKY_CONSOLE_DATA=off` ⇒ `Ok([])` in every mode | **new** |

**`runtime-go/rt/console_data_sql_test.go`** (ported + extended)

| Test | Asserts | Fails today because |
|---|---|---|
| `TestSqlRedactionTokenAware` | `isSensitiveCol` true for `password,passwd,pwd,user_pw,passphrase,pin,signing_key,api_key,session_token,secret,credential,ssn,cvv`; false for `email,name,monkey_id,keyboard,id,created_at,description,keynote_url` | **new** (port) — no redaction exists on HEAD |
| `TestSqlBrowseDefaultDeny` | a table that exists but was never `registerBrowsableTable`d ⇒ browse refused | **new** (port) |
| `TestSqlSourceHandleOpacity` | `listSqlSources()` output contains neither the sqlite path nor the postgres password anywhere (marshal to JSON and `strings.Contains`) | **new** (port) |
| `TestSqlBrowseRedactsServerSide` | the emitted SQL contains `'***' AS "password"` and the returned cell is `***` | **new** (port) |
| `TestAutoMigrateRegistersBrowsable` | a table created only via `Db_autoMigrate` **is** listed | **FAILS on the exp port** — exp hooks only `Db_createCols` |
| `TestSqlBrowse_ScopedRequiresTenantCol` | a Scoped decision over a table with no registered tenant column ⇒ omitted from `Tables` and refused on direct browse | **new** |
| `TestSqlBrowse_ScopedBindsTenantAsParam` | with a registered tenant column, the SQL is `… WHERE "tenant" = ?` and the arg is the **verified** tenant — assert a `tenant` value supplied in the *request* is never used | **new** |
| `TestSqlBrowseSemaphoreReleasedOnPanic` | inject a panic in `browseSqlTable`; run 5 browses; the 5th still proceeds | **FAILS on the exp port** — exp's release is not deferred |

**`runtime-go/rt/console_app/fieldset_guard_test.go`** (new, package `console_app`)

| Test | Asserts | Fails today because |
|---|---|---|
| `TestConsoleAppNoRecordFieldsetCollision` | parse `main.go` for `type \w+_R struct`, group by sorted field-name set, assert every set containing the Data-tab field names (`dataKey`,`dataValue`,`sqlCells`,…) maps to **exactly one** `_R` type | **new** — mechanically enforces D5. Rename `dataKey`→`key` and it fails, catching the CoerceFailure class *at test time* instead of in a browser. (Test files are not touched by `regenerate-console.sh`, which writes only `main.go`.) |

**`runtime-go/bluedb/embedded_schema_test.go`**

| Test | Asserts | Fails today because |
|---|---|---|
| `TestSchemaOf_ReturnsFullRegisteredSchema` | `Register` a 3-col schema, `SchemaOf` returns 3 cols; mutating the returned copy does not change a second `SchemaOf` | **new** — `SchemaOf` does not exist |
| `TestQueryWithBareSchemaFindsNothing` | *documents the trap*: `Query(CollSchema{Name:"notes"}, plan-with-CondEq)` returns 0 rows while `Query(registered, same plan)` returns 1 | **new** — the executable proof of the bug this design fixes; a future refactor that makes `Query` silently consult the registry must update this test deliberately |

**Delete `TestAdminReadRows_ReadsSeededRows`** (`console_data_test.go:~93`). It is
**vacuous**: it exercises only the zero `CondNode` (= `CondTrue`), which
short-circuits at `cond.go:50-51` before any column lookup, so it passes with an
empty `Cols` set and proves nothing about scoped reads. Its replacement is
`TestReadRows_TenantFilterActuallyFilters` (which fails today) plus
`TestReadRows_NeverPoisonsRegistry`.

Command: `cd runtime-go && timeout 1800 go test -race ./rt/... ./bluedb/...`

### 6.2 Leg 2 — integration

**`runtime-go/rt/console_data_integration_test.go`** — a real `httptest` server with
`MountEmbeddedConsole`-shaped wiring, a real `bluedb.Open(t.TempDir())` backend, and a
real sqlite `SkyDb` in `dbRegistry`:

| Test | Asserts | Regression caught |
|---|---|---|
| `TestIntegration_DevOpenSeesEverything` | `ENV` unset; two tenants seeded; the kernels return **all** rows | dev arm must stay zero-config |
| `TestIntegration_ProdUnsetProdDeclines` | `ENV=production`, `SKY_CONSOLE_AUTH` unset ⇒ `MountEmbeddedConsole` logs `console.disabled reason=auth-unset` and **no** console route exists | the goal's "gated behind `SKY_CONSOLE_AUTH`" clause |
| `TestIntegration_TokenModeNoDataEnvDenies` | `ENV=production`, `SKY_CONSOLE_AUTH=token`, valid cookie, **no** `SKY_CONSOLE_DATA_TENANT`/`SKY_CONSOLE_SUPER_ADMIN` ⇒ kernels return empty + `console.data.denied` logged | **the B2 fix, end to end** — a verified-but-unclaimed session must see nothing, not everything |
| `TestIntegration_TokenModeScopedSeesOwnTenantOnly` | + `SKY_CONSOLE_DATA_TENANT=acme` ⇒ exactly the acme rows from the KV plane; the SQL plane lists only tables with a registered tenant column | the cross-tenant read |
| `TestIntegration_AppModeClaimsFlowGateToKernel` | a `consoleAuth` callback returning `tenant=globex`; drive `ConsoleGate` → session mint → a kernel call on that session's goroutine ⇒ globex rows only | **the whole producer chain** — every link is new |
| `TestIntegration_CookieFastPathPreservesTenant` | second request hits the cookie branch (callback not re-invoked) and still scopes to globex | the claims-in-cookie requirement (without it the fast path silently degrades to DENY) |
| `TestIntegration_SixTelemetryEndpointsAuthorizeWithInternalToken` | `ENV=production`, `SKY_CONSOLE_AUTH=token`; `GET /_sky/console/api/traces` with the internal Bearer ⇒ 200; without ⇒ 401 | pins §5.4's fix and proves the 401 class was real |

### 6.3 Leg 3 — real use (browser)

**App:** `examples/59-persist-live` — a Sky.Live app with a real
`Std.Persist` embedded collection (`src/Main.sky:47-54`). Changes for the check
(committed, since they also exercise the new API): add a `tenant : String` field to
`Todo`, `|> P.tenantCol "tenant"` to the collection, and seed two tenants.

```bash
# ── prep
cd /Users/anzel/works/playground/sky
pgrep -f mem-guard.sh >/dev/null || (nohup ./scripts/mem-guard.sh >/tmp/mem-guard.out 2>&1 & disown)
./scripts/regenerate-console.sh
( cd rust && timeout 3600 cargo build --release --locked -p sky )
command cp -f "${CARGO_TARGET_DIR:-$PWD/rust/target}/release/sky" ./sky-out/sky

# ── A. dev arm — unscoped, zero config
cd examples/59-persist-live
rm -rf sky-out .skycache .skydeps data
timeout 600 ../../sky-out/sky build src/Main.sky
( ./sky-out/app & echo $! > /tmp/sky59.pid )
#   browser → http://localhost:8000/_sky/console  → click "Data"
#   EXPECT: `todos` listed; rows for BOTH tenants; a row click shows the detail JSON.

# ── B. production + token mode, NO data env — fail-closed (the B2 gate)
kill "$(cat /tmp/sky59.pid)"
ENV=production SKY_CONSOLE_AUTH=token SKY_CONSOLE_TOKEN=devtok123 ./sky-out/app &
#   browser → /_sky/console → login form → paste devtok123 → click "Data"
#   EXPECT: empty state, NO rows, NO collection names. Server log: console.data.denied
#           reason="no tenant claim and no super-admin marker …". The other six tabs
#           MUST render (that is §5.4's authGet fix; before it they were blank/401).

# ── C. production + token mode + tenant — scoped
kill %1
ENV=production SKY_CONSOLE_AUTH=token SKY_CONSOLE_TOKEN=devtok123 \
  SKY_CONSOLE_DATA_TENANT=acme ./sky-out/app &
#   EXPECT: `todos` listed; ONLY acme rows; globex rows absent. Log: console.data.read
#           scoped=true tenant=acme rows=<n>.

# ── D. kill switch
kill %1
ENV=production SKY_CONSOLE_AUTH=token SKY_CONSOLE_TOKEN=devtok123 \
  SKY_CONSOLE_DATA_TENANT=acme SKY_CONSOLE_DATA=off ./sky-out/app &
#   EXPECT: Data tab present but empty; no console.data.read events.

# ── E. no CoerceFailure anywhere
#   Across A–D: server log has ZERO `CoerceFailure` / `rt.Coerce` panics and the
#   Sky.Live session survives every tab switch (Overview→Data→Logs→Data→Traces).
#   This is the D5 + View.sky:495-510 verification that only a browser can give.

# ── F. SQL plane
cd ../57-persist-parity   # (or any Std.Db.Store example) — dev arm
#   EXPECT: the Data tab's SQL section lists an opaque `src-…` handle, never the
#           DB path; a `password`-like column renders `***`.

# ── teardown (CLAUDE.md §2)
pkill -f "examples/.*/sky-out/app"
ps -u "$USER" -o pid,command | awk '/while pgrep|until ! pgrep/ && /\/bin\/zsh -c/ {print $1}' | xargs -n1 kill -9 2>/dev/null
```

Milestone sweep (once, at the end): `timeout 3600 ./scripts/example-sweep.sh` +
`cd rust && timeout 3600 cargo test --workspace` +
`cd runtime-go && timeout 1800 go test -race ./...`.

---

## 7. Phase / commit ordering

Each commit is additive, independently verifiable, and revertable. **P** = pure port,
**N** = net-new, **F** = fix of an existing defect.

| # | Commit | Kind | Touches | Verified by | Depends on |
|---|---|---|---|---|---|
| **C1** | `fix(console): fail-closed identity when the consoleAuth callback errors` | **P** (exp `1a19aeca`) | `console_auth_v2.go:~556` | `TestInvokeConsoleAuthCallback_ErrDenies` (fails today) | — |
| **C2** | `fix(console): carry the internal Bearer on every console→parent fetch` | **P/F** | `Main.sky` ×6 + `consoleAuthToken`/`authGet`; regen + rebuild | `TestIntegration_SixTelemetryEndpointsAuthorizeWithInternalToken`; browser check B (six tabs render in prod) | — |
| **C3** | `fix(bluedb): EmbeddedBackend.SchemaOf — never query with a bare schema` | **N/F** | `bluedb/embedded.go` only (no new exported value API — see §3.5) | `TestSchemaOf_*`, `TestQueryWithBareSchemaFindsNothing` | — |
| **C4** | `feat(bluedb): declared tenantCol threaded Sky→CollSchema` | **N** | `Persist.sky` ×5, `embedded_kernel.go` ×2, `bluedb/backend.go` | `parseEmbeddedSchema` validation tests; `sky check examples/59-persist-live` | C3 |
| **C5** | `feat(console): consoledata package — unforgeable fail-closed decision + scoped read` | **N** | new `rt/consoledata/`, delete `consoleDataAccess`+`adminReadRows` from `console_data.go`, move tests | all of §6.1 `consoledata/*`; the vacuous test is deleted here | C3, C4 |
| **C6** | `feat(console): per-request principal + ConsoleGate identity stamp` | **N** | new `console_principal.go`; `console_auth_v2.go` ×7 sites | `console_principal_test.go` (all); `TestIntegration_AppModeClaimsFlowGateToKernel` | C1, C5 |
| **C7** | `feat(console): Data_* kernels (KV plane) + audit events` | **N** | new `rt/data_kernel.go`; `console_data.go` adapters | `data_kernel_test.go`; `TestIntegration_TokenMode*` | C5, C6 |
| **C8** | `feat(console): SQL enumeration + hardened browse (port from exp/bluedb)` | **P** + 3 fixes | new `console_data_sql.go` + test; `db_codec.go:145,~210` hooks; `Persist.sky` `declareSqlTenantCol` | `console_data_sql_test.go` (all, incl. the 3 exp-defect tests) | C4, C7 |
| **C9** | `feat(console): Data tab UI (read-only) + Tui/Hub arms + MainTui logoutUrl fix` | **N**+**F** | the 22 rows of §5.3; new `DataStore.sky`, `DataTab.sky`; regen + rebuild | `TestConsoleAppNoRecordFieldsetCollision`; browser checks A–F | C7, C8 |
| **C10** | `chore(ci): gate console_app regeneration drift` | **N** | `.github/workflows/rust-ci.yml` (or an xtask gate) | the gate fails on a deliberately stale `main.go` | C9 |
| **C11** | `docs(bluedb): 5e closure — RESUME + EMBEDDED.md + sky-toml env reference` | — | `docs/bluedb/RESUME.md`, `docs/v0.16.x-console/EMBEDDED.md`, `docs/sky-toml.md`, `AGENTS.md` if the `[data]` surface changed | `scripts/doc-examples.sh` | C10 |

**Push cadence** (CLAUDE.md §0.1): local commits throughout; push **once** after C9 +
the milestone sweep, and once after C11. Not per commit.

C1–C3 are independently useful and land first because they are the three
defect-fixes; the feature stack (C4–C9) sits on top. C2 and C9 each require the
`regenerate-console.sh` + `cargo build` + binary-copy dance (§5.6).

---

## 8. Risk register

Every trap from the brief's §H, plus what this design found. **Mitigation** = what
prevents it; **Gate** = what catches it if the mitigation fails.

| # | Risk | Mitigation | Gate if the mitigation fails |
|---|---|---|---|
| **R1** | **Console regeneration drift.** `console_app/main.go` (291 KB) is committed and generated. The brief says CI enforces `git diff --exit-code`; **it does not** — grep of `.github/workflows/` + `xtask` finds nothing; `regenerate-console.sh:386` only prints a hint. A forgotten regen ships an old UI silently. | C9's checklist runs the script; §5.6 is explicit. | **C10 adds the missing CI gate** (`./scripts/regenerate-console.sh --check` or `git diff --exit-code runtime-go/rt/console_app/` after a regen step). Until C10 lands, the browser leg (§6.3-A) catches it because the tab simply does not appear. |
| **R2** | **`goty.rs` record-fieldset collision** → runtime `CoerceFailure`, dropped Sky.Live session. | D5: uniquely-prefixed field names ⇒ `candidates.len() <= 1` ⇒ `select_record_candidate` (`goty.rs:275-282`) is unambiguous by construction. Explicit "do not rename" comment in `State.sky`. | `TestConsoleAppNoRecordFieldsetCollision` (mechanical, runs in `go test`); browser check E (zero `CoerceFailure` across tab switches). |
| **R3** | **Synthetic-Model panic** (`View.sky:495-510`) — `{ model \| … }` + per-branch `Coerce` dropped production sessions in v0.16.20. | The `DataTab` arm passes the plain `model` and nothing else (§5.3 #20). | Browser check E (session survives Overview→Data→Logs→Data). Code review against `View.sky:495-510`. |
| **R4** | **`MainTui.sky` breaks** on a new `Tab`/`Store` field — **and is already broken** (missing `logoutUrl`, `MainTui.sky:48-68`). | C9 fixes `logoutUrl` and adds the four `tuiStore` fields + nine Model fields. | An explicit Tui build (`entry = src/MainTui.sky`, or `sky console --tui`) in C9's verification. `MainTui` has no `case tab of`, so only these two literals matter. |
| **R5** | **`View.sky:196` silently omitted** ⇒ the tab routes but no button renders; **`Main.sky:758` silently omitted** ⇒ 3 s poll instead of 5 s. Neither is compiler-enforced. | §5.3 flags both as **NO — silent** with the exact line. | Browser check A (the button is the first thing verified). The poll rate is cosmetic. |
| **R6** | **`tabFetches` has TWO `case tab of` blocks** (`Main.sky:577`, `:614`) — patching one leaves the hub arm non-exhaustive. | §5.3 #14 names both lines. | Compiler: both blocks are exhaustive with no `_ ->`, so missing one is a **build error**. |
| **R7** | **`View.sky:606`'s fused `)`** — the closing paren of the `case` sits on the `AnalyticsTab` arm. | §5.3 #20 calls it out explicitly. | Build error. |
| **R8** | **Bare-schema query returns zero rows / poisons the registry.** | `consoledata.ReadRows` obtains the schema via `be.SchemaOf` and errors when absent; it never constructs a `CollSchema` literal. `parseEmbeddedSchema` validates `TenantCol ∈ Cols` and `ColText`. | `TestReadRows_TenantFilterActuallyFilters` (fails today), `TestReadRows_NeverPoisonsRegistry`, `TestQueryWithBareSchemaFindsNothing`. |
| **R9** | **A Scoped decision performs an unscoped read.** | D3: `consoledata.Decision` has unexported fields and one constructor, in a **separate package** — `rt` code cannot fabricate one. `ReadRows` is the only row-read entry and applies the predicate itself. | `TestDecision_ZeroValueIsDeny`, `TestReadRows_ScopedNoTenantColRefuses`, `TestDataKernels_NeverAcceptTenantFromArgs` (B3-style tripwire). |
| **R10** | **Confused deputy:** the console sub-app's internal token used as a data principal. | D1 removes the HTTP data plane entirely; `Verified=false` for the internal token. | `TestDataKernels_InternalTokenIsNotAPrincipal`. |
| **R11** | **Tenant forged from the request** (query param / header). | The tenant is read from `SessionIdentity(currentLiveSession())` inside the kernel; there is no request-supplied tenant anywhere in the design. | `TestSqlBrowse_ScopedBindsTenantAsParam` asserts a request-supplied `tenant` is ignored; the tripwire test asserts no kernel parameter feeds the tenant. |
| **R12** | **Claims lost on the cookie fast path** ⇒ app mode silently degrades to DENY after the first request. | `setConsolePrincipalCookie` writes claims into the HMAC-signed payload. | `TestConsolePrincipalCookie_RoundTripsClaims`, `TestIntegration_CookieFastPathPreservesTenant`. |
| **R13** | **Cookie claim tampering** — a user edits their tenant. | Claims are inside the existing HMAC payload (`signCookieValue`'s part-1/part-2 are both MAC'd, `console_auth_v2.go:286-296`). | `TestConsolePrincipalCookie_TamperedClaimsRejected`. |
| **R14** | **Privilege escalation via operator env in app mode** — `SKY_CONSOLE_SUPER_ADMIN=1` promoting every callback-approved user. | §2.5: the operator envs are consulted **only** for token-mode / admin-token principals. | `TestAppMode_IgnoresOperatorEnvClaims`. |
| **R15** | **Stale claims / revocation window** — claims frozen in the cookie and on the session. | Documented in `EMBEDDED.md`; bounded by `consoleAuthCookieV2MaxAge` and session lifetime; identical to the existing hub console property. | Not a test — a documented, bounded property. Immediate lockout = `SKY_CONSOLE_AUTH=off`. |
| **R16** | **`ensureRegistered` first-write-wins staleness** — a collection registered before `tenantCol` was declared keeps `TenantCol: ""` for the process, silently making it invisible to a scoped admin. | Every Persist verb passes the full `schemaJson`-derived schema, and `Register` (not `ensureRegistered`) is what the app's own first write uses via `Query`→`ensureRegistered` on a **full** schema; the only bare-schema caller is being deleted. | `TestReadRows_NeverPoisonsRegistry`; the browser leg would show an unexpectedly empty tab. **Filed follow-up:** `WatchTenant` (`bluedb/embedded.go:557`) reads the registry copy — if a future change makes `tenantCol` matter to the reactive baseline, that line must switch to `&coll`. Out of scope here (the reactive path is tag-partitioned, not column-filtered). |
| **R17** | **Tenant predicate defeats index selection** — `AND(tenantEq, userWhere)` fails `classifyAndRange` (`bluedb/cond.go:288-318`, which needs two bounds on ONE column) ⇒ collection scan. | Sound (over-approximate read-set); the admin browse is capped at `MaxRows=200`. | `TestReadRows_CapsAtMaxRows`. Filed as a perf follow-up, not a correctness one. |
| **R18** | **`sqlBrowseSem` slot leak on panic** (exp defect). | `defer` the release. | `TestSqlBrowseSemaphoreReleasedOnPanic`. |
| **R19** | **Lock-order inversion** between `dbRegistryMu` and `browsableTablesMu` (exp's `findSqlSource` takes them nested; `listSqlSources` does not). | Make `findSqlSource` follow the snapshot-then-release discipline. | `go test -race` on the SQL tests; a deadlock would hang the 30 min timeout. |
| **R20** | **`Db_autoMigrate` tables invisible** (exp defect). | Hook both `Db_createCols:145` and `Db_autoMigrate:~210`. | `TestAutoMigrateRegistersBrowsable`. |
| **R21** | **Secrets leak via the SQL plane** (DSN password, `password` column). | Opaque sha256 handle; credential-free label; `'***' AS col` **server-side** so the bytes never leave the DB; token-aware `isSensitiveCol`. | `TestSqlSourceHandleOpacity`, `TestSqlBrowseRedactsServerSide`, `TestSqlRedactionTokenAware`; browser check F. |
| **R22** | **`Ffi.kernel "Data_*"` fails to resolve** (if raw kernel aliases turn out to need a `kernel.rs` entry after all). | `lower.rs:1122-1133` resolves `name = Ffi.kernel "Raw"` to `rt.Raw` directly; `HubStore.sky:67-137` is the working precedent. | The regen build fails loudly with `undefined: rt.Data_collections`; `abi_guard.rs` is the second net. Remedy if it happens: add `("Data", "collections", "rt.Data_collections")` rows to `kernel.rs:621`. |
| **R23** | **`[]any` of `map[string]any` fails to coerce** into `[]State_DataRow_R`. | Exactly the `Hub_readLogs` → `[]State_LogEntry_R` path in production (`console_app/main.go:7196` shows the emitted `rt.TaskCoerceT[…, []State_LogEntry_R]`). Keys must match the Sky field names. | Browser check E (a mismatch surfaces as `CoerceFailure`); `TestIntegration_*` exercise the Go side only, so the browser leg is load-bearing here. |
| **R24** | **`SKY_CONSOLE_INTERNAL_TOKEN` CAF ordering** — the console's memoised `consoleAuthToken` reads the env before `ConsoleInternalTokenInit()` publishes it. | `console.go:317-320` mints it explicitly "BEFORE the sub-app inits"; `SKY_CONSOLE_LOGOUT_URL` (`console.go:315` → `Main.sky:222`) is the identical, working precedent. | `TestIntegration_SixTelemetryEndpointsAuthorizeWithInternalToken`; browser check B (six tabs render in prod). If it ever bites, make it a `()`-function per the `caf_db_read_footgun` memory. |
| **R25** | **Scope creep into the edit form.** | §1.3/§1.4: read-only is the gate's word; 5e′ is filed with its four prerequisites. | The Judge's verbatim-goal check. Any commit adding a write path is out of scope by construction (no mutate kernel exists). |
| **R26** | **Disk / memory during the sweep.** | `mem-guard.sh` running (CLAUDE.md §1); `timeout` on every long command; `scripts/example-sweep.sh` auto-prunes the Go cache at 5 GB. | The mem-guard kill itself; the `timeout` ceiling. |

---

## 9. Architectural-mechanism citations (CLAUDE.md §0.3)

Per §0.3 rule 4, each "this closes X" claim names its mechanism and site:

- **Identity producers** — mechanism: the generic gate→session identity bridge
  (`runtime-go/rt/session_identity.go:1-27`, `IdentityContextKey` at `:44`), already
  activated by `runtime-go/rt/hub/app_auth.go:129`. Site of the new write:
  `console_auth_v2.go:962` (`ConsoleGate`). Consumer, unchanged: `live.go:4117-4119`.
- **Kernel-side identity resolution** — mechanism: goroutine-local session stamping
  (`live_session_ctx.go:34-78`), applied to `Cmd.perform` bodies at
  `live.go:5306-5324` (`runWithLiveSession`). Precedents:
  `hub_bridge.go:539-549`, `bluedb_reactive.go:50-60`.
- **Structural refusal** — mechanism: grill B3's package-boundary encapsulation
  (`phase5-grill-findings.md` B3 FIX clause), applied to
  `runtime-go/rt/consoledata/`. Precedent for the tripwire form:
  `live_persist_invariant_test.go` / `TestPersistBeforeAck_EmitSiteTripwire`.
- **Row filter** — mechanism: `QueryPlan.Where` + `CondEq` over `decodeColumns`
  (`bluedb/backend.go:193-201`, `cond.go:98-104`, `indexer.go:49-67`), with the
  schema sourced from the registry (`EmbeddedBackend.SchemaOf`, new) rather than a
  caller literal (`embedded.go:325-334` uses the caller's schema — the bug).
- **Fail-closed default** — mechanism: `CondEq` on an absent/NULL column returns
  `false` (`cond.go:98-104`), and the Phase-4 strict tenant partition with **no
  wildcard bucket** (`bluedb/reactive.go:39-44,95-113`). The admin surface inherits
  the same posture, inverted from the fail-open `rejectCrossTenantSvc`
  (`hub_bridge.go:561-572`) that B2 flagged.
- **Irreducible floor (§8):** none of the above touches Go-FFI return, gob/JSON wire
  decode, or TEA `reflect.MakeFunc` dispatch. No floor authorization required.

---

## 10. Definition of done (what the Judge verifies)

1. Every declared `Std.Persist` collection appears in the console's Data tab with a
   LIST and a row DETAIL, with **no app code and no configuration**, in dev.
2. In production with a verified tenant claim, the tab shows **only that tenant's
   rows**; with a verified identity and **no** tenant claim and **no** super-admin
   marker it shows **nothing** (B2), and the reason is in the audit log.
3. A collection with no declared `tenantCol` is **hidden and unreadable** under a
   scoped decision — never a full read.
4. No code path in `rt` can obtain an allowed/unscoped decision without calling
   `consoledata.Decide` (compile-enforced by the package boundary).
5. `SKY_CONSOLE_AUTH=off` / production-unset ⇒ no console, hence no data plane.
6. `go test -race ./rt/... ./bluedb/...` green; `cargo test --workspace` green;
   `scripts/example-sweep.sh` green; `git diff --exit-code runtime-go/rt/console_app/`
   clean after a regen.
7. Browser checks A–F pass with zero `CoerceFailure` and zero dropped Sky.Live
   sessions.
8. The vacuous `TestAdminReadRows_ReadsSeededRows` is gone, replaced by tests that
   fail against `0242154e`.
