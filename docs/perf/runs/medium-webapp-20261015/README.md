# A medium production web:app, before and after

The build-speed work started from one real application: a medium production
Sky.Spa app built with `--target web:app`. It is a private project, so its
name, its modules, its types and its fields are withheld here. Only totals and
per-stage timings are given.

## Field data before the work (v0.27.8 line)

These are the user's own timings of a full `--target web:app` build, reported
when the work started (`.claude/AUTONOMOUS_GOAL.md`). They are wall-clock
observations, not runs of a script in this repository.

| Machine | Build time |
|---|---|
| Spot VM, 8 CPUs, 32 GB | 29 min |
| the user's Mac | about 25 min |
| GitHub-hosted runner | 30 to 38 min |

## On this branch

The user ran `SKY_TIMINGS=1 sky build --target web:app` on the 16 GB Apple
silicon Mac (8 CPUs) used for the build-speed work, go1.26.1, with the
compiler built from two commits of `perf/build-speed`. The figures are the
totals from the `SKY_TIMINGS` report.

### Commit `58fec7490` (an intermediate commit)

| Stage | Time |
|---|---|
| total | 142.5 s |
| lowering, parent process | 49.4 s |
| backend leg | 44.9 s |
| frontend leg | 13.0 s |
| brotli | 27.6 s |
| peak memory | 3.5 GB |

### Commit `5d30f9127` (after the call-site harvest fix)

| Stage | Time |
|---|---|
| total | 21.8 s |
| type check, parent process | 1.99 s |
| lowering, parent process | 1.80 s |
| split | 3.33 s |
| frontend leg | 5.79 s |
| backend leg | 8.15 s |
| peak memory | 1.25 GB |

In this run `go build` and brotli were cache hits: the app's source had not
changed since the previous build, so the Go packages and the `.br` / `.gz`
files came from the caches. The 21.8 s is therefore a rebuild of unchanged
source, not a cold build. A cold build of the same commit, and a build on the
VM or the runner from the field data above, were not measured.

## Reading it

- The two runs are on a different machine from the field data, so the step
  from 25 to 38 min to these figures is not one controlled comparison.
- Between the two commits, lowering in the parent fell from 49.4 s to 1.80 s.
  Commit `5d30f9127` merges the call-site harvest fix (the `#166` close
  demanded a harvest whose answer it never read, and ran one inference per
  callee instead of per caller).
- No other numbers for this app are claimed.
