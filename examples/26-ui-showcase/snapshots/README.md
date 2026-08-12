# UI showcase snapshots

Baseline PNGs for `scripts/verify-ui-showcase.{sh,mjs}`. Recorded at:

- **Desktop** viewport 1280×720, deviceScaleFactor 1
- **Mobile**  viewport 375×667, deviceScaleFactor 1
- Reduced motion, light colour scheme
- Animations + transitions disabled via `addInitScript`
- Font stack forced to `monospace` for deterministic text metrics

## One directory per platform

```
snapshots/
  darwin/   recorded on macOS   (the release-preflight platform)
  linux/    recorded on ubuntu  (the nightly `web-runtime` platform)
```

The runner picks its directory from `process.platform`
(`SKY_UI_SNAPSHOT_PLATFORM` overrides). This is not tidiness — it is what
makes the gate runnable on more than one machine.

Chromium composites text with the HOST's font stack. The runner forces
`font-family: monospace` for determinism, but "monospace" is Menlo on macOS
and DejaVu Sans Mono on a Linux runner: different advance widths, different
hinting. Identical DOM, 3-8 % of pixels different — 3-8x the 1 % budget. A
single shared directory therefore cannot be compared on two platforms, which
is why this gate spent its life release-only, on one developer's Mac, and
failed nine snapshots on its first nightly Linux run with no product change
behind any of them.

A platform with no recorded baselines **fails** — every snapshot reports
"no baseline … nothing was compared". Missing baselines are never
self-blessed, on any platform.

## Updating baselines

Snapshots change legitimately when Sky's renderer, `Std.Ui`, or the
showcase source itself is modified. **Never** commit a blind update
without a human eyeball check — the runner is the regression net for
the Cycle 5 renderer churn.

```bash
# 1. Run the runner against your change. Look at the failing snapshots.
TMPDIR=/tmp bash scripts/verify-ui-showcase.sh

# 2. Open `.skycache/ui-showcase-diffs/*.current.png` in an image
#    viewer alongside `examples/26-ui-showcase/snapshots/<platform>/*.png`.
#    Confirm every pixel difference is intentional.

# 3. ONLY after the human eyeball pass, re-record:
TMPDIR=/tmp bash scripts/verify-ui-showcase.sh --update-baseline

# 4. `git add` the updated PNGs. The PR template includes a sign-off
#    checkbox for this.
```

A re-record only ever touches the platform you ran it on. To refresh the
OTHER platform's set, run the `ui-snapshot-baselines` workflow
(`.github/workflows/ui-snapshot-baselines.yml`, manual dispatch): it records
on a GitHub runner and uploads the PNGs as an artifact for review. It
deliberately does not commit them — the eyeball pass in step 2 is not
optional just because a runner did the rendering.

`fullpage-desktop.png` / `fullpage-mobile.png` are eyeball references for
that review; nothing compares them. They are written into this directory
only during a `--update-baseline` run, and into `.skycache/ui-showcase-diffs/`
otherwise — an ordinary verification must not rewrite a tracked baseline.

## Tolerance

`±3` px pixel tolerance + 1 % per-pixel colour delta + 1 % total-
pixel budget — see `scripts/verify-ui-showcase.mjs` § constants. The
tolerance absorbs same-platform antialiasing jitter; it is not, and was
never, wide enough to absorb a different font stack — hence the per-platform
directories above.
