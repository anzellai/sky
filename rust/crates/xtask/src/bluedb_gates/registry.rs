//! The gate registry — §9.2 of `docs/bluedb/v2-architecture.md`.
//!
//! The registry is the single source of truth. `STATUS.md` renders from **this
//! table**, never from a run's results (§9.3 property 1), so a gate cannot
//! disappear from the report by failing to execute.
//!
//! Three structural guarantees, in descending order of strength:
//!
//! 1. A gate with no `run` does not **compile** (the field is not optional).
//! 2. A gate with no mutation does not **construct**: [`Mutations::new`] is the
//!    only constructor and `assert!`s on the empty slice inside a `const fn`.
//!    Because [`REGISTRY`] is a `static`, that assertion is evaluated during
//!    const-eval — `Mutations::new(&[])` is a **compile error**, not a runtime
//!    panic (H1, made unrepresentable rather than merely forbidden).
//! 3. A gate that somehow reaches runtime with zero mutations renders
//!    `UNPROVEN`, and `UNPROVEN` makes its goal FAIL (§9.6 check 3 — the
//!    belt-and-braces backstop behind (2), in case a future refactor
//!    reintroduces a permissive constructor).

use std::path::{Path, PathBuf};

use super::gates_g0;
use super::pending;

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Tier {
    Fast,
    Full,
}

impl Tier {
    pub fn label(self) -> &'static str {
        match self {
            Tier::Fast => "fast",
            Tier::Full => "full",
        }
    }
}

/// What a gate body reports. `UNPROVEN` is deliberately NOT constructible here:
/// it is computed by the static checks from the registry, never claimed by a
/// gate about itself.
pub enum GateOutcome {
    Pass {
        detail: String,
    },
    Fail {
        detail: String,
        findings: Vec<String>,
    },
    /// The gate did not execute. `reason` must describe a **machine-checked**
    /// condition (see [`super::pending`]) — never an author's judgement that
    /// the gate is not worth running. A `NotRun` gate renders its goal
    /// `UNKNOWN`, never PASS.
    NotRun {
        reason: String,
    },
}

impl GateOutcome {
    pub fn pass(detail: impl Into<String>) -> GateOutcome {
        GateOutcome::Pass {
            detail: detail.into(),
        }
    }

    pub fn fail(detail: impl Into<String>, findings: Vec<String>) -> GateOutcome {
        GateOutcome::Fail {
            detail: detail.into(),
            findings,
        }
    }

    pub fn not_run(reason: impl Into<String>) -> GateOutcome {
        GateOutcome::NotRun {
            reason: reason.into(),
        }
    }

    /// §9.4's canary spelling. Kept because the design names it; the canary is
    /// the ONE gate for which a pass is the failure signal under
    /// `--verify-mutations`.
    pub fn pass_if(cond: bool) -> GateOutcome {
        if cond {
            GateOutcome::pass("asserted")
        } else {
            GateOutcome::fail("assertion failed", vec![])
        }
    }
}

/// The rendered state of a gate. Four states, not two (§9.3).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GateState {
    Pass,
    Fail,
    NotRun,
    /// No mutation declared (§9.2 / §9.6 check 3).
    Unproven,
}

impl GateState {
    pub fn label(self) -> &'static str {
        match self {
            GateState::Pass => "PASS",
            GateState::Fail => "FAIL",
            GateState::NotRun => "NOT RUN",
            GateState::Unproven => "UNPROVEN",
        }
    }

    /// The single-char marker used in the goal roll-up.
    /// Legend: (blank)=PASS  ✗=FAIL  ⊘=NOT RUN  ⊗=UNPROVEN
    pub fn marker(self) -> &'static str {
        match self {
            GateState::Pass => "",
            GateState::Fail => "✗",
            GateState::NotRun => "⊘",
            GateState::Unproven => "⊗",
        }
    }
}

/// A goal's computed verdict. There is no prose verdict anywhere; this is a
/// total function over the goal's gates (§9.3).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GoalVerdict {
    Pass,
    Fail,
    /// Absence of evidence is NOT evidence. Never collapses to PASS.
    Unknown,
}

impl GoalVerdict {
    pub fn label(self) -> &'static str {
        match self {
            GoalVerdict::Pass => "PASS",
            GoalVerdict::Fail => "FAIL",
            GoalVerdict::Unknown => "UNKNOWN",
        }
    }
}

