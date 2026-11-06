# Incremental execution final validation continuation

Measured compiler source checkpoint: `dadf2a0df3305380f8975df00ddbba13df4c1e0c`.
Build and execution receipts cover public fixtures. The separate private-app
discovery receipt contains numeric observations only, with no application values
or schema names.

- `core-target-builds.json`: one identical public Std.App fixture builds for web,
  web:app, terminal:cli and terminal:tui; actual CLI execution with EOF succeeds.
- `desktop-target-builds.json`: the same fixture builds for desktop and
  desktop:mac with the matched locked Nix toolchain. This is compilation only.
- `tui-pty-execution.json`: the unchanged complete terminal gate passes, including
  real PTY screen assertions and the terminal CLI effect/guard/exit assertions.
- `std-app-execution-tests.json`: six individually selected existing tests pass
  with exactly one passed and zero ignored per invocation, including native HTTP
  startup/configuration and desktop failure-to-listen handling.

- `source-workload-discovery.json`: the source-derived private setup executes,
  but its retained model stays constant despite verified database growth. This
  does not reproduce the large-model workload or pass its acceptance gate.
- `changed-dict-red.json`: an executed temporary regression confirms whole-Dict
  re-encoding after one value changes. Correctness checks pass before the
  intended performance assertion fails; no implementation fix is claimed.

The canonical compiler and generated artifacts are hash-bound in the build
receipts. Cargo integration tests build their own current compiler; they are
not claimed byte-identical to the canonical executable. The unchanged external
memory guard and original per-test deadlines remain in effect. Initial isolated
link attempts failed after dropping Nix SDK configuration; retaining the clean
locked Nix context resolved them without a compiler-source change.

The small public fixture contains no indexedMap region. These results therefore
do not prove view-cache activation, private application parity, physical-device
IME behavior or successful desktop GUI operation.
Real private-app browser validation remains a separate required gate.

`dict-summary-validation-attempts.json` records guard-stopped prototype
compilations. No prototype test executed locally. Hosted run 42136087088 subsequently failed
the same cache-retention invariant in three jobs; 36 of 40 jobs passed.
`dict-scalar-ci-correction.json` records that failure and the reviewed source
correction removing retained model pointers. The corrected checkpoint still
requires hosted full-runtime and race validation.
