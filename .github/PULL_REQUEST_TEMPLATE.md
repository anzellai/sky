## Summary

<!-- 1-3 bullets describing the change -->

## Test plan

- [ ] `cabal test` green
- [ ] `TMPDIR=/tmp bash scripts/example-sweep.sh` — all examples build
- [ ] `TMPDIR=/tmp bash scripts/verify-ui-showcase.sh` — Std.Ui regression gates green

## Std.Ui snapshot churn checklist

If this PR modifies `sky-stdlib/Std/Ui*.sky`, `examples/26-ui-showcase/`,
or any renderer / layout code, the visual snapshots in
`examples/26-ui-showcase/snapshots/` may legitimately change.

**Snapshot updates require manual eyeball review** — never approve a
blind baseline rewrite. The runner is the regression net for the
Cycle 5 renderer churn (mediaQuery, pseudo-classes, transitions,
aspectRatio); a sloppy baseline reset would defeat that.

- [ ] If snapshots changed, I opened each `.png` and confirmed the
      new render is intentional and correct
- [ ] The diff between baseline and current was reviewed pixel-by-
      pixel for the affected sections (open `.skycache/ui-showcase-
      diffs/*.current.png` against `examples/26-ui-showcase/snapshots/*.png`)
- [ ] `scripts/verify-ui-showcase.sh --update-baseline` was run only
      AFTER the diff was reviewed, not before
