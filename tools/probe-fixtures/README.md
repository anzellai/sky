# probe-fixtures

Micro-fixtures driving `tools/probe-sweep.sh` — the safety net for
`feat/v0.17-fully-typed-codegen`.

Each subdirectory is a standalone Sky project. The probe sweep
runs `sky build` against the current compiler, captures
`sky-out/main.go`, and asserts string-presence invariants from
`expectations.txt`.

## Naming convention

`probe-<cause>-<short-slug>/` — e.g. `probe-A-sigma-projection`,
`probe-C-stdui-callback-typed`. Causes map to the v5 plan
(docs/v0.17-fully-typed-codegen-v5-plan.md §"Root causes"):

| Cause | Surface |
|---|---|
| A | σ-projection name-space drift |
| B | Call-site instance keying loss |
| C | Std.Ui kernel-sig polymorphism erased |
| D | Record update over `any`-typed base |
| E | Poly ADT type-arg propagation |
| F | Cmd/Sub non-generic runtime |
| G | FFI opaque collapse to "Value" |
| H | Tuple aliases hide structure |
| I | TEA loop type-erasure |
| J | Basics maintenance drift |

Plus baselines (`probe-baseline-*`) that MUST always pass —
they catch generic regressions.

## Fixture shape

```
probe-<cause>-<slug>/
├── README.md            # one paragraph: what this probes, expected GREEN behaviour
├── sky.toml             # minimal: name + entry + [source]
├── src/Main.sky         # the Sky source under test
└── expectations.txt     # MUST_CONTAIN / MUST_NOT_CONTAIN / etc.
```

## Status

| Phase | Cause | Fixtures |
|---|---|---|
| Baseline | — | probe-baseline-counter |
| α (C1-C3) | foundation | (no new fixtures — differential) |
| β (C4-C7) | D, E, H, J | probe-D-record-update-typed, probe-E-poly-adt, probe-H-tuple-typed |
| γ (C8-C12) | A, B | probe-A-sigma-projection, probe-B-csi-keying |
| δ (C13-C14) | C | probe-C-stdui-callback-typed |
| ε (C15-C16) | F, I | probe-F-cmd-generic, probe-I-tea-msg-typed |
| ζ (C17-C20) | G | probe-G-ffi-opaque-distinct |

Target: 50 fixtures by end of Phase η. Current count is the
starter set — each phase commit adds the fixtures that lock its
root-cause close.

## Running

```bash
tools/probe-sweep.sh                # all fixtures
tools/probe-sweep.sh --verbose      # report each fixture
tools/probe-sweep.sh --only probe-baseline-counter
```

Exit 0 on full pass; non-zero with failure summary on any failure.
