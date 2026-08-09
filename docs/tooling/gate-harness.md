# The gate harness (`xtask harness`)

The harness runs the repo's CI gates and decides, for each one, whether it
**passed**, **failed**, **did not run**, or **is unproven** — and refuses to
report success in any case where it cannot tell.

Design authority: [`docs/ci-test-architecture-v2.md`](../ci-test-architecture-v2.md) §7.
This page is the operator's view.

```bash
cargo run --release -p xtask -- harness --list          # the registry
cargo run --release -p xtask -- harness                 # run the T1 gates
cargo run --release -p xtask -- harness --only reject   # run one
cargo run --release -p xtask -- harness --verify-falsifiers
```

## Why it exists

The v0.19.13 release burned nine consecutive preflight attempts, none for a
product reason, and the audit that followed found **23 gate defects**. The
shape of almost all of them was the same: *a gate that could not fail*.

- `xtask` exited **0** on an unknown subcommand — a typo'd gate name in CI was a
  permanently green no-op.
- `verify-all-web.sh` used `if node … | tail -8; then`, testing `tail`'s exit
  status. The console e2e gate could not fail.
- `grep -qE "0 fail"` matched inside `"10 fail"`, so a run ending
  `0 pass / 12 fail` passed.
- `SKIP` counted as `pass`; nightly reported `29 passed, 0 failed` with three
  examples never built.
- `doc-examples.sh` with `total=0` printed `0/0 … GATE: PASS`.
- `ty/tests/reject.rs` asserts `>= 13` against an actual 63 — deleting 50
  corpus files keeps it green.

Every rule below is a direct answer to one of those.

## States

| State | Meaning | Suite effect |
|---|---|---|
| `PASS` | ran, every assertion held, `assertions > 0`, **and the count matched exactly** | PASS |
| `FAIL` | an assertion broke, the budget was exceeded, the gate was vacuous, or the body could not be spawned | FAIL (exit 1) |
| `NOT RUN` | registered and selected, but no usable verdict | **UNKNOWN (exit 3)** |
| `UNPROVEN` | passed, but its falsifying mutation is unproven or stale | **UNKNOWN (exit 3)** |
| `NOT APPLICABLE` | outside the selected tier/platform, or deselected by `--only` | none |

Three properties follow, and they are the point:

- **A suite containing `NOT RUN` or `UNPROVEN` can never render `PASS`.** A run
  that cannot say whether a gate passed has not passed.
- **`--only` produces `NOT APPLICABLE`, never `NOT RUN`.** Deliberate selection
  is not an unknown. Conflating them makes local runs emit `UNKNOWN` constantly,
  which trains people to ignore the one state that means "we do not know".
- **Rows come from the registry, not from the run.** A gate cannot disappear by
  not executing. This kills "SKIP counted as pass" at the root.

## Every gate declares a falsifier, and the compiler enforces it

```rust
mutations: Mutations::new(&[Mutation {
    id: "reject.neutralise-axis",
    description: "neutralise the axis under test so the file type-checks",
    kind: MutationKind::ReplaceOnce { path: "…", from: "add 1 2 3", to: "add 1 2" },
}]),
```

`Mutations::new` is a `const fn` whose `assert!` is const-evaluated, so an empty
set fails the **build**:

```
error[E0080]: evaluation panicked: every gate must declare at least one
              falsifying mutation (a gate that cannot fail is worse than no gate)
```

`--verify-falsifiers` then proves the mutation actually bites:

1. run the gate — the baseline **must** be green, or the result is
   `INCONCLUSIVE` (a red baseline says nothing about what the mutation did);
2. apply the mutation — exact-once replacement; a pattern that is missing or
   ambiguous is **refused**, because a mutation that silently did nothing would
   report `VACUOUS` and be misread as a gate defect;
3. run again under the same `killpg`-backed budget — red ⇒ `PROVEN`, green ⇒
   `VACUOUS`;
4. revert, guaranteed — the patch reverts in `Drop`, including on panic.

Proofs are recorded in `docs/coverage/falsifier-proofs.json`. A gate whose proof
is missing or older than the window renders `UNPROVEN` under `--require-proofs`.

## The canary

One gate — `canary` — is deliberately vacuous and paired with a **no-op** patch.
A correct runner must report it `VACUOUS`. Reporting `PROVEN` means the harness
applied its patch somewhere the gate never read, or is not reading the verdict
from the run it just performed — in which case every other `PROVEN` is worthless.

It is the only place a *passing* gate is the success signal, and the only
construction that catches a verifier whose every answer is "green".

## Budgets are enforced by `killpg`, not by hope

Gate bodies run in a **child process** placed in its own process group
(`process_group(0)`). On budget expiry the harness sends `SIGTERM` then
`SIGKILL` to the **group**.

This is not stylistic. Gates spawn `go build`s, servers, PTYs and browsers:

- A thread's children are not reachable as a group, so "kill the process group"
  is unimplementable from a thread — a timeout would leak a process holding a
  port into every later gate.
- An orphaned worker can write a result *after* its gate was recorded FAIL,
  corrupting a **later** gate's verdict — a wrong *green*, attributed to the
  wrong gate. Results are therefore **generation-stamped**, and a result whose
  generation does not match the gate being awaited is discarded.

Measured negative control: replacing the `killpg` with a plain `kill` of the
direct child leaves the body's `sleep 600` grandchild **alive**.

Timeouts live in the harness and never in GNU `timeout`, which is absent on
every macOS runner — the exact hole that left `conformance.sh` unbounded there.

## Wrapped verifiers emit JSON; the gate reads the file

No verifier is rewritten. Each gains a `--json <path>` mode, and the gate asserts
on the **file** — never on scraped stdout, which is where the unanchored-`grep`
class comes from.

- `scripts/conformance.sh --json <path>` writes a manifest of
  `(suite, exit_code, per-suite Sky.Test report)` and deliberately does **not**
  aggregate. It emits only values it controls and never parses JSON; a shell
  that parses its own output is how `grep -qE "0 fail"` came to match `"10 fail"`.
  The `conformance` gate aggregates in a real JSON parser.
- `scripts/verify-cli.sh --json <path> --rebuild` writes one record per entry.
  **`--rebuild` is mandatory for gate use**: without it the script only builds an
  example whose binary is missing, so it certifies whatever artefact an earlier
  run left behind, and no source mutation can falsify it.

Per-case data comes from the `Sky.Test` JSON reporter — see
[`testing.md`](testing.md#machine-readable-output-sky_test_json).

## Assertion counts are exact

Every gate pins an **exact** expected count, never a `>=`. A corpus that shrinks
is a failure with an actionable message, not a quieter green.

Pinning exact counts found a real discrepancy on its first run: v2 §5.4 records
conformance at **772** cases, from a static count of `Test.test` leaves. The
harness measured **770**. Both are correct about different things —
`StoreConformanceTest.sky:75` and `StoreCrudConformanceTest.sky:68` each declare
a `Test.test "setup"` leaf inside the `Err` arm of `case setup () of`, which
materialises only when the DB setup fails. The gate pins the number that *runs*.

## Concurrency

Gates run **sequentially**. Deliberate: the failure that motivated this whole
mandate is a parallel sweep spawning thousands of `xcrun` processes and
exhausting the per-uid process table (measured 2,167 of 2,472), which kills
mem-guard's ability to fork. Parallelism and the persistent semaphore belong
with the CI-topology phase, when there are measured runner numbers to budget
against.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | PASS |
| 1 | FAIL |
| 2 | usage error (including an unknown gate name — never an empty selection that passes) |
| 3 | UNKNOWN — a `NOT RUN` or `UNPROVEN` gate |
