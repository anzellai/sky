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
use super::gates_g2;
use super::gates_g2_13;
use super::gates_runtime;
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
        // NARROWED to what the body proves. It read "…and matches a fresh run
        // (hand edits detected)"; the body checks the GENERATED banner and the
        // trailing `body-sha256`, and nothing here re-renders the file to
        // compare against. The fresh-run comparison lives in `--check`, which
        // CANNOT run inside a gate: the fast tier regenerates `STATUS.md` as
        // part of the same invocation, so a post-run comparison always compares
        // the file with itself. `docs/bluedb/P1-STAGE2-PLAN.md` asked for the
        // narrowing AND for `--check` to be wired; the second half is not
        // implementable in this position, so the title states the first.
        title: "STATUS.md is generated output: GENERATED banner + a body-sha256 that matches its body (hand edits detected)",
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
        // TITLE NARROWED TO THE BODY (Judge gap B). It said "builds cold-cache
        // offline". The body builds OFFLINE — `GOPROXY=off`, `GOTOOLCHAIN=local`
        // and a scratch `HOME`, so the build provably fetches nothing — but the
        // module cache is the AMBIENT one, which `offline_build_env`'s own
        // docstring has always said in as many words ("a fresh cache is not
        // asserted").
        //
        // Narrowed rather than strengthened, and the argument is in that
        // docstring: nothing in this repo is vendored, so an empty `GOMODCACHE`
        // plus `GOPROXY=off` cannot build ANY Sky app on ANY branch. A
        // cold-cache arm would be permanently red for a property no code has
        // ever had, and a gate that is always red for a reason unrelated to the
        // code is a gate people stop reading — the same failure mode as a gate
        // that is always green. A caller with a genuinely fresh, pre-seeded
        // cache supplies it via `SKY_BLUEDB_GOMODCACHE`, and the PASS detail
        // then names it, so the arm reports which cache it used instead of
        // claiming one it did not have.
        title: "non-Persist app links no pebble, builds offline (GOPROXY=off, scratch HOME, ambient module cache), ships no bluedb/, keeps its non-`data` session store",
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
    // TITLE RE-CUT TO THE INVARIANT THE BODY PROVES (Judge gap A). It read "one
    // go build site; it carries -tags pebblegozstd so all three call paths
    // inherit it", and BOTH halves were false against correct code.
    //
    // There is not one site. There are three in the compiler — the app build
    // (`project::build`), the hub build (`sky::main`, `cmd/sky-hub` inside
    // `runtime-go/`) and the inspector build (`ffi::inspect`, `sky-ffi-inspect`)
    // — plus the gate's own cgo re-link. Two of the three shipped with NO
    // `-tags`, and the scan read `rust/crates/project/src` only, so the gate
    // that exists to prevent exactly this defect could not see either of them.
    //
    // "Exactly 1 site" was therefore a claim the tree falsifies, and its
    // declared mutation — "add a second site" — is satisfied by real shipped
    // code, which makes it a mutation that proves nothing. The invariant that
    // survives is the one the count was only ever a proxy for: EVERY site
    // carries the tag, however many there are. The re-cut mutation adds an
    // UNTAGGED site (in `sky/src/main.rs`, so it also falsifies the widening
    // itself — the old scan could not have seen it).
    Gate {
        id: "G0.5",
        goal: 0,
        title: "every go build site under rust/crates carries -tags pebblegozstd; a Persist binary links no cgo DataDog zstd",
        tier: Tier::Fast,
        run: gates_g0::g0_5_zstd_tag,
        budget_s: 30,
        mutations: Mutations::new(&[Mutation {
            id: "G0.5/untagged-go-build-site",
            patch: "docs/bluedb/mutations/G0.5.untagged-go-build-site.patch",
            expect: "does not carry `-tags pebblegozstd`",
            targets: &["rust/crates/sky/src/main.rs"],
        }]),
    },
    // TITLE NARROWED TO THE BODY (Judge gap 4). It used to read "every gate's
    // recorded mutation still applies and still turns it red", which is
    // `--verify-mutations`' claim, not this gate's: `g0_6_mutations_verified`
    // never calls `git apply` and never runs a gate. It audits the LEDGER that
    // run wrote — the patch file exists, the ledger has an entry, the entry
    // records PROVEN (VACUOUS for the canary), the recorded RED transcript
    // contains the declared assertion, and no declared target has moved since the
    // sha the proof was taken at.
    //
    // That distinction is the whole reason the gate is cheap (0.4s) and
    // `--verify-mutations` is not (~1h). A title claiming the expensive property
    // for the cheap gate is exactly the thing a compacted session inherits and
    // cannot check, so it says what it does.
    //
    // NO MUTATION LISTS A `*_test.go` IN `targets`, so `targets_moved` never fires
    // on a fixture edit. That is deliberate, and it is not a hole:
    //
    //   * The fixture-side dependency IS real — a reworded `t.Fatalf` can make a
    //     mutation's `expect` unreachable — but it is already covered by a check
    //     that is strictly stronger than a staleness hint.
    //     `every_pinned_leaf_is_reddened_by_a_recorded_mutation` (gates_g2_13.rs)
    //     resolves each leaf's body out of the Go source ON EVERY `cargo test` and
    //     requires the mutation's `expect` to still live in it; G0.6 itself
    //     requires the recorded transcript to still contain it. Both read the tree
    //     as it is, rather than asking whether a file changed.
    //   * `audit_test.go` is ONE file behind ~20 mutations across 14 gates. Listing
    //     it would decay every one of them on any edit to the corpus — including
    //     adding an unrelated fixture — which is the "signal that always fires is a
    //     signal nobody reads" rule this harness already applies to
    //     `gate-state.tsv`, `STATUS.md` and `*.expected.txt` (see
    //     `mutations::harness_generated`).
    //
    // So the answer is recorded rather than fixed: the decay clock stays on the
    // SUBJECT of the patch, and the fixture side is guarded by assertions that run
    // more often and say more.
    Gate {
        id: "G0.6",
        goal: 0,
        // TITLE NARROWED AGAIN, and the BODY STRENGTHENED with it (Judge gap B).
        // It said "every mutation's …", and the body exempted every patch-less
        // mutation — 39 of the 90 — then PASSed. The exemption is right for a
        // gate that has no body (there is nothing for a mutation to falsify),
        // and indefensible for one that RUNS and reports PASS. It could not tell
        // the two apart, so it granted the first to both.
        //
        // `gate_does_not_run` now decides by EXECUTING the gate's own body and
        // reading `NotRun`, so the exemption is machine-derived rather than
        // author-granted. All 39 sit on `pending::*` substrate probes today —
        // verified by the check, not asserted here — and an unauthored mutation
        // on a gate that runs is now a FINDING.
        title: "every mutation of a gate that RUNS has a current ledger proof: it exists, records PROVEN, carries its RED output, and has not decayed",
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
            //
            // Naming the directory has the same problem one level down — the
            // runner writes the `*.expected.txt` files INSIDE it — which is why
            // the staleness clock filters the diff through
            // `mutations::harness_generated` rather than relying on this list
            // being hand-pruned. Hand-authored `*.patch` files here still decay
            // the proof, as they must.
            targets: &["docs/bluedb/mutations"],
        }]),
    },
    // TITLE MATCHED TO THE BODY (Judge gap 5), and the BODY tightened first.
    //
    // It used to read "every cited file:line resolves on its tagged branch". For a
    // citation with no adjacent backticked identifier the only check was
    // `c.line > n_lines` — "does the file have that many lines" — and for one WITH
    // an identifier not even that: presence at the cited line was a warning that
    // never entered `findings`, and the EOF check sat in the `else` arm, so
    // `foo.go:99999` passed as long as `foo` appeared anywhere in the file.
    //
    // The body now checks the line bound for EVERY citation (see `check_citations`)
    // — a strengthening that cannot invent a failure, because a line past EOF is
    // wrong however the citation is written. The remaining gap is deliberate and
    // §9.6's: whether the cited line is the RIGHT line stays a warning, because
    // line numbers drift on every edit of a cited file and a checker that fires on
    // all of them is one nobody reads. The title therefore claims resolution and
    // range, and says out loud that the line itself is a warning.
    Gate {
        id: "G0.7",
        goal: 0,
        // TITLE NARROWED TO THE BODY (Judge gap B). It said "every citation
        // resolves". The body reads ONE document — `docs/bluedb/v2-architecture.md`
        // — and skips fenced blocks inside it, so "every" was quantified over a
        // set the reader could not see.
        //
        // Narrowed rather than widened, and the widening was MEASURED first:
        // `docs/bluedb/` carries 39 further `path:line` citations (35 in
        // `P1-STAGE2-PLAN.md`, 3 in `RESUME.md`, 1 in the generated `STATUS.md`).
        // They are not written in this grammar — almost none carries a
        // `[main]`/`[bdb]`/`[p5e]`/`[exp]` tag, and a large share cite PEBBLE's
        // own source (`db.go:885`, `version_state.go:191`,
        // `pebble/v2@v2.1.6/snapshot.go:62-69`), which resolves against the Go
        // module cache and against no branch this vocabulary knows. Checking
        // them would produce dozens of findings that are not defects, and a
        // gate that cries wolf is one nobody reads.
        //
        // The fenced-block exclusion is likewise stated rather than removed: a
        // fenced block here is an ILLUSTRATION (the §9.3 `STATUS.md` sample, the
        // §9.2 struct sketches), and its `committer.go:214` is invented for the
        // example.
        title: "harness self-integrity + every citation in docs/bluedb/v2-architecture.md outside its fenced illustrations resolves: one file, in-range line, named token present (a MOVED line is a warning)",
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
    // G0.8 — the CLASS behind four rounds of one-leaf-at-a-time findings.
    //
    // Round 3 hardened the producer of `collWitness`; round 4 deleted the
    // consumer, one file away, and every gate stayed green. Each fix was local to
    // the site attacked, and the mechanically answerable question nobody asked
    // was: which engine sources are touched by NO recorded mutation? Across the
    // 51 patches then in `docs/bluedb/mutations/`, zero touched `validate.go` or
    // `readset.go` — both named verbatim in P1's scope row — and eight of the
    // seventeen non-test sources were in that state.
    //
    // The population is `read_dir`, never a list; coverage is read from the
    // patches' own `diff --git` paths, never from `Mutation.targets` (which is
    // deliberately broader — see `mutations.rs`, which refuses to read it for the
    // same reason). The exemption list carries an argument AND a `funcs` pin
    // reconciled both ways, so an exemption written once cannot silently cover
    // behaviour written later. The body and its argument live in
    // `gates_runtime.rs`.
    Gate {
        id: "G0.8",
        goal: 0,
        title: "every non-test source in runtime-go/bluedb/ is touched by a recorded mutation, or is deliberately unmutated with an argument and a reconciled `funcs` pin",
        tier: Tier::Fast,
        run: gates_runtime::g0_8_engine_sources_are_mutation_covered,
        budget_s: 60,
        mutations: Mutations::new(&[Mutation {
            id: "G0.8/a-mutation-stops-touching-the-source-it-proved",
            patch: "docs/bluedb/mutations/G0.8.a-mutation-stops-touching-the-source-it-proved.patch",
            // The decay this gate exists to catch, in the exact shape rounds 3
            // and 4 took: a file's ONLY falsifier stops reaching it and nothing
            // says so. `recent_changes.go` — the ring that IS the SSI validation
            // window — is touched by exactly one recorded mutation, and the patch
            // re-points that mutation at a different diff.
            //
            // It is also the proof that reading the PATCH rather than the
            // declaration is load-bearing. `Mutation.targets` still names
            // `recent_changes.go` under this patch, so a gate that trusted the
            // declared targets would report the file covered and stay green;
            // only reading what `git apply` would actually change sees it.
            //
            // The obvious alternative — a patch that ADDS an unmutated engine
            // source — cannot work here and the reason is worth recording: the
            // patch that creates the file NAMES the file, so the gate reads it
            // as covered by that very mutation. Tried, observed green, replaced.
            expect: "no recorded mutation's patch touches this engine source",
            targets: &["rust/crates/xtask/src/bluedb_gates/registry.rs"],
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
        run: gates_g2::g2_6_injection_manifest,
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
        run: gates_g2::g2_9a_durability_on_ack,
        budget_s: 900,
        // THREE mutations, because the gate pins seven leaves and the first one
        // alone left three of them falsified only in name. `NoSync` turns all
        // seven red, but for the seal contract, the injected-fault fixture and
        // the durable-prefix fixture the line it reddens is that fixture's own
        // PRECONDITION guard ("the WAL-fsync injector fired ZERO times … this
        // test proves NOTHING", "not one of the 322 acked commits survived …
        // the prefix property below was never exercised") — the property is
        // never reached. Two of the three now have a mutation that reaches it;
        // the third's argument, and its source-side falsifier, are recorded in
        // `gates_g2_13.rs`'s LEAF_COVERAGE and SOURCE_SIDE_FALSIFIERS.
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.9a/ack-before-fsync",
                patch: "docs/bluedb/mutations/G2.9.ack-before-fsync.patch",
                expect: "acked write missing after restart",
                targets: &["runtime-go/bluedb"],
            },
            Mutation {
                id: "G2.9a/sealed-engine-still-runs-gc",
                patch: "docs/bluedb/mutations/G2.9a.sealed-engine-still-runs-gc.patch",
                // Verbatim from the observed failure with gc.go's `sealed` check
                // reverted: the engine sealed on the injected WAL fault (the
                // fixture's earlier arms all pass) and then ran a GC pass anyway.
                // "Every write path refuses loudly" includes the one that deletes.
                expect: "sealed engine must refuse GC with ErrSealed, got",
                targets: &["runtime-go/bluedb/gc.go"],
            },
            Mutation {
                id: "G2.9a/wal-fatal-never-reaches-the-ack",
                patch: "docs/bluedb/mutations/G2.9a.wal-fatal-never-reaches-the-ack.patch",
                // Verbatim. Deleting N3 consumption point 3/5 leaves the injector
                // FIRING — so the fixture's precondition guard passes and the run
                // reaches the property — while a latched WAL fatal never reaches
                // the ack: the commit acks nil and its write is gone after reopen.
                expect: "ABSENT after reopen — acked⇒durable violated",
                targets: &["runtime-go/bluedb/committer.go"],
            },
        ]),
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
    // G2.13a–h — the audit corpus, ONE GATE PER PROPERTY.
    //
    // Eight gates rather than one gate with eight mutations, because
    // `mutations.rs` classifies with `red.exit_ok || !red.output.contains(
    // m.expect)`: it checks only that THIS mutation's assertion fired, never
    // that the others did not. Eight mutations on one gate would let a
    // single defect that broke several properties mint eight PROVENs out of one
    // undifferentiated failure. See `gates_g2_13.rs`'s module doc and
    // `expect_strings_are_pairwise_discriminating` below, which closes the
    // other half of that hole for the whole registry.
    //
    // Titles name the PROPERTY, not the defect id: a gate is a statement about
    // what holds, and "N1" is only a pointer to when it did not.
    Gate {
        id: "G2.13a",
        goal: 2,
        title: "Iterate bounds do not leak rows across collections",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13a_iterate_bounds,
        budget_s: 300,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13a/iterate-bounds-end-in-a-user-byte",
                patch: "docs/bluedb/mutations/G2.13a.iterate-bounds-end-in-a-user-byte.patch",
                // Verbatim from the observed failure of collNameLen=30 under the
                // reverted bound construction: 2 rows where 1 was required.
                //
                // It is the LEAKAGE branch specifically. Until 2026-08-14 the
                // fixture printed both diagnoses from one `t.Fatalf` on any
                // row-count deviation, so this string appeared in a transcript
                // whose actual failure was `returned 0 rows` — the opposite
                // regime — and the declared assertion could not discriminate
                // its own defect. The fixture now emits one assertion per
                // regime; this names the >1-row one.
                expect: "cross-collection leakage (another collection's rows scanned as this one's)",
                targets: &["runtime-go/bluedb/reader.go"],
            },
            // The two SHORT collection names (28, 29) are correct by luck of
            // the length, so no revert of N1's fix can redden them — they were
            // pinned leaves falsified by nothing. A degenerate `[lower, lower)`
            // upper bound is the same class (a silent empty collection) at
            // EVERY length, so it covers the two controls as well as the six.
            Mutation {
                id: "G2.13a/degenerate-upper-bound",
                patch: "docs/bluedb/mutations/G2.13a.degenerate-upper-bound.patch",
                // Verbatim from the observed failure, and the ZERO-ROW branch —
                // deliberately the other one, so the two mutations of this gate
                // prove the two regimes separately.
                expect: "inverted bounds (a silent empty collection): the scan is indistinguishable from a",
                targets: &["runtime-go/bluedb/reader.go"],
            },
        ]),
    },
    Gate {
        id: "G2.13b",
        goal: 2,
        title: "a failed scan surfaces an error, not an empty collection",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13b_failed_scan_is_an_error,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.13b/failed-scan-reads-as-an-empty-collection",
            patch: "docs/bluedb/mutations/G2.13b.failed-scan-reads-as-an-empty-collection.patch",
            // Verbatim from the observed failure: the write-set overlay alone
            // came back as a one-row collection instead of an error.
            expect: "a partial/write-set-only collection is worse than an error",
            // txn.go only: N1's fix is in reader.go, so the two patches — and
            // therefore the two gates — cannot trigger each other.
            targets: &["runtime-go/bluedb/txn.go"],
        }]),
    },
    Gate {
        id: "G2.13c",
        goal: 2,
        title: "a mis-sized hlc_hi refuses to open and never re-issues a commitTs",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13c_corrupt_hlc_hi,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.13c/corrupt-hlc-hi-reads-as-a-fresh-store",
            patch: "docs/bluedb/mutations/G2.13c.corrupt-hlc-hi-reads-as-a-fresh-store.patch",
            // Verbatim: the fixture asserts the CONSEQUENCE, so the recorded
            // failure is the restarted clock, not merely "openWith succeeded".
            expect: "and the commit clock RESTARTED:",
            targets: &["runtime-go/bluedb/pebble_engine.go"],
        }]),
    },
    Gate {
        id: "G2.13d",
        goal: 2,
        // The title said "a closed ENGINE" until 2026-08-14, and the fixture it
        // names does not close one. `audit_test.go:271-281` closes the COMMIT
        // CHANNEL out from under a live engine — and asserts, explicitly, that
        // the engine is neither closed nor sealed — so that `Commit` meets a
        // `send on closed channel` panic on a store that still looks healthy.
        // That is the C1 defect: the panic was recovered and the caller acked
        // `nil` for a write that was never enqueued. A title naming a closed
        // engine described a different (and easier) scenario, and a reader
        // checking STATUS.md against the corpus would have found no fixture for
        // it.
        title: "a commit whose channel closed under it does not ack success",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13d_no_false_ack,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.13d/commit-on-a-closed-engine-acks-success",
            patch: "docs/bluedb/mutations/G2.13d.commit-on-a-closed-engine-acks-success.patch",
            // Verbatim. The patch reverts ONLY the recover body and leaves the
            // named return in place, so it also reproduces the half-fix the
            // plan names as risk 4 — the diff that looks like the fix and is not.
            expect: "the write was never enqueued, never applied and is not durable",
            targets: &["runtime-go/bluedb/pebble_engine.go"],
        }]),
    },
    Gate {
        id: "G2.13e",
        goal: 2,
        title: "a Snapshot's readTs is pinned with its snapshot",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13e_readts_pinned_with_snapshot,
        budget_s: 300,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13e/snapshot-readts-is-the-in-memory-high-water",
                patch: "docs/bluedb/mutations/G2.13e.snapshot-readts-is-the-in-memory-high-water.patch",
                // Verbatim. The patch moves ONLY the readTs choice: N4's
                // closed-check-and-pin section stays, so G2.13f is untouched by it.
                expect: "the ASSIGNED-but-unapplied commitTs.",
                targets: &["runtime-go/bluedb/pebble_engine.go"],
            },
            // The PROPERTY arm is insensitive to the mutation above — a readTs
            // that is too HIGH still leaves every acked commit visible — so it
            // was a pinned leaf falsified by nothing. What violates "sees every
            // commit at or below its readTs" is the visibility boundary itself:
            // `commitTs < readTs` instead of `<=`, which makes a reader unable
            // to serve the very commit it names.
            Mutation {
                id: "G2.13e/mvcc-visibility-excludes-the-readts-itself",
                patch: "docs/bluedb/mutations/G2.13e.mvcc-visibility-excludes-the-readts-itself.patch",
                // Verbatim from the property arm's own failure.
                expect: "Its readTs names a commit outside its own pinned snapshot — defect H1.",
                targets: &["runtime-go/bluedb/reader.go"],
            },
        ]),
    },
    Gate {
        id: "G2.13f",
        goal: 2,
        // FULL, not fast: the drain arms carry real timeouts (a 20s
        // `closeWithin` with a 20s guard behind it, a 15s hang guard on the
        // leaked-reader arm). The passing case is ~2s; the budget is sized for
        // the failing one, because a gate that outruns its budget is a FAIL and
        // a FAIL for the wrong reason proves nothing.
        title: "Close quiesces readers instead of racing them",
        tier: Tier::Full,
        run: gates_g2_13::g2_13f_close_quiesces_readers,
        budget_s: 600,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13f/close-does-not-quiesce-readers",
                patch: "docs/bluedb/mutations/G2.13f.close-does-not-quiesce-readers.patch",
                // Verbatim from the observed failure: Close returned pebble's own
                // "leaked snapshots" error while the transaction's reader was pinned.
                expect: "handle underneath a live reader, whose next operation panics inside pebble",
                targets: &["runtime-go/bluedb/pebble_engine.go"],
            },
            // Two of this gate's four arms were falsified by nothing. Each gets
            // the mutation that reddens it DETERMINISTICALLY — neither relies
            // on winning the race the arm is about, because a mutation that
            // only sometimes fires records VACUOUS the times it does not.
            //
            // The concurrent-snapshot arm ends in two post-Close assertions:
            // a closed engine must refuse, not read as an empty store.
            Mutation {
                id: "G2.13f/closed-engine-reads-as-an-empty-store",
                patch: "docs/bluedb/mutations/G2.13f.closed-engine-reads-as-an-empty-store.patch",
                // Verbatim. `snapshotAt` returns a reader whose Err() is nil, so
                // the time-travel path after Close reads as an empty store and
                // a transaction has nothing to fail closed on. Never reaches
                // pebble, so it cannot panic the test binary instead.
                expect: "snapshotAt() after Close reports Err() =",
                targets: &["runtime-go/bluedb/pebble_engine.go"],
            },
            // The Begin-path arm: the ORDER of the two statements in
            // pebbleReader.Close, which C7 recorded rather than fixed.
            Mutation {
                id: "G2.13f/token-released-before-the-snapshot",
                patch: "docs/bluedb/mutations/G2.13f.token-released-before-the-snapshot.patch",
                expect: "snapshot(s) STILL OPEN. The token is what the close drain counts, so between that",
                targets: &["runtime-go/bluedb/reader.go"],
            },
        ]),
    },
    Gate {
        id: "G2.13g",
        goal: 2,
        title: "a failed point read is an error, not an absent row",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13g_failed_read_is_an_error,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.13g/failed-point-read-reads-as-an-absent-row",
            patch: "docs/bluedb/mutations/G2.13g.failed-point-read-reads-as-an-absent-row.patch",
            // Verbatim, and it is assertion 1 of the fixture — the FLAG. The
            // fixture uses Errorf there precisely so assertion 2 (the txn
            // failing closed) still runs; both fire under this patch.
            expect: "Get swallowed an injected SSTable read fault: ok=false with Err() == nil",
            targets: &["runtime-go/bluedb/reader.go"],
        }]),
    },
    Gate {
        id: "G2.13h",
        goal: 2,
        // The FAIL-OPEN class, as a property: an error on the commit/validation
        // route must fail the operation CLOSED rather than return a plausible
        // zero a later transaction then validates against. Four fixtures, four
        // doors — `pending`, the recent-changes ring, the watermark registry,
        // the ring's cold-start seed — all ending in under-rejection.
        //
        // Until this gate existed those four were recorded in AUDIT_OWNERSHIP
        // as run by NO gate: CI's `go test ./bluedb/...` executed them and
        // nothing else did, so they were invisible to --verify-mutations, to
        // STATUS.md and to every goal verdict. This is the class that produced
        // N6 *and* a second instance in the same file.
        title: "the commit/validation route fails closed",
        // Measured at 0.39s wall for all four fixtures — no timed arm, unlike
        // G2.13f's drains.
        tier: Tier::Fast,
        run: gates_g2_13::g2_13h_commit_route_fails_closed,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.13h/undecodable-payload-validates-as-no-changes",
            patch: "docs/bluedb/mutations/G2.13h.undecodable-payload-validates-as-no-changes.patch",
            // Verbatim from the observed failure of the N6 arm under the
            // reverted decodePayload: the blind job committed at
            // {WallMs:41000 Logical:2} and the txn that had read the key
            // committed at Logical:3, in one drain window.
            //
            // Chosen from the CONSEQUENCE assertion (audit_test.go:1280), not
            // from the remedy assertion below it: "an undecodable payload
            // returns an error" would also be satisfied by a fix that returns
            // the error and then ignores it. It is one Go string-literal
            // segment, so it survives `every_declared_assertion_is_verbatim_
            // in_the_fixture_that_emits_it`.
            expect: "did not decode, so it contributed NOTHING to `pending`, and the txn validated",
            // committer.go only. The N6 fix and C6b's blind-path decode both
            // route through decodePayload there; reader.go / txn.go /
            // pebble_engine.go — G2.13a–g's targets — are untouched.
            targets: &["runtime-go/bluedb/committer.go"],
        },
        // FOUR mutations, one per door, because the gate pins six leaves and
        // the mutation above reddens three of them. The other three — the N6
        // CONTROL arm and the two remaining C6b doors — were falsified by
        // nothing, and an empty Go test emits `pass`, so their bodies could
        // have been gutted with this gate staying green AND PROVEN. See
        // `gates_g2_13.rs`'s LEAF_COVERAGE, which records the leaf each
        // mutation's RED transcript actually turned red and is checked against
        // that transcript.
        Mutation {
            id: "G2.13h/pending-window-does-not-see-the-batch",
            patch: "docs/bluedb/mutations/G2.13h.pending-window-does-not-see-the-batch.patch",
            // Verbatim from the observed failure of the CONTROL arm with the
            // intra-batch half of the window dropped from validate()'s input:
            // the txn that had read the key committed clean.
            expect: "validate() do not detect this conflict at",
            targets: &["runtime-go/bluedb/committer.go"],
        },
        Mutation {
            id: "G2.13h/advance-on-an-unknown-token-returns-nil",
            patch: "docs/bluedb/mutations/G2.13h.advance-on-an-unknown-token-returns-nil.patch",
            // Verbatim from the observed failure with Advance's
            // ErrUnknownReader restored to the pre-fix `return nil`.
            expect: "the caller then reads at a readTs GC may collect underneath it",
            targets: &["runtime-go/bluedb/watermark.go"],
        },
        Mutation {
            id: "G2.13h/corrupt-cold-start-seed-leaves-the-floor-low",
            patch: "docs/bluedb/mutations/G2.13h.corrupt-cold-start-seed-leaves-the-floor-low.patch",
            // Verbatim from the observed failure with the per-entry decode
            // error dropped on the floor again: floor {0,0} where persistedHi
            // was required.
            expect: "lower floor means after(readTs) answers `not spilled` for a range the ring does NOT",
            targets: &["runtime-go/bluedb/pebble_engine.go"],
        },
        // The FIFTH door, and the one G0.8 asked for: `recent_changes.go` — the
        // ring that IS the SSI validation window — was touched by no recorded
        // mutation at all, so nothing had shown a line of it to be load-bearing.
        // The mutation is the one its own docstring names ("A `return nil,
        // false` here would be the exact N6 shape"): delete the floor check, and
        // a reader below the retained window is handed whatever entries survive,
        // reported as a COMPLETE window, so the committer never diverts it to
        // Changelog.Tail. Same class as the four above, one file over, and it
        // reddens exactly one leaf.
        Mutation {
            id: "G2.13h/ring-answers-for-a-range-it-does-not-hold",
            patch: "docs/bluedb/mutations/G2.13h.ring-answers-for-a-range-it-does-not-hold.patch",
            // Verbatim from the observed failure, and from the SECOND assertion
            // of the cold-start fixture rather than the first: the floor is still
            // raised correctly under this patch (that is the mutation above), so
            // the fixture reaches the arm that asks whether the floor actually
            // diverts anything.
            expect: "a readTs below the raised floor did not report spilled",
            targets: &["runtime-go/bluedb/recent_changes.go"],
        },
        ]),
    },
    // G2.13i — the two Stage-1 remedies that shipped with NO test.
    //
    // `gc.go`'s corrupt-key count + per-pass abort and `changelog.go`'s
    // fail-closed on a malformed key were both authored, both argued for in
    // their docstrings, and neither was ever executed by anything. The second is
    // the one `P1-STAGE2-PLAN.md` ranks risk #5 — "`changelog.go` gets
    // `continue` by mechanical analogy with `gc.go`, silently breaking
    // serializability" — so the remedy against the plan's own named risk was
    // unguarded.
    //
    // TWO mutations, because there are two files and two doors: a single
    // mutation would leave whichever half it did not touch falsifiable by
    // nothing, which is precisely the finding this gate exists in answer to.
    Gate {
        id: "G2.13i",
        goal: 2,
        title: "corrupt keys fail the operation closed (GC counts + aborts; the changelog refuses)",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13i_corrupt_keys_fail_closed,
        budget_s: 300,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13i/gc-skips-corrupt-keys-without-bound",
                patch: "docs/bluedb/mutations/G2.13i.gc-skips-corrupt-keys-without-bound.patch",
                // Verbatim from the observed failure of the counted-skip arm
                // under a bare `continue`: CorruptKeys = 0 where 3 were met.
                expect: "A skip that is not counted is a permanent and INVISIBLE leak of the fault",
                targets: &["runtime-go/bluedb/gc.go"],
            },
            Mutation {
                id: "G2.13i/changelog-skips-a-corrupt-key",
                patch: "docs/bluedb/mutations/G2.13i.changelog-skips-a-corrupt-key.patch",
                // Verbatim from the observed failure under the plan's risk #5:
                // Tail returned 3 entries and err=nil over a malformed key.
                expect: "A changelog key that does not parse must fail the read, never be skipped",
                targets: &["runtime-go/bluedb/changelog.go"],
            },
        ]),
    },
    // G2.13j / G2.13k — the three fixtures commit `ad9b3900` landed with its
    // fix, and which for one commit were run by CI's `go test ./bluedb/...` and
    // by NOTHING else: invisible to --verify-mutations, to STATUS.md and to every
    // goal verdict. `AUDIT_OWNERSHIP` said so, in the word it keeps for it.
    //
    // TWO gates, not one, and the split is the doctrine rather than a preference:
    // a handle's lifecycle and a committer's post-ack fault handling are two
    // properties, no single defect breaks both, and STATUS.md should carry a row
    // for each. Folding them together would be the "one gate with several
    // mutations" shape the G2.13a–i split exists to refuse.
    Gate {
        id: "G2.13j",
        goal: 2,
        title: "the exported non-reader surface is pinned against Close",
        // FULL: both arms carry real waits sized for the FAILING case (a 30s
        // per-worker report deadline, a 60s closeWithin, a 90s pass deadline).
        // Passing wall time is 0.8s; a gate that outruns its budget is a FAIL for
        // the wrong reason.
        tier: Tier::Full,
        run: gates_g2_13::g2_13j_lifecycle_pins_the_exported_surface,
        budget_s: 480,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13j/changelog-handed-out-without-a-pin",
                patch: "docs/bluedb/mutations/G2.13j.changelog-handed-out-without-a-pin.patch",
                // Verbatim from the observed failure with `Changelog()` reverted
                // to `&changelog{db: e.db}`: 6/6 changelog workers took an
                // unrecovered "pebble: closed" on their own goroutines, and the
                // held handle panicked again after Close had fully returned.
                expect: "A pebble handle operation on a closed DB panics unconditionally, on the CALLER's",
                targets: &["runtime-go/bluedb/pebble_engine.go", "runtime-go/bluedb/changelog.go"],
            },
            Mutation {
                id: "G2.13j/gc-checks-closed-without-pinning",
                patch: "docs/bluedb/mutations/G2.13j.gc-checks-closed-without-pinning.patch",
                // Verbatim, and from the OTHER fixture — which is the whole
                // reason there are two. This revert leaves
                // `…DoNotRaceCloseIntoAPanic` GREEN (gc.go's isClosed() does
                // answer a call made after Close returned), so only a fixture
                // that puts Close INSIDE a pass can see it.
                //
                // THE ASSERTION IS THE PROPERTY, NOT THE SIDE THAT OBSERVED IT,
                // and this one was authored the other way round. It read
                // `Close PANICKED with an unpinned GC pass in flight:` — the
                // verdict raised when CLOSE touches the closed handle first.
                // The pass can touch it first instead, and then the fixture
                // fails on its other arm with a message this string does not
                // appear in. The gate is red either way; the CLASSIFIER is not,
                // because it asks only whether this exact string is present.
                //
                // Measured rather than reasoned about: 12 runs of the mutated
                // fixture, 12 red, split 7 (Close's side) / 5 (the pass's side).
                // So the recorded PROVEN was a ~58% coin flip, and the original
                // "fires on every run (5/5) by construction" was right about the
                // GATE and wrong about the ASSERTION — 5/5 was luck on which
                // side won. A falsifier that reports PROVEN most of the time is
                // worse than none, because nobody looks again.
                //
                // Both arms now carry one shared verdict clause naming the
                // property, so the classification no longer depends on who wins
                // the race. Re-measured at 12/12 after the change.
                expect: "an unpinned GC pass raced Close into a panic",
                targets: &["runtime-go/bluedb/gc.go"],
            },
        ]),
    },
    Gate {
        id: "G2.13k",
        goal: 2,
        title: "a post-ack durability panic is never absorbed",
        // Measured at 0.15s: the fault comes through a seam and neither arm waits.
        tier: Tier::Fast,
        run: gates_g2_13::g2_13k_post_ack_panic_is_never_absorbed,
        budget_s: 300,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13k/post-ack-panic-absorbed-on-the-blind-path",
                patch: "docs/bluedb/mutations/G2.13k.post-ack-panic-absorbed-on-the-blind-path.patch",
                // Verbatim. The patch restores `if r := recover(); r != nil &&
                // !acked` on the blind path ONLY, and the txn arm is observed
                // still PASSING under it — so the two arms are proven separately
                // rather than by one undifferentiated failure of their parent.
                expect: "a panic raised AFTER processBlindPhase1's acks went out was SILENTLY ABSORBED.",
                targets: &["runtime-go/bluedb/committer.go"],
            },
            Mutation {
                id: "G2.13k/post-ack-panic-absorbed-on-the-txn-path",
                patch: "docs/bluedb/mutations/G2.13k.post-ack-panic-absorbed-on-the-txn-path.patch",
                // Verbatim, and the mirror image: the blind arm is observed
                // PASSING under this one.
                expect: "a panic raised AFTER processTxn's acks went out was SILENTLY ABSORBED.",
                targets: &["runtime-go/bluedb/committer.go"],
            },
        ]),
    },
    // G2.13l — the OTHER half of N3, and the half that was gated 1-in-7.
    //
    // `quietLogger.Fatalf` latches instead of panicking; the value of that is
    // entirely in the CONSUMPTION, at every exit that would otherwise report
    // success. Each consumption point is an independently deletable hunk, and
    // deleting each in turn and running the whole suite left FIVE OF SIX GREEN —
    // including `pebble_engine.go`'s Commit door, which the source itself calls
    // decisive ("without this check the fix trades a process kill for a silent,
    // permanent hang of every writer"). The one that was covered,
    // `committer.go`'s blind-path fold, was covered by G2.9a, whose subject is
    // durability-on-ack rather than the latch.
    //
    // FIVE mutations, not six: the blind-path fold's revert is already
    // `G2.9a/wal-fatal-never-reaches-the-ack`, and two mutations of one hunk are
    // one proof counted twice. That arm's per-leaf falsifier is the source anchor
    // on its own assertion. The post-Open check has no mutation either, and its
    // argument is recorded — with the measurements behind it — in
    // `gates_g2_13.rs`'s N3_CONSUMPTION_POINTS.
    Gate {
        id: "G2.13l",
        goal: 2,
        title: "the N3 Fatalf latch is consumed at every exit that could claim success",
        // Measured at 0.37s for all six arms: each latches directly and waits on
        // nothing, which is the point of latching directly.
        tier: Tier::Fast,
        run: gates_g2_13::g2_13l_latch_is_consumed_at_every_exit,
        budget_s: 300,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.13l/commit-door-does-not-consult-the-latch",
                patch: "docs/bluedb/mutations/G2.13l.commit-door-does-not-consult-the-latch.patch",
                // Verbatim. With the door deleted the job reaches the committer,
                // whose blind-path fold DOES answer — so the ack still names the
                // pebble fatal and only the SHAPE discriminates: the door refuses
                // with ErrSealed before anything is written, the fold reports
                // after the write is durable.
                expect: ", which is not ErrSealed — the ",
                targets: &["runtime-go/bluedb/pebble_engine.go"],
            },
            Mutation {
                id: "G2.13l/transactional-drain-does-not-fold-the-latch",
                patch: "docs/bluedb/mutations/G2.13l.transactional-drain-does-not-fold-the-latch.patch",
                // Verbatim. The blind path keeps its fold, so this reddens the
                // transactional arm alone: a validated transaction acks nil over a
                // batch pebble has already declared unrecoverable.
                expect: " with a fatal latched. It is a second ",
                targets: &["runtime-go/bluedb/committer.go"],
            },
            Mutation {
                id: "G2.13l/gc-threshold-persist-does-not-fold-the-latch",
                patch: "docs/bluedb/mutations/G2.13l.gc-threshold-persist-does-not-fold-the-latch.patch",
                // Verbatim, and note WHICH assertion fires: the pass still errors
                // (the delete pass folds instead), so the discriminating half is
                // that it DELETED under a floor it could not establish.
                expect: "was DELETED by a pass whose own threshold write it ",
                targets: &["runtime-go/bluedb/gc.go"],
            },
            Mutation {
                id: "G2.13l/gc-delete-pass-does-not-fold-the-latch",
                patch: "docs/bluedb/mutations/G2.13l.gc-delete-pass-does-not-fold-the-latch.patch",
                // Verbatim. Reached with `advanced == false`, so persistThreshold
                // is skipped and this Apply is the only consumer left.
                expect: "the delete pass applied its batch and reported err = ",
                targets: &["runtime-go/bluedb/gc.go"],
            },
            Mutation {
                id: "G2.13l/close-discards-a-fatal-latched-after-the-last-ack",
                patch: "docs/bluedb/mutations/G2.13l.close-discards-a-fatal-latched-after-the-last-ack.patch",
                // Verbatim. Close is the last consumer; with its join deleted the
                // verdict is a bare nil and the fatal is gone with the handle.
                expect: " with a fatal latched. A background flush or compaction can ",
                targets: &["runtime-go/bluedb/pebble_engine.go"],
            },
        ]),
    },
    // G2.13m — H3's live sibling, and the fixture that landed without a gate.
    //
    // `TestAuditH3ScanSurfacesIoErrorsAtTheCommitBoundary` arrived in `b540bed2`
    // with the H3b fix it pins and sat in AUDIT_OWNERSHIP as UNGATED: run only by
    // CI's `go test ./bluedb/...`, and therefore invisible to
    // `--verify-mutations`, to `STATUS.md` and to every goal verdict. That is the
    // third time reading the ownership table honestly has found a fixture nothing
    // gated (G2.13h's four, G2.13j/k's three, this one).
    //
    // ONE mutation, and it is chosen to be discriminating rather than merely
    // sufficient: deleting the two arms of the H3b fix that reach the READER
    // leaves `Cursor.Err()` answering exactly as before, so G2.13b's property
    // (N1b) and this fixture's own pre-condition check both still pass, while the
    // transaction commits its INSERT over the row the scan could not read. Run
    // against the whole `./bluedb/` suite it produces exactly one `--- FAIL:`.
    Gate {
        id: "G2.13m",
        goal: 2,
        title: "an I/O fault inside a txn's scan fails the commit, not just the cursor",
        tier: Tier::Fast,
        run: gates_g2_13::g2_13m_scan_failure_reaches_the_commit_boundary,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.13m/scan-failure-never-reaches-the-commit-boundary",
            patch: "docs/bluedb/mutations/G2.13m.scan-failure-never-reaches-the-commit-boundary.patch",
            // Verbatim from the observed failure, and from the CONSEQUENCE arm
            // rather than the flag arm: the fixture's cursor-error check is N1b's
            // and passes under this patch, which is the point of it.
            expect: "closed on the scan's error the same way it does on a point read's (defect H3b).",
            targets: &["runtime-go/bluedb/reader.go", "runtime-go/bluedb/txn.go"],
        }]),
    },
    // G2.14–G2.25 — the engine corpus P1 INHERITED, one gate per property.
    //
    // Until 2026-08-14 the harness gated the two test files P1 WROTE
    // (`audit_test.go`, `crashsim_test.go`) and none of the eight it inherited.
    // 38 of the package's 68 `func Test…` names appeared nowhere under
    // `rust/crates/xtask/src/`, so group commit, GC, the watermark, the
    // changelog and the key format were — under RULE ZERO — unproven, however
    // green `go test ./bluedb/` was: no gate ran them, no pin named them, no
    // mutation targeted them, and nothing could tell a corpus that asserts them
    // from one that had been deleted.
    //
    // That was not theoretical. An adversarial Judge deleted four lines —
    // `tx.WitnessCollection(coll)` from `ScanCollection` and the
    // `if !rs.collWitness[coll]` assertion that is its only witness — and a
    // scan-then-insert transaction that conflicts at HEAD committed CLEAN,
    // with `go test`, `cargo test -p xtask`, `--tier=full` and
    // `--verify-mutations` all green. G2.14 is that assertion's gate.
    //
    // The split is by property, for the reason the G2.13a–m split exists: one
    // gate with twelve mutations would let one defect mint twelve PROVENs out of
    // one undifferentiated failure. The population pins, the per-leaf source
    // anchors and the grouping argument live in `gates_runtime.rs`.
    Gate {
        id: "G2.14",
        goal: 2,
        title: "the excised Stage-2 read-set arms have no producer, and the live arms do record",
        tier: Tier::Fast,
        run: gates_runtime::g2_14_readset_scope,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.14/scan-does-not-witness-its-collection",
            patch: "docs/bluedb/mutations/G2.14.scan-does-not-witness-its-collection.patch",
            // Verbatim, and it is the Judge's own attack: delete the
            // `tx.WitnessCollection(coll)` call out of `ScanCollection` and the
            // read-set records NOTHING for a scanned collection, which validate()
            // reads as a satisfied dependency.
            expect: ": ScanCollection must record it",
            targets: &["runtime-go/bluedb/txn.go"],
        }]),
    },
    // G2.26 / G2.27 — `validate()` ITSELF, the two arms Stage 2 still claims.
    //
    // G2.14 gates the fact that a transaction RECORDS its dependencies. A fourth
    // Judge round measured what that leaves open: deleting `validate()`'s
    // collection-witness arm —
    //
    //     -  if len(rs.collWitness) > 0 && rs.collWitness[ch.Coll] {
    //     -      return true, ch.Pk, false
    //     -  }
    //
    // — let a scan-then-insert transaction that conflicts at HEAD commit CLEAN (a
    // phantom, no serial order) with `go test ./bluedb/...`, `cargo test -p
    // xtask`, `--tier=full` and `--verify-mutations` all green. Gutting
    // `validate()` entirely was caught by exactly ONE assertion in the corpus,
    // and only on the point arm. `stage2_readset_test.go` asserts the read-set is
    // POPULATED; nothing asserted the conflict was DETECTED, and the distance
    // between those two is the whole of SERIALIZABLE.
    //
    // One gate per arm, and each fixture carries a control (a change that must
    // NOT conflict), so a validator gutted to `return false` fails one arm and
    // one gutted to `return true` fails the other.
    Gate {
        id: "G2.26",
        goal: 2,
        title: "validate() REFUSES a transaction whose point read was superseded after its readTs, and does not refuse one whose read-set the window never touched",
        tier: Tier::Fast,
        run: gates_runtime::g2_26_point_arm_enforces,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.26/point-arm-is-not-consulted",
            patch: "docs/bluedb/mutations/G2.26.point-arm-is-not-consulted.patch",
            // Verbatim from the observed failure. The read-set is still fully
            // populated under this patch — G2.14 stays GREEN, which is the point
            // of it — while a transaction whose row was superseded commits a
            // value derived from a version that no longer exists.
            expect: "validate()'s point arm did not detect the conflict",
            targets: &["runtime-go/bluedb/validate.go"],
        }]),
    },
    Gate {
        id: "G2.27",
        goal: 2,
        title: "validate() REFUSES a phantom insert into a witnessed collection, does not refuse a change to a collection it never witnessed, and the collection id it matches on survives the changelog wire format",
        tier: Tier::Fast,
        run: gates_runtime::g2_27_collection_witness_enforces,
        budget_s: 300,
        mutations: Mutations::new(&[
            Mutation {
                id: "G2.27/collection-witness-arm-is-not-consulted",
                patch: "docs/bluedb/mutations/G2.27.collection-witness-arm-is-not-consulted.patch",
                // The Judge's four-line deletion, verbatim in effect, and its
                // assertion verbatim from the observed failure.
                expect: "validate()'s collection-witness arm is the only thing that detects it",
                targets: &["runtime-go/bluedb/validate.go"],
            },
            Mutation {
                id: "G2.27/payload-drops-the-collection-id",
                patch: "docs/bluedb/mutations/G2.27.payload-drops-the-collection-id.patch",
                // The same arm deleted at a DISTANCE, with every line of
                // validate.go intact: the SSI window is built from the DECODED
                // payload, so a collection id that does not survive the wire makes
                // the witness match nothing. Classified on the round-trip
                // fixture's own assertion, which is the leaf that names the wire.
                expect: "The SSI validation window is built by DECODING this payload",
                targets: &["runtime-go/bluedb/keychange.go"],
            },
        ]),
    },
    Gate {
        id: "G2.15",
        goal: 2,
        title: "every job in a drained batch commits at its own strictly-increasing commitTs",
        tier: Tier::Fast,
        run: gates_runtime::g2_15_group_commit_per_job,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.15/one-committs-for-the-whole-batch",
            patch: "docs/bluedb/mutations/G2.15.one-committs-for-the-whole-batch.patch",
            // Verbatim from the observed failure, and from the FIRST assertion the
            // revert trips rather than the one the defect is named after: with one
            // `hlc.next()` before the loop instead of one per job, the jobs' own
            // timestamps stop being strictly increasing before any changelog entry
            // has a chance to be overwritten. Declaring the changelog-loss string
            // would have recorded VACUOUS — the fixture never reaches it.
            expect: "per-job commitTs not strictly increasing: ",
            targets: &["runtime-go/bluedb/committer.go"],
        }]),
    },
    Gate {
        id: "G2.16",
        goal: 2,
        title: "a read resolves the newest version at or below its readTs, from memtable or SSTable",
        tier: Tier::Fast,
        run: gates_runtime::g2_16_read_resolution,
        budget_s: 600,
        mutations: Mutations::new(&[Mutation {
            id: "G2.16/tombstone-resolves-as-a-present-row",
            patch: "docs/bluedb/mutations/G2.16.tombstone-resolves-as-a-present-row.patch",
            // Verbatim. A delete is a VERSION; dropping the marker arm of the
            // point read makes the visible tombstone resolve as a present (empty)
            // row, which is a deleted key reading as existing.
            expect: "Get(K,t2 after delete)=",
            targets: &["runtime-go/bluedb/reader.go"],
        }]),
    },
    Gate {
        id: "G2.17",
        goal: 2,
        title: "the commit clock never re-issues a timestamp across a restart",
        tier: Tier::Fast,
        run: gates_runtime::g2_17_restart_floor,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.17/reopen-does-not-floor-the-clock",
            patch: "docs/bluedb/mutations/G2.17.reopen-does-not-floor-the-clock.patch",
            // Verbatim. Seeding the HLC from zero rather than the persisted
            // hlc_hi is §3.3's floor deleted: a backward wall clock then re-issues
            // commitTs values that are already on disk.
            expect: "restart floor violated: persisted hi=",
            targets: &["runtime-go/bluedb/pebble_engine.go"],
        }]),
    },
    Gate {
        id: "G2.18",
        goal: 2,
        title: "the changelog round-trips verbatim and tails strictly after a commitTs",
        tier: Tier::Fast,
        run: gates_runtime::g2_18_changelog_roundtrip,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.18/tail-after-is-inclusive",
            patch: "docs/bluedb/mutations/G2.18.tail-after-is-inclusive.patch",
            // Verbatim. `Tail(after)` feeds the SSI validation window; making it
            // inclusive re-delivers a change the transaction has already accounted
            // for at its readTs.
            expect: "want 2 starting chg-b",
            targets: &["runtime-go/bluedb/changelog.go"],
        }]),
    },
    Gate {
        id: "G2.19",
        goal: 2,
        title: "GC never collects a version a live reader can still need",
        tier: Tier::Fast,
        run: gates_runtime::g2_19_gc_collects_only_the_dead,
        budget_s: 400,
        mutations: Mutations::new(&[Mutation {
            id: "G2.19/gc-floor-ignores-live-readers",
            patch: "docs/bluedb/mutations/G2.19.gc-floor-ignores-live-readers.patch",
            // Verbatim, and note WHICH assertion fires: the floor one, before the
            // collection one. §5.2's min-over-live is what holds T down to the
            // oldest registered reader; without it T jumps to the high-water and
            // the version a live snapshot resolves is collected under it.
            expect: "GC floor must be pinned to live reader readTs ",
            targets: &["runtime-go/bluedb/watermark.go"],
        }]),
    },
    Gate {
        id: "G2.20",
        goal: 2,
        title: "the GC threshold is durable, monotone and never above the durable high-water",
        tier: Tier::Fast,
        run: gates_runtime::g2_20_threshold_is_durable_and_clamped,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.20/threshold-not-clamped-to-durablehi",
            patch: "docs/bluedb/mutations/G2.20.threshold-not-clamped-to-durablehi.patch",
            // Verbatim. Deleting Fix-3's one-line clamp lets a GC in the
            // assigned-but-not-yet-applied window persist a T above what is
            // durable; after a crash, recovered hlc_hi < gc_threshold and every
            // reader wedges on ErrSnapshotTooOld.
            expect: "advanceThreshold must clamp candidate (tNew=",
            targets: &["runtime-go/bluedb/watermark.go"],
        }]),
    },
    Gate {
        id: "G2.21",
        goal: 2,
        title: "a GC pass trims the changelog below T and writes no other logical state",
        tier: Tier::Fast,
        run: gates_runtime::g2_21_gc_pass_is_physical,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.21/retention-does-not-trim-below-t",
            patch: "docs/bluedb/mutations/G2.21.retention-does-not-trim-below-t.patch",
            // Verbatim. The retention range-tombstone is the ONE logical write a
            // pass makes; deleting it leaves the changelog growing without bound,
            // and leaves this gate's other fixture (which asserts a pass writes
            // NOTHING else) trivially satisfiable by a pass that does nothing.
            expect: "expected retention trim at T=",
            targets: &["runtime-go/bluedb/gc.go"],
        }]),
    },
    Gate {
        id: "G2.22",
        goal: 2,
        title: "the on-disk key encoding satisfies Pebble's comparer contract",
        tier: Tier::Fast,
        run: gates_runtime::g2_22_comparer_contract,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.22/version-suffix-not-inverted",
            patch: "docs/bluedb/mutations/G2.22.version-suffix-not-inverted.patch",
            // Verbatim. The 12-byte suffix is bit-inverted precisely so a larger
            // commitTs sorts EARLIER; that is what makes one SeekGE resolve
            // "newest version <= readTs". Un-inverting it is the irreversible
            // format change §8.1 exists to catch before the first SSTable.
            expect: "expected newer < older under Compare (newest first); got ",
            targets: &["runtime-go/bluedb/keys.go"],
        }]),
    },
    Gate {
        id: "G2.23",
        goal: 2,
        title: "the comparer's key-shortening hooks stay inside Pebble's contract",
        tier: Tier::Fast,
        run: gates_runtime::g2_23_shortening_hooks,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.23/successor-returns-its-input",
            patch: "docs/bluedb/mutations/G2.23.successor-returns-its-input.patch",
            // Verbatim. A Successor that returns its input satisfies
            // `Compare(a, succ) <= 0` trivially and is legal-looking; what it
            // costs is every index-block shortening Pebble does, and the fixture's
            // must-shorten clause is the only thing that says so.
            expect: "returned its input unchanged; a strictly greater shortened key exists (key-part ",
            targets: &["runtime-go/bluedb/comparer.go"],
        }]),
    },
    Gate {
        id: "G2.24",
        goal: 2,
        title: "every key parser rejects corrupt bytes without panicking and still accepts well-formed ones",
        tier: Tier::Fast,
        run: gates_runtime::g2_24_key_parsers_fail_closed,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.24/decode-data-version-drops-its-bounds-guard",
            patch: "docs/bluedb/mutations/G2.24.decode-data-version-drops-its-bounds-guard.patch",
            // Verbatim from the recovered panic the fixture converts into a
            // failure. `decodeDataVersion` parses bytes that came off DISK, where
            // a truncated key is a data-integrity event to report — never a
            // process crash in the middle of a scan.
            expect: "panicked on key ",
            targets: &["runtime-go/bluedb/keys.go"],
        }]),
    },
    Gate {
        id: "G2.25",
        goal: 2,
        title: "a store admits one writer and one immutable format name",
        tier: Tier::Fast,
        run: gates_runtime::g2_25_one_writer_one_format,
        budget_s: 300,
        mutations: Mutations::new(&[Mutation {
            id: "G2.25/comparer-name-drifts",
            patch: "docs/bluedb/mutations/G2.25.comparer-name-drifts.patch",
            // Verbatim. The name IS the format's identity — it is what makes a
            // foreign-comparer open refusable — and editing it is the one change
            // that silently makes every existing store unopenable. The gate's two
            // other leaves assert Pebble-provided refusals that no revert of
            // BlueDB code can redden; their falsifier is the anchor, recorded with
            // its argument in SOURCE_SIDE_FALSIFIERS.
            expect: "comparer name drifted: ",
            targets: &["runtime-go/bluedb/comparer.go"],
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

    /// **The gap that made seven-mutations-on-one-gate dangerous, closed for
    /// every gate.**
    ///
    /// `mutations.rs` classifies a falsification with
    /// `if red.exit_ok || !red.output.contains(m.expect)`. It asks only whether
    /// THIS mutation's assertion is present — never whether any OTHER
    /// mutation's is absent. Nothing anywhere required the declared assertions
    /// to be mutually distinguishable, so two gates could be satisfied by one
    /// message: mutate A, watch a shared string appear, record `PROVEN` for
    /// both A and B, and B's proof is a statement about A's defect.
    ///
    /// The strongest cheap invariant is that no declared assertion is a
    /// SUBSTRING of another. Substring, not equality: `contains` is the
    /// operator the classifier uses, so an assertion nested inside another's
    /// text fires whenever the outer one does, which is exactly the collision.
    /// (`<never>` is the canary's sentinel and is compared by identity in the
    /// runner, so it is exempt — but only from itself.)
    ///
    /// This does not make a mutation's blast radius zero — a patch can still
    /// break more than one gate's SUBJECT — but it does make each recorded
    /// proof a statement about its own property.
    /// **`<never>` belongs to the canary and to nothing else.**
    ///
    /// Three independent checks exempt it, and each exemption is a hole if the
    /// sentinel can be spelled by an ordinary gate:
    ///
    /// * `mutations.rs` classifies `<never>` on `red.exit_ok` ALONE — no
    ///   discriminating assertion at all, so any patch that leaves the gate red
    ///   reports `PROVEN` and any patch that leaves it green reports `VACUOUS`.
    /// * `gates_g0.rs`'s G0.6 skips the recorded-output check, so such a
    ///   mutation needs no RED transcript.
    /// * `expect_strings_are_pairwise_discriminating` skips it, so it cannot
    ///   collide with anything.
    ///
    /// Together those make `<never>` a general escape from every falsification
    /// requirement in the crate, available by typing seven characters. It is the
    /// canary's sentinel — the ONE gate whose correct verdict is `VACUOUS` — and
    /// nothing about the three exemptions is sound for any other gate. Asserted
    /// here rather than left to the reader of three separate files.
    #[test]
    fn the_never_sentinel_is_the_canary_s_alone() {
        for g in REGISTRY {
            for m in g.mutations.as_slice() {
                if m.expect == "<never>" {
                    assert_eq!(
                        g.id, CANARY_ID,
                        "{} declares the `<never>` sentinel, which exempts it from the \
                         discriminating-assertion check (mutations.rs), from G0.6's recorded-output \
                         check (gates_g0.rs) and from pairwise discrimination (below). Those \
                         exemptions are sound only for the canary, whose correct verdict IS \
                         VACUOUS; on any other gate they are a falsification requirement opted out \
                         of by spelling",
                        m.id
                    );
                }
            }
        }
        // And the canary really does use it — otherwise the rule above is a
        // statement about an empty set.
        let canary = find(CANARY_ID).expect("the canary is permanently registered");
        assert!(
            canary
                .mutations
                .as_slice()
                .iter()
                .all(|m| m.expect == "<never>"),
            "the canary must declare `<never>`: it asserts `true`, so there is no assertion that \
             could fire and no RED output to record"
        );
    }

    #[test]
    fn expect_strings_are_pairwise_discriminating() {
        let mut all: Vec<(&str, &str)> = Vec::new();
        for g in REGISTRY {
            for m in g.mutations.as_slice() {
                if m.expect == "<never>" {
                    continue; // the canary sentinel; never matched by `contains`
                }
                assert!(
                    !m.expect.is_empty(),
                    "{} declares an empty assertion, which every output contains",
                    m.id
                );
                all.push((m.id, m.expect));
            }
        }
        for (i, (id_a, a)) in all.iter().enumerate() {
            for (j, (id_b, b)) in all.iter().enumerate() {
                if i == j {
                    continue;
                }
                assert!(
                    !b.contains(a),
                    "{id_a}'s assertion {a:?} is a substring of {id_b}'s {b:?} — a single failure \
                     emitting {b:?} would satisfy BOTH proofs, and the classifier only ever checks \
                     for presence"
                );
            }
        }
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
