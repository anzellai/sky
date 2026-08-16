# AUTONOMOUS GOAL — embedded PostgreSQL (feat/embedded-postgres)

Set 2026-08-14. Branch `feat/embedded-postgres`, off `origin/main` @ `84e388d3`.

The BlueDB v2 mandate is **PAUSED, not cancelled**, and lives on its own branch
(`feat/bluedb-v2` @ `a54bff58`) with its own goal file. It is not superseded by
this one; a build-vs-buy review concluded the custom engine was not justified
under the restated requirements, and the branch is preserved with its evidence.

## Verbatim user mandate (2026-08-14)

The operating instruction:

> ok from now please in fully autonomous + agents + PIV mode, and resolve
> anything you hit by yourself. we're 100% aligned
> and set look/schedule and keep going and don't ask me questions/continuation
> until the goals are fully achieved and ready to review/merge/pr

The goals for the data layer, stated by the user:

> - DX perfect, not fussy setup, shipped with sky, even if it's running
>   separate process it's ok in such case
> - it can frequent read/write as we have session/auth/data store + analytics +
>   metrics so it has to support really frequent writes/reads
> - potentially horizontally scaling, after vertically in a single server
> - have excellent querying capabilities
> - transactional

The shape, as the user specified it:

> like after installing sky, we can do sky db init --embed; then it fetches and
> replace sky binary with embed process postgresql?!
>
> and on production they can configure external postgresql and the app just
> works.

and, after the built-binary case was raised:

> i think the semantics must run against the built binary with --embed

and on distribution:

> we need to check legals carefully, if i distribute sky can I do embed
> postgresql?
>
> ideally the postgresql ci build will include most common extension? like
> pgvector etc.?

## What "done" means

The design is `docs/skydb/embedded-postgres.md`; it is the scope decision and
the phase list. Done is every phase landed, tested, and the branch ready to
review as a PR — not "the phases I chose are green".

Two user decisions are locked and must not be re-litigated:

- **The built binary supports `--embed`.** One execution semantics; `sky run`
  must not be able to do something `./sky-out/app` cannot. The single-static-
  binary property is only spent when the flag is passed.
- **Bundles are built from source in Sky's own CI**, not taken from a third
  party's prebuilt artifacts, so the licence surface, reproducibility and
  availability are all ours.

## Standing rules for this mandate

- **Resolve blockers autonomously.** Do not stop to ask. If a decision is
  genuinely ambiguous, take the option most consistent with the design doc,
  record the choice and the reasoning in the commit, and continue.
- **PIV**: plan, implement, verify — with agents.
- **GRILL EVERY PHASE.** Not "where the surface is risky" — every phase, as a
  distinct step with its own agent, whose job is to BREAK the landed work while
  its tests stay green. An implementer verifying its own work is not an
  adversary, however conscientious; it shares the assumptions that produced the
  gap. The evidence is direct: P5a found P2's shell-injection defect
  *incidentally*, while doing something else. A griller pointed at P2 would have
  gone looking for it. Eleven commits landed before the first grill round, which
  is eleven too many.
- **RULE ZERO still applies**, carried from the BlueDB mandate because it was
  earned there: a gate that has never been observed failing is not evidence.
  The licence gate in particular must be shown rejecting a bundle it should
  reject. This is not ceremony — the first draft of that gate inspected only
  the server binary and would have passed a GPL extension in `lib/`, which is
  exactly the defect it exists to prevent.
- **No `Co-Authored-By`.** No commits of build artefacts. Explicit pathspecs on
  every commit, because several agents share this branch.

## Closing mandate (2026-08-15, verbatim)

Restated by the user after the benchmarking and profiling work:

> remember to run in autonomos + grill + agent + PIV mode to complete the
> benchmark findings: what to fix, optimise, and ensure embedded postgresql
> works are done e2e + re-benchmarked, ci green + ready to merge the PR.

This does not replace the goals above; it names the remaining path to "ready
to review/merge/pr". Five things must all be true, and each is checked against
the user's words, not a narrower reading:

