# Phase 5e closure design **v2.1** — built-in Sky Console admin access to records

> **Status:** design, not implemented. Supersedes `docs/bluedb/phase5e-closure-design.md` (v1),
> whose authorization architecture was returned **REDESIGN REQUIRED** by the security grill.
> **v2.1** amends **v2** after the second adversarial grill (verdict
> *PROCEED WITH CHANGES: 5 blocking*).
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

## v2.1 changelog — what changed from v2, and why

Every row below is a grill finding whose fix is **structural**, not a wording change. Each was
re-verified against the working tree by direct read before the amendment was written.

| # | v2 said | v2.1 says | Why (evidence) |
|---|---|---|---|
| **B-1** | Reconcile at `handleInitial` only; `handleEvent` hardening is a "revocation timing window" (§2.9). | **`handleEvent` cookie-binding is a hard PREREQUISITE (new commit C0)**, and the principal check ALSO runs in `handleEvent` + `handleSSE` (defence in depth, §2.3-5). Login evicts the old session **server-side**, not just its cookie. | `handleEvent` reads `SessionID` from the request **body** (`live.go:4353-4355`) and calls `app.store.Get(req.SessionID)` (`:4398`) with **no** cookie comparison; `handleSSE` **does** read the cookie (`live.go:6142-6148`). The sid is not confidential — it is templated into the page as `var __skySid = %q` (`live.go:6953`). CSRF does not help: it is a **double-submit cookie** pair (`live.go:3831`, `:7724`), so any principal on the browser holds a valid pair. This is a **direct cross-principal path**, not a window. |
| **B-2** | Fail-closed posture pivots on `ENV` being set; "unset" is treated as dev, which opens every arm at once. | The data plane arms only on **two positive signals**: `SKY_CONSOLE_DATA` explicitly `on`/`all` in **every** environment (no default-on anywhere), **and** the unauthenticated/unscoped/`DiscloseAll` arm requires `skyenv.IsExplicitDev()` — `ENV`/`SKY_ENV` **explicitly** a dev marker. **Unset ⇒ production tier for the data plane.** §2.6 / §2.10 / new §2.13. | `productionFromEnv()` returns **false** when both `ENV` and `SKY_ENV` are unset (`observability.go:314-324`), and in that single state the dev-open arm returns `true` unconditionally (`console_auth_v2.go:443-452`), `SKY_CONSOLE_DATA` unset was "on", `!ok && !prod` was allowed/unscoped/`DiscloseAll`, and §2.12's weak-key refusal did not fire. Forgetting one env var disclosed every row of every tenant. |
| **B-3** | §2.12 required an explicit `SKY_CONSOLE_TOKEN`, while §2.8 promised app-mode needs no second secret. | The data plane accepts **`SKY_CONSOLE_TOKEN` *or* the new `SKY_CONSOLE_COOKIE_SECRET`** (≥32 B) as key material, in **every** mode. App mode's "no second secret" contract is preserved for the **console**; only the **data plane** demands one. §2.8's `app` rows now list it. | `deriveConsoleSigningKey` falls back to `ensureDevConsoleToken()` whenever `SKY_CONSOLE_TOKEN` is empty (`console_auth_v2.go:216-246`), and app mode's documented contract is "*App mode reuses the same signing key … (no second secret to provision)*" (`:196-198`). Both could not hold. |
| **B-4** | `Bind` appeared in **two** files (`bluedb_admin.go`'s `init()` §3.9, and the ungated hook file in the prose) and panicked on a second call. | **Exactly one `Bind` site** — the ungated `rt/data_kernel_hooks.go`. `Bind` is `sync.Once`-guarded: **first wins**, a second call is a loud `console.data.bind-duplicate` error log and a **no-op** (never a panic — a panic in `init()` kills every app). `Decide()` denies when unbound in **every** environment, not just production. | Two `Bind` calls in one package = guaranteed init panic for every Persist app with a console. A no-op preserves the actual security property (a second `Bind` cannot **substitute** the source); a panic only adds an availability failure. |
| **B-5** | Logout became POST-only in C2 while the shipped `<a href>` Sign-out link was only fixed in C12 ⇒ a 405 window. | The route accepts **GET and POST from C2 onward** and performs the **full** clear+evict on both; a cross-site GET is refused by an `Origin`/`Sec-Fetch-Site` same-site check. C12 converts the UI to a POST form. **No 405 window.** §2.3-3. | `console.go:315` injects `SKY_CONSOLE_LOGOUT_URL`; `View.sky:142-148` renders it with `Ui.link` — a plain GET anchor. |
| **M-1** | "*a strict no-op for every non-console Sky.Live app*"; fingerprint over the **full** claim set. | Reworded to "**every app that never stamps an identity**". The fingerprint covers `Subject` + `Email` + an **explicitly declared authorization-relevant claim subset** (`consoleDataTenant`, `consoleDataSuperAdmin`, `tenant`), never the whole map — volatile claims (`exp`/`iat`/`nonce`) cannot cause a rotation storm. Row 3 of the table is re-examined (§2.3-1). | `IdentityContextKey` is exported and generic (`session_identity.go:36-43`) and the hub stamps it on **every** gated request (`hub/app_auth.go:127-129`). Sorted claims fix ordering, not volatility. |
| **M-2** | Reconcile inserted **before** `sid := sessionIDNamed(...)`, outside the per-session lock; its `store.Delete` was resurrectable. | Reconcile runs **after** `sessionIDNamed` + `app.locker.Lock(sid)`; the evicted session is marked **dead** and every `store.Set` goes through one `app.persistSession` funnel that refuses a dead session. §2.3-2. | `sid := sessionIDNamed(...)` is `live.go:4043`; `app.locker.Lock(sid)` is `:4044`. Five re-persist sites (`live.go:4213, 4435, 4575, 5345, 6301`) could resurrect a deleted session. Funnel precedent: this branch's own `9ad00daf` persist-before-ack funnel. |
| **M-3** | A5's post-filter existed only in `ReadRows` (KV); `SQLSource` still supplied `TenantColOf` and a string→string `Rebind`. | `BrowseSQL` gains a **row post-filter** (the tenant column is forced into the SELECT list even when absent from `adminShow`), `tcol` is validated against the browse-tx introspection, and **`Rebind` is deleted** — replaced by a `Driver` enum owned by `consoledata`, which emits its own placeholders. §4.2. | `rt` supplying `Rebind(q string) string` means `rt` gets the finished statement back before execution: B5 moved the concatenation, not the trust. R15's "three independent guards" was KV-only. |
| **M-4** | `Page.Row.Fields` was per-field-redacted, but the Sky type was `DataRow = { dataKey, dataValue : String, … }` — one opaque string rendered "field-by-field". | The Sky row carries **`dataFields : List DataField`**, re-serialized from the **post-filtered** `Fields` — **never** the stored codec bytes. New test asserts on the **kernel return payload**, not on `Page`. §5.3's "never crosses the database socket" is now scoped to **SQL only**; for KV the record is read whole and filtered **in-process**. §3.7 / §5.3 / §6.2. | If `dataValue` carried the stored JSON it **was** the v1 leak, re-introduced one layer down. |
| **M-5** | `ensureRegistered` became a "strengthening upsert" that **replaces** a resident schema. | The **upgrade is dropped**. `ensureRegistered` stays set-if-absent; `Register` **also** refuses to replace a resident schema and logs `persist.schema.conflict`. A resident `*CollSchema` is therefore **immutable for the process's life** — strictly stronger than today, and no write-path change. §3.5(a). | A live `subscription.schema` pins the pointer (`embedded.go:558` → `:563`, comment: "*stable pointer for the sub's life*"). Rationale for dropping the upgrade: after §3.3, **every** registration path carries the full declaration (`parseEmbeddedSchema` copies `tenantCol`/`adminShow` through), and the only bare-`{Name}` producer (`console_data.go:84`) is deleted — so there is no weaker-first case left to upgrade from. |
| **M-6** | `ErrScopeViolation` rendered identically to "empty collection". | `console.data.scope-violation` added to §4.5, and the tab gets a **distinct** UI state (§6.3). Deny still renders neutral-empty; a scope violation is an internal-consistency failure and discloses nothing about other tenants. | One non-conforming row otherwise makes a collection permanently unbrowsable with no operator-visible reason. |
| **M-7** | The PK was unconditionally disclosed under `DiscloseDeclared`, while §2.10 justified opt-in because "*keys are frequently emails*". | Under `DiscloseDeclared` the PK renders only when named in `adminShow`; otherwise the row is addressed by an **opaque per-boot row handle** (`HMAC(perBootKey, coll‖key)`), resolved back to the real key inside `consoledata`. §5.2. | Same contradiction the design used to justify opt-in. Handle mechanism is the one already established for SQL sources (M11). |
| **M-8** | §2.5's headline implied `consoledata` **eliminates** the trust boundary. | The package doc now says plainly: what was removed is **per-call fabrication**; the resolver's installation is **relocated**, not eliminated (`Source` is exported; `rt` both implements and installs it). §2.5. | Honest-guarantee rule; the same standard §5.3 already applies to the disclosure claim. |
| **M-9** | §1.4's W1 write gate rested on the app-forgeable tenant column with no note. | §1.4 + §3.1 state that documenting the boundary suffices for **5e-1** and **does not** for **5e-2** (tenant A poisons `tenant=B`, B's operator is shown it, W1 authorizes B to write it). W1 gains a **durable-tenant prerequisite**. The tenant column's **value is rendered in every row** so the operator sees what the filter matched. | `bluedb/txn.go:78-81`, `engine.go:112-130` — the engine tag is never durably written, by design. |
| **minor** | — | `skyenv` gets a test table incl. whitespace cases; the two production notions are unified and the `SKY_ADMIN_TOKEN` row corrected; `SKY_CONSOLE_DATA=all` added to §2.10 with **unknown ⇒ off**; `ScanPage` gains a rows-examined budget + partial-page signal; `ORDER BY` uses the **full** introspected column list and refuses empty introspection; M5's low-entropy blind spot is a **named non-goal**. | See §2.13, §2.10, §3.5(c), §4.2, §2.12. |

**Explicitly kept from v2** (the grill confirmed these sound; nothing below is re-litigated):
HKDF mode-binding + the in-payload `Mode` check (§2.4), the absence of a v3→v2 replay path
(no legacy fallback), the M4 distinct-claim-key closure (§2.11), logout eviction *not* being a
DoS, the `live_reactive_hooks.go` build-gate seam (§2.7), and v1's B3 / B4 / B7-SQL / M2 / M7 /
M10–M13 closures.

**Two grill findings pushed back on, with evidence** — the fixes still ship, but the stated
mechanism is corrected:

1. **M-5's "`indexerFn` (`:112`) and `collResolver` (`:102`) pin a schema pointer" is false.**
   Both call `b.schemaByName(...)` on **every invocation** (`embedded.go:100-104` and
   `:110-118` — read verbatim), so neither can hold a stale pointer. The *only* long-lived pin
   is `WatchTenant`'s `cs := b.schemaByName(coll.Name)` (`:558`) stored into
   `subscription.schema` (`:563`). The finding's conclusion (an upgrade breaks a live
   subscription) is therefore **correct**, but for one site, not three — and that is why the
   §3.5(a) fix targets pointer **immutability** rather than per-call re-resolution.
2. **"Two notions of production ⇒ §2.8's `SKY_ADMIN_TOKEN` row is wrong" is a latent, not a
   live, divergence.** Both shipped entry points set the atomic from the same function —
   `SetProductionMode(productionFromEnv())` at `rt.go:8708` and `live.go:3812` — so
   `isProductionMode()` and `productionFromEnv()` **agree in every shipped path**. They can
   diverge only for an embedder that calls neither, or for a test that mutates `ENV` after
   startup. The row is nevertheless corrected (it is `isProductionMode()`, the startup
   snapshot, that gates `hasAdminAuth`) and the two are unified behind `skyenv` (§2.13), because
   a latent divergence in a security gate is still a defect.

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
| Row **detail** — the *post-filtered* field set, field by field (never the stored bytes — M-4) | §3.7, §6.3 |
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
| W1 | A **write decision** distinct from the read decision — `Decision.MayWrite()`, false unless `SKY_CONSOLE_DATA=readwrite` **and** the principal is verified **and** (scoped ⇒ the row's tenant column equals the verified tenant *both before and after* the edit). **Blocked on a DURABLE verified tenant — see M-9 below.** | §2.5's `Decide()` gains a second, independently-gated outcome; the durable tenant is §3.1's Phase-6 item |
| W2 | **Session-store collections are refused.** A raw row write corrupts the gob frame (`live_store.go` `storableSession` / `decodeSession:1391`) | a denylist of engine-owned collection names inside `consoledata` |
| W3 | The **`Generated` column contract** is preserved — `CollSchema.Generated` (`bluedb/backend.go:153`), refilled by `fillGenerated` (`embedded.go:637`), never client-supplied | write goes through `PutTenant`, never `blindPut` |
| W4 | The write carries the **verified** tenant: `PutTenant(coll, key, row, cols, verifiedTenant)` (`embedded.go:149`), so a scoped admin cannot write into another tenant | §2.6 |
| W5 | CSRF: the write is a Sky.Live event on the console's own session (no cross-site POST reaches a kernel), plus a per-session action token | §2.3's reconciled session |
| W6 | **Audit**: `console.data.write` with before/after column names (never values) | §4.5 |
| W7 | A **scalar-only** form: relations / nested records / enum-choices map to a JSON blob a generic form cannot structure — declared limit, not a bug | `clean-slate-architecture.md:930-932` |

**M-9 — W1's premise is weaker than the read plane's, and this must not be glossed.** The read
filter compares an **application-written** column (§3.1). For a READ that is a *view* filter and
the exposure is bounded: an operator sees a row someone else mislabelled. For a **WRITE** it is
an **authorization** input:

> Tenant A's application code writes a row whose `tenant` column says `"B"`. B's operator is
> shown that row (read: a poisoned view). Under W1, B's operator is now **authorized to write
> it** — because W1's "the row's tenant column equals the verified tenant" test consults the
> forged value. A tenant can therefore *hand* another tenant's operator write authority over a
> row it controls.

⇒ **Documenting the boundary suffices for 5e-1. It does NOT suffice for 5e-2.** W1 is
therefore gated on the **durable verified tenant** named in §3.1 (persist `CommitReq.Tenant`
into the MVCC value header at `committer.go:152/318` and filter on *that*) — a Phase-6 engine
item, not a 5e-2 rider. A 5e-2 delivery that implements W1 against the application-written
column is **not** fail-closed and must not ship. This is stated here, in §3.1, and in §11.

**Mitigation that ships in 5e-1:** the tenant column's **value is rendered in every row**
(§3.1, §5.2) — forced into the field/SELECT set even when `adminShow` omits it — so an operator
can see *what the filter matched* rather than trusting that it matched something.

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

**B-1 — the second, INDEPENDENT path the v2 draft missed, and the prerequisite it creates.**
The attack above needs a page load. There is a path that needs none:

> `handleEvent` takes the session id from the request **BODY** — `SessionID string
> \`json:"sessionId"\`` (`live.go:4353-4355`) — and does `sess, ok :=
> app.store.Get(req.SessionID)` (`:4398`) with **no comparison against the sid cookie**.
> `handleSSE` **does** read the cookie (`live.go:6142-6148`). So `/_sky/event` accepts *any*
> session id from *any* authenticated caller. The sid is **not confidential**: it is templated
> straight into the page as `var __skySid = %q` (`live.go:6953`).
>
> **CSRF does not close this.** The guard is a **double-submit cookie** pair
> (`CSRFMiddleware`, `live.go:3831`; `X-Sky-Csrf` from `__skyCsrfToken`, `:7724`) — every
> principal on the browser holds a valid pair of their own. Double-submit proves *same origin*,
> never *same session*.

This is a **direct cross-principal path**, not a timing window: a ConsoleGate-passing principal
who learns another operator's sid drives that operator's session and reads their tenant, without
ever touching `handleInitial`. v2's §11.1-5 and §2.9 both mis-framed it, and both are corrected
here.

> **PREREQUISITE (commit C0, §8).** `handleEvent` MUST bind the body's `sessionId` to the
> request's sid cookie — `if req.SessionID != cookieSid { 404 + X-Sky-Live:1 +
> X-Sky-Status:session-lost }` — before any of the work below lands. This is shipped as its own
> **security fix**, independently reviewed and independently tested, and it is **not** a
> deliverable of this design; this design **depends** on it. The two fixes are orthogonal: C0
> binds the *transport* (which session may this request address), §2.3 binds the *principal*
> (which operator may this session serve). Neither substitutes for the other, and this design
> therefore runs the principal check in `handleEvent`/`handleSSE` **as well** (part 5 below),
> never relying on `handleInitial` alone.

**The fix — five parts.**

**(1) A principal fingerprint over a DECLARED claim subset.** New file
`runtime-go/rt/live_principal_reconcile.go` (package `rt`, bluedb-free):

```go
// principalFingerprint is a stable digest of the authorization-relevant
// principal. It covers Subject, Email and — CRITICALLY — only the claims named
// in authzRelevantClaims, sorted. It does NOT cover the whole Claims map.
//
// M-1: IdentityContextKey is EXPORTED and generic (session_identity.go:36-43)
// and the hub stamps it on every gated request (hub/app_auth.go:127-129). Any
// gate that puts a volatile value in Claims (exp / iat / nonce / a request id)
// would change a whole-map fingerprint on EVERY request — a rotation storm that
// wipes the Model on every page load of a shipped, working app. Sorting fixes
// claim ORDER; it does nothing about VOLATILITY. Hashing a declared subset does.
//
// A claim that is not in this set cannot influence any authorization decision in
// this codebase — the set and consoledata's claim readers are pinned equal by
// TestAuthzClaimSubsetMatchesConsoledataReaders.
var authzRelevantClaims = []string{
	claimDataTenant,      // "consoleDataTenant"      — consoledata.Decide
	claimDataSuperAdmin,  // "consoleDataSuperAdmin"  — consoledata.Decide
	"tenant",             // hub_bridge.go:539-549 / :561-572 (telemetry scope)
}