/// A recorded falsification proof for one gate.
pub struct Mutation {
    /// e.g. `"G0.2/rt-imports-bluedb"`.
    pub id: &'static str,
    /// Repo-relative path to a `git apply`-able patch that reintroduces the defect.
    pub patch: &'static str,
    /// The assertion that must go RED, verbatim — asserted to appear in the
    /// mutated gate's output.
    pub expect: &'static str,
    /// Paths the patch touches. Drives MAJOR-17's `UNVERIFIED-SINCE` decay
    /// check: a whole-tree "has anything changed" probe would mark everything
    /// unverified after every commit, and a signal that always fires is a
    /// signal nobody reads.
    pub targets: &'static [&'static str],
}

/// H1: a plain `&'static [Mutation]` accepts `&[]`, and an empty slice iterates
/// zero elements and "succeeds" — twelve of v2.0's twenty-six gates would have
/// been PROVEN-by-vacuum. The newtype makes the empty case unrepresentable.
///
/// The inner field is private to this module: outside `registry.rs` there is no
/// way to build a `Mutations` except through [`Mutations::new`].
pub struct Mutations(&'static [Mutation]);

impl Mutations {
    /// The ONLY constructor. In a `const`/`static` initialiser — which is where
    /// [`REGISTRY`] lives — the assertion is evaluated by const-eval, so
    /// `Mutations::new(&[])` fails the **build**.
    pub const fn new(m: &'static [Mutation]) -> Mutations {
        assert!(
            !m.is_empty(),
            "every gate must declare at least one mutation"
        );
        Mutations(m)
    }

    pub fn as_slice(&self) -> &'static [Mutation] {
        self.0
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}

pub struct Gate {
    /// e.g. `"G2.2"`.
    pub id: &'static str,
    /// `0` = cross-cutting, `1..=5` = the numbered goal.
    pub goal: u8,
    pub title: &'static str,
    pub tier: Tier,
    pub run: fn(&Ctx) -> GateOutcome,
    /// Hard timeout in seconds; exceeding it is a FAIL, not a hang.
    pub budget_s: u64,
    pub mutations: Mutations,
}

/// Everything a gate body is allowed to look at.
///
/// **H3 invariant.** `root` is the tree being certified. Under
/// `--verify-mutations` that is the scratch worktree, never the developer's
/// tree. A gate body that reaches outside `ctx.root` — an absolute path, an
/// inherited `cwd`, a `repo_root()` call — breaks the mutation runner's
/// guarantee silently. Gate bodies resolve every path through
/// [`Ctx::path`].
#[derive(Clone)]
pub struct Ctx {
    root: PathBuf,
    pub tier: Tier,
    pub verbose: bool,
    /// `STATUS.md` as it was on disk **before** this run regenerates it. G0.1
    /// checks the snapshot, not the file it is about to write.
    pub status_snapshot: Option<String>,
}

impl Ctx {
    pub fn new(
        root: PathBuf,
        tier: Tier,
        verbose: bool,
        status_snapshot: Option<String>,
    ) -> Ctx {
        Ctx {
            root,
            tier,
            verbose,
            status_snapshot,
        }
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    /// The only sanctioned way for a gate body to name a file.
    pub fn path(&self, rel: &str) -> PathBuf {
        self.root.join(rel)
    }

    pub fn read(&self, rel: &str) -> Option<String> {
        std::fs::read_to_string(self.path(rel)).ok()
    }

    pub fn exists(&self, rel: &str) -> bool {
        self.path(rel).exists()
    }
}

// ---------------------------------------------------------------------------
// The registry
// ---------------------------------------------------------------------------

/// Every gate named in §1 of `docs/bluedb/v2-architecture.md`.
///
/// Gates for goals 1–5 gate code that does not exist yet (P1–P8). They are
/// **registered** — §9.6 check 1 requires every goal to own at least one gate —
/// and their bodies are [`super::pending`] probes: `NOT RUN` while the
/// substrate is absent, and **FAIL** the moment it appears without the gate
/// being written. That is a ratchet, not an escape hatch: the probe decides,
/// not the author, and a `NOT RUN` gate renders its goal `UNKNOWN`, so no goal
/// can be closed by leaving gates unimplemented.
pub static REGISTRY: &[Gate] = &[
    // -- Goal 0 — cross-cutting -------------------------------------------
    Gate {
        id: "G0.1",
        goal: 0,
        title: "STATUS.md is generated and matches a fresh run (hand edits detected)",
        tier: Tier::Fast,
        run: gates_g0::g0_1_status_generated,
        budget_s: 30,
        mutations: Mutations::new(&[Mutation {
            id: "G0.1/hand-edit-status",
            patch: "docs/bluedb/mutations/G0.1.hand-edit-status.patch",
            expect: "body-sha256 mismatch",
            targets: &["docs/bluedb/STATUS.md"],
        }]),
    },
    Gate {
        id: "G0.2",
        goal: 0,
        title: "rt never imports bluedb; bluedb imports only pebble + stdlib",
        tier: Tier::Fast,
        run: gates_g0::g0_2_layering,
        budget_s: 30,
        mutations: Mutations::new(&[Mutation {
            id: "G0.2/rt-imports-bluedb",
            patch: "docs/bluedb/mutations/G0.2.rt-imports-bluedb.patch",
            expect: "forbidden edge rt -> bluedb",
            targets: &["runtime-go/rt", "runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G0.3",
        goal: 0,
        title: "non-Persist app links no pebble, builds cold-cache offline, ships no bluedb/, keeps its non-`data` session store",
        tier: Tier::Full,
        run: gates_g0::g0_3_no_pebble_leak,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G0.3/persistglue-unconditional",
            patch: "docs/bluedb/mutations/G0.3.persistglue-unconditional.patch",
            expect: "pebble symbols in a non-Persist binary",
            targets: &["runtime-go/rt", "runtime-go/persistglue", "rust/crates/project/src/build.rs"],
        }]),
    },
    Gate {
        id: "G0.4",
        goal: 0,
        title: "no dead config key: every env the compiler writes has a runtime reader",
        tier: Tier::Fast,
        run: gates_g0::g0_4_no_dead_config,
        budget_s: 60,
        mutations: Mutations::new(&[Mutation {
            id: "G0.4/dead-key",
            patch: "docs/bluedb/mutations/G0.4.dead-key.patch",
            // Names the key the patch introduces, not the generic message: the
            // generic one already fires on the four pre-existing dead keys, so
            // it would prove nothing.
            expect: "dead config key DATA_BOGUS",
            targets: &["rust/crates/project/src/build.rs", "runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G0.5",
        goal: 0,
        title: "one go build site; it carries -tags pebblegozstd so all three call paths inherit it",
        tier: Tier::Fast,
        run: gates_g0::g0_5_zstd_tag,
        budget_s: 30,
        mutations: Mutations::new(&[Mutation {
            id: "G0.5/second-go-build-site",
            patch: "docs/bluedb/mutations/G0.5.second-go-build-site.patch",
            expect: "expected exactly 1 `go build` site",
            targets: &["rust/crates/project/src/build.rs"],
        }]),
    },
    Gate {
        id: "G0.6",
        goal: 0,
        title: "every gate's recorded mutation still applies and still turns it red",
        tier: Tier::Full,
        run: gates_g0::g0_6_mutations_verified,
        budget_s: 3600,
        mutations: Mutations::new(&[Mutation {
            id: "G0.6/corrupt-expected",
            patch: "docs/bluedb/mutations/G0.6.corrupt-expected.patch",
            expect: "does not contain the declared assertion",
            // NOT `gate-state.tsv`: `--verify-mutations` writes that file
            // itself, so listing it would make this proof UNVERIFIED-SINCE the
            // instant it was taken — a signal that always fires.
            targets: &["docs/bluedb/mutations"],
        }]),
    },
    Gate {
        id: "G0.7",
        goal: 0,
        title: "harness self-integrity + every cited file:line resolves on its tagged branch",
        tier: Tier::Fast,
        run: gates_g0::g0_7_self_integrity,
        budget_s: 120,
        mutations: Mutations::new(&[Mutation {
            id: "G0.7/mutationless-gate",
            patch: "docs/bluedb/mutations/G0.7.mutationless-gate.patch",
            expect: "declares no mutation",
            targets: &["rust/crates/xtask/src/bluedb_gates"],
        }]),
    },
    Gate {
        id: "G0.C",
        goal: 0,
        title: "CANARY — deliberately vacuous; --verify-mutations MUST report VACUOUS",
        tier: Tier::Fast,
        run: gates_g0::g0_c_canary,
        budget_s: 30,
        mutations: Mutations::new(&[Mutation {
            id: "G0.C/noop",
            patch: "docs/bluedb/mutations/G0.C.noop.patch",
            expect: "<never>",
            targets: &["docs/bluedb/mutations/CANARY_TOUCHED"],
        }]),
    },
    // -- Goal 1 — session-bounded Model state sync (P5) ---------------------
    Gate {
        id: "G1.1",
        goal: 1,
        title: "session cache ceiling holds (count + bytes)",
        tier: Tier::Fast,
        run: pending::p5_sessions,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G1.1/remove-ceiling",
            patch: "docs/bluedb/mutations/G1.1.remove-ceiling.patch",
            expect: "resident session bytes exceeded the ceiling",
            targets: &["runtime-go/rt/live_store.go"],
        }]),
    },
    Gate {
        id: "G1.1d",
        goal: 1,
        title: "per-connection floor capacity report (arm D)",
        tier: Tier::Full,
        run: pending::p5_sessions,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G1.1d/unpublish-perconn-floor",
            patch: "docs/bluedb/mutations/G1.1d.unpublish-perconn-floor.patch",
            expect: "perConnFloor missing from baselines.json",
            targets: &["docs/bluedb/baselines.json"],
        }]),
    },
    Gate {
        id: "G1.2",
        goal: 1,
        title: "Model correctness across spill / rehydrate",
        tier: Tier::Fast,
        run: pending::p5_sessions,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G1.2/drop-field-on-deflate",
            patch: "docs/bluedb/mutations/G1.2.drop-field-on-deflate.patch",
            expect: "rehydrated Model differs from pre-deflation Model",
            targets: &["runtime-go/rt/live_store.go"],
        }]),
    },
    Gate {
        id: "G1.3",
        goal: 1,
        title: "no acked-then-lost transition across spill",
        tier: Tier::Fast,
        run: pending::p5_sessions,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G1.3/ack-before-persist",
            patch: "docs/bluedb/mutations/G1.3.ack-before-persist.patch",
            expect: "acked transition absent after rehydrate",
            targets: &["runtime-go/rt/live.go"],
        }]),
    },
    Gate {
        id: "G1.4",
        goal: 1,
        title: "provisional admission — a crawler GET mints no resident session",
        tier: Tier::Fast,
        run: pending::p5_sessions,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G1.4/admit-on-get",
            patch: "docs/bluedb/mutations/G1.4.admit-on-get.patch",
            expect: "resident session count rose on an unauthenticated GET",
            targets: &["runtime-go/rt/live.go"],
        }]),
    },
    Gate {
        id: "G1.5",
        goal: 1,
        title: "lock safety — the stated global lock order is not violated",
        tier: Tier::Full,
        run: pending::p5_sessions,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G1.5/invert-lock-order",
            patch: "docs/bluedb/mutations/G1.5.invert-lock-order.patch",
            expect: "lock order violation",
            targets: &["runtime-go/rt/live.go", "runtime-go/rt/live_store.go"],
        }]),
    },
    Gate {
        id: "G1.6",
        goal: 1,
        title: "durable-session write amplification stays within the committed baseline",
        tier: Tier::Full,
        run: pending::p5_sessions,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G1.6/persist-every-frame",
            patch: "docs/bluedb/mutations/G1.6.persist-every-frame.patch",
            expect: "write amplification exceeds the committed baseline",
            targets: &["runtime-go/rt/live_store.go", "docs/bluedb/baselines.json"],
        }]),
    },
    Gate {
        id: "G1.7",
        goal: 1,
        title: "sync convergence — every live connection of a session sees every acked transition",
        tier: Tier::Fast,
        run: pending::p5_sessions,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G1.7/ship-to-origin-conn-only",
            patch: "docs/bluedb/mutations/G1.7.ship-to-origin-conn-only.patch",
            expect: "second connection of the session did not receive the transition",
            targets: &["runtime-go/rt/live.go"],
        }]),
    },
    // -- Goal 2 — unified store, real SERIALIZABLE (P1/P2/P3) --------------
    Gate {
        id: "G2.1",
        goal: 2,
        title: "isolation conformance, all three backends, discriminating",
        tier: Tier::Full,
        run: pending::p3_isolation,
        budget_s: 1800,
        mutations: Mutations::new(&[Mutation {
            id: "G2.1/sqlite-deferred",
            patch: "docs/bluedb/mutations/G2.1.sqlite-deferred.patch",
            expect: "write-skew admitted",
            targets: &["runtime-go/rt/db_auth.go"],
        }]),
    },
    Gate {
        id: "G2.2",
        goal: 2,
        title: "index seek complexity — O(log n + k), not O(all rows)",
        tier: Tier::Fast,
        run: pending::p2_index,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.2/force-full-scan",
            patch: "docs/bluedb/mutations/G2.2.force-full-scan.patch",
            expect: "rows scanned grew with collection size",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.3",
        goal: 2,
        title: "index<->data consistency under crash",
        tier: Tier::Full,
        run: pending::p2_index,
        budget_s: 1800,
        mutations: Mutations::new(&[Mutation {
            id: "G2.3/index-outside-batch",
            patch: "docs/bluedb/mutations/G2.3.index-outside-batch.patch",
            expect: "orphan index entry",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.4",
        goal: 2,
        title: "transact body replayability",
        tier: Tier::Fast,
        run: pending::p3_isolation,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.4/side-effect-in-body",
            patch: "docs/bluedb/mutations/G2.4.side-effect-in-body.patch",
            expect: "transact body observed a non-replayable effect",
            targets: &["runtime-go/bluedb", "sky-stdlib/Std/Persist.sky"],
        }]),
    },
    Gate {
        id: "G2.5",
        goal: 2,
        title: "cross-tenant reads are structurally impossible (key scoping, not a residual)",
        tier: Tier::Fast,
        run: pending::p2_index,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.5/tenant-as-residual",
            patch: "docs/bluedb/mutations/G2.5.tenant-as-residual.patch",
            expect: "adversarial row from another tenant appeared",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.6",
        goal: 2,
        title: "substrate crash corpus (errorfs injection manifest)",
        tier: Tier::Full,
        run: pending::p1_engine,
        budget_s: 1800,
        mutations: Mutations::new(&[Mutation {
            id: "G2.6/disable-injection-point",
            patch: "docs/bluedb/mutations/G2.6.disable-injection-point.patch",
            expect: "fewer injection sites than the recorded manifest",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.7",
        goal: 2,
        title: "unique constraint enforcement",
        tier: Tier::Fast,
        run: pending::p2_index,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.7/skip-unique-probe",
            patch: "docs/bluedb/mutations/G2.7.skip-unique-probe.patch",
            expect: "duplicate accepted on a unique column",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.8",
        goal: 2,
        title: "tenant key rewrite (reindex) under kill arms",
        tier: Tier::Full,
        run: pending::p4_full_api,
        budget_s: 1800,
        mutations: Mutations::new(&[Mutation {
            id: "G2.8/rewrite-without-barrier",
            patch: "docs/bluedb/mutations/G2.8.rewrite-without-barrier.patch",
            expect: "row visible under both the old and the new tenant key",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    // G2.9 was ONE gate whose five arms straddled three phases, which is not a
    // gate but a scheduling accident: arms (a)-(c) are embedded/pebble and close
    // in P1, arm (d) needs a `[data] durability` config key whose parser is P4's,
    // and arm (e) is sqlite WAL/checkpoint policy, which is P3's. A gate that
    // cannot close inside the phase that owns it has exactly two outcomes — it
    // reports partial green (a lie), or it stays red for three phases until
    // someone stops reading it. Split so each half can actually close.
    Gate {
        id: "G2.9a",
        goal: 2,
        title: "durability on ack — embedded (fsync before ack, survives crash, no reorder)",
        tier: Tier::Full,
        run: pending::p1_engine,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G2.9a/ack-before-fsync",
            patch: "docs/bluedb/mutations/G2.9.ack-before-fsync.patch",
            expect: "acked write missing after restart",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.9b",
        goal: 2,
        title: "durability on ack — the `durability` knob and the sqlite WAL policy",
        tier: Tier::Full,
        // P3 substrate, not P1: arm (d) needs a durability setting the engine
        // does not have (`pebble_engine.go`'s config has no such field and the
        // committer hard-codes `Apply(pebble.Sync)`), and arm (e) is sqlite.
        run: pending::p3_isolation,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G2.9b/normal-durability-still-syncs",
            patch: "docs/bluedb/mutations/G2.9b.normal-durability-still-syncs.patch",
            expect: "durability=normal indistinguishable from strict",
            targets: &["runtime-go/bluedb", "runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G2.10",
        goal: 2,
        title: "no cross-tenant conflicts (tenant is in the conflict domain)",
        tier: Tier::Fast,
        run: pending::p2_index,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.10/tenant-out-of-conflict-domain",
            patch: "docs/bluedb/mutations/G2.10.tenant-out-of-conflict-domain.patch",
            expect: "T1 and T2 transactions conflicted",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.11",
        goal: 2,
        title: "seek bounds and read-set bounds are the same bounds",
        tier: Tier::Fast,
        run: pending::p2_index,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.11/widen-seek-bounds",
            patch: "docs/bluedb/mutations/G2.11.widen-seek-bounds.patch",
            expect: "seek bound differs from the recorded read-set bound",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    Gate {
        id: "G2.12",
        goal: 2,
        title: "per-Sky-type index encoding is order preserving (incl. Decimal/Money — U1)",
        tier: Tier::Fast,
        run: pending::p2_index,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.12/decimal-scale-naive",
            patch: "docs/bluedb/mutations/G2.12.decimal-scale-naive.patch",
            expect: "encoded order differs from value order",
            targets: &["runtime-go/bluedb"],
        }]),
    },
    // -- Goal 3 — easy + simple (P4) ---------------------------------------
    Gate {
        id: "G3.1",
        goal: 3,
        title: "the zero-config app builds, runs, and persists across restart",
        tier: Tier::Full,
        run: pending::p4_full_api,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G3.1/remove-zero-config-default",
            patch: "docs/bluedb/mutations/G3.1.remove-zero-config-default.patch",
            expect: "app requires a [data] section",
            targets: &["rust/crates/project/src/build.rs", "sky-stdlib/Std/Persist.sky"],
        }]),
    },
    Gate {
        id: "G3.2",
        goal: 3,
        title: "doc-examples gate over docs/skypersist/",
        tier: Tier::Fast,
        run: pending::p4_full_api,
        budget_s: 600,
        mutations: Mutations::new(&[Mutation {
            id: "G3.2/break-doc-example",
            patch: "docs/bluedb/mutations/G3.2.break-doc-example.patch",
            expect: "doc example failed to check",
            targets: &["docs/skypersist"],
        }]),
    },
    Gate {
        id: "G3.3",
        goal: 3,
        title: "graduation embedded->sqlite->postgres on identical app source",
        tier: Tier::Full,
        run: pending::p4_full_api,
        budget_s: 1800,
        mutations: Mutations::new(&[Mutation {
            id: "G3.3/driver-conditional-source",
            patch: "docs/bluedb/mutations/G3.3.driver-conditional-source.patch",
            expect: "app source differs between drivers",
            targets: &["docs/skypersist"],
        }]),
    },
    Gate {
        id: "G3.4",
        goal: 3,
        title: "migration lifecycle",
        tier: Tier::Full,
        run: pending::p4_full_api,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G3.4/skip-generation-stamp",
            patch: "docs/bluedb/mutations/G3.4.skip-generation-stamp.patch",
            expect: "migration applied twice",
            targets: &["rust/crates/project/src", "sky-stdlib/Std/Persist.sky"],
        }]),
    },
    // -- Goal 4 — changeset notification (P6) -------------------------------
    Gate {
        id: "G4.1",
        goal: 4,
        title: "changeset delivery on all backends",
        tier: Tier::Fast,
        run: pending::p6_reactivity,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G4.1/drop-sqlite-emit",
            patch: "docs/bluedb/mutations/G4.1.drop-sqlite-emit.patch",
            expect: "no delivery on sqlite",
            targets: &["runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G4.2",
        goal: 4,
        title: "cross-tenant non-delivery",
        tier: Tier::Fast,
        run: pending::p6_reactivity,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G4.2/tenantless-bucket-key",
            patch: "docs/bluedb/mutations/G4.2.tenantless-bucket-key.patch",
            expect: "a T1 commit woke a T2 subscriber",
            targets: &["runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G4.3",
        goal: 4,
        title: "fan-out cost within the committed baseline",
        tier: Tier::Full,
        run: pending::p6_reactivity,
        budget_s: 1800,
        mutations: Mutations::new(&[Mutation {
            id: "G4.3/re-query-instead-of-delta",
            patch: "docs/bluedb/mutations/G4.3.re-query-instead-of-delta.patch",
            expect: "fan-out cost exceeds the committed baseline",
            targets: &["runtime-go/rt", "docs/bluedb/baselines.json"],
        }]),
    },
    Gate {
        id: "G4.4",
        goal: 4,
        title: "startup fatal on missing capability — never a first-session os.Exit",
        tier: Tier::Fast,
        run: pending::p6_reactivity,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G4.4/exit-on-first-session",
            patch: "docs/bluedb/mutations/G4.4.exit-on-first-session.patch",
            expect: "process exited during a request, not at startup",
            targets: &["runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G4.5",
        goal: 4,
        title: "no permanently-stale session after a forced drop",
        tier: Tier::Fast,
        run: pending::p6_reactivity,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G4.5/no-resync-consumer",
            patch: "docs/bluedb/mutations/G4.5.no-resync-consumer.patch",
            expect: "session never converged after the drop",
            targets: &["runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G4.6",
        goal: 4,
        title: "query-scoped NON-delivery (a subscriber outside the predicate is not woken)",
        tier: Tier::Fast,
        run: pending::p6_reactivity,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G4.6/wake-all-subscribers",
            patch: "docs/bluedb/mutations/G4.6.wake-all-subscribers.patch",
            expect: "subscriber outside the predicate was woken",
            targets: &["runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G4.7",
        goal: 4,
        title: "the delta is applied, not used as a go-re-query nudge",
        tier: Tier::Fast,
        run: pending::p6_reactivity,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G4.7/nudge-not-delta",
            patch: "docs/bluedb/mutations/G4.7.nudge-not-delta.patch",
            expect: "a query was issued in response to the changeset",
            targets: &["runtime-go/rt"],
        }]),
    },
    Gate {
        id: "G4.8",
        goal: 4,
        title: "changeset is atomic with the commit",
        tier: Tier::Full,
        run: pending::p6_reactivity,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G4.8/emit-after-commit",
            patch: "docs/bluedb/mutations/G4.8.emit-after-commit.patch",
            expect: "commit visible with no changeset artefact",
            targets: &["runtime-go/rt", "runtime-go/bluedb"],
        }]),
    },
    // -- Goal 5 — console admin access, READ **and** WRITE (P7/P8) ---------
    Gate {
        id: "G5.1",
        goal: 5,
        title: "authorization funnel decision matrix",
        tier: Tier::Fast,
        run: pending::p7_console_read,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.1/reorder-funnel",
            patch: "docs/bluedb/mutations/G5.1.reorder-funnel.patch",
            expect: "a decision was reached before the fail-closed check",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.2",
        goal: 5,
        title: "scoped admin read cannot cross tenants",
        tier: Tier::Fast,
        run: pending::p7_console_read,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.2/where-residual-scoping",
            patch: "docs/bluedb/mutations/G5.2.where-residual-scoping.patch",
            expect: "the adversarial rows appear",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.3",
        goal: 5,
        title: "admin read end-to-end (allow-list disclosure, not a deny-list)",
        tier: Tier::Fast,
        run: pending::p7_console_read,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.3/adminshow-denylist",
            patch: "docs/bluedb/mutations/G5.3.adminshow-denylist.patch",
            expect: "the stripe_sk fixture column renders",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.4",
        goal: 5,
        title: "write authorization matrix",
        tier: Tier::Fast,
        run: pending::p8_console_write,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.4/default-readwrite",
            patch: "docs/bluedb/mutations/G5.4.default-readwrite.patch",
            expect: "a POST succeeded under a read-only decision",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.5",
        goal: 5,
        title: "cross-tenant write rejected and creates no row",
        tier: Tier::Fast,
        run: pending::p8_console_write,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.5/write-key-from-request",
            patch: "docs/bluedb/mutations/G5.5.write-key-from-request.patch",
            expect: "a row appears under T1",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.6",
        goal: 5,
        title: "audit completeness (same transaction, both images)",
        tier: Tier::Full,
        run: pending::p8_console_write,
        budget_s: 900,
        mutations: Mutations::new(&[Mutation {
            id: "G5.6/audit-after-commit",
            patch: "docs/bluedb/mutations/G5.6.audit-after-commit.patch",
            expect: "a write with no audit entry",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.7",
        goal: 5,
        title: "optimistic concurrency — the console cannot cause a lost update",
        tier: Tier::Fast,
        run: pending::p8_console_write,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.7/drop-cas-precondition",
            patch: "docs/bluedb/mutations/G5.7.drop-cas-precondition.patch",
            expect: "a concurrent edit was overwritten",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
    Gate {
        id: "G5.8",
        goal: 5,
        title: "confirm + undo",
        tier: Tier::Fast,
        run: pending::p8_console_write,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G5.8/accept-without-confirm",
            patch: "docs/bluedb/mutations/G5.8.accept-without-confirm.patch",
            expect: "a destructive write executed without a confirm token",
            targets: &["runtime-go/rt/consoledata"],
        }]),
    },
];