1. **The benchmark findings are completed** — every finding either fixed,
   measured-and-dropped with the measurement recorded, or explicitly carried
   with a reason. "Profiled it" is not "completed it".
2. **Fix + optimise** — the reflective-HOF dispatch (§6 category 6, NOT the
   §8 floor) and the allocation costs it drives. A change that cannot be shown
   to pay for itself against the committed attribution harness is dropped, and
   the null result is recorded.
3. **Embedded PostgreSQL works E2E** — `sky db provision --embed` through
   `sky build --embed` to `./sky-out/app --embed` serving real traffic, proven
   end to end. At checkpoint no `postgres-bundle-v*` release existed, so the
   DELIVERY path had never run. Unproven is not done.
4. **Re-benchmarked** — after the fixes land, against the same harness, and
   every capacity figure downstream of it corrected in the same pass:
   `docs/skydb/embedded-postgres.md`, `README.md`, `AGENTS.md`, and the
   unpushed sky-lang.org blog post. Numbers the profiling falsified must not
   ship.
5. **CI green + PR ready** — green on the real workflows, not locally only.

Grill applies to every phase of this closing mandate too. Seven adversarial
rounds have run and ALL SEVEN breached; round 7 was still finding LIVE defects,
two of them in code an earlier round had specifically remediated. The rounds
stop when a round comes back empty against real effort — not when I run out of
appetite for them.

## Performance target (2026-08-15, verbatim)

> as a reminder, the goal must include to optimise and improve sky.live apps
> performance, with embedded postgresql as base line, single dB for all
> sessions, dB, analytics and metrics, to be able to serve 3-500 concurrent
> users with 1k+ interactions per second in a small instance -- or very close
> to this.
> this is technically achievable so we need to fix and optimise where current
> implementation is the culprit

This is a NUMERIC acceptance criterion, not a direction of travel. It is met
when a small instance (e2-small class) serves **300-500 concurrent sessions at
1,000+ interactions/sec**, or demonstrably close, with **ONE embedded
PostgreSQL carrying sessions + application data + analytics + metrics** — not a
tuned topology, not four stores, not a stripped-down view.

### The measured gap at capture

| | measured | target | gap |
|---|---|---|---|
| concurrent sessions | knee 50-100 (e2-small) | 300-500 | ~5x |
| interactions/sec | ~35-42 peak (e2-small) | 1,000+ | **~25x** |

Sessions are NOT the hard part: live heap is 336 kB/session (measured idle,
post-GC), so 500 sessions is ~168 MB. The earlier 1.4 MB/session figure was an
RSS regression that charged a fixed allocator pool to sessions and was wrong in
the pessimistic direction.

Throughput is the work. The headroom is real and measured: **~2% of self-time
is compiled Sky logic**; the rest is GC (42-46%), reflect (12-15%) and
interface boxing. A minimal Go SSE control on identical hardware costs 0.021 ms
and 3.13 kB per interaction against Sky's 9.15 ms and 5.66 MB / 133,628 objects
— to emit an 86-byte patch. The user's judgement that this is achievable is
supported by the profile, not merely optimistic.

### Known levers, and their state

1. **DONE, 1.36x** — eta-expand func-to-func coercion (`rt.Coerce[func...]` →
   closure at target shape), killing the `reflect.MakeFunc` adapter.
2. **IN FLIGHT** — typed `SkyLen`/`SkyElem`/`SkyTailSlice`; the residual 126
   allocations per scan are list erasure, not the adapter.
3. **NOT STARTED — the other reflect route.** `rt.SkyCall`-in-loop calls
   `reflect.Value.Call` PER ELEMENT inside `List_mapAny`, `List_filterMap`,
   `List_filterAny`, `List_foldlAnyT`, `List_foldr`, `List_indexedMap`,
   `List_find`. 15 sites in emitted `26-ui-showcase` alone. The eta fix did not
   touch this.
4. **NOT STARTED — allocation volume.** 133,628 objects/interaction against a
   control's ~50. ~200 allocations per rendered element.
