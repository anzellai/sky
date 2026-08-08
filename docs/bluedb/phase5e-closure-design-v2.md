# Phase 5e closure design **v2** — built-in Sky Console admin access to records

> **Status:** design, not implemented. Supersedes `docs/bluedb/phase5e-closure-design.md` (v1),
> whose authorization architecture was returned **REDESIGN REQUIRED** by the security grill.
>
> **Authority chain:** `.claude/AUTONOMOUS_GOAL.md` (original goal **#5**) →
> `docs/bluedb/phase5-grill-findings.md` **B2** (authoritative; overrides the v1
> dx-collapse doc) → this document.
>
> **Branch** `feat/bluedb` @ `8ceea18d`. **Every file:line below was re-verified against
> the working tree at that SHA by direct read** — including the ones v1 got right, and
> especially the ones v1 got wrong. Where v1 and the tree disagree, the tree wins and the
> disagreement is called out explicitly.
>
> **Path corrections carried forward:** the engine package is `runtime-go/bluedb/`
> (module path `sky-app/bluedb`), **not** `runtime-go/rt/bluedb/`. The `rt` package is
> `sky-app/rt` (`runtime-go/rt/console_app/main.go:13`).

---

## 0. What is kept from v1, what is replaced, and why

v1's **research** is sound and is reused wholesale. v1's **authorization architecture** is
rebuilt from the ground up. Explicitly:

| v1 section | Verdict | Reason |
|---|---|---|
| §1.2 goal-clause mapping, §5.3 UI edit list (22 rows), §5.6 regeneration dance, §8-R1 (no CI drift gate) | **KEPT** — re-verified | The console tab architecture, the `View.sky:196` hardcoded strip, the two `case tab of` blocks in `tabFetches`, the fused `)` at `View.sky:606`, the `MainTui.sky` missing `logoutUrl`, the absence of a `console_app` drift gate — all confirmed. |
| §1.3/§1.4 "read-only confirmed, the gate text says read-only" | **REPLACED** (§1 here) | Circular: it cites an earlier *agent-authored* narrowing as the authority for narrowing. Its stated technical blocker (`goty.rs` collision) is **falsified** (§1.4). |
| **D1** kernels-not-HTTP | **KEPT + strengthened** | The mechanism is right (`runWithLiveSession`, `live.go:5306-5324`). v1 missed that kernels which import `sky-app/bluedb` are **gated out of non-Persist builds** — see §2.7, a build-breaking hole v1 would have shipped. |
| **D2** "`ConsoleGate` stamps identity, zero changes to `live.go`" | **REPLACED** (§2.3) | Falsified by **B1**: the stamp at `live.go:4100-4110` is inside the *mint-only* `else` branch; a resumed session keeps a frozen identity forever. `live.go` **must** change. |
| **D3** `consoledata` package with `Decide(prod, verified, superAdmin, tenant)` | **REPLACED** (§2.5) | Falsified by **B4**: trust arrives as caller-supplied booleans. v2's `Decide()` takes **zero** arguments. |
| **D4** `tenantCol` validated in `parseEmbeddedSchema` | **REPLACED** (§3.4) | Falsified by **M2**: `parseEmbeddedSchema` runs on **every** Persist verb (6 call sites, `embedded_kernel.go:380,403,438,465,490,509`) — an admin-only typo would brick the app's data path. |
| **D5** "unique field names avoid the `goty.rs` collision" | **REPLACED** (§1.4, §6.2) | The cited collision **does not exist in the console's compile unit** and the collision class was **fixed in v0.19.1**. The rule is replaced by a correctly-signed *type-ambiguity* test. |
| §4 SQL plane in `rt` with `browseSqlTable(d Decision, …)` | **REPLACED** (§4) | Falsified by **B5**: it builds the `WHERE` from exported `d.Scoped()`/`d.Tenant()`, so any `rt` caller can build a SELECT without the predicate. |
| §4.2 "redact-by-default" | **REPLACED** (§5) | Falsified by **B7**: `isSensitiveCol` is a **name deny-list** (14 substrings + 35 tokens, `exp/bluedb:console_data_sql.go:191-228`). |
| §6 test plan | **REPLACED** (§7) | 9 of 43 tests were vacuous, unimplementable, or wrong-signed (**T5**). Every v2 test names the mutation that makes it fail. |
| §7 commit order | **REPLACED** (§8) | Rebuilt around the new architecture + the **G1** dependency + a real automated closure artifact. |

---

## 1. Scope statement

### 1.1 The user's goal, verbatim and unnarrowed

> **`5. Built-in Sky Console admin access to records.`**

Seven words. The words **"read-only"**, **"CRUD"**, **"LIST/detail"**, **"browse"** and
**"filter"** appear **nowhere** in it. They originate in agent-authored documents:

- `docs/bluedb/clean-slate-architecture.md:917-939` — "*What ships is a READ-ONLY browser
  + `Cond` filter … A scalar-field EDIT form is a future add-on … GATED on the open
  `record_fieldset_collision` codegen bug.*"
- `docs/bluedb/phase5-dx-collapse-design.md:315-352` — repeats it, and then **recommends
  shipping the edit form anyway** via the tuple workaround: "*The design **recommends
  option 2 for a Phase-5e edit form** (tuple-backed, so the edit form is not hostage to a
  compiler fix)*" (`:349-351`).
- `docs/bluedb/RESUME.md` §5e — repeats it again.
- v1 §1.3-1.4 — cites the above as "the gate text", concludes "**Verdict: read-only
  confirmed**".

`.claude/AUTONOMOUS_GOAL.md` does cite the architecture doc as part of "what done means",
so §5.7 is the *nearest* thing to an authority. It is still an **agent's** proposal, the
user has never ruled on it, and its own stated blocker is now falsified (§1.4). Under
CLAUDE.md §0 rule 3, "shipped for the scope of [my chosen subtask]" is forbidden framing.

### 1.2 Therefore — the explicit labelling

> **Read-only is an AGENT-PROPOSED SCOPE REDUCTION of the user's goal, not the goal.**
>
> **Goal #5 is NOT to be declared closed on the read-only surface alone.** A Judge asked to
> verify goal #5 must return `NOT ACHIEVED` for a read-only-only delivery **unless the user
> has explicitly ruled that read access satisfies "admin access to records."** That ruling
> is a **user decision** (CLAUDE.md §0.3 rule 2: strategic feasibility is a user-level
> decision), and it is the one thing in this design that must be escalated (§11).

### 1.3 Phase 5e-1 — the read surface, designed to be complete on its own

5e-1 is **not** "half of CRUD". It is a self-contained, independently useful capability:
*an operator can find any record in any declared collection, on any backend, scoped to
what they are entitled to see, without writing an admin app.* It ships:

| Capability | Mechanism |
|---|---|
| Enumerate collections across every open embedded backend **and** every registered SQL source | §3.6 `Collections`, §4.2 `Sources` |
| PK-ordered **cursor pagination** over a collection (not offset) | §3.5 `EmbeddedBackend.ScanPage` — the mechanism `clean-slate-architecture.md:919-921` actually specifies |
| Key-prefix narrowing (KV) and table browse with `limit`/`offset` (SQL) | §3.6, §4.3 |
| Row **detail** — the stored codec JSON rendered field-by-field | §6.3 |
| Tenant scoping, **fail-closed** | §2.6, §3.7 |
| Explicit-disclosure redaction in production | §5 |
| Full audit trail | §4.5 |

The one thing 5e-1 deliberately does **not** ship is an arbitrary `Cond` filter builder in
the UI (a key-prefix box + the SQL table browse cover the enumeration need; a `Cond` builder
is UI surface with no mechanism gap behind it). That is a **UI convenience** deferral, not
an authorization or data-access deferral, and it is named here so no one has to guess.

### 1.4 Phase 5e-2 — the write surface, and the re-verified premise

**The premise that blocked it is false.** Re-verified from scratch, not inherited:

1. **`Std.Live` does not import `Std.Analytics`.** `sky-stdlib/Std/Live.sky:45-48` — the
   complete import block is `Sky.Ffi`, `Sky.Core.Error`, `Std.Html`, `Std.Persist`. Neither
   `Std.Html` (`Std/Html.sky:19-21`) nor `Std.Persist` (`Std/Persist.sky:78-90`) reaches
   `Std.Analytics`. Repo-wide, **no stdlib module imports `Std.Analytics`**.
2. **`EventProp` is not in the console's compile unit.** `grep -c EventProp
   runtime-go/rt/console_app/main.go` → **0**. No console `.sky` file imports
   `Std.Analytics`.
3. **The collision class was FIXED in v0.19.1.** `select_record_candidate`
   (`rust/crates/lower/src/goty.rs:274-302`) keeps every candidate per field-name set and
   selects the one whose field **types** match. `CHANGELOG.md:594-611`;
   regression example `examples/54-record-fieldset-collision`.
4. **The console already contains a benign 2-candidate field-name set and builds.**
   `State_Identity_R` (`console_app/main.go:189-193`) and `Std_Live_Console_Identity_R`
   (`:585-589`) share `{Subject, Email, Claims}` with **byte-identical field types**.
5. **The residual, genuinely-open risk is narrower than claimed** — the
   `record_fieldset_collision_erased` recurrence: when a field value **erases to `any`**
   (tuple destructure / `fst`/`snd`), `go_ty(tmpl) == go_ty(ct)` fails for *every*
   candidate and `select_record_candidate` falls through to
   `.or_else(|| candidates.first())` (`goty.rs:301`) — an arbitrary pick. This bites only
   when (a) ≥2 candidates share the field-name set **and** (b) their field types differ
   **and** (c) a value erases. The console satisfies none of (a)∧(b) today.

⇒ **`goty.rs` does not block a Phase 5e-2 edit form.** v1's D5 ("uniquely-prefixed field
names, because a generic `{key,value}` would collide with `Std.Analytics.EventProp`") is
built on a false statement and its comment text must not be written into `State.sky`. What
replaces it is a *correctly-signed mechanical test* (§7.5 `TestConsoleAppRecordFieldsetsAreTypeUnambiguous`).

**What Phase 5e-2 actually requires** (each is a separate fail-closed proof, none of them a
compiler bug):

| # | Requirement | Mechanism / site |
|---|---|---|
| W1 | A **write decision** distinct from the read decision — `Decision.MayWrite()`, false unless `SKY_CONSOLE_DATA=readwrite` **and** the principal is verified **and** (scoped ⇒ the row's tenant column equals the verified tenant *both before and after* the edit) | §2.5's `Decide()` gains a second, independently-gated outcome |
| W2 | **Session-store collections are refused.** A raw row write corrupts the gob frame (`live_store.go` `storableSession` / `decodeSession:1391`) | a denylist of engine-owned collection names inside `consoledata` |
| W3 | The **`Generated` column contract** is preserved — `CollSchema.Generated` (`bluedb/backend.go:153`), refilled by `fillGenerated` (`embedded.go:637`), never client-supplied | write goes through `PutTenant`, never `blindPut` |
| W4 | The write carries the **verified** tenant: `PutTenant(coll, key, row, cols, verifiedTenant)` (`embedded.go:149`), so a scoped admin cannot write into another tenant | §2.6 |
| W5 | CSRF: the write is a Sky.Live event on the console's own session (no cross-site POST reaches a kernel), plus a per-session action token | §2.3's reconciled session |
| W6 | **Audit**: `console.data.write` with before/after column names (never values) | §4.5 |
| W7 | A **scalar-only** form: relations / nested records / enum-choices map to a JSON blob a generic form cannot structure — declared limit, not a bug | `clean-slate-architecture.md:930-932` |

5e-2 is a designed, costed follow-on — **not** "deferred to Stage 6+". It is listed in §8 with
its own commit slots and it is what the user's ruling (§11) unblocks.

### 1.5 Explicitly out of scope, with the reason stated as a mechanism

1. **Hub-mode data browsing.** `SKY_CONSOLE_HUB_DB` runs the console in the hub daemon's
   process; `embeddedByID` and `dbRegistry` are per-host-app and are **absent** there.
   Enumeration returns empty by physics. `HubStore.sky` gets explicit `Task.succeed []`
   arms — the precedent is `readAnalytics` (`HubStore.sky:57-61`) for the same reason.
   (exp/bluedb reached the same conclusion: `exp/bluedb:HubStore.sky:66-73`.)
2. **A public HTTP data endpoint.** exp's plane is HTTP-only and its `dataAuthOK` accepts
   the per-boot **internal** token as a data principal — a confused deputy (the token
   authenticates the console *sub-app*, not a human;
   `console_internal_token.go:14-19` says so). §2.2 removes the transport entirely.
3. **A durable, engine-verified tenant.** See **M1**/§3.9 — named, mechanism given, filed.

---

## 2. The rebuilt authorization architecture

### 2.1 The five things that are broken today (each re-verified)

| | Finding | Verified at |
|---|---|---|
| **G-a** | Nothing computes a console principal. `ConsoleGate` returns a bare `bool` (`console_auth_v2.go:962-964`); `evaluateAppMode` **holds** a populated `ConsoleIdentity` at `:503` and discards it at `:510`, keeping only the subject in the cookie. `verifyCookieValue`'s subject return is discarded at **both** call sites (`:465`, `:488`). | ✅ |
| **G-b (B1)** | The identity stamp is inside the **mint-only** `else` branch (`live.go:4100-4110`); the resume discriminator is `live.go:4059-4061`. `sess.sid` is deliberately re-set on both paths (`:4115`) — identity was not given the same treatment. | ✅ |
| **G-c (B1)** | `/_sky/console/_logout` (`console_auth_v2.go:874-877`) clears **one** cookie. There are **three**: `__Host-sky_console` (`:81`, 4 h, Secure, Strict), `sky_console_sid` (v0.15.x, `console_auth.go:95`, 4 h) and **`sky_sky_console_sid`** — the Live sub-app session that *carries the identity* (`subapp_inprocess.go:316-323` → `live.go:6402-6427`: `Path=/`, **`Secure:false`**, `SameSite=Lax`, sliding, no `__Host-` prefix). | ✅ |
| **G-d (B2)** | Token, app **and** dev-open all call the identical `deriveConsoleSigningKey()` with no mode discriminator (`console_auth_v2.go:194-195` → `:218-246`, HKDF info `"sky-console-cookie"`). The cookie payload is `b64(subject).exp.sig` (`:286-293`) — **no version, no mode, no audience, no issued-at**. | ✅ |
| **G-e (B3)** | `consoleDataAccess` evaluates `if !prod { return allowed-unscoped }` **first** (`console_data.go:33-35`), and `productionFromEnv()` returns `false` when both `ENV` and `SKY_ENV` are unset (`observability.go:314-324`). A correctly-configured multi-tenant app that forgets `ENV` gives a scoped operator **unscoped reads of every tenant**. | ✅ |

Plus: `operatorClaims`, `SKY_CONSOLE_SUPER_ADMIN`, `SKY_CONSOLE_DATA` and any `superAdmin`
producer **do not exist anywhere in the repo**. `console_data.go` has **zero production
callers** — the whole file is unwired.

### 2.2 Transport: in-process kernels (D1, kept — with the build-gate correction)

The console sub-app's `Http.get` runs **server-side** carrying at most the per-boot internal
token — which authenticates the sub-app, not the human. It structurally cannot be
tenant-scoped. Instead:

```
browser → ConsoleGate → (identity stamped on the request)
        → Sky.Live session mint/reconcile (live.go)
        → Cmd.perform Task → runPerform (live.go:5306) → runWithLiveSession(sess, …)
        → rt.Data_* kernel → currentLiveSession() → SessionIdentity(sess)
        → consoledata.Decide()  [zero arguments]
```

Precedent, in production today: `tenantPrefixForSession` (`hub_bridge.go:539-549`) and
`currentSessionTenant` (`bluedb_reactive.go:50-60`) resolve exactly this way. **The tenant
never touches the wire, so it cannot be forged.**

### 2.3 B1 — principal reconciliation on the session (the rebuilt part of D2)

**The attack, restated concretely.** Operator Alice (`consoleDataTenant=acme`) signs in →
session `S` minted with `sess.identity = Alice`. Alice signs out (or her 4 h auth cookie
expires) — `sky_sky_console_sid` survives untouched, and so does `S`. Attacker Bob
authenticates as a legitimate `tenant=globex` user on the same browser. `ConsoleGate`
passes, stamps Bob. `handleInitial` takes the `existing && sess.model != nil` branch at
`live.go:4059-4061`, **never reaches the stamp at `:4100-4110`**, and serves Bob session
`S` — carrying **Alice's** identity. Bob reads acme.

**The fix — three parts.**

**(1) A principal fingerprint reconciled before session lookup.** New file
`runtime-go/rt/live_principal_reconcile.go` (package `rt`, bluedb-free):

```go
// principalFingerprint is a stable digest of the request/session principal. It
// covers EVERY field that any authorization decision reads — subject, email and
// the full sorted claim set — so a change in any of them is a different
// principal. A session may only be resumed for the SAME fingerprint it was
// minted for.
func principalFingerprint(id ConsoleIdentity, valid bool) [32]byte

// reconcileSessionPrincipal is called at the TOP of handleInitial, BEFORE
// sessionIDNamed. It compares the request's stamped identity (if any) with the
// identity frozen on the session the sid cookie names, and on a mismatch it
//   (a) deletes the server-side session,
//   (b) strips the sid cookie from r.Header so sessionIDNamed mints a FRESH sid
//       (rotation — the sid cookie is Path=/ and NOT Secure, so it is plantable
//       on plain HTTP; reusing it across principals would be a fixation hole),
// and returns rotated=true. Sessions with no identity on either side are left
// completely untouched: an ordinary Sky.Live app never stamps
// IdentityContextKey, so this is a no-op for every non-console app.
func reconcileSessionPrincipal(app *liveApp, w http.ResponseWriter, r *http.Request) (rotated bool)
```

The exact rule (the table IS the specification):

| request identity | session identity | action | why |
|---|---|---|---|
| absent | absent | **reuse** | ordinary Sky.Live app — zero behaviour change |
| present | absent | **re-stamp in place** | upgrade; a session that was never granted anything cannot be downgraded by gaining an identity |
| **absent** | **present** | **rotate + mint** | Alice's auth cookie is gone; her privileged session must not keep serving |
| present | present, FP equal | **reuse** | the common case |
| present | present, **FP differs** | **rotate + mint** | the Bob leg |

**(2) `handleInitial` edit — exactly two inserted lines.** At `live.go:4043`, immediately
before `sid := sessionIDNamed(...)`:

```go
	// v0.19 goal-#5 / grill B1 — a Live session may only be resumed for the
	// principal it was minted for. See live_principal_reconcile.go.
	_ = reconcileSessionPrincipal(app, w, r)
	sid := sessionIDNamed(r, w, app.sessionTTL, app.cookieName)
```

and the stamp block at `:4100-4110` is **hoisted out of the `else`** so it runs on both
paths (it is idempotent after reconciliation, and it is what implements row 2 of the table):

```go
	// Hoisted out of the mint-only branch (grill B1). Reconciliation above
	// guarantees the identity on r matches the session's, or that the session
	// was rotated — so this can only ever ADD an identity, never swap one.
	if id, ok := IdentityFromContext(r.Context()); ok {
		sess.identity = id
		sess.identityValid = true
	}
```

**(3) Logout and login both clear ALL THREE cookies and evict the session.**
`console.go:346` already binds the sub-app handle (`app := MountLiveSubAppInProcessWithGate(...)`;
`_ = app` at `:347`) — stash it in a package var so the auth routes can reach it.

- `/_sky/console/_logout` (`console_auth_v2.go:874-877`): `clearConsoleV2Cookie(w)` **plus**
  `clearConsoleV1Cookie(w)` (`sky_console_sid`) **plus** `clearSubAppSessionCookie(w, "sky_sky_console_sid")`
  **plus** `consoleLiveApp.store.Delete(sid)` for the sid the request carries. Also: require
  `POST` (today the closure accepts `GET`, unlike `/_login` at `:863-866`) — a `GET` logout
  is CSRF-able and, post-fix, destroys server state.
- `handleConsoleLogin` success path (`:707-712`): clear the **sub-app sid cookie** before the
  redirect, so the next `handleInitial` mints a session bound to the *new* principal.
  Belt-and-braces with (1), and it closes login-time session fixation.

**(4) Session TTL cap.** The console sub-app's Live session TTL is capped at
`min(configured, consoleAuthCookieV2MaxAge)` and defaults to **30 minutes**. This bounds the
residual window in §2.9.

### 2.4 B2 — versioned, mode-bound cookie (and mode-separated key derivation)

**Two independent fixes; ship both.**

**(a) Mode enters the KEY DERIVATION.** `deriveConsoleSigningKey` already uses HKDF
(`console_auth_v2.go:239`); the fix is the `info` parameter:

```go
// deriveConsoleSigningKeyV3 binds the key to the auth MODE. An app-mode cookie
// presented to a token-mode instance (blue/green, canary, a shared
// SKY_CONSOLE_TOKEN, or an operator flipping SKY_CONSOLE_AUTH inside the 4h
// window) now fails the MAC outright — there is no payload to trust, and no
// claim-synthesis path to promote it.
func deriveConsoleSigningKeyV3(mode consoleAuthMode) []byte {
	// … identical IKM + salt as :219-238 …
	info := []byte("sky-console-cookie/v3\x00" + consoleAuthModeName(mode) + "\x00" +
		strings.TrimSpace(os.Getenv("SKY_CONSOLE_AUDIENCE")))
	r := hkdf.New(sha256.New, []byte(secret), salt, info)
	// …
}
```

`loadConsoleAuthState` (`:192-198`) calls it with the resolved mode. `SKY_CONSOLE_AUDIENCE`
is optional (default `""`) for operators who want blue/green separation *within* a mode.

**(b) The payload is versioned, mode-stamped and self-describing.** Format is unchanged
(`part1.exp.sig`, MAC over `part1 + "." + exp`, constant-time compare at `:307`, MAC verified
**before** any decode at `:304-318` — all of that is correct and is kept verbatim). Part 1
becomes `base64.RawURLEncoding(compact JSON)`:

```go
// consoleCookieV3 is the ENTIRE authenticated payload. Every authorization
// input lives inside the MAC. Nothing is ever synthesized at verify time.
type consoleCookieV3 struct {
	V    int               `json:"v"`             // MUST be 3
	Mode string            `json:"m"`             // the mode that MINTED it
	Sub  string            `json:"s"`
	Em   string            `json:"e,omitempty"`
	Iat  int64             `json:"iat"`
	C    map[string]string `json:"c,omitempty"`   // the FULL claim set, verbatim
}
```

Verify order in `verifyConsoleCookieV3(key []byte, mode consoleAuthMode, value string) (consolePrincipal, bool)`:

1. split → 3 parts, HMAC over `part1+"."+exp`, `subtle.ConstantTimeCompare` — **unchanged**.
2. expiry (`:311-317`) — **unchanged**, but `strconv.ParseInt` replaces
   `fmt.Sscanf(expStr,"%d",&exp)` (which accepts trailing garbage; MAC-covered, so not
   exploitable, but a needless soft spot).
3. b64-decode part 1, `json.Unmarshal` into `consoleCookieV3`. **Any error ⇒ reject.**
4. `V != 3` ⇒ **reject**. `Mode != consoleAuthModeName(mode)` ⇒ **reject**.
5. Return the principal built **only** from the payload.

**There is no legacy fallback and no format discriminator.** v1's rule ("payload begins
with `{` ⇒ JSON, else a legacy bare subject") is an *unauthenticated* discriminator on
attacker-influenced bytes and is deleted. A pre-upgrade cookie simply fails step 3/4 and the
user re-authenticates once. That is the fail-closed outcome.

**Claims are NEVER synthesized at verify time.** `operatorClaims()` (the
`SKY_CONSOLE_DATA_TENANT` / `SKY_CONSOLE_DATA_SUPERADMIN` envs) is read **at mint time
only**, in token / admin-token mode, and the resulting values are written **into** the MAC'd
payload. A cookie that carries no claims stays claimless for its whole life ⇒ DENY in
production. This is the single change that kills the B2 escalation chain.

`verifyCookieValue` (`:298-323`) stays **verbatim** for any non-console caller; the console
paths (`:465`, `:488`) switch to `verifyConsoleCookieV3`.

### 2.5 B4 — trust is resolved INSIDE the package; `Decide()` takes no arguments

v1's `Decide(prod, verified, superAdmin bool, tenant string)` lets any `rt` code write
`Decide(false, false, false, "")` and get **allowed + unscoped**. Unexported fields stop
struct-literal fabrication, not *input* fabrication.

**New package `runtime-go/rt/consoledata/`** (import path `sky-app/rt/consoledata`).
It imports **stdlib only** — no `sky-app/bluedb`, no `sky-app/rt` (no cycle, and the
layering rule holds).

`runtime-go/rt/consoledata/decision.go`:

```go
// Package consoledata owns the Sky Console's admin data-access decision and is
// the ONLY place an admin-plane row or table read is constructed.
//
// GUARANTEE (stated precisely — see the "not a guarantee" note below):
// every read reachable from the console's admin surface carries this package's
// decision and, when the decision is scoped, its tenant predicate. That holds
// because (a) Decision's fields are unexported and Decide is its only
// constructor, (b) Decide takes NO trust inputs — production is resolved here
// and the principal comes from the bound Source, and (c) ReadRows/BrowseSQL
// build their own predicates from the unexported `tenant` field; no exported
// accessor for the tenant is used to construct a query anywhere.
//
// NOT a guarantee: this package cannot stop other rt code from reading the
// underlying store directly. The application's own data path legitimately does
// exactly that. What is prevented is an ADMIN-PLANE read that forgets, drops,
// or fabricates the scope. (v1's doc comment claimed the stronger, false
// property; same-package construction always remains possible in Go.)
package consoledata

// Source is the process's binding to the live console session. consoledata
// NEVER accepts booleans describing trust — a caller cannot assert "verified",
// it can only hand over the resolver.
type Source interface {
	// CurrentPrincipal returns the cryptographically-established principal for
	// the calling goroutine's live console session, or ok=false.
	CurrentPrincipal() (subject string, claims map[string]string, ok bool)
}

// Bind installs the process-wide Source. Calling it twice panics: a second
// Bind would be a trust-substitution vector. rt calls it from an init().
func Bind(s Source)

// Decision is the outcome of the gate. The zero value denies. There is no
// exported accessor for the tenant: it is used to build predicates INSIDE this
// package and is emitted for audit only through LogFields.
type Decision struct {
	allowed    bool
	scoped     bool
	tenant     string
	disclose   Disclosure
	reason     string
}

func (d Decision) Allowed() bool         { return d.allowed }
func (d Decision) Scoped() bool          { return d.scoped }   // UI empty-state copy only
func (d Decision) Disclosure() Disclosure { return d.disclose }
func (d Decision) Reason() string        { return d.reason }
func (d Decision) LogFields() []any      // structured-log k/v pairs, never row data

// Decide is the FAIL-CLOSED gate. It takes NO arguments by design (grill B4).
func Decide() Decision
```

`prod` is resolved inside the package. To avoid two definitions of "is this production",
the canonical check moves to a new leaf package **`runtime-go/rt/skyenv/`**
(`sky-app/rt/skyenv`, ~20 lines, zero dependencies), and
`rt.productionFromEnv()` (`observability.go:314-324`) becomes a one-line forward so its
existing callers are untouched. *Rejected alternative:* duplicate the logic in
`consoledata` + a cross-package parity test — a parity test can only catch drift after it
happens; one definition cannot drift. The move also fixes the missing `strings.TrimSpace`
(`ENV=" production"` currently works by luck).

### 2.6 The decision function — B3 fixed (scope before the `!prod` shortcut)

```go
func Decide() Decision {
	if !ReadsEnabled() {
		return Decision{reason: "data plane disabled (SKY_CONSOLE_DATA)"}
	}
	prod := skyenv.IsProduction()

	sub, claims, ok := currentSource().CurrentPrincipal()
	if !ok {
		if prod {
			return Decision{reason: "no verified console identity"}
		}
		return Decision{allowed: true, scoped: false,
			disclose: DiscloseAll, reason: "dev console — no identity, unscoped"}
	}

	// ── B3: a VERIFIED TENANT CLAIM SCOPES, IN EVERY ENVIRONMENT. This branch
	// is deliberately ABOVE the !prod shortcut. v1 kept the shortcut first, so a
	// correctly-configured multi-tenant deployment that merely forgot to set ENV
	// handed a scoped operator every tenant's rows (console_data.go:33-35 +
	// observability.go:314-324). It is also the only way an operator can TEST
	// their scoping without pretending to be in production.
	if t := claims[claimDataTenant]; t != "" {
		return Decision{allowed: true, scoped: true, tenant: t,
			disclose: discloseFor(prod), reason: "tenant-scoped"}
	}
	if truthy(claims[claimDataSuperAdmin]) {
		return Decision{allowed: true, scoped: false,
			disclose: discloseFor(prod), reason: "platform super-admin"}
	}
	if !prod {
		return Decision{allowed: true, scoped: false,
			disclose: DiscloseAll, reason: "dev console — verified, no data claim"}
	}
	// verified, no data-tenant claim, no super-admin marker → fail-closed (B2).
	_ = sub
	return Decision{reason: "no data-tenant claim and no super-admin marker — " +
		"refusing all-tenant access (fail-closed)"}
}
```

`discloseFor(prod) = DiscloseAll` when `!prod`, else `DiscloseDeclared` (§5).

### 2.7 The build gate (B6) — the hole v1 would have shipped

`materialise_rt` (`rust/crates/project/src/build.rs:1253-1308`) **does** recurse into
subdirectories (`:1274`, unconditional) **and** the `"sky-app/bluedb"` import scan applies at
every depth (the gate is in the same function body; `persist_needed` is threaded through).
So a new `rt/consoledata/` directory is safe **provided it never imports bluedb** — which is
the design.

**But v1's `rt/data_kernel.go` would have broken every non-Persist app with a console.**
`console_app/main.go` is materialised whenever `console_needed`, independently of
`persist_needed`. It will reference `rt.Data_collections`. If `data_kernel.go` imports
`sky-app/bluedb` (which it must, to enumerate `*bluedb.EmbeddedBackend`), the gate skips it
for a non-Persist project → **`undefined: rt.Data_collections`**. That is exactly the
`19cffb93` regression class (`console_data.go` making `examples/01-hello-world`
unbuildable), and neither `go test ./rt/...` nor `cargo test --workspace` would catch it.

**The fix is the pattern the codebase already uses.** `live_reactive_hooks.go:1-14`:
*"the bluedb-free seam between live.go and the Phase-4b reactive integration …
bluedb_reactive.go's init() wires them to the real implementations … When bluedb_reactive.go
is absent the hooks stay no-ops."* Apply it:

| File | Imports bluedb? | Gated? | Contents |
|---|---|---|---|
| `rt/data_kernel.go` | **no** | never | `Data_collections`, `Data_rows`, `Data_sqlSources`, `Data_sqlRows`; calls `kvAdminSourcesHook` |
| `rt/data_kernel_hooks.go` | **no** | never | `var kvAdminSourcesHook = func() []consoledata.KVSource { return nil }` |
| `rt/bluedb_admin.go` | **yes** | yes | the `*bluedb.EmbeddedBackend` → `consoledata.KVSource` adapter; `init()` sets the hook |
| `rt/consoledata/**` | **no** | never | the funnel |
| `rt/skyenv/**` | **no** | never | `IsProduction` |

For a non-Persist app the hook stays nil, the Data tab lists only SQL sources, and the build
is green. `console_data.go` (which imports bluedb today) is deleted; its enumeration moves to
`bluedb_admin.go`.

**Regression-test extension** (the `persist_gate` test at `build.rs:1703-1753` has two blind
spots the agent confirmed: it runs with `console_needed:false`, and it cannot see *companion*
breakage): §7.6 adds `persist_gate_console_app_builds_without_persist`, which materialises
with `console_needed:true, persist_needed:false` **and runs `go build ./...`** on the result.

### 2.8 The full decision table, per auth mode

`SKY_CONSOLE_DATA` (§2.10): unset ⇒ **on** in non-production, **OFF** in production. Parsing
is `strings.ToLower(strings.TrimSpace(...))` — exp's matching is case-sensitive, so
`SKY_CONSOLE_DATA=OFF` is a **no-op** there (`exp/bluedb:console_data.go:29-40`); that defect
is not ported.

| `SKY_CONSOLE_AUTH` | prod | principal source | `Verified` | data-tenant claim | super claim | **Decision** | disclosure | operator must set |
|---|---|---|---|---|---|---|---|---|
| `off` | any | — | — | — | — | **console not mounted** (`console.go:293-296`) | — | — |
| unset | **true** | — | — | — | — | **console not mounted** (`consoleAuthModeUnsetProd`, `console.go:297-300`) | — | — |
| unset | false | dev-open | `false` | — | — | **ALLOW · unscoped** | All | nothing |
| `token` | false | v3 cookie | `true` | env→cookie | env→cookie | tenant ⇒ **SCOPED**; super ⇒ unscoped; else **ALLOW · unscoped** (dev) | All | `SKY_CONSOLE_TOKEN` |
| `token` | **true** | v3 cookie (sub `token-auth`, `:710`) | `true` | `SKY_CONSOLE_DATA_TENANT` | `SKY_CONSOLE_DATA_SUPERADMIN` | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | `SKY_CONSOLE_TOKEN` + `SKY_CONSOLE_DATA=on` + one data env |
| `app` | false | callback / v3 cookie | `true` | `Claims["consoleDataTenant"]` | `Claims["consoleDataSuperAdmin"]` | tenant ⇒ **SCOPED**; else **ALLOW · unscoped** (dev) | All | `consoleAuth` callback |
| `app` | **true** | callback / v3 cookie | `true` | `Claims["consoleDataTenant"]` | `Claims["consoleDataSuperAdmin"]` | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | callback returns the claim + `SKY_CONSOLE_DATA=on` |
| any | any | **internal token** (`SKY_CONSOLE_INTERNAL_TOKEN`) | **`false`** | — | — | **DENY** in prod / dev-open arm otherwise | — | — (by design) |
| any | **true** | `SKY_ADMIN_TOKEN` bearer | `true` | env | env | per matrix | Declared | `SKY_ADMIN_TOKEN` + `SKY_CONSOLE_DATA=on` + a data env |
| **app-mode cookie presented to a token-mode instance** | any | — | — | — | — | **MAC FAILS** (mode-keyed HKDF, §2.4a) → re-auth | — | — |
| **pre-v3 cookie after upgrade** | any | — | — | — | — | **rejected at step 4** → one re-login | — | — |

**Why the operator envs are token/admin-token only.** In app mode the callback's claims are
the sole authority. If `SKY_CONSOLE_DATA_SUPERADMIN=1` also applied in app mode it would
promote **every** callback-approved end user to platform-wide read — a privilege escalation
across the app's own users. Token mode has exactly one shared operator (subject
`token-auth`, `console_auth_v2.go:710`), so a deploy-time declaration *is* that operator's
identity. Pinned by `TestAppMode_IgnoresOperatorEnvClaims` (§7.3).

### 2.9 Revocation window — bounded and named, not waved away

Claims are frozen twice: into the v3 cookie (`consoleAuthCookieV2MaxAge` = 4 h, `:86`) and
onto the session at mint. §2.3 makes a *changed or lost* principal rotate the session on the
next `handleInitial`. The residual window is a session that is **already open and never
reloads**: its SSE stream and event POSTs keep using the frozen identity until the session
TTL expires. Mitigations, all shipped: the console TTL cap of 30 min (§2.3-4); immediate
lockout via `SKY_CONSOLE_AUTH=off` or `SKY_CONSOLE_DATA=off`; documented in
`docs/v0.16.x-console/EMBEDDED.md`. **Hardening filed with its mechanism:** run
`reconcileSessionPrincipal` in `handleEvent` too, answering `401` on mismatch — this
requires verifying the Sky.Live client's 401 handling (the `__skyForceReopenSSE` path,
memory `sky_live_patch_target_missing_2026_07_17`) and is therefore an explicitly-named
follow-on, not an assumption.

### 2.10 M3 — `SKY_CONSOLE_DATA` is opt-IN in production

**Decision: opt-in in production, on by default in dev.** Justification, not preference:
the upgrade path is the threat. Apps already return `Claims={"tenant":t}` for telemetry
filtering; if the data plane were default-on, upgrading the compiler would newly disclose
tenant *t*'s application records — collection names and primary keys at minimum (keys are
frequently emails). A plane that discloses **arbitrary application data** must not switch
itself on during a version bump. exp had this right (`dataReadsEnabled`,
`exp/bluedb:console_data.go:42-49`) and v1's demotion to opt-out is reverted.

Values (case-insensitive, trimmed): `off`/`0`/`false` ⇒ off; `on`/`readonly`/`ro`/`1`/`true`
⇒ on; `readwrite`/`rw` ⇒ **rejected with a startup warn** in 5e-1 (no write path exists;
silently accepting it would let an operator believe writes are gated when they are absent).
Unset ⇒ on in non-production, **off in production**.

### 2.11 M4 — a distinct data-plane claim key

`tenant` is already consumed by `tenantPrefixForSession` (`hub_bridge.go:539-549`) and
`rejectCrossTenantSvc` (`:561-572`), which gate **all four** `Hub_readFiltered*` kernels
(`:581/588/589`, `:622/629/630`, `:661/668/669`, `:700/707/708`). Reusing it means
`SKY_CONSOLE_DATA_TENANT` would silently re-filter six telemetry tabs, and conversely an app
that sets `tenant` for telemetry would silently acquire data scoping it never asked for.

**Decision: distinct keys, no fallback.**

```go
const (
	claimDataTenant     = "consoleDataTenant"
	claimDataSuperAdmin = "consoleDataSuperAdmin"
)
```

A fallback to `tenant` would re-couple the surfaces, so there is none; the tab's empty state
names the exact claim to set. Pinned in both directions by
`TestDataClaimDoesNotAffectTelemetryScope` and `TestTelemetryClaimDoesNotScopeData` (§7.3).
Documented in `docs/v0.16.x-console/EMBEDDED.md` and `docs/sky-toml.md`.

### 2.12 M5 — no file-derived signing key when the data plane is live in production

`deriveConsoleSigningKey` falls back to `ensureDevConsoleToken()` (`:256-268`), which reads a
**CWD-relative** `.sky/console-token`, accepts any `len(b) >= 32`, and on an unwritable CWD
returns a **different** fresh token to the key-derivation site (`:227`) than to the login
comparison site (`:700`). Its `randomDevToken` fallback on a `crypto/rand` failure is
`fmt.Sprintf("dev-fallback-%d-%d", os.Getpid(), time.Now().UnixNano())` (`:271-279`) — not a
secret. Post-redesign a forged cookie buys super-admin over every record.

**Rule:** at `MountEmbeddedConsole` time, if `skyenv.IsProduction() && dataPlaneEnabled &&
the signing secret did not come from an explicit ≥32-byte `SKY_CONSOLE_TOKEN`` ⇒ **the data
plane declines to arm** (loud `console.data.disabled reason=weak-signing-key` naming the env
var). The console itself still mounts — fail-closed on the privileged plane only, so a
misconfiguration does not brick observability. Additionally `randomDevToken`'s non-random
fallback becomes a hard error rather than a guessable string.

---

## 3. The tenant-scoped row filter, and the engine fixes it needs

### 3.1 M1 — the trust boundary, stated plainly (not buried)

The engine's write-time tenant tag is **explicitly, by design, never durable**. Verified at
four sites: `bluedb/txn.go:78-81` ("*NEVER durably written*"), `txn.go:126-128`,
`txn.go:303`, `engine.go:112-130` ("*It is NEVER written durably: it is not part of
ChangelogPayload, never reaches EncodeChangelogPayload, and the L1 store never sees it*"),
`changefeed.go:16-27`. Mechanically corroborated: the tag appears in exactly two code sites
outside `txn.go`, both in-RAM emits (`committer.go:152`, `:318`), and **zero** times in
`changelog.go` / `keychange.go`.

⇒ the admin filter must compare a **row column**, and that column is written by the
application.

**The consequences, stated in the design, in the UI, and in the docs:**

1. **A tenant can poison another tenant's operator view.** Tenant A's app code can write a
   row whose `tenant` column says `"B"`; it will appear in B's operator browse.
2. **Rows written without a session tenant are invisible to their owner.** A background job
   or CLI write has `currentSessionTenant() == ""` (`embedded.go:376-387`); if the app does
   not set the column explicitly, the row belongs to no tenant in the admin view.
3. **Restart/replay loses tenant attribution entirely** for anything the engine tagged.

**Therefore, the honest statement of what the filter is:**

> The admin tenant filter is a **view filter over application-declared data**, not an
> authorization boundary over application *writes*. It prevents an operator from browsing
> outside their tenant. It does not defend against a malicious tenant poisoning the column.

This sentence appears verbatim in three places: this design; the Data tab's scoped footer
("*scoped by the declared `tenant` column — rows are filtered by a value the application
writes*"); and `docs/skydb/overview.md` + `docs/v0.16.x-console/EMBEDDED.md`.

**Scoped out explicitly, with its mechanism named:** a durable verified tenant would persist
`CommitReq.Tenant` into the MVCC value header (or a reserved system column) at
`committer.go:152/318` and filter on *that*. It is an engine-format change gated by
`base.CheckComparer` (`AUTONOMOUS_GOAL.md`: *`Name="skydb.mvcc.v1"` IRREVERSIBLE*) and is a
**Phase-6 item**, not a 5e line item.

### 3.2 Where `tenantCol` and `adminShow` are declared

On the Sky `Collection`, alongside `key` and `index` (`sky-stdlib/Std/Persist.sky:114-121`,
builders at `:128-145`):

```elm
todos : P.Collection Todo
todos =
    P.collection "todos" todoCodec
        |> P.key "id"
        |> P.tenantCol "tenant"
        |> P.adminShow [ "id", "title", "done" ]
```

Both ride the existing `schemaJson` string (`Persist.sky:961-968`), so the kernel arity stays
at 3 (`Persist.sky:613-656`) — **no FFI arity change**.

*Rejected:* `[data] tenantCol` in `sky.toml` (one global column for every collection;
`build.rs`'s `_ => {}` swallows typos; a *deployment* knob for a *schema* fact).
*Rejected:* auto-detecting a column literally named `tenant` (magic that silently changes the
security posture the day someone adds an unrelated `tenant` column — the whole point of B2 is
that scope is **declared**, not inferred).

### 3.3 The thread (exact edits)

| # | File:line | Edit |
|---|---|---|
| 1 | `sky-stdlib/Std/Persist.sky:114-121` | `Collection` record gains `, tenantCol : String` and `, adminShow : List String` (leading-comma-at-column-1 layout) |
| 2 | `Persist.sky:126-127` | `collection` defaults both to `""` / `[]` |
| 3 | `Persist.sky:~142` | `tenantCol : String -> Collection a -> Collection a`, `adminShow : List String -> Collection a -> Collection a`, plus `tenantColOf` / `adminShowOf` accessors |
| 4 | `Persist.sky:34` | export both builders next to `index` |
| 5 | `Persist.sky:961-968` | `schemaJson` gains `( "tenantCol", … )` and `( "adminShow", E.list E.string … )` |
| 6 | `runtime-go/rt/embedded_kernel.go:122-137` | `embeddedSchemaJSON` gains `TenantCol string \`json:"tenantCol"\`` + `AdminShow []string \`json:"adminShow"\`` |
| 7 | `runtime-go/rt/embedded_kernel.go:174-213` | `parseEmbeddedSchema` **copies both through verbatim — no validation** (§3.4) |
| 8 | `runtime-go/bluedb/backend.go:147-154` | `CollSchema` gains `TenantCol string` + `AdminShow []string` |

### 3.4 M2 — validation moves off the hot path; the swallowed error is fixed

`parseEmbeddedSchema` runs on **every** `get`/`put`/`insert`/`delete`/`query`/`count`
(`embedded_kernel.go:380,403,438,465,490,509`) plus the reactive setup
(`bluedb_reactive.go:188`). Putting admin-only validation there means an admin-only typo
returns `Err(ErrInvalidInput)` from the app's own data path. Worse, the reactive caller
**swallows** the error: `if err1 == nil && err2 == nil` (`bluedb_reactive.go:190`) —
verified, inside `(*liveApp).reactiveLoop` at `:177` — so a typo silently disables live
reactivity with no diagnostic, and the loop then blocks on `<-done` forever (`:207-214`,
whose comment only covers the SQL-arm case).

**Three-part fix:**

1. **Verb path: no validation.** `parseEmbeddedSchema` copies `TenantCol`/`AdminShow` through
   unchanged.
2. **Boot/first-registration path: WARN once.** `Register` logs
   `persist.schema.suspect coll=… tenantCol=… reason=not-a-column` once per collection.
   Non-fatal.
3. **Admin path: REFUSE.** `consoledata.ReadRows` validates and returns `ErrBadTenantCol` /
   `ErrNullableTenantCol`. The blast radius is exactly the admin plane.

**Independent fix, same commit:** `reactiveLoop`'s three silent swallows
(`bluedb_reactive.go:180` blanket `recover()`, `:190` `err1/err2`, `:195` `WatchTenant` err)
each get a `logStructured("warn", "persist.reactive.setup-failed", …)`. This is the
no-deferral rule (AGENTS.md): a silently-dead reactive binding is a bug found by this work.

### 3.5 Registry root-cause fixes at the ENGINE (not one call site)

Four defects, all confirmed, all fixed in `runtime-go/bluedb/embedded.go`:

**(a) `ensureRegistered` is set-if-absent** (`:71-78`, verified verbatim) and is called from
**12 sites** (`:128,150,168,190,326,337,396,405,410,422,433,556`). `Query` (`:325-334`) then
scans with **the caller's** `&coll`, not the registry copy — so
`adminReadRows`'s `bluedb.CollSchema{Name: collName}` (`console_data.go:84`) yields an empty
column map from `decodeColumns` and **any** non-`CondTrue` predicate evaluates false. It
"works" today only because `QueryPlan{Limit:limit}` leaves `Where` as the zero `CondNode`
= `CondTrue` (`cond.go:17-21`, `:52-55`). The doc comment at `console_data.go:78-79` claiming
a bare `{Name}` "reuses the registered schema" is **false**.

> **Root-cause fix (AGENTS.md "root-cause fixes only"):** `ensureRegistered` becomes a
> **strengthening upsert**. It refuses to install a schema weaker than the resident one, and
> it *upgrades* the registry when the incoming schema is strictly stronger:
>
> ```go
> // ensureRegistered installs cs when absent. When a schema is already resident
> // it installs cs ONLY if cs is not WEAKER — i.e. it declares at least the
> // resident column set. A bare {Name} schema (len(Cols)==0) can therefore never
> // blank a registered collection for the life of the process, from ANY caller.
> // A STRICTLY STRONGER schema (a superset of the columns, or a newly-declared
> // TenantCol/AdminShow) REPLACES the resident one: a collection first touched
> // before its tenantCol was declared must not keep TenantCol:"" forever.
> func (b *EmbeddedBackend) ensureRegistered(cs CollSchema)
> ```
>
> The check/act race in the current form (RUnlock between the read and `Register`, `:73-77`)
> is closed by doing the comparison **under the write lock**.

**(b) M6 — copies are shallow.** `Register` (`:64-69`) does `cp := cs`, a shallow struct
copy: the `Cols []ColSpec` / `Indexes []IndexSpec` slice headers and the
`Generated map[string]bool` reference are **shared with the caller**, who can mutate registry
state afterwards. And `schemaByName` (`:94-98`) returns the `*CollSchema` **after** dropping
the RLock, with the pointer escaping to `collResolver:102`, `indexerFn:112` and — long-lived
— `WatchTenant:558` (`cs` stored into `subscription.schema:563`).

> **Root-cause fix:** make the registry **copy-on-write and immutable**. `Register` deep-copies
> `Cols`, `Indexes` and `Generated` into a fresh `*CollSchema` and *replaces* the map entry;
> nothing ever mutates a resident schema in place. Escaping pointers then become safe **by
> construction** (a subscription that keeps an older pointer keeps a *consistent* older
> schema, not a torn one). The new `SchemaOf` (below) deep-copies on the way out too.
>
> *(Note for the record: v1 and the brief both refer to "`SchemaOf` shallow copy … `bluedb/backend.go:147-154`". `SchemaOf` **does not exist** — repo-wide grep returns zero Go hits; `backend.go:147-154` is the `CollSchema` declaration. The shallow-copy defect is real, but it lives in `Register:64-69`. Fixed here.)*

**(c) M7 — `Limit` is applied after full materialisation.** Confirmed: `scanFilter`
(`embedded.go:353-368`) is limit-unaware and copies + JSON-decodes **every** matching row;
`orderAndPage` (`indexer.go:174`) sorts (decoding every row *again*, `:176-183`) and only then
slices (`:223-231`). An admin browse of a large collection allocates the whole matching set —
a memory DoS on a privileged path. Also note `plan.Limit >= 0 && plan.Limit < len(rows)`:
the "no limit" sentinel is **`-1`**, so a zero-valued `QueryPlan` means limit-**zero**.

> **Fix — the mechanism the architecture doc already specifies.**
> `clean-slate-architecture.md:919-921` says the Data tab is an *"ordered range scan +
> cursor pagination"*. Implement exactly that:
>
> ```go
> // ScanPage is a PK-ORDERED, cursor-paged, EARLY-EXITING scan. It never
> // materialises more than `limit` rows: iteration stops as soon as `limit`
> // matches are collected, so an admin browse of a 10M-row collection allocates
> // O(limit), not O(matching). Cursor pagination (afterKey) replaces OFFSET, so
> // the early exit is exactly correct — there is no global sort to defeat.
> // nextAfter is "" when the page is the last one.
> func (b *EmbeddedBackend) ScanPage(coll CollSchema, where CondNode, afterKey string, limit int) (rows [][]byte, nextAfter string, err error)
> ```
>
> `Query`/`orderAndPage` are untouched (the app's own query path keeps its ordering
> semantics). Only the admin plane uses `ScanPage`.

**(d) `SchemaOf` — new, deep-copying, registry-sourced:**

```go
// SchemaOf returns a DEEP COPY of the registry-owned schema for `name`, and
// ok=false when the collection is not registered. Deep because a caller
// mutating Cols/Indexes/Generated must not be able to corrupt the registry
// (Register's `cp := cs` shares all three — embedded.go:64-69). The copy carries
// the FULL Cols/Indexes/TenantCol/AdminShow set, because Query scans with the
// CALLER's schema (embedded.go:332): a bare {Name} decodes zero columns and
// every non-CondTrue predicate evaluates false.
func (b *EmbeddedBackend) SchemaOf(name string) (CollSchema, bool)
```

### 3.6 M8 + M9 — the predicate cannot be silently lost or mistyped

**M8.** `CondTrue == 0` (`cond.go:17-21`) and `QueryPlan.Where` is a **value**, not a pointer
(`backend.go:193-201`) — so an unset predicate means **full disclosure**, fail-open. Three
independent guards, all shipped:

1. **Adapter assertion.** `rt/bluedb_admin.go`'s `ScanTenant` implementation asserts
   `where.Op != bluedb.CondTrue` whenever a tenant was supplied, and returns
   `errScopePredicateLost` otherwise.
2. **Engine assertion.** `ScanPage` returns `ErrEmptyPredicateWithCursor` if it is handed a
   `CondTrue` node together with a non-empty `tenantCol` argument.
3. **Post-filter inside the funnel (the strongest, and it needs no bluedb import).**
   `consoledata.ReadRows` receives rows as JSON bytes. Under a scoped decision it **decodes
   each returned row and asserts `row[tenantCol] == d.tenant`**; on *any* mismatch it discards
   the **entire** result and returns `ErrScopeViolation`. Cost: a JSON decode of ≤200 rows on
   a rare privileged path. This one check independently defeats M8 (lost predicate), M9
   (mistyped comparison) and an adapter bug.

**M9.** `valuesEqual` (`cond.go:107-108`) is `bytes.Equal(a.Bytes, b.Bytes)` — it consults
**neither** `.Type` nor `.Null`. And `bluedbEvalCond` never reads `CondNode.Type` at all; the
type that matters is `cond.Val.Type`. So:

- the tenant predicate sets **`Val.Type`** (not just `Node.Type`) from the declared
  `ColSpec.Type` resolved out of `SchemaOf`;
- **nullable tenant columns are rejected.** `embeddedColType` strips a trailing `"?"`
  (`embedded_kernel.go:141`), so a check on the *mapped enum* passes a nullable column —
  v1's check had exactly this hole. Validation therefore runs on the **raw declared type
  string** from `embeddedSchemaJSON.Cols[i].Type`: a `text?` tenant column is refused with
  `ErrNullableTenantCol`. A NULL tenant is not a tenant.
- the declared type must be `text` (a non-string tenant identifier is meaningless), so the
  predicate is unambiguous and no new exported `bluedb` value API is needed.

### 3.7 The funnel — narrow, consumer-side interfaces (B5, B6)

`runtime-go/rt/consoledata/read.go` — **no `sky-app/bluedb` import**:

```go
// KVSource is the narrow view consoledata needs of one embedded backend.
// Declaring it HERE (consumer-side) keeps this package bluedb-free, so it is
// materialised into EVERY app — including apps that never import Std.Persist
// (the 19cffb93 regression class; see build.rs:1278-1296).
type KVSource interface {
	ConnID() int64
	// CollectionNames lists the collections REGISTERED on this backend.
	CollectionNames() []string
	// Schema returns the declared tenant column, the declared adminShow set,
	// and the primary-key column; ok=false when the collection is unregistered.
	Schema(coll string) (tenantCol string, adminShow []string, pk string, ok bool)
	// ScanPage returns up to limit rows of coll, PK-ordered, cursor-paged. When
	// tenantCol is non-empty the SOURCE applies the equality predicate itself —
	// the caller cannot drop it, and consoledata re-verifies every returned row
	// (ReadRows' post-filter).
	ScanPage(coll, tenantCol, tenant, keyPrefix, after string, limit int) (rows [][]byte, next string, err error)
}

var (
	ErrDenied             = errors.New("consoledata: access denied")
	ErrNotRegistered      = errors.New("consoledata: collection is not registered on this backend")
	ErrNoTenantCol        = errors.New("consoledata: collection declares no tenantCol; refusing a scoped read")
	ErrBadTenantCol       = errors.New("consoledata: declared tenantCol is not a column of the codec")
	ErrNullableTenantCol  = errors.New("consoledata: declared tenantCol is nullable; a NULL tenant is not a tenant")
	ErrScopeViolation     = errors.New("consoledata: a returned row did not match the scoped tenant; discarding the whole page")
)

const MaxRows = 200
const MaxCellBytes = 512

// Collections returns the collections this decision may LIST on one source.
// Under a Scoped decision a collection with no declared tenantCol is omitted
// ENTIRELY — its existence is not disclosed.
func Collections(src KVSource, d Decision) []string

// ReadRows is the ONLY admin row-read entry. Address = (source, collection);
// keyPrefix narrows; `after` is the opaque cursor from the previous page.
func ReadRows(src KVSource, d Decision, coll, keyPrefix, after string, limit int) (Page, error)

// Page carries the disclosure-filtered rows plus the cursor. Values are already
// redacted per Decision.Disclosure() and capped at MaxCellBytes.
type Page struct {
	Rows     []Row
	Next     string
	Redacted []string
}
type Row struct {
	Key    string
	Fields []Field // ordered; Value is "***" for an undisclosed field
}
```

`ReadRows` body, in order: `!d.allowed ⇒ ErrDenied` → clamp `limit` to `MaxRows` →
`src.Schema(coll)`, `!ok ⇒ ErrNotRegistered` → if `d.scoped`: `tenantCol == "" ⇒
ErrNoTenantCol` (**never a full read**) → `src.ScanPage(coll, tenantCol, d.tenant, keyPrefix,
after, limit)` → **post-filter every row against `d.tenant`** → apply the disclosure filter
(§5) → cap every value at `MaxCellBytes`.

**M14 is resolved by the signature**: the connection id is part of a collection's *address*,
so `Data_collections` returns `{dataConn, dataName, dataKind}` and `Data_rows` takes
`(connId, coll, prefix, after)`. The v1 ambiguity (a collection name existing on two
backends) cannot arise.

### 3.8 T1 — "every declared collection" vs "every registered collection"

**The gap is real.** `CollectionNames` (`embedded.go:80-92`) enumerates `byName`, which is
populated **only** by `ensureRegistered` from the 12 CRUD/Query/Watch sites. **There is no
declaration-time registration anywhere.** A declared-but-never-touched collection is
invisible. v1 invented an eager declaration path for the SQL arm (`ensureSqlTable` →
`Db_createCols`) and **no KV equivalent** — an asymmetry it never acknowledged.

**What ships in 5e-1 — three declaration channels, covering everything except one case:**

1. **SQL arm (already works):** `ensureSqlTable` (`Persist.sky:690-692`) → `Store.create` →
   `Db_createCols`. Extended to also hook `Db_autoMigrate` (§4.4).
2. **Reactive arm:** every `Std.Persist.LiveBinding` registers its collection at
   `ensureReactiveStarted` time, before any verb runs.
3. **New explicit verb:** `P.declare : Collection a -> Task Error ()`, a one-liner an app may
   call at boot. Lowered to `Persist_declare`, which parses the schema and calls
   `ensureRegistered` **without touching any data**.

**The residual case, stated plainly rather than silently narrowed:** a collection that is
declared in Sky, is **not** reactive, is **not** SQL-backed, has **never** been read or
written in this process, and whose app never calls `P.declare` **will not appear until first
touch.** Such a collection necessarily has zero rows in this process.

Under CLAUDE.md §0 rule 3 this is a **narrowing of the gate wording and is labelled as one**,
not slipped through:

> The 5e-1 enumeration is *declaration-driven for SQL-backed, reactive, and explicitly
> declared collections, and touch-driven for the remainder.* It is **not** literally "every
> declared collection" for the residual case above.

It is surfaced in the tab's empty state ("*collections appear once declared (`P.declare`),
reactive, SQL-backed, or first used*") and escalated in §11. **The complete fix**, named with
its mechanism so it is a plan and not a wish: a lowering pass that collects every
`P.collection "<literal>" <codec>` application into a generated boot manifest calling
`Persist_declare` — a `rust/crates/lower` change with real blast radius
(`docs/rust-rewrite/13-change-verification-and-edge-cases.md` D1/D4 apply), correctly a
separate compiler task rather than a rider on a console feature.

### 3.9 What replaces `runtime-go/rt/console_data.go`

The file is **deleted**. `consoleDataAccess` and `adminReadRows` (and their tests) are
replaced, not moved: their signatures are the defects. The bluedb-touching enumeration moves
to the gated `rt/bluedb_admin.go`:

```go
//go:build !skip  — (no build tag; gating is by the import scan, build.rs:1278-1296)
package rt

import "sky-app/bluedb"

// kvSource adapts *bluedb.EmbeddedBackend to consoledata.KVSource. It is the
// ONLY place the admin plane touches the engine, and it lives in a file that
// imports sky-app/bluedb so materialise_rt gates it out of non-Persist builds
// together with the rest of the engine bridge.
type kvSource struct{ id int64; be *bluedb.EmbeddedBackend }

func init() {
	kvAdminSourcesHook = func() []consoledata.KVSource { /* snapshot embeddedByID */ }
	consoledata.Bind(rtConsoleSource{})
}

