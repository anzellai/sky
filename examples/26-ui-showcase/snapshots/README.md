# UI showcase snapshots

Baseline PNGs for `scripts/verify-ui-showcase.{sh,mjs}`. Recorded at:

- **Desktop** viewport 1280×720, deviceScaleFactor 1
- **Mobile**  viewport 375×667, deviceScaleFactor 1
- Reduced motion, light colour scheme
- Animations + transitions disabled via `addInitScript`
- Font stack forced to `monospace` for deterministic text metrics

## Updating baselines

Snapshots change legitimately when Sky's renderer, `Std.Ui`, or the
showcase source itself is modified. **Never** commit a blind update
without a human eyeball check — the runner is the regression net for
the Cycle 5 renderer churn.

```bash
# 1. Run the runner against your change. Look at the failing snapshots.
TMPDIR=/tmp bash scripts/verify-ui-showcase.sh

# 2. Open `.skycache/ui-showcase-diffs/*.current.png` in an image
#    viewer alongside `examples/26-ui-showcase/snapshots/*.png`.
#    Confirm every pixel difference is intentional.

# 3. ONLY after the human eyeball pass, re-record:
TMPDIR=/tmp bash scripts/verify-ui-showcase.sh --update-baseline

# 4. `git add` the updated PNGs. The PR template includes a sign-off
#    checkbox for this.
```

## Tolerance

`±3` px pixel tolerance + 1 % per-pixel colour delta + 1 % total-
pixel budget — see `scripts/verify-ui-showcase.mjs` § constants.
This matches CLAUDE.md §"Critical constraints" for cross-platform
Chromium renders (macOS vs Linux text differs by 1-2 px). On CI we
only run the runner on macOS today; Linux Chromium's different font
stack would false-positive every baseline.