5. **OPEN ARCHITECTURAL QUESTION** — Sky re-runs the ENTIRE `view` through
   reflective dispatch to produce a diff costing 1.3% of the total, which is
   why cost tracks view size (+139% for a 4x view). A static/dynamic template
   split is the LiveView-shaped answer. Purity makes hoisting sound by
   construction: any subexpression not mentioning `model` is constant and the
   compiler can prove it. Earlier I judged this likely moot once the constant
   factor dropped; at 1.36x that judgement no longer holds and it must be
   re-costed against the 25x target.

### Falsifiable, and it must stay so

If the target proves unreachable, the honest close is a measured statement of
what a small instance DOES serve with the full single-DB topology, and the
named architectural reason for the ceiling — with the numbers to support it.
"We improved it" is not a close. Neither is quoting a figure from a tuned
configuration the user did not ask for.

## Scope decision — no auto-split frontend/backend (2026-08-16)

Considered and REJECTED, by the user, with reasoning worth preserving:

> we still need to store session etc. so async update vs sync will just create
> more problems down the line.
> we accpet it's truly fullstack + secured by default.
> we shall focus on optimisation on sky.live app lifecycle + throughputs

The proposal was to auto-partition a Sky.Live app: compile `view` to JS/WASM and
run it client-side, keep the server as an API, with `Msg -> update` as an
auto-generated wire protocol. Sky is unusually well suited to it — the effect
system already distinguishes pure from `Task`, and `compute_def_effect` is
already a whole-program per-`DefId` effect fixpoint, which is exactly the
partition oracle such a design needs. It would have removed the view render
(51.9% of interaction CPU) and server-held session state (625 kB/session) from
the server entirely.

Rejected because sessions must be stored either way, so the split does not
remove the state problem — it relocates it and adds client/server reconciliation
on top. Server-held state is also AUTHORITATIVE; client-held state can be lied
about, and the effect system cannot tell you which pure branches are
security-relevant.

**The pinned model stands: Sky.Live is server-driven, truly full-stack, secure
by default.** Optimisation targets the interaction lifecycle and throughput
WITHIN that model. Do not re-propose the split as a performance tactic.

## Next structural lever — model-diff-driven selective render (agreed 2026-08-16)

The user's framing:

> wouldn't it be great if the diff happens first and the view rendering logic
> accept JUST the diff? as the end response only render the diff?
> ^^ exception on fully re-render page rather than diff when the page refresh etc.

**Adjusted to be buildable**: you cannot diff the view before rendering it — the
diff is OF the rendered output. But you CAN diff the MODEL. Knowing which model
fields changed, plus a static map of which view subtrees read which fields, means
only re-rendering subtrees whose dependencies moved. **Sound because `view` is
pure**: an unchanged dependency set means provably identical output. That is a
theorem here, not a heuristic.

An unchanged subtree then costs nothing twice: skipped in the render AND in the
diff, contributing no patch. `prevTree` is already retained, so the old subtree
splices in. First render / page refresh renders everything, which is what
`renderView` already does — the user's stated exception falls out.

**Why it outranks everything else remaining.** Measured law: `cost ≈ 0.12 ms +
0.018 ms × elements`. Every stage so far made rendering the whole view CHEAPER;
this stops rendering most of it. A typical interaction touches a tiny fraction of
the view, so this is potentially an order of magnitude where the ADT sealing the
user declined was ~1.1×. It also improves as views grow, reversing the current
+139%-for-4x-elements behaviour.

**Scouted obstacles, both already surveyed:**
- Seeing through first-class callbacks (`List.map (postRow s) posts`): the corpus
  census found bucket F — genuinely unknown callee — is EMPTY. 86.3% resolve by
  first-order resolution; 100% have a syntactically known body.
- Row-polymorphic records erase to `Any` (`goty.rs:226-228`) with reads through
  reflective `rt.Field`, so field-dependency analysis is UNSOUND there. Must fall
  back to "assume everything changed" — correct, slow, right failure direction.
- `compute_def_effect` (`lower.rs:1062`) is already a whole-program call-graph
  fixpoint of the shape the dependency analysis needs.

