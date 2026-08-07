# Phase 5 design grill — findings

Grill of `docs/bluedb/phase5-dx-collapse-design.md` @ `ec543c1d`. Two adversaries.
Design-only; **not implemented**. The high-risk sub-phases (5c session-store, 5d durability
funnel) do NOT proceed to code until these are folded. 5a (config) / 5b (`sky data` verb) are
low-risk and unaffected by Grill A — implementable first.

## Grill A — data-loss / corruption (5 BLOCKING + 1 NB)

### R1 (acked-then-lost) — the durability tier
- **A1 (BLOCKING) — the §5.3 heuristic ships acked-then-lost.** `runPerformBody` (`live.go:5317`) +
  `Time.every` (`live.go:5540-5610`) mutate Model + push to `sess.sseCh` and NEVER `store.Set` (R1
  confirmed). §5.3's rule (async→semantic, `onInput`→ephemeral, app-declares-the-rest) is UNDECIDABLE
  at the emit site: an `onInput UpdateDraft` autosave (note/comment/blog composer — the user expects
  the draft saved) defaults ephemeral → acked without `commit(Sync)` → crash → draft the user watched
  render is GONE. A bare synchronous `onClick DoTransfer` ("Place Order"/"Confirm Transfer" — common,
  not a form `onSubmit`) is in NEITHER bucket → if the unclassified sync default is ephemeral, a
  money-transfer commit is acked-then-lost. **FIX: ship a FIRST-CLASS app-facing durability marker
  (`Persist.durable`/explicit semantic-Msg declaration) in 5d — NOT the heuristic. The heuristic is
  undecidable and the design's own §8#1 admits it "is not defensible."**
- **A2 (BLOCKING, SQL session backends) — §5.4's durability proof only holds for the embedded
  committer.** Embedded acks only after `Apply(b, pebble.Sync)` (`committer.go:135/159,306/323`) —
  sound. But §5.2 extends the same ack contract to SQL sessions, and `sqliteStore.Set` runs under
  `PRAGMA synchronous=NORMAL` (`live_store.go:518`) → no per-commit fsync under WAL → an acked
  semantic transition is lost on host power-loss (durable only against a process crash). **FIX: a
  semantic transition on a SQL session backend must persist under `synchronous=FULL` (or an explicit
  fsync), OR the durability contract must be honestly backend-scoped + documented.**

