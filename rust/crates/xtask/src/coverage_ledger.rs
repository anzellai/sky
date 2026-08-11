//! `xtask coverage-ledger` — THE coverage ledger
//! (docs/ci-test-architecture-v2.md §9.2, denominator discipline per §5).
//!
//! # The problem this exists to solve
//!
//! §9.2 requires that coverage conservation be **proven, not asserted**, and
//! that the sole-ownership table be "generated mechanically by a committed
//! script, never hand-maintained". A hand-maintained table has exactly one
//! failure mode, and it is fatal: the moment a directory is retired, an import
//! is deleted, or a gate is renamed, the table keeps quoting the world as it
//! was. The retirement then looks free, because the artefact that would have
//! shown the loss was written before the loss happened.
//!
//! So every number below is measured at run time from the tree. Nothing in
//! `docs/coverage/ledger.json` is typed by a person. The two hand-written
//! tables in this file ([`CROSS_CUTTING`] and [`GATE_SURFACES`]) are
//! *taxonomies*, not measurements — they name surfaces and say which gate
//! claims each one — and both are pinned by `cargo test`s that fail the build
//! when a new gate arrives without declaring its surfaces, or when a declared
//! surface id does not exist.
//!
//! # The comparison currency: strength classes
//!
//! "Covered" is not a boolean. A directory that merely compiles and a suite
//! whose assertions are proven falsifiable are both "covered" under a boolean,
//! and conflating them is how a retirement that drops the *run* reads as
//! neutral (§9.3: `sky check` ≡ `sky build`, so dropping `examples/` to
//! `sky check` drops the run, not the build). [`Strength`] is the ordered
//! scale every comparison in this file uses:
//!
//! | n | class       | meaning                                                    |
//! |---|-------------|------------------------------------------------------------|
//! | 0 | `None`      | nothing covers it                                           |
//! | 1 | `Builds`    | something compiles it, nothing runs it                      |
//! | 2 | `Runs`      | something builds AND runs it; verdict = exit status only    |
//! | 3 | `Asserted`  | explicit counted assertions                                 |
//! | 4 | `Falsified` | assertions in a REGISTERED gate whose falsifying mutation is |
//! |   |             | recorded `PROVEN` in `docs/coverage/falsifier-proofs.json`  |
//!
//! Each surface carries TWO strengths: `cover_today` (what the pre-overhaul
//! corpus — examples, conformance, the `tests/<Sub>` suites — actually
//! provided) and `cover_new` (what the Layer 1 + Layer 2 + registered-gate
//! world provides). `verdict` is their comparison. `weaker` is the interesting
//! one: it is a coverage removal, and §9.2 says a removal gets a ledger row
//! before it is allowed, which is what the `[[weakening]]` stanza in
//! `docs/coverage/removals.toml` is.
//!
//! # Reference counting is TEXTUAL, and honest about which way it errs
//!
//! There is no type-checked call graph here; imports and symbol references are
//! read out of `.sky` source text (comments and string literals stripped
//! first). That makes the numbers approximate, so the file computes TWO sets
//! and — mirroring the filtered/unfiltered discipline of §5.2 — **never
//! averages or conflates them**:
//!
//! * `qualified` (STRICT) — a `(module, symbol)` counts as referenced only if
//!   the token `<Alias>.<symbol>` or `<Module>.<symbol>` literally appears.
//! * `generous` — `qualified` PLUS bare `<symbol>` occurring in a file that
//!   imports `<module>` and either exposes `<symbol>` explicitly or uses
//!   `exposing (..)`. `Sky.Core.Prelude` is auto-imported, so every file is
//!   treated as if it carried `import Sky.Core.Prelude exposing (..)`.
//!
//! **Every "uncovered" claim uses the STRICT set.** Over-counting references
//! makes uncovered surface look smaller than it is, and understating uncovered
//! surface is the forbidden direction — it is the same move as shrinking a
//! denominator (§5.2), one layer up. The generous set is reported alongside so
//! the gap between the two readings is visible instead of being split.
//!
//! # Where the numbers come from
//!
//! * **stdlib surface** — `api/symbols.json`, produced by calling
//!   `project::render_doc_site_export` (the `sky doc --export` code path) into
//!   a temp dir, exactly as `denominators_gate` does. The entry and module
//!   counts are then cross-checked against `docs/coverage/denominators.json`;
//!   a disagreement is a HARD FAILURE, because two independently-derived
//!   denominators that differ is precisely the drift the denominator contract
//!   exists to catch, and picking either one would be a coin toss recorded as
//!   a fact.
//! * **units** — `examples/*`, the `[[member]]` rows of `apps/manifest.toml`,
//!   `tests/conformance`, each `tests/<Sub>` suite group, and the generated
//!   Layer 1 corpus (scanned through its generator's string literals, since
//!   materialising it is the `corpus` gate's job, not this file's).
//! * **gates** — `crate::harness::registry::GATES` plus the `PROVEN` markers
//!   in `docs/coverage/falsifier-proofs.json`.
//!
//! # The ratchet (`--check`)
//!
//! `--check` writes nothing and exits non-zero when the checked-in ledger is
//! stale, when `summary.surfaces_covered` fell, when any individual surface's
//! `cover_new` fell, or when a surface is `weaker` without a `[[weakening]]`
//! stanza naming it. An INCREASE is always fine and rewrites the file.

use crate::harness::registry::{Tier, GATES};
use serde_json::{json, Value};
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

// ---------------------------------------------------------------- taxonomies

/// A cross-cutting surface: something the product must keep working that is
/// not a stdlib module and not a CLI verb.
///
/// This taxonomy CANNOT be derived — nothing in the tree enumerates "the
/// things this repo must not break". It is therefore declared, and pinned by
/// [`tests::every_declared_surface_id_exists`]. What is *not* declared is the
/// new-world coverage: that is computed from [`GATE_SURFACES`] plus the
/// registry plus the falsifier proofs, so a gate that stops existing takes its
/// coverage claim with it.
struct CrossSurface {
    id: &'static str,
    category: &'static str,
    description: &'static str,
    /// What covered this surface BEFORE the overhaul: `(evidence, strength)`.
    /// Every entry names the script, workflow step or test file that produced
    /// it, so the claim is checkable. `("nothing", 0)` where nothing did.
    today: &'static [(&'static str, u8)],
}

