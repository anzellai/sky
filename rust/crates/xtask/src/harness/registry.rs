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
    // ---- Layer 1: the combinatorial corpus (v2 §3) -------------------------
    Gate {
        name: "shared-world",
        tier: Tier::T1,
        platforms: ALL_PLATFORMS,
        budget_s: 600,
        expected: bodies::SHARED_WORLD_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "whole-program vs shared-world: identical verdicts, per item",
        mutations: Mutations::new(&[Mutation {
            id: "shared-world.inject-divergence",
            description: "route the shared path through the deliberately-wrong \
                          check that skips the case's body-derived passes; the \
                          per-item comparison must go red",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/xtask/src/shared_world_gate.rs",
                from: "shared.check_case(&item.modules, &item.to_check)",
                to: "shared.check_case_injected_divergence(&item.modules, &item.to_check)",
            },
        }]),
        body: bodies::shared_world,
    },
    Gate {
        name: "corpus-manifest",
        tier: Tier::T1,
        platforms: ALL_PLATFORMS,
        budget_s: 120,
        expected: bodies::CORPUS_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "the generator and corpus/manifest.toml declare the same membership",
        mutations: Mutations::new(&[Mutation {
            id: "corpus-manifest.drift",
            description: "alter the checked-in manifest so it no longer matches \
                          what the generator produces; the membership comparison \
                          must go red",
            // A DATA mutation on purpose: it needs no rebuild, and it falsifies
            // the comparison itself rather than crashing the generator.
            kind: MutationKind::ReplaceOnce {
                path: "corpus/manifest.toml",
                from: "n_min = 206",
                to: "n_min = 207",
            },
        }]),
        body: bodies::corpus_manifest,
    },
    Gate {
        name: "corpus",
        tier: Tier::T2,
        platforms: UNIX,
        budget_s: 1800,
        expected: bodies::CORPUS_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "every generated case built + run; values compared against the generator's own",
        mutations: Mutations::new(&[Mutation {
            id: "corpus.wrong-expected-value",
            description: "corrupt the EXPECTED value the generator constructs for \
                          the record_update family, leaving the program correct; \
                          that family's value comparison must go red",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/xtask/src/corpus/gen.rs",
                from: "(decls, check, format!(\"{UPDATED}/{SURVIVOR}\"))",
                to: "(decls, check, format!(\"{UPDATED}/999\"))",
            },
        }]),
        body: bodies::corpus,
    },
    Gate {
        name: "corpus-isolation",
        tier: Tier::T2,
        platforms: UNIX,
        budget_s: 900,
        expected: bodies::CORPUS_ISOLATION_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "sampled cases give identical verdicts alone / in-batch / shuffled (v2 §3.2)",
        mutations: Mutations::new(&[Mutation {
            id: "corpus-isolation.perturb-batch",
            description: "make the batched build report a different value than the \
                          alone build; the three-way comparison must detect the \
                          divergence",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/xtask/src/corpus/isolation.rs",
                from: "prints.push(format!(\"\\\"{id}\\\\t\\\" ++ {leaf}.checkValue\"));",
                to: "prints.push(format!(\"\\\"{id}\\\\t\\\" ++ {leaf}.checkValue ++ \\\"X\\\"\"));",
            },
        }]),
        body: bodies::corpus_isolation,
    },
    Gate {
        name: "corpus-witness",
        tier: Tier::T2,
        platforms: UNIX,
        budget_s: 900,
        expected: bodies::CORPUS_WITNESS_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "each case emits different Go from its axis-neutralised twin (v2 §4.4)",
        mutations: Mutations::new(&[Mutation {
            id: "corpus-witness.compare-against-itself",
            description: "build the 'neutralised twin' from the case's OWN axes, so \
                          the two fingerprints are identical by construction; every \
                          case must then report NOT WITNESSED",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/xtask/src/corpus/witness.rs",
                from: "let baseline = gen::build(stratum, neutralised);",
                to: "let baseline = gen::build(stratum, &case.axes);",
            },
        }]),
        body: bodies::corpus_witness,
    },
    // ---- Layer 2: real-world projects (v2 §6) ------------------------------
    Gate {
        name: "apps-bundled",
        tier: Tier::T1,
        platforms: UNIX,
        // Two full `go build`s of ~35 MB binaries from a wiped slate. Measured
        // ~13 s warm on the dev host; the ceiling is sized for a cold CI runner
        // with no Go build cache.
        budget_s: 900,
        expected: bodies::APPS_BUNDLED_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member F — the Sky apps shipped inside the compiler build from a wiped slate",
        mutations: Mutations::new(&[Mutation {
            id: "apps-bundled.reintroduce-missing-field",
            description: "rename the field MainTui's init supplies, so the shared \
                          Model is constructed incomplete again — the exact defect \
                          this gate found on its first run; the build assertion \
                          must go red",
            kind: MutationKind::ReplaceOnce {
                path: "sky-bundled/console/src/MainTui.sky",
                from: ", logoutUrl = \"\"",
                to: ", logoutUrlNotAField = \"\"",
            },
        }]),
        body: bodies::apps_bundled,
    },
    Gate {
        name: "cli-verbs",
        tier: Tier::T1,
        platforms: ALL_PLATFORMS,
        // The suite itself runs in ~0.1 s; the ceiling is for the `cargo test`
        // compile on a cold CI target dir.
        budget_s: 900,
        expected: bodies::CLI_VERBS_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member G — sky CLI verbs (init/clean/watch/db/install/update/upgrade/dispatch)",
        mutations: Mutations::new(&[Mutation {
            id: "cli-verbs.break-an-expectation",
            description: "corrupt the string `sky clean` must print when there is \
                          nothing to remove; the clean test must go red and take \
                          the suite's exit status with it",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/sky/tests/cli_verb_flow.rs",
                from: "out.contains(\"nothing to remove\")",
                to: "out.contains(\"nothing to remove NEVER\")",
            },
        }]),
        body: bodies::cli_verbs,
    },
    Gate {
        name: "apps-ledger",
        tier: Tier::T1,
        platforms: UNIX,
        // Measured: 19 s cold build, 0.08 s to listening, plus migrate + seed.
        budget_s: 900,
        expected: bodies::APPS_LEDGER_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member A (SQLite arm) — migrations, auth, journal ordering, money residue",
        mutations: Mutations::new(&[Mutation {
            id: "apps-ledger.drop-the-ordering",
            description: "order the journal by id instead of (entry_date, id); the \
                          by-value ordering assertion must go red, because insertion \
                          order cannot produce the expected sequence",
            kind: MutationKind::ReplaceOnce {
                path: "apps/ledger/src/Repo.sky",
                from: "|> Store.orderAsc \"entryDate\"\n            |> Store.orderAsc \"id\"\n            |> Store.limit 500",
                to: "|> Store.limit 500",
            },
        }]),
        body: bodies::apps_ledger,
    },
    Gate {
        name: "apps-ledger-postgres",
        tier: Tier::T3,
        platforms: UNIX,
        budget_s: 900,
        expected: bodies::APPS_LEDGER_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member A (Postgres arm) — identical source, identical assertions, real pgx",
        mutations: Mutations::new(&[Mutation {
            id: "apps-ledger-postgres.break-health-driver",
            description: "stop recognising a postgres:// DSN, so the app misreports \
                          which backend it is on; the arm can then no longer prove \
                          it ran against Postgres and the driver assertion must go \
                          red (the SQLite arm is unaffected, which is the point)",
            kind: MutationKind::ReplaceOnce {
                path: "apps/ledger/src/Api.sky",
                from: "String.startsWith \"postgres://\" low",
                to: "String.startsWith \"nomatch://\" low",
            },
        }]),
        body: bodies::apps_ledger_postgres,
    },
    Gate {
        name: "apps-fleet",
        tier: Tier::T3,
        platforms: UNIX,
        budget_s: 900,
        expected: bodies::APPS_FLEET_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member E — Ledger as a multi-replica topology over one shared session store",
        mutations: Mutations::new(&[Mutation {
            id: "apps-fleet.drop-the-production-gate",
            description: "run the unreachable-store probe in dev instead of \
                          production, where the runtime warns and falls back to an \
                          in-memory store instead of refusing; the silent-fallback \
                          assertion must go red",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/xtask/src/harness/bodies.rs",
                from: "const FLEET_PROD_ENV: &str = \"production\";",
                to: "const FLEET_PROD_ENV: &str = \"\";",
            },
        }]),
        body: bodies::apps_fleet,
    },
    Gate {
        name: "apps-relay",
        tier: Tier::T1,
        platforms: UNIX,
        // Measured: 2.5 s clean rebuild + 0.16 s to first 200 + <1 s of probes.
        budget_s: 600,
        expected: bodies::APPS_RELAY_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member B — headless HTTP: auth refusal, rate limiting, CORS, asserted live",
        mutations: Mutations::new(&[
            Mutation {
                id: "apps-relay.break-health-identity",
                description: "change the service name /health reports; the body \
                              assertion must go red (proves the gate reads the \
                              response, not just the status)",
                kind: MutationKind::ReplaceOnce {
                    path: "apps/relay/src/Handlers.sky",
                    from: "\\\"service\\\":\\\"relay\\\"",
                    to: "\\\"service\\\":\\\"not-relay\\\"",
                },
            },
            Mutation {
                id: "apps-relay.disable-rate-limiting",
                description: "raise the default bucket capacity far above the burst \
                              the gate sends, so the limiter never refuses; the \
                              429 assertion must go red",
                kind: MutationKind::ReplaceOnce {
                    path: "apps/relay/src/Config.sky",
                    from: "capacity = 5",
                    to: "capacity = 100000",
                },
            },
        ]),
        body: bodies::apps_relay,
    },
    Gate {
        name: "apps-fieldbook",
        tier: Tier::T2,
        platforms: UNIX,
        // Measured: 6.7 s clean rebuild + ~130 ms across four dump invocations.
        budget_s: 900,
        expected: bodies::APPS_FIELDBOOK_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member C — one Std.Ui view renders identically across backends",
        mutations: Mutations::new(&[Mutation {
            id: "apps-fieldbook.diverge-one-backend",
            description: "map one Std.Ui Region to a different tag on the Html side \
                          only, so the same view canonicalises differently for Live \
                          than for Tui; the structural-parity assertion must go red",
            kind: MutationKind::ReplaceOnce {
                path: "apps/fieldbook/src/Structure.sky",
                from: "Ui.DescContentInfo ->\n            \"footer\"",
                to: "Ui.DescContentInfo ->\n            \"div\"",
            },
        }]),
        body: bodies::apps_fieldbook,
    },
    Gate {
        name: "apps-ffi-scale",
        tier: Tier::T4,
        platforms: UNIX,
        // Measured cold on the dev host: install 131 s + build 105 s = 236 s.
        // The ceiling allows for a slower runner and a cold Go module cache.
        budget_s: 1800,
        expected: bodies::APPS_FFI_SCALE_EXPECTED,
        expect: Expect::Falsifiable,
        summary: "member D — Go FFI at 76k-symbol scale (install + build, pre-release)",
        mutations: Mutations::new(&[Mutation {
            id: "apps-ffi-scale.break-an-ffi-symbol",
            description: "call a Stripe SDK symbol that does not exist; resolution \
                          against the 76k-symbol FFI surface must fail, which is \
                          the scale path this gate exists to exercise",
            kind: MutationKind::ReplaceOnce {
                path: "examples/13-skyshop/src/Lib/Stripe.sky",
                from: "Stripe.setKey key",
                to: "Stripe.setKeyNotASymbol key",
            },
        }]),
        body: bodies::apps_ffi_scale,
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
    Gate {
        name: "selftest-blocked",
        tier: Tier::SelfTest,
        platforms: ALL_PLATFORMS,
        budget_s: 30,
        expected: 1,
        expect: Expect::Falsifiable,
        summary: "PERMANENT WITNESS for the BLOCKED state — its body would PASS, \
                  and the BLOCKED declaration must stop it rendering PASS anyway",
        // The body is a plain passing assertion ON PURPOSE. A body that failed
        // would make BLOCKED indistinguishable from FAIL, and the property under
        // test is precisely that a gate which WOULD pass still does not render
        // PASS while it is blocked.
        mutations: Mutations::new(&[Mutation {
            id: "selftest-blocked.expire-the-block",
            description: "move this gate's BLOCKED expiry into the past; the gate \
                          must flip from BLOCKED to FAIL with no human action",
            kind: MutationKind::ReplaceOnce {
                path: "rust/crates/xtask/src/harness/registry.rs",
                from: "\"2999-01-01\"",
                to: "\"2000-01-01\"",
            },
        }]),
        body: bodies::canary,
    },
];

