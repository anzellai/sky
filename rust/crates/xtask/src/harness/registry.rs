//! The gate registry — the single membership authority.
//!
//! **Rows are rendered from this table, not from the run's results** (v2 §7.2).
//! A gate cannot disappear by not executing; if it did not run, it renders
//! `NOT RUN` and the suite renders `UNKNOWN`. That single property kills the
//! "SKIP counted as pass" class at the root.
//!
//! Every gate declares, and cannot omit:
//!   * `name` — stable id, referenced from CI
//!   * `tier` — when it runs (declared, never chosen at runtime; v2 §7.2's
//!     deletion of `DEGRADED` depends on this)
//!   * `platforms` — where it is applicable
//!   * `budget_s` — the wall-clock ceiling the harness enforces by `killpg`
//!   * `expected` — the **exact** assertion count (never a `>=`; v2 §7.4)
//!   * `mutations` — **at least one** falsifying mutation, enforced at
//!     compile time by [`Mutations::new`]

use super::bodies;
use std::path::PathBuf;

/// When a gate runs. Declared in the registry, never chosen at runtime.
#[derive(Clone, Copy, PartialEq, Eq, Debug, PartialOrd, Ord)]
pub enum Tier {
    /// local pre-commit, ≤ 60 s
    T0,
    /// per-push / PR
    T1,
    /// merge queue
    T2,
    /// nightly
    T3,
    /// pre-release
    T4,
    /// Harness self-verification. Never part of a product tier; exercised by
    /// `cargo test -p xtask` and by `--only`.
    SelfTest,
}

impl Tier {
    pub fn label(self) -> &'static str {
        match self {
            Tier::T0 => "T0",
            Tier::T1 => "T1",
            Tier::T2 => "T2",
            Tier::T3 => "T3",
            Tier::T4 => "T4",
            Tier::SelfTest => "self",
        }
    }

    pub fn parse(s: &str) -> Option<Tier> {
        Some(match s.to_ascii_lowercase().as_str() {
            "t0" => Tier::T0,
            "t1" => Tier::T1,
            "t2" => Tier::T2,
            "t3" => Tier::T3,
            "t4" => Tier::T4,
            "self" | "selftest" => Tier::SelfTest,
            _ => return None,
        })
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Platform {
    Linux,
    Macos,
    Windows,
}

impl Platform {
    /// The platform this binary is running on.
    pub fn current() -> Platform {
        if cfg!(target_os = "macos") {
            Platform::Macos
        } else if cfg!(target_os = "windows") {
            Platform::Windows
        } else {
            Platform::Linux
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Platform::Linux => "linux",
            Platform::Macos => "macos",
            Platform::Windows => "windows",
        }
    }
}

/// A non-empty platform applicability set.
///
/// Empty is rejected at compile time for the same reason an empty mutation set
/// is: a gate applicable nowhere is `verified nowhere`, which is exactly the
/// `11-fyne-stopwatch` state the mandate exists to make inexpressible.
pub struct Platforms(&'static [Platform]);

impl Platforms {
    pub const fn new(p: &'static [Platform]) -> Platforms {
        assert!(
            !p.is_empty(),
            "a gate applicable on no platform is verified nowhere"
        );
        Platforms(p)
    }

    pub fn contains(&self, p: Platform) -> bool {
        let mut i = 0;
        while i < self.0.len() {
            if self.0[i] as u8 == p as u8 {
                return true;
            }
            i += 1;
        }
        false
    }

    pub fn labels(&self) -> Vec<&'static str> {
        self.0.iter().map(|p| p.label()).collect()
    }
}

/// Every platform. The common case.
pub const ALL_PLATFORMS: Platforms =
    Platforms::new(&[Platform::Linux, Platform::Macos, Platform::Windows]);
/// Unix only — anything that depends on `killpg`, PTYs, or POSIX shell.
pub const UNIX: Platforms = Platforms::new(&[Platform::Linux, Platform::Macos]);

/// How a gate's source is perturbed to prove its assertion is live.
#[derive(Clone, Copy, Debug)]
pub enum MutationKind {
    /// Replace the **first** occurrence of `from` with `to` in `path`
    /// (repo-relative). Applying a mutation whose `from` is absent, or occurs
    /// more than once, is a hard error — a mutation that silently did nothing
    /// would report `VACUOUS` and be read as a gate defect.
    ReplaceOnce {
        path: &'static str,
        from: &'static str,
        to: &'static str,
    },
    /// Change nothing. **Only** legitimate for the canary (v2 §7.5): a correct
    /// falsifier runner must report `VACUOUS` for a no-op patch, and a runner
    /// that reports `PROVEN` has answered "green" without looking.
    NoOp,
}

#[derive(Clone, Copy, Debug)]
pub struct Mutation {
    pub id: &'static str,
    pub description: &'static str,
    pub kind: MutationKind,
}

/// A **non-empty** set of falsifying mutations.
///
/// `Mutations::new(&[])` fails the **build**, not a test — the constructor is
/// `const fn`, every call site is a `const` item, and a `const`-evaluated
/// `assert!` is a compile error. This is the one property adopted wholesale
/// from the BlueDB precedent, and it is genuinely good: a gate that ships
/// without a way to falsify it cannot exist.
pub struct Mutations(&'static [Mutation]);

impl Mutations {
    pub const fn new(m: &'static [Mutation]) -> Mutations {
        assert!(
            !m.is_empty(),
            "every gate must declare at least one falsifying mutation \
             (a gate that cannot fail is worse than no gate)"
        );
        Mutations(m)
    }

    pub fn as_slice(&self) -> &'static [Mutation] {
        self.0
    }
}

/// What the falsifier runner must observe for this gate.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Expect {
    /// The normal contract: under its mutation the gate must go **red**.
    Falsifiable,
    /// The canary's contract: under its no-op patch the gate must stay green,
    /// so the runner must report `VACUOUS`. Reporting `PROVEN` here is a
    /// **harness failure**, not a gate failure.
    Vacuous,
}

