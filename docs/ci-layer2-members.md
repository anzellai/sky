# Layer 2 — the real-world projects

> Implementation record for Phase 5 of the CI/test overhaul.
> The authority is [`ci-test-architecture-v2.md`](ci-test-architecture-v2.md) §6;
> the project *shape* was adjudicated in favour of
> [`ci-corpus-proposal.md`](ci-corpus-proposal.md) §5. This file records the
> decisions those two documents leave open, so they are not re-inferred
> differently by the next session.

## Why the tier exists

Layer 1 is fast, static, and massively parallel — and it is **structurally
blind** to a whole class of defect (v2 §3.4): session/SSE lifecycle, cookie
expiry against wall-clock, cross-process gob, multi-replica session routing,
reverse-proxy topology, a real SQL engine's behaviour, a browser's DOM.

Layer 2 exists for that class and nothing else. It is deliberately small, and
its cost must not be allowed to crowd out Layer 1's completeness — which is why
member D is T4 and member E is T3.

## Decision 1 — Layer 2 lives at `apps/`

**v2 never states a Layer-2 directory.** The only path either document names is
`apps/`, from the corpus proposal (§2 line 90, §3 line 353, §4 rule 4), which is
the document that won on project shape. Recorded here rather than left implicit:

- **Layer-2 project source lives at `apps/<member>/`**, one normal Sky project
  per directory (`sky.toml` + `src/`).
- `apps/.gitignore` excludes `sky-out/`, `.skycache/`, `.skydeps/`, `.sky/` and
  SQLite files. Every gate builds its member from a **wiped slate**, so a
  committed artifact could only ever mask the build under test.
- Two members are **not** directories under `apps/`, on purpose:
  - **F** stays at `sky-bundled/console` and `sky-bundled/doc`. Membership is by
    manifest entry, not by location — it is shipped compiler source, not a
    fixture, and copying it would create a second copy that can drift.
  - **E (Fleet)** is a *scenario*, not source: "Ledger, run as a topology". A
    directory would be a mock, and the whole reason E is a scenario over the
    real app is that a scenario **cannot rot into a mock**.
  - **G (CLI verbs)** is `rust/crates/sky/tests/*_flow.rs`. v2 §6 forbids making
    an app responsible for a CLI verb: it would couple the verb's coverage to
    that app's build health.

## Decision 2 — membership is declared, never discovered

`apps/manifest.toml` is the membership authority. **No gate calls `read_dir` on
`apps/`** (v2 §3.1; the discovery-by-listing model has already produced two live
defects in this repo — `39-hub-demo` invisible to all six gates, and the
reject-face discovery divergence).

Consequently a member that is not in the manifest is not in the corpus, and a
manifest row whose gate does not exist is a visible inconsistency rather than a
silent absence.

## Decision 3 — every member's gate is a registry gate

Layer-2 gates are ordinary rows in `rust/crates/xtask/src/harness/registry.rs`
and obey the same contract as every other gate (v2 §7):

| Property | Rule |
|---|---|
| `tier` | declared in the registry, never chosen at runtime |
| `budget_s` | enforced by `killpg` over the gate's process group |
| `expected` | the **exact** assertion count — never a `>=` |
| `mutations` | at least one, or the build fails (`Mutations::new` is `const`) |

Shared support lives in `harness/layer2.rs`, which encodes four operational
rules as code rather than leaving them to each gate — each is a defect this repo
has shipped:

1. **Unique ports.** `free_port()` binds `:0` and reads the port back. Fourteen
   examples share `8000`, and the sweep's probe asserted only that *something*
   answered — so a squatter left by an earlier run satisfied it.
2. **Process-group kill.** `Server` spawns with `process_group(0)` and tears down
   with `killpg`. `kill -9 $!` kills the subshell and leaves the app holding the
   port.
3. **The port must be observed released.** Teardown is `killpg` *and* asserts the
   port came back, because a leaked listener silently satisfies the next gate.
4. **Build from a wiped slate.** `clean_build` removes `sky-out/`, `.skycache/`,
   `.skydeps/`. A gate that reuses artifacts cannot be falsified by a source
   mutation — its declared mutation would report `VACUOUS` for ever and be
   misread as a harness defect.

