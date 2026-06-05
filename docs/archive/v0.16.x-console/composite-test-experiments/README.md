# v0.16.2 composite-test-apps — measurement experiments

This directory holds the 5 experiments the v0.16.2 RFC requires
(see `../RFC-v0.16.2-composite-test-apps.md`). Each is a bash script
that:

1. Runs a self-contained measurement against the current cabal-test
   sweep,
2. Appends a row to its `RUN_LOG.md` file (date + laptop spec +
   measurements), so we can compare across runs and across
   committers.

The numbers gate v0.16.2's release. The exit criteria in the RFC
cap cold-cache wall time at < 10 min and peak GOCACHE at < 5 GB.

## Experiments

| # | Script | Measures | Budget |
|---|---|---|---|
| 1 | `cold-cache-baseline.sh` | wall, peak GOCACHE, peak RSS, cabal exit | < 10 min wall, < 5 GB GOCACHE, < 4 GB RSS |
| 2 | `warm-cache-baseline.sh` | second-run wall time, GOCACHE hit-rate | < 2 min wall |
| 3 | `disk-pressure-experiment.sh` | sweep behaviour with disk at 70%+ | completes OR fails fast with clear error |
| 4 | `memory-pressure-experiment.sh` | sweep behaviour with concurrent memory load | mem-guard fires before OOM |
| 5 | `scale-projection.sh` | wall time + cache growth vs. N synthetic fixtures | linear, not super-linear |

## Methodology rules

- **Reproducibility:** every experiment wipes its own caches at the
  start so two runs in a row produce comparable numbers. Don't rely
  on warm state unless the experiment is specifically about warm
  state.
- **Measured, not asserted:** every script must produce a number,
  not a yes/no. Numbers go to `RUN_LOG.md` with a timestamp + hash
  of the sky commit being measured.
- **Laptop spec:** record `sysctl -n machdep.cpu.brand_string` +
  total RAM via `sysctl hw.memsize` so a 2019 i7 result and a 2024
  M3 result are distinguishable.
- **Aborts on disk floor:** if disk drops below 20 GB free during
  the experiment, abort cleanly — the result is "machine too full
  to measure" rather than a wedged sweep.
- **No `exec` in scripts:** see `scripts/cabal-test.sh` history
  (#459) — `exec`'d cleanup traps don't fire.

## When to re-run

- Before tagging v0.16.2: every script must have a green run-log
  entry on a clean main, within the RFC's exit-criteria budgets.
- After any change to `scripts/cabal-test.sh`, `copyRuntime`, the
  composite apps, or the cabal test suite shape.
- Quarterly thereafter (in `docs/v0.16.x-console/composite-test-
  experiments/SCALE.md`) to track drift as the test surface grows.