/// Context handed to a gate body. Bodies run in a child process, so this is
/// deliberately tiny — anything larger belongs in a file the body reads.
pub struct GateCtx {
    pub repo_root: PathBuf,
}

/// What a gate body reports.
///
/// `assertions` is load-bearing: a body that checked nothing reports `0`, and
/// `0` is a **FAIL** (vacuous), never a pass. Bodies must count real checks,
/// not loop iterations they skipped.
pub struct GateOutcome {
    pub passed: bool,
    pub assertions: u64,
    pub detail: String,
}

impl GateOutcome {
    pub fn new(passed: bool, assertions: u64, detail: impl Into<String>) -> GateOutcome {
        GateOutcome {
            passed,
            assertions,
            detail: detail.into(),
        }
    }
}

pub type GateBody = fn(&GateCtx) -> GateOutcome;

pub struct Gate {
    pub name: &'static str,
    pub tier: Tier,
    pub platforms: Platforms,
    /// Wall-clock ceiling. On expiry the harness `killpg`s the gate's process
    /// group and records **FAIL** — never a fabricated success, and never a
    /// leaked process holding a port for the next gate.
    pub budget_s: u64,
    /// The **exact** number of assertions this gate must report.
    ///
    /// Exact, never `>=`. `ty/tests/reject.rs` USED to assert `>= 13` against an
    /// actual 63 — deleting 50 corpus files kept it green. Both reject faces now
    /// read the exact count from `ty::reject_corpus::EXPECTED_CORPUS_FILES`, so
    /// a shrinking corpus is a build failure with an actionable message.
    pub expected: u64,
    pub mutations: Mutations,
    pub expect: Expect,
    pub body: GateBody,
    pub summary: &'static str,
}

