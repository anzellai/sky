# Autonomous mandate — config/withX overhaul + perf (2026-08-18)

VERBATIM USER DIRECTIVE:
"we need the sky.toml + withX overhaul fully closed + perf done."
Sliding session: "the sliding session should expand to non Auth sessions too";
auth-token intent: "bump the TTL whenever a user interacts, so auth'd sessions
continue working if user has interactions, rather than hard cut off from the
first interaction."
Mode: "fully autonomous + agents + grill + PIV mode."

(Supersedes the completed v0.17 compiler-close mandate; git preserves that history.)

## Scope (from .claude/TELEMETRY_STORAGE_PLAN.md follow-up list)

HEADLINE (user-emphasised "fully closed"):
  A. sky.toml + withX config overhaul — stages 1-3 + withX builders SHIPPED on
     main; MISSING: legacy->new format migration/converter, full withX as a
     complete user-facing sky.toml replacement, upgrade path printed on
     sky run/build when legacy config detected. "phase 1 of N" -> close it.
  B. Perf — render-path / model-diff selective render. Diff the MODEL, static
     map of which view subtrees read which fields, re-render only dirty regions
     (sound because view is pure). The framework benchmark identified this as
     THE lever: Django took the interaction ~2x because it renders 1 fragment
     where Sky re-renders 30 posts + diffs.

SECONDARY:
  C. Sliding Std.Auth JWT re-issue (rolling, ABSOLUTE-CAP) + 30-day(csrf)/
     30-min(live) TTL default reconciliation. Non-auth sky_sid ALREADY slides
     (verified live_store.go:499/507/554/715) — add the regression gate that an
     interacting session never expires.
  D. Telemetry storage P1-P3 (size report, metric aggregation; the 2 live bugs
     already merged on main).
  E. runPerform concurrency bound (live.go:5786) — dependent-perform deadlock +
     must-not-serialise-parallel-fetch constraints; NOT a drop-in.
  F. Cosmetic: coerce-floor --bless drops golden header comments; freshness gate
     mtime for Rust sources; --verify-falsifiers T2-T4; gc_tuning.go:101 number.

## Method (INVIOLABLE per CLAUDE.md §0)
- PIV per phase: Architecture-Consult -> adversarial grill -> implement ->
  Judge (fresh context, verifies the LITERAL claim). No "done" without Judge PASS.
- Phase boundaries commit + verify. Milestone verification MUST include
  coerce-floor (the gap that slipped CI last wave). Local green != CI green until
  build.sh + workspace + sweep + conformance + harness --tier t1 + coerce-floor
  all pass, on a log confirmed to be THIS run (noclobber/case-insensitive trap).
- Don't bloat one branch: merge coherent phases; split a branch if it grows.
- Grill every phase. The recurring class this session found is "the check
  measures a proxy for the thing." Assume each phase breaches.

## CONFIG SCOPE DECISION (user, 2026-08-18)
"withX full overhaul, and if user uses legacy sky toml config, print migration
LIST on console."

=> FULL Sky.Config cross-cutting builder module (withDatabase/withSessions/
   withJobs/withLog/withCsrf/withTelemetry) + Sky.Env + second entry point.
=> Legacy sky.toml detected => print the migration LIST on console (build-time
   AND runtime startup): which keys move to which withX builders.

MANDATORY SEQUENCING (grill found the crux BROKEN — do not skip):
  CRUX-FIX FIRST: the precedence is INVERTED as designed. Legacy seeds run in
  Go init(), ApplyConfig in main(), init-before-main + set-if-unset => legacy
  seed WINS over withX (the reads-and-discards defect). BEFORE any Sky.Config
  builder ships:
   1. ApplyConfig must be SEED-AWARE — clear-and-override a seeded value, defer
      to an operator value via isSeededDefault (mirror resolveLivePort), NOT
      plain set-if-unset. Reconcile with the existing configLayers
      (live_config_precedence.go:51-66) so Sky.Config.withX and Live.withX
      cannot disagree — one precedence, not three.
   2. DCE/entry-point trap (G2): a `config` binding not referenced by main is
      pruned by DCE (lower.rs:613 roots at main only) => silent no-op. config_def
      must be added to the DCE work-list + a second discovery pass.
   3. REAL end-to-end oracle (G5 fix): config-matrix is 4/111 — a proxy. Need an
      actual app serving a request under legacy-vs-migrated config producing
      identical output. Judge bar is behavioural, not census-green.
  Grill verdicts: G1 BREAKS(precedence) G2 UNPROVEN(DCE) G3 HOLDS(auth-del safe)
  G4 BREAKS(migrate self-proof proxy) G5 BREAKS(4/111) G6 was over-scope — user
  chose full scope anyway, so the crux-fix is now REQUIRED not optional.