/// Look a gate up by name.
pub fn find(name: &str) -> Option<&'static Gate> {
    GATES.iter().find(|g| g.name == name)
}

// ─────────────────────────────────────────────────────────────────────────────
// BLOCKED — the declared, expiring, coverage-losing block (v2 §7.2)
// ─────────────────────────────────────────────────────────────────────────────

/// A gate that is structurally impossible to run right now.
///
/// v2 §7.2 declares a `BLOCKED` state; the first cut of the harness deliberately
/// did not implement one, on the correct grounds that a soft block is
/// indistinguishable from the `SKIP counted as pass` class. The state is
/// admitted here only with the four teeth that make it distinguishable —
/// enumerated on [`crate::harness::state::GateState::Blocked`] — of which the
/// load-bearing two are:
///
/// * **`expires` is mandatory and self-enforcing.** Past that date the gate
///   renders `FAIL` with no human in the loop. A block is a deadline.
/// * **A blocked gate's surfaces count as UNCOVERED** in the coverage ledger, so
///   blocking never preserves a coverage number.
///
/// Every field is required, and empty fields fail the **build** — not a test —
/// via the `const fn` constructor, for the same reason `Mutations::new(&[])`
/// does: a block without an owner, a reason and a deadline is just a skip
/// wearing a label.
pub struct Blocked {
    pub gate: &'static str,
    /// Issue URL or `owner/repo#N`. Where the work to unblock is tracked.
    pub issue: &'static str,
    /// `YYYY-MM-DD`. **The gate FAILs from this date onward.**
    pub expires: &'static str,
    /// Why it cannot run — the structural obstacle, not "flaky" or "todo".
    pub reason: &'static str,
}