func principalFingerprint(id ConsoleIdentity, valid bool) [32]byte

// reconcileSessionPrincipal runs INSIDE handleInitial's per-session lock, after
// sessionIDNamed + locker.Lock + store.Get (grill M-2 — see part 2). On a
// mismatch it marks the resident session DEAD, deletes it, and returns a freshly
// minted sid so the caller re-locks and takes the fresh-mint path (rotation — the
// sid cookie is Path=/ and NOT Secure, live.go:6402-6427, so it is plantable on
// plain HTTP; reusing it across principals would be a fixation hole).
//
// Sessions with no identity on either side are left completely untouched: an app
// that never stamps IdentityContextKey never enters this code path at all.
func reconcileSessionPrincipal(app *liveApp, w http.ResponseWriter, r *http.Request,
	sid string, sess *liveSession, existing bool) (newSid string, rotated bool)
```

The exact rule (the table IS the specification):

| request identity | session identity | action | why |
|---|---|---|---|
| absent | absent | **reuse** | an app that never stamps an identity — zero behaviour change |
| present | absent | **re-stamp in place** | upgrade; a session that was never granted anything cannot be downgraded by gaining an identity |
| **absent** | **present** | **rotate + mint** — *but only when this app has EVER stamped an identity* (see below) | Alice's auth cookie is gone; her privileged session must not keep serving |
| present | present, FP equal | **reuse** | the common case |
| present | present, **FP differs** | **rotate + mint** | the Bob leg |

> **M-1, row 3 re-examined.** As written in v2, row 3 regresses an app that stamps an identity
> on *some* routes only (a public landing page + a gated dashboard sharing one Live session):
> every public-route request would rotate the session and wipe the Model. The rule is therefore
> qualified by a **per-app latch**: `liveApp.everStampedIdentity atomic.Bool`, set the first
> time `IdentityFromContext` returns `ok` for this app. Row 3 fires only when the latch is
> **set** *and* the session carries an identity — i.e. this app is identity-bearing and this
> particular session *was* privileged. For an app that has never stamped anything the latch is
> false and rows 1–5 collapse to "reuse", which is the same no-op guarantee, stated correctly.
> Pinned by `TestReconcile_MixedRouteAppDoesNotRotateOnPublicRoute` (§7.4).

**(2) `handleInitial` edit — M-2: under the lock, and the delete is not resurrectable.**
v2 inserted the call at `live.go:4043`, *before* `sid := sessionIDNamed(...)`. That is outside
the per-session mutex — `app.locker.Lock(sid)` is `live.go:4044` — so a concurrent `handleEvent`
(`:4435`, `:4575`), `runPerform` (`:5345`) or SSE (`:6301`) can `app.store.Set` the session back
**after** the delete. Both halves are fixed:

```go
	sid := sessionIDNamed(r, w, app.sessionTTL, app.cookieName)   // :4043, unchanged
	app.locker.Lock(sid)                                          // :4044, unchanged
	defer func() { app.locker.Unlock(sid) }()                     // was: defer app.locker.Unlock(sid)
	                                                              // ← closure so a rotation
	                                                              //   unlocks the NEW sid
	sess, existing := app.store.Get(sid)                          // :4047, unchanged

	// v0.19 goal-#5 / grill B1+M-2 — a Live session may only be resumed for the
	// principal it was minted for. Runs UNDER the lock, on the session we just
	// read. See live_principal_reconcile.go.
	if newSid, rotated := reconcileSessionPrincipal(app, w, r, sid, sess, existing); rotated {
		app.locker.Unlock(sid)
		sid = newSid
		app.locker.Lock(sid)
		sess, existing = app.store.Get(sid)   // empty ⇒ the fresh-mint path below
	}
