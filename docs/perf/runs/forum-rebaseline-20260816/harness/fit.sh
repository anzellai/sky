#!/usr/bin/env bash
# fit.sh — OLS of per-interaction cost on view size, with the intercept's
# standard error, because the intercept IS the result and a point estimate
# without one is not a measurement.
#
# Reads the summarise.sh TSV on stdin. Columns 2 (elements) and 6 (ms_win) by
# default; `MSCOL=7` fits the whole-run figure instead.
#
# Also prints residuals. A fixed term extrapolated from two points assumes
# the relation is linear; the residual pattern is the only thing that says
# whether it is. On this data it is NOT quite -- cost is mildly superlinear
# in element count, because per-session retained VNode trees grow with the
# view and the GC cost grows with them -- so a single straight line through
# the full range reports an intercept that is an artefact of that curvature.
# The narrow-range fits below exist to show how much the answer moves.
set -euo pipefail
MSCOL="${MSCOL:-6}"

awk -F'\t' -v mc="$MSCOL" '
NR == 1 { next }
NF < 6 { next }
{ x[++n] = $2 + 0; y[n] = $mc + 0 }
END {
  if (n < 3) { print "not enough points"; exit 1 }

  # sizes present, for the narrow-range fits. asorti sorts keys as STRINGS
  # by default, which put 1614 between 30 and 206 and silently made the
  # "3 smallest sizes" fit span the entire range -- a fit labelled as the
  # narrow-range control while being the full-range one. PROCINFO sorts
  # numerically.
  for (i = 1; i <= n; i++) seen[x[i]] = 1
  PROCINFO["sorted_in"] = "@ind_num_asc"
  ns = 0
  for (k in seen) sizes[++ns] = k + 0

  fit(1, n, "ALL POINTS")
  if (ns >= 3) fit_range(sizes[1], sizes[3], "3 SMALLEST SIZES")
  if (ns >= 4) fit_range(sizes[ns-2], sizes[ns], "3 LARGEST SIZES")
}

function fit_range(lo, hi, label,   i, xs, ys, m) {
  m = 0
  for (i = 1; i <= n; i++) if (x[i] >= lo && x[i] <= hi) { m++; xs[m] = x[i]; ys[m] = y[i] }
  fit_arrays(xs, ys, m, label " (" lo "-" hi " elements)")
}
function fit(a, b, label) { fit_arrays(x, y, n, label) }

function fit_arrays(xs, ys, m, label,   i, sx, sy, xb, yb, sxx, sxy, slope, icept, sse, sst, s, sea, seb, pred) {
  sx = sy = 0
  for (i = 1; i <= m; i++) { sx += xs[i]; sy += ys[i] }
  xb = sx / m; yb = sy / m
  sxx = sxy = 0
  for (i = 1; i <= m; i++) { sxx += (xs[i]-xb)^2; sxy += (xs[i]-xb)*(ys[i]-yb) }
  slope = sxy / sxx
  icept = yb - slope * xb
  sse = sst = 0
  for (i = 1; i <= m; i++) { pred = icept + slope*xs[i]; sse += (ys[i]-pred)^2; sst += (ys[i]-yb)^2 }
  s = (m > 2 ? sqrt(sse/(m-2)) : 0)
  sea = s * sqrt(1/m + xb*xb/sxx)
  seb = s / sqrt(sxx)

  printf "\n=== %s   (n=%d) ===\n", label, m
  printf "  cost_ms = %.4f  +  %.6f x elements\n", icept, slope
  printf "  fixed term      %.3f ms   (se %.3f, 95%%CI %.3f .. %.3f)\n",
         icept, sea, icept-1.96*sea, icept+1.96*sea
  printf "  per element     %.2f us    (se %.2f)\n", slope*1000, seb*1000
  printf "  R^2             %.5f\n", (sst > 0 ? 1 - sse/sst : 0)
  printf "  residuals (elements: observed - predicted, ms)\n"
  for (i = 1; i <= m; i++) {
    pred = icept + slope*xs[i]
    printf "    %6d  %8.3f  %8.3f  %+7.3f  (%+6.1f%%)\n",
           xs[i], ys[i], pred, ys[i]-pred, 100*(ys[i]-pred)/pred
  }
}'