impl Blocked {
    pub const fn new(
        gate: &'static str,
        issue: &'static str,
        expires: &'static str,
        reason: &'static str,
    ) -> Blocked {
        assert!(!gate.is_empty(), "a BLOCKED row must name its gate");
        assert!(
            !issue.is_empty(),
            "a BLOCKED row must carry an issue link — a block nobody tracks is a skip"
        );
        assert!(
            expires.len() == 10,
            "a BLOCKED row must carry a YYYY-MM-DD expiry — a block without a deadline is a parking space"
        );
        assert!(
            !reason.is_empty(),
            "a BLOCKED row must state the structural obstacle"
        );
        Blocked {
            gate,
            issue,
            expires,
            reason,
        }
    }

    /// Days since the Unix epoch for this row's `expires` date, or `None` if it
    /// is not a well-formed `YYYY-MM-DD`.
    ///
    /// A malformed date is NOT treated as "far future". [`block_for`]'s caller
    /// turns `None` into a FAIL, because a block whose deadline cannot be read
    /// has no deadline.
    pub fn expires_epoch_day(&self) -> Option<i64> {
        parse_ymd(self.expires)
    }
}

/// THE blocked list. Empty is the healthy state.
///
/// `selftest-blocked` is the mechanism's live witness and is deliberately
/// permanent: with an empty list, nothing would exercise the state and it would
/// rot exactly like the gates this harness exists to police. It is `SelfTest`
/// tier, so it never touches a product tier.
pub static BLOCKED: &[Blocked] = &[Blocked::new(
    "selftest-blocked",
    "https://github.com/anzellai/sky/blob/main/docs/tooling/gate-harness.md#blocked",
    "2999-01-01",
    "the permanent witness for the BLOCKED mechanism: it proves a blocked gate \
     renders BLOCKED, never PASS, and is counted as uncovered by the ledger. \
     Its expiry is deliberately unreachable because the mechanism, unlike a \
     real block, is not work anybody is going to finish",
)];