```

**The dead-session latch (M-2's second half).** `liveSession` gains `dead atomic.Bool`.
`reconcileSessionPrincipal` sets it **before** `store.Delete`, under the lock. Every
`app.store.Set(...)` site — **all five**: `live.go:4213, 4435, 4575, 5345, 6301` — is routed
through one funnel:

```go
// persistSession is the ONE place a live session is written back to the store.
// It refuses a session that reconciliation has evicted, so a request already in
// flight when the principal changed cannot resurrect a revoked session.
// Precedent: this branch's own persist-before-ack funnel (commit 9ad00daf) —
// the same "dissolve the N-site band-aid into one seam" move.
func (app *liveApp) persistSession(id string, sess *liveSession) bool {
	if sess == nil || sess.dead.Load() {
		return false
	}
	app.store.Set(id, sess)
	return true
}
```

Pinned by `TestReconcile_ConcurrentEventCannotResurrectEvictedSession` and
`TestPersistSessionFunnelCoversEveryStoreSet` (a source-grep tripwire: zero `app.store.Set(`
outside `persistSession`), §7.4.

**(3) The identity stamp is hoisted out of the mint-only branch.** `:4100-4110` becomes:

```go
	// Hoisted out of the mint-only branch (grill B1). Reconciliation above
	// guarantees the identity on r matches the session's, or that the session
	// was rotated — so this can only ever ADD an identity, never swap one.
	if id, ok := IdentityFromContext(r.Context()); ok {
		sess.identity = id
		sess.identityValid = true
		app.everStampedIdentity.Store(true)
	}
```

**(4) Logout and login clear ALL THREE cookies AND evict the session server-side.**
`console.go:346` already binds the sub-app handle (`app := MountLiveSubAppInProcessWithGate(...)`;
`_ = app` at `:347`) — stash it in a package var so the auth routes can reach it.

- `/_sky/console/_logout` (`console_auth_v2.go:874-877`): `clearConsoleV2Cookie(w)` **plus**
  `clearConsoleV1Cookie(w)` (`sky_console_sid`) **plus**
  `clearSubAppSessionCookie(w, "sky_sky_console_sid")` **plus**
  `consoleLiveApp.markDeadAndDelete(sid)` for the sid the request carries.
- **B-5 — the method contract, corrected.** v2 made the route `POST`-only in C2 while the
  `View.sky` edit landed in C12. `console.go:315` injects `SKY_CONSOLE_LOGOUT_URL` and
  `View.sky:142-148` renders it with `Ui.link` — **a plain GET anchor** — so v2 shipped a
  window in which Sign-out returned **405**. Instead:
  - the route accepts **GET and POST from C2 onward**, and performs the **full** clear+evict on
    both (a forced logout is a nuisance-tier annoyance; leaving the previous operator's
    privileged session alive is strictly worse);
  - a **GET** is additionally gated by a same-site check —
    `Sec-Fetch-Site ∈ {same-origin, none}` when the header is present, else an `Origin`/`Referer`
    host match, else refuse `405`. The shipped `<a>` link satisfies it; a cross-site
    `<img src=…/_logout>` does not;
  - **C12** (which already performs the regeneration dance) converts the UI to a POST form, and
    **C13** may then flip the route to POST-only with no user-visible window.
- `handleConsoleLogin` success path (`:707-712`): clear the sub-app sid cookie **and**
  `consoleLiveApp.markDeadAndDelete(oldSid)` for the sid the *login request* carries.
  **B-1(c):** v2 cleared only the cookie, which defeats part (1) — with the cookie gone, the
  next `handleInitial` never presents the old sid, so reconciliation never sees it and the
  **previous operator's session survives to TTL**, still addressable by anyone holding its sid
  (see the `handleEvent` path above). Clearing the cookie is the C2 half; the server-side
  eviction is the half that actually revokes. Pinned by
  `TestLogin_EvictsPreviousOperatorSessionServerSide` (§7.4).

**(5) Defence in depth — the check ALSO runs on `handleEvent` and `handleSSE`.** Independent of
C0, and independent of `handleInitial`:

```go
// In handleEvent (after C0's cookie binding) and handleSSE, immediately after
// app.store.Get succeeds and the per-session lock is held:
if principalChanged(sess, r) {
	sess.dead.Store(true)
	app.store.Delete(sid)
	w.Header().Set("X-Sky-Live", "1")
	w.Header().Set("X-Sky-Status", "session-lost")
	http.Error(w, "session not found", 404)
	return
}
```

**Mechanism citation — why this needs NO client change** (v2 filed it as a follow-on precisely
because it feared one): `X-Sky-Status: session-lost` is already the *deterministic hard-reload
signal*, handled by the shipped client at `live.go:7752-7760` with `window.location.reload()`.
The reload lands on `handleInitial`, where part (1)'s rotation is the tested path. So the
revocation completes through machinery that is already in production, and the 401-handling
question v2 raised does not arise. `handleSSE` already emits the identical header for a lost
session (`live.go:6160`).

**(6) Session TTL cap.** The console sub-app's Live session TTL is capped at
`min(configured, consoleAuthCookieV2MaxAge)` and defaults to **30 minutes**. With (5) in place
this is a backstop, not the primary bound (§2.9).

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
// WHAT THIS PACKAGE ACTUALLY REMOVES (M-8, stated precisely — do not overclaim):
// it removes PER-CALL FABRICATION of trust. Before it, any rt caller could write
// Decide(false, false, false, "") and receive allowed+unscoped, once per call
// site, invisibly. After it, Decide takes no arguments; the environment tier is
// resolved here; the principal comes from a Source installed exactly once at
// process start; and ReadRows/BrowseSQL build their own predicates from the
// unexported `tenant` field, which has no exported accessor.
//
// WHAT IS RELOCATED, NOT ELIMINATED: `Source` is an exported interface, and rt
// both implements it (rtConsoleSource) and installs it (Bind, from an init() in
// a file whose ordering is filename-derived). A hostile or buggy rt could install
// a different resolver. What that costs an attacker is the difference between
// "edit one call site" and "replace the process-wide trust resolver in a file
// that exists for that purpose and is named in the risk register" — a real
// reduction in accident surface, and a real reduction in review surface, but NOT
// a cryptographic or type-system guarantee.
//
// ALSO NOT A GUARANTEE: this package cannot stop other rt code from reading the
// underlying store directly. The application's own data path legitimately does
// exactly that. What is prevented is an ADMIN-PLANE read that forgets, drops, or
// fabricates the scope. (v1's doc comment claimed a stronger, false property;
// same-package construction always remains possible in Go.)
package consoledata

// Source is the process's binding to the live console session. consoledata
// NEVER accepts booleans describing trust — a caller cannot assert "verified",
// it can only hand over the resolver.
type Source interface {
	// CurrentPrincipal returns the cryptographically-established principal for
	// the calling goroutine's live console session, or ok=false.
	CurrentPrincipal() (subject string, claims map[string]string, ok bool)
}

// Bind installs the process-wide Source. FIRST CALL WINS: it is sync.Once-
// guarded, and any later call is a no-op that logs console.data.bind-duplicate
// at error level with both implementations' concrete types.
//
// B-4: v2 specified a panic here AND showed Bind at two call sites (§3.9's code
// block and its prose). Two Bind calls in one package is a guaranteed init()
// panic for every Persist app with a console — an availability failure, shipped,
// for a property a no-op already provides. The security property is that a
// second Bind cannot SUBSTITUTE the source; first-wins delivers exactly that.
// There is now exactly ONE Bind site in the tree (rt/data_kernel_hooks.go), and
// TestExactlyOneBindCallSite is a source-grep tripwire on it.
func Bind(s Source)

// BoundSourceType reports the concrete type of the installed Source, or "" when
// unbound. Test-only observability for TestBind_SecondCallIsIgnoredAndLogged.
func BoundSourceType() string

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

The environment tier is resolved inside the package, via the new leaf package
**`runtime-go/rt/skyenv/`** (§2.13) — one definition, so nothing can drift.

### 2.6 The decision function — B3 fixed, and B-2's positive arming

**B-2 — the posture must not pivot on a forgettable variable.** v2's `Decide` consulted
`skyenv.IsProduction()`, which is `productionFromEnv()` (`observability.go:314-324`) and returns
**false when both `ENV` and `SKY_ENV` are unset**. In that one state — the state you land in by
*doing nothing* — four independent arms all opened at once: the console mounted dev-open with no
auth (`console_auth_v2.go:443-452`, the `isProductionMode()` guard also false), `SKY_CONSOLE_DATA`
unset meant *on*, `!ok && !prod` meant allowed/unscoped/`DiscloseAll`, and §2.12's weak-key
refusal was itself gated on production so it did not fire. Net effect of forgetting one variable:
**every row of every collection of every tenant, unredacted and unauthenticated** — precisely the
misconfiguration class §2.1 G-e cites as realistic.

> **Re-architecture: the data plane arms on POSITIVE signals only. There is no state of the
> environment in which "forgot to configure it" yields disclosure.**
>
> **P1 — explicit enablement, in EVERY environment.** `SKY_CONSOLE_DATA` must be explicitly
> `on` (or `all`). Unset ⇒ **off**, in dev exactly as in production (§2.10). This alone makes
> the B-2 scenario inert: forget `ENV` and the plane is simply not armed.
>
> **P2 — the unauthenticated arm requires an explicit DEV marker.** The
> allowed/unscoped/`DiscloseAll`-without-a-principal arm is gated on
> `skyenv.IsExplicitDev()` — `ENV` or `SKY_ENV` **set** and matching a dev marker. **Unset is
> the UNKNOWN tier and the data plane treats UNKNOWN as production** (§2.13). So the second
> mistake (setting `SKY_CONSOLE_DATA=on` in a container image and forgetting `ENV`) is also
> fail-closed.
>
> **Why this option, and not the other two the grill offered.** A loopback-only listener bind
> was considered and rejected: it is unobservable at kernel time (the decision is taken on a
> `Cmd.perform` goroutine, not in an HTTP handler), it would require plumbing `RemoteAddr`
> through `liveSession` — a second `live.go` change with blast radius on top of §2.3's — and it
> is wrong under every container/reverse-proxy topology, which is the exact class
> `productionFromEnv`'s doc comment says the previous addr-based heuristic broke
> (`observability.go:308-312`). Requiring a positive `SKY_CONSOLE_DATA` plus a positive dev
> marker needs **zero** request plumbing and is checkable at a glance.
>
> **Blast radius on the rest of the console: none.** `skyenv.IsProduction()` keeps
> `productionFromEnv`'s exact semantics (unset ⇒ false) for the six telemetry tabs, the metrics
> gate and `resolveConsoleAuthMode`. Only the **data plane** consults the stricter
> `IsExplicitDev()` / `DataTier()`. Zero-config dev keeps working for everything that is not
> an arbitrary-application-data disclosure.

```go
func Decide() Decision {
	// P1 — positive enablement, checked first, in every environment.
	if !ReadsEnabled() {                       // SKY_CONSOLE_DATA ∈ {on, all}; §2.10
		return Decision{reason: "data plane not enabled (set SKY_CONSOLE_DATA=on)"}
	}
	// B-4 — an unbound Source denies EVERYWHERE, not just in production. v2
	// gated this on prod, so an unbound source in dev fell through to
	// allowed/unscoped/DiscloseAll — the same fail-open shape as B-2.
	src := currentSource()
	if src == nil {
		return Decision{reason: "no consoledata.Source is bound"}
	}
	devTier := skyenv.IsExplicitDev()          // P2 — UNSET is NOT dev; §2.13

	sub, claims, ok := src.CurrentPrincipal()
	if !ok {
		if !devTier {
			return Decision{reason: "no verified console identity"}
		}
		return Decision{allowed: true, scoped: false,
			disclose: discloseFor(devTier), reason: "explicit-dev console — no identity, unscoped"}
	}

	// ── B3: a VERIFIED TENANT CLAIM SCOPES, IN EVERY ENVIRONMENT. This branch
	// is deliberately ABOVE the dev shortcut. v1 kept the shortcut first, so a
	// correctly-configured multi-tenant deployment that merely forgot to set ENV
	// handed a scoped operator every tenant's rows (console_data.go:33-35 +
	// observability.go:314-324). It is also the only way an operator can TEST
	// their scoping without pretending to be in production.
	if t := claims[claimDataTenant]; t != "" {
		return Decision{allowed: true, scoped: true, tenant: t,
			disclose: discloseFor(devTier), reason: "tenant-scoped"}
	}
	if truthy(claims[claimDataSuperAdmin]) {
		return Decision{allowed: true, scoped: false,
			disclose: discloseFor(devTier), reason: "platform super-admin"}
	}
	if devTier {
		return Decision{allowed: true, scoped: false,
			disclose: discloseFor(devTier), reason: "explicit-dev console — verified, no data claim"}
	}
	// verified, no data-tenant claim, no super-admin marker → fail-closed (B2).
	_ = sub
	return Decision{reason: "no data-tenant claim and no super-admin marker — " +
		"refusing all-tenant access (fail-closed)"}
}

// discloseFor returns DiscloseAll ONLY when the operator has BOTH declared an
// explicit dev tier AND asked for the firehose with SKY_CONSOLE_DATA=all.
// SKY_CONSOLE_DATA=on redacts to the declared adminShow set even in dev — a
// forgotten `all` therefore under-discloses, never over-discloses.
func discloseFor(devTier bool) Disclosure {
	if devTier && discloseAllRequested() {
		return DiscloseAll
	}
	return DiscloseDeclared
}
```

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

### 2.8 The full decision table, per auth mode — **re-derived for v2.1**

Two axes replace v2's single `prod` column, because B-2 showed one boolean cannot express the
posture:

- **`SKY_CONSOLE_AUTH` mode** — unchanged, resolved by `resolveConsoleAuthMode`
  (`console_auth_v2.go:122-146`) from `productionFromEnv()`. **Not** re-derived: the console's
  own mount contract is out of scope for this design and its zero-config dev behaviour is kept.
- **data tier** — `skyenv.DataTier()` (§2.13): `Dev` **only** when `ENV`/`SKY_ENV` is explicitly
  a dev marker; `Prod` for every other value **and for UNSET**.

`SKY_CONSOLE_DATA` (§2.10): unset ⇒ **OFF, in every tier**. Parsing is
`strings.ToLower(strings.TrimSpace(...))` — exp's matching is case-sensitive, so
`SKY_CONSOLE_DATA=OFF` is a **no-op** there (`exp/bluedb:console_data.go:29-40`); that defect is
not ported. Unknown values ⇒ **off** (§2.10).

**Gate 0, applied to every row below**: `SKY_CONSOLE_DATA ∈ {on, all}` **and** a `Source` is
bound **and** (§2.12) the cookie signing key derives from an explicit env secret. Any one
missing ⇒ **DENY, in every tier**, with `console.data.disabled` naming which one.

| `SKY_CONSOLE_AUTH` | `ENV`/`SKY_ENV` | data tier | principal source | data-tenant claim | super claim | **Decision** | disclosure | operator must set |
|---|---|---|---|---|---|---|---|---|
| `off` | any | — | — | — | — | **console not mounted** (`console.go:293-296`) | — | — |
| unset | prod-ish (set, non-dev) | Prod | — | — | — | **console not mounted** (`consoleAuthModeUnsetProd`, `console.go:297-300`) | — | — |
| unset | **unset** | **Prod** | dev-open, no principal | — | — | **DENY** — *B-2's exact state; v2 said ALLOW·unscoped·All* | — | — |
| unset | `dev`/`development`/`local` | Dev | dev-open, no principal | — | — | **ALLOW · unscoped** | Declared, or All with `SKY_CONSOLE_DATA=all` | `SKY_CONSOLE_DATA=on` + `ENV=dev` |
| `token` | **unset** | **Prod** | v3 cookie | env→cookie | env→cookie | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | `SKY_CONSOLE_TOKEN` + `SKY_CONSOLE_DATA=on` + one data env |
| `token` | `dev`… | Dev | v3 cookie | env→cookie | env→cookie | tenant ⇒ **SCOPED**; super ⇒ unscoped; else **ALLOW · unscoped** | Declared / All | `SKY_CONSOLE_TOKEN` + `SKY_CONSOLE_DATA=on` |
| `token` | prod-ish | Prod | v3 cookie (sub `token-auth`, `:710`) | `SKY_CONSOLE_DATA_TENANT` | `SKY_CONSOLE_DATA_SUPERADMIN` | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | `SKY_CONSOLE_TOKEN` + `SKY_CONSOLE_DATA=on` + one data env |
| `app` | **unset** | **Prod** | callback / v3 cookie | `Claims["consoleDataTenant"]` | `Claims["consoleDataSuperAdmin"]` | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | callback claim + `SKY_CONSOLE_DATA=on` + **`SKY_CONSOLE_COOKIE_SECRET`** (B-3) |
| `app` | `dev`… | Dev | callback / v3 cookie | `Claims["consoleDataTenant"]` | `Claims["consoleDataSuperAdmin"]` | tenant ⇒ **SCOPED**; else **ALLOW · unscoped** | Declared / All | `consoleAuth` callback + `SKY_CONSOLE_DATA=on` |
| `app` | prod-ish | Prod | callback / v3 cookie | `Claims["consoleDataTenant"]` | `Claims["consoleDataSuperAdmin"]` | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | callback claim + `SKY_CONSOLE_DATA=on` + **`SKY_CONSOLE_COOKIE_SECRET`** (B-3) |
| any | any | any | **internal token** (`SKY_CONSOLE_INTERNAL_TOKEN`) | — | — | **DENY** — it is never a data principal, in **any** tier | — | — (by design) |
| unset | prod-ish | Prod | `SKY_ADMIN_TOKEN` / `SKY_METRICS_TOKEN` bearer via `hasAdminAuth` | env | env | tenant ⇒ **SCOPED**; else super ⇒ **UNSCOPED**; else **DENY** | Declared | `SKY_METRICS_TOKEN` + `SKY_CONSOLE_TOKEN`/`SKY_CONSOLE_COOKIE_SECRET` + `SKY_CONSOLE_DATA=on` + a data env |
| **app-mode cookie presented to a token-mode instance** | any | any | — | — | — | **MAC FAILS** (mode-keyed HKDF, §2.4a) → re-auth | — | — |
| **pre-v3 cookie after upgrade** | any | any | — | — | — | **rejected at step 4** → one re-login | — | — |

**Corrections to v2's matrix, called out explicitly:**

1. Rows 3, 5 and 8 (`ENV` **unset**) flipped from a permissive outcome to the **production**
   outcome. That is the whole of B-2.
2. Disclosure is `Declared` **everywhere** unless the operator explicitly asks for
   `SKY_CONSOLE_DATA=all` **and** is in the explicit-Dev tier. v2 wrote `All` for every
   non-production row.
3. The `SKY_ADMIN_TOKEN` row is corrected. v2 wrote "`SKY_ADMIN_TOKEN` bearer / prod=true / per
   matrix". The actual gate is `hasAdminAuth(r)` inside the **dev-open** arm
   (`console_auth_v2.go:443-452`), reached only when `SKY_CONSOLE_AUTH` is **unset** and
   `isProductionMode()` — the **startup snapshot** atomic (`observability.go:280-293`), not a
   live `productionFromEnv()` read. Both are set from the same source at both shipped entry
   points (`rt.go:8708`, `live.go:3812`), so they agree in every shipped path; §2.13 unifies
   them so they cannot diverge for an embedder or a test. The env var is `SKY_METRICS_TOKEN`
   (that is what the 401 hint names, `:448`); `SKY_ADMIN_TOKEN` alone was wrong.
4. This bearer principal carries **no cookie**, so it has no v3 payload and therefore no claims
   of its own; its claims come from `operatorClaims()` at gate time, exactly like token mode.
   Without a data env it is **DENY**, not "per matrix".

**Why the operator envs are token/admin-token only.** In app mode the callback's claims are
the sole authority. If `SKY_CONSOLE_DATA_SUPERADMIN=1` also applied in app mode it would
promote **every** callback-approved end user to platform-wide read — a privilege escalation
across the app's own users. Token mode has exactly one shared operator (subject
`token-auth`, `console_auth_v2.go:710`), so a deploy-time declaration *is* that operator's
identity. Pinned by `TestAppMode_IgnoresOperatorEnvClaims` (§7.3).

### 2.9 Revocation window — **corrected**: the event path is closed, not deferred

**v2 got this wrong and the correction is structural.** v2 framed `/_sky/event` as a
"revocation timing window" and filed the hardening as a follow-on. It is not a window — see
§2.3's B-1 box: `handleEvent` reads the session id from the request **body** (`live.go:4353-4355`)
and never compares it to the sid cookie (`:4398`), and the sid is templated into the page
(`live.go:6953`). That is a direct path, and a "window" framing would have shipped it.

**What closes it, in v2.1:**

| Leg | Mechanism | Where |
|---|---|---|
| The transport binding | `handleEvent` compares the body's `sessionId` to the sid **cookie**; mismatch ⇒ `404` + `X-Sky-Status: session-lost` | **C0 — a PREREQUISITE security fix shipped separately** (§8) |
| The principal binding on the event path | `principalChanged(sess, r)` ⇒ mark dead, delete, `404` + `session-lost` | §2.3-5, this design |
| The principal binding on the SSE path | identical check after `store.Get` (`handleSSE` already reads the cookie, `live.go:6142-6148`) | §2.3-5 |
| The principal binding on page load | rotation under the per-session lock | §2.3-1/2 |
| Client recovery | `X-Sky-Status: session-lost` → `window.location.reload()`, **already shipped** (`live.go:7752-7760`) | no client change |

**The residual window, now genuinely small and stated honestly:** an operator whose *entitlement*
changes without their *identity* changing — e.g. `consoleDataTenant` is revoked in the identity
provider, but the v3 cookie minted before the revocation still carries the old claim. Claims are
frozen into the cookie (`consoleAuthCookieV2MaxAge` = 4 h, `:86`) and onto the session at mint;
nothing re-consults the provider mid-life. Bounds and levers, all shipped: the console session
TTL cap of **30 min** (§2.3-6); the 4 h cookie ceiling; immediate global lockout via
`SKY_CONSOLE_AUTH=off` or `SKY_CONSOLE_DATA=off`. Documented in
`docs/v0.16.x-console/EMBEDDED.md`. Live per-request re-authorization would require calling the
app's `consoleAuth` callback on every event — a latency and blast-radius change to every gated
Sky.Live app — and is explicitly **not** attempted here.

### 2.10 M3 + B-2 — `SKY_CONSOLE_DATA` is opt-IN in **every** environment

**Decision (v2.1): opt-in everywhere. Unset ⇒ OFF, in dev exactly as in production.**
v2 said "opt-in in production, on by default in dev", which is the same sentence as "on by
default whenever `productionFromEnv()` returns false" — and that function returns false when
`ENV` is simply **unset** (`observability.go:314-324`). B-2 is the consequence.

Two justifications, both mechanical:

1. **The upgrade path.** Apps already return `Claims={"tenant":t}` for telemetry filtering; if
   the data plane were default-on, upgrading the compiler would newly disclose tenant *t*'s
   application records — collection names and primary keys at minimum (keys are frequently
   emails). A plane that discloses **arbitrary application data** must not switch itself on
   during a version bump. exp had this right (`dataReadsEnabled`,
   `exp/bluedb:console_data.go:42-49`).
2. **B-2.** A default-on-in-"dev" plane inherits every weakness of the "is this dev?" question.
   Making the answer irrelevant is cheaper and more auditable than making it reliable.

**DX cost, and why it is acceptable:** a developer must type one env var to browse their own
data. `sky run` / `sky watch` MAY export `SKY_CONSOLE_DATA=on` into the child process (and log
that it did) — a positive signal that is dev-only *by construction*, because those verbs are
developer commands and are never a production entrypoint. That affordance is a **C15 docs +
CLI** item, not a default in the runtime, and the runtime's behaviour with the variable unset is
identical either way.

**Values** (case-insensitive, `strings.ToLower(strings.TrimSpace(...))`):

| Value | Meaning |
|---|---|
| `off` · `0` · `false` | off |
| `on` · `readonly` · `ro` · `1` · `true` | **on**, disclosure `DiscloseDeclared` |
| `all` | on, and `DiscloseAll` — **but only in the explicit-Dev tier** (§2.6 `discloseFor`); in any other tier it behaves exactly as `on` and logs `console.data.disclose-all-ignored` |
| `readwrite` · `rw` | **rejected with a startup warn** in 5e-1 (no write path exists; silently accepting it would let an operator believe writes are gated when they are absent) — treated as **off** |
| **unset** | **off** |
| **anything else** | **off**, with a `console.data.disabled reason=unknown-value` warn naming the value. Never a permissive fallback. |

`all` was used by §5.2 in v2 but was missing from this list — fixed.

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

### 2.12 M5 + B-3 — no file-derived signing key outside an explicit dev tier

`deriveConsoleSigningKey` falls back to `ensureDevConsoleToken()` (`:256-268`), which reads a
**CWD-relative** `.sky/console-token`, accepts any `len(b) >= 32`, and on an unwritable CWD
returns a **different** fresh token to the key-derivation site (`:227`) than to the login
comparison site (`:700`). Its `randomDevToken` fallback on a `crypto/rand` failure is
`fmt.Sprintf("dev-fallback-%d-%d", os.Getpid(), time.Now().UnixNano())` (`:271-279`) — not a
secret. Post-redesign a forged cookie buys super-admin over every record.

**B-3 — v2's rule contradicted its own §2.8, and the contradiction is resolved here.**
`deriveConsoleSigningKey` falls back to `ensureDevConsoleToken()` whenever `SKY_CONSOLE_TOKEN`
is empty (`console_auth_v2.go:216-246`), and app mode's documented contract is verbatim
"*App mode reuses the same signing key for its post-callback session cookie (no second secret to
provision)*" (`:196-198`). v2's §2.8 nevertheless promised `app`+prod SCOPED/UNSCOPED needing
only the callback claim plus `SKY_CONSOLE_DATA=on`. **Both cannot hold**: in app mode with no
`SKY_CONSOLE_TOKEN`, v2's M5 rule kills the data plane, so those two matrix rows were
unreachable.

**Resolution — a dedicated cookie secret, accepted in every mode.**

```go
// consoleCookieSecret returns the EXPLICIT operator-provided key material for
// the console cookie, or ok=false when there is none.
//
// Accepted, in order: SKY_CONSOLE_COOKIE_SECRET, then SKY_CONSOLE_TOKEN. Both
// must be >= 32 bytes after TrimSpace. A file-derived or in-memory-random secret
// is NEVER "explicit".
//
// SKY_CONSOLE_COOKIE_SECRET exists so app mode keeps its "no second secret to
// provision" contract for the CONSOLE (the console still mounts and
// authenticates via the callback on a dev-derived key) while the DATA PLANE —
// and only the data plane — can demand real key material. Token mode may keep
// using SKY_CONSOLE_TOKEN as both password and key material, which is what it
// already does.
func consoleCookieSecret() (string, bool)
```

**Rule:** at `MountEmbeddedConsole` time, if `dataPlaneRequested && !skyenv.IsExplicitDev() &&
!consoleCookieSecret().ok` ⇒ **the data plane declines to arm** (loud
`console.data.disabled reason=weak-signing-key` naming **both** env vars). The console itself
still mounts — fail-closed on the privileged plane only, so a misconfiguration does not brick
observability.

Two changes from v2's wording, each closing a hole B-2 exposed:

- the gate is `!skyenv.IsExplicitDev()`, **not** `IsProduction()`. v2's version did not fire in
  the `ENV`-unset state — the exact state in which everything else also opened.
- `SKY_CONSOLE_COOKIE_SECRET` makes §2.8's `app` rows reachable; they now list it under
  "operator must set".

Additionally `randomDevToken`'s non-random `fmt.Sprintf("dev-fallback-%d-%d", …)` fallback
(`:271-279`) becomes a **hard error** rather than a guessable string.

**Named non-goal (minor):** this check distinguishes *explicit* from *derived*, and *length*
from *too short*. It does **not** estimate entropy — `SKY_CONSOLE_COOKIE_SECRET` set to 32
repeated characters passes. Entropy estimation on operator-supplied secrets is out of scope:
it produces false rejections on legitimately-formatted secrets (base64 of 24 random bytes has
low character diversity by some measures) and there is no threshold that is both meaningful and
non-surprising. The documented guidance is `openssl rand -base64 32`; the mechanical guarantee
is *explicit and ≥32 bytes*, and it is stated that way in `EMBEDDED.md` rather than implied to
be more.

### 2.13 `skyenv` — one definition of the environment, two questions

New leaf package **`runtime-go/rt/skyenv/`** (`sky-app/rt/skyenv`, ~40 lines, zero
dependencies). `rt.productionFromEnv()` (`observability.go:314-324`) becomes a one-line forward
so its existing callers are untouched. *Rejected alternative:* duplicate the logic in
`consoledata` plus a cross-package parity test — a parity test can only catch drift after it
happens; one definition cannot drift.

```go
package skyenv

type Tier uint8
const (
	TierUnknown Tier = iota // ENV and SKY_ENV are BOTH unset
	TierDev                 // explicitly dev / development / local
	TierProd                // explicitly anything else
)

// Which() reads ENV, then SKY_ENV, trimmed + lowercased.
func Which() Tier

// IsProduction preserves productionFromEnv's EXACT semantics — TierUnknown
// returns FALSE. Every existing caller (the metrics gate, resolveConsoleAuthMode,
// SetProductionMode) keeps its behaviour bit-for-bit. Do not "fix" this: the
// zero-config dev console depends on it and it is not the data plane's question.
func IsProduction() bool { return Which() == TierProd }

// IsExplicitDev is the DATA PLANE's question, and it is the opposite default:
// only an EXPLICIT dev marker counts. TierUnknown is NOT dev. This is the
// positive signal B-2 requires — there is no way to *forget* your way into it.
func IsExplicitDev() bool { return Which() == TierDev }

// DataTier collapses UNKNOWN into PROD for the admin data plane.
func DataTier() Tier { if Which() == TierDev { return TierDev }; return TierProd }
```

**The two production notions (minor), reconciled.** `productionFromEnv()` reads the environment
live; `isProductionMode()` (`observability.go:280-293`) reads an atomic snapshot. They agree in
every shipped path — **both** entry points do `SetProductionMode(productionFromEnv())`
(`rt.go:8708`, `live.go:3812`) — so the grill's "§2.8's `SKY_ADMIN_TOKEN` row is wrong" is a
*latent* divergence, not a live one. It is still a defect in a security gate, so: both forward
to `skyenv`, `SetProductionMode` keeps its snapshot role and is documented as
"*the startup snapshot of `skyenv.IsProduction()`; request-time gates read this, tier-time
decisions read `skyenv` directly*", and §7.3 adds
`TestProductionModeSnapshotMatchesSkyenv`.

**`strings.TrimSpace` is a deliberate, tested behaviour change (minor).** Today `ENV=" dev "`
is treated as **production** (it does not match the `dev` case) — so adding `TrimSpace` moves it
to **dev**, which is a *fail-open* direction for the telemetry gate. It ships anyway, for two
reasons, and neither is "it's probably fine":

1. For the **data plane** the direction is now irrelevant: `IsExplicitDev()` only widens *which
   spellings mean dev*, and the data plane additionally requires `SKY_CONSOLE_DATA=on`, so
   `ENV=" dev "` cannot disclose anything a bare `ENV=dev` would not.
2. For the **telemetry gate** the alternative — `" dev "` silently meaning production — is a
   *surprise* in the other direction (a developer who typed a stray space gets 401s they cannot
   explain), and `productionFromEnv`'s own doc comment states the design preference is to
   surprise toward the gate only when `ENV` is *deliberately* set to something non-dev
   (`observability.go:305-312`).

§7.3 gains a `skyenv` **table test** — the thing v2 omitted entirely — covering: unset/unset;
`ENV=""`+`SKY_ENV=production`; `dev`, `DEV`, `" dev "`, `"dev "`, `development`, `local`;
`production`, `prod`, `staging`, `qa`, `test`, `eu-west-2`; and the precedence of `ENV` over
`SKY_ENV`. Each row asserts all three of `Which()`, `IsProduction()`, `IsExplicitDev()`.

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

**M-9 — the boundary is sufficient for 5e-1 and is a HARD BLOCKER for 5e-2.** Read-side, a
poisoned column costs an operator a misleading row. Write-side (§1.4's **W1**) the same value is
an *authorization input*: tenant A writes `tenant="B"`, B's operator is shown the row, and W1's
"the row's tenant column equals the verified tenant" test then **authorizes B to write it**. A
tenant can hand another tenant's operator write authority. So:

> **W1 must not be implemented against the application-written column.** 5e-2 is gated on the
> durable verified tenant below. A 5e-2 delivery that ships W1 on the current mechanism is not
> fail-closed, regardless of how carefully it is documented — documentation bounds a *view*, it
> cannot bound a *grant*.

**What 5e-1 does ship against it (the operator-visible mitigation):** the tenant column's
**value is rendered in every row**, forced into the KV field set and the SQL SELECT list even
when `adminShow` omits it, so the operator sees *what the filter matched* instead of trusting
that it matched. Disclosure interaction: under a **scoped** decision the value necessarily
equals `d.tenant`, so rendering it discloses nothing the operator does not already know; under
an **unscoped** decision it is rendered only when `adminShow` names it (otherwise `***`), so
tenant identifiers are not enumerable by a super-admin who was not given the column.

**Scoped out explicitly, with its mechanism named:** a durable verified tenant would persist
`CommitReq.Tenant` into the MVCC value header (or a reserved system column) at
`committer.go:152/318` and filter on *that*. It is an engine-format change gated by
`base.CheckComparer` (`AUTONOMOUS_GOAL.md`: *`Name="skydb.mvcc.v1"` IRREVERSIBLE*) and is a
**Phase-6 item**, not a 5e line item — **and it is now on 5e-2's critical path** (M-9 above),
which is a change from v2, where it was purely a nice-to-have.

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

> **Root-cause fix (AGENTS.md "root-cause fixes only") — REVISED in v2.1 (grill M-5).**
> v2 made `ensureRegistered` a *strengthening upsert* that **replaces** a resident schema. That
> is a **write-path change to the storage engine motivated by an admin read feature**, and it
> breaks a guarantee the engine currently relies on: `WatchTenant` pins
> `cs := b.schemaByName(coll.Name)` (`embedded.go:558`) into `subscription.schema` (`:563`) for
> the subscription's whole life, with the comment "*the registry-owned copy (stable pointer for
> the sub's life)*". Under set-if-absent that pointer can never go stale; under replace-on-
> stronger a live subscription keeps indexing against the pre-upgrade column set.
>
> **v2.1 drops the upgrade.** The registry becomes **write-once per collection per process**:
>
> ```go
> // ensureRegistered installs cs ONLY when the collection is absent. A resident
> // schema is NEVER replaced, by any caller, for the life of the process — so a
> // *CollSchema handed out by schemaByName (collResolver:102, indexerFn:112,
> // WatchTenant:558 → subscription.schema:563) is immutable by construction.
> //
> // Register (exported) gets the same rule: a second Register for a resident
> // collection is a NO-OP that logs persist.schema.conflict at warn with a
> // field-level diff. It used to overwrite unconditionally (embedded.go:64-69) —
> // that was the only in-process pointer-swap and it is now gone.
> func (b *EmbeddedBackend) ensureRegistered(cs CollSchema)
> ```
>
> **Why no upgrade path is needed** (this is the load-bearing argument, not an omission):
> after §3.3, **every** registration path carries the full declaration — `parseEmbeddedSchema`
> copies `TenantCol`/`AdminShow` through on every verb (`embedded_kernel.go:174-213`), the
> reactive arm registers from the same parse (`bluedb_reactive.go:188`), and `P.declare` (§3.8)
> registers from the same parse at boot. The **only** producer of a weaker schema in the tree is
> `adminReadRows`'s `bluedb.CollSchema{Name: collName}` literal (`console_data.go:84`) — and
> §3.9 **deletes that file**. So every registration for a given collection in a given process
> derives from the *same* Sky declaration and is byte-identical; there is nothing to upgrade
> *from*. A genuine difference means an application bug, and a loud `persist.schema.conflict`
> warn is the correct response to an application bug — not a silent security-relevant mutation
> of live subscription state.
>
> The check/act race in the current form (RUnlock between the read and `Register`, `:73-77`)
> is closed by doing the presence check **under the write lock**.
>
> **Pushback registered (evidence).** The grill listed `collResolver` (`:102`) and `indexerFn`
> (`:112`) alongside `WatchTenant` as sites where "*`schemaByName` pointers escape*". They do
> not pin: both call `b.schemaByName(...)` on **every invocation** (`embedded.go:100-104`,
> `:110-118` — read verbatim), so both would pick up an upgrade immediately. The finding's
> **conclusion** is nonetheless correct for `WatchTenant`, which is why the fix targets pointer
> **immutability** rather than per-call re-resolution — immutability covers all three sites and
> needs no per-site reasoning.

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
> // ScanPage is a PK-ORDERED, cursor-paged, EARLY-EXITING, BUDGETED scan.
> //
> // MEMORY (M7): it never materialises more than `limit` rows — iteration stops
> // as soon as `limit` matches are collected, so an admin browse of a 10M-row
> // collection allocates O(limit), not O(matching). Cursor pagination (afterKey)
> // replaces OFFSET, so the early exit is exactly correct — there is no global
> // sort to defeat.
> //
> // TIME (v2.1, grill minor): early exit bounds memory, NOT work. A tenant with
> // 3 matching rows in a 10M-row collection still walks 10M keys before the page
> // is full — an admin browse that pins a core for minutes on a privileged path,
> // which is the same DoS in a different resource. ScanPage therefore also takes
> // a rows-EXAMINED budget (default maxExamine = 50_000, ~250x MaxRows) and
> // returns partial=true with a resumable nextAfter when it is hit. The caller
> // renders "showing N rows examined so far — continue" rather than an empty page,
> // so a sparse tenant is browsable in bounded steps instead of hanging.
> //
> // nextAfter is "" ONLY when the scan reached the end of the collection.
> func (b *EmbeddedBackend) ScanPage(coll CollSchema, where CondNode, afterKey string,
> 	limit, maxExamine int) (rows [][]byte, nextAfter string, partial bool, err error)
> ```
>
> `Query`/`orderAndPage` are untouched (the app's own query path keeps its ordering
> semantics). Only the admin plane uses `ScanPage`. `partial` is surfaced through
> `consoledata.Page.Partial` and rendered as its own UI state (§6.3) — it must not look like
> "end of collection", because that would silently under-report a scoped operator's rows.

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
	// (ReadRows' post-filter). partial=true means the rows-examined budget was
	// hit before the page filled; next is still resumable (§3.5c).
	ScanPage(coll, tenantCol, tenant, keyPrefix, after string, limit int) (rows [][]byte, next string, partial bool, err error)
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
	Partial  bool     // the rows-examined budget was hit; Next is resumable (§3.5c)
	Redacted []string
}
type Row struct {
	// Handle addresses the row for a subsequent detail fetch. Under
	// DiscloseDeclared with the PK absent from adminShow it is the ONLY
	// identifier the UI receives (M-7). Opaque, per-boot.
	Handle string
	// Key is the primary key VERBATIM — populated only when disclosure permits
	// it (DiscloseAll, or the PK named in adminShow). Otherwise "***".
	Key string
	// Fields is the ordered, post-filtered field set. Value is "***" for any
	// undisclosed field. THE STORED BYTES NEVER APPEAR ANYWHERE ELSE ON Page.
	Fields []Field
}
```

`ReadRows` body, in order: `!d.allowed ⇒ ErrDenied` → clamp `limit` to `MaxRows` →
`src.Schema(coll)`, `!ok ⇒ ErrNotRegistered` → if `d.scoped`: `tenantCol == "" ⇒
ErrNoTenantCol` (**never a full read**) → `src.ScanPage(coll, tenantCol, d.tenant, keyPrefix,
after, limit)` → **post-filter every row against `d.tenant`** → apply the disclosure filter
(§5) → force the tenant column into `Fields` per §3.1's M-9 mitigation → cap every value at
`MaxCellBytes` → mint each `Handle`.

**M14 is resolved by the signature**: the connection id is part of a collection's *address*,
so `Data_collections` returns `{dataConn, dataName, dataKind}` and `Data_rows` takes
`(connId, coll, prefix, after)`. The v1 ambiguity (a collection name existing on two
backends) cannot arise.

**M-4 — the stored bytes must not survive the funnel, and v2 let them.** v2 defined
`Page.Row.Fields` with per-field `***` (correct) *and* a Sky-side
`DataRow = { dataKey, dataValue : String, dataRedacted : Int }` (§6.2) that §6.3 rendered as
"*the stored codec JSON field-by-field*". A single opaque `dataValue` carrying the stored JSON
**is** the v1 leak that §5.1 congratulates itself on catching, re-introduced one layer down,
because the redaction would then happen in Sky, after the secret bytes had already crossed the
kernel boundary into the console's model (and thus into any rendered HTML, any log of the model,
and any session-store persistence of it). Two structural rules:

> 1. **`consoledata` never emits the stored record.** The kernel return payload is built
>    **exclusively** from the post-filtered `Fields` — `map[string]any{"dataKey": …,
>    "dataFields": []any{ {"fieldName":…, "fieldValue":…}, … }, "dataRedacted": n}`. There is no
>    field on `Page`, `Row`, or the payload that carries the source bytes, and `Row` has no
>    exported raw accessor. §6.2's Sky type changes to match.
> 2. **The test asserts on the PAYLOAD, not on `Page`.**
>    `TestReadRows_UndeclaredValueAbsentFromKernelPayload` (§7.2) marshals the exact `[]any` the
>    kernel returns and asserts the undeclared field's *value* appears nowhere in the bytes.
>    v2's §7.2 asserted on `Page`, which cannot see a leak introduced by the payload builder.

**And the scope of §5.3's socket claim, corrected.** "*Secret bytes never cross the database
socket*" is true for **SQL only** (`'***' AS "col"` is evaluated server-side). For **KV** the
whole codec record is read out of the store and filtered **in-process** — the bytes cross the
process boundary and are discarded in `ReadRows`. The guarantee for KV is therefore *"never
leaves the process"*, which is weaker and is stated as such in §5.3, the package doc, and both
user docs.

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

// init in THIS file assigns the KV hook and NOTHING ELSE. In particular it does
// NOT call consoledata.Bind — see the B-4 box below.
func init() {
	kvAdminSourcesHook = func() []consoledata.KVSource { /* snapshot embeddedByID */ }
}
```

