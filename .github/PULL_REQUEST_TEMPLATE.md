## Summary

<!-- 1-3 bullets describing the change -->

## Test plan

- [ ] `scripts/gates-for-change.sh` green (it runs the narrowest gates for the paths this PR changes; `--dry-run` prints the plan)
- [ ] `(cd rust && cargo test -p <crate>)` green for every crate this PR touches
- [ ] Every file touched is formatted (`cargo fmt`, `gofmt -w`, `sky fmt`)
- [ ] If Std.Ui changed: `scripts/verify-ui-showcase.sh` green
- [ ] The release workflow's full suite runs in CI before merge to `main`

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