/// Pre-overhaul coverage, grounded in `scripts/`, `.github/workflows/rust-ci.yml`
/// and `.github/workflows/nightly-sweep.yml`. "CI-wired" means a workflow step
/// invokes it on every push; "local-only" means the script exists and asserts,
/// but no workflow runs it — a distinction that matters because an unrun
/// assertion is an intention, not a gate.
static CROSS_CUTTING: &[CrossSurface] = &[
    CrossSurface {
        id: "compiler.parse",
        category: "compiler",
        description: "every corpus construct parses; reprint is byte-exact; zero ERROR nodes",
        today: &[("xtask roundtrip (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "compiler.resolve",
        category: "compiler",
        description: "name resolution agrees with the oracle over the corpus",
        today: &[("xtask resolve (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "compiler.infer",
        category: "compiler",
        description: "type inference accepts what the oracle accepts",
        today: &[("xtask infer (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "compiler.reject",
        category: "compiler",
        description: "the reject corpus is rejected, with the right diagnostic",
        today: &[("xtask reject (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "compiler.lower-emit-shape",
        category: "compiler",
        description: "lowering emits Go that compiles, and whose stdout matches the golden",
        today: &[
            ("xtask build-run --all (CI-wired: emit + go build every example)", 2),
            ("xtask build-run --shape cli --run --golden (CI-wired stdout goldens)", 3),
        ],
    },
    CrossSurface {
        id: "compiler.codegen-determinism",
        category: "compiler",
        description: "identical input emits byte-identical Go across fresh processes and hosts",
        today: &[("xtask repro (CI-wired, linux + macos)", 3)],
    },
    CrossSurface {
        id: "compiler.coerce-floor",
        category: "compiler",
        description: "the rt.Coerce token floor never rises",
        today: &[("xtask coerce-floor (CI-wired, FAIL-ON-INCREASE, linux + macos)", 3)],
    },
    CrossSurface {
        id: "compiler.fmt",
        category: "compiler",
        description: "sky fmt is idempotent and never drops a comment",
        today: &[("xtask fmt (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "compiler.fuzz",
        category: "compiler",
        description: "random well-typed programs neither crash the compiler nor emit unstable Go",
        today: &[("xtask fuzz (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "compiler.oracle-differential",
        category: "compiler",
        description: "Rust output is checked against the retired Haskell oracle",
        today: &[
            ("xtask divergences (CI-wired: the known-divergence ledger)", 3),
            ("xtask diff — STUB, exits 2, asserts nothing", 0),
        ],
    },
    CrossSurface {
        id: "compiler.shared-world",
        category: "compiler",
        description: "whole-program and shared-world compilation give identical verdicts",
        today: &[("xtask shared-world (local-only: no workflow step invokes it)", 3)],
    },
    CrossSurface {
        id: "runtime.go-rt",
        category: "runtime",
        description: "the embedded Go runtime's own unit tests",
        today: &[("go test ./rt/... (CI-wired, linux + macos)", 3)],
    },
    CrossSurface {
        id: "runtime.panic-classes",
        category: "runtime",
        description: "every known panic class has a live regression",
        today: &[("runtime-go/rt/*_test.go via go test ./rt/... (CI-wired)", 3)],
    },
    CrossSurface {
        id: "skylive.session-sse-csrf",
        category: "skylive",
        description: "sessions, the persistent SSE channel, and CSRF refusal",
        today: &[
            ("xtask build-run --shape live --run (CI-wired: starts + serves, exit-status verdict)", 2),
            ("scripts/playwright-live-verify.mjs (local-only)", 3),
        ],
    },
    CrossSurface {
        id: "skylive.multi-replica",
        category: "skylive",
        description: "shared session store, cross-instance pub/sub, sticky sessions",
        today: &[
            ("go test -tags integration ./rt/ -run Postgres (CI-wired: Postgres session store)", 3),
            ("scripts/verify-pubsub-multitab.sh (local-only)", 3),
        ],
    },
    CrossSurface {
        id: "ui.cross-backend",
        category: "ui",
        description: "one Std.Ui view renders equivalently on Live / Tui / Webview",
        today: &[("scripts/verify-stdui-matrix.mjs (local-only)", 3)],
    },
    CrossSurface {
        id: "ui.tui",
        category: "ui",
        description: "Sky.Tui apps start, render, and do not panic",
        today: &[
            ("xtask build-run --shape tui --run (CI-wired: starts, no panic)", 2),
            ("scripts/verify-cli.sh — TUI entries are pty-skip, i.e. build-only (local-only)", 1),
        ],
    },
    CrossSurface {
        id: "ui.webview",
        category: "ui",
        description: "Sky.Webview desktop apps build and launch",
        today: &[("examples/29,31 clean-built by scripts/example-sweep.sh (nightly) — build only", 1)],
    },
    CrossSurface {
        id: "db.sqlite",
        category: "db",
        description: "Std.Db against the SQLite driver, real statements",
        today: &[("scripts/conformance.sh Store/StoreCrud suites (CI-wired)", 3)],
    },
    CrossSurface {
        id: "db.postgres",
        category: "db",
        description: "Std.Db against the Postgres driver from Sky application code",
        today: &[("nothing", 0)],
    },
    CrossSurface {
        id: "db.migrations",
        category: "db",
        description: "committed migration files applied by `sky db migrate` against a live DB",
        today: &[("nothing", 0)],
    },
    CrossSurface {
        id: "db.codec-store",
        category: "db",
        description: "one Std.Codec drives both JSON and dialect-safe DB round-trips",
        today: &[("scripts/conformance.sh CodecConformanceTest + StoreConformanceTest (CI-wired)", 3)],
    },
    CrossSurface {
        id: "auth.password-session",
        category: "auth",
        description: "Std.Auth password hashing, token signing, session TTL",
        today: &[
            ("scripts/conformance.sh JwtAuthConformanceTest (CI-wired)", 3),
            ("tests/Auth/AuthTest.sky — NO RUNNER executes it", 0),
        ],
    },
    CrossSurface {
        id: "observability.console",
        category: "observability",
        description: "the embedded console mounts, authenticates, and streams",
        today: &[
            ("scripts/verify-console-e2e.mjs (local-only)", 3),
            ("examples/25-sky-console clean-built by scripts/example-sweep.sh (nightly)", 1),
        ],
    },
    CrossSurface {
        id: "lsp",
        category: "tooling",
        description: "editor parity: hover, completion, diagnostics, go-to-definition",
        today: &[("xtask lsp (CI-wired: Neovim editor-parity 17/17)", 3)],
    },
    CrossSurface {
        id: "ffi.scale",
        category: "ffi",
        description: "Go FFI at SDK scale (76k symbols) still resolves and builds",
        today: &[(
            "scripts/example-sweep.sh nightly clean-slate build of examples/13-skyshop",
            2,
        )],
    },
    CrossSurface {
        id: "ffi.cgo-host-target",
        category: "ffi",
        description: "cgo-linked FFI builds for the host target",
        today: &[("nothing", 0)],
    },
    CrossSurface {
        id: "docs.examples-gate",
        category: "docs",
        description: "every full-module Sky example in docs/ still compiles",
        today: &[("scripts/doc-examples.sh (CI-wired, rust-ci.yml)", 3)],
    },
    CrossSurface {
        id: "lang.constructs",
        category: "language",
        description: "the classified syntax constructs appear in something that is compiled",
        today: &[("xtask roundtrip over examples/ (CI-wired) — parse-level assertion only", 3)],
    },
    CrossSurface {
        id: "config.sky-toml",
        category: "config",
        description: "sky.toml sections are parsed and honoured by a built app",
        today: &[("examples/*/sky.toml consumed by xtask build-run --all (CI-wired) — build verdict only", 2)],
    },
    // Surfaces beyond the mandated minimum: `apps/relay` (member B) owns real
    // product surface that none of the mandated ids names, and a gate must be
    // able to declare what it actually covers.
    CrossSurface {
        id: "http.middleware-ratelimit",
        category: "http",
        description: "Sky.Http.Middleware + Sky.Http.RateLimit refuse and throttle as documented",
        today: &[("xtask build-run --shape http --run (CI-wired: starts + serves, exit-status verdict)", 2)],
    },
    CrossSurface {
        id: "http.sse-websocket",
        category: "http",
        description: "Sky.Http.Server.Stream (SSE) and .WebSocket carry a real session",
        today: &[
            ("xtask build-run --shape http --run (CI-wired: starts + serves)", 2),
            ("scripts/verify-streaming-chat.sh (local-only)", 3),
        ],
    },
    // The accounting is itself a surface. Every coverage claim in this repo
    // rests on `denominators.json` and `ledger.json` being current, and until
    // this cycle nothing checked either: `xtask denominators` was invoked by no
    // workflow at all, so the denominator every percentage divides by could
    // drift silently while CI stayed green. A ledger nobody verifies is a
    // number, not a measurement.
    CrossSurface {
        id: "meta.coverage-accounting",
        category: "meta",
        description: "the checked-in denominators and coverage ledger are current, \
                      and neither shrank without an accounted entry",
        today: &[("nothing — `xtask denominators` was invoked by no workflow", 0)],
    },
];

/// Which surfaces each REGISTERED gate covers.
///
/// This is the only place a gate's coverage claim is written down, and
/// [`tests::every_registered_gate_declares_its_surfaces`] makes the omission a
/// build failure: a gate added to the registry without a row here does not
/// compile-and-pass, so "we added a gate" can never quietly mean "and nobody
/// wrote down what it covers". Ids are resolved against [`CROSS_CUTTING`] and
/// the generated `stdlib.*` / `cli.*` / `config.*` namespaces.
///
/// `SelfTest`-tier gates are excluded: they verify the harness, not the
/// product, and giving them product surfaces would let harness self-tests
/// count as product coverage.
static GATE_SURFACES: &[(&str, &[&str])] = &[
    // Layer-2 member H (`apps/dispatch`). These three gates are the ONLY cover
    // for five modules that, until 2026-08-10, were imported by nothing at all
    // — and with them the file-based migration verbs, which no project
    // exercised. Declared explicitly so the ledger charges them as coverage
    // rather than letting the modules keep reading as uncovered.
    (
        "apps-dispatch",
        &[
            "stdlib.Std.Jobs",
            "stdlib.Std.Db.Schema",
            "stdlib.Std.Db.Migrate",
            "stdlib.Std.Markdown",
            "stdlib.Std.Email",
            "db.migrations",
            "db.sqlite",
            "cli.db",
        ],
    ),
    // The destructive-diff arm is what pins the incident class: `sky db migrate`
    // once dropped UNIQUE + AUTOINCREMENT + DEFAULT, breaking apps on Postgres.
    // It was fixed once and nothing gated it until now.
    ("apps-dispatch-destructive", &["db.migrations", "cli.db"]),
    (
        "apps-dispatch-postgres",
        &[
            "stdlib.Std.Jobs",
            "stdlib.Std.Db.Schema",
            "stdlib.Std.Db.Migrate",
            "db.migrations",
            "db.postgres",
        ],
    ),
    ("roundtrip", &["compiler.parse", "lang.constructs"]),
    ("reject", &["compiler.reject", "compiler.infer"]),
    (
        "conformance",
        &["db.sqlite", "db.codec-store", "auth.password-session"],
    ),
    ("verify-cli", &["ui.tui", "config.sky-toml"]),
    ("sky-verify", &["compiler.fmt", "lang.constructs"]),
    ("shared-world", &["compiler.shared-world", "compiler.resolve"]),
    ("coverage-ledger", &["meta.coverage-accounting"]),
    ("corpus-manifest", &["lang.constructs"]),
    // Family R is the combinatorial face of `compiler.reject`. The standalone
    // `reject` gate covers 63 hand-written defects, one file each; this one
    // crosses 14 defect CLASSES against the positions and import shapes the
    // repository's own bugs moved along, and asserts the diagnostic CODE rather
    // than a boolean. It also carries `compiler.resolve`, because every case
    // routes a value it needs through an imported helper module — a broken
    // resolver takes the twin down with it.
    (
        "corpus-reject",
        &["compiler.reject", "compiler.resolve", "lang.constructs"],
    ),
    // Family E asserts properties of the emitted Go without a `go build`, which
    // is the cheap face of the same surface `coerce-floor` ratchets and the only
    // one that can state a per-function invariant rather than a per-example
    // count.
    (
        "corpus-emit-shape",
        &["compiler.lower-emit-shape", "compiler.codegen-determinism"],
    ),
    // Layer 1's `corpus` gate. The `lang.*` / `compiler.*` claims are the
    // language strata; the `stdlib.*` claims are **Family S**, which asserts
    // 323 of the 336 public symbols these 20 modules export, at their empty /
    // boundary / unicode / failure edges (`xtask corpus --stdlib-coverage`
    // prints the numerator and names every gap).
    //
    // Declared here rather than inferred, because the ledger derives a Layer-1
    // module import from the string LITERALS in `corpus/*.rs`
    // (`enumerate_units`, the `parse_imports(&emitted)` call) and Family S
    // builds its import lines with `format!("import {module}")` — the literal
    // is the module path alone, so the inference misses it. Leaving it to the
    // inference would have recorded `Sky.Core.Basics`, `Sky.Core.Char` and
    // `Sky.Core.Path` as having ZERO new cover while 18 assertions ran against
    // each of them every corpus run. Undeclared coverage looks like no
    // coverage; that is the right default, and this is the declaration.
    (
        "corpus",
        &[
            "lang.constructs",
            "compiler.infer",
            "compiler.lower-emit-shape",
            "stdlib.Sky.Core.String",
            "stdlib.Sky.Core.List",
            "stdlib.Sky.Core.Dict",
            "stdlib.Sky.Core.Set",
            "stdlib.Sky.Core.Maybe",
            "stdlib.Sky.Core.Result",
            "stdlib.Sky.Core.Char",
            "stdlib.Sky.Core.Encoding",
            "stdlib.Sky.Core.Crypto",
            "stdlib.Sky.Core.Math",
            "stdlib.Sky.Core.Basics",
            "stdlib.Sky.Core.ToString",
            "stdlib.Sky.Core.Path",
            "stdlib.Sky.Core.Error",
            "stdlib.Sky.Core.Regex",
            "stdlib.Sky.Core.Json.Encode",
            "stdlib.Sky.Core.Json.Decode",
            "stdlib.Std.Decimal",
            "stdlib.Std.Money",
            "stdlib.Std.Csv",
        ],
    ),
    ("corpus-isolation", &["compiler.shared-world", "lang.constructs"]),
    (
        "corpus-witness",
        &["compiler.codegen-determinism", "compiler.lower-emit-shape"],
    ),
    (
        "apps-bundled",
        &["observability.console", "skylive.session-sse-csrf"],
    ),
    (
        "cli-verbs",
        &[
            "cli.init",
            "cli.clean",
            "cli.watch",
            "cli.db",
            "cli.install",
            "cli.update",
            "cli.upgrade",
        ],
    ),
    (
        "apps-ledger",
        &[
            "db.sqlite",
            "db.migrations",
            "db.codec-store",
            "auth.password-session",
            "skylive.session-sse-csrf",
            "config.sky-toml",
        ],
    ),
    ("apps-ledger-postgres", &["db.postgres", "db.migrations"]),
    (
        "apps-fleet",
        &["skylive.multi-replica", "skylive.session-sse-csrf"],
    ),
    (
        "apps-relay",
        &[
            "http.middleware-ratelimit",
            "http.sse-websocket",
            "runtime.panic-classes",
        ],
    ),
    (
        "apps-fieldbook",
        &["ui.cross-backend", "ui.tui", "ui.webview"],
    ),
    ("apps-ffi-scale", &["ffi.scale", "ffi.cgo-host-target"]),
    // Deliberately ONE surface. The root `tests/` suites assert language
    // constructs at RUN time (pattern matching, ADTs, records, pipelines,
    // recursion, Dict/List/String/Maybe kernels), which is strictly stronger
    // evidence for `lang.constructs` than `roundtrip`'s parse-level pass.
    //
    // They are NOT declared against `ui.cross-backend` even though 81 of their
    // cases are `Std.Ui`: those assert element + attribute construction shapes
    // in-process, not "renders equivalently on Live / Tui / Webview", which is
    // what that surface means. Nor against `db.*`: `Db/DbTest` asserts against a
    // SIMULATED exec/query seam, not a real engine. Claiming either would be
    // free coverage, which is the exact defect this table exists to prevent.
    ("sky-suites", &["lang.constructs"]),
];

/// What the CI workflows run that is NOT a harness-registered gate, and which
/// surfaces each such invocation covers.
///
/// A gate that runs on every push but has not migrated into the harness still
/// asserts. On the declared scale that is `Asserted` (3), not `None` (0) —
/// only `Falsified` (4) requires "registered gate AND a PROVEN falsifier".
/// Scoring these 0 would manufacture false `weaker` verdicts, and a false
/// weakening is as dishonest as a hidden one: it spends the reviewer's
/// attention on damage that did not happen.
///
/// Keys are namespaced by invocation shape, and every one of them is MATCHED
/// AGAINST THE WORKFLOWS at run time — a row here contributes nothing unless
/// `.github/workflows/**` actually invokes it, so deleting the CI step deletes
/// the coverage claim with it:
///   * `xtask:<subcommand>` — resolved by [`crate::ci_scan::scan_xtask_refs`],
///     the same extractor `gate_manifest_test` uses.
///   * `script:scripts/<path>` — resolved by
///     [`crate::ci_scan::scan_script_refs`].
///   * `cmd:<literal>` — the handful of steps that are neither (`go test
///     ./rt/...`). Pretending they do not exist because they do not fit the two
///     tidy shapes would understate coverage.
static CI_SURFACES: &[(&str, &[&str])] = &[
    ("xtask:roundtrip", &["compiler.parse", "lang.constructs"]),
    ("xtask:resolve", &["compiler.resolve"]),
    ("xtask:infer", &["compiler.infer"]),
    ("xtask:reject", &["compiler.reject"]),
    ("xtask:fmt", &["compiler.fmt"]),
    ("xtask:repro", &["compiler.codegen-determinism"]),
    ("xtask:coerce-floor", &["compiler.coerce-floor"]),
    ("xtask:fuzz", &["compiler.fuzz"]),
    ("xtask:welltyped", &["compiler.fuzz"]),
    ("xtask:divergences", &["compiler.oracle-differential"]),
    ("xtask:shared-world", &["compiler.shared-world"]),
    ("xtask:lsp", &["lsp"]),
    (
        "xtask:build-run",
        &[
            "compiler.lower-emit-shape",
            "config.sky-toml",
            "ui.tui",
            "skylive.session-sse-csrf",
            "http.middleware-ratelimit",
            "http.sse-websocket",
        ],
    ),
    // `s8` forbids public-surface patterns (Result String, raw .(T) assertions);
    // that is a source-shape assertion about emission, not a runtime one.
    ("xtask:s8", &["compiler.lower-emit-shape"]),
    // These two are the accounting itself, not product surface. Declared so the
    // anti-drift test stays total; they claim nothing.
    ("xtask:denominators", &[]),
    ("xtask:coverage-ledger", &[]),
    // The harness runs the registered gates; their coverage is already scored
    // through GATE_SURFACES, and counting it twice here would double-claim.
    ("xtask:harness", &[]),
    (
        "script:scripts/conformance.sh",
        &["db.sqlite", "db.codec-store", "auth.password-session"],
    ),
    ("script:scripts/doc-examples.sh", &["docs.examples-gate"]),
    (
        "script:scripts/example-sweep.sh",
        &["ffi.scale", "ui.webview", "observability.console"],
    ),
    // Infrastructure, not product surface.
    ("script:scripts/ci/bound-go-cache.sh", &[]),
    ("script:scripts/ci/assert-tier-budget.sh", &[]),
    ("script:scripts/build-docs-site.sh", &[]),
    ("script:scripts/release-notes.sh", &[]),
    ("script:scripts/preflight-tag.sh", &[]),
    (
        "cmd:go test ./rt/...",
        &["runtime.go-rt", "runtime.panic-classes"],
    ),
    (
        "cmd:go test -tags integration ./rt/ -run Postgres",
        &["skylive.multi-replica"],
    ),
];

// ------------------------------------------------------------------ strength

/// The comparison currency. Ordered, so `max` over evidence is meaningful and
/// `cover_new < cover_today` is exactly "this got weaker".
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
enum Strength {
    None = 0,
    Builds = 1,
    Runs = 2,
    Asserted = 3,
    Falsified = 4,
}

impl Strength {
    fn from_u8(n: u8) -> Strength {
        match n {
            0 => Strength::None,
            1 => Strength::Builds,
            2 => Strength::Runs,
            3 => Strength::Asserted,
            _ => Strength::Falsified,
        }
    }

    fn label(self) -> &'static str {
        match self {
            Strength::None => "None",
            Strength::Builds => "Builds",
            Strength::Runs => "Runs",
            Strength::Asserted => "Asserted",
            Strength::Falsified => "Falsified",
        }
    }
}

/// One piece of evidence: what provides the coverage, and how strongly.
#[derive(Clone, Debug)]
struct Ev {
    by: String,
    strength: Strength,
}

impl Ev {
    fn new(by: impl Into<String>, strength: Strength) -> Ev {
        Ev {
            by: by.into(),
            strength,
        }
    }
}

fn max_strength(evs: &[Ev]) -> Strength {
    evs.iter()
        .map(|e| e.strength)
        .max()
        .unwrap_or(Strength::None)
}

/// A surface row. `today` and `new` are never merged — the whole point of the
/// row is the comparison between them.
#[derive(Clone, Debug)]
struct Surface {
    id: String,
    category: String,
    description: String,
    today: Vec<Ev>,
    new: Vec<Ev>,
}

impl Surface {
    fn today_max(&self) -> Strength {
        max_strength(&self.today)
    }

    fn new_max(&self) -> Strength {
        max_strength(&self.new)
    }

    fn verdict(&self) -> &'static str {
        match self.new_max().cmp(&self.today_max()) {
            std::cmp::Ordering::Greater => "stronger",
            std::cmp::Ordering::Equal => "equal",
            std::cmp::Ordering::Less => "weaker",
        }
    }

    fn to_json(&self) -> Value {
        let ev = |evs: &[Ev]| -> Value {
            Value::Array(
                evs.iter()
                    .map(|e| json!({ "by": e.by, "strength": e.strength as u8, "class": e.strength.label() }))
                    .collect(),
            )
        };
        json!({
            "id": self.id,
            "category": self.category,
            "description": self.description,
            "cover_today": {
                "strength": self.today_max() as u8,
                "class": self.today_max().label(),
                "evidence": ev(&self.today),
            },
            "cover_new": {
                "strength": self.new_max() as u8,
                "class": self.new_max().label(),
                "evidence": ev(&self.new),
            },
            "verdict": self.verdict(),
        })
    }
}

// ---------------------------------------------------------------------- units

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug)]
enum Role {
    Example,
    Layer2,
    Conformance,
    SkySuite,
    Layer1,
}

impl Role {
    fn label(self) -> &'static str {
        match self {
            Role::Example => "Example",
            Role::Layer2 => "Layer2",
            Role::Conformance => "Conformance",
            Role::SkySuite => "SkySuite",
            Role::Layer1 => "Layer1",
        }
    }
}

/// One consumer of the stdlib surface.
///
/// `path_key` exists because `apps/manifest.toml` deliberately backs two
/// members with `apps/ledger` and backs member D with `examples/13-skyshop`.
/// Ownership questions ("is this module solely owned?") must be asked of
/// distinct PATHS — otherwise a directory with two member rows looks like two
/// independent owners and never appears as sole-owned, which is the exact
/// direction that hides a loss.
struct Unit {
    id: String,
    role: Role,
    path_key: String,
    /// `apps/manifest.toml` `gate` field; Layer 2 only.
    gate: Option<String>,
    files: usize,
    imports: BTreeSet<String>,
    qualified: BTreeSet<(String, String)>,
    generous: BTreeSet<(String, String)>,
    toml_sections: BTreeSet<String>,
    db_drivers: BTreeSet<String>,
    /// An `examples/*/tests/` directory containing real `Test.<assertion>`
    /// calls. Raises the example's `cover_today` from `Runs` to `Asserted`.
    has_assertions: bool,
}

// -------------------------------------------------------------------- lexing

const ASSERTION_FNS: &[&str] = &[
    "equal",
    "notEqual",
    "ok",
    "err",
    "expectErrorKind",
    "isTrue",
    "isFalse",
    "fail",
];

/// Strip `--` line comments, `{- -}` block comments, and string literals
/// (including `"""` multiline strings) from Sky source.
///
/// This runs before reference extraction because a `Std.Foo.bar` inside a doc
/// comment or an error message is not a reference; counting it would inflate
/// the reference set, and an inflated reference set makes uncovered surface
/// look smaller than it is. Stripping errs the other way, which is the safe
/// direction (§5.2's reasoning, one layer up).
fn strip_noise(src: &str) -> String {
    let b: Vec<char> = src.chars().collect();
    let mut out = String::with_capacity(src.len());
    let mut i = 0usize;
    while i < b.len() {
        // block comment
        if b[i] == '{' && i + 1 < b.len() && b[i + 1] == '-' {
            let mut depth = 1;
            i += 2;
            while i < b.len() && depth > 0 {
                if b[i] == '{' && i + 1 < b.len() && b[i + 1] == '-' {
                    depth += 1;
                    i += 2;
                } else if b[i] == '-' && i + 1 < b.len() && b[i + 1] == '}' {
                    depth -= 1;
                    i += 2;
                } else {
                    if b[i] == '\n' {
                        out.push('\n');
                    }
                    i += 1;
                }
            }
            continue;
        }
        // line comment
        if b[i] == '-' && i + 1 < b.len() && b[i + 1] == '-' {
            while i < b.len() && b[i] != '\n' {
                i += 1;
            }
            continue;
        }
        // triple-quoted string
        if b[i] == '"' && i + 2 < b.len() && b[i + 1] == '"' && b[i + 2] == '"' {
            i += 3;
            while i + 2 < b.len() && !(b[i] == '"' && b[i + 1] == '"' && b[i + 2] == '"') {
                if b[i] == '\n' {
                    out.push('\n');
                }
                i += 1;
            }
            i = (i + 3).min(b.len());
            continue;
        }
        // ordinary string
        if b[i] == '"' {
            i += 1;
            while i < b.len() && b[i] != '"' {
                if b[i] == '\\' {
                    i += 1;
                }
                i += 1;
            }
            i = (i + 1).min(b.len());
            out.push(' ');
            continue;
        }
        out.push(b[i]);
        i += 1;
    }
    out
}

/// What one file's `import` lines declare.
#[derive(Default)]
struct FileImports {
    /// alias (and the module's own name) -> module
    alias: BTreeMap<String, String>,
    /// module -> explicitly exposed names
    exposed: BTreeMap<String, BTreeSet<String>>,
    /// modules imported with `exposing (..)`
    exposing_all: BTreeSet<String>,
    modules: BTreeSet<String>,
}

fn is_module_name(s: &str) -> bool {
    !s.is_empty()
        && s.chars().next().map(|c| c.is_ascii_uppercase()).unwrap_or(false)
        && s.chars().all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '.')
}

/// Parse `import M`, `import M as A`, `import M exposing (a, b)`,
/// `import M as A exposing (..)`. An `exposing` list may wrap over lines, so
/// the parser keeps consuming until the closing paren.
fn parse_imports(src: &str) -> FileImports {
    let mut fi = FileImports::default();
    // Sky.Core.Prelude is auto-imported into every module. A file that
    // references `Result` without importing anything is still referencing the
    // stdlib, and pretending otherwise would inflate "unreferenced".
    fi.alias
        .insert("Sky.Core.Prelude".into(), "Sky.Core.Prelude".into());
    fi.exposing_all.insert("Sky.Core.Prelude".into());
    fi.modules.insert("Sky.Core.Prelude".into());

    let lines: Vec<&str> = src.lines().collect();
    let mut i = 0usize;
    while i < lines.len() {
        let line = lines[i];
        let trimmed = line.trim_start();
        if !trimmed.starts_with("import ") {
            i += 1;
            continue;
        }
        // An `exposing` list may wrap. `sky fmt` emits the leading-comma style,
        // where the opening paren is on the SECOND line — so "unbalanced
        // parens" alone is not the continuation test; a statement that says
        // `exposing` and has not yet closed a list is still open.
        let mut stmt = trimmed.to_string();
        i += 1;
        let open = |s: &str| -> bool {
            s.contains("exposing")
                && (!s.contains(')') || s.matches('(').count() > s.matches(')').count())
        };
        while open(&stmt) && i < lines.len() {
            let next = lines[i].trim();
            // A new declaration ends the list no matter what the parens say —
            // otherwise one malformed import would swallow the whole file and
            // credit it with every name in it.
            if next.starts_with("import ") || next.starts_with("module ") {
                break;
            }
            stmt.push(' ');
            stmt.push_str(next);
            i += 1;
        }

        let rest = stmt["import ".len()..].trim();
        let mut it = rest.split_whitespace();
        let Some(module) = it.next() else { continue };
        let module = module.trim_end_matches(',');
        if !is_module_name(module) {
            continue;
        }
        fi.modules.insert(module.to_string());
        fi.alias.insert(module.to_string(), module.to_string());

        if let Some(pos) = rest.find(" as ") {
            let after = rest[pos + 4..].trim();
            if let Some(alias) = after.split_whitespace().next() {
                if is_module_name(alias) {
                    fi.alias.insert(alias.to_string(), module.to_string());
                }
            }
        }
        if let Some(pos) = rest.find("exposing") {
            let after = &rest[pos + "exposing".len()..];
            if let (Some(o), Some(c)) = (after.find('('), after.rfind(')')) {
                if o < c {
                    let body = &after[o + 1..c];
                    if body.trim() == ".." {
                        fi.exposing_all.insert(module.to_string());
                    } else {
                        let set = fi.exposed.entry(module.to_string()).or_default();
                        for part in body.split(',') {
                            let name = part.trim().trim_end_matches("(..)").trim();
                            if !name.is_empty() {
                                set.insert(name.to_string());
                            }
                        }
                    }
                }
            }
        }
    }
    fi
}

/// Everything one file mentions: `(prefix, symbol)` pairs from dotted tokens,
/// and bare identifiers.
struct FileTokens {
    pairs: BTreeSet<(String, String)>,
    bare: BTreeSet<String>,
}

fn tokenize(src: &str) -> FileTokens {
    let mut pairs = BTreeSet::new();
    let mut bare = BTreeSet::new();
    let chars: Vec<char> = src.chars().collect();
    let mut i = 0usize;
    while i < chars.len() {
        let c = chars[i];
        if c.is_ascii_alphanumeric() || c == '_' {
            let start = i;
            while i < chars.len()
                && (chars[i].is_ascii_alphanumeric() || chars[i] == '_' || chars[i] == '.')
            {
                i += 1;
            }
            let run: String = chars[start..i].iter().collect();
            let run = run.trim_end_matches('.');
            if run.is_empty() {
                continue;
            }
            if !run.contains('.') {
                bare.insert(run.to_string());
                continue;
            }
            let segs: Vec<&str> = run.split('.').collect();
            for k in 1..segs.len() {
                let prefix = segs[..k].join(".");
                let sym = segs[k];
                if !prefix.is_empty() && !sym.is_empty() {
                    pairs.insert((prefix, sym.to_string()));
                }
            }
        } else {
            i += 1;
        }
    }
    FileTokens { pairs, bare }
}

/// The stdlib surface, indexed for the two reference rules.
struct Surfaces {
    modules: BTreeSet<String>,
    symbols: BTreeSet<(String, String)>,
    by_name: BTreeMap<String, BTreeSet<String>>,
}

/// Fold one file's references into `q` (strict) and `g` (generous).
fn refs_of_file(src: &str, s: &Surfaces, q: &mut BTreeSet<(String, String)>, g: &mut BTreeSet<(String, String)>) {
    let clean = strip_noise(src);
    let fi = parse_imports(&clean);
    let tk = tokenize(&clean);

    for (prefix, sym) in &tk.pairs {
        let mut cands: Vec<&String> = Vec::new();
        if s.modules.contains(prefix) {
            cands.push(prefix);
        }
        if let Some(m) = fi.alias.get(prefix) {
            cands.push(m);
        }
        for m in cands {
            let pair = (m.clone(), sym.clone());
            if s.symbols.contains(&pair) {
                q.insert(pair.clone());
                g.insert(pair);
            }
        }
    }

    for name in &tk.bare {
        let Some(owners) = s.by_name.get(name) else {
            continue;
        };
        for m in owners {
            let exposed_explicitly = fi
                .exposed
                .get(m)
                .map(|set| set.contains(name))
                .unwrap_or(false);
            if fi.exposing_all.contains(m) || exposed_explicitly {
                g.insert((m.clone(), name.clone()));
            }
        }
    }
}

// ------------------------------------------------------------ file discovery

fn is_generated(path: &Path) -> bool {
    path.components().any(|c| {
        matches!(
            c.as_os_str().to_str(),
            Some("sky-out") | Some(".skycache") | Some(".skydeps")
        )
    })
}

fn collect_sky(dir: &Path, out: &mut Vec<PathBuf>) {
    let Ok(rd) = std::fs::read_dir(dir) else {
        return;
    };
    let mut entries: Vec<PathBuf> = rd.filter_map(|e| e.ok()).map(|e| e.path()).collect();
    entries.sort();
    for p in entries {
        if is_generated(&p) {
            continue;
        }
        if p.is_dir() {
            collect_sky(&p, out);
        } else if p.extension().and_then(|e| e.to_str()) == Some("sky") {
            out.push(p);
        }
    }
}

/// `[section]` headers, and any `[database]` `driver = "..."` value.
fn read_sky_toml(path: &Path) -> (BTreeSet<String>, BTreeSet<String>) {
    let mut sections = BTreeSet::new();
    let mut drivers = BTreeSet::new();
    let Ok(src) = std::fs::read_to_string(path) else {
        return (sections, drivers);
    };
    let mut in_db = false;
    for line in src.lines() {
        let t = line.trim();
        if t.starts_with('#') || t.is_empty() {
            continue;
        }
        if t.starts_with('[') && t.ends_with(']') {
            sections.insert(t.to_string());
            in_db = t == "[database]";
            continue;
        }
        if in_db {
            if let Some((k, v)) = t.split_once('=') {
                if k.trim() == "driver" {
                    drivers.insert(v.trim().trim_matches('"').to_string());
                }
            }
        }
    }
    (sections, drivers)
}

/// `[[member]]` blocks from `apps/manifest.toml`.
///
/// Hand-parsed rather than pulling in a TOML dependency: the shape is four
/// scalar keys inside repeated array-of-table blocks, and adding a dependency
/// to read four keys is a worse trade than 25 lines that fail loudly when the
/// file stops looking like that (zero members parsed => hard error at the
/// call site).
fn parse_members(path: &Path) -> Result<Vec<BTreeMap<String, String>>, String> {
    let src = std::fs::read_to_string(path)
        .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
    let mut members: Vec<BTreeMap<String, String>> = Vec::new();
    let mut cur: Option<BTreeMap<String, String>> = None;
    for line in src.lines() {
        let t = line.trim();
        if t.starts_with('#') || t.is_empty() {
            continue;
        }
        if t == "[[member]]" {
            if let Some(m) = cur.take() {
                members.push(m);
            }
            cur = Some(BTreeMap::new());
            continue;
        }
        if t.starts_with('[') {
            if let Some(m) = cur.take() {
                members.push(m);
            }
            continue;
        }
        if let (Some(m), Some((k, v))) = (cur.as_mut(), t.split_once('=')) {
            let key = k.trim().to_string();
            let val = v.trim().trim_matches('"').to_string();
            if !val.starts_with('[') {
                m.insert(key, val);
            }
        }
    }
    if let Some(m) = cur {
        members.push(m);
    }
    if members.is_empty() {
        return Err(format!(
            "{} declared ZERO [[member]] blocks — the Layer 2 membership authority is \
             empty or its shape changed. Refusing to report a corpus with no members.",
            path.display()
        ));
    }
    Ok(members)
}

/// Concatenate the contents of Rust string literals in `src`, unescaping the
/// escapes that matter for reading Sky source back out (`\n`, `\"`, `\\`) and
/// honouring `\` line continuations.
///
/// The Layer 1 corpus is generated, not committed, so the only committed
/// artefact that says which modules it touches is the generator's own emitted
/// source templates. Scanning the raw `.rs` would match Rust identifiers
/// (`map`, `len`) against stdlib names; restricting to string literals keeps
/// the scan inside the Sky text the generator actually emits.
fn rust_string_literals(src: &str) -> String {
    let chars: Vec<char> = src.chars().collect();
    let mut out = String::new();
    let mut i = 0usize;
    while i < chars.len() {
        if chars[i] != '"' {
            i += 1;
            continue;
        }
        i += 1;
        while i < chars.len() && chars[i] != '"' {
            if chars[i] == '\\' && i + 1 < chars.len() {
                match chars[i + 1] {
                    'n' => out.push('\n'),
                    't' => out.push(' '),
                    '"' => out.push('"'),
                    '\\' => out.push('\\'),
                    '\n' => {
                        // line continuation: skip the newline and the leading
                        // whitespace of the next line
                        i += 2;
                        while i < chars.len() && (chars[i] == ' ' || chars[i] == '\t') {
                            i += 1;
                        }
                        continue;
                    }
                    other => out.push(other),
                }
                i += 2;
                continue;
            }
            out.push(chars[i]);
            i += 1;
        }
        out.push('\n');
        i += 1;
    }
    out
}

// ------------------------------------------------------------------- gates

/// Gate name -> `PROVEN` in `docs/coverage/falsifier-proofs.json`.
fn read_proofs(repo_root: &Path) -> BTreeMap<String, bool> {
    let mut out = BTreeMap::new();
    let path = repo_root.join("docs/coverage/falsifier-proofs.json");
    let Ok(src) = std::fs::read_to_string(&path) else {
        return out;
    };
    let Ok(v) = serde_json::from_str::<Value>(&src) else {
        return out;
    };
    if let Some(map) = v["gates"].as_object() {
        for (name, g) in map {
            out.insert(
                name.clone(),
                g["observed"].as_str() == Some("PROVEN"),
            );
        }
    }
    out
}

/// A registered gate's coverage strength: `Falsified` only when its falsifying
/// mutation is recorded PROVEN, `Asserted` otherwise. A registered-but-unproven
/// gate still counts assertions; it just has not shown they can go red.
///
/// A **BLOCKED** gate contributes `None`. `GateState::counts_as_cover()` is
/// false for `Blocked`, and that property is the entire reason the state was
/// affordable: a block that still counted as coverage would be a skip with
/// better paperwork.
fn gate_strength(gate: &str, proofs: &BTreeMap<String, bool>) -> Strength {
    if crate::harness::registry::block_for(gate).is_some() {
        return Strength::None;
    }
    if *proofs.get(gate).unwrap_or(&false) {
        Strength::Falsified
    } else {
        Strength::Asserted
    }
}

fn registered(gate: &str) -> bool {
    GATES.iter().any(|g| g.name == gate)
}

/// The `.github/workflows/**` tree — the only place a step "runs in CI".
fn workflow_roots(repo_root: &Path) -> Vec<PathBuf> {
    vec![repo_root.join(".github/workflows")]
}

/// Which [`CI_SURFACES`] rows are actually invoked by the workflows, with the
/// evidence string naming where.
///
/// Returns `(key, evidence, surface ids)`. A row whose invocation is not found
/// is NOT returned: the table declares what an invocation would cover, and the
/// workflows decide whether it happens. That ordering is deliberate — it makes
/// deleting a CI step delete the coverage claim, instead of leaving a table
/// entry asserting coverage for a step that no longer runs.
type CiInvocation = (String, String, Vec<String>);

fn ci_invocations(repo_root: &Path) -> Result<Vec<CiInvocation>, String> {
    let roots = workflow_roots(repo_root);
    let (xrefs, unresolved) = crate::ci_scan::scan_xtask_refs(repo_root, &roots);
    if !unresolved.is_empty() {
        return Err(format!(
            "cannot read every xtask invocation in .github/workflows:\n  {}\n\
             An unread invocation would be scored as absent, which understates coverage.",
            unresolved.join("\n  ")
        ));
    }
    let srefs = crate::ci_scan::scan_script_refs(repo_root, &roots);

    // key -> the first place it is invoked, for the evidence string.
    let mut found: BTreeMap<String, String> = BTreeMap::new();
    // Deliberately NO line number in the evidence string: it would make the
    // checked-in ledger go stale whenever an unrelated CI step is inserted
    // above the invocation, and a gate that cries stale on noise stops being
    // read. The file name is the checkable part.
    for r in &xrefs {
        found
            .entry(format!("xtask:{}", r.gate))
            .or_insert_with(|| format!("{}: xtask {}", r.file, r.gate));
    }
    for r in &srefs {
        found
            .entry(format!("script:{}", r.gate))
            .or_insert_with(|| format!("{}: {}", r.file, r.gate));
    }

    let mut out: Vec<CiInvocation> = Vec::new();
    for (key, ids) in CI_SURFACES {
        if let Some(lit) = key.strip_prefix("cmd:") {
            if crate::ci_scan::mentions_command(&roots, lit) {
                out.push((
                    (*key).to_string(),
                    format!(".github/workflows: {lit}"),
                    ids.iter().map(|s| (*s).to_string()).collect(),
                ));
            }
            continue;
        }
        if let Some(evidence) = found.get(*key) {
            out.push((
                (*key).to_string(),
                evidence.clone(),
                ids.iter().map(|s| (*s).to_string()).collect(),
            ));
        }
    }
    Ok(out)
}

// ------------------------------------------------------------------ CLI verbs

/// The CLI verb list, DERIVED — never typed.
///
/// Primary source is the dispatch table in `rust/crates/sky/src/main.rs`
/// (`Some("<verb>") => cmd_...`): that is what actually runs, so a verb that
/// exists but is undocumented still gets a row, and a documented verb that was
/// deleted stops getting one. `docs/tooling/cli.md`'s `### \`sky <verb>\``
/// headings are read as a CROSS-CHECK, and the two directions of disagreement
/// are reported rather than silently unioned away.
/// `(dispatched verbs, dispatched-but-undocumented, documented-but-not-dispatched)`.
type CliVerbs = (Vec<String>, Vec<String>, Vec<String>);

fn derive_cli_verbs(repo_root: &Path) -> Result<CliVerbs, String> {
    let main_rs = repo_root.join("rust/crates/sky/src/main.rs");
    let src = std::fs::read_to_string(&main_rs)
        .map_err(|e| format!("cannot read {}: {e}", main_rs.display()))?;
    let mut dispatched: BTreeSet<String> = BTreeSet::new();
    for line in src.lines() {
        let t = line.trim();
        let Some(rest) = t.strip_prefix("Some(\"") else {
            continue;
        };
        let Some(end) = rest.find('"') else { continue };
        let verb = &rest[..end];
        if !t.contains("=> cmd_") {
            continue;
        }
        if verb.is_empty()
            || verb.starts_with("--")
            || verb.starts_with("__")
            || !verb
                .chars()
                .all(|c| c.is_ascii_lowercase() || c == '-')
        {
            continue;
        }
        dispatched.insert(verb.to_string());
    }
    if dispatched.is_empty() {
        return Err(format!(
            "derived ZERO CLI verbs from {} — the dispatch shape changed. \
             A ledger with no CLI rows would silently report full CLI coverage.",
            main_rs.display()
        ));
    }

    let doc = repo_root.join("docs/tooling/cli.md");
    let mut documented: BTreeSet<String> = BTreeSet::new();
    if let Ok(md) = std::fs::read_to_string(&doc) {
        for line in md.lines() {
            let Some(rest) = line.trim().strip_prefix("### `sky ") else {
                continue;
            };
            let verb: String = rest
                .chars()
                .take_while(|c| c.is_ascii_lowercase() || *c == '-')
                .collect();
            if !verb.is_empty() {
                documented.insert(verb);
            }
        }
    }

    let undocumented: Vec<String> = dispatched.difference(&documented).cloned().collect();
    let undispatched: Vec<String> = documented.difference(&dispatched).cloned().collect();
    Ok((
        dispatched.into_iter().collect(),
        undocumented,
        undispatched,
    ))
}

// --------------------------------------------------------------- computation

struct Ledger {
    surfaces: Vec<Surface>,
    doc: Value,
}

fn compute(repo_root: &Path) -> Result<Ledger, String> {
    // ---- 1. the stdlib surface, from the `sky doc --export` code path ------
    let tmp = std::env::temp_dir().join(format!("sky-coverage-ledger-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&tmp);
    let proj = tmp.join("project");
    let out = tmp.join("site");
    std::fs::create_dir_all(&proj).map_err(|e| format!("temp dir: {e}"))?;
    std::fs::create_dir_all(&out).map_err(|e| format!("temp dir: {e}"))?;
    let export = project::render_doc_site_export(repo_root, &proj, &out)
        .map_err(|e| format!("sky doc --export code path FAILED: {e}"));
    let manifest = export.and_then(|()| {
        std::fs::read_to_string(out.join("api").join("symbols.json"))
            .map_err(|e| format!("no api/symbols.json: {e}"))
    });
    let _ = std::fs::remove_dir_all(&tmp);
    let manifest: Value =
        serde_json::from_str(&manifest?).map_err(|e| format!("symbols.json is not JSON: {e}"))?;
    let entries = manifest["entries"]
        .as_array()
        .ok_or("symbols.json has no `entries` array")?;

    let mut surf = Surfaces {
        modules: BTreeSet::new(),
        symbols: BTreeSet::new(),
        by_name: BTreeMap::new(),
    };
    let mut bucket_of: BTreeMap<(String, String), String> = BTreeMap::new();
    for e in entries {
        let m = e["module"].as_str().unwrap_or_default().to_string();
        let n = e["name"].as_str().unwrap_or_default().to_string();
        if m.is_empty() || n.is_empty() {
            continue;
        }
        surf.modules.insert(m.clone());
        surf.symbols.insert((m.clone(), n.clone()));
        surf.by_name.entry(n.clone()).or_default().insert(m.clone());
        bucket_of.insert(
            (m, n),
            e["bucket"].as_str().unwrap_or_default().to_string(),
        );
    }

    // ---- 2. the denominator contract: ONE denominator, not two ------------
    let den_path = repo_root.join("docs/coverage/denominators.json");
    let den: Value = serde_json::from_str(
        &std::fs::read_to_string(&den_path)
            .map_err(|e| format!("cannot read {}: {e}", den_path.display()))?,
    )
    .map_err(|e| format!("{} is not JSON: {e}", den_path.display()))?;
    let den_entries = den["stdlib"]["entries"].as_u64().unwrap_or(0) as usize;
    let den_modules = den["stdlib"]["modules"].as_u64().unwrap_or(0) as usize;
    if den_entries != surf.symbols.len() || den_modules != surf.modules.len() {
        return Err(format!(
            "DENOMINATOR DRIFT — this ledger and {} disagree about the stdlib surface:\n  \
             ledger (api/symbols.json): {} entries / {} modules\n  \
             denominators.json:         {den_entries} entries / {den_modules} modules\n\n\
             The denominator contract (docs/ci-test-architecture-v2.md §5.3) exists so that \
             exactly ONE script produces every denominator. Two independently-derived numbers \
             that differ means one of them is stale; picking either would record a coin toss \
             as a fact. Run `xtask denominators` and commit the result, then re-run this gate.",
            den_path.display(),
            surf.symbols.len(),
            surf.modules.len()
        ));
    }

    // ---- 3. units ----------------------------------------------------------
    let mut units = enumerate_units(repo_root, &surf)?;
    units.sort_by(|a, b| a.id.cmp(&b.id));

    let proofs = read_proofs(repo_root);
    let layer2_paths: BTreeSet<String> = units
        .iter()
        .filter(|u| u.role == Role::Layer2)
        .map(|u| u.path_key.clone())
        .collect();

    // ---- 4. surface rows ---------------------------------------------------
    let mut surfaces: Vec<Surface> = Vec::new();

    // (a) stdlib modules
    for m in &surf.modules {
        let mut today: Vec<Ev> = Vec::new();
        let mut new: Vec<Ev> = Vec::new();
        for u in &units {
            if !u.imports.contains(m) {
                continue;
            }
            match u.role {
                Role::Example => {
                    let s = if u.has_assertions {
                        Strength::Asserted
                    } else {
                        Strength::Runs
                    };
                    let why = if u.has_assertions {
                        "built + run by the example sweep; owns a tests/ suite with real assertions"
                    } else {
                        "built + run by scripts/example-sweep.sh + xtask build-run --run"
                    };
                    today.push(Ev::new(format!("{} ({why})", u.id), s));
                }
                Role::Conformance => {
                    today.push(Ev::new(
                        format!("{} (scripts/conformance.sh: counted Sky.Test assertions)", u.id),
                        Strength::Asserted,
                    ));
                }
                Role::SkySuite => {
                    // Verified against scripts/conformance.sh (and pinned by
                    // `conformance_runner_still_only_globs_the_conformance_project`):
                    // it sets PROJ="$ROOT/tests/conformance", cds there, and
                    // globs `tests/*Test.sky` RELATIVE to that directory — i.e.
                    // tests/conformance/tests/*Test.sky. Nothing in the
                    // pre-overhaul world globs tests/<Sub>/**, so these suites
                    // were compiled by nothing and run by nothing. Their
                    // assertions existed; nothing ever read them.
                    today.push(Ev::new(
                        format!(
                            "{} — NO PRE-OVERHAUL RUNNER: scripts/conformance.sh globs \
                             tests/*Test.sky relative to tests/conformance, so \
                             tests/<Sub>/*Test.sky was never executed",
                            u.id
                        ),
                        Strength::None,
                    ));
                }
                Role::Layer2 | Role::Layer1 => {}
            }
            match u.role {
                // Any unit that DECLARES a gate is scored by whether that gate
                // is actually registered — one rule, so the ledger tracks the
                // registry instead of a hard-coded idea of it. `tests/<Sub>`
                // reaches this arm through the `sky-suites` gate: those suites
                // are unrun in the old world (strength 0 above) and gated in the
                // new one, which is a `stronger` verdict, not a weakening.
                Role::Layer2 | Role::Conformance | Role::SkySuite => {
                    let gate = u.gate.clone().unwrap_or_default();
                    if registered(&gate) {
                        new.push(Ev::new(
                            format!("{} (gate `{gate}`)", u.id),
                            gate_strength(&gate, &proofs),
                        ));
                    } else {
                        new.push(Ev::new(
                            format!("{} (gate `{gate}` is NOT in the registry)", u.id),
                            Strength::None,
                        ));
                    }
                }
                // `examples/` is RETAINED (v2 §9.6): no example is deleted, the
                // directory keeps its path, and the compiler-facing ratchets
                // (roundtrip / infer / shared-world / coerce-floor / the stdout
                // goldens) still key on it. So an example still contributes to
                // the new world — CAPPED, because its PRODUCT-facing regression
                // duty genuinely moved to Layer 2. It is built and run, which is
                // `Runs`; it reaches `Asserted` only by owning a tests/ suite
                // that the registered `sky-verify` gate executes; it can never
                // reach `Falsified` from being an example.
                Role::Example => {
                    if layer2_paths.contains(&u.path_key) {
                        new.push(Ev::new(
                            format!("{} (also a Layer 2 member path)", u.id),
                            Strength::Runs,
                        ));
                    } else if u.has_assertions && registered("sky-verify") {
                        new.push(Ev::new(
                            format!("{} (retained; tests/ suite run by gate `sky-verify`)", u.id),
                            Strength::Asserted.min(gate_strength("sky-verify", &proofs)),
                        ));
                    } else {
                        new.push(Ev::new(
                            format!("{} (retained; built + run by gate `build-run` + the sweep)", u.id),
                            Strength::Runs,
                        ));
                    }
                }
                _ => {}
            }
        }
        // Layer 1 is reference-based, not import-based: its cases are
        // generated, so "imports" is what the generator's templates emit.
        if let Some(u) = units.iter().find(|u| u.role == Role::Layer1) {
            if u.imports.contains(m) {
                new.push(Ev::new(
                    format!("{} (gate `corpus`)", u.id),
                    gate_strength("corpus", &proofs),
                ));
            }
        }
        surfaces.push(Surface {
            id: format!("stdlib.{m}"),
            category: "stdlib".into(),
            description: format!("the {m} public API"),
            today,
            new,
        });
    }

    // (b) CLI verbs
    let (verbs, undocumented, undispatched) = derive_cli_verbs(repo_root)?;
    let verify_cli_src =
        std::fs::read_to_string(repo_root.join("scripts/verify-cli.sh")).unwrap_or_default();
    let mut flow_src = String::new();
    if let Ok(rd) = std::fs::read_dir(repo_root.join("rust/crates/sky/tests")) {
        let mut paths: Vec<PathBuf> = rd.filter_map(|e| e.ok()).map(|e| e.path()).collect();
        paths.sort();
        for p in paths {
            if p.file_name()
                .and_then(|f| f.to_str())
                .map(|f| f.ends_with("_flow.rs"))
                .unwrap_or(false)
            {
                flow_src.push_str(&std::fs::read_to_string(&p).unwrap_or_default());
            }
        }
    }
    for verb in &verbs {
        let mut today: Vec<Ev> = Vec::new();
        let mut new: Vec<Ev> = Vec::new();
        if verify_cli_src.contains(&format!("sky {verb}")) {
            today.push(Ev::new(
                "scripts/verify-cli.sh (local-only: no workflow step invokes it)",
                Strength::Runs,
            ));
            new.push(Ev::new(
                "gate `verify-cli`",
                gate_strength("verify-cli", &proofs),
            ));
        }
        if flow_src.contains(&format!("\"{verb}\"")) {
            new.push(Ev::new(
                "gate `cli-verbs` (rust/crates/sky/tests/*_flow.rs)",
                gate_strength("cli-verbs", &proofs),
            ));
        }
        surfaces.push(Surface {
            id: format!("cli.{verb}"),
            category: "cli".into(),
            description: format!("`sky {verb}`"),
            today,
            new,
        });
    }

    // (c) cross-cutting
    for cs in CROSS_CUTTING {
        let today: Vec<Ev> = cs
            .today
            .iter()
            .map(|(by, s)| Ev::new(*by, Strength::from_u8(*s)))
            .collect();
        surfaces.push(Surface {
            id: cs.id.into(),
            category: cs.category.into(),
            description: cs.description.into(),
            today,
            new: Vec::new(),
        });
    }

    // GATE_SURFACES contributes new-world evidence to ANY surface row it names
    // — stdlib, cli, or cross-cutting. One mechanism, so a gate cannot claim a
    // surface in one namespace and be invisible in another.
    let mut by_id: BTreeMap<String, usize> = BTreeMap::new();
    for (i, s) in surfaces.iter().enumerate() {
        by_id.insert(s.id.clone(), i);
    }
    for (gate, ids) in GATE_SURFACES {
        if !registered(gate) {
            continue;
        }
        let s = gate_strength(gate, &proofs);
        let note = if crate::harness::registry::block_for(gate).is_some() {
            " — BLOCKED, counts as UNCOVERED"
        } else {
            ""
        };
        for id in *ids {
            if let Some(i) = by_id.get(*id) {
                surfaces[*i]
                    .new
                    .push(Ev::new(format!("gate `{gate}`{note}"), s));
            }
        }
    }

    // Third source: what CI runs that the harness has not adopted. Matched
    // against the workflows, so a deleted CI step deletes its coverage claim.
    for (key, evidence, ids) in ci_invocations(repo_root)? {
        for id in ids {
            if let Some(i) = by_id.get(&id) {
                surfaces[*i].new.push(Ev::new(
                    format!("{evidence} (`{key}` is not harness-registered — no proven falsifier)"),
                    Strength::Asserted,
                ));
            }
        }
    }
    surfaces.sort_by(|a, b| a.id.cmp(&b.id));

    // ---- 5. sole ownership -------------------------------------------------
    // Ownership is asked of distinct PATHS, then reported with the unit ids
    // that share the path (member D and member E deliberately reuse a path).
    let ids_for_path = |key: &str| -> Vec<String> {
        units
            .iter()
            .filter(|u| u.path_key == key)
            .map(|u| u.id.clone())
            .collect()
    };

    let mut mods_examples_only: BTreeMap<String, Value> = BTreeMap::new();
    let mut mods_repo_wide: BTreeMap<String, Value> = BTreeMap::new();
    let mut lost_modules: BTreeMap<String, Value> = BTreeMap::new();
    for m in &surf.modules {
        let ex_owners: BTreeSet<String> = units
            .iter()
            .filter(|u| u.role == Role::Example && u.imports.contains(m))
            .map(|u| u.path_key.clone())
            .collect();
        if ex_owners.len() == 1 {
            let key = ex_owners.iter().next().unwrap().clone();
            mods_examples_only.insert(m.clone(), json!(key));
        }
        let all_owners: BTreeSet<String> = units
            .iter()
            .filter(|u| u.imports.contains(m))
            .map(|u| u.path_key.clone())
            .collect();
        if all_owners.len() == 1 {
            let key = all_owners.iter().next().unwrap().clone();
            mods_repo_wide.insert(
                m.clone(),
                json!({ "path": key, "units": ids_for_path(&key) }),
            );
            let is_example_only = units
                .iter()
                .filter(|u| u.path_key == key)
                .all(|u| u.role == Role::Example);
            if is_example_only && !layer2_paths.contains(&key) {
                lost_modules.insert(m.clone(), json!(key));
            }
        }
    }

    let mut section_owners: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
    for u in &units {
        for s in &u.toml_sections {
            section_owners
                .entry(s.clone())
                .or_default()
                .insert(u.path_key.clone());
        }
        for d in &u.db_drivers {
            section_owners
                .entry(format!("[database] driver = \"{d}\""))
                .or_default()
                .insert(u.path_key.clone());
        }
    }
    let mut sole_sections: BTreeMap<String, Value> = BTreeMap::new();
    let mut lost_sections: BTreeMap<String, Value> = BTreeMap::new();
    for (sec, owners) in &section_owners {
        if owners.len() != 1 {
            continue;
        }
        let key = owners.iter().next().unwrap().clone();
        sole_sections.insert(
            sec.clone(),
            json!({ "path": key, "units": ids_for_path(&key) }),
        );
        let is_example_only = units
            .iter()
            .filter(|u| u.path_key == key)
            .all(|u| u.role == Role::Example);
        if is_example_only && !layer2_paths.contains(&key) {
            lost_sections.insert(sec.clone(), json!(key));
        }
    }

    // ---- 6. uncovered ------------------------------------------------------
    let mut all_q: BTreeSet<(String, String)> = BTreeSet::new();
    let mut all_g: BTreeSet<(String, String)> = BTreeSet::new();
    for u in &units {
        all_q.extend(u.qualified.iter().cloned());
        all_g.extend(u.generous.iter().cloned());
    }
    let imported_anywhere: BTreeSet<String> = units
        .iter()
        .flat_map(|u| u.imports.iter().cloned())
        .collect();
    let imported_by_running: BTreeSet<String> = units
        .iter()
        .filter(|u| u.role != Role::SkySuite)
        .flat_map(|u| u.imports.iter().cloned())
        .collect();
    let unimported: Vec<String> = surf
        .modules
        .difference(&imported_anywhere)
        .cloned()
        .collect();
    let only_unrun: Vec<String> = imported_anywhere
        .difference(&imported_by_running)
        .cloned()
        .collect();

    let breakdown = |missing: &BTreeSet<(String, String)>| -> (Value, usize) {
        let mut per: BTreeMap<String, Vec<String>> = BTreeMap::new();
        for (m, n) in missing {
            per.entry(m.clone()).or_default().push(n.clone());
        }
        let obj: serde_json::Map<String, Value> = per
            .into_iter()
            .map(|(m, mut names)| {
                names.sort();
                (m, json!({ "count": names.len(), "symbols": names }))
            })
            .collect();
        (Value::Object(obj), missing.len())
    };
    let missing_q: BTreeSet<(String, String)> =
        surf.symbols.difference(&all_q).cloned().collect();
    let missing_g: BTreeSet<(String, String)> =
        surf.symbols.difference(&all_g).cloned().collect();
    let (bq, nq) = breakdown(&missing_q);
    let (bg, ng) = breakdown(&missing_g);

    let zero_new: Vec<String> = surfaces
        .iter()
        .filter(|s| s.new_max() == Strength::None)
        .map(|s| s.id.clone())
        .collect();

    // ---- 7. assemble -------------------------------------------------------
    let total = surfaces.len();
    let covered = surfaces
        .iter()
        .filter(|s| s.new_max() >= Strength::Asserted)
        .count();
    let weaker: Vec<String> = surfaces
        .iter()
        .filter(|s| s.verdict() == "weaker")
        .map(|s| s.id.clone())
        .collect();
    let stronger = surfaces.iter().filter(|s| s.verdict() == "stronger").count();
    let equal = surfaces.iter().filter(|s| s.verdict() == "equal").count();

    let mut by_role: BTreeMap<String, usize> = BTreeMap::new();
    for u in &units {
        *by_role.entry(u.role.label().to_string()).or_default() += 1;
    }

    let pct = |n: usize, d: usize| -> f64 {
        if d == 0 {
            0.0
        } else {
            (n as f64) * 100.0 / (d as f64)
        }
    };

    let gates_json: Value = Value::Object(
        GATES
            .iter()
            .filter(|g| g.tier != Tier::SelfTest)
            .map(|g| {
                let ids: Vec<&str> = GATE_SURFACES
                    .iter()
                    .find(|(n, _)| *n == g.name)
                    .map(|(_, ids)| ids.to_vec())
                    .unwrap_or_default();
                (
                    g.name.to_string(),
                    json!({
                        "tier": g.tier.label(),
                        "expected_assertions": g.expected,
                        "summary": g.summary,
                        "falsifier_proven": *proofs.get(g.name).unwrap_or(&false),
                        "surfaces": ids,
                    }),
                )
            })
            .collect(),
    );

    let units_json: Value = Value::Array(
        units
            .iter()
            .map(|u| {
                json!({
                    "id": u.id,
                    "role": u.role.label(),
                    "path": u.path_key,
                    "gate": u.gate,
                    "sky_files": u.files,
                    "modules_imported": u.imports.len(),
                    "refs_qualified": u.qualified.len(),
                    "refs_generous": u.generous.len(),
                    "sky_toml_sections": u.toml_sections.iter().cloned().collect::<Vec<_>>(),
                    "db_drivers": u.db_drivers.iter().cloned().collect::<Vec<_>>(),
                })
            })
            .collect(),
    );

    // Counted over distinct PATHS, not units: `apps/ledger` backs two member
    // rows, and counting rows would report its `[database]` twice — inflating
    // the config totals with one directory pretending to be two projects.
    let mut config_sections: BTreeMap<String, usize> = BTreeMap::new();
    let mut config_drivers: BTreeMap<String, usize> = BTreeMap::new();
    let mut counted: BTreeSet<&String> = BTreeSet::new();
    let mut paths_with_toml = 0usize;
    for u in &units {
        if !counted.insert(&u.path_key) {
            continue;
        }
        if !u.toml_sections.is_empty() {
            paths_with_toml += 1;
        }
        for s in &u.toml_sections {
            *config_sections.entry(s.clone()).or_default() += 1;
        }
        for d in &u.db_drivers {
            *config_drivers.entry(d.clone()).or_default() += 1;
        }
    }

    let doc = json!({
        "_README": "GENERATED by `xtask coverage-ledger` — do not hand-edit. Every number here \
                    was measured from the tree at generation time; nothing is typed by a person. \
                    A DECREASE in summary.surfaces_covered, a DECREASE in any surface's cover_new, \
                    or a `weaker` verdict without a [[weakening]] stanza in \
                    docs/coverage/removals.toml fails `xtask coverage-ledger --check`. \
                    See docs/ci-test-architecture-v2.md §9.2.",
        "_source": {
            "stdlib": "api/symbols.json via project::render_doc_site_export (the `sky doc --export` \
                       code path); cross-checked against docs/coverage/denominators.json",
            "cli_verbs": "the dispatch table in rust/crates/sky/src/main.rs (Some(\"<verb>\") => cmd_...), \
                          cross-checked against the `### `sky <verb>`` headings in docs/tooling/cli.md",
            "layer2": "the [[member]] blocks of apps/manifest.toml (the membership authority)",
            "layer1": "modules named by `import <Module>` inside the string literals of \
                       rust/crates/xtask/src/corpus/*.rs",
            "gates": "crate::harness::registry::GATES + docs/coverage/falsifier-proofs.json",
            "references": "TEXTUAL. `qualified` (STRICT) requires a literal <Alias|Module>.<symbol> \
                           token; `generous` additionally credits bare <symbol> in a file that \
                           imports <module> and exposes it. Every uncovered claim uses STRICT, \
                           because over-counting references understates uncovered surface and \
                           understating uncovered surface is the forbidden direction."
        },
        "strength_classes": {
            "0": "None — nothing covers it",
            "1": "Builds — something compiles it, nothing runs it",
            "2": "Runs — something builds AND runs it; verdict = exit status only",
            "3": "Asserted — explicit counted assertions",
            "4": "Falsified — assertions in a REGISTERED gate whose falsifying mutation is \
                  recorded PROVEN in docs/coverage/falsifier-proofs.json"
        },
        "summary": {
            "surfaces_total": total,
            "surfaces_covered": covered,
            "surfaces_covered_pct": (pct(covered, total) * 10.0).round() / 10.0,
            "surfaces_stronger": stronger,
            "surfaces_equal": equal,
            "surfaces_weaker": weaker.len(),
            "units_total": units.len(),
            "units_by_role": by_role,
            "stdlib_modules": surf.modules.len(),
            "stdlib_entries": surf.symbols.len()
        },
        "surfaces": Value::Array(surfaces.iter().map(|s| s.to_json()).collect()),
        "surfaces_weaker": weaker,
        "units": units_json,
        "gates": gates_json,
        "cli": {
            "_source": "rust/crates/sky/src/main.rs dispatch table (primary); \
                        docs/tooling/cli.md headings (cross-check)",
            "verbs": verbs,
            "dispatched_but_undocumented": undocumented,
            "documented_but_not_dispatched": undispatched
        },
        "config": {
            "_note": "counted over distinct paths: apps/ledger backs two member rows and must \
                      not have its sections counted twice.",
            "paths_with_sky_toml": paths_with_toml,
            "sections": config_sections,
            "database_drivers": config_drivers
        },
        "sole_ownership": {
            "_note": "ownership is computed over distinct PATHS, because apps/manifest.toml \
                      deliberately backs two members with apps/ledger and backs member D with \
                      examples/13-skyshop. Counting member rows would make a shared path look \
                      like two independent owners and hide the sole ownership.",
            "stdlib_modules_examples_only": mods_examples_only,
            "stdlib_modules_repo_wide": mods_repo_wide,
            "config_sections": sole_sections,
            "lost_if_examples_retired": {
                "_note": "sole owner is an examples/* path that is NOT a Layer 2 member path — \
                          exactly what retiring examples/ would remove.",
                "stdlib_modules": lost_modules,
                "config_sections": lost_sections
            }
        },
        "uncovered": {
            "modules_imported_by_nothing": {
                "count": unimported.len(),
                "pct_of_denominator": (pct(unimported.len(), den_modules) * 10.0).round() / 10.0,
                "modules": unimported
            },
            "modules_imported_only_by_root_test_suites": {
                "_note": "imported ONLY by the root tests/<Sub> suites — the ones no runner \
                          executed before this overhaul. They are gated now (`sky-suites`), \
                          but their sole owner is still a suite, not an application: nothing \
                          builds or runs these modules in a real program.",
                "count": only_unrun.len(),
                "modules": only_unrun
            },
            "symbols_unreferenced_strict": {
                "count": nq,
                "pct_of_denominator": (pct(nq, den_entries) * 10.0).round() / 10.0,
                "by_module": bq
            },
            "symbols_unreferenced_generous": {
                "count": ng,
                "pct_of_denominator": (pct(ng, den_entries) * 10.0).round() / 10.0,
                "by_module": bg
            },
            "surfaces_with_zero_new_cover": {
                "count": zero_new.len(),
                "surfaces": zero_new
            }
        }
    });

    Ok(Ledger { surfaces, doc })
}

fn enumerate_units(repo_root: &Path, surf: &Surfaces) -> Result<Vec<Unit>, String> {
    let mut units: Vec<Unit> = Vec::new();

    let build = |id: String,
                     role: Role,
                     path_key: String,
                     gate: Option<String>,
                     files: Vec<PathBuf>,
                     toml: Option<PathBuf>,
                     has_assertions: bool|
     -> Unit {
        let mut imports = BTreeSet::new();
        let mut qualified = BTreeSet::new();
        let mut generous = BTreeSet::new();
        for f in &files {
            let Ok(src) = std::fs::read_to_string(f) else {
                continue;
            };
            let clean = strip_noise(&src);
            for m in parse_imports(&clean).modules {
                if surf.modules.contains(&m) {
                    imports.insert(m);
                }
            }
            refs_of_file(&src, surf, &mut qualified, &mut generous);
        }
        let (toml_sections, db_drivers) = match &toml {
            Some(p) if p.is_file() => read_sky_toml(p),
            _ => (BTreeSet::new(), BTreeSet::new()),
        };
        Unit {
            id,
            role,
            path_key,
            gate,
            files: files.len(),
            imports,
            qualified,
            generous,
            toml_sections,
            db_drivers,
            has_assertions,
        }
    };

    // --- Example ------------------------------------------------------------
    let ex_root = repo_root.join("examples");
    let mut ex_dirs: Vec<PathBuf> = std::fs::read_dir(&ex_root)
        .map_err(|e| format!("cannot read {}: {e}", ex_root.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.is_dir() && (p.join("sky.toml").is_file() || p.join("src").is_dir()))
        .collect();
    ex_dirs.sort();
    for dir in ex_dirs {
        let name = dir.file_name().unwrap().to_string_lossy().to_string();
        let mut files = Vec::new();
        collect_sky(&dir, &mut files);
        let tests_dir = dir.join("tests");
        let has_assertions = tests_dir.is_dir() && {
            let mut tf = Vec::new();
            collect_sky(&tests_dir, &mut tf);
            tf.iter().any(|p| {
                let src = std::fs::read_to_string(p).unwrap_or_default();
                ASSERTION_FNS
                    .iter()
                    .any(|f| src.contains(&format!("Test.{f}")))
            })
        };
        units.push(build(
            format!("examples/{name}"),
            Role::Example,
            format!("examples/{name}"),
            None,
            files,
            Some(dir.join("sky.toml")),
            has_assertions,
        ));
    }

    // --- Layer 2 ------------------------------------------------------------
    let members = parse_members(&repo_root.join("apps/manifest.toml"))?;
    for m in &members {
        let (Some(name), Some(path)) = (m.get("name"), m.get("path")) else {
            return Err(format!(
                "apps/manifest.toml has a [[member]] without name/path: {m:?}"
            ));
        };
        let dir = repo_root.join(path);
        let mut files = Vec::new();
        collect_sky(&dir, &mut files);
        units.push(build(
            format!("apps:{name}"),
            Role::Layer2,
            path.clone(),
            m.get("gate").cloned(),
            files,
            Some(dir.join("sky.toml")),
            true,
        ));
    }

    // --- Conformance --------------------------------------------------------
    let conf = repo_root.join("tests/conformance");
    let mut cfiles = Vec::new();
    collect_sky(&conf, &mut cfiles);
    units.push(build(
        "tests/conformance".into(),
        Role::Conformance,
        "tests/conformance".into(),
        Some("conformance".into()),
        cfiles,
        Some(conf.join("sky.toml")),
        true,
    ));

    // --- SkySuite -----------------------------------------------------------
    let tests_root = repo_root.join("tests");
    let mut subs: Vec<PathBuf> = std::fs::read_dir(&tests_root)
        .map_err(|e| format!("cannot read {}: {e}", tests_root.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.is_dir() && p.file_name().and_then(|f| f.to_str()) != Some("conformance"))
        .collect();
    subs.sort();
    for dir in subs {
        let mut files = Vec::new();
        collect_sky(&dir, &mut files);
        files.retain(|p| {
            p.file_name()
                .and_then(|f| f.to_str())
                .map(|f| f.ends_with("Test.sky"))
                .unwrap_or(false)
        });
        if files.is_empty() {
            continue;
        }
        let name = dir.file_name().unwrap().to_string_lossy().to_string();
        units.push(build(
            format!("tests/{name}"),
            Role::SkySuite,
            format!("tests/{name}"),
            // The gate that runs these in the new world. Naming it here rather
            // than asserting it exists means the ledger reports the truth in
            // both directions: registered => the suites are gated; absent =>
            // the row reads "gate `sky-suites` is NOT in the registry" at
            // strength 0, which is what unrun assertions are worth.
            Some("sky-suites".into()),
            files,
            None,
            true,
        ));
    }

    // --- Layer 1 ------------------------------------------------------------
    let corpus_dir = repo_root.join("rust/crates/xtask/src/corpus");
    let mut rs: Vec<PathBuf> = std::fs::read_dir(&corpus_dir)
        .map_err(|e| format!("cannot read {}: {e}", corpus_dir.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().and_then(|e| e.to_str()) == Some("rs"))
        .collect();
    rs.sort();
    let mut emitted = String::new();
    for p in &rs {
        emitted.push_str(&rust_string_literals(
            &std::fs::read_to_string(p).unwrap_or_default(),
        ));
        emitted.push('\n');
    }
    let mut imports = BTreeSet::new();
    for m in parse_imports(&emitted).modules {
        if surf.modules.contains(&m) {
            imports.insert(m);
        }
    }
    let mut qualified = BTreeSet::new();
    let mut generous = BTreeSet::new();
    refs_of_file(&emitted, surf, &mut qualified, &mut generous);
    units.push(Unit {
        id: "corpus:layer1".into(),
        role: Role::Layer1,
        path_key: "corpus:layer1".into(),
        gate: Some("corpus".into()),
        files: rs.len(),
        imports,
        qualified,
        generous,
        toml_sections: BTreeSet::new(),
        db_drivers: BTreeSet::new(),
        has_assertions: true,
    });

    Ok(units)
}

// -------------------------------------------------------------------- ratchet

/// The `[[weakening]]` stanzas of `docs/coverage/removals.toml`, keyed by
/// surface id. All four fields are REQUIRED; a stanza missing any of them is a
/// hard error, for the same reason the `[[removal]]` parser rejects one — an
/// empty stanza would buy an unaccounted weakening.
fn parse_weakenings(path: &Path) -> Result<BTreeSet<String>, String> {
    let Ok(src) = std::fs::read_to_string(path) else {
        return Ok(BTreeSet::new());
    };
    let required = ["surface", "reason", "owner", "commit"];
    let mut out = BTreeSet::new();
    let mut problems: Vec<String> = Vec::new();
    let mut idx = 0usize;
    let mut open = false;
    let mut fields: BTreeMap<String, String> = BTreeMap::new();

    let close = |idx: usize,
                 fields: &BTreeMap<String, String>,
                 out: &mut BTreeSet<String>,
                 problems: &mut Vec<String>| {
        for r in required {
            if fields.get(r).map(|v| v.trim().is_empty()).unwrap_or(true) {
                problems.push(format!("[[weakening]] #{idx} is missing `{r}`"));
            }
        }
        if let Some(s) = fields.get("surface") {
            if !s.trim().is_empty() {
                out.insert(s.trim().to_string());
            }
        }
    };

    for line in src.lines() {
        let t = line.trim();
        if t.starts_with('#') || t.is_empty() {
            continue;
        }
        if t.starts_with("[[weakening]]") {
            if open {
                close(idx, &fields, &mut out, &mut problems);
            }
            idx += 1;
            open = true;
            fields.clear();
            continue;
        }
        if t.starts_with('[') {
            if open {
                close(idx, &fields, &mut out, &mut problems);
            }
            open = false;
            fields.clear();
            continue;
        }
        if open {
            if let Some((k, v)) = t.split_once('=') {
                fields.insert(k.trim().to_string(), v.trim().trim_matches('"').to_string());
            }
        }
    }
    if open {
        close(idx, &fields, &mut out, &mut problems);
    }
    if problems.is_empty() {
        Ok(out)
    } else {
        Err(problems.join("\n"))
    }
}

fn baseline_surface_strengths(base: &Value) -> BTreeMap<String, u8> {
    let mut out = BTreeMap::new();
    if let Some(rows) = base["surfaces"].as_array() {
        for r in rows {
            if let Some(id) = r["id"].as_str() {
                out.insert(
                    id.to_string(),
                    r["cover_new"]["strength"].as_u64().unwrap_or(0) as u8,
                );
            }
        }
    }
    out
}

/// The weaker surfaces already recorded in the checked-in ledger.
///
/// `None` means there is no checked-in ledger at all — the bootstrap run. See
/// [`ratchet`] for why that one case is treated differently from every later
/// one.
fn baseline_weaker(base: Option<&Value>) -> Option<BTreeSet<String>> {
    let base = base?;
    Some(
        base["surfaces_weaker"]
            .as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|v| v.as_str().map(str::to_string))
                    .collect()
            })
            .unwrap_or_default(),
    )
}

/// THE RATCHET. Returns every violation, not the first — a report that stops
/// at the first failure makes the next run look like new damage.
///
/// # The one deliberate asymmetry, and why it is not a loophole
///
/// A `weaker` surface needs a `[[weakening]]` stanza — EXCEPT when it is
/// already recorded as weaker in the checked-in ledger. Two facts force that
/// carve-out, and neither is a softening:
///
/// 1. **The bootstrap is not signable.** A stanza's `commit` field names "the
///    commit that weakened it". On the run that first creates the ledger there
///    is no such commit: the migration's opening coverage debt was not caused
///    by anyone, it was *discovered*. Demanding thirteen signatures for a state
///    nobody created would be answered by thirteen fabricated signatures, which
///    is strictly worse than an honest, visible debt list.
/// 2. **The debt cannot hide.** The grandfathered set lives in
///    `surfaces_weaker` inside the generated file, is reprinted by every run,
///    and is rendered into `ledger.md`. And it cannot be edited into existence:
///    hand-adding an id to `surfaces_weaker` makes the checked-in file differ
///    from the recomputation, which `--check` reports as STALE.
///
/// What is NOT carved out is laundering. A weakening that is new relative to
/// the checked-in ledger fails in BOTH modes — the write path refuses to record
/// it — so "just re-run the generator" is not a way to accept a weakening
/// without writing a stanza.
fn ratchet(led: &Ledger, base: Option<&Value>, weakenings: &BTreeSet<String>) -> Vec<String> {
    let mut fails: Vec<String> = Vec::new();
    let grandfathered = baseline_weaker(base);

    let unaccounted: Vec<&Surface> = led
        .surfaces
        .iter()
        .filter(|s| s.verdict() == "weaker" && !weakenings.contains(&s.id))
        .filter(|s| match &grandfathered {
            // Bootstrap: no checked-in ledger exists, so nothing is "new".
            None => false,
            Some(prev) => !prev.contains(&s.id),
        })
        .collect();
    if !unaccounted.is_empty() {
        let listed: Vec<String> = unaccounted
            .iter()
            .map(|s| {
                format!(
                    "  {} : cover_today={} -> cover_new={}",
                    s.id,
                    s.today_max().label(),
                    s.new_max().label()
                )
            })
            .collect();
        fails.push(format!(
            "UNACCOUNTED WEAKENING — {} surface(s) became weaker than the checked-in ledger \
             records, with no [[weakening]] stanza in docs/coverage/removals.toml:\n{}\n\
             Each needs a stanza with surface / reason / owner / commit, or the coverage \
             restored. A weakening that lands silently is a coverage removal nobody signed.",
            unaccounted.len(),
            listed.join("\n")
        ));
    }

    let Some(base) = base else {
        return fails;
    };

    let covered_now = led.doc["summary"]["surfaces_covered"].as_u64().unwrap_or(0);
    let covered_before = base["summary"]["surfaces_covered"].as_u64().unwrap_or(0);
    if covered_now < covered_before {
        fails.push(format!(
            "COVERED SURFACES FELL — summary.surfaces_covered {covered_before} -> {covered_now}. \
             Surfaces at strength >= Asserted may only grow; a fall means the new corpus stopped \
             asserting something it used to assert."
        ));
    }

    let before = baseline_surface_strengths(base);
    let mut dropped: Vec<String> = Vec::new();
    for s in &led.surfaces {
        let now = s.new_max() as u8;
        if let Some(was) = before.get(&s.id) {
            if now < *was {
                dropped.push(format!(
                    "  {} : cover_new {} -> {}",
                    s.id,
                    Strength::from_u8(*was).label(),
                    Strength::from_u8(now).label()
                ));
            }
        }
    }
    for (id, was) in &before {
        if !led.surfaces.iter().any(|s| &s.id == id) {
            dropped.push(format!(
                "  {id} : cover_new {} -> (surface disappeared)",
                Strength::from_u8(*was).label()
            ));
        }
    }
    if !dropped.is_empty() {
        fails.push(format!(
            "COVER_NEW REGRESSED — {} surface(s) lost strength versus the checked-in ledger:\n{}",
            dropped.len(),
            dropped.join("\n")
        ));
    }
    fails
}

// --------------------------------------------------------------------- report

fn print_report(led: &Ledger) {
    let d = &led.doc;
    let g = |p: &[&str]| -> i64 {
        let mut cur = d;
        for k in p {
            cur = &cur[*k];
        }
        cur.as_i64().unwrap_or(-1)
    };
    println!("xtask coverage-ledger — docs/ci-test-architecture-v2.md §9.2");
    println!("============================================================");
    println!("\nSURFACES");
    println!("  total ............................... {}", g(&["summary", "surfaces_total"]));
    println!(
        "  covered by the new corpus (>= Asserted)  {}  ({:.1}%)",
        g(&["summary", "surfaces_covered"]),
        d["summary"]["surfaces_covered_pct"].as_f64().unwrap_or(0.0)
    );
    println!("  stronger / equal / weaker ........... {} / {} / {}",
        g(&["summary", "surfaces_stronger"]),
        g(&["summary", "surfaces_equal"]),
        g(&["summary", "surfaces_weaker"]));

    let mut by_cat: BTreeMap<&str, (usize, usize)> = BTreeMap::new();
    for s in &led.surfaces {
        let e = by_cat.entry(s.category.as_str()).or_default();
        e.0 += 1;
        if s.new_max() >= Strength::Asserted {
            e.1 += 1;
        }
    }
    println!("\n  by category (covered / total)");
    for (cat, (t, c)) in &by_cat {
        println!("    {cat:<16} {c:>4} / {t}");
    }

    // Reprinted on EVERY run, not only when it changes: the weaker set is the
    // migration's open coverage debt, and a debt that is only mentioned once —
    // on the run that discovered it — is a debt nobody pays.
    let weak: Vec<&Surface> = led
        .surfaces
        .iter()
        .filter(|s| s.verdict() == "weaker")
        .collect();
    if !weak.is_empty() {
        println!(
            "\nOPEN WEAKENING DEBT — {} surface(s) the new corpus covers MORE WEAKLY than the old \
             one.\n  Each needs either a registered gate that restores the strength, or a \
             [[weakening]]\n  stanza in docs/coverage/removals.toml (surface / reason / owner / \
             commit).",
            weak.len()
        );
        for s in &weak {
            println!(
                "    {:<40} {} -> {}",
                s.id,
                s.today_max().label(),
                s.new_max().label()
            );
        }
    }

    println!("\nUNITS  (consumers of the surface)");
    if let Some(map) = d["summary"]["units_by_role"].as_object() {
        for (role, n) in map {
            println!("  {role:<14} {}", n.as_i64().unwrap_or(0));
        }
    }

    println!("\nSOLE OWNERSHIP  (generated; §9.2 requires it be mechanical)");
    let count = |p: &[&str]| -> usize {
        let mut cur = d;
        for k in p {
            cur = &cur[*k];
        }
        cur.as_object().map(|o| o.len()).unwrap_or(0)
    };
    println!(
        "  stdlib modules owned by exactly ONE example .......... {}",
        count(&["sole_ownership", "stdlib_modules_examples_only"])
    );
    println!(
        "  stdlib modules owned by exactly ONE unit (any role) .. {}",
        count(&["sole_ownership", "stdlib_modules_repo_wide"])
    );
    println!(
        "  sky.toml sections owned by exactly ONE unit .......... {}",
        count(&["sole_ownership", "config_sections"])
    );
    println!(
        "  LOST IF examples/ RETIRED — modules {} · config sections {}",
        count(&["sole_ownership", "lost_if_examples_retired", "stdlib_modules"]),
        count(&["sole_ownership", "lost_if_examples_retired", "config_sections"])
    );

    println!("\nUNCOVERED  (denominators from docs/coverage/denominators.json)");
    println!(
        "  stdlib modules imported by NOTHING .......... {} of {}  ({:.1}%)",
        g(&["uncovered", "modules_imported_by_nothing", "count"]),
        g(&["summary", "stdlib_modules"]),
        d["uncovered"]["modules_imported_by_nothing"]["pct_of_denominator"]
            .as_f64()
            .unwrap_or(0.0)
    );
    println!(
        "  imported ONLY by a root tests/ suite ........ {}",
        g(&["uncovered", "modules_imported_only_by_root_test_suites", "count"])
    );
    println!(
        "  symbols with ZERO qualified refs (STRICT) ... {} of {}  ({:.1}%)",
        g(&["uncovered", "symbols_unreferenced_strict", "count"]),
        g(&["summary", "stdlib_entries"]),
        d["uncovered"]["symbols_unreferenced_strict"]["pct_of_denominator"]
            .as_f64()
            .unwrap_or(0.0)
    );
    println!(
        "  symbols unreferenced under the GENEROUS rule  {} of {}  ({:.1}%)",
        g(&["uncovered", "symbols_unreferenced_generous", "count"]),
        g(&["summary", "stdlib_entries"]),
        d["uncovered"]["symbols_unreferenced_generous"]["pct_of_denominator"]
            .as_f64()
            .unwrap_or(0.0)
    );
    println!(
        "  surfaces with ZERO new cover ................ {}",
        g(&["uncovered", "surfaces_with_zero_new_cover", "count"])
    );
    if let Some(list) = d["uncovered"]["surfaces_with_zero_new_cover"]["surfaces"].as_array() {
        for s in list {
            println!("      {}", s.as_str().unwrap_or("?"));
        }
    }
}

fn render_markdown(led: &Ledger) -> String {
    let d = &led.doc;
    let mut s = String::new();
    s.push_str("# Coverage ledger\n\n");
    s.push_str(
        "> **GENERATED by `xtask coverage-ledger` — do not hand-edit.**\n\
         > Regenerate with `cargo run -q -p xtask -- coverage-ledger`; \
         `--check` is the CI form and writes nothing.\n\
         > The machine-readable canonical form is \
         [`ledger.json`](ledger.json); this page is a view of it.\n\n",
    );
    s.push_str(
        "Every number below was measured from the tree. The stdlib denominator is \
         cross-checked against `docs/coverage/denominators.json`; a disagreement fails the \
         gate rather than being averaged away.\n\n",
    );

    s.push_str("## Strength classes\n\n| n | class | meaning |\n|---|---|---|\n");
    for (n, label, meaning) in [
        (0, "None", "nothing covers it"),
        (1, "Builds", "something compiles it, nothing runs it"),
        (2, "Runs", "something builds AND runs it; verdict = exit status only"),
        (3, "Asserted", "explicit counted assertions"),
        (
            4,
            "Falsified",
            "assertions in a REGISTERED gate whose falsifying mutation is recorded PROVEN",
        ),
    ] {
        s.push_str(&format!("| {n} | `{label}` | {meaning} |\n"));
    }

    s.push_str("\n## Summary\n\n| metric | value |\n|---|---|\n");
    for (k, label) in [
        ("surfaces_total", "surfaces"),
        ("surfaces_covered", "covered by the new corpus (>= Asserted)"),
        ("surfaces_stronger", "verdict `stronger`"),
        ("surfaces_equal", "verdict `equal`"),
        ("surfaces_weaker", "verdict `weaker`"),
        ("units_total", "corpus units"),
        ("stdlib_modules", "stdlib modules (denominator)"),
        ("stdlib_entries", "stdlib entries (denominator)"),
    ] {
        s.push_str(&format!(
            "| {label} | {} |\n",
            d["summary"][k].as_i64().unwrap_or(-1)
        ));
    }

    s.push_str("\n## Uncovered\n\n| metric | count | % of denominator |\n|---|---|---|\n");
    for (path, label) in [
        (
            "modules_imported_by_nothing",
            "stdlib modules imported by nothing",
        ),
        (
            "symbols_unreferenced_strict",
            "symbols with zero qualified references (STRICT — the number any uncovered claim uses)",
        ),
        (
            "symbols_unreferenced_generous",
            "symbols unreferenced under the generous rule",
        ),
    ] {
        s.push_str(&format!(
            "| {label} | {} | {:.1}% |\n",
            d["uncovered"][path]["count"].as_i64().unwrap_or(-1),
            d["uncovered"][path]["pct_of_denominator"]
                .as_f64()
                .unwrap_or(0.0)
        ));
    }
    s.push_str(&format!(
        "| stdlib modules imported ONLY by a root `tests/` suite (no application builds them) | {} | — |\n",
        d["uncovered"]["modules_imported_only_by_root_test_suites"]["count"]
            .as_i64()
            .unwrap_or(-1)
    ));

    s.push_str("\n### Surfaces with zero new cover\n\n");
    match d["uncovered"]["surfaces_with_zero_new_cover"]["surfaces"].as_array() {
        Some(list) if !list.is_empty() => {
            for v in list {
                s.push_str(&format!("- `{}`\n", v.as_str().unwrap_or("?")));
            }
        }
        _ => s.push_str("None.\n"),
    }

    s.push_str("\n## Sole ownership\n\n");
    s.push_str(
        "Computed over distinct paths, not member rows: `apps/manifest.toml` backs two members \
         with `apps/ledger` and backs member D with `examples/13-skyshop`, and counting rows \
         would make one directory look like two independent owners.\n\n",
    );
    let obj_len = |p: &[&str]| -> usize {
        let mut cur = d;
        for k in p {
            cur = &cur[*k];
        }
        cur.as_object().map(|o| o.len()).unwrap_or(0)
    };
    s.push_str("| table | entries |\n|---|---|\n");
    s.push_str(&format!(
        "| stdlib modules owned by exactly one `examples/*` | {} |\n",
        obj_len(&["sole_ownership", "stdlib_modules_examples_only"])
    ));
    s.push_str(&format!(
        "| stdlib modules owned by exactly one unit of any role | {} |\n",
        obj_len(&["sole_ownership", "stdlib_modules_repo_wide"])
    ));
    s.push_str(&format!(
        "| sky.toml sections owned by exactly one unit | {} |\n",
        obj_len(&["sole_ownership", "config_sections"])
    ));
    s.push_str(&format!(
        "| **lost if `examples/` retired** — modules | **{}** |\n",
        obj_len(&["sole_ownership", "lost_if_examples_retired", "stdlib_modules"])
    ));
    s.push_str(&format!(
        "| **lost if `examples/` retired** — config sections | **{}** |\n",
        obj_len(&["sole_ownership", "lost_if_examples_retired", "config_sections"])
    ));

    if let Some(map) = d["sole_ownership"]["lost_if_examples_retired"]["stdlib_modules"].as_object()
    {
        if !map.is_empty() {
            s.push_str("\n### Modules lost if `examples/` is retired\n\n| module | sole owner |\n|---|---|\n");
            for (m, owner) in map {
                s.push_str(&format!("| `{m}` | `{}` |\n", owner.as_str().unwrap_or("?")));
            }
        }
    }
    if let Some(map) =
        d["sole_ownership"]["lost_if_examples_retired"]["config_sections"].as_object()
    {
        if !map.is_empty() {
            s.push_str("\n### Config sections lost if `examples/` is retired\n\n| section | sole owner |\n|---|---|\n");
            for (sec, owner) in map {
                s.push_str(&format!(
                    "| `{sec}` | `{}` |\n",
                    owner.as_str().unwrap_or("?")
                ));
            }
        }
    }

    s.push_str("\n## Weaker surfaces — the open coverage debt\n\n");
    s.push_str(
        "A `weaker` verdict is a coverage removal. §9.2 requires it be written down before it \
         lands: a `[[weakening]]` stanza in `docs/coverage/removals.toml` with `surface`, \
         `reason`, `owner`, `commit`.\n\n\
         The rows below are the state the ledger was bootstrapped on — the migration's opening \
         debt, not something anyone signed. Each is closed either by registering a gate that \
         restores the strength, or by a stanza. Any surface that becomes weaker *after* this \
         list was recorded fails `xtask coverage-ledger` in both modes.\n\n",
    );
    let weak: Vec<&Surface> = led
        .surfaces
        .iter()
        .filter(|x| x.verdict() == "weaker")
        .collect();
    if weak.is_empty() {
        s.push_str("None.\n");
    } else {
        s.push_str("| surface | cover_today | cover_new |\n|---|---|---|\n");
        for x in weak {
            s.push_str(&format!(
                "| `{}` | {} | {} |\n",
                x.id,
                x.today_max().label(),
                x.new_max().label()
            ));
        }
    }

    s.push_str("\n## Surfaces\n\n| surface | category | today | new | verdict |\n|---|---|---|---|---|\n");
    for x in &led.surfaces {
        s.push_str(&format!(
            "| `{}` | {} | {} | {} | {} |\n",
            x.id,
            x.category,
            x.today_max().label(),
            x.new_max().label(),
            x.verdict()
        ));
    }

    s.push_str("\n## Gates and the surfaces they declare\n\n");
    s.push_str("| gate | tier | falsifier | surfaces |\n|---|---|---|---|\n");
    if let Some(map) = d["gates"].as_object() {
        for (name, g) in map {
            let ids: Vec<String> = g["surfaces"]
                .as_array()
                .map(|a| {
                    a.iter()
                        .map(|v| format!("`{}`", v.as_str().unwrap_or("?")))
                        .collect()
                })
                .unwrap_or_default();
            s.push_str(&format!(
                "| `{name}` | {} | {} | {} |\n",
                g["tier"].as_str().unwrap_or("?"),
                if g["falsifier_proven"].as_bool().unwrap_or(false) {
                    "PROVEN"
                } else {
                    "not proven"
                },
                if ids.is_empty() {
                    "— (none declared)".to_string()
                } else {
                    ids.join(" · ")
                }
            ));
        }
    }
    s
}

// ------------------------------------------------------------------ entry pts

pub fn run(args: &[String], repo_root: &Path) -> i32 {
    let check_only = args.iter().any(|a| a == "--check");

    let led = match compute(repo_root) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("xtask coverage-ledger: FAILED to compute the ledger\n{e}");
            return 1;
        }
    };
    print_report(&led);

    let removals_path = repo_root.join("docs/coverage/removals.toml");
    let weakenings = match parse_weakenings(&removals_path) {
        Ok(w) => w,
        Err(e) => {
            eprintln!(
                "\nxtask coverage-ledger: {} is malformed\n{e}",
                removals_path.display()
            );
            return 1;
        }
    };

    let json_path = repo_root.join("docs/coverage/ledger.json");
    let md_path = repo_root.join("docs/coverage/ledger.md");
    let baseline: Option<Value> = std::fs::read_to_string(&json_path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok());

    let mut fails = ratchet(&led, baseline.as_ref(), &weakenings);

    let text = format!("{}\n", serde_json::to_string_pretty(&led.doc).unwrap());
    if check_only {
        match &baseline {
            None => fails.push(format!(
                "STALE — {} does not exist. Run `xtask coverage-ledger`.",
                json_path.display()
            )),
            Some(base) => {
                if base != &led.doc {
                    fails.push(format!(
                        "STALE — {} does not match the recomputed ledger.\n{}\n\
                         Run `xtask coverage-ledger` and commit the result. A checked-in ledger \
                         that no longer describes the tree is worse than none: it is a coverage \
                         claim about a repository that no longer exists.",
                        json_path.display(),
                        stale_diff(base, &led.doc)
                    ));
                }
            }
        }
    }

    if !fails.is_empty() {
        eprintln!();
        for f in &fails {
            eprintln!("{f}\n");
        }
        eprintln!(
            "xtask coverage-ledger{}: FAIL — {} violation(s).",
            if check_only { " --check" } else { "" },
            fails.len()
        );
        return 1;
    }

    if check_only {
        println!("\nxtask coverage-ledger --check: PASS — the checked-in ledger is current and the ratchet holds.");
        return 0;
    }

    if let Some(parent) = json_path.parent() {
        if let Err(e) = std::fs::create_dir_all(parent) {
            eprintln!("xtask coverage-ledger: cannot create {}: {e}", parent.display());
            return 1;
        }
    }
    if let Err(e) = std::fs::write(&json_path, text) {
        eprintln!("xtask coverage-ledger: cannot write {}: {e}", json_path.display());
        return 1;
    }
    if let Err(e) = std::fs::write(&md_path, render_markdown(&led)) {
        eprintln!("xtask coverage-ledger: cannot write {}: {e}", md_path.display());
        return 1;
    }
    println!(
        "\nxtask coverage-ledger: wrote {} and {}",
        json_path.display(),
        md_path.display()
    );
    0
}

/// A short, actionable description of HOW the checked-in ledger differs.
fn stale_diff(base: &Value, cur: &Value) -> String {
    let mut lines: Vec<String> = Vec::new();
    for k in [
        "surfaces_total",
        "surfaces_covered",
        "surfaces_stronger",
        "surfaces_equal",
        "surfaces_weaker",
        "units_total",
        "stdlib_modules",
        "stdlib_entries",
    ] {
        let (b, c) = (
            base["summary"][k].as_i64().unwrap_or(-1),
            cur["summary"][k].as_i64().unwrap_or(-1),
        );
        if b != c {
            lines.push(format!("  summary.{k}: {b} -> {c}"));
        }
    }
    let (bs, cs) = (baseline_surface_strengths(base), baseline_surface_strengths(cur));
    for (id, c) in &cs {
        match bs.get(id) {
            Some(b) if b != c => lines.push(format!(
                "  {id}: cover_new {} -> {}",
                Strength::from_u8(*b).label(),
                Strength::from_u8(*c).label()
            )),
            None => lines.push(format!(
                "  {id}: (new surface) -> {}",
                Strength::from_u8(*c).label()
            )),
            _ => {}
        }
    }
    for id in bs.keys() {
        if !cs.contains_key(id) {
            lines.push(format!("  {id}: (surface disappeared)"));
        }
    }
    if lines.is_empty() {
        "  (no surface or summary change; a detail field differs — evidence, sole-ownership \
         or uncovered lists)"
            .to_string()
    } else {
        lines.join("\n")
    }
}

/// Harness-gate face. Runs the same computation and the same ratchet as
/// `--check`, writes nothing, and reports the number of REAL checks performed:
/// one per surface row verified, plus the four ratchet checks (staleness,
/// `surfaces_covered` non-decrease, per-surface `cover_new` non-decrease,
/// unaccounted weakenings). Never 0 — a zero here would be read as vacuous,
/// which is exactly what it must not be able to claim.
// Callable but not yet called: the harness gate that consumes it is registered
// separately (registry.rs is owned elsewhere). The allow goes away the moment
// that row lands; it is here so an unregistered-yet face does not warn.
#[allow(dead_code)]
pub fn check_body(repo_root: &Path) -> (bool, u64, String) {
    let led = match compute(repo_root) {
        Ok(l) => l,
        Err(e) => return (false, 1, format!("compute FAILED: {e}")),
    };
    let weakenings = match parse_weakenings(&repo_root.join("docs/coverage/removals.toml")) {
        Ok(w) => w,
        Err(e) => return (false, 1, format!("removals.toml malformed: {e}")),
    };
    let json_path = repo_root.join("docs/coverage/ledger.json");
    let baseline: Option<Value> = std::fs::read_to_string(&json_path)
        .ok()
        .and_then(|s| serde_json::from_str(&s).ok());

    let mut fails = ratchet(&led, baseline.as_ref(), &weakenings);
    match &baseline {
        None => fails.push(format!("{} does not exist", json_path.display())),
        Some(base) => {
            if base != &led.doc {
                fails.push(format!(
                    "{} is STALE:\n{}",
                    json_path.display(),
                    stale_diff(base, &led.doc)
                ));
            }
        }
    }

    let assertions = led.surfaces.len() as u64 + 4;
    if fails.is_empty() {
        (
            true,
            assertions,
            format!(
                "{} surfaces verified; {} covered (>= Asserted); ratchet holds",
                led.surfaces.len(),
                led.doc["summary"]["surfaces_covered"].as_u64().unwrap_or(0)
            ),
        )
    } else {
        (false, assertions, fails.join("\n"))
    }
}

// ----------------------------------------------------------------------- tests

#[cfg(test)]
mod tests {
    use super::*;

    fn repo_root() -> PathBuf {
        // crates/xtask -> crates -> rust -> repo root
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .parent()
            .unwrap()
            .parent()
            .unwrap()
            .parent()
            .unwrap()
            .to_path_buf()
    }

    /// ANTI-DRIFT #1. A gate that enters the registry without declaring what it
    /// covers would grow the harness while the ledger silently kept reporting
    /// the old surface set — the gate would exist, and its coverage claim would
    /// not. Making that a compile-and-test failure is the only way the ledger
    /// stays a description of the registry rather than a snapshot of it.
    #[test]
    fn every_registered_gate_declares_its_surfaces() {
        let declared: BTreeSet<&str> = GATE_SURFACES.iter().map(|(n, _)| *n).collect();
        let missing: Vec<&str> = GATES
            .iter()
            .filter(|g| g.tier != Tier::SelfTest)
            .map(|g| g.name)
            .filter(|n| !declared.contains(n))
            .collect();
        assert!(
            missing.is_empty(),
            "these registered gates declare no surfaces in GATE_SURFACES: {missing:?}\n\
             Add a row to coverage_ledger.rs naming the surfaces each one covers. A gate whose \
             coverage is undeclared cannot appear in the ledger, so adding it would look like \
             free coverage."
        );
    }

    /// ANTI-DRIFT #1b. The same obligation for CI steps that are not harness
    /// gates: a subcommand that CI starts invoking, with no `CI_SURFACES` row,
    /// would run on every push and contribute nothing to the ledger — the
    /// coverage would exist and be invisible, which is how a surface comes to
    /// read `weaker` while a gate for it is running.
    #[test]
    fn every_ci_invoked_xtask_subcommand_declares_its_surfaces() {
        let root = repo_root();
        let (refs, unresolved) =
            crate::ci_scan::scan_xtask_refs(&root, &workflow_roots(&root));
        assert!(unresolved.is_empty(), "{unresolved:?}");
        assert!(
            !refs.is_empty(),
            "found no xtask invocations in .github/workflows — the extractor is broken"
        );
        let declared: BTreeSet<&str> = CI_SURFACES.iter().map(|(k, _)| *k).collect();
        let mut missing: Vec<String> = refs
            .iter()
            .map(|r| format!("xtask:{}", r.gate))
            .filter(|k| !declared.contains(k.as_str()))
            .collect();
        missing.sort();
        missing.dedup();
        assert!(
            missing.is_empty(),
            "these xtask subcommands are invoked by .github/workflows but declare no \
             surfaces in CI_SURFACES: {missing:?}\n\
             Add a row (use an empty surface list if it genuinely covers no product surface, \
             e.g. accounting or cache hygiene)."
        );
    }

    /// ANTI-DRIFT #2. A typo'd or retired surface id in `GATE_SURFACES` would
    /// attach a gate's coverage to nothing, and the surface it meant to cover
    /// would silently read as uncovered — or, worse, a renamed surface would
    /// keep a stale claim alive.
    #[test]
    fn every_declared_surface_id_exists() {
        let cross: BTreeSet<&str> = CROSS_CUTTING.iter().map(|c| c.id).collect();
        let mut bad: Vec<String> = Vec::new();
        for (owner, ids) in GATE_SURFACES.iter().chain(CI_SURFACES.iter()) {
            for id in *ids {
                let generated = id.starts_with("stdlib.")
                    || id.starts_with("cli.")
                    || id.starts_with("config.");
                if !cross.contains(id) && !generated {
                    bad.push(format!("`{owner}` claims unknown surface `{id}`"));
                }
            }
        }
        assert!(
            bad.is_empty(),
            "{}\nA surface id must exist in CROSS_CUTTING or live in a generated namespace \
             (stdlib.* / cli.* / config.*).",
            bad.join("\n")
        );
    }

    /// The CLI verb list is DERIVED. This asserts the derivation still works
    /// against the real tree — an empty or truncated list would make the ledger
    /// report full CLI coverage by having no CLI rows to be uncovered.
    #[test]
    fn cli_verbs_are_derived_and_complete() {
        let (verbs, _undocumented, _undispatched) = derive_cli_verbs(&repo_root()).unwrap();
        assert!(!verbs.is_empty(), "derived no CLI verbs");
        for expected in ["build", "check", "run", "test", "fmt", "doc", "db", "init"] {
            assert!(
                verbs.iter().any(|v| v == expected),
                "derived CLI verb list is missing `{expected}`: {verbs:?}"
            );
        }
    }

    /// Every cross-cutting surface must carry grounded `today` evidence — an
    /// empty list is indistinguishable from "we did not look", and would read
    /// as strength 0 (nothing covered it), which is a claim, not an absence.
    #[test]
    fn every_cross_surface_declares_its_prior_evidence() {
        for c in CROSS_CUTTING {
            assert!(
                !c.today.is_empty(),
                "cross-cutting surface `{}` declares no `today` evidence; use \
                 (\"nothing\", 0) if nothing covered it",
                c.id
            );
            for (by, s) in c.today {
                assert!(!by.is_empty(), "`{}` has an empty evidence string", c.id);
                assert!(*s <= 4, "`{}` has strength {s} > 4", c.id);
            }
        }
    }

    /// A BLOCKED gate counts as UNCOVERED. `GateState::counts_as_cover()` is
    /// false for `Blocked`, and the ledger must agree — a block that still
    /// scored as coverage would be a skip with better paperwork, which is the
    /// exact thing the state was introduced to make inexpressible.
    #[test]
    fn a_blocked_gate_contributes_no_cover() {
        use crate::harness::registry::BLOCKED;
        let proofs: BTreeMap<String, bool> = BLOCKED
            .iter()
            .map(|b| (b.gate.to_string(), true))
            .collect();
        assert!(
            !BLOCKED.is_empty(),
            "the BLOCKED table is empty — this test would assert nothing"
        );
        for b in BLOCKED {
            // Even with a PROVEN falsifier recorded, a blocked gate is 0.
            assert_eq!(
                gate_strength(b.gate, &proofs),
                Strength::None,
                "BLOCKED gate `{}` contributed cover",
                b.gate
            );
        }
        // Control: an unblocked gate with a proven falsifier is still 4, so the
        // assertion above is about BLOCKED and not about the whole function.
        let unblocked = GATES
            .iter()
            .map(|g| g.name)
            .find(|n| crate::harness::registry::block_for(n).is_none())
            .expect("some gate is unblocked");
        let proven: BTreeMap<String, bool> =
            [(unblocked.to_string(), true)].into_iter().collect();
        assert_eq!(gate_strength(unblocked, &proven), Strength::Falsified);
    }

    /// The script scanner must see every invocation form CI actually uses.
    /// `../scripts/doc-examples.sh` (run from `rust/`) was missed by a
    /// prefix-anchored match, which scored a CI-wired gate as absent and
    /// produced a false `weaker` verdict for `docs.examples-gate`.
    #[test]
    fn ci_script_scan_sees_every_invocation_form() {
        let root = repo_root();
        let refs = crate::ci_scan::scan_script_refs(&root, &workflow_roots(&root));
        let found: BTreeSet<&str> = refs.iter().map(|r| r.gate.as_str()).collect();
        for expect in [
            "scripts/conformance.sh",
            "scripts/doc-examples.sh",
            "scripts/example-sweep.sh",
        ] {
            assert!(
                found.contains(expect),
                "the script scanner missed `{expect}`, which .github/workflows invokes. \
                 Found: {found:?}"
            );
        }
    }

    #[test]
    fn cross_surface_ids_are_unique() {
        let mut seen = BTreeSet::new();
        for c in CROSS_CUTTING {
            assert!(seen.insert(c.id), "duplicate surface id `{}`", c.id);
        }
    }

    #[test]
    fn strength_ordering_is_the_comparison_currency() {
        assert!(Strength::None < Strength::Builds);
        assert!(Strength::Builds < Strength::Runs);
        assert!(Strength::Runs < Strength::Asserted);
        assert!(Strength::Asserted < Strength::Falsified);
        assert_eq!(max_strength(&[]), Strength::None);
        assert_eq!(
            max_strength(&[Ev::new("a", Strength::Builds), Ev::new("b", Strength::Asserted)]),
            Strength::Asserted
        );
    }

    #[test]
    fn imports_parse_every_documented_form() {
        let fi = parse_imports(
            "module M exposing (..)\n\
             import Std.Log\n\
             import Sky.Core.List as L\n\
             import Std.Db exposing (Store, query)\n\
             import Std.Ui exposing (..)\n",
        );
        assert_eq!(fi.alias.get("L").map(String::as_str), Some("Sky.Core.List"));
        assert_eq!(
            fi.alias.get("Std.Log").map(String::as_str),
            Some("Std.Log")
        );
        assert!(fi.exposed["Std.Db"].contains("query"));
        assert!(fi.exposing_all.contains("Std.Ui"));
        // Prelude is auto-imported; a file that never names it still uses it.
        assert!(fi.exposing_all.contains("Sky.Core.Prelude"));
    }

    #[test]
    fn multiline_exposing_lists_are_not_truncated() {
        let fi = parse_imports("import Std.Db exposing\n    ( Store\n    , query\n    )\n");
        assert!(fi.exposed["Std.Db"].contains("Store"));
        assert!(fi.exposed["Std.Db"].contains("query"));
    }

    #[test]
    fn comments_and_strings_do_not_create_references() {
        let src = "-- Std.Log.println is documented here\n\
                   x = \"Std.Log.println\"\n\
                   {- Std.Log.println -}\n";
        let clean = strip_noise(src);
        assert!(!clean.contains("println"), "{clean}");
    }

    #[test]
    fn strict_requires_a_qualified_token_and_generous_does_not() {
        let surf = Surfaces {
            modules: ["Std.Log".to_string()].into_iter().collect(),
            symbols: [("Std.Log".to_string(), "println".to_string())]
                .into_iter()
                .collect(),
            by_name: [(
                "println".to_string(),
                ["Std.Log".to_string()].into_iter().collect(),
            )]
            .into_iter()
            .collect(),
        };

        let mut q = BTreeSet::new();
        let mut g = BTreeSet::new();
        refs_of_file("import Std.Log\nmain = Std.Log.println \"hi\"\n", &surf, &mut q, &mut g);
        assert_eq!(q.len(), 1, "qualified token must count as STRICT");
        assert_eq!(g.len(), 1);

        let mut q2 = BTreeSet::new();
        let mut g2 = BTreeSet::new();
        refs_of_file(
            "import Std.Log exposing (println)\nmain = println \"hi\"\n",
            &surf,
            &mut q2,
            &mut g2,
        );
        assert!(q2.is_empty(), "a bare token is NOT a strict reference");
        assert_eq!(g2.len(), 1, "a bare exposed token IS a generous reference");

        // An alias resolves for the strict rule too.
        let mut q3 = BTreeSet::new();
        let mut g3 = BTreeSet::new();
        refs_of_file("import Std.Log as L\nmain = L.println \"hi\"\n", &surf, &mut q3, &mut g3);
        assert_eq!(q3.len(), 1);
    }

    #[test]
    fn weakening_stanza_must_carry_all_four_fields() {
        let dir = std::env::temp_dir().join(format!("sky-ledger-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("removals.toml");

        std::fs::write(&p, "# nothing here\n").unwrap();
        assert!(parse_weakenings(&p).unwrap().is_empty());

        std::fs::write(
            &p,
            "[[weakening]]\nsurface = \"stdlib.Std.Csv\"\nreason = \"r\"\nowner = \"o\"\ncommit = \"c\"\n",
        )
        .unwrap();
        assert!(parse_weakenings(&p).unwrap().contains("stdlib.Std.Csv"));

        std::fs::write(&p, "[[weakening]]\nsurface = \"stdlib.Std.Csv\"\n").unwrap();
        assert!(parse_weakenings(&p).unwrap_err().contains("missing `reason`"));

        std::fs::write(
            &p,
            "[[weakening]]\nsurface = \"x\"\nreason = \"\"\nowner = \"o\"\ncommit = \"c\"\n",
        )
        .unwrap();
        assert!(parse_weakenings(&p).unwrap_err().contains("missing `reason`"));

        // A [[removal]] stanza is NOT a weakening and must not be counted as one.
        std::fs::write(
            &p,
            "[[removal]]\nsymbol = \"Std.A.b\"\nreason = \"r\"\nowner = \"o\"\ncommit = \"c\"\n",
        )
        .unwrap();
        assert!(parse_weakenings(&p).unwrap().is_empty());

        let _ = std::fs::remove_dir_all(&dir);
    }

    fn weaker_ledger() -> Ledger {
        Ledger {
            surfaces: vec![Surface {
                id: "db.postgres".into(),
                category: "db".into(),
                description: "d".into(),
                today: vec![Ev::new("old", Strength::Asserted)],
                new: vec![Ev::new("new", Strength::Runs)],
            }],
            doc: json!({ "summary": { "surfaces_covered": 0 } }),
        }
    }

    /// THE RATCHET'S RED, at the unit level: a NEW weakening with no stanza
    /// must fail, and the same surface with a stanza must pass.
    #[test]
    fn unaccounted_weakening_fails_and_a_stanza_clears_it() {
        let led = weaker_ledger();
        let clean_base = json!({
            "summary": { "surfaces_covered": 0 },
            "surfaces": [],
            "surfaces_weaker": []
        });
        let none = BTreeSet::new();
        let fails = ratchet(&led, Some(&clean_base), &none);
        assert_eq!(fails.len(), 1);
        assert!(fails[0].contains("db.postgres"), "{}", fails[0]);

        let accounted: BTreeSet<String> = ["db.postgres".to_string()].into_iter().collect();
        assert!(ratchet(&led, Some(&clean_base), &accounted).is_empty());
    }

    /// The bootstrap carve-out: with NO checked-in ledger there is no commit to
    /// name in a stanza, so the opening debt is recorded rather than signed.
    #[test]
    fn the_bootstrap_run_records_opening_debt_instead_of_demanding_signatures() {
        assert!(ratchet(&weaker_ledger(), None, &BTreeSet::new()).is_empty());
    }

    /// ...and that carve-out is not a laundering route: once a ledger exists,
    /// a weakening it does not already record fails in BOTH modes, so re-running
    /// the generator cannot accept one. Only a surface the baseline ALREADY
    /// records as weaker is carried forward.
    #[test]
    fn a_recorded_weakening_carries_forward_but_a_new_one_does_not() {
        let led = weaker_ledger();
        let base_records_it = json!({
            "summary": { "surfaces_covered": 0 },
            "surfaces": [],
            "surfaces_weaker": ["db.postgres"]
        });
        assert!(ratchet(&led, Some(&base_records_it), &BTreeSet::new()).is_empty());

        let base_records_another = json!({
            "summary": { "surfaces_covered": 0 },
            "surfaces": [],
            "surfaces_weaker": ["ui.webview"]
        });
        let fails = ratchet(&led, Some(&base_records_another), &BTreeSet::new());
        assert_eq!(fails.len(), 1);
        assert!(fails[0].contains("db.postgres"), "{}", fails[0]);
    }

    #[test]
    fn a_fall_in_covered_surfaces_fails_and_a_rise_does_not() {
        let led = Ledger {
            surfaces: Vec::new(),
            doc: json!({ "summary": { "surfaces_covered": 40 } }),
        };
        let none = BTreeSet::new();
        let base_higher = json!({ "summary": { "surfaces_covered": 41 }, "surfaces": [] });
        let fails = ratchet(&led, Some(&base_higher), &none);
        assert_eq!(fails.len(), 1);
        assert!(fails[0].contains("COVERED SURFACES FELL"), "{}", fails[0]);

        let base_lower = json!({ "summary": { "surfaces_covered": 39 }, "surfaces": [] });
        assert!(ratchet(&led, Some(&base_lower), &none).is_empty());
    }

    #[test]
    fn a_per_surface_cover_new_drop_fails() {
        let led = Ledger {
            surfaces: vec![Surface {
                id: "lsp".into(),
                category: "tooling".into(),
                description: "d".into(),
                today: vec![Ev::new("old", Strength::None)],
                new: vec![Ev::new("new", Strength::Asserted)],
            }],
            doc: json!({ "summary": { "surfaces_covered": 1 } }),
        };
        let none = BTreeSet::new();
        let base = json!({
            "summary": { "surfaces_covered": 1 },
            "surfaces": [ { "id": "lsp", "cover_new": { "strength": 4 } } ]
        });
        let fails = ratchet(&led, Some(&base), &none);
        assert_eq!(fails.len(), 1);
        assert!(fails[0].contains("COVER_NEW REGRESSED"), "{}", fails[0]);
        assert!(fails[0].contains("Falsified -> Asserted"), "{}", fails[0]);
    }

    /// A surface that vanishes is a drop to nothing, not a free pass.
    #[test]
    fn a_disappearing_surface_is_a_regression() {
        let led = Ledger {
            surfaces: Vec::new(),
            doc: json!({ "summary": { "surfaces_covered": 0 } }),
        };
        let base = json!({
            "summary": { "surfaces_covered": 0 },
            "surfaces": [ { "id": "db.postgres", "cover_new": { "strength": 3 } } ]
        });
        let fails = ratchet(&led, Some(&base), &BTreeSet::new());
        assert_eq!(fails.len(), 1);
        assert!(fails[0].contains("surface disappeared"), "{}", fails[0]);
    }

    /// `scripts/conformance.sh` is the load-bearing premise behind giving the
    /// `tests/<Sub>` suites strength 0: it cds to `tests/conformance` and globs
    /// `tests/*Test.sky` relative to THAT, so nothing outside
    /// `tests/conformance/tests/` is ever executed. If the script gains a
    /// broader glob, the ledger's claim becomes false and this test says so.
    #[test]
    fn conformance_runner_still_only_globs_the_conformance_project() {
        let src =
            std::fs::read_to_string(repo_root().join("scripts/conformance.sh")).unwrap();
        assert!(
            src.contains("PROJ=\"$ROOT/tests/conformance\""),
            "conformance.sh no longer pins PROJ to tests/conformance"
        );
        assert!(
            src.contains("for suite in tests/*Test.sky"),
            "conformance.sh no longer globs tests/*Test.sky relative to PROJ"
        );
    }

    #[test]
    fn rust_string_literals_recover_the_emitted_sky_source() {
        let rs = "let s = \"module M exposing (main)\\n\\n\\\n                   import Std.Log exposing (println)\\n\";";
        let out = rust_string_literals(rs);
        assert!(out.contains("import Std.Log exposing (println)"), "{out}");
    }

    #[test]
    fn member_parser_reads_the_membership_authority() {
        let members = parse_members(&repo_root().join("apps/manifest.toml")).unwrap();
        assert!(members.len() >= 7, "only {} members parsed", members.len());
        assert!(members
            .iter()
            .any(|m| m.get("name").map(String::as_str) == Some("ledger")
                && m.get("path").map(String::as_str) == Some("apps/ledger")));
        // Member D deliberately reuses an examples/ path; the ledger's
        // path-keyed ownership depends on that staying true.
        assert!(members
            .iter()
            .any(|m| m.get("path").map(String::as_str) == Some("examples/13-skyshop")));
    }
}