### R9 (never-silent session-blob migration)
- **A3 (BLOCKING) — rolling-deploy NEW-blob→OLD-reader unhandled.** §2.3 only migrates OLDER blobs
  forward. In any rolling deploy on a shared store, a V2-written blob is read by a still-live V1
  instance → V1's `storableSession` has no `BlobVersion` field → gob ignores the tag → decodes S2
  Model into S1 type → decode error → `decodeSession` returns err → `Get`→(nil,false)
  (`live_store.go:578`) → fresh session minted → **user logged out / cart lost** = the exact R9 silent
  reset, in a window the design never models. **FIX: a version-prefixed envelope EVERY reader can
  gate on — an older reader refuses a newer blob (keep-alive / graceful) instead of corrupt-decoding;
  model the mixed-version window explicitly (sticky sessions + drain don't give atomic cutover).**
- **A4 (BLOCKING, worst) — gob-type structural hash is too coarse for gob-SILENT changes.** §2.4
  hashes field-name→gob-type. But ADT reorder (`Status=Active|Suspended|Closed`→insert `Pending`;
  nullary ADTs lower to int tags — same gob-type) and semantic int remap (`role:Int` 0=guest→admin)
  decode WITHOUT a gob error → hash never flips → NO migration, NO WARN → silent corruption +
  **privilege escalation** (guest reads as admin). `decodeSession` only errors on shape mismatch
  (`live_store.go:1391`), so there's no error to hang a WARN on. Refutes §2.6 "never silent again."
  **FIX: session-blob version must be a DEVELOPER-DECLARED explicit integer (bump on any Model
  semantic change), not an auto-derived structural hash — OR serialize the session Model via
  `Std.Codec` (explicit shape, version-checkable) instead of raw gob. Auto-hash cannot see
  gob-silent semantic changes; make versioning explicit + the envelope safe.**
- **A6 (BLOCKING, compound with A4) — coarse-hash + per-store forward-only + no rollback → wedged
  session.** App tables migrate to V2; a gob-silent session change never flips the hash → no
  per-session migration → V2 `update()` reads S1-shaped data → corruption/crash; forward-only (§2.6)
  means no rollback, silent means no recovery trigger → session wedged until manual cookie clear.
- **A5 (non-blocking, availability) — over-fine hash mass-logs-out on a benign additive deploy**
  (flips version on a no-op change with no declared migration → reset-to-init → every live session
  logged out). Recoverable by re-login.

## Grill B — compat / funnel-recurrence / auto-admin / phasing (4 BLOCKING + 3 NB)

- **B1 (BLOCKING) — `[data]` conflict-precedence is INVERTED.** §1.5 claims "[data] pushed LAST wins",
  but `SetSkyDefault`/`SetEnvDefault` is SET-IF-UNSET = FIRST-writer wins (`lower.rs:785-786`,
  `env_prefix.go:107-108`), and `read_sky_toml_config` fills `extra_defaults` in FILE LINE ORDER
  (`build.rs:810-874`). So a mixed `[data]`+`[database]` manifest keeps whichever section is typed
  first — `[database]` above `[data]` → LEGACY wins, `[data]` silently ignored (opposite of the
  contract). **FIX: 5a pushes `[data]`-derived defaults FIRST (before scanning legacy sections), or
  special-cases the suffix, so `[data]` genuinely wins. The WARN must match reality.**
- **B2 (BLOCKING) — auto-admin tenant gate is fail-OPEN.** The reused v0.16.6 pattern returns ""
  for a no-tenant-claim session (`hub_bridge.go:539-548`) and `rejectCrossTenantSvc(_, "")` returns
  IN-SCOPE ("no tenant claim → every svc in-scope", `:559-564`) → a super-admin / no-claim session
  reads ALL tenants. AND the hub's `LIKE service_name` filter (`hub_bridge.go:112-114`) has no
  analogue for user collections (no `service_name` column) → the row filter for real admin tables is
  UNSPECIFIED. **FIX: the admin surface INVERTS the gate (no verified tenant → fail-CLOSED, show
  nothing) + a concrete per-collection tenant-column row filter (or engine `WatchTenant`-tag scope)
  for user tables. Auth behind `SKY_CONSOLE_AUTH`.**
- **B3 (BLOCKING) — the R1 funnel is NOT structurally enforceable in Go.** `emitFrame`/`applyModelDelta`
  don't exist; the real emit is `fanOutFrame` on `*liveSession`, called from ≥3 sites (`live.go:4216,
  4587,6622`). Go has no intra-package access control → a lowercase funnel stays callable everywhere →
  §5.4's "structural guarantee" is convention, and §8#3 admits it. **FIX: make the emit surface real
  encapsulation — move the sse-emit + persist into a SEPARATE Go package whose only exported entry is
  the funnel (so an ack-before-persist bypass won't compile), OR a CI grep-gate asserting
  `fanOutFrame`/`sseCh`-write callers are exactly the funnel. Not convention.**
- **B4 (BLOCKING) — 5b depends on 5c → dishonest decomposition.** `sky data status`'s session-blob
  column needs `BlobVersion` on `storableSession`, which 5c adds (5b runs before 5c). **FIX:
  re-sequence so `BlobVersion` + the envelope land in the sub-phase that first claims session status,
  or scope 5b's status to app+analytics only and move session status to 5c honestly.**

- **N1 (NB) — deprecation-WARN noise** (~9 examples `[database]`, 10+ `[live] store`, 1 `[analytics]`):
  emit ONCE per project, not per-section-per-build. `build.rs` has no warn infra today (net-new).
- **N2 (NB, confirmed live) — the Phase-4 reactive gate's `[data] reactiveScope` hint is a LIE until
  5a.** The gate reads raw `SKY_DATA_REACTIVE_SCOPE` (`bluedb_reactive_gate.go:159`) but its fatal
  message tells operators to set `[data] reactiveScope` (`:172`), which `build.rs` doesn't parse. So
  5a RETRO-ACTIVATES an already-shipped fatal gate's documented setter — a real Phase-4/5 coupling.
  **5a MUST wire `[data] reactiveScope` → `SKY_DATA_REACTIVE_SCOPE` so the hint becomes truthful.**
- **N3 (NB) — "PORT" framing overstates reuse:** `SKY_CONSOLE_DATA`, `live_store_bluedb.go`,
  `DataTab.sky` are net-new (only in the ref worktree / docs), not ports. Frame honestly.
- **Positive (not findings):** single-owner/mutex/fan-out invariants hold below the store layer;
  `chooseStore` fails loud on unknown kinds in prod; the edit form is correctly excluded from the
  read-only floor.

## Consolidated revision direction (v2 — all 9 blocking)
- **Config (B1/N2):** push `[data]` defaults FIRST (first-wins semantics) so `[data]` genuinely
  overrides legacy; wire `[data] reactiveScope`→env (makes the Phase-4 gate hint truthful); WARN once
  per project.
- **Durability (A1/A2):** first-class `Persist.durable`/semantic-Msg MARKER (not the undecidable
  heuristic) + backend-honest durability (SQL semantic writes under `synchronous=FULL`/explicit fsync,
  or documented backend scope). No acked-then-lost.
- **Session-blob (A3/A4/A6):** EXPLICIT developer-declared blob version (or `Std.Codec`-serialised
  Model) + a version-prefixed ENVELOPE every reader gates on (older reader REFUSES a newer blob —
  keep-alive, never corrupt-decode) + explicit mixed-version rolling-deploy handling. KILL the
  auto-structural-hash (can't see gob-silent ADT-reorder / int-remap → priv-esc).
- **R1 funnel (B3):** structural encapsulation (separate package) or a CI caller-gate — not convention.
- **Auto-admin (B2):** admin surface fail-CLOSED on missing tenant + concrete per-collection row
  filter + `SKY_CONSOLE_AUTH`. Read-only floor (edit gated on the goty.rs fieldset-collision bug).
- **Phasing (B4):** re-sequence so every sub-phase is genuinely independently shippable+verifiable.
- **Sequence:** revise design v2 → re-grill the new mechanism → implement 5a (now B1/N2-correct) first.