/// The blocked declaration for `gate`, if any.
pub fn block_for(gate: &str) -> Option<&'static Blocked> {
    BLOCKED.iter().find(|b| b.gate == gate)
}

/// `YYYY-MM-DD` → days since the Unix epoch. `None` on any malformation.
///
/// Hand-rolled rather than pulling a date crate into `xtask`: the harness is
/// the thing that decides whether CI is green, and its dependency surface is
/// kept deliberately small.
pub fn parse_ymd(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return None;
    }
    let num = |r: std::ops::Range<usize>| -> Option<i64> { s.get(r)?.parse::<i64>().ok() };
    let (y, m, d) = (num(0..4)?, num(5..7)?, num(8..10)?);
    if !(1..=12).contains(&m) || !(1..=31).contains(&d) {
        return None;
    }
    // Howard Hinnant's days_from_civil. Exact for the proleptic Gregorian
    // calendar; no leap-year special-casing to get wrong.
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Some(era * 146_097 + doe - 719_468)
}

/// Has this block expired as of `today` (both in epoch days)?
///
/// A **malformed** `expires` is reported as EXPIRED, deliberately. The
/// alternative — treating an unreadable date as "not yet" — would make a typo
/// into an unbounded block, which is the parking space the expiry exists to
/// forbid. Fail toward noticing.
pub fn block_is_expired(b: &Blocked, today: i64) -> bool {
    match b.expires_epoch_day() {
        Some(day) => today >= day,
        None => true,
    }
}