**B-4 — there is exactly ONE `Bind` site, and it is the ungated file.** v2 shipped a
contradiction: this §3.9 code block called `consoledata.Bind(rtConsoleSource{})` inside
`bluedb_admin.go`'s `init()`, while the prose two paragraphs later said `Bind` must live in the
ungated `rt/data_kernel_hooks.go`. Ship both and the **second `Bind` panics** — Go runs
same-package `init()`s in filename order, so `bluedb_admin.go` would win and
`data_kernel_hooks.go` would blow up — and **every Persist app with a console fails at startup**.
That is an availability regression shipped in exchange for nothing: the property `Bind` needs is
that a second call cannot *substitute* the source, and first-wins delivers it (§2.5).

```go
// runtime-go/rt/data_kernel_hooks.go — UNGATED (no sky-app/bluedb import), so it
// is materialised into EVERY app. This is the ONE AND ONLY Bind call site in the
// tree; TestExactlyOneBindCallSite is a source-grep tripwire on it.
//
// It must be ungated because SQL sources exist without bluedb: a non-Persist app
// with a console still needs a bound Source or every Decide() denies (which is
// correct, but would make the Data tab dead rather than SQL-only).
var kvAdminSourcesHook = func() []consoledata.KVSource { return nil }

func init() { consoledata.Bind(rtConsoleSource{}) }

// rtConsoleSource implements consoledata.Source from the goroutine's live
// session — the same mechanism as tenantPrefixForSession (hub_bridge.go:539).
// It lives HERE, not in bluedb_admin.go, because it touches no bluedb type.
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

**And an unbound source denies in EVERY environment.** v2's `TestDecide_WithoutBindDenies`
covered production only, and v2's `Decide` reached the dev arm before it would have noticed —
so an unbound source in dev fell through to allowed/unscoped/`DiscloseAll`, the same fail-open
shape as B-2. §2.6's `Decide` now checks `currentSource() == nil` **before** the tier is even
consulted, and §7.1 tests both tiers.

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

**M-3 — v2 left two security-relevant parameters in `rt`'s hands, and A5 did not reach SQL.**
Two independent defects in v2's §4.2:

1. **`Rebind(q string) string`.** A string→string hook to which `consoledata` hands its
   *finished statement* immediately before execution. B5 moved the concatenation into the
   funnel; `Rebind` moved the trust straight back out. An `rt` implementation that returns a
   different string executes a different query, and nothing in the funnel can tell.
   **Deleted.** Replaced by a `Driver` **enum owned by `consoledata`**, which emits its own
   placeholders (`?` for SQLite, `$n` for Postgres). `rt` supplies a value from a closed set;
   it can pick the wrong member, but it cannot author SQL.
2. **No post-filter on the SQL plane.** v2's A5/R15 claimed "three independent guards"; all
   three lived in `ReadRows`. `BrowseSQL` had **none**, so `TenantColOf(table)` returning a
   wrong-but-real column name (a typo, a renamed column, a table whose tenant column is called
   something else) produced a syntactically valid `WHERE` over the wrong column and **nothing
   caught it**. Fixed by giving `BrowseSQL` the same post-filter, plus an introspection check.

```go
// Driver is a CLOSED set owned by consoledata. rt names its dialect; it does not
// get to touch the statement (grill M-3 — Rebind is deleted).
type Driver uint8
const (
	DriverUnknown Driver = iota // ⇒ ErrUnsupportedDriver; never a fallback dialect
	DriverSQLite
	DriverPostgres
)