**Cheap validation first**: `Std.Ui.Lazy` already exists but memoises the
`Element` ADT BEFORE the expensive walk, so a hit still pays most of the cost; its
key is a reflective deep walk paid on hits, behind a process-wide mutex with a
1024 cap. Fixing WHERE it caches is days, not weeks, and would validate the
approach before committing to the compiler analysis.

**Sequence**: Stage 4 lands (it moves the denominator) → architecture consult
costs this properly → build. Do NOT let me sketch the mechanism; two of my
mechanism proposals were revised by consults today (the inline loop, the Go
generics) and both replacements were better.

## Decision — the dev console stays on by default (2026-08-16)

A framework-comparison audit found Sky was the only arm not running as it ships:
with `ENV`/`SKY_ENV` unset, the runtime mounts its embedded console INSIDE the
measured process and injects a dev-console anchor into every page. I proposed
flipping the default so the fast, safe configuration is what you get without
asking. **The user rejected that, and was right:**

> console logics gated by ENV=production not not blank shouldn't change.
> we'll rather document this and remind users.
> so whatever FIRST try of sky.live app instantly shows value.
> no one suddenly prompt building an app and instantly ship to production

A framework whose first run shows nothing has failed at the moment it most needs
to succeed. The gating on `ENV=production` (`observability.go:333`) is correct and
**must not change**.

**What ships instead is visibility.** Sky.Live's entire startup output is
`Sky.Live listening on :%d` (`live.go:4329`) — nothing announces that the console
is mounted, that it is dev-only, or that `ENV=production` exists. So a first-time
user gets a console and is not told; a deploying user gets one and is not warned.
The startup banner now states the mode, the console URL, and the derived GC
settings — advertising the console as the feature it is, and simply omitting the
line under production rather than scolding.

**This is documentation delivered where it is read, NOT a behaviour change.**

## Config architecture — DECIDED (2026-08-16)

The user settled it:

> ok I'm in favour of using withX pattern with value or basically a config record,
> env vars should be derived from .env files or env vars. with defaults
> sky.toml for deps and compiled only flag like embed etc?

**Three-way split:**

1. **`sky.toml` — build manifest only.** Name, deps, toolchain, and COMPILE-ONLY
   flags such as `embed` (it changes what is compiled INTO the binary, so it
   cannot be a runtime value). Everything the compiler must know before or during
   compilation, and nothing else.
2. **A typed config record in the app's own `main`, built with the existing
   `withX` pattern.** This is the runtime surface. Defaults are Sky values.
3. **`.env` / environment variables feed that record**, with defaults — for what
   genuinely varies per deployment (secrets, DSNs, ports).

**Why this beats the schema-codegen design it replaces:** no schema language to
invent, no generated readers to keep in sync, and drift becomes IMPOSSIBLE because
the config IS the code. Discoverability comes free from `sky doc` and LSP
completion. Most of the fourteen hand-rolled parsers disappear rather than being
unified.

The earlier verdict — "the schema is not the deliverable, the gate is", because the
failure that reached users was *config that looks set and does nothing* — now reads
as **the compiler IS the gate**. A `withX` nothing consumes will not type-check.
That is a stronger guarantee than any drift-detection test.

**Open, and load-bearing:**
- **Bootstrap ordering.** `GOMEMLIMIT`, the `--embed` PostgreSQL supervisor, the
  console mount and `[env] prefix` are consumed before user `main` could have built
  a record. Whatever genuinely cannot move IS the residual runtime surface — and it
  may be small enough to live in the manifest.
- **Non-Sky.Live shapes.** `Sky.Http.Server`, `Sky.Cli`, `Sky.Tui`, `Sky.Webview`,
  `Std.Jobs` each have their own entry point, plus cross-cutting concerns
  (telemetry, database, prefix). A design where a Cli job cannot configure its
  database the way a Live app does is not a win.
- **Migration must be MECHANICAL, not prose.** A user — including one with a
  running production deployment — must be able to ask "what do I change?" and get
  an answer from a tool. An unmigrated app either keeps working or fails loudly;
  silent behaviour change is the one unacceptable outcome.

Design record: `docs/tooling/config-architecture.md`.