/// THE registry. Membership authority for every harness run.
pub static GATES: &[Gate] = &[
    Gate {
        name: "roundtrip",
        tier: Tier::T1,
        platforms: ALL_PLATFORMS,
        budget_s: 300,
        expected: bodies::ROUNDTRIP_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "byte-exact reprint + zero ERROR nodes over every corpus .sky file",
        mutations: Mutations::new(&[Mutation {
            id: "roundtrip.error-node",
            description: "introduce an unparseable token into a corpus file; \
                          the ERROR-node assertion must go red",
            kind: MutationKind::ReplaceOnce {
                path: "examples/01-hello-world/src/Main.sky",
                from: "main =",
                to: "main ) =",
            },
        }]),
        body: bodies::roundtrip,
    },
    Gate {
        name: "reject",
        tier: Tier::T1,
        platforms: ALL_PLATFORMS,
        budget_s: 900,
        expected: bodies::REJECT_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "every file in the reject corpus is rejected by the type checker",
        mutations: Mutations::new(&[Mutation {
            id: "reject.neutralise-axis",
            description: "neutralise the axis under test in one reject-corpus file \
                          so it type-checks; the rejected-count assertion must go red",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/ty/tests/reject/corpus/arity_over_application.sky",
                from: "add 1 2 3",
                to: "add 1 2",
            },
        }]),
        body: bodies::reject,
    },
    Gate {
        name: "conformance",
        tier: Tier::T1,
        platforms: UNIX,
        budget_s: 2400,
        expected: bodies::CONFORMANCE_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "stdlib behavioural conformance suites (Sky source, real runtime)",
        mutations: Mutations::new(&[Mutation {
            id: "conformance.break-expectation",
            description: "corrupt one conformance expectation; the suite's own \
                          assertion must go red and the wrapper must see it in JSON",
            kind: MutationKind::ReplaceOnce {
                path: "tests/conformance/tests/MathConformanceTest.sky",
                from: "Test.equal 3 (Math.min 3 7)",
                to: "Test.equal 4 (Math.min 3 7)",
            },
        }]),
        body: bodies::conformance,
    },
    Gate {
        name: "verify-cli",
        tier: Tier::T1,
        platforms: UNIX,
        budget_s: 2400,
        expected: bodies::VERIFY_CLI_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "CLI/TUI examples start, produce expected output, and do not panic",
        mutations: Mutations::new(&[Mutation {
            id: "verify-cli.drop-expected-output",
            description: "change the greeting 01-hello-world prints; the \
                          expected-substring assertion must go red",
            kind: MutationKind::ReplaceOnce {
                path: "examples/01-hello-world/src/Main.sky",
                from: "\"Hello",
                to: "\"Goodbye",
            },
        }]),
        body: bodies::verify_cli,
    },
    Gate {
        name: "sky-verify",
        tier: Tier::T1,
        platforms: UNIX,
        budget_s: 2400,
        expected: bodies::SKY_VERIFY_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "`sky verify` over every examples/* project that owns a tests/ suite",
        mutations: Mutations::new(&[Mutation {
            id: "sky-verify.break-a-suite",
            description: "corrupt one assertion in an examples/*/tests suite; \
                          `sky verify`'s test phase must go red",
            kind: MutationKind::ReplaceOnce {
                path: "examples/35-composite-generics/tests/CompositeGenericsTest.sky",
                from: "Test.equal 12055 (Compute.amountToCents \"120.55\")",
                to: "Test.equal 12056 (Compute.amountToCents \"120.55\")",
            },
        }]),
        body: bodies::sky_verify,
    },
    // ---- harness self-verification ----------------------------------------
    //
    // `selftest-hang` is deliberately registered BEFORE `canary`. Registry order
    // is render order and therefore run order, so `--only selftest-hang,canary
    // --fail-fast` leaves `canary` selected-but-unreached — which is the only
    // honest way to produce a NOT RUN row and demonstrate that it exits
    // non-zero. Reversing these two silently deletes that demonstration.
    Gate {
        name: "selftest-hang",
        tier: Tier::SelfTest,
        platforms: UNIX,
        budget_s: 3,
        expected: 1,
        expect: Expect::Falsifiable,
        summary: "SELF-TEST — hangs on purpose, with a grandchild, to prove killpg works",
        mutations: Mutations::new(&[Mutation {
            id: "selftest-hang.always",
            description: "this gate never passes; it exists to be timed out",
            kind: MutationKind::NoOp,
        }]),
        body: bodies::selftest_hang,
    },
    Gate {
        name: "canary",
        tier: Tier::SelfTest,
        platforms: ALL_PLATFORMS,
        budget_s: 30,
        expected: 1,
        // The one gate whose falsifier result is inverted. See `Expect::Vacuous`.
        expect: Expect::Vacuous,
        summary: "PERMANENT CANARY — deliberately vacuous; the falsifier MUST report VACUOUS",
        mutations: Mutations::new(&[Mutation {
            id: "canary.noop",
            description: "a no-op patch: a correct runner reports VACUOUS, \
                          a runner that applies patches in the wrong tree \
                          (or never looks) reports PROVEN",
            kind: MutationKind::NoOp,
        }]),
        body: bodies::canary,
    },
];