/// §9.4 — the canary is the one gate for which a *pass* under mutation is the
/// failure signal.
pub const CANARY_ID: &str = "G0.C";

pub fn find(id: &str) -> Option<&'static Gate> {
    REGISTRY.iter().find(|g| g.id == id)
}

/// The goal-verdict function of §9.3, extended by MAJOR-17: a gate whose
/// mutation proof is no longer known-good (`UNVERIFIED-SINCE`) is treated
/// exactly like `NOT RUN` — the gate may still be PASS, but its *proof* is
/// unrevalidated, so the goal is `UNKNOWN`, not PASS.
///
/// It is deliberately impossible to reach `Pass` from a set containing any
/// non-`Pass` gate.
pub fn goal_verdict(states: &[(GateState, bool /* proof_unknown */)]) -> GoalVerdict {
    if states.is_empty() {
        // A goal with no gate at all. §9.6 check 1 fails the harness outright;
        // rendering it PASS here would be the very collapse this function exists
        // to prevent.
        return GoalVerdict::Fail;
    }
    if states
        .iter()
        .any(|(s, _)| matches!(s, GateState::Fail | GateState::Unproven))
    {
        return GoalVerdict::Fail;
    }
    if states
        .iter()
        .any(|(s, proof_unknown)| matches!(s, GateState::NotRun) || *proof_unknown)
    {
        return GoalVerdict::Unknown;
    }
    GoalVerdict::Pass
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_registered_gate_declares_at_least_one_mutation() {
        // The type-level guarantee (const-eval on `Mutations::new`) is the
        // primary mechanism; this is §9.6 check 3's independent backstop
        // asserted as a unit test so it cannot regress unnoticed.
        for g in REGISTRY {
            assert!(
                !g.mutations.is_empty(),
                "{} declares no mutation — it would render UNPROVEN",
                g.id
            );
        }
    }

    #[test]
    fn every_goal_one_to_five_has_a_gate() {
        for goal in 1..=5u8 {
            assert!(
                REGISTRY.iter().any(|g| g.goal == goal),
                "goal {goal} has no gate"
            );
        }
    }

    #[test]
    fn gate_ids_are_unique() {
        let mut ids: Vec<&str> = REGISTRY.iter().map(|g| g.id).collect();
        ids.sort_unstable();
        let before = ids.len();
        ids.dedup();
        assert_eq!(before, ids.len(), "duplicate gate id in the registry");
    }

    #[test]
    fn mutation_ids_are_unique_and_namespaced_to_their_gate() {
        let mut ids: Vec<&str> = Vec::new();
        for g in REGISTRY {
            for m in g.mutations.as_slice() {
                assert!(
                    m.id.starts_with(g.id) && m.id[g.id.len()..].starts_with('/'),
                    "mutation {} is not namespaced under gate {}",
                    m.id,
                    g.id
                );
                assert!(
                    !m.targets.is_empty(),
                    "mutation {} declares no targets — UNVERIFIED-SINCE could never fire",
                    m.id
                );
                ids.push(m.id);
            }
        }
        ids.sort_unstable();
        let before = ids.len();
        ids.dedup();
        assert_eq!(before, ids.len(), "duplicate mutation id");
    }

    #[test]
    fn goal_verdict_never_collapses_unknown_to_pass() {
        use GateState::*;
        assert_eq!(
            goal_verdict(&[(Pass, false), (NotRun, false)]),
            GoalVerdict::Unknown
        );
        assert_eq!(
            goal_verdict(&[(Pass, false), (Unproven, false)]),
            GoalVerdict::Fail
        );
        assert_eq!(
            goal_verdict(&[(Pass, false), (Fail, false)]),
            GoalVerdict::Fail
        );
        // FAIL wins over NOT RUN — a broken gate is decisive.
        assert_eq!(
            goal_verdict(&[(NotRun, false), (Fail, false)]),
            GoalVerdict::Fail
        );
        // An unrevalidated proof is UNKNOWN even when every gate passed.
        assert_eq!(
            goal_verdict(&[(Pass, false), (Pass, true)]),
            GoalVerdict::Unknown
        );
        assert_eq!(
            goal_verdict(&[(Pass, false), (Pass, false)]),
            GoalVerdict::Pass
        );
        assert_eq!(goal_verdict(&[]), GoalVerdict::Fail);
    }

    #[test]
    fn canary_is_registered_and_is_fast_tier() {
        let c = find(CANARY_ID).expect("the canary must be permanently registered");
        assert_eq!(c.tier, Tier::Fast, "the canary must run in the default tier");
        assert_eq!(c.goal, 0);
    }
}
