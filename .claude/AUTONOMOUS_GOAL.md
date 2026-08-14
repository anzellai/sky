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