/// Look a gate up by name.
pub fn find(name: &str) -> Option<&'static Gate> {
    GATES.iter().find(|g| g.name == name)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn gate_names_are_unique() {
        let mut seen = HashSet::new();
        for g in GATES {
            assert!(seen.insert(g.name), "duplicate gate name `{}`", g.name);
        }
    }

    #[test]
    fn every_gate_declares_at_least_one_mutation() {
        // `Mutations::new` already makes the empty case a *compile* error; this
        // asserts the invariant survives at run time too (e.g. if the registry
        // were ever built dynamically).
        for g in GATES {
            assert!(
                !g.mutations.as_slice().is_empty(),
                "gate `{}` declares no falsifying mutation",
                g.name
            );
        }
    }

    #[test]
    fn every_gate_declares_a_positive_budget() {
        for g in GATES {
            assert!(g.budget_s > 0, "gate `{}` has a zero budget", g.name);
        }
    }

    #[test]
    fn every_gate_expects_a_positive_assertion_count() {
        // `expected == 0` would be a gate that passes having checked nothing —
        // the `0/0 … GATE: PASS` class, declared rather than accidental.
        for g in GATES {
            assert!(
                g.expected > 0,
                "gate `{}` expects zero assertions; a gate that asserts nothing cannot pass",
                g.name
            );
        }
    }

    #[test]
    fn every_gate_is_applicable_somewhere() {
        for g in GATES {
            let anywhere = [Platform::Linux, Platform::Macos, Platform::Windows]
                .into_iter()
                .any(|p| g.platforms.contains(p));
            assert!(anywhere, "gate `{}` is applicable on no platform", g.name);
        }
    }

    #[test]
    fn exactly_one_canary_exists_and_it_is_the_only_inverted_gate() {
        let inverted: Vec<&str> = GATES
            .iter()
            .filter(|g| g.expect == Expect::Vacuous)
            .map(|g| g.name)
            .collect();
        assert_eq!(
            inverted,
            vec!["canary"],
            "exactly one gate may invert the falsifier contract"
        );
    }

    #[test]
    fn non_canary_mutations_are_never_noop() {
        // A `NoOp` mutation on a real gate would report VACUOUS forever and be
        // mistaken for a gate defect. Only the canary and the never-passing
        // hang self-test may carry one.
        for g in GATES {
            if g.name == "canary" || g.name == "selftest-hang" {
                continue;
            }
            for m in g.mutations.as_slice() {
                assert!(
                    !matches!(m.kind, MutationKind::NoOp),
                    "gate `{}` mutation `{}` is a no-op",
                    g.name,
                    m.id
                );
            }
        }
    }

    #[test]
    fn every_replace_once_mutation_targets_a_real_unique_site() {
        // A mutation whose `from` is missing (a file was renamed, a literal was
        // reworded) silently stops falsifying its gate. That is the precedent's
        // "7 of 48 verified" failure mode, caught here as a unit test instead of
        // by a 2-3 h falsifier sweep.
        let root = crate::repo_root();
        for g in GATES {
            for m in g.mutations.as_slice() {
                let MutationKind::ReplaceOnce { path, from, .. } = m.kind else {
                    continue;
                };
                let full = root.join(path);
                let src = std::fs::read_to_string(&full).unwrap_or_else(|e| {
                    panic!(
                        "gate `{}` mutation `{}` targets {} which cannot be read: {e}",
                        g.name, m.id, path
                    )
                });
                let hits = src.matches(from).count();
                assert_eq!(
                    hits, 1,
                    "gate `{}` mutation `{}`: pattern {from:?} occurs {hits}x in {path} \
                     (must be exactly 1 — 0 means the mutation is dead, >1 means it is ambiguous)",
                    g.name, m.id
                );
            }
        }
    }
}