// SQLSource is the narrow view of one registered connection. rt supplies
// connections and the allowlist; consoledata constructs every statement and
// every placeholder.
type SQLSource interface {
	Handle() string  // opaque, per-boot (see M11)
	Label() string   // credential-free (see M10)
	Driver() Driver
	Tables() []string                    // the default-deny allowlist for this source
	TenantColOf(table string) string     // "" when none declared — VERIFIED against
	                                     // introspection before use (M-3)
	AdminShowOf(table string) []string   // empty set ⇒ nothing disclosed outside dev
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
ph, err := placeholders(src.Driver())   // M-3: consoledata owns the dialect;
if err != nil { return Page{}, err }    // DriverUnknown ⇒ ErrUnsupportedDriver
clamp(limit, MaxRows); clamp(offset, MaxOffset)
release := acquireBrowseSlot()          // see M-sem below
defer release()
tx, err := src.BrowseTx(ctx); defer tx.Rollback()
cols, err := tx.Columns(ctx, table)     // introspection on the BROWSE conn (M13)
if len(cols) == 0 { return Page{}, ErrNoColumns }   // minor: v2 indexed cols[0] blind

// ── M-3: the tenant column is VERIFIED, FORCED INTO THE SELECT, and POST-FILTERED
tcol := ""
if d.scoped {
    tcol = src.TenantColOf(table)
    if tcol == "" { return Page{}, ErrNoTenantCol }          // NEVER a full read
    if !contains(cols, tcol) { return Page{}, ErrBadTenantCol } // a typo is now caught
}
// The tenant column rides in the SELECT list even when adminShow omits it, so the
// post-filter has something to check and §3.1's M-9 mitigation has something to
// render. It is marked hidden-unless-disclosed so §5's rules still govern display.
sel := discloseSelectList(cols, src.AdminShowOf(table), d.Disclosure(), /*force*/ tcol)  // §5
q := "SELECT " + join(sel) + " FROM " + quoteIdent(table)
args := []any{}
if d.scoped {
    q += " WHERE " + quoteIdent(tcol) + " = " + ph.next()
    args = append(args, d.tenant)                     // BOUND PARAMETER, never interpolated
}
// minor: v2 ordered by cols[0], which panics on empty introspection (fixed above)
// and paginates UNSTABLY whenever cols[0] is not unique — OFFSET paging over a
// non-total order silently duplicates and skips rows. Ordering on the FULL
// introspected column list is a total order on any table. Redacted columns are
// still real columns: `'***' AS "x"` is a projection, ORDER BY names "x" itself.
q += " ORDER BY " + joinQuoted(cols) + " LIMIT " + ph.next() + " OFFSET " + ph.next()
args = append(args, limit+1, offset)
rows, err := tx.QueryContext(ctx, q, args...)          // no Rebind hop (M-3)

// ── M-3: the SQL post-filter — the exact analogue of ReadRows'. Under a scoped
// decision EVERY returned row's tenant cell must equal d.tenant; any mismatch
// discards the WHOLE page and returns ErrScopeViolation + an audit event. This is
// what makes R15's "three independent guards" true of the SQL plane too: it
// independently defeats a dropped WHERE, a wrong TenantColOf, a driver/placeholder
// mismatch, and a collation-induced non-equality.
```

No `rt` code constructs SQL, chooses a placeholder, or sees the finished statement. `d.tenant`
is unexported and is read only here.

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
console.data.denied           warn   reason=<Decision.Reason()> subject=<sub> coll=<name>
console.data.list             info   subject=… scoped=<bool> disclose=<all|declared> kv=<n> sql=<n>
console.data.read             info   subject=… scoped=<bool> coll=<name> rows=<n> redacted=<n> partial=<bool>
console.data.sql.read         info   subject=… handle=<opaque> table=<t> rows=<n> redacted=<cols>
console.data.disabled         warn   reason=<not-enabled|unknown-value|weak-signing-key|no-source-bound>
console.data.disclose-all-ignored warn tier=<prod|unknown>            (§2.10)
console.data.bind-duplicate   error  bound=<type> rejected=<type>     (§2.5, B-4)
console.data.scope-violation  ERROR  subject=… plane=<kv|sql> coll=<name> rows_discarded=<n>
                                     (M-6 — never the offending value, never the foreign
                                      tenant; this is an INTERNAL-CONSISTENCY failure and it
                                      is logged at error because a guard fired that should
                                      never fire: the predicate reached the store and the
                                      store returned a non-conforming row anyway)
persist.schema.conflict       warn   coll=<name> field=<f> resident=<a> incoming=<b>  (§3.5a)
```

**M-6 — a scope violation must not look like an empty collection.** §6.3 returns `Ok([])` on
**deny**, deliberately, so the UI cannot distinguish "no tenant claim" from "no such
collection". v2 routed `ErrScopeViolation` down the same path, which combines badly with
discard-the-whole-page: **one** non-conforming row makes a collection permanently unbrowsable,
with no operator-visible reason and (in v2) no audit event either. A scope violation is not an
authorization outcome — it reveals nothing about other tenants, only that the plane's own
guards disagreed — so it gets its own event **and** its own UI state (§6.3).

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

- **`DiscloseAll`** requires **both** `skyenv.IsExplicitDev()` **and** `SKY_CONSOLE_DATA=all`
  (§2.6 `discloseFor`): every field/column is rendered. **v2's justification was circular and is
  replaced.** v2 argued "*`IsProduction()` returns false only when `ENV` is unset or is a dev
  marker … and the data is on the developer's own disk, which they can already `cat`*" — the
  first clause concedes that **unset** lands here, and the second assumes exactly the thing that
  fails (that this *is* a developer's own disk, which is unknowable when `ENV` is unset). The
  v2.1 justification is mechanical instead: `DiscloseAll` requires **two** positive operator
  declarations, neither of which has a default, so it cannot be reached by omission. The
  deny-list heuristic is still applied here as a **dev convenience** — explicitly *not* a
  security boundary.
- **`DiscloseDeclared`** (every other combination, including `ENV` unset): only fields named in
  the collection's declared `adminShow` are rendered. Everything else is `***`.
- **The primary key is no longer unconditionally disclosed (M-7).** v2 rendered it always,
  while §2.10 justified opt-in *precisely because* "*keys are frequently emails*" — a direct
  contradiction. Under `DiscloseDeclared` the PK renders verbatim **only when `adminShow` names
  it**; otherwise it renders `***` and the row is addressed by an opaque per-boot **row
  handle**:

  ```go
  // rowHandle = base64url(HMAC-SHA256(perBootKey, connID ‖ 0 ‖ coll ‖ 0 ‖ key))[:16].
  // Same construction as the SQL source handle (M11): keyed, per-boot, so it is
  // neither an offline oracle nor stable across restarts.
  //
  // consoledata keeps a bounded reverse map (LRU, cap 8·MaxRows) populated as it
  // emits each page, so a detail fetch for a row the operator just listed always
  // resolves. An unknown handle ⇒ ErrStaleRowHandle ⇒ "re-list this collection",
  // never a scan. The resolved key is re-checked against the CURRENT decision
  // before the detail read, so a handle minted under one decision cannot be
  // replayed under another.
  ```

  The advice "*do not use PII as a primary key*" stays in the docs, but it is now advice rather
  than the only thing standing between an email-keyed collection and a production console.
- **The declared tenant column is always in the field set** (§3.1's M-9 mitigation), rendered
  under a scoped decision (its value necessarily equals the operator's own tenant) and `***` under
  an unscoped decision unless `adminShow` names it.
- **Zero value is `DiscloseDeclared`** — a forgotten assignment redacts rather than discloses.

**Non-Persist raw `Std.Db.Store` tables have no declaration channel.** In production they are
therefore listed but render **PK-only**, with an explicit UI state naming `P.adminShow`. That
is a deliberate, stated limitation of 5e-1, not an oversight.

### 5.3 The guarantee, restated honestly — everywhere

The ported file header, the `consoledata` package doc, `docs/skydb/overview.md` and
`docs/v0.16.x-console/EMBEDDED.md` all carry the same sentence:

> Outside an explicitly-declared dev environment, the admin plane discloses **only** the fields
> a collection explicitly declares via `P.adminShow`; everything else is `***`.
>
> **On SQL sources** the redaction happens **at SELECT time** (`'***' AS "col"`), so the secret
> bytes never cross the database socket.
>
> **On KV collections it does not, and cannot.** A KV record is one codec-encoded value: the
> engine must return the whole record and `consoledata` discards the undeclared fields
> **in-process**, before they reach any kernel payload, model, view, log or session store. The
> guarantee there is *"never leaves the process"*, which is weaker than the SQL one, and it is
> stated that way rather than blurred into it. (M-4: v2's §5.3 asserted the socket property
> for both planes.)
>
> The sensitive-name heuristic is a **development-mode convenience and is not a security
> boundary** — it is a deny-list and deny-lists are incomplete by construction (`stripe_sk`,
> `iban`, `dob`, `national_id` and `backup_codes` all pass it).

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
type alias DataField      = { fieldName : String, fieldValue : String }
type alias DataRow        = { dataHandle : String, dataKey : String, dataFields : List DataField, dataRedacted : Int }
type alias SqlSource      = { sqlHandle : String, sqlLabel : String, sqlDriver : String, sqlTables : List String }
type alias SqlRow         = { sqlCells : List String, sqlIsHeader : Bool, sqlRedactedCols : List String }
```

**M-4 — `dataValue : String` is DELETED and replaced by `dataFields`.** v2's single opaque
string was, by §6.3's own description, "*the stored codec JSON*" — i.e. every field of the
record including the secrets, shipped across the kernel boundary into the console's Model, to be
redacted afterwards in Sky. That is the v1 leak (§5.1) one layer down: once the bytes are in the
Model they are in the rendered HTML, in any log of the Model, and in the session store that
persists it. `dataFields` is built **only** from `consoledata`'s post-filtered `Page.Row.Fields`
(§3.7), so an undisclosed field arrives as the literal `"***"` and its value never crosses the
boundary at all. `dataHandle` carries M-7's opaque row address; `dataKey` is `"***"` whenever
disclosure does not permit the PK.

**Fieldset-ambiguity note:** `DataField` adds a `{fieldName, fieldValue}` name set to the compile
unit. `§7.5`'s `TestConsoleAppRecordFieldsetsAreTypeUnambiguous` covers it — both fields are
`String`, and the test asserts type-identity across any shared name set, so a future alias with
`{fieldName : String, fieldValue : Int}` fails the gate rather than silently picking a candidate.

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
table with a **Next** button driving the cursor · a row-detail panel rendering `dataFields`
(**never** a stored JSON blob — M-4) · the declared tenant column's value rendered on every row
(§3.1's M-9 mitigation) · and the explicit states below.

**Kernels return `Ok([])` on deny, never `Err`.** The reason goes to the audit log; the UI
gets a neutral empty state, so it cannot distinguish "no tenant claim" from "no such
collection".

**The distinguishable states (M-6 + §3.5c).** v2 had one empty state for everything, which made
a scope violation indistinguishable from an empty collection and a budget-truncated page
indistinguishable from the end of the data. `Model` gains
`dataState : DataState`:

| `DataState` | Rendered as | Set by |
|---|---|---|
| `DataOk` | the rows | a normal page |
| `DataEmpty` | "no rows" — **the same neutral copy for deny, disabled, unknown collection, and genuinely empty** | `Ok([])`; the reason is in the audit log only |
| `DataNoTenantCol` | "this collection declares no `tenantCol`; a scoped operator cannot browse it — add `P.tenantCol` " | `ErrNoTenantCol` (already distinguishable: it discloses only the *schema*, which the operator's own app declares) |
| **`DataScopeViolation`** | **"this collection could not be displayed safely — the server refused a page whose rows did not match your tenant. See `console.data.scope-violation` in the log."** | `ErrScopeViolation` (M-6) |
| **`DataPartial`** | the rows **plus** "examined the row budget before filling this page — **Continue**" wired to `Next` | `Page.Partial` (§3.5c) |
| `DataStaleHandle` | "this row is no longer addressable — re-open the collection" | `ErrStaleRowHandle` (M-7) |

`DataScopeViolation` discloses nothing about other tenants — only that the plane's own guards
disagreed — so surfacing it is safe, and *not* surfacing it was the actual harm: one poisoned
row silently bricked a collection forever.

Plus the standing footers: the declaration-driven enumeration note (§3.8) and M1's
trust-boundary footer (§3.1).

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
| `TestDecide_ScopesBeforeProdShortcut` | source returns `consoleDataTenant=acme`, `ENV=dev`, `SKY_CONSOLE_DATA=on` ⇒ `Scoped() && tenant=="acme"` | **B3.** Move the dev branch above the tenant branch (i.e. restore `console_data.go:33-35`) ⇒ unscoped ⇒ fail. **Fails against `8ceea18d`.** |
| `TestDecide_VerifiedNoClaimInProdDenies` | prod + verified + no data claim + no super ⇒ `!Allowed()` | Return `allowed:true` from the final branch ⇒ fail. (The non-vacuous B2 pin.) |
| `TestDecide_NoPrincipalInProdDenies` | prod + `ok=false` ⇒ `!Allowed()` | Drop the `if !devTier` guard in the `!ok` branch ⇒ fail. |
| **`TestDecide_EnvUnsetIsNotDev`** | **`ENV` and `SKY_ENV` BOTH unset**, `SKY_CONSOLE_DATA=on`, source returns `ok=false` ⇒ `!Allowed()` | **B-2, the headline pin.** Swap `skyenv.IsExplicitDev()` back to `!skyenv.IsProduction()` (v2's body) ⇒ allowed/unscoped/`DiscloseAll` ⇒ fail. **Fails against v2's own design.** |
| **`TestReadsDisabledWhenSkyConsoleDataUnset`** | `ENV=dev`, `SKY_CONSOLE_DATA` **unset** ⇒ `!Allowed()`, reason names the env var | **B-2/P1.** Restore "unset ⇒ on in non-production" ⇒ allowed ⇒ fail. |
| **`TestReadsDisabledOnUnknownValue`** | `SKY_CONSOLE_DATA=yes-please` ⇒ `!Allowed()` + `console.data.disabled reason=unknown-value` | Fall back to on for unrecognised values ⇒ fail. (minor) |
| **`TestDiscloseAllRequiresBothSignals`** | table: (`dev`,`all`)⇒`DiscloseAll`; (`dev`,`on`)⇒`Declared`; (unset,`all`)⇒`Declared` **+ `disclose-all-ignored` logged**; (`production`,`all`)⇒`Declared` | Drop either conjunct in `discloseFor` ⇒ a row flips ⇒ fail. |
| `TestDecide_TakesNoTrustArguments` | reflection over `consoledata.Decide`: `NumIn()==0` | **B4.** Add any parameter to `Decide` ⇒ fail. This is the *structural* pin v1's `TestDecision_ZeroValueIsDeny` pretended to be. |
| **`TestBind_SecondCallIsIgnoredAndLogged`** | `Bind(a); Bind(b)` ⇒ **no panic**, `BoundSourceType()` still reports `a`, and `console.data.bind-duplicate` is logged naming both | **B-4.** Make `Bind` overwrite ⇒ `BoundSourceType()` reports `b` ⇒ fail. Pins the trust-substitution vector *without* the init-time panic v2 would have shipped. (Replaces `TestBind_SecondCallPanics`.) |
| **`TestExactlyOneBindCallSite`** | source-grep over `runtime-go/rt/**.go`: exactly **one** non-test `consoledata.Bind(` occurrence, in `data_kernel_hooks.go` | **B-4.** Add the `bluedb_admin.go` `Bind` back (v2's §3.9 code block) ⇒ two sites ⇒ fail. This is the test that would have caught v2's contradiction at review time. |
| **`TestDecide_WithoutBindDeniesInEveryTier`** | table over `{unset, dev, production}` × unbound source ⇒ `!Allowed()` in **all three** | **B-4.** Restore v2's prod-only check ⇒ the `dev` row returns allowed/unscoped/`DiscloseAll` ⇒ fail. (Replaces `TestDecide_WithoutBindDenies`, which tested prod only.) |
| `TestDisclosure_ZeroValueIsDeclared` | `var d Disclosure; d == DiscloseDeclared` | Reorder the const block so `DiscloseAll` is 0 ⇒ fail. (**Not** a Go tautology: it pins the *ordering choice*, which is the safety property.) |
| `TestDecide_ProdAlwaysDeclaredDisclosure` | prod + scoped ⇒ `Disclosure()==DiscloseDeclared` | Make `discloseFor` ignore its tier argument ⇒ fail. |

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
| **`TestReadRows_UndeclaredValueAbsentFromKernelPayload`** | prod-scoped, `adminShow=["id"]`, row field `secret="sk_live_XYZZY"` ⇒ build the **exact `[]any` of `map[string]any` the `Data_rows` kernel returns**, `json.Marshal` it, assert `"sk_live_XYZZY"` is **absent from the bytes** | **M-4, the pin v2 lacked.** Re-introduce a `dataValue` carrying the stored record (v2's §6.2) ⇒ the secret is in the payload ⇒ fail. Deliberately asserts on the **payload**, not on `Page`: v2's §7.2 asserted on `Page`, which cannot see a leak introduced by the payload builder. |
| **`TestReadRows_PKRedactedUnlessInAdminShow`** | prod-scoped, `adminShow=["title"]`, key `"ada@example.com"` ⇒ `Row.Key == "***"`, `Row.Handle != ""`, and the email is absent from the payload; with `adminShow=["id","title"]` and PK column `id` ⇒ the key renders | **M-7.** Restore v2's unconditional PK render ⇒ the email leaks ⇒ fail. |
| **`TestRowHandleResolvesThenGoesStale`** | list a page, resolve a handle to its row; then evict the LRU ⇒ `ErrStaleRowHandle`, and **zero** `ScanPage` calls | Fall back to scanning for an unknown handle ⇒ a call is recorded ⇒ fail. |
| **`TestRowHandleIsKeyedAndPerBoot`** | two `perBootKey`s ⇒ different handles for the same `(conn, coll, key)`; the handle is not `sha256(key)` | Derive the handle unkeyed ⇒ equal ⇒ fail. (Same oracle class as M11.) |
| **`TestReadRows_TenantColAlwaysInFields`** | prod-scoped, `adminShow=["id"]`, `tenantCol="tenant"` ⇒ `Fields` contains `tenant` with the **value** `acme`; unscoped + `adminShow=["id"]` ⇒ `tenant` renders `***` | **M-9 mitigation.** Drop the force ⇒ the operator cannot see what matched ⇒ fail. |
| **`TestReadRows_PartialIsDistinctFromEnd`** | fake source returns `partial=true, next="k42"` ⇒ `Page.Partial == true && Page.Next == "k42"` | Collapse `partial` into "no more rows" ⇒ a sparse tenant silently under-reports ⇒ fail. (§3.5c) |

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
| **`TestWeakSigningKeyDisablesDataPlaneOutsideExplicitDev`** | table: (`ENV` unset, no secret) ⇒ **disabled**; (`ENV=production`, no secret) ⇒ disabled; (`ENV=dev`, no secret) ⇒ enabled; (`ENV=production`, `SKY_CONSOLE_COOKIE_SECRET`=32 B) ⇒ **enabled**; (`ENV=production`, `SKY_CONSOLE_TOKEN`=32 B) ⇒ enabled; (`ENV=production`, secret 31 B) ⇒ disabled | **M5 + B-2 + B-3.** Gate on `IsProduction()` instead of `!IsExplicitDev()` (v2) ⇒ the `ENV`-unset row arms on a file-derived key ⇒ fail. Drop `SKY_CONSOLE_COOKIE_SECRET` (v2) ⇒ the app-mode rows are unreachable ⇒ fail. |
| **`TestAppModeProdWithCookieSecretReachesScopedDecision`** | `SKY_CONSOLE_AUTH=app`, `ENV=production`, `SKY_CONSOLE_DATA=on`, `SKY_CONSOLE_COOKIE_SECRET` set, callback returns `consoleDataTenant=globex`, **no `SKY_CONSOLE_TOKEN`** ⇒ `Scoped() && tenant=="globex"` | **B-3, the contradiction pin.** Restore v2's `SKY_CONSOLE_TOKEN`-only rule ⇒ the data plane declines ⇒ §2.8's `app`+prod rows are unreachable ⇒ fail. |
| **`TestRandomDevTokenFallbackIsHardError`** | force `crypto/rand` failure ⇒ an error, **not** a `dev-fallback-<pid>-<nanos>` string | Restore `randomDevToken`'s `fmt.Sprintf` fallback (`:271-279`) ⇒ a guessable signing secret ⇒ fail. |
| **`TestSkyenvTierTable`** | table over `{unset/unset, ENV=""+SKY_ENV=production, dev, DEV, " dev ", "dev ", development, local, production, prod, staging, qa, test, eu-west-2}` + `ENV`-over-`SKY_ENV` precedence, asserting all three of `Which()`, `IsProduction()`, `IsExplicitDev()` per row | **minor.** Drop `TrimSpace` ⇒ the `" dev "` rows flip; make `TierUnknown` return true from `IsExplicitDev` ⇒ the unset row flips ⇒ fail. **v2 had no `skyenv` test at all.** |
| **`TestProductionModeSnapshotMatchesSkyenv`** | after `SetProductionMode(skyenv.IsProduction())`, `isProductionMode() == skyenv.IsProduction()` for every tier row | **minor.** Let `SetProductionMode` be fed from anything other than `skyenv` ⇒ the two notions diverge ⇒ fail. |
| **`TestAuthzClaimSubsetMatchesConsoledataReaders`** | the claim keys `consoledata.Decide` reads (via a package-level `AuthzClaimKeys()`) equal `rt.authzRelevantClaims` minus the telemetry key | **M-1.** Add a claim reader to `Decide` without adding it to the fingerprint subset ⇒ a change in that claim would not rotate a session ⇒ fail. |

### 7.4 `runtime-go/rt/live_principal_reconcile_test.go` — the B1 legs

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestReconcile_DifferentPrincipalRotatesSession` | mint a session stamped `acme`; issue a second `handleInitial` whose context carries `globex` ⇒ a **new sid**, the old session **deleted**, and the served session's identity is `globex` | Restore the mint-only stamp (`live.go:4100-4110` inside the `else`) ⇒ the acme session is served to globex ⇒ fail. **This is the B1 attack, executable.** |
| `TestReconcile_LostPrincipalRotatesSession` | session stamped `acme`; second request carries **no** identity ⇒ rotate, and `SessionIdentity` on the served session is `ok=false` | Treat "no request identity" as "reuse" ⇒ Alice's privileged session survives her logout ⇒ fail. |
| `TestReconcile_SamePrincipalReusesSession` | identical fingerprint ⇒ **same sid**, model preserved | Rotate unconditionally ⇒ sid changes ⇒ fail. (Guards against a rotation storm.) |
| `TestReconcile_NoIdentityEitherSideIsNoOp` | an app that never stamps `IdentityContextKey`: 10 sequential requests ⇒ one sid, model preserved, zero deletions | Compare fingerprints when both sides are empty and treat them as unequal ⇒ every request rotates ⇒ fail. **The blast-radius guard.** |
| `TestReconcile_ClaimOrderDoesNotRotate` | the same claims supplied in a different map iteration order ⇒ same fingerprint ⇒ no rotation | Hash the map without sorting keys ⇒ nondeterministic rotation ⇒ fail. |
| **`TestReconcile_VolatileClaimsDoNotRotate`** | identity whose `Claims` carry `exp`/`iat`/`nonce` changing on **every** request, `consoleDataTenant` constant ⇒ 10 requests, **one** sid, model preserved | **M-1, the rotation-storm pin.** Fingerprint the **whole** claim map (v2's spec, sorted or not) ⇒ 10 sids and a Model wiped on every page load ⇒ fail. Sorting does **not** save it — that is the point. |
| **`TestReconcile_MixedRouteAppDoesNotRotateOnPublicRoute`** | an app that stamps identity on `/dash` but not on `/`: request `/dash` (stamped), then `/` (unstamped) ⇒ **same** sid, model preserved, identity retained | **M-1, row 3.** Fire row 3 without the `everStampedIdentity` latch qualification (v2's table) ⇒ the public route rotates and wipes the dashboard's Model ⇒ fail. |
| **`TestReconcile_RunsUnderThePerSessionLock`** | instrumented locker: assert `Lock(sid)` is held for the whole reconcile, and that a rotation `Unlock`s the old sid and `Lock`s the new one exactly once each | **M-2.** Move the call before `sessionIDNamed` (v2's §2.3-2) ⇒ the lock is not held ⇒ fail. |
| **`TestReconcile_ConcurrentEventCannotResurrectEvictedSession`** | goroutine A drives the rotating `handleInitial`; goroutine B holds a reference to the old session and calls `persistSession(oldSid, sess)` immediately after the delete ⇒ `store.Get(oldSid)` is **absent** and `persistSession` returned false | **M-2.** Drop the `dead` latch ⇒ the revoked session is back in the store and still addressable ⇒ fail. |
| **`TestPersistSessionFunnelCoversEveryStoreSet`** | source-grep `live.go`: zero `app.store.Set(` outside `persistSession` | **M-2.** Re-inline any of the five sites (`:4213, 4435, 4575, 5345, 6301`) ⇒ fail. Same funnel discipline as `9ad00daf`. |
| **`TestEventPathRejectsChangedPrincipal`** | session stamped `acme`; `POST /_sky/event` with that sid on a request whose context carries `globex` ⇒ **404** + `X-Sky-Live: 1` + `X-Sky-Status: session-lost`, session deleted, **no dispatch ran** | **B-1, defence in depth.** Rely on `handleInitial` alone (v2) ⇒ the event dispatches against acme's session ⇒ fail. |
| **`TestSSEPathRejectsChangedPrincipal`** | same, on `handleSSE` | Same mutation ⇒ the stream serves acme's session to globex ⇒ fail. |
| **`TestSessionLostHeaderIsTheShippedClientContract`** | assert the exact header pair the client switches on (`live.go:7752`): `X-Sky-Status == "session-lost"` | Emit `401` (v2's filed follow-on) ⇒ the shipped client has no handler ⇒ the operator is stranded ⇒ fail. Cites `live.go:7752-7760` as the reason no client change is needed. |
| `TestLogout_ClearsAllThreeCookiesAndEvictsSession` | after `POST /_sky/console/_logout`: `__Host-sky_console`, `sky_console_sid` **and** `sky_sky_console_sid` are all `Max-Age=-1`, and `store.Get(sid)` is gone | Clear only `__Host-sky_console` (today, `:874-877`) ⇒ fail. **Fails against `8ceea18d`.** |
| **`TestLogout_SameSiteGETAlsoEvicts`** | `GET /_sky/console/_logout` with `Sec-Fetch-Site: same-origin` ⇒ **303**, all three cookies cleared, session evicted | **B-5.** Make the route POST-only in C2 (v2) ⇒ the shipped `View.sky:142-148` `Ui.link` gets **405** ⇒ fail. This is the test that catches the 405 window v2 would have shipped. |
| **`TestLogout_CrossSiteGETRefused`** | `GET` with `Sec-Fetch-Site: cross-site` (and, separately, a foreign `Referer` with no `Sec-Fetch-*`) ⇒ **405**, nothing cleared, session intact | Accept any GET (today) ⇒ a cross-site `<img>` force-logs-out the operator ⇒ fail. |
| `TestLogin_RotatesSubAppSid` | a successful token login clears `sky_sky_console_sid` | Omit the clear ⇒ the pre-login sid survives ⇒ session fixation ⇒ fail. |
| **`TestLogin_EvictsPreviousOperatorSessionServerSide`** | session `S` stamped `acme` exists; a successful login as `globex` arrives carrying `S`'s sid ⇒ `store.Get(S)` is **gone** | **B-1(c).** Clear only the cookie (v2's §2.3-3) ⇒ `S` survives to TTL, still driveable via `/_sky/event` by anyone holding its sid ⇒ fail. Clearing the cookie *defeats* the `handleInitial` reconcile: with no cookie presented, reconciliation never sees `S`. |

### 7.5 Generated-console guards (`runtime-go/rt/console_app/*_test.go`)

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestConsoleAppRecordFieldsetsAreTypeUnambiguous` | parse `main.go` for `type \w+_R struct`; group by sorted field-**name** set; for every set with ≥2 members assert **all members have identical Go field types** | Give two console aliases the same field names with different types (e.g. `{fieldName:String,fieldValue:Int}` alongside `DataField`'s `{fieldName:String,fieldValue:String}`) ⇒ fail. **Correctly signed** — it currently passes because the one existing 2-candidate set (`State_Identity_R` / `Std_Live_Console_Identity_R`, `main.go:189-193` / `:585-589`) is byte-identical. v1's `TestConsoleAppNoRecordFieldsetCollision` is **DELETED**: its own stated falsifying mutation (renaming `dataKey`→`key`) does not falsify it, and it cites a collision that does not exist in this compile unit. |
| `TestConsoleTabStripCoversEveryTab` | every `State_Tab` constructor emitted in `main.go` appears in the emitted `allTabs` list | Add a `Tab` variant without adding it to `allTabs` ⇒ fail. Pins the `View.sky:196` silent-omission class (v1 could only flag it in prose). |

### 7.6 Engine + build gate

`runtime-go/bluedb/embedded_admin_test.go`:

| Test | Asserts | **Mutation that makes it fail** |
|---|---|---|
| `TestEnsureRegistered_RefusesWeakerSchema` | register `notes` with 3 cols, then `ensureRegistered(CollSchema{Name:"notes"})` ⇒ `SchemaOf` still reports 3 cols | Restore set-if-absent + allow a bare overwrite path ⇒ blanked ⇒ fail. **The registry-poisoning root-cause pin.** (v1's `TestQueryWithBareSchemaFindsNothing` is **DELETED**: it was **wrong-signed** — it fails when someone *fixes* the root cause, locking the trap in.) |
| **`TestResidentSchemaPointerIsImmutableForProcessLife`** | register `notes`; take `p1 := schemaByName("notes")`; call `Register` **and** `ensureRegistered` again with a *stronger* schema ⇒ `schemaByName("notes") == p1` (same pointer) and its fields are unchanged; `persist.schema.conflict` was logged | **M-5.** Restore v2's replace-on-stronger `ensureRegistered`, or `Register`'s unconditional `b.byName[cs.Name] = &cp` (`embedded.go:64-69`) ⇒ the pointer changes ⇒ fail. (**Replaces `TestEnsureRegistered_UpgradesStrongerSchema`, which pinned the behaviour v2.1 removes.**) |
| **`TestWatchTenantSubscriptionKeepsItsSchemaAcrossReregistration`** | open a `WatchTenant` subscription; re-`Register` the collection with an extra indexed column; commit a write ⇒ the subscription's `schema` pointer is unchanged, its footprint/order indices still match, and the delivered change is correct | **M-5, the test §7.6 lacked entirely.** Allow the registry to swap the resident pointer ⇒ the live sub indexes against a stale column set ⇒ fail. Cites `embedded.go:558` → `:563` ("*stable pointer for the sub's life*"). |
| **`TestIndexerAndResolverReResolvePerCall`** | instrument `schemaByName`; drive one `indexerFn` + one `collResolver` call ⇒ each recorded a lookup | Cache the pointer in either closure ⇒ zero lookups on the second call ⇒ fail. Pins the property that made the grill's "three pinned sites" claim false, so a future refactor cannot make it true. |
| `TestSchemaOf_DeepCopies` | mutate the returned `Cols`/`Indexes`/`Generated` ⇒ a second `SchemaOf` is unchanged | Return `*cs` (a shallow copy, v1's body) ⇒ the registry is corrupted ⇒ fail. (**M6**.) |
| `TestRegister_DeepCopiesCallerSlices` | mutate the caller's `Cols` slice **after** `Register` ⇒ `SchemaOf` unchanged | Keep `cp := cs` (`:64-69`) ⇒ fail. |
| `TestScanPage_StopsAtLimit` | seed 10 000 rows; `ScanPage(limit=10)` ⇒ 10 rows **and** an instrumented iterator counter ≤ 11 | Materialise then trim (today's `scanFilter`+`orderAndPage`) ⇒ counter is 10 000 ⇒ fail. (**M7**.) |
| **`TestScanPage_HonoursExamineBudget`** | seed 100 000 rows of which **3** match `tenant=acme`; `ScanPage(limit=200, maxExamine=50_000)` ⇒ `partial==true`, a **non-empty resumable** `nextAfter`, an examined counter ≤ 50 001, and resuming from `nextAfter` eventually returns all 3 | **minor.** Keep v2's memory-only early exit ⇒ the call walks all 100 000 keys and `partial` is false ⇒ fail. Early exit bounds memory, not time. |
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
| **`TestIntegration_DevOpenSeesEverything`** | **`ENV=dev`** + **`SKY_CONSOLE_DATA=all`**, two tenants seeded ⇒ the kernels return **all** rows, `DiscloseAll` | Require a claim in dev ⇒ empty ⇒ fail. (Zero-*config* dev is gone by design — B-2 — but zero-*ceremony* dev must stay: two env vars, no code, no claim.) |
| **`TestIntegration_EnvUnsetDataPlaneIsInert`** | **`ENV` and `SKY_ENV` both unset**, `SKY_CONSOLE_AUTH` unset (⇒ dev-open, console mounted, six telemetry tabs render **200**), `SKY_CONSOLE_DATA=on`, two tenants seeded ⇒ the Data kernels return **empty** + `console.data.denied`, and the six telemetry endpoints still return 200 | **B-2, end to end, and the blast-radius guard for the fix.** Restore v2's `!skyenv.IsProduction()` arm ⇒ every row of both tenants is returned unauthenticated and unredacted ⇒ fail. The second half of the assertion is what proves B-2's fix did **not** break zero-config observability. |
| `TestIntegration_ProdUnsetAuthDeclines` | `ENV=production`, `SKY_CONSOLE_AUTH` unset ⇒ `console.disabled reason=auth-unset` and **no** console route | Mount anyway ⇒ fail. (The goal's "gated behind `SKY_CONSOLE_AUTH`" clause.) |
| **`TestIntegration_DataPlaneOptInInEveryTier`** | table over `ENV ∈ {unset, dev, production}` with valid auth+claims and `SKY_CONSOLE_DATA` **unset** ⇒ empty + `console.data.disabled` in **all three** | **M3 + B-2/P1.** Default the plane on in any tier ⇒ that row returns rows ⇒ fail. (v2 tested production only.) |
| `TestIntegration_TokenModeNoDataEnvDenies` | prod, valid cookie, no data env ⇒ empty + `console.data.denied` | Restore the fail-open `""`-tenant behaviour (`hub_bridge.go:561-572`) ⇒ all tenants ⇒ fail. (**B2, end to end.**) |
| `TestIntegration_TokenModeScopedSeesOwnTenantOnly` | + `SKY_CONSOLE_DATA_TENANT=acme` ⇒ exactly the acme rows; the SQL plane lists only tables with a registered tenant column | Drop the SQL `WHERE` ⇒ globex rows ⇒ fail. |
| `TestIntegration_AppModeClaimsFlowGateToKernel` | callback returns `consoleDataTenant=globex`; drive `ConsoleGate` → mint → a kernel call on that session's goroutine ⇒ globex rows only | Break any link in the chain ⇒ fail. (Every link is new.) |
| `TestIntegration_CookieFastPathPreservesTenant` | request 1 exercises the callback (counter==1); **delete `sky_sky_console_sid`**; request 2 hits the cookie branch (counter still 1) and its **freshly minted** session still scopes to globex | Drop `C` from the cookie payload ⇒ the fresh session has no claim ⇒ DENY ⇒ 0 rows ⇒ fail. **v1's version was vacuous** — under session reuse it passed with zero claims in the cookie; deleting the sid cookie is what makes it bite. |
| `TestIntegration_PrincipalSwapDoesNotLeak` | authenticate as acme, read rows; **clear the auth cookie, keep the sid cookie**, authenticate as globex; read ⇒ **only globex rows** | Skip reconciliation ⇒ acme rows ⇒ fail. **The B1 attack, end to end.** |
| **`TestIntegration_EventPathCannotDriveAnotherPrincipalsSession`** | operator A authenticates (tenant `acme`) and A's `__skySid` is captured from the rendered page (`live.go:6953`); operator B authenticates (tenant `globex`) **in a second client with its own cookie jar and its own CSRF pair**; B `POST`s `/_sky/event` with **A's** `sessionId` in the body and B's valid CSRF header ⇒ **404 `session-lost`**, zero acme rows in B's response, `console.data.*` shows no acme read | **B-1, the direct path, end to end.** Revert either leg independently: (i) revert C0's cookie binding ⇒ `app.store.Get(req.SessionID)` (`live.go:4398`) serves A's session ⇒ fail; (ii) revert §2.3-5's principal check ⇒ once C0 is bypassed (or A and B share a browser) the dispatch runs against A's identity ⇒ fail. **Two independent mutations must each fail the test** — that is what "defence in depth" has to mean to be worth the words. Also asserts the CSRF pair does **not** save it (`live.go:3831`, `:7724` — double-submit proves same-origin, never same-session). |
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
| `TestSqlBrowse_ScopedBindsTenantAsParam` | the SQL is `… WHERE "tenant" = ?` (sqlite) / `= $1` (pgx) with the **verified** tenant as an arg; a request-supplied `tenant` is never used | Interpolate the tenant ⇒ fail. |
| `TestSqlBrowse_ScopedRequiresTenantCol` | scoped + no registered tenant column ⇒ omitted from `Tables` **and** refused on direct browse | Fall through to an unscoped browse ⇒ fail. |
| **`TestSqlBrowse_PostFilterDiscardsWholePageOnViolation`** | a `SQLSource` whose `BrowseTx` returns a hand-built `*sql.Rows` containing a `tenant=globex` row under a scoped-`acme` decision ⇒ `ErrScopeViolation`, **zero** rows, `console.data.scope-violation` logged | **M-3.** Remove the SQL post-filter (v2 had none) ⇒ the globex row is returned ⇒ fail. **This is what makes R15's "three independent guards" true of the SQL plane** — v2's were KV-only. |
| **`TestSqlBrowse_WrongTenantColIsCaughtByIntrospection`** | `TenantColOf` returns `"tenat"` (a typo) while introspection reports `tenant` ⇒ `ErrBadTenantCol`, **no query executed** | **M-3.** Trust `TenantColOf` blindly (v2) ⇒ a syntactically valid `WHERE "tenat" = ?` runs against the wrong column and nothing catches it ⇒ fail. |
| **`TestSqlBrowse_NoRebindHookExists`** | reflection over `consoledata.SQLSource`: it has **no** method whose signature is `func(string) string`; and `BrowseSQL` passes the statement it built to `QueryContext` **byte-identically** (recorded by the fake tx) | **M-3.** Re-add `Rebind(q string) string` ⇒ `rt` gets the finished statement back before execution ⇒ fail. B5 moved the concatenation; this moves the trust. |
| **`TestSqlBrowse_UnknownDriverRefuses`** | `Driver()` returns `DriverUnknown` ⇒ `ErrUnsupportedDriver`, no `BrowseTx` opened | Default to `?` placeholders ⇒ a Postgres source silently executes a malformed or mis-bound statement ⇒ fail. |
| **`TestSqlBrowse_EmptyIntrospectionRefuses`** | `Columns` returns `[]` ⇒ `ErrNoColumns` | Keep `ORDER BY quoteIdent(cols[0])` (v2) ⇒ **index out of range panic** on a privileged path ⇒ fail. (minor) |
| **`TestSqlBrowse_OrdersOnTotalOrder`** | a table whose `cols[0]` has duplicate values across ≥ `limit` rows: page 1 then page 2 by offset ⇒ **no duplicated and no skipped row** across the two pages | **minor.** Order by `cols[0]` alone (v2) ⇒ an unstable order under OFFSET paging duplicates and skips ⇒ fail. |
| **`TestSqlBrowse_TenantColForcedIntoSelect`** | prod-scoped, `adminShow=["id"]`, `tenantCol="tenant"` ⇒ the emitted SQL selects `"tenant"` (not `'***' AS "tenant"`), and the rendered row shows `acme` | **M-9 mitigation.** Let `adminShow` redact the tenant column ⇒ the post-filter has nothing to check and the operator cannot see what matched ⇒ fail. |
| `TestBrowseSlotReleasedOnPanic` | `acquireBrowseSlot()`'s `release` runs from a `defer` in a panicking closure; 5 sequential panics then a 6th normal acquire succeeds within 1 s | Release non-deferred (exp's `:85`) ⇒ the 5th blocks ⇒ timeout ⇒ fail. **The seam exists by construction** (v1's version needed a panic seam that did not exist). |
| `TestIntrospectionUsesBrowseConn` | an instrumented `SkyDb` counts app-pool queries during a browse ⇒ **0** | Call `codecTableColumns(d, …)` before `BrowseTx` (exp, `:278` vs `:316`) ⇒ ≥1 ⇒ fail. (**M13**.) |
| `TestCellTruncatedAt512` | a 1 MB `TEXT` cell ⇒ ≤ `MaxCellBytes` + marker | No cap (exp) ⇒ fail. (**M12**.) |
| `TestBrowseIsReadOnly` | attempt an `UPDATE` through the browse tx ⇒ error on both sqlite and pgx | Drop the pragma/`ReadOnly` ⇒ succeeds ⇒ fail. |

### 7.9 Leg 3 — real use, and the automated artifact that keeps goal #5 closed

**A manual browser checklist is not a gate.** Three automated artifacts do the closing:

1. **`TestIntegration_ConsoleRendersScopedRows`** (`rt`, the strongest single test): boot the
   **real** generated `console_app` Live app via `MountEmbeddedConsole`, drive
   `handleInitial`, dispatch the `SelectTab DataTab` event, run the resulting
   `Cmd.perform`, and assert the rendered HTML **contains** an acme row's declared field value,
   **does not** contain any globex value, and **does not** contain the value of an undeclared
   field of an acme row (the M-4 leg, asserted on the *rendered page* rather than on any
   intermediate type). This is the only automated leg that exercises the Sky↔Go coercion
   (`[]map[string]any` → `[]State_DataRow_R`, **including the nested
   `[]any`→`[]State_DataField_R`**) that unit tests structurally cannot — i.e. the
   `CoerceFailure` class. *Mutations:* rename a map key in `Data_rows` ⇒ coercion fails ⇒ fail;
   flatten `dataFields` back to a single `dataValue` string ⇒ the undeclared value appears in
   the HTML ⇒ fail.
2. **`scripts/example-sweep.sh`** gains a `script`-kind assertion on
   `examples/59-persist-live` that curls `/_sky/console/` and greps for the Data tab button
   (the `View.sky:196` silent-omission class end to end). The sweep already gates examples
   56–59 (`ca67d0f8` on the sweep-gating worktree).
3. **CI drift gate** (§8-C11): `git diff --exit-code runtime-go/rt/console_app/` after a
   regeneration step — today `regenerate-console.sh:386` only *prints* the hint, and grep of
   `.github/workflows/` + `rust/crates/xtask/` finds no gate.

The **manual browser pass** is retained as **evidence**, not as the gate:

- **A** — `ENV=dev` + `SKY_CONSOLE_DATA=all` ⇒ unscoped, every field rendered.
- **A′ (new, B-2)** — `ENV` **unset** + `SKY_CONSOLE_DATA=on` ⇒ the Data tab is **empty** while
  the other six tabs render normally. This is the pass that would have caught v2.
- **A″ (new, B-2)** — `ENV=dev`, `SKY_CONSOLE_DATA` **unset** ⇒ Data tab empty, telemetry fine.
- **B** — prod token, no data env ⇒ empty + the other six tabs render.
- **C** — + `SKY_CONSOLE_DATA_TENANT=acme` ⇒ acme only, **and the `tenant` column is visible on
  every row** (M-9 mitigation), **and** a collection whose `adminShow` omits the PK shows `***`
  for the key while row-detail still opens (M-7).
- **D** — `SKY_CONSOLE_DATA=off` ⇒ empty.
- **E** — zero `CoerceFailure` and no dropped session across Overview→Data→Logs→Data→Traces.
- **E′ (new, M-1)** — sign in, load 10 pages of a gated app whose gate stamps an `exp` claim;
  the Model survives all ten (no rotation storm).
- **F** — the SQL section shows an opaque `src-…` handle and `***` cells.
- **G (new, B-5)** — the **Sign out** link works (no 405) at every commit from C2 onward, and
  after it the previous session is gone server-side (a stale tab's next event gets a reload).

---

## 8. Commit ordering, and the G1 dependency

**G1 is a hard blocker for the real-use leg.** `docs/bluedb/g1-reactive-deadlock-fix-design.md`
documents a self-deadlock on **every initial page load of every reactive Sky.Live app**:
`handleInitial` holds `sess.mu` (`live.go:4176`) → `setupSubscriptions` (`:4183`) →
`reactiveEnsureStartedHook` (`:5492`) → `ensureReactiveStarted` re-acquires `sess.mu`
(`bluedb_reactive.go:149`). `examples/59-persist-live` — the app §7.9's browser and sweep legs
use — hangs. The fix is designed and in flight in a worktree.

**PREREQUISITE — C0 is not part of this design, and this design does not start without it.**

> **C0** · `fix(live): bind /_sky/event's body sessionId to the sid cookie` · **F, security**
>
> `handleEvent` reads the session id from the request **body** (`live.go:4353-4355`) and calls
> `app.store.Get(req.SessionID)` (`:4398`) with **no cookie comparison**, while `handleSSE`
> does read the cookie (`:6142-6148`). The sid is templated into the page
> (`live.go:6953`), and the double-submit CSRF pair (`:3831`, `:7724`) proves same-origin, not
> same-session. Fix: compare `req.SessionID` to the sid cookie and answer
> `404` + `X-Sky-Live: 1` + `X-Sky-Status: session-lost` on mismatch.
>
> **This is being shipped by a separate agent as its own security fix**, with its own review and
> its own regression test, because it is a Sky.Live vulnerability that exists independently of
> the console and must not wait on a console feature. **Every commit below assumes it has
> landed.** §2.3-5's principal check in `handleEvent`/`handleSSE` ships here regardless — the two
> are orthogonal (C0 binds *which session a request may address*; §2.3 binds *which operator a
> session may serve*) and `TestIntegration_EventPathCannotDriveAnotherPrincipalsSession` (§7.7)
> requires **each** to fail the test on its own.

**Dependency, stated precisely:** C1–C10 are Go/Rust/Sky commits verified by `go test`,
`cargo test` and the integration tests, and **none of them needs G1**. §7.9's artifacts 1–2
and the manual pass **do** need G1 merged. Ordering below reflects that; nothing is blocked
on G1 that need not be.

**P** = port · **N** = net-new · **F** = defect fix.

| # | Commit | Kind | Touches | Verified by | Depends on |
|---|---|---|---|---|---|
| **C0** | *(separate agent)* `fix(live): bind /_sky/event's body sessionId to the sid cookie` | **F, security** | `live.go:4353-4398` | its own regression test + `TestIntegration_EventPathCannotDriveAnotherPrincipalsSession` | — |
| **C1** | `fix(console): fail-closed identity when the consoleAuth callback errors` | P (exp `1a19aeca`) | `console_auth_v2.go:~556` | `TestInvokeConsoleAuthCallback_ErrDenies` (**fails today**) | — |
| **C2** | `fix(console): logout/login clear all three cookies + evict the Live session server-side` | F | `console_auth_v2.go:707-712,874-877`, `console.go:346` | `TestLogout_ClearsAllThreeCookiesAndEvictsSession`, `TestLogout_SameSiteGETAlsoEvicts`, `TestLogout_CrossSiteGETRefused`, `TestLogin_RotatesSubAppSid`, `TestLogin_EvictsPreviousOperatorSessionServerSide` (**all fail today**) | — |
| **C3** | `fix(live): a session may only be resumed for the principal it was minted for` | **F (B1)** | new `live_principal_reconcile.go`; `live.go` — reconcile **inside** the lock at `:4043-4047`, the `defer` closure, the `:4100-4110` hoist, the `persistSession` funnel over `:4213/4435/4575/5345/6301`, and the `handleEvent`/`handleSSE` checks | `live_principal_reconcile_test.go` (all) | C0, C2 |
| **C4** | `fix(console): carry the internal Bearer on every console→parent fetch` | P/F | `Main.sky` ×6 + `authGet`; regen + rebuild | `TestIntegration_SixTelemetryEndpointsAuthorize` | — |
| **C5** | `fix(bluedb): registry is copy-on-write and write-once; a resident schema is never replaced` | **F (M6 + M-5, root-cause)** | `bluedb/embedded.go:64-98` | `TestEnsureRegistered_RefusesWeakerSchema`, `TestResidentSchemaPointerIsImmutableForProcessLife`, `TestWatchTenantSubscriptionKeepsItsSchemaAcrossReregistration`, `TestIndexerAndResolverReResolvePerCall`, `TestRegister_DeepCopiesCallerSlices`, `TestSchemaOf_DeepCopies` | — |
| **C6** | `feat(bluedb): ScanPage — PK-ordered cursor scan with early exit + examine budget` | **N (M7 + minor)** | `bluedb/embedded.go`, `indexer.go` | `TestScanPage_StopsAtLimit`, `TestScanPage_HonoursExamineBudget`, `TestScanPage_CursorIsStableUnderConcurrentWrite` | C5 |
| **C7** | `feat(rt): skyenv tiers; consoledata funnel with a zero-argument Decide and positive arming` | **N (B3/B4/B5/B7 + B-2/B-4)** | new `rt/skyenv/`, new `rt/consoledata/`, new `rt/data_kernel_hooks.go` (**the one `Bind` site**), delete `rt/console_data.go` | `consoledata/*_test.go` (all), `TestSkyenvTierTable`, `TestExactlyOneBindCallSite` | C5, C6 |
| **C8** | `feat(console): v3 mode-bound cookie + per-request principal + explicit cookie secret` | **N (B2 + B-3)** | new `console_principal.go`; `console_auth_v2.go` ×9 sites incl. `consoleCookieSecret()` + the `randomDevToken` hard error | `console_principal_test.go` (all) | C1, C7 |
| **C9** | `feat(persist): declared tenantCol + adminShow threaded Sky→CollSchema; P.declare` | N | `Persist.sky` ×7, `embedded_kernel.go` ×2, `bluedb/backend.go`; `bluedb_reactive.go` warn fixes | parse tests; `sky check examples/59-persist-live` | C5 |
| **C10** | `feat(console): Data_* kernels (bluedb-free) + gated bluedb adapter + audit events` | **N (B6)** | new `data_kernel.go`, `data_kernel_hooks.go`, `bluedb_admin.go` | `TestIntegration_*`; `persist_gate_console_app_builds_without_persist` | C7, C8, C9 |
| **C11** | `feat(console): SQL enumeration + hardened browse inside the funnel (driver enum, no Rebind, post-filter)` | P + 7 fixes + **M-3** | new `console_data_sql.go`; `db_codec.go:145,~210` hooks; `Persist_declareSqlAdmin` | `console_data_sql_test.go` (all) | C9, C10 |
| **C12** | `feat(console): Data tab UI (read surface) + allTabs + POST sign-out + Tui/Hub arms + MainTui logoutUrl fix` | N+F | §6.1's 22 rows; new `DataStore.sky`, `DataTab.sky`; **`View.sky:142-148` GET anchor → POST form (B-5)**; `dataState` states (M-6, §3.5c, M-7); regen + rebuild | `TestConsoleAppRecordFieldsetsAreTypeUnambiguous`, `TestConsoleTabStripCoversEveryTab` | C10, C11 |
| **C13** | `chore(ci): gate console_app regeneration drift + the console/non-Persist build matrix; POST-only logout` | N | `.github/workflows/`, xtask, `console_auth_v2.go:874` | the gate fails on a deliberately stale `main.go`; `TestLogout_RejectsGET` (**newly valid only now** — the UI is a POST form from C12) | C12 |
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
| **R1** | **B1 principal swap** — a resumed session serves another tenant's operator. | §2.3 reconciliation + rotation **under the per-session lock**; the evicted session is marked dead and `persistSession` refuses it; all three cookies cleared **and the session evicted server-side** on both logout and login. | `TestReconcile_DifferentPrincipalRotatesSession`, `TestReconcile_ConcurrentEventCannotResurrectEvictedSession`, `TestLogin_EvictsPreviousOperatorSessionServerSide`, `TestIntegration_PrincipalSwapDoesNotLeak`. |
| **R1b** | **B-1 direct cross-principal drive via `/_sky/event`** — the body-supplied `sessionId` is never compared to the sid cookie (`live.go:4353-4398`) and the sid is public (`:6953`); CSRF is double-submit (`:3831`) and does not bind the session. | **Two independent legs**: C0 (prerequisite, separate security fix) binds the transport; §2.3-5 binds the principal on `handleEvent` **and** `handleSSE`, answering the shipped `X-Sky-Status: session-lost` contract (`:7752-7760`) so no client change is needed. | `TestEventPathRejectsChangedPrincipal`, `TestSSEPathRejectsChangedPrincipal`, `TestIntegration_EventPathCannotDriveAnotherPrincipalsSession` — which requires **each leg independently** to fail the test. **v2 filed this as a "revocation timing window" (§2.9). It is a direct path, and that framing would have shipped it.** |
| **R2** | **Reconciliation rotation storm** — a gate that stamps volatile claims (`exp`/`iat`/`nonce`) churns every session on every request, wiping the Model on every page load of a working app. | Fingerprint over `Subject` + `Email` + an **explicitly declared authorization-relevant claim subset** (`authzRelevantClaims`), **not** the whole map — sorting fixes ordering, not volatility; the `everStampedIdentity` latch qualifies row 3 so mixed public/gated-route apps do not rotate; no-identity-either-side is an explicit no-op. | `TestReconcile_VolatileClaimsDoNotRotate`, `TestReconcile_MixedRouteAppDoesNotRotateOnPublicRoute`, `TestReconcile_NoIdentityEitherSideIsNoOp`, `TestReconcile_ClaimOrderDoesNotRotate`, `TestAuthzClaimSubsetMatchesConsoledataReaders`. **Highest blast-radius change in this design** — `IdentityContextKey` is exported and generic (`session_identity.go:36-43`) and the hub stamps it on every gated request (`hub/app_auth.go:127-129`), so this touches shipped third-party apps, not just the console. |
| **R3** | **B2 cross-mode cookie acceptance.** | Mode enters the HKDF `info` (MAC fails) **and** `Mode` is checked inside the MAC'd payload. | `TestCookieV3_WrongModeRejected`, `TestCookieV3_KeyDerivationDiffersPerMode`. |
| **R4** | **Claim synthesis at verify time** re-promoting a claimless cookie. | Envs read at **mint** only; verify never touches them. | `TestCookieV3_NoClaimSynthesisAtVerify`. |
| **R5** | **B3 `!prod` shortcut** giving a scoped operator every tenant. | Tenant branch is **above** the dev shortcut in `Decide`. | `TestDecide_ScopesBeforeProdShortcut` (**fails today**). |
| **R5b** | **B-2 forget-one-variable full disclosure** — with `ENV`/`SKY_ENV` unset, `productionFromEnv()` is false (`observability.go:314-324`), the dev-open arm returns true unconditionally (`console_auth_v2.go:443-452`), `SKY_CONSOLE_DATA` unset meant on, `!ok && !prod` meant allowed/unscoped/`DiscloseAll`, and the weak-key refusal did not fire. **Every row of every tenant, unauthenticated and unredacted.** | **Positive arming only** (§2.6): `SKY_CONSOLE_DATA ∈ {on, all}` required in every tier, and the unauthenticated arm requires `skyenv.IsExplicitDev()` — **unset is the production tier for the data plane** (§2.13). `DiscloseAll` needs both signals. Neither has a permissive default; there is no state reachable by omission. | `TestDecide_EnvUnsetIsNotDev`, `TestReadsDisabledWhenSkyConsoleDataUnset`, `TestDiscloseAllRequiresBothSignals`, `TestIntegration_EnvUnsetDataPlaneIsInert` (which **also** asserts the six telemetry tabs still 200 — the fix must not cost zero-config observability). |
| **R6** | **B4 fabricated decision.** | `Decide()` takes zero arguments; the tier is internal; principal via a bound `Source`; `Bind` is `sync.Once` first-wins with a loud duplicate log; an unbound `Source` denies in **every** tier. | `TestDecide_TakesNoTrustArguments`, `TestBind_SecondCallIsIgnoredAndLogged`, `TestExactlyOneBindCallSite`, `TestDecide_WithoutBindDeniesInEveryTier`. |
| **R6b** | **B-4 init panic** — two `Bind` sites in one package (v2 shipped both) would panic at startup in every Persist app with a console. | Exactly one `Bind` site, in the ungated `data_kernel_hooks.go`; the gated file only assigns `kvAdminSourcesHook`; `Bind` never panics. | `TestExactlyOneBindCallSite` (source grep), `TestBind_SecondCallIsIgnoredAndLogged`. |
| **R7** | **B5 SQL read outside the funnel.** | `BrowseSQL` builds every statement **and every placeholder** inside `consoledata`; `Rebind` is deleted; `tenant` has no exported accessor. | `TestSqlBrowse_ScopedBindsTenantAsParam`, `TestSqlBrowse_NoRebindHookExists`, `TestSqlBrowse_UnknownDriverRefuses`. |
| **R7b** | **M-3 SQL scope lost through an `rt`-supplied parameter** — a wrong `TenantColOf` produces a valid `WHERE` over the wrong column and v2 had nothing downstream to catch it. | Introspection-verified `tcol`; the tenant column forced into the SELECT list; **row post-filter** discarding the whole page on any mismatch — the SQL analogue of `ReadRows`', so R15's "three independent guards" is true of both planes. | `TestSqlBrowse_WrongTenantColIsCaughtByIntrospection`, `TestSqlBrowse_PostFilterDiscardsWholePageOnViolation`, `TestSqlBrowse_TenantColForcedIntoSelect`. |
| **R8** | **B6 build break** — non-Persist app + console ⇒ `undefined: rt.Data_*`. | Hook seam (`live_reactive_hooks.go` precedent); kernels bluedb-free. | `persist_gate_console_app_builds_without_persist` (a real `go build`). **This is the risk v1 would have shipped.** |
| **R9** | **B7 secret disclosure** — deny-list misses `iban`/`stripe_sk`/`dob`. | Outside explicit-dev, an explicit `adminShow` allow-list; server-side `'***' AS` on SQL. | `TestSqlBrowseRedactsUndeclaredInProd`, `TestReadRows_DeclaredDisclosureRedactsUndeclared`. |
| **R9b** | **M-4 the KV plane leaks the whole record through the Sky layer** — v2's `dataValue : String` carried the stored codec JSON into the Model, where it reaches the HTML, the logs and the session store, and is redacted only afterwards. | `Page.Row.Fields` is the **only** row representation; the kernel payload is built from it; `dataValue` is deleted in favour of `dataFields`; the KV guarantee is restated as *"never leaves the process"* (the socket claim is SQL-only). | `TestReadRows_UndeclaredValueAbsentFromKernelPayload` — asserts on the **marshalled kernel payload**, not on `Page`, because v2's `Page`-level assertion could not see this class. |
| **R10** | **`adminShow` makes the tab useless outside dev** (nothing declared ⇒ handle-only). | Explicit-dev + `SKY_CONSOLE_DATA=all` stays the zero-declaration path; the empty state names the exact builder; Persist collections declare it in one line. The PK is no longer a free identifier either (M-7). | Browser pass C. **Accepted, explicitly:** a useful-but-leaky default is the wrong trade for a plane that discloses arbitrary app data — and M-7 removes the last unconditional disclosure, which v2 had kept in direct contradiction with §2.10's own "*keys are frequently emails*" justification. |
| **R11** | **M1 tenant-column poisoning / job-written rows invisible.** | Cannot be fixed at this layer for **reads** (the engine tag is non-durable by design). Mitigated by rendering the tenant column's value on every row so the operator sees what matched. | **Not a test — a documented trust boundary**, stated in the design, the UI footer and two docs; `TestReadRows_TenantColAlwaysInFields` + `TestSqlBrowse_TenantColForcedIntoSelect` pin the mitigation. |
| **R11b** | **M-9 — the same boundary is an AUTHORIZATION input for 5e-2's W1**: tenant A writes `tenant="B"`, B's operator is shown it, W1 authorizes B to write it. | **None available at this layer** — documenting bounds a *view*, not a *grant*. W1 is therefore **gated on the durable verified tenant** (§3.1, Phase 6), which moves from "nice to have" to 5e-2's critical path. | **A stop condition, not a test.** §11.3's Q1 escalation now carries this: a "writes required" ruling implies the Phase-6 engine item, and a 5e-2 that ships W1 on the current mechanism must be rejected by the Judge. |
| **R12** | **M2 blast radius** — an admin-only typo bricks the data path. | Validation lives in the admin path; verb path copies through; boot warns. | `TestReadRows_NullableTenantColRefused` + the absence of any validation in `parseEmbeddedSchema`. |
| **R13** | **Registry poisoning / stale `tenantCol`.** | `ensureRegistered` **never** replaces a resident schema and `Register` no longer overwrites; a resident `*CollSchema` is immutable for the process's life, so `WatchTenant`'s pinned pointer (`embedded.go:558`→`:563`) cannot go stale. Every registration path already carries the full declaration (§3.5a), so no upgrade path is needed. | `TestEnsureRegistered_RefusesWeakerSchema`, `TestResidentSchemaPointerIsImmutableForProcessLife`, `TestWatchTenantSubscriptionKeepsItsSchemaAcrossReregistration`. |
| **R13b** | **M-5 — v2's upgrade-on-stronger was a WRITE-PATH change to the engine motivated by an admin READ feature**, and it would have left a live subscription indexing against the pre-upgrade column set with no test covering it. | The upgrade is dropped entirely (see R13). Net effect on the engine: strictly *fewer* mutations than today, not more. | `TestWatchTenantSubscriptionKeepsItsSchemaAcrossReregistration` — the indexer/subscription test §7.6 lacked. |
| **R14** | **M7 memory DoS on a privileged path.** | `ScanPage` early-exits at `limit`; cursor paging, not offset. | `TestScanPage_StopsAtLimit` (instrumented iterator count). |
| **R14b** | **Time DoS on the same path** — early exit bounds memory but not work: a tenant with 3 rows in a 10 M-row collection still walks 10 M keys. | Rows-**examined** budget (`maxExamine`, default 50 000) with a resumable cursor and a `Partial` signal surfaced as its own UI state, so a sparse tenant browses in bounded steps instead of hanging. | `TestScanPage_HonoursExamineBudget`, `TestReadRows_PartialIsDistinctFromEnd`. |
| **R15** | **M8/M9 predicate silently lost or mistyped** (`CondTrue`==0; `valuesEqual` ignores `.Type`) — **on both planes**. | KV: adapter assertion + engine assertion + `ReadRows`' row post-filter. **SQL: introspection-verified `tcol` + forced tenant column in the SELECT + `BrowseSQL`'s row post-filter.** v2's three guards were KV-only and R15 overstated their reach. | `TestReadRows_PostFilterDiscardsWholePageOnViolation`, `TestScanTenant_RejectsCondTrueWithTenant`, `TestSqlBrowse_PostFilterDiscardsWholePageOnViolation`, `TestSqlBrowse_WrongTenantColIsCaughtByIntrospection`. |
| **R15b** | **M-6 — a scope violation is indistinguishable from an empty collection**, so one poisoned row bricks a collection forever with no operator-visible reason and (in v2) no audit event. | `console.data.scope-violation` at **error**, plus a distinct `DataScopeViolation` UI state. Deny keeps its neutral empty state; a scope violation discloses nothing about other tenants. | `TestSqlBrowse_PostFilterDiscardsWholePageOnViolation` (asserts the log event), and the §6.3 state table. |
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
  Consumer: `live.go:4100-4110`, **hoisted** out of the mint-only branch (§2.3). *Because that
  bridge is generic and hub-activated, every reconciliation rule is scoped by an explicitly
  declared claim subset + the `everStampedIdentity` latch (§2.3-1, M-1) — the blast radius is
  every gated Sky.Live app, not the console.*
- **Session revocation on the event/SSE paths without a client change** — mechanism: the
  shipped server-authored desync classification `X-Sky-Status: session-lost`, emitted today at
  `live.go:4407` and `:6160` and handled by the inlined client at `:7752-7760` with
  `window.location.reload()`. §2.3-5 reuses it, so the reload lands on `handleInitial` where
  rotation is the tested path. **This is why v2's "needs 401 handling, therefore a follow-on"
  reasoning was wrong** — the contract already exists.
- **Single-writer funnel over an N-site mutation** — mechanism: this branch's own
  persist-before-ack funnel (`9ad00daf`, "*dissolve the 6-site band-aid*"). Applied to the five
  `app.store.Set` sites (`live.go:4213/4435/4575/5345/6301`) as `persistSession`, which is what
  makes the reconcile's delete non-resurrectable (M-2).
- **Positive-signal arming for a privileged plane** — mechanism: `resolveConsoleAuthMode`'s own
  existing posture (`console_auth_v2.go:132-145`), where an **unset** `SKY_CONSOLE_AUTH` in
  production declines to mount rather than choosing a default, and an **unknown** value returns
  `consoleAuthModeOff` with the comment "*refuse to silently fall back to something more
  permissive*" (`:141-144`). §2.6/§2.10/§2.13 apply exactly that rule to the data plane, which
  v2 had not: it inherited `productionFromEnv`'s deliberately-permissive unset default
  (`observability.go:308-312`) into a plane that discloses arbitrary application data.
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
4. Outside an explicitly-declared dev environment, only `adminShow`-declared fields are
   disclosed; everything else is `***`, redacted **at SELECT time** on the SQL plane and
   **in-process before any payload** on the KV plane. The primary key is disclosed only when
   `adminShow` names it.
5. **A Live session cannot be driven by a different principal through ANY entry point** —
   `handleInitial` (rotate), `handleEvent` and `handleSSE` (refuse with the shipped
   `session-lost` contract) — and both logout **and login** clear all three cookies **and evict
   the session server-side**. *(v2 worded this as "cannot be resumed", which was true only of
   `handleInitial` and was falsified by `/_sky/event`'s body-supplied session id — see §2.3's
   B-1 box. The C0 prerequisite must have landed for this criterion to be verifiable at all.)*
6. No `rt` code can obtain an allowed decision without `consoledata.Decide()`, which takes no
   trust inputs and denies when unbound in **every** tier; no `rt` code constructs an admin row
   or table query, chooses a placeholder dialect, or sees a finished statement.
7. **There is no environment configuration reachable by omission that discloses a row.** With
   `SKY_CONSOLE_DATA` unset the plane is inert in every tier; with `ENV`/`SKY_ENV` unset the
   data plane is in the production tier; `DiscloseAll` requires two explicit declarations. The
   six telemetry tabs' zero-config dev behaviour is **unchanged**.
8. A non-Persist project **with the console enabled** builds (`go build ./...` on the
   materialised tree), and there is exactly one `consoledata.Bind` call site.
9. `go test -race ./rt/... ./bluedb/...` green; `cargo test --workspace` green;
   `scripts/example-sweep.sh` green; `git diff --exit-code runtime-go/rt/console_app/` clean
   after a regeneration; §7.9's three automated artifacts green.
10. Every deleted v1/v2 test (`TestDecision_ZeroValueIsDeny`,
    `TestConsoleAppNoRecordFieldsetCollision`, `TestQueryWithBareSchemaFindsNothing`,
    `TestAdminReadRows_ReadsSeededRows`, `TestBind_SecondCallPanics`,
    `TestEnsureRegistered_UpgradesStrongerSchema`) is gone, and each replacement fails under its
    stated mutation.

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
> exist in this compile unit** (§1.4) — so the write surface is a matter of the seven
> authorization/integrity requirements W1–W7, all of which are designed above, not of a
> compiler fix. C16+ implements W2–W7 on top of the architecture in §2 with no rework.
> **However — the cost estimate changed in v2.1 (M-9).** W1 cannot be implemented against the
> application-written tenant column: a tenant that writes `tenant="B"` hands B's operator write
> authority over its own row (§1.4/§3.1). W1 therefore requires the **durable verified tenant**
> — persisting `CommitReq.Tenant` into the MVCC value header at `committer.go:152/318` — which
> is an engine-format change guarded by `base.CheckComparer` (`skydb.mvcc.v1`, IRREVERSIBLE).
> So "writes required" is a ruling that pulls a **Phase-6 engine item onto the critical path**,
> not just a UI form. That is the honest price and it is stated before the decision, not after.
>
> **Q2 — Is the enumeration narrowing in §3.8 acceptable for 5e-1?**
> A collection that is non-reactive, non-SQL, never touched, and whose app never calls
> `P.declare` appears only on first use. The complete fix is a `rust/crates/lower` pass
> emitting a boot manifest — a compiler task, correctly separate from a console feature.

Neither question blocks C1–C15. Both are surfaced now rather than answered silently.
