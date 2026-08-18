# Session checkpoint — 2026-08-15, `feat/embedded-postgres`

Written before a deliberate restart. Records what landed, what is committed
but unverified, and what a resuming session must NOT redo.

Branch tip at checkpoint: `b131e751`.

## Landed and verified (merged into `feat/embedded-postgres`)

| Merge | What |
|---|---|
| close-idempotency | Session-store `Close` idempotent on all four backends; `HubExporter.drained` became a real gate; hub saturation warn emitted per epoch |
| release phase | `drainAndRelease` — a release phase separate from the drain, all three termination sequences routed through it; `GET /_hub/metrics` |
| pool arithmetic | Demand is a function of the APP POOL, not `cpus`; fixture gains the knob axis (12 shapes × 64 cores); Rust dev cluster retunes on every start; **eleven dead `Log_warn` sites** — a Sky kernel returning a `Task`, built and dropped as a bare Go statement |
| gate gaps | Live tests panic rather than skip (~33 sites, incl. four private `fn skip()` helpers); `dbshare.Acquire` accounting resolves symbols not spellings; licence scanner meets a real bundle |
| mutation evidence | `scripts/grill-mutation-matrix.sh` + the discrimination table: 12 top-level failures, clean partition, 0 cascades, 0 masked |
| Sky.Live alloc | Six `hasMarker` scans → one; `VNode.Attrs/Events` on first write; style injection stops rebuilding its spec per node |
| timeout shim | `scripts/lib/with-timeout.sh` + `require-tool.sh`; 18 bare `timeout` sites routed through it; 4 of them silently reported PASS when the binary was absent |
| mem-guard | Measures swap (the thing it exists to prevent) and survives fork exhaustion; `scripts/test-mem-guard.sh`, 14 assertions, mutation-proven |

## Committed but UNVERIFIED — do not treat as done

- **`perf/hof-dispatch-codegen`** @ `e613cbec` — worktree
  `.claude/worktrees/hof-dispatch`, clean.
  - `303d1c68` test-first: the HOF callback pays a reflect allocation per element visit
  - `e613cbec` the fix: eta-expand a func value into a func slot rather than `rt.Coerce`

  The agent was **inside its verification sweep** when the session ended. No
  gate result was recorded. On resume: the two commits stand, but every leg
  (`cargo test --workspace`, `go test -race ./rt/...`, `xtask repro`/`golden`/
  `coerce-floor`/`infer`/`roundtrip`, `scripts/example-sweep.sh`,
  `scripts/doc-examples.sh`) and the before/after measurement must be re-run.
  **Do not re-derive the fix — it is written.**

- **`wip/metrics-help-table-audit`** @ `820e8979` — worktree
  `../sky-wt-residual3`. A bidirectional gate: eight declared-never-recorded
  metrics, and twelve recorded-with-no-entry that DO reach the wire as
  `# HELP … Sky metric`. Never compiled.

## The measured target for the codegen work

`layoutContextFor` shape, M1, min of 6, six marker probes over a 6-attribute element:

| variant | ns/element | allocs/element |
|---|---|---|
| as emitted before the fix | 47,937 | 432 |
| adapter removed, still erased | 12,442 | 198 |
| fully monomorphic | 1,426 | 0 |

Expect roughly the middle row. Whole-request estimate **1.4–2.0×** — do not
extrapolate the microbenchmark's 33×; 12.55% of the profile is network syscall.
The `Std.Ui` fusion has since landed, so the baseline is already better than the
first row: re-measure, do not assume.

Still outstanding on that branch: typed `SkyLen`/`SkyElem`/`SkyTailSlice`
(they take `any`, boxing the slice header 4× per element), and the §5.3
architecture-doc correction — it claims "~100 ns per element. Bounded." and
"cannot be elided without monomorphising", both false, and that wrong claim is
why the category was filed as irreducible.

## Standing state

- **Seven adversarial grill rounds, seven breached.** Round 7's live defects and
  gate gaps are closed; no round 8 has run. The rounds were still finding *live*
  defects, not just gate gaps, and two of round 7's were in code a previous
  round had specifically remediated. That is the strongest argument against
  merging on current evidence.
- **Nothing is pushed.** No tag, no release.
- Capacity numbers in `docs/skydb/embedded-postgres.md`, `README.md`,
  `AGENTS.md` and the unpushed sky-lang.org blog post are **downstream of the
  perf work** and still quote figures the profiling falsified. They must not
  ship until the codegen result settles.
- Host: 16 GB, hard-killed once today by concurrent builds. One heavy job at a
  time. `mem-guard.sh` runs detached (ppid 1) and survives a session restart.