/// Today, as days since the Unix epoch, from the system clock.
pub fn today_epoch_day() -> i64 {
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    secs.div_euclid(86_400)
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

    // ── BLOCKED ─────────────────────────────────────────────────────────────

    #[test]
    fn every_blocked_row_names_a_registered_gate() {
        // A block on a gate that does not exist is an invisible absence: the
        // row renders nowhere and nobody is reminded it is owed.
        for b in BLOCKED {
            assert!(
                find(b.gate).is_some(),
                "BLOCKED names `{}`, which is not a registered gate",
                b.gate
            );
        }
    }

    #[test]
    fn every_blocked_row_carries_a_readable_deadline() {
        for b in BLOCKED {
            assert!(
                b.expires_epoch_day().is_some(),
                "BLOCKED row for `{}` has an unparseable expiry {:?} \
                 (must be YYYY-MM-DD)",
                b.gate,
                b.expires
            );
        }
    }

    #[test]
    fn no_product_tier_gate_is_blocked() {
        // A block is affordable because it costs coverage, not because it is
        // free. Blocking a T0-T4 gate is a real decision and must be argued in
        // review; this test makes it impossible to do quietly. Raise it only
        // together with a ledger row showing the surface going uncovered.
        for b in BLOCKED {
            let g = find(b.gate).expect("checked by every_blocked_row_names_a_registered_gate");
            assert_eq!(
                g.tier,
                Tier::SelfTest,
                "gate `{}` is blocked but sits in product tier {} — a blocked \
                 product gate silently removes coverage. If this is intended, \
                 land the coverage-ledger row that shows the surface uncovered \
                 in the SAME commit, then relax this test deliberately.",
                b.gate,
                g.tier.label()
            );
        }
    }

    /// THE demonstrated falsifier for the BLOCKED mechanism.
    ///
    /// `selftest-blocked`'s declared mutation is "move the expiry into the
    /// past". This asserts the consequence directly, on the same pure function
    /// the run loop calls — so the mechanism is proven red-able without a
    /// 2-3 h falsifier sweep, and without depending on the system clock.
    #[test]
    fn an_expired_block_flips_from_blocked_to_fail() {
        let b = block_for("selftest-blocked").expect("the permanent witness must be declared");
        let deadline = b.expires_epoch_day().unwrap();

        // The day before the deadline: still blocked.
        assert!(!block_is_expired(b, deadline - 1));
        // The deadline itself, and after: expired. `>=`, not `>` — a block
        // expires ON its date, not the day after.
        assert!(block_is_expired(b, deadline));
        assert!(block_is_expired(b, deadline + 1));

        // And the mutation's own effect, applied to a copy of the row.
        let mutated = Blocked::new(b.gate, b.issue, "2000-01-01", b.reason);
        assert!(
            block_is_expired(&mutated, today_epoch_day()),
            "the declared mutation must make the block expired TODAY"
        );
    }

    #[test]
    fn a_malformed_expiry_is_expired_not_forever() {
        // Fail toward noticing: a typo must not buy an unbounded block.
        let bad = Blocked::new("selftest-blocked", "issue", "not-a-date", "typo");
        assert!(bad.expires_epoch_day().is_none());
        assert!(block_is_expired(&bad, 0));
    }

    #[test]
    fn parse_ymd_matches_known_epoch_days() {
        assert_eq!(parse_ymd("1970-01-01"), Some(0));
        assert_eq!(parse_ymd("1970-01-02"), Some(1));
        assert_eq!(parse_ymd("2000-03-01"), Some(11017));
        assert_eq!(parse_ymd("2024-02-29"), Some(19782)); // a real leap day
        assert_eq!(parse_ymd("2026-08-10"), Some(20675));
        // Malformations, each of which would otherwise become a silent block.
        assert_eq!(parse_ymd(""), None);
        assert_eq!(parse_ymd("2026-8-10"), None);
        assert_eq!(parse_ymd("2026/08/10"), None);
        assert_eq!(parse_ymd("2026-13-01"), None);
        assert_eq!(parse_ymd("2026-00-01"), None);
        assert_eq!(parse_ymd("2026-01-00"), None);
    }
}