## PROGRESS (2026-08-18, later)
CONFIG fully closed + PERF render levers + docs sweep + CSRF bundled-app isolation
(user-found) all merged on feat/config-perf-followup @ e81ce2fd, milestone green.
USER DECISIONS: (1) HOLD the PR — do secondary tracks on THIS branch, one big PR
later. (2) Next: SLIDING AUTH TOKEN (security).
Remaining after auth: telemetry storage P1-P3, runPerform bound, (maybe) kernel-ABI
perf lever (separate risk decision).

## SLIDING AUTH — design grilled, decisions locked (2026-08-18)
Opt-in only (automatic infeasible: runtime can't know app's auth cookie/secret — VERIFIED).
Grill found 2 ship-blockers:
 - G2 (revocation): USER CHOSE optional per-user revocation hook consulted at
   RE-ISSUE time (revokedCheck : sub -> Task Error Bool). Makes long-cap sliding safe.
 - G4 (SameSite downgrade): builder-owned cookie setter at BOTH login + re-issue
   (single source, can't drift), default SameSite=Strict. Middleware CANNOT read
   attrs off the request (browsers don't send them) — so login must use the builder's setter.
Required gates (grill): idle-timeout real (verify-first-bail before re-issue, db_auth.go:1942);
 builder REJECTS window>maxLifetime; fail-CLOSED on malformed/missing aexp/iat/exp; carry
 window as its own signed claim `w` (avoid silent shrink near cap); wrong-cookie-name fails
 closed (no 500, no stray mint); SSE caveat (slides on interaction POST not SSE heartbeat) —
 document. Exposure delta (stolen token: window->aexp on continuous use) documented in
 docs/skyauth. Layer-2 flow tests (stolen/expired/stale-claim/downgrade) — coerce-floor is
 structurally blind to these.

## revokeUser — GRILLED, design reshaped (2026-08-18). DO NOT build as first designed.
Grill BREAKS: G1 silent-miss (sub has 3 uncoordinated reps; signToken does NOT stamp
sub — app-supplied; %v on float64 misformats ids>=1e8 -> revoke misses silently),
G2 Live-kill INFEASIBLE on current store (event path is sid-keyed, no JWT/iat/sub;
only ConsoleIdentity known, app user is in opaque model gob; no user->session index),
G3 prune footgun (app maxTTL<real TTL resurrects), G4 name misleads (kill-existing !=
lock-out; compromise wants lock-out).
REQUIRED reshape:
 1. canonicalSub(any)->string helper used on BOTH write + middleware read (float64 via
    strconv.FormatInt never %v; reject non-integral/oob). revokeUser takes STRING sub,
    not Int (else can't express OAuth subs).
 2. Split into THREE named pieces:
    - Auth.invalidateSessions / revokeSessions = kill-existing (revoked_at epoch,
      checked at token re-issue/verify). Needs the token to CARRY a sub (tie to
      signSlidingToken including sub).
    - Auth.disableUser = lock-out (users.disabled flag checked in login before pw
      verify, db_auth.go:2035-2062). This is what actually stops re-login.
    - Live-session active kill = INFEASIBLE without a session-schema change (app-identity
      field on liveSession/storableSession + user->sid index + DeleteByUser + Broker
      cross-replica). SCOPE DECISION for user.
 3. No auto-prune (bounded by revoked-user count; TTL-parameterised prune is a footgun).
 4. Gates must include numeric-sub + id>=1e8 cases (existing fixtures are all small
    string subs -> would be green while broken).
PENDING: sliding-auth IMPLEMENTATION landing first (it owns the revokedCheck hook +
whether the sliding token carries sub). Then synthesize + user decides Live-kill scope.

## revokeUser SCOPE — user corrected the infeasibility (2026-08-18)
USER: "follow 1 & 2, but you CAN derive the session, as you Auth the user session
you know what user it is for the session, then just need to remove the session and
response to user for the state."
=> The grill's "Live-kill infeasible" was about REVERSE lookup on the current store.
   The unlock: bind userId<->session AT AUTH TIME (the app has both; it declares it
   once — the runtime need not guess). Then revokeUser removes the user's sessions.
BUILD ALL THREE:
 1. invalidateSessions/revoked_at token kill (plugs into shipped revokedCheck hook).
 2. disableUser lock-out (users.disabled flag checked in login pre-pw-verify).
 3. Live-session removal via auth-time binding: an app API tags the session with the
    userId at login (app provides it); session store gains a userId index; revokeUser
    Delete()s the user's sids + session-lost response. Cross-replica: the in-mem
    memCache pointers on OTHER replicas must drop too -> the pub/sub Broker fan-out
    (live_store.go Broker). String subjects (JWT float64 floor >2^53, Judge-confirmed).
Design+grill the session-binding + cross-replica invalidation BEFORE touching the hot
path (sky_sessions is the runtime's hottest table).

## revokeUser DESIGN — grilled, 2 BREAKS fixed into the impl (2026-08-18)
G1 BREAK (unbound=silent no-op footgun): gate keys off sess.userID set only by an
app one-shot; forgotten/OAuth/pre-feature sessions -> userID="" -> revoke silently
misses. G2 BREAK (revoked session keeps mutating): gate only at handleEvent+SSE-connect,
but Cmd.perform/Time.every/subscriber/stream all reach app.dispatch->update ungated,
and writeSessionLost is browser-only (evicts nothing) -> server-side goroutines write
as the revoked user forever. G3 HOLDS (revoked_at off the blob, boundAt immutable,
fresh shared read). G5 leave sliding hook alone (slide-stopper; arity change buys nothing).
FIXES (grill-prescribed, use existing machinery):
 - Gate INSIDE the app.dispatch funnel (covers event/Time.every/perform/subscriber/
   stream in ONE place; verify they share the funnel, else gate each). On Revoked/
   Disabled -> EVICT: markDone -> sess.done retires every goroutine (they select on
   it, live.go:6440-6459/6618) + drop from store. NOT just a 404.
 - Add the gate to handleInitial (the initial authed-GET + init Cmds).
 - Bind at the per-request identity chokepoint (live.go:4658-4660) or a Std.Auth
   middleware that auto-stamps the app user; unbound-under-revocation = LOUD sky doctor
   lint + runtime warning, never silent Active. Sliding-auth apps auto-bind from sub.
 - revoked_at/disabled_at MUST NEVER be added to storableSession (test asserts blob
   has no revocation field, else G3 reopens).
 - disable check BEFORE verifyPassword (db_auth.go:2141).
REQUIRED gates (grill): (i) a BOUND-LESS session revoke still stops it; (ii) a
SERVER-INITIATED dispatch (live Sub.every / in-flight Cmd.perform) after revocation
mutates NOTHING. Without both, the suite certifies a hole it never touched. -race required
(session teardown is where the periodic-goroutine work found races).

## PROGRESS (2026-08-18, later still) — PR #187 MERGED
Config+perf+security block MERGED to main @ e70e2310 (merge commit, 40 commits
preserved). NOT tagged (user owns tag scope). CI fully green incl. sharded
repro/repro-2 (T1 budget cleared by sharding, not by raising the ceiling).
feat/perf-security-config remote branch deleted.

NOW ON: feat/telemetry-storage-runperform (off merged main e70e2310).
Remaining tracks D + E (see TELEMETRY_STORAGE_PLAN.md):
  D. Telemetry storage P1-P3:
     - P2 two live bugs (analytics_store recover/silent-swallow +
       telemetry/persist mirror + console_analytics unbounded SELECT/indexes)
       — VERIFY whether already merged on main (plan flags CHECK).
     - P1 size report (pg_total_relation_size per runtime table on existing
       prune timers; warn on ratio of free space; free space known only --embed).
     - P3 aggregate the metric write losslessly over ~10s window (root cause:
       msg_logging writes 2 metric rows/interaction unconditionally). Window is
       a knob; 0 = raw as today. P4 partition only if P1 shows metric still binds.
  E. runPerform concurrency bound (live.go) — unbounded per-Cmd.perform goroutine.
     Must handle dependent-perform deadlock + not serialise parallel-fetch.
     Sound: release-on-done w/ deadlock analysis OR global worker pool decoupled
     from session. NOT a drop-in.
Method: PIV per phase (arch-consult=verify current code vs stale plan lines,
grill, implement, Judge). Milestone MUST include coerce-floor (the gap that
slipped CI last wave). These are runtime-Go changes, not compiler-floor.

## PROGRESS (2026-08-18, evening) — Tracks E + D implemented, milestone pending
feat/telemetry-storage-runperform, 3 phases committed LOCALLY (not pushed):
  0a1a2c25 E: runPerform concurrency bound (per-session+global sem, M1-M5,
            falsifier proven, deadlock-free, -race green)
  902bb527 D-P1: hourly DB size report (dialect-aware, statfs unix+stub, log
            event not metric row, -race green)
  455e378d D-P3: opt-in counter coalescing SKY_TELEMETRY_AGGREGATION_WINDOW
            (default 0=off; counters only; force-drain flushReq+stop; histogram
            floor documented blocked-on-external; -race green)
P2 was already merged on main (verified). Docs: observability.md updated.
MILESTONE verification RUNNING: full runtime-go/rt -race + example sweep +
conformance + coerce-floor. Then Judge (fresh ctx, literal-claim verify).
OPEN USER-DECISION (surface at milestone, non-blocking): default the counter
window to 10s? lossless for counters, only reduces SkyDeploy graph resolution
<10s. Recommended: yes for production, ship default-0 now.
HISTOGRAM FLOOR: sky_live_msg_seconds per-interaction persist needs a
bucket-vector schema coordinated w/ SkyDeploy — future cross-repo item.

## JUDGE VERDICT (2026-08-18) — ALL PASS
Fresh-context adversarial Judge: CLAIM E PASS, CLAIM P1 PASS, CLAIM P3 PASS,
OVERALL ALL PASS. Independently re-ran the E falsifier (removed per-session
acquire → RED "max in-flight 5 > cap 2", reverted clean). No hedge words.
Full runtime-go/rt -race suite green (0 races). Diff scope: runtime-go + docs
ONLY (no rust/, no .sky) → coerce-floor + compiler gates structurally
unaffected (justified skip). Remaining: rebuild+re-embed → example sweep +
conformance (integration), then push + PR.

## NEW SUB-MANDATE (2026-08-19) — histogram coalescing (full telemetry close)
User: "PIV and ensure solutions are accurate, correct and scalable +
maintainable + perf. please proceed." => implement lossless histogram
coalescing (the deferred half of P3) via cumulative bucket-vector rows per
window (Prometheus _bucket{le}/_sum/_count representation, schema-free), gated
behind the SAME SKY_TELEMETRY_AGGREGATION_WINDOW knob (default OFF). Extends the
P3 flusher pending-map + window ticker.
METHOD: full PIV. Phase0 Architecture-Consult (verify vs real code; find the
CANONICAL bucket representation already used for /_sky/metrics or OTLP export
and REUSE it, don't invent; resolve the reader coupling — do local console /
hub/store.go / SkyDeploy read raw rows or aggregated? does exploding require a
reader change, and is it in-repo or cross-repo?). Phase0b Grill (G1 false-neg /
G2 false-pos / G3 cost+cardinality scalability / G4 layering / G5
lossless-for-every-reader). Then implement + Judge (fresh ctx, literal claim).
CORRECTNESS BAR: reconstructed percentiles from the exploded rows must equal
what raw rows would yield (lossless). SCALABILITY BAR: rows/window bounded by
bucket_count × series_cardinality, NOT observation rate. MAINTAINABILITY: reuse
the P3 mechanism + the existing bucket representation; no parallel impl.
Default OFF preserves the external-reader contract until opt-in.

## HISTOGRAM COALESCING — SHIPPED + JUDGE ALL-PASS (2026-08-19)
a2e035b6. Full PIV: arch-consult (REVISE→separate opt-in), grill
(PROCEED-w/-mods: lossy-not-lossless + monotonic clamp + zero-alloc sink + name),
implement, Judge ALL-PASS 7/7 (falsifier empirically RED, tree pristine).
Full rt -race GREEN. 4 CI ratchets fixed proactively (env-reads, config-surface,
denominators, coverage-ledger). Pushed; CI full matrix running (32247715507).
Telemetry storage is now FULLY addressable: counters coalesce (lossless),
histograms coalesce (lossy bucket-resolution, separate opt-in), P1 size report
measures whether either binds. Remaining floor is purely a SkyDeploy cross-repo
reader update to consume _bucket/_sum/_count rows — out of this repo.

## PR #188 FULLY GREEN (2026-08-19) — histogram sub-mandate CLOSED
a1ae3aff. CI ALL JOBS SUCCESS. T1 budget cleared by splitting the sky crate into
test-sky (test-rest 375s + test-sky 485s parallel; chain well under 990). PR #188
now carries: perform bound + P1 size report + P3 counter coalescing + histogram
coalescing (lossy bucket-resolution, separate opt-in), all Judge-verified.
Awaiting user merge decision (no tag). Open user calls: counter-window default
(0 vs 10s), histogram-window rollout (needs bucket-aware reader), + the
SkyDeploy cross-repo reader update to consume _bucket/_sum/_count (out of repo).

## NEW SUB-MANDATE (2026-08-19) — P1 size report v2 (startup + capacity + tiers)
User spec: on startup AND hourly, tiered size/free/danger reporting:
 - SQLite / embedded / same-server PG: server free space + DB consumed + danger flag
 - External/remote PG: DB consumed (pg_database_size) + danger flag ONLY if a
   capacity is configured (can't statfs another machine's disk).
Defaults: SKY_TELEMETRY_DB_CAPACITY (human units e.g. 100GB); unset => remote =
size+growth only. "Same server" = runtime can reach the DB's files (sqlite file
or the embedded cluster it manages); localhost system PG = external. Danger =
free < consumed (owned-path) OR consumed > ratio×capacity (external+capacity).
Add total-DB-size (pg_database_size / sqlite file size) alongside the existing
per-telemetry-table breakdown. Run once at boot + keep hourly.
METHOD: full PIV. Arch-consult crux: can the telemetry persistence obtain the
EMBEDDED cluster data dir (today it has only the DSN)? + owned-path detection +
pg_database_size cost on shared pool + startup hook (EnablePersistence). Then
grill (capacity parse/edge cases, owned-path false pos/neg, startup timing,
double-report), implement, Judge. Remember: new env var trips
sky_env_reads + config-surface ratchets — handle proactively (see
[[ci_only_ratchets_env_and_census]]).

## SIZE REPORT v2 — SHIPPED + JUDGE ALL-PASS (2026-08-19)
ab06b015. Full PIV: arch-consult (PROCEED, embedded data-dir reachable via
supervisor cfg.dataDir), grill (PROCEED-w/-5-mods: own-recover startup, don't
bump prune timer, danger=free% not free<consumed, warn-loud on bad capacity,
stable page_count not WAL-sum), implement, Judge ALL-PASS 8/8 (crux item5
embedded plumbing verified). Full rt -race GREEN. 4 CI ratchets handled
proactively. Answers user's 2 questions: external DB near-full via
SKY_TELEMETRY_DB_CAPACITY quota (can't statfs remote disk); startup+hourly
cadence for immediate baseline. Pushed; CI full matrix running (32254483043).

## NEW SUB-MANDATE (2026-08-19) — withX coverage for system env vars
User principle (VERBATIM intent): "system/sky's reading env vars should be able
to set withX patterns, NO EXCEPTION. The exception is for user-defined env vars."
Precedence: operator .env OVERRIDES withX > withX value > safe default.
 - No withX used → read .env → else safe default (safe for simple prod).
 - withX used → user sets value; .env still OVERRIDES if present.
 - withX builder DOCS must hint the corresponding env var.
IMMEDIATE: the 3 telemetry storage settings (SKY_TELEMETRY_AGGREGATION_WINDOW,
_HISTOGRAM_AGGREGATION_WINDOW, DB_CAPACITY) — currently .env-only — get withX
builders. Consider SYNCHRONOUS_COMMIT too. Follow the EXISTING withTelemetry
mechanism (Config_withTelemetry / ApplyConfig, "operator env still wins").
CRUX TO GRILL: TIMING. withX/ApplyConfig runs in main(); telemetry flusher reads
the window ONCE at EnablePersistence (rt init() OR embed handoff) — if ApplyConfig
runs AFTER, a withX-set env var is too late for the read-once windows (capacity is
re-read each cycle, more forgiving). Must resolve ordering so env-overrides-withX
actually works for the read-once settings.
BROADER: the "no exception" principle implies an AUDIT (every system FIXED_NAME_READS
env var has a withX builder) + maybe a GATE enforcing it. Assess scope in grill.
METHOD: full PIV — arch-consult (existing mechanism + timing), grill, implement, Judge.

## withX-TELEMETRY — grilled, LOCKED (2026-08-19)
Consult: PROCEED+REVISE. Timing crux REAL: EnablePersistenceFromEnv runs in
observability.go init() BEFORE main's ApplyConfig → read-once windows miss withX
in the operator-enabled normal path (silent failure). Fix = MOVE enable to
lower_main after ApplyConfig.
Grill: PROCEED-WITH-MODIFICATIONS. 5 mods:
 M1(blocker): emit rt.EnableConsolePersistence() UNCONDITIONALLY, OUTSIDE the
   `if let Some(cfg)=apply_config` block in lower_main, between config block +
   MaybeStartEmbeddedPostgres. (config-less apps must keep persistence.)
 M2(blocker): observability.go init() — remove ONLY the EnablePersistenceFromEnv
   call+warn; KEEP RegisterShutdownHook("telemetry-persistence") or drain lost.
 M3(gate-red): add 4 MigrationKind::BornInCode rows to config_migration.rs for
   the SKY_TELEMETRY_* literals (like DATABASE_URL/OTEL_EXPORTER_OTLP_ENDPOINT).
 M4: capacity builder takes a Capacity ADT (Bytes/Megabytes/Gigabytes)→human
   string, NOT raw Int bytes. Keys go in configKeyToLiteralEnv ONLY, never seeded.
 M5: crux emission test — assert find(ApplyConfig)<find(EnableConsolePersistence)
   in main AND EnableConsolePersistence present for a config-LESS program.
Precedence (HOLDS): operator env > withX (ApplyConfig applyConfigValue defers to
operator via !isSeededDefault) > Go default 0/off. New keys literal-env + never
seeded → Go-default reachable. Builders: Int seconds (windows), Capacity ADT,
Bool→on/off (synccommit). Touches Sky stdlib + rt + lower.rs(compiler) + gates →
full gate suite + rebuild + Judge required.

## withX-TELEMETRY — IMPLEMENTED (2026-08-19), milestone pending
All 5 mods done + unit-verified:
 M1 lower.rs: rt.EnableConsolePersistence() emitted unconditionally, after
   ApplyConfig, before MaybeStartEmbeddedPostgres. sky_config_entry gate updated
   (order + unconditional-presence) — 9/9 green.
 M2 observability.go: init() enable removed, shutdown hook kept + new
   EnableConsolePersistence() exported func.
 M3 config_migration.rs: 4 BornInCode rows. config-migration gate PASS (6 literals).
 M4 Sky/Config.sky: Capacity ADT + 4 builders (Int seconds / Capacity / Bool);
   Go kernels normalise (Int→"Ns", bytes→decimal, Bool→on/off); keys in
   configKeyToLiteralEnv only.
 M5 sky_config_entry emission test (ApplyConfig<EnableConsolePersistence + present
   for config-less program). Go precedence tests (withX-sets / operator-overrides
   /neither) + telemetry env-read tests. config-surface --check PASS (unaffected).
Rebuilding compiler now → smoke app (4 builders + Gigabytes 100) → milestone
(example sweep + conformance + coerce-floor + rt -race + full config gates) → Judge.

## DOCS SWEEP (2026-08-19, user-requested)
User: "ensure docs sweep + readme up to date, incl claude+claude.md+agents.md
template for auto-discovery + APIs; ensure kernel fns exposed to sky have a sky
ffi fn for docs+LSP."
KERNEL/FFI/LSP: VERIFIED in place — `sky doc Sky.Config` shows all 4 new builders
+ Capacity type with signatures+docs (LSP reads same .sky source). Config
builders are .sky-backed FFI wrappers, not kernel-only → no kernel_api.rs entry;
kernel gate GREEN.
DOCS updated: docs/sky-toml.md (3 new SKY_TELEMETRY_* env rows + builder names),
docs/observability.md (done earlier), AGENTS.md (observability row → telemetry
storage tuning + Sky.Config), README.md (observability section), templates/
AGENTS.md (config front door: Sky.Config withX incl telemetry storage tuning +
sky doc pointer). templates/CLAUDE.md + CLAUDE.md import AGENTS.md → covered.
Version banner v0.20.x current (matches CHANGELOG v0.20.3).
BUILD SNAG: gofmt-after-build left sky-out/sky embedding stale content
(fingerprint mismatch — freshness gate caught it). Fix: touch ffi/build.rs +
rebuild to force re-stage. LESSON: gofmt/format BEFORE the milestone build.
