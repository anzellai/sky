# Incremental execution final validation continuation

Measured compiler source checkpoint: `dadf2a0df3305380f8975df00ddbba13df4c1e0c`.
These receipts cover public fixtures only and contain no private application data.

- `core-target-builds.json`: one identical public Std.App fixture builds for web,
  web:app, terminal:cli and terminal:tui; actual CLI execution with EOF succeeds.
- `desktop-target-builds.json`: the same fixture builds for desktop and
  desktop:mac with the matched locked Nix toolchain. This is compilation only.
- `std-app-execution-tests.json`: six individually selected existing tests pass
  with exactly one passed and zero ignored per invocation, including native HTTP
  startup/configuration and desktop failure-to-listen handling.

The canonical compiler and generated artifacts are hash-bound in the build
receipts. Cargo integration tests build their own current compiler; they are
not claimed byte-identical to the canonical executable. The unchanged external
memory guard and original per-test deadlines remain in effect. Initial isolated
link attempts failed after dropping Nix SDK configuration; retaining the clean
locked Nix context resolved them without a compiler-source change.

The small public fixture contains no indexedMap region. These results therefore
do not prove view-cache activation, private application parity, physical-device
IME behavior, terminal PTY screen behavior or successful desktop GUI operation.
Real private-app browser validation remains a separate required gate.