// rtConsoleSource implements consoledata.Source from the goroutine's live
// session — the same mechanism as tenantPrefixForSession (hub_bridge.go:539).
type rtConsoleSource struct{}

func (rtConsoleSource) CurrentPrincipal() (string, map[string]string, bool) {
	sess := currentLiveSession()
	if sess == nil {
		return "", nil, false
	}
	id, ok := SessionIdentity(sess)
	if !ok {
		return "", nil, false
	}
	return id.Subject, id.Claims, true
}
```

`consoledata.Bind` must be reachable for non-Persist apps too (SQL sources exist without
bluedb), so the `Bind` call lives in the **ungated** `rt/data_kernel_hooks.go`; only the
`kvAdminSourcesHook` assignment lives in the gated file.

---

## 4. The SQL plane — inside the funnel

### 4.1 What is ported and what is not

`exp/bluedb:runtime-go/rt/console_data_sql.go` (374 lines) is the prior art. Ported:
`browsableTables` + `registerBrowsableTable` + `browsableTablesFor` + `isBrowsableTable`
(default-deny allowlist), `sqlSourceInfo`/`listSqlSources`/`findSqlSource`,
`sqlBrowseMaxLimit=200`, `sqlBrowseMaxOffset=100000`, `sqlBrowseSem` (cap 4), the read-only
connection discipline, and the `'***' AS "col"` **server-side** redaction (the secret bytes
never leave the database — the one part of exp's redaction design that is unambiguously
right, and it is kept).

**Not ported:** `handleSqlBrowse` and the entire HTTP surface (§1.5-2); `dataAuthOK`
(Bearer-only, accepts the internal token as a data principal, has no notion of a tenant, and
cannot express B2); the `bearerToken` redeclaration (already on HEAD at
`console_internal_token.go:66` — a duplicate is a compile error).

### 4.2 B5 — query construction moves INSIDE the funnel

v1 put `browseSqlTable(d Decision, …)` in package `rt` and built the `WHERE` from the exported
`d.Scoped()`/`d.Tenant()` accessors — so any `rt` caller can construct a SELECT without the
predicate, and the KV guarantee would not extend to SQL. Fixed with the same consumer-side
interface trick (`database/sql` is stdlib, so `consoledata` stays bluedb-free **and**
registry-free):

```go
// SQLSource is the narrow view of one registered connection. rt supplies
// connections and the allowlist; consoledata constructs every statement.
type SQLSource interface {
	Handle() string  // opaque, per-boot (see M11)
	Label() string   // credential-free (see M10)
	Driver() string
	Tables() []string                    // the default-deny allowlist for this source
	TenantColOf(table string) string     // "" when none declared
	AdminShowOf(table string) []string   // "" set ⇒ nothing disclosed in prod
	Rebind(q string) string
	// BrowseTx opens a SEPARATE read-only connection + transaction. Column
	// introspection happens INSIDE it (see M13).
	BrowseTx(ctx context.Context) (BrowseTx, error)
}
type BrowseTx interface {
	Columns(ctx context.Context, table string) ([]string, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	Rollback() error
}

// Sources returns the sources this decision may see. Under a Scoped decision a
// source's table is listed ONLY if it has a declared tenant column — same
// DENY/HIDE rule as the KV plane.
func Sources(all []SQLSource, d Decision) []SourceInfo

// BrowseSQL is the ONLY admin SQL entry. It builds the statement itself.
func BrowseSQL(ctx context.Context, src SQLSource, d Decision, table string, limit, offset int) (Page, error)
```

`BrowseSQL` body, in order:

```go
if !d.allowed { return Page{}, ErrDenied }
if !contains(src.Tables(), table) { return Page{}, ErrNotAllowlisted }   // default deny
if !isSafeIdent(table) { return Page{}, ErrBadIdent }
clamp(limit, MaxRows); clamp(offset, MaxOffset)
release := acquireBrowseSlot()          // see M-sem below
defer release()
tx, err := src.BrowseTx(ctx); defer tx.Rollback()
cols, err := tx.Columns(ctx, table)     // introspection on the BROWSE conn (M13)
sel := discloseSelectList(cols, src.AdminShowOf(table), d.Disclosure())  // §5
q := "SELECT " + join(sel) + " FROM " + quoteIdent(table)
args := []any{}
if d.scoped {
    tcol := src.TenantColOf(table)
    if tcol == "" { return Page{}, ErrNoTenantCol }   // NEVER a full read
    q += " WHERE " + quoteIdent(tcol) + " = ?"
    args = append(args, d.tenant)                     // BOUND PARAMETER, never interpolated
}
q += " ORDER BY " + quoteIdent(cols[0]) + " LIMIT ? OFFSET ?"
args = append(args, limit+1, offset)
rows, err := tx.QueryContext(ctx, src.Rebind(q), args...)
```

No `rt` code constructs SQL. `d.tenant` is unexported and is read only here.

### 4.3 Retained hardening (verified verbatim in exp)

`isSafeIdent` + `quoteIdent` on every identifier (`exp:db_auth.go:106-140`); read-only
connection — `PRAGMA query_only = ON` / `SET default_transaction_read_only = on`
(`exp:console_data_sql.go:230-259`) **plus** `BeginTx(ReadOnly:true)` **plus** a constructed
column-only SELECT (three independent read-only guarantees); `MaxOpenConns(1)`/`MaxIdleConns(1)`;
3 s setup timeout; 5 s statement timeout; `limit+1` truncation detection.

**One latent hole exp did not see, fixed here:** `SetConnMaxLifetime(30 * time.Second)`
combined with the *session-scoped* read-only pragma means a connection retired mid-browse is
re-dialled **without** the pragma. exp's comment reasons about `MaxOpenConns > 1` but not
about lifetime-driven reconnection. Fix: `SetConnMaxLifetime(0)` for the browse pool (it is
closed at the end of the browse anyway) and apply the pragma **inside** `BrowseTx` after
`BeginTx`, so it is attached to the transaction's own connection.

### 4.4 M10–M13 + the semaphore — the exp defects, each root-caused

| # | Defect (verified) | Fix |
|---|---|---|
| **M10** | `sqlSourceLabel` (`exp:console_data_sql.go:42-55`) strips userinfo **only** on `driver == "pgx"`; the sqlite branch returns `dsn[i+1:]`, i.e. **the DSN verbatim** when it contains no `/`. `detectDriver` (`exp:db_auth.go:337-350`) needs **both** `host=` and `user=` for the libpq form, so `password=`-bearing keyword DSNs fall to sqlite. And the label is computed from `d.driver` while `openBrowseConn` re-runs `detectDriver(d.name)` — the two can disagree. | **Never derive the label from the DSN by string surgery.** `Label()` = the `dbRegistry` name the app itself gave the connection, plus the driver. If a host is genuinely wanted, `net/url.Parse` must succeed **and** yield a known scheme **and** a non-empty `Host` — any parse failure ⇒ `"<driver> source"`. Fail-closed labelling; no substring heuristics anywhere. |
| **M11** | `sqlSourceHandle` = `"src-" + hex(sha256(dsn))[:12]` (`exp:console_data_sql.go:34-40`) — unsalted, unkeyed, 48 bits. An offline **password-confirmation oracle**: given a guessed DSN, confirm it by hashing. | `HMAC-SHA256(perBootKey, dsn)` with a 32-byte `crypto/rand` key minted once per process. Handles are meaningless across boots — so the UI must re-list rather than persist a handle, which it does (`Data_sqlSources` on tab entry). |
| **M12** | `cellToString` (`exp:console_data_sql.go:361-374`) has **no cap**, while the file header claims "*Row + byte caps*" (`:14`). One `TEXT`/`BLOB` cell is read whole via `rows.Scan` into `any` and JSON-serialised. | Hard `MaxCellBytes = 512` with a `…(truncated, N bytes)` marker, enforced **inside `consoledata`** so both planes get it. The ported header comment is corrected to describe what exists. |
| **M13** | `codecTableColumns` (`exp:db_codec.go:166-204`) runs `Db_query(d, …)` at `:186` on the **app's hot-path pool**, *before* `openBrowseConn` is ever called (`:278` vs `:316`) — contending with SQLite's `MaxOpenConns(1)`. This directly contradicts the file's own header at `:8-9`. | Introspection moves **inside** `BrowseTx.Columns` (§4.2), on the browse connection. Root cause: introspection is part of the browse, not of the app's data path. `codecTableColumns` keeps its non-console migration caller (`exp:db_codec.go:221`) untouched. |
| **sem** | `sqlBrowseSem` release (`exp:console_data_sql.go:85`) is **not deferred**; a panic leaks a slot permanently and four leaks deadlock the plane forever. | `acquireBrowseSlot() (release func())` — a real seam that exists by construction, so §7.4's test is not hypothetical: `release` is `defer`red by the caller and the seam is directly testable with a panicking closure. |
| **lock** | exp's `findSqlSource` takes `dbRegistryMu` then `browsableTablesMu`; `listSqlSources` takes them in the opposite order. | `findSqlSource` follows `listSqlSources`' discipline (snapshot under `dbRegistryMu`, release, then resolve) so no nested order exists at all. |
| **allowlist** | exp hooks only `Db_createCols` (`exp:db_codec.go:147`), so a table reached solely through `Store.migrate` is invisible. | Hook **both** `Db_createCols` (`db_codec.go:133`, after the `schemaRenderTable` loop, immediately before the `return Ok`) **and** `Db_autoMigrate` (`db_codec.go:201`, right after the `codecValidIdent(table)` guard). Do **not** hook `Db_execObject*`/`Db_updateByPk`/`Db_upsertObject`: the allowlist must mean "declared schema", not "any table someone wrote to". |

Persist-declared SQL collections carry their declarations through one new kernel:

```elm
-- Persist.sky:690-692
ensureSqlTable db coll =
    Store.create db (sqlStoreOf coll)
        |> Task.andThen (\_ -> declareSqlAdmin db (collNameOf coll) (tenantColOf coll) (adminShowOf coll))

declareSqlAdmin : Db -> String -> String -> List String -> Task Error ()
declareSqlAdmin = Ffi.kernel "Persist_declareSqlAdmin"
```

### 4.5 Audit logging

Via `logStructured` (`console_auth_v2.go:669`). Never the DSN, never a row value, never the
tenant of a *denied* request beyond the reason string.

```
console.data.denied    warn  reason=<Decision.Reason()> subject=<sub> coll=<name>
console.data.list      info  subject=… scoped=<bool> disclose=<all|declared> kv=<n> sql=<n>
console.data.read      info  subject=… scoped=<bool> coll=<name> rows=<n> redacted=<n>
console.data.sql.read  info  subject=… handle=<opaque> table=<t> rows=<n> redacted=<cols>
console.data.disabled  warn  reason=<weak-signing-key|prod-opt-out>
```

---

## 5. B7 — disclosure: an explicit allow-list, and an honest guarantee

### 5.1 Why "redact by default" is false

`isSensitiveCol` (`exp/bluedb:console_data_sql.go:191-228`) is a **name deny-list**: 14
substrings + 35 tokens. Verified leaks include `stripe_sk`, `sk_live`, `dob`, `iban`,
`bank_account`, `routing_number`, `national_id`, `passport_no`, `tax_id`, `ccnum`,
`backup_codes`, `two_factor_seed`, `phone`, `home_address`. And columns come from live
introspection, so hand-migrated and foreign-service columns appear too. **v1's KV plane is
worse and it never noticed:** a KV row's stored value is the *whole* codec JSON — every
field, including the secrets — so v1's `dataValue` renders them unredacted.

### 5.2 The policy — uniform across KV and SQL

```go
type Disclosure uint8
const (
	DiscloseDeclared Disclosure = iota // ZERO VALUE — the safe default
	DiscloseAll
)
```

- **`DiscloseAll`** (non-production, or `SKY_CONSOLE_DATA=all` explicitly in dev): every
  field/column is rendered. Justification, and it is bounded: `skyenv.IsProduction()` returns
  false **only** when `ENV`/`SKY_ENV` is unset or is `dev`/`development`/`local`, so any
  deployment that sets `ENV` at all lands in the production column; and the data is on the
  developer's own disk, which they can already `cat`. The deny-list heuristic is still applied
  here as a **dev convenience** — explicitly *not* a security boundary.
- **`DiscloseDeclared`** (production, always): only fields named in the collection's declared
  `adminShow` are rendered. Everything else is `***`. The primary key is always rendered (a
  row you cannot identify is useless); documented, with the advice not to use PII as a
  primary key.
- **Zero value is `DiscloseDeclared`** — a forgotten assignment redacts rather than discloses.

**Non-Persist raw `Std.Db.Store` tables have no declaration channel.** In production they are
therefore listed but render **PK-only**, with an explicit UI state naming `P.adminShow`. That
is a deliberate, stated limitation of 5e-1, not an oversight.

### 5.3 The guarantee, restated honestly — everywhere

The ported file header, the `consoledata` package doc, `docs/skydb/overview.md` and
`docs/v0.16.x-console/EMBEDDED.md` all carry the same sentence:

> In production, the admin plane discloses **only** the fields a collection explicitly
> declares via `P.adminShow`; everything else is redacted at SELECT time (`'***' AS "col"`,
> so secret bytes never cross the database socket). The sensitive-name heuristic is a
> **development-mode convenience and is not a security boundary** — it is a deny-list and
> deny-lists are incomplete by construction (`stripe_sk`, `iban`, `dob`, `national_id` and
> `backup_codes` all pass it).

---

## 6. The Data tab UI

### 6.1 The edit list — v1's §5.3 is KEPT, re-verified, with four corrections

All 22 rows of v1 §5.3 stand (tab enum `State.sky:18-24`; `tabLabel` exhaustive `:27-46`;
`Store` `:338-351`; `Model` `:360-382`; `Msg` `:420-440`; the **hardcoded** strip list
`View.sky:196`; the router `View.sky:549-605`; **both** `case tab of` blocks in `tabFetches`
`Main.sky:569-640`; the `MainTui.sky` missing `logoutUrl`). Corrections:

1. **`State.sky:10-11`'s "The console has 5 tabs" comment is stale** (there are 6) — fix it
   while adding the 7th.
2. **`View.sky:196` is the one genuinely dangerous silent omission**: the strip list is a
   literal, not derived from `type Tab`, so a new variant type-checks and renders **no
   button**. Add `allTabs : List Tab` in `State.sky` immediately below `type Tab`, use it in
   `tabStrip`, and pin it with §7.5's `TestConsoleTabStripCoversEveryTab`.
3. **`View.sky:547-553`'s prohibition is load-bearing** — a prior shape threaded
   `{ model | logs = … }` through a per-branch `Coerce[Model]` and **dropped Sky.Live sessions
   in production** (v0.16.20 hotfix). The `DataTab` arm passes the plain `model` plus explicit
   args. No synthetic Model.
4. **Row shapes are plain records** (see §6.2) — v1's D5 rationale is deleted from the source
   comments because it is false.

### 6.2 Row shapes — records, and the correct guard

```elm
type alias DataCollection = { dataConn : Int, dataName : String, dataKind : String }
type alias DataRow        = { dataKey : String, dataValue : String, dataRedacted : Int }
type alias SqlSource      = { sqlHandle : String, sqlLabel : String, sqlDriver : String, sqlTables : List String }
type alias SqlRow         = { sqlCells : List String, sqlIsHeader : Bool, sqlRedactedCols : List String }
```

Prefixed names are kept — but for **readability and grep-ability**, not because of a
compiler bug. The comment written into `State.sky` says exactly that:

```elm
-- Field names are prefixed for readability and to keep each alias's field-NAME
-- set distinct in `record_fieldsets` (goty.rs:69). This is a HYGIENE rule, not a
-- workaround: the v0.19.1 resolver picks by field TYPES among candidates sharing
-- a name set (goty.rs:274-302), and this compile unit already contains a benign
-- 2-candidate set (State_Identity_R / Std_Live_Console_Identity_R, identical
-- field types). The residual risk is only a name-set collision whose candidates
-- have DIFFERENT field types AND a value that erases to `any` — pinned by
-- TestConsoleAppRecordFieldsetsAreTypeUnambiguous.
```

`SqlRow` carries the header as a row (`sqlIsHeader = True`) so the payload is one homogeneous
list — no 3-tuple, and none of exp's `-- Sky has no 3-tuple fst/snd` destructuring
(`exp/bluedb:DataTab.sky`). exp's tuple choice is retired: its stated cause is
non-reproducible (§1.4).

### 6.3 The tab

New `sky-bundled/console/src/DataTab.sky` (`viewDataTab : Model -> List (Element Msg)`,
local palette copied from `LogsTab.sky:31-59` — there is no shared palette module) and
`src/DataStore.sky` (four `Ffi.kernel` aliases, mirroring `HubStore.sky`'s point-free idiom;
`Ffi.kernel "X"` lowers directly to `rt.X`, `lower.rs:1122-1133`, no `kernel.rs` entry needed
for a raw alias). Return convention `[]any` of `map[string]any`, exactly the
`Hub_readLogs`/`decodeRowsJSON` path already in production (`hub_bridge.go:257-280`).

Layout: a collection list (KV + SQL, grouped by source) · a key-prefix box · a paged row
table with a **Next** button driving the cursor · a row-detail panel rendering the stored
JSON field-by-field · an explicit empty state per reason (denied / disabled / no `tenantCol`
under a scoped decision / declaration-driven enumeration note / M1's trust-boundary footer).

**Kernels return `Ok([])` on deny, never `Err`.** The reason goes to the audit log; the UI
gets a neutral empty state, so it cannot distinguish "no tenant claim" from "no such
collection".

### 6.4 The `authGet` migration — a real bug found by this work (no-deferral)

`Main.sky:642+` issues bare `Http.get` with **no** `Authorization` header at six sites.
`consoleAccessAllowed` (`console.go:395`) accepts a Bearer or falls through to
`evaluateConsoleAuth`, which in `token`/`app` mode requires the `__Host-sky_console` cookie.
The console sub-app's server-side `Http.get` has neither ⇒ **all six telemetry tabs 401 in
production today.** It went unnoticed because dev-open returns `true` unconditionally
(`console_auth_v2.go:432-452`). exp already fixed this (`exp/bluedb:Main.sky:813-825`
`consoleAuthToken` + `authGet`); port it verbatim. The memoised-CAF ordering is safe and
precedented: `ConsoleInternalTokenInit()` runs at `console.go:317-320` explicitly "*BEFORE the
sub-app inits*", and `SKY_CONSOLE_LOGOUT_URL` (`console.go:315` → `Main.sky:222`) is the
identical working pattern.

### 6.5 Regeneration

```bash
./scripts/regenerate-console.sh                     # sky fmt-clean sources first
git diff --stat runtime-go/rt/console_app/          # main.go must change
( cd rust && timeout 3600 cargo build --release --locked -p sky )
command cp -f "${CARGO_TARGET_DIR:-$PWD/rust/target}/release/sky" ./sky-out/sky
```

Environment gotchas that cost hours (`docs/bluedb/RESUME.md`): `CARGO_TARGET_DIR` may be
`/Users/anzel/.cargo/bin`; `cp` is aliased interactive → always `command cp -f`; zsh
`noclobber` → `>|`; the script needs `SKY_RUNTIME_DIR` pointing at the worktree `runtime-go`
(it sets this itself at `:146-160`) or `runtime-go/rt/*.go` edits silently fall through to the
embedded snapshot. **There is no CI gate on `console_app` drift** —
`regenerate-console.sh:386` only *prints* a hint; grep of `.github/workflows/` and
`rust/crates/xtask/` finds nothing. §8-C11 adds it.

---

## 7. Test plan — every test names the mutation that makes it fail

Ground rules, enforced by review: **no `t.Skipf` on backend-open failure — use `t.Fatalf`**
(a test that silently skips is worse than absent). A test that would pass against `8ceea18d`
without the fix, or that passes under its own stated falsifying mutation, is vacuous and does
not count.

### 7.1 `runtime-go/rt/consoledata/decision_test.go`

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestDecide_ScopesBeforeProdShortcut` | source returns `consoleDataTenant=acme`, `ENV` **unset** ⇒ `Scoped() && tenant=="acme"` | **B3.** Move the `!prod` branch above the tenant branch (i.e. restore `console_data.go:33-35`) ⇒ unscoped ⇒ fail. **Fails against `8ceea18d`.** |
| `TestDecide_VerifiedNoClaimInProdDenies` | prod + verified + no data claim + no super ⇒ `!Allowed()` | Return `allowed:true` from the final branch ⇒ fail. (The non-vacuous B2 pin.) |
| `TestDecide_NoPrincipalInProdDenies` | prod + `ok=false` ⇒ `!Allowed()` | Drop the `if prod` guard in the `!ok` branch ⇒ fail. |
| `TestDecide_TakesNoTrustArguments` | reflection over `consoledata.Decide`: `NumIn()==0` | **B4.** Add any parameter to `Decide` ⇒ fail. This is the *structural* pin v1's `TestDecision_ZeroValueIsDeny` pretended to be. |
| `TestBind_SecondCallPanics` | `Bind(a); Bind(b)` panics | Make `Bind` overwrite silently ⇒ no panic ⇒ fail. Pins the only trust-substitution vector. |
| `TestDecide_WithoutBindDenies` | unbound source in prod ⇒ `!Allowed()` | Default the unbound source to an allow-all stub ⇒ fail. |
| `TestDisclosure_ZeroValueIsDeclared` | `var d Disclosure; d == DiscloseDeclared` | Reorder the const block so `DiscloseAll` is 0 ⇒ fail. (**Not** a Go tautology: it pins the *ordering choice*, which is the safety property.) |
| `TestDecide_ProdAlwaysDeclaredDisclosure` | prod + scoped ⇒ `Disclosure()==DiscloseDeclared` | Make `discloseFor` return `DiscloseAll` ⇒ fail. |

**`TestDecision_ZeroValueIsDeny` (v1) is DELETED** — `var d Decision; !d.Allowed()` is a Go
tautology over a `bool` field and can never fail.

### 7.2 `runtime-go/rt/consoledata/read_test.go` (fake `KVSource`, no engine needed)

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestReadRows_TenantFilterActuallyFilters` | fake source holding acme+globex rows; scoped `acme` ⇒ exactly the acme rows, and the source recorded `tenantCol="tenant", tenant="acme"` | Pass `""` for the tenant ⇒ globex rows returned ⇒ post-filter trips ⇒ fail. |
| `TestReadRows_PostFilterDiscardsWholePageOnViolation` | a **deliberately non-compliant** fake source that ignores the predicate and returns a globex row ⇒ `ErrScopeViolation` **and zero rows** | Remove the post-filter loop ⇒ the globex row is returned ⇒ fail. This is the M8/M9 guard and it is the reason a fake source is used. |
| `TestReadRows_ScopedNoTenantColRefuses` | `tenantCol==""` under a scoped decision ⇒ `ErrNoTenantCol`, `len(Rows)==0` | Fall back to an unscoped read ⇒ rows returned ⇒ fail. |
| `TestReadRows_UnregisteredRefuses` | `Schema` `ok=false` ⇒ `ErrNotRegistered`, and the fake records **zero** `ScanPage` calls | Construct a bare schema and scan anyway ⇒ a call is recorded ⇒ fail. |
| `TestReadRows_DeniedReturnsErrDenied` | `ReadRows(src, Decision{}, …)` ⇒ `ErrDenied`, zero `ScanPage` calls | Check `d.allowed` after the scan ⇒ a call is recorded ⇒ fail. |
| `TestReadRows_CapsAtMaxRows` | fake seeded with **250** rows, `limit=10_000` ⇒ exactly `MaxRows` rows **and** the fake records `limit==MaxRows` | Drop the clamp ⇒ 250 rows / `limit==10000` ⇒ fail. (v1's version seeded nothing and was vacuous.) |
| `TestReadRows_CursorAdvances` | page 1 (`limit=2`) returns `Next != ""`; page 2 with that cursor returns the **next** rows, no overlap | Return `Next: ""` unconditionally ⇒ page 2 repeats page 1 ⇒ fail. |
| `TestReadRows_NullableTenantColRefused` | declared type `"text?"` ⇒ `ErrNullableTenantCol` | Validate the **mapped** `ColType` instead of the raw string (v1's check) ⇒ `embeddedColType` strips the `?` ⇒ accepted ⇒ fail. |
| `TestReadRows_CellsCappedAt512` | a 5 KB field ⇒ the rendered value is ≤ `MaxCellBytes`+marker | Remove the cap (exp's actual state, M12) ⇒ fail. |
| `TestCollections_ScopedHidesTenantColless` | two collections, one without `tenantCol`; scoped ⇒ only the tenanted name; unscoped ⇒ both | List both under scope ⇒ fail. (Existence non-disclosure.) |
| `TestReadRows_DeclaredDisclosureRedactsUndeclared` | prod-scoped, `adminShow=["id"]`, row has `secret` ⇒ `secret` renders `***`, `id` renders its value | Ignore `Disclosure()` ⇒ `secret` leaks ⇒ fail. |

### 7.3 `runtime-go/rt/console_principal_test.go`

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestConsoleGate_StampsIdentityOnRequest` | after `ConsoleGate(w,r)` in app mode, `IdentityFromContext(r.Context())` is `ok` with `Claims["consoleDataTenant"]=="acme"` | Restore `evaluateAppMode:510`'s discard ⇒ no stamp ⇒ fail. **Fails against `8ceea18d`.** |
| `TestCookieV3_RoundTripsClaims` | mint→verify preserves subject, email and every claim | Drop `C` from the payload ⇒ fail. |
| `TestCookieV3_TamperedClaimRejected` | flip one byte inside the b64 payload ⇒ `ok=false` | Verify the MAC over part 2 only ⇒ accepted ⇒ fail. |
| `TestCookieV3_WrongModeRejected` | mint under `app`, verify under `token` ⇒ `ok=false` | Use a mode-independent HKDF `info` (today's `:239`) **and** drop the `Mode` check ⇒ accepted ⇒ fail. **This is the B2 regression test.** |
| `TestCookieV3_KeyDerivationDiffersPerMode` | `deriveConsoleSigningKeyV3(token) != deriveConsoleSigningKeyV3(app)` | Revert `info` to `"sky-console-cookie"` ⇒ equal ⇒ fail. |
| `TestCookieV3_LegacyPayloadRejected` | a cookie minted by `signCookieValue` (`:286-293`) ⇒ `ok=false` | Add v1's "begins with `{`" discriminator + legacy acceptance ⇒ accepted ⇒ fail. |
| `TestCookieV3_NoClaimSynthesisAtVerify` | `SKY_CONSOLE_DATA_SUPERADMIN=1` set **at verify time**, cookie carries no claims ⇒ principal has no super claim ⇒ prod DENY | Read `operatorClaims()` inside verify (v1's rule) ⇒ promoted ⇒ fail. |
| `TestTokenMode_MintWritesOperatorClaims` | `SKY_CONSOLE_DATA_TENANT=acme` at **mint** ⇒ the payload carries it; unset ⇒ it does not | Read the env at verify instead of mint ⇒ the unset case still resolves ⇒ fail. |
| `TestAppMode_IgnoresOperatorEnvClaims` | `SKY_CONSOLE_DATA_SUPERADMIN=1` + a callback with no claims ⇒ **DENY** | Consult `operatorClaims()` in app mode ⇒ unscoped ⇒ fail. |
| `TestInvokeConsoleAuthCallback_ErrDenies` | a callback returning `Err …` ⇒ `(ConsoleIdentity{}, false)` | — **FAILS against `8ceea18d`**: the `idAny == nil` guard is absent, so an `Err` (`SkyResult{Tag:1}`, which the string-tagged `consoleIsResultErr` misses) unwraps to an empty-but-VALID identity. |
| `TestDataClaimDoesNotAffectTelemetryScope` | `Claims={"consoleDataTenant":"acme"}` ⇒ `tenantPrefixForSession()==""` | Reuse `"tenant"` as the data claim ⇒ non-empty ⇒ fail. (**M4**, direction 1.) |
| `TestTelemetryClaimDoesNotScopeData` | `Claims={"tenant":"acme"}` ⇒ `Decide()` in prod is **DENY** | Fall back to `"tenant"` for the data plane ⇒ scoped ⇒ fail. (**M4**, direction 2.) |
| `TestWeakSigningKeyDisablesDataPlaneInProd` | prod + data plane on + no `SKY_CONSOLE_TOKEN` ⇒ `ReadsEnabled()==false` + `console.data.disabled` logged | Remove the check ⇒ armed on a file-derived key ⇒ fail. (**M5**.) |

### 7.4 `runtime-go/rt/live_principal_reconcile_test.go` — the B1 legs

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestReconcile_DifferentPrincipalRotatesSession` | mint a session stamped `acme`; issue a second `handleInitial` whose context carries `globex` ⇒ a **new sid**, the old session **deleted**, and the served session's identity is `globex` | Restore the mint-only stamp (`live.go:4100-4110` inside the `else`) ⇒ the acme session is served to globex ⇒ fail. **This is the B1 attack, executable.** |
| `TestReconcile_LostPrincipalRotatesSession` | session stamped `acme`; second request carries **no** identity ⇒ rotate, and `SessionIdentity` on the served session is `ok=false` | Treat "no request identity" as "reuse" ⇒ Alice's privileged session survives her logout ⇒ fail. |
| `TestReconcile_SamePrincipalReusesSession` | identical fingerprint ⇒ **same sid**, model preserved | Rotate unconditionally ⇒ sid changes ⇒ fail. (Guards against a rotation storm.) |
| `TestReconcile_NoIdentityEitherSideIsNoOp` | an ordinary Sky.Live app (no `IdentityContextKey` ever): 10 sequential requests ⇒ one sid, model preserved, zero deletions | Compare fingerprints when both sides are empty and treat them as unequal ⇒ every request rotates ⇒ fail. **The blast-radius guard.** |
| `TestReconcile_ClaimOrderDoesNotRotate` | the same claims supplied in a different map iteration order ⇒ same fingerprint ⇒ no rotation | Hash the map without sorting keys ⇒ nondeterministic rotation ⇒ fail. |
| `TestLogout_ClearsAllThreeCookiesAndEvictsSession` | after `POST /_sky/console/_logout`: `__Host-sky_console`, `sky_console_sid` **and** `sky_sky_console_sid` are all `Max-Age=-1`, and `store.Get(sid)` is gone | Clear only `__Host-sky_console` (today, `:874-877`) ⇒ fail. **Fails against `8ceea18d`.** |
| `TestLogout_RejectsGET` | `GET /_sky/console/_logout` ⇒ 405 | Accept `GET` (today) ⇒ fail. |
| `TestLogin_RotatesSubAppSid` | a successful token login clears `sky_sky_console_sid` | Omit the clear ⇒ the pre-login sid survives ⇒ session fixation ⇒ fail. |

### 7.5 Generated-console guards (`runtime-go/rt/console_app/*_test.go`)

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestConsoleAppRecordFieldsetsAreTypeUnambiguous` | parse `main.go` for `type \w+_R struct`; group by sorted field-**name** set; for every set with ≥2 members assert **all members have identical Go field types** | Give two console aliases the same field names with different types (e.g. `{dataKey:String,dataValue:Int}` alongside `{dataKey:String,dataValue:String}`) ⇒ fail. **Correctly signed** — it currently passes because the one existing 2-candidate set (`State_Identity_R` / `Std_Live_Console_Identity_R`, `main.go:189-193` / `:585-589`) is byte-identical. v1's `TestConsoleAppNoRecordFieldsetCollision` is **DELETED**: its own stated falsifying mutation (renaming `dataKey`→`key`) does not falsify it, and it cites a collision that does not exist in this compile unit. |
| `TestConsoleTabStripCoversEveryTab` | every `State_Tab` constructor emitted in `main.go` appears in the emitted `allTabs` list | Add a `Tab` variant without adding it to `allTabs` ⇒ fail. Pins the `View.sky:196` silent-omission class (v1 could only flag it in prose). |

### 7.6 Engine + build gate

`runtime-go/bluedb/embedded_admin_test.go`:

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestEnsureRegistered_RefusesWeakerSchema` | register `notes` with 3 cols, then `ensureRegistered(CollSchema{Name:"notes"})` ⇒ `SchemaOf` still reports 3 cols | Restore set-if-absent + allow a bare overwrite path ⇒ blanked ⇒ fail. **The registry-poisoning root-cause pin.** (v1's `TestQueryWithBareSchemaFindsNothing` is **DELETED**: it was **wrong-signed** — it fails when someone *fixes* the root cause, locking the trap in.) |
| `TestEnsureRegistered_UpgradesStrongerSchema` | register `notes` without `TenantCol`, then with it ⇒ `SchemaOf("notes").TenantCol != ""` | Keep set-if-absent ⇒ the tenantCol never lands ⇒ a scoped admin sees nothing forever ⇒ fail. |
| `TestSchemaOf_DeepCopies` | mutate the returned `Cols`/`Indexes`/`Generated` ⇒ a second `SchemaOf` is unchanged | Return `*cs` (a shallow copy, v1's body) ⇒ the registry is corrupted ⇒ fail. (**M6**.) |
| `TestRegister_DeepCopiesCallerSlices` | mutate the caller's `Cols` slice **after** `Register` ⇒ `SchemaOf` unchanged | Keep `cp := cs` (`:64-69`) ⇒ fail. |
| `TestScanPage_StopsAtLimit` | seed 10 000 rows; `ScanPage(limit=10)` ⇒ 10 rows **and** an instrumented iterator counter ≤ 11 | Materialise then trim (today's `scanFilter`+`orderAndPage`) ⇒ counter is 10 000 ⇒ fail. (**M7**.) |
| `TestScanPage_CursorIsStableUnderConcurrentWrite` | page 1, insert a row **before** the cursor, page 2 ⇒ no duplicate, no skip of pre-existing rows | Use offset paging ⇒ a shift ⇒ fail. |
| `TestScanTenant_RejectsCondTrueWithTenant` | adapter called with a tenant but a `CondTrue` node ⇒ `errScopePredicateLost` | Drop the assertion ⇒ full disclosure ⇒ fail. (**M8**.) |

`rust/crates/project/src/build.rs` tests:

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `persist_gate_console_app_builds_without_persist` | materialise with `console_needed:true, persist_needed:false` into a temp module, then **`go build ./...`** ⇒ success | Put `Data_*` in a file that imports `sky-app/bluedb` (v1's `data_kernel.go`) ⇒ `undefined: rt.Data_collections` ⇒ fail. **Closes the `19cffb93` class for the console path, which the existing `persist_gate` test cannot see (it runs with `console_needed:false`, `build.rs:1724`).** |
| `persist_gate_detects_companion_breakage` | the same build catches a file referencing a symbol defined only in a gated file | Add such a companion without adding it to `PERSIST_COMPANIONS` ⇒ fail. Closes the second blind spot (the comment at `build.rs:1294-1295` claims the existing test does this; it does not). |

### 7.7 Integration — `runtime-go/rt/console_data_integration_test.go`

Real `httptest` server, real `MountEmbeddedConsole` wiring, a real `bluedb.Open(t.TempDir())`
backend and a real sqlite `SkyDb` in `dbRegistry`.

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestIntegration_DevOpenSeesEverything` | `ENV` unset, two tenants seeded ⇒ the kernels return **all** rows, `DiscloseAll` | Require a claim in dev ⇒ empty ⇒ fail. (Zero-config dev must stay.) |
| `TestIntegration_ProdUnsetAuthDeclines` | `ENV=production`, `SKY_CONSOLE_AUTH` unset ⇒ `console.disabled reason=auth-unset` and **no** console route | Mount anyway ⇒ fail. (The goal's "gated behind `SKY_CONSOLE_AUTH`" clause.) |
| `TestIntegration_ProdDataPlaneOptIn` | `ENV=production`, auth+claims valid, `SKY_CONSOLE_DATA` **unset** ⇒ empty + `console.data.disabled` | Default the plane on in prod (v1's opt-out) ⇒ rows returned ⇒ fail. (**M3**.) |
| `TestIntegration_TokenModeNoDataEnvDenies` | prod, valid cookie, no data env ⇒ empty + `console.data.denied` | Restore the fail-open `""`-tenant behaviour (`hub_bridge.go:561-572`) ⇒ all tenants ⇒ fail. (**B2, end to end.**) |
| `TestIntegration_TokenModeScopedSeesOwnTenantOnly` | + `SKY_CONSOLE_DATA_TENANT=acme` ⇒ exactly the acme rows; the SQL plane lists only tables with a registered tenant column | Drop the SQL `WHERE` ⇒ globex rows ⇒ fail. |
| `TestIntegration_AppModeClaimsFlowGateToKernel` | callback returns `consoleDataTenant=globex`; drive `ConsoleGate` → mint → a kernel call on that session's goroutine ⇒ globex rows only | Break any link in the chain ⇒ fail. (Every link is new.) |
| `TestIntegration_CookieFastPathPreservesTenant` | request 1 exercises the callback (counter==1); **delete `sky_sky_console_sid`**; request 2 hits the cookie branch (counter still 1) and its **freshly minted** session still scopes to globex | Drop `C` from the cookie payload ⇒ the fresh session has no claim ⇒ DENY ⇒ 0 rows ⇒ fail. **v1's version was vacuous** — under session reuse it passed with zero claims in the cookie; deleting the sid cookie is what makes it bite. |
| `TestIntegration_PrincipalSwapDoesNotLeak` | authenticate as acme, read rows; **clear the auth cookie, keep the sid cookie**, authenticate as globex; read ⇒ **only globex rows** | Skip reconciliation ⇒ acme rows ⇒ fail. **The B1 attack, end to end.** |
| `TestIntegration_InternalTokenIsNotADataPrincipal` | a request bearing `SKY_CONSOLE_INTERNAL_TOKEN` in prod ⇒ `Verified=false` ⇒ DENY | Accept it as a principal (exp's `dataAuthOK`) ⇒ allowed ⇒ fail. |
| `TestIntegration_SixTelemetryEndpointsAuthorize` | prod + token mode: `GET /_sky/console/api/traces` with the internal Bearer ⇒ 200; without ⇒ 401 | Revert `authGet` ⇒ 401 with the console's own fetch ⇒ fail. (Proves §6.4's 401 class is real.) |
| `TestIntegration_DataKernelsIgnoreAdversarialArgs` | session scoped `acme`; call every `Data_*` with a `tenant` field in the arg map **and** a `?tenant=globex` query param ⇒ only acme rows | Read a tenant from any argument ⇒ globex rows ⇒ fail. **Replaces v1's `TestDataKernels_NeverAcceptTenantFromArgs`**, a grep tripwire of the known-vacuous shape (`d := consoleDataDecide(); _ = d` passed it). |
| `TestDataKernelSurfaceIsPinned` | the exported `Data_*` set equals a pinned list | Add `Data_rowsAsTenant` ⇒ fail, forcing a reviewer to add its behavioural test. (A *deliberate* speed bump, not a proof.) |

### 7.8 SQL plane — `runtime-go/rt/console_data_sql_test.go`

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestSqlBrowseDefaultDeny` | a table never `registerBrowsableTable`d ⇒ refused | Skip the allowlist check ⇒ browsable ⇒ fail. |
| `TestAutoMigrateRegistersBrowsable` | a table created only via `Db_autoMigrate` **is** listed | Hook only `Db_createCols` (exp) ⇒ absent ⇒ fail. |
| `TestSqlSourceHandleIsKeyedAndPerBoot` | two processes (two `perBootKey`s) produce **different** handles for the same DSN; the handle is not `sha256(dsn)[:12]` | Restore the unsalted digest ⇒ equal ⇒ fail. (**M11** — the oracle.) |
| `TestSqlSourceLabelNeverContainsDSN` | for 8 DSN shapes incl. `host=h user=u password=SECRET dbname=d` (no `/`), marshal `listSqlSources()` to JSON ⇒ `"SECRET"` absent | Restore `sqlSourceLabel`'s sqlite fallback ⇒ the DSN leaks verbatim ⇒ fail. (**M10**.) |
| `TestSqlBrowseRedactsUndeclaredInProd` | prod, `adminShow=["id"]`, table has `iban` and `stripe_sk` ⇒ the emitted SQL contains `'***' AS "iban"` **and** `'***' AS "stripe_sk"` | Use the deny-list only ⇒ both pass it ⇒ leak ⇒ fail. (**B7** — chosen precisely because the deny-list misses them.) |
| `TestSqlBrowse_ScopedBindsTenantAsParam` | the SQL is `… WHERE "tenant" = ?` with the **verified** tenant as an arg; a request-supplied `tenant` is never used | Interpolate the tenant ⇒ fail. |
| `TestSqlBrowse_ScopedRequiresTenantCol` | scoped + no registered tenant column ⇒ omitted from `Tables` **and** refused on direct browse | Fall through to an unscoped browse ⇒ fail. |
| `TestBrowseSlotReleasedOnPanic` | `acquireBrowseSlot()`'s `release` runs from a `defer` in a panicking closure; 5 sequential panics then a 6th normal acquire succeeds within 1 s | Release non-deferred (exp's `:85`) ⇒ the 5th blocks ⇒ timeout ⇒ fail. **The seam exists by construction** (v1's version needed a panic seam that did not exist). |
| `TestIntrospectionUsesBrowseConn` | an instrumented `SkyDb` counts app-pool queries during a browse ⇒ **0** | Call `codecTableColumns(d, …)` before `BrowseTx` (exp, `:278` vs `:316`) ⇒ ≥1 ⇒ fail. (**M13**.) |
| `TestCellTruncatedAt512` | a 1 MB `TEXT` cell ⇒ ≤ `MaxCellBytes` + marker | No cap (exp) ⇒ fail. (**M12**.) |
| `TestBrowseIsReadOnly` | attempt an `UPDATE` through the browse tx ⇒ error on both sqlite and pgx | Drop the pragma/`ReadOnly` ⇒ succeeds ⇒ fail. |

### 7.9 Leg 3 — real use, and the automated artifact that keeps goal #5 closed

**A manual browser checklist is not a gate.** Three automated artifacts do the closing:

1. **`TestIntegration_ConsoleRendersScopedRows`** (`rt`, the strongest single test): boot the
   **real** generated `console_app` Live app via `MountEmbeddedConsole`, drive
   `handleInitial`, dispatch the `SelectTab DataTab` event, run the resulting
   `Cmd.perform`, and assert the rendered HTML **contains** an acme key and **does not
   contain** any globex key. This is the only automated leg that exercises the Sky↔Go
   coercion (`[]map[string]any` → `[]State_DataRow_R`) that unit tests structurally cannot —
   i.e. the `CoerceFailure` class. *Mutation:* rename a map key in `Data_rows` ⇒ coercion
   fails ⇒ fail.
2. **`scripts/example-sweep.sh`** gains a `script`-kind assertion on
   `examples/59-persist-live` that curls `/_sky/console/` and greps for the Data tab button
   (the `View.sky:196` silent-omission class end to end). The sweep already gates examples
   56–59 (`ca67d0f8` on the sweep-gating worktree).
3. **CI drift gate** (§8-C11): `git diff --exit-code runtime-go/rt/console_app/` after a
   regeneration step — today `regenerate-console.sh:386` only *prints* the hint, and grep of
   `.github/workflows/` + `rust/crates/xtask/` finds no gate.

The **manual browser pass** (A: dev unscoped · B: prod token, no data env ⇒ empty + the other
six tabs render · C: + `SKY_CONSOLE_DATA_TENANT=acme` ⇒ acme only · D: `SKY_CONSOLE_DATA=off`
⇒ empty · E: zero `CoerceFailure` and no dropped session across
Overview→Data→Logs→Data→Traces · F: the SQL section shows an opaque `src-…` handle and `***`
cells) is retained as **evidence**, not as the gate.

---

## 8. Commit ordering, and the G1 dependency

**G1 is a hard blocker for the real-use leg.** `docs/bluedb/g1-reactive-deadlock-fix-design.md`
documents a self-deadlock on **every initial page load of every reactive Sky.Live app**:
`handleInitial` holds `sess.mu` (`live.go:4176`) → `setupSubscriptions` (`:4183`) →
`reactiveEnsureStartedHook` (`:5492`) → `ensureReactiveStarted` re-acquires `sess.mu`
(`bluedb_reactive.go:149`). `examples/59-persist-live` — the app §7.9's browser and sweep legs
use — hangs. The fix is designed and in flight in a worktree.

**Dependency, stated precisely:** C1–C10 are Go/Rust/Sky commits verified by `go test`,
`cargo test` and the integration tests, and **none of them needs G1**. §7.9's artifacts 1–2
and the manual pass **do** need G1 merged. Ordering below reflects that; nothing is blocked
on G1 that need not be.

**P** = port · **N** = net-new · **F** = defect fix.

| # | Commit | Kind | Touches | Verified by | Depends on |
|---|---|---|---|---|---|
| **C1** | `fix(console): fail-closed identity when the consoleAuth callback errors` | P (exp `1a19aeca`) | `console_auth_v2.go:~556` | `TestInvokeConsoleAuthCallback_ErrDenies` (**fails today**) | — |
| **C2** | `fix(console): logout/login clear all three cookies + evict the Live session; POST-only logout` | F | `console_auth_v2.go:707-712,874-877`, `console.go:346` | `TestLogout_*`, `TestLogin_RotatesSubAppSid` (**fail today**) | — |
| **C3** | `fix(live): a session may only be resumed for the principal it was minted for` | **F (B1)** | new `live_principal_reconcile.go`; `live.go:4043` (+1 line), `:4100-4110` (hoist) | `live_principal_reconcile_test.go` (all) | C2 |
| **C4** | `fix(console): carry the internal Bearer on every console→parent fetch` | P/F | `Main.sky` ×6 + `authGet`; regen + rebuild | `TestIntegration_SixTelemetryEndpointsAuthorize` | — |
| **C5** | `fix(bluedb): registry is copy-on-write; ensureRegistered strengthens, never weakens` | **F (M6, root-cause)** | `bluedb/embedded.go:64-98` | `TestEnsureRegistered_*`, `TestRegister_DeepCopiesCallerSlices`, `TestSchemaOf_DeepCopies` | — |
| **C6** | `feat(bluedb): ScanPage — PK-ordered cursor scan with early exit` | **N (M7)** | `bluedb/embedded.go`, `indexer.go` | `TestScanPage_*` | C5 |
| **C7** | `feat(rt): skyenv leaf package; consoledata funnel with a zero-argument Decide` | **N (B3/B4/B5/B7)** | new `rt/skyenv/`, new `rt/consoledata/`, delete `rt/console_data.go` | `consoledata/*_test.go` (all) | C5, C6 |
| **C8** | `feat(console): v3 mode-bound cookie + per-request principal + gate identity stamp` | **N (B2)** | new `console_principal.go`; `console_auth_v2.go` ×8 sites | `console_principal_test.go` (all) | C1, C7 |
| **C9** | `feat(persist): declared tenantCol + adminShow threaded Sky→CollSchema; P.declare` | N | `Persist.sky` ×7, `embedded_kernel.go` ×2, `bluedb/backend.go`; `bluedb_reactive.go` warn fixes | parse tests; `sky check examples/59-persist-live` | C5 |
| **C10** | `feat(console): Data_* kernels (bluedb-free) + gated bluedb adapter + audit events` | **N (B6)** | new `data_kernel.go`, `data_kernel_hooks.go`, `bluedb_admin.go` | `TestIntegration_*`; `persist_gate_console_app_builds_without_persist` | C7, C8, C9 |
| **C11** | `feat(console): SQL enumeration + hardened browse inside the funnel` | P + 7 fixes | new `console_data_sql.go`; `db_codec.go:145,~210` hooks; `Persist_declareSqlAdmin` | `console_data_sql_test.go` (all) | C9, C10 |
| **C12** | `feat(console): Data tab UI (read surface) + allTabs + Tui/Hub arms + MainTui logoutUrl fix` | N+F | §6.1's 22 rows; new `DataStore.sky`, `DataTab.sky`; regen + rebuild | `TestConsoleAppRecordFieldsetsAreTypeUnambiguous`, `TestConsoleTabStripCoversEveryTab` | C10, C11 |
| **C13** | `chore(ci): gate console_app regeneration drift + the console/non-Persist build matrix` | N | `.github/workflows/`, xtask | the gate fails on a deliberately stale `main.go` | C12 |
| **G1** | *(separate worktree)* reactive initial-render deadlock fix | F | `live.go`, `bluedb_reactive.go` | its own design's §5.1/§6 | — |
| **C14** | `test(console): end-to-end scoped-render gate + example-59 sweep assertion` | N | `console_data_integration_test.go`, `scripts/example-sweep.sh` | §7.9 artifacts 1–2 | C12, **G1** |
| **C15** | `docs(bluedb): 5e-1 closure — EMBEDDED.md, sky-toml env reference, skydb trust boundary, RESUME` | — | `docs/v0.16.x-console/EMBEDDED.md`, `docs/sky-toml.md`, `docs/skydb/overview.md`, `docs/bluedb/RESUME.md`, `AGENTS.md` | `scripts/doc-examples.sh` | C14 |
| **C16+** | Phase **5e-2** — the write surface (W1–W7, §1.4) | — | — | — | **the user's ruling (§11)** |

**Push cadence** (CLAUDE.md §0.1): local commits throughout; push **once** after C13 + a
milestone sweep, and once after C15. Not per commit. C4 and C12 each require the
`regenerate-console.sh` + `cargo build` + binary-copy dance (§6.5).

**Milestone sweep** (once, at the end, backgrounded per CLAUDE.md §0.2-6):
`timeout 1800 go test -race ./rt/... ./bluedb/...` · `cd rust && timeout 3600 cargo test --workspace`
· `timeout 3600 ./scripts/example-sweep.sh` · `cargo run -p xtask -- coerce-floor` +
`repro`, per `codegen_change_run_xtask_gates_locally`.

---

## 9. Risk register

**Mitigation** = what prevents it; **Gate** = what catches it if the mitigation fails.

| # | Risk | Mitigation | Gate |
|---|---|---|---|
| **R1** | **B1 principal swap** — a resumed session serves another tenant's operator. | §2.3 reconciliation + rotation; all three cookies cleared on logout. | `TestReconcile_DifferentPrincipalRotatesSession`, `TestIntegration_PrincipalSwapDoesNotLeak`. |
| **R2** | **Reconciliation rotation storm** — an app whose identity provider returns unstable claims churns sessions. | Fingerprint over **sorted** claims; no-identity-either-side is an explicit no-op. | `TestReconcile_NoIdentityEitherSideIsNoOp`, `TestReconcile_ClaimOrderDoesNotRotate`. **Highest blast-radius change in this design** — it touches `live.go`, which every Sky.Live app runs. |
| **R3** | **B2 cross-mode cookie acceptance.** | Mode enters the HKDF `info` (MAC fails) **and** `Mode` is checked inside the MAC'd payload. | `TestCookieV3_WrongModeRejected`, `TestCookieV3_KeyDerivationDiffersPerMode`. |
| **R4** | **Claim synthesis at verify time** re-promoting a claimless cookie. | Envs read at **mint** only; verify never touches them. | `TestCookieV3_NoClaimSynthesisAtVerify`. |
| **R5** | **B3 `!prod` shortcut** giving a scoped operator every tenant. | Tenant branch is **above** the prod shortcut in `Decide`. | `TestDecide_ScopesBeforeProdShortcut` (**fails today**). |
| **R6** | **B4 fabricated decision.** | `Decide()` takes zero arguments; `prod` internal; principal via a bound `Source`; `Bind` panics twice. | `TestDecide_TakesNoTrustArguments`, `TestBind_SecondCallPanics`. |
| **R7** | **B5 SQL read outside the funnel.** | `BrowseSQL` builds every statement inside `consoledata`; `tenant` has no exported accessor. | `TestSqlBrowse_ScopedBindsTenantAsParam`; code review against "no `d.Tenant()` exists". |
| **R8** | **B6 build break** — non-Persist app + console ⇒ `undefined: rt.Data_*`. | Hook seam (`live_reactive_hooks.go` precedent); kernels bluedb-free. | `persist_gate_console_app_builds_without_persist` (a real `go build`). **This is the risk v1 would have shipped.** |
| **R9** | **B7 secret disclosure** — deny-list misses `iban`/`stripe_sk`/`dob`. | Production is an explicit `adminShow` allow-list; server-side `'***' AS`. | `TestSqlBrowseRedactsUndeclaredInProd`, `TestReadRows_DeclaredDisclosureRedactsUndeclared`. |
| **R10** | **`adminShow` makes the tab useless in production** (nothing declared ⇒ PK-only). | Zero-config stays in dev; the empty state names the exact builder; Persist collections declare it in one line. | Browser pass C. **Accepted, explicitly:** a useful-but-leaky prod default is the wrong trade for a plane that discloses arbitrary app data. |
| **R11** | **M1 tenant-column poisoning / job-written rows invisible.** | Cannot be fixed at this layer (the engine tag is non-durable by design). | **Not a test — a documented trust boundary**, stated in the design, the UI footer and two docs; the durable-tenant mechanism is named and filed to Phase 6. |
| **R12** | **M2 blast radius** — an admin-only typo bricks the data path. | Validation lives in the admin path; verb path copies through; boot warns. | `TestReadRows_NullableTenantColRefused` + the absence of any validation in `parseEmbeddedSchema`. |
| **R13** | **Registry poisoning / stale `tenantCol`.** | `ensureRegistered` strengthens and never weakens; copy-on-write registry. | `TestEnsureRegistered_RefusesWeakerSchema`, `TestEnsureRegistered_UpgradesStrongerSchema`. |
| **R14** | **M7 memory DoS on a privileged path.** | `ScanPage` early-exits at `limit`; cursor paging, not offset. | `TestScanPage_StopsAtLimit` (instrumented iterator count). |
| **R15** | **M8/M9 predicate silently lost or mistyped** (`CondTrue`==0; `valuesEqual` ignores `.Type`). | Three independent guards, incl. the funnel's row post-filter. | `TestReadRows_PostFilterDiscardsWholePageOnViolation`, `TestScanTenant_RejectsCondTrueWithTenant`. |
| **R16** | **M11 DSN oracle / M10 DSN leak / M12 unbounded cells / M13 pool contention.** | HMAC-per-boot handles; registry-name labels; 512 B cap in the funnel; introspection on the browse conn. | The four named tests in §7.8. |
| **R17** | **`goty.rs` record-fieldset ambiguity** (the *real*, narrow class). | Prefixed names as hygiene; the compile unit has one benign identical-type 2-candidate set. | `TestConsoleAppRecordFieldsetsAreTypeUnambiguous`; browser pass E. |
| **R18** | **Synthetic-Model panic** (`View.sky:495-510`/`547-553`) — dropped Sky.Live sessions in v0.16.20. | The `DataTab` arm passes plain `model` + explicit args. | Browser pass E; review against `View.sky:547-553`. |
| **R19** | **`View.sky:196` silent omission** (tab routes, no button). | `allTabs` in `State.sky`, used by `tabStrip`. | `TestConsoleTabStripCoversEveryTab`; sweep assertion §7.9-2. |
| **R20** | **Console regeneration drift** — `main.go` (291 KB) is generated **and** committed, with **no CI gate**. | §6.5 checklist. | **C13 adds the gate.** Until then the sweep assertion catches it (the tab simply is not there). |
| **R21** | **`Ffi.kernel "Data_*"` fails to resolve.** | `lower.rs:1122-1133` resolves a raw alias to `rt.X`; `HubStore.sky:67-137` is the working precedent. | The regen build fails loudly (`undefined: rt.Data_collections`); `abi_guard.rs` is the second net. Remedy: add `("Data", …)` rows to `kernel.rs:621`. |
| **R22** | **`[]any` of `map[string]any` fails to coerce** into `[]State_DataRow_R`. | The `Hub_readLogs` → `[]State_LogEntry_R` path in production. | `TestIntegration_ConsoleRendersScopedRows` (§7.9-1) — the only automated leg that sees it. |
| **R23** | **`SKY_CONSOLE_INTERNAL_TOKEN` CAF ordering** for `authGet`. | `console.go:317-320` mints it "*BEFORE the sub-app inits*"; `SKY_CONSOLE_LOGOUT_URL` is the identical working precedent. | `TestIntegration_SixTelemetryEndpointsAuthorize`. If it bites: make it a `()`-function (memory `caf_db_read_footgun`). |
| **R24** | **G1 blocks the real-use leg.** | C1–C13 need no G1; C14 is sequenced after it. | The §8 table. If G1 slips, C1–C13 still land and the *automated* Go-side gates still hold — only artifacts §7.9-1/2 wait. |
| **R25** | **Scope drift into 5e-2 mid-phase.** | No write kernel exists; `readwrite` is rejected with a warn. | `TestDataKernelSurfaceIsPinned`; §11's escalation is the only door. |
| **R26** | **Disk / memory during the sweep.** | `mem-guard.sh` running (CLAUDE.md §1); `timeout` on every long command; the sweep auto-prunes the Go cache at 5 GB. | The mem-guard kill; the `timeout` ceiling. |

---

## 10. Architectural-mechanism citations (CLAUDE.md §0.3)

- **Identity producer** — mechanism: the generic gate→session identity bridge
  (`session_identity.go:34,43,51-61`), already activated in production by
  `hub/app_auth.go:129-130`. New write site: `ConsoleGate` (`console_auth_v2.go:962-964`).
  Consumer: `live.go:4100-4110`, **hoisted** out of the mint-only branch (§2.3).
- **Kernel-side identity resolution** — mechanism: goroutine-local session stamping
  (`live_session_ctx.go`), applied to `Cmd.perform` bodies by `runWithLiveSession`
  (`live.go:5306-5324`). Precedents: `hub_bridge.go:539-549`, `bluedb_reactive.go:50-60`.
- **Structural refusal** — mechanism: grill **B3**'s package-boundary encapsulation ("*a
  separate package whose only exported entry is the funnel*"), applied to
  `rt/consoledata/` and strengthened past v1 by removing every trust argument from `Decide`
  and every query-building accessor from `Decision`.
- **Build-gate seam** — mechanism: the bluedb-free hook file
  (`live_reactive_hooks.go:1-14`), the pattern `build.rs:1278-1296` documents as the reason
  `live.go` compiles in non-Persist apps. Applied to `data_kernel.go` ↔ `bluedb_admin.go`.
- **Row filter** — mechanism: `QueryPlan.Where` + `CondEq` over `decodeColumns`
  (`backend.go:193-201`, `cond.go:98-108`, `indexer.go:49-67`), with the schema sourced from
  the **registry** (`SchemaOf`, new) rather than a caller literal — `Query` scans with the
  caller's schema (`embedded.go:332`), which is the bug.
- **Ordered cursor scan** — mechanism: the engine's native ordered iteration
  (`bluedb/keys.go`), which `clean-slate-architecture.md:919-921` names as the admin browse
  mechanism. `ScanPage` implements exactly it.
- **Fail-closed default** — mechanism: the Phase-4 strict tenant partition with **no wildcard
  bucket** (`bluedb/reactive.go`), inverted from the fail-open `rejectCrossTenantSvc`
  (`hub_bridge.go:561-572`) that B2 flagged, plus the funnel's own post-filter.
- **Irreducible floor (§8):** none of the above touches Go-FFI return, gob/JSON wire decode,
  or TEA `reflect.MakeFunc` dispatch. **No floor authorization required.** The one adjacent
  item — a *durable* verified tenant — **would** touch the MVCC key/value format guarded by
  `base.CheckComparer`, and is therefore explicitly **not** attempted here (§3.1).

---

## 11. Definition of done — and the one thing escalated to the user

### 11.1 What a Judge may verify for **Phase 5e-1**

1. Every collection reachable by the three declaration channels (§3.8) appears in the Data
   tab with a paged LIST and a row DETAIL, **with no app code and no configuration**, in dev.
2. In production with a verified data-tenant claim the tab shows **only that tenant's rows**;
   with a verified identity and no data claim and no super-admin marker it shows **nothing**,
   and the reason is in the audit log.
3. A collection with no declared `tenantCol` is **hidden and unreadable** under a scoped
   decision — never a full read.
4. In production, only `adminShow`-declared fields are disclosed; everything else is `***`,
   redacted at SELECT time on the SQL plane.
5. A Live session cannot be resumed for a different principal, and logout clears all three
   cookies and evicts the session.
6. No `rt` code can obtain an allowed decision without `consoledata.Decide()`, which takes no
   trust inputs; no `rt` code constructs an admin row or table query.
7. A non-Persist project **with the console enabled** builds (`go build ./...` on the
   materialised tree).
8. `go test -race ./rt/... ./bluedb/...` green; `cargo test --workspace` green;
   `scripts/example-sweep.sh` green; `git diff --exit-code runtime-go/rt/console_app/` clean
   after a regeneration; §7.9's three automated artifacts green.
9. Every deleted v1 test (`TestDecision_ZeroValueIsDeny`,
   `TestConsoleAppNoRecordFieldsetCollision`, `TestQueryWithBareSchemaFindsNothing`,
   `TestAdminReadRows_ReadsSeededRows`) is gone, and each replacement fails under its stated
   mutation.

### 11.2 What a Judge may **NOT** conclude

> **5e-1 does not close original goal #5 on its own.** "Admin access to records" is the
> user's phrasing; "read-only" is an agent's. A Judge verifying goal #5 against
> `.claude/AUTONOMOUS_GOAL.md` must return **NOT ACHIEVED** for a read-only delivery unless
> the user has explicitly ruled that read access satisfies it. Phrases such as "*closed for
> the read-only scope*", "*the gate text says read-only*", or "*edit deferred to 5e-2*" in a
> PASS verdict are drift under CLAUDE.md §0 rule 3 and invalidate the verdict.

### 11.3 The escalation (the only user decision this design requires)

Per CLAUDE.md §0.3 rule 2, strategic feasibility is a user-level call. Two questions, both
answerable in a sentence:

> **Q1 — Does goal #5 ("Built-in Sky Console admin access to records") require the ability to
> CREATE / EDIT / DELETE records, or is read access sufficient?**
> Context for the decision: the compiler bug that previously blocked the edit form **does not
> exist in this compile unit** (§1.4) — so the write surface is now a matter of the seven
> authorization/integrity requirements W1–W7, all of which are designed above, not of a
> compiler fix. If the answer is "writes required", C16+ implements W1–W7 on top of the
> architecture in §2 with no rework.
>
> **Q2 — Is the enumeration narrowing in §3.8 acceptable for 5e-1?**
> A collection that is non-reactive, non-SQL, never touched, and whose app never calls
> `P.declare` appears only on first use. The complete fix is a `rust/crates/lower` pass
> emitting a boot manifest — a compiler task, correctly separate from a console feature.

Neither question blocks C1–C15. Both are surfaced now rather than answered silently.