**A gate that asserts "no crash" is not enough.** `liveInto` on a SQL backend
must either deliver or fail loudly; "no crash" passes while it silently never
updates (v2 §6.1). Layer-2 gates assert the *verdict*.

## Decision 4 — no bind-position port literals

`layer2::bind_position_port_literals` scans a member's source and fails the gate
on a numeric literal in the bind position of `Server.listen` / `Live.app` /
`Tui.app` / `Webview.app`. Members take their port from the environment.

This is forced by a real runtime constraint, not style: `Server.listen 0` *does*
bind an OS-assigned ephemeral port, but **there is no API to read the assigned
port back**, and the startup banner prints the literal `0`
(`runtime-go/rt/rt.go`). So the harness must choose the port and pass it in.

## Members

Tiers are v2 §6's table. "Owns" is the surface no other member covers.

| | Member | Location | Gate | Tier | Owns |
|---|---|---|---|---|---|
| **A** | Ledger | `apps/ledger` | `apps-ledger`, `apps-ledger-postgres` | T1 / T3 | sessions/CSRF/SSE · `Std.Auth` · committed `migrations/` · `Std.Money` · **the Postgres arm** |
| **B** | Relay | `apps/relay` | `apps-relay` | T1 | `Sky.Http.Middleware` · `RateLimit` · WebSocket · SSE · **both kernel-alias arity shapes** |
| **C** | Fieldbook | `apps/fieldbook` | `apps-fieldbook` | T2 | cross-backend `Std.Ui` parity · `Std.Ui.Events` |
| **D** | Storefront | `examples/13-skyshop` | `apps-ffi-scale` | T4 | 76k-symbol FFI · `.skydeps` · external Sky package |
| **E** | Fleet | scenario over A | `apps-fleet` | T3 | multi-replica · shared session store · `ENV=production` refusal |
| **F** | console + doc | `sky-bundled/` | `apps-bundled` | T1 | 5,746 lines linked into every emitted binary |
| **G** | CLI verbs | `rust/crates/sky/tests/` | `cli-verbs` | T1 | `init` · `clean` · `watch` · `db` · `install/update/upgrade` |

### Why Postgres is the point of member A

Measured on this commit: **58** example directories, **8** declare `[database]`,
**8** use `driver = "sqlite"`, **0** use Postgres. Session stores are `sqlite` or
`memory` only — **0** Redis, **0** Postgres. The Ledger Postgres arm is *new*
coverage, not a replacement.

(v2 §6.1 states 7 rather than 8 `[database]` declarations; the count has since
moved. The load-bearing claim — zero Postgres — holds.)

### Member E's assertion was rewritten because its falsifier said VACUOUS

Worth recording, because the obvious design is the one that does not work.

Fleet's first assertion was the intuitive one: *a session created on replica 1
must be recognised by replica 2 through the shared store*. Its falsifier pointed
replica 2 at a private in-memory store, and the runner reported **`VACUOUS`** —
the gate stayed green. The reason is that a replica **adopts** a client-supplied
`sky_sid` and creates a fresh local session under the same id, so for a
state-free session nothing in the response distinguishes "restored from the
shared store" from "invented locally".

The assertion was replaced rather than weakened, with one that bites: under
`ENV=production` an unreachable session store must make the app **refuse to
start**. That is the `[live] store="postgres" silently falls back to memory`
incident stated as a verdict, and its falsifier (run the probe in dev instead)
goes red with the app still serving.

**The residual gap is real:** Fleet proves the topology runs on one shared store
and refuses to degrade silently; it does **not** yet prove session *state*
migrates between replicas. That needs an authenticated flow, and it is recorded
here rather than papered over.

### Why member D is pre-release

`examples/13-skyshop` is the 76k-symbol FFI benchmark member D is specified as
the successor to. It cannot build without a network fetch: it declares an
external Sky package (`github.com/anzellai/sky-tailwind`) and refuses to build
until `sky install` has run. That is the tier assignment justifying itself —
network-dependent and cold-expensive work does not belong on the per-push path.
